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
	if !IsTerminalTurnStatus(TurnStatusInterrupted) {
		t.Fatal("interrupted should be terminal")
	}
}
