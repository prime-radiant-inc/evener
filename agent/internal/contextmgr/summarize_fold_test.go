package contextmgr

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func TestSummarizeWithLLM_DefaultsToActiveModel(t *testing.T) {
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				// Without an explicitly configured compaction model, summarize on
				// the active session model. This avoids provider-default auxiliary
				// models that may be unavailable for the account type.
				if req.Model != "gpt-5.2" {
					t.Errorf("expected active model gpt-5.2, got %q", req.Model)
				}
				// Verify the prompt asks for compaction/summarization.
				found := false
				for _, m := range req.Messages {
					text := m.Text()
					if strings.Contains(text, "COMPACTION") || strings.Contains(text, "handoff summary") {
						found = true
					}
				}
				if !found {
					t.Error("expected compaction/handoff prompt")
				}
				return llm.Response{Message: llm.Assistant("## Progress\nSummary: fixed auth bug")}
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	cm := NewManager(NewOpenAIProfile("gpt-5.2"), client, cheapmodel.New(client))

	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("Fix the auth bug")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("I'll fix it")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent1")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent2")},
	}

	ctx := context.Background()
	result, err := cm.summarizeFold(ctx, history, 2, "", nil)
	if err != nil {
		t.Fatalf("summarizeFold: %v", err)
	}

	// Should have summary + 2 preserved turns.
	if len(result) != 3 {
		t.Fatalf("expected 3 turns, got %d", len(result))
	}
}

func TestSummarizeWithLLM_UsesConfiguredCompactionModel(t *testing.T) {
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("## Progress\nsummary")} },
	}}
	client := llm.NewClient()
	client.Register(adapter)

	profile := provider.WithCheapModel(NewOpenAIProfile("gpt-5.2"), "gpt-5-mini")
	cm := NewManager(profile, client, cheapmodel.New(client))

	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("old")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent")},
	}

	if _, err := cm.summarizeFold(context.Background(), history, 1, "", nil); err != nil {
		t.Fatalf("summarizeFold: %v", err)
	}
	reqs := adapter.Requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(reqs))
	}
	if reqs[0].Model != "gpt-5-mini" {
		t.Fatalf("summary model = %q, want gpt-5-mini", reqs[0].Model)
	}
}

func TestSummarizeWithLLM_FallsBackToActiveModelWhenConfiguredModelFails(t *testing.T) {
	adapter := &fallbackSummaryAdapter{name: "openai", t: t}
	client := llm.NewClient()
	client.Register(adapter)

	profile := provider.WithCheapModel(NewOpenAIProfile("gpt-5.2"), "gpt-5-mini")
	cm := NewManager(profile, client, cheapmodel.New(client))

	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("old")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent")},
	}

	result, err := cm.summarizeFold(context.Background(), history, 1, "", nil)
	if err != nil {
		t.Fatalf("summarizeFold: %v", err)
	}
	if !strings.Contains(result[0].Message.Text(), "fallback summary") {
		t.Fatalf("summary did not come from fallback response: %q", result[0].Message.Text())
	}
	if len(adapter.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(adapter.requests))
	}
}

type fallbackSummaryAdapter struct {
	name     string
	t        *testing.T
	requests []llm.Request
}

func (a *fallbackSummaryAdapter) Name() string { return a.name }

func (a *fallbackSummaryAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	_ = ctx
	a.requests = append(a.requests, req)
	switch len(a.requests) {
	case 1:
		if req.Model != "gpt-5-mini" {
			a.t.Fatalf("first summary model = %q, want gpt-5-mini", req.Model)
		}
		return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 400, "configured compaction model unavailable", nil, nil)
	case 2:
		if req.Model != "gpt-5.2" {
			a.t.Fatalf("fallback summary model = %q, want gpt-5.2", req.Model)
		}
		return llm.Response{Message: llm.Assistant("## Progress\nfallback summary")}, nil
	default:
		a.t.Fatalf("unexpected request %d", len(a.requests))
		return llm.Response{}, errors.New("unexpected request")
	}
}

func TestSummarizeWithLLM_DoesNotFallbackOnCancellation(t *testing.T) {
	adapter := &cancelingSummaryAdapter{name: "openai"}
	client := llm.NewClient()
	client.Register(adapter)

	profile := provider.WithCheapModel(NewOpenAIProfile("gpt-5.2"), "gpt-5-mini")
	cm := NewManager(profile, client, cheapmodel.New(client))
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("old")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent")},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := cm.summarizeFold(ctx, history, 1, "", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if len(adapter.requests) != 1 {
		t.Fatalf("requests = %d, want 1 (no active-model fallback after cancellation)", len(adapter.requests))
	}
}

