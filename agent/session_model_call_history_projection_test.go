package agent

// Tests for the request-assembly history projection (#6448). The contract test
// pins what a legitimately optimized assembly must keep: the messages that
// reach the provider, structurally (role, content-part kind, text, tool-call
// and tool-result identity), in the same order. The allocation test pins the
// copies that are pure waste: a target projection that walks the whole
// projected message list without changing anything must not copy the list.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// historyProjectionWireMessage is one dispatched message reduced to what the
// provider wire carries: the role, and each content part's kind plus the text,
// or the tool-call/tool-result identity it holds. Media presence is recorded as
// a type and byte count so a lost payload shows without printing bytes.
type historyProjectionWireMessage struct {
	Role  llm.Role
	Parts []string
}

// historyProjectionWirePart describes one content part by what the request
// carries, not by how it was built: kind, then whichever payload identifies it.
func historyProjectionWirePart(p llm.ContentPart) string {
	desc := string(p.Kind)
	switch {
	case p.Text != "":
		desc += ":text=" + p.Text
	case p.ToolCall != nil:
		desc += ":call=" + p.ToolCall.ID + "/" + p.ToolCall.Name
	case p.ToolResult != nil:
		desc += ":result=" + p.ToolResult.ToolCallID
	case p.Thinking != nil:
		desc += ":thinking=" + p.Thinking.Text
	}
	if p.Kind == llm.ContentToolResult && p.ToolResult != nil && len(p.ToolResult.ImageData) > 0 {
		desc += fmt.Sprintf(":media=%s/%d", p.ToolResult.ImageMediaType, len(p.ToolResult.ImageData))
	}
	return desc
}

// historyProjectionWire reduces a dispatched request to its structural wire
// shape. It ignores pointer identity and struct layout, so a change in how the
// list is assembled that carries the same wire content passes; a lost,
// duplicated or reordered message, or a dropped content part, does not.
func historyProjectionWire(messages []llm.Message) []historyProjectionWireMessage {
	out := make([]historyProjectionWireMessage, 0, len(messages))
	for _, m := range messages {
		parts := make([]string, 0, len(m.Content))
		for _, p := range m.Content {
			parts = append(parts, historyProjectionWirePart(p))
		}
		out = append(out, historyProjectionWireMessage{Role: m.Role, Parts: parts})
	}
	return out
}

// describeHistoryProjectionWire renders a wire shape one message per line, so a
// mismatch names the message that moved instead of dumping the whole request.
func describeHistoryProjectionWire(messages []historyProjectionWireMessage) string {
	var b strings.Builder
	for i, m := range messages {
		fmt.Fprintf(&b, "%d %s [%s]\n", i, m.Role, strings.Join(m.Parts, " | "))
	}
	return b.String()
}

// historyProjectionContractTurns is the fixture the contract test dispatches:
// user text, an assistant turn carrying thinking, answer text and a tool call,
// an aggregated tool-result turn, a steering turn, and closing user text. It
// covers the cases expansion projects differently — a thinking part the target
// policy may drop, an aggregated tool-result turn that expands into individual
// messages, and plain pass-through text.
func historyProjectionContractTurns() []schema.Turn {
	return []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("first user turn")),
		{
			Kind: schema.TurnAssistant,
			Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "reasoning text"}},
				{Kind: llm.ContentText, Text: "assistant answer"},
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_1", Name: "read_file"}},
			}},
		},
		{
			Kind:    schema.TurnToolResults,
			Message: llm.ToolResult("call_1", "file body", false),
		},
		schema.NewTurn(schema.TurnSteering, llm.User("steering text")),
		schema.NewTurn(schema.TurnUserInput, llm.User("second user turn")),
	}
}

// newHistoryProjectionContractSession builds a session on a text-only
// OpenAI-chat row (a target that drops replayed thinking) carrying the contract
// fixture in its history.
func newHistoryProjectionContractSession(t *testing.T) (*Session, *fakeAdapter) {
	t.Helper()
	adapter := &fakeAdapter{name: "unknown-gateway"}
	sess := newSession(t, withProfile(newOpenAICompatProfile(adapter.name, "model-a", 0)), withAdapter(adapter))
	for _, turn := range historyProjectionContractTurns() {
		sess.appendTurn(turn.Kind, turn.Message)
	}
	return sess, adapter
}

// runHistoryProjectionContractTurn drives one real turn and returns the message
// list the provider received.
func runHistoryProjectionContractTurn(t *testing.T, sess *Session, adapter *fakeAdapter) []llm.Message {
	t.Helper()
	adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.ToolCallResponse(communicateCall("c9", "ok")) },
	})
	// TRIPWIRE: scripted in-process adapter; the timeout only catches a hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "continue", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	reqs := adapter.Requests()
	if len(reqs) == 0 {
		t.Fatal("no provider request recorded")
	}
	return reqs[len(reqs)-1].Messages
}

