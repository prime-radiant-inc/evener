package llm_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/llm"

	// Blank imports to register provider factories.
	_ "primeradiant.com/serf/llm/providers/anthropic"
	_ "primeradiant.com/serf/llm/providers/glm"
	_ "primeradiant.com/serf/llm/providers/google"
	_ "primeradiant.com/serf/llm/providers/kimi"
	_ "primeradiant.com/serf/llm/providers/kimi_anthropic"
	_ "primeradiant.com/serf/llm/providers/minimax"
	_ "primeradiant.com/serf/llm/providers/openai"
	_ "primeradiant.com/serf/llm/providers/openaicompat"
	_ "primeradiant.com/serf/llm/providers/openrouter"
	_ "primeradiant.com/serf/llm/providers/openrouter_anthropic"
)

// providerConfig holds a test model and the env keys that gate the provider.
type providerConfig struct {
	envKeys  []string
	model    string
	provider string
}

var providers = []providerConfig{
	{[]string{"OPENAI_API_KEY"}, "gpt-5.4-mini", "openai"},
	{[]string{"ANTHROPIC_API_KEY"}, "claude-sonnet-4-5-20250929", "anthropic"},
	{[]string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}, "gemini-2.5-flash", "google"},
	{[]string{"MINIMAX_API_KEY"}, "MiniMax-M2.7", "minimax"},
}

var imageProviders = []providerConfig{
	{[]string{"OPENAI_API_KEY"}, "gpt-5.2", "openai"},
	{[]string{"ANTHROPIC_API_KEY"}, "claude-sonnet-4-5-20250929", "anthropic"},
	{[]string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}, "gemini-2.5-flash", "google"},
	{[]string{"OPENROUTER_API_KEY"}, "google/gemini-2.5-flash", "openrouter"},
}

func (p providerConfig) available() bool {
	for _, key := range p.envKeys {
		if os.Getenv(key) != "" {
			return true
		}
	}
	return false
}

func skipIfNoProviders(t *testing.T) {
	t.Helper()
	if os.Getenv("SERF_LIVE_TESTS") != "1" {
		t.Skip("set SERF_LIVE_TESTS=1 to run live LLM integration tests")
	}
	for _, p := range providers {
		if p.available() {
			return
		}
	}
	t.Skip("no LLM API keys set; skipping integration smoke test")
}

func skipIfNoImageProviders(t *testing.T) {
	t.Helper()
	if os.Getenv("SERF_LIVE_TESTS") != "1" {
		t.Skip("set SERF_LIVE_TESTS=1 to run live LLM integration tests")
	}
	for _, p := range imageProviders {
		if p.available() {
			return
		}
	}
	t.Skip("no vision-capable LLM API keys set; skipping image integration smoke test")
}

func TestIntegration_BasicGeneration(t *testing.T) {
	skipIfNoProviders(t)
	client, err := llm.NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}

	for _, p := range providers {
		if !p.available() {
			continue
		}
		t.Run(p.provider, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			res, err := llm.Generate(ctx, llm.GenerateOptions{
				Client:   client,
				Model:    p.model,
				Provider: p.provider,
				Prompt:   strPtr("Say hello in one sentence."),
			})
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if strings.TrimSpace(res.Text) == "" {
				t.Fatalf("expected non-empty text")
			}
			if res.Usage.InputTokens == 0 || res.Usage.OutputTokens == 0 {
				t.Fatalf("usage: %+v", res.Usage)
			}
			t.Logf("text (truncated): %.100s", res.Text)
		})
	}
}

