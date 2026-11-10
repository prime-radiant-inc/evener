package repoaudit

import (
	"os"
	"path/filepath"
	"testing"
)

// writeScriptFixture writes content to path, creating any missing parent
// directories, for checks that read real files at chosen paths.
func writeScriptFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestRecursiveDeleteAllowlistDoesNotInheritByBasename is the regression
// test for #183's second defect: a poison file at a DIFFERENT path than an
// allowlisted one, sharing only its basename, must not inherit the
// allowlisted path's exemption. Before the fix, recursiveDeleteAllowedLines
// was keyed by filepath.Base(path), so an unrelated "install.sh" anywhere in
// the tree read the same allowance as the real root install.sh.
func TestRecursiveDeleteAllowlistDoesNotInheritByBasename(t *testing.T) {
	dir := t.TempDir()
	poisonPath := filepath.Join(dir, "unrelated", "install.sh")
	writeScriptFixture(t, poisonPath, "#!/bin/sh\nrm -rf \"$stage\"\n")

	// allowedPath is a DIFFERENT file, never written here, that legitimately
	// earned this allowance under review; poisonPath must not ride on it just
	// because both happen to end in "install.sh".
	allowedPath := filepath.Join(dir, "reviewed", "install.sh")
	allowed := map[string]int{filepath.Base(allowedPath): 1}

	offenders, _, _ := recursiveDeleteOffenders(&audit{}, []string{poisonPath}, allowed)
	if len(offenders) == 0 {
		t.Fatalf("poisonPath (%s) shares allowedPath's basename (install.sh) but is a different, "+
			"unreviewed file; its recursive delete must be flagged rather than silently inherit "+
			"allowedPath's exemption", poisonPath)
	}
}

// TestScratchOrderAuditCatchesMintBeforeTrap is the regression test for
// #183's first defect: a poison script that mints scratch with scratch_dir
// before arming its EXIT trap must be flagged. This is exactly the mutation
// noted in the PR #133 review — reordering a script to mkdir-then-trap
// reintroduces the signal-window leak race the trap-before-mkdir convention
// exists to prevent.
func TestScratchOrderAuditCatchesMintBeforeTrap(t *testing.T) {
	dir := t.TempDir()
	poison := filepath.Join(dir, "poison-scratchdir.sh")
	writeScriptFixture(t, poison, "#!/usr/bin/env bash\n"+
		"set -uo pipefail\n"+
		"scratch_dir work poison-scratchdir\n"+
		"trap 'scratch_rm' EXIT\n")

	offenses := scratchOrderOffenses(&audit{}, []string{poison})
	if len(offenses) == 0 {
		t.Fatalf("poison script mints scratch with scratch_dir before arming its EXIT trap; " +
			"the audit must flag it")
	}
}

// TestScratchOrderAuditCatchesManualMkdirBeforeTrap is the manual-pattern
// twin of the test above: the coverage runners' pid-suffixed mkdir idiom,
// reordered to mint before the trap is armed.
func TestScratchOrderAuditCatchesManualMkdirBeforeTrap(t *testing.T) {
	dir := t.TempDir()
	poison := filepath.Join(dir, "poison-coverage.sh")
	writeScriptFixture(t, poison, "#!/usr/bin/env bash\n"+
		"set -uo pipefail\n"+
		`work_dir="${TMPDIR:-/tmp}/evener-poison.$$"`+"\n"+
		"fail=0\n"+
		"cleanup_work() { [ \"$fail\" -eq 0 ] && rm -rf \"$work_dir\"; }\n"+
		`mkdir "$work_dir" || { trap - EXIT; echo "cannot create $work_dir" >&2; exit 1; }`+"\n"+
		"trap cleanup_work EXIT\n")

	offenses := scratchOrderOffenses(&audit{}, []string{poison})
	if len(offenses) == 0 {
		t.Fatalf("poison script mkdirs its manual scratch directory before arming its EXIT " +
			"trap; the audit must flag it")
	}
}

// TestScratchOrderAuditAllowsTrapBeforeMint proves the check does not
// false-positive on either correctly-ordered idiom.
func TestScratchOrderAuditAllowsTrapBeforeMint(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good-scratchdir.sh")
	writeScriptFixture(t, good, "#!/usr/bin/env bash\n"+
		"set -uo pipefail\n"+
		"trap 'scratch_rm' EXIT\n"+
		"scratch_dir work good-scratchdir\n")
	goodManual := filepath.Join(dir, "good-coverage.sh")
	writeScriptFixture(t, goodManual, "#!/usr/bin/env bash\n"+
		"set -uo pipefail\n"+
		`work_dir="${TMPDIR:-/tmp}/evener-good.$$"`+"\n"+
		"fail=0\n"+
		"cleanup_work() { [ \"$fail\" -eq 0 ] && rm -rf \"$work_dir\"; }\n"+
		"trap cleanup_work EXIT\n"+
		`mkdir "$work_dir" || { trap - EXIT; echo "cannot create $work_dir" >&2; exit 1; }`+"\n")

	offenses := scratchOrderOffenses(&audit{}, []string{good, goodManual})
	if len(offenses) != 0 {
		t.Fatalf("correctly-ordered scripts (trap armed before mint) were flagged: %v", offenses)
	}
}
