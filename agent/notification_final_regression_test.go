package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

func TestFinalCorrectionSelfWatchWait(t *testing.T) {
	for _, mode := range []struct{ child, persistent bool }{{false, false}, {false, true}, {true, true}} {
		t.Run(fmt.Sprintf("child_%v_persistent_%v", mode.child, mode.persistent), func(t *testing.T) {
			child := mode.child
			clock := agenttest.NewFakeClock()
			a := finiteUpdateAdapter(t, "SELF-WAIT-85")
			key := "root"
			if child {
				key = "SELF-WAIT-85"
				a.finiteScript(key, updateReply(toolCallResponse(llm.ToolCallData{ID: "parent-watch-85", Name: "job_watch", Type: "function", Arguments: []byte(`{"operation":"create","source":"parent","events":["assistant.tool"],"event_filter":{"tool_name":"shell"}}`)})))
			}
			target := make(chan string, 1)
			a.finiteScript(key, updateReply(toolCallResponse(llm.ToolCallData{ID: "self-create-85", Name: "job_watch", Type: "function", Arguments: []byte(`{"operation":"create","source":"self","after_seconds":60}`)})), func(_ context.Context, req llm.Request) (llm.Response, error) {
				id := regexp.MustCompile(`watch_[A-Za-z0-9]{10,}`).FindString(lastToolResultText(req.Messages))
				if id == "" {
					t.Error("real watch creation returned no identity")
				}
				target <- id
				return waitResponseFor("self-wait-85", id), nil
			}, func(_ context.Context, req llm.Request) (llm.Response, error) {
				found := false
				for _, frame := range incidentFrames(req) {
					if frame.tag == "job-notification" && frame.attrs["reason"] == "after" {
						found = true
					}
				}
				if !found {
					t.Error("resumed request omitted timer firing")
				}
				return finalResponse("useful-self-result-85"), nil
			})
			if child {
				a.finiteScript("root", updateReply(toolCallResponse(llm.ToolCallData{ID: "self-owner-85", Name: "delegate", Type: "function", Arguments: []byte(`{"prompt":"SELF-WAIT-85","watch_parent":true}`)})), updateReply(finalResponse("root-result-85")))
			}
			root := newMessagingRoot(t, a, func(cfg *SessionConfig) {
				cfg.clock = clock
				if !mode.persistent {
					cfg.StateDir = ""
				}
			})
			wakes := make(chan struct{}, 1)
			root.SetNotifyFunc(func() {
				select {
				case wakes <- struct{}{}:
				default:
				}
			})
			if _, err := root.ProcessInput(context.Background(), "start-self-85", nil); err != nil {
				t.Fatal(err)
			}
			var id string
			select {
			case id = <-target:
			case <-time.After(3 * time.Second):
				t.Fatal("script did not reach real timer wait")
			}
			receiver := root
			if child {
				receiver = root.subagents.sessions()[0]
				sub := root.getSub(receiver.ID())
				waitForCondition(t, 3*time.Second, "self timer yielded", func() bool { sub.mu.Lock(); defer sub.mu.Unlock(); return !sub.running })
			}
			if err := receiver.validateWaitTarget(context.Background(), "watch:"+id); err != nil {
				t.Fatal(err)
			}
			work, err := receiver.outstandingDelegateWork()
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(work.Watches, id) {
				t.Fatalf("local watch absent from report inventory: %v", work.Watches)
			}
			if child {
				var parentWatch string
				for _, req := range a.requestsFor(key) {
					for _, message := range req.Messages {
						for _, part := range message.Content {
							if result := part.ToolResult; result != nil && result.ToolCallID == "parent-watch-85" {
								parentWatch = regexp.MustCompile(`watch_[A-Za-z0-9]{10,}`).FindString(fmt.Sprint(result.Content))
							}
						}
					}
				}
				if parentWatch == "" || !slices.Contains(work.Watches, parentWatch) {
					t.Fatalf("parent-source watch not owned by observer: id=%s inventory=%v", parentWatch, work.Watches)
				}
				if ids := root.receiverWatchIDs(); slices.Contains(ids, id) || slices.Contains(ids, parentWatch) {
					t.Fatalf("root adopted foreign implicit or explicit receiver watches: %v", ids)
				}
				if err := root.validateWaitTarget(context.Background(), id); err == nil {
					t.Fatal("root authorized a child's self watch")
				}
				// Register real source configurations for both explicit receiver
				// shapes. A session-keyed route is valid, a mismatched delegate is not.
				for _, delegate := range []string{"", "dlg_foreign_85"} {
					registered, err := root.jobManager.configureWatch(watchArgs{Operation: "create", Source: "self", Target: runtimeMessageAliasCaller, ReceiverSessionID: receiver.ID(), ReceiverDelegateID: delegate, Events: []string{"assistant.tool"}, EventFilter: &watchEventFilter{ToolName: "shell"}})
					if err != nil {
						t.Fatal(err)
					}
					if got := slices.Contains(receiver.receiverWatchIDs(), registered.WatchID); got != (delegate == "") {
						t.Fatalf("explicit receiver delegate=%q included=%v", delegate, got)
					}
					if slices.Contains(root.receiverWatchIDs(), registered.WatchID) {
						t.Fatal("own holder overrode an explicit foreign receiver")
					}
				}
			}
			if mode.persistent {
				cp, err := readWaitCheckpoint(transcriptPath(root.stateDir, receiver.ID()), receiver.ID())
				if err != nil || cp == nil || cp.WaitingOn != id {
					t.Fatalf("accepted wait checkpoint=%+v err=%v", cp, err)
				}
			} else {
				receiver.mu.Lock()
				report := receiver.comm
				receiver.mu.Unlock()
				if !report.called || report.endReason != "waiting_on_work" || report.waitingOn != id {
					t.Fatalf("lifetime-local wait not accepted: %+v", report)
				}
			}
			clock.BlockUntil(1)
			clock.Advance(time.Minute)
			clock.Drain()
			if child {
				waitForCondition(t, 3*time.Second, "self timer resumed and settled", func() bool {
					return !delegateAggregateSnapshot(t, root.delegateController, receiver.owningDelegateID).CurrentRunOpen
				})
				root.sendersWG.Wait()
			} else {
				awaitSignal(t, wakes, "timer readiness")
				if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
					t.Fatal(err)
				}
			}
			wantRequests := 3
			if child {
				wantRequests++
			}
			if got := len(a.requestsFor(key)); got != wantRequests {
				t.Fatalf("requests=%d want%d", got, wantRequests)
			}
			assertNotificationPending(t, receiver, []string{})
		})
	}
}