// findHistoryProjectionWindow returns the index at which want appears as a
// contiguous run inside got, or -1. The window is the fixture's turns, which
// sit after the system message and before the ambient environment-context and
// prompt turns this test does not control.
func findHistoryProjectionWindow(got, want []historyProjectionWireMessage) int {
	if len(want) == 0 || len(want) > len(got) {
		return -1
	}
	for start := 0; start+len(want) <= len(got); start++ {
		match := true
		for i := range want {
			if got[start+i].Role != want[i].Role || !slices.Equal(got[start+i].Parts, want[i].Parts) {
				match = false
				break
			}
		}
		if match {
			return start
		}
	}
	return -1
}

// TestHistoryProjectionKeepsDispatchedMessages pins the contract request
// assembly must hold whatever it does with its intermediate slices: each stored
// turn reaches the provider once, in conversation order, carrying the content
// its kind projects to.
//
// This is the whole forwarded conversation window, not a sample of it: the
// fixture's turns must appear as one contiguous run, so a run that gains or
// loses a message, reorders two, or drops a content part fails here. The
// expectation is written out by hand rather than derived from the projection
// code, so it can fail for a real behavior change.
//
// The leading system message is checked only for shape — role, one text part,
// non-empty — because its prose is assembled elsewhere and is not this path's
// contract.
func TestHistoryProjectionKeepsDispatchedMessages(t *testing.T) {
	t.Parallel()
	sess, adapter := newHistoryProjectionContractSession(t)
	messages := runHistoryProjectionContractTurn(t, sess, adapter)
	got := historyProjectionWire(messages)

	if len(got) < 2 || got[0].Role != llm.RoleSystem || len(got[0].Parts) != 1 || !strings.HasPrefix(got[0].Parts[0], "text:text=") {
		t.Fatalf("first dispatched message is not a single-part system prompt:\n%s", describeHistoryProjectionWire(got))
	}

	// The target is a text-only chat row: the replayed thinking part is dropped
	// by the target's policy, and the answer text, tool call and tool result
	// arrive unchanged and in turn order.
	want := []historyProjectionWireMessage{
		{Role: llm.RoleUser, Parts: []string{"text:text=first user turn"}},
		{Role: llm.RoleAssistant, Parts: []string{"text:text=assistant answer", "tool_call:call=call_1/read_file"}},
		{Role: llm.RoleTool, Parts: []string{"tool_result:result=call_1"}},
		{Role: llm.RoleUser, Parts: []string{"text:text=steering text"}},
		{Role: llm.RoleUser, Parts: []string{"text:text=second user turn"}},
	}
	start := findHistoryProjectionWindow(got, want)
	if start < 0 {
		t.Fatalf("dispatched messages do not carry the stored conversation as one contiguous run in order.\nwant window:\n%s\ngot:\n%s",
			describeHistoryProjectionWire(want), describeHistoryProjectionWire(got))
	}
	if start != 1 {
		t.Fatalf("stored conversation starts at message %d, want 1 (immediately after the system prompt):\n%s",
			start, describeHistoryProjectionWire(got))
	}
}

// TestHistoryProjectionPreservesStoredTurns pins that request assembly leaves
// the stored history untouched. It is the settlement for the copy question this
// file exists for: a projection may reorder and drop for the wire, but the
// turns a later round expands must read back exactly as they were stored.
//
// The comparison is against the turns that existed before the turn ran, because
// a turn legitimately appends its own environment-context and prompt turns. A
// shallow copy of the turn slice is what those appends reallocate or overwrite;
// this compares the element bytes, so an in-place rewrite of a stored turn
// fails here even when the slice header survives.
func TestHistoryProjectionPreservesStoredTurns(t *testing.T) {
	t.Parallel()
	sess, adapter := newHistoryProjectionContractSession(t)
	before := requestHistorySourceSnapshot(t, sess)
	sess.mu.Lock()
	fixtureTurns := len(sess.history)
	sess.mu.Unlock()

	messages := runHistoryProjectionContractTurn(t, sess, adapter)
	if len(messages) == 0 {
		t.Fatal("no messages dispatched")
	}
	sess.mu.Lock()
	if len(sess.history) < fixtureTurns {
		sess.mu.Unlock()
		t.Fatalf("history shrank to %d turns, want at least the %d that existed before the turn", len(sess.history), fixtureTurns)
	}
	stored, err := json.Marshal(sess.history[:fixtureTurns])
	sess.mu.Unlock()
	if err != nil {
		t.Fatalf("marshal stored history: %v", err)
	}
	if !bytes.Equal(stored, before) {
		t.Fatalf("request assembly mutated the stored history:\nbefore %s\nafter  %s", before, stored)
	}
}

