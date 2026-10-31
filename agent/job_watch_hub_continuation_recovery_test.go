package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/transcriptindex"
	"primeradiant.com/evener/llm"
)

// The scripted boundary is the external hub peer, not watch recovery. Successful
// verdicts traverse real persisted answer inputs with the production index.
type continuationRecoveryPeer struct {
	invoke func(context.Context, appwire.SessionOperationRequest, int) (appwire.SessionOperationResponse, error)
	calls  atomic.Int32
}

func (*continuationRecoveryPeer) State(context.Context) (appwire.ManagerView, error) {
	return appwire.ManagerView{Ref: "local:manager", InstanceID: "manager-instance", Manager: true}, nil
}
func (*continuationRecoveryPeer) Watch(context.Context, appwire.SessionWatchRequest) (<-chan appwire.SessionWatchUpdate, error) {
	panic("continuation recovery must not re-register the subscription")
}
func (p *continuationRecoveryPeer) Invoke(ctx context.Context, r appwire.SessionOperationRequest) (appwire.SessionOperationResponse, error) {
	return p.invoke(ctx, r, int(p.calls.Add(1)))
}

type continuationRecoveryFixture struct {
	root              *Session
	id                string
	initial, terminal appwire.SessionWatchUpdate
	updates           chan appwire.SessionWatchUpdate
	peer              *continuationRecoveryPeer
	index             *transcriptindex.Index
	done              chan struct{}
}

