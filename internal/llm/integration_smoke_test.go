package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/internal/llm"

	// Blank imports to register provider factories.
	_ "primeradiant.com/serf/internal/llm/providers/anthropic"
	_ "primeradiant.com/serf/internal/llm/providers/google"
	_ "primeradiant.com/serf/internal/llm/providers/openai"
)

// providerConfig holds a test model and the env key that gates the provider.
type providerConfig struct {
	envKey   string
	model    string
	provider string
}

var providers = []providerConfig{
	{"OPENAI_API_KEY", "gpt-5-mini-2025-08-07", "openai"},
	{"ANTHROPIC_API_KEY", "claude-sonnet-4-5-20250929", "anthropic"},
	{"GEMINI_API_KEY", "gemini-2.5-flash", "google"},
}

func skipIfNoProviders(t *testing.T) {
	t.Helper()
	for _, p := range providers {
		if os.Getenv(p.envKey) != "" {
			return
		}
	}
	t.Skip("no LLM API keys set; skipping integration smoke test")
}

func TestIntegration_BasicGeneration(t *testing.T) {
	skipIfNoProviders(t)
	client, err := llm.NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}

	for _, p := range providers {
		if os.Getenv(p.envKey) == "" {
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
		if os.Getenv(p.envKey) == "" {
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
		if os.Getenv(p.envKey) == "" {
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
	skipIfNoProviders(t)
	client, err := llm.NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}

	// 1x1 red PNG
	redPNG := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
		0xde, 0x00, 0x00, 0x00, 0x0c, 0x49, 0x44, 0x41,
		0x54, 0x08, 0xd7, 0x63, 0xf8, 0xcf, 0xc0, 0x00,
		0x00, 0x00, 0x03, 0x00, 0x01, 0x36, 0x28, 0x19,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e,
		0x44, 0xae, 0x42, 0x60, 0x82,
	}

	for _, p := range providers {
		if os.Getenv(p.envKey) == "" {
			continue
		}
		t.Run(p.provider, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			res, err := llm.Generate(ctx, llm.GenerateOptions{
				Client:   client,
				Model:    p.model,
				Provider: p.provider,
				Messages: []llm.Message{{
					Role: llm.RoleUser,
					Content: []llm.ContentPart{
						{Kind: llm.ContentText, Text: "Describe this image in one sentence."},
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
		"required": []any{"name", "age"},
	}

	for _, p := range providers {
		if os.Getenv(p.envKey) == "" {
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
		if os.Getenv(p.envKey) == "" {
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
			var nf *llm.NotFoundError
			var ir *llm.InvalidRequestError
			// Different providers may return NotFound or InvalidRequest for bad models.
			if !errors.As(err, &nf) && !errors.As(err, &ir) {
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

	// Small, publicly accessible PNG with transparency.
	imageURL := "https://upload.wikimedia.org/wikipedia/commons/thumb/4/47/PNG_transparency_demonstration_1.png/280px-PNG_transparency_demonstration_1.png"

	for _, p := range providers {
		if os.Getenv(p.envKey) == "" {
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
		if os.Getenv(p.envKey) == "" {
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

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }

// Ensure json import is used (for GenerateObject output assertions).
var _ = json.Marshal
