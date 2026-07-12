//go:build serffuzz

package contextmgr

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// FuzzGlobalContextmgrTails drives the remaining context-manager branches with
// bounded transcripts and a scripted provider boundary.
func FuzzGlobalContextmgrTails(f *testing.F) {
	f.Add("tail")
	f.Add("")
	f.Fuzz(func(t *testing.T, token string) {
		if len(token) > 64 {
			token = token[:64]
		}
		gctPureTails(t, token)
		gctManagerTails(t, token)
		gctStrategyTails(t, token)
	})
}

func gctPureTails(t *testing.T, token string) {
	t.Helper()
	plain := "tail"
	blocks := parseMarkdownBlocks("### Plain\n" + plain + "\n")
	if len(blocks) != 1 || blocks[0].Heading != "Plain" || blocks[0].Text != plain {
		t.Fatalf("plain markdown blocks = %#v", blocks)
	}
	cleaned := cleanCheckpointConversation([]checkpointConversationEntry{
		{Role: "assistant", Text: " " + plain + " "},
		{Role: "system", Text: "drop"},
	})
	if len(cleaned) != 1 || cleaned[0].Role != "agent" {
		t.Fatalf("cleaned conversation = %#v", cleaned)
	}

	maskObservations(nil, 0, "communicate")
	maskObservations([]schema.Turn{schema.NewTurn(schema.TurnToolResults, llm.User("not result"))}, 0, "communicate")
	if findToolCallArgs(nil, "missing") != nil {
		t.Fatal("missing tool call returned arguments")
	}
	end, msg := parseCommunicateArgs([]byte(`{"output":{"message":" fallback "},"end_turn":true}`))
	if !end || msg != "fallback" {
		t.Fatalf("output fallback = %v, %q", end, msg)
	}
	_ = summarizeToolResult("unknown", token, []byte(`{}`))
	_ = summarizeToolResult("read_file", token, []byte(`{}`))
	_ = summarizeToolResult("shell", token, []byte(`{"command":"`+strings.Repeat("x", 61)+`"}`))
	_ = summarizeToolResult("delegate", token, []byte(`{}`))
	clearThinking(nil, 0)
	clearThinking([]schema.Turn{schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "[thinking: 1 chars]"}},
		{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{}},
	}})}, 0)
	if extractJSONField(`{"other":1}`, "missing") != "" {
		t.Fatal("missing JSON field returned a value")
	}

	data := checkpointData{
		lastShellResults: []string{"1", "2", "3", "4"},
		workingNotes:     []string{strings.Repeat("n", 800), strings.Repeat("m", 800)},
		conversation: []checkpointConversationEntry{
			{Role: "user", Text: strings.Repeat("u", 800)},
			{Role: "agent", Text: strings.Repeat("a", 800)},
		},
	}
	formatted := formatCheckpoint(data, nil, 1)
	if !strings.Contains(formatted, "Last shell results") {
		t.Fatal("checkpoint omitted shell tail")
	}
	if findToolResultByCallID(schema.Turn{}, "missing") != "" {
		t.Fatal("missing tool result returned content")
	}
	if shouldFallbackSummarizationModel(context.Background(), errors.New("ordinary")) {
		t.Fatal("ordinary error requested fallback")
	}
	_ = shouldFallbackSummarizationModel(context.Background(), llm.ErrorFromHTTPStatus("openai", 401, "unauthorized", nil, nil))

	oldCheckpoint := "[CONTEXT CHECKPOINT]\n## Conversation\n\n### User\n\n```text\nold\n```\n"
	shellCall := schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
		Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "shell", Name: "shell", Arguments: []byte(`{"command":"` + strings.Repeat("z", 61) + `"}`)},
	}}})
	_ = collectCheckpointData([]schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("")),
		schema.NewTurn(schema.TurnUserInput, llm.User(oldCheckpoint)),
		shellCall,
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("skip while finding shell result")),
		schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("shell", "shell", "exit_code=2", false)),
	}, 5, "communicate")
}

