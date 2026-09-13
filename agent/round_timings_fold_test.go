package agent

import (
	"context"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func TestRoundTimingsFoldPublicationSurvivesReloadWithoutProviderHistory(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	proceed := make(chan struct{})
	var calls atomic.Int32
	cheap := &agenttest.ScriptedAdapter{
		Provider: "round-timings-fold-cheap",
		Responder: func(llm.Request) llm.Response {
			if calls.Add(1) == 1 {
				close(entered)
				<-proceed
			}
			return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nsummary\n[END SUMMARY]")}
		},
	}
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	client.Register(cheap)
	stateDir := t.TempDir()
	s := newSession(t,
		withClient(client),
		withProfile(WithCheapModel(NewOpenAIProfile("gpt-5.2"), "round-timings-fold-cheap/model")),
		withConfig(SessionConfig{MaxSubagentDepth: 1, NoProjectPrompts: true, StateDir: stateDir}),
		withoutGitSnapshot(),
	)
	seedNumberedSessionHistory(t, s, 12)
	_, _, doneEvents := collectEvents(s)

	compactErr := make(chan error, 1)
	go func() { compactErr <- s.Compact(context.Background()) }()
	<-entered

	want := events.RoundTimings{
		Round:         7,
		SystemPrompt:  11 * time.Millisecond,
		ContextMgmt:   13 * time.Millisecond,
		HistoryExpand: 17 * time.Millisecond,
		ToolDefs:      19 * time.Millisecond,
		LLMCall:       23 * time.Millisecond,
		ToolExec:      29 * time.Millisecond,
		Persistence:   31 * time.Millisecond,
		AfterAction:   37 * time.Millisecond,
		LoopOverhead:  41 * time.Millisecond,
		TotalRound:    43 * time.Millisecond,
	}
	// This append runs while the real fold is between its snapshot and
	// publication. The fold must merge this durable turn into its published
	// history and rewrite it inside its own run, tagged with the fold id its
	// marker carries.
	s.persistAndEmitRoundTimings(want)
	close(proceed)
	if err := <-compactErr; err != nil {
		t.Fatalf("Compact: %v", err)
	}

	data, err := readTranscriptFull(transcriptPath(s.stateDir, s.id))
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	s.Close()
	<-doneEvents

	var restored []schema.Turn
	for _, turn := range ResumeHistory(data.Entries) {
		if turn.Kind == schema.TurnRoundTimings {
			restored = append(restored, turn)
		}
	}
	if len(restored) != 1 || restored[0].RoundTimings == nil {
		t.Fatalf("reloaded round timing records = %+v, want exactly one semantic record", restored)
	}
	if !reflect.DeepEqual(*restored[0].RoundTimings, want.Timings()) {
		t.Fatalf("reloaded round timings = %+v, want %+v", *restored[0].RoundTimings, want)
	}
	wantAnnouncement := want.Timings().Announcement()

	reloadAdapter := &agenttest.ScriptedAdapter{
		Provider: "openai",
		Responder: func(llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant("unused")}
		},
	}
	reloadClient := llm.NewClient()
	reloadClient.Register(reloadAdapter)
	reloaded, err := RestoreSessionFromMeta(reloadClient, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), s.Meta(), stateDir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer reloaded.Close()
	_, _, _, request, _, _, err := reloaded.prepareModelRequestWithError(context.Background(), 0, &events.RoundTimings{})
	if err != nil {
		t.Fatalf("prepareModelRequestWithError: %v", err)
	}
	for _, message := range request.Messages {
		if message.Text() == wantAnnouncement {
			t.Fatalf("provider request contains persisted round timing announcement: %q", message.Text())
		}
	}
}

// The persisted timing record names the logical turn it belongs to, and the
// live event has to name the same one. Without it the projector resolves
// ownership from whatever turn is running when the event arrives — mutable
// state that a timing published as its turn ends can miss, grouping the round's
// timing under a different turn than the transcript does.
func TestRoundTimings_LiveEventCarriesThePersistedOwner(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	s := newSession(t, withConfig(SessionConfig{MaxSubagentDepth: 1, NoProjectPrompts: true, StateDir: stateDir}), withoutGitSnapshot())
	owner := s.nameTurnItself()

	evs, mu, done := collectEvents(s)
	s.persistAndEmitRoundTimings(events.RoundTimings{Round: 2, TotalRound: 5 * time.Millisecond})
	s.Close()
	<-done

	var persisted string
	for _, turn := range currentHistory(t, s) {
		if turn.Kind == schema.TurnRoundTimings {
			persisted = turn.OwningTurnID
		}
	}
	if persisted != owner {
		t.Fatalf("test setup: the persisted timing record is owned by %q, want the running turn %q", persisted, owner)
	}
	mu.Lock()
	defer mu.Unlock()
	seen := 0
	for _, event := range *evs {
		data, ok := event.Data.(events.RoundTimings)
		if !ok || event.Kind != events.EventRoundTimings {
			continue
		}
		seen++
		if data.OwningTurnID != persisted {
			t.Fatalf("the live round-timing event is owned by %q, want the owner its entry carries, %q", data.OwningTurnID, persisted)
		}
	}
	if seen != 1 {
		t.Fatalf("round-timing events on the stream = %d, want 1", seen)
	}
}
