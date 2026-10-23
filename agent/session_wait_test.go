package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/doctor"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

// The fixture is a persisted execution, not a folded recovery answer. Both
// authorities are closed and reopened before the controller reconciles them.
func TestWaitCheckpointRecovery(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                                              string
		inflight, omitCheckpoint, refused, omitCompletion bool
		change                                            func(*schema.WaitCheckpointInfo)
		after                                             string
		waiting, uncertain                                bool
	}{
		{name: "safe completed batch", waiting: true},
		{name: "completed progress then wait", after: "progress", waiting: true},
		{name: "unfinished progress then wait", after: "progress unfinished"},
		{name: "wait result without paired call", after: "omit call"},
		{name: "wait result wrong call identity", after: "wrong result call"},
		{name: "missing waker interruption", after: "missing waker", waiting: true},
		{name: "shell source replay", after: "shell source", waiting: true},
		{name: "partial source pending tail", after: "partial source pending", waiting: true},
		{name: "partial source identity tail", after: "partial source identity", waiting: true},
		{name: "unreadable waker evidence", after: "unreadable source", waiting: true},
		{name: "explicit Stop retains ownership", after: "stop", waiting: true},
		{name: "interrupted tool without checkpoint", inflight: true, omitCheckpoint: true},
		{name: "completed batch without checkpoint", omitCheckpoint: true},
		{name: "checkpoint without completed execution", omitCompletion: true},
		{name: "checkpoint without completed tool batch", inflight: true},
		{name: "refused communicate", refused: true},
		{name: "duplicate checkpoint", after: "duplicate", waiting: true},
		{name: "resumed checkpoint", after: "resumed"},
		{name: "later interrupted execution", after: "execution"},
		{name: "later same-span tool", after: "tool"},
		{name: "later same-span communicate", after: "communicate"},
		{name: "later failed completion", after: "failed"},
		{name: "later other-span completion", after: "other failed"},
		{name: "later question", after: "question"},
		{name: "later approval", after: "approval"},
		{name: "wrong generation", change: func(c *schema.WaitCheckpointInfo) { c.Generation++ }},
		{name: "wrong delegate", change: func(c *schema.WaitCheckpointInfo) { c.DelegateID = "dlg_other" }},
		{name: "wrong execution", change: func(c *schema.WaitCheckpointInfo) { c.ExecutionTurnID = "execution-other" }},
		{name: "wrong call", change: func(c *schema.WaitCheckpointInfo) { c.CommunicateCallID = "call-other" }},
		{name: "wrong session", change: func(c *schema.WaitCheckpointInfo) { c.SessionID = "session-other" }, uncertain: true},
		{name: "truncated final append", after: "truncated", uncertain: true},
		{name: "unreadable evidence", after: "unreadable", uncertain: true},
		{name: "durability barrier failure", after: "unsynced", uncertain: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, journalPath := newDelegateControllerTestHarness(t, 1, 1)
			childID := identifier.MustNewSessionID()
			created := delegateControllerCreatedEvent("dlg_wait", "")
			created.Created.Descriptor.ChildSessionID = childID
			created.Created.Descriptor.TranscriptRef = "local:" + childID
			c.mu.Lock()
			_, err := c.appendLocked(created, delegateControllerRunStartedEvent("dlg_wait", 1, delegatestore.TriggerInitial, time.Unix(10, 0)))
			c.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			path := transcriptPath(c.stateDir, childID)
			w, err := transcript.NewWriter(path, transcript.Header{SessionID: childID})
			if err != nil {
				t.Fatal(err)
			}
			w.BeginExecution("execution-1", false)
			s := &Session{id: childID, stateDir: c.stateDir, profile: NewOpenAIProfile("gpt-5.2"), restoredMetaIsSubagent: true}
			s.attachTranscript(w)
			// The real wait validator needs a live waker at tool admission. These
			// tests remove it before reopening and separately seed durable sources.
			s.inputQueue = []queuedInput{{ID: "fixture-job"}}
			reg := tool.NewRegistry()
			if err := registerCommunicateTool(reg, newToolDeps(s)); err != nil {
				t.Fatal(err)
			}
			record := func(turn schema.Turn, place transcript.Placement) {
				t.Helper()
				if _, err := w.Record(turn, transcript.RecordOptions{Door: transcript.DoorSynced, Place: place}); err != nil {
					t.Fatal(err)
				}
			}
			var calls []llm.ToolCallData
			var results []tool.ExecResult
			communicate := func(call llm.ToolCallData, endTurn, refused bool) {
				t.Helper()
				if tc.after != "omit call" {
					calls = append(calls, call)
					record(schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}}}, transcript.PlaceSession)
				}
				if tc.inflight {
					return
				}
				if refused {
					// Exercise the real first-terminal-wins refusal, not a guessed
					// state payload or an executor error substituted for refusal.
					s.comm.called = true
				}
				res := reg.ExecuteCall(context.WithValue(context.Background(), ctxToolCallID, call.ID), nil, call)
				var output struct {
					Accepted bool `json:"accepted"`
					EndTurn  bool `json:"end_turn"`
				}
				if res.IsError || json.Unmarshal([]byte(res.Output), &output) != nil || output.Accepted == refused || output.EndTurn != endTurn || res.CallID != call.ID || res.ToolName != call.Name {
					t.Fatalf("registered communicate result = %+v, parsed %+v", res, output)
				}
				if endTurn && !refused {
					if string(res.ToolState) != `{"communicate_end_reason":"waiting_on_work"}` {
						t.Fatalf("actual accepted wait state = %s", res.ToolState)
					}
				} else if len(res.ToolState) != 0 {
					t.Fatalf("progress/refused call acquired accepted end state: %s", res.ToolState)
				}
				if tc.after == "progress unfinished" && !endTurn {
					return // A real completed call whose result never reached disk.
				}
				if tc.after == "wrong result call" {
					res.CallID = "call-other"
				}
				results = append(results, res)
				record(schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: res.CallID, Name: res.ToolName, Content: res.Output, IsError: res.IsError, ToolState: res.ToolState}}}}}, transcript.PlaceSession)
			}
			record(schema.Turn{Kind: schema.TurnUserInput, Message: llm.User("fixture-input")}, transcript.PlaceSession)
			if tc.after == "progress" || tc.after == "progress unfinished" {
				communicate(communicateCallArgs("call-progress", map[string]any{"message": "fixture-progress", "end_turn": false}), false, false)
			}
			communicate(communicateCallArgs("call-1", map[string]any{"message": "fixture-report", "end_turn": true, "end_reason": "waiting_on_work", "waiting_on": "fixture-job"}), true, tc.refused)
			s.inputQueue = nil
			if !tc.inflight {
				if !tc.omitCompletion {
					record(schema.Turn{Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{Status: schema.TurnCompleted}}, transcript.PlaceCompletion)
				}
			}
			checkpoint := schema.WaitCheckpointInfo{SessionID: childID, DelegateID: "dlg_wait", Generation: 1, ExecutionTurnID: "execution-1", CommunicateCallID: "call-1", WaitingOn: "fixture-job"}
			if tc.change != nil {
				tc.change(&checkpoint)
			}
			if !tc.omitCheckpoint {
				if tc.name == "wrong session" {
					record(schema.Turn{Kind: schema.TurnWaitCheckpoint, WaitCheckpoint: &checkpoint}, transcript.PlaceInTurn("execution-1"))
				} else if err := s.persistWaitCheckpoint(checkpoint); err != nil {
					t.Fatal(err)
				}
			}
			switch tc.after {
			case "duplicate":
				record(schema.Turn{Kind: schema.TurnWaitCheckpoint, WaitCheckpoint: &checkpoint}, transcript.PlaceInTurn("execution-1"))
			case "resumed":
				checkpoint.ResumedByTurnID = "execution-2"
				record(schema.Turn{Kind: schema.TurnWaitCheckpoint, WaitCheckpoint: &checkpoint}, transcript.PlaceInTurn("execution-2"))
			case "execution":
				w.BeginExecution("execution-2", false)
				record(schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant("fixture-next")}, transcript.PlaceSession)
			case "tool":
				record(schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "late-call", Name: "read_file"}}}}}, transcript.PlaceInTurn("execution-1"))
			case "communicate":
				record(schema.Turn{Kind: schema.TurnCommunicate, Communicate: &schema.CommunicateInfo{CallID: "late-call", Message: "late progress"}}, transcript.PlaceInTurn("execution-1"))
			case "failed":
				record(schema.Turn{Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{Status: schema.TurnFailed}}, transcript.PlaceInTurn("execution-1"))
			case "other failed":
				record(schema.Turn{Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{Status: schema.TurnFailed}}, transcript.PlaceInTurn("execution-other"))
			case "question":
				record(schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "ask-1", Name: "ask_user", ToolState: json.RawMessage(`{"status":"pending"}`)}}}}}, transcript.PlaceDelivery)
			case "approval":
				record(schema.Turn{Kind: schema.TurnNotice, Notice: &schema.NoticeInfo{Kind: schema.NoticeApprovalDecision, ApprovalDecision: &schema.ApprovalDecisionNotice{EscalationID: "approval-1"}}}, transcript.PlaceDelivery)
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			// Verify the persisted evidence independently of the recovery predicate.
			data, err := readTranscriptFull(path, "")
			if err != nil {
				t.Fatal(err)
			}
			var diskCalls []llm.ToolCallData
			var diskResults []llm.ToolResultData
			var messages []schema.CommunicateInfo
			for _, entry := range data.Entries {
				if info := entry.Turn.Communicate; info != nil && info.CallID != "late-call" {
					messages = append(messages, *info)
				}
				for _, part := range entry.Turn.Message.Content {
					if part.ToolCall != nil && part.ToolCall.Name == "communicate" {
						diskCalls = append(diskCalls, *part.ToolCall)
					}
					if part.ToolResult != nil && part.ToolResult.Name == "communicate" {
						diskResults = append(diskResults, *part.ToolResult)
					}
				}
			}
			if !reflect.DeepEqual(diskCalls, calls) || len(diskResults) != len(results) {
				t.Fatalf("persisted call/result pairs differ: calls=%+v results=%+v", diskCalls, diskResults)
			}
			for i, result := range diskResults {
				want := results[i]
				if result.ToolCallID != want.CallID || result.Content != want.Output || result.IsError != want.IsError || !bytes.Equal(result.ToolState, want.ToolState) {
					t.Fatalf("persisted result %d differs from real registry result: %+v, %+v", i, result, want)
				}
			}
			wantMessages := 1
			if tc.inflight || tc.refused {
				wantMessages = 0
			} else if tc.after == "progress" || tc.after == "progress unfinished" {
				wantMessages = 2
			}
			if len(messages) != wantMessages || wantMessages > 0 && (messages[len(messages)-1].CallID != "call-1" || !messages[len(messages)-1].EndTurn || messages[len(messages)-1].WaitingOn != "fixture-job") {
				t.Fatalf("accepted/refused messages = %+v, want %d", messages, wantMessages)
			}
			if wantMessages == 2 && (messages[0].CallID != "call-progress" || messages[0].EndTurn || messages[0].Message != "fixture-progress") {
				t.Fatalf("earlier progress was not delivered: %+v", messages)
			}
			switch tc.after {
			case "truncated":
				if err := os.WriteFile(path, append(bytes.Clone(original), []byte(`{"kind":"entry","turn":`)...), 0o600); err != nil {
					t.Fatal(err)
				}
			case "unreadable":
				if err := os.Rename(path, path+".saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := c.store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err := delegatestore.Open(journalPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			reopened, err := openDelegateTreeController(delegateTreeControllerConfig{store: store, rootSessionID: c.rootSessionID, stateDir: c.stateDir})
			if err != nil {
				t.Fatal(err)
			}
			var faultFS *waitCheckpointFaultFS
			if tc.after == "unsynced" {
				faultFS = &waitCheckpointFaultFS{Fs: afero.NewOsFs(), fail: true}
				reopened.attentionOpen = func(path, id string) (*transcript.Writer, []transcript.Entry, error) {
					return transcript.OpenWriterForSessionWithFS(faultFS, path, id)
				}
			}
			// Select while the journal still says running, before reconciliation can
			// turn an interrupted generation into an ordinary idle receiver.
			if err := validateWaitCheckpointsForBootstrap(reopened); (err != nil) != tc.uncertain {
				t.Fatalf("bootstrap reader preflight = %v, want uncertainty %v", err, tc.uncertain)
			}
			reopened.mu.Lock()
			receiver, selectionErr := reopened.deliveryReceiverWithEvidenceLocked("dlg_wait")
			reopened.mu.Unlock()
			_, cold := receiver.(coldDelegateDeliveryReceiver)
			if cold != tc.waiting || (selectionErr != nil) != tc.uncertain {
				t.Fatalf("running cold eligibility = %v, %v, want %v, uncertainty %v", cold, selectionErr, tc.waiting, tc.uncertain)
			}
			reopened.mu.Lock()
			_, err = reopened.reconcileRuntimeLostFromEvidenceLocked()
			reopened.mu.Unlock()
			if (err != nil) != tc.uncertain {
				t.Fatalf("recovery error = %v, want uncertainty %v", err, tc.uncertain)
			}
			if tc.uncertain {
				if run := reopened.durable["dlg_wait"]; !run.CurrentRunOpen || run.LatestOutcome != nil {
					t.Fatalf("uncertain evidence fabricated an outcome: %+v", run)
				}
				switch tc.after {
				case "truncated":
					got, readErr := os.ReadFile(path)
					if readErr != nil || !bytes.Equal(got, append(bytes.Clone(original), []byte(`{"kind":"entry","turn":`)...)) {
						t.Fatalf("uncertain read modified transcript: %v", readErr)
					}
					if err := os.WriteFile(path, original, 0o600); err != nil {
						t.Fatal(err)
					}
				case "unreadable":
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(path+".saved", path); err != nil {
						t.Fatal(err)
					}
				case "unsynced":
					faultFS.fail = false
				default:
					return
				}
				reopened.mu.Lock()
				_, err = reopened.reconcileRuntimeLostFromEvidenceLocked()
				reopened.mu.Unlock()
				if err != nil {
					t.Fatalf("recovery did not retry after filesystem healed: %v", err)
				}
				tc.waiting = true
			}
			run := reopened.durable["dlg_wait"]
			if tc.waiting {
				if !run.CurrentRunOpen || run.Generation != 1 || run.Phase != delegatestore.PhaseRunning || run.LatestOutcome != nil || run.PreparedTerminal != nil {
					t.Fatalf("safe wait was settled as runtime loss: %+v", run)
				}
			} else if run.CurrentRunOpen || run.LatestOutcome == nil || run.LatestOutcome.Reason != "runtime_lost" {
				t.Fatalf("interrupted execution did not retain runtime_lost semantics: %+v", run)
			}
			if tc.name == "safe completed batch" {
				data, err := readTranscriptFull(path, "")
				if err != nil {
					t.Fatal(err)
				}
				if len(data.Entries) != 6 || data.Entries[5].Turn.Kind != schema.TurnWaitCheckpoint || data.Entries[5].Turn.WaitCheckpoint.CommunicateCallID != "call-1" {
					t.Fatalf("raw reader lost private checkpoint: %+v", data.Entries)
				}
				if open := transcript.ExecutionTurns(data.Entries); len(open) != 1 || open["execution-1"] {
					t.Fatalf("checkpoint reopened or added an execution span: %v", open)
				}
				history := ResumeHistory(data.Entries)
				if len(history) != 3 || history[0].Kind != schema.TurnUserInput || history[1].Kind != schema.TurnAssistant || history[2].Kind != schema.TurnToolResults || len(turnsToMessages(history)) != 3 {
					t.Fatalf("checkpoint entered resumed/model history: %+v", history)
				}
				for _, turn := range history {
					if turn.WaitCheckpoint != nil {
						t.Fatal("private checkpoint entered model content")
					}
				}
				forensic, err := doctor.Transcript(c.stateDir, childID, doctor.TranscriptOpts{})
				if err != nil || forensic.TurnsTotal != 3 || len(forensic.Turns) != 6 || forensic.Turns[5].Kind != "WAIT_CHECKPOINT" || forensic.Turns[5].Turn != nil {
					t.Fatalf("doctor checkpoint classification = %+v, %v", forensic, err)
				}
				// A resident executor must never lend its writer to this cold path.
				reopened.mu.Lock()
				live := reopened.live["dlg_wait"]
				reopened.live["dlg_wait"] = &delegateLiveState{binding: &delegateRuntimeBinding{lease: delegateLease{delegateID: "dlg_wait", generation: 1}}}
				_, err = reopened.deliveryReceiverWithEvidenceLocked("dlg_wait")
				reopened.live["dlg_wait"] = live
				reopened.mu.Unlock()
				if !errors.Is(err, errDelegateTargetBusy) {
					t.Fatalf("active executor admitted cold writer: %v", err)
				}
				reopened.mu.Lock()
				reopened.holdAttentionRestoreLocked("dlg_wait")
				_, err = reopened.deliveryReceiverWithEvidenceLocked("dlg_wait")
				reopened.attentionRestoreHolds["dlg_wait"]--
				reopened.mu.Unlock()
				if !errors.Is(err, errDelegateTargetBusy) {
					t.Fatalf("active restore admitted cold writer: %v", err)
				}
			}
			if tc.after == "stop" {
				if _, _, _, err := reopened.StopSubtree(rootDelegateActor(reopened.rootSessionID), "dlg_wait"); err != nil {
					t.Fatal(err)
				}
				if err := reopened.enqueueNotification(childID, notification{ID: "held-notification", Message: llm.User("must not restart stopped wait")}); err == nil {
					t.Fatal("explicit Stop admitted waiting cold receiver")
				}
				run = reopened.durable["dlg_wait"]
				if run.PendingStopSeq == 0 || run.Phase != delegatestore.PhaseStopping || run.Generation != 1 || run.LatestOutcome != nil {
					t.Fatalf("Stop lost its generation ownership: %+v", run)
				}
			}
			if tc.after == "unreadable source" {
				sourcePath := filepath.Join(jobsDir(c.stateDir, childID), "jobs.jsonl")
				if err := os.MkdirAll(sourcePath, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := repairWaitCheckpointWakersForBootstrap(reopened); err == nil {
					t.Fatal("unreadable source was treated as absent")
				}
				fold, err := readExistingDelegateAttentionFold(path, childID)
				if err != nil || len(fold.pendingIDs()) != 0 || !run.CurrentRunOpen {
					t.Fatalf("unreadable source fabricated interruption or settlement: %+v, %v", fold, err)
				}
				if err := os.Remove(sourcePath); err != nil {
					t.Fatal(err)
				}
				if err := repairWaitCheckpointWakersForBootstrap(reopened); err != nil {
					t.Fatalf("source evidence did not retry: %v", err)
				}
			}
			if tc.after == "shell source" || tc.after == "partial source pending" || tc.after == "partial source identity" {
				sourcePath := filepath.Join(jobsDir(c.stateDir, childID), "jobs.jsonl")
				now := time.Unix(20, 0)
				appendDelegateShellEventsAt(t, sourcePath,
					jobstore.Event{Kind: jobstore.EventJobStarted, TS: now, JobID: "fixture-job", Type: jobstore.JobShell, OwnerSessionID: childID, ParentDelegateID: "dlg_wait", StartedAt: &now},
					jobstore.Event{Kind: jobstore.EventJobFinished, TS: now, JobID: "fixture-job", Status: jobstore.StatusCompleted, EndedAt: &now, TerminalGen: "terminal-1"},
					jobstore.Event{Kind: jobstore.EventJobNotificationPending, TS: now, JobID: "fixture-job", TerminalGen: "terminal-1"},
				)
				if tc.after != "shell source" {
					complete, err := os.ReadFile(sourcePath)
					if err != nil {
						t.Fatal(err)
					}
					cut := bytes.LastIndexByte(bytes.TrimSuffix(complete, []byte{'\n'}), '\n') + 1
					if tc.after == "partial source identity" {
						cut = 0
					}
					partial := complete[:cut+len(`{"kind":`)]
					if err := os.WriteFile(sourcePath, partial, 0o600); err != nil {
						t.Fatal(err)
					}
					beforeJournal, err := os.ReadFile(journalPath)
					if err != nil {
						t.Fatal(err)
					}
					for range 2 {
						if _, err := reconcileDelegateResourcesForBootstrap(reopened); err == nil {
							t.Error("partial source fabricated absence instead of retryable uncertainty")
						}
						fold, err := readExistingDelegateAttentionFold(path, childID)
						if err != nil || len(fold.pendingIDs()) != 0 || len(fold.resolutions) != 0 {
							t.Errorf("partial source pending IDs=%v resolutions=%v, %v", fold.pendingIDs(), fold.resolutions, err)
						}
						run := reopened.durable["dlg_wait"]
						if !run.CurrentRunOpen || run.Generation != 1 || run.Phase != delegatestore.PhaseRunning || run.LatestOutcome != nil || run.PreparedTerminal != nil {
							t.Errorf("partial source changed open generation: %+v", run)
						}
						for checkPath, want := range map[string][]byte{sourcePath: partial, path: original, journalPath: beforeJournal} {
							if got, err := os.ReadFile(checkPath); err != nil || !bytes.Equal(got, want) {
								t.Errorf("uncertain recovery modified %s: %v", checkPath, err)
							}
						}
					}
					if t.Failed() {
						return
					}
					if err := os.WriteFile(sourcePath, complete, 0o600); err != nil {
						t.Fatal(err)
					}
					if _, err := reconcileDelegateResourcesForBootstrap(reopened); err != nil {
						t.Fatalf("repaired source did not retry via bootstrap reconciliation: %v", err)
					}
				}
				if err := repairWaitCheckpointWakersForBootstrap(reopened); err != nil {
					t.Fatal(err)
				}
				journal, err := jobstore.ReadEvents(sourcePath)
				if err != nil || jobstore.Fold(journal)["fixture-job"].NotifyState != jobstore.NotifyPending {
					t.Fatalf("waker validation settled source without custody: %v", err)
				}
				for range 2 {
					if err := repairStableShellAttentionForBootstrap(reopened); err != nil {
						t.Fatal(err)
					}
					if err := repairWaitCheckpointWakersForBootstrap(reopened); err != nil {
						t.Fatal(err)
					}
				}
				fold, err := readExistingDelegateAttentionFold(path, childID)
				run = reopened.durable["dlg_wait"]
				if err != nil || len(fold.pendingIDs()) != 1 || fold.pendingIDs()[0] != "shell:fixture-job:terminal-1" || len(fold.resolutions) != 0 || !run.CurrentRunOpen || run.Generation != 1 || run.Phase != delegatestore.PhaseRunning || run.LatestOutcome != nil || run.PreparedTerminal != nil {
					t.Fatalf("shell replay did not retain exact wait custody/generation: %+v, %v", fold, err)
				}
				custody := 0
				for _, entry := range notificationRawEntries(t, path) {
					if entry.Turn.AttentionID == "shell:fixture-job:terminal-1" {
						custody++
					}
				}
				if custody != 1 {
					t.Fatalf("raw repaired source custody records = %d, want exactly one", custody)
				}
			}
			if tc.after == "missing waker" {
				for range 2 {
					if _, err := reconcileDelegateResourcesForBootstrap(reopened); err != nil {
						t.Fatal(err)
					}
				}
				fold, err := readExistingDelegateAttentionFold(path, childID)
				if err != nil {
					t.Fatal(err)
				}
				if ids := fold.pendingIDs(); len(ids) != 1 || ids[0] != "wait-interrupted:"+childID+":execution-1:call-1" {
					t.Fatalf("missing waker recovery IDs = %v", ids)
				}
				count := 0
				for _, entry := range notificationRawEntries(t, path) {
					if entry.Turn.AttentionID == "wait-interrupted:"+childID+":execution-1:call-1" {
						count++
					}
				}
				if count != 1 || len(fold.resolutions) != 0 {
					t.Fatalf("raw interruption records=%d resolutions=%v", count, fold.resolutions)
				}
				if !run.CurrentRunOpen || run.LatestOutcome != nil {
					t.Fatal("missing waker interruption settled the waiting generation")
				}
			}
		})
	}
}

