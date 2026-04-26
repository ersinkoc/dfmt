package setup

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestManifestAddFileInsertsNewEntry(t *testing.T) {
	m := &Manifest{}
	m.AddFile(FileEntry{Path: "/home/u/.claude/mcp.json", Agent: "claude-code", Version: "1"})

	if len(m.Files) != 1 {
		t.Fatalf("len(Files) = %d, want 1", len(m.Files))
	}
	if m.Files[0].Agent != "claude-code" {
		t.Errorf("Agent = %q, want claude-code", m.Files[0].Agent)
	}
}

func TestManifestAddFileUpsertsByPath(t *testing.T) {
	m := &Manifest{
		Files: []FileEntry{
			{Path: "/home/u/.claude/mcp.json", Agent: "claude-code", Version: "0"},
			{Path: "/home/u/.codex/mcp.json", Agent: "codex", Version: "1"},
		},
	}
	m.AddFile(FileEntry{Path: "/home/u/.claude/mcp.json", Agent: "claude-code", Version: "2"})

	if len(m.Files) != 2 {
		t.Fatalf("len(Files) = %d, want 2 (no duplicate)", len(m.Files))
	}
	if m.Files[0].Version != "2" {
		t.Errorf("Version = %q, want 2 (replaced)", m.Files[0].Version)
	}
	if m.Files[1].Path != "/home/u/.codex/mcp.json" {
		t.Errorf("unrelated entry mutated: Path = %q", m.Files[1].Path)
	}
}

func TestManifestAddFileAppendsDistinctPaths(t *testing.T) {
	m := &Manifest{}
	m.AddFile(FileEntry{Path: "/a", Agent: "x", Version: "1"})
	m.AddFile(FileEntry{Path: "/b", Agent: "y", Version: "1"})

	if len(m.Files) != 2 {
		t.Fatalf("len(Files) = %d, want 2", len(m.Files))
	}
}

func TestManifestAddFileWindowsCaseInsensitive(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("case-insensitive path collision is Windows-only")
	}
	m := &Manifest{
		Files: []FileEntry{
			{Path: `C:\Users\u\.claude\mcp.json`, Agent: "claude-code", Version: "1"},
		},
	}
	// Same path, different case -- should upsert, not append.
	m.AddFile(FileEntry{Path: `c:\users\u\.claude\mcp.json`, Agent: "claude-code", Version: "2"})

	if len(m.Files) != 1 {
		t.Fatalf("len(Files) = %d, want 1 (case variants must collapse on Windows)", len(m.Files))
	}
	if m.Files[0].Version != "2" {
		t.Errorf("Version = %q, want 2", m.Files[0].Version)
	}
}

func TestLoadManifestDedupsLegacyDuplicates(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmp)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", tmp)
	} else {
		t.Setenv("HOME", tmp)
	}

	dup := []FileEntry{}
	for i := 0; i < 5; i++ {
		dup = append(dup,
			FileEntry{Path: "/a/mcp.json", Agent: "claude-code", Version: "1"},
			FileEntry{Path: "/b/mcp.json", Agent: "codex", Version: "1"},
		)
	}
	if err := SaveManifest(&Manifest{Version: 1, Files: dup}); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}

	m, err := LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(m.Files) != 2 {
		t.Fatalf("len(Files) = %d, want 2 after dedup; got: %+v", len(m.Files), m.Files)
	}
	seen := map[string]bool{}
	for _, f := range m.Files {
		if seen[f.Path] {
			t.Errorf("duplicate Path after load-dedup: %s", f.Path)
		}
		seen[f.Path] = true
	}
}

// Sanity check: ManifestPath honors XDG_DATA_HOME so the dedup test above
// writes/reads the same file. (Guarding against a future refactor.)
func TestManifestPathRespectsXDG(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmp)
	got := ManifestPath()
	want := filepath.Join(tmp, "dfmt", "setup-manifest.json")
	if got != want {
		t.Errorf("ManifestPath = %q, want %q", got, want)
	}
}
