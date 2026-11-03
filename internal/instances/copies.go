package instances

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// signature is embedded in every dfmt build (Go's build info and symbol
// table carry the module path), including broken or truncated-but-mostly
// intact ones, and is absent from unrelated programs that share the name.
var signature = []byte("github.com/ersinkoc/dfmt")

// maxScan bounds how much of a candidate file is read.
const maxScan = 64 << 20

// IsDFMTFile reports whether the file at path is a dfmt binary, without
// executing it. A broken binary that no longer runs still qualifies, which
// is what lets cleanup remove the corrupt copies a failed update leaves.
func IsDFMTFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }() // read-only
	return containsSignature(io.LimitReader(f, maxScan))
}

func containsSignature(r io.Reader) bool {
	const chunk = 1 << 20
	buf := make([]byte, chunk+len(signature))
	carry := 0
	for {
		n, err := r.Read(buf[carry:])
		end := carry + n
		if bytes.Contains(buf[:end], signature) {
			return true
		}
		if err != nil {
			return false
		}
		// Keep the tail so a marker split across two reads is still seen.
		keep := len(signature) - 1
		if end < keep {
			keep = end
		}
		copy(buf, buf[end-keep:end])
		carry = keep
	}
}

// Copy is a dfmt binary (or leftover) that is not the installed one.
type Copy struct {
	Path   string
	Reason string
}

// binaryNames are the file names a stray dfmt can have, including what a
// Windows update leaves behind (<exe>.old).
var binaryNames = []string{"dfmt", "dfmt.exe", "dfmt.old", "dfmt.exe.old"}

// FindCopies returns every verified dfmt binary other than keep that a user
// could end up running: each PATH directory, the usual per-user install
// locations, and keep's own directory (update leftovers). Files inside a Go
// source checkout are never reported — `dist/dfmt` is a build artifact the
// developer owns, not a stray install.
func FindCopies(keep string) []Copy {
	keepInfo, _ := os.Stat(keep)
	seenDir := map[string]bool{}
	seenFile := map[string]bool{}
	var out []Copy

	consider := func(path, reason string) {
		key := path
		if runtime.GOOS == goosWindows {
			key = strings.ToLower(key)
		}
		if seenFile[key] {
			return
		}
		seenFile[key] = true
		li, err := os.Lstat(path)
		if err != nil || li.IsDir() {
			return
		}
		if fi, err := os.Stat(path); err == nil && keepInfo != nil && os.SameFile(fi, keepInfo) {
			return // keep itself, a link to it, or a hard link
		}
		if inSourceTree(path) {
			return
		}
		if !IsDFMTFile(path) {
			return
		}
		out = append(out, Copy{Path: path, Reason: reason})
	}

	scanDir := func(dir, why string) {
		if dir == "" {
			return
		}
		dir = filepath.Clean(dir)
		dk := dir
		if runtime.GOOS == goosWindows {
			dk = strings.ToLower(dk)
		}
		if seenDir[dk] {
			return
		}
		seenDir[dk] = true
		for _, n := range binaryNames {
			consider(filepath.Join(dir, n), why)
		}
		if matches, _ := filepath.Glob(filepath.Join(dir, ".dfmt-update-*")); len(matches) > 0 {
			for _, m := range matches {
				consider(m, "interrupted update leftover")
			}
		}
	}

	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		scanDir(d, "another dfmt earlier or later on PATH")
	}
	for _, d := range knownDirs() {
		scanDir(d, "dfmt in a known install location")
	}
	scanDir(filepath.Dir(keep), "leftover beside the installed binary")
	return out
}

// knownDirs lists the places dfmt installers and `go install` put binaries.
func knownDirs() []string {
	home, _ := os.UserHomeDir()
	dirs := []string{"/usr/local/bin", "/opt/homebrew/bin"}
	if home != "" {
		dirs = append(dirs,
			filepath.Join(home, ".local", "bin"),
			filepath.Join(home, "bin"),
			filepath.Join(home, "go", "bin"),
			filepath.Join(home, ".dfmt"),
		)
	}
	if v := os.Getenv("GOBIN"); v != "" {
		dirs = append(dirs, v)
	}
	if v := os.Getenv("GOPATH"); v != "" {
		for _, p := range filepath.SplitList(v) {
			dirs = append(dirs, filepath.Join(p, "bin"))
		}
	}
	if v := os.Getenv("LOCALAPPDATA"); v != "" {
		dirs = append(dirs, filepath.Join(v, "Programs", "dfmt"))
	}
	return dirs
}

// inSourceTree reports whether path sits inside a Go module checkout, found
// by a go.mod within a few parent directories.
func inSourceTree(path string) bool {
	dir := filepath.Dir(path)
	for i := 0; i < 4; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
	return false
}

// String implements fmt.Stringer for log lines.
func (c Copy) String() string { return fmt.Sprintf("%s (%s)", c.Path, c.Reason) }