func TestWaitCheckpointSourceCompleteness(t *testing.T) {
	for _, tail := range []string{"pending notification", "producer identity"} {
		t.Run(tail, func(t *testing.T) {
			c, _ := newDelegateControllerTestHarness(t, 1, 1)
			childID := identifier.MustNewSessionID()
			created := delegateControllerCreatedEvent("dlg_wait", "")
			created.Created.Descriptor.ChildSessionID = childID
			c.mu.Lock()
			_, err := c.appendLocked(created, delegateControllerRunStartedEvent("dlg_wait", 1, delegatestore.TriggerInitial, time.Unix(10, 0)))
			c.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(jobsDir(c.stateDir, childID), "jobs.jsonl")
			now := time.Unix(20, 0)
			appendDelegateShellEventsAt(t, path,
				jobstore.Event{Kind: jobstore.EventJobStarted, TS: now, JobID: "fixture-job", Type: jobstore.JobShell, OwnerSessionID: childID, ParentDelegateID: "dlg_wait", StartedAt: &now},
				jobstore.Event{Kind: jobstore.EventJobFinished, TS: now, JobID: "fixture-job", Status: jobstore.StatusCompleted, EndedAt: &now, TerminalGen: "terminal-1"},
				jobstore.Event{Kind: jobstore.EventJobNotificationPending, TS: now, JobID: "fixture-job", TerminalGen: "terminal-1"},
			)
			complete, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			cut := bytes.LastIndexByte(bytes.TrimSuffix(complete, []byte{'\n'}), '\n') + 1
			if tail == "producer identity" {
				cut = 0
			}
			partial := complete[:cut+len(`{"kind":`)]
			if err := os.WriteFile(path, partial, 0o600); err != nil {
				t.Fatal(err)
			}
			checkpoint := schema.WaitCheckpointInfo{SessionID: childID, DelegateID: "dlg_wait", WaitingOn: "fixture-job"}
			if available, err := waitCheckpointSourceAvailable(c, checkpoint); available || err == nil {
				t.Fatalf("partial %s source = %v, %v, want retryable uncertainty", tail, available, err)
			}
			if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, partial) {
				t.Fatalf("source read changed partial bytes: %v", err)
			}
			if err := os.WriteFile(path, complete, 0o600); err != nil {
				t.Fatal(err)
			}
			if available, err := waitCheckpointSourceAvailable(c, checkpoint); !available || err != nil {
				t.Fatalf("repaired source = %v, %v", available, err)
			}
			// A complete JSON record needs no newline to prove honest absence.
			if err := os.WriteFile(path, bytes.TrimSuffix(complete, []byte{'\n'}), 0o600); err != nil {
				t.Fatal(err)
			}
			checkpoint.WaitingOn = "absent-job"
			if available, err := waitCheckpointSourceAvailable(c, checkpoint); available || err != nil {
				t.Fatalf("complete source without trailing newline = %v, %v, want honest absence", available, err)
			}
		})
	}
}

