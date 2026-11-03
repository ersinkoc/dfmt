package instances

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const keep = "/home/u/.local/bin/dfmt"

func proc(pid, ppid int, exe string, args ...string) Proc {
	return Proc{PID: pid, PPID: ppid, Exe: exe, Args: append([]string{exe}, args...), IsDFMT: true}
}

func killedPIDs(p Plan) map[int]string {
	m := map[int]string{}
	for _, v := range p.Kill {
		m[v.Proc.PID] = v.Reason
	}
	return m
}

func TestBuildPlanKillsStaleAndKeepsCurrent(t *testing.T) {
	replaced := proc(10, 1, keep, "daemon", "--global")
	replaced.Deleted = true
	in := Input{
		Keep: keep, Self: 99, DaemonPID: 12,
		Procs: []Proc{
			replaced, // old inode, same path
			proc(11, 1, "/usr/local/bin/dfmt", "mcp"), // other path
			proc(12, 1, keep, "daemon", "--global"),   // the real daemon
			proc(13, 1, keep, "mcp"),                  // an agent's proxy
			proc(14, 1, keep, "mcp"),                  // another agent's proxy
			proc(99, 98, keep, "cleanup"),             // self
		},
	}
	plan := BuildPlan(in)
	got := killedPIDs(plan)
	if len(got) != 2 || got[10] == "" || got[11] == "" {
		t.Fatalf("kill set = %v, want exactly pids 10 and 11", got)
	}
	if plan.Healthy["mcp"] != 2 || plan.Healthy["daemon"] != 1 {
		t.Errorf("healthy = %v, want 2 mcp and 1 daemon", plan.Healthy)
	}
}

func TestBuildPlanIgnoresForeignDfmt(t *testing.T) {
	foreign := proc(20, 1, "/usr/bin/dfmt") // the D formatter
	foreign.IsDFMT = false
	plan := BuildPlan(Input{Keep: keep, Self: 99, Procs: []Proc{foreign}})
	if len(plan.Kill) != 0 {
		t.Fatalf("killed a program that is not dfmt: %v", plan.Kill)
	}
}

func TestBuildPlanDuplicateDaemons(t *testing.T) {
	d1 := proc(30, 1, keep, "daemon")
	d2 := proc(31, 1, keep, "daemon")
	d3 := proc(32, 1, keep, "daemon")

	plan := BuildPlan(Input{Keep: keep, Self: 99, DaemonPID: 31, Procs: []Proc{d1, d2, d3}})
	got := killedPIDs(plan)
	if len(got) != 2 || got[30] == "" || got[32] == "" || got[31] != "" {
		t.Fatalf("kill set = %v, want 30 and 32 only", got)
	}
	if plan.Healthy["daemon"] != 1 {
		t.Errorf("healthy daemons = %d, want 1", plan.Healthy["daemon"])
	}

	// No daemon matches daemon.json: it may just have lost its files, so a
	// guess here would kill the live one. Nothing dies, the user is told.
	plan = BuildPlan(Input{Keep: keep, Self: 99, DaemonPID: 0, Procs: []Proc{d1, d2}})
	if len(plan.Kill) != 0 || len(plan.Warnings) == 0 {
		t.Fatalf("unnamed duplicates: kill=%v warnings=%v, want no kill and a warning", plan.Kill, plan.Warnings)
	}
}

// dfmt <- bash <- daemon: the shell between them is not a dfmt process, so
// the chain must come from the real tree or a stale daemon hosting this very
// command would be killed out from under it.
func TestBuildPlanNeverKillsAncestors(t *testing.T) {
	stale := proc(40, 1, "/old/dfmt", "daemon")
	snap := Snapshot{parent: map[int]int{99: 50, 50: 40, 40: 1}}
	plan := BuildPlan(Input{
		Keep: keep, Self: 99, Procs: []Proc{stale},
		Ancestors: snap.AncestorsOf(99),
	})
	if len(plan.Kill) != 0 {
		t.Fatalf("killed an ancestor: %v", plan.Kill)
	}
	if len(plan.Warnings) != 1 || !strings.Contains(plan.Warnings[0], "ancestor") {
		t.Errorf("warnings = %v, want one ancestor warning", plan.Warnings)
	}
}

