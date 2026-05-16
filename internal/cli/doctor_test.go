package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ersinkoc/dfmt/internal/osutil"
)

// TestSuggestToolchainDirsIn_Empty: with no candidates, no missing tools, or
// candidates that contain none of the wanted binaries, the result is empty.
func TestSuggestToolchainDirsIn_Empty(t *testing.T) {
	if got := suggestToolchainDirsIn(nil, nil); len(got) != 0 {
		t.Fatalf("nil/nil: want empty, got %v", got)
	}
	if got := suggestToolchainDirsIn([]string{"go"}, nil); len(got) != 0 {
		t.Fatalf("missing only: want empty, got %v", got)
	}
	if got := suggestToolchainDirsIn(nil, []string{t.TempDir()}); len(got) != 0 {
		t.Fatalf("candidates only: want empty, got %v", got)
	}
}

// TestSuggestToolchainDirsIn_HitsOneCandidate: a single candidate that
// contains one of the wanted binaries is returned. Binary name suffix is
// platform-dependent (.exe on Windows).
func TestSuggestToolchainDirsIn_HitsOneCandidate(t *testing.T) {
	dir := t.TempDir()
	binName := "go"
	if osutil.IsWindows() {
		binName = "go.exe"
	}
	if err := os.WriteFile(filepath.Join(dir, binName), []byte("#!fake\n"), 0o755); err != nil {
		t.Fatalf("write fake go: %v", err)
	}
	got := suggestToolchainDirsIn([]string{"go"}, []string{dir})
	if len(got) != 1 || got[0] != dir {
		t.Fatalf("want [%s], got %v", dir, got)
	}
}

// TestSuggestToolchainDirsIn_DedupsRepeatedCandidates: the candidate list
// may contain duplicates (e.g., /usr/bin appearing twice). The returned
// list must not.
func TestSuggestToolchainDirsIn_DedupsRepeatedCandidates(t *testing.T) {
	dir := t.TempDir()
	binName := "node"
	if osutil.IsWindows() {
		binName = "node.exe"
	}
	if err := os.WriteFile(filepath.Join(dir, binName), []byte("#!fake\n"), 0o755); err != nil {
		t.Fatalf("write fake node: %v", err)
	}
	got := suggestToolchainDirsIn([]string{"node"}, []string{dir, dir, dir})
	if len(got) != 1 || got[0] != dir {
		t.Fatalf("dedup failed: want [%s], got %v", dir, got)
	}
}

// TestSuggestToolchainDirsIn_DirectoryNotFile: a path whose target is a
// directory (not a regular file) must be skipped — fi.IsDir() guards
// against false positives where `<candidate>/go` is itself a dir.
func TestSuggestToolchainDirsIn_DirectoryNotFile(t *testing.T) {
	dir := t.TempDir()
	binName := "python"
	if osutil.IsWindows() {
		binName = "python.exe"
	}
	// Make the "binary" a directory.
	if err := os.MkdirAll(filepath.Join(dir, binName), 0o755); err != nil {
		t.Fatalf("mkdir fake: %v", err)
	}
	got := suggestToolchainDirsIn([]string{"python"}, []string{dir})
	if len(got) != 0 {
		t.Fatalf("dir-not-file: want empty, got %v", got)
	}
}

// TestSuggestToolchainDirsIn_MultipleCandidatesOrderPreserved: when several
// candidates each provide a wanted binary, the output preserves the input
// order.
func TestSuggestToolchainDirsIn_MultipleCandidatesOrderPreserved(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	dirEmpty := t.TempDir()
	binName := "go"
	if osutil.IsWindows() {
		binName = "go.exe"
	}
	for _, d := range []string{dirA, dirB} {
		if err := os.WriteFile(filepath.Join(d, binName), []byte("#!fake\n"), 0o755); err != nil {
			t.Fatalf("write fake: %v", err)
		}
	}
	// dirEmpty intentionally has no binary — must be filtered out.
	got := suggestToolchainDirsIn([]string{"go"}, []string{dirA, dirEmpty, dirB})
	want := []string{dirA, dirB}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("order: want %v, got %v", want, got)
	}
}

