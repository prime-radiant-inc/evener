package appwire

import "testing"

func TestThreadStatusHelpersUseCodexVocabulary(t *testing.T) {
	if !IsActiveThreadStatus(ThreadStatusActive) {
		t.Fatal("active should be active")
	}
	if IsActiveThreadStatus("processing") {
		t.Fatal("processing should not be accepted")
	}
}

func TestTurnStatusHelpersUseCodexVocabulary(t *testing.T) {
	if !IsActiveTurnStatus(TurnStatusInProgress) {
		t.Fatal("inProgress should be active")
	}
	if IsActiveTurnStatus("running") {
		t.Fatal("running should not be accepted")
	}

	terminalStatuses := []string{
		TurnStatusCompleted,
		TurnStatusFailed,
		TurnStatusInterrupted,
	}
	for _, status := range terminalStatuses {
		if !IsTerminalTurnStatus(status) {
			t.Errorf("status %q should be terminal", status)
		}
	}
	if IsTerminalTurnStatus(TurnStatusInProgress) {
		t.Fatal("inProgress should not be terminal")
	}
}

func TestItemStatusHelpers(t *testing.T) {
	if !IsActiveItemStatus(TurnStatusInProgress) {
		t.Fatal("inProgress should be active item status")
	}
	if IsActiveItemStatus("running") {
		t.Fatal("running should not be accepted as active item status")
	}

	terminalStatuses := []string{
		TurnStatusCompleted,
		TurnStatusFailed,
		TurnStatusInterrupted,
	}
	for _, status := range terminalStatuses {
		if !IsTerminalItemStatus(status) {
			t.Errorf("status %q should be terminal item status", status)
		}
	}
	if IsTerminalItemStatus(TurnStatusInProgress) {
		t.Fatal("inProgress should not be terminal item status")
	}
}
