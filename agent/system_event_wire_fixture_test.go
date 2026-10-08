package agent

// A transcript carries more than messages and tool calls: a failed turn's
// diagnostic, the daemon's notices (a tool repair, a compaction, a plugin
// load) and its own steering, each kind with its own words. A client that
// reads hand-built items pins shapes the daemon may never send: the phone read
// a plugin_loaded notice's text, where the daemon writes its summary into the
// description, so every plugin load rendered as a blank row.
//
// This test is the corpus for those rows. Each notice comes from the builder
// the live projector and history share (apptranscript's announcements and
// NoticeItem), each steer from its real producer where one can be called, and
// the failed turn from a user entry and a TurnFailure entry grouped the way a
// reload groups them, so it carries both turn.error and the error
// systemMessage. The phone's transcript row tests read the file this test
// pins.
//
// Regenerate after an intentional change with `make fuzz-goldens`
// (wire_fixture_test.go).

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	taskpkg "primeradiant.com/evener/agent/task"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

// systemEventWireFixturePath is the committed corpus the phone reads.
const systemEventWireFixturePath = "testdata/systemeventwire/events.json"

type systemEventWireCase struct {
	Case string             `json:"case"`
	Note string             `json:"note"`
	Item appwire.ThreadItem `json:"item"`
}

