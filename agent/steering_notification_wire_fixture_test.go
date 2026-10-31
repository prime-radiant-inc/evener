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
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/doctor"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/task"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

// notificationWireFixturePath is the committed corpus both clients read.
const notificationWireFixturePath = "testdata/notificationwire/steering.json"

// TestNotificationReportProjection records real communicate -> journal ->
// receiver -> AppWire production, not a hand-built controller outcome.
func TestNotificationReportProjection(t *testing.T) {
	t.Parallel()
	var fixtures []notificationWireFixture
	const jsonProse = `{"message":"JSON example, not a captured result","data":{"unproven":"must remain prose"},"artifacts":[]}`
	for _, kind := range []string{"update", "readiness", "waiting", "waiting-empty", "done", "blocked", "interruption", "waiting-empty-json", "waiting-json", "update-json"} {
		t.Run(kind, func(t *testing.T) {
			message := "payload-" + kind + "-9c2f"
			if strings.HasSuffix(kind, "-json") {
				message = jsonProse
			}
			if strings.HasPrefix(kind, "waiting") {
				adapter := newMessagingAdapter("WAIT-WORKER", "WAIT-TARGET")
				returns := newActivationReturns()
				yieldGate := make(chan struct{})
				releaseYield := sync.OnceFunc(func() { close(yieldGate) })
				defer releaseYield()
				adapter.script("root", func(llm.Request) llm.Response { return delegateToolResponse("spawn", "WAIT-WORKER") }, func(llm.Request) llm.Response { return finalResponse("root-finished") })
				gate := make(chan struct{})
				release := sync.OnceFunc(func() { close(gate) })
				defer release()
				adapter.script("WAIT-TARGET", func(llm.Request) llm.Response { <-gate; return finalResponse("target-finished") })
				adapter.script("WAIT-WORKER", func(llm.Request) llm.Response { return delegateToolResponse("target", "WAIT-TARGET") }, func(req llm.Request) llm.Response {
					id := deferredHeadDelegateID.FindString(lastToolResultText(req.Messages))
					output := publicationOutput("result-9c2f")
					if strings.HasPrefix(kind, "waiting-empty") {
						output = map[string]any{"message": "", "data": map[string]any{}, "artifacts": []string{}}
					}
					awaitSignal(t, yieldGate, "waiting projection activation capture")
					return toolCallResponse(publicationCall("projection-"+kind, map[string]any{"message": message, "end_turn": true, "end_reason": "waiting_on_work", "waiting_on": id, "output": output}))
				})
				root := newMessagingRoot(t, adapter, returns.configure)
				t.Cleanup(func() { returns.close(t, root) })
				if _, err := root.ProcessInput(t.Context(), "start", nil); err != nil {
					t.Fatal(err)
				}
				// Release the held descendant before root.Close cleanup.
				t.Cleanup(release)
				adapter.waitForRequests(t, "WAIT-WORKER", 2)
				children := root.subagents.sessions()
				if len(children) != 1 {
					t.Fatalf("waiting children = %d", len(children))
				}
				child := children[0]
				activation := returns.take(t, child.ID())
				releaseYield()
				incidentYielded(t, root, child.ID(), activation)
				if child.State() != SessionIdle || len(rootAttentionFold(t, root).order) != 1 {
					t.Fatalf("waiting projection state = %s, attention = %v", child.State(), rootAttentionFold(t, root).order)
				}
				before, err := root.pendingNotificationIDs()
				if err != nil {
					t.Fatal(err)
				}
				for _, e := range publicationEvents(t, root) {
					if e.RunFinished != nil && e.DelegateID == child.owningDelegateID {
						t.Fatal("wait settled generation")
					}
					if u := e.UpdatePosted; u != nil && e.DelegateID == child.owningDelegateID {
						if u.Generation != 1 || u.Communicate == nil || u.Communicate.CallID != "projection-"+kind || !u.Communicate.EndTurn || u.Communicate.EndReason != "waiting_on_work" || u.Communicate.WaitingOn == "" || u.Packet == nil {
							t.Fatalf("wait publication=%+v", u)
						}
						var capturedMessage string
						if err := json.Unmarshal(u.Packet.Message, &capturedMessage); err != nil || capturedMessage != message {
							t.Fatalf("wait message=%s, want %q: %v", u.Packet.Message, message, err)
						}
						if strings.HasPrefix(kind, "waiting-empty") {
							if len(u.Packet.StructuredResult) != 0 || u.Packet.StructuredResultSource != "" || u.Packet.StructuredResultValid != nil {
								t.Fatalf("empty output captured as a result: %+v", u.Packet)
							}
						} else if u.Packet.StructuredResultSource != delegatestore.StructuredResultSourceDefaultEnvelope || !strings.Contains(string(u.Packet.StructuredResult), "result-9c2f") {
							t.Fatalf("wait provenance=%+v", u.Packet)
						}
						frame := rootAttentionFold(t, root).content[before[0]].Text()
						frame = strings.NewReplacer(root.ID(), "root", child.ID(), "worker", child.owningDelegateID, "dlg_projection", u.Communicate.WaitingOn, "dlg_target").Replace(frame)
						items := apptranscript.ProjectTurn("turn_1", 0, notificationWireAttention("projection-"+kind, frame), nil, nil, nil)
						fixtures = append(fixtures, notificationWireFixture{Case: kind, Generation: u.Generation, Note: "Real delegate target and yielded communicate publication before target settlement, receiver and AppWire projection. Identities normalized.", Item: items[0]})
					}
				}
				return
			}
			nextEntered, nextGate := make(chan struct{}), make(chan struct{})
			root, child, lease, release := publicationRunningChild(t, func(adapter *messagingAdapter) {
				if kind == "done" {
					adapter.script("BATCH-WORKER", func(llm.Request) llm.Response { close(nextEntered); <-nextGate; return finalResponse("next-finished") })
				}
			})
			t.Cleanup(func() { close(nextGate) })
			sub := root.getSub(child.ID())
			sub.mu.Lock()
			generationDone := sub.done
			sub.mu.Unlock()
			writeTaskStoreFixture(t, child.stateDir, child.ID(), []task.Task{
				{ID: 1, Description: "task-a", Status: task.TaskDone},
				{ID: 2, Description: "task-b", Status: task.TaskInProgress},
				{ID: 3, Description: "task-c", Status: task.TaskOpen},
			})
			ctx := context.WithValue(t.Context(), delegateRunLeaseContextKey{}, lease)
			args := map[string]any{"message": message, "end_turn": kind == "done" || kind == "blocked", "output": publicationOutput("result-9c2f")}
			if kind == "readiness" {
				args["readiness"] = "ready"
			}

			if kind == "blocked" {
				args["end_reason"], args["blocker"] = "blocked", "blocker-4d2a"
				path := filepath.Join(child.jobManager.dir, "jobs.jsonl")
				original, err := os.ReadFile(path)
				if err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("invalid inventory journal\n"), 0600); err != nil {
					t.Fatal(err)
				}
				restore := func() {
					if os.IsNotExist(err) {
						if err := os.Remove(path); err != nil {
							t.Error(err)
						}
					} else if err := os.WriteFile(path, original, 0600); err != nil {
						t.Error(err)
					}
				}
				defer restore()
			}
			terminal := kind == "done" || kind == "blocked" || kind == "interruption"
			if kind == "interruption" {
				if _, err := root.StopDelegateRun(lease.delegateID); err != nil {
					t.Fatal(err)
				}
			} else if out := publicationExecute(ctx, child, publicationCall("projection-"+kind, args)); out.IsError {
				t.Fatalf("communicate: %+v", out)
			}
			if terminal {
				release()
				waitForTestSignal(t, generationDone, "captured projection terminal generation")
				// No successor is admitted until the later explicit delegate_send.
				root.sendersWG.Wait()
			}
			// Updates have synchronous publication custody. Terminal kinds have
			// joined the captured generation and its executor's publication tail.
			if ids, err := root.pendingNotificationIDs(); err != nil || len(ids) != 1 {
				t.Fatalf("report receiver custody = %v, error = %v", ids, err)
			}
			var packet *delegatestore.TerminalPacket
			for _, e := range publicationEvents(t, root) {
				if p := e.TerminalPrepared; p != nil {
					if !terminal || p.Generation != lease.generation {
						t.Fatalf("unexpected terminal generation: %+v", p)
					}
					packet = &p.Packet
				}
				if u := e.UpdatePosted; u != nil {
					if terminal || u.Generation != lease.generation || u.Communicate == nil || u.Communicate.CallID != "projection-"+kind {
						t.Fatalf("unexpected update: %+v", u)
					}
					packet = u.Packet
				}
			}
			if packet == nil {
				t.Fatal("no captured producer packet")
			}
			if kind == "update-json" {
				var capturedMessage string
				if err := json.Unmarshal(packet.Message, &capturedMessage); err != nil || capturedMessage != message {
					t.Fatalf("update message=%s, want %q: %v", packet.Message, message, err)
				}
			}
			if kind != "interruption" && (packet.StructuredResultSource != delegatestore.StructuredResultSourceDefaultEnvelope || !strings.Contains(string(packet.StructuredResult), "result-9c2f")) {
				t.Fatalf("provenance=%+v", packet)
			}
			var metadata delegateTerminalPacketMetadata
			if err := json.Unmarshal(packet.Metadata, &metadata); err != nil {
				t.Fatal(err)
			}
			if terminal {
				if metadata.Report == nil || metadata.Report.Tasks.Done != 1 || metadata.Report.Tasks.Remaining != 2 || len(metadata.Report.Tasks.Incomplete) != 2 || metadata.Report.Tasks.Current == nil || metadata.Report.Tasks.Current.ID != 2 {
					t.Fatalf("task summary=%+v", metadata.Report)
				}
				if kind != "interruption" && (packet.StructuredResultValid == nil || !*packet.StructuredResultValid) {
					t.Fatalf("result validity=%+v", packet)
				}
				if kind == "blocked" && (metadata.Blocker != "blocker-4d2a" || !strings.Contains(renderReportNotice(*metadata.Report), "unknown:job-journal")) {
					t.Fatalf("blocked inventory=%+v", metadata)
				}
			}
			// Metadata reads are not model observation or delivery acknowledgement.
			before, err := root.pendingNotificationIDs()
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"job_status", "job_list"} {
				toolArgs := map[string]any{"intent": "projection metadata"}
				if name == "job_status" {
					toolArgs["target"] = lease.delegateID
				}
				raw, _ := json.Marshal(toolArgs)
				out := publicationExecute(t.Context(), root, llm.ToolCallData{ID: "projection-" + name, Name: name, Type: "function", Arguments: raw})
				if out.IsError {
					t.Fatalf("%s: %+v", name, out)
				}
			}
			if _, err := root.JobActivityTree(appwire.JobsListParams{}); err != nil {
				t.Fatal(err)
			}
			if _, err := root.ListActivityDelegates(t.Context(), appwire.SessionActivityListParams{Ref: encodeRef("", root.ID()), Scope: appwire.SessionActivityScopeSubtree}); err != nil {
				t.Fatal(err)
			}
			if _, err := doctor.Jobs(root.stateDir, root.ID(), doctor.JobOpts{}); err != nil {
				t.Fatal(err)
			}
			after, err := root.pendingNotificationIDs()
			if err != nil || !slices.Equal(before, after) {
				t.Fatalf("reads consumed notifications: %v -> %v, %v", before, after, err)
			}
			writeTaskStoreFixture(t, child.stateDir, child.ID(), []task.Task{{ID: 1, Status: task.TaskDone}})
			for _, e := range publicationEvents(t, root) {
				if e.TerminalPrepared != nil && !reflect.DeepEqual(packet, &e.TerminalPrepared.Packet) {
					t.Fatal("accepted report changed after task refresh")
				}
			}
			fold := rootAttentionFold(t, root)
			frame := fold.content[before[0]].Text()
			if metadata.ScratchPath != "" {
				frame = strings.ReplaceAll(frame, metadata.ScratchPath, "/fixture/scratch")
			}
			frame = strings.NewReplacer(root.ID(), "root", child.ID(), "worker", lease.delegateID, "dlg_projection").Replace(frame)
			frame = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z`).ReplaceAllString(frame, wireFixtureStart.Format(time.RFC3339))
			items := apptranscript.ProjectTurn("turn_1", 0, notificationWireAttention("projection-"+kind, frame), nil, nil, nil)
			if len(items) != 1 {
				t.Fatalf("items=%d", len(items))
			}
			fixture := notificationWireFixture{Case: kind, Generation: lease.generation, Note: "Real tool publication or user stop, journal, receiver custody and AppWire projection. Identities, scratch path and clocks normalized.", Item: items[0]}
			if kind == "done" {
				readRow := func() *appwire.SessionDelegate {
					rows, err := root.ListActivityDelegates(t.Context(), appwire.SessionActivityListParams{Ref: encodeRef("", root.ID()), Scope: appwire.SessionActivityScopeSubtree})
					if err != nil || len(rows.Delegates) != 1 {
						t.Fatalf("activity: %+v %v", rows, err)
					}
					raw, _ := json.Marshal(rows.Delegates[0])
					normalized := strings.NewReplacer(root.ID(), "root", child.ID(), "worker", lease.delegateID, "dlg_projection").Replace(string(raw))
					normalized = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z`).ReplaceAllString(normalized, wireFixtureStart.Format(time.RFC3339))
					var row appwire.SessionDelegate
					if err := json.Unmarshal([]byte(normalized), &row); err != nil {
						t.Fatal(err)
					}
					return &row
				}
				fixture.Activity = readRow()
				if fixture.Activity.RunGeneration != 1 || fixture.Activity.ReportPreview != "payload-done-9c2f" {
					t.Fatalf("settled row=%+v", fixture.Activity)
				}
				raw, _ := json.Marshal(map[string]any{"to": lease.delegateID, "message": "resume-3a7c", "max_wait_ms": 0, "intent": "projection resume"})
				if out := publicationExecute(t.Context(), root, llm.ToolCallData{ID: "projection-resume", Name: "delegate_send", Type: "function", Arguments: raw}); out.IsError {
					t.Fatalf("resume: %+v", out)
				}
				awaitSignal(t, nextEntered, "new generation provider")
				fixture.NextActivity = readRow()
				if fixture.NextActivity.RunGeneration != 2 || fixture.NextActivity.Terminal || fixture.NextActivity.ReportPreview != "" || fixture.NextActivity.Outcome != "" {
					t.Fatalf("prior report attached to new generation: %+v", fixture.NextActivity)
				}
			}
			fixtures = append(fixtures, fixture)
		})
	}
	if len(fixtures) != 10 {
		t.Fatalf("captured %d report fixtures, want ten producer cases", len(fixtures))
	}
	checkWireFixture(t, "testdata/notificationwire/reports.json", fixtures, "shared adapter, web and native report projection tests")
}

