package server

import (
	"bufio"
	"context"
	"io"
	"os"
	"sort"
	"strings"

	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
	"primeradiant.com/evener/internal/appserver"
)

// transcriptHeaderReadBufferBytes bounds the prefetch for an ordinary header
// line. The parser stops at that line; keeping this buffer finite prevents a
// large historical transcript from crossing the reader boundary merely because
// identity validation needs its header. Oversized headers are still governed
// by the maxLineBytes limit passed to transcript.ReadLine.
const transcriptHeaderReadBufferBytes = 64 * 1024

func (s *Server) registerAppWireHandlers() {
	router := s.appServer.Router()
	s.registerSessionActivityHandlers()
	appserver.HandleTyped(router, appwire.MethodThreadList, s.handleAppThreadList)
	appserver.HandleThreadHistory(router, s.handleAppThreadRead, s.handleAppThreadTurnsList)
	appserver.HandleTyped(router, appwire.MethodThreadUnsubscribe, s.handleAppThreadUnsubscribe)
	appserver.HandleTyped(router, appwire.MethodTurnStart, s.handleAppTurnStart)
	appserver.HandleTyped(router, appwire.MethodTurnSteer, s.handleAppTurnSteer)
	appserver.HandleTyped(router, appwire.MethodEvenerSandboxEscalationResolve, s.handleAppSandboxEscalationResolve)
	appserver.HandleTyped(router, appwire.MethodEvenerDelegateStop, s.handleAppDelegateStop)
	appserver.HandleTyped(router, appwire.MethodEvenerJobsStop, s.handleAppJobsStop)
	appserver.HandleTyped(router, appwire.MethodTurnInterrupt, s.handleAppTurnInterrupt)
	appserver.HandleTyped(router, appwire.MethodTurnQueue, s.handleAppTurnQueue)
	appserver.HandleTyped(router, appwire.MethodTurnDrainAsSteer, s.handleAppTurnDrainAsSteer)
	appserver.HandleTyped(router, appwire.MethodTurnPromoteQueuedAsSteer, s.handleAppTurnPromoteQueuedAsSteer)
	appserver.HandleTyped(router, appwire.MethodTurnCancelQueued, s.handleAppTurnCancelQueued)
	appserver.HandleTyped(router, appwire.MethodGoalSet, s.handleAppGoalSet)
	appserver.HandleTyped(router, appwire.MethodNotesHumanSet, s.handleAppNotesHumanSet)
	appserver.HandleTyped(router, appwire.MethodUrlsRemove, s.handleAppUrlsRemove)
	appserver.HandleTyped(router, appwire.MethodThreadCompactStart, s.handleAppThreadCompactStart)
	appserver.HandleTyped(router, appwire.MethodThreadShutdown, s.handleAppThreadShutdown)
	appserver.HandleTyped(router, appwire.MethodEvenerDaemonStatus, s.handleAppDaemonStatus)
	appserver.HandleTyped(router, appwire.MethodEvenerDaemonRetire, s.handleAppDaemonRetire)
	appserver.HandleTyped(router, appwire.MethodEvenerDaemonIdleTimeoutSet, s.handleAppDaemonIdleTimeoutSet)
	appserver.HandleTyped(router, appwire.MethodThreadClear, s.handleAppThreadClear)
	appserver.HandleTyped(router, appwire.MethodThreadModelSet, s.handleAppThreadModelSet)
	appserver.HandleTyped(router, appwire.MethodThreadVisionModelSet, s.handleAppThreadVisionModelSet)
	appserver.HandleTyped(router, appwire.MethodEvenerThreadNameSet, s.handleAppThreadNameSet)
	appserver.HandleTyped(router, appwire.MethodThreadReasoningEffortSet, s.handleAppThreadReasoningEffortSet)
	appserver.HandleTyped(router, appwire.MethodEvenerTasksList, s.handleAppTasksList)
	appserver.HandleTyped(router, appwire.MethodEvenerJobsList, s.handleAppJobsList)
	appserver.HandleTyped(router, appwire.MethodEvenerJobsOutput, s.handleAppJobsOutput)
	appserver.HandleTyped(router, appwire.MethodEvenerJobsGet, s.handleAppJobsGet)
	appserver.HandleTyped(router, appwire.MethodModelList, s.handleAppModelList)
}

