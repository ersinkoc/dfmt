// Package selfupdate replaces the running dfmt binary with a published
// GitHub release (ADR-0028).
//
// It only runs when the user asks (`dfmt update`); dfmt never checks for
// updates on its own. Every download is verified against the release's
// sha256sums.txt, the new binary is executed once (`--version`) before it
// replaces anything, and the swap is a rename in the binary's own
// directory, so an interrupted update leaves the old binary in place.
package selfupdate

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Repo is the GitHub repository releases are published to.
const Repo = "ersinkoc/dfmt"

// APIBase is the GitHub API root. A var so tests can point it at a local
// server; production never changes it.
var APIBase = "https://api.github.com"

// WebBase is the github.com root, used when the API refuses to answer.
// A var for the same reason as APIBase.
var WebBase = "https://github.com"

// maxAssetBytes bounds a download. A release binary is ~10 MB; anything
// far beyond that is not a dfmt binary.
const maxAssetBytes = 128 << 20

// checksumsAsset is the file the release workflow publishes next to the
// binaries, in `sha256sum` format.
const checksumsAsset = "sha256sums.txt"

// Release is the subset of a GitHub release this package needs.
type Release struct {
	Tag    string
	Assets map[string]string // asset name -> download URL
}

// FetchRelease returns the release for tag, or the latest release when
// tag is empty.
//
// The API allows 60 unauthenticated requests an hour per IP, shared by
// everyone behind one NAT, so a 403/429 is routine rather than exotic.
// It then falls back to github.com itself: /releases/latest redirects to
// the tag, and assets live at fixed /releases/download/<tag>/<name> URLs.
// The checksum verification downstream is identical either way.
func FetchRelease(ctx context.Context, client *http.Client, tag string) (*Release, error) {
	rel, err := fetchReleaseAPI(ctx, client, tag)
	if errors.Is(err, errRateLimited) {
		return fetchReleaseWeb(ctx, client, tag)
	}
	return rel, err
}

// errRateLimited marks an API refusal that the web fallback can route
// around.
var errRateLimited = errors.New("GitHub API rate limit exceeded")

