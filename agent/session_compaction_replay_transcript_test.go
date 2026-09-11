package agent

import (
	"context"
	"regexp"
	"slices"
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
// copies carry context_replay and the fold's id, and sit just ahead of the
// compaction markers that carry the same id, so the returned transcript holds
// a duplicate of the tool call and its result followed by the fold's own
// records.
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
	// and rewrites them as replay copies just ahead of the compaction
	// markers, tagged with the same fold id.
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

// A fold publishes on every model request, whether or not its layers produced
// a replacement marker (session_model_call.go's ManageContext block is gated
// only on s.strategy != nil). The tail rewrite exists so pairs recorded DURING
// a fold survive ResumeHistory's last-marker anchor, which discards everything
// before that marker — but a fold that landed no marker moved no anchor, so
// those pairs' own entries are already on the surviving side and a copy of
// them is a pure duplicate. Writing one leaves the transcript holding both,
// and against an EARLIER fold's marker the anchored branch returns the pair
// twice.
func TestCompactionReplay_NoMarkerFoldWritesNoReplayTail(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	s := newSession(t,
		withConfig(SessionConfig{MaxSubagentDepth: 1, NoProjectPrompts: true, StateDir: stateDir}),
		withoutGitSnapshot(),
	)
	const durableText = "recorded while a marker-less fold was in flight"

	s.mu.Lock()
	histCopy := append([]schema.Turn{}, s.history...)
	snapLen := len(s.history)
	snapRevision := s.historyRevision
	snapAppends := s.persistedAppendLogBase + len(s.persistedAppendLog)
	s.mu.Unlock()

	// The pair lands after the fold's snapshot, so it is exactly what the tail
	// rewrite would re-append.
	msg := llm.User(durableText)
	if err := s.appendTurnWithDurableTranscriptMessage(schema.TurnUserInput, msg, msg); err != nil {
		t.Fatalf("durable append: %v", err)
	}

	// Stage and publish without running any layer: no checkpoint, no summary.
	_, _, commit, _ := s.stageCompactionEffects(context.Background(), &histCopy)
	if _, ok := s.publishFoldTransaction(snapLen, snapRevision, snapAppends, histCopy, commit, nil); !ok {
		t.Fatal("fold lost the publication race with nothing else publishing")
	}

	data, err := readTranscriptFull(transcriptPath(s.stateDir, s.id))
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	markers, copies, originals := 0, 0, 0
	for _, entry := range data.Entries {
		switch {
		case entry.Turn.Kind == schema.TurnCheckpoint || entry.Turn.Kind == schema.TurnSummary:
			markers++
		case entry.Turn.ContextReplay:
			copies++
		case entry.Turn.Message.Text() == durableText:
			originals++
		}
	}
	if markers != 0 {
		t.Fatalf("test setup: the fold landed %d replacement markers, so this is not the marker-less path", markers)
	}
	if originals != 1 {
		t.Fatalf("durable pair reached the transcript %d times, want once", originals)
	}
	if copies != 0 {
		t.Fatalf("marker-less fold wrote %d replay copies; nothing discards the originals, so each is a duplicate", copies)
	}
}

// A fold's markers and its replay tail are two separate durable writes, so a
// crash can land between them. ResumeHistory anchors on the last marker and
// discards everything before it, so whichever of the two is written FIRST is
// the one a crash can lose: with the markers first, a crash after them keeps
// an anchor that has already discarded the originals while the copies that
// were to replace them never arrived, and the turns recorded during the fold
// are gone from every later resume.
//
// Truncating the real fold's transcript at its last marker is that crash. The
// fold's turns must still be there.
func TestCompactionReplay_CrashAtMarkerKeepsTheFoldsTurns(t *testing.T) {
	t.Parallel()
	data, err := readTranscriptFull(compactionReplayTranscript(t))
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	marker := lastCompactionMarkerIndex(data.Entries)
	if marker < 0 {
		t.Fatal("fixture wrote no compaction marker")
	}
	crashed := data.Entries[:marker+1]
	calls, results := 0, 0
	for _, turn := range ResumeHistory(crashed) {
		c, r := replayToolRoundParts(turn.Message)
		calls += c
		results += r
	}
	if calls != 1 || results != 1 {
		t.Fatalf("a crash at the marker resumed %d calls and %d results, want the fold's tool round intact", calls, results)
	}
}

// The other side of the same window: a crash partway through the tail, before
// any marker is durable. There is no anchor, so nothing is discarded and the
// originals are still the record — the copies written so far are duplicates of
// them and must not come back as well.
func TestCompactionReplay_CrashMidTailKeepsOriginalsOnce(t *testing.T) {
	t.Parallel()
	data, err := readTranscriptFull(compactionReplayTranscript(t))
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	firstCopy := -1
	for i, entry := range data.Entries {
		if entry.Turn.ContextReplay {
			firstCopy = i
			break
		}
	}
	if firstCopy < 0 {
		t.Fatal("fixture wrote no replay copies")
	}
	crashed := data.Entries[:firstCopy+1]
	if marker := lastCompactionMarkerIndex(crashed); marker >= 0 {
		t.Fatalf("test setup: a marker at %d is already durable before the tail; this case needs the pre-marker crash", marker)
	}
	calls, results := 0, 0
	for _, turn := range ResumeHistory(crashed) {
		c, r := replayToolRoundParts(turn.Message)
		calls += c
		results += r
	}
	if calls != 1 || results != 1 {
		t.Fatalf("a crash mid-tail resumed %d calls and %d results, want the originals exactly once", calls, results)
	}
}

func lastCompactionMarkerIndex(entries []transcript.Entry) int {
	for i := range slices.Backward(entries) {
		if entries[i].Turn.Kind == schema.TurnCheckpoint || entries[i].Turn.Kind == schema.TurnSummary {
			return i
		}
	}
	return -1
}
