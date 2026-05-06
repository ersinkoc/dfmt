package setup

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/ersinkoc/dfmt/internal/safefs"
)

// LegacyKind classifies what kind of fossil entry was found in a Claude
// settings.json. Each kind has its own detection and purge logic; callers
// should not switch on the string form -- exported only for diagnostics.
type LegacyKind string

const (
	// LegacyDenyTriplet: pre-v0.3.1 dfmt versions injected
	// permissions.deny = ["Bash", "WebFetch", "WebSearch"]. v0.3.1 stopped
	// writing this; the cleanup heuristic only strips the exact triplet so
	// a user who added one of those three on their own keeps their entry.
	LegacyDenyTriplet LegacyKind = "deny_triplet"

	// LegacyDottedMCP: pre-v0.2.1 the MCP tool names were dotted
	// (mcp__dfmt__dfmt.exec). Claude Code's MCP regex
	// ^[a-zA-Z][a-zA-Z0-9_-]*$ rejects these so they're dead permission
	// strings -- but they still clutter the file and confuse readers.
	LegacyDottedMCP LegacyKind = "dotted_mcp_perm"

	// LegacyHookCmdDrift: a PreToolUse / PreCompact / SessionStart hook
	// whose command starts with "dfmt" but doesn't equal the current
	// canonical form (typically because the user moved the binary or
	// changed install layout). Detection requires a path resolution to
	// avoid false positives -- so callers pass the freshly resolved
	// dfmt command via the resolveDFMTCommand parameter.
	LegacyHookCmdDrift LegacyKind = "hook_cmd_drift"

	// LegacyMatcherSubset: a PreToolUse hook with a dfmt-shaped command
	// but a matcher regex that's a strict subset of the current set
	// (Bash|Read|WebFetch|Grep|Task|Edit|Write|Glob). Pre-v0.x dfmt
	// versions registered narrower matchers; on upgrade those need to
	// be expanded so the hook fires for every tool dfmt now handles.
	LegacyMatcherSubset LegacyKind = "matcher_subset"

	// LegacyOrphanBackup: an `<path>.dfmt.bak` / `.dfmt.preclean.bak` /
	// `.dfmt-unpatch-*` artifact left by an aborted past run. These are
	// flagged but NOT auto-deleted -- a user might still want them to
	// restore from. Callers can choose to remove them after confirming.
	LegacyOrphanBackup LegacyKind = "orphan_backup"
)

// LegacyEntry describes one fossil that PurgeLegacyClaudeSettings will
// either remove (Removed) or rewrite in place (Adjusted, e.g. matcher
// expansion).
type LegacyEntry struct {
	Kind     LegacyKind
	Path     string // file path the entry lives in
	Location string // human-readable JSON path: "permissions.deny", "hooks.PreToolUse[0].hooks[0].command"
	Detail   string // short description suitable for logging / UI
}

// PurgeReport summarizes a PurgeLegacyClaudeSettings run.
type PurgeReport struct {
	// Removed: entries deleted outright (deny triplet, dotted MCP perms,
	// hook entries with a drifted command, orphan backups deleted).
	Removed []LegacyEntry

	// Adjusted: hook entries kept but rewritten (matcher set expansion,
	// command repointed to current resolved path).
	Adjusted []LegacyEntry

	// Skipped: entries detected but left in place because removing them
	// might destroy user data (e.g. a deny entry with extra user-added
	// names alongside the triplet).
	Skipped []LegacyEntry

	// Backup: path of the one-shot `<path>.dfmt.bak` written before any
	// purge. Empty if no backup was needed (no changes were made) or if
	// the backup already existed and was preserved.
	Backup string
}

// hookEventPreToolUse is the JSON key for the native-tool intercept hook
// event. Its name appears in legacy detail strings and matcher checks; a
// constant keeps lint quiet (goconst) and centralizes the spelling.
const hookEventPreToolUse = "PreToolUse"

// CurrentPreToolUseMatchers is the canonical list of native tool names
// dfmt's PreToolUse hook handles in the current release. Hooks whose
// matcher regex is a strict subset get expanded by the purger.
//
// Keep this in sync with mergeClaudeHookWithMatcher's matcher arg in
// projectinit.go::writeProjectClaudeSettings and
// claude.go::WriteClaudeCodeSettingsHook -- those are the writers; this
// list is the spec the purger compares against.
var CurrentPreToolUseMatchers = []string{
	"Bash", "Read", "WebFetch", "Grep", "Task", "Edit", "Write", "Glob",
}

