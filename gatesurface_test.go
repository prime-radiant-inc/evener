package evener_test

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// gateShardSkip sources the gate-surface library and returns what
// gate_shard_skip prints for the gate's skip and a caller's.
func gateShardSkip(t *testing.T, gate, user string) string {
	t.Helper()
	cmd := exec.Command("sh", "-c", `. scripts/lib/gate-surface-lib.sh && gate_shard_skip "$1" "$2"`, "sh", gate, user)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gate_shard_skip: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestGateShardSkipKeepsTheCallersSkip pins that a caller's *_SHARD_SKIP
// survives the gate: the shard runners get the gate's fuzz-owned skip and the
// caller's as alternatives, and the gate's alone when the caller set none.
func TestGateShardSkipKeepsTheCallersSkip(t *testing.T) {
	gate := "(SeqFuzz|Sanity)"
	if got := gateShardSkip(t, gate, ""); got != gate {
		t.Fatalf("gate_shard_skip without a caller skip = %q, want the gate's %q", got, gate)
	}
	combined := regexp.MustCompile(gateShardSkip(t, gate, "^TestSlow$|^TestFlaky$"))
	for name, skipped := range map[string]bool{
		"TestLifecycleSeqFuzz": true,
		"TestSlow":             true,
		"TestFlaky":            true,
		"TestOrdinary":         false,
		"TestSlowButNotExact":  false,
	} {
		if combined.MatchString(name) != skipped {
			t.Errorf("combined skip %q matches %s = %v, want %v", combined, name, !skipped, skipped)
		}
	}
}

// TestGateShardSkipNeverMatchesEveryTest pins the empty-gate case: "|(user)"
// has an empty alternative that matches every name, which would skip the
// whole suite while the gate reported PASS.
func TestGateShardSkipNeverMatchesEveryTest(t *testing.T) {
	if got := gateShardSkip(t, "", "^TestSlow$"); got != "^TestSlow$" {
		t.Fatalf("gate_shard_skip with no gate skip = %q, want the caller's alone", got)
	}
	if got := gateShardSkip(t, "", ""); got != "" {
		t.Fatalf("gate_shard_skip with neither = %q, want empty", got)
	}
}
