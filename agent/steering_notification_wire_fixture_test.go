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
// Regenerate after an intentional frame change with:
//
//	go test ./agent -run TestSteeringNotificationWireFixtures -update-notificationwire

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
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

var updateNotificationWire = flag.Bool("update-notificationwire", false,
	"rewrite agent/testdata/notificationwire/steering.json from the current notification producers")

// notificationWireFixturePath is the committed corpus both clients read.
const notificationWireFixturePath = "testdata/notificationwire/steering.json"

type notificationWireFixture struct {
	Case string             `json:"case"`
	Note string             `json:"note"`
	Item appwire.ThreadItem `json:"item"`
}

var notificationWireStart = time.Date(2026, 9, 28, 20, 0, 0, 0, time.UTC)

func notificationWireDelegateFrame(t *testing.T, delegateID, name string, inputs delegateTerminalRunInputs) string {
	t.Helper()
	finish := stableDelegateFinishFromRun(inputs)
	content, err := delegateNotificationContent(delegateDeliveryPlan{delegateID: delegateID, name: name, packet: *finish.packet})
	if err != nil {
		t.Fatalf("delegate frame %s: %v", delegateID, err)
	}
	return content
}

func notificationWireDescriptor(name string) delegatestore.Descriptor {
	return delegatestore.Descriptor{
		Name:        name,
		Task:        "Find and fix the race between tree settle and the drain.",
		Description: name,
		AgentType:   "general-purpose",
	}
}

func notificationWireExitCode(code int) *int { return &code }