func (s *Server) handleAppThreadList(_ context.Context, params appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	diagnostics := appDiagnosticsFromDetailedStatus
	if params.StatusOnly {
		diagnostics = appStatusDiagnosticsFromDetailedStatus
	}
	s.mu.RLock()
	data := []appwire.Thread{s.appThreadWithDiagnosticsLocked(diagnostics)}
	// Only the list carries the meter: a thread/read snapshot would hand a
	// subscriber a value no notification ever updates.
	data[0].Evener.Activity = s.appActivity.snapshot()
	ids := make([]string, 0, len(s.appDescendants))
	for id := range s.appDescendants {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		projection := s.appDescendants[id]
		thread := projection.thread
		thread.Evener.ActiveTurnID = s.descendantRunningTurnID(id)
		data = append(data, thread)
	}
	s.mu.RUnlock()
	// Sample every row's own live watches AFTER releasing s.mu. The resolver
	// reaches into agent session state (job manager + subagent manager locks),
	// while a descendant event being projected takes s.mu from the agent side;
	// holding s.mu across the resolver would invert that order. data[0] is the
	// root: no event refreshes its diagnostics facet when a watch is armed or
	// cleared, so its own envelope can lag until a turn boundary, and the live
	// sample is what keeps the root row current.
	//
	// The page is sampled in one call: the resolver answers every row ID from a
	// single walk of the live tree, where resolving each row on its own searched
	// that tree once per row.
	s.attachLiveWatches(data)
	s.attachSubagentTally(&data[0])
	return appwire.ThreadListResponse{Data: data}, nil
}

func (s *Server) handleAppThreadRead(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	if err := appwire.ValidateThreadReadParams(params); err != nil {
		return appwire.ThreadReadResponse{}, s.readError(err, s.appThreadIDForRead(params))
	}
	if err := params.ValidateAnswerContinuation(); err != nil {
		return appwire.ThreadReadResponse{}, err
	}
	if params.AnswerContinuation != nil {
		cut := s.appThreadReadCutForTarget(params, s.appThreadIDForRead(params))
		if !cut.found || cut.history == nil {
			return appwire.ThreadReadResponse{}, appwire.Unavailable("answer continuation history unavailable")
		}
		verdict, snapshot, err := cut.history.answerContinuation(ctx, params)
		if err != nil {
			return appwire.ThreadReadResponse{}, appwire.WithHistoryReadIdentity(appserver.WireError(err), cut.response.BootGeneration, cut.response.Epoch)
		}
		current := s.appThreadReadCutForTarget(params, s.appThreadIDForRead(params))
		if !current.found || current.history != cut.history || current.response.Thread.Evener.Ref != cut.response.Thread.Evener.Ref || current.response.Thread.Evener.InstanceID != cut.response.Thread.Evener.InstanceID || current.response.BootGeneration != cut.response.BootGeneration || current.response.Epoch != cut.response.Epoch {
			return appwire.ThreadReadResponse{}, appwire.Unavailable("answer continuation target changed during read")
		}
		response := cut.response
		response.Thread.Turns = nil
		response.AnswerContinuation, response.Snapshot = &verdict, &snapshot
		return response, nil
	}
	if !params.Subscribe {
		return s.appThreadReadSnapshotChecked(params)
	}
	var threadID string
	var cut appThreadReadCut
	captured := appserver.CaptureSubscription(
		ctx,
		params.ReplaceSubscription,
		nil,
		s.appNotifier.CurrentSequence,
		func() bool {
			cut = s.appThreadReadCutForTarget(params, threadID)
			return true
		},
		func() appserver.SubscriptionTarget {
			var key string
			threadID, key = s.appReadTarget(params)
			if threadID == "" {
				return appserver.SubscriptionTarget{}
			}
			return appserver.SubscriptionTarget{ThreadID: key, LifecycleKey: key}
		},
	)
	if !captured {
		return appwire.ThreadReadResponse{}, s.readError(appwire.SessionUnavailable("thread subscription is unavailable"), threadID)
	}
	if threadReadAfterCutHook != nil {
		threadReadAfterCutHook()
	}
	return s.finishAppThreadRead(params, cut)
}

