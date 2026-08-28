package evener_test

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// mktempEscapesTMPDIR reports whether a `mktemp` invocation creates its scratch
// somewhere a caller's TMPDIR cannot reach. Two spellings do:
//
//   - `mktemp -t PREFIX`, and
//   - `mktemp` with no path argument at all.
//
// On macOS both ignore TMPDIR and create in the Darwin per-user temp directory
// (confstr _CS_DARWIN_USER_TEMP_DIR). Only an explicit path template is honoured,
// which is why `mktemp -d "${TMPDIR:-/tmp}/name.XXXXXX"` is the spelling to use.
//
// The consequence is not cosmetic. The dev-tooling wave gives each suite its own
// TMPDIR and fails a suite that leaves anything behind, so a script whose scratch
// escapes to the Darwin directory sits outside BOTH the isolation and the leak
// check. The coverage floor ratchet leaked one directory per run that way —
// including one per run of its own selftest, which asserted `ls -A "$tmphome"`
// was empty and passed by inspecting a directory the scratch never entered. 56
// abandoned directories holding 1.5GB accumulated before anyone looked.
func mktempEscapesTMPDIR(call string) bool {
	// Stop at whatever ends the command, so the next command's words are not
	// mistaken for mktemp's arguments.
	for _, terminator := range []string{")", ";", "|", "&&", "||", "#"} {
		if i := strings.Index(call, terminator); i >= 0 {
			call = call[:i]
		}
	}
	fields := strings.Fields(call)
	if len(fields) == 0 || fields[0] != "mktemp" {
		return false
	}
	for _, f := range fields[1:] {
		if f == "-t" {
			return true
		}
		if !strings.HasPrefix(f, "-") {
			return false // an explicit path template
		}
	}
	return true // flags only, no path
}

// mktempAllowedScripts are the scripts still carrying the defect, each creating
// scratch outside any TMPDIR a caller sets.
//
// This list is the finding, not an exemption. The coverage runners were swept
// because that is where the 1.5GB came from; sweeping the rest is its own piece
// of work. Removing an entry after fixing its script is the intended lifecycle —
// the list should only ever get shorter.
var mktempAllowedScripts = map[string]bool{
	"e2e-cover.sh":           true,
	"fuzz-bisect.sh":         true,
	"fuzz-gap-check.sh":      true,
	"fuzz-oracle-audit.sh":   true,
	"fuzz-registry-check.sh": true,
	"fuzz-triage.sh":         true,
}

// scriptShellFiles returns every .sh file under scripts/, recursively. The
// scripts/ directory was reorganized into subdirectories (lib/, gate/,
// coverage/, fuzz/, e2e/, ops/, web/), so a flat os.ReadDir misses every file
// in a subdirectory. The audit must walk the tree.
func scriptShellFiles(t *testing.T) []string {
	t.Helper()
	var paths []string
	if err := filepath.WalkDir("scripts", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".sh") {
			return nil
		}
		paths = append(paths, path)
		return nil
	}); err != nil {
		t.Fatalf("walk scripts/: %v", err)
	}
	sort.Strings(paths)
	return paths
}

// TestNoScriptCreatesScratchOutsideTMPDIR keeps the swept scripts swept: the
// coverage runners and every *-selftest.sh, which the dev-tooling wave isolates
// by TMPDIR and then checks for leftovers.
func TestNoScriptCreatesScratchOutsideTMPDIR(t *testing.T) {
	t.Parallel()
	paths := scriptShellFiles(t)
	var offenders []string
	seenAllowed := map[string]bool{}
	for _, path := range paths {
		name := filepath.Base(path)
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for i, line := range strings.Split(string(body), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			at := strings.Index(trimmed, "mktemp")
			if at < 0 || !mktempEscapesTMPDIR(trimmed[at:]) {
				continue
			}
			if mktempAllowedScripts[name] {
				seenAllowed[name] = true
				continue
			}
			offenders = append(offenders, fmt.Sprintf("%s:%d: %s", path, i+1, trimmed))
		}
	}
	sort.Strings(offenders)
	for _, o := range offenders {
		t.Errorf("this mktemp ignores TMPDIR on macOS, so the wave's per-suite isolation "+
			"and its leftover check both miss what it creates; give it an explicit "+
			"template like mktemp -d \"${TMPDIR:-/tmp}/name.XXXXXX\":\n  %s", o)
	}

	// An allowlist entry that matches nothing is stale: the script was fixed or
	// deleted, and the entry now hides the next regression in it.
	for name := range mktempAllowedScripts {
		if !seenAllowed[name] {
			t.Errorf("mktempAllowedScripts names %s, which no longer has an escaping "+
				"mktemp call — delete the entry", name)
		}
	}
}