func TestBuildPlanCaseInsensitiveWindows(t *testing.T) {
	k := `C:\Users\u\AppData\Local\Programs\dfmt\dfmt.exe`
	p := proc(60, 1, strings.ToLower(k), "daemon")
	plan := BuildPlan(Input{Keep: k, Self: 99, Procs: []Proc{p}, CaseInsensitive: true})
	if len(plan.Kill) != 0 {
		t.Fatalf("same file under a different case was treated as stale: %v", plan.Kill)
	}
}

func TestBuildPlanRemovesCopiesNotKeep(t *testing.T) {
	plan := BuildPlan(Input{Keep: keep, Self: 99, Copies: []Copy{
		{Path: "/z/dfmt", Reason: "r"}, {Path: keep, Reason: "must not happen"}, {Path: "/a/dfmt", Reason: "r"},
	}})
	if len(plan.Remove) != 2 || plan.Remove[0].Path != "/a/dfmt" || plan.Remove[1].Path != "/z/dfmt" {
		t.Fatalf("remove = %v", plan.Remove)
	}
}

func TestRole(t *testing.T) {
	for args, want := range map[string]string{
		"daemon --global --foreground": "daemon",
		"--project /x mcp":             "mcp",
		"status":                       "cli",
		"":                             "cli",
	} {
		p := Proc{Args: append([]string{"dfmt"}, strings.Fields(args)...)}
		if got := p.Role(); got != want {
			t.Errorf("Role(%q) = %s, want %s", args, got, want)
		}
	}
	if (Proc{}).Role() != "cli" {
		t.Error("a process with no args must not panic")
	}
}

func TestContainsSignatureAcrossChunkBoundary(t *testing.T) {
	// Put the marker astride the 1 MiB read boundary.
	pad := bytes.Repeat([]byte{'x'}, (1<<20)-5)
	data := append(append(pad, signature...), bytes.Repeat([]byte{'y'}, 100)...)
	if !containsSignature(bytes.NewReader(data)) {
		t.Fatal("marker split across a read boundary was missed")
	}
	if containsSignature(bytes.NewReader(bytes.Repeat([]byte{'x'}, 3<<20))) {
		t.Fatal("found a marker in data without one")
	}
}

func TestFindCopies(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	other := filepath.Join(root, "other")
	for _, d := range []string{bin, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	good := "ELF...path\tgithub.com/ersinkoc/dfmt/cmd/dfmt..."
	write(filepath.Join(bin, "dfmt"), good)
	write(filepath.Join(other, "dfmt"), good)                                                    // stray copy
	write(filepath.Join(other, "dfmt.exe.old"), "corrupt "+good[:20]+"github.com/ersinkoc/dfmt") // broken leftover
	write(filepath.Join(bin, ".dfmt-update-123"), good)                                          // interrupted update
	if err := os.MkdirAll(filepath.Join(root, "dd"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(root, "dd", "dfmt"), "the D formatter, not ours") // different program

	// A checkout: build output inside a go.mod tree is the developer's.
	src := filepath.Join(root, "src", "dist")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(root, "src", "go.mod"), "module x")
	write(filepath.Join(src, "dfmt"), good)

	t.Setenv("PATH", strings.Join([]string{bin, other, filepath.Join(root, "dd"), src}, string(os.PathListSeparator)))
	t.Setenv("HOME", filepath.Join(root, "nohome"))

	got := map[string]bool{}
	for _, c := range FindCopies(filepath.Join(bin, "dfmt")) {
		got[c.Path] = true
	}
	want := []string{
		filepath.Join(other, "dfmt"),
		filepath.Join(other, "dfmt.exe.old"),
		filepath.Join(bin, ".dfmt-update-123"),
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing copy %s; got %v", w, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d copies %v, want exactly %v (keep, the D formatter and dist/ must be left alone)", len(got), got, want)
	}
}

