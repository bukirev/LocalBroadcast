package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testUpdateDigest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func TestUpdateSemVer(t *testing.T) {
	ordered := []string{"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.10", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.2.0", "1.10.0"}
	for i := 1; i < len(ordered); i++ {
		a, e := parseReleaseVersion(ordered[i-1])
		if e != nil {
			t.Fatal(e)
		}
		b, e := parseReleaseVersion(ordered[i])
		if e != nil || compareReleaseVersion(a, b) >= 0 {
			t.Fatalf("%s < %s", ordered[i-1], ordered[i])
		}
	}
	a, _ := parseReleaseVersion("v1.0.0+build.10")
	b, _ := parseReleaseVersion("1.0.0+build.11")
	if compareReleaseVersion(a, b) != 0 {
		t.Fatal("build metadata affects precedence")
	}
	for _, s := range []string{"01.0.0", "1.0.0-beta.01", "1.0", "-1.0.0", "1.0.0+", "1.0.0-a..b", "999999999999999999999999999999.0.0"} {
		if _, err := parseReleaseVersion(s); err == nil {
			t.Fatalf("accepted %s", s)
		}
	}
}
func TestUpdateURLPolicy(t *testing.T) {
	for _, s := range []string{"https://api.github.com/repos/" + updateRepo + "/releases/assets/1", "https://github.com/" + updateRepo + "/releases/download/v1/a.zip", "https://release-assets.githubusercontent.com/a"} {
		u, _ := url.Parse(s)
		if !allowedUpdateURL(u) {
			t.Fatal(s)
		}
	}
	for _, s := range []string{"http://github.com/" + updateRepo + "/releases/download/v1/a.zip", "https://evil.example/a", "https://api.github.com/repos/evil/evil/releases", "https://github.com/evil/evil/releases/download/v1/a", "https://github.com:443/" + updateRepo + "/releases/download/a", "https://a@release-assets.githubusercontent.com/a"} {
		u, _ := url.Parse(s)
		if allowedUpdateURL(u) {
			t.Fatal(s)
		}
	}
	c := newUpdateClient()
	u, _ := url.Parse("https://evil.example/token")
	if c.http.CheckRedirect(&http.Request{URL: u}, nil) == nil {
		t.Fatal("redirect accepted")
	}
}
func testGitHubUpdate(v string, prerelease bool) map[string]any {
	return map[string]any{"tag_name": "v" + v, "prerelease": prerelease, "body": "notes <script>", "assets": []any{map[string]any{"name": updateAssetName(v), "id": 1, "state": "uploaded", "size": 4, "digest": "sha256:" + strings.Repeat("a", 64)}}}
}
func TestUpdateDiscoveryChannelsPaginationAndErrors(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "1" {
			items := make([]any, 100)
			for i := range items {
				items[i] = map[string]any{"draft": true, "tag_name": "v9.0.0"}
			}
			json.NewEncoder(w).Encode(items)
			return
		}
		json.NewEncoder(w).Encode([]any{testGitHubUpdate("1.3.0-beta.10", true), testGitHubUpdate("1.2.0", false), testGitHubUpdate("1.3.0-beta.2", true)})
	}))
	defer s.Close()
	c := &updateClient{http: s.Client(), api: s.URL}
	for channel, want := range map[string]string{"stable": "1.2.0", "beta": "1.3.0-beta.10"} {
		r, err := c.discover(context.Background(), "1.1.0", channel)
		if err != nil || r == nil || r.Version != want {
			t.Fatalf("%s: %+v %v", channel, r, err)
		}
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "rate limit", 429) }))
	defer bad.Close()
	c.api = bad.URL
	if _, err := c.discover(context.Background(), "1.0.0", "beta"); err == nil {
		t.Fatal("network error shown as no update")
	}
	invalid := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		x := testGitHubUpdate("2.0.0", false)
		x["assets"].([]any)[0].(map[string]any)["digest"] = nil
		json.NewEncoder(w).Encode([]any{x})
	}))
	defer invalid.Close()
	c.api = invalid.URL
	if _, err := c.discover(context.Background(), "1.0.0", "beta"); err == nil {
		t.Fatal("missing digest accepted")
	}
}
func TestUpdateDownloadIntegrityAndCancellation(t *testing.T) {
	for _, mode := range []string{"ok", "short", "long", "hash", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.(http.Flusher).Flush()
				switch mode {
				case "short":
					w.Write([]byte("abc"))
				case "long":
					w.Write([]byte("abcde"))
				case "cancel":
					w.Write([]byte("a"))
					w.(http.Flusher).Flush()
					<-r.Context().Done()
				default:
					w.Write([]byte("abcd"))
				}
			}))
			defer s.Close()
			c := &updateClient{http: s.Client(), api: s.URL}
			r := updateRelease{AssetID: 1, Size: 4, SHA256: testUpdateDigest([]byte("abcd"))}
			if mode == "hash" {
				r.SHA256 = strings.Repeat("0", 64)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			path := filepath.Join(t.TempDir(), "update.zip")
			err := c.download(ctx, r, path, nil)
			if mode == "ok" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				if err == nil {
					t.Fatal("invalid download accepted")
				}
				if _, err = os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("partial package retained")
				}
			}
		})
	}
}
func testPE() []byte {
	b := make([]byte, 512)
	copy(b, "MZ")
	binary.LittleEndian.PutUint32(b[0x3c:], 128)
	copy(b[128:], "PE\x00\x00")
	binary.LittleEndian.PutUint16(b[132:], 0x8664)
	binary.LittleEndian.PutUint16(b[148:], 240)
	binary.LittleEndian.PutUint16(b[150:], 2)
	binary.LittleEndian.PutUint16(b[152:], 0x20b)
	binary.LittleEndian.PutUint32(b[260:], 16)
	return b
}
func testUpdateZIP(t *testing.T, mutate func(*updateManifest, map[string][]byte), extra string) (string, string, int64) {
	t.Helper()
	files := map[string][]byte{"LocalBroadcast.exe": testPE(), "RELEASE_NOTES.md": []byte("notes")}
	m := updateManifest{Format: 1, Product: "LocalBroadcast", Version: "1.0.0", Runtime: "win-x64", UpdaterProtocol: 1, DataSchema: 2, DependencySet: updateDependencySet, Files: map[string]updateFile{}}
	for name, b := range files {
		m.Files[name] = updateFile{int64(len(b)), testUpdateDigest(b)}
	}
	if mutate != nil {
		mutate(&m, files)
	}
	files["update-manifest.json"], _ = json.Marshal(m)
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for _, name := range []string{"LocalBroadcast.exe", "RELEASE_NOTES.md", "update-manifest.json"} {
		w, _ := z.Create(name)
		w.Write(files[name])
	}
	if extra != "" {
		h := &zip.FileHeader{Name: extra, Method: zip.Store}
		if extra == "symlink" {
			h.SetMode(os.ModeSymlink | 0777)
		}
		w, _ := z.CreateHeader(h)
		w.Write([]byte("x"))
	}
	z.Close()
	path := filepath.Join(t.TempDir(), "update.zip")
	os.WriteFile(path, b.Bytes(), 0600)
	return path, testUpdateDigest(b.Bytes()), int64(b.Len())
}
func TestUpdatePackageValidation(t *testing.T) {
	p, h, n := testUpdateZIP(t, nil, "")
	if _, _, err := validateUpdatePackage(p, "1.0.0", h, n); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../escape.exe", "config.json", "CON", "x:y", "Localbroadcast.exe", "LocalBroadcast.exe", "symlink", "sub/x"} {
		t.Run(name, func(t *testing.T) {
			p, h, n := testUpdateZIP(t, nil, name)
			if _, _, err := validateUpdatePackage(p, "1.0.0", h, n); err == nil {
				t.Fatal("accepted extra/unsafe entry")
			}
		})
	}
	for _, kind := range []string{"product", "version", "runtime", "schema", "protocol", "dependency", "hash", "exe"} {
		t.Run(kind, func(t *testing.T) {
			p, h, n := testUpdateZIP(t, func(m *updateManifest, f map[string][]byte) {
				switch kind {
				case "product":
					m.Product = "Other"
				case "version":
					m.Version = "9.0.0"
				case "runtime":
					m.Runtime = "win-arm64"
				case "schema":
					m.DataSchema = 3
				case "protocol":
					m.UpdaterProtocol = 2
				case "dependency":
					m.DependencySet = "future"
				case "hash":
					m.Files["LocalBroadcast.exe"] = updateFile{512, strings.Repeat("a", 64)}
				case "exe":
					f["LocalBroadcast.exe"][132] = 0
					m.Files["LocalBroadcast.exe"] = updateFile{512, testUpdateDigest(f["LocalBroadcast.exe"])}
				}
			}, "")
			if _, _, err := validateUpdatePackage(p, "1.0.0", h, n); err == nil {
				t.Fatal("incompatible package accepted")
			}
		})
	}
	if _, _, err := validateUpdatePackage(p, "1.0.0", strings.Repeat("0", 64), n); err == nil {
		t.Fatal("ZIP hash mismatch accepted")
	}
}
func testUpdateJournal(t *testing.T) updateJournal {
	t.Helper()
	root := t.TempDir()
	old := []byte("old exe")
	os.WriteFile(updateTarget(root), old, 0700)
	os.WriteFile(filepath.Join(root, "config.json"), []byte("keep"), 0600)
	return updateJournal{Protocol: 1, ID: strings.Repeat("1", 32), Root: root, OldHash: testUpdateDigest(old), NewHash: testUpdateDigest([]byte("new exe")), Version: "1.0.0"}
}
func TestUpdateRecoveryEveryCrashWindow(t *testing.T) {
	for _, phase := range []string{"journal", "staged", "renamed-old", "backed-up", "renamed-new", "installed"} {
		t.Run(phase, func(t *testing.T) {
			j := testUpdateJournal(t)
			err := installUpdateFiles(&j, []byte("new exe"), func(p string) error {
				if p == phase {
					return fmt.Errorf("crash at %s", p)
				}
				return nil
			})
			if err == nil {
				t.Fatal("fault did not fire")
			}
			persisted, err := loadUpdateJournal(j.Root)
			if err != nil {
				t.Fatal(err)
			}
			for repeat := 0; repeat < 2; repeat++ {
				if err = rollbackUpdate(&persisted); err != nil {
					t.Fatal(err)
				}
			}
			if err = checkUpdateHash(updateTarget(j.Root), j.OldHash); err != nil {
				t.Fatal(err)
			}
			b, _ := os.ReadFile(filepath.Join(j.Root, "config.json"))
			if string(b) != "keep" {
				t.Fatal("user data changed")
			}
		})
	}
}
func TestUpdateRollbackRefusesForeignAndCommitted(t *testing.T) {
	j := testUpdateJournal(t)
	if err := installUpdateFiles(&j, []byte("new exe"), nil); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(updateTarget(j.Root), []byte("foreign"), 0700)
	if err := rollbackUpdate(&j); err == nil {
		t.Fatal("foreign file replaced")
	}
	j.Phase = "committed"
	if err := rollbackUpdate(&j); err == nil {
		t.Fatal("committed transaction rolled back")
	}
}
func TestUpdateReadinessRejectsWrongNonceVersionPID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ready.json")
	atomicUpdateJSON(path, updateReceipt{ID: "wrong", Version: "1.0.0", PID: 1})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := updateWaitReceipt(ctx, path, "expected", "1.0.0", 1); err == nil {
		t.Fatal("wrong nonce accepted")
	}
}