// uncheckedMktempAssignment reports whether a line assigns a variable from a
// `mktemp` command substitution without checking whether mktemp succeeded.
//
// `if ! work="$(mktemp -d ...)"` and `work="$(mktemp -d ...)" || die` both
// check. A bare assignment does not, and under `set -uo pipefail` — which the
// selftests use, deliberately, so that a failed assertion does not abort the
// suite before its summary — nothing stops the script at that line.
func uncheckedMktempAssignment(line string) bool {
	at := strings.Index(line, "=$(mktemp")
	if at < 0 {
		at = strings.Index(line, `="$(mktemp`)
	}
	if at <= 0 {
		return false
	}
	if strings.HasPrefix(line, "if ") || strings.Contains(line, "||") {
		return false
	}
	return true
}

// scratchDirAllowedScripts are the *-selftest.sh suites still building
// scratch from an unchecked `mktemp`. As with mktempAllowedScripts above, the
// list is the finding rather than an exemption, and removing an entry after
// converting its suite to scratch_dir is the intended lifecycle.
//
// Every suite now builds its scratch root with scratch_dir; the list is empty
// and a new entry needs the same reviewed reason any allowlist growth does.
var scratchDirAllowedScripts = map[string]bool{}

// TestScratchDirCannotResolveToCWD keeps kata 5hs2's failure mode out of
// the selftests: an unchecked `mktemp -d` whose empty result was then resolved
// to the suite's OWN WORKING DIRECTORY by `work="$(cd "$work" && pwd -P)"`,
// because `cd ""` succeeds and leaves $PWD alone. The suite wrote its fixtures
// into the checkout and its EXIT trap ran `rm -rf` on it.
//
// The rule here is aimed at the unchecked mktemp rather than at the
// canonicalization that amplified it, because the unchecked mktemp is the line
// an author actually writes, and you cannot reach the destructive shape without
// it first. Every entry in the allowlist is a real one; a rule aimed at the
// canonicalization instead would have needed entries for lines that are fine.
//
// It also pins scratch_dir's calling convention. The guard reports failure
// by exiting, so calling it inside a command substitution would end only that
// subshell and hand the caller an empty path — reinstating the bug. Making that
// a failing test is what keeps the convention from being folklore.
func TestScratchDirCannotResolveToCWD(t *testing.T) {
	t.Parallel()
	paths := scriptShellFiles(t)
	var unchecked, swallowed []string
	seenAllowed := map[string]bool{}
	for _, path := range paths {
		name := filepath.Base(path)
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		isSelftest := strings.HasSuffix(name, "-selftest.sh")
		for i, line := range strings.Split(string(body), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			where := fmt.Sprintf("%s:%d: %s", path, i+1, trimmed)
			if strings.Contains(trimmed, "$(scratch_dir") {
				swallowed = append(swallowed, where)
			}
			if !isSelftest || !uncheckedMktempAssignment(trimmed) {
				continue
			}
			if scratchDirAllowedScripts[name] {
				seenAllowed[name] = true
				continue
			}
			unchecked = append(unchecked, where)
		}
	}
	sort.Strings(unchecked)
	for _, o := range unchecked {
		t.Errorf("this mktemp is unchecked, so a failed mktemp leaves the variable empty "+
			"and any later `cd` canonicalization of it resolves to the suite's own working "+
			"directory, which its cleanup then deletes (kata 5hs2); build scratch with "+
			"`scratch_dir <var> <prefix>` from scripts/lib/scratch-lib.sh:\n  %s", o)
	}
	sort.Strings(swallowed)
	for _, o := range swallowed {
		t.Errorf("scratch_dir reports failure by exiting, which a command substitution "+
			"swallows — the caller would continue with an empty path. Call it as a "+
			"statement instead: `scratch_dir work my-selftest`:\n  %s", o)
	}

	for name := range scratchDirAllowedScripts {
		if !seenAllowed[name] {
			t.Errorf("scratchDirAllowedScripts names %s, which no longer builds scratch "+
				"from an unchecked mktemp — delete the entry", name)
		}
	}
}

