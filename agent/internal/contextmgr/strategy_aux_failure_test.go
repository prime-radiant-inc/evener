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