type cancelingSummaryAdapter struct {
	name     string
	requests []llm.Request
}

func (a *cancelingSummaryAdapter) Name() string { return a.name }

func (a *cancelingSummaryAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	a.requests = append(a.requests, req)
	return llm.Response{}, ctx.Err()
}

func (a *cancelingSummaryAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	_ = ctx
	_ = req
	return nil, llm.ErrStreamUnsupported
}

func (a *fallbackSummaryAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	_ = ctx
	_ = req
	return nil, llm.ErrStreamUnsupported
}

func TestSummarizeWithLLM_ReplacesOldHistory(t *testing.T) {
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("## Progress\nSummary of work done")}
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	cm := NewManager(NewOpenAIProfile("gpt-5.2"), client, cheapmodel.New(client))

	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("step 1")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("step 2")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent")},
	}

	ctx := context.Background()
	result, err := cm.summarizeFold(ctx, history, 1, "", nil)
	if err != nil {
		t.Fatalf("summarizeFold: %v", err)
	}

	// First turn should contain the LLM summary.
	text := result[0].Message.Text()
	if !strings.Contains(text, "Summary of work done") {
		t.Fatalf("expected summary in first turn, got: %q", text)
	}
	// First turn should be a user message (context for the model).
	if result[0].Message.Role != llm.RoleUser {
		t.Fatalf("summary should be user role, got %s", result[0].Message.Role)
	}
}

func TestSummarizeWithLLM_PreservesRecentTurns(t *testing.T) {
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("## Progress\nsummary")}
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	cm := NewManager(NewOpenAIProfile("gpt-5.2"), client, cheapmodel.New(client))

	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("old")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent1")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent2")},
	}

	ctx := context.Background()
	result, err := cm.summarizeFold(ctx, history, 2, "", nil)
	if err != nil {
		t.Fatalf("summarizeFold: %v", err)
	}

	if result[1].Message.Text() != "recent1" {
		t.Fatalf("expected recent1, got %q", result[1].Message.Text())
	}
	if result[2].Message.Text() != "recent2" {
		t.Fatalf("expected recent2, got %q", result[2].Message.Text())
	}
}

func TestSummarizeWithLLM_ErrorFallsBackGracefully(t *testing.T) {
	// Use errorAdapter for a reliable error path test.
	adapter := &errorAdapter{name: "openai"}
	client := llm.NewClient()
	client.Register(adapter)

	cm := NewManager(NewOpenAIProfile("gpt-5.2"), client, cheapmodel.New(client))

	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("step 1")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent")},
	}

	result, err := cm.summarizeFold(context.Background(), history, 1, "", nil)
	if err == nil {
		t.Fatal("expected error from summarizeFold when adapter fails")
	}
	if result != nil {
		t.Fatalf("expected nil result on error, got %d turns", len(result))
	}
}

func TestSummarizeWithLLM_AdjustsCutoffToAvoidOrphanedToolTurn(t *testing.T) {
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("## Progress\nsummary")}
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	cm := NewManager(NewOpenAIProfile("gpt-5.2"), client, cheapmodel.New(client))

	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: assistantWithToolCall("c1", "read_file", `{"file_path":"a.go"}`)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c1", "read_file", "content", false)},
		{Kind: schema.TurnAssistant, Message: assistantWithToolCall("c2", "shell", `{"command":"go test"}`)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c2", "shell", "ok\nexit_code=0\n", false)},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("done")},
	}

	// preserveRecent=2 → cutoff=4, preserved starts at TurnTool — BAD.
	result, err := cm.summarizeFold(context.Background(), history, 2, "", nil)
	if err != nil {
		t.Fatalf("summarizeFold: %v", err)
	}

	if len(result) < 2 {
		t.Fatalf("expected at least 2 turns, got %d", len(result))
	}
	if result[1].Kind == schema.TurnTool {
		t.Fatalf("second turn must not be TurnTool — invalid message ordering for LLM APIs")
	}
}