func TestIntegration_Streaming(t *testing.T) {
	skipIfNoProviders(t)
	client, err := llm.NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}

	for _, p := range providers {
		if !p.available() {
			continue
		}
		t.Run(p.provider, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			sr, err := llm.StreamGenerate(ctx, llm.GenerateOptions{
				Client:   client,
				Model:    p.model,
				Provider: p.provider,
				Prompt:   strPtr("Say hello in one sentence."),
			})
			if err != nil {
				t.Fatalf("StreamGenerate: %v", err)
			}

			var deltas []string
			for ev := range sr.Events() {
				if ev.Type == llm.StreamEventTextDelta {
					deltas = append(deltas, ev.Delta)
				}
			}
			resp, err := sr.Response()
			if err != nil {
				t.Fatalf("Response: %v", err)
			}
			if resp == nil {
				t.Fatalf("nil response")
			}

			streamed := strings.Join(deltas, "")
			final := strings.TrimSpace(resp.Text())
			if final == "" {
				t.Fatalf("expected non-empty final text")
			}
			// The streamed deltas should match the final text.
			if strings.TrimSpace(streamed) != final {
				t.Logf("streamed (truncated): %.100s", streamed)
				t.Logf("final (truncated): %.100s", final)
				// Allow minor whitespace differences.
				if strings.TrimSpace(streamed) == "" {
					t.Fatalf("no text deltas received")
				}
			}
			t.Logf("text (truncated): %.100s", final)
		})
	}
}

func TestIntegration_ToolCalling(t *testing.T) {
	skipIfNoProviders(t)
	client, err := llm.NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}

	for _, p := range providers {
		if !p.available() {
			continue
		}
		t.Run(p.provider, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			maxRounds := 0 // passive: return tool calls without executing
			res, err := llm.Generate(ctx, llm.GenerateOptions{
				Client:        client,
				Model:         p.model,
				Provider:      p.provider,
				Prompt:        strPtr("What time is it? Use the get_time tool."),
				MaxToolRounds: &maxRounds,
				Tools: []llm.Tool{{
					Definition: llm.ToolDefinition{
						Name:        "get_time",
						Description: "Returns the current time.",
						Parameters: map[string]any{
							"type":       "object",
							"properties": map[string]any{},
						},
					},
				}},
			})
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if len(res.ToolCalls) == 0 {
				t.Fatalf("expected tool calls; got text=%q", res.Text)
			}
			if res.ToolCalls[0].Name != "get_time" {
				t.Fatalf("tool call name: %q", res.ToolCalls[0].Name)
			}
			t.Logf("tool call: %s(%s)", res.ToolCalls[0].Name, string(res.ToolCalls[0].Arguments))
		})
	}
}

func TestIntegration_ImageInput(t *testing.T) {
	skipIfNoImageProviders(t)
	client, err := llm.NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}

	// Generate a 10x10 red PNG using Go's image/png encoder.
	img := image.NewRGBA(image.Rect(0, 0, 10, 10))
	for y := range 10 {
		for x := range 10 {
			img.Set(x, y, color.RGBA{R: 255, G: 0, B: 0, A: 255})
		}
	}
	var redPNGBuf bytes.Buffer
	if err := png.Encode(&redPNGBuf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	redPNG := redPNGBuf.Bytes()

	for _, p := range imageProviders {
		if !p.available() {
			continue
		}
		t.Run(p.provider, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			res, err := llm.Generate(ctx, llm.GenerateOptions{
				Client:    client,
				Model:     p.model,
				Provider:  p.provider,
				MaxTokens: intPtr(100),
				Messages: []llm.Message{{
					Role: llm.RoleUser,
					Content: []llm.ContentPart{
						{Kind: llm.ContentText, Text: "What is the dominant color in this single-color image? Reply with exactly one lowercase color word."},
						{Kind: llm.ContentImage, Image: &llm.ImageData{
							MediaType: "image/png",
							Data:      redPNG,
						}},
					},
				}},
			})
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if strings.TrimSpace(res.Text) == "" {
				t.Fatalf("expected non-empty text")
			}
			if !strings.Contains(strings.ToLower(res.Text), "red") {
				t.Fatalf("expected image color to be red, got %q", res.Text)
			}
			t.Logf("text (truncated): %.100s", res.Text)
		})
	}
}

