//go:build serffuzz

package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

func FuzzDelegateAttentionFold(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0, 0, 8, 2, 2, 0})
	f.Add([]byte{0, 8, 2, 11, 4, 52})
	f.Add([]byte{0, 4})
	f.Add([]byte{3, 5, 6})
	f.Add([]byte{5})
	f.Add([]byte{6})

	f.Fuzz(func(t *testing.T, program []byte) {
		if len(program) > 32 {
			program = program[:32]
		}
		entries := delegateAttentionFuzzEntries(program)
		want, wantErr := foldDelegateAttentionModel(entries)
		got, gotErr := foldDelegateAttention(entries)
		if (gotErr != nil) != (wantErr != nil) {
			t.Fatalf("fold error = %v, model error = %v", gotErr, wantErr)
		}
		if wantErr == nil {
			assertDelegateAttentionFuzzFold(t, got, want)
		}

		stateDir := t.TempDir()
		const sessionID = "attention-fuzz"
		path := filepath.Join(stateDir, sessionID+".jsonl")
		writer, err := transcript.NewWriter(path, transcript.Header{SessionID: sessionID})
		if err != nil {
			t.Fatalf("NewWriter: %v", err)
		}
		for _, entry := range entries {
			if err := writer.AppendDurable(entry.Turn); err != nil {
				_ = writer.Close()
				t.Fatalf("AppendDurable: %v", err)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile before: %v", err)
		}
		beforeInfo, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat before: %v", err)
		}
		cold, coldErr := readDelegateAttentionFold(path, sessionID)
		if (coldErr != nil) != (wantErr != nil) {
			t.Fatalf("cold fold error = %v, model error = %v", coldErr, wantErr)
		}
		if wantErr == nil {
			assertDelegateAttentionFuzzFold(t, cold, want)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile after: %v", err)
		}
		afterInfo, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat after: %v", err)
		}
		if !bytes.Equal(before, after) || beforeInfo.Size() != afterInfo.Size() || !beforeInfo.ModTime().Equal(afterInfo.ModTime()) {
			t.Fatalf("cold fold mutated transcript: before=%d/%s after=%d/%s", beforeInfo.Size(), beforeInfo.ModTime(), afterInfo.Size(), afterInfo.ModTime())
		}
	})
}

func FuzzStableDelegateWatchDelivery(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0})
	f.Add([]byte{1, 2, 3})

	f.Fuzz(func(t *testing.T, program []byte) {
		if len(program) > 8 {
			program = program[:8]
		}
		fixture := newStableWatchRuntimeFixture(t, nil)
		for i := 0; i <= len(program); i++ {
			var value byte
			if i < len(program) {
				value = program[i]
			}
			onSessionEventKD(fixture.sourceJM, events.EventCommunicate, events.CommunicateData{
				Message: fmt.Sprintf("stable-watch-%d-%02x", i, value),
			})
		}

		pending := fixture.requireOnePending(t)
		state := pending.state
		if state.SourceDelegateID != "dlg_source" || state.SourceDelegateGeneration != 1 {
			t.Fatalf("stable source identity = %q/%d, want dlg_source/1", state.SourceDelegateID, state.SourceDelegateGeneration)
		}
		if !state.StableReceiver || state.ReceiverSessionID != fixture.root.ID() || state.ReceiverDelegateID != "" {
			t.Fatalf("stable receiver identity = session:%q delegate:%q stable:%t", state.ReceiverSessionID, state.ReceiverDelegateID, state.StableReceiver)
		}

		attentionID := stableWatchAttentionID(state)
		result, err := fixture.source.drainJobManagerWatchSends(context.Background(), fixture.sourceJM, "")
		if err != nil {
			t.Fatalf("deliver stable watch: %v", err)
		}
		if !result.observerHandoff {
			t.Fatal("stable watch delivery did not report observer handoff")
		}
		if pending := fixture.sourceJM.pendingWatchSendDeliveries(nil); len(pending) != 0 {
			t.Fatalf("stable watch source remained pending: %#v", pending)
		}
		if got := countAttentionEntries(t, fixture.rootTranscriptPath, attentionID); got != 1 {
			t.Fatalf("stable watch attention count = %d, want 1", got)
		}

		if _, err := fixture.source.drainJobManagerWatchSends(context.Background(), fixture.sourceJM, ""); err != nil {
			t.Fatalf("repeat stable watch drain: %v", err)
		}
		if got := countAttentionEntries(t, fixture.rootTranscriptPath, attentionID); got != 1 {
			t.Fatalf("repeat drain duplicated stable watch attention: %d", got)
		}
	})
}

type delegateAttentionFoldModel struct {
	order           []string
	content         map[string]llm.Message
	resolutions     map[string]delegateAttentionResolution
	deliveryCommits map[string]string
}

