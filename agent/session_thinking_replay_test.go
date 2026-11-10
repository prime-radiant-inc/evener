package agent

// Regression tests for #4563: an OpenAI-chat target does not carry replayed
// reasoning as continuation state, so request assembly drops assistant
// thinking before it can be billed every round. The transcript keeps it; only
// the outgoing messages lose it, and a row that declares thinking_as_text (the
// adapter merges it into assistant text) is the deliberate exception.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// TestExpandHistory_OpenAIChat_DropThinkingStripsPriorAndInFlight covers both
// halves of the replay: a completed prior turn and the in-flight turn's own
// earlier round. The in-flight exemption belongs to the N4 provenance rules;
// the chat drop is a target policy and applies to every replayed message.
func TestExpandHistory_OpenAIChat_DropThinkingStripsPriorAndInFlight(t *testing.T) {
	t.Parallel()
	turns := []schema.Turn{
		assistantThinkingTurn("work", "kimi-k2", "kimi-k2"),
		assistantThinkingTurn("work", "kimi-k2", "kimi-k2"),
	}
	out := expandHistory(turns, replayScope{
		Instance: "work", Model: "kimi-k2", Protocol: registry.ProtocolOpenAIChat,
		InFlightFrom: 1, DropThinking: true, protocolOf: replayProtocolOf,
	})
	if hasContentKind(out, llm.ContentThinking) {
		t.Fatal("chat target replayed assistant thinking; the wire carries no continuation state in it")
	}
	if !hasContentKind(out, llm.ContentText) {
		t.Fatal("answer text must survive the thinking drop")
	}
	if got := len(out); got != len(turns) {
		t.Fatalf("expanded messages = %d, want %d: the drop must not remove whole turns", got, len(turns))
	}
}

// TestDropsReplayedThinking pins which targets take replayed thinking. Only an
// OpenAI-chat target drops it by default; the three protocols whose
// continuation state lives in the thinking (signature, thought signature,
// encrypted content) keep their provenance rules, and an explicit
// thinking_as_text row keeps it too.
func TestDropsReplayedThinking(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		profile *provider.Profile
		want    bool
	}{
		{"openai-chat", newOpenAICompatProfile("kimi", "kimi-k3", 0), true},
		{"openai-chat thinking_as_text", reasoningProfile("glm-5.3", registry.Caps{ThinkingAsText: new(true)}), false},
		{"anthropic", newAnthropicProfile("claude-opus-4-6"), false},
		{"openai-responses", NewOpenAIProfile("gpt-5.2"), false},
		{"google", newGeminiProfile("gemini-3-pro"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		if got := dropsReplayedThinking(tc.profile); got != tc.want {
			t.Errorf("dropsReplayedThinking(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestSession_ChatTargetRequestCarriesNoReplayedThinking drives one real turn
// and asserts the recorded provider request, not the expansion alone: a chat
// target's outgoing messages carry no ContentThinking, while the answer text
// stays.
func TestSession_ChatTargetRequestCarriesNoReplayedThinking(t *testing.T) {
	t.Parallel()
	adapter := &fakeAdapter{name: "lunaroute", steps: []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return agenttest.ToolCallResponse(communicateCall("c1", "ok")) },
	}}
	sess := newSession(t, withProfile(reasoningProfile("glm-5.3", registry.Caps{Reasoning: new(true)})), withAdapter(adapter))
	sess.appendTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "old chain of thought"}},
		{Kind: llm.ContentText, Text: "old answer"},
	}})

	// TRIPWIRE: scripted in-process adapter; 30s only catches a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "continue", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	reqs := adapter.Requests()
	if len(reqs) == 0 {
		t.Fatal("no provider request recorded")
	}
	req := reqs[len(reqs)-1]
	if hasContentKind(req.Messages, llm.ContentThinking) {
		t.Fatal("chat provider request carried replayed thinking")
	}
	if !hasContentKind(req.Messages, llm.ContentText) {
		t.Fatal("chat provider request lost the answer text")
	}
}

