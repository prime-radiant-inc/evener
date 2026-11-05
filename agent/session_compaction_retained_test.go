package agent

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func TestCompactionRetainedNoteOrder(t *testing.T) {
	t.Parallel()
	s := newScriptedSummaryCompactSession(t, "review-note-order", func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\n## Progress\nopaque-review-summary\n[END SUMMARY]")}
	}, withStateDir(t.TempDir()))
	if err := s.Rename("Retained fold fixture"); err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		turn := schema.NewTurn(schema.TurnUserInput, llm.User(fmt.Sprintf("opaque-input-%d", i)))
		s.recordTurn(turn, turn)
	}
	s.setPinnedNote("opaque-note-527")
	if err := s.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	live := currentHistory(t, s)
	if retainedNoteCount(live) != 1 {
		t.Fatal("note not retained live")
	}
	entries := readPersistedTranscriptEntries(t, s.stateDir, s.id)
	resumed := ResumeHistory(entries)
	if !reflect.DeepEqual(retainedTailTexts(resumed), retainedTailTexts(live)) {
		t.Fatalf("fold order changed: live=%q resumed=%q", retainedTailTexts(live), retainedTailTexts(resumed))
	}
}

func TestCompactionRetainedRestoreThenFoldKeepsNote(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	summarize := func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\n## Progress\nopaque-review-summary\n[END SUMMARY]")}
	}
	s := newScriptedSummaryCompactSession(t, "review-note-repeat", summarize, withStateDir(dir))
	if err := s.Rename("Retained fold fixture"); err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		turn := schema.NewTurn(schema.TurnUserInput, llm.User(fmt.Sprintf("opaque-input-%d", i)))
		s.recordTurn(turn, turn)
	}
	s.setPinnedNote("opaque-note-527")
	if err := s.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	id := s.ID()
	s.Close()
	restored := restoreQueuePersistTestSession(t, dir, id)
	defer restored.Close()
	restored.client.Register(&agenttest.ScriptedAdapter{Provider: "openai", Responder: func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\n## Progress\nopaque-review-restored-summary\n[END SUMMARY]")}
	}})
	// Keep the restored six-turn suffix while a real fold replaces its marker.
	restored.contextMgr.PreserveRecentTurns = len(currentHistory(t, restored)) - 1
	markerCount := func() int {
		n := 0
		for _, e := range readPersistedTranscriptEntries(t, dir, id) {
			if e.Turn.Kind == schema.TurnCheckpoint || e.Turn.Kind == schema.TurnSummary {
				n++
			}
		}
		return n
	}
	markersBefore := markerCount()
	before := currentHistory(t, restored)
	if retainedNoteCount(before) != 1 {
		t.Fatal("note missing before refold")
	}
	if err := restored.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	if markerCount() <= markersBefore {
		t.Fatal("second fold wrote no new marker")
	}
	live := currentHistory(t, restored)
	if retainedNoteCount(live) != 1 {
		t.Fatalf("scenario did not retain note live: %q", retainedTailTexts(live))
	}
	entries := readPersistedTranscriptEntries(t, dir, id)
	resumed := ResumeHistory(entries)
	if retainedNoteCount(resumed) != 1 {
		t.Fatalf("retained note lost after restore and refold: live=%q resumed=%q", retainedTailTexts(live), retainedTailTexts(resumed))
	}
}

func retainedNoteCount(turns []schema.Turn) int {
	n := 0
	for _, turn := range turns {
		if turn.SteeringKind == events.SteeringKindNoteHandoff {
			n++
		}
	}
	return n
}

func retainedTailTexts(turns []schema.Turn) []string {
	var out []string
	for _, turn := range turns {
		if turn.Kind != schema.TurnSummary && turn.Kind != schema.TurnCheckpoint {
			out = append(out, turn.Message.Text())
		}
	}
	return out
}

