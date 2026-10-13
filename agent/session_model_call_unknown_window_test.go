package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/contextmgr"
	"primeradiant.com/evener/llm"
)

// The unknown-window contract of issue #4564 at the session layer: a custom
// gateway serving a model nothing vouches for still gets context management.
// maybeWarnContextUsage warns against the same conservative window the context
// manager budgets compaction against, and a real session folds and discloses
// the assumption through its own event wiring.

// TestMaybeWarnContextUsage_UnknownWindowWarnsAgainstAssumption pins the
// warning half of issue #4564: a request that has filled the assumed window
// warns even though nothing vouches for the model's real window, and the
// warning's structured fields carry the assumption.
func TestMaybeWarnContextUsage_UnknownWindowWarnsAgainstAssumption(t *testing.T) {
	t.Parallel()
	sess := newSession(t)
	col := newChanCollector()
	go col.drain(sess)

	profile := newOpenAICompatProfile("gw", "deepseek-4.1-flash", 0)
	if profile.ContextWindowSize() != 0 {
		t.Fatalf("fixture window = %d, want 0 (unknown)", profile.ContextWindowSize())
	}
	// ~120K tokens by the char/4 estimate: ~94% of the assumed 128K window.
	bigReq := llm.Request{Messages: []llm.Message{llm.User(strings.Repeat("token ", 80_000))}}
	if !sess.maybeWarnContextUsage(profile, bigReq) {
		t.Fatal("expected a context-usage warning against the assumed window")
	}

	sess.Close()
	<-col.done
	var warned *events.WarningData
	for _, w := range col.warnings() {
		if w.Code == events.WarningCodeContextBudget {
			warned = &w
			break
		}
	}
	if warned == nil {
		t.Fatal("no context-budget warning emitted")
	}
	if warned.ContextWindowSize != contextmgr.AssumedContextWindow {
		t.Fatalf("warned against window %d, want the assumed %d", warned.ContextWindowSize, contextmgr.AssumedContextWindow)
	}
	if warned.Percent < 80 {
		t.Fatalf("warned at %d%%, want at least 80%% of the assumption", warned.Percent)
	}
}

// TestMaybeWarnContextUsage_UnknownWindowUnderAssumptionStaysQuiet guards the
// other direction: the assumption restores the warning's ordinary threshold
// behavior, it does not make every request on an unknown-window model warn.
func TestMaybeWarnContextUsage_UnknownWindowUnderAssumptionStaysQuiet(t *testing.T) {
	t.Parallel()
	sess := newSession(t)
	defer sess.Close()

	profile := newOpenAICompatProfile("gw", "deepseek-4.1-flash", 0)
	if sess.maybeWarnContextUsage(profile, llm.Request{Messages: []llm.Message{llm.User("hi")}}) {
		t.Fatal("a request far under the assumed window must not warn")
	}
}

// TestUnknownWindowSessionCompactsAndDiscloses drives the real per-request
// path: a session on the unknown-window gateway model folds once its exact API
// measurement outgrows the assumed window, and the assumption is disclosed
// exactly once through the session's own event wiring.
func TestUnknownWindowSessionCompactsAndDiscloses(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "gw",
		steps: []func(req llm.Request) llm.Response{
			// First round: a tool call whose reported usage already fills the
			// assumed window, the way a real gateway reports input tokens.
			func(req llm.Request) llm.Response {
				return llm.Response{
					Usage: llm.Usage{InputTokens: 150_000},
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
								ID:        "c1",
								Name:      "read_file",
								Arguments: json.RawMessage(`{"file_path":"big.txt"}`),
							}},
						},
					},
				}
			},
			func(req llm.Request) llm.Response { return finalResponse("done") },
		},
	})
	env := execenv.NewLocalExecutionEnvironment(dir)
	if _, err := env.WriteFile("big.txt", strings.Repeat("line of content\n", 200)); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	profile := newOpenAICompatProfile("gw", "deepseek-4.1-flash", 0)
	sess, err := NewSession(c, profile, env, SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	var evs []events.SessionEvent
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range sess.Events() {
			evs = append(evs, ev)
		}
	}()

	if _, err := sess.ProcessInput(context.Background(), "read the file", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()
	<-done

	var compactions, disclosures int
	for _, ev := range evs {
		if ev.Kind == events.EventContextCompaction {
			compactions++
		}
		if ev.Kind == events.EventWarning {
			if w, ok := ev.Data.(events.WarningData); ok && w.Code == events.WarningCodeContextWindowAssumed {
				disclosures++
			}
		}
	}
	if compactions == 0 {
		t.Fatal("unknown-window session never compacted under the assumed window")
	}
	if disclosures != 1 {
		t.Fatalf("assumption disclosures = %d, want exactly 1", disclosures)
	}
}