// The rm-detection predicate this audit walks scripts/ with —
// recursiveDeleteLineText, recursiveDeleteCommands, namesRM, rmArguments,
// recursiveDelete, operandComesFromVariable — is shared with the Makefile
// audit's TestNoMakefileRecipeFeedsVariableToRecursiveDelete and lives in
// recursivedelete_audit_test.go; see that file's header for why (issue #153).
//
// What the scan still cannot see, even shared: `find -exec rm` is a delete of
// the literal `{}` operand, never a variable. None of that spelling exists in
// the scanned scripts today, and the policy in docs/developing-evener/testing.md covers it. A
// false positive is impossible to suppress silently: the only way past the
// check is a reviewed entry in recursiveDeleteAllowedLines.

// recursiveDeleteAllowedLines is every variable-fed recursive delete the
// repository still contains, pinned to an exact count per script. Keyed on
// the exact repo-relative path, not basename: a basename key would let an
// unrelated script that happens to share a name (any other install.sh, say)
// inherit an exemption it never earned (#183, noted in the PR #116 review).
// The count matters in both directions: a new offending line in an
// allowlisted script fails the audit, and a removed one leaves the entry
// stale and fails the audit until the entry shrinks. What the count cannot
// catch is substitution: replacing one banned line with a different banned
// line in the same file keeps the count and passes, so review of these files
// still has to read the deletes themselves. The list is the finding, not an
// exemption, and each entry carries the reason it is allowed to exist.
var recursiveDeleteAllowedLines = map[string]int{
	// The one blessed delete: scratch_rm removing only what scratch_dir
	// minted, validated, and registered. Everything else defers here.
	"scripts/lib/scratch-lib.sh": 1,
	// e2e_stop_run reaps a run directory only after finding the marker file the
	// start wrote there; an emptied or clobbered argument fails the marker
	// check and exits 2 without deleting. The two e2e harness scripts used to
	// each carry their own copy of this delete; centralising it in e2e-lib.sh
	// moved the one delete and its guard into a single sourced library, which
	// is why the per-script entries for the harnesses are gone.
	"scripts/lib/e2e-lib.sh": 1,
	// Printed operator guidance, not a delete. The heredoc is unquoted — the
	// scanned root has to interpolate — so this line escapes its own
	// expansion (`rm -rf "\$dir"`) to reach the reader verbatim. Nothing
	// runs it; a human reviews each entry and removes it by hand (kata gmpr).
	"scripts/ops/report-tmp-debris.sh": 1,
	// A sourced operator library that must return, never exit, so it cannot
	// adopt scratch_dir. Its mint is a bare mktemp; what keeps the delete safe
	// is the cleanup guard beside it, which refuses to run when the root
	// variable is empty or unset.
	"scripts/lib/live-eval-isolation.sh": 1,
	// Per-scenario corpus scratch reclaimed inside the provider loop; the
	// suite-level guard cannot express "remove this one, keep the rest".
	"scripts/fuzz/fuzz-drive.sh": 2,
	// The coverage runner keeps the pid-suffixed name-first/trap-first/mkdir
	// pattern instead of scratch_dir: the trap exists before the directory
	// can, so a signal in the window abandons nothing (measured 0/150 leaks
	// vs 29/150 for a mint-then-trap ordering), and #105's owner-side reclaim
	// parses the pid out of the basename, which a random mktemp suffix would
	// turn into a permanent no-op. Its delete targets the name the script
	// composed from its own $$ before anything else ran.
	"scripts/coverage/coverage-floor.sh": 1,
	// test-cost adopts the same pid-suffixed pattern as the runners above,
	// for the same reasons; before this it minted with `mktemp -t` (outside
	// any TMPDIR a caller set) and never deleted its scratch at all.
	"scripts/coverage/test-cost.sh": 1,
	// Owner-side reclamation of the coverage runners' own abandoned scratch:
	// the delete IS the job. Targets come from a prefix glob walked with
	// existence and symlink guards, scoped to basenames whose pid suffix no
	// longer answers kill -0 — never from a caller's variable.
	"scripts/lib/covscratch-lib.sh": 1,
	// Between-check resets of the suite's private tmphome fixture, each
	// guarded by ${tmphome:?} so an unset or empty variable aborts the
	// expansion instead of widening the delete.
	"scripts/lib/covscratch-selftest-lib.sh": 4,
	// POSIX sh by contract — its own test execs it via `sh`, which ignores
	// the shebang — so the bash-only guard is unreachable. Under set -eu a
	// failed mint aborts before the trap arms, and the trap deletes only the
	// name mktemp just returned.
	"scripts/ops/build-runtime-pair.sh": 1,
	// The curl|sh bootstrap runs on machines that have no checkout, so the
	// bash-only guard is unreachable. Its mint is checked and empty-guarded,
	// and cleanup refuses an empty root rather than deleting from it.
	"install.sh": 1,
}