// TestExpandHistory_OpenAIChat_DropThinkingKeepsEncryptedDetails pins the
// continuation-state exception: an OpenRouter-style reasoning_details array is
// not the display text #4563 measured. The chat adapter must round-trip it, so
// the part survives with only its text cleared. (A signature-bearing item that
// needs the text is the next test's case.)
func TestExpandHistory_OpenAIChat_DropThinkingKeepsEncryptedDetails(t *testing.T) {
	t.Parallel()
	const compat = `[{"type":"reasoning.encrypted","data":"abc"}]`
	turns := []schema.Turn{{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "display reasoning", EncryptedContent: compat}},
		{Kind: llm.ContentText, Text: "answer"},
	}}}}
	out := expandHistory(turns, replayScope{
		Instance: "work", Model: "kimi-k2", Protocol: registry.ProtocolOpenAIChat,
		InFlightFrom: len(turns), DropThinking: true, protocolOf: replayProtocolOf,
	})
	for _, m := range out {
		for _, p := range m.Content {
			if p.Kind != llm.ContentThinking {
				continue
			}
			if p.Thinking == nil || p.Thinking.EncryptedContent != compat {
				t.Fatalf("encrypted reasoning_details did not round-trip: %+v", p.Thinking)
			}
			if p.Thinking.Text != "" {
				t.Fatalf("display reasoning rode with the encrypted detail: %q", p.Thinking.Text)
			}
			return
		}
	}
	t.Fatal("the encrypted reasoning_details part was dropped; the chat adapter needs it for continuation")
}

// TestExpandHistory_OpenAIChat_SignatureItemKeepsItsText covers the signed
// variant: when the reasoning_details array carries a reasoning.text item with
// an empty text field, ThinkingData.Text is the text the builder merges into
// that signed item, so the trim must keep it.
func TestExpandHistory_OpenAIChat_SignatureItemKeepsItsText(t *testing.T) {
	t.Parallel()
	const compat = `[{"type":"reasoning.text","text":"","signature":"sig"}]`
	turns := []schema.Turn{{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "signed reasoning", EncryptedContent: compat}},
		{Kind: llm.ContentText, Text: "answer"},
	}}}}
	out := expandHistory(turns, replayScope{
		Instance: "work", Model: "kimi-k2", Protocol: registry.ProtocolOpenAIChat,
		InFlightFrom: len(turns), DropThinking: true, protocolOf: replayProtocolOf,
	})
	for _, m := range out {
		for _, p := range m.Content {
			if p.Kind != llm.ContentThinking {
				continue
			}
			if p.Thinking == nil || p.Thinking.Text != "signed reasoning" || p.Thinking.EncryptedContent != compat {
				t.Fatalf("signature item lost the text it needs: %+v", p.Thinking)
			}
			return
		}
	}
	t.Fatal("the signature-bearing part was dropped")
}

// TestFallbackContinuationKeepsThinkingForTheFallbackPolicy pins the per-target
// projection: a chat primary strips its own thinking, but the untrimmed source
// must still feed an Anthropic fallback, whose signed thinking has to survive
// into the fallback's next request after a tool round (#4563 review).
func TestFallbackContinuationKeepsThinkingForTheFallbackPolicy(t *testing.T) {
	t.Parallel()
	primary := provider.FromResolved(registry.Resolved{
		Instance: "gw", ModelID: "chat-primary", WireID: "chat-primary",
		Protocol: registry.ProtocolOpenAIChat, Surface: registry.SurfaceOpenAI,
		Caps: registry.Caps{Reasoning: new(true)},
	}, nil)
	fallback := provider.FromResolved(registry.Resolved{
		Instance: "gwb", ModelID: "claude-fallback", WireID: "claude-fallback",
		Protocol: registry.ProtocolAnthropic, Surface: registry.SurfaceOpenAI,
		Caps: registry.Caps{InputModalities: []string{"text", "image"}},
	}, nil)
	permErr := llm.ErrorFromHTTPStatus("gw", 404, "model not found", nil, nil)
	primaryAdapter := &agenttest.ScriptedAdapter{
		Provider:       "gw",
		FaultResponder: func(llm.Request) error { return permErr },
		Responder:      func(llm.Request) llm.Response { return agenttest.FinalResponse("unused") },
	}
	const text, signature = "signed fallback thought", "sig-abc"
	tool := llm.ToolCallData{ID: "call_unknown", Name: "no_such_tool", Arguments: []byte(`{}`)}
	var fallbackCalls int
	fallbackAdapter := &agenttest.ScriptedAdapter{
		Provider: "gwb",
		Responder: func(llm.Request) llm.Response {
			fallbackCalls++
			if fallbackCalls == 1 {
				resp := agenttest.ToolCallResponse(tool)
				resp.Message.Content = append([]llm.ContentPart{{
					Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: text, Signature: signature},
				}}, resp.Message.Content...)
				return resp
			}
			return agenttest.FinalResponse("done")
		},
	}
	sess := newSession(t,
		withProfile(primary),
		withAdapter(primaryAdapter),
		withAdapter(fallbackAdapter),
		withConfig(SessionConfig{
			ModelFallbacks: []string{"gwb/claude-fallback"},
			ResolveProfile: func(ref string) (*provider.Profile, error) {
				if ref == "gwb/claude-fallback" {
					return fallback, nil
				}
				return nil, nil
			},
			testOnly: testConfig{skipGitSnapshot: true},
		}),
	)
	// TRIPWIRE: scripted in-process adapters; 30s only catches a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "go", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	fallbackReqs := fallbackAdapter.Requests()
	if len(fallbackReqs) < 2 {
		t.Fatalf("fallback requests = %d, want a tool round and its continuation", len(fallbackReqs))
	}
	if !hasSignedThinking(fallbackReqs[len(fallbackReqs)-1].Messages, text, signature) {
		t.Fatal("the fallback continuation lost the signed thinking the chat primary does not carry")
	}
	primaryReqs := primaryAdapter.Requests()
	if len(primaryReqs) >= 2 && hasSignedThinking(primaryReqs[len(primaryReqs)-1].Messages, text, signature) {
		t.Fatal("the chat primary received thinking its policy drops")
	}
}

