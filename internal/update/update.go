// Package update replaces the running binary with a GitHub release: the
// same assets and checksums.txt that install.sh / install.ps1 use.
package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const maxAsset = 200 << 20

var client = &http.Client{
	Timeout: 2 * time.Minute,
	// Don't follow redirects for /releases/latest: the Location is the answer.
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if strings.HasSuffix(via[0].URL.Path, "/releases/latest") {
			return http.ErrUseLastResponse
		}
		return nil
	},
}

// Repo is owner/name, overridable like the install scripts.
func Repo() string {
	if r := os.Getenv("USED_REPO"); r != "" {
		return r
	}
	return "KilianSen/used"
}

// Latest returns the newest release tag, e.g. "v0.2.0".
func Latest(repo string) (string, error) {
	resp, err := client.Get("https://github.com/" + repo + "/releases/latest")
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if resp.StatusCode/100 != 3 || !strings.Contains(loc, "/releases/tag/") {
		return "", fmt.Errorf("no published release found for %s", repo)
	}
	return path.Base(loc), nil
}

// Newer reports whether tag is a later version than current ("0.1.1" or
// "v0.1.1"). Pre-release suffixes are ignored.
func Newer(tag, current string) bool {
	a, b := parse(tag), parse(current)
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

func parse(v string) [3]int {
	v, _, _ = strings.Cut(strings.TrimPrefix(v, "v"), "-")
	var out [3]int
	for i, p := range strings.SplitN(v, ".", 3) {
		out[i], _ = strconv.Atoi(p)
	}
	return out
}

// Download fetches this platform's binary for tag and verifies its checksum.
func Download(repo, tag string) ([]byte, error) {
	ext := ".tar.gz"
	if runtime.GOOS == "windows" {
		ext = ".zip"
	}
	asset := fmt.Sprintf("used_%s_%s%s", runtime.GOOS, runtime.GOARCH, ext)
	base := "https://github.com/" + repo + "/releases/download/" + tag + "/"

	sums, err := get(base + "checksums.txt")
	if err != nil {
		return nil, err
	}
	want := ""
	for line := range strings.Lines(string(sums)) {
		if f := strings.Fields(line); len(f) == 2 && f[1] == asset {
			want = f[0]
		}
	}
	if want == "" {
		return nil, fmt.Errorf("%s is not part of release %s", asset, tag)
	}
	archive, err := get(base + asset)
	if err != nil {
		return nil, err
	}
	if got := sha256.Sum256(archive); hex.EncodeToString(got[:]) != want {
		return nil, fmt.Errorf("checksum mismatch for %s", asset)
	}
	if ext == ".zip" {
		return fromZip(archive, "used.exe")
	}
	return fromTarGz(archive, "used")
}

func get(url string) ([]byte, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxAsset))
}

func fromTarGz(b []byte, name string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err != nil {
			return nil, fmt.Errorf("%s not found in archive: %w", name, err)
		}
		if path.Base(h.Name) == name && h.Typeflag == tar.TypeReg {
			return io.ReadAll(io.LimitReader(tr, maxAsset))
		}
	}
}

func fromZip(b []byte, name string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return nil, err
	}
	for _, f := range zr.File {
		if path.Base(f.Name) == name {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(io.LimitReader(rc, maxAsset))
		}
	}
	return nil, errors.New(name + " not found in archive")
}

// Replace swaps exe for bin. Unix: write beside it and rename over it (running
// processes keep the old inode). Windows: a running exe can be renamed but
// not overwritten, so it moves aside to exe.old first.
func Replace(exe string, bin []byte) error {
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".used-update-*")
	if err != nil {
		return fmt.Errorf("can't write next to %s (%w); reinstall with the install script or update it the way you installed it", exe, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		old := exe + ".old"
		_ = os.Remove(old) // left by the previous update
		if err := os.Rename(exe, old); err != nil {
			return err
		}
		if err := os.Rename(tmp.Name(), exe); err != nil {
			_ = os.Rename(old, exe)
			return err
		}
		return nil
	}
	return os.Rename(tmp.Name(), exe)
}