func newContinuationRecoveryFixture(t *testing.T, replay bool) *continuationRecoveryFixture {
	t.Helper()
	root := newMessagingRoot(t, newMessagingAdapter(), func(c *SessionConfig) { c.Manager = true })
	path := filepath.Join(t.TempDir(), "worker.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []schema.Turn{
		{TurnID: "A", TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("A")},
		{TurnID: "B", TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("B"), AnswerToTurnIDs: []string{"A"}},
		{TurnID: "C", TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("C"), AnswerToTurnIDs: []string{"B"}},
		{TurnID: "C", Kind: schema.TurnCommunicate, CausalKnown: true, Communicate: &schema.CommunicateInfo{CallID: "terminal", Message: "result", EndTurn: true, EndReason: "done"}},
		{TurnID: "C", Kind: schema.TurnToolResults, Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "terminal", Name: "communicate", Content: `{"accepted":true,"end_turn":true}`}}}}},
	} {
		e.Format = schema.TurnFormatIdentity
		if err := w.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	index, err := transcriptindex.Open(path, filepath.Join(t.TempDir(), "index"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = index.Close() })
	window, err := index.Latest(20)
	if err != nil {
		t.Fatal(err)
	}
	scope := appwire.SessionObservationMetadata{Ref: "local:worker", InstanceID: "instance", GenerationID: "7@worker"}
	snapshot := &appwire.ThreadReadResponse{BootGeneration: scope.GenerationID, Epoch: 3, Snapshot: &appwire.SnapshotIdentity{Incarnation: window.Incarnation, Length: window.Length}, Thread: appwire.Thread{Evener: appwire.EvenerThread{Ref: scope.Ref, InstanceID: scope.InstanceID, ActiveTurnID: "A"}}}
	var winner appwire.ThreadItem
	for _, c := range window.Candidates {
		if c.Item.Observation != nil && c.Item.Observation.TerminalAccepted != nil && *c.Item.Observation.TerminalAccepted {
			winner = c.Item
		}
	}
	if winner.Observation == nil {
		t.Fatal("persisted terminal not projected")
	}
	m := *winner.Observation
	m.Ref, m.InstanceID, m.GenerationID, m.EntryID = scope.Ref, scope.InstanceID, scope.GenerationID, winner.TranscriptKey
	f := &continuationRecoveryFixture{root: root, initial: appwire.SessionWatchUpdate{Metadata: scope, Snapshot: snapshot}, terminal: appwire.SessionWatchUpdate{Event: "communicate", Metadata: m}, updates: make(chan appwire.SessionWatchUpdate, 8), index: index, done: make(chan struct{})}
	if replay {
		snapshot.Thread.Turns = []appwire.Turn{{ID: "C", Items: []appwire.ThreadItem{winner}}}
	} else {
		f.updates <- f.terminal
	}
	watch, err := root.jobManager.configureHubWatch(scope.Ref, scope.InstanceID, scope.GenerationID, "local:manager", []string{"question"}, true)
	if err != nil {
		t.Fatal(err)
	}
	f.id = watch.WatchID
	f.peer = &continuationRecoveryPeer{}
	f.peer.invoke = func(ctx context.Context, r appwire.SessionOperationRequest, _ int) (appwire.SessionOperationResponse, error) {
		return f.verdict(ctx, r)
	}
	root.SetHubClient(f.peer)
	return f
}
func (f *continuationRecoveryFixture) verdict(ctx context.Context, r appwire.SessionOperationRequest) (appwire.SessionOperationResponse, error) {
	q := r.Read.AnswerContinuation
	matches, cut, err := f.index.AnswerContinuation(ctx, q.AncestorTurnID, q.CandidateTurnID, r.Read.HeldSnapshot)
	if err != nil {
		return appwire.SessionOperationResponse{}, err
	}
	read := *f.initial.Snapshot
	read.Snapshot = &cut
	read.Thread.Turns = nil // No later snapshot can supply the failed candidate.
	read.AnswerContinuation = &appwire.AnswerContinuationVerdict{AncestorTurnID: q.AncestorTurnID, CandidateTurnID: q.CandidateTurnID, Matches: matches}
	return appwire.SessionOperationResponse{Read: &read}, nil
}
func (f *continuationRecoveryFixture) start(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	if !f.root.installHubWatchSubscription(f.root.jobManager, f.id, cancel) {
		t.Fatal("subscription not installed")
	}
	t.Cleanup(func() { cancel(); <-f.done })
	go func() {
		defer close(f.done)
		f.root.consumeHubWatch(ctx, f.id, appwire.SessionWatchRequest{Ref: "local:worker", Completion: true}, "local:manager", "manager-instance", f.updates, f.initial)
	}()
}
func (f *continuationRecoveryFixture) wait(t *testing.T) {
	t.Helper()
	select {
	case <-f.done:
	case <-t.Context().Done():
		t.Fatal("consumer did not finish")
	}
}
func (f *continuationRecoveryFixture) counts(t *testing.T) (completed, ends, interruptions int) {
	t.Helper()
	completionSends := 0
	for _, event := range loadJobStoreEvents(t, f.root.jobManager) {
		if event.WatchID == f.id && event.Watch != nil && event.Watch.EndReason == "completed" {
			completed++
		}
		if event.Kind == jobstore.EventWatchSendPending && event.WatchSend != nil && event.WatchSend.Key.WatchID == f.id {
			if event.WatchSend.TriggerIdentity == hubObservationIdentity(f.terminal.Metadata)+":communicate:completed" {
				completionSends++
			}
			if event.WatchSend.EndNotice {
				ends++
			}
			if event.WatchSend.TriggerIdentity == "local:worker#interruption" {
				interruptions++
			}
		}
	}
	if completionSends != completed {
		t.Fatalf("durable completions=%d completion sends=%d", completed, completionSends)
	}
	return
}

// Mutation caught: either consumer error arm ends the watch after the first
// continuation call, or recovery discards the failed candidate and re-reads latest.
func TestManagerWatchContinuationRecoveryTransient(t *testing.T) {
	t.Parallel()
	for _, replay := range []bool{true, false} {
		name := "live"
		if replay {
			name = "initial_replay"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newContinuationRecoveryFixture(t, replay)
			f.peer.invoke = func(ctx context.Context, r appwire.SessionOperationRequest, n int) (appwire.SessionOperationResponse, error) {
				if n == 1 {
					return appwire.SessionOperationResponse{}, appwire.InternalError(transcriptindex.ErrLockBusy.Error())
				}
				return f.verdict(ctx, r)
			}
			f.updates <- f.terminal // A duplicate after replay/live evidence must not deliver twice.
			f.start(t)
			f.wait(t)
			completed, ends, interruptions := f.counts(t)
			if completed != 1 || ends != 0 || interruptions != 1 || f.peer.calls.Load() != 2 {
				t.Fatalf("completion=%d end_notices=%d interruptions=%d lookups=%d, want 1/0/1/2", completed, ends, interruptions, f.peer.calls.Load())
			}
		})
	}
}