// dottedMCPPermPattern matches the legacy "mcp__dfmt__dfmt.exec"-style
// permission strings. The new form is mcp__dfmt__dfmt_<word>.
var dottedMCPPermPattern = regexp.MustCompile(`^mcp__dfmt__dfmt\.[a-z]+$`)

// dfmtHookCmdPattern detects whether a hook command was likely written by
// dfmt itself (any command beginning with the dfmt binary name, with or
// without a path, followed by an argv that looks like a dfmt subcommand).
// We do NOT remove hooks that simply mention "dfmt" in a free-form way;
// the regex requires the dfmt binary to be the leading executable token.
var dfmtHookCmdPattern = regexp.MustCompile(
	`(?i)(^|[/\\])(dfmt|dfmt\.exe)(\s+(hook|recall|capture|exec|read|fetch))`,
)

// CleanLegacyClaudeSettings parses a Claude settings.json and reports
// every fossil it finds, without writing. Returns an empty slice when
// the file is missing or empty (a missing file is not an error).
//
// resolveDFMTCommand is the value the purger should treat as canonical
// when deciding hook-command drift. Pass setup.ResolveDFMTCommand() at
// call time so the comparison reflects the currently-running binary.
func CleanLegacyClaudeSettings(path, resolveDFMTCommand string) ([]LegacyEntry, error) {
	cfg, err := readSettingsJSON(path)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, nil
	}
	return detectLegacy(path, cfg, resolveDFMTCommand), nil
}

// PurgeLegacyClaudeSettings detects fossils, writes a one-shot backup at
// `<path>.dfmt.bak` (if it doesn't already exist), removes the deletable
// entries, expands matcher subsets, and rewrites the file via
// safefs.WriteFileAtomic. Idempotent: a second call against an already-
// purged file produces an empty PurgeReport with no I/O beyond the read.
//
// Missing / empty input file: returns an empty report, no error, no
// write. The cleaner is opt-in safe -- a project that never went near
// dfmt will never get a backup or a modified file.
func PurgeLegacyClaudeSettings(path, resolveDFMTCommand string) (PurgeReport, error) {
	report := PurgeReport{}

	cfg, err := readSettingsJSON(path)
	if err != nil {
		return report, err
	}
	if cfg == nil {
		return report, nil
	}

	entries := detectLegacy(path, cfg, resolveDFMTCommand)
	if len(entries) == 0 {
		return report, nil
	}

	mutated := false
	for _, e := range entries {
		switch e.Kind {
		case LegacyDenyTriplet:
			if applied := purgeDenyTriplet(cfg); applied {
				report.Removed = append(report.Removed, e)
				mutated = true
			} else {
				report.Skipped = append(report.Skipped, e)
			}
		case LegacyDottedMCP:
			if applied := purgeDottedMCPPerms(cfg); applied {
				report.Removed = append(report.Removed, e)
				mutated = true
			} else {
				report.Skipped = append(report.Skipped, e)
			}
		case LegacyHookCmdDrift:
			if applied := repointHookCommand(cfg, e.Location, resolveDFMTCommand); applied {
				report.Adjusted = append(report.Adjusted, e)
				mutated = true
			} else {
				report.Skipped = append(report.Skipped, e)
			}
		case LegacyMatcherSubset:
			if applied := expandHookMatcher(cfg, e.Location); applied {
				report.Adjusted = append(report.Adjusted, e)
				mutated = true
			} else {
				report.Skipped = append(report.Skipped, e)
			}
		case LegacyOrphanBackup:
			// Orphan backups are reported but NOT auto-removed -- a user
			// may want to restore from one. Caller can sweep them with a
			// separate explicit step.
			report.Skipped = append(report.Skipped, e)
		}
	}

	if !mutated {
		return report, nil
	}

	// Write a one-shot backup before mutating.
	backupPath := path + ".dfmt.bak"
	if _, statErr := os.Stat(backupPath); errors.Is(statErr, os.ErrNotExist) {
		if raw, rerr := os.ReadFile(path); rerr == nil {
			dir := filepath.Dir(path)
			if werr := safefs.WriteFile(dir, backupPath, raw, 0o600); werr == nil {
				report.Backup = backupPath
			}
		}
	}

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return report, err
	}
	out = append(out, '\n')

	dir := filepath.Dir(path)
	if err := safefs.WriteFileAtomic(dir, path, out, 0o600); err != nil {
		return report, fmt.Errorf("write %s: %w", path, err)
	}
	return report, nil
}

