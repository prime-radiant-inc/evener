package appwire

import "testing"

func TestCanonicalThreadStatusUsesCodexVocabulary(t *testing.T) {
	tests := map[string]string{
		"processing":  ThreadStatusActive,
		"active":      ThreadStatusActive,
		"ended":       ThreadStatusNotLoaded,
		"notLoaded":   ThreadStatusNotLoaded,
		"error":       ThreadStatusSystemError,
		"systemError": ThreadStatusSystemError,
	}
	for in, want := range tests {
		if got := CanonicalThreadStatus(in); got != want {
			t.Fatalf("CanonicalThreadStatus(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestCanonicalTurnStatusUsesCodexVocabulary(t *testing.T) {
	tests := map[string]string{
		"running":     TurnStatusInProgress,
		"inProgress":  TurnStatusInProgress,
		"processing":  TurnStatusInProgress,
		"canceled":    TurnStatusInterrupted,
		"interrupted": TurnStatusInterrupted,
	}
	for in, want := range tests {
		if got := CanonicalTurnStatus(in); got != want {
			t.Fatalf("CanonicalTurnStatus(%q)=%q, want %q", in, got, want)
		}
	}
}
