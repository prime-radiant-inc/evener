package appsource

import (
	"encoding/json"
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestCodexTurnsUnavailableBeforeFirstMessage(t *testing.T) {
	err := errors.New("includeTurns is unavailable before first user message")
	if !codexTurnsUnavailableBeforeFirstMessage(err) {
		t.Fatal("should match the expected error")
	}
	err = errors.New("some other error")
	if codexTurnsUnavailableBeforeFirstMessage(err) {
		t.Fatal("should not match other errors")
	}
}

func TestCodexNoRolloutFound(t *testing.T) {
	if !codexNoRolloutFound(errors.New("No rollout found for thread id abc")) {
		t.Fatal("should match 'no rollout found' error")
	}
	if !codexNoRolloutFound(errors.New("no rollout found for thread id xyz")) {
		t.Fatal("should match case-insensitive")
	}
	if codexNoRolloutFound(errors.New("some other error")) {
		t.Fatal("should not match other errors")
	}
	if codexNoRolloutFound(nil) {
		t.Fatal("nil error should return false")
	}
}

func TestCloneCodexCachedThreadNoTurns(t *testing.T) {
	thread := appwire.Thread{
		ID:     "th1",
		Status: appwire.ThreadStatus{ActiveFlags: []string{"flag1"}},
		Turns:  []appwire.Turn{{ID: "turn1"}},
	}
	clone := cloneCodexCachedThread(thread, false)
	if clone.ID != "th1" {
		t.Fatal("ID should be preserved")
	}
	if clone.Turns != nil {
		t.Fatal("Turns should be nil when includeTurns=false")
	}
	if len(clone.Status.ActiveFlags) != 1 {
		t.Fatal("ActiveFlags should be cloned")
	}
}

func TestCloneCodexCachedThreadWithTurns(t *testing.T) {
	startedAt := int64(1000)
	thread := appwire.Thread{
		ID: "th1",
		Turns: []appwire.Turn{{
			ID:        "turn1",
			StartedAt: &startedAt,
		}},
	}
	clone := cloneCodexCachedThread(thread, true)
	if len(clone.Turns) != 1 {
		t.Fatal("Turns should be cloned when includeTurns=true")
	}
	if clone.Turns[0].ID != "turn1" {
		t.Fatal("turn ID should be preserved")
	}
	// Modifying the clone should not affect the original
	*clone.Turns[0].StartedAt = 9999
	if *thread.Turns[0].StartedAt != 1000 {
		t.Fatal("original should be unaffected by clone modification")
	}
}

func TestCloneCodexCachedTurnWithError(t *testing.T) {
	raw := json.RawMessage(`{"code":"x"}`)
	turn := appwire.Turn{
		Error: &appwire.TurnError{
			Message:        "fail",
			CodexErrorInfo: raw,
		},
	}
	clone := cloneCodexCachedTurn(turn)
	if clone.Error == nil || clone.Error.Message != "fail" {
		t.Fatal("error should be cloned")
	}
	// Verify the raw message was cloned (not shared)
	originalRaw, ok := turn.Error.CodexErrorInfo.(json.RawMessage)
	if !ok {
		t.Fatal("original should still have json.RawMessage")
	}
	cloneRaw, ok := clone.Error.CodexErrorInfo.(json.RawMessage)
	if !ok {
		t.Fatal("clone should have json.RawMessage")
	}
	// Modifying clone's raw should not affect original
	cloneRaw[0] = 'X'
	if originalRaw[0] != '{' {
		t.Fatal("original raw should be unaffected")
	}
}

func TestCloneCodexCachedTurnNoError(t *testing.T) {
	turn := appwire.Turn{ID: "turn1"}
	clone := cloneCodexCachedTurn(turn)
	if clone.Error != nil {
		t.Fatal("nil error should stay nil")
	}
}

func TestCloneCodexCachedItem(t *testing.T) {
	startedAt := int64(500)
	exitCode := int64(0)
	position := appwire.ThreadItemPosition{Entry: 7, Item: 3}
	item := appwire.ThreadItem{
		Type:      "commandExecution",
		StartedAt: &startedAt,
		ExitCode:  &exitCode,
		Position:  &position,
		Raw:       json.RawMessage(`{"x":1}`),
		Images: []appwire.InputItem{
			{Data: []byte("img"), Metadata: map[string]string{"w": "10"}},
		},
	}
	clone := cloneCodexCachedItem(item)
	if clone.Type != "commandExecution" {
		t.Fatal("type should be preserved")
	}
	if clone.StartedAt == nil || *clone.StartedAt != 500 {
		t.Fatal("StartedAt should be cloned")
	}
	// Modify clone, verify original is unaffected
	*clone.StartedAt = 999
	if *item.StartedAt != 500 {
		t.Fatal("original StartedAt should be unaffected")
	}
	clone.Position.Entry = 99
	if item.Position.Entry != 7 {
		t.Fatal("original Position should be unaffected by clone modification")
	}
	if clone.ExitCode == nil || *clone.ExitCode != 0 {
		t.Fatal("ExitCode should be cloned")
	}
	if len(clone.Raw) != len(item.Raw) {
		t.Fatal("Raw should be cloned")
	}
	if len(clone.Images) != 1 {
		t.Fatal("Images should be cloned")
	}
	// Modify clone's image data, verify original unaffected
	clone.Images[0].Data[0] = 'X'
	if item.Images[0].Data[0] != 'i' {
		t.Fatal("original image data should be unaffected")
	}
	clone.Images[0].Metadata["new"] = "val"
	if _, exists := item.Images[0].Metadata["new"]; exists {
		t.Fatal("original metadata should be unaffected")
	}
}

func TestCloneCodexCachedInt64Nil(t *testing.T) {
	if cloneCodexCachedInt64(nil) != nil {
		t.Fatal("nil should return nil")
	}
}

func TestCloneCodexCachedInt64Value(t *testing.T) {
	v := int64(42)
	got := cloneCodexCachedInt64(&v)
	if got == nil || *got != 42 {
		t.Fatal("should return pointer to 42")
	}
	*got = 99
	if v != 42 {
		t.Fatal("modifying clone should not affect original")
	}
}
