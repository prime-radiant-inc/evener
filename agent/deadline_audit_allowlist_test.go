package agent

import (
	"os"
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
// bare wall-clock bound in any agent test file. It writes a temp _test.go
// file carrying a bare time.After(5 * time.Second) directly into the agent
// package dir (the audit scans ".", not a t.TempDir), runs the audit with the
// real (eventually empty) allowlist, asserts a finding for the temp file, and
// removes the temp file via t.Cleanup. The temp file is not on the allowlist
// under any phase, so this test passes both before and after the GREEN fix --
// its purpose is to prove the audit still works once the list is empty.
func TestDeadlineAuditCatchesFreshBareBound(t *testing.T) {
	const tempFile = "zzz_fresh_offense_142_test.go"
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
	if err := os.WriteFile(tempFile, []byte(source), 0o644); err != nil {
		t.Fatalf("write temp offense: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(tempFile) })

	findings, err := deadlineAuditFindings(".", deadlineAuditAllowlist)
	if err != nil {
		t.Fatalf("deadlineAuditFindings: %v", err)
	}
	found := false
	for _, f := range findings {
		if strings.Contains(f, tempFile) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("audit did not catch the fresh bare 5s bound in %s; findings: %v",
			tempFile, findings)
	}
}
