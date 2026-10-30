package contextmgr

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// Every model-facing truncation in this package cuts text at a byte cap. Each
// test below builds text whose cap lands inside a 3-byte rune ("世") and
// asserts the result is still valid UTF-8, so a cut never hands the model a
// torn rune.

// multibyteRun is n runes of a 3-byte character, optionally preceded by one
// ASCII byte so the cap can be made to land mid-rune for caps divisible by 3.
func multibyteRun(prefix string, n int) string {
	return prefix + strings.Repeat("世", n)
}

func TestTruncateKeepsRunesWhole(t *testing.T) {
	got := truncate(multibyteRun("", 10), 4)
	if !utf8.ValidString(got) {
		t.Fatalf("truncate split a rune: %q", got)
	}
}

func TestSummarizeToolResultShellCommandKeepsRunesWhole(t *testing.T) {
	args, _ := json.Marshal(map[string]string{"command": multibyteRun("a", 40)})
	got := summarizeToolResult("shell", "exit code: 0", false, args)
	// The command is %q-quoted, which escapes a torn rune into valid text.
	if !utf8.ValidString(got) || strings.Contains(got, `\x`) {
		t.Fatalf("shell summary split a rune: %q", got)
	}
}

func TestCheckpointShellCommandKeepsRunesWhole(t *testing.T) {
	args, _ := json.Marshal(map[string]string{"command": multibyteRun("a", 40)})
	history := []schema.Turn{{Kind: schema.TurnAssistant, Message: llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
			ID: "c1", Name: "shell", Arguments: args,
		}}},
	}}}
	data := collectCheckpointData(history, len(history), "")
	if len(data.lastShellResults) != 1 {
		t.Fatalf("expected one shell result, got %v", data.lastShellResults)
	}
	if got := data.lastShellResults[0]; !utf8.ValidString(got) || strings.Contains(got, `\x`) {
		t.Fatalf("checkpoint shell command split a rune: %q", got)
	}
}

func TestRenderHistoryForElicitTailKeepsRunesWhole(t *testing.T) {
	history := []schema.Turn{{Kind: schema.TurnUserInput, Message: llm.User(multibyteRun("", 50))}}
	full := renderTurnForElicit(history[0])
	// The kept tail starts 7 bytes in: inside the second rune after the
	// ASCII label.
	got := renderHistoryForElicit(history, len(full)-7)
	if !utf8.ValidString(got) {
		t.Fatalf("elicit history split a rune: %q", got)
	}
}

// summarizeFold caps user messages at 5000 bytes, tool results at 200 and
// assistant text at 500; each cap here lands mid-rune.
func TestSummarizeFoldCapsKeepRunesWhole(t *testing.T) {
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("## Progress\nsummary")} },
	}}
	client := llm.NewClient()
	client.Register(adapter)
	profile := testOpenAIProfileWithContextWindow(1_000_000)
	cm := NewManager(profile, client, cheapmodel.New(client))

	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User(multibyteRun("", 2000))),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant(multibyteRun("a", 400))),
		schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("t1", "shell", multibyteRun("", 100), false)),
		schema.NewTurn(schema.TurnUserInput, llm.User("recent")),
	}
	if _, err := cm.summarizeFold(context.Background(), history, 1, "", nil); err != nil {
		t.Fatalf("summarize: %v", err)
	}
	reqs := adapter.Requests()
	if len(reqs) == 0 {
		t.Fatal("no summarization request was sent")
	}
	for _, req := range reqs {
		for _, m := range req.Messages {
			if !utf8.ValidString(m.Text()) {
				t.Fatalf("summarization prompt holds a torn rune")
			}
		}
	}
}
