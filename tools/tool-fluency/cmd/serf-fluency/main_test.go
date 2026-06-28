package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

func TestRunSuiteRejectsUnknownHarness(t *testing.T) {
	err := runSuite([]string{"--harness", "bogus"})
	if err == nil {
		t.Fatal("runSuite returned nil, want harness validation error")
	}
	if !strings.Contains(err.Error(), "--harness must be cli or live") {
		t.Fatalf("error = %q, want harness guidance", err.Error())
	}
}

func TestCLIProbeArgsIncludesSystemPromptAppendFiles(t *testing.T) {
	cfg := runConfig{
		model:              "openai/test-model",
		fastCheapModel:     "openai/cheap-model",
		systemPromptAppend: []string{"/tmp/append-a.md", " ", "/tmp/append-b.md"},
		reasoningEffort:    "high",
	}
	probe := probeFile{Prompt: "inspect the fixture"}
	res := probeResult{WorkDir: "/work", StateDir: "/state"}

	args := cliProbeArgs(cfg, probe, res)

	want := []string{
		"--system-prompt-append", "/tmp/append-a.md",
		"--system-prompt-append", "/tmp/append-b.md",
	}
	assertSubsequence(t, args, want)

	// The whitespace-only entry ' ' must be excluded: exactly 2 flags, not 3.
	count := 0
	for _, a := range args {
		if a == "--system-prompt-append" {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("--system-prompt-append flag count = %d, want 2 (blank entry must be excluded)", count)
	}
}

func TestMaybeClearOpenAIAPIKeyRestoresExistingValue(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-existing")

	restore := maybeClearOpenAIAPIKey(true)
	if got, ok := os.LookupEnv("OPENAI_API_KEY"); ok || got != "" {
		t.Fatalf("OPENAI_API_KEY after clear = %q, %v; want unset", got, ok)
	}

	restore()
	if got := os.Getenv("OPENAI_API_KEY"); got != "sk-existing" {
		t.Fatalf("OPENAI_API_KEY after restore = %q, want original", got)
	}
}

func TestAllTranscriptToolCountsIncludesChildSessions(t *testing.T) {
	stateDir := t.TempDir()
	writeFluencyTranscript(t, stateDir, "root_session", []schema.Turn{
		schema.NewTurn(schema.TurnAssistant, llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				fluencyToolCall("delegate", `{"task":"watch"}`),
				fluencyToolCall("read_file", `{"file_path":"watch-trigger.txt"}`),
			},
		}),
	})
	writeFluencyTranscript(t, stateDir, "child_session", []schema.Turn{
		schema.NewTurn(schema.TurnAssistant, llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				fluencyToolCall("job_watch", `{"source":"parent"}`),
				fluencyToolCall("communicate", `{"message":"OBSERVER_READY","end_turn":true}`),
				// read_file also appears in root_session; its counts must be summed, not overwritten.
				fluencyToolCall("read_file", `{"file_path":"child-result.txt"}`),
			},
		}),
	})

	got, err := allTranscriptToolCounts(stateDir)
	if err != nil {
		t.Fatalf("allTranscriptToolCounts: %v", err)
	}

	assertToolCount(t, got, "delegate", 1)
	assertToolCount(t, got, "read_file", 2) // one call per session — += accumulation path exercised
	assertToolCount(t, got, "job_watch", 1)
	assertToolCount(t, got, "communicate", 1)
}

func assertSubsequence(t *testing.T, haystack, needle []string) {
	t.Helper()
	next := 0
	for _, value := range haystack {
		if value == needle[next] {
			next++
			if next == len(needle) {
				return
			}
		}
	}
	t.Fatalf("args = %#v, want subsequence %#v", haystack, needle)
}

func TestMaybeClearOpenAIAPIKeyRestoresUnsetState(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "temporary")
	if err := os.Unsetenv("OPENAI_API_KEY"); err != nil {
		t.Fatalf("Unsetenv: %v", err)
	}

	restore := maybeClearOpenAIAPIKey(true)
	if got, ok := os.LookupEnv("OPENAI_API_KEY"); ok || got != "" {
		t.Fatalf("OPENAI_API_KEY after clear = %q, %v; want unset", got, ok)
	}

	restore()
	if got, ok := os.LookupEnv("OPENAI_API_KEY"); ok || got != "" {
		t.Fatalf("OPENAI_API_KEY after restore = %q, %v; want unset", got, ok)
	}
}

func writeFluencyTranscript(t *testing.T, stateDir, sid string, turns []schema.Turn) {
	t.Helper()
	path := filepath.Join(stateDir, "sessions", sid+".transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: sid})
	if err != nil {
		t.Fatalf("transcript.NewWriter: %v", err)
	}
	for _, turn := range turns {
		if err := w.Append(turn); err != nil {
			t.Fatalf("append transcript turn: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close transcript writer: %v", err)
	}
}

func fluencyToolCall(name, args string) llm.ContentPart {
	return llm.ContentPart{
		Kind: llm.ContentToolCall,
		ToolCall: &llm.ToolCallData{
			ID:        "call_" + name,
			Name:      name,
			Arguments: json.RawMessage(args),
		},
	}
}

func assertToolCount(t *testing.T, counts map[string]int, name string, want int) {
	t.Helper()
	if got := counts[name]; got != want {
		t.Fatalf("%s count = %d, want %d; counts=%v", name, got, want, counts)
	}
}
