package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/llm"
)

// fakeStrategyHost is a minimal strategyHost implementation that returns canned
// values and records Emit calls. It proves that context strategies operate
// through the strategyHost seam without a real Session.
type fakeStrategyHost struct {
	stateDir string
	id       string
	profile  ProviderProfile
	client   *llm.Client
	emitted  []events.EventKind
	sideFx   int // number of WithResponseSideEffects invocations
}

func (h *fakeStrategyHost) Emit(kind events.EventKind, _ events.EventData) {
	h.emitted = append(h.emitted, kind)
}

func (h *fakeStrategyHost) WithResponseSideEffects(_ context.Context, fn func()) error {
	h.sideFx++
	fn()
	return nil
}

func (h *fakeStrategyHost) StateDir() string          { return h.stateDir }
func (h *fakeStrategyHost) ID() string                { return h.id }
func (h *fakeStrategyHost) Profile() ProviderProfile  { return h.profile }
func (h *fakeStrategyHost) Snapshot() SessionSnapshot { return SessionSnapshot{ID: h.id} }
func (h *fakeStrategyHost) Client() *llm.Client       { return h.client }

func TestStrategyHost_FakeSatisfiesInterface(t *testing.T) {
	var _ strategyHost = (*fakeStrategyHost)(nil)
}

// TestSessionLogStrategy_OperatesWithFakeHost proves a strategy can be
// constructed from and drive its work through a fake strategyHost: the log
// path is built from the host's StateDir/ID, and AfterAction routes its
// summary side-effects through the host's WithResponseSideEffects + Emit.
func TestSessionLogStrategy_OperatesWithFakeHost(t *testing.T) {
	entry := SessionLogEntry{
		Action:  "shell",
		Summary: "Ran the tests; all passed.",
		Outcome: "success",
	}
	entryJSON, _ := json.Marshal(entry)

	adapter := &stubSummarizeAdapter{
		name: "openai",
		respFn: func(_ llm.Request) (llm.Response, error) {
			return llm.Response{Message: llm.Assistant(string(entryJSON))}, nil
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	profile := testOpenAIProfileWithContextWindow(1000)
	dir := t.TempDir()
	host := &fakeStrategyHost{
		stateDir: dir,
		id:       "FAKE-1",
		profile:  profile,
		client:   client,
	}

	// Constructor accepts the fake host (not a real Session) and uses
	// StateDir/ID to build the log path.
	sls, err := newSessionLogStrategy(newContextManager(profile, client), host)
	if err != nil {
		t.Fatalf("newSessionLogStrategy with fake host: %v", err)
	}

	turns := []Turn{
		{Kind: TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
					ID:        "c1",
					Name:      "shell",
					Arguments: json.RawMessage(`{"command":"go test ./..."}`),
				}},
			},
		}},
		{Kind: TurnToolResults, Message: llm.ToolResultNamed("c1", "shell", "PASS", false)},
	}

	if err := sls.AfterAction(context.Background(), turns, client); err != nil {
		t.Fatalf("AfterAction: %v", err)
	}

	// Side-effects were routed through the host seam.
	if host.sideFx != 1 {
		t.Errorf("expected 1 WithResponseSideEffects call, got %d", host.sideFx)
	}
	if len(host.emitted) != 1 || host.emitted[0] != events.EventForkSummary {
		t.Errorf("expected one EventForkSummary emit, got %v", host.emitted)
	}
	// The log entry was persisted via the host-derived path (StateDir + ID).
	if got := sls.log.Len(); got != 1 {
		t.Fatalf("expected 1 log entry, got %d", got)
	}
	wantLog := filepath.Join(dir, "sessions", "FAKE-1.log.jsonl")
	if _, err := os.Stat(wantLog); err != nil {
		t.Errorf("expected log file at host-derived path %q: %v", wantLog, err)
	}
}