// recursiveDeleteOffenders scans paths for variable-fed recursive deletes
// using the shared predicate described above, keyed and counted by the exact
// path rather than basename (#183): a poison file at a different path must
// never inherit another path's allowance just because the two share a
// basename. Returns the offending lines beyond each path's allowance, the
// lines the predicate cannot fully read (a delete split across a
// backslash-continued line, or with no path at all — see
// recursiveDeleteUnreadableReason), and the raw per-path counts so a caller
// can also check for stale (over-)allowances.
func recursiveDeleteOffenders(t *testing.T, paths []string, allowed map[string]int) (offenders, unreadable []string, counts map[string]int) {
	t.Helper()
	counts = map[string]int{}
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for i, line := range strings.Split(string(body), "\n") {
			// continues reports whether the shell joins the NEXT physical
			// line onto this one, which is what makes a delete at the end
			// of this line unreviewable.
			trimmed, continues := recursiveDeleteLineText(line)
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			where := fmt.Sprintf("%s:%d: %s", path, i+1, trimmed)
			commands := recursiveDeleteCommands(trimmed, "$(")
			for c, command := range commands {
				args, isRM := rmArguments(command)
				if !isRM {
					continue
				}
				// An rm that is the LAST command on a continued line runs on
				// into the next physical line, which may carry its
				// recursion flag, its path, or both. Neither this line nor
				// the next says what gets deleted, so the shape is refused
				// outright rather than allowlisted: a delete that cannot be
				// read cannot be reviewed.
				if continues && c == len(commands)-1 {
					unreadable = append(unreadable, where)
					continue
				}
				operands, recursive := recursiveDelete(args)
				if !recursive {
					continue
				}
				// A recursive delete with no path at all takes its target
				// from a pipe (`xargs rm -rf`), which is equally unreadable.
				if len(operands) == 0 {
					unreadable = append(unreadable, where)
					continue
				}
				fromVariable := false
				for _, operand := range operands {
					if operandComesFromVariable(operand) {
						fromVariable = true
					}
				}
				if !fromVariable {
					continue
				}
				counts[path]++
				if counts[path] > allowed[path] {
					offenders = append(offenders, where)
				}
			}
		}
	}
	return offenders, unreadable, counts
}