func foldDelegateAttentionModel(entries []transcript.Entry) (delegateAttentionFoldModel, error) {
	model := delegateAttentionFoldModel{
		content:         make(map[string]llm.Message),
		resolutions:     make(map[string]delegateAttentionResolution),
		deliveryCommits: make(map[string]string),
	}
	for _, entry := range entries {
		turn := entry.Turn
		if len(turn.DelegateDeliveryCommits) != 0 {
			if turn.Kind != schema.TurnToolResults {
				return delegateAttentionFoldModel{}, errors.New("delivery commit on wrong turn kind")
			}
			results := make(map[string]bool)
			for _, part := range turn.Message.Content {
				if part.Kind == llm.ContentToolResult && part.ToolResult != nil {
					results[part.ToolResult.ToolCallID] = true
				}
			}
			for _, commit := range turn.DelegateDeliveryCommits {
				if commit.ToolCallID == "" || commit.DeliveryID == "" || !results[commit.ToolCallID] {
					return delegateAttentionFoldModel{}, errors.New("invalid delivery commit identity")
				}
				if previous, exists := model.deliveryCommits[commit.DeliveryID]; exists && previous != commit.ToolCallID {
					return delegateAttentionFoldModel{}, errors.New("conflicting delivery commit")
				}
				for deliveryID, toolCallID := range model.deliveryCommits {
					if toolCallID == commit.ToolCallID && deliveryID != commit.DeliveryID {
						return delegateAttentionFoldModel{}, errors.New("conflicting tool-call commit")
					}
				}
				model.deliveryCommits[commit.DeliveryID] = commit.ToolCallID
			}
		}
		if turn.AttentionID != "" {
			if turn.Kind != schema.TurnSteering || turn.AttentionResolution != nil {
				return delegateAttentionFoldModel{}, errors.New("invalid attention append")
			}
			if previous, exists := model.content[turn.AttentionID]; exists {
				if !reflect.DeepEqual(previous, turn.Message) {
					return delegateAttentionFoldModel{}, errors.New("conflicting attention content")
				}
			} else {
				model.content[turn.AttentionID] = turn.Message
				model.order = append(model.order, turn.AttentionID)
			}
		}
		resolution := turn.AttentionResolution
		if resolution == nil {
			if turn.Kind == schema.TurnAttentionResolution {
				return delegateAttentionFoldModel{}, errors.New("missing attention resolution")
			}
			continue
		}
		if turn.Kind != schema.TurnAttentionResolution || turn.AttentionID != "" || resolution.AttentionID == "" {
			return delegateAttentionFoldModel{}, errors.New("invalid attention resolution")
		}
		disposition := delegateAttentionResolution(resolution.Disposition)
		if disposition != delegateAttentionConsumed && disposition != delegateAttentionDiscarded {
			return delegateAttentionFoldModel{}, errors.New("invalid attention disposition")
		}
		if _, exists := model.content[resolution.AttentionID]; !exists {
			return delegateAttentionFoldModel{}, errors.New("resolution before attention")
		}
		if previous, exists := model.resolutions[resolution.AttentionID]; exists && previous != disposition {
			return delegateAttentionFoldModel{}, errors.New("conflicting attention resolution")
		}
		model.resolutions[resolution.AttentionID] = disposition
	}
	return model, nil
}

func delegateAttentionFuzzEntries(program []byte) []transcript.Entry {
	entries := make([]transcript.Entry, 0, len(program))
	for index, operation := range program {
		attentionID := fmt.Sprintf("attention-%d", (operation>>3)&1)
		callID := fmt.Sprintf("call-%d", (operation>>4)&1)
		deliveryID := fmt.Sprintf("delivery-%d", (operation>>5)&1)
		var turn schema.Turn
		switch operation % 8 {
		case 0:
			turn = schema.NewTurn(schema.TurnSteering, llm.User("content-"+attentionID))
			turn.AttentionID = attentionID
		case 1:
			turn = schema.NewTurn(schema.TurnSteering, llm.User(fmt.Sprintf("variant-%d", operation>>6)))
			turn.AttentionID = attentionID
		case 2:
			turn = delegateAttentionResolutionTurn(attentionID, delegateAttentionConsumed)
		case 3:
			turn = delegateAttentionResolutionTurn(attentionID, delegateAttentionDiscarded)
		case 4:
			turn = schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed(callID, "delegate_send", "done", false))
			turn.DelegateDeliveryCommits = []schema.DelegateDeliveryCommit{{ToolCallID: callID, DeliveryID: deliveryID}}
		case 5:
			turn = schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed(callID, "delegate_send", "done", false))
			turn.DelegateDeliveryCommits = []schema.DelegateDeliveryCommit{{ToolCallID: callID + "-wrong", DeliveryID: deliveryID}}
		case 6:
			turn = schema.NewTurn(schema.TurnUserInput, llm.User("wrong-kind"))
			turn.DelegateDeliveryCommits = []schema.DelegateDeliveryCommit{{ToolCallID: callID, DeliveryID: deliveryID}}
		case 7:
			turn = schema.NewTurn(schema.TurnHookCompleted, llm.System("presentational"))
		}
		entries = append(entries, transcript.Entry{Seq: index, Turn: turn})
	}
	return entries
}

func assertDelegateAttentionFuzzFold(t *testing.T, got delegateAttentionFold, want delegateAttentionFoldModel) {
	t.Helper()
	if !reflect.DeepEqual(got.order, want.order) || !reflect.DeepEqual(got.content, want.content) || !reflect.DeepEqual(got.resolutions, want.resolutions) || !reflect.DeepEqual(got.deliveryCommits, want.deliveryCommits) {
		t.Fatalf("fold mismatch:\n got order=%#v content=%#v resolutions=%#v commits=%#v\nwant order=%#v content=%#v resolutions=%#v commits=%#v", got.order, got.content, got.resolutions, got.deliveryCommits, want.order, want.content, want.resolutions, want.deliveryCommits)
	}
	wantPending := make([]string, 0, len(want.order))
	for _, attentionID := range want.order {
		if _, resolved := want.resolutions[attentionID]; !resolved {
			wantPending = append(wantPending, attentionID)
		}
	}
	if gotPending := got.pendingIDs(); !reflect.DeepEqual(gotPending, wantPending) {
		t.Fatalf("pending IDs = %#v, want %#v", gotPending, wantPending)
	}
}
