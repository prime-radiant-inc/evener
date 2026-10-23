package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func waitResponseFor(id, target string) llm.Response {
	return toolCallResponse(communicateCallArgs(id, map[string]any{"message": "wait-payload-71a9", "end_turn": true, "end_reason": "waiting_on_work", "waiting_on": target}))
}

func waitProducerFIFO(t *testing.T) (string, func()) {
	t.Helper()
	fifo := filepath.Join(t.TempDir(), "release")
	if out, err := exec.Command("mkfifo", fifo).CombinedOutput(); err != nil {
		t.Fatalf("mkfifo: %v %s", err, out)
	}
	f, err := os.OpenFile(fifo, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return fifo, sync.OnceFunc(func() { _, _ = f.WriteString("release\n") })
}

func TestNotificationResumesYieldedWait(t *testing.T) {
	t.Parallel()
	for _, producer := range []string{"child", "shell"} {
		t.Run(producer, func(t *testing.T) {
			adapter := newMessagingAdapter("WAIT-OWNER-71", "WAIT-WORKER-71")
			gate := make(chan struct{})
			release := sync.OnceFunc(func() { close(gate) })
			defer release()
			var target string
			resumed := make(chan llm.Request, 1)
			adapter.script("root", func(llm.Request) llm.Response { return delegateToolResponse("owner", "WAIT-OWNER-71") }, func(llm.Request) llm.Response { return finalResponse("root-71") })
			if producer == "child" {
				adapter.script("WAIT-OWNER-71", func(llm.Request) llm.Response { return delegateToolResponse("worker", "WAIT-WORKER-71") })
				adapter.script("WAIT-WORKER-71", func(llm.Request) llm.Response {
					awaitSignal(t, gate, "worker release")
					return finalResponse("worker-payload-71")
				})
			} else {
				fifo, releaseShell := waitProducerFIFO(t)
				adapter.script("WAIT-OWNER-71", func(llm.Request) llm.Response {
					args, _ := json.Marshal(map[string]any{"command": fmt.Sprintf("read value < %q", fifo), "mode": "background"})
					return toolCallResponse(llm.ToolCallData{ID: "shell-71", Name: "shell", Arguments: args, Type: "function"})
				})
				go func() {
					<-gate
					releaseShell()
				}()
			}
			adapter.script("WAIT-OWNER-71", func(req llm.Request) llm.Response {
				if producer == "child" {
					target = deferredHeadDelegateID.FindString(lastToolResultText(req.Messages))
				} else {
					target = regexp.MustCompile(`job_[A-Za-z0-9_]{10,}`).FindString(lastToolResultText(req.Messages))
				}
				if target == "" {
					t.Error("missing real producer identity")
				}
				return waitResponseFor("wait-71", target)
			}, func(req llm.Request) llm.Response {
				resumed <- req
				return finalResponse("resumed-result-71")
			})
			root := newMessagingRoot(t, adapter, nil)
			claimFenced := make(chan bool, 1)
			root.SetDescendantRecordedFunc(func(_ string, rec transcript.Record) {
				if cp := rec.Turn.WaitCheckpoint; cp != nil && cp.ResumedByTurnID != "" {
					unlocked := root.delegateController.mu.TryLock()
					if unlocked {
						root.delegateController.mu.Unlock()
					}
					claimFenced <- !unlocked
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := root.ProcessInput(ctx, "start-71", nil); err != nil {
				t.Fatal(err)
			}
			adapter.waitForRequests(t, "WAIT-OWNER-71", 2)
			owner := root.subagents.sessions()[0]
			sub := root.getSub(owner.ID())
			t.Cleanup(func() {
				if t.Failed() {
					sub.mu.Lock()
					t.Logf("sub running=%v err=%v result=%s", sub.running, sub.err, sub.result)
					sub.mu.Unlock()
					owner.mu.Lock()
					t.Logf("comm=%+v execution=%+v", owner.comm, owner.execution)
					owner.mu.Unlock()
				}
			})
			// No producer can finish until release. The old polling loop never releases
			// sub.running or its model capacity here.
			waitForCondition(t, 3*time.Second, "yielded activation to release execution", func() bool { sub.mu.Lock(); defer sub.mu.Unlock(); return !sub.running })
			checkpoint, err := readWaitCheckpoint(transcriptPath(root.stateDir, owner.ID()), owner.ID())
			if err != nil || checkpoint == nil || checkpoint.ResumedByTurnID != "" || checkpoint.CommunicateCallID != "wait-71" {
				t.Fatalf("yield checkpoint=%+v err=%v", checkpoint, err)
			}
			generation := delegateAggregateSnapshot(t, root.delegateController, owner.owningDelegateID)
			if generation.Generation != 1 || !generation.CurrentRunOpen || generation.Phase != delegatestore.PhaseRunning || generation.LatestOutcome != nil {
				t.Fatalf("waiting generation=%+v", generation)
			}
			for _, event := range publicationEvents(t, root) {
				if event.DelegateID == owner.owningDelegateID && event.RunFinished != nil {
					t.Fatal("yield wrote RunFinished")
				}
			}
			pending, _ := root.pendingNotificationIDs()
			if len(pending) == 0 {
				t.Fatal("wait publication not delivered to owner before producer completion")
			}
			root.delegateController.mu.Lock()
			active := root.delegateController.turnsInUse
			root.delegateController.mu.Unlock()
			want := 0
			if producer == "child" {
				want = 1
			}
			if active != want {
				t.Fatalf("active model turns=%d want=%d", active, want)
			}
			release()
			select {
			case <-resumed:
			case <-ctx.Done():
				t.Fatal("producer did not resume yielded generation")
			}
			if !<-claimFenced {
				t.Fatal("resume checkpoint append escaped the original lease fence")
			}
			waitForCondition(t, 3*time.Second, "resumed generation settlement", func() bool {
				return !delegateAggregateSnapshot(t, root.delegateController, owner.owningDelegateID).CurrentRunOpen
			})
			final := delegateAggregateSnapshot(t, root.delegateController, owner.owningDelegateID)
			if final.Generation != 1 {
				t.Fatalf("resumption created generation %d", final.Generation)
			}
			// Check the raw private resumption record, not a status projection.
			claimed, err := readWaitCheckpoint(transcriptPath(root.stateDir, owner.ID()), owner.ID())
			if err != nil || claimed == nil || claimed.ResumedByTurnID == "" || claimed.ResumedByTurnID == checkpoint.ExecutionTurnID {
				t.Fatalf("resume claim=%+v err=%v", claimed, err)
			}
		})
	}
}

func TestAlreadyObservedTargetCannotStrandWait(t *testing.T) {
	t.Parallel()
	t.Run("completion after validation", testWaitTargetCompletesBeforeCheckpoint)
	for _, producer := range []string{"child", "shell"} {
		t.Run(producer, func(t *testing.T) {
			adapter := newMessagingAdapter("STALE-WORKER-19")
			var target string
			gate := make(chan struct{})
			release := sync.OnceFunc(func() { close(gate) })
			defer release()
			if producer == "child" {
				adapter.script("root", func(llm.Request) llm.Response { return delegateToolResponse("worker-19", "STALE-WORKER-19") }, func(req llm.Request) llm.Response {
					target = deferredHeadDelegateID.FindString(lastToolResultText(req.Messages))
					return finalResponse("initial-19")
				})
				adapter.script("STALE-WORKER-19", func(llm.Request) llm.Response { <-gate; return finalResponse("terminal-payload-19") })
			} else {
				fifo, releaseShell := waitProducerFIFO(t)
				go func() { <-gate; releaseShell() }()
				adapter.script("root", func(llm.Request) llm.Response {
					args, _ := json.Marshal(map[string]any{"command": fmt.Sprintf("read value < %q", fifo), "mode": "background"})
					return toolCallResponse(llm.ToolCallData{ID: "shell-19", Name: "shell", Arguments: args, Type: "function"})
				}, func(req llm.Request) llm.Response {
					target = regexp.MustCompile(`job_[A-Za-z0-9_]{10,}`).FindString(lastToolResultText(req.Messages))
					return finalResponse("initial-19")
				})
			}
			root := newMessagingRoot(t, adapter, nil)
			if _, err := root.ProcessInput(context.Background(), "start-19", nil); err != nil {
				t.Fatal(err)
			}
			release()
			waitForCondition(t, 3*time.Second, "real producer completion custody", func() bool { ids, _ := root.pendingNotificationIDs(); return len(ids) > 0 })
			adapter.script("root", func(llm.Request) llm.Response { return finalResponse("observed-19") })
			if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
				t.Fatal(err)
			}
			assertNotificationPending(t, root, []string{})
			// Completion has been durably observed before attempting to reuse it.
			adapter.script("root", func(llm.Request) llm.Response { return waitResponseFor("stale-wait-19", target) }, func(req llm.Request) llm.Response {
				var found *llm.ToolResultData
				for _, msg := range req.Messages {
					for _, part := range msg.Content {
						if part.ToolResult != nil && part.ToolResult.ToolCallID == "stale-wait-19" {
							found = part.ToolResult
						}
					}
				}
				if found == nil || found.IsError {
					t.Errorf("stale wait is not an ordinary refused result: %+v", found)
				} else {
					var result struct {
						Accepted    bool            `json:"accepted"`
						Reason      string          `json:"reason"`
						TargetState json.RawMessage `json:"target_state"`
					}
					if err := json.Unmarshal([]byte(fmt.Sprint(found.Content)), &result); err != nil || result.Accepted || result.Reason != "stale_wait_target" || len(result.TargetState) == 0 {
						t.Errorf("stale target state=%s err=%v", found.Content, err)
					}
					if producer == "child" {
						var state struct {
							Phase      string `json:"phase"`
							Generation int    `json:"generation"`
							Outcome    struct {
								Status string `json:"status"`
							} `json:"outcome"`
						}
						if err := json.Unmarshal(result.TargetState, &state); err != nil || state.Phase != "idle" || state.Generation != 1 || state.Outcome.Status != "completed" {
							t.Errorf("actual child terminal state=%s err=%v", result.TargetState, err)
						}
					} else {
						var state struct {
							JobID  string `json:"job_id"`
							Status string `json:"status"`
						}
						if err := json.Unmarshal(result.TargetState, &state); err != nil || state.JobID != target || state.Status != "completed" {
							t.Errorf("actual shell terminal state=%s err=%v", result.TargetState, err)
						}
					}
				}
				return finalResponse("refused-19")
			})
			if _, err := root.ProcessInput(context.Background(), "try-wait-19", nil); err != nil {
				t.Fatal(err)
			}
			checkpoint, err := readWaitCheckpoint(transcriptPath(root.stateDir, root.ID()), root.ID())
			if err != nil || checkpoint != nil {
				t.Fatalf("stale wait checkpoint=%+v err=%v", checkpoint, err)
			}
			assertNotificationPending(t, root, []string{})
			data, err := readTranscriptFull(transcriptPath(root.stateDir, root.ID()), "")
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range data.Entries {
				if entry.Turn.Communicate != nil && entry.Turn.Communicate.CallID == "stale-wait-19" {
					t.Fatal("refused wait was published")
				}
			}
		})
	}
}

func testWaitTargetCompletesBeforeCheckpoint(t *testing.T) {
	adapter := newMessagingAdapter("RACE-WAIT-26", "RACE-CHILD-26")
	gate, recorded, checkpointGate := make(chan struct{}), make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	unblock := sync.OnceFunc(func() { close(checkpointGate) })
	defer release()
	defer unblock()
	adapter.script("root", func(llm.Request) llm.Response { return delegateToolResponse("race-owner", "RACE-WAIT-26") }, func(llm.Request) llm.Response { return finalResponse("root-26") })
	adapter.script("RACE-WAIT-26", func(llm.Request) llm.Response { return delegateToolResponse("race-child", "RACE-CHILD-26") }, func(req llm.Request) llm.Response {
		return waitResponseFor("race-wait-26", deferredHeadDelegateID.FindString(lastToolResultText(req.Messages)))
	}, func(llm.Request) llm.Response { return finalResponse("race-result-26") })
	adapter.script("RACE-CHILD-26", func(llm.Request) llm.Response { <-gate; return finalResponse("race-child-result-26") })
	root := newMessagingRoot(t, adapter, nil)
	root.SetDescendantRecordedFunc(func(_ string, rec transcript.Record) {
		if rec.Turn.Communicate != nil && rec.Turn.Communicate.CallID == "race-wait-26" {
			close(recorded)
			<-checkpointGate
		}
	})
	if _, err := root.ProcessInput(context.Background(), "start-26", nil); err != nil {
		t.Fatal(err)
	}
	awaitSignal(t, recorded, "accepted wait publication before checkpoint")
	owner := root.subagents.sessions()[0]
	child := owner.subagents.sessions()[0]
	release()
	waitForCondition(t, 3*time.Second, "target completion before checkpoint", func() bool {
		return !delegateAggregateSnapshot(t, root.delegateController, child.owningDelegateID).CurrentRunOpen
	})
	cp, err := readWaitCheckpoint(transcriptPath(root.stateDir, owner.ID()), owner.ID())
	if err != nil || cp != nil {
		t.Fatalf("checkpoint preceded target completion: %+v %v", cp, err)
	}
	unblock()
	waitForCondition(t, 3*time.Second, "race accepted wait resumed", func() bool {
		return !delegateAggregateSnapshot(t, root.delegateController, owner.owningDelegateID).CurrentRunOpen
	})
	if len(adapter.requestsFor("RACE-WAIT-26")) != 3 {
		t.Fatal("completion lost between validation and checkpoint")
	}
	cp, err = readWaitCheckpoint(transcriptPath(root.stateDir, owner.ID()), owner.ID())
	if err != nil || cp == nil || cp.ResumedByTurnID == "" {
		t.Fatalf("accepted race wait not claimed: %+v %v", cp, err)
	}
}

// Copy only this test's generated state, never alter a live writer's input.
func copyWaitingState(t *testing.T, source string) string {
	t.Helper()
	dest := t.TempDir()
	if err := filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		to := filepath.Join(dest, rel)
		if entry.IsDir() {
			return os.MkdirAll(to, 0o700)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(to, data, 0o600)
	}); err != nil {
		t.Fatal(err)
	}
	return dest
}

func restoreWaitingCopy(t *testing.T, root *Session, stateDir string) *Session {
	t.Helper()
	meta, err := schema.LoadSessionMeta(stateDir, root.ID())
	if err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreSessionFromMetaWithConfig(root.client, root.profile, execenv.NewLocalExecutionEnvironment(meta.EnvInfo.WorkingDir), meta, RestoreSessionConfig{
		StateDir: stateDir, ForceRealIO: true,
		testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, disableDelegateIdleRelease: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restored.Close)
	return restored
}

func TestWaitCheckpointBeforeCompletionMarkerReopen(t *testing.T) {
	adapter := newMessagingAdapter()
	adapter.script("root", func(llm.Request) llm.Response { return waitResponseFor("root-wait-88", "") })
	root := newMessagingRoot(t, adapter, nil)
	if _, err := root.ProcessInput(context.Background(), "root-wait-88", nil); err != nil {
		t.Fatal(err)
	}
	cp, err := readWaitCheckpoint(transcriptPath(root.stateDir, root.ID()), root.ID())
	if err != nil || cp == nil {
		t.Fatalf("real root wait checkpoint=%+v err=%v", cp, err)
	}
	copyDir := copyWaitingState(t, root.stateDir)
	path := transcriptPath(copyDir, root.ID())
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cut []byte
	for _, line := range bytes.SplitAfter(raw, []byte{'\n'}) {
		cut = append(cut, line...)
		entry, err := transcript.DecodeEntry(bytes.TrimSpace(line))
		if err == nil && entry.Turn.WaitCheckpoint != nil {
			break
		}
	}
	if err := os.WriteFile(path, cut, 0o600); err != nil {
		t.Fatal(err)
	}
	restored := restoreWaitingCopy(t, root, copyDir)
	data, err := readTranscriptFull(path, "")
	if err != nil {
		t.Fatal(err)
	}
	var status schema.TurnCompletionStatus
	for _, entry := range data.Entries {
		if entry.Turn.TurnID == cp.ExecutionTurnID && entry.Turn.Completion != nil {
			status = entry.Turn.Completion.Status
		}
	}
	if status != schema.TurnCompleted {
		t.Fatalf("safe wait accounting repaired as %q, want completed", status)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	adapter.script("root", func(llm.Request) llm.Response { close(entered); <-release; return finalResponse("resumed-88") })
	done := make(chan error, 1)
	go func() { _, err := restored.ProcessInput(context.Background(), "resume-88", nil); done <- err }()
	awaitSignal(t, entered, "resumed model request")
	claimed, err := readWaitCheckpoint(path, root.ID())
	if err != nil || claimed.ResumedByTurnID == "" {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	interruptedCopy := copyWaitingState(t, copyDir)
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	_ = restoreWaitingCopy(t, root, interruptedCopy)
	data, err = readTranscriptFull(transcriptPath(interruptedCopy, root.ID()), "")
	if err != nil {
		t.Fatal(err)
	}
	status = ""
	for _, entry := range data.Entries {
		if entry.Turn.TurnID == claimed.ResumedByTurnID && entry.Turn.Completion != nil {
			status = entry.Turn.Completion.Status
		}
	}
	if status != schema.TurnInterrupted {
		t.Fatalf("resumed activation crash repaired as %q, want interrupted", status)
	}
}

func TestMissingWaitProducerNotifiesOnce(t *testing.T) {
	adapter := newMessagingAdapter("COLD-WAIT-37")
	fifo, releaseShell := waitProducerFIFO(t)
	defer releaseShell()
	adapter.script("root", func(llm.Request) llm.Response { return delegateToolResponse("cold-owner", "COLD-WAIT-37") }, func(llm.Request) llm.Response { return finalResponse("root-37") })
	adapter.script("COLD-WAIT-37", func(llm.Request) llm.Response {
		return toolCallResponse(llm.ToolCallData{ID: "unfinished-37", Name: "task_list", Type: "function", Arguments: json.RawMessage(`{"add":[{"type":"implement","description":"unfinished task","prompt":"leave this incomplete"}]}`)})
	}, func(llm.Request) llm.Response {
		args, _ := json.Marshal(map[string]any{"command": fmt.Sprintf("read value < %q", fifo), "mode": "background"})
		return toolCallResponse(llm.ToolCallData{ID: "shell-37", Name: "shell", Type: "function", Arguments: args})
	}, func(req llm.Request) llm.Response {
		return waitResponseFor("cold-wait-37", regexp.MustCompile(`job_[A-Za-z0-9_]{10,}`).FindString(lastToolResultText(req.Messages)))
	})
	root := newMessagingRoot(t, adapter, nil)
	if _, err := root.ProcessInput(context.Background(), "start-37", nil); err != nil {
		t.Fatal(err)
	}
	owner := root.subagents.sessions()[0]
	sub := root.getSub(owner.ID())
	waitForCondition(t, 3*time.Second, "cold fixture yield", func() bool { sub.mu.Lock(); defer sub.mu.Unlock(); return !sub.running })
	copyDir := copyWaitingState(t, root.stateDir)
	jobPath := filepath.Join(jobsDir(copyDir, owner.ID()), "jobs.jsonl")
	if err := os.Remove(jobPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(jobsDir(copyDir, root.ID()), "jobs.jsonl")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	entered, gate := make(chan llm.Request, 1), make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	defer release()
	adapter.script("COLD-WAIT-37", func(req llm.Request) llm.Response { entered <- req; <-gate; return finalResponse("cold-result-37") })
	restored := restoreWaitingCopy(t, root, copyDir)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("cold wait did not resume")
	}

	before := delegateAggregateSnapshot(t, restored.delegateController, owner.owningDelegateID)
	if before.Generation != 1 || !before.CurrentRunOpen {
		t.Fatalf("cold resumed generation=%+v", before)
	}
	release()
	waitForCondition(t, 3*time.Second, "cold resumed settlement", func() bool {
		return !delegateAggregateSnapshot(t, restored.delegateController, owner.owningDelegateID).CurrentRunOpen
	})
	if requests := len(adapter.requestsFor("COLD-WAIT-37")); requests != 4 {
		t.Fatalf("model requests=%d want exactly4", requests)
	}
	resumedOwner := restored.getSub(owner.ID())
	if resumedOwner == nil {
		t.Fatal("resumed owner missing")
	}
	if summary := resumedOwner.sess.getOrCreateTaskStore().Summary(); summary.Total != 1 || summary.Done != 0 {
		t.Fatalf("unfinished task was lost or completed: %+v", summary)
	}
	for _, event := range publicationEvents(t, restored) {
		if event.DelegateID == owner.owningDelegateID && event.RunStarted != nil && event.RunStarted.Generation != 1 {
			t.Fatal("cold resume replaced generation")
		}
	}
	data, err := readTranscriptFull(transcriptPath(copyDir, owner.ID()), "")
	if err != nil {
		t.Fatal(err)
	}
	interruptions := 0
	for _, entry := range data.Entries {
		if strings.HasPrefix(entry.Turn.AttentionID, "wait-interrupted:") {
			interruptions++
		}
	}
	if interruptions != 1 {
		t.Fatalf("interruption records=%d want1", interruptions)
	}
}

func TestYieldedWaitCapacityReleaseAndStop(t *testing.T) {
	adapter := newMessagingAdapter("CAP-WAIT-52", "CAP-BUSY-52")
	fifo, releaseShell := waitProducerFIFO(t)
	defer releaseShell()
	busyGate := make(chan struct{})
	releaseBusy := sync.OnceFunc(func() { close(busyGate) })
	defer releaseBusy()
	adapter.script("root", func(llm.Request) llm.Response { return delegateToolResponse("cap-owner", "CAP-WAIT-52") }, func(llm.Request) llm.Response { return finalResponse("root-52") })
	var target string
	resumed := make(chan struct{}, 1)
	adapter.script("CAP-WAIT-52", func(llm.Request) llm.Response {
		args, _ := json.Marshal(map[string]any{"command": fmt.Sprintf("read value < %q", fifo), "mode": "background"})
		return toolCallResponse(llm.ToolCallData{ID: "shell-52", Name: "shell", Type: "function", Arguments: args})
	}, func(req llm.Request) llm.Response {
		target = regexp.MustCompile(`job_[A-Za-z0-9_]{10,}`).FindString(lastToolResultText(req.Messages))
		return waitResponseFor("cap-wait-52", target)
	}, func(llm.Request) llm.Response {
		resumed <- struct{}{}
		return waitResponseFor("cap-wait-again-52", target)
	})
	adapter.script("CAP-BUSY-52", func(llm.Request) llm.Response { <-busyGate; return finalResponse("busy-result-52") })
	root := newMessagingRoot(t, adapter, func(cfg *SessionConfig) {
		cfg.MaxConcurrentDelegateTurns = 1
		cfg.clock = agenttest.NewFakeClock() // Never advance, retries cannot mask a missed release wake.
	})
	if _, err := root.ProcessInput(context.Background(), "start-52", nil); err != nil {
		t.Fatal(err)
	}
	owner := root.subagents.sessions()[0]
	sub := root.getSub(owner.ID())
	waitForCondition(t, 3*time.Second, "capacity fixture yield", func() bool { sub.mu.Lock(); defer sub.mu.Unlock(); return !sub.running })
	adapter.script("root", func(llm.Request) llm.Response { return delegateToolResponse("cap-busy", "CAP-BUSY-52") }, func(llm.Request) llm.Response { return finalResponse("root-busy-52") })
	if _, err := root.ProcessInput(context.Background(), "busy-52", nil); err != nil {
		t.Fatal(err)
	}
	adapter.waitForRequests(t, "CAP-BUSY-52", 1)
	if err := root.delegateController.enqueueNotification(owner.ID(), notification{ID: "unrelated-cap-52", Message: llm.UserMachinery("unrelated-cap-payload-52")}); err != nil {
		t.Fatal(err)
	}
	root.delegateController.mu.Lock()
	active := root.delegateController.turnsInUse
	root.delegateController.mu.Unlock()
	if active != 1 || len(adapter.requestsFor("CAP-WAIT-52")) != 2 {
		t.Fatalf("capacity refusal active=%d requests=%d", active, len(adapter.requestsFor("CAP-WAIT-52")))
	}
	releaseBusy()
	awaitSignal(t, resumed, "capacity release wakes yielded receiver")
	root.delegateController.mu.Lock()
	admissionOrder := root.delegateController.attention[owner.owningDelegateID].driveTurn
	root.delegateController.mu.Unlock()
	if admissionOrder == 0 {
		t.Fatal("same-generation admission did not advance existing fairness order")
	}
	waitForCondition(t, 3*time.Second, "second yield", func() bool { sub.mu.Lock(); defer sub.mu.Unlock(); return !sub.running })
	root.delegateController.mu.Lock()
	active = root.delegateController.turnsInUse
	root.delegateController.mu.Unlock()
	if active != 0 {
		t.Fatalf("second yield capacity=%d want0", active)
	}
	result, plan, mutations, err := root.delegateController.StopSubtreeAndDrive(context.Background(), rootDelegateActor(root.ID()), owner.owningDelegateID)
	if err != nil {
		t.Fatal(err)
	}
	executeDelegateCancelPlan(plan)
	if err := root.executeDelegateMutationPlans(mutations); err != nil {
		t.Fatal(err)
	}
	select {
	case <-result.done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop did not settle yielded generation")
	}
	root.delegateController.mu.Lock()
	active = root.delegateController.turnsInUse
	root.delegateController.mu.Unlock()
	if active != 0 {
		t.Fatalf("Stop double released capacity=%d", active)
	}
}

func TestAttentionWaitPreservesSiblingDriveCapacity(t *testing.T) {
	adapter := newMessagingAdapter("DRIVE-WAIT-72", "DRIVE-BUSY-72")
	fifo, releaseShell := waitProducerFIFO(t)
	defer releaseShell()
	busyGate := make(chan struct{})
	releaseBusy := sync.OnceFunc(func() { close(busyGate) })
	defer releaseBusy()
	adapter.script("root", func(llm.Request) llm.Response { return delegateToolResponse("drive-owner", "DRIVE-WAIT-72") }, func(llm.Request) llm.Response { return finalResponse("root-72") })
	adapter.script("DRIVE-WAIT-72", func(llm.Request) llm.Response { return finalResponse("initial-owner-72") }, func(llm.Request) llm.Response {
		args, _ := json.Marshal(map[string]any{"command": fmt.Sprintf("read value < %q", fifo), "mode": "background"})
		return toolCallResponse(llm.ToolCallData{ID: "shell-72", Name: "shell", Type: "function", Arguments: args})
	}, func(req llm.Request) llm.Response {
		return waitResponseFor("drive-wait-72", regexp.MustCompile(`job_[A-Za-z0-9_]{10,}`).FindString(lastToolResultText(req.Messages)))
	})
	adapter.script("DRIVE-BUSY-72", func(llm.Request) llm.Response { return finalResponse("initial-busy-72") }, func(llm.Request) llm.Response { <-busyGate; return finalResponse("busy-drive-72") })
	root := newMessagingRoot(t, adapter, nil)
	if _, err := root.ProcessInput(context.Background(), "start-72", nil); err != nil {
		t.Fatal(err)
	}
	owner := root.subagents.sessions()[0]
	waitForCondition(t, 3*time.Second, "owner initial settlement", func() bool {
		return !delegateAggregateSnapshot(t, root.delegateController, owner.owningDelegateID).CurrentRunOpen
	})
	adapter.script("root", func(llm.Request) llm.Response { return delegateToolResponse("drive-busy", "DRIVE-BUSY-72") }, func(llm.Request) llm.Response { return finalResponse("root-busy-72") })
	if _, err := root.ProcessInput(context.Background(), "busy-72", nil); err != nil {
		t.Fatal(err)
	}
	var busy *Session
	for _, candidate := range root.subagents.sessions() {
		if candidate.ID() != owner.ID() {
			busy = candidate
		}
	}
	if busy == nil {
		t.Fatal("busy sibling missing")
	}
	waitForCondition(t, 3*time.Second, "busy initial settlement", func() bool {
		return !delegateAggregateSnapshot(t, root.delegateController, busy.owningDelegateID).CurrentRunOpen
	})
	if err := root.delegateController.enqueueNotification(owner.ID(), notification{ID: "drive-owner-72", Message: llm.UserMachinery("drive-owner-payload-72")}); err != nil {
		t.Fatal(err)
	}
	sub := root.getSub(owner.ID())
	waitForCondition(t, 3*time.Second, "attention generation yield", func() bool {
		run := delegateAggregateSnapshot(t, root.delegateController, owner.owningDelegateID)
		sub.mu.Lock()
		defer sub.mu.Unlock()
		return run.Generation == 2 && run.Trigger == delegatestore.TriggerAttention && run.CurrentRunOpen && !sub.running
	})
	if err := root.delegateController.enqueueNotification(busy.ID(), notification{ID: "drive-busy-72", Message: llm.UserMachinery("drive-busy-payload-72")}); err != nil {
		t.Fatal(err)
	}
	adapter.waitForRequests(t, "DRIVE-BUSY-72", 2)
	result, plan, mutations, err := root.delegateController.StopSubtreeAndDrive(context.Background(), rootDelegateActor(root.ID()), owner.owningDelegateID)
	if err != nil {
		t.Fatal(err)
	}
	executeDelegateCancelPlan(plan)
	if err := root.executeDelegateMutationPlans(mutations); err != nil {
		t.Fatal(err)
	}
	awaitSignal(t, result.done, "yielded attention generation Stop")
	root.delegateController.mu.Lock()
	turns, drives := root.delegateController.turnsInUse, root.delegateController.drivesInUse
	root.delegateController.mu.Unlock()
	if turns != 0 || drives != 1 {
		t.Fatalf("Stop stole sibling drive capacity: turns=%d drives=%d, want0,1", turns, drives)
	}
	releaseBusy()
	waitForCondition(t, 3*time.Second, "sibling drive settlement", func() bool {
		root.delegateController.mu.Lock()
		defer root.delegateController.mu.Unlock()
		return root.delegateController.drivesInUse == 0
	})
}

func TestDirectUserStopYieldedWait(t *testing.T) {
	adapter := newMessagingAdapter("USER-WAIT-63", "USER-CHILD-63")
	gate := make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	defer release()
	adapter.script("root", func(llm.Request) llm.Response { return delegateToolResponse("user-owner", "USER-WAIT-63") }, func(llm.Request) llm.Response { return finalResponse("root-63") })
	adapter.script("USER-WAIT-63", func(llm.Request) llm.Response { return delegateToolResponse("user-child", "USER-CHILD-63") }, func(req llm.Request) llm.Response {
		return waitResponseFor("user-wait-63", deferredHeadDelegateID.FindString(lastToolResultText(req.Messages)))
	})
	adapter.script("USER-CHILD-63", func(llm.Request) llm.Response { <-gate; return finalResponse("child-result-63") })
	root := newMessagingRoot(t, adapter, nil)
	if _, err := root.ProcessInput(context.Background(), "start-63", nil); err != nil {
		t.Fatal(err)
	}
	owner := root.subagents.sessions()[0]
	sub := root.getSub(owner.ID())
	waitForCondition(t, 3*time.Second, "direct Stop fixture yield", func() bool { sub.mu.Lock(); defer sub.mu.Unlock(); return !sub.running })
	outcome, err := root.StopDelegateRun(owner.owningDelegateID)
	if err != nil || outcome != appwire.DelegateStopStopping {
		t.Fatalf("direct Stop=%s err=%v", outcome, err)
	}
	waitForCondition(t, 3*time.Second, "direct Stop settlement", func() bool {
		return !delegateAggregateSnapshot(t, root.delegateController, owner.owningDelegateID).CurrentRunOpen
	})
	if again, err := root.StopDelegateRun(owner.owningDelegateID); err != nil || again != appwire.DelegateStopNotRunning {
		t.Fatalf("repeated Stop=%s err=%v", again, err)
	}
	cp, err := readWaitCheckpoint(transcriptPath(root.stateDir, owner.ID()), owner.ID())
	if err != nil || cp.ResumedByTurnID != "" {
		t.Fatalf("Stop admitted a resumed execution: %+v %v", cp, err)
	}
	child := owner.subagents.sessions()[0]
	if !delegateAggregateSnapshot(t, root.delegateController, child.owningDelegateID).CurrentRunOpen {
		t.Fatal("direct Stop settled live child")
	}
	root.delegateController.mu.Lock()
	active := root.delegateController.turnsInUse
	root.delegateController.mu.Unlock()
	if active != 1 {
		t.Fatalf("direct Stop stole child capacity=%d want1", active)
	}
	finishes := 0
	for _, event := range publicationEvents(t, root) {
		if event.DelegateID == owner.owningDelegateID && event.RunFinished != nil {
			finishes++
			if event.RunFinished.Outcome.Status != delegatestore.OutcomeCancelled {
				t.Fatalf("direct Stop outcome=%+v", event.RunFinished.Outcome)
			}
		}
	}
	if finishes != 1 {
		t.Fatalf("direct Stop finishes=%d want1", finishes)
	}
	release()
	waitForCondition(t, 3*time.Second, "retained child result successor", func() bool {
		run := delegateAggregateSnapshot(t, root.delegateController, owner.owningDelegateID)
		return run.Generation == 2 && !run.CurrentRunOpen
	})
	if requests := len(adapter.requestsFor("USER-WAIT-63")); requests != 3 {
		t.Fatalf("retained child result model requests=%d want3", requests)
	}
}

func TestWaitResumeClaimSerializesStop(t *testing.T) {
	for _, claimFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("claim_first=%v", claimFirst), func(t *testing.T) {
			adapter := newMessagingAdapter("FENCE-WAIT-81")
			fifo, releaseShell := waitProducerFIFO(t)
			defer releaseShell()
			adapter.script("root", func(llm.Request) llm.Response { return delegateToolResponse("fence-owner", "FENCE-WAIT-81") }, func(llm.Request) llm.Response { return finalResponse("root-81") })
			modelGate := make(chan struct{})
			releaseModel := sync.OnceFunc(func() { close(modelGate) })
			defer releaseModel()
			adapter.script("FENCE-WAIT-81", func(llm.Request) llm.Response {
				args, _ := json.Marshal(map[string]any{"command": fmt.Sprintf("read value < %q", fifo), "mode": "background"})
				return toolCallResponse(llm.ToolCallData{ID: "shell-81", Name: "shell", Type: "function", Arguments: args})
			}, func(req llm.Request) llm.Response {
				return waitResponseFor("fence-wait-81", regexp.MustCompile(`job_[A-Za-z0-9_]{10,}`).FindString(lastToolResultText(req.Messages)))
			}, func(llm.Request) llm.Response { <-modelGate; return finalResponse("fence-result-81") })
			root := newMessagingRoot(t, adapter, nil)
			claimEntered, claimGate := make(chan bool, 1), make(chan struct{})
			releaseClaim := sync.OnceFunc(func() { close(claimGate) })
			defer releaseClaim()
			root.SetDescendantRecordedFunc(func(_ string, rec transcript.Record) {
				if cp := rec.Turn.WaitCheckpoint; cp != nil && cp.ResumedByTurnID != "" {
					unlocked := root.delegateController.mu.TryLock()
					if unlocked {
						root.delegateController.mu.Unlock()
					}
					claimEntered <- !unlocked
					<-claimGate
				}
			})
			if _, err := root.ProcessInput(context.Background(), "start-81", nil); err != nil {
				t.Fatal(err)
			}
			owner := root.subagents.sessions()[0]
			sub := root.getSub(owner.ID())
			waitForCondition(t, 3*time.Second, "fence fixture yielded", func() bool { sub.mu.Lock(); defer sub.mu.Unlock(); return !sub.running })
			if claimFirst {
				if err := root.delegateController.enqueueNotification(owner.ID(), notification{ID: "fence-message-81", Message: llm.UserMachinery("fence-payload-81")}); err != nil {
					t.Fatal(err)
				}
				select {
				case fenced := <-claimEntered:
					if !fenced {
						t.Error("claim append is not serialized with Stop")
					}
				case <-time.After(3 * time.Second):
					t.Fatal("resume claim did not reach the writer")
				}
			}
			stopStarted, stopDone := make(chan struct{}), make(chan struct{})
			go func() {
				close(stopStarted)
				result, plan, mutations, err := root.delegateController.StopSubtreeAndDrive(context.Background(), rootDelegateActor(root.ID()), owner.owningDelegateID)
				if err != nil {
					t.Error(err)
					close(stopDone)
					return
				}
				executeDelegateCancelPlan(plan)
				if err := root.executeDelegateMutationPlans(mutations); err != nil {
					t.Error(err)
				}
				releaseModel()
				<-result.done
				close(stopDone)
			}()
			awaitSignal(t, stopStarted, "Stop contender started")
			releaseClaim()
			awaitSignal(t, stopDone, "Stop settles exact generation")
			data, err := readTranscriptFull(transcriptPath(root.stateDir, owner.ID()), "")
			if err != nil {
				t.Fatal(err)
			}
			claims, finishes := 0, 0
			for _, entry := range data.Entries {
				if cp := entry.Turn.WaitCheckpoint; cp != nil && cp.ResumedByTurnID != "" {
					claims++
				}
			}
			for _, event := range publicationEvents(t, root) {
				if event.DelegateID == owner.owningDelegateID && event.RunFinished != nil {
					finishes++
					if event.RunFinished.Generation != 1 || event.RunFinished.Outcome.Status != delegatestore.OutcomeStopped {
						t.Fatalf("Stop outcome=%+v", event.RunFinished)
					}
				}
			}
			wantClaims := 0
			if claimFirst {
				wantClaims = 1
			}
			if claims != wantClaims || finishes != 1 {
				t.Fatalf("claims=%d want%d, stopped outcomes=%d want1", claims, wantClaims, finishes)
			}
		})
	}
}

func TestUnrelatedNotificationResumesWait(t *testing.T) {
	adapter := newMessagingAdapter("OTHER-WAIT-94", "OTHER-CHILD-94")
	fifo, releaseShell := waitProducerFIFO(t)
	defer releaseShell()
	updateGate, finishGate := make(chan struct{}), make(chan struct{})
	releaseUpdate := sync.OnceFunc(func() { close(updateGate) })
	releaseChild := sync.OnceFunc(func() { close(finishGate) })
	defer releaseUpdate()
	defer releaseChild()
	resumed := make(chan struct{}, 1)
	var jobID string
	adapter.script("root", func(llm.Request) llm.Response { return delegateToolResponse("other-owner", "OTHER-WAIT-94") }, func(llm.Request) llm.Response { return finalResponse("root-94") })
	adapter.script("OTHER-WAIT-94", func(llm.Request) llm.Response {
		args, _ := json.Marshal(map[string]any{"command": fmt.Sprintf("read value < %q", fifo), "mode": "background"})
		return toolCallResponse(llm.ToolCallData{ID: "shell-94", Name: "shell", Type: "function", Arguments: args})
	}, func(req llm.Request) llm.Response {
		jobID = regexp.MustCompile(`job_[A-Za-z0-9_]{10,}`).FindString(lastToolResultText(req.Messages))
		return delegateToolResponse("other-child", "OTHER-CHILD-94")
	}, func(llm.Request) llm.Response { return waitResponseFor("other-wait-94", jobID) }, func(llm.Request) llm.Response {
		resumed <- struct{}{}
		return waitResponseFor("other-wait-again-94", jobID)
	})
	adapter.script("OTHER-CHILD-94", func(llm.Request) llm.Response {
		<-updateGate
		return updateResponse("other-update-94", "other-child-payload-94")
	}, func(llm.Request) llm.Response { <-finishGate; return finalResponse("other-child-result-94") })
	root := newMessagingRoot(t, adapter, nil)
	if _, err := root.ProcessInput(context.Background(), "start-94", nil); err != nil {
		t.Fatal(err)
	}
	owner := root.subagents.sessions()[0]
	sub := root.getSub(owner.ID())
	waitForCondition(t, 3*time.Second, "unrelated fixture yield", func() bool { sub.mu.Lock(); defer sub.mu.Unlock(); return !sub.running })
	releaseUpdate()
	awaitSignal(t, resumed, "unrelated child update resumes shell wait")
	waitForCondition(t, 3*time.Second, "unrelated update processed and yielded again", func() bool { sub.mu.Lock(); defer sub.mu.Unlock(); return !sub.running })
	journal, err := retirementJobJournal(filepath.Join(jobsDir(root.stateDir, owner.ID()), "jobs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	job := jobstore.Fold(journal)[jobID]
	if job == nil || job.Status.IsTerminal() {
		t.Fatalf("unrelated notification claimed waited job finished: %+v", job)
	}
	if run := delegateAggregateSnapshot(t, root.delegateController, owner.owningDelegateID); run.Generation != 1 || !run.CurrentRunOpen {
		t.Fatalf("unrelated notification changed generation: %+v", run)
	}
	data, err := readTranscriptFull(transcriptPath(root.stateDir, owner.ID()), "")
	if err != nil {
		t.Fatal(err)
	}
	claimed := 0
	for _, entry := range data.Entries {
		if cp := entry.Turn.WaitCheckpoint; cp != nil && cp.CommunicateCallID == "other-wait-94" && cp.ResumedByTurnID != "" {
			claimed++
		}
	}
	if claimed != 1 {
		t.Fatalf("unrelated update resume claims=%d want1", claimed)
	}
	releaseChild()
	releaseShell()
}

func TestColdWaitingAncestorRestoresOnlyAddressedDescendant(t *testing.T) {
	adapter := newMessagingAdapter("TREE-WAIT-45", "TREE-CHILD-45")
	fifo, releaseShell := waitProducerFIFO(t)
	defer releaseShell()
	childGate := make(chan struct{})
	releaseChild := sync.OnceFunc(func() { close(childGate) })
	defer releaseChild()
	var childID string
	adapter.script("root", func(llm.Request) llm.Response { return delegateToolResponse("tree-owner", "TREE-WAIT-45") }, func(llm.Request) llm.Response { return finalResponse("root-45") })
	adapter.script("TREE-WAIT-45", func(llm.Request) llm.Response { return delegateToolResponse("tree-child", "TREE-CHILD-45") }, func(req llm.Request) llm.Response {
		childID = deferredHeadDelegateID.FindString(lastToolResultText(req.Messages))
		return waitResponseFor("tree-wait-45", childID)
	}, func(llm.Request) llm.Response { return waitResponseFor("tree-wait-again-45", childID) })
	adapter.script("TREE-CHILD-45", func(llm.Request) llm.Response {
		<-childGate
		args, _ := json.Marshal(map[string]any{"command": fmt.Sprintf("read value < %q", fifo), "mode": "background"})
		return toolCallResponse(llm.ToolCallData{ID: "shell-45", Name: "shell", Type: "function", Arguments: args})
	}, func(req llm.Request) llm.Response {
		return waitResponseFor("tree-child-wait-45", regexp.MustCompile(`job_[A-Za-z0-9_]{10,}`).FindString(lastToolResultText(req.Messages)))
	})
	root := newMessagingRoot(t, adapter, nil)
	if _, err := root.ProcessInput(context.Background(), "start-45", nil); err != nil {
		t.Fatal(err)
	}
	owner := root.subagents.sessions()[0]
	sub := root.getSub(owner.ID())
	waitForCondition(t, 3*time.Second, "tree parent yield", func() bool { sub.mu.Lock(); defer sub.mu.Unlock(); return !sub.running })
	releaseChild()
	adapter.waitForRequests(t, "TREE-WAIT-45", 3)
	child := owner.subagents.sessions()[0]
	childSub := owner.getSub(child.ID())
	waitForCondition(t, 3*time.Second, "both tree activations yielded", func() bool {
		sub.mu.Lock()
		parentRunning := sub.running
		sub.mu.Unlock()
		childSub.mu.Lock()
		defer childSub.mu.Unlock()
		return !parentRunning && !childSub.running
	})
	copyDir := copyWaitingState(t, root.stateDir)
	entered, finish := make(chan struct{}), make(chan struct{})
	releaseFinish := sync.OnceFunc(func() { close(finish) })
	defer releaseFinish()
	adapter.script("TREE-CHILD-45", func(llm.Request) llm.Response {
		close(entered)
		<-finish
		return finalResponse("restored-child-result-45")
	})
	restored := restoreWaitingCopy(t, root, copyDir)
	// The server installs the outer input owner after bootstrap returns.
	restored.SetNotifyFunc(func() {})
	awaitSignal(t, entered, "cold addressed descendant resume")
	if requests := len(adapter.requestsFor("TREE-WAIT-45")); requests != 3 {
		t.Fatalf("ancestor model ran to restore child, requests=%d want3", requests)
	}
	for _, id := range []string{owner.owningDelegateID, child.owningDelegateID} {
		run := delegateAggregateSnapshot(t, restored.delegateController, id)
		if run.Generation != 1 || !run.CurrentRunOpen || run.LatestOutcome != nil {
			t.Fatalf("restored open wait=%+v", run)
		}
	}
	releaseFinish()
	waitForCondition(t, 3*time.Second, "cold child result resumes ancestor", func() bool {
		return !delegateAggregateSnapshot(t, restored.delegateController, owner.owningDelegateID).CurrentRunOpen
	})
	starts := map[string]int{}
	for _, event := range publicationEvents(t, restored) {
		if event.RunStarted != nil {
			starts[event.DelegateID]++
		}
	}
	if starts[owner.owningDelegateID] != 1 || starts[child.owningDelegateID] != 1 {
		t.Fatalf("replacement RunStarted events=%v", starts)
	}
}
