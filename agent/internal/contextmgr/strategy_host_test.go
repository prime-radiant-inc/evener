package contextmgr

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/sessionlog"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// fakeStrategyHost is a minimal Host implementation that returns canned values
// and records Emit calls. It proves that context strategies operate through the
// Host seam without a real Session.
type fakeStrategyHost struct {
	stateDir string
	id       string
	profile  *provider.Profile
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

func (h *fakeStrategyHost) StateDir() string           { return h.stateDir }
func (h *fakeStrategyHost) ID() string                 { return h.id }
func (h *fakeStrategyHost) Profile() *provider.Profile { return h.profile }

func TestStrategyHost_FakeSatisfiesInterface(t *testing.T) {
	var _ Host = (*fakeStrategyHost)(nil)
}

// TestSessionLogStrategy_OperatesWithFakeHost proves a strategy can be
// constructed from and drive its work through a fake strategyHost: the log
// path is built from the host's StateDir/ID, and AfterAction routes its
// summary side-effects through the host's WithResponseSideEffects + Emit.
func TestSessionLogStrategy_OperatesWithFakeHost(t *testing.T) {
	entry := sessionlog.SessionLogEntry{
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
	}

	// Constructor accepts the fake host (not a real Session) and uses
	// StateDir/ID to build the log path.
	sls, err := NewSessionLogStrategy(NewManager(profile, client), host)
	if err != nil {
		t.Fatalf("newSessionLogStrategy with fake host: %v", err)
	}

	turns := []schema.Turn{
		{Kind: schema.TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
					ID:        "c1",
					Name:      "shell",
					Arguments: json.RawMessage(`{"command":"go test ./..."}`),
				}},
			},
		}},
		{Kind: schema.TurnToolResults, Message: llm.ToolResultNamed("c1", "shell", "PASS", false)},
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

func TestSessionLogStrategy_AttentionResolutionDoesNotDisplaceRecentContext(t *testing.T) {
	entryJSON, _ := json.Marshal(sessionlog.SessionLogEntry{Action: "assistant", Summary: "ok", Outcome: "success"})
	adapter := &stubSummarizeAdapter{
		name: "openai",
		respFn: func(_ llm.Request) (llm.Response, error) {
			return llm.Response{Message: llm.Assistant(string(entryJSON))}, nil
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	profile := testOpenAIProfileWithContextWindow(1000)
	host := &fakeStrategyHost{stateDir: t.TempDir(), id: "marker-window", profile: profile}
	strategy, err := NewSessionLogStrategy(NewManager(profile, client), host)
	if err != nil {
		t.Fatalf("NewSessionLogStrategy: %v", err)
	}
	history := []schema.Turn{schema.NewTurn(schema.TurnUserInput, llm.User("oldest visible recent evidence"))}
	for range 9 {
		history = append(history, schema.NewTurn(schema.TurnAssistant, llm.Assistant("visible action")))
	}
	marker := schema.NewTurn(schema.TurnAttentionResolution, llm.System("private marker"))
	marker.AttentionResolution = &schema.AttentionResolutionInfo{AttentionID: "private", Disposition: "consumed"}
	history = append(history, marker)

	if err := strategy.AfterAction(context.Background(), history, client); err != nil {
		t.Fatalf("AfterAction: %v", err)
	}
	prompt := adapter.lastReq.Messages[0].Text()
	if !strings.Contains(prompt, "oldest visible recent evidence") {
		t.Fatalf("private marker displaced visible recent evidence: %s", prompt)
	}
	if strings.Contains(prompt, "private marker") {
		t.Fatalf("private marker reached side provider: %s", prompt)
	}
}
