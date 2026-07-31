package version

import (
	"runtime/debug"
	"testing"
)

func TestResolveCurrentKeepsStampedRelease(t *testing.T) {
	got := resolveCurrent("v1.2.3", func() (*debug.BuildInfo, bool) {
		t.Fatal("build info should not be read for stamped releases")
		return nil, false
	})
	if got != "v1.2.3" {
		t.Errorf("resolveCurrent(stamped) = %q, want v1.2.3", got)
	}
}

func TestResolveCurrentUsesVCSRevisionForDevBuild(t *testing.T) {
	got := resolveCurrent("dev", func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "0123456789abcdef0123456789abcdef01234567"},
		}}, true
	})
	if got != "dev-0123456789ab" {
		t.Errorf("resolveCurrent(dev) = %q, want dev-0123456789ab", got)
	}
}

func TestResolveCurrentMarksModifiedDevBuild(t *testing.T) {
	got := resolveCurrent("dev", func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "abcdef0123456789abcdef0123456789abcdef01"},
			{Key: "vcs.modified", Value: "true"},
		}}, true
	})
	if got != "dev-abcdef012345-modified" {
		t.Errorf("resolveCurrent(modified dev) = %q, want dev-abcdef012345-modified", got)
	}
}

func TestResolveCurrentFallsBackToDevWithoutBuildInfo(t *testing.T) {
	got := resolveCurrent("dev", func() (*debug.BuildInfo, bool) { return nil, false })
	if got != "dev" {
		t.Errorf("resolveCurrent(no build info) = %q, want dev", got)
	}
}