type notificationWireFixture struct {
	Case         string                   `json:"case"`
	Note         string                   `json:"note"`
	Item         appwire.ThreadItem       `json:"item"`
	Generation   uint64                   `json:"generation,omitempty"`
	Activity     *appwire.SessionDelegate `json:"activity,omitempty"`
	NextActivity *appwire.SessionDelegate `json:"-"`
}

// The shared client corpus uses nextActivity, unlike the snake-case appwire
// fields. Append that fixture-only key last to preserve its existing bytes.
func (f notificationWireFixture) MarshalJSON() ([]byte, error) {
	type fixture notificationWireFixture
	body, err := json.Marshal(fixture(f))
	if err != nil || f.NextActivity == nil {
		return body, err
	}
	next, err := json.Marshal(f.NextActivity)
	if err != nil {
		return nil, err
	}
	body = append(body[:len(body)-1], `,"nextActivity":`...)
	body = append(body, next...)
	return append(body, '}'), nil
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
	// Frozen historical token wrapper, retained only to test old transcript readers.
	watchSend := "<job-notification job_id=\"job_7\" event=\"watch_send\" delivery_id=\"wsd_1\" trigger=\"output_match: PASS\">\nCI finished: 3 checks passed.\n</job-notification>"
	completedExcerpt := notificationExcerpt{text: "ok  \tprimeradiant.com/evener/agent\t12.3s", complete: true}
	failedExcerpt := notificationExcerpt{
		text:     "--- FAIL: TestTreeSettle (0.02s)\n    tree_test.go:88: drain ran before settle\nFAIL",
		complete: true,
	}
	frame := func(n jobNotification, excerpt notificationExcerpt) string {
		return formatJobNotificationBlock(n, excerpt, true)
	}
	const entityText = `& < &lt; &amp; &amp;lt; " > ' &quot; &gt; &#39;`
	entityMessage, err := json.Marshal(entityText)
	if err != nil {
		t.Fatal(err)
	}
	entityWatch := newTestJM(t).watchNotificationFromWatch(&watchConfig{note: entityText}, "", "entity note", nil)

	cases := []struct {
		name string
		note string
		turn schema.Turn
	}{
		{
			name: "job-literal-entities",
			note: "Literal sender entities survive a real job excerpt body round trip.",
			turn: notificationWireReminder(frame(shellCompleted, notificationExcerpt{text: entityText, complete: true})),
		},
		{
			name: "delegate-update-literal-entities",
			note: "Literal sender entities survive a real delegate update body round trip, while attributes retain their full codec.",
			turn: notificationWireAttention("entity_update", notificationWireBarePacketFrame(t, "dlg_entities", entityText, delegatestore.TerminalPacket{Kind: delegatestore.PacketUpdate, Message: entityMessage})),
		},
		{
			name: "watch-note-literal-entities",
			note: "Condition watch notes pass through the central body encoder exactly once.",
			turn: notificationWireReminder(frame(entityWatch, notificationExcerpt{})),
		},
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
			name: "delegate-update",
			note: "A named subagent's mid-work update, communicate(end_turn=false): kind update, and the body is its message with \"<\" escaped.",
			turn: notificationWireAttention(
				delegateAttentionID(delegatestore.UpdateDeliveryID("dlg_1", 7)),
				notificationWireBarePacketFrame(t, "dlg_1", "Fix race in tree settle", delegatestore.TerminalPacket{
					Kind:    delegatestore.PacketUpdate,
					Message: json.RawMessage(`"Which table holds the drain cursor: tree_state or drain_log? I need it before I add the index for seq < head."`),
				}),
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
			turn: notificationWireReminder(watchSend),
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