// Mutation caught: removing the finite limit or ending without a durable notice.
func TestManagerWatchContinuationRecoveryExhaustion(t *testing.T) {
	t.Parallel()
	for _, replay := range []bool{true, false} {
		t.Run(strconv.FormatBool(replay), func(t *testing.T) {
			t.Parallel()
			f := newContinuationRecoveryFixture(t, replay)
			f.peer.invoke = func(context.Context, appwire.SessionOperationRequest, int) (appwire.SessionOperationResponse, error) {
				return appwire.SessionOperationResponse{}, appwire.InternalError(transcriptindex.ErrLockBusy.Error())
			}
			f.start(t)
			f.wait(t)
			completed, ends, interruptions := f.counts(t)
			if completed != 0 || ends != 1 || interruptions != 1 || f.peer.calls.Load() != 5 {
				t.Fatalf("completion=%d end_notices=%d interruptions=%d lookups=%d, want 0/1/1/5", completed, ends, interruptions, f.peer.calls.Load())
			}
		})
	}
}

// Mutation caught: treating the operation deadline as watch cancellation, or
// refusing the typed transient wire errors that the history reader can return.
func TestManagerWatchContinuationRecoveryCallFailures(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"deadline", "locked", "rebuilding", "unavailable", "transport"} {
		for _, replay := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/%v", kind, replay), func(t *testing.T) {
				t.Parallel()
				f := newContinuationRecoveryFixture(t, replay)
				f.peer.invoke = func(ctx context.Context, r appwire.SessionOperationRequest, n int) (appwire.SessionOperationResponse, error) {
					if n > 1 {
						return f.verdict(ctx, r)
					}
					var err error
					switch kind {
					case "deadline":
						<-ctx.Done() // Await the real production operation budget.
						err = ctx.Err()
					case "locked":
						err = appwire.TranscriptIndexLocked()
					case "rebuilding":
						err = appwire.WireError{Code: appwire.CodeInternalError, Data: map[string]any{"evenerErrorInfo": string(appwire.ErrorTranscriptIndexRebuilding)}}
					case "unavailable":
						err = appwire.Unavailable("peer unavailable")
					case "transport":
						err = appwire.TransportFailureError{WireError: appwire.InvalidParams("transport closed")}
					}
					return appwire.SessionOperationResponse{}, err
				}
				f.start(t)
				f.wait(t)
				completed, ends, interruptions := f.counts(t)
				if completed != 1 || ends != 0 || interruptions != 1 || f.peer.calls.Load() != 2 {
					t.Fatalf("completion=%d end_notices=%d interruptions=%d lookups=%d", completed, ends, interruptions, f.peer.calls.Load())
				}
			})
		}
	}
}