func TestUpdatePublicReleaseDiscovery(t *testing.T) {
	if os.Getenv("LB_TEST_UPDATE_GITHUB") != "1" {
		t.Skip("use LB_TEST_UPDATE_GITHUB=1 for anonymous real GitHub read")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c := newUpdateClient()
	r, err := c.discover(ctx, appVersion, "beta")
	if err != nil {
		t.Fatal(err)
	}
	if r == nil {
		t.Log("anonymous GitHub discovery succeeded; no newer protocol-1 asset")
	} else {
		t.Logf("anonymous discovery: version=%s asset=%d bytes=%d", r.Version, r.AssetID, r.Size)
	}
}

func TestUpdatePublicAssetDownload(t *testing.T) {
	if os.Getenv("LB_TEST_UPDATE_GITHUB") != "1" {
		t.Skip("use LB_TEST_UPDATE_GITHUB=1 for real anonymous asset download")
	}
	c := newUpdateClient()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	response, err := c.request(ctx, "/releases/tags/v0.3.4-beta", "application/vnd.github+json")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var release githubRelease
	if err = json.NewDecoder(response.Body).Decode(&release); err != nil {
		t.Fatal(err)
	}
	for _, a := range release.Assets {
		if strings.HasSuffix(a.Name, ".sha256") && a.Size > 0 && a.Size < 1024 && strings.HasPrefix(a.Digest, "sha256:") {
			r := updateRelease{AssetID: a.ID, Size: a.Size, SHA256: strings.TrimPrefix(a.Digest, "sha256:")}
			if err = c.download(ctx, r, filepath.Join(t.TempDir(), "asset.sha256"), nil); err != nil {
				t.Fatal(err)
			}
			t.Logf("anonymous asset API/CDN download and SHA256 passed: %s (%d bytes)", a.Name, a.Size)
			return
		}
	}
	t.Fatal("no small historical asset with GitHub digest available")
}

func TestUpdateRejectsOversizedZIPEntryAndPEAllocationCounts(t *testing.T) {
	p, _, _ := testUpdateZIP(t, nil, "")
	b, _ := os.ReadFile(p)
	idx := bytes.Index(b, []byte{'P', 'K', 1, 2})
	if idx < 0 {
		t.Fatal("missing central directory")
	}
	binary.LittleEndian.PutUint32(b[idx+24:], updateMaxExpanded+1)
	os.WriteFile(p, b, 0600)
	if _, _, err := validateUpdatePackage(p, "1.0.0", testUpdateDigest(b), int64(len(b))); err == nil {
		t.Fatal("oversized ZIP entry accepted")
	}
	exe := testPE()
	binary.LittleEndian.PutUint32(exe[144:], 0xffffffff)
	if err := boundedUpdatePE(exe); err == nil {
		t.Fatal("unbounded symbol allocation accepted")
	}
}

func TestUpdateCleanupPreservesForeignAndPending(t *testing.T) {
	base := t.TempDir()
	root := t.TempDir()
	id := strings.Repeat("a", 32)
	dir := filepath.Join(base, id)
	os.Mkdir(dir, 0700)
	data := []byte("download")
	os.WriteFile(filepath.Join(dir, "package.zip"), data, 0600)
	owner := updateDownloadOwner{Product: "LocalBroadcast", Root: root, ID: id, SHA256: testUpdateDigest(data)}
	atomicUpdateJSON(filepath.Join(dir, "download-owner.json"), owner)
	foreign := filepath.Join(dir, "user.txt")
	os.WriteFile(foreign, []byte("keep"), 0600)
	// A plan always protects a prepared/unfinished transaction from cancellation.
	os.WriteFile(filepath.Join(dir, "plan.json"), []byte("pending"), 0600)
	discardUpdateDownload(dir, base, root)
	if _, err := os.Stat(filepath.Join(dir, "package.zip")); err != nil {
		t.Fatal("pending package removed")
	}
	os.Remove(filepath.Join(dir, "plan.json"))
	discardUpdateDownload(dir, base, "other-root")
	if _, err := os.Stat(filepath.Join(dir, "package.zip")); err != nil {
		t.Fatal("foreign installation removed")
	}
	discardUpdateDownload(dir, base, root)
	if b, _ := os.ReadFile(foreign); string(b) != "keep" {
		t.Fatal("foreign file removed")
	}
	if _, err := os.Stat(filepath.Join(dir, "package.zip")); !os.IsNotExist(err) {
		t.Fatal("owned unused package not removed")
	}
}

func TestUpdateReadOnlyConfigDoesNotCreateOrMigrateFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if _, err := newConfigStore(path, true); !os.IsNotExist(err) {
		t.Fatalf("read-only loader created config: %v", err)
	}
	if _, err := newConfigStore(path); err != nil {
		t.Fatal(err)
	}
	before, _, _ := fileDigest(path)
	if _, err := newConfigStore(path, true); err != nil {
		t.Fatal(err)
	}
	if err := checkUpdateHash(path, before); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateAPIRejectsUntrustedWrites(t *testing.T) {
	a := &Application{updates: newUpdateManager()}
	mux := http.NewServeMux()
	a.registerUpdateUI(mux)
	for _, method := range []string{"GET", "POST"} {
		r := httptest.NewRequest(method, "http://127.0.0.1:39888/api/updates/check", strings.NewReader(`{"channel":"beta"}`))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 405 && w.Code != 403 {
			t.Fatalf("untrusted write accepted: %d", w.Code)
		}
	}
}
