package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/llm"
)

func TestBuildSystemPrompt(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.txt")
	if err := os.WriteFile(base, []byte("  BASE SYSTEM  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	appA := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(appA, []byte("APP A\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	appEmpty := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(appEmpty, []byte("   \n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("both text and file is an error", func(t *testing.T) {
		if _, err := buildSystemPrompt("x", base, nil); err == nil {
			t.Fatal("expected error when both --system and --system-file are set")
		}
	})

	t.Run("text only is trimmed", func(t *testing.T) {
		got, err := buildSystemPrompt("  hello  ", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		if got != "hello" {
			t.Fatalf("got %q, want %q", got, "hello")
		}
	})

	t.Run("file only is read and trimmed", func(t *testing.T) {
		got, err := buildSystemPrompt("", base, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got != "BASE SYSTEM" {
			t.Fatalf("got %q, want %q", got, "BASE SYSTEM")
		}
	})

	t.Run("missing file errors", func(t *testing.T) {
		if _, err := buildSystemPrompt("", filepath.Join(dir, "nope.txt"), nil); err == nil {
			t.Fatal("expected error reading missing --system-file")
		}
	})

	t.Run("append joins base and files, skips empties", func(t *testing.T) {
		got, err := buildSystemPrompt("head", "", []string{appA, appEmpty})
		if err != nil {
			t.Fatal(err)
		}
		if got != "head\n\nAPP A" {
			t.Fatalf("got %q, want %q", got, "head\n\nAPP A")
		}
	})

	t.Run("append with empty base drops the base part", func(t *testing.T) {
		got, err := buildSystemPrompt("", "", []string{appA})
		if err != nil {
			t.Fatal(err)
		}
		if got != "APP A" {
			t.Fatalf("got %q, want %q", got, "APP A")
		}
	})

	t.Run("missing append file errors", func(t *testing.T) {
		if _, err := buildSystemPrompt("head", "", []string{filepath.Join(dir, "nope.txt")}); err == nil {
			t.Fatal("expected error reading missing --system-append")
		}
	})
}

func TestReadJSONSchemaFile(t *testing.T) {
	dir := t.TempDir()

	valid := filepath.Join(dir, "ok.json")
	if err := os.WriteFile(valid, []byte(`{"type":"object"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readJSONSchemaFile(valid)
	if err != nil {
		t.Fatal(err)
	}
	if got["type"] != "object" {
		t.Fatalf("got %#v, want type=object", got)
	}

	if _, err := readJSONSchemaFile(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("expected error for missing schema file")
	}

	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readJSONSchemaFile(bad); err == nil {
		t.Fatal("expected error for invalid JSON schema")
	}

	empty := filepath.Join(dir, "null.json")
	if err := os.WriteFile(empty, []byte(`null`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readJSONSchemaFile(empty); err == nil {
		t.Fatal("expected error for null (empty object) schema")
	}
}

func TestWriteJSON(t *testing.T) {
	var compact bytes.Buffer
	if err := writeJSON(&compact, map[string]int{"a": 1}, false); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(compact.String()) != `{"a":1}` {
		t.Fatalf("compact = %q", compact.String())
	}

	var pretty bytes.Buffer
	if err := writeJSON(&pretty, map[string]int{"a": 1}, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pretty.String(), "\n  \"a\": 1") {
		t.Fatalf("pretty output not indented: %q", pretty.String())
	}

	// An unmarshalable value surfaces the marshal error.
	if err := writeJSON(&bytes.Buffer{}, make(chan int), false); err == nil {
		t.Fatal("expected marshal error for a channel value")
	}
}

func TestPrintUsage(t *testing.T) {
	// nil result is a no-op.
	var buf bytes.Buffer
	printUsage(&buf, nil)
	if buf.Len() != 0 {
		t.Fatalf("expected no output for nil result, got %q", buf.String())
	}

	res := &llm.GenerateResult{
		FinishReason: llm.FinishReason{Reason: llm.FinishReasonStop},
		TotalUsage:   llm.Usage{InputTokens: 3, OutputTokens: 5, TotalTokens: 8},
		Response:     llm.Response{Model: "m1", Provider: "fake"},
	}
	printUsage(&buf, res)
	out := buf.String()
	for _, want := range []string{"[model] m1 (fake)", "[finish] stop", "in=3 out=5 total=8"} {
		if !strings.Contains(out, want) {
			t.Fatalf("printUsage output missing %q; got %q", want, out)
		}
	}
}

func TestRunLLMCall_InvalidFormat(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "fake"})
	err := runLLMCall(context.Background(), llmCallConfig{
		prompt: "hi", provider: "fake", model: "m1", format: "xml",
		stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, client: c,
	})
	if err == nil || !strings.Contains(err.Error(), "invalid --format") {
		t.Fatalf("expected invalid format error, got %v", err)
	}
}

func TestRunLLMCall_BadMetadata(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "fake"})
	err := runLLMCall(context.Background(), llmCallConfig{
		prompt: "hi", provider: "fake", model: "m1", format: "text",
		metadata: []string{"=novalue"},
		stdout:   &bytes.Buffer{}, stderr: &bytes.Buffer{}, client: c,
	})
	if err == nil || !strings.Contains(err.Error(), "invalid --meta") {
		t.Fatalf("expected metadata error, got %v", err)
	}
}

func TestRunLLMCall_SchemaReadError(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "fake"})
	err := runLLMCall(context.Background(), llmCallConfig{
		prompt: "hi", provider: "fake", model: "m1",
		schema: filepath.Join(t.TempDir(), "missing.json"),
		stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, client: c,
	})
	if err == nil || !strings.Contains(err.Error(), "read --schema") {
		t.Fatalf("expected schema read error, got %v", err)
	}
}

func TestRunLLMCall_JSONFormatParseError(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "fake",
		resp: llm.Response{
			Model: "m1", Provider: "fake",
			Message: llm.Assistant("not json at all"),
			Finish:  llm.FinishReason{Reason: llm.FinishReasonStop},
		},
	})
	err := runLLMCall(context.Background(), llmCallConfig{
		prompt: "hi", provider: "fake", model: "m1", format: "json",
		stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, client: c,
	})
	if err == nil || !strings.Contains(err.Error(), "failed to parse JSON output") {
		t.Fatalf("expected JSON parse error, got %v", err)
	}
}

func TestRunLLMCall_VerboseAndSystemAndMeta(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "fake",
		check: func(req llm.Request) {
			if len(req.Messages) == 0 || req.Messages[0].Role != llm.RoleSystem {
				t.Errorf("expected a leading system message, got %#v", req.Messages)
			} else if txt := req.Messages[0].Content[0].Text; txt != "sys!" {
				t.Errorf("expected system prompt %q, got %q", "sys!", txt)
			}
			if req.Metadata["k"] != "v" {
				t.Errorf("expected metadata k=v, got %#v", req.Metadata)
			}
		},
		resp: llm.Response{
			Model: "m1", Provider: "fake",
			Message: llm.Assistant("hi there"),
			Finish:  llm.FinishReason{Reason: llm.FinishReasonStop},
		},
	})
	var stdout, stderr bytes.Buffer
	err := runLLMCall(context.Background(), llmCallConfig{
		prompt: "hi", provider: "fake", model: "m1", format: "text",
		systemText: "sys!", metadata: []string{"k=v"}, verbose: true,
		stdout: &stdout, stderr: &stderr, client: c,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(stdout.String()) != "hi there" {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "[usage]") {
		t.Fatalf("expected verbose usage on stderr, got %q", stderr.String())
	}
}

func TestRunLLMCall_AllSamplingOptions(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "fake",
		check: func(req llm.Request) {
			if req.Temperature == nil || *req.Temperature != 0.25 {
				t.Errorf("temperature = %v", req.Temperature)
			}
			if req.TopP == nil || *req.TopP != 0.9 {
				t.Errorf("top_p = %v", req.TopP)
			}
			if req.MaxTokens == nil || *req.MaxTokens != 42 {
				t.Errorf("max_tokens = %v", req.MaxTokens)
			}
			if req.ReasoningEffort == nil || *req.ReasoningEffort != "high" {
				t.Errorf("reasoning_effort = %v", req.ReasoningEffort)
			}
			if len(req.StopSequences) != 1 || req.StopSequences[0] != "STOP" {
				t.Errorf("stop_sequences = %v", req.StopSequences)
			}
			if !req.WebSearch {
				t.Errorf("expected web_search enabled")
			}
		},
	})
	err := runLLMCall(context.Background(), llmCallConfig{
		prompt: "hi", provider: "fake", model: "m1", format: "text",
		temperature: 0.25, topP: 0.9, maxTokens: 42, reasoningEffort: " high ",
		stopSequences: []string{"STOP"}, webSearch: true,
		stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, client: c,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRunLLMCall_SchemaVerbosePretty(t *testing.T) {
	schema := filepath.Join(t.TempDir(), "s.json")
	if err := os.WriteFile(schema, []byte(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "fake",
		resp: llm.Response{
			Model: "m1", Provider: "fake",
			Message: llm.Assistant(`{"ok": true}`),
			Finish:  llm.FinishReason{Reason: llm.FinishReasonStop},
		},
	})
	var stdout, stderr bytes.Buffer
	err := runLLMCall(context.Background(), llmCallConfig{
		prompt: "hi", provider: "fake", model: "m1", schema: schema, verbose: true, pretty: true,
		stdout: &stdout, stderr: &stderr, client: c,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "\"ok\": true") {
		t.Fatalf("expected pretty schema output, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "[usage]") {
		t.Fatalf("expected verbose usage on stderr, got %q", stderr.String())
	}
}

func TestLLMCallMain_NoPromptWithBadTimeout(t *testing.T) {
	var stdout, stderr bytes.Buffer
	// A prompt arg is present, so stdin is never consulted; the bad --timeout fails.
	err := llmcallMain([]string{"--timeout", "notaduration", "hello"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "invalid --timeout") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

func TestLLMCallMain_MissingProviderAndModel(t *testing.T) {
	for _, ev := range []envvars.Var{envvars.LLMProvider, envvars.SERFProvider, envvars.LLMModel, envvars.SERFModel} {
		t.Setenv(ev.Name, "")
	}
	var stdout, stderr bytes.Buffer
	err := llmcallMain([]string{"hello"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "no provider specified") {
		t.Fatalf("expected provider error, got %v", err)
	}

	// Provider set, model still missing.
	stdout.Reset()
	stderr.Reset()
	err = llmcallMain([]string{"--provider", "fake", "hello"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "no model specified") {
		t.Fatalf("expected model error, got %v", err)
	}
}

func TestLLMCallMain_HelpFlagReturnsNil(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := llmcallMain([]string{"-h"}, &stdout, &stderr); err != nil {
		t.Fatalf("expected nil error for -h, got %v", err)
	}
}

func TestLLMCallMain_UnknownFlagErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := llmcallMain([]string{"--definitely-not-a-flag"}, &stdout, &stderr); err == nil {
		t.Fatal("expected error for unknown flag")
	}
}