func TestFinalCorrectionNonpersistentWait(t *testing.T) {
	for _, shell := range []bool{false, true} {
		t.Run(fmt.Sprintf("shell_%v", shell), func(t *testing.T) {
			var target string
			resumed := false
			s := newSession(t, withoutGitSnapshot(), withSteps(func(llm.Request) llm.Response { return finalResponse("initialized") }, func(llm.Request) llm.Response { return waitResponseFor("ephemeral-wait-85", target) }, func(req llm.Request) llm.Response {
				resumed = true
				if shell && !requestContainsText(req, target) {
					t.Error("resumed provider request omitted owned shell identity")
				}
				return finalResponse("useful-after-wait-85")
			}))
			if _, err := s.ProcessInput(context.Background(), "initialize", nil); err != nil {
				t.Fatal(err)
			}
			if s.stateDir != "" || s.attachedTranscript() != nil {
				t.Fatal("fixture is persistent")
			}
			wakes := make(chan struct{}, 1)
			s.SetNotifyFunc(func() {
				select {
				case wakes <- struct{}{}:
				default:
				}
			})
			if shell {
				var release func()
				target, release = startControlledBackgroundShell(t, s, "ephemeral-shell-85")
				defer release()
				if _, err := s.ProcessInput(context.Background(), "wait", nil); err != nil {
					t.Fatal(err)
				}
				release()
				awaitSignal(t, wakes, "owned shell completion")
				if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
					t.Fatal(err)
				}
				assertNotificationPending(t, s, []string{})
			} else {
				if _, err := s.ProcessInput(context.Background(), "wait", nil); err != nil {
					t.Fatal(err)
				}
				if _, err := s.ProcessInput(context.Background(), "continue useful work", nil); err != nil {
					t.Fatal(err)
				}
			}
			s.mu.Lock()
			accepted := s.execution.failed
			s.mu.Unlock()
			if accepted {
				t.Fatal("successful local wait or resumption marked failed")
			}
			if !resumed {
				t.Fatal("accepted wait did not permit subsequent useful execution")
			}
		})
	}
}

