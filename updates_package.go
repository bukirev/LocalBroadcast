package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"debug/pe"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const updateRepo = "bukirev/LocalBroadcast"
const updateDependencySet = "ffmpeg-9.0.2-srt"
const updateMaxZip = 32 << 20
const updateMaxExpanded = 64 << 20

type releaseVersion struct {
	core [3]uint64
	pre  []string
}

var versionPattern = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)

func parseReleaseVersion(s string) (releaseVersion, error) {
	v := releaseVersion{}
	if len(s) > 128 {
		return v, fmt.Errorf("слишком длинная версия")
	}
	m := versionPattern.FindStringSubmatch(s)
	if m == nil {
		return v, fmt.Errorf("неверная версия %q", s)
	}
	for i := 0; i < 3; i++ {
		n, err := strconv.ParseUint(m[i+1], 10, 64)
		if err != nil {
			return v, err
		}
		v.core[i] = n
	}
	if m[4] != "" {
		v.pre = strings.Split(m[4], ".")
		for _, p := range v.pre {
			if numericID(p) && len(p) > 1 && p[0] == '0' {
				return v, fmt.Errorf("leading zero in prerelease")
			}
		}
	}
	return v, nil
}
func numericID(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(s) > 0
}
func compareReleaseVersion(a, b releaseVersion) int {
	for i := range a.core {
		if a.core[i] < b.core[i] {
			return -1
		}
		if a.core[i] > b.core[i] {
			return 1
		}
	}
	if len(a.pre) == 0 && len(b.pre) > 0 {
		return 1
	}
	if len(b.pre) == 0 && len(a.pre) > 0 {
		return -1
	}
	for i := 0; i < min(len(a.pre), len(b.pre)); i++ {
		x, y := a.pre[i], b.pre[i]
		if x == y {
			continue
		}
		nx, ny := numericID(x), numericID(y)
		if nx != ny {
			if nx {
				return -1
			}
			return 1
		}
		if nx && len(x) != len(y) {
			if len(x) < len(y) {
				return -1
			}
			return 1
		}
		if x < y {
			return -1
		}
		return 1
	}
	if len(a.pre) < len(b.pre) {
		return -1
	}
	if len(a.pre) > len(b.pre) {
		return 1
	}
	return 0
}

type updateFile struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type updateManifest struct {
	Format          int                   `json:"format"`
	Product         string                `json:"product"`
	Version         string                `json:"version"`
	Runtime         string                `json:"runtime"`
	UpdaterProtocol int                   `json:"updaterProtocol"`
	DataSchema      int                   `json:"dataSchema"`
	DependencySet   string                `json:"dependencySet"`
	Files           map[string]updateFile `json:"files"`
}

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func fileDigest(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, err
}
func readBounded(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if int64(len(b)) > max {
		return nil, fmt.Errorf("файл превышает лимит")
	}
	return b, err
}
func readUpdateJSON(path string, dst any) error {
	b, err := readBounded(path, 16<<10)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dst)
}

