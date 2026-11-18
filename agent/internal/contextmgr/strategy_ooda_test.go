package contextmgr

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/agent/internal/sessionlog"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// newOODATestManager builds the manager every OODA orient test drives: an
// OpenAI profile with the given window, the compaction thresholds the caller
// names (in newCompactionManager's order), and the transcript tools the
// orient text names.
func newOODATestManager(client *llm.Client, contextWindow int, checkpoint, summarize float64) *Manager {
	cm := newCompactionManager(client, contextWindow, checkpoint, summarize)
	cm.Meta.AvailableTranscriptTools = []string{"read_transcript", "find_session_transcripts"}
	return cm
}

func TestOODAStrategy_SatisfiesInterface(t *testing.T) {
	var _ Strategy = (*OODAStrategy)(nil)
}

func TestOODAStrategy_Name(t *testing.T) {
	s := &OODAStrategy{}
	if s.Name() != "ooda" {
		t.Errorf("expected name %q, got %q", "ooda", s.Name())
	}
}

func TestOODAStrategy_Tools_InheritsFromSessionLogStrategy(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log.jsonl")
	sls := &SessionLogStrategy{
		log: mustNewSessionLog(t, logPath),
	}
	ooda := &OODAStrategy{
		SessionLogStrategy: sls,
	}

	if tools := ooda.Tools(); len(tools) != 0 {
		t.Fatalf("expected no tools, got %d", len(tools))
	}
}

func TestOODAStrategy_ManageContext_NoOrientMessageWhenLogEmpty(t *testing.T) {
	client := llm.NewClient()
	// Thresholds high so no compaction occurs.
	cm := newOODATestManager(client, 1000, 0.99, 0.99)
	sls, _ := newTestSessionLogStrategy(t, cm)
	ooda := &OODAStrategy{SessionLogStrategy: sls}

	history := pingPongHistory("hello", "world")

	err := ooda.ManageContext(context.Background(), &history, 0, func(events.EventKind, events.EventData) {})
	if err != nil {
		t.Fatalf("ManageContext returned error: %v", err)
	}

	// Should not add an orient message when log is empty.
	if len(history) != 2 {
		t.Errorf("expected 2 turns (no orient message), got %d", len(history))
	}
}

func TestOODAStrategy_ManageContext_InjectsOrientMessageWhenLogHasEntries(t *testing.T) {
	client := llm.NewClient()
	// Thresholds high so no compaction occurs.
	cm := newOODATestManager(client, 1000, 0.99, 0.99)
	sls, sessionLog := newTestSessionLogStrategy(t, cm)
	ooda := &OODAStrategy{SessionLogStrategy: sls}

	// Pre-populate the session log with entries.
	_ = sessionLog.Append(sessionlog.SessionLogEntry{
		Turn:    1,
		Action:  "shell",
		Summary: "Ran git status to check repo state",
		Outcome: "success",
	})
	_ = sessionLog.Append(sessionlog.SessionLogEntry{
		Turn:         2,
		Action:       "edit_file",
		Summary:      "Modified auth.go to fix login bug",
		Outcome:      "success",
		FilesTouched: []string{"auth.go"},
	})

	history := pingPongHistory("hello", "world")

	var reported int
	var reportedCalled bool
	ctx := WithPostFoldInjectionCallback(context.Background(), func(n int) { reported = n; reportedCalled = true })
	err := ooda.ManageContext(ctx, &history, 0, func(events.EventKind, events.EventData) {})
	if err != nil {
		t.Fatalf("ManageContext returned error: %v", err)
	}

	// Should add an orient message at the end.
	if len(history) != 3 {
		t.Fatalf("expected 3 turns (including orient message), got %d", len(history))
	}
	// No pre-existing orient turn, no compaction (thresholds forced to 0.99):
	// net +1 turn appended purely by the orient injection.
	if !reportedCalled || reported != 1 {
		t.Errorf("expected post-fold injection report of 1, got %d (called=%v)", reported, reportedCalled)
	}

	// Last turn should be the orient message.
	orientTurn := history[len(history)-1]
	if orientTurn.Kind != schema.TurnSteering {
		t.Errorf("expected last turn to be TurnSteering, got %v", orientTurn.Kind)
	}

	orientText := orientTurn.Message.Text()
	if !strings.Contains(orientText, "[SESSION ORIENTATION]") {
		t.Errorf("expected orient message header, got: %s", orientText)
	}
	if !strings.Contains(orientText, "Ran git status") {
		t.Errorf("expected session log entry in orient message, got: %s", orientText)
	}
	if !strings.Contains(orientText, "Modified auth.go") {
		t.Errorf("expected second session log entry in orient message, got: %s", orientText)
	}
	if !strings.Contains(orientText, "[END ORIENTATION]") {
		t.Errorf("expected orient message footer, got: %s", orientText)
	}
}