func gctManagerTails(t *testing.T, token string) {
	t.Helper()
	ctx := context.Background()
	profile := WithCheapModel(testOpenAIProfileWithContextWindow(64).WithModel("gct-model"), "gct-cheap")

	failing := llm.NewClient()
	failing.Register(&agenttest.ScriptedAdapter{
		Provider:       profile.ID(),
		FaultResponder: func(llm.Request) error { return errors.New("scripted failure") },
	})
	cm := NewManager(profile, failing)
	if _, err := cm.ElicitNote(ctx, []schema.Turn{schema.NewTurn(schema.TurnUserInput, llm.User(token))}); err == nil {
		t.Fatal("elicitation failure was swallowed")
	}

	nonFallback := llm.NewClient()
	nonFallback.Register(&agenttest.ScriptedAdapter{Provider: profile.ID(), FaultResponder: func(llm.Request) error {
		return errors.New("non-fallback")
	}})
	if _, err := NewManager(profile, nonFallback).ElicitNote(ctx, nil); err == nil {
		t.Fatal("non-fallback elicitation failure was swallowed")
	}

	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User(strings.Repeat(token+"u", 3000))),
		schema.NewTurn(schema.TurnCheckpoint, llm.User(strings.Repeat("c", 1100))),
		schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: strings.Repeat("a", 600)},
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{Name: "communicate", Arguments: []byte(`{"end_turn":true}`)}},
		}}),
		schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("id", "read_file", strings.Repeat("r", 250), false)),
		schema.NewTurn(schema.TurnSteering, llm.User("steer")),
		schema.NewTurn(schema.TurnUserInput, llm.User("recent")),
	}
	if _, err := cm.summarizeWithLLMSteered(ctx, history, 1, token); err == nil {
		t.Fatal("scripted summarization failure was swallowed")
	}
	if got := renderHistoryForElicit([]schema.Turn{{}}, 20); got != "" {
		t.Fatalf("empty elicitation history = %q", got)
	}
}

