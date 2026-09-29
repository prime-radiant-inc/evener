package agent

// The daemon wakes a session with notification frames — <delegate-notification>
// for a subagent's report or quiet watchdog, <job-notification> for a
// background job or watch — appended as steering turns. Both clients parse
// those frames out of the steering item's text, and a parser tested against
// hand-built frames pins a shape the daemon may never send: the web's
// delegate parser read status/description/excerpt markup that
// delegateNotificationContent never writes, so every real subagent report
// rendered as an empty card, with a green suite the whole time.
//
// This test is the corpus that closes that gap. Every case builds its frame
// with the real producer (delegateNotificationContent over the packet
// stableDelegateFinishFromRun settles, delegateQuietAttentionContent,
// formatJobNotificationBlock), wraps it in a steering turn the way the
// producers append it, and projects that turn through apptranscript the way
// history reaches the wire. The AppWire package's steeringNotifications tests
// and the phone's transcript row tests read the file this test pins.
//
// Regenerate after an intentional frame change with `make fuzz-goldens`
// (wire_fixture_test.go).

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

// notificationWireFixturePath is the committed corpus both clients read.
const notificationWireFixturePath = "testdata/notificationwire/steering.json"

type notificationWireFixture struct {
	Case string             `json:"case"`
	Note string             `json:"note"`
	Item appwire.ThreadItem `json:"item"`
}

func notificationWireDelegateFrame(t *testing.T, delegateID, name string, inputs delegateTerminalRunInputs) string {
	t.Helper()
	return notificationWireBarePacketFrame(t, delegateID, name, *stableDelegateFinishFromRun(inputs).packet)
}

func notificationWireBarePacketFrame(t *testing.T, delegateID, name string, packet delegatestore.TerminalPacket) string {
	t.Helper()
	content, err := delegateNotificationContent(delegateDeliveryPlan{delegateID: delegateID, name: name, packet: packet})
	if err != nil {
		t.Fatalf("delegate frame %s: %v", delegateID, err)
	}
	return content
}

// notificationWireStopFrame is the frame an owner receives when a parent's
// stop ends the generation: the packet stoppedGenerationFinishEvent puts on
// the RunFinished event, from the run loop's own packet when it left one.
func notificationWireStopFrame(t *testing.T, delegateID, name string, runPacket *delegatestore.TerminalPacket) string {
	t.Helper()
	event, _ := stoppedGenerationFinishEvent(delegateLease{delegateID: delegateID, generation: 1}, runPacket, wireFixtureStart)
	return notificationWireBarePacketFrame(t, delegateID, name, *event.RunFinished.Packet)
}

func notificationWireDescriptor(name string) delegatestore.Descriptor {
	return delegatestore.Descriptor{
		Name:        name,
		Task:        "Find and fix the race between tree settle and the drain.",
		Description: name,
		AgentType:   "general-purpose",
	}
}

// notificationWireAttention is the steering turn an attention message
// appends as (delegateAttentionTurn), with its random stable turn id pinned
// so the corpus stays byte-stable.
func notificationWireAttention(attentionID, text string) schema.Turn {
	turn := delegateAttentionTurn(attentionID, llm.User(text), wireFixtureStart)
	turn.StableTurnID = "q_fixture_" + attentionID
	return turn
}

// notificationWireReminder is the job notification reminder turn a session
// appends when a background job or watch wakes it (kindedSteeringTurn).
func notificationWireReminder(blocks ...string) schema.Turn {
	turn := kindedSteeringTurn(strings.Join(blocks, "\n"), events.SteeringKindNotification, "turn_1")
	turn.Timestamp = wireFixtureStart
	return turn
}

func notificationWireReport(delegateID string) string {
	return delegateAttentionID(delegateDeliveryID(delegateID, 1))
}

