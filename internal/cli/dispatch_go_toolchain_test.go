package cli

import "testing"

// TestGoToolchainAtLeast pins the comparator's behavior across realistic
// runtime.Version() shapes:
//
//   - Stable releases ("go1.26.1") parse cleanly.
//   - The patch field is optional ("go1.26") — must be treated as
//     patch=0 for the comparison.
//   - Pre-release suffixes ("go1.27.0-rc1") strip the suffix and use
//     the version part.
//   - "devel go1.27" or other unparseable inputs fall through to "true"
//     so developers building from tip don't see a spurious warning.
//
// The doctor caller invokes it with (1, 26, 2) — that's the threshold
// for the Jan-2026 stdlib CVEs. Each case below exercises that threshold
// directly.
func TestGoToolchainAtLeast(t *testing.T) {
	cases := []struct {
		version string
		major   int
		minor   int
		patch   int
		want    bool
	}{
		// at exact threshold
		{"go1.26.2", 1, 26, 2, true},
		// older patches than threshold
		{"go1.26.1", 1, 26, 2, false},
		{"go1.26.0", 1, 26, 2, false},
		{"go1.26", 1, 26, 2, false},
		// older minor than threshold
		{"go1.25.4", 1, 26, 2, false},
		// newer patches and minors
		{"go1.26.3", 1, 26, 2, true},
		{"go1.27.0", 1, 26, 2, true},
		{"go1.27.0-rc1", 1, 26, 2, true},
		{"go2.0.0", 1, 26, 2, true},
		// unparseable / devel — be permissive so tip builds don't warn
		{"devel go1.27", 1, 26, 2, true},
		{"go-not-a-version", 1, 26, 2, true},
		{"", 1, 26, 2, true},
		// trailing "+" tolerated
		{"go1.26.2+", 1, 26, 2, true},
	}
	for _, tc := range cases {
		t.Run(tc.version, func(t *testing.T) {
			got := goToolchainAtLeast(tc.version, tc.major, tc.minor, tc.patch)
			if got != tc.want {
				t.Errorf("goToolchainAtLeast(%q, %d, %d, %d) = %v, want %v",
					tc.version, tc.major, tc.minor, tc.patch, got, tc.want)
			}
		})
	}
}
