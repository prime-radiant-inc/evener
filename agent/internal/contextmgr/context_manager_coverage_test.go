package contextmgr

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// TestEstimateUsage_NoPriorMeasurement covers the estimate-from-history branch
// (gremlins flagged context_manager.go:196 as not covered): with no recorded
// input-token measurement, Used is the char/4 estimate of the whole history plus
// the system-prompt budget, and Remaining is the window minus that.
func TestEstimateUsage_NoPriorMeasurement(t *testing.T) {
	const window = 1_000_000
	cm := NewManager(testProfile("openai", "gpt-5.2", window), nil)
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("write a parser for the config file")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("Sure, here is the plan.")},
	}
	const sysPromptChars = 4_000

	want := estimateTokens(history) + sysPromptChars/4
	m := cm.EstimateUsage(history, sysPromptChars)
	if m.Window != window {
		t.Errorf("Window = %d, want %d", m.Window, window)
	}
	if m.Used != want {
		t.Errorf("Used = %d, want %d (estimateTokens + sysPromptChars/4)", m.Used, want)
	}
	if m.Remaining != window-want {
		t.Errorf("Remaining = %d, want %d", m.Remaining, window-want)
	}
}

// TestEstimateUsage_RemainingClampedToZero exercises the negative-remaining
// clamp: a system-prompt budget larger than the whole window drives Used past
// the window, and Remaining must floor at 0 rather than go negative.
func TestEstimateUsage_RemainingClampedToZero(t *testing.T) {
	const window = 1_000
	cm := NewManager(testProfile("openai", "gpt-5.2", window), nil)
	m := cm.EstimateUsage(nil, window*8)
	if m.Used <= window {
		t.Fatalf("Used = %d, want > window %d for this case", m.Used, window)
	}
	if m.Remaining != 0 {
		t.Errorf("Remaining = %d, want 0 (clamped)", m.Remaining)
	}
}

// TestApplyThresholdScale covers ApplyThresholdScale (gremlins flagged
// context_manager.go:211-222 as not covered): scaling multiplies every threshold
// by the factor and clamps the result to a 0.20 floor; scale 0 or 1 is a no-op.
func TestApplyThresholdScale(t *testing.T) {
	const eps = 1e-9

	t.Run("scales every threshold, no clamp needed", func(t *testing.T) {
		cm := NewManager(testProfile("openai", "gpt-5.2", 1_000), nil)
		ApplyThresholdScale(cm, 0.5)
		for _, c := range []struct {
			name string
			got  float64
			want float64
		}{
			{"ObservationMask", cm.ObservationMaskThreshold, 0.30},
			{"ThinkingClear", cm.ThinkingClearThreshold, 0.35},
			{"Warn", cm.WarnThreshold, 0.375},
			{"Checkpoint", cm.CheckpointThreshold, 0.40},
			{"Summarize", cm.SummarizeThreshold, 0.475},
		} {
			if d := c.got - c.want; d > eps || d < -eps {
				t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
			}
		}
	})

	t.Run("clamps thresholds below the 0.20 floor", func(t *testing.T) {
		cm := NewManager(testProfile("openai", "gpt-5.2", 1_000), nil)
		ApplyThresholdScale(cm, 0.25) // 0.60*0.25=0.15 -> floor, 0.95*0.25=0.2375 stays
		if d := cm.ObservationMaskThreshold - 0.20; d > eps || d < -eps {
			t.Errorf("ObservationMask = %v, want clamped to 0.20", cm.ObservationMaskThreshold)
		}
		if d := cm.SummarizeThreshold - 0.2375; d > eps || d < -eps {
			t.Errorf("Summarize = %v, want 0.2375 (above floor, unclamped)", cm.SummarizeThreshold)
		}
	})

	for _, scale := range []float64{0, 1.0} {
		t.Run("no-op", func(t *testing.T) {
			cm := NewManager(testProfile("openai", "gpt-5.2", 1_000), nil)
			ApplyThresholdScale(cm, scale)
			if cm.ObservationMaskThreshold != 0.60 || cm.SummarizeThreshold != 0.95 {
				t.Errorf("scale %v changed thresholds: obs=%v sum=%v", scale, cm.ObservationMaskThreshold, cm.SummarizeThreshold)
			}
		})
	}
}

// TestHasClient covers the HasClient predicate (gremlins flagged
// context_manager.go:1057 as not covered).
func TestHasClient(t *testing.T) {
	if NewManager(testProfile("openai", "gpt-5.2", 1_000), nil).HasClient() {
		t.Error("HasClient() = true for a nil client, want false")
	}
	if !NewManager(testProfile("openai", "gpt-5.2", 1_000), llm.NewClient()).HasClient() {
		t.Error("HasClient() = false with a client, want true")
	}
}

