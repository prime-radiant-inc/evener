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
	"agent-test-shards-selftest.sh":         true,
	"deploy-hub-selftest.sh":                true,
	"e2e-cover.sh":                          true,
	"e2e-ratelimited-provider.sh":           true,
	"e2e-webui-turn-controls-selftest.sh":   true,
	"e2e-webui-turn-controls.sh":            true,
	"fuzz-bisect-selftest.sh":               true,
	"fuzz-bisect.sh":                        true,
	"fuzz-continuous-selftest.sh":           true,
	"fuzz-gap-check.sh":                     true,
	"fuzz-oracle-audit-selftest.sh":         true,
	"fuzz-oracle-audit.sh":                  true,
	"fuzz-registry-check.sh":                true,
	"fuzz-triage-selftest.sh":               true,
	"fuzz-triage.sh":                        true,
	"live-compaction-eval-selftest.sh":      true,
	"live-eval-isolation-selftest.sh":       true,
	"live-eval-isolation.sh":                true,
	"merge-approval-gate-selftest.sh":       true,
	"parallelize-tests.sh":                  true,
	"private-go-home-selftest.sh":           true,
	"reclaim-test-debris-selftest.sh":       true,
	"report-orphaned-worktrees-selftest.sh": true,
	"report-tmp-debris-selftest.sh":         true,
	"run-module-lint-selftest.sh":           true,
	"run-module-lint.sh":                    true,
	"run-module-tests-selftest.sh":          true,
	"scenario-cite-migrate-selftest.sh":     true,
	"seatbelt-smoke.sh":                     true,
	"setup-gocache-selftest.sh":             true,
	"test-cost.sh":                          true,
	"test-overlap.sh":                       true,
	"tmux-read-selftest.sh":                 true,
	"tmux-read.sh":                          true,
	"tmux-send-selftest.sh":                 true,
	"tmux-send.sh":                          true,
	"web-preflight-selftest.sh":             true,
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