func hasSignedThinking(messages []llm.Message, text, signature string) bool {
	for _, m := range messages {
		for _, p := range m.Content {
			if p.Kind == llm.ContentThinking && p.Thinking != nil &&
				p.Thinking.Text == text && p.Thinking.Signature == signature {
				return true
			}
		}
	}
	return false
}

// TestFallbackReceivesTurnsAppendedDuringSkillReload pins the fallback snapshot
// refresh: a compacted-skill reload appends instructions while the request is
// prepared, and a permanent primary failure must hand the fallback the rebuilt
// history rather than the snapshot taken before the reload (#4563 review).
func TestFallbackReceivesTurnsAppendedDuringSkillReload(t *testing.T) {
	t.Parallel()
	root := skillFixtureRoot(t)
	markGitRoot(t, root)
	const reloadedMarker = "RELOAD_BODY_a71c"
	newBody := strings.Repeat(reloadedMarker+" reloaded instruction\n", 64)
	oldBody := strings.Repeat("RELOAD_OLD_5e10 stale instruction\n", 64)
	writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\n---\n"+oldBody)
	source := filepath.Join(root, "skills", "opaque", "SKILL.md")
	var mainCalls atomic.Int32
	fallback := provider.FromResolved(registry.Resolved{
		Instance: "gwb", ModelID: "claude-fallback", WireID: "claude-fallback",
		Protocol: registry.ProtocolAnthropic, Surface: registry.SurfaceAnthropic,
		Caps: registry.Caps{InputModalities: []string{"text", "image"}},
	}, nil)
	fallbackAdapter := &agenttest.ScriptedAdapter{
		Provider:  "gwb",
		Responder: func(llm.Request) llm.Response { return agenttest.FinalResponse("fallback ok") },
	}
	s, main, _ := newReloadSession(t, root, 0,
		func(llm.Request) llm.Response {
			if mainCalls.Add(1) == 1 {
				return agenttest.ToolCallResponse(useSkillCall("skill-1", "opaque"))
			}
			return agenttest.ToolCallResponse(communicateCall("done-1", "ok"))
		},
		reloadSummaryResponder("SUMMARY_reload_fb", nil),
		withAdapter(fallbackAdapter),
		withConfig(SessionConfig{
			MaxSubagentDepth: 1, StateDir: t.TempDir(),
			ModelFallbacks: []string{"gwb/claude-fallback"},
			ResolveProfile: func(ref string) (*provider.Profile, error) {
				if ref == "gwb/claude-fallback" {
					return fallback, nil
				}
				return nil, nil
			},
		}),
	)
	var mainAttempts atomic.Int32
	main.FaultResponder = func(llm.Request) error {
		if mainAttempts.Add(1) == 3 {
			return llm.ErrorFromHTTPStatus("anthropic", 404, "model not found", nil, nil)
		}
		return nil
	}
	seedNumberedSessionHistory(t, s, 12)
	s.contextMgr.PreserveRecentTurns = 2
	if _, err := s.ProcessInput(context.Background(), "REQUEST_93d2", nil); err != nil {
		t.Fatalf("activation: %v", err)
	}
	if lifecycleInventory(s)["opaque"].Ordinary == nil {
		t.Fatal("test premise: the activation recorded no ordinary skill inventory")
	}
	foldWithReloadSelection(t, s, schema.SkillReloadSelection{State: "valid", Names: []string{"opaque"}})
	if handoffs := pendingHandoffsSnapshot(s); len(handoffs) == 0 {
		t.Fatal("test premise: the fold published no reload handoff")
	}
	if err := os.WriteFile(source, []byte("---\nname: opaque\ndescription: fixture\n---\n"+newBody), 0o644); err != nil {
		t.Fatalf("rewrite source: %v", err)
	}
	if _, err := s.ProcessInput(context.Background(), "REQUEST_again", nil); err != nil {
		t.Fatalf("reload round: %v", err)
	}
	if mainReqs := main.Requests(); len(mainReqs) < 3 || !requestCarriesText(mainReqs[2], reloadedMarker) {
		t.Fatalf("test premise: the reload round did not carry the reloaded instructions (%d requests)", len(mainReqs))
	}
	fallbackReqs := fallbackAdapter.Requests()
	if len(fallbackReqs) == 0 {
		t.Fatal("the fallback never ran")
	}
	if !requestCarriesText(fallbackReqs[len(fallbackReqs)-1], reloadedMarker) {
		t.Fatal("the fallback request lost the instructions appended during the skill reload")
	}
}