// threadReadAfterCutHook, when set, runs in a subscribing thread/read after
// its subscription cut and before it projects history. Test seam only; nil
// in production.
var threadReadAfterCutHook func()

// appThreadReadCut is what a thread read takes inside the subscription cut:
// the thread envelope, and the thread's overlay and resync epoch. Its history
// is projected after the cut (finishAppThreadRead).
type appThreadReadCut struct {
	response appwire.ThreadReadResponse
	found    bool
	history  *threadHistory
	capture  historyCapture
}

// appThreadReadSnapshot answers a read with no subscription.
func (s *Server) appThreadReadSnapshot(params appwire.ThreadReadParams) appwire.ThreadReadResponse {
	response, _ := s.appThreadReadSnapshotChecked(params)
	return response
}

func (s *Server) appThreadReadSnapshotChecked(params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	return s.finishAppThreadRead(params, s.appThreadReadCutForTarget(params, s.appThreadIDForRead(params)))
}

// appThreadReadCutForTarget captures the exact target selected under the
// subscription projection gate, without resolving mutable request aliases. It
// runs under the subscription cut, so it does no file I/O: history is read
// from the transcript after the cut, projected up to the recorded length, and
// merged with the overlay captured here. Nothing announced before the cut is
// missing from that projection, because an entry is recorded before it is
// announced.
//
// It stays under the cut for a second reason, which is why sampling the session
// envelope before CaptureSubscription would be wrong however tempting: queue,
// status, active turn, escalations and the failure count are each announced by
// a notification as well as carried here. A session writes its state before
// emitting the event that announces it, so a sample taken inside the gate can
// only lead its notifications. A sample taken outside can lag one -- an event
// emitted after the sample and committed before the cut is discarded on release
// as already-reflected, and the state it announced then never reaches the
// client at all.
//
// Anything called from here must therefore be cheap AND must not block on a
// lock another component holds across disk I/O. That is now structural rather
// than a rule to remember: the envelope is materialized (server/thread_envelope.go),
// this is a struct copy, the overlay capture copies in-memory state under a
// leaf lock, and the Server holds no callback that could reach a session from
// a read path.
func (s *Server) appThreadReadCutForTarget(params appwire.ThreadReadParams, threadID string) appThreadReadCut {
	thread, ok := s.appThreadForID(threadID)
	if !ok {
		return appThreadReadCut{}
	}
	// Descendant projections carry events and transcript windows, but not the
	// addressed child's durable queue and mutation receipts.
	thread.Evener.MutationStateAuthoritative = threadID == s.appProjectionThreadID()
	cut := appThreadReadCut{
		response: appwire.ThreadReadResponse{Thread: thread, RequestGeneration: params.RequestGeneration, BootGeneration: s.bootGenerationFor(threadID)},
		found:    true,
		history:  s.appHistoryForID(threadID),
	}
	if cut.history != nil {
		cut.capture = cut.history.capture()
		cut.response.Epoch = cut.capture.epoch
		cut.response.BootGeneration = cut.history.bootGeneration
	}
	return cut
}

// finishAppThreadRead projects the cut's thread history after the cut: the
// latest window up to the recorded length, and the captured overlay minus
// what that projection covers.
func (s *Server) finishAppThreadRead(params appwire.ThreadReadParams, cut appThreadReadCut) (appwire.ThreadReadResponse, error) {
	if !cut.found {
		return appwire.ThreadReadResponse{}, nil
	}
	response := cut.response
	if params.IncludeTurns && cut.history != nil {
		turns, olderCursor, snapshot, latestExecutionTurnID, overlay, err := cut.history.latest(cut.capture, cut.history.ref, params.ItemLimit)
		if err != nil {
			return appwire.ThreadReadResponse{}, appwire.WithHistoryReadIdentity(appserver.WireError(err), response.BootGeneration, response.Epoch)
		}
		response.Thread.Turns = turns
		response.OlderCursor = olderCursor
		response.Snapshot = &snapshot
		response.LatestExecutionTurnID = latestExecutionTurnID
		response.Overlay = overlay
	}
	if err := appwire.ValidateThreadReadItemResponse(response); err != nil {
		if cut.history != nil {
			return appwire.ThreadReadResponse{}, appwire.WithHistoryReadIdentity(appserver.WireError(err), response.BootGeneration, response.Epoch)
		}
		return appwire.ThreadReadResponse{}, appwire.WithReadBootGeneration(appserver.WireError(err), response.BootGeneration)
	}
	return response, nil
}

