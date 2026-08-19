package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDeadlineAuditAllowlistIsEmpty asserts that deadlineAuditAllowlist has
// been emptied (issue #142). The list is the ratchet kata ww3g left behind:
// every agent/*_test.go file that carried a bare wall-clock bound when the
// audit was written. It only ever shrinks -- a conversion wave removes a
// file's entry once every bound in it is either replaced by its real
// completion signal or carries a "// TRIPWIRE:" marker. The last hold-back
// was delegate_resource_runtime_test.go, held back because another kata was
// redesigning a test in it; both branches are merged now, so the entry must
// be gone. This test fails RED until the GREEN phase empties the map.
func TestDeadlineAuditAllowlistIsEmpty(t *testing.T) {
	if len(deadlineAuditAllowlist) != 0 {
		t.Fatalf("deadlineAuditAllowlist should be empty (issue #142), has %d entries: %v",
			len(deadlineAuditAllowlist), deadlineAuditAllowlist)
	}
}

// TestDeadlineAuditCatchesFreshBareBound proves the "Done when" criterion of
// issue #142: once the allowlist is empty, the audit must still catch a fresh
// bare wall-clock bound in the formerly-allowlisted file. It writes a fixture
// named delegate_resource_runtime_test.go -- the last hold-back's exact name
// -- carrying a bare time.After(5 * time.Second) into a t.TempDir, then runs
// the audit over that dir with the REAL allowlist. A finding proves both that
// the audit catches a fresh offense and that the real allowlist no longer
// exempts the file by name; with the pre-#142 allowlist this test is RED.
//
// The fixture goes in a t.TempDir, never the live package dir: CI runs this
// package's tests as concurrently-scheduled shards of one binary in the same
// directory (cmd/evener-dev agent-shards), so a poison file written into "."
// races another shard's TestNoBareWallClockDeadlineInAgentTests scan of ".".
func TestDeadlineAuditCatchesFreshBareBound(t *testing.T) {
	const offendingFile = "delegate_resource_runtime_test.go"
	source := `package agent

import (
	"testing"
	"time"
)

func TestFreshOffense142(t *testing.T) {
	select {
	case <-time.After(5 * time.Second):
	}
}
`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, offendingFile), []byte(source), 0o600); err != nil {
		t.Fatalf("write fixture offense: %v", err)
	}

	findings, err := deadlineAuditFindings(dir, deadlineAuditAllowlist)
	if err != nil {
		t.Fatalf("deadlineAuditFindings: %v", err)
	}
	found := false
	for _, f := range findings {
		if strings.Contains(f, offendingFile) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("audit did not catch the fresh bare 5s bound in %s; findings: %v",
			offendingFile, findings)
	}
}