func TestWaitCheckpointProtocol(t *testing.T) {
	t.Parallel()
	checkpoint := schema.WaitCheckpointInfo{
		SessionID: "session-1", DelegateID: "delegate-1", Generation: 7,
		ExecutionTurnID: "execution-1", CommunicateCallID: "call-1", WaitingOn: "job-1",
	}
	resumed := checkpoint
	resumed.ResumedByTurnID = "execution-2"

	newFixture := func(t *testing.T) (*Session, string, *transcript.Writer) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "session.jsonl")
		writer, err := transcript.NewWriter(path, transcript.Header{SessionID: checkpoint.SessionID})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = writer.Close() })
		s := &Session{id: checkpoint.SessionID}
		s.attachTranscript(writer)
		return s, path, writer
	}
	assertRead := func(t *testing.T, path string, want *schema.WaitCheckpointInfo) {
		t.Helper()
		got, err := readWaitCheckpoint(path, checkpoint.SessionID)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("read checkpoint = %+v, %v, want %+v", got, err, want)
		}
	}
	appendRaw := func(t *testing.T, writer *transcript.Writer, turnID string, value *schema.WaitCheckpointInfo) {
		t.Helper()
		_, err := writer.Record(schema.Turn{Kind: schema.TurnWaitCheckpoint, WaitCheckpoint: value}, transcript.RecordOptions{
			Door: transcript.DoorSynced, Place: transcript.PlaceInTurn(turnID),
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	t.Run("root may omit delegate generation and named target", func(t *testing.T) {
		s, path, writer := newFixture(t)
		root := checkpoint
		root.DelegateID, root.Generation, root.WaitingOn = "", 0, ""
		if err := s.persistWaitCheckpoint(root); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		assertRead(t, path, &root)
	})

	t.Run("yield and resumed pair survive close and reopen", func(t *testing.T) {
		s, path, writer := newFixture(t)
		if err := s.persistWaitCheckpoint(checkpoint); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		assertRead(t, path, &checkpoint)
		writer, _, err := transcript.OpenWriterForSession(path, checkpoint.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		s.attachTranscript(writer)
		t.Cleanup(func() { _ = writer.Close() })
		if err := s.persistWaitCheckpoint(resumed); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		assertRead(t, path, &resumed)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
		if len(lines) != 3 {
			t.Fatalf("raw transcript lines = %d, want header and two records", len(lines))
		}
		for i, wantTurn := range []string{"execution-1", "execution-2"} {
			var raw struct {
				Kind string `json:"kind"`
				Seq  int    `json:"seq"`
				Turn struct {
					Kind       string         `json:"kind"`
					TurnID     string         `json:"turn_id"`
					Checkpoint map[string]any `json:"wait_checkpoint"`
				} `json:"turn"`
			}
			if err := json.Unmarshal(lines[i+1], &raw); err != nil {
				t.Fatal(err)
			}
			if raw.Kind != "entry" || raw.Seq != i || raw.Turn.Kind != "WAIT_CHECKPOINT" || raw.Turn.TurnID != wantTurn {
				t.Fatalf("raw record %d = %+v", i, raw)
			}
			want := map[string]any{
				"session_id": "session-1", "delegate_id": "delegate-1", "generation": float64(7),
				"execution_turn_id": "execution-1", "communicate_call_id": "call-1", "waiting_on": "job-1",
			}
			if i == 1 {
				want["resumed_by_turn_id"] = "execution-2"
			}
			if !reflect.DeepEqual(raw.Turn.Checkpoint, want) {
				t.Fatalf("raw checkpoint %d = %+v, want %+v", i, raw.Turn.Checkpoint, want)
			}
		}
		if len(s.history) != 0 {
			t.Fatal("checkpoint entered model history")
		}
	})

	t.Run("duplicate identity is idempotent and cannot reopen a claim", func(t *testing.T) {
		s, path, _ := newFixture(t)
		for _, value := range []schema.WaitCheckpointInfo{checkpoint, checkpoint, resumed, resumed, checkpoint} {
			if err := s.persistWaitCheckpoint(value); err != nil {
				t.Fatal(err)
			}
		}
		assertRead(t, path, &resumed)
	})

	t.Run("conflicting retries cannot change a yield or claim", func(t *testing.T) {
		for _, claimed := range []bool{false, true} {
			s, path, writer := newFixture(t)
			if err := s.persistWaitCheckpoint(checkpoint); err != nil {
				t.Fatal(err)
			}
			wrong := checkpoint
			wrong.WaitingOn = "job-other"
			turnID := "execution-1"
			if claimed {
				if err := s.persistWaitCheckpoint(resumed); err != nil {
					t.Fatal(err)
				}
				wrong = resumed
				wrong.ResumedByTurnID = "execution-3"
				turnID = "execution-3"
			}
			appendRaw(t, writer, turnID, &wrong)
			if got, err := readWaitCheckpoint(path, checkpoint.SessionID); err == nil || got != nil {
				t.Fatalf("read accepted contradictory retry: %+v, %v", got, err)
			}
		}
	})

	t.Run("latest activation", func(t *testing.T) {
		s, path, _ := newFixture(t)
		next := checkpoint
		next.ExecutionTurnID, next.CommunicateCallID = "execution-2", "call-2"
		for _, value := range []schema.WaitCheckpointInfo{checkpoint, resumed, next, checkpoint} {
			if err := s.persistWaitCheckpoint(value); err != nil {
				t.Fatal(err)
			}
		}
		assertRead(t, path, &next)
	})

	t.Run("wrong session", func(t *testing.T) {
		s, path, writer := newFixture(t)
		wrong := checkpoint
		wrong.SessionID = "session-other"
		if err := s.persistWaitCheckpoint(wrong); err == nil {
			t.Fatal("persist accepted another session")
		}
		appendRaw(t, writer, checkpoint.ExecutionTurnID, &wrong)
		if got, err := readWaitCheckpoint(path, checkpoint.SessionID); err == nil || got != nil {
			t.Fatalf("read accepted wrong payload session: %+v, %v", got, err)
		}
		_, err := readWaitCheckpoint(path, "session-other")
		var mismatch *transcript.SessionIDMismatchError
		if !errors.As(err, &mismatch) {
			t.Fatalf("header mismatch error = %v", err)
		}
	})

	for _, test := range []struct {
		name   string
		change func(*schema.WaitCheckpointInfo)
	}{
		{"communicate identity", func(c *schema.WaitCheckpointInfo) { c.CommunicateCallID = "call-other" }},
		{"execution identity", func(c *schema.WaitCheckpointInfo) { c.ExecutionTurnID = "execution-other" }},
		{"delegate identity", func(c *schema.WaitCheckpointInfo) { c.DelegateID = "delegate-other" }},
		{"generation identity", func(c *schema.WaitCheckpointInfo) { c.Generation++ }},
		{"waiting target", func(c *schema.WaitCheckpointInfo) { c.WaitingOn = "job-other" }},
		{"same execution resume", func(c *schema.WaitCheckpointInfo) { c.ResumedByTurnID = c.ExecutionTurnID }},
	} {
		t.Run("mismatched "+test.name, func(t *testing.T) {
			s, path, writer := newFixture(t)
			if err := s.persistWaitCheckpoint(checkpoint); err != nil {
				t.Fatal(err)
			}
			wrong := resumed
			test.change(&wrong)
			appendRaw(t, writer, wrong.ResumedByTurnID, &wrong)
			if got, err := readWaitCheckpoint(path, checkpoint.SessionID); err == nil || got != nil {
				t.Fatalf("read accepted conflicting pair: %+v, %v", got, err)
			}
		})
	}

	t.Run("resumed without yield", func(t *testing.T) {
		_, path, writer := newFixture(t)
		appendRaw(t, writer, resumed.ResumedByTurnID, &resumed)
		if got, err := readWaitCheckpoint(path, checkpoint.SessionID); err == nil || got != nil {
			t.Fatalf("read accepted claim without yield: %+v, %v", got, err)
		}
	})

	t.Run("record execution identity", func(t *testing.T) {
		_, path, writer := newFixture(t)
		appendRaw(t, writer, "execution-other", &checkpoint)
		if got, err := readWaitCheckpoint(path, checkpoint.SessionID); err == nil || got != nil {
			t.Fatalf("read accepted mismatched record turn: %+v, %v", got, err)
		}
	})

	t.Run("malformed checkpoint records", func(t *testing.T) {
		missingExecution, missingCall := checkpoint, checkpoint
		missingExecution.ExecutionTurnID = ""
		missingCall.CommunicateCallID = ""
		for _, value := range []*schema.WaitCheckpointInfo{nil, &missingExecution, &missingCall} {
			s, path, writer := newFixture(t)
			if value != nil {
				if err := s.persistWaitCheckpoint(*value); err == nil {
					t.Fatal("persist accepted missing required identity")
				}
			}
			appendRaw(t, writer, checkpoint.ExecutionTurnID, value)
			if got, err := readWaitCheckpoint(path, checkpoint.SessionID); err == nil || got != nil {
				t.Fatalf("read accepted malformed checkpoint: %+v, %v", got, err)
			}
		}
	})

	t.Run("truncated final record preserves uncertainty", func(t *testing.T) {
		s, path, writer := newFixture(t)
		if err := s.persistWaitCheckpoint(checkpoint); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(`{"kind":"entry","turn":`); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		if got, err := readWaitCheckpoint(path, checkpoint.SessionID); !errors.Is(err, io.ErrUnexpectedEOF) || got != nil {
			t.Fatalf("truncated read = %+v, %v", got, err)
		}
	})

	t.Run("missing and unreadable evidence is not absence", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing.jsonl")
		if got, err := readWaitCheckpoint(path, checkpoint.SessionID); !errors.Is(err, os.ErrNotExist) || got != nil {
			t.Fatalf("missing read = %+v, %v", got, err)
		}
		if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, err := readWaitCheckpoint(path, checkpoint.SessionID); err == nil || got != nil {
			t.Fatalf("invalid header read = %+v, %v", got, err)
		}
		_, path, _ = newFixture(t)
		assertRead(t, path, nil)
	})

	t.Run("unavailable writer cannot claim persistence", func(t *testing.T) {
		s := &Session{id: checkpoint.SessionID}
		if err := s.persistWaitCheckpoint(checkpoint); err == nil {
			t.Fatal("pre-attach write succeeded")
		}
		s.attachTranscript(nil)
		if err := s.persistWaitCheckpoint(checkpoint); err == nil {
			t.Fatal("nil-writer write succeeded")
		}
		s, _, writer := newFixture(t)
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if err := s.persistWaitCheckpoint(checkpoint); !errors.Is(err, transcript.ErrWriterClosed) {
			t.Fatalf("closed-writer error = %v", err)
		}
	})

	t.Run("unsynced retained record remains an error", func(t *testing.T) {
		fs := &waitCheckpointFaultFS{Fs: afero.NewOsFs()}
		path := filepath.Join(t.TempDir(), "session.jsonl")
		writer, err := transcript.NewWriterWithFS(fs, path, transcript.Header{SessionID: checkpoint.SessionID})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = writer.Close() })
		s := &Session{id: checkpoint.SessionID}
		s.attachTranscript(writer)
		fs.fail = true
		if err := s.persistWaitCheckpoint(checkpoint); !errors.Is(err, transcript.ErrRetainedUnsynced) {
			t.Fatalf("unsynced persistence error = %v", err)
		}
		fs.fail = false
		if err := writer.EstablishDurability(); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		assertRead(t, path, &checkpoint)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Count(data, []byte{'\n'}) != 2 {
			t.Fatal("durability repair duplicated checkpoint")
		}
	})
}

// Faults live at the filesystem boundary; all writes, reads and close/reopen
// operations still use the real transcript writer and real files.
type waitCheckpointFaultFS struct {
	afero.Fs
	fail bool
}

type waitCheckpointFaultFile struct {
	afero.File
	fs *waitCheckpointFaultFS
}

func (fs *waitCheckpointFaultFS) Create(path string) (afero.File, error) {
	file, err := fs.Fs.Create(path)
	if err != nil {
		return nil, err
	}
	return &waitCheckpointFaultFile{File: file, fs: fs}, nil
}

func (fs *waitCheckpointFaultFS) OpenFile(path string, flag int, perm os.FileMode) (afero.File, error) {
	file, err := fs.Fs.OpenFile(path, flag, perm)
	if err != nil {
		return nil, err
	}
	return &waitCheckpointFaultFile{File: file, fs: fs}, nil
}

func (f *waitCheckpointFaultFile) Sync() error {
	if f.fs.fail {
		return os.ErrPermission
	}
	return f.File.Sync()
}

func (f *waitCheckpointFaultFile) Truncate(size int64) error {
	if f.fs.fail {
		return os.ErrPermission
	}
	return f.File.Truncate(size)
}
