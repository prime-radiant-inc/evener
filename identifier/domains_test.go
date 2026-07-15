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
		"agent call": ValidateAgentCallID, "synthetic call": ValidateSyntheticCallID,
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
		"synthetic call": MustNewSyntheticCallID, "terminal generation": MustNewTerminalGeneration,
	} {
		t.Run(name, func(t *testing.T) {
			if got := newID(); got == "" {
				t.Fatal("returned empty ID")
			}
		})
	}
}
