//go:build windows

package cli

import (
	"os/exec"
	"testing"
)

// detachSysProcAttr encodes the Windows contract that keeps exactly one
// daemon alive after the spawning command exits. It was uncovered, and the
// consequences of getting it wrong are severe and hard to trace:
//
//   - without DETACHED_PROCESS the daemon inherits the parent's console and
//     dies when that terminal closes;
//   - without CREATE_NEW_PROCESS_GROUP a Ctrl-C in the spawning shell
//     forwards to the daemon and kills it;
//   - CREATE_NO_WINDOW keeps the detached child from flashing a console.
//
// None of those show up as a test failure elsewhere — they show up as "my
// daemon keeps disappearing".
func TestDetachSysProcAttrSetsDetachFlags(t *testing.T) {
	cmd := exec.Command("cmd", "/c", "exit")
	detachSysProcAttr(cmd)

	if cmd.SysProcAttr == nil {
		t.Fatal("SysProcAttr is nil; the child would inherit the parent's console")
	}
	flags := cmd.SysProcAttr.CreationFlags

	for _, tc := range []struct {
		name string
		flag uint32
	}{
		{"DETACHED_PROCESS", winDetachedProcess},
		{"CREATE_NO_WINDOW", winCreateNoWindow},
		{"CREATE_NEW_PROCESS_GROUP", winCreateNewProcessGrp},
	} {
		if flags&tc.flag == 0 {
			t.Errorf("%s not set in CreationFlags (0x%X)", tc.name, flags)
		}
	}
}