func TestSteeringNotificationWireFixtures(t *testing.T) {
	t.Parallel()
	ranFor := func(minutes int) (time.Time, time.Time) {
		return wireFixtureStart, wireFixtureStart.Add(time.Duration(minutes) * time.Minute)
	}
	reportedStart, reportedEnd := ranFor(2)
	failedStart, failedEnd := ranFor(6)
	stoppedStart, stoppedEnd := ranFor(1)
	exhaustedStart, exhaustedEnd := ranFor(14)

	shell := func(jobID, intent string, status jobstore.Status, reason string, exitCode *int) jobNotification {
		return jobNotification{
			JobID:         jobID,
			JobType:       string(jobstore.JobShell),
			Intent:        intent,
			Status:        string(status),
			Reason:        reason,
			ExitCode:      exitCode,
			TranscriptRef: shellTranscriptRef(jobID),
		}
	}
	// Statuses, reasons and exit codes as job_shell.go's terminal status
	// assigns them: exit_zero, exit_nonzero, and -1 with killed_by_signal.
	shellCompleted := shell("job_7", "Run the agent tests", jobstore.StatusCompleted, "exit_zero", new(0))
	shellCompleted.OutputBytes = 41
	shellFailed := shell("job_8", "Run the tree tests under -race", jobstore.StatusCommandExitedNonzero, "exit_nonzero", new(1))
	shellFailed.Description = "Race detector pass"
	shellFailed.OutputBytes = 96
	shellStopped := shell("job_9", "Tail the hub log", jobstore.StatusStopped, "stopped_by_parent", nil)
	shellKilled := shell("job_11", "Serve the docs preview", jobstore.StatusCommandKilled, "killed_by_signal: SIGKILL", new(-1))
	shellCancelled := shell("job_12", "Rebuild the fuzz corpus", jobstore.StatusCancelled, "cancelled", nil)
	shellUnderDelegate := shell("job_10", "Run the settle tests", jobstore.StatusCompleted, "exit_zero", new(0))
	timerFired := jobNotification{
		JobType:         jobNotificationEventWatch,
		Status:          jobNotificationEventWatch,
		WatchID:         "watch_3",
		IntervalSeconds: 300,
		Terminal:        true,
		Note:            "Check whether CI finished.",
	}
	watchSend := watchSendTokenNotification("", jobstore.WatchSendState{
		Key:           jobstore.WatchSendKey{ResolvedWatchedIdentity: "job_7"},
		TriggerReason: "output_match: PASS",
		DeliveryID:    "wsd_1",
	})
	watchSend.watchSendFrame = "CI finished: 3 checks passed."
	completedExcerpt := notificationExcerpt{text: "ok  \tprimeradiant.com/evener/agent\t12.3s", complete: true}
	failedExcerpt := notificationExcerpt{
		text:     "--- FAIL: TestTreeSettle (0.02s)\n    tree_test.go:88: drain ran before settle\nFAIL",
		complete: true,
	}
	frame := func(n jobNotification, excerpt notificationExcerpt) string {
		return formatJobNotificationBlock(n, excerpt, true)
	}

	cases := []struct {
		name string
		note string
		turn schema.Turn
	}{
		{
			name: "delegate-reported",
			note: "A named subagent communicated its report and finished. The body is the TerminalPacket JSON; message is a JSON string.",
			turn: notificationWireAttention(notificationWireReport("dlg_1"), notificationWireDelegateFrame(t, "dlg_1", "Fix race in tree settle", delegateTerminalRunInputs{
				result:       "Done: the settle pass now waits for the drain.\n\nTests: go test ./agent/... passes.",
				communicated: true,
				descriptor:   notificationWireDescriptor("Fix race in tree settle"),
				startedAt:    reportedStart,
				endedAt:      reportedEnd,
			})),
		},
		{
			name: "delegate-failed-unnamed",
			note: "A subagent with no name failed without reporting: kind terminal_error, metadata outcome failed, and no name attribute.",
			turn: notificationWireAttention(notificationWireReport("dlg_2"), notificationWireDelegateFrame(t, "dlg_2", "", delegateTerminalRunInputs{
				runErr:     errors.New("go test exited 1 three times"),
				descriptor: notificationWireDescriptor(""),
				startedAt:  failedStart,
				endedAt:    failedEnd,
			})),
		},
		{
			name: "delegate-stopped",
			note: "A subagent the user stopped: kind terminal_error, metadata outcome cancelled.",
			turn: notificationWireAttention(notificationWireReport("dlg_3"), notificationWireDelegateFrame(t, "dlg_3", "Check drain ordering", delegateTerminalRunInputs{
				runErr:        context.Canceled,
				stoppedByUser: true,
				descriptor:    notificationWireDescriptor("Check drain ordering"),
				startedAt:     stoppedStart,
				endedAt:       stoppedEnd,
			})),
		},
		{
			name: "delegate-stopped-by-parent",
			note: "The packet a parent's stop settles when the run left none: kind terminal_error, metadata outcome stopped, reason stopped_by_parent.",
			turn: notificationWireAttention(notificationWireReport("dlg_4"), notificationWireStopFrame(t, "dlg_4", "Tail the hub log", nil)),
		},
		{
			name: "delegate-stopped-by-parent-mid-run",
			note: "A parent's stop that cancelled a run which left its own packet: the run loop's packet is carried, metadata outcome cancelled.",
			turn: notificationWireAttention(notificationWireReport("dlg_6"), notificationWireStopFrame(t, "dlg_6", "Index the docs", stableDelegateFinishFromRun(delegateTerminalRunInputs{
				runErr:     context.Canceled,
				descriptor: notificationWireDescriptor("Index the docs"),
				startedAt:  stoppedStart,
				endedAt:    stoppedEnd,
			}).packet)),
		},
		{
			name: "delegate-exhausted",
			note: "A subagent that ran out of its turn budget: kind terminal_error, metadata outcome exhausted.",
			turn: notificationWireAttention(notificationWireReport("dlg_5"), notificationWireDelegateFrame(t, "dlg_5", "Sweep the flaky tests", delegateTerminalRunInputs{
				runErr:     &budgetExhaustionError{Budget: exhaustedBudgetTurns, Limit: 40},
				descriptor: notificationWireDescriptor("Sweep the flaky tests"),
				startedAt:  exhaustedStart,
				endedAt:    exhaustedEnd,
			})),
		},
		{
			name: "delegate-quiet",
			note: "The quiet watchdog: a plain-text body with no packet and no name attribute.",
			turn: notificationWireAttention(
				delegateQuietAttentionID(delegateLease{delegateID: "dlg_1", generation: 1}),
				delegateQuietAttentionContent(delegateLease{delegateID: "dlg_1", generation: 1}, wireFixtureStart.Add(time.Minute)),
			),
		},
		{
			name: "job-shell-completed",
			note: "A background shell job that exited 0, with its complete output as the excerpt.",
			turn: notificationWireReminder(frame(shellCompleted, completedExcerpt)),
		},
		{
			name: "job-shell-failed",
			note: "A background shell job whose command exited 1.",
			turn: notificationWireReminder(frame(shellFailed, failedExcerpt)),
		},
		{
			name: "job-shell-killed",
			note: "A background shell job whose command a signal killed.",
			turn: notificationWireReminder(frame(shellKilled, notificationExcerpt{})),
		},
		{
			name: "job-shell-cancelled",
			note: "A background shell job that was cancelled.",
			turn: notificationWireReminder(frame(shellCancelled, notificationExcerpt{})),
		},
		{
			name: "job-shell-attention",
			note: "A stable shell job a subagent started, delivered to its owner through the attention path: no steering kind.",
			turn: notificationWireAttention(stableShellAttentionID("job_10", "gen_1"), frame(shellUnderDelegate, notificationExcerpt{})),
		},
		{
			name: "job-pair",
			note: "Two notifications delivered in one steering turn, joined the way formatJobNotificationReminder joins them.",
			turn: notificationWireReminder(frame(shellStopped, notificationExcerpt{}), frame(timerFired, notificationExcerpt{})),
		},
		{
			name: "job-watch-send",
			note: "A watch's send frame delivered on its trigger.",
			turn: notificationWireReminder(frame(watchSend, notificationExcerpt{})),
		},
	}

	got := make([]notificationWireFixture, 0, len(cases))
	for i, tc := range cases {
		items := apptranscript.ProjectTurn("turn_1", i, tc.turn, nil, nil, nil)
		if len(items) != 1 {
			t.Fatalf("%s: projected %d items, want 1", tc.name, len(items))
		}
		got = append(got, notificationWireFixture{Case: tc.name, Note: tc.note, Item: items[0]})
	}
	checkWireFixture(t, notificationWireFixturePath, got, "the AppWire package and mobile-native tests that read it")
}