// TestNoScriptFeedsVariableToRecursiveDelete enforces the rule directly
// rather than by its upstream causes: after kata 5hs2's cleanup deleted a
// home directory, the standing rule is that no recursive delete in scripts/
// takes an argument that could get clobbered. Scratch is minted by
// `scratch_dir <var> <prefix>` and reclaimed by the no-argument `scratch_rm`
// (scripts/scratch-lib.sh); a delete that cannot be handed a path cannot be
// handed the wrong one.
func TestNoScriptFeedsVariableToRecursiveDelete(t *testing.T) {
	t.Parallel()
	paths := scriptShellFiles(t)
	// Everything under scripts/, plus the repo-root shell that ships to
	// machines this audit will never run on: install.sh is curl|sh'd into a
	// user's shell, where a clobbered delete would be someone else's home.
	allPaths := append([]string{"install.sh"}, paths...)
	offenders, unreadable, counts := recursiveDeleteOffenders(t, allPaths, recursiveDeleteAllowedLines)
	sort.Strings(offenders)
	for _, o := range offenders {
		t.Errorf("this recursive delete takes a variable a caller can clobber (kata 5hs2 "+
			"deleted a home directory this way); mint scratch with `scratch_dir <var> <prefix>` "+
			"and reclaim it with the no-argument `scratch_rm` from scripts/lib/scratch-lib.sh:\n  %s", o)
	}
	sort.Strings(unreadable)
	for _, o := range unreadable {
		t.Errorf(recursiveDeleteUnreadableReason, o)
	}
	for path, want := range recursiveDeleteAllowedLines {
		if got := counts[path]; got < want {
			t.Errorf("recursiveDeleteAllowedLines allows %d variable-fed recursive deletes in %s "+
				"but only %d exist — shrink the entry so the next one cannot hide behind it", want, path, got)
		}
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
	writeAuditScriptFixture(t, poisonPath, "#!/bin/sh\nrm -rf \"$stage\"\n")

	// allowedPath is a DIFFERENT file, never written here, that legitimately
	// earned this allowance under review; poisonPath must not ride on it just
	// because both happen to end in "install.sh".
	allowedPath := filepath.Join(dir, "reviewed", "install.sh")
	allowed := map[string]int{filepath.Base(allowedPath): 1}

	offenders, _, _ := recursiveDeleteOffenders(t, []string{poisonPath}, allowed)
	if len(offenders) == 0 {
		t.Fatalf("poisonPath (%s) shares allowedPath's basename (install.sh) but is a different, "+
			"unreviewed file; its recursive delete must be flagged rather than silently inherit "+
			"allowedPath's exemption", poisonPath)
	}
}

// writeAuditScriptFixture writes content to path, creating any missing
// parent directories, for audit tests that need real files at specific
// paths rather than under scriptShellFiles' scripts/ walk.
func writeAuditScriptFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// scratchMintLine reports whether a line mints the scratch directory a
// script's cleanup trap is responsible for: either the scratch_dir library
// call, or the manual pid-suffixed `mkdir "$dir" || { trap - EXIT; ...}`
// idiom the coverage runners use instead (see recursiveDeleteAllowedLines'
// entry for coverage-floor.sh for why they keep the manual pattern rather
// than adopting scratch_dir). Both are the "mkdir" side of the
// trap-before-mkdir convention: arm the cleanup trap before this line runs,
// or a crash in the gap abandons the directory this line creates.
//
// Textual, like the checks above it: a caller who reaches either spelling
// through a function or a variable slips past it.
func scratchMintLine(line string) bool {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return false
	}
	if fields[0] == "scratch_dir" {
		return true
	}
	return fields[0] == "mkdir" && strings.Contains(line, "trap - EXIT")
}

// exitTrapInstallLine reports whether a line arms an EXIT trap with a real
// handler, as distinct from `trap - EXIT`, which disarms one. The manual
// mint pattern's failed-mkdir branch disarms on the very line that mints
// (`mkdir "$dir" || { trap - EXIT; ...}`), and that disarm must not be
// mistaken for the install this check looks for.
func exitTrapInstallLine(line string) bool {
	fields := strings.Fields(line)
	if len(fields) < 2 || fields[0] != "trap" || fields[1] == "-" {
		return false
	}
	return strings.Contains(line, "EXIT")
}

// scratchOrderOffense is one line that mints scratch before any EXIT trap
// has been armed earlier in the same file.
type scratchOrderOffense struct {
	path, text string
	line       int
}

func (o scratchOrderOffense) String() string {
	return fmt.Sprintf("%s:%d: %s", o.path, o.line, o.text)
}

// scratchOrderOffenses scans paths for scratchMintLine occurrences with no
// preceding exitTrapInstallLine earlier in the same file. A file whose trap
// is armed after its first mint reports that mint; a file with no trap at
// all reports every mint, since trapArmed never becomes true.
func scratchOrderOffenses(t *testing.T, paths []string) []scratchOrderOffense {
	t.Helper()
	var offenses []scratchOrderOffense
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		trapArmed := false
		for i, line := range strings.Split(string(body), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			if exitTrapInstallLine(trimmed) {
				trapArmed = true
				continue
			}
			if scratchMintLine(trimmed) && !trapArmed {
				offenses = append(offenses, scratchOrderOffense{path: path, line: i + 1, text: trimmed})
			}
		}
	}
	return offenses
}