func TestOODAStrategy_ManageContext_OrientTextUsesTranscriptTools(t *testing.T) {
	client := llm.NewClient()
	cm := newOODATestManager(client, 1000, 0.99, 0.99)
	sls, sessionLog := newTestSessionLogStrategy(t, cm)
	ooda := &OODAStrategy{SessionLogStrategy: sls}

	_ = sessionLog.Append(sessionlog.SessionLogEntry{
		Turn:    1,
		Action:  "shell",
		Summary: "Ran go test",
		Outcome: "success",
	})

	history := pingPongHistory("hello", "world")

	err := ooda.ManageContext(context.Background(), &history, 0, func(events.EventKind, events.EventData) {})
	if err != nil {
		t.Fatalf("ManageContext returned error: %v", err)
	}

	if len(history) < 3 {
		t.Fatalf("expected orient turn to be appended, got %d turns", len(history))
	}
	orientText := history[len(history)-1].Message.Text()
	if !strings.Contains(orientText, "read_transcript") {
		t.Errorf("expected read_transcript in orient text, got: %s", orientText)
	}
	if strings.Contains(orientText, "recall") {
		t.Errorf("expected no 'recall' reference in orient text, got: %s", orientText)
	}
}

func TestOODAStrategy_ManageContext_TruncatesVeryLargeLog(t *testing.T) {
	client := llm.NewClient()
	// Thresholds high so no compaction occurs.
	cm := newOODATestManager(client, 1000, 0.99, 0.99)
	sls, sessionLog := newTestSessionLogStrategy(t, cm)
	ooda := &OODAStrategy{SessionLogStrategy: sls}

	// Create a large log entry that exceeds 80k chars.
	largeSummary := strings.Repeat("x", 85000)
	_ = sessionLog.Append(sessionlog.SessionLogEntry{
		Turn:    1,
		Action:  "shell",
		Summary: largeSummary,
		Outcome: "success",
	})

	history := pingPongHistory("hello", "world")

	err := ooda.ManageContext(context.Background(), &history, 0, func(events.EventKind, events.EventData) {})
	if err != nil {
		t.Fatalf("ManageContext returned error: %v", err)
	}

	// Should add an orient message.
	if len(history) != 3 {
		t.Fatalf("expected 3 turns, got %d", len(history))
	}

	orientText := history[len(history)-1].Message.Text()
	if !strings.Contains(orientText, "session log truncated") {
		t.Errorf("expected truncation notice in orient message, got: %s", orientText[:min(500, len(orientText))])
	}
	if !strings.Contains(orientText, "read_transcript") {
		t.Errorf("expected read_transcript in truncation notice, got: %s", orientText[:min(500, len(orientText))])
	}
	if strings.Contains(orientText, "recall") {
		t.Errorf("expected no 'recall' reference in truncation notice, got: %s", orientText[:min(500, len(orientText))])
	}
}

// TestOODAStrategy_ManageContext_TruncationNeverSplitsRune pins that the 80k
// log cap lands on a rune boundary, so the injected orient message stays valid
// UTF-8. The rendered entry prefix ("Turn 1 [shell] success: ") is 24 bytes, so
// 3-byte runes start on a grid that puts byte 80000 inside a rune.
func TestOODAStrategy_ManageContext_TruncationNeverSplitsRune(t *testing.T) {
	client := llm.NewClient()
	// Thresholds high so no compaction occurs.
	cm := newOODATestManager(client, 1000, 0.99, 0.99)
	sls, sessionLog := newTestSessionLogStrategy(t, cm)
	ooda := &OODAStrategy{SessionLogStrategy: sls}

	_ = sessionLog.Append(sessionlog.SessionLogEntry{
		Turn:    1,
		Action:  "shell",
		Summary: strings.Repeat("€", 27000),
		Outcome: "success",
	})

	history := pingPongHistory("hello", "world")

	err := ooda.ManageContext(context.Background(), &history, 0, func(events.EventKind, events.EventData) {})
	if err != nil {
		t.Fatalf("ManageContext returned error: %v", err)
	}

	orientText := history[len(history)-1].Message.Text()
	if !strings.Contains(orientText, "session log truncated") {
		t.Fatalf("expected the log to be truncated, got: %s", orientText[:min(200, len(orientText))])
	}
	if !utf8.ValidString(orientText) {
		t.Fatalf("orient message is not valid UTF-8: the log cap split a rune")
	}
}