// handleAppThreadUnsubscribe drops the calling connection's subscription to
// this daemon's thread. It resolves the thread identity exactly as
// handleAppThreadRead does, so an unsubscribe names the same registry key the
// subscribe created — including through a replace/clear identity swap, where
// appNotificationTarget's stable ref is the key subscribers were registered
// under. A ref that no longer resolves (an identity swap moved the stable ref
// on, an old pre-swap ref) still tries the raw identities the client named:
// the registry keys a subscribe could have used are exactly the stable-ref
// form and the bare thread ID, and Unsubscribe is idempotent, so trying both
// costs nothing and leaks nothing.
func (s *Server) handleAppThreadUnsubscribe(ctx context.Context, params appwire.ThreadUnsubscribeParams) (appwire.EmptyResponse, error) {
	threadID, target := s.appReadTarget(appwire.ThreadReadParams{ThreadID: params.ThreadID, Ref: params.Ref})
	if threadID == "" {
		// Teardown finding nothing is a success; still try the raw identities
		// so a pre-swap key does not linger until connection close.
		rawRef := strings.TrimSpace(params.Ref)
		if rawRef != "" {
			appserver.Unsubscribe(ctx, rawRef)
		}
		if bare := strings.TrimSpace(params.ThreadID); bare != "" {
			appserver.Unsubscribe(ctx, s.appNotificationTarget(bare))
		}
		return appwire.EmptyResponse{}, nil
	}
	appserver.Unsubscribe(ctx, target)
	return appwire.EmptyResponse{}, nil
}

func (s *Server) appThreadIDForRead(params appwire.ThreadReadParams) string {
	threadID, _ := s.appReadTarget(params)
	return threadID
}

// appReadTarget takes one identity snapshot for both the requested thread and
// its admission/delivery key. Ingress must not recompute the key after releasing
// this lock: a same-workspace identity replacement could turn the saved root ID
// into a stale bare key between those two reads.
func (s *Server) appReadTarget(params appwire.ThreadReadParams) (string, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.appReadTargetLocked(params)
}

// appReadTargetLocked requires s.mu and calls no lock-taking helpers.
func (s *Server) appReadTargetLocked(params appwire.ThreadReadParams) (string, string) {
	threadID := strings.TrimSpace(params.ThreadID)
	rawRef := strings.TrimSpace(params.Ref)
	if threadID == "" && rawRef != "" {
		// A Ref was supplied but is either unparseable or names a source this
		// daemon does not serve. Either way it is a caller error, not "no ref
		// given" -- falling through to appProjectionThreadID here would answer
		// with the ROOT thread's content for a request that named something
		// else entirely (ledger #110/#111).
		ref, err := appwire.ParseRef(rawRef)
		if err != nil || ref.SourceID != "local" {
			return "", ""
		}
		if rawRef == s.appRef {
			threadID = s.appThreadID
		} else {
			threadID = ref.ThreadID
		}
	}
	rootID := s.appThreadID
	if rootID == "" {
		rootID = s.status.SessionID
	}
	if threadID == "" {
		threadID = rootID
	}
	if threadID == "" || (threadID != rootID && s.appDescendants[threadID] == nil) {
		return "", ""
	}
	target := threadID
	if threadID == s.appThreadID && s.appRef != "" {
		target = s.appRef
	}
	return threadID, target
}

