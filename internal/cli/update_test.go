package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"github.com/ersinkoc/dfmt/internal/selfupdate"
	"github.com/ersinkoc/dfmt/internal/version"
)

// fakeReleases serves /repos/<repo>/releases/latest as a GitHub-shaped
// release and the asset / sums files it names.
func fakeReleases(t *testing.T, tag string, withAsset, withSums bool, sums string) {
	t.Helper()
	asset := selfupdate.AssetName(runtime.GOOS, runtime.GOARCH)
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, _ *http.Request) {
		type a struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		}
		var assets []a
		if withAsset {
			assets = append(assets, a{asset, srv.URL + "/dl/" + asset})
		}
		if withSums {
			assets = append(assets, a{"sha256sums.txt", srv.URL + "/dl/sha256sums.txt"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": tag, "assets": assets})
	})
	mux.HandleFunc("/dl/"+asset, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("new binary")) })
	mux.HandleFunc("/dl/sha256sums.txt", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(sums)) })

	prevAPI, prevWeb := selfupdate.APIBase, selfupdate.WebBase
	selfupdate.APIBase, selfupdate.WebBase = srv.URL, srv.URL
	t.Cleanup(func() { selfupdate.APIBase, selfupdate.WebBase = prevAPI, prevWeb })
}

func withVersion(t *testing.T, v string) {
	t.Helper()
	prev := version.Current
	version.Current = v
	t.Cleanup(func() { version.Current = prev })
}

// runUpdate's decision table, end to end against a fake GitHub. Every case
// stops before Install, so the test binary is never replaced.
func TestRunUpdateDecisions(t *testing.T) {
	asset := selfupdate.AssetName(runtime.GOOS, runtime.GOARCH)
	badSums := fmt.Sprintf("%s  %s\n", strings.Repeat("0", 64), asset)
	cases := []struct {
		name      string
		current   string
		tag       string
		asset     bool
		sums      bool
		args      []string
		wantCode  int
		wantInOut string
	}{
		{"check newer", "v0.9.0", "v0.9.1", true, true, []string{"--check"}, 0, "An update is available"},
		{"check up to date", "v0.9.1", "v0.9.1", true, true, []string{"--check"}, 0, "Up to date"},
		{"check dev build", "dev", "v0.9.1", true, true, []string{"--check"}, 0, "development build"},
		{"not newer", "v0.9.1", "v0.9.1", true, true, nil, 0, "Up to date"},
		{"dev build refuses", "dev", "v0.9.1", true, true, nil, 1, ""},
		{"pinned same tag", "v0.9.1", "v0.9.1", true, true, []string{"--version", "v0.9.1"}, 0, "Already running"},
		{"no asset", "v0.9.0", "v0.9.1", false, true, nil, 1, ""},
		{"no sums", "v0.9.0", "v0.9.1", true, false, nil, 1, ""},
		{"checksum mismatch", "v0.9.0", "v0.9.1", true, true, nil, 1, "Downloading"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withVersion(t, tc.current)
			fakeReleases(t, tc.tag, tc.asset, tc.sums, badSums)
			var code int
			out := captureStdout(t, func() { code = runUpdate(tc.args) })
			if code != tc.wantCode {
				t.Errorf("code = %d, want %d (out %q)", code, tc.wantCode, out)
			}
			if tc.wantInOut != "" && !strings.Contains(out, tc.wantInOut) {
				t.Errorf("stdout %q lacks %q", out, tc.wantInOut)
			}
			if strings.Contains(out, "Updated ") {
				t.Fatal("an update was installed during a test")
			}
		})
	}
}

func TestRunUpdateArgErrors(t *testing.T) {
	if code := runUpdate([]string{"--no-such-flag"}); code != 2 {
		t.Errorf("bad flag: code %d, want 2", code)
	}
	if code := runUpdate([]string{"stray"}); code != 2 {
		t.Errorf("positional arg: code %d, want 2", code)
	}
	if code := runUpdate([]string{"--help"}); code != 0 {
		t.Errorf("--help: code %d, want 0", code)
	}
}