// TestSuggestToolchainDirsIn_MultipleToolsOneDirCountsOnce: if a single
// candidate dir contains multiple wanted binaries, the dir is still
// reported once (the inner loop breaks on first hit).
func TestSuggestToolchainDirsIn_MultipleToolsOneDirCountsOnce(t *testing.T) {
	dir := t.TempDir()
	for _, tool := range []string{"go", "node", "python"} {
		bin := tool
		if osutil.IsWindows() {
			bin += ".exe"
		}
		if err := os.WriteFile(filepath.Join(dir, bin), []byte("#!fake\n"), 0o755); err != nil {
			t.Fatalf("write fake %s: %v", tool, err)
		}
	}
	got := suggestToolchainDirsIn([]string{"go", "node", "python"}, []string{dir})
	if len(got) != 1 || got[0] != dir {
		t.Fatalf("multi-tool single-dir: want [%s], got %v", dir, got)
	}
}

// TestToolchainCandidateDirs_PlatformAppropriate: smoke-test the platform
// switch. On Windows we expect at least one C:\… path; on Unix we expect
// at least one POSIX absolute path. The list is non-empty on every
// supported GOOS so downstream callers never have to nil-guard it.
func TestToolchainCandidateDirs_PlatformAppropriate(t *testing.T) {
	dirs := toolchainCandidateDirs()
	if len(dirs) == 0 {
		t.Fatalf("toolchainCandidateDirs returned empty on %s", runtime.GOOS)
	}
	if osutil.IsWindows() {
		// Every base entry must look like a Windows absolute path.
		// LOCALAPPDATA-derived entries may not match this if the test
		// environment sets LOCALAPPDATA to a tempdir; check just the
		// first three baseline entries.
		for _, d := range dirs[:3] {
			if !strings.Contains(d, `\`) {
				t.Errorf("windows entry %q missing backslash", d)
			}
		}
	} else {
		for _, d := range dirs {
			if !strings.HasPrefix(d, "/") {
				t.Errorf("posix entry %q not absolute", d)
			}
		}
	}
}

// TestToolchainCandidateDirs_WindowsLocalAppDataPython: when LOCALAPPDATA
// points at a tree containing Programs/Python/Python3XX/ entries, those
// directories appear in the returned list. Skipped on non-Windows where
// the LOCALAPPDATA branch is not reached.
func TestToolchainCandidateDirs_WindowsLocalAppDataPython(t *testing.T) {
	if !osutil.IsWindows() {
		t.Skip("LOCALAPPDATA python probe is Windows-only")
	}
	tmp := t.TempDir()
	// Build the expected Programs\Python\Python3XX layout.
	pyRoot := filepath.Join(tmp, "Programs", "Python")
	wantDirs := []string{
		filepath.Join(pyRoot, "Python311"),
		filepath.Join(pyRoot, "Python312"),
	}
	for _, d := range wantDirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	// Also drop a non-matching entry to confirm the HasPrefix filter works.
	if err := os.MkdirAll(filepath.Join(pyRoot, "JuliaIsNotPython"), 0o755); err != nil {
		t.Fatalf("mkdir noise: %v", err)
	}

	t.Setenv("LOCALAPPDATA", tmp)
	dirs := toolchainCandidateDirs()

	have := map[string]bool{}
	for _, d := range dirs {
		have[d] = true
	}
	for _, want := range wantDirs {
		if !have[want] {
			t.Errorf("expected %s in candidates, got %v", want, dirs)
		}
	}
	if have[filepath.Join(pyRoot, "JuliaIsNotPython")] {
		t.Errorf("non-Python3 entry leaked into candidates: %v", dirs)
	}
}

// TestToolchainCandidateDirs_WindowsMissingLocalAppData: when
// LOCALAPPDATA is unset, the baseline three entries are still returned
// and the function does not panic on the missing env var.
func TestToolchainCandidateDirs_WindowsMissingLocalAppData(t *testing.T) {
	if !osutil.IsWindows() {
		t.Skip("LOCALAPPDATA branch is Windows-only")
	}
	t.Setenv("LOCALAPPDATA", "")
	dirs := toolchainCandidateDirs()
	if len(dirs) < 3 {
		t.Fatalf("baseline windows dirs: want >=3, got %v", dirs)
	}
}

// TestRunRemove_HelpFlag: --help returns 0 without performing any removal.
func TestRunRemove_HelpFlag(t *testing.T) {
	if code := runRemove([]string{"--help"}); code != 0 {
		t.Errorf("runRemove --help: want 0, got %d", code)
	}
}

// TestRunRemove_UnknownFlag: unknown flags return 2 (flag-parse error
// code), matching the rest of the CLI's flag-error convention.
func TestRunRemove_UnknownFlag(t *testing.T) {
	// Suppress stderr noise so the test log doesn't carry the flag-parse
	// error message. flag.ContinueOnError still routes the message
	// through fs.Output() — we don't intercept that here, just confirm
	// the exit code.
	if code := runRemove([]string{"--no-such-flag"}); code != 2 {
		t.Errorf("runRemove --no-such-flag: want 2, got %d", code)
	}
}

// TestRunRemove_RemovesDfmtDir: with a tempdir containing a populated
// .dfmt/, runRemove deletes the directory and exits 0. setup.RemoveProject
// is idempotent so the test is safe to re-run.
func TestRunRemove_RemovesDfmtDir(t *testing.T) {
	dir := t.TempDir()
	dfmt := filepath.Join(dir, ".dfmt")
	if err := os.MkdirAll(dfmt, 0o700); err != nil {
		t.Fatalf("mkdir .dfmt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dfmt, "journal.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write journal: %v", err)
	}

	if code := runRemove([]string{"--dir", dir}); code != 0 {
		t.Fatalf("runRemove: want 0, got %d", code)
	}
	if _, err := os.Stat(dfmt); !os.IsNotExist(err) {
		t.Fatalf(".dfmt should be gone, stat err = %v", err)
	}
}

// TestRunRemove_NoDfmtDir: removing a directory that never had .dfmt/ is
// a no-op success — the function is meant to be idempotent.
func TestRunRemove_NoDfmtDir(t *testing.T) {
	dir := t.TempDir()
	if code := runRemove([]string{"--dir", dir}); code != 0 {
		t.Fatalf("runRemove on clean dir: want 0, got %d", code)
	}
}

// TestRunStats_UnknownFlag: stats refuses unrecognized flags.
func TestRunStats_UnknownFlag(t *testing.T) {
	if code := runStats([]string{"--no-such-flag"}); code != 2 {
		t.Errorf("runStats --no-such-flag: want 2, got %d", code)
	}
}

// TestRunStats_PositionalArgs: stats refuses positional arguments.
// Pre-fix the function silently ignored them, so `dfmt stats foo`
// printed stats instead of erroring.
func TestRunStats_PositionalArgs(t *testing.T) {
	if code := runStats([]string{"foo"}); code != 2 {
		t.Errorf("runStats foo: want 2, got %d", code)
	}
}

// TestRunStats_HelpFlag: --help returns 0 without contacting the daemon.
func TestRunStats_HelpFlag(t *testing.T) {
	if code := runStats([]string{"--help"}); code != 0 {
		t.Errorf("runStats --help: want 0, got %d", code)
	}
}

// TestReadGlobalPortFile_NotExist: missing port file returns an error,
// not a panic. Caller treats this as "no daemon ever started".
func TestReadGlobalPortFile_NotExist(t *testing.T) {
	t.Setenv("DFMT_GLOBAL_DIR", t.TempDir())
	if _, _, err := readGlobalPortFile(); err == nil {
		t.Fatalf("missing port file: want error, got nil")
	}
}

// TestReadGlobalPortFile_Empty: a port file with only whitespace is
// treated as malformed — distinct from "no file at all" so the operator
// gets a different message.
func TestReadGlobalPortFile_Empty(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DFMT_GLOBAL_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "port"), []byte("   \n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, _, err := readGlobalPortFile()
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("want empty-port error, got %v", err)
	}
}

// TestReadGlobalPortFile_PlainPort: the legacy single-line format —
// just a port number — must keep working. token comes back as "".
func TestReadGlobalPortFile_PlainPort(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DFMT_GLOBAL_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "port"), []byte("12345\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	port, tok, err := readGlobalPortFile()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if port != 12345 {
		t.Errorf("port: want 12345, got %d", port)
	}
	if tok != "" {
		t.Errorf("token: want empty, got %q", tok)
	}
}

// TestReadGlobalPortFile_JSONFormat: the v0.6.x format is a JSON object
// with port + token. Both fields must come back to the caller.
func TestReadGlobalPortFile_JSONFormat(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DFMT_GLOBAL_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "port"),
		[]byte(`{"port":54321,"token":"abc123"}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	port, tok, err := readGlobalPortFile()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if port != 54321 {
		t.Errorf("port: want 54321, got %d", port)
	}
	if tok != "abc123" {
		t.Errorf("token: want abc123, got %q", tok)
	}
}

// TestReadGlobalPortFile_BadJSON: a JSON-looking body that doesn't parse
// surfaces a wrapped error rather than getting silently turned into
// port=0.
func TestReadGlobalPortFile_BadJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DFMT_GLOBAL_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "port"), []byte("{not-json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, _, err := readGlobalPortFile()
	if err == nil || !strings.Contains(err.Error(), "parse port file") {
		t.Fatalf("want parse-error, got %v", err)
	}
}

// TestReadGlobalPortFile_NonNumericPlain: a body that's neither JSON nor
// a parseable integer should fail loudly.
func TestReadGlobalPortFile_NonNumericPlain(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DFMT_GLOBAL_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "port"), []byte("not-a-port"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, _, err := readGlobalPortFile()
	if err == nil {
		t.Fatalf("want error on non-numeric port body, got nil")
	}
}

// TestReadHookStdin_HappyPath: a well-formed Claude Code hook payload
// decodes to its HookStdinInput shape. stdin is rebound to a pipe for
// the duration of the call so the test doesn't block on the real stdin.
func TestReadHookStdin_HappyPath(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = orig
		_ = r.Close()
	})

	go func() {
		_, _ = w.Write([]byte(`{"tool_name":"Bash","tool_input":{"command":"ls"}}`))
		_ = w.Close()
	}()

	got, err := readHookStdin()
	if err != nil {
		t.Fatalf("readHookStdin: %v", err)
	}
	if got.ToolName != "Bash" {
		t.Errorf("ToolName: want Bash, got %q", got.ToolName)
	}
	if got.ToolInput["command"] != "ls" {
		t.Errorf("ToolInput.command: want ls, got %v", got.ToolInput["command"])
	}
}

// TestReadHookStdin_MalformedJSON: a garbage payload surfaces the
// decoder error to the caller. runHook turns this into a "no
// redirect" response, but readHookStdin itself must report the error.
func TestReadHookStdin_MalformedJSON(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = orig
		_ = r.Close()
	})

	go func() {
		_, _ = w.Write([]byte(`{not-json`))
		_ = w.Close()
	}()

	if _, err := readHookStdin(); err == nil {
		t.Fatalf("malformed json: want error, got nil")
	}
}

// TestSuggestToolchainDirs_DelegatesToHelper: the public wrapper should
// invoke the In-helper with the platform candidate list. Verify the
// wrapper itself runs (it's a one-liner but the coverage tool counts
// the call). We use a tool name guaranteed to exist nowhere so the
// output is empty either way — the goal is line coverage, not behavior.
func TestSuggestToolchainDirs_DelegatesToHelper(t *testing.T) {
	got := suggestToolchainDirs([]string{"definitely-not-a-real-toolchain-xyz"})
	if len(got) != 0 {
		t.Errorf("want empty result for fake tool, got %v", got)
	}
}

// silence unused import on platforms where strings doesn't get hit.
var _ = strings.HasPrefix
