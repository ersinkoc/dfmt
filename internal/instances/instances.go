// Package instances finds and retires stray dfmt processes and binaries so a
// host ends up with exactly one installed dfmt and at most one daemon.
//
// The planner (BuildPlan) is a pure function over observed state; listing
// processes, finding copies and acting on the plan live beside it so each can
// be tested without touching the real machine. ADR-0034.
package instances

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Proc is one running process that looks like dfmt.
type Proc struct {
	PID  int
	PPID int
	// Exe is the executable path as the OS reports it, with any Linux
	// " (deleted)" suffix already stripped and recorded in Deleted.
	Exe     string
	Args    []string // Args[0] is the program, as launched
	Deleted bool     // the file this process runs was replaced or removed
	// IsDFMT is true only when the executable image carries dfmt's module
	// path. A different program that happens to be called dfmt (the D
	// formatter ships a dfmt binary) must never be touched.
	IsDFMT bool
}

// Role classifies the process by its subcommand: "daemon", "mcp" or "cli".
func (p Proc) Role() string {
	skip := false
	for _, a := range p.Args[min(1, len(p.Args)):] {
		if skip { // the value of a space-separated global flag
			skip = false
			continue
		}
		if a == "--project" || a == "-project" {
			skip = true
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		switch a {
		case "daemon":
			return "daemon"
		case "mcp":
			return "mcp"
		}
		return "cli"
	}
	return "cli"
}

// Input is everything BuildPlan needs; none of it is read from the machine.
type Input struct {
	// Keep is the one binary that stays: the installed, current dfmt.
	Keep string
	// Procs are the dfmt-looking processes found.
	Procs []Proc
	// Copies are verified dfmt binaries other than Keep (see FindCopies).
	Copies []Copy
	// Self is this process; it and its ancestors are never killed.
	Self int
	// Ancestors are the PIDs above Self in the REAL process tree (shells
	// included). Deriving them from Procs alone would break the chain at
	// the first non-dfmt parent: dfmt <- bash <- daemon.
	Ancestors map[int]bool
	// DaemonPID is the daemon named by ~/.dfmt/daemon.json, 0 if unknown.
	DaemonPID int
	// CaseInsensitive compares paths ignoring case (Windows).
	CaseInsensitive bool
}

// Victim is a process the plan will end.
type Victim struct {
	Proc   Proc
	Reason string
}

// Removal is a file the plan will delete.
type Removal struct {
	Path   string
	Reason string
}

// Plan is the set of actions that leaves one binary and one daemon.
type Plan struct {
	Keep     string
	Kill     []Victim
	Remove   []Removal
	Warnings []string
	// Healthy counts the current-binary processes left alone, by role.
	Healthy map[string]int
}

// Empty reports whether there is nothing to do.
func (p Plan) Empty() bool { return len(p.Kill) == 0 && len(p.Remove) == 0 }

// BuildPlan decides what to kill and delete. Rules:
//
//   - A dfmt process not running Keep (other path, or its file was replaced
//     or deleted) is stale and is killed.
//   - Of several daemons running Keep, only the one named by the identity
//     file survives. If none is named, nothing is killed: that daemon may
//     merely have lost its rendezvous files, and it repairs them itself.
//   - `dfmt mcp` proxies on Keep are the agents' own sessions and stay.
//   - This process and its ancestors are never killed; a stale ancestor is
//     reported instead (typically `dfmt cleanup` run through the daemon).
//   - Copies are removed.
func BuildPlan(in Input) Plan {
	plan := Plan{Keep: in.Keep, Healthy: map[string]int{}}
	same := func(a, b string) bool {
		if in.CaseInsensitive {
			return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
		}
		return filepath.Clean(a) == filepath.Clean(b)
	}

	protected := map[int]bool{in.Self: true}
	for pid := range in.Ancestors {
		protected[pid] = true
	}

	var daemons []Proc
	for _, p := range in.Procs {
		if !p.IsDFMT || p.PID == in.Self {
			continue
		}
		stale, reason := false, ""
		switch {
		case p.Deleted:
			stale, reason = true, "its binary was replaced or deleted while it ran"
		case !same(p.Exe, in.Keep):
			stale, reason = true, fmt.Sprintf("runs %s, not %s", p.Exe, in.Keep)
		}
		if stale {
			if protected[p.PID] {
				plan.Warnings = append(plan.Warnings, fmt.Sprintf(
					"pid %d (%s) is stale but is an ancestor of this command; rerun `dfmt cleanup` from a normal shell",
					p.PID, p.Role()))
				continue
			}
			plan.Kill = append(plan.Kill, Victim{Proc: p, Reason: reason})
			continue
		}
		plan.Healthy[p.Role()]++
		if p.Role() == "daemon" {
			daemons = append(daemons, p)
		}
	}

	if len(daemons) > 1 {
		named := false
		for _, d := range daemons {
			if d.PID == in.DaemonPID {
				named = true
			}
		}
		if !named {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf(
				"%d daemons run %s but none matches daemon.json; leaving them (run `dfmt stop`, then any dfmt command)",
				len(daemons), in.Keep))
		} else {
			for _, d := range daemons {
				if d.PID == in.DaemonPID {
					continue
				}
				if protected[d.PID] {
					plan.Warnings = append(plan.Warnings, fmt.Sprintf(
						"duplicate daemon pid %d hosts this command; rerun `dfmt cleanup` from a normal shell", d.PID))
					continue
				}
				plan.Healthy["daemon"]--
				plan.Kill = append(plan.Kill, Victim{Proc: d, Reason: fmt.Sprintf(
					"duplicate daemon; pid %d holds the singleton", in.DaemonPID)})
			}
		}
	}

	for _, c := range in.Copies {
		if same(c.Path, in.Keep) {
			continue
		}
		plan.Remove = append(plan.Remove, Removal{Path: c.Path, Reason: c.Reason})
	}

	sort.Slice(plan.Kill, func(i, j int) bool { return plan.Kill[i].Proc.PID < plan.Kill[j].Proc.PID })
	sort.Slice(plan.Remove, func(i, j int) bool { return plan.Remove[i].Path < plan.Remove[j].Path })
	return plan
}
