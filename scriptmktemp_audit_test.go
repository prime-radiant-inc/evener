package serf_test

import (
	"fmt"
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
// check. scripts/test-coverage-floor.sh leaked one directory per run that way —
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
	"deploy-hub-selftest.sh":           true,
	"e2e-cover.sh":                     true,
	"e2e-ratelimited-provider.sh":      true,
	"e2e-webui-turn-controls.sh":       true,
	"fuzz-bisect-selftest.sh":          true,
	"fuzz-bisect.sh":                   true,
	"fuzz-continuous-selftest.sh":      true,
	"fuzz-gap-check.sh":                true,
	"fuzz-oracle-audit-selftest.sh":    true,
	"fuzz-oracle-audit.sh":             true,
	"fuzz-registry-check.sh":           true,
	"fuzz-triage-selftest.sh":          true,
	"fuzz-triage.sh":                   true,
	"live-compaction-eval-selftest.sh": true,
	"live-eval-isolation-selftest.sh":  true,
	"live-eval-isolation.sh":           true,
	"merge-approval-gate-selftest.sh":  true,
	"parallelize-tests.sh":             true,
	"private-go-home-selftest.sh":      true,
	"run-module-lint-selftest.sh":      true,
	"run-module-lint.sh":               true,
	"run-module-tests-selftest.sh":     true,
	"seatbelt-smoke.sh":                true,
	"setup-gocache-selftest.sh":        true,
	"test-cost.sh":                     true,
	"test-overlap.sh":                  true,
	"tmux-read-selftest.sh":            true,
	"tmux-read.sh":                     true,
	"tmux-send-selftest.sh":            true,
	"tmux-send.sh":                     true,
}

// TestNoScriptCreatesScratchOutsideTMPDIR keeps the swept scripts swept: the
// coverage runners and every *-selftest.sh, which the dev-tooling wave isolates
// by TMPDIR and then checks for leftovers.
func TestNoScriptCreatesScratchOutsideTMPDIR(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir("scripts")
	if err != nil {
		t.Fatalf("read scripts/: %v", err)
	}
	var offenders []string
	seenAllowed := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sh") {
			continue
		}
		body, err := os.ReadFile(filepath.Join("scripts", e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
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
			if mktempAllowedScripts[e.Name()] {
				seenAllowed[e.Name()] = true
				continue
			}
			offenders = append(offenders, fmt.Sprintf("scripts/%s:%d: %s", e.Name(), i+1, trimmed))
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

// selftestScratchAllowedScripts are the *-selftest.sh suites still building
// scratch from an unchecked `mktemp`. As with mktempAllowedScripts above, the
// list is the finding rather than an exemption, and removing an entry after
// converting its suite to selftest_scratch is the intended lifecycle.
//
// These were left carrying it deliberately, because none of them is destructive
// the way the eight converted suites were: they do not canonicalize, so a failed
// mktemp leaves the variable empty and their cleanup runs `rm -rf ""`, which
// reaches nothing.
//
// fuzz-drive-selftest.sh is the one that does canonicalize (its per-scenario
// `r`), and it still stays here. Its cleanup only ever removes the suite root,
// so a failed inner mktemp scribbles fixtures into $PWD without deleting it —
// and converting it would make things worse, not better: new_repo is called as
// `repo="$(new_repo)"`, so selftest_scratch's exit would be swallowed by that
// substitution and hand the caller an empty path.
var selftestScratchAllowedScripts = map[string]bool{
	"coverage-gaps-selftest.sh":        true,
	"coverage-union-selftest.sh":       true,
	"deploy-hub-selftest.sh":           true,
	"fuzz-bisect-selftest.sh":          true,
	"fuzz-continuous-selftest.sh":      true,
	"fuzz-drive-selftest.sh":           true,
	"fuzz-oracle-audit-selftest.sh":    true,
	"fuzz-triage-selftest.sh":          true,
	"live-compaction-eval-selftest.sh": true,
	"live-eval-isolation-selftest.sh":  true,
	"merge-approval-gate-selftest.sh":  true,
	"private-go-home-selftest.sh":      true,
	"run-module-lint-selftest.sh":      true,
	"run-module-tests-selftest.sh":     true,
	"setup-gocache-selftest.sh":        true,
	"test-coverage-floor-selftest.sh":  true,
	"tmux-read-selftest.sh":            true,
	"tmux-send-selftest.sh":            true,
	"web-coverage-floor-selftest.sh":   true,
}

// TestSelftestScratchCannotResolveToCWD keeps kata 5hs2's failure mode out of
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
// It also pins selftest_scratch's calling convention. The guard reports failure
// by exiting, so calling it inside a command substitution would end only that
// subshell and hand the caller an empty path — reinstating the bug. Making that
// a failing test is what keeps the convention from being folklore.
func TestSelftestScratchCannotResolveToCWD(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir("scripts")
	if err != nil {
		t.Fatalf("read scripts/: %v", err)
	}
	var unchecked, swallowed []string
	seenAllowed := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sh") {
			continue
		}
		body, err := os.ReadFile(filepath.Join("scripts", e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		isSelftest := strings.HasSuffix(e.Name(), "-selftest.sh")
		for i, line := range strings.Split(string(body), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			where := fmt.Sprintf("scripts/%s:%d: %s", e.Name(), i+1, trimmed)
			if strings.Contains(trimmed, "$(selftest_scratch") {
				swallowed = append(swallowed, where)
			}
			if !isSelftest || !uncheckedMktempAssignment(trimmed) {
				continue
			}
			if selftestScratchAllowedScripts[e.Name()] {
				seenAllowed[e.Name()] = true
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
			"`selftest_scratch <var> <prefix>` from scripts/selftest-lib.sh:\n  %s", o)
	}
	sort.Strings(swallowed)
	for _, o := range swallowed {
		t.Errorf("selftest_scratch reports failure by exiting, which a command substitution "+
			"swallows — the caller would continue with an empty path. Call it as a "+
			"statement instead: `selftest_scratch work my-selftest`:\n  %s", o)
	}

	for name := range selftestScratchAllowedScripts {
		if !seenAllowed[name] {
			t.Errorf("selftestScratchAllowedScripts names %s, which no longer builds scratch "+
				"from an unchecked mktemp — delete the entry", name)
		}
	}
}
