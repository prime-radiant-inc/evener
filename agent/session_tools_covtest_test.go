package agent

import (
	"testing"
)

// TestResultToolName_Default covers the default name (line 233-234).
func TestResultToolName_Default(t *testing.T) {
	t.Parallel()
	s := &Session{}
	if got := s.resultToolName(); got != "communicate" {
		t.Fatalf("resultToolName = %q, want 'communicate'", got)
	}
}

// TestResultToolName_Custom covers the custom name (line 231-232).
func TestResultToolName_Custom(t *testing.T) {
	t.Parallel()
	s := &Session{cfg: SessionConfig{ResultToolName: "custom_result"}}
	if got := s.resultToolName(); got != "custom_result" {
		t.Fatalf("resultToolName = %q, want 'custom_result'", got)
	}
}