func TestApplyContinuesPastFailures(t *testing.T) {
	plan := Plan{
		Kill:   []Victim{{Proc: proc(1, 0, "/x/dfmt", "daemon"), Reason: "r"}, {Proc: proc(2, 0, "/x/dfmt", "mcp"), Reason: "r"}},
		Remove: []Removal{{Path: "/a"}, {Path: "/b"}},
	}
	var killed []int
	var removed []string
	res := plan.Apply(Actions{
		Terminate: func(pid int) error {
			if pid == 1 {
				return errors.New("denied")
			}
			killed = append(killed, pid)
			return nil
		},
		Remove: func(p string) error { removed = append(removed, p); return nil },
	})
	if len(res.Failed) != 1 || len(res.Done) != 3 {
		t.Fatalf("done=%v failed=%v", res.Done, res.Failed)
	}
	if len(killed) != 1 || len(removed) != 2 {
		t.Fatalf("killed=%v removed=%v: one failure must not stop the rest", killed, removed)
	}
}

func TestRemoveFileMissingIsFine(t *testing.T) {
	if err := removeFile(filepath.Join(t.TempDir(), "nope")); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "dfmt")
	if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := removeFile(p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("file still present")
	}
}

func TestParseStatPPID(t *testing.T) {
	// The comm field can contain spaces and ')'; counting from the last ')'
	// is the only reliable parse.
	if got := parseStatPPID("123 (we ird) name) S 77 123 123"); got != 77 {
		t.Errorf("ppid = %d, want 77", got)
	}
	if parseStatPPID("garbage") != 0 {
		t.Error("malformed stat must give 0")
	}
}

func TestListProcFakeTree(t *testing.T) {
	root := t.TempDir()
	self := filepath.Join(root, "dfmtbin")
	if err := os.WriteFile(self, []byte("x github.com/ersinkoc/dfmt x"), 0o755); err != nil {
		t.Fatal(err)
	}
	mk := func(pid, ppid, comm string, exe string, cmd string) {
		d := filepath.Join(root, pid)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(d, "stat"), []byte(pid+" ("+comm+") S "+ppid+" 1"), 0o644)
		if exe != "" {
			if err := os.Symlink(exe, filepath.Join(d, "exe")); err != nil {
				t.Skip("symlinks unavailable")
			}
		}
		_ = os.WriteFile(filepath.Join(d, "cmdline"), []byte(cmd), 0o644)
	}
	mk("200", "1", "bash", "/bin/bash", "bash\x00")
	mk("201", "200", "dfmt", self+" (deleted)", "/x/dfmt\x00daemon\x00--global\x00")
	mk("notapid", "1", "x", "", "")

	snap, err := listProc(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Procs) != 1 {
		t.Fatalf("procs = %+v, want only the dfmt one", snap.Procs)
	}
	p := snap.Procs[0]
	if p.PID != 201 || p.PPID != 200 || !p.Deleted || p.Role() != "daemon" {
		t.Errorf("proc = %+v", p)
	}
	if !snap.AncestorsOf(201)[200] || !snap.AncestorsOf(201)[1] {
		t.Errorf("ancestors = %v, want 200 (a non-dfmt shell) and 1", snap.AncestorsOf(201))
	}
}

func TestParsePS(t *testing.T) {
	snap := parsePS("  1     0 /sbin/launchd\n 50 1 /nonexistent/dfmt mcp\n junk\n")
	if len(snap.Procs) != 1 || snap.Procs[0].IsDFMT {
		t.Fatalf("procs = %+v: unverifiable image must not be IsDFMT", snap.Procs)
	}
	if snap.parent[50] != 1 {
		t.Error("parent map missing pid 50")
	}
}

func TestParseWindows(t *testing.T) {
	out := "4|0|System|\n" +
		"77|4|explorer.exe|C:\\Windows\\explorer.exe|C:\\Windows\\explorer.exe\n" +
		"88|77|dfmt.exe|C:\\nope\\dfmt.exe|\"C:\\nope\\dfmt.exe\" daemon --global|x\n"
	snap := parseWindows(out)
	if len(snap.Procs) != 1 {
		t.Fatalf("procs = %+v", snap.Procs)
	}
	p := snap.Procs[0]
	if p.PID != 88 || p.PPID != 77 || p.Role() != "daemon" || p.IsDFMT {
		t.Errorf("proc = %+v", p)
	}
	if !snap.AncestorsOf(88)[77] {
		t.Error("ancestor chain missing explorer")
	}
}