func requestCarriesText(req llm.Request, want string) bool {
	for _, m := range req.Messages {
		for _, p := range m.Content {
			if p.Kind == llm.ContentText && strings.Contains(p.Text, want) {
				return true
			}
		}
	}
	return false
}

// TestFallbackThinkingProjectionRepricesTheBudget pins the estimate refresh:
// the continuation builder prices the untrimmed messages, but a chat fallback
// drops their thinking before dispatch. Keeping the stale estimate makes the
// budget reject a request that fits, so the fallback never runs (#4563 review).
func TestFallbackThinkingProjectionRepricesTheBudget(t *testing.T) {
	t.Parallel()
	primary := provider.FromResolved(registry.Resolved{
		Instance: "gw", ModelID: "chat-primary", WireID: "chat-primary",
		Protocol: registry.ProtocolOpenAIChat, Surface: registry.SurfaceOpenAI,
		Caps: registry.Caps{Reasoning: new(true)},
	}, nil)
	fallback := provider.FromResolved(registry.Resolved{
		Instance: "gwb", ModelID: "chat-fallback", WireID: "chat-fallback",
		Protocol: registry.ProtocolOpenAIChat, Surface: registry.SurfaceOpenAI,
	}, nil)
	fallback = WithContextWindow(fallback, 50000)
	permErr := llm.ErrorFromHTTPStatus("gw", 404, "model not found", nil, nil)
	primaryAdapter := &agenttest.ScriptedAdapter{
		Provider:       "gw",
		FaultResponder: func(llm.Request) error { return permErr },
		Responder:      func(llm.Request) llm.Response { return agenttest.FinalResponse("unused") },
	}
	fallbackAdapter := &agenttest.ScriptedAdapter{
		Provider:  "gwb",
		Responder: func(llm.Request) llm.Response { return agenttest.FinalResponse("ok") },
	}
	sess := newSession(t,
		withProfile(primary),
		withAdapter(primaryAdapter),
		withAdapter(fallbackAdapter),
		withConfig(SessionConfig{
			ModelFallbacks: []string{"gwb/chat-fallback"},
			ResolveProfile: func(ref string) (*provider.Profile, error) {
				if ref == "gwb/chat-fallback" {
					return fallback, nil
				}
				return nil, nil
			},
			testOnly: testConfig{skipGitSnapshot: true},
		}),
	)
	sess.appendTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: strings.Repeat("THINK_b7d1 padded reasoning\n", 80000)}},
		{Kind: llm.ContentText, Text: "prior answer"},
	}})

	// TRIPWIRE: scripted in-process adapters; 30s only catches a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "continue", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	fallbackReqs := fallbackAdapter.Requests()
	if len(fallbackReqs) == 0 {
		t.Fatal("the fallback never ran")
	}
	if requestCarriesText(fallbackReqs[len(fallbackReqs)-1], "THINK_b7d1") {
		t.Fatal("the chat fallback carried thinking its policy drops")
	}
}