func TestFinalCorrectionNonpersistentSourceCustody(t *testing.T) {
	s := newSession(t, withoutGitSnapshot(), withSteps(func(llm.Request) llm.Response { return finalResponse("initialized") }))
	if _, err := s.ProcessInput(context.Background(), "initialize", nil); err != nil {
		t.Fatal(err)
	}
	jm := s.jobManager
	key := jobstore.WatchSendKey{VisibleSessionID: s.ID(), WatchID: "watch_final_85", WatchTarget: runtimeMessageAliasCaller, ResolvedWatchedIdentity: runtimeMessageAliasCaller, ResolvedSendTo: runtimeMessageAliasCaller, WatchGeneration: "wg_final_85"}
	old := jobstore.WatchSendState{Key: key, DeliveryID: "wd_final_old_85", UpdateSeq: 1, ReceiverSessionID: s.ID(), Frame: "old-accepted-85"}
	newer := old
	newer.DeliveryID = "wd_final_new_85"
	newer.UpdateSeq = 2
	newer.Frame = "new-pending-85"
	for _, state := range []jobstore.WatchSendState{old, newer} {
		copy := state
		if err := jm.appendWatchSendEvents([]jobstore.Event{{Kind: jobstore.EventWatchSendPending, TS: jm.now(), WatchSend: &copy}}); err != nil {
			t.Fatal(err)
		}
	}
	id := stableWatchAttentionID(old)
	if err := s.enqueueNotification(notification{ID: id, Message: llm.User(old.Frame)}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		id   string
		want bool
	}{{id, true}, {stableWatchAttentionID(newer), false}, {"absent-85", false}} {
		accepted, err := s.notificationSourceAccepted(test.id)
		if err != nil || accepted != test.want {
			t.Errorf("custody id=%s accepted=%v want=%v err=%v", test.id, accepted, test.want, err)
		}
	}
	fold, err := jm.store.LoadWatchSends()
	if err != nil {
		t.Fatal(err)
	}
	if len(fold.Superseded) != 1 {
		t.Fatalf("superseded cut=%+v", fold)
	}
	if err := jm.settleSupersededWatchSends(fold.Superseded); err != nil {
		t.Fatal(err)
	}
	if err := jm.settleSupersededWatchSends(fold.Superseded); err != nil {
		t.Fatal(err)
	}
	fresh, err := jobstore.Open(filepath.Join(jm.dir, "jobs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	reloaded, err := fresh.LoadWatchSends()
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Superseded) != 0 || len(reloaded.Pending) != 1 || !jm.watchSendDelivered(old.DeliveryID) {
		t.Fatalf("unsettled or lost source cursor: %+v", reloaded)
	}
	if !reflect.DeepEqual(reloaded.Pending[key], &newer) {
		t.Fatalf("newer cursor changed: %+v", reloaded.Pending)
	}
	if got := len(producerSourceEvents(t, jm, jobstore.EventWatchSendDelivered)); got != 1 {
		t.Fatalf("predecessor acknowledgements=%d want1", got)
	}
	assertNotificationPending(t, s, []string{id})
	s.attentionMu.Lock()
	f, err := s.delegateAttentionFoldLocked()
	s.attentionMu.Unlock()
	if err != nil || len(f.observed) != 0 || len(f.resolutions) != 0 {
		t.Fatalf("source settlement invented observation: %+v err=%v", f, err)
	}
}

// A configured store never falls back to lifetime-local custody, including a
// readable but retained-unsynced acceptance. Recovery uses the same writer rail.
func TestFinalCorrectionConfiguredCustodyFailure(t *testing.T) {
	for _, mode := range []string{"missing", "closed", "retained_unsynced"} {
		t.Run(mode, func(t *testing.T) {
			s := newSession(t, withoutGitSnapshot(), withClock(agenttest.NewFakeClock()), withConfig(SessionConfig{StateDir: t.TempDir()}), withSteps(func(llm.Request) llm.Response { return finalResponse("initialized-85") }))
			if _, err := s.ProcessInput(context.Background(), "initialize", nil); err != nil {
				t.Fatal(err)
			}
			const id = "configured-custody-85"
			writer := s.attachedTranscript()
			var recoverWriter func()
			if mode == "retained_unsynced" {
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				fs := &waitCheckpointFaultFS{Fs: afero.NewOsFs()}
				var err error
				writer, _, err = transcript.OpenWriterForSessionWithFS(fs, transcriptPath(s.stateDir, s.ID()), s.ID())
				if err != nil {
					t.Fatal(err)
				}
				s.attachTranscript(writer)
				fs.fail = true
				err = s.enqueueNotification(notification{ID: id, Message: llm.User("configured-payload-85")})
				if !errors.Is(err, transcript.ErrRetainedUnsynced) {
					t.Fatalf("retained acceptance error=%v", err)
				}
				recoverWriter = func() { fs.fail = false }
			} else {
				observationEnqueue(t, s, id, "configured-payload-85")
				if mode == "missing" {
					s.attachTranscript(nil)
					recoverWriter = func() { s.attachTranscript(writer) }
				} else {
					if err := writer.Close(); err != nil {
						t.Fatal(err)
					}
					recoverWriter = func() {
						w, _, err := transcript.OpenWriterForSession(transcriptPath(s.stateDir, s.ID()), s.ID())
						if err != nil {
							t.Fatal(err)
						}
						s.attachTranscript(w)
					}
				}
			}
			for _, sourceID := range []string{id, "absent-85"} {
				accepted, err := s.notificationSourceAccepted(sourceID)
				if accepted || err == nil {
					t.Fatalf("configured %s accepted=%v err=%v", mode, accepted, err)
				}
			}
			recoverWriter()
			for _, test := range []struct {
				id   string
				want bool
			}{{id, true}, {"absent-85", false}} {
				accepted, err := s.notificationSourceAccepted(test.id)
				if err != nil || accepted != test.want {
					t.Fatalf("recovered custody=%v want=%v err=%v", accepted, test.want, err)
				}
			}
			assertNotificationPending(t, s, []string{id})
			data, err := readTranscriptFull(transcriptPath(s.stateDir, s.ID()), "")
			if err != nil {
				t.Fatal(err)
			}
			accepted := 0
			for _, e := range data.Entries {
				if e.Turn.AttentionID == id {
					accepted++
				}
				if slices.Contains(e.Turn.ObservedNotificationIDs, id) || e.Turn.AttentionResolution != nil && e.Turn.AttentionResolution.AttentionID == id {
					t.Fatal("custody query invented observation or resolution")
				}
			}
			if accepted != 1 {
				t.Fatalf("physical acceptances=%d want1", accepted)
			}
		})
	}
}
