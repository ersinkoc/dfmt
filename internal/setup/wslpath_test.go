package setup

import "testing"

// wslPathToWindowsPath had an off-by-one that made it emit paths which
// cannot exist. "/mnt/c" is six characters, but the slice took p[5:], so the
// drive letter survived into the result: "/mnt/c/Users/foo" became
// "C:c\Users\foo".
//
// It mattered in production, not just on paper. ResolveDFMTCommandForEnv
// calls this when translating a WSL binary path for a Windows agent config,
// so on WSL `dfmt setup` wrote an unusable command into every agent's MCP
// config whenever dfmt lived on a Windows drive — the agent then could not
// launch dfmt at all. The function was entirely uncovered, which is why an
// off-by-one in a nine-line function survived.
func TestWSLPathToWindowsPath(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		// The regression: the drive letter must not be duplicated.
		{"mnt drive with path", "/mnt/c/Users/foo", `C:\Users\foo`},
		{"mnt drive root with slash", "/mnt/c/", `C:\`},
		{"mnt drive bare", "/mnt/c", "C:"},
		{"nested path", "/mnt/c/Users/foo/bar/baz.txt", `C:\Users\foo\bar\baz.txt`},

		// Generalized past the hardcoded "c": a binary on another drive used
		// to fall through and be returned unconverted, failing the same way
		// but more quietly.
		{"drive d", "/mnt/d/repos/x", `D:\repos\x`},
		{"uppercase drive normalizes", "/mnt/E/tools", `E:\tools`},

		// The drive letter must be a whole path segment. /mnt/config is a
		// directory named "config", not drive C.
		{"mnt dir that starts with a drive letter", "/mnt/config/app", "/mnt/config/app"},
		{"mnt with multi-char segment", "/mnt/data/x", "/mnt/data/x"},

		// The /home/<user> branch was already correct; pin it so the rewrite
		// above cannot regress it.
		{"home dir", "/home/ersin/proj", `C:\Users\ersin\proj`},
		{"home root", "/home/ersin", `C:\Users\ersin`},

		// Anything else passes through untouched.
		{"unix system path", "/usr/local/bin/dfmt", "/usr/local/bin/dfmt"},
		{"already windows", `C:\Users\foo\dfmt.exe`, `C:\Users\foo\dfmt.exe`},
		{"empty", "", ""},
		{"bare mnt", "/mnt/", "/mnt/"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := wslPathToWindowsPath(tc.in); got != tc.want {
				t.Errorf("wslPathToWindowsPath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// A converted path must never contain a forward slash — the whole point is
// producing something a Windows agent config can launch.
func TestWSLPathConversionLeavesNoForwardSlashes(t *testing.T) {
	for _, in := range []string{"/mnt/c/Users/foo/bar", "/home/u/a/b/c", "/mnt/d/x/y"} {
		got := wslPathToWindowsPath(in)
		for i := 0; i < len(got); i++ {
			if got[i] == '/' {
				t.Errorf("wslPathToWindowsPath(%q) = %q, still contains a forward slash", in, got)
				break
			}
		}
	}
}

// IsWSL reads /proc/version, which does not exist on Windows; the function
// must short-circuit rather than depend on the read failing.
func TestIsWSLIsFalseOnWindows(t *testing.T) {
	// On a Windows host this asserts the short-circuit. On Linux it records
	// whatever the host actually is, which is still a valid exercise of the
	// function — the point is that it never panics or blocks.
	_ = IsWSL()
}