// H4: summarizeFold should truncate the prompt to avoid exceeding the cheap model's context.
func TestSummarizeWithLLM_TruncatesPromptForCheapModel(t *testing.T) {
	var receivedPromptLen int
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				for _, m := range req.Messages {
					receivedPromptLen += len(m.Text())
				}
				return llm.Response{Message: llm.Assistant("## Progress\nsummary")}
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	cm := NewManager(NewOpenAIProfile("gpt-5.2"), client, cheapmodel.New(client))

	// Build history with enormous user messages — many times larger than any cheap model can handle.
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User(strings.Repeat("x", 100_000))},
		{Kind: schema.TurnAssistant, Message: llm.Assistant(strings.Repeat("y", 100_000))},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent")},
	}

	_, err := cm.summarizeFold(context.Background(), history, 1, "", nil)
	if err != nil {
		t.Fatalf("summarizeFold: %v", err)
	}

	// The prompt sent to the cheap model should be bounded, not 200k+ chars.
	// The history serializer caps at ~80k chars, plus the instruction prefix.
	if receivedPromptLen > 95_000 {
		t.Fatalf("prompt sent to cheap model is too large: %d chars (should be truncated)", receivedPromptLen)
	}
}

func TestSummarizeWithLLM_RequestsInterleavedConversationTimeline(t *testing.T) {
	var prompt string
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				var promptSb1365 strings.Builder
				for _, m := range req.Messages {
					promptSb1365.WriteString(m.Text())
				}
				prompt += promptSb1365.String()
				return llm.Response{Message: llm.Assistant("## Progress\nsummary")}
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	cm := NewManager(NewOpenAIProfile("gpt-5.2"), client, cheapmodel.New(client))

	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("first request")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("first reply")},
		{Kind: schema.TurnUserInput, Message: llm.User("second request")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent")},
	}

	if _, err := cm.summarizeFold(context.Background(), history, 1, "", nil); err != nil {
		t.Fatalf("summarizeFold: %v", err)
	}
	if !strings.Contains(prompt, "## Conversation Timeline") || !strings.Contains(prompt, "interleaved") {
		t.Fatalf("summary prompt should request an interleaved conversation timeline:\n%s", prompt)
	}
	if strings.Contains(prompt, "## User Messages") || strings.Contains(prompt, "## Agent Responses") {
		t.Fatalf("summary prompt should not request bundled user/agent sections:\n%s", prompt)
	}
}

// H5: summarizeFold error path must be reliably testable.
func TestSummarizeWithLLM_AdapterError_ReturnsError(t *testing.T) {
	adapter := &errorAdapter{name: "openai"}
	client := llm.NewClient()
	client.Register(adapter)
	cm := NewManager(NewOpenAIProfile("gpt-5.2"), client, cheapmodel.New(client))

	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("step 1")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent")},
	}

	result, err := cm.summarizeFold(context.Background(), history, 1, "", nil)
	if err == nil {
		t.Fatal("expected error from summarizeFold when adapter fails")
	}
	if result != nil {
		t.Fatalf("expected nil result on error, got %d turns", len(result))
	}
}

func TestSafeCutoff_NoAdjustmentNeeded(t *testing.T) {
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("answer")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent")},
	}
	got := safeCutoff(history, 2)
	if got != 2 {
		t.Fatalf("safeCutoff = %d, want 2 (no adjustment needed)", got)
	}
}

func TestSafeCutoff_WalksToZero_ReturnsNegative(t *testing.T) {
	// All turns after index 0 are TurnTool — walking back from any cutoff
	// should reach 0, which is not a safe position.
	history := []schema.Turn{
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c1", "read_file", "content", false)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c2", "read_file", "content", false)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c3", "read_file", "content", false)},
	}
	got := safeCutoff(history, 2)
	if got != -1 {
		t.Fatalf("safeCutoff = %d, want -1 (no safe position)", got)
	}
}

func TestSafeCutoff_SkipsSteering(t *testing.T) {
	// TurnSteering at the cutoff position should be walked back, just like TurnTool.
	// Otherwise, preserved turns would start with a steering message that becomes
	// a consecutive user-role message after the checkpoint's user-role message.
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("answer")},
		{Kind: schema.TurnSteering, Message: llm.User("you should do X")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent")},
	}
	// preserveRecent=1 → cutoff = len(history)-1 = 3.
	// history[3] is TurnAssistant (OK), so normally no adjustment.
	// But if cutoff = len(history)-2 = 2, history[2] is TurnSteering → walk to 1.
	got := safeCutoff(history, 2)
	// cutoff=2 → TurnSteering → walk back to 1 → TurnAssistant → stop.
	if got != 1 {
		t.Fatalf("safeCutoff = %d, want 1 (should skip TurnSteering)", got)
	}
}

