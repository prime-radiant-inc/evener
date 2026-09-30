package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/llm"
)

// TestConfigureHermeticRunEnvDefaultHidesUserSkills pins that a hermetic run
// (inheritOperatorEnv=false, today's default) hides the operator's personal
// skills from every run: it sets EVENER_NO_USER_SKILLS, and the environment
// the CLI probe hands a spawned evener (fixtureEnv) carries it, as an
// in-process live session already inherits it (#3227).
func TestConfigureHermeticRunEnvDefaultHidesUserSkills(t *testing.T) {
	t.Setenv(envvars.EVENERNoUserSkills.Name, "")
	configureHermeticRunEnv(false)
	if got := envvars.EVENERNoUserSkills.Getenv(); got != "1" {
		t.Fatalf("%s = %q, want 1", envvars.EVENERNoUserSkills.Name, got)
	}
	want := envvars.EVENERNoUserSkills.Assignment("1")
	if !slices.Contains(fixtureEnv(t.TempDir()), want) {
		t.Fatalf("fixtureEnv does not carry %q", want)
	}
}

// TestConfigureHermeticRunEnvInheritOperatorEnvRestoresUserSkills pins
// --inherit-operator-env's debugging escape hatch: it must clear
// EVENER_NO_USER_SKILLS so a run goes back to seeing the operator's real home
// and user-config skills, exactly like before #3227.
func TestConfigureHermeticRunEnvInheritOperatorEnvRestoresUserSkills(t *testing.T) {
	t.Setenv(envvars.EVENERNoUserSkills.Name, "1")
	configureHermeticRunEnv(true)
	if got := envvars.EVENERNoUserSkills.Getenv(); got == "1" {
		t.Fatalf("%s = %q after --inherit-operator-env, want cleared", envvars.EVENERNoUserSkills.Name, got)
	}
}

// TestLastBytesNeverSplitsARune: lastBytes cuts at a byte offset, which can
// land inside a multi-byte UTF-8 rune. It must back off to a rune boundary
// instead of returning invalid UTF-8, for every cut point through the
// string, the way agent/doctor.Truncate does when it cuts from the front.
func TestLastBytesNeverSplitsARune(t *testing.T) {
	t.Parallel()
	const s = "prefix-日本語-suffix"
	for n := 0; n <= len(s)+2; n++ {
		got := lastBytes(s, n)
		if !utf8.ValidString(got) {
			t.Fatalf("lastBytes(%q, %d) = %q, not valid UTF-8", s, n, got)
		}
	}
}

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

	args, err := cliProbeArgs(cfg, probe, res)
	if err != nil {
		t.Fatalf("cliProbeArgs: %v", err)
	}

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
	const rootID = "02wMz5Txv1C3Hut0M8GCeB"
	const childID = "02wMz5Txv2enqVTitaig6F"
	writeFluencyTranscript(t, stateDir, rootID, []schema.Turn{
		schema.NewTurn(schema.TurnAssistant, llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				fluencyToolCall("delegate", `{"task":"watch"}`),
				fluencyToolCall("read_file", `{"file_path":"watch-trigger.txt"}`),
			},
		}),
	})
	writeFluencyTranscript(t, stateDir, childID, []schema.Turn{
		schema.NewTurn(schema.TurnAssistant, llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				fluencyToolCall("job_watch", `{"source":"parent"}`),
				fluencyToolCall("communicate", `{"message":"OBSERVER_READY","end_turn":true}`),
				// read_file also appears in rootID; its counts must be summed, not overwritten.
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