// appThreadForID returns the already-projected thread for threadID. It is the
// single-thread READ path (appThreadReadSnapshotForTarget), which runs under the
// subscription cut, so it must never reach into agent session state:
// appThreadReadSnapshot's contract is that anything called from here is cheap
// and does not block on a lock another component holds across disk I/O. It
// therefore returns the projection as installed, without the descendant watch
// sample. The list path (handleAppThreadList) is where descendant watches are
// sampled -- after releasing s.mu, outside the cut -- because that is what feeds
// the hub prober and hence the child rows in navigation.
func (s *Server) appThreadForID(threadID string) (appwire.Thread, bool) {
	if threadID == s.appProjectionThreadID() {
		return s.appThread(), true
	}
	s.mu.RLock()
	projection := s.appDescendants[threadID]
	if projection == nil {
		s.mu.RUnlock()
		return appwire.Thread{}, false
	}
	thread := projection.thread
	s.mu.RUnlock()
	thread.Evener.ActiveTurnID = s.descendantRunningTurnID(threadID)
	thread.Evener.Access = cloneThreadAccess(thread.Evener.Access)
	return thread, true
}

// descendantRunningTurnID is a delegate's running execution. Nothing calls
// SetProcessingTurn for a delegate, so its running state is its overlay's,
// from EXECUTION_STARTED/ENDED. A delegate with no history (released) runs
// nothing. The overlay mutex is a leaf, so this is cheap under the cut.
func (s *Server) descendantRunningTurnID(threadID string) string {
	if history := s.appHistories.get(threadID); history != nil {
		return history.overlay.RunningTurnID()
	}
	return ""
}

func (s *Server) appProjectionThreadID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.appThreadID != "" {
		return s.appThreadID
	}
	return s.status.SessionID
}

// transcriptHeader strictly reads the header of the transcript at path. A
// missing file is an error wrapping fs.ErrNotExist.
func transcriptHeader(path string, maxLineBytes int) (transcript.Header, error) {
	file, err := os.Open(path)
	if err != nil {
		return transcript.Header{}, err
	}
	defer file.Close() //nolint:errcheck // read-only file; close error is not actionable
	return transcriptHeaderFromReader(file, maxLineBytes)
}

// transcriptHeaderFromReader reads source's leading header line and nothing
// past its read buffer.
func transcriptHeaderFromReader(source io.Reader, maxLineBytes int) (transcript.Header, error) {
	return transcript.ReadHeader(context.Background(), bufio.NewReaderSize(source, transcriptHeaderReadBufferBytes), maxLineBytes)
}

// handleAppThreadTurnsList pages backward (older) through the thread's
// history from the transcript.
func (s *Server) handleAppThreadTurnsList(_ context.Context, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
	threadID := s.appThreadIDForRead(appwire.ThreadReadParams{ThreadID: params.ThreadID, Ref: params.Ref})
	if err := appwire.ValidateThreadTurnsListParams(params); err != nil {
		return appwire.ThreadTurnsListResponse{}, s.readError(err, threadID)
	}
	params, err := appitempaging.ApplyBefore(params)
	if err != nil {
		return appwire.ThreadTurnsListResponse{}, s.readError(err, threadID)
	}
	if threadID == "" {
		return appwire.ThreadTurnsListResponse{}, s.readError(appwire.SessionUnavailable("thread is unavailable"), threadID)
	}
	history := s.appHistoryForID(threadID)
	if history == nil {
		return appwire.ThreadTurnsListResponse{BootGeneration: s.bootGenerationFor(threadID)}, nil
	}
	epoch := history.Epoch()
	turns, olderCursor, snapshot, err := history.before(history.ref, params)
	if err != nil {
		return appwire.ThreadTurnsListResponse{}, appwire.WithHistoryReadIdentity(appserver.WireError(err), history.bootGeneration, epoch)
	}
	response := appwire.ThreadTurnsListResponse{
		Data:           turns,
		NextCursor:     olderCursor,
		BootGeneration: history.bootGeneration,
		Epoch:          epoch,
		Snapshot:       &snapshot,
	}
	if err := appwire.ValidateThreadTurnsListItemResponse(response); err != nil {
		return appwire.ThreadTurnsListResponse{}, appwire.WithHistoryReadIdentity(appserver.WireError(err), history.bootGeneration, epoch)
	}
	return response, nil
}