func TestSafeCutoff_DoesNotSkipStandaloneHookCompleted(t *testing.T) {
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("earlier answer")},
		schema.NewTurn(schema.TurnHookCompleted, llm.System("SessionStart hook exit 0")),
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent")},
	}

	if got := safeCutoff(history, 2); got != 2 {
		t.Fatalf("safeCutoff = %d, want 2 (standalone hook must not move cutoff)", got)
	}
}

func TestSafeCutoff_SkipsHookBeforeStandaloneSteering(t *testing.T) {
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("earlier answer")},
		schema.NewTurn(schema.TurnHookCompleted, llm.System("SessionStart hook exit 0")),
		{Kind: schema.TurnSteering, Message: llm.User("continue with the next step")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent")},
	}

	if got := safeCutoff(history, 2); got != 1 {
		t.Fatalf("safeCutoff = %d, want 1 (hook before steering must move cutoff)", got)
	}
}

func TestAttentionResolutionDoesNotConsumeContextBudgetOrRecentSlots(t *testing.T) {
	visible := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("must stay")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent answer")),
		schema.NewTurn(schema.TurnUserInput, llm.User("latest")),
	}
	withMarkers := append([]schema.Turn(nil), visible[:2]...)
	for i := range 6 {
		marker := schema.NewTurn(schema.TurnAttentionResolution, llm.System(fmt.Sprintf("private marker %d", i)))
		marker.AttentionResolution = &schema.AttentionResolutionInfo{
			AttentionID: fmt.Sprintf("attention-%d", i),
			Disposition: "consumed",
		}
		withMarkers = append(withMarkers, marker)
	}
	withMarkers = append(withMarkers, visible[2])

	if got, want := testEstimateUsedTokens(t, 0, 0, withMarkers, 0), testEstimateUsedTokens(t, 0, 0, visible, 0); got != want {
		t.Fatalf("resolution markers changed estimated provider tokens: got %d want %d", got, want)
	}
	compacted := checkpoint(withMarkers, 6, nil, "communicate")
	if !reflect.DeepEqual(compacted, withMarkers) {
		t.Fatalf("resolution markers consumed recent slots and triggered compaction:\n got %#v\nwant %#v", compacted, withMarkers)
	}
}

func TestAttentionResolutionDoesNotConsumeCompactionLayerRecentSlots(t *testing.T) {
	marker := schema.NewTurn(schema.TurnAttentionResolution, llm.System("private marker"))
	marker.AttentionResolution = &schema.AttentionResolutionInfo{AttentionID: "private", Disposition: "consumed"}
	const resultContent = "opaque tool output that must remain available in the recent provider-visible window"
	toolHistory := func() []schema.Turn {
		return []schema.Turn{
			{Kind: schema.TurnAssistant, Message: assistantWithToolCall("call_recent", "probe", `{}`)},
			schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("call_recent", "probe", resultContent, false)),
			marker,
			schema.NewTurn(schema.TurnUserInput, llm.User("latest")),
		}
	}

	t.Run("readable observation mask", func(t *testing.T) {
		history := toolHistory()
		maskObservations(history, 2, "communicate")
		if got := toolResultContent(history[1]); got != resultContent {
			t.Fatalf("private marker displaced recent tool result: got %q", got)
		}
	})

	t.Run("thinking clear", func(t *testing.T) {
		const thinking = "provider-visible recent reasoning"
		history := []schema.Turn{
			{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: thinking}}}}},
			marker,
			schema.NewTurn(schema.TurnUserInput, llm.User("latest")),
		}
		clearThinking(history, 2)
		if got := history[0].Message.Content[0].Thinking.Text; got != thinking {
			t.Fatalf("private marker displaced recent thinking: got %q", got)
		}
	})
}

func TestSummarizeWithLLM_SafeCutoffNegative_ReturnsUnchanged(t *testing.T) {
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				t.Fatal("LLM should not be called when safeCutoff returns -1")
				return llm.Response{}
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	cm := NewManager(NewOpenAIProfile("gpt-5.2"), client, cheapmodel.New(client))

	// Same scenario as checkpoint test: cutoff walks to 0 → return -1.
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c1", "read_file", "content", false)},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("answer")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent")},
	}
	result, err := cm.summarizeFold(context.Background(), history, 3, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != len(history) {
		t.Fatalf("expected unchanged history (len %d), got len %d", len(history), len(result))
	}
}

// errorAdapter always returns an error from Complete.
type errorAdapter struct {
	name string
}

func (a *errorAdapter) Name() string { return a.name }

func (a *errorAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("simulated LLM error")
}

func (a *errorAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}
