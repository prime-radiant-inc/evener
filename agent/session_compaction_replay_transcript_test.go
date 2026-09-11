package agent

import (
	"context"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

const (
	compactionReplayCallID = "compaction-replay-tool-call"
	compactionReplayResult = "replayed tool result"
)

// compactionReplayTranscript runs one real fold whose publication rewrites a
// tool round that was appended while the fold was in flight. The rewritten
// copies carry context_replay, so the returned transcript holds the compaction
// markers plus a duplicate of the tool call and its result.
func compactionReplayTranscript(t *testing.T) string {
	t.Helper()
	entered := make(chan struct{})
	proceed := make(chan struct{})
	var calls atomic.Int32
	cheap := &agenttest.ScriptedAdapter{
		Provider: "compaction-replay-cheap",
		Responder: func(llm.Request) llm.Response {
			if calls.Add(1) == 1 {
				close(entered)
				<-proceed
			}
			return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nsummary\n[END SUMMARY]")}
		},
	}
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	client.Register(cheap)
	stateDir := t.TempDir()
	s := newSession(t,
		withClient(client),
		withProfile(WithCheapModel(NewOpenAIProfile("gpt-5.2"), "compaction-replay-cheap/model")),
		withConfig(SessionConfig{MaxSubagentDepth: 1, NoProjectPrompts: true, StateDir: stateDir}),
		withoutGitSnapshot(),
	)
	seedNumberedSessionHistory(t, s, 12)

	compactErr := make(chan error, 1)
	go func() { compactErr <- s.Compact(context.Background()) }()
	<-entered
	// Both appends run while the fold sits between its snapshot and its
	// publication, so the publication merges them into the published history
	// and rewrites them after the compaction markers as replay copies.
	toolCall := delegateAttentionToolCall(compactionReplayCallID)
	if err := s.appendTurnWithDurableTranscriptMessage(schema.TurnAssistant, toolCall, toolCall); err != nil {
		t.Fatalf("append tool call: %v", err)
	}
	toolResult := llm.ToolResultNamed(compactionReplayCallID, "probe", compactionReplayResult, false)
	if err := s.appendTurnWithDurableTranscriptMessage(schema.TurnToolResults, toolResult, toolResult); err != nil {
		t.Fatalf("append tool result: %v", err)
	}
	close(proceed)
	if err := <-compactErr; err != nil {
		t.Fatalf("Compact: %v", err)
	}
	return transcriptPath(s.stateDir, s.id)
}

// replayToolRoundParts counts how many times message names the replayed tool
// round: once as the call it made and once as the result it carries.
func replayToolRoundParts(message llm.Message) (calls, results int) {
	for _, part := range message.Content {
		if part.ToolCall != nil && part.ToolCall.ID == compactionReplayCallID {
			calls++
		}
		if part.ToolResult != nil && part.ToolResult.ToolCallID == compactionReplayCallID {
			results++
		}
	}
	return calls, results
}

func TestCompactionReplay_PublicTranscriptReadsExcludeReplayCopies(t *testing.T) {
	t.Parallel()
	path := compactionReplayTranscript(t)
	const ref = "local:compaction-replay"

	data, err := readTranscriptFull(path)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	replayed := 0
	for _, entry := range data.Entries {
		if entry.Turn.ContextReplay {
			replayed++
		}
	}
	if replayed != 2 {
		t.Fatalf("durable replay copies = %d, want the rewritten tool call and its result", replayed)
	}
	publicTurns := len(data.Entries) - replayed

	markdownValue, err := readMarkdownPage(path, ref, schema.SessionMeta{}, "", nil, 0, 0)
	if err != nil {
		t.Fatalf("read markdown transcript: %v", err)
	}
	markdown, ok := markdownValue.(readMarkdownEnvelope)
	if !ok {
		t.Fatalf("markdown transcript type = %T", markdownValue)
	}
	if markdown.Meta.TurnsTotal != publicTurns {
		t.Fatalf("markdown turns_total = %d, want %d public turns", markdown.Meta.TurnsTotal, publicTurns)
	}
	if got := strings.Count(markdown.Content, compactionReplayResult); got != 1 {
		t.Fatalf("markdown rendered the tool result %d times, want once: %s", got, markdown.Content)
	}
	if strings.Contains(markdown.Content, "Tool results without a shown call") {
		t.Fatalf("markdown orphaned a tool result from its call: %s", markdown.Content)
	}
	seen := make(map[string]bool)
	turnHeading := regexp.MustCompile(`(?m)^## Turn (\d+) — `)
	for _, heading := range turnHeading.FindAllStringSubmatch(markdown.Content, -1) {
		if seen[heading[1]] {
			t.Fatalf("markdown rendered turn %s twice: %s", heading[1], markdown.Content)
		}
		seen[heading[1]] = true
	}

	outlineValue, err := readOutline(path, ref, "")
	if err != nil {
		t.Fatalf("read transcript outline: %v", err)
	}
	outline, ok := outlineValue.(readOutlineEnvelope)
	if !ok {
		t.Fatalf("outline transcript type = %T", outlineValue)
	}
	if outline.TurnsTotal != publicTurns {
		t.Fatalf("outline turns_total = %d, want %d public turns", outline.TurnsTotal, publicTurns)
	}
	if got := strings.Count(outline.Content, "· Assistant · probe"); got != 1 {
		t.Fatalf("outline listed the tool round %d times, want once: %s", got, outline.Content)
	}

	// The range window is computed over public turns, so the last two are the
	// compaction's own markers rather than the replay copies behind them.
	tailValue, err := readMarkdownPage(path, ref, schema.SessionMeta{}, "last:2", nil, 0, 0)
	if err != nil {
		t.Fatalf("read ranged markdown transcript: %v", err)
	}
	tail := tailValue.(readMarkdownEnvelope)
	if tail.Meta.TurnsTotal != publicTurns || tail.Meta.TurnsRendered != 2 {
		t.Fatalf("ranged markdown meta = total %d rendered %d, want total %d rendered 2", tail.Meta.TurnsTotal, tail.Meta.TurnsRendered, publicTurns)
	}
	if strings.Contains(tail.Content, compactionReplayResult) {
		t.Fatalf("ranged markdown window landed on the replay copies: %s", tail.Content)
	}

	rawValue, err := readRaw(path, ref, "")
	if err != nil {
		t.Fatalf("read raw transcript: %v", err)
	}
	raw := rawValue.(readRawEnvelope)
	lines := strings.Split(strings.TrimSpace(raw.Content), "\n")
	if len(lines) != publicTurns+1 {
		t.Fatalf("public JSONL lines = %d, want header plus %d public turns: %s", len(lines), publicTurns, raw.Content)
	}
	callIDs, resultIDs := 0, 0
	for _, line := range lines[1:] {
		entry, err := transcript.DecodeEntry([]byte(line))
		if err != nil {
			t.Fatalf("decode public entry: %v", err)
		}
		if entry.Turn.ContextReplay {
			t.Fatalf("public JSONL exposed a replay copy: %s", line)
		}
		calls, results := replayToolRoundParts(entry.Turn.Message)
		callIDs += calls
		resultIDs += results
	}
	if callIDs != 1 || resultIDs != 1 {
		t.Fatalf("public JSONL paired call ID %d times with %d results, want exactly one of each", callIDs, resultIDs)
	}
}

func TestCompactionReplay_ResumeHistoryRetainsReplayCopies(t *testing.T) {
	t.Parallel()
	path := compactionReplayTranscript(t)

	data, err := readTranscriptFull(path)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	callIDs, resultIDs := 0, 0
	for _, turn := range ResumeHistory(data.Entries) {
		if turn.ContextReplay {
			t.Fatalf("resume history carried the durable replay marker: %#v", turn)
		}
		calls, results := replayToolRoundParts(turn.Message)
		callIDs += calls
		resultIDs += results
	}
	if callIDs != 1 || resultIDs != 1 {
		t.Fatalf("resume history rebuilt the tool round as %d calls and %d results, want exactly one of each", callIDs, resultIDs)
	}

	// The originals sit before the compaction marker, so the copies are the
	// only record of this tool round resume can reach.
	withoutReplay := make([]transcript.Entry, 0, len(data.Entries))
	for _, entry := range data.Entries {
		if !entry.Turn.ContextReplay {
			withoutReplay = append(withoutReplay, entry)
		}
	}
	for _, turn := range ResumeHistory(withoutReplay) {
		if calls, results := replayToolRoundParts(turn.Message); calls != 0 || results != 0 {
			t.Fatalf("tool round survived without its replay copies: %#v", turn)
		}
	}
}