func fetchReleaseAPI(ctx context.Context, client *http.Client, tag string) (*Release, error) {
	endpoint := APIBase + "/repos/" + Repo + "/releases/latest"
	if tag != "" {
		endpoint = APIBase + "/repos/" + Repo + "/releases/tags/" + url.PathEscape(tag)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query releases: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		if tag != "" {
			return nil, fmt.Errorf("no release %s", tag)
		}
		return nil, errors.New("no published release")
	}
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return nil, errRateLimited
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("query releases: HTTP %d", resp.StatusCode)
	}
	var body struct {
		TagName string `json:"tag_name"`
		Draft   bool   `json:"draft"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode release: %w", err)
	}
	if body.TagName == "" || body.Draft {
		return nil, errors.New("release has no tag or is a draft")
	}
	rel := &Release{Tag: body.TagName, Assets: make(map[string]string, len(body.Assets))}
	for _, a := range body.Assets {
		rel.Assets[a.Name] = a.URL
	}
	return rel, nil
}

// fetchReleaseWeb resolves a release without the API. Only the assets
// this host needs are listed: the web pages carry no machine-readable
// asset list, and a missing one surfaces as a 404 on download.
func fetchReleaseWeb(ctx context.Context, client *http.Client, tag string) (*Release, error) {
	if tag == "" {
		var err error
		if tag, err = latestTagWeb(ctx, client); err != nil {
			return nil, err
		}
	}
	base := WebBase + "/" + Repo + "/releases/download/" + url.PathEscape(tag) + "/"
	asset := AssetName(runtime.GOOS, runtime.GOARCH)
	return &Release{Tag: tag, Assets: map[string]string{
		asset:          base + asset,
		checksumsAsset: base + checksumsAsset,
	}}, nil
}

// latestTagWeb reads the tag from the redirect github.com/<repo>/releases/latest
// answers with (…/releases/tag/<tag>), without following it.
func latestTagWeb(ctx context.Context, client *http.Client) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, WebBase+"/"+Repo+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	noFollow := *client
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := noFollow.Do(req)
	if err != nil {
		return "", fmt.Errorf("query releases: %w", err)
	}
	_ = resp.Body.Close()
	loc := resp.Header.Get("Location")
	_, tag, ok := strings.Cut(loc, "/releases/tag/")
	if resp.StatusCode/100 != 3 || !ok || tag == "" {
		return "", fmt.Errorf("%w, and github.com did not name the latest release (HTTP %d)",
			errRateLimited, resp.StatusCode)
	}
	if t, err := url.PathUnescape(tag); err == nil {
		tag = t
	}
	return tag, nil
}

// AssetName is the release asset built for goos/goarch.
func AssetName(goos, goarch string) string {
	name := "dfmt-" + goos + "-" + goarch
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

// Download fetches url, refusing anything larger than maxAssetBytes.
func Download(ctx context.Context, client *http.Client, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: HTTP %d", assetBase(rawURL), resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAssetBytes+1))
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", assetBase(rawURL), err)
	}
	if len(data) > maxAssetBytes {
		return nil, fmt.Errorf("download %s: larger than %d MiB", assetBase(rawURL), maxAssetBytes>>20)
	}
	return data, nil
}

func assetBase(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil {
		return filepath.Base(u.Path)
	}
	return rawURL
}

// VerifyChecksum checks data against name's line in a sha256sums.txt.
// A missing line is an error: an unverifiable binary is never installed.
func VerifyChecksum(sums []byte, name string, data []byte) error {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != name {
			continue
		}
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, fields[0]) {
			return fmt.Errorf("checksum mismatch for %s: got %s, want %s", name, got, fields[0])
		}
		return nil
	}
	return fmt.Errorf("%s is not listed in %s", name, checksumsAsset)
}

// Version is a parsed vMAJOR.MINOR.PATCH release tag.
type Version struct{ Major, Minor, Patch int }

// ParseVersion parses a release tag. ok is false for anything that is not
// exactly vX.Y.Z — notably the `v0.9.0-3-gabc` / `dev-…` strings of builds
// made from a checkout, which are not releases and cannot be ordered
// against one.
func ParseVersion(s string) (v Version, ok bool) {
	parts := strings.Split(strings.TrimPrefix(s, "v"), ".")
	if !strings.HasPrefix(s, "v") || len(parts) != 3 {
		return Version{}, false
	}
	nums := [3]int{}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return Version{}, false
		}
		nums[i] = n
	}
	return Version{nums[0], nums[1], nums[2]}, true
}

// Less reports whether v orders before w.
func (v Version) Less(w Version) bool {
	if v.Major != w.Major {
		return v.Major < w.Major
	}
	if v.Minor != w.Minor {
		return v.Minor < w.Minor
	}
	return v.Patch < w.Patch
}

// Executable returns the real path of the running binary.
func Executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return exe, nil
}

// Install writes data next to exe, runs it once with --version to prove it
// starts on this machine and reports wantTag, then renames it over exe.
//
// On Windows a running executable cannot be overwritten but can be renamed,
// so the old binary is moved to `<exe>.old` first; CleanupOld removes it on
// a later run.
func Install(exe string, data []byte, wantTag string) error {
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".dfmt-update-*")
	if err != nil {
		return fmt.Errorf("cannot write to %s (reinstall with install.sh, "+
			"or run with permission to write there): %w", dir, err)
	}
	tmpPath := tmp.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s: %w", tmpPath, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		// CreateTemp names have no .exe; Windows will not run them.
		withExt := tmpPath + ".exe"
		if err := os.Rename(tmpPath, withExt); err != nil {
			return err
		}
		tmpPath = withExt
	}

	if err := smokeTest(tmpPath, wantTag); err != nil {
		return err
	}

	if runtime.GOOS == "windows" {
		old := exe + ".old"
		_ = os.Remove(old)
		if err := os.Rename(exe, old); err != nil {
			return fmt.Errorf("move running binary aside: %w", err)
		}
		if err := os.Rename(tmpPath, exe); err != nil {
			_ = os.Rename(old, exe)
			return fmt.Errorf("install new binary: %w", err)
		}
	} else if err := os.Rename(tmpPath, exe); err != nil {
		return fmt.Errorf("install new binary: %w", err)
	}
	keep = true
	return nil
}

// smokeTest runs the candidate binary with --version. A binary for the
// wrong platform, a truncated one, or one stamped with another version
// fails here, before the working binary is touched.
func smokeTest(bin, wantTag string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("downloaded binary does not run: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	if !strings.Contains(string(out), wantTag) {
		return fmt.Errorf("downloaded binary reports %q, want %s", strings.TrimSpace(string(out)), wantTag)
	}
	return nil
}

// CleanupOld removes the `<exe>.old` a Windows update leaves behind. Best
// effort: it is still locked while the old binary runs.
func CleanupOld(exe string) {
	if runtime.GOOS == "windows" {
		_ = os.Remove(exe + ".old")
	}
}