func TestSteeringNotificationWireFixtures(t *testing.T) {
	t.Parallel()
	ranFor := func(minutes int) (time.Time, time.Time) {
		return notificationWireStart, notificationWireStart.Add(time.Duration(minutes) * time.Minute)
	}
	reportedStart, reportedEnd := ranFor(2)
	failedStart, failedEnd := ranFor(6)
	stoppedStart, stoppedEnd := ranFor(1)

	shellCompleted := jobNotification{
		JobID:         "job_7",
		JobType:       string(jobstore.JobShell),
		Intent:        "Run the agent tests",
		Status:        string(jobstore.StatusCompleted),
		OutputBytes:   41,
		ExitCode:      notificationWireExitCode(0),
		TranscriptRef: shellTranscriptRef("job_7"),
	}
	shellFailed := jobNotification{
		JobID:         "job_8",
		JobType:       string(jobstore.JobShell),
		Description:   "Race detector pass",
		Intent:        "Run the tree tests under -race",
		Status:        string(jobstore.StatusCommandExitedNonzero),
		Reason:        "exit_nonzero",
		OutputBytes:   96,
		ExitCode:      notificationWireExitCode(1),
		TranscriptRef: shellTranscriptRef("job_8"),
	}
	shellStopped := jobNotification{
		JobID:         "job_9",
		JobType:       string(jobstore.JobShell),
		Intent:        "Tail the hub log",
		Status:        string(jobstore.StatusStopped),
		Reason:        "stopped_by_parent",
		TranscriptRef: shellTranscriptRef("job_9"),
	}
	timerFired := jobNotification{
		JobType:         jobNotificationEventWatch,
		Status:          jobNotificationEventWatch,
		WatchID:         "watch_3",
		IntervalSeconds: 300,
		Terminal:        true,
		Note:            "Check whether CI finished.",
	}
	completedExcerpt := notificationExcerpt{text: "ok  \tprimeradiant.com/evener/agent\t12.3s", complete: true}
	failedExcerpt := notificationExcerpt{
		text:     "--- FAIL: TestTreeSettle (0.02s)\n    tree_test.go:88: drain ran before settle\nFAIL",
		complete: true,
	}

	cases := []struct {
		name string
		note string
		kind string
		text string
	}{
		{
			name: "delegate-reported",
			note: "A named subagent communicated its report and finished. The body is the TerminalPacket JSON; message is a JSON string.",
			text: notificationWireDelegateFrame(t, "dlg_1", "Fix race in tree settle", delegateTerminalRunInputs{
				result:       "Done: the settle pass now waits for the drain.\n\nTests: go test ./agent/... passes.",
				communicated: true,
				descriptor:   notificationWireDescriptor("Fix race in tree settle"),
				startedAt:    reportedStart,
				endedAt:      reportedEnd,
			}),
		},
		{
			name: "delegate-failed-unnamed",
			note: "A subagent with no name failed without reporting: kind terminal_error, metadata outcome failed, and no name attribute.",
			text: notificationWireDelegateFrame(t, "dlg_2", "", delegateTerminalRunInputs{
				runErr:     errors.New("go test exited 1 three times"),
				descriptor: notificationWireDescriptor(""),
				startedAt:  failedStart,
				endedAt:    failedEnd,
			}),
		},
		{
			name: "delegate-stopped",
			note: "A subagent the user stopped: kind terminal_error, metadata outcome cancelled.",
			text: notificationWireDelegateFrame(t, "dlg_3", "Check drain ordering", delegateTerminalRunInputs{
				runErr:        context.Canceled,
				stoppedByUser: true,
				descriptor:    notificationWireDescriptor("Check drain ordering"),
				startedAt:     stoppedStart,
				endedAt:       stoppedEnd,
			}),
		},
		{
			name: "delegate-quiet",
			note: "The quiet watchdog: a plain-text body with no packet and no name attribute.",
			text: delegateQuietAttentionContent(delegateLease{delegateID: "dlg_1", generation: 1}, notificationWireStart.Add(time.Minute)),
		},
		{
			name: "job-shell-completed",
			note: "A background shell job that exited 0, with its complete output as the excerpt.",
			kind: events.SteeringKindNotification,
			text: formatJobNotificationBlock(shellCompleted, completedExcerpt, true),
		},
		{
			name: "job-shell-failed",
			note: "A background shell job whose command exited 1.",
			kind: events.SteeringKindNotification,
			text: formatJobNotificationBlock(shellFailed, failedExcerpt, true),
		},
		{
			name: "job-pair",
			note: "Two notifications delivered in one steering turn, joined the way formatJobNotificationReminder joins them.",
			kind: events.SteeringKindNotification,
			text: strings.Join([]string{
				formatJobNotificationBlock(shellStopped, notificationExcerpt{}, true),
				formatJobNotificationBlock(timerFired, notificationExcerpt{}, true),
			}, "\n"),
		},
	}

	got := make([]notificationWireFixture, 0, len(cases))
	for i, tc := range cases {
		turn := schema.NewTurn(schema.TurnSteering, llm.User(tc.text))
		turn.Timestamp = notificationWireStart
		turn.SteeringKind = tc.kind
		items := apptranscript.ProjectTurn("turn_1", i, turn, nil, nil, nil)
		if len(items) != 1 {
			t.Fatalf("%s: projected %d items, want 1", tc.name, len(items))
		}
		got = append(got, notificationWireFixture{Case: tc.name, Note: tc.note, Item: items[0]})
	}
	encoded, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("encode corpus: %v", err)
	}
	encoded = append(encoded, '\n')

	if *updateNotificationWire {
		if err := os.MkdirAll(filepath.Dir(notificationWireFixturePath), 0o755); err != nil {
			t.Fatalf("create fixture dir: %v", err)
		}
		if err := os.WriteFile(notificationWireFixturePath, encoded, 0o644); err != nil {
			t.Fatalf("write fixtures: %v", err)
		}
		return
	}

	want, err := os.ReadFile(notificationWireFixturePath)
	if err != nil {
		t.Fatalf("read %s: %v (regenerate with -update-notificationwire)", notificationWireFixturePath, err)
	}
	if !bytes.Equal(want, encoded) {
		t.Fatalf("the notification frames drifted from %s.\n got: %s\nwant: %s\nRegenerate with `go test ./agent -run TestSteeringNotificationWireFixtures -update-notificationwire`, then re-run the AppWire package and mobile-native tests that read it.",
			notificationWireFixturePath, encoded, want)
	}
}
