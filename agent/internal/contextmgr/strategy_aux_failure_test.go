package contextmgr

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// errAuxProviderDown is the scripted auxiliary-provider failure an opt-in
// memory strategy must surface to its caller instead of swallowing.
var errAuxProviderDown = errors.New("aux provider down")

// failingAuxAdapter fails every completion, standing in for an auxiliary
// cheap-model outage at the LLM boundary.
func failingAuxAdapter() *stubSummarizeAdapter {
	return &stubSummarizeAdapter{
		name: "openai",
		respFn: func(llm.Request) (llm.Response, error) {
			return llm.Response{}, errAuxProviderDown
		},
	}
}

// TestSessionLogStrategy_AfterAction_SurfacesForkSummarizeFailure proves the
// opt-in session-log strategy reports its auxiliary summarization failure to
// the caller (which surfaces it as a warning) instead of swallowing it as a
// silent success, while still not appending a bogus log entry.
func TestSessionLogStrategy_AfterAction_SurfacesForkSummarizeFailure(t *testing.T) {
	client := llm.NewClient()
	client.Register(failingAuxAdapter())

	profile := testOpenAIProfileWithContextWindow(1000)
	sls := &SessionLogStrategy{
		cm:      NewManager(profile, client, cheapmodel.New(client)),
		log:     mustNewSessionLog(t, filepath.Join(t.TempDir(), "test.log.jsonl")),
		session: &fakeStrategyHost{profile: profile},
	}

	turns := []schema.Turn{
		{Kind: schema.TurnAssistant, Message: llm.Assistant("hello")},
	}

	err := sls.AfterAction(context.Background(), turns, client)
	if !errors.Is(err, errAuxProviderDown) {
		t.Fatalf("AfterAction must surface the auxiliary failure, got %v", err)
	}
	if sls.log.Len() != 0 {
		t.Errorf("a failed summarize must not append, got %d entries", sls.log.Len())
	}
}

// TestMemoryCrystalsStrategy_AfterAction_SurfacesCrystallizeFailure proves the
// opt-in crystals strategy reports its failed crystallization, and keeps
// reporting it across turns whose cadence guard skips an attempt, so a
// persistently degraded strategy stays observable instead of clearing between
// attempts.
func TestMemoryCrystalsStrategy_AfterAction_SurfacesCrystallizeFailure(t *testing.T) {
	client := llm.NewClient()
	client.Register(failingAuxAdapter())

	profile := testOpenAIProfileWithContextWindow(1000)
	s := NewMemoryCrystalsStrategy(NewManager(profile, client, cheapmodel.New(client)))

	three := make([]schema.Turn, 3)
	for i := range three {
		three[i] = schema.NewTurn(schema.TurnAssistant, llm.Assistant("turn"))
	}

	err := s.AfterAction(context.Background(), three, client)
	if !errors.Is(err, errAuxProviderDown) {
		t.Fatalf("AfterAction must surface the auxiliary failure, got %v", err)
	}
	if len(s.crystals) != 0 {
		t.Errorf("a failed crystallization must not bank a crystal, got %d", len(s.crystals))
	}

	// A skipped turn does no work but must keep the degradation observable.
	err = s.AfterAction(context.Background(), three[:2], client)
	if !errors.Is(err, errAuxProviderDown) {
		t.Fatalf("a skipped turn while degraded must keep reporting the failure, got %v", err)
	}
}

// TestRecursiveDistillStrategy_AfterAction_SurfacesMicroSummaryFailure proves
// the opt-in recursive-distill strategy reports its failed micro-summary and
// leaves its cadence watermark unchanged, so the failure stays observable
// while the eligible action is retried.
func TestRecursiveDistillStrategy_AfterAction_SurfacesMicroSummaryFailure(t *testing.T) {
	client := llm.NewClient()
	client.Register(failingAuxAdapter())

	profile := testOpenAIProfileWithContextWindow(1000)
	s := NewRecursiveDistillStrategy(NewManager(profile, client, cheapmodel.New(client)))

	ten := make([]schema.Turn, 10)
	for i := range ten {
		ten[i] = schema.NewTurn(schema.TurnAssistant, llm.Assistant("turn"))
	}

	// The cadence counts completed actions (CORE-09): nine ineligible actions
	// keep the failed summary out of reach and must return nil rather than a
	// stale failure.
	for i := range 9 {
		if err := s.AfterAction(context.Background(), ten, client); err != nil {
			t.Fatalf("ineligible action %d must not report: %v", i, err)
		}
	}

	// The tenth completed action is eligible: the failed micro-summary surfaces
	// and must not advance the action watermark.
	err := s.AfterAction(context.Background(), ten, client)
	if !errors.Is(err, errAuxProviderDown) {
		t.Fatalf("AfterAction must surface the auxiliary failure, got %v", err)
	}
	if len(s.microSummaries) != 0 {
		t.Errorf("a failed micro-summary must not be banked, got %d", len(s.microSummaries))
	}
	if s.lastMicroAt != 0 {
		t.Errorf("a failed micro-summary must not advance the cadence watermark, got %d", s.lastMicroAt)
	}

	// The unchanged watermark makes the next action eligible again: the retry
	// must keep reporting rather than swallowing the outage.
	err = s.AfterAction(context.Background(), ten, client)
	if !errors.Is(err, errAuxProviderDown) {
		t.Fatalf("an eligible retry must keep reporting the failure, got %v", err)
	}
}
