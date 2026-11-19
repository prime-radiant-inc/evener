package agent

import (
	"context"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm"
)

// TestSetModelOn_ExplicitProviderDecidesTheInstance pins the two readings of a
// switch. A picker that names an instance lands the switch on that instance
// even when the current instance serves the same model id under a namespaced
// name, which is the OpenRouter case: "anthropic/claude-opus-5" is one of
// OpenRouter's own ids, so the joined ref alone cannot say whether the reader
// chose OpenRouter's namespaced row or the direct Anthropic one. A picker that
// names only a ref keeps the current instance's reading.
func TestSetModelOn_ExplicitProviderDecidesTheInstance(t *testing.T) {
	t.Parallel()
	const (
		anthropicModel  = "claude-opus-5"
		openRouterModel = "anthropic/claude-opus-5"
	)
	cases := []struct {
		name      string
		base      *provider.Profile
		set       func(*Session) error
		wantID    string
		wantModel string
	}{
		{
			name:      "explicit Anthropic leaves OpenRouter for the direct row",
			base:      newOpenAICompatProfile("openrouter", openRouterModel, 0),
			set:       func(s *Session) error { return setModelOn(s, "anthropic", anthropicModel) },
			wantID:    "anthropic",
			wantModel: anthropicModel,
		},
		{
			name:      "explicit OpenRouter leaves Anthropic for the namespaced row",
			base:      newAnthropicProfile(anthropicModel),
			set:       func(s *Session) error { return setModelOn(s, "openrouter", openRouterModel) },
			wantID:    "openrouter",
			wantModel: openRouterModel,
		},
		{
			name:      "omitted provider keeps the namespaced id on the current instance",
			base:      newOpenAICompatProfile("openrouter", "openai/gpt-5.2", 0),
			set:       func(s *Session) error { return setModel(s, openRouterModel) },
			wantID:    "openrouter",
			wantModel: openRouterModel,
		},
		{
			name:      "omitted provider keeps an unnamespaced id on the current instance",
			base:      newAnthropicProfile("claude-sonnet-5"),
			set:       func(s *Session) error { return setModel(s, anthropicModel) },
			wantID:    "anthropic",
			wantModel: anthropicModel,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newSession(t,
				withAdapter(&fakeAdapter{name: "openrouter"}),
				withAdapter(&fakeAdapter{name: "anthropic"}),
				withProfile(tc.base),
				withConfig(SessionConfig{
					ResolveProfile: testResolverFull,
					testOnly:       testConfig{skipGitSnapshot: true},
				}),
			)
			if err := tc.set(s); err != nil {
				t.Fatalf("switch: %v", err)
			}
			got := s.currentProfile()
			if got.ID() != tc.wantID || got.Model() != tc.wantModel {
				t.Fatalf("profile = %s/%s, want %s/%s", got.ID(), got.Model(), tc.wantID, tc.wantModel)
			}
		})
	}
}

// TestModelPickOnExplicitProviderQueuesToTheTurnBoundary pins the queued path:
// a pick made while an input is in flight is stored with the instance it
// named, and the boundary applies both halves, so the turn queued behind the
// pick runs on the direct instance rather than the current one's namespaced id.
func TestModelPickOnExplicitProviderQueuesToTheTurnBoundary(t *testing.T) {
	openRouter := newFakeAdapter("openrouter", func(llm.Request) llm.Response {
		return agenttest.CommunicateResponse(true, "first turn done")
	})
	anthropic := newFakeAdapter("anthropic", func(llm.Request) llm.Response {
		return agenttest.CommunicateResponse(true, "queued turn done")
	})
	dir := t.TempDir()
	s := newSession(t,
		withAdapter(openRouter),
		withAdapter(anthropic),
		withDir(dir),
		withProfile(newOpenAICompatProfile("openrouter", "anthropic/claude-opus-5", 0)),
		withConfig(SessionConfig{
			StateDir:         dir,
			MaxSubagentDepth: 1,
			ResolveProfile:   testResolverFull,
			testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
		}),
	)
	serveSessionWithMutationStore(t, s)
	queueOneMutation(t, s, "cm-explicit-provider-boundary", "the message queued behind the first turn")

	var picked atomic.Bool
	updateSessionTestConfig(s, func(cfg *testConfig) {
		cfg.afterCommunicateBoundary = func(sess *Session) {
			if picked.Swap(true) {
				return
			}
			if err := setModelOn(sess, "anthropic", "claude-opus-5"); err != nil {
				t.Errorf("setModelOn mid-input: %v", err)
			}
		}
	})

	if _, err := s.ProcessInput(context.Background(), "the first message", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if !picked.Load() {
		t.Fatal("the mid-input pick never ran")
	}

	first := openRouter.Requests()
	if len(first) != 1 || first[0].Provider != "openrouter" || first[0].Model != "anthropic/claude-opus-5" {
		t.Fatalf("the interrupted turn ran %d request(s) %v, want one on openrouter/anthropic/claude-opus-5", len(first), requestTargets(first))
	}
	queued := anthropic.Requests()
	if len(queued) != 1 || queued[0].Provider != "anthropic" || queued[0].Model != "claude-opus-5" {
		t.Fatalf("the turn queued behind the pick ran %d request(s) %v, want one on anthropic/claude-opus-5", len(queued), requestTargets(queued))
	}
	if got := s.currentProfile(); got.ID() != "anthropic" || got.Model() != "claude-opus-5" {
		t.Fatalf("profile after the boundary = %s/%s, want anthropic/claude-opus-5", got.ID(), got.Model())
	}
}

// requestTargets renders the provider/model of each request for failure
// messages.
func requestTargets(reqs []llm.Request) []string {
	out := make([]string, len(reqs))
	for i, req := range reqs {
		out[i] = req.Provider + "/" + req.Model
	}
	return out
}
