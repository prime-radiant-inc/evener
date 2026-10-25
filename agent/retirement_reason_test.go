package agent

import (
	"testing"

	"primeradiant.com/evener/agent/schema"
)

func TestSuccessfulRetirementRecordsReason(t *testing.T) {
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MaxSubagentDepth: 1}))
	if err := s.RecordRetirementReason("idle"); err != nil {
		t.Fatalf("RecordRetirementReason: %v", err)
	}
	meta, err := schema.LoadSessionMeta(s.stateDir, s.id)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if meta.RetirementReason != "idle" {
		t.Fatalf("retirement reason = %q, want idle", meta.RetirementReason)
	}
	if meta.UpdatedAt.IsZero() {
		t.Fatal("retirement record has no UpdatedAt")
	}
}