func gctStrategyTails(t *testing.T, token string) {
	t.Helper()
	ctx := context.Background()
	profile := testOpenAIProfileWithContextWindow(32)
	client := llm.NewClient()
	client.Register(&agenttest.ScriptedAdapter{Provider: profile.ID(), Responder: func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("summary " + token)}
	}})

	longHistory := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User(strings.Repeat("prompt", 120))),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant(strings.Repeat("analysis", 100))),
		schema.NewTurn(schema.TurnUserInput, llm.User("recent")),
	}

	cpCM := NewManager(profile, client)
	cpCM.PreserveRecentTurns = 1
	cpCM.ObservationMaskThreshold = 0
	cpCM.ThinkingClearThreshold = 0
	cpCM.CheckpointThreshold = 0
	cpCM.SummarizeThreshold = 0
	cpCallbacks := 0
	cpCM.OnCompactionTurn = func(schema.Turn) { cpCallbacks++ }
	cpHistory := append([]schema.Turn(nil), longHistory...)
	if err := NewCheckpointPredStrategy(cpCM).ManageContext(ctx, &cpHistory, 0, ctxmgr_noopEmit); err != nil || cpCallbacks == 0 {
		t.Fatalf("checkpoint strategy callbacks=%d err=%v", cpCallbacks, err)
	}

	obsCM := NewManager(profile, nil)
	obsCM.PreserveRecentTurns = 1
	obsCM.ObservationMaskThreshold = 0
	obsCM.CheckpointThreshold = 0
	obsCallbacks := 0
	obsCM.OnCompactionTurn = func(schema.Turn) { obsCallbacks++ }
	obsHistory := append([]schema.Turn(nil), longHistory...)
	if err := NewObsMaskStrategy(obsCM).ManageContext(ctx, &obsHistory, 0, ctxmgr_noopEmit); err != nil || obsCallbacks == 0 {
		t.Fatalf("obs strategy callbacks=%d err=%v", obsCallbacks, err)
	}

	rd := NewRecursiveDistillStrategy(NewManager(profile, client))
	rd.microSummaries = []string{"micro"}
	if _, err := rd.microSummarize(ctx, client, append(longHistory, schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("id", "shell", strings.Repeat("x", 101), false)))); err != nil {
		t.Fatalf("micro summarize: %v", err)
	}
	if _, err := rd.macroSummarize(ctx, client, []string{strings.Repeat("m", 501)}); err != nil {
		t.Fatalf("macro summarize: %v", err)
	}
	failing := llm.NewClient()
	failing.Register(&agenttest.ScriptedAdapter{Provider: profile.ID(), FaultResponder: func(llm.Request) error { return errors.New("distill failure") }})
	if _, err := rd.microSummarize(ctx, failing, longHistory); err == nil {
		t.Fatal("micro summarization failure was swallowed")
	}
	if _, err := rd.macroSummarize(ctx, failing, []string{"micro"}); err == nil {
		t.Fatal("macro summarization failure was swallowed")
	}

	largePrediction := make([]schema.Turn, 0, 110)
	for i := 0; i < 110; i++ {
		largePrediction = append(largePrediction, schema.NewTurn(schema.TurnAssistant, llm.Assistant(strings.Repeat("p", 300))))
	}
	if _, err := NewCheckpointPredStrategy(NewManager(profile, client)).predictiveCheckpoint(ctx, largePrediction, 0); err != nil {
		t.Fatalf("large predictive checkpoint: %v", err)
	}
	aggressiveMaskObservations([]schema.Turn{schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("error", "shell", "keep", true))}, 0)

	goodHost := &fakeStrategyHost{stateDir: t.TempDir(), id: "good", profile: profile}
	sls, err := NewSessionLogStrategy(NewManager(profile, client), goodHost)
	if err != nil {
		t.Fatalf("new session-log strategy: %v", err)
	}
	_ = sls.sessionLogCheckpoint([]schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("prompt")),
		schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("orphan", "shell", "x", false)),
	}, 1)
	_ = extractOriginalPrompt([]schema.Turn{schema.NewTurn(schema.TurnAssistant, llm.Assistant("skip"))})
	_ = extractOriginalPrompt([]schema.Turn{schema.NewTurn(schema.TurnUserInput, llm.User("[CONTEXT CHECKPOINT]\nnone"))})
	_ = extractOriginalPrompt([]schema.Turn{schema.NewTurn(schema.TurnUserInput, llm.User("[CONTEXT SUMMARY]\nnone"))})
	_ = extractOriginalPromptLine("Original task: no newline", "Original task: ")

	sls.cm.PreserveRecentTurns = 1
	sls.cm.ObservationMaskThreshold = 0
	sls.cm.ThinkingClearThreshold = 0
	sls.cm.CheckpointThreshold = 0
	sls.cm.SummarizeThreshold = 0
	slsCallbacks := 0
	sls.cm.OnCompactionTurn = func(schema.Turn) { slsCallbacks++ }
	slsHistory := append([]schema.Turn(nil), longHistory...)
	if err := sls.ManageContext(ctx, &slsHistory, 0, ctxmgr_noopEmit); err != nil || slsCallbacks == 0 {
		t.Fatalf("session-log callbacks=%d err=%v", slsCallbacks, err)
	}

	blockedRoot := t.TempDir()
	if err := os.MkdirAll(blockedRoot+"/sessions", 0o700); err != nil {
		t.Fatalf("create sessions directory: %v", err)
	}
	if err := os.WriteFile(blockedRoot+"/sessions/child.log.jsonl", []byte(strings.Repeat("x", 70_000)), 0o600); err != nil {
		t.Fatalf("create unreadable session log: %v", err)
	}
	badHost := &fakeStrategyHost{stateDir: blockedRoot, id: "child", profile: profile}
	if _, err := NewSessionLogStrategy(NewManager(profile, client), badHost); err == nil {
		t.Fatal("invalid nested session log path unexpectedly succeeded")
	}
	if _, err := NewOODAStrategy(NewManager(profile, client), badHost); err == nil {
		t.Fatal("invalid OODA session log path unexpectedly succeeded")
	}

	appendRoot := t.TempDir() + "/file"
	if err := os.WriteFile(appendRoot, []byte("blocked"), 0o600); err != nil {
		t.Fatalf("create append-blocked root: %v", err)
	}
	appendHost := &fakeStrategyHost{stateDir: appendRoot, id: "child", profile: profile}
	appendSLS, err := NewSessionLogStrategy(NewManager(profile, client), appendHost)
	if err != nil {
		t.Fatalf("construct append-failing session log: %v", err)
	}
	appendClient := llm.NewClient()
	appendClient.Register(&agenttest.ScriptedAdapter{Provider: profile.ID(), Responder: func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant(`{"action":"shell","summary":"done","outcome":"success"}`)}
	}})
	if err := appendSLS.AfterAction(ctx, longHistory, appendClient); err != nil {
		t.Fatalf("best-effort append failure escaped: %v", err)
	}
}
