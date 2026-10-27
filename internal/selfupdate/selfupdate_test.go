package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseVersionAndLess(t *testing.T) {
	for _, s := range []string{"v0.9.0-3-gabc1234", "dev-abc", "0.9.0", "v1.2", "v1.2.x", ""} {
		if _, ok := ParseVersion(s); ok {
			t.Errorf("ParseVersion(%q) ok, want rejected (not a release tag)", s)
		}
	}
	a, _ := ParseVersion("v0.9.0")
	b, _ := ParseVersion("v0.10.0")
	if !a.Less(b) || b.Less(a) || a.Less(a) {
		t.Errorf("ordering wrong: v0.9.0 < v0.10.0 must hold numerically, not lexically")
	}
}

func sumLine(name string, data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:]) + "  " + name + "\n"
}

func TestVerifyChecksum(t *testing.T) {
	data := []byte("binary")
	sums := []byte(sumLine("dfmt-other", []byte("x")) + sumLine("dfmt-linux-amd64", data))
	if err := VerifyChecksum(sums, "dfmt-linux-amd64", data); err != nil {
		t.Errorf("valid checksum rejected: %v", err)
	}
	if err := VerifyChecksum(sums, "dfmt-linux-amd64", []byte("tampered")); err == nil {
		t.Error("tampered binary accepted")
	}
	if err := VerifyChecksum(sums, "dfmt-linux-arm64", data); err == nil {
		t.Error("binary with no checksum line accepted")
	}
}

// fakeGitHub serves one release with the given assets.
func fakeGitHub(t *testing.T, tag string, assets map[string][]byte) {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	release := func(w http.ResponseWriter) {
		var parts []string
		for name := range assets {
			parts = append(parts, fmt.Sprintf(`{"name":%q,"browser_download_url":%q}`, name, srv.URL+"/dl/"+name))
		}
		fmt.Fprintf(w, `{"tag_name":%q,"draft":false,"assets":[%s]}`, tag, strings.Join(parts, ","))
	}
	mux.HandleFunc("/repos/"+Repo+"/releases/latest", func(w http.ResponseWriter, _ *http.Request) { release(w) })
	mux.HandleFunc("/repos/"+Repo+"/releases/tags/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/"+tag) {
			release(w)
			return
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("/dl/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(assets[strings.TrimPrefix(r.URL.Path, "/dl/")])
	})
	prev := APIBase
	APIBase = srv.URL
	t.Cleanup(func() { APIBase = prev })
}

func TestFetchReleaseLatestAndTag(t *testing.T) {
	fakeGitHub(t, "v9.9.9", map[string][]byte{"dfmt-linux-amd64": []byte("x")})
	ctx := context.Background()
	rel, err := FetchRelease(ctx, http.DefaultClient, "")
	if err != nil || rel.Tag != "v9.9.9" || rel.Assets["dfmt-linux-amd64"] == "" {
		t.Fatalf("latest = %+v, %v", rel, err)
	}
	if _, err := FetchRelease(ctx, http.DefaultClient, "v9.9.9"); err != nil {
		t.Errorf("by tag: %v", err)
	}
	if _, err := FetchRelease(ctx, http.DefaultClient, "v0.0.1"); err == nil || !strings.Contains(err.Error(), "no release") {
		t.Errorf("missing tag: err = %v", err)
	}
}

// script returns a shell script that prints a version, standing in for a
// release binary in the smoke test.
func script(version string) []byte {
	return []byte("#!/bin/sh\necho 'dfmt " + version + "'\n")
}

func TestInstallReplacesAfterSmokeTest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as the stand-in binary")
	}
	exe := filepath.Join(t.TempDir(), "dfmt")
	if err := os.WriteFile(exe, script("v0.0.1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Install(exe, script("v9.9.9"), "v9.9.9"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got, _ := os.ReadFile(exe)
	if !strings.Contains(string(got), "v9.9.9") {
		t.Errorf("binary not replaced:\n%s", got)
	}
	if st, err := os.Stat(exe); err != nil || st.Mode().Perm()&0o100 == 0 {
		t.Errorf("installed binary not executable: %v %v", st, err)
	}
	assertNoTempLeft(t, filepath.Dir(exe))
}

// A binary that does not report the expected version (wrong build, wrong
// platform, truncated) must never replace the working one.
func TestInstallKeepsOldBinaryWhenSmokeTestFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as the stand-in binary")
	}
	exe := filepath.Join(t.TempDir(), "dfmt")
	old := script("v0.0.1")
	if err := os.WriteFile(exe, old, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{script("v1.0.0"), []byte("\x7fELF garbage")} {
		if err := Install(exe, bad, "v9.9.9"); err == nil {
			t.Fatal("Install accepted a binary that failed the smoke test")
		}
		if got, _ := os.ReadFile(exe); string(got) != string(old) {
			t.Fatalf("old binary modified after a failed update:\n%s", got)
		}
		assertNoTempLeft(t, filepath.Dir(exe))
	}
}

func assertNoTempLeft(t *testing.T, dir string) {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".dfmt-update-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

// The API's unauthenticated limit (60/h per IP, shared behind NAT) is
// routine. A 403 there must fall back to github.com: the latest-release
// redirect names the tag, assets sit at fixed download URLs.
func TestFetchReleaseFallsBackWhenRateLimited(t *testing.T) {
	asset := AssetName(runtime.GOOS, runtime.GOARCH)
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"API rate limit exceeded"}`, http.StatusForbidden)
	})
	mux.HandleFunc("/"+Repo+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/"+Repo+"/releases/tag/v9.9.9", http.StatusFound)
	})
	prevAPI, prevWeb := APIBase, WebBase
	APIBase, WebBase = srv.URL, srv.URL
	t.Cleanup(func() { APIBase, WebBase = prevAPI, prevWeb })

	rel, err := FetchRelease(context.Background(), http.DefaultClient, "")
	if err != nil {
		t.Fatalf("FetchRelease: %v", err)
	}
	if rel.Tag != "v9.9.9" {
		t.Errorf("tag = %q, want v9.9.9 from the redirect", rel.Tag)
	}
	want := srv.URL + "/" + Repo + "/releases/download/v9.9.9/" + asset
	if rel.Assets[asset] != want || rel.Assets["sha256sums.txt"] == "" {
		t.Errorf("assets = %v, want %s and sha256sums.txt", rel.Assets, want)
	}

	rel, err = FetchRelease(context.Background(), http.DefaultClient, "v1.2.3")
	if err != nil || rel.Tag != "v1.2.3" {
		t.Errorf("by tag = %+v, %v", rel, err)
	}
}