func TestOODAStrategy_ManageContext_AppliesCompactionLayers(t *testing.T) {
	client := llm.NewClient()
	// Low thresholds to trigger compaction.
	cm := newOODATestManager(client, 1000, 0.05, 0.99)
	sls, sessionLog := newTestSessionLogStrategy(t, cm)
	ooda := &OODAStrategy{SessionLogStrategy: sls}

	_ = sessionLog.Append(sessionlog.SessionLogEntry{
		Turn:    1,
		Action:  "shell",
		Summary: "Did something",
		Outcome: "success",
	})

	// Build a history with large content to trigger compaction.
	largeContent := strings.Repeat("x", 30000)

	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("read some files")},
		{Kind: schema.TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
					ID:        "call1",
					Name:      "read_file",
					Arguments: []byte(`{"file_path": "/tmp/test.txt"}`),
				}},
			},
		}},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("call1", "read_file", largeContent, false)},
		{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: "Done reading"},
		}}},
		{Kind: schema.TurnUserInput, Message: llm.User("another task")},
		{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: "working on it"},
		}}},
	}

	var emittedLayers []string
	emitFn := func(kind events.EventKind, data events.EventData) {
		if kind == events.EventContextCompaction {
			if cd, ok := data.(events.ContextCompactionData); ok {
				emittedLayers = append(emittedLayers, cd.Layer)
			}
		}
	}

	err := ooda.ManageContext(context.Background(), &history, 0, emitFn)
	if err != nil {
		t.Fatalf("ManageContext returned error: %v", err)
	}

	// Should have applied compaction (inherited from sessionLogStrategy).
	if len(emittedLayers) == 0 {
		t.Fatal("expected at least one compaction event")
	}
	if emittedLayers[0] != "session_log_checkpoint" {
		t.Errorf("expected first layer to be session_log_checkpoint, got %q", emittedLayers[0])
	}

	// Should also have injected orient message at the end, carrying the
	// fixture's own session-log entry. The banner is production copy; the log
	// summary is data this test supplied, so that is what the tail turn is
	// checked for.
	lastTurn := history[len(history)-1]
	if lastTurn.Kind != schema.TurnSteering {
		t.Errorf("expected last turn to be TurnSteering, got %v", lastTurn.Kind)
	}
	if !strings.Contains(lastTurn.Message.Text(), "Did something") {
		t.Errorf("expected the orient message at the end after compaction to carry the fixture's session-log entry")
	}
}

func TestOODAStrategy_AfterAction_InheritsFromSessionLogStrategy(t *testing.T) {
	entry := sessionlog.SessionLogEntry{
		Action:       "shell",
		Summary:      "Ran go test and all tests passed.",
		Outcome:      "success",
		FilesTouched: []string{"main.go"},
	}
	entryJSON, _ := json.Marshal(entry)

	adapter := &stubSummarizeAdapter{
		name: "openai",
		respFn: func(req llm.Request) (llm.Response, error) {
			return llm.Response{Message: llm.Assistant(string(entryJSON))}, nil
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	profile := testOpenAIProfileWithContextWindow(1000)

	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log.jsonl")

	ooda := &OODAStrategy{
		SessionLogStrategy: &SessionLogStrategy{
			cm:      NewManager(profile, client, cheapmodel.New(client)),
			log:     mustNewSessionLog(t, logPath),
			session: &fakeStrategyHost{profile: profile},
		},
	}

	turns := []schema.Turn{
		{Kind: schema.TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
					ID:        "c1",
					Name:      "shell",
					Arguments: json.RawMessage(`{"command":"go test ./..."}`),
				}},
			},
		}},
		{Kind: schema.TurnToolResults, Message: llm.ToolResultNamed("c1", "shell", "PASS", false)},
	}

	err := ooda.AfterAction(context.Background(), turns, client)
	if err != nil {
		t.Fatalf("AfterAction: %v", err)
	}

	// Verify the session log has the entry (inherited behavior).
	entries := ooda.log.Entries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(entries))
	}
	if entries[0].Action != "shell" {
		t.Errorf("expected action %q, got %q", "shell", entries[0].Action)
	}
}