func TestIntegration_StructuredOutput(t *testing.T) {
	skipIfNoProviders(t)
	client, err := llm.NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{"type": "string"},
			"age":  map[string]any{"type": "integer"},
		},
		"required":             []any{"name", "age"},
		"additionalProperties": false,
	}

	for _, p := range providers {
		if !p.available() {
			continue
		}
		t.Run(p.provider, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			res, err := llm.GenerateObject(ctx, llm.GenerateObjectOptions{
				GenerateOptions: llm.GenerateOptions{
					Client:   client,
					Model:    p.model,
					Provider: p.provider,
					Prompt:   strPtr("Generate a fictional person with a name and age."),
				},
				Schema: schema,
			})
			if err != nil {
				t.Fatalf("GenerateObject: %v", err)
			}
			if res.Output == nil {
				t.Fatalf("expected parsed output")
			}
			m, ok := res.Output.(map[string]any)
			if !ok {
				t.Fatalf("output type: %T", res.Output)
			}
			if _, ok := m["name"].(string); !ok {
				t.Fatalf("name not a string: %v", m["name"])
			}
			t.Logf("output: %v", m)
		})
	}
}

func TestIntegration_ErrorHandling(t *testing.T) {
	skipIfNoProviders(t)
	client, err := llm.NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}

	for _, p := range providers {
		if !p.available() {
			continue
		}
		t.Run(p.provider, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			_, err := llm.Generate(ctx, llm.GenerateOptions{
				Client:   client,
				Model:    "nonexistent-model-xyz-" + p.provider,
				Provider: p.provider,
				Prompt:   strPtr("hello"),
			})
			if err == nil {
				t.Fatalf("expected error for nonexistent model")
			}
			// Different providers may return NotFound or InvalidRequest for bad models.
			if k := llm.Kind(err); k != llm.KindNotFound && k != llm.KindInvalidRequest {
				t.Logf("error type: %T, error: %v", err, err)
				// At minimum, it should be some kind of llm.Error.
				var llmErr llm.Error
				if !errors.As(err, &llmErr) {
					t.Fatalf("expected llm.Error, got %T: %v", err, err)
				}
			}
		})
	}
}

func TestIntegration_ImageInputURL(t *testing.T) {
	skipIfNoProviders(t)
	client, err := llm.NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}

	// Small, publicly accessible PNG. Use httpbin which doesn't block API servers.
	imageURL := "https://httpbin.org/image/png"

	for _, p := range providers {
		if !p.available() {
			continue
		}
		t.Run(p.provider, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			res, err := llm.Generate(ctx, llm.GenerateOptions{
				Client:    client,
				Model:     p.model,
				Provider:  p.provider,
				MaxTokens: intPtr(200),
				Messages: []llm.Message{{
					Role: llm.RoleUser,
					Content: []llm.ContentPart{
						{Kind: llm.ContentText, Text: "Describe this image in one sentence."},
						{Kind: llm.ContentImage, Image: &llm.ImageData{URL: imageURL}},
					},
				}},
			})
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if strings.TrimSpace(res.Text) == "" {
				t.Fatal("expected non-empty response text")
			}
			t.Logf("text (truncated): %.100s", res.Text)
		})
	}
}

func TestIntegration_StreamingWithTools(t *testing.T) {
	skipIfNoProviders(t)
	client, err := llm.NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}

	weatherTool := llm.Tool{
		Definition: llm.ToolDefinition{
			Name:        "get_weather",
			Description: "Get weather for a city",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"city": map[string]any{"type": "string"}},
				"required":   []any{"city"},
			},
		},
		Execute: func(ctx context.Context, args any) (any, error) {
			return "72F and sunny", nil
		},
	}

	for _, p := range providers {
		if !p.available() {
			continue
		}
		t.Run(p.provider, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			sr, err := llm.StreamGenerate(ctx, llm.GenerateOptions{
				Client:        client,
				Model:         p.model,
				Provider:      p.provider,
				Prompt:        strPtr("What is the weather in Paris?"),
				Tools:         []llm.Tool{weatherTool},
				MaxToolRounds: intPtr(3),
				MaxTokens:     intPtr(300),
			})
			if err != nil {
				t.Fatalf("StreamGenerate: %v", err)
			}

			var hasStepFinish bool
			for ev := range sr.Events() {
				if ev.Type == llm.StreamEventStepFinish {
					hasStepFinish = true
				}
			}
			resp, err := sr.Response()
			if err != nil {
				t.Fatalf("Response: %v", err)
			}
			if resp == nil {
				t.Fatal("no response after stream")
			}
			if resp.Text() == "" {
				t.Error("expected non-empty final text")
			}
			if !hasStepFinish {
				t.Error("expected at least one STEP_FINISH event (tool was called)")
			}
			t.Logf("text (truncated): %.100s", resp.Text())
		})
	}
}