// TestScratchOrderAuditCatchesMintBeforeTrap is the regression test for
// #183's first defect: a poison script that mints scratch with scratch_dir
// before arming its EXIT trap must be flagged. This is exactly the mutation
// noted in the PR #133 review — reordering a script to mkdir-then-trap
// reintroduces the signal-window leak race the trap-before-mkdir convention
// exists to prevent.
func TestScratchOrderAuditCatchesMintBeforeTrap(t *testing.T) {
	dir := t.TempDir()
	poison := filepath.Join(dir, "poison-selftest.sh")
	writeAuditScriptFixture(t, poison, "#!/usr/bin/env bash\n"+
		"set -uo pipefail\n"+
		"scratch_dir work poison-selftest\n"+
		"trap 'scratch_rm' EXIT\n")

	offenses := scratchOrderOffenses(t, []string{poison})
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
	writeAuditScriptFixture(t, poison, "#!/usr/bin/env bash\n"+
		"set -uo pipefail\n"+
		`work_dir="${TMPDIR:-/tmp}/evener-poison.$$"`+"\n"+
		"fail=0\n"+
		"cleanup_work() { [ \"$fail\" -eq 0 ] && rm -rf \"$work_dir\"; }\n"+
		`mkdir "$work_dir" || { trap - EXIT; echo "cannot create $work_dir" >&2; exit 1; }`+"\n"+
		"trap cleanup_work EXIT\n")

	offenses := scratchOrderOffenses(t, []string{poison})
	if len(offenses) == 0 {
		t.Fatalf("poison script mkdirs its manual scratch directory before arming its EXIT " +
			"trap; the audit must flag it")
	}
}

// TestScratchOrderAuditAllowsTrapBeforeMint proves the check does not
// false-positive on either correctly-ordered idiom.
func TestScratchOrderAuditAllowsTrapBeforeMint(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good-selftest.sh")
	writeAuditScriptFixture(t, good, "#!/usr/bin/env bash\n"+
		"set -uo pipefail\n"+
		"trap 'scratch_rm' EXIT\n"+
		"scratch_dir work good-selftest\n")
	goodManual := filepath.Join(dir, "good-coverage.sh")
	writeAuditScriptFixture(t, goodManual, "#!/usr/bin/env bash\n"+
		"set -uo pipefail\n"+
		`work_dir="${TMPDIR:-/tmp}/evener-good.$$"`+"\n"+
		"fail=0\n"+
		"cleanup_work() { [ \"$fail\" -eq 0 ] && rm -rf \"$work_dir\"; }\n"+
		"trap cleanup_work EXIT\n"+
		`mkdir "$work_dir" || { trap - EXIT; echo "cannot create $work_dir" >&2; exit 1; }`+"\n")

	offenses := scratchOrderOffenses(t, []string{good, goodManual})
	if len(offenses) != 0 {
		t.Fatalf("correctly-ordered scripts (trap armed before mint) were flagged: %v", offenses)
	}
}

// scratchOrderAllowedPaths is every script that still mints scratch before
// arming its cleanup trap, keyed on the exact repo-relative path rather than
// basename (#183 — see recursiveDeleteAllowedLines for why basename keys are
// unsafe here too). Empty: every script that mints scratch now arms its trap
// first, so a new entry needs the same reviewed reason any allowlist growth
// does.
var scratchOrderAllowedPaths = map[string]bool{}

// TestScratchTrapInstalledBeforeMkdir pins the trap-before-mkdir convention
// the coverage runner's allowlist entry above already describes and measures
// (0/150 leaks trap-first vs 29/150 mint-then-trap): a cleanup trap must be
// armed before the line that creates the scratch directory it cleans up, so
// a signal in between abandons nothing. Neither TestNoScriptCreatesScratchOutsideTMPDIR
// nor TestNoScriptFeedsVariableToRecursiveDelete checks ordering — a
// mutation that reorders a script to mkdir-then-trap left both green (#183,
// PR #133 review).
func TestScratchTrapInstalledBeforeMkdir(t *testing.T) {
	t.Parallel()
	paths := scriptShellFiles(t)
	allPaths := append([]string{"install.sh"}, paths...)
	offenses := scratchOrderOffenses(t, allPaths)
	sort.Slice(offenses, func(i, j int) bool { return offenses[i].String() < offenses[j].String() })
	seenAllowed := map[string]bool{}
	for _, o := range offenses {
		if scratchOrderAllowedPaths[o.path] {
			seenAllowed[o.path] = true
			continue
		}
		t.Errorf("this mints scratch before any EXIT trap is armed earlier in the file, so a "+
			"crash in between leaks it (the coverage runner measured 0/150 leaks for "+
			"trap-first vs 29/150 for mint-then-trap); arm the trap first:\n  %s", o)
	}
	for path := range scratchOrderAllowedPaths {
		if !seenAllowed[path] {
			t.Errorf("scratchOrderAllowedPaths names %s, which no longer mints scratch before "+
				"its trap is armed — delete the entry", path)
		}
	}
}