// readSettingsJSON returns nil cfg when the file is missing or empty,
// nil error in both cases. Other read / parse errors propagate.
func readSettingsJSON(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return nil, nil
	}
	cfg := map[string]any{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

// detectLegacy walks the parsed cfg and returns one LegacyEntry per
// fossil found. Order is stable (deny first, then dotted perms, then
// hook entries in PreToolUse/PreCompact/SessionStart order) so test
// assertions and operator output are predictable.
func detectLegacy(path string, cfg map[string]any, resolveDFMTCommand string) []LegacyEntry {
	var entries []LegacyEntry

	// 1. Deny triplet.
	if hasDenyTriplet(cfg) {
		entries = append(entries, LegacyEntry{
			Kind:     LegacyDenyTriplet,
			Path:     path,
			Location: "permissions.deny",
			Detail:   "legacy {Bash, WebFetch, WebSearch} triplet (pre-v0.3.1 injection)",
		})
	}

	// 2. Dotted MCP permissions.
	if dotted := findDottedMCPPerms(cfg); len(dotted) > 0 {
		sort.Strings(dotted)
		entries = append(entries, LegacyEntry{
			Kind:     LegacyDottedMCP,
			Path:     path,
			Location: "permissions.allow",
			Detail:   fmt.Sprintf("legacy dotted MCP names: %s", strings.Join(dotted, ", ")),
		})
	}

	// 3. Per-event hook scan.
	for _, eventName := range []string{hookEventPreToolUse, "PreCompact", "SessionStart"} {
		hookEntries := scanHooksForLegacy(cfg, eventName, path, resolveDFMTCommand)
		entries = append(entries, hookEntries...)
	}

	return entries
}

// hasDenyTriplet returns true when permissions.deny contains all three
// legacy names. The detection mirrors the original pruneStaleDfmtDeny
// heuristic so behavior is unchanged from pre-Phase-1.
func hasDenyTriplet(cfg map[string]any) bool {
	perms, _ := cfg["permissions"].(map[string]any)
	if perms == nil {
		return false
	}
	existing, _ := perms["deny"].([]any)
	if len(existing) == 0 {
		return false
	}
	seen := map[string]bool{"Bash": false, "WebFetch": false, "WebSearch": false}
	for _, v := range existing {
		s, _ := v.(string)
		if _, ok := seen[s]; ok {
			seen[s] = true
		}
	}
	return seen["Bash"] && seen["WebFetch"] && seen["WebSearch"]
}

func findDottedMCPPerms(cfg map[string]any) []string {
	perms, _ := cfg["permissions"].(map[string]any)
	if perms == nil {
		return nil
	}
	allow, _ := perms["allow"].([]any)
	var hits []string
	for _, v := range allow {
		s, _ := v.(string)
		if dottedMCPPermPattern.MatchString(s) {
			hits = append(hits, s)
		}
	}
	return hits
}

// scanHooksForLegacy walks one event's hook groups and returns LegacyEntry
// rows for each command that matches dfmtHookCmdPattern but either drifts
// from the canonical command (resolveDFMTCommand) or has a matcher
// missing some of the current PreToolUse names.
//
// Location format encodes the indices so the purger can find the same
// entry on rewrite without re-walking from scratch:
//
//	"hooks.PreToolUse[<groupIdx>].hooks[<innerIdx>]"
func scanHooksForLegacy(cfg map[string]any, eventName, path, resolveDFMTCommand string) []LegacyEntry {
	hooks, _ := cfg["hooks"].(map[string]any)
	if hooks == nil {
		return nil
	}
	groups, _ := hooks[eventName].([]any)
	if len(groups) == 0 {
		return nil
	}
	var entries []LegacyEntry
	for gi, g := range groups {
		grp, _ := g.(map[string]any)
		if grp == nil {
			continue
		}
		matcher, _ := grp["matcher"].(string)
		inner, _ := grp["hooks"].([]any)
		for hi, h := range inner {
			hc, _ := h.(map[string]any)
			if hc == nil {
				continue
			}
			cmd, _ := hc["command"].(string)
			if !dfmtHookCmdPattern.MatchString(cmd) {
				// Not a dfmt-shaped command; user-owned, leave alone.
				continue
			}
			loc := fmt.Sprintf("hooks.%s[%d].hooks[%d]", eventName, gi, hi)

			// Command drift: dfmt-shaped but doesn't equal the current
			// resolved canonical form. Skip this check when
			// resolveDFMTCommand is empty (caller couldn't resolve);
			// otherwise we'd rewrite every hook on every refresh.
			if resolveDFMTCommand != "" && !cmdEqualsCanonical(cmd, resolveDFMTCommand, eventName) {
				expected := expectedCommandFor(eventName, resolveDFMTCommand)
				entries = append(entries, LegacyEntry{
					Kind:     LegacyHookCmdDrift,
					Path:     path,
					Location: loc,
					Detail:   fmt.Sprintf("hook command %q does not match current %q", cmd, expected),
				})
			}

			// Matcher subset: only relevant for PreToolUse, where matcher
			// is a regex of pipe-separated tool names.
			if eventName == hookEventPreToolUse && matcherIsSubset(matcher, CurrentPreToolUseMatchers) {
				missing := strings.Join(missingMatchers(matcher, CurrentPreToolUseMatchers), "|")
				entries = append(entries, LegacyEntry{
					Kind:     LegacyMatcherSubset,
					Path:     path,
					Location: loc,
					Detail:   fmt.Sprintf("PreToolUse matcher %q misses %q", matcher, missing),
				})
			}
		}
	}
	return entries
}

// expectedCommandFor returns the canonical command string for a given
// hook event, given the resolved dfmt binary path. Currently we only
// rewrite PreToolUse drift; PreCompact and SessionStart commands are
// embedded shell snippets (not bare `dfmt ...`) so we leave them alone.
func expectedCommandFor(eventName, resolveDFMTCommand string) string {
	switch eventName {
	case "PreToolUse":
		return resolveDFMTCommand + " hook claude-code pretooluse"
	default:
		return ""
	}
}

// cmdEqualsCanonical compares a hook's stored command to the canonical
// form for its event. PreToolUse is the only event where we enforce
// equality; for other events we always return true so they are never
// flagged as drift. We treat absolute-path and bare-name forms as
// equivalent: literal "dfmt hook ..." is acceptable when the user has
// dfmt on PATH and the canonical form is just the resolved abs path.
func cmdEqualsCanonical(cmd, resolveDFMTCommand, eventName string) bool {
	if eventName != "PreToolUse" {
		return true
	}
	canonical := expectedCommandFor(eventName, resolveDFMTCommand)
	if cmd == canonical {
		return true
	}
	// Accept the bare-name form ("dfmt hook claude-code pretooluse") as
	// equivalent so we don't rewrite a user-installed PATH-resolved hook
	// into an absolute one unnecessarily.
	if cmd == "dfmt hook claude-code pretooluse" {
		return true
	}
	return false
}

// matcherIsSubset returns true when matcher contains a strict subset of
// the names in current. Empty matcher is treated as "matches nothing"
// for our purposes -- pre-Phase-1 hooks never wrote an empty matcher
// for PreToolUse.
func matcherIsSubset(matcher string, current []string) bool {
	if matcher == "" {
		return false
	}
	parts := strings.Split(matcher, "|")
	have := make(map[string]bool, len(parts))
	for _, p := range parts {
		have[strings.TrimSpace(p)] = true
	}
	missing := 0
	for _, want := range current {
		if !have[want] {
			missing++
		}
	}
	return missing > 0
}

func missingMatchers(matcher string, current []string) []string {
	parts := strings.Split(matcher, "|")
	have := make(map[string]bool, len(parts))
	for _, p := range parts {
		have[strings.TrimSpace(p)] = true
	}
	var miss []string
	for _, want := range current {
		if !have[want] {
			miss = append(miss, want)
		}
	}
	return miss
}

// purgeDenyTriplet removes the legacy triplet from cfg's permissions.deny.
// This is the migration of pruneStaleDfmtDeny -- same semantics, lifted
// here so all legacy handling lives in one file.
func purgeDenyTriplet(cfg map[string]any) bool {
	if !hasDenyTriplet(cfg) {
		return false
	}
	perms, _ := cfg["permissions"].(map[string]any)
	existing, _ := perms["deny"].([]any)
	pruned := make([]any, 0, len(existing))
	legacy := map[string]bool{"Bash": true, "WebFetch": true, "WebSearch": true}
	for _, v := range existing {
		s, ok := v.(string)
		if ok && legacy[s] {
			continue
		}
		pruned = append(pruned, v)
	}
	if len(pruned) == 0 {
		delete(perms, "deny")
	} else {
		perms["deny"] = pruned
	}
	cfg["permissions"] = perms
	return true
}

func purgeDottedMCPPerms(cfg map[string]any) bool {
	perms, _ := cfg["permissions"].(map[string]any)
	if perms == nil {
		return false
	}
	allow, _ := perms["allow"].([]any)
	if len(allow) == 0 {
		return false
	}
	pruned := make([]any, 0, len(allow))
	changed := false
	for _, v := range allow {
		s, _ := v.(string)
		if dottedMCPPermPattern.MatchString(s) {
			changed = true
			continue
		}
		pruned = append(pruned, v)
	}
	if !changed {
		return false
	}
	if len(pruned) == 0 {
		delete(perms, "allow")
	} else {
		perms["allow"] = pruned
	}
	cfg["permissions"] = perms
	return true
}

// repointHookCommand rewrites the command of a hook at the given
// dot-and-bracket location to the canonical form for its event.
func repointHookCommand(cfg map[string]any, location, resolveDFMTCommand string) bool {
	hookEntry, eventName := lookupHookByLocation(cfg, location)
	if hookEntry == nil {
		return false
	}
	canonical := expectedCommandFor(eventName, resolveDFMTCommand)
	if canonical == "" {
		return false
	}
	hookEntry["command"] = canonical
	return true
}

// expandHookMatcher rewrites the matcher of the group containing the
// hook at the given location to include every name in
// CurrentPreToolUseMatchers, preserving any extra names the user added.
func expandHookMatcher(cfg map[string]any, location string) bool {
	groupEntry, _ := lookupHookGroupByLocation(cfg, location)
	if groupEntry == nil {
		return false
	}
	existing, _ := groupEntry["matcher"].(string)
	have := make(map[string]bool)
	if existing != "" {
		for _, p := range strings.Split(existing, "|") {
			have[strings.TrimSpace(p)] = true
		}
	}
	for _, want := range CurrentPreToolUseMatchers {
		have[want] = true
	}
	merged := make([]string, 0, len(have))
	for k := range have {
		if k != "" {
			merged = append(merged, k)
		}
	}
	sort.Strings(merged)
	// Restore the canonical order: current set first (in defined order),
	// then any user extras alphabetically afterwards.
	canonical := make([]string, 0, len(CurrentPreToolUseMatchers)+1)
	canonical = append(canonical, CurrentPreToolUseMatchers...)
	canonicalSet := make(map[string]bool, len(canonical))
	for _, c := range canonical {
		canonicalSet[c] = true
	}
	var extras []string
	for _, m := range merged {
		if !canonicalSet[m] {
			extras = append(extras, m)
		}
	}
	canonical = append(canonical, extras...)
	groupEntry["matcher"] = strings.Join(canonical, "|")
	return true
}

// lookupHookByLocation parses a "hooks.<Event>[<gi>].hooks[<hi>]" string
// and returns a pointer to the inner hook map, plus the event name. The
// returned map is the live entry inside cfg -- mutations are persisted
// when cfg is re-marshaled.
func lookupHookByLocation(cfg map[string]any, location string) (map[string]any, string) {
	gi, hi, eventName, ok := parseHookLocation(location)
	if !ok {
		return nil, ""
	}
	hooks, _ := cfg["hooks"].(map[string]any)
	if hooks == nil {
		return nil, eventName
	}
	groups, _ := hooks[eventName].([]any)
	if gi < 0 || gi >= len(groups) {
		return nil, eventName
	}
	grp, _ := groups[gi].(map[string]any)
	if grp == nil {
		return nil, eventName
	}
	inner, _ := grp["hooks"].([]any)
	if hi < 0 || hi >= len(inner) {
		return nil, eventName
	}
	hc, _ := inner[hi].(map[string]any)
	return hc, eventName
}

func lookupHookGroupByLocation(cfg map[string]any, location string) (map[string]any, string) {
	gi, _, eventName, ok := parseHookLocation(location)
	if !ok {
		return nil, ""
	}
	hooks, _ := cfg["hooks"].(map[string]any)
	if hooks == nil {
		return nil, eventName
	}
	groups, _ := hooks[eventName].([]any)
	if gi < 0 || gi >= len(groups) {
		return nil, eventName
	}
	grp, _ := groups[gi].(map[string]any)
	return grp, eventName
}

// parseHookLocation strictly parses "hooks.<Event>[<gi>].hooks[<hi>]".
// Returns ok=false on any malformed input so callers degrade gracefully
// instead of panicking on a corrupt LegacyEntry.Location.
func parseHookLocation(location string) (gi, hi int, eventName string, ok bool) {
	const prefix = "hooks."
	if !strings.HasPrefix(location, prefix) {
		return 0, 0, "", false
	}
	rest := strings.TrimPrefix(location, prefix)
	openBracket := strings.IndexByte(rest, '[')
	if openBracket < 0 {
		return 0, 0, "", false
	}
	eventName = rest[:openBracket]
	rest = rest[openBracket:]

	var gi64 int
	n, err := fmt.Sscanf(rest, "[%d].hooks[%d]", &gi64, &hi)
	if err != nil || n != 2 {
		return 0, 0, "", false
	}
	return gi64, hi, eventName, true
}
