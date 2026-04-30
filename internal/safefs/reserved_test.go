package safefs

import (
	"errors"
	"testing"
)

func TestIsWindowsReservedComponent(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"NUL", true},
		{"nul", true},
		{"Nul", true},
		{"NUL:", true},
		{"NUL.txt", true},
		{"nul.log", true},
		{" NUL ", true},
		{"CON", true},
		{"PRN", true},
		{"AUX", true},
		{"COM1", true},
		{"COM9", true},
		{"COM0", true},
		{"LPT1", true},
		{"LPT9", true},
		{"COM10", false}, // only COM0-COM9 are reserved
		{"LPT10", false},
		{"NULL", false}, // four letters; not the device name
		{"foo", false},
		{"", false},
		{".", false},
		{":", false},
		{"NULA", false},
		{"file.NUL", false}, // suffix only; stem is "file"
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := IsWindowsReservedComponent(c.name)
			if got != c.want {
				t.Errorf("IsWindowsReservedComponent(%q) = %v; want %v", c.name, got, c.want)
			}
		})
	}
}

func TestCheckNoReservedNames(t *testing.T) {
	t.Run("empty path", func(t *testing.T) {
		if err := CheckNoReservedNames(""); err != nil {
			t.Errorf("empty path: unexpected error: %v", err)
		}
	})

	t.Run("plain path", func(t *testing.T) {
		if err := CheckNoReservedNames("foo/bar/baz.txt"); err != nil {
			t.Errorf("plain path: unexpected error: %v", err)
		}
	})

	t.Run("absolute unix path", func(t *testing.T) {
		if err := CheckNoReservedNames("/tmp/dfmt/journal.jsonl"); err != nil {
			t.Errorf("abs unix path: unexpected error: %v", err)
		}
	})

	t.Run("windows drive letter not reserved", func(t *testing.T) {
		if err := CheckNoReservedNames("C:/Users/me/file.txt"); err != nil {
			t.Errorf("drive letter: unexpected error: %v", err)
		}
		if err := CheckNoReservedNames("D:\\Codebox\\file"); err != nil {
			t.Errorf("drive letter backslash: unexpected error: %v", err)
		}
	})

	t.Run("NUL component rejected", func(t *testing.T) {
		err := CheckNoReservedNames("NUL:/test.journal")
		if !errors.Is(err, ErrReservedName) {
			t.Errorf("NUL:/test.journal: got %v, want ErrReservedName", err)
		}
	})

	t.Run("nested NUL component rejected", func(t *testing.T) {
		err := CheckNoReservedNames("NUL:/nested/dir/test.journal")
		if !errors.Is(err, ErrReservedName) {
			t.Errorf("nested NUL: got %v, want ErrReservedName", err)
		}
	})

	t.Run("CON in middle rejected", func(t *testing.T) {
		err := CheckNoReservedNames("/tmp/CON/file.txt")
		if !errors.Is(err, ErrReservedName) {
			t.Errorf("middle CON: got %v, want ErrReservedName", err)
		}
	})

	t.Run("backslash separator", func(t *testing.T) {
		err := CheckNoReservedNames("foo\\NUL\\bar")
		if !errors.Is(err, ErrReservedName) {
			t.Errorf("backslash NUL: got %v, want ErrReservedName", err)
		}
	})

	t.Run("dot dot allowed", func(t *testing.T) {
		if err := CheckNoReservedNames("../foo/bar"); err != nil {
			t.Errorf("dot-dot path: unexpected error: %v", err)
		}
	})
}
