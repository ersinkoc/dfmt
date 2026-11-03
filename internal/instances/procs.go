package instances

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Snapshot is the process table as observed at one moment.
type Snapshot struct {
	// Procs are the dfmt-looking processes.
	Procs []Proc
	// parent maps every visible PID to its parent, dfmt or not.
	parent map[int]int
}

// AncestorsOf returns the PIDs above pid in the real process tree.
func (s Snapshot) AncestorsOf(pid int) map[int]bool {
	out := map[int]bool{}
	for p, ok := s.parent[pid]; ok && p > 0 && !out[p]; p, ok = s.parent[p] {
		out[p] = true
	}
	return out
}

// List snapshots the processes of this host that look like dfmt, verifying
// each image's identity so an unrelated program named dfmt is excluded.
func List() (Snapshot, error) {
	switch runtime.GOOS {
	case "linux":
		return listProc("/proc")
	case "windows":
		return listWindows()
	default:
		return listPS()
	}
}

// looksLikeDFMTName is deliberately loose: it only nominates candidates.
// IsDFMT, set from the image's contents, is what authorizes action.
func looksLikeDFMTName(exe string) bool {
	base := strings.ToLower(filepath.Base(strings.ReplaceAll(exe, `\`, "/")))
	return strings.HasPrefix(base, "dfmt") && !strings.HasPrefix(base, "dfmt-bench")
}

// listProc reads a /proc tree. root is a parameter so tests can fake one.
func listProc(root string) (Snapshot, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{parent: map[int]int{}}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		dir := filepath.Join(root, e.Name())
		stat, err := os.ReadFile(filepath.Join(dir, "stat"))
		if err != nil {
			continue
		}
		ppid := parseStatPPID(string(stat))
		snap.parent[pid] = ppid

		link, err := os.Readlink(filepath.Join(dir, "exe"))
		if err != nil { // kernel thread, or another user's process
			continue
		}
		deleted := strings.HasSuffix(link, " (deleted)")
		exe := strings.TrimSuffix(link, " (deleted)")
		if !looksLikeDFMTName(exe) {
			continue
		}
		p := Proc{PID: pid, PPID: ppid, Exe: exe, Deleted: deleted}
		if raw, err := os.ReadFile(filepath.Join(dir, "cmdline")); err == nil {
			for _, a := range bytes.Split(bytes.TrimRight(raw, "\x00"), []byte{0}) {
				p.Args = append(p.Args, string(a))
			}
		}
		// Opening <pid>/exe reads the image even after its path was replaced.
		p.IsDFMT = IsDFMTFile(filepath.Join(dir, "exe"))
		snap.Procs = append(snap.Procs, p)
	}
	return snap, nil
}

// parseStatPPID extracts the parent PID from /proc/<pid>/stat. The command
// name is parenthesised and may itself contain spaces and parentheses, so
// the fields are counted from the LAST ')'.
func parseStatPPID(stat string) int {
	i := strings.LastIndexByte(stat, ')')
	if i < 0 {
		return 0
	}
	f := strings.Fields(stat[i+1:])
	if len(f) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(f[1])
	return n
}

// listPS covers macOS and the BSDs, which have no /proc. The executable is
// the first word of args, so a path containing spaces is mis-split; such a
// process simply fails verification and is left alone.
func listPS() (Snapshot, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=,args=").Output()
	if err != nil {
		return Snapshot{}, fmt.Errorf("ps: %w", err)
	}
	return parsePS(string(out)), nil
}

func parsePS(out string) Snapshot {
	snap := Snapshot{parent: map[int]int{}}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		pid, e1 := strconv.Atoi(f[0])
		ppid, e2 := strconv.Atoi(f[1])
		if e1 != nil || e2 != nil {
			continue
		}
		snap.parent[pid] = ppid
		exe := f[2]
		if !looksLikeDFMTName(exe) {
			continue
		}
		if !filepath.IsAbs(exe) {
			if lp, err := exec.LookPath(exe); err == nil {
				exe, _ = filepath.Abs(lp)
			}
		}
		p := Proc{PID: pid, PPID: ppid, Exe: exe, Args: f[2:]}
		p.IsDFMT = filepath.IsAbs(exe) && IsDFMTFile(exe)
		snap.Procs = append(snap.Procs, p)
	}
	return snap
}

const windowsListScript = `Get-CimInstance Win32_Process | ForEach-Object { '{0}|{1}|{2}|{3}|{4}' -f ` +
	`$_.ProcessId,$_.ParentProcessId,$_.Name,$_.ExecutablePath,$_.CommandLine }`

func listWindows() (Snapshot, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive",
		"-Command", windowsListScript).Output()
	if err != nil {
		return Snapshot{}, fmt.Errorf("powershell: %w", err)
	}
	return parseWindows(string(out)), nil
}

// parseWindows reads `pid|ppid|name|exe|cmdline` lines. The command line is
// last because it may contain '|'.
func parseWindows(out string) Snapshot {
	snap := Snapshot{parent: map[int]int{}}
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(strings.TrimSpace(line), "|", 5)
		if len(f) < 4 {
			continue
		}
		pid, e1 := strconv.Atoi(f[0])
		ppid, e2 := strconv.Atoi(f[1])
		if e1 != nil || e2 != nil {
			continue
		}
		snap.parent[pid] = ppid
		if !looksLikeDFMTName(f[2]) || f[3] == "" {
			continue
		}
		p := Proc{PID: pid, PPID: ppid, Exe: f[3]}
		if len(f) == 5 {
			p.Args = splitCommandLine(f[4])
		}
		if len(p.Args) == 0 {
			p.Args = []string{f[3]}
		}
		p.IsDFMT = IsDFMTFile(f[3])
		snap.Procs = append(snap.Procs, p)
	}
	return snap
}

// splitCommandLine splits on spaces, honouring double quotes. Only the
// subcommand word matters here, so no escape handling is attempted.
func splitCommandLine(s string) []string {
	var out []string
	var cur strings.Builder
	inQ := false
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case r == '"':
			inQ = !inQ
		case r == ' ' && !inQ:
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}
