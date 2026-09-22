//go:build windows

package agent

import (
	"path/filepath"
	"strings"
	"testing"
)

// A drive-relative --state-dir (C:state) anchors to the current directory
// of drive C — GetFullPathName semantics — not to the working directory
// of whichever drive the process runs on. Regression test for the
// cwd-concatenation anchoring that corrupted C:state into D:\cwd\C:state
// (PR #2132 follow-up).
func TestAnchorRelativeStateDirDriveRelativeUsesDriveCwd(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	vol := filepath.VolumeName(dir) // e.g. "C:"
	got, ok := anchorRelativeStateDir(vol + "state")
	if !ok {
		t.Fatalf("anchorRelativeStateDir(%q) failed", vol+"state")
	}
	if want := filepath.Join(dir, "state"); !strings.EqualFold(got, want) {
		t.Fatalf("anchorRelativeStateDir(%q) = %q, want %q", vol+"state", got, want)
	}
}

// A volume-root-relative --state-dir (\state) anchors to the root of the
// current drive, not to the process working directory.
func TestAnchorRelativeStateDirRootRelativeUsesDriveRoot(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	vol := filepath.VolumeName(dir)
	got, ok := anchorRelativeStateDir(`\state`)
	if !ok {
		t.Fatal(`anchorRelativeStateDir(\state) failed`)
	}
	if want := vol + `\state`; !strings.EqualFold(got, want) {
		t.Fatalf(`anchorRelativeStateDir(\state) = %q, want %q`, got, want)
	}
}