// TestSummarizeRefusalKeepsFreeLayers pins the split LIFE-01 (#2340 review F6)
// needs: refusing the summarizer must not also refuse the zero-provider-call
// layers. A stable child's allowed unleased turn is entitled to its orientation
// refresh from the session log; only the layer that spends a provider request is
// gated.
//
// The history here is the same shape TestMaybeCompact_SummarizeThreshold uses to
// force the summarize layer, so "no summary" below is caused by the refusal and
// not by a history too short to fold. Both directions are asserted: with the
// refusal the layer must not run and the orientation turn must still be
// appended, and without it the layer must run.
func TestSummarizeRefusalKeepsFreeLayers(t *testing.T) {
	build := func(t *testing.T) (*OODAStrategy, *[]schema.Turn) {
		t.Helper()
		adapter := newFakeAdapter("openai", []func(req llm.Request) llm.Response{
			func(llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("## Progress\nSummary: tests were run and passed")}
			},
		})
		client := llm.NewClient()
		client.Register(adapter)
		profile := testProfile("openai", "test", minSummarizingWindow)
		cm := NewManager(profile, client, cheapmodel.New(client))
		cm.PreserveRecentTurns = 1
		cm.Meta.AvailableTranscriptTools = []string{"read_transcript", "find_session_transcripts"}
		sls, sessionLog := newTestSessionLogStrategy(t, cm)
		_ = sessionLog.Append(sessionlog.SessionLogEntry{Turn: 1, Action: "shell", Summary: "Ran git status to check repo state", Outcome: "success"})
		history := &[]schema.Turn{
			{Kind: schema.TurnUserInput, Message: llm.User(strings.Repeat("task ", 50))},
			{Kind: schema.TurnAssistant, Message: llm.Assistant(strings.Repeat("work ", 30))},
			{Kind: schema.TurnAssistant, Message: llm.Assistant(strings.Repeat("recent content ", 14_000))},
		}
		return &OODAStrategy{SessionLogStrategy: sls}, history
	}
	summarized := func(t *testing.T, history []schema.Turn) bool {
		t.Helper()
		for _, turn := range history {
			if turn.Kind == schema.TurnSummary {
				return true
			}
		}
		return false
	}

	// Without the refusal the provider-spending layer runs: this is the control
	// that makes the refusal case meaningful.
	ooda, history := build(t)
	if err := ooda.ManageContext(context.Background(), history, 0, func(events.EventKind, events.EventData) {}); err != nil {
		t.Fatalf("ManageContext: %v", err)
	}
	if !summarized(t, *history) {
		t.Fatal("the summarizer did not run without a refusal: this fixture cannot discriminate the refusal")
	}

	// With the refusal it must not, and the free layers must still run.
	ooda, history = build(t)
	if err := ooda.ManageContext(WithSummarizeRefusal(context.Background()), history, 0, func(events.EventKind, events.EventData) {}); err != nil {
		t.Fatalf("ManageContext: %v", err)
	}
	if summarized(t, *history) {
		t.Fatal("a refused context still summarized")
	}
	// The free orientation layer is identified by the fixture's OWN session-log
	// entry reaching the tail turn, which is data this test supplied. Asserting
	// the production banner instead would only prove the copy is unchanged.
	last := (*history)[len(*history)-1]
	if last.Kind != schema.TurnSteering {
		t.Fatalf("the refusal also dropped the zero-call orientation layer: rather than only the summarizer, the tail turn is %v", last.Kind)
	}
	if !strings.Contains(last.Message.Text(), "Ran git status to check repo state") {
		t.Fatal("the refusal also dropped the zero-call orientation layer: the tail turn does not carry the fixture's session-log entry")
	}
}