// TestTargetProjectionsSkipUnchangedList pins that the two target projections a
// round applies before it builds its request do not copy the message list when
// the target leaves every message as it is.
//
// Each projection may rewrite messages only when it has something to rewrite:
// media projection when a tool result carries bytes the target cannot see,
// thinking projection when a message carries reasoning a non-replaying target
// must drop. Both run on every request. On a history with no tool-result media
// and no thinking — the common case, since most turns carry neither — they have
// nothing to change, so each must hand the input list back instead of
// allocating a copy of it first. The round keeps the untrimmed list as its
// fallback source, which is why a list that does change still needs its own
// array.
//
// The counts are per call over one fixture, so a projection that stops copying
// reads 0 here and a reintroduced up-front copy reads 1.
//
// Serial: testing.AllocsPerRun counts every allocation in the process, and a
// concurrent test would land inside its measurement.
func TestTargetProjectionsSkipUnchangedList(t *testing.T) {
	const messageCount = 512
	history := make([]llm.Message, 0, messageCount)
	for i := range messageCount {
		history = append(history, llm.User(requestHistoryPayload(i)))
	}
	// An Anthropic row sees every media type it is handed and replays thinking,
	// so neither projection has anything to do with this history.
	keepsEverything := newAnthropicProfile("claude-opus-4-6")
	if !keepsEverything.SeesToolResultMedia("image/png") {
		t.Fatal("test premise: the fixture target must accept tool-result images")
	}
	if dropsReplayedThinking(keepsEverything) {
		t.Fatal("test premise: the fixture target must replay thinking")
	}
	// A chat row drops replayed thinking but has none here to drop.
	dropsThinking := newOpenAICompatProfile("unknown-gateway", "model-a", 0)
	if !dropsReplayedThinking(dropsThinking) {
		t.Fatal("test premise: the chat fixture target must drop replayed thinking")
	}

	for _, tc := range []struct {
		name    string
		project func([]llm.Message) []llm.Message
	}{
		{
			name: "tool-result media",
			project: func(messages []llm.Message) []llm.Message {
				return projectToolResultMediaFor(keepsEverything, messages)
			},
		},
		{
			name: "thinking",
			project: func(messages []llm.Message) []llm.Message {
				return projectThinkingForTarget(dropsThinking, messages)
			},
		},
	} {
		var wrongLength bool
		allocs := testing.AllocsPerRun(20, func() {
			if projected := tc.project(history); len(projected) != len(history) {
				wrongLength = true
			}
		})
		if wrongLength {
			t.Fatalf("%s projection does not hold %d messages", tc.name, messageCount)
		}
		if allocs > 0 {
			t.Fatalf("%s projection allocated %v times on a %d-message history it does not change, want 0", tc.name, allocs, messageCount)
		}
	}
}

// TestProjectToolResultMediaForCopiesWhenPayloadDrops pins the other side of the
// same projection: when a tool result carries media the target cannot see, the
// projection must hand back a list whose bytes are gone while the untrimmed
// source keeps them. That copy is load-bearing — the round's fallback source
// retains the untrimmed list — so the laziness above must not remove it.
func TestProjectToolResultMediaForCopiesWhenPayloadDrops(t *testing.T) {
	t.Parallel()
	// An OpenAI-chat row cannot see tool-result images.
	profile := newOpenAICompatProfile("kimi", "kimi-k3", 0)
	if profile.SeesToolResultMedia("image/png") {
		t.Fatal("test premise: the fixture target must not accept tool-result images")
	}
	history := []llm.Message{
		llm.User("before"),
		{Role: llm.RoleTool, Content: []llm.ContentPart{{
			Kind: llm.ContentToolResult,
			ToolResult: &llm.ToolResultData{
				ToolCallID: "call_img", Name: "read_file", Content: "[image: chart.png]",
				ImageData: []byte("png-bytes"), ImageMediaType: "image/png",
			},
		}}},
		llm.User("after"),
	}

	projected := projectToolResultMediaFor(profile, history)
	if len(projected) != len(history) {
		t.Fatalf("projected %d messages, want %d", len(projected), len(history))
	}
	for _, message := range projected {
		for _, part := range message.Content {
			if part.Kind == llm.ContentToolResult && part.ToolResult != nil && len(part.ToolResult.ImageData) > 0 {
				t.Fatal("projected list still carries media the target cannot see")
			}
		}
	}
	// The untrimmed source is what a fallback target projects for itself, so it
	// must keep the bytes this target dropped.
	dropped := history[1].Content[0].ToolResult
	if dropped == nil || len(dropped.ImageData) == 0 || dropped.ImageMediaType != "image/png" {
		t.Fatalf("projection mutated the untrimmed source's media: %+v", dropped)
	}
}