// TestElicitNote_RequiresClient covers the no-client guard in ElicitNote
// (context_manager.go:1069).
func TestElicitNote_RequiresClient(t *testing.T) {
	cm := NewManager(testProfile("openai", "gpt-5.2", 1_000), nil)
	if _, err := cm.ElicitNote(context.Background(), nil); err == nil {
		t.Fatal("ElicitNote with no client: want an error")
	}
}

// TestElicitNote_Success covers the happy path of ElicitNote: the model's bullet
// list is returned trimmed, and the rendered history reaches the prompt.
func TestElicitNote_Success(t *testing.T) {
	adapter := &stubSummarizeAdapter{
		name: "openai",
		respFn: func(req llm.Request) (llm.Response, error) {
			if !strings.Contains(req.Messages[0].Text(), "secret-token-XYZ") {
				t.Errorf("prompt missing the history detail; got %q", req.Messages[0].Text())
			}
			return llm.Response{Message: llm.Assistant("  - keep secret-token-XYZ\n")}, nil
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	cm := NewManager(NewOpenAIProfile("gpt-5.2"), client)

	history := []schema.Turn{
		{Kind: schema.TurnToolResults, Message: llm.ToolResultNamed("c1", "shell", "API key is secret-token-XYZ", false)},
	}
	got, err := cm.ElicitNote(context.Background(), history)
	if err != nil {
		t.Fatalf("ElicitNote: %v", err)
	}
	if got != "- keep secret-token-XYZ" {
		t.Errorf("note = %q, want the trimmed bullet list", got)
	}
}

// TestElicitNote_FallsBackAcrossModels covers the model-fallback loop
// (context_manager.go:1080-1092): a configured cheap route that returns a
// fallback-eligible error (HTTP 400) is retried on the active route.
func TestElicitNote_FallsBackAcrossModels(t *testing.T) {
	cheapCalled, activeCalled := false, false
	cheap := &stubSummarizeAdapter{
		name: "anthropic",
		respFn: func(req llm.Request) (llm.Response, error) {
			cheapCalled = true
			return llm.Response{}, llm.ErrorFromHTTPStatus("anthropic", 400, "bad request", nil, nil)
		},
	}
	active := &stubSummarizeAdapter{
		name: "openai",
		respFn: func(req llm.Request) (llm.Response, error) {
			activeCalled = true
			return llm.Response{Message: llm.Assistant("- survived")}, nil
		},
	}
	client := llm.NewClient()
	client.Register(cheap)
	client.Register(active)

	profile := WithCheapModel(NewOpenAIProfile("gpt-5.2"), "anthropic/claude-haiku-4-5-20251001")
	cm := NewManager(profile, client)

	got, err := cm.ElicitNote(context.Background(), []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("do the thing")},
	})
	if err != nil {
		t.Fatalf("ElicitNote: %v", err)
	}
	if !cheapCalled || !activeCalled {
		t.Errorf("fallback not exercised: cheapCalled=%v activeCalled=%v", cheapCalled, activeCalled)
	}
	if got != "- survived" {
		t.Errorf("note = %q, want the active-route result", got)
	}
}

// TestBuildSummarizePrompt_RendersUserAndSteering covers the user-input and
// steering branches plus truncation in buildSummarizePrompt (gremlins flagged
// fork_summarize.go:57 and :77, and truncate at :102).
func TestBuildSummarizePrompt_RendersUserAndSteering(t *testing.T) {
	longText := strings.Repeat("x", 600)
	turns := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User(longText)},
		{Kind: schema.TurnSteering, Message: llm.User("be careful with deletes")},
	}
	out := buildSummarizePrompt(turns)
	if !strings.Contains(out, "User: ") {
		t.Error("prompt missing the User: line")
	}
	if !strings.Contains(out, "System: be careful with deletes") {
		t.Error("prompt missing the steering System: line")
	}
	if !strings.Contains(out, "...") {
		t.Error("long user text was not truncated")
	}
	// The 600-char body must be truncated to the 500-char limit + ellipsis.
	if strings.Contains(out, longText) {
		t.Error("untruncated 600-char body leaked into the prompt")
	}
}

// TestTruncate covers both branches of truncate (fork_summarize.go:100).
func TestTruncate(t *testing.T) {
	if got := truncate("short", 10); got != "short" {
		t.Errorf("truncate under limit = %q, want unchanged", got)
	}
	if got := truncate("abcdef", 3); got != "abc..." {
		t.Errorf("truncate over limit = %q, want %q", got, "abc...")
	}
}

// TestStripCodeFence covers stripCodeFence including the fenced branch
// (fork_summarize.go:111-120).
func TestStripCodeFence(t *testing.T) {
	if got := stripCodeFence("{\"a\":1}"); got != `{"a":1}` {
		t.Errorf("unfenced input = %q, want unchanged", got)
	}
	fenced := "```json\n{\"a\":1}\n```"
	if got := stripCodeFence(fenced); got != `{"a":1}` {
		t.Errorf("fenced input = %q, want the inner JSON", got)
	}
}
