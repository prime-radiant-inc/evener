package identifier

import (
	"strings"
	"testing"
)

func TestGeneratedIDDomains(t *testing.T) {
	tests := []struct {
		name     string
		prefix   string
		newID    func() (string, error)
		validate func(string) error
	}{
		{"session", "", NewSessionID, ValidateSessionID},
		{"installation", "", NewInstallationID, ValidateInstallationID},
		{"job", "job_", NewJobID, ValidateJobID},
		{"delegate", "dlg_", NewDelegateID, ValidateDelegateID},
		{"delegate-generation", "dg_", NewDelegateGeneration, ValidateDelegateGeneration},
		{"watch", "watch_", NewWatchID, ValidateWatchID},
		{"watch-generation", "wg_", NewWatchGeneration, ValidateWatchGeneration},
		{"watch-delivery", "wd_", NewWatchDeliveryID, ValidateWatchDeliveryID},
		{"agent-call", "ag_", NewAgentCallID, ValidateAgentCallID},
		{"api-attempt", "att_", NewAPIAttemptID, ValidateAPIAttemptID},
		{"synthetic-call", "call_", NewSyntheticCallID, ValidateSyntheticCallID},
		{"terminal-generation", "", NewTerminalGeneration, ValidateTerminalGeneration},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.newID()
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tt.prefix)+22 || !strings.HasPrefix(got, tt.prefix) {
				t.Errorf("%q: %q", tt.name, got)
			}
			if err := tt.validate(got); err != nil {
				t.Errorf("%s: %v", tt.name, err)
			}
		})
	}
}

func TestGeneratedIDValidatorsRejectWrongDomain(t *testing.T) {
	tests := []struct {
		name     string
		prefix   string
		newID    func() (string, error)
		validate func(string) error
	}{
		{"session", "", NewSessionID, ValidateSessionID},
		{"installation", "", NewInstallationID, ValidateInstallationID},
		{"job", "job_", NewJobID, ValidateJobID},
		{"delegate", "dlg_", NewDelegateID, ValidateDelegateID},
		{"delegate-generation", "dg_", NewDelegateGeneration, ValidateDelegateGeneration},
		{"watch", "watch_", NewWatchID, ValidateWatchID},
		{"watch-generation", "wg_", NewWatchGeneration, ValidateWatchGeneration},
		{"watch-delivery", "wd_", NewWatchDeliveryID, ValidateWatchDeliveryID},
		{"agent-call", "ag_", NewAgentCallID, ValidateAgentCallID},
		{"api-attempt", "att_", NewAPIAttemptID, ValidateAPIAttemptID},
		{"synthetic-call", "call_", NewSyntheticCallID, ValidateSyntheticCallID},
		{"terminal-generation", "", NewTerminalGeneration, ValidateTerminalGeneration},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			valid, err := tt.newID()
			if err != nil {
				t.Fatal(err)
			}
			for name, input := range map[string]string{
				"wrong prefix": "wrong_" + valid[len(tt.prefix):],
				"ULID":         tt.prefix + strings.Repeat("0", 26),
				"truncated":    valid[:len(valid)-1],
			} {
				t.Run(name, func(t *testing.T) {
					if err := tt.validate(input); err == nil {
						t.Fatalf("accepted %q", input)
					}
				})
			}
		})
	}
}

func TestGeneratedIDValidatorsRejectCrossDomain(t *testing.T) {
	job, err := NewJobID()
	if err != nil {
		t.Fatal(err)
	}
	for name, validate := range map[string]func(string) error{
		"session": ValidateSessionID, "delegate": ValidateDelegateID, "watch": ValidateWatchID,
		"agent call": ValidateAgentCallID, "API attempt": ValidateAPIAttemptID,
		"synthetic call": ValidateSyntheticCallID,
	} {
		t.Run(name, func(t *testing.T) {
			if err := validate(job); err == nil {
				t.Fatalf("accepted cross-domain ID %q", job)
			}
		})
	}
}

func TestMustGeneratedIDDomains(t *testing.T) {
	for name, newID := range map[string]func() string{
		"session": MustNewSessionID, "installation": MustNewInstallationID, "job": MustNewJobID,
		"delegate": MustNewDelegateID, "delegate generation": MustNewDelegateGeneration,
		"watch": MustNewWatchID, "watch generation": MustNewWatchGeneration,
		"watch delivery": MustNewWatchDeliveryID, "agent call": MustNewAgentCallID,
		"API attempt": MustNewAPIAttemptID, "synthetic call": MustNewSyntheticCallID,
		"terminal generation": MustNewTerminalGeneration,
	} {
		t.Run(name, func(t *testing.T) {
			if got := newID(); got == "" {
				t.Fatal("returned empty ID")
			}
		})
	}
}

func TestAPIAttemptGeneratedIDsAreValidAndUnique(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for range 1000 {
		id, err := NewAPIAttemptID()
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateAPIAttemptID(id); err != nil {
			t.Fatalf("ValidateAPIAttemptID(%q): %v", id, err)
		}
		if _, exists := seen[id]; exists {
			t.Fatalf("duplicate API attempt ID %q", id)
		}
		seen[id] = struct{}{}
	}
}
