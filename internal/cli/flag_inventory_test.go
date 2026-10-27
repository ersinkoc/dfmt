package cli

import (
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestCLIFlagInventory pins every subcommand's flag names, read from its
// own --help. Scripts and agent instructions call these flags; a renamed or
// dropped one is a breaking change and has to show up here (update the
// table in the same change, with a CHANGELOG entry).
func TestCLIFlagInventory(t *testing.T) {
	want := map[string]string{
		"init":       "-agent,-dir",
		"remember":   "-actor,-cached-tokens,-data,-input-tokens,-model,-output-tokens,-source,-tags,-type",
		"search":     "-limit,-type",
		"recall":     "-budget,-format,-save",
		"doctor":     "-dir",
		"dashboard":  "-open",
		"update":     "-check,-force,-version",
		"tail":       "-follow,-from,-limit",
		"setup":      "-agent,-dry-run,-force,-refresh,-uninstall,-verify",
		"exec":       "-async,-cancel,-intent,-job,-lang,-timeout",
		"read":       "-intent,-limit,-offset",
		"fetch":      "-body,-intent,-method,-timeout",
		"glob":       "-intent",
		"grep":       "-files,-i,-intent",
		"edit":       "-old",
		"write":      "-content",
		"quickstart": "-agent,-dir",
		"remove":     "-dir",
	}
	flagLine := regexp.MustCompile(`(?m)^  (-[a-zA-Z0-9-]+)`)
	for cmd, flags := range want {
		out := captureStderrInventory(t, func() { _ = Dispatch([]string{cmd, "--help"}) })
		var got []string
		for _, m := range flagLine.FindAllStringSubmatch(out, -1) {
			got = append(got, m[1])
		}
		sort.Strings(got)
		if g := strings.Join(got, ","); g != flags {
			t.Errorf("dfmt %s flags = %q, pinned %q", cmd, g, flags)
		}
	}
}

func captureStderrInventory(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	_ = w.Close()
	os.Stderr = orig
	return <-done
}