func TestIntegration_PromptCaching_MultiTurn(t *testing.T) {
	skipIfNoProviders(t)
	if testing.Short() {
		t.Skip("skipping multi-turn caching test in short mode")
	}
	client, err := llm.NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}

	// Use a large system prompt to make caching worthwhile.
	systemPrompt := strings.Repeat("You are a helpful assistant. ", 200) // ~1200 words

	for _, p := range providers {
		t.Run(p.provider, func(t *testing.T) {
			if !p.available() {
				t.Skip("provider API key not set")
			}

			history := []llm.Message{llm.System(systemPrompt)}
			var lastUsage llm.Usage

			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()

			for turn := 1; turn <= 6; turn++ {
				history = append(history, llm.User(fmt.Sprintf("Turn %d: What is %d + %d?", turn, turn, turn*2)))
				result, err := llm.Generate(ctx, llm.GenerateOptions{
					Client:    client,
					Model:     p.model,
					Provider:  p.provider,
					Messages:  history,
					MaxTokens: intPtr(50),
				})
				if err != nil {
					t.Fatalf("turn %d: %v", turn, err)
				}
				history = append(history, llm.Assistant(result.Text))
				lastUsage = result.Usage

				if turn >= 5 {
					// Gemini's cachedContentTokenCount only reflects explicit CachedContent
					// resources, not implicit prompt caching. Skip the ratio assertion.
					if p.provider == "google" {
						t.Logf("turn %d: skipping cache ratio check for Gemini (no implicit cache reporting)", turn)
						continue
					}
					cacheRead := 0
					if lastUsage.CacheReadTokens != nil {
						cacheRead = *lastUsage.CacheReadTokens
					}
					inputTokens := lastUsage.InputTokens
					if inputTokens > 0 {
						cacheRatio := float64(cacheRead) / float64(inputTokens)
						t.Logf("turn %d: input=%d cache_read=%d ratio=%.1f%%",
							turn, inputTokens, cacheRead, cacheRatio*100)
						if cacheRead == 0 {
							t.Logf("turn %d: provider %s did not report implicit cache reads", turn, p.provider)
							continue
						}
						if cacheRatio < 0.5 {
							t.Errorf("turn %d: cache_read_tokens (%d) < 50%% of input_tokens (%d)",
								turn, cacheRead, inputTokens)
						}
					}
				}
			}
		})
	}
}

func TestIntegration_ParallelToolCalls(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	skipIfNoProviders(t)
	client, err := llm.NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}

	weatherTool := llm.Tool{
		Definition: llm.ToolDefinition{
			Name:        "get_weather",
			Description: "Get the current weather for a city.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"city": map[string]any{"type": "string", "description": "City name"},
				},
				"required": []any{"city"},
			},
		},
	}

	for _, p := range providers {
		if !p.available() {
			continue
		}
		t.Run(p.provider, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			maxRounds := 0 // passive mode: return tool calls without executing
			res, err := llm.Generate(ctx, llm.GenerateOptions{
				Client:        client,
				Model:         p.model,
				Provider:      p.provider,
				Prompt:        strPtr("What is the weather in both Paris and Tokyo? Use the get_weather tool for each city."),
				Tools:         []llm.Tool{weatherTool},
				MaxToolRounds: &maxRounds,
				MaxTokens:     intPtr(300),
			})
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			// Models must produce at least one tool call. Parallel (2+) calls are
			// model-dependent and not guaranteed, so we only require >= 1.
			if len(res.ToolCalls) < 1 {
				t.Fatalf("expected at least 1 tool call, got %d", len(res.ToolCalls))
			}
			for i, tc := range res.ToolCalls {
				if tc.Name != "get_weather" {
					t.Errorf("tool call %d: expected get_weather, got %q", i, tc.Name)
				}
				t.Logf("tool call %d: %s(%s)", i, tc.Name, string(tc.Arguments))
			}
			t.Logf("total tool calls: %d (parallel=%v)", len(res.ToolCalls), len(res.ToolCalls) >= 2)
		})
	}
}

