package agent

// The fallback chain used to receive a message slice built eagerly on every
// round, whether or not a fallback ever ran (#6442). It now receives the
// round's unprojected source (system prompt plus expanded history) and lays its
// messages out when a fallback actually runs. These tests pin the two halves of
// that equivalence: the layout it produces is the slice the eager build
// produced, and a round that never falls back records no source at all.

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/llm"
)

// TestRequestMessagesLaysOutTheSystemPromptAndHistory pins the layout a request
// dispatches and the fallback lays out: the system prompt leads as its own
// message, or, when the session folds it into the first user message, as that
// message's leading text. A history starting with another role takes the
// separate-message form either way.
func TestRequestMessagesLaysOutTheSystemPromptAndHistory(t *testing.T) {
	t.Parallel()
	const system = " \t\r\n "
	histories := map[string][]llm.Message{
		"empty":            nil,
		"leading-user":     {llm.User("first"), llm.Assistant("answer"), llm.User("second")},
		"leading-non-user": {llm.Assistant("earlier answer"), llm.User("now")},
		"single-user":      {llm.User("only")},
	}
	for _, tc := range []struct {
		name         string
		promptAsUser bool
	}{
		{name: "system-message"},
		{name: "prompt-as-user", promptAsUser: true},
	} {
		s := newSession(t, withConfig(SessionConfig{SystemPromptAsUser: tc.promptAsUser, testOnly: testConfig{skipGitSnapshot: true}}))
		for name, history := range histories {
			laid := s.requestMessages(system, history)
			// A prompt-as-user session folds the prompt into a leading user
			// message; any other shape keeps it as its own message.
			folds := tc.promptAsUser && len(history) > 0 && history[0].Role == llm.RoleUser
			if folds {
				if got := len(laid); got != len(history) {
					t.Errorf("%s/%s: laid out %d messages, want %d: the folded prompt rides the first user message", tc.name, name, got, len(history))
					continue
				}
				if got := laid[0].Text(); got != system+"\n\n"+history[0].Text() {
					t.Errorf("%s/%s: first message does not lead with the system prompt", tc.name, name)
				}
				if laid[0].Role != llm.RoleUser {
					t.Errorf("%s/%s: folded prompt landed on role %q, want user", tc.name, name, laid[0].Role)
				}
				continue
			}
			if got := len(laid); got != len(history)+1 {
				t.Errorf("%s/%s: laid out %d messages, want %d", tc.name, name, got, len(history)+1)
				continue
			}
			wantRole := llm.RoleSystem
			if tc.promptAsUser {
				wantRole = llm.RoleUser
			}
			if laid[0].Role != wantRole || laid[0].Text() != system {
				t.Errorf("%s/%s: leading message role %q text %q, want role %q carrying the system prompt", tc.name, name, laid[0].Role, laid[0].Text(), wantRole)
			}
			rest := laid[1:]
			if len(history) == 0 {
				rest = nil
			}
			if eq, _, _ := jsonEqual(t, rest, history); !eq {
				t.Errorf("%s/%s: the history does not follow the leading message unchanged", tc.name, name)
			}
		}
	}
}

// TestCompletedRoundLeavesNoFallbackSource pins that a round which runs to
// completion hands its source to the model call and leaves nothing behind, so
// a later round or a later fallback never reads an earlier round's input.
func TestCompletedRoundLeavesNoFallbackSource(t *testing.T) {
	t.Parallel()
	sess := newSession(t,
		withAdapter(&agenttest.ScriptedAdapter{
			Provider:  "openai",
			Responder: func(llm.Request) llm.Response { return agenttest.FinalResponse("done") },
		}),
		withConfig(SessionConfig{testOnly: testConfig{skipGitSnapshot: true}}),
	)
	// TRIPWIRE: scripted in-process adapter; 30s only catches a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "task", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.mu.Lock()
	source, set := sess.roundFallbackSource, sess.roundFallbackSourceSet
	sess.mu.Unlock()
	if set {
		t.Fatalf("a completed round left its fallback source behind: %#v", source)
	}
}

// TestRoundWithoutFallbackRecordsNoSource pins the removed work: a round whose
// preparation completes records nothing once its model call has taken the
// source, so no later round can inherit this round's input and no eager
// message slice survives the round.
func TestRoundWithoutFallbackRecordsNoSource(t *testing.T) {
	t.Parallel()
	s, _ := newRequestBuildBenchSession(t)
	route := s.priceRoundRoute(s.currentProfile(), llm.Request{})
	s.setRoundFallbackSource("system prompt", []llm.Message{llm.User("first")}, nil, replayScope{}, route)
	if source, ok := s.takeRoundFallbackSource(); !ok || source.system != "system prompt" || len(source.history) != 1 ||
		source.route.toolBytes != route.toolBytes || source.route.resolved.Instance != route.resolved.Instance {
		t.Fatalf("round source did not hold the recorded input: %#v", source)
	}
	if source, ok := s.takeRoundFallbackSource(); ok {
		t.Fatalf("a taken source stayed behind: %#v", source)
	}
}