func TestCompactionRetainedSnapshotPrivateIdentity(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := newScriptedSummaryCompactSession(t, "review-private-snapshot", func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\n## Progress\nopaque-summary\n[END SUMMARY]")}
	}, withStateDir(dir))
	if err := s.Rename("Retained fold fixture"); err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		turn := schema.NewTurn(schema.TurnUserInput, llm.User(fmt.Sprintf("opaque-padding-%d", i)))
		s.recordTurn(turn, turn)
	}
	call := llm.ToolCallData{ID: "opaque-call-551", Name: "read_transcript", Arguments: []byte(`{"transcript_ref":"opaque-ref-551"}`)}
	assistant := schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}})
	s.recordTurn(assistant, assistant)
	toolTurn := func(text string) schema.Turn {
		return schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: call.ID, Name: call.Name, Content: text}}}})
	}
	live := toolTurn("opaque-private-551")
	persisted := live
	persisted.Message = toolTurn("opaque-public-551").Message
	persisted.DelegateDeliveryCommits = []schema.DelegateDeliveryCommit{{ToolCallID: call.ID, DeliveryID: "opaque-delivery-551"}}
	s.recordTurn(live, persisted)
	entries := readPersistedTranscriptEntries(t, dir, s.id)
	original := len(entries) - 1
	expected := entries[original].Turn
	check := func(sess *Session) {
		t.Helper()
		entries := readPersistedTranscriptEntries(t, dir, sess.ID())
		for _, entry := range entries {
			for _, text := range toolResultContents([]schema.Turn{entry.Turn}) {
				if text == "opaque-private-551" {
					t.Fatal("private live tool evidence persisted")
				}
			}
		}
		resumed := ResumeHistory(entries)
		n := 0
		for _, turn := range resumed {
			for _, text := range toolResultContents([]schema.Turn{turn}) {
				if text != "opaque-public-551" {
					continue
				}
				n++
				if turn.OriginalOrdinal == nil || *turn.OriginalOrdinal != uint64(original) {
					t.Fatalf("wrong original ordinal: %+v", turn.OriginalOrdinal)
				}
				turn.OriginalOrdinal = nil
				if !reflect.DeepEqual(turn, expected) {
					t.Fatalf("persisted identity or metadata changed: got=%+v want=%+v", turn, expected)
				}
			}
		}
		if n != 1 {
			t.Fatalf("restored persisted tool records=%d, want 1", n)
		}
	}
	for range 2 {
		before := retainedFrom(readPersistedTranscriptEntries(t, dir, s.ID()))
		if err := s.Compact(context.Background()); err != nil {
			t.Fatal(err)
		}
		if retainedFrom(readPersistedTranscriptEntries(t, dir, s.ID())) <= before {
			t.Fatal("fold did not write a new final marker")
		}
		check(s)
	}
	id := s.ID()
	s.Close()
	restored := restoreQueuePersistTestSession(t, dir, id)
	defer restored.Close()
	restored.client.Register(&agenttest.ScriptedAdapter{Provider: "openai", Responder: func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\n## Progress\nopaque-review-restored-summary\n[END SUMMARY]")}
	}})
	check(restored)
	before := retainedFrom(readPersistedTranscriptEntries(t, dir, id))
	if err := restored.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	if retainedFrom(readPersistedTranscriptEntries(t, dir, id)) <= before {
		t.Fatal("restored fold did not write a new final marker")
	}
	check(restored)
}

func TestCompactionRetainedNoteOrdering(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	s := newScriptedSummaryCompactSession(t, "review-note-order", func(llm.Request) llm.Response {
		calls++
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\n## Progress\nopaque-review-summary\n[END SUMMARY]")}
	}, withStateDir(dir))
	if err := s.Rename("Retained fold fixture"); err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		turn := schema.NewTurn(schema.TurnUserInput, llm.User(fmt.Sprint("opaque-padding-", i)))
		s.recordTurn(turn, turn)
	}
	s.setPinnedNote("opaque-latest-note-927")
	if err := s.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls == 0 {
		t.Fatal("fold did not run")
	}
	sequence := func(h []schema.Turn) []string {
		var result []string
		for _, turn := range h {
			if turn.Kind == schema.TurnUserInput || turn.Kind == schema.TurnSteering {
				result = append(result, turn.Message.Text())
			}
		}
		return result
	}
	live := currentHistory(t, s)
	noteAt, lastInput := -1, -1
	for i, turn := range live {
		if turn.SteeringKind == events.SteeringKindNoteHandoff {
			noteAt = i
		}
		if turn.Kind == schema.TurnUserInput {
			lastInput = i
		}
	}
	if noteAt <= lastInput || lastInput < 0 {
		t.Fatalf("scenario missing later note: note=%d input=%d", noteAt, lastInput)
	}
	t.Logf("live history has note after retained input, note=%d lastInput=%d", noteAt, lastInput)
	want := sequence(live)
	id := s.ID()
	s.Close()
	restored := restoreQueuePersistTestSession(t, dir, id)
	defer restored.Close()
	got := sequence(currentHistory(t, restored))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("restored retained input/steering order changed: got %q want %q", got, want)
	}
	checkRetainedProviderOrder(t, restored, want)
}

