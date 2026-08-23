package main

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"primeradiant.com/evener/appwire"
)

// TestClampSessionInputHeightBelowMin covers the below-minimum path.
func TestClampSessionInputHeightBelowMin(t *testing.T) {
	if got := clampSessionInputHeight(0, 10); got != 1 {
		t.Fatalf("clampSessionInputHeight(0, 10) = %d, want 1", got)
	}
	if got := clampSessionInputHeight(-5, 10); got != 1 {
		t.Fatalf("clampSessionInputHeight(-5, 10) = %d, want 1", got)
	}
}

// TestClampSessionInputHeightAboveMax covers the above-maximum path.
func TestClampSessionInputHeightAboveMax(t *testing.T) {
	if got := clampSessionInputHeight(20, 10); got != 10 {
		t.Fatalf("clampSessionInputHeight(20, 10) = %d, want 10", got)
	}
}

// TestClampSessionInputHeightInRange covers the in-range path.
func TestClampSessionInputHeightInRange(t *testing.T) {
	if got := clampSessionInputHeight(5, 10); got != 5 {
		t.Fatalf("clampSessionInputHeight(5, 10) = %d, want 5", got)
	}
}

// TestSessionSendUnavailableReasonEmpty covers the empty-reason path.
func TestSessionSendUnavailableReasonEmpty(t *testing.T) {
	got := sessionSendUnavailableReason("")
	if got != "send is not available for this session" {
		t.Fatalf("sessionSendUnavailableReason('') = %q, want default", got)
	}
}

// TestSessionSendUnavailableReasonNonEmpty covers the non-empty path.
func TestSessionSendUnavailableReasonNonEmpty(t *testing.T) {
	got := sessionSendUnavailableReason("custom reason")
	if got != "custom reason" {
		t.Fatalf("sessionSendUnavailableReason('custom reason') = %q, want 'custom reason'", got)
	}
}

// TestIsAltVKeyLower covers the lowercase 'v' path.
func TestIsAltVKeyLower(t *testing.T) {
	msg := tea.KeyMsg{Alt: true, Type: tea.KeyRunes, Runes: []rune{'v'}}
	if !isAltVKey(msg) {
		t.Fatalf("isAltVKey with Alt+v should return true")
	}
}

// TestIsAltVKeyUpper covers the uppercase 'V' path.
func TestIsAltVKeyUpper(t *testing.T) {
	msg := tea.KeyMsg{Alt: true, Type: tea.KeyRunes, Runes: []rune{'V'}}
	if !isAltVKey(msg) {
		t.Fatalf("isAltVKey with Alt+V should return true")
	}
}

// TestIsAltVKeyNoAlt covers the no-Alt path.
func TestIsAltVKeyNoAlt(t *testing.T) {
	msg := tea.KeyMsg{Alt: false, Type: tea.KeyRunes, Runes: []rune{'v'}}
	if isAltVKey(msg) {
		t.Fatalf("isAltVKey without Alt should return false")
	}
}

// TestIsAltVKeyWrongType covers the wrong-type path.
func TestIsAltVKeyWrongType(t *testing.T) {
	msg := tea.KeyMsg{Alt: true, Type: tea.KeyEnter, Runes: []rune{'v'}}
	if isAltVKey(msg) {
		t.Fatalf("isAltVKey with wrong type should return false")
	}
}

// TestIsAltVKeyMultipleRunes covers the multiple-runes path.
func TestIsAltVKeyMultipleRunes(t *testing.T) {
	msg := tea.KeyMsg{Alt: true, Type: tea.KeyRunes, Runes: []rune{'v', 'x'}}
	if isAltVKey(msg) {
		t.Fatalf("isAltVKey with multiple runes should return false")
	}
}

// TestIsAltVKeyWrongRune covers the wrong-rune path.
func TestIsAltVKeyWrongRune(t *testing.T) {
	msg := tea.KeyMsg{Alt: true, Type: tea.KeyRunes, Runes: []rune{'x'}}
	if isAltVKey(msg) {
		t.Fatalf("isAltVKey with wrong rune should return false")
	}
}

// TestIsQueuedDrainPartialNil covers the nil-error path.
func TestIsQueuedDrainPartialNil(t *testing.T) {
	if isQueuedDrainPartial(nil) {
		t.Fatalf("isQueuedDrainPartial(nil) should return false")
	}
}

// TestIsQueuedDrainPartialNonWireError covers the non-WireError path.
func TestIsQueuedDrainPartialNonWireError(t *testing.T) {
	if isQueuedDrainPartial(errors.New("ordinary error")) {
		t.Fatalf("isQueuedDrainPartial with ordinary error should return false")
	}
}

// TestIsQueuedDrainPartialWithWireErrorErrorData covers the ErrorData path.
func TestIsQueuedDrainPartialWithWireErrorErrorData(t *testing.T) {
	err := appwire.WireError{
		Data: appwire.ErrorData{EvenerErrorInfo: appwire.ErrorQueuedDrainPartial},
	}
	if !isQueuedDrainPartial(err) {
		t.Fatalf("isQueuedDrainPartial with ErrorData should return true")
	}
}

// TestIsQueuedDrainPartialWithWireErrorMap covers the map[string]any path.
func TestIsQueuedDrainPartialWithWireErrorMap(t *testing.T) {
	err := appwire.WireError{
		Data: map[string]any{"evenerErrorInfo": string(appwire.ErrorQueuedDrainPartial)},
	}
	if !isQueuedDrainPartial(err) {
		t.Fatalf("isQueuedDrainPartial with map should return true")
	}
}

// TestIsQueuedDrainPartialWithWireErrorWrongData covers the wrong-data path.
func TestIsQueuedDrainPartialWithWireErrorWrongData(t *testing.T) {
	err := appwire.WireError{
		Data: appwire.ErrorData{EvenerErrorInfo: "something-else"},
	}
	if isQueuedDrainPartial(err) {
		t.Fatalf("isQueuedDrainPartial with wrong EvenerErrorInfo should return false")
	}
}

// TestIsQueuedDrainPartialWithWireErrorOtherType covers the default case.
func TestIsQueuedDrainPartialWithWireErrorOtherType(t *testing.T) {
	err := appwire.WireError{
		Data: "string-data",
	}
	if isQueuedDrainPartial(err) {
		t.Fatalf("isQueuedDrainPartial with string data should return false")
	}
}
