package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

type updateRelease struct {
	Version string `json:"version"`
	Notes   string `json:"notes"`
	URL     string `json:"url"`
	AssetID int64  `json:"assetId"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
}
type githubRelease struct {
	Tag        string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Body       string `json:"body"`
	Assets     []struct {
		ID     int64  `json:"id"`
		Name   string `json:"name"`
		State  string `json:"state"`
		Size   int64  `json:"size"`
		Digest string `json:"digest"`
	} `json:"assets"`
}
type updateClient struct {
	http *http.Client
	api  string
}

func newUpdateClient() *updateClient {
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 20 * time.Second, IdleConnTimeout: 30 * time.Second}
	return &updateClient{api: "https://api.github.com/repos/" + updateRepo, http: &http.Client{Transport: transport, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) > 3 || !allowedUpdateURL(r.URL) {
			return fmt.Errorf("запрещён redirect сервера обновлений")
		}
		return nil
	}}}
}
func allowedUpdateURL(u *url.URL) bool {
	if u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" {
		return false
	}
	switch u.Hostname() {
	case "api.github.com":
		return u.Path == "/repos/"+updateRepo+"/releases" || strings.HasPrefix(u.Path, "/repos/"+updateRepo+"/releases/")
	case "github.com":
		return strings.HasPrefix(u.Path, "/"+updateRepo+"/releases/download/")
	case "release-assets.githubusercontent.com", "objects.githubusercontent.com":
		return true
	}
	return false
}
func (c *updateClient) request(ctx context.Context, path, accept string) (*http.Response, error) {
	r, err := http.NewRequestWithContext(ctx, "GET", c.api+path, nil)
	if err != nil {
		return nil, err
	}
	r.Header.Set("Accept", accept)
	r.Header.Set("User-Agent", "LocalBroadcast-Updater/1")
	r.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := c.http.Do(r)
	if err != nil {
		return nil, fmt.Errorf("нет связи с сервером обновлений: %w", ctxOrNetworkError(ctx))
	}
	if response.StatusCode != 200 {
		response.Body.Close()
		return nil, fmt.Errorf("сервер обновлений: HTTP %d; повторите позже (лимит GitHub или ошибка сети)", response.StatusCode)
	}
	return response, nil
}
func ctxOrNetworkError(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("соединение не установлено или прервано")
}
func updateAssetName(v string) string { return "LocalBroadcast-" + v + "-Update-v1-win-x64.zip" }
func (c *updateClient) discover(ctx context.Context, current, channel string) (*updateRelease, error) {
	if channel != "stable" && channel != "beta" {
		return nil, fmt.Errorf("неизвестный канал обновлений")
	}
	installed, err := parseReleaseVersion(current)
	if err != nil {
		return nil, err
	}
	var best *updateRelease
	var bestVersion releaseVersion
	for page := 1; page <= 10; page++ {
		response, err := c.request(ctx, "/releases?per_page=100&page="+strconv.Itoa(page), "application/vnd.github+json")
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
		response.Body.Close()
		if err != nil {
			return nil, err
		}
		if len(b) > 2<<20 {
			return nil, fmt.Errorf("слишком большой ответ GitHub")
		}
		var releases []githubRelease
		if err = json.Unmarshal(b, &releases); err != nil {
			return nil, fmt.Errorf("неверные metadata GitHub: %w", err)
		}
		for _, r := range releases {
			v, err := parseReleaseVersion(r.Tag)
			if err != nil || r.Draft || compareReleaseVersion(v, installed) <= 0 {
				continue
			}
			if channel == "stable" && (r.Prerelease || len(v.pre) > 0) {
				continue
			}
			version := strings.TrimPrefix(r.Tag, "v")
			matches := 0
			var candidate *updateRelease
			for _, a := range r.Assets {
				if a.Name != updateAssetName(version) {
					continue
				}
				matches++
				digest := strings.TrimPrefix(a.Digest, "sha256:")
				if a.State != "uploaded" || a.ID <= 0 || a.Size <= 0 || a.Size > updateMaxZip || !strings.HasPrefix(a.Digest, "sha256:") || !digestPattern.MatchString(digest) {
					return nil, fmt.Errorf("пакет %s не имеет проверяемого размера/SHA-256; установка недоступна", version)
				}
				notes := r.Body
				if len(notes) > 16<<10 {
					notes = notes[:16<<10]
				}
				candidate = &updateRelease{Version: version, Notes: notes, URL: "https://github.com/" + updateRepo + "/releases/tag/v" + version, AssetID: a.ID, Size: a.Size, SHA256: digest}
			}
			if matches > 1 {
				return nil, fmt.Errorf("неоднозначный пакет релиза %s", version)
			}
			if candidate != nil && (best == nil || compareReleaseVersion(v, bestVersion) > 0) {
				best = candidate
				bestVersion = v
			}
		}
		if len(releases) < 100 {
			return best, nil
		}
	}
	return nil, fmt.Errorf("превышен лимит 1000 релизов; результат проверки неполон")
}

func (c *updateClient) download(ctx context.Context, r updateRelease, destination string, progress func(int64)) error {
	if r.Size <= 0 || r.Size > updateMaxZip || r.AssetID <= 0 || !digestPattern.MatchString(r.SHA256) {
		return fmt.Errorf("неверные metadata пакета")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	lastByte := atomic.Int64{}
	lastByte.Store(time.Now().UnixNano())
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if time.Since(time.Unix(0, lastByte.Load())) > 30*time.Second {
					cancel()
					return
				}
			}
		}
	}()
	response, err := c.request(ctx, "/releases/assets/"+strconv.FormatInt(r.AssetID, 10), "application/octet-stream")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.ContentLength >= 0 && response.ContentLength != r.Size {
		return fmt.Errorf("размер ответа не совпадает с metadata")
	}
	f, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(destination)
		}
	}()
	h := sha256.New()
	buf := make([]byte, 64<<10)
	total := int64(0)
	for {
		n, readErr := response.Body.Read(buf)
		if n > 0 {
			lastByte.Store(time.Now().UnixNano())
			total += int64(n)
			if total > r.Size {
				return fmt.Errorf("пакет превышает заявленный размер")
			}
			if _, err = io.MultiWriter(f, h).Write(buf[:n]); err != nil {
				return err
			}
			if progress != nil {
				progress(total)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("загрузка прервана; повторите: %w", ctxOrNetworkError(ctx))
		}
		if err = ctx.Err(); err != nil {
			return err
		}
	}
	if total != r.Size || hex.EncodeToString(h.Sum(nil)) != r.SHA256 {
		return fmt.Errorf("SHA-256/размер не совпадает; пакет не установлен")
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}