func TestSystemEventWireFixtures(t *testing.T) {
	t.Parallel()

	announced := func(t *testing.T, name string, announcement apptranscript.NoticeAnnouncement) appwire.ThreadItem {
		t.Helper()
		item, ok := apptranscript.SystemMessage(announcement, "item_"+name, "turn_1")
		if !ok {
			t.Fatalf("%s: the announcement has nothing to show", name)
		}
		return item
	}
	projected := func(t *testing.T, name string, turn schema.Turn) appwire.ThreadItem {
		t.Helper()
		turn.Timestamp = wireFixtureStart
		items := apptranscript.ProjectTurn("turn_1", 1, turn, nil, nil, nil)
		if len(items) != 1 {
			t.Fatalf("%s: projected %d items, want 1", name, len(items))
		}
		return items[0]
	}
	noticed := func(t *testing.T, name string, entryIndex int, turn schema.Turn) appwire.ThreadItem {
		t.Helper()
		item, ok := apptranscript.NoticeItem("turn_1", entryIndex, turn)
		if !ok {
			t.Fatalf("%s: the notice has nothing to show", name)
		}
		return item
	}
	steer := func(t *testing.T, name, kind, text string) appwire.ThreadItem {
		t.Helper()
		return projected(t, name, kindedSteeringTurn(text, kind, "turn_1"))
	}

	summary := "# Summary\n\nThe settle pass now waits for the drain.\n\n- Fixed the race in `tree.go`.\n- Next: run the race detector."
	cases := []systemEventWireCase{
		{
			Case: "tool-repair",
			Note: "A NOTICE entry for a tool call the repair engine corrected before it ran (session_tools.go recordNotice).",
			Item: noticed(t, "tool-repair", 1, schema.Turn{Kind: schema.TurnNotice, Notice: &schema.NoticeInfo{
				Kind:       schema.NoticeToolRepair,
				ToolRepair: &schema.ToolRepairNotice{ToolName: "shell", CallID: "call_shell_1", Changes: []string{"drop_unknown:timeout:dropped timeout"}},
			}}),
		},
		{
			Case: "approval-allowed",
			Note: "A NOTICE entry for a human's Allow on a sandbox escalation (session_escalation.go recordNotice): raw.approvalDecision carries the decision, startedAt when it was made.",
			Item: noticed(t, "approval-allowed", 1, schema.Turn{Kind: schema.TurnNotice, Timestamp: wireFixtureStart, Notice: &schema.NoticeInfo{
				Kind:             schema.NoticeApprovalDecision,
				ApprovalDecision: &schema.ApprovalDecisionNotice{EscalationID: "esc_1_a", Approved: true, Tool: "write_file", Kind: "file_tool", DeniedPath: "/Users/j/sites/docs/index.md"},
			}}),
		},
		{
			Case: "approval-denied",
			Note: "A NOTICE entry for a human's Deny on a sandbox escalation.",
			Item: noticed(t, "approval-denied", 2, schema.Turn{Kind: schema.TurnNotice, Timestamp: wireFixtureStart, Notice: &schema.NoticeInfo{
				Kind:             schema.NoticeApprovalDecision,
				ApprovalDecision: &schema.ApprovalDecisionNotice{EscalationID: "esc_2_b", Approved: false, Tool: "read_file", Kind: "file_tool", DeniedPath: "/etc/hosts"},
			}}),
		},
		{
			Case: "compaction-summary",
			Note: "A summary turn: description Context summary, the whole summary as markdown text.",
			Item: projected(t, "compaction-summary", schema.Turn{Kind: schema.TurnSummary, Message: llm.System(summary)}),
		},
		{
			Case: "compaction-checkpoint",
			Note: "A checkpoint turn: description Context checkpoint, the checkpoint as markdown text.",
			Item: projected(t, "compaction-checkpoint", schema.Turn{Kind: schema.TurnCheckpoint, Message: llm.System(summary)}),
		},
		{
			Case: "plugin-loaded",
			Note: "A named plugin load: the summary rides the description, text is empty, and raw.pluginLoaded carries the name and counts.",
			Item: announced(t, "plugin-loaded", apptranscript.PluginLoadedAnnouncement(events.PluginLoadedData{Name: "superpowers", SkillCount: 12, AgentCount: 2, MCPCount: 1})),
		},
		{
			Case: "plugin-loaded-unnamed",
			Note: "A plugin load with no name.",
			Item: announced(t, "plugin-loaded-unnamed", apptranscript.PluginLoadedAnnouncement(events.PluginLoadedData{SkillCount: 3})),
		},
		{
			Case: "context-compaction",
			Note: "A compaction pass with turn and token counts: raw.compaction carries the numbers.",
			Item: announced(t, "context-compaction", apptranscript.ContextCompactionAnnouncement(events.ContextCompactionData{
				Layer: "summary", TurnsBefore: 40, TurnsAfter: 5, EstTokensBefore: 412000, EstTokensAfter: 38000,
			})),
		},
		{
			Case: "context-compaction-turns",
			Note: "A compaction pass that counted turns but no tokens.",
			Item: announced(t, "context-compaction-turns", apptranscript.ContextCompactionAnnouncement(events.ContextCompactionData{
				Layer: "trim", TurnsBefore: 40, TurnsAfter: 5,
			})),
		},
		{
			Case: "context-compaction-bare",
			Note: "A compaction pass with no numbers: no raw.",
			Item: announced(t, "context-compaction-bare", apptranscript.ContextCompactionAnnouncement(events.ContextCompactionData{})),
		},
		{
			Case: "steer-hook-context",
			Note: "Context a hook supplied (wrapHookContext).",
			Item: steer(t, "steer-hook-context", events.SteeringKindHookContext, wrapHookContext("Remember to run make lint before pushing.")),
		},
		{
			Case: "steer-precompact-hook",
			Note: "Context a PreCompact hook supplied before the history was compacted (runPreCompactHook).",
			Item: steer(t, "steer-precompact-hook", events.SteeringKindPrecompactHook, wrapHookContext("Keep the open PR number: #3149.")),
		},
		{
			Case: "steer-compact-nudge",
			Note: "The low-headroom nudge (selfCompactNudge).",
			Item: steer(t, "steer-compact-nudge", events.SteeringKindCompactNudge, selfCompactNudge(true, nil)),
		},
		{
			Case: "steer-no-tool-calls",
			Note: "The retry steer after an empty response (session_tool_round.go's first empty-response text, copied: it is inline there).",
			Item: steer(t, "steer-no-tool-calls", events.SteeringKindNoToolCalls, "Your previous response was empty. Please continue working on the task."),
		},
		{
			Case: "steer-loop-detected",
			Note: "The failure-loop intervention (failureLoopIntervention).",
			Item: steer(t, "steer-loop-detected", events.SteeringKindLoopDetected, failureLoopIntervention([]string{"shell:a", "shell:a", "shell:a"}, 3, "exit status 1")),
		},
		{
			Case: "steer-provider-failure",
			Note: "The steer after a provider failure (composeFailureSteering's content-filter text).",
			Item: steer(t, "steer-provider-failure", events.SteeringKindProviderFailure, contentFilterSteering),
		},
		{
			Case: "steer-transcript-pointer",
			Note: "The pointer to the pre-compaction transcript (session_compaction.go, inline there; abridged).",
			Item: steer(t, "steer-transcript-pointer", events.SteeringKindTranscriptPointer, "<SYSTEM-REMINDER>If you need the exact transcript of this session before compaction, use the transcript tool instead of reading raw transcript files directly.</SYSTEM-REMINDER>"),
		},
		{
			Case: "steer-current-task",
			Note: "The current task (formatCurrentTaskSteering).",
			Item: steer(t, "steer-current-task", events.SteeringKindCurrentTask, formatCurrentTaskSteering(taskpkg.Task{ID: 3, Description: "Fix race"}, true)),
		},
		{
			Case: "steer-task-list",
			Note: "The full task list after a compaction (taskReminderFull's kind; the text is abridged).",
			Item: steer(t, "steer-task-list", events.SteeringKindTaskList, "<SYSTEM-REMINDER>\nTasks:\n- #3 Fix race (in progress)\n</SYSTEM-REMINDER>"),
		},
		{
			Case: "steer-note-handoff",
			Note: "The agent's note to itself, carried across a compaction (renderNoteHandoff).",
			Item: steer(t, "steer-note-handoff", events.SteeringKindNoteHandoff, renderNoteHandoff("Next: run the race detector on tree.go.")),
		},
	}

	checkWireFixture(t, systemEventWireFixturePath, struct {
		Note       string                `json:"note"`
		Items      []systemEventWireCase `json:"items"`
		FailedTurn appwire.Turn          `json:"failed_turn"`
		// The same failed turn after an earlier, unrelated error the live
		// overlay showed during it: that one is not the turn's failure.
		FailedTurnWithOtherError appwire.Turn `json:"failed_turn_with_other_error"`
	}{
		Note:                     "System events and daemon steers, each projected the way history (or the live overlay, for plugin_loaded and context_compaction) reaches the wire, and a failed turn as a reload groups it.",
		Items:                    cases,
		FailedTurn:               systemEventWireFailedTurn(t),
		FailedTurnWithOtherError: systemEventWireFailedTurnWithOtherError(t),
	}, "the mobile-native tests that read it")
}

