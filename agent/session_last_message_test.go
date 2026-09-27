package agent

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// An assistant response's agent message is its last text part: the transcript
// shows each text part as its own agent message, the last one last. Reasoning,
// redacted reasoning and tool call arguments are never part of it.
func TestLastAgentText_IsTheLastTextPart(t *testing.T) {
	t.Parallel()
	message := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "private reasoning"}},
		{Kind: llm.ContentText, Text: "Looking at the tests."},
		{Kind: llm.ContentRedThinking, Thinking: &llm.ThinkingData{Redacted: true}},
		{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "c1", Name: "shell", Arguments: []byte(`{"command":"cat secrets.env"}`)}},
		{Kind: llm.ContentText, Text: "Three layouts are ready."},
		{Kind: llm.ContentText, Text: "  \n "},
	}}
	if got := lastAgentText(message); got != "Three layouts are ready." {
		t.Fatalf("lastAgentText = %q, want the last text part", got)
	}
	thinking := llm.Message{Content: []llm.ContentPart{{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "only reasoning"}}}}
	if got := lastAgentText(thinking); got != "" {
		t.Fatalf("a response with no text reports %q", got)
	}
}

// A turn's last agent message leaves its opening on the session as one line cut
// to the wire's bound, and the turn's end persists it in the meta, so an ended
// session and a restarted daemon still have it (S1d). The response's own text
// before the delivered message, and its reasoning, are not what it ended with.
func TestLastMessage_RecordsTheOpeningOfTheLastAgentMessage(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	report := "Three layouts are ready for review.\n\nI recommend B: " + strings.Repeat("it keeps the project first. ", 20)
	sess := newSession(t, withConfig(SessionConfig{StateDir: dir}), withSteps(
		func(llm.Request) llm.Response {
			response := communicateResponse(true, report)
			response.Message.Content = append([]llm.ContentPart{
				{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "private reasoning"}},
				{Kind: llm.ContentText, Text: "Writing the report now."},
			}, response.Message.Content...)
			return response
		},
	))
	if got := sess.Meta().LastMessage; got != "" {
		t.Fatalf("a session that has written nothing reports %q", got)
	}
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "mock up three layouts", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	want := appwire.Excerpt(report, appwire.MaxMessageExcerptRunes)
	if !strings.HasPrefix(want, "Three layouts are ready for review. I recommend B:") || utf8.RuneCountInString(want) > appwire.MaxMessageExcerptRunes {
		t.Fatalf("the fixture's excerpt %q is not one bounded line", want)
	}
	if got := sess.Meta().LastMessage; got != want {
		t.Fatalf("LastMessage = %q, want %q", got, want)
	}
	saved, err := schema.LoadSessionMeta(dir, sess.ID())
	if err != nil || saved.LastMessage != want {
		t.Fatalf("saved LastMessage = %q (%v), want %q", saved.LastMessage, err, want)
	}
}

// A restored session keeps the opening of its last agent message, so its
// Finished row still says what it finished with after a daemon restart (S1d).
func TestLastMessage_SurvivesRestore(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	meta := schema.SessionMeta{
		ID:          "01RESTORELASTMESSAGE0001",
		ProfileID:   "openai",
		Model:       "gpt-5.2",
		CreatedAt:   time.Date(2026, 9, 27, 11, 0, 0, 0, time.UTC),
		LastMessage: "Three layouts are ready for review.",
	}
	sess, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, "")
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer sess.Close()
	if got := sess.Meta().LastMessage; got != meta.LastMessage {
		t.Fatalf("restored LastMessage = %q, want %q", got, meta.LastMessage)
	}
}
