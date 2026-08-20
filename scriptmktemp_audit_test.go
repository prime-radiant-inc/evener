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
// the scanned scripts today, and the policy in docs/testing.md covers it. A
// false positive is impossible to suppress silently: the only way past the
// check is a reviewed entry in recursiveDeleteAllowedLines.

// recursiveDeleteAllowedLines is every variable-fed recursive delete the
// repository still contains, pinned to an exact count per script. The count
// matters in both directions: a new offending line in an allowlisted script
// fails the audit, and a removed one leaves the entry stale and fails the
// audit until the entry shrinks. What the count cannot catch is substitution:
// replacing one banned line with a different banned line in the same file
// keeps the count and passes, so review of these files still has to read the
// deletes themselves. The list is the finding, not an exemption, and each
// entry carries the reason it is allowed to exist.
var recursiveDeleteAllowedLines = map[string]int{
	// The one blessed delete: scratch_rm removing only what scratch_dir
	// minted, validated, and registered. Everything else defers here.
	"scratch-lib.sh": 1,
	// e2e_stop_run reaps a run directory only after finding the marker file the
	// start wrote there; an emptied or clobbered argument fails the marker
	// check and exits 2 without deleting. The two e2e harness scripts used to
	// each carry their own copy of this delete; centralising it in e2e-lib.sh
	// moved the one delete and its guard into a single sourced library, which
	// is why the per-script entries for the harnesses are gone.
	"e2e-lib.sh": 1,
	// Printed operator guidance, not a delete. The heredoc is unquoted — the
	// scanned root has to interpolate — so this line escapes its own
	// expansion (`rm -rf "\$dir"`) to reach the reader verbatim. Nothing
	// runs it; a human reviews each entry and removes it by hand (kata gmpr).
	"report-tmp-debris.sh": 1,
	// A sourced operator library that must return, never exit, so it cannot
	// adopt scratch_dir. Its mint is a bare mktemp; what keeps the delete safe
	// is the cleanup guard beside it, which refuses to run when the root
	// variable is empty or unset.
	"live-eval-isolation.sh": 1,
	// Per-scenario corpus scratch reclaimed inside the provider loop; the
	// suite-level guard cannot express "remove this one, keep the rest".
	"fuzz-drive.sh": 2,
	// Fixture rebuild/removal of paths built under guard-minted scratch,
	// mid-suite, where the no-argument delete would take the suite's whole
	// scratch with it.
	"e2e-webui-turn-controls-selftest.sh": 1,
	// The coverage runners keep the pid-suffixed name-first/trap-first/mkdir
	// pattern instead of scratch_dir: the trap exists before the directory
	// can, so a signal in the window abandons nothing (measured 0/150 leaks
	// vs 29/150 for a mint-then-trap ordering), and #105's owner-side reclaim
	// parses the pid out of the basename, which a random mktemp suffix would
	// turn into a permanent no-op. Each delete targets the name the script
	// composed from its own $$ before anything else ran.
	"coverage-union.sh":       1,
	"fuzz-coverage.sh":        1,
	"fuzz-coverage-global.sh": 1,
	// test-cost adopts the same pid-suffixed pattern as the runners above,
	// for the same reasons; before this it minted with `mktemp -t` (outside
	// any TMPDIR a caller set) and never deleted its scratch at all.
	"test-cost.sh": 1,
	// Owner-side reclamation of the coverage runners' own abandoned scratch:
	// the delete IS the job. Targets come from a prefix glob walked with
	// existence and symlink guards, scoped to basenames whose pid suffix no
	// longer answers kill -0 — never from a caller's variable.
	"covscratch-lib.sh": 1,
	// Between-check resets of the suite's private tmphome fixture, each
	// guarded by ${tmphome:?} so an unset or empty variable aborts the
	// expansion instead of widening the delete.
	"covscratch-selftest-lib.sh": 4,
	// POSIX sh by contract — its own test execs it via `sh`, which ignores
	// the shebang — so the bash-only guard is unreachable. Under set -eu a
	// failed mint aborts before the trap arms, and the trap deletes only the
	// name mktemp just returned.
	"build-runtime-pair.sh": 1,
	// The curl|sh bootstrap runs on machines that have no checkout, so the
	// bash-only guard is unreachable. Its mint is checked and empty-guarded,
	// and cleanup refuses an empty root rather than deleting from it.
	"install.sh": 1,
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
	counts := map[string]int{}
	var offenders, unreadable []string
	for _, path := range allPaths {
		name := filepath.Base(path)
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
				counts[name]++
				if counts[name] > recursiveDeleteAllowedLines[name] {
					offenders = append(offenders, where)
				}
			}
		}
	}
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
	for name, want := range recursiveDeleteAllowedLines {
		if got := counts[name]; got < want {
			t.Errorf("recursiveDeleteAllowedLines allows %d variable-fed recursive deletes in %s "+
				"but only %d exist — shrink the entry so the next one cannot hide behind it", want, name, got)
		}
	}
}