// Validate the complete, tiny core package in memory. There is no archive path
// extraction: only these three literal filenames can ever be written.
func validateUpdatePackage(path, version, digest string, size int64) (updateManifest, map[string][]byte, error) {
	var m updateManifest
	if !digestPattern.MatchString(digest) || size <= 0 || size > updateMaxZip {
		return m, nil, fmt.Errorf("нет допустимого SHA-256/размера пакета")
	}
	b, err := readBounded(path, updateMaxZip)
	if err != nil {
		return m, nil, err
	}
	h := sha256.Sum256(b)
	if int64(len(b)) != size || hex.EncodeToString(h[:]) != digest {
		return m, nil, fmt.Errorf("SHA-256 или размер пакета не совпадает; установка не началась")
	}
	z, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return m, nil, err
	}
	if len(z.File) != 3 {
		return m, nil, fmt.Errorf("ожидаются ровно три файла update-пакета")
	}
	files := map[string][]byte{}
	total := int64(0)
	for _, f := range z.File {
		limit := int64(updateMaxExpanded)
		if f.Name == "update-manifest.json" {
			limit = 16 << 10
		}
		if f.Name == "RELEASE_NOTES.md" {
			limit = 256 << 10
		}
		if (f.Name != "LocalBroadcast.exe" && f.Name != "update-manifest.json" && f.Name != "RELEASE_NOTES.md") || f.Mode()&os.ModeType != 0 || files[f.Name] != nil || f.UncompressedSize64 > uint64(limit) {
			return m, nil, fmt.Errorf("недопустимый файл ZIP: %q", f.Name)
		}
		total += int64(f.UncompressedSize64)
		if total > updateMaxExpanded {
			return m, nil, fmt.Errorf("распакованный пакет слишком большой")
		}
		r, err := f.Open()
		if err != nil {
			return m, nil, err
		}
		data, err := io.ReadAll(io.LimitReader(r, limit+1))
		closeErr := r.Close()
		if err != nil {
			return m, nil, err
		}
		if closeErr != nil {
			return m, nil, closeErr
		}
		if int64(len(data)) != int64(f.UncompressedSize64) {
			return m, nil, fmt.Errorf("неверный размер ZIP entry")
		}
		files[f.Name] = data
	}
	if err = json.Unmarshal(files["update-manifest.json"], &m); err != nil {
		return m, nil, err
	}
	if m.Format != 1 || m.Product != "LocalBroadcast" || m.Version != version || m.Runtime != "win-x64" || m.UpdaterProtocol != 1 || m.DataSchema != 2 || m.DependencySet != updateDependencySet || len(m.Files) != 2 {
		return m, nil, fmt.Errorf("несовместимый manifest: нужен полный установщик или другой пакет")
	}
	if _, err = parseReleaseVersion(m.Version); err != nil {
		return m, nil, err
	}
	for _, name := range []string{"LocalBroadcast.exe", "RELEASE_NOTES.md"} {
		expected, ok := m.Files[name]
		data := files[name]
		h := sha256.Sum256(data)
		if !ok || expected.Size != int64(len(data)) || !digestPattern.MatchString(expected.SHA256) || expected.SHA256 != hex.EncodeToString(h[:]) {
			return m, nil, fmt.Errorf("manifest: неверный хеш %s", name)
		}
	}
	image := files["LocalBroadcast.exe"]
	if err = boundedUpdatePE(image); err != nil {
		return m, nil, err
	}
	exe, err := pe.NewFile(bytes.NewReader(image))
	if err != nil {
		return m, nil, fmt.Errorf("неверный PE: %w", err)
	}
	defer exe.Close()
	_, is64 := exe.OptionalHeader.(*pe.OptionalHeader64)
	if exe.Machine != pe.IMAGE_FILE_MACHINE_AMD64 || !is64 || exe.Characteristics&2 == 0 {
		return m, nil, fmt.Errorf("нужен Windows AMD64 EXE")
	}
	return m, files, nil
}

// debug/pe reads COFF symbols/string tables during construction. Bound their
// declared counts before parsing an untrusted header to prevent huge allocations.
func boundedUpdatePE(b []byte) error {
	bad := func() error { return fmt.Errorf("неверные/слишком большие таблицы PE") }
	if len(b) < 64 || string(b[:2]) != "MZ" {
		return bad()
	}
	off := uint64(binary.LittleEndian.Uint32(b[60:64]))
	size := uint64(len(b))
	if off+24 > size || string(b[off:off+4]) != "PE\x00\x00" {
		return bad()
	}
	sections := uint64(binary.LittleEndian.Uint16(b[off+6:]))
	optional := uint64(binary.LittleEndian.Uint16(b[off+20:]))
	if sections > 96 || optional > 512 || off+24+optional+sections*40 > size {
		return bad()
	}
	ptr := uint64(binary.LittleEndian.Uint32(b[off+12:]))
	symbols := uint64(binary.LittleEndian.Uint32(b[off+16:]))
	if symbols > 65536 {
		return bad()
	}
	if ptr != 0 {
		end := ptr + symbols*18
		if end+4 > size {
			return bad()
		}
		table := uint64(binary.LittleEndian.Uint32(b[end:]))
		if table < 4 || end+table > size {
			return bad()
		}
	}
	return nil
}

func buildUpdatePackage(exe, notes, version, out string) error {
	x, err := os.ReadFile(exe)
	if err != nil {
		return err
	}
	n, err := os.ReadFile(notes)
	if err != nil {
		return err
	}
	m := updateManifest{Format: 1, Product: "LocalBroadcast", Version: version, Runtime: "win-x64", UpdaterProtocol: 1, DataSchema: 2, DependencySet: updateDependencySet, Files: map[string]updateFile{}}
	files := map[string][]byte{"LocalBroadcast.exe": x, "RELEASE_NOTES.md": n}
	for name, b := range files {
		h := sha256.Sum256(b)
		m.Files[name] = updateFile{int64(len(b)), hex.EncodeToString(h[:])}
	}
	files["update-manifest.json"], err = json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	z := zip.NewWriter(f)
	for _, name := range []string{"update-manifest.json", "LocalBroadcast.exe", "RELEASE_NOTES.md"} {
		w, e := z.Create(name)
		if e != nil {
			z.Close()
			f.Close()
			return e
		}
		if _, e = w.Write(files[name]); e != nil {
			z.Close()
			f.Close()
			return e
		}
	}
	if err = z.Close(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	digest, size, err := fileDigest(out)
	if err != nil {
		return err
	}
	if _, _, err = validateUpdatePackage(out, version, digest, size); err != nil {
		return err
	}
	return os.WriteFile(out+".sha256", []byte(digest+"  "+filepath.Base(out)+"\n"), 0600)
}