// Mutation caught: retrying permanent request/response defects or accepting a
// relaxed fence after a transient call. Every corrupt reply starts as a real
// indexed positive verdict before exactly one evidence field is changed.
func TestManagerWatchContinuationRecoveryPermanent(t *testing.T) {
	t.Parallel()
	for _, defect := range []string{"invalid_params", "invalid_request", "unknown_method", "stale_cut", "history_failed", "missing_read", "missing_verdict", "ancestor", "candidate", "ref", "instance", "generation", "epoch", "missing_snapshot", "incarnation", "truncation"} {
		for _, replay := range []bool{true, false} {
			for _, transientFirst := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/replay_%v/retry_%v", defect, replay, transientFirst), func(t *testing.T) {
					t.Parallel()
					f := newContinuationRecoveryFixture(t, replay)
					f.peer.invoke = func(ctx context.Context, r appwire.SessionOperationRequest, n int) (appwire.SessionOperationResponse, error) {
						if transientFirst && n == 1 {
							return appwire.SessionOperationResponse{}, appwire.InternalError(transcriptindex.ErrLockBusy.Error())
						}
						result, err := f.verdict(ctx, r)
						if err != nil {
							return result, err
						}
						switch defect {
						case "invalid_params":
							err = appwire.InvalidParams("invalid query")
						case "invalid_request":
							err = appwire.InvalidRequest("refused")
						case "unknown_method":
							err = appwire.MethodNotFound("thread/read")
						case "stale_cut":
							err = appwire.TranscriptItemCursorStale()
						case "history_failed":
							err = appwire.WireError{Code: appwire.CodeInternalError, Data: map[string]any{"evenerErrorInfo": string(appwire.ErrorTranscriptHistoryFailed)}}
						case "missing_read":
							result.Read = nil
						case "missing_verdict":
							result.Read.AnswerContinuation = nil
						case "ancestor":
							result.Read.AnswerContinuation.AncestorTurnID = "other"
						case "candidate":
							result.Read.AnswerContinuation.CandidateTurnID = "other"
						case "ref":
							result.Read.Thread.Evener.Ref = "local:other"
						case "instance":
							result.Read.Thread.Evener.InstanceID = "other"
						case "generation":
							result.Read.BootGeneration = "8@worker"
						case "epoch":
							result.Read.Epoch++
						case "missing_snapshot":
							result.Read.Snapshot = nil
						case "incarnation":
							result.Read.Snapshot.Incarnation = "other"
						case "truncation":
							result.Read.Snapshot.Length--
						}
						return result, err
					}
					f.start(t)
					f.wait(t)
					completed, ends, interruptions := f.counts(t)
					wantCalls, wantInterruptions := int32(1), 0
					if transientFirst {
						wantCalls, wantInterruptions = 2, 1
					}
					if completed != 0 || ends != 1 || interruptions != wantInterruptions || f.peer.calls.Load() != wantCalls {
						t.Fatalf("completion=%d end_notices=%d interruptions=%d lookups=%d", completed, ends, interruptions, f.peer.calls.Load())
					}
				})
			}
		}
	}
}

