package tool

import (
	"testing"
)

// TestFailureLedger_NilGuards covers the nil-ledger guards in check, record,
// and clearFailures (lines 136-137, 157-158, 213-214).
func TestFailureLedger_NilGuards(t *testing.T) {
	var l *failureLedger

	// check on nil ledger returns zeros.
	failStreak, repeatStreak, snippets := l.check("tool", []byte("{}"))
	if failStreak != 0 || repeatStreak != 0 || snippets != nil {
		t.Errorf("nil check = (%d, %d, %v), want (0, 0, nil)", failStreak, repeatStreak, snippets)
	}

	// record on nil ledger returns zeros.
	failStreak, repeatStreak = l.record("tool", []byte("{}"), true, "error")
	if failStreak != 0 || repeatStreak != 0 {
		t.Errorf("nil record = (%d, %d), want (0, 0)", failStreak, repeatStreak)
	}

	// clearFailures on nil ledger is a no-op (should not panic).
	l.clearFailures("tool", []byte("{}"))
}

// TestClearFailures_NoEntry covers the branch where clearFailures is called on
// a signature that was never recorded (line 220-221).
func TestClearFailures_NoEntry(t *testing.T) {
	l := newFailureLedger()
	// Clear a signature that doesn't exist — should be a safe no-op.
	l.clearFailures("nonexistent", []byte("{}"))
}

// TestFirstNonBlankLine_AllBlank covers the return-empty path when all lines
// are blank (line 271).
func TestFirstNonBlankLine_AllBlank(t *testing.T) {
	if got := firstNonBlankLine("\n  \n\t\n"); got != "" {
		t.Errorf("firstNonBlankLine(all blank) = %q, want empty", got)
	}
}

// TestErrorClass_BlankOutput covers errorClass on all-blank input (exercises
// firstNonBlankLine returning "" through the errorClass pipeline).
func TestErrorClass_BlankOutput(t *testing.T) {
	// Should not panic and should return a consistent hash.
	got := errorClass("\n\n  \n")
	if got == "" {
		t.Error("errorClass(blank) returned empty, want a hash")
	}
}