func TestCompactionRetainedRepeatedConcurrentNote(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(fmt.Sprintf("restart-between-folds=%v", restart), func(t *testing.T) {
			dir := t.TempDir()
			entered, proceed := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			summarize := func(llm.Request) llm.Response {
				if calls.Add(1) == 1 {
					close(entered)
					<-proceed
				}
				return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\n## Progress\nopaque-review-summary\n[END SUMMARY]")}
			}
			s := newScriptedSummaryCompactSession(t, "review-repeat-concurrent-note", summarize, withStateDir(dir))
			if err := s.Rename("Retained fold fixture"); err != nil {
				t.Fatal(err)
			}
			for i := range 20 {
				turn := schema.NewTurn(schema.TurnUserInput, llm.User(fmt.Sprint("opaque-padding-", i)))
				s.recordTurn(turn, turn)
			}
			const note = "opaque-latest-note-928"
			s.setPinnedNote(note)
			done := make(chan error, 1)
			go func() { done <- s.Compact(context.Background()) }()
			<-entered
			correction := schema.NewTurn(schema.TurnUserInput, llm.User("opaque-concurrent-correction"))
			s.recordTurn(correction, correction)
			close(proceed)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			findNote := func(h []schema.Turn) int {
				for i, turn := range h {
					if strings.Contains(turn.Message.Text(), note) {
						return i
					}
				}
				return -1
			}
			live := currentHistory(t, s)
			noteAt := findNote(live)
			correctionAt := indexOfTurnText(live, "opaque-concurrent-correction")
			if noteAt < 0 || correctionAt <= noteAt {
				t.Fatalf("missing live sequence: note=%d correction=%d", noteAt, correctionAt)
			}
			t.Logf("first real fold kept note=%d before concurrent correction=%d", noteAt, correctionAt)
			data, err := readTranscriptFull(context.Background(), transcriptPath(s.stateDir, s.id), "")
			if err != nil {
				t.Fatal(err)
			}
			originalNote, originalCorrection := -1, -1
			for i, entry := range data.Entries {
				if entry.Turn.OriginalOrdinal != nil {
					continue
				}
				if strings.Contains(entry.Turn.Message.Text(), note) {
					originalNote = i
				}
				if entry.Turn.Message.Text() == "opaque-concurrent-correction" {
					originalCorrection = i
				}
			}
			if originalCorrection < 0 || originalNote <= originalCorrection {
				t.Fatalf("missing reversed durable original order: note=%d correction=%d", originalNote, originalCorrection)
			}
			t.Logf("original durable ordinals reverse that order: note=%d correction=%d", originalNote, originalCorrection)
			if restart {
				id := s.ID()
				s.Close()
				s = restoreQueuePersistTestSession(t, dir, id)
				defer s.Close()
				s.client.Register(&agenttest.ScriptedAdapter{Provider: "openai", Responder: summarize})
				s.contextMgr.PreserveRecentTurns = len(currentHistory(t, s)) - 1
			}
			before := calls.Load()
			markerBefore := retainedFrom(readPersistedTranscriptEntries(t, dir, s.ID()))
			if err := s.Compact(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !restart && calls.Load() <= before {
				t.Fatal("second real fold did not call summarizer")
			}
			if retainedFrom(readPersistedTranscriptEntries(t, dir, s.ID())) <= markerBefore {
				t.Fatal("second real fold wrote no new final marker")
			}
			if findNote(currentHistory(t, s)) < 0 {
				t.Fatal("scenario invalid: second fold dropped note from live history")
			}
			t.Log("second real fold kept note in live history")
			want := retainedTailTexts(currentHistory(t, s))
			id := s.ID()
			s.Close()
			restored := restoreQueuePersistTestSession(t, dir, id)
			defer restored.Close()
			if findNote(currentHistory(t, restored)) < 0 {
				t.Fatal("retained pinned-note handoff disappeared after second fold and restore")
			}
			if got := retainedTailTexts(currentHistory(t, restored)); !reflect.DeepEqual(got, want) {
				t.Fatalf("repeated fold changed published order: got %q want %q", got, want)
			}
			checkRetainedProviderOrder(t, restored, want)
		})
	}
}

func checkRetainedProviderOrder(t *testing.T, s *Session, want []string) {
	t.Helper()
	calls := 0
	s.profile = withTestSessionNamer(s.client, s.profile)
	s.client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{func(req llm.Request) llm.Response {
		calls++
		var got []string
		for _, message := range req.Messages {
			for _, part := range message.Content {
				if part.Kind != llm.ContentText {
					continue
				}
				for _, text := range want {
					if part.Text == text {
						got = append(got, part.Text)
						break
					}
				}
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("actual provider reordered or duplicated retained history: got %q want %q", got, want)
		}
		call := communicateCall("retained-order", "opaque-result-928")
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}}}
	}}})
	if _, err := s.ProcessInput(context.Background(), "opaque-after-restore-928", nil); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("actual provider calls=%d, want 1", calls)
	}
}