// Mutation caught: recovery outliving cancellation/clear/replacement, or the
// result of an in-flight old lookup completing the replacement registration.
func TestManagerWatchContinuationRecoveryCancellation(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"cancel", "clear", "replace"} {
		for _, replay := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/%v", action, replay), func(t *testing.T) {
				t.Parallel()
				f := newContinuationRecoveryFixture(t, replay)
				entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
				f.peer.invoke = func(ctx context.Context, r appwire.SessionOperationRequest, n int) (appwire.SessionOperationResponse, error) {
					if n == 1 {
						return appwire.SessionOperationResponse{}, appwire.InternalError(transcriptindex.ErrLockBusy.Error())
					}
					close(entered)
					<-ctx.Done()
					close(cancelled)
					<-release // A late successful peer response must remain harmless.
					return f.verdict(t.Context(), r)
				}
				f.start(t)
				t.Cleanup(func() {
					select {
					case <-release:
					default:
						close(release)
					}
				})
				select {
				case <-entered:
				case <-f.done:
					t.Fatal("consumer ended before recovery")
				case <-t.Context().Done():
					t.Fatal("recovery did not start")
				}
				var replacement string
				var next context.Context
				switch action {
				case "cancel":
					f.root.cancelHubWatch(f.id)
				case "clear":
					result := f.root.reg.ExecuteCall(t.Context(), f.root.currentEnv(), llm.ToolCallData{ID: "clear", Name: "job_watch", Arguments: []byte(fmt.Sprintf(`{"operation":"clear","watch_id":%q}`, f.id))})
					if result.IsError {
						t.Fatalf("clear failed: %+v", result)
					}
				case "replace":
					watch, err := f.root.jobManager.configureHubWatch("local:worker", "instance", "7@worker", "local:manager", []string{"approval"}, true)
					if err != nil {
						t.Fatal(err)
					}
					replacement = watch.WatchID
					var stop context.CancelFunc
					next, stop = context.WithCancel(t.Context())
					if !f.root.installHubWatchSubscription(f.root.jobManager, replacement, stop) {
						t.Fatal("replacement subscription not installed")
					}
					t.Cleanup(func() { f.root.cancelHubWatch(replacement) })
					if next.Err() != nil {
						t.Fatal("replacement was canceled")
					}
				}
				select {
				case <-cancelled:
				case <-t.Context().Done():
					t.Fatal("outstanding recovery call not canceled")
				}
				close(release)
				f.wait(t)
				completed, ends, interruptions := f.counts(t)
				if completed != 0 || ends != 0 || interruptions != 1 || f.peer.calls.Load() != 2 {
					t.Fatalf("completion=%d end_notices=%d interruptions=%d lookups=%d", completed, ends, interruptions, f.peer.calls.Load())
				}
				if action != "cancel" && managerHubWatchPresent(f.root.jobManager, f.id) {
					t.Fatal("retired old watch remains active")
				}
				if replacement != "" {
					jm := f.root.jobManager
					jm.mu.Lock()
					_, cfg, present := jm.watchConfigByIDLocked(replacement)
					untouched := present && !cfg.hubTurnBound && cfg.hubTurnID == "" && !cfg.hubSuspended && len(cfg.hubSeen) == 0 && !cfg.rejectingDelivery
					jm.mu.Unlock()
					if !untouched || next.Err() != nil {
						t.Fatal("old consumer mutated or canceled replacement")
					}
				}
			})
		}
	}
}

// Mutation caught: dropping pending sends on suspension or clearing the entry
// dedup map while retrying a snapshot that also replays an earlier question.
func TestManagerWatchContinuationRecoveryPendingDedup(t *testing.T) {
	t.Parallel()
	for _, replay := range []bool{true, false} {
		t.Run(strconv.FormatBool(replay), func(t *testing.T) {
			t.Parallel()
			f := newContinuationRecoveryFixture(t, replay)
			question := f.initial.Metadata
			question.EntryID, question.CausalKnown = "question:A", true
			if err := f.root.jobManager.onHubObservation(f.id, appwire.SessionWatchUpdate{Event: "question", Metadata: question}); err != nil {
				t.Fatal(err)
			}
			before := loadWatchSendRecord(t, f.root.jobManager)
			if len(before.Pending) != 1 {
				t.Fatalf("expected one pending question, got %d", len(before.Pending))
			}
			f.initial.Snapshot.Thread.Evener.AskPending = true
			f.peer.invoke = func(ctx context.Context, r appwire.SessionOperationRequest, n int) (appwire.SessionOperationResponse, error) {
				if n == 1 {
					return appwire.SessionOperationResponse{}, appwire.InternalError(transcriptindex.ErrLockBusy.Error())
				}
				return f.verdict(ctx, r)
			}
			f.start(t)
			f.wait(t)
			if err := f.root.jobManager.onHubObservation(f.id, f.terminal); err != nil {
				t.Fatal(err)
			}
			after := loadWatchSendRecord(t, f.root.jobManager)
			for key, pending := range before.Pending {
				if got := after.Pending[key]; got == nil || got.DeliveryID != pending.DeliveryID || got.Key != pending.Key {
					t.Fatalf("pending question identity lost: before=%+v after=%+v", pending, got)
				}
			}
			completed, ends, interruptions := f.counts(t)
			if completed != 1 || ends != 0 || interruptions != 1 || len(after.Pending) != 3 {
				t.Fatalf("completion=%d end_notices=%d interruptions=%d pending=%d", completed, ends, interruptions, len(after.Pending))
			}
		})
	}
}