// systemEventWireFailedTurn is a user message and the TurnFailure entry
// recordTurnFailure writes for it, grouped the way a reload groups them: the
// turn carries turn.error (StampTurnFailure) and the error systemMessage.
func systemEventWireFailedTurn(t *testing.T) appwire.Turn {
	t.Helper()
	data := enrichErrorData(events.ErrorData{Error: "Provider exploded: 529 overloaded"})
	info := schema.TurnFailureInfo{Message: data.Error, Source: data.Source, Title: data.Title, Hint: data.Hint}
	failure := schema.NewTurn(schema.TurnFailure, llm.System(info.Message))
	failure.Error = &info
	failure.Timestamp = wireFixtureStart.Add(time.Second)
	user := schema.NewTurn(schema.TurnUserInput, llm.User("Go on"))
	user.Timestamp = wireFixtureStart
	user.StableTurnID = "turn_2"

	reg := apptranscript.NewToolCallRegistry()
	project := func(turn schema.Turn, turnID string, turnIndex int) []appwire.ThreadItem {
		return apptranscript.ProjectTurn(turnID, turnIndex, turn, reg, nil, nil)
	}
	turns, err := apptranscript.ItemTurnsFromEntries(transcript.Header{}, []transcript.Entry{
		{Kind: "entry", Seq: 1, Turn: user},
		{Kind: "entry", Seq: 2, Turn: failure},
	}, project)
	if err != nil {
		t.Fatalf("project the failed turn: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("the failed turn grouped into %d turns, want 1", len(turns))
	}
	turn := turns[0]
	if turn.Error == nil {
		t.Fatal("the failed turn carries no turn.error")
	}
	errorItems := 0
	for _, item := range turn.Items {
		if item.EventKind == appwire.ThreadItemEventKindError {
			errorItems++
		}
	}
	if errorItems != 1 {
		t.Fatalf("the failed turn carries %d error systemMessages, want 1: %+v", errorItems, turn.Items)
	}
	return turn
}

// systemEventWireFailedTurnWithOtherError is the failed turn with an earlier,
// distinct error in it: an unrecorded session error the live overlay shows as
// an error systemMessage (appoverlay's noticeAnnouncement for ErrorData, which
// is unexported, so its fields are built here the way it builds them).
func systemEventWireFailedTurnWithOtherError(t *testing.T) appwire.Turn {
	t.Helper()
	turn := systemEventWireFailedTurn(t)
	message := "MCP server github disconnected"
	data := enrichErrorData(events.ErrorData{Error: message})
	other, ok := apptranscript.SystemMessage(apptranscript.NoticeAnnouncement{
		EventKind:   appwire.ThreadItemEventKindError,
		Description: data.Title,
		Text:        message,
	}, "notice_error_1", turn.ID)
	if !ok {
		t.Fatal("the earlier error has nothing to show")
	}
	items := append([]appwire.ThreadItem{}, turn.Items[:1]...)
	items = append(items, other)
	turn.Items = append(items, turn.Items[1:]...)
	return turn
}