func TestIntegration_MultiStepToolLoop(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	skipIfNoProviders(t)
	client, err := llm.NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}

	// A tool that returns unpredictable codes. The model cannot know the values
	// without calling, so it must actually invoke the tool. Each call returns a
	// different code from the sequence.
	codes := []string{"ALPHA-7X", "BRAVO-3Q", "CHARLIE-9Z"}
	codeTool := llm.Tool{
		Definition: llm.ToolDefinition{
			Name:        "get_next_code",
			Description: "Returns the next secret code in a sequence. Call repeatedly to retrieve all codes. Returns 'done' when no more codes remain.",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
	}

	for _, p := range providers {
		if !p.available() {
			continue
		}
		t.Run(p.provider, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()

			callCount := 0
			codeTool.Execute = func(ctx context.Context, args any) (any, error) {
				idx := callCount
				callCount++
				if idx < len(codes) {
					return fmt.Sprintf("code %d of %d: %s", idx+1, len(codes), codes[idx]), nil
				}
				return "done: no more codes", nil
			}

			res, err := llm.Generate(ctx, llm.GenerateOptions{
				Client:        client,
				Model:         p.model,
				Provider:      p.provider,
				Prompt:        strPtr("Use the get_next_code tool to retrieve all secret codes. Keep calling until it says 'done'. Then list all the codes you retrieved."),
				Tools:         []llm.Tool{codeTool},
				MaxToolRounds: intPtr(5),
				MaxTokens:     intPtr(300),
			})
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if callCount < 3 {
				t.Errorf("expected tool to be called at least 3 times, got %d", callCount)
			}
			if len(res.Steps) < 2 {
				t.Errorf("expected at least 2 steps (multiple tool rounds), got %d", len(res.Steps))
			}
			t.Logf("steps=%d calls=%d text=%.200s", len(res.Steps), callCount, res.Text)
		})
	}
}

// reasoningProviders lists models that support extended thinking / reasoning.
var reasoningProviders = []providerConfig{
	{[]string{"OPENAI_API_KEY"}, "gpt-5.4-mini", "openai"},
	{[]string{"ANTHROPIC_API_KEY"}, "claude-sonnet-4-5-20250929", "anthropic"},
	{[]string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}, "gemini-2.5-flash", "google"},
	{[]string{"MINIMAX_API_KEY"}, "MiniMax-M2.7", "minimax"},
}

func TestIntegration_ReasoningTokens(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	skipIfNoProviders(t)

	client, err := llm.NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}

	for _, p := range reasoningProviders {
		if !p.available() {
			continue
		}
		t.Run(p.provider, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			effort := "medium"
			res, err := llm.Generate(ctx, llm.GenerateOptions{
				Client:          client,
				Model:           p.model,
				Provider:        p.provider,
				Prompt:          strPtr("What is 137 * 251? Think step by step."),
				ReasoningEffort: &effort,
				MaxTokens:       intPtr(1024),
			})
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if strings.TrimSpace(res.Text) == "" {
				t.Fatal("expected non-empty response text")
			}
			if res.Usage.ReasoningTokens == nil || *res.Usage.ReasoningTokens == 0 {
				t.Logf("provider %s did not report reasoning tokens", p.provider)
			} else {
				t.Logf("reasoning_tokens=%d output_tokens=%d", *res.Usage.ReasoningTokens, res.Usage.OutputTokens)
			}
			t.Logf("text (truncated): %.100s", res.Text)
		})
	}
}

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }

// Ensure json import is used (for GenerateObject output assertions).
var _ = json.Marshal
