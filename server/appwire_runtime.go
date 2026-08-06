package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/internal/appprojector"
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/llm"
)

func (s *Server) AppServer() *appserver.Server {
	return s.appServer
}

// PreparedAppIdentity is a validated AppWire identity whose transcript has
// already been projected, but which nothing has published yet. Every fallible
// step -- header validation and transcript projection -- happens while building
// one, so installing it cannot fail and cannot leave the server half-switched.
type PreparedAppIdentity struct {
	sourceID  string
	threadID  string
	projector *appprojector.AppEventProjector
	turns     *appTurnSnapshot
}

// PrepareAppIdentity projects transcriptPath into a seeded turn snapshot for
// threadID and builds the matching event projector. It touches no Server state,
// so a caller can abandon the result on any later failure.
//
// An empty transcript path seeds empty state. A transcript that does not exist
// yet is the same answer: the session simply has no persisted history to seed
// from. A transcript whose header names a DIFFERENT session is an error --
// seeding one thread from another thread's history would publish a
// conversation that never happened.
func PrepareAppIdentity(sourceID, threadID, transcriptPath string) (PreparedAppIdentity, error) {
	if sourceID == "" {
		sourceID = "local"
	}
	if strings.TrimSpace(threadID) == "" {
		return PreparedAppIdentity{}, errors.New("thread id is required")
	}
	var turns []appwire.Turn
	persistedEntries := 0
	if path := strings.TrimSpace(transcriptPath); path != "" {
		header := transcriptHeader(path, appTranscriptMaxLineBytes)
		if header.SessionID != "" && header.SessionID != threadID {
			return PreparedAppIdentity{}, fmt.Errorf("transcript %s belongs to session %s, not %s", path, header.SessionID, threadID)
		}
		projected, entries, err := appTurnsFromTranscriptFile(path)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return PreparedAppIdentity{}, err
		}
		turns = projected
		persistedEntries = entries
	}
	ref := appwire.Ref{SourceID: sourceID, ThreadID: threadID}.String()
	snapshot := &appTurnSnapshot{threadID: threadID}
	snapshot.Seed(turns)
	// Fence the live turn ids above the seeded ones HERE, where the seed count
	// is known, rather than waiting for the session's own SessionStart to carry
	// it: nothing orders that event ahead of the first turn-starting request,
	// and since this snapshot became the only turn authority a collision
	// overwrites the seeded turn permanently.
	projector := appprojector.NewAppEventProjector(threadID, ref)
	projector.SeedPersistedTurns(persistedEntries)
	return PreparedAppIdentity{
		sourceID:  sourceID,
		threadID:  threadID,
		projector: projector,
		turns:     snapshot,
	}, nil
}

// ReplaceAppIdentity publishes a prepared identity. It runs inside one
// projection commit so no notification can cross between the old authority and
// the new one, and it cannot fail: preparation already did everything that
// could.
//
// activate, when non-nil, runs first and must itself be infallible -- it is
// where a caller swaps whatever it owns alongside the daemon's identity (the
// live session on clear).
//
// Replacing a different, non-empty identity closes the old thread's stream with
// one thread/closed record targeted at the OLD thread and ref. That record is
// recorded through the notifier so it takes the same ordered delivery path as
// every other notification, and it is deliberately not applied to the new
// snapshot: it describes the thread that just ended.
func (s *Server) ReplaceAppIdentity(prepared PreparedAppIdentity, activate func()) {
	s.appServer.CommitProjection(func() []appserver.SequencedNotification {
		if activate != nil {
			activate()
		}
		s.mu.Lock()
		oldSourceID, oldThreadID := s.appSourceID, s.appThreadID
		s.appSourceID = prepared.sourceID
		s.appThreadID = prepared.threadID
		s.appProjector = prepared.projector
		s.appTurns = prepared.turns
		oldDescendantIDs := make([]string, 0, len(s.appDescendants))
		for threadID := range s.appDescendants {
			oldDescendantIDs = append(oldDescendantIDs, threadID)
		}
		s.appDescendants = make(map[string]*appDescendantProjection)
		s.appActiveTurnID = ""
		s.appReservedTurnID = ""
		s.appLastStampedFailedToolCalls = nil
		// The envelope describes the session that just stopped being this
		// daemon's session, so it is replaced in the SAME commit as the identity
		// it belongs to. Zeroing the whole struct rather than clearing named
		// fields is what makes that structural: a field added to threadEnvelope
		// later is reset here for free, and cannot survive an identity change by
		// being forgotten. The caller re-seeds with RefreshThreadEnvelope once
		// the replacement session is the live one.
		s.appEnvelope = threadEnvelope{}
		s.status.SessionID = prepared.threadID
		s.mu.Unlock()

		if oldThreadID == "" || oldThreadID == prepared.threadID {
			return nil
		}
		if oldSourceID == "" {
			oldSourceID = "local"
		}
		oldRef := appwire.Ref{SourceID: oldSourceID, ThreadID: oldThreadID}.String()
		closed := []appserver.SequencedNotification{s.appNotifier.Record(oldThreadID, appwire.NotifyThreadClosed, appwire.ThreadClosedParams{
			ThreadID: oldThreadID,
			Ref:      oldRef,
			Reason:   "replaced",
		})}
		sort.Strings(oldDescendantIDs)
		for _, threadID := range oldDescendantIDs {
			closed = append(closed, s.appNotifier.Record(threadID, appwire.NotifyThreadClosed, appwire.ThreadClosedParams{
				ThreadID: threadID,
				Ref:      appwire.Ref{SourceID: oldSourceID, ThreadID: threadID}.String(),
				Reason:   "replaced",
			}))
		}
		return closed
	})
}

// SetAppIdentity installs an identity with no seeded history. Production serve
// prepares from the session's transcript instead; this is the shorthand for a
// thread that has none. It runs the same validation, so an identity
// PrepareAppIdentity would reject is not installed through this door either --
// it is rejected here and nothing changes.
func (s *Server) SetAppIdentity(sourceID, threadID string) {
	prepared, err := PrepareAppIdentity(sourceID, threadID, "")
	if err != nil {
		return
	}
	s.ReplaceAppIdentity(prepared, nil)
}

func (s *Server) AppNotificationsAfter(cursor uint64, threadID string) []appserver.SequencedNotification {
	return s.appNotifier.ReplayAfter(cursor, threadID)
}

func (s *Server) RecordAppEvent(event events.SessionEvent) {
	s.mu.RLock()
	beforeCommit := s.beforeAppProjectionCommit
	s.mu.RUnlock()
	if beforeCommit != nil {
		beforeCommit()
	}
	s.appServer.CommitProjection(func() []appserver.SequencedNotification {
		var committed []appserver.SequencedNotification
		// A test-only park INSIDE the commit, where projectionMu is actually
		// held. beforeAppProjectionCommit above cannot serve that purpose: it
		// runs before CommitProjection takes the gate, so a goroutine parked
		// there holds nothing and any ordering it appears to establish is a
		// coin toss. Deliberately called without s.mu, so a parked commit
		// blocks a concurrent one on the projection gate rather than on s.mu.
		s.mu.RLock()
		insideCommit := s.insideAppProjectionCommit
		s.mu.RUnlock()
		if insideCommit != nil {
			insideCommit()
		}
		s.mu.Lock()
		if !s.acceptsSessionEventLocked(event.SessionID) {
			s.mu.Unlock()
			return nil
		}
		s.ensureAppProjectorLocked(event.SessionID)
		projected := s.appProjector.Project(event)
		snapshot := s.appTurns
		s.appActiveTurnID = s.appProjector.ActiveTurnID()
		threadID := s.appThreadID
		if threadID == "" {
			threadID = event.SessionID
		}
		if threadID == "" {
			threadID = s.status.SessionID
		}
		sourceID := s.appSourceID
		if sourceID == "" {
			sourceID = "local"
		}
		ref := appwire.Ref{SourceID: sourceID, ThreadID: threadID}.String()
		s.mu.Unlock()

		for _, item := range projected {
			params := s.stampFailureCountOnStatusChange(item.Method, item.Params)
			params = s.stampCapabilitiesOnStatusChange(item.Method, params)
			params = s.stampFailureCountOnItemCompleted(item.Method, params)
			params = stampAppNotificationTarget(params, threadID, ref)
			record := s.appNotifier.Record(threadID, item.Method, params)
			snapshot.Apply([]appserver.SequencedNotification{record})
			committed = append(committed, record)
		}
		return committed
	})
}

// RecordDescendantAppEvent projects an in-process descendant onto its root
// daemon's AppWire transport. ownerThreadID fences late events from a child of
// a replaced root identity: those events must not bleed into the new tree.
// This deliberately does not sample or stamp the root session envelope;
// descendant mutations remain owned by the agent tree, while this path supplies
// the independently addressed read/notification view.
func (s *Server) RecordDescendantAppEvent(ownerThreadID string, event events.SessionEvent) {
	threadID := strings.TrimSpace(event.SessionID)
	ownerThreadID = strings.TrimSpace(ownerThreadID)
	if threadID == "" || ownerThreadID == "" || threadID == ownerThreadID {
		return
	}
	s.appServer.CommitProjection(func() []appserver.SequencedNotification {
		s.mu.Lock()
		if s.appThreadID != ownerThreadID {
			s.mu.Unlock()
			return nil
		}
		projection := s.appDescendants[threadID]
		if projection == nil {
			sourceID := s.appSourceID
			if sourceID == "" {
				sourceID = "local"
			}
			ref := appwire.Ref{SourceID: sourceID, ThreadID: threadID}.String()
			projection = &appDescendantProjection{
				projector: appprojector.NewAppEventProjector(threadID, ref),
				turns:     &appTurnSnapshot{threadID: threadID},
				thread: appwire.Thread{
					ID:        threadID,
					SessionID: threadID,
					Source:    sourceID,
					Serf:      appwire.SerfThread{Ref: ref, Kind: "subagent"},
				},
			}
			s.appDescendants[threadID] = projection
		}
		projected := projection.projector.Project(event)
		projection.activeTurnID = projection.projector.ActiveTurnID()
		for _, item := range projected {
			switch params := item.Params.(type) {
			case appwire.ThreadStartedParams:
				projection.thread = params.Thread
				projection.thread.Serf.Kind = "subagent"
			case appwire.ThreadStatusChangedParams:
				projection.thread.Status = params.Status
			}
		}
		ref := projection.thread.Serf.Ref
		if ref == "" {
			ref = appwire.Ref{SourceID: projection.thread.Source, ThreadID: threadID}.String()
			projection.thread.Serf.Ref = ref
		}
		snapshot := projection.turns
		s.mu.Unlock()

		committed := make([]appserver.SequencedNotification, 0, len(projected))
		for _, item := range projected {
			params := stampAppNotificationTarget(item.Params, threadID, ref)
			record := s.appNotifier.Record(threadID, item.Method, params)
			snapshot.Apply([]appserver.SequencedNotification{record})
			committed = append(committed, record)
		}
		return committed
	})
}

func stampAppNotificationTarget(params any, threadID, ref string) any {
	data, err := json.Marshal(params)
	if err != nil {
		return params
	}
	fields := map[string]json.RawMessage{}
	if len(data) > 0 && string(data) != "null" {
		if err := json.Unmarshal(data, &fields); err != nil {
			return params
		}
	}
	fields["threadId"], _ = json.Marshal(threadID)
	fields["ref"], _ = json.Marshal(ref)
	qualified, err := json.Marshal(fields)
	if err != nil {
		return params
	}
	return json.RawMessage(qualified)
}

// stampFailureCountOnStatusChange rides the session's running failure count
// along on every thread/status/changed (kata 12rq).
//
// The count is otherwise snapshot-only, refreshed by thread/read — so a client
// that attached while the session was clean would keep showing nothing however
// many failures followed, which is exactly the watcher the figure exists for.
// A status transition is a turn boundary, the only moment the count can have
// moved, so this refreshes it precisely when there is something to say and
// never polls.
//
// It happens HERE, at the server's single notification egress, rather than in
// the projector: the projector maps events to notifications and holds no
// session handle, while the pull callback is the server's.
//
// An unmeasured count is left off entirely. Absence on a notification means
// "no update" — a zero here would let a client that never measured anything
// start claiming a clean run.
func (s *Server) stampFailureCountOnStatusChange(method string, params any) any {
	if method != appwire.NotifyThreadStatusChanged {
		return params
	}
	status, ok := params.(appwire.ThreadStatusChangedParams)
	if !ok {
		return params
	}
	count, measured := s.envelopeFailedToolCalls()
	if !measured {
		return params
	}
	status.FailedToolCalls = &count
	return status
}

// stampCapabilitiesOnStatusChange rides the action set that goes with the
// announced status along on every thread/status/changed (kata 06t8).
//
// The set is otherwise snapshot-only, refreshed by thread/read — and Send,
// Steer and Queue are all defined by whether a turn is in flight
// (appCapabilities). So a client that hydrated while the session was idle, or
// while it was a cold exited session the hub answered from the past index,
// holds steer=false/queue=false for the whole turn its own send starts: the
// composer knows the turn is live (it has the status change and turn/started)
// and still renders no Steer, no Stop and a disabled Send until a reload.
// Every capability change is a status transition, so this refreshes them
// exactly when they can have moved and never polls — the same shape
// stampFailureCountOnStatusChange already uses for the failure count.
//
// It happens HERE, at the server's single notification egress, rather than in
// the projector, for the same reason that one does: the projector maps events
// to notifications and holds no session handle.
//
// The set is computed FROM THE ANNOUNCED STATUS, not from the server's ambient
// processing flag. The two are written by different goroutines — the projector
// emits this notification from the event bridge while SetProcessing is the
// session loop's — so reading the ambient flag here would be a race that
// silently publishes the old status's capabilities. Deriving them from the
// status in hand makes the frame self-consistent by construction: a client
// applying both fields of one notification can never hold a status and a
// capability set that disagree.
func (s *Server) stampCapabilitiesOnStatusChange(method string, params any) any {
	if method != appwire.NotifyThreadStatusChanged {
		return params
	}
	status, ok := params.(appwire.ThreadStatusChangedParams)
	if !ok {
		return params
	}
	// A daemon announcing its own close is describing a thread it is about to
	// stop running, and what that thread can still be asked to do is the HUB's
	// to say: it answers an exited session's read from the past index and
	// resumes it on the next send, advertising Send there
	// (cmd/serf-hub/app_threadread.go's pastThreadCapabilities). This daemon's
	// own all-false set would take the follow-up composer away from a session
	// the hub would happily wake — the same wedge in the other direction. So
	// the close frame leaves the field empty and the hub fills it in with its
	// own answer as it relays the frame (app_relay.go's
	// stampClosedThreadCapabilities): a client is never left reading a set the
	// departing daemon cut for a turn that is over (kata pk2d).
	if status.Status.Type == appwire.ThreadStatusClosed {
		return params
	}
	capabilities := s.appCapabilities(status.Status.Type, status.Status.Type == appwire.ThreadStatusActive)
	status.Capabilities = &capabilities
	return status
}

// envelopeFailedToolCalls reads the running failure count out of the
// materialized envelope. The bridge refreshes the failures facet BEFORE the
// commit that projects the event being stamped, so the figure a notification
// carries is at least as new as the event it rides on.
func (s *Server) envelopeFailedToolCalls() (int, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.appEnvelope.FailedToolCalls == nil {
		return 0, false
	}
	return *s.appEnvelope.FailedToolCalls, true
}

// stampFailureCountOnItemCompleted rides the running failure count on an
// item/completed notification, but ONLY on the item whose completion just
// moved it (kata 895d) — every other item/completed passes through
// untouched.
//
// thread/status/changed already carries the count unconditionally, but only
// at a turn boundary; a live watcher on a long turn sees nothing move however
// many tool calls fail inside it, the same shape of harm the count exists to
// fix at session scale (kata 12rq). item/completed is the natural finer-grain
// carrier — a failure IS an item completing — but it fires once per tool
// call, so stamping it unconditionally would resend an unchanged figure on
// every success to change it on a few. Gating on "did the count move since
// the last stamp" adds the field only where it is news, which is the same
// rule the client's own render gate applies (StatusRow.tsx's FailureCount:
// absent or unchanged says nothing).
//
// The params arrive as either a typed ItemLifecycleParams or a map[string]any,
// so this handles both. That is not future-proofing: this function was written
// when internal/appprojector built these params as a map, kcb5 then converted
// that producer to the catalog type, and the map-only type assertion here
// started failing - silently, returning the params unstamped, because a failed
// assertion is not an error. The count simply stopped moving mid-turn and only
// two tests noticed.
func (s *Server) stampFailureCountOnItemCompleted(method string, params any) any {
	if method != appwire.NotifyItemCompleted {
		return params
	}
	typed, isTyped := params.(appwire.ItemLifecycleParams)
	fields, isMap := params.(map[string]any)
	if !isTyped && !isMap {
		return params
	}
	count, measured := s.envelopeFailedToolCalls()
	if !measured {
		return params
	}
	s.mu.Lock()
	unchanged := s.appLastStampedFailedToolCalls != nil && *s.appLastStampedFailedToolCalls == count
	if !unchanged {
		stamped := count
		s.appLastStampedFailedToolCalls = &stamped
	}
	s.mu.Unlock()
	if unchanged {
		return params
	}
	if isTyped {
		stamped := count
		typed.FailedToolCalls = &stamped
		return typed
	}
	fields["failedToolCalls"] = count
	return fields
}

func (s *Server) acceptsSessionEvent(sessionID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.acceptsSessionEventLocked(sessionID)
}

func (s *Server) acceptsSessionEventLocked(sessionID string) bool {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return true
	}
	if s.appThreadID != "" {
		return sessionID == s.appThreadID || s.status.SessionID == ""
	}
	if s.status.SessionID != "" {
		return sessionID == s.status.SessionID
	}
	return true
}

func (s *Server) registerAppWireHandlers() {
	router := s.appServer.Router()
	appserver.HandleTyped(router, appwire.MethodThreadList, s.handleAppThreadList)
	appserver.HandleTyped(router, appwire.MethodThreadRead, s.handleAppThreadRead)
	appserver.HandleTyped(router, appwire.MethodTurnStart, s.handleAppTurnStart)
	appserver.HandleTyped(router, appwire.MethodTurnSteer, s.handleAppTurnSteer)
	appserver.HandleTyped(router, appwire.MethodSerfSandboxEscalationResolve, s.handleAppSandboxEscalationResolve)
	appserver.HandleTyped(router, appwire.MethodTurnInterrupt, s.handleAppTurnInterrupt)
	appserver.HandleTyped(router, appwire.MethodTurnQueue, s.handleAppTurnQueue)
	appserver.HandleTyped(router, appwire.MethodTurnDrainAsSteer, s.handleAppTurnDrainAsSteer)
	appserver.HandleTyped(router, appwire.MethodTurnPromoteQueuedAsSteer, s.handleAppTurnPromoteQueuedAsSteer)
	appserver.HandleTyped(router, appwire.MethodTurnCancelQueued, s.handleAppTurnCancelQueued)
	appserver.HandleTyped(router, appwire.MethodGoalSet, s.handleAppGoalSet)
	appserver.HandleTyped(router, appwire.MethodThreadCompactStart, s.handleAppThreadCompactStart)
	appserver.HandleTyped(router, appwire.MethodThreadShutdown, s.handleAppThreadShutdown)
	appserver.HandleTyped(router, appwire.MethodThreadClear, s.handleAppThreadClear)
	appserver.HandleTyped(router, appwire.MethodThreadModelSet, s.handleAppThreadModelSet)
	appserver.HandleTyped(router, appwire.MethodSerfThreadNameSet, s.handleAppThreadNameSet)
	appserver.HandleTyped(router, appwire.MethodThreadReasoningEffortSet, s.handleAppThreadReasoningEffortSet)
	appserver.HandleTyped(router, appwire.MethodSerfTasksList, s.handleAppTasksList)
	appserver.HandleTyped(router, appwire.MethodSerfJobsList, s.handleAppJobsList)
	appserver.HandleTyped(router, appwire.MethodSerfJobsOutput, s.handleAppJobsOutput)
	appserver.HandleTyped(router, appwire.MethodModelList, s.handleAppModelList)
	appserver.HandleTyped(router, appwire.MethodThreadTurnsList, s.handleAppThreadTurnsList)
}

func (s *Server) handleAppThreadList(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	data := []appwire.Thread{s.appThread()}
	s.mu.RLock()
	ids := make([]string, 0, len(s.appDescendants))
	for id := range s.appDescendants {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		projection := s.appDescendants[id]
		thread := projection.thread
		thread.Serf.ActiveTurnID = projection.activeTurnID
		data = append(data, thread)
	}
	s.mu.RUnlock()
	return appwire.ThreadListResponse{Data: data}, nil
}

func (s *Server) handleAppThreadRead(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	if !params.Subscribe {
		return s.appThreadReadSnapshot(params), nil
	}
	threadID := s.appThreadIDForRead(params)
	if threadID == "" {
		return appwire.ThreadReadResponse{}, appwire.SessionUnavailable("thread is unavailable")
	}
	var response appwire.ThreadReadResponse
	captured := appserver.CaptureSubscription(
		ctx,
		params.ReplaceSubscription,
		func() string { return threadID },
		s.appNotifier.CurrentSequence,
		func() bool {
			response = s.appThreadReadSnapshot(params)
			return true
		},
	)
	if !captured {
		return appwire.ThreadReadResponse{}, appwire.SessionUnavailable("thread subscription is unavailable")
	}
	return response, nil
}

// appThreadReadSnapshot answers entirely from memory. It runs under the
// subscription cut, so it must not open a file: transcript persistence can lead
// live event delivery, and a read that parsed the file there would return an
// output whose matching notification is still on the other side of the cut --
// the same answer twice, under two identities.
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
// this is a struct copy, and the Server holds no callback that could reach a
// session from a read path.
func (s *Server) appThreadReadSnapshot(params appwire.ThreadReadParams) appwire.ThreadReadResponse {
	threadID := s.appThreadIDForRead(params)
	thread, ok := s.appThreadForID(threadID)
	if !ok {
		return appwire.ThreadReadResponse{}
	}
	var olderCursor string
	if params.IncludeTurns {
		thread.Turns, olderCursor = s.appLatestTurns(thread.ID, params.TurnLimit)
	}
	return appwire.ThreadReadResponse{Thread: thread, OlderCursor: olderCursor}
}

func (s *Server) appThreadIDForRead(params appwire.ThreadReadParams) string {
	threadID := strings.TrimSpace(params.ThreadID)
	if threadID == "" && strings.TrimSpace(params.Ref) != "" {
		if ref, err := appwire.ParseRef(params.Ref); err == nil && ref.SourceID == "local" {
			threadID = ref.ThreadID
		}
	}
	if threadID == "" {
		threadID = s.appProjectionThreadID()
	}
	if _, ok := s.appThreadForID(threadID); !ok {
		return ""
	}
	return threadID
}

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
	thread.Serf.ActiveTurnID = projection.activeTurnID
	s.mu.RUnlock()
	return thread, true
}

func (s *Server) appProjectionThreadID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.appThreadID != "" {
		return s.appThreadID
	}
	return s.status.SessionID
}

// appAllTurns returns the whole installed snapshot for threadID, oldest-first.
// It is the one authority: thread/read, the latest window, and older pages all
// derive from this slice, so they cannot disagree with each other or with what
// subscribers were sent.
func (s *Server) appAllTurns(threadID string) []appwire.Turn {
	s.mu.RLock()
	snapshot := s.appTurns
	installed := s.appThreadID == threadID && snapshot != nil && snapshot.threadID == threadID
	if !installed {
		if projection := s.appDescendants[threadID]; projection != nil {
			snapshot = projection.turns
			installed = snapshot != nil && snapshot.threadID == threadID
		}
	}
	s.mu.RUnlock()
	if !installed {
		return nil
	}
	return snapshot.Snapshot()
}

func transcriptHeader(path string, maxLineBytes int) transcript.Header {
	file, err := os.Open(path)
	if err != nil {
		return transcript.Header{}
	}
	defer file.Close() //nolint:errcheck // read-only file; close error is not actionable

	reader := bufio.NewReaderSize(file, 64*1024)
	for {
		lineBytes, complete, _, err := transcript.ReadLine(reader, maxLineBytes)
		if err != nil || !complete {
			return transcript.Header{}
		}
		line := strings.TrimSpace(string(lineBytes))
		if line == "" {
			continue
		}
		header, err := transcript.DecodeHeader([]byte(line))
		if err != nil {
			return transcript.Header{}
		}
		return header
	}
}

// appLatestTurns windows the newest limit turns out of the installed snapshot
// and returns the cursor for the page before them. A limit of zero or less
// returns the whole thread with no cursor.
func (s *Server) appLatestTurns(threadID string, limit int) ([]appwire.Turn, string) {
	return appwire.WindowTurns(s.appAllTurns(threadID), limit)
}

// appPageTurns pages backward through the installed snapshot from cursor.
func (s *Server) appPageTurns(threadID, cursor string, limit int) appwire.ThreadTurnsListResponse {
	return appwire.PageTurns(s.appAllTurns(threadID), cursor, limit)
}

// handleAppThreadTurnsList pages turns backward (older) for lazy transcript
// loading. The web client seeds the latest window via thread/read(TurnLimit)
// and walks back with this as the user scrolls up.
func (s *Server) handleAppThreadTurnsList(_ context.Context, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
	threadID := s.appThreadIDForRead(appwire.ThreadReadParams{ThreadID: params.ThreadID, Ref: params.Ref})
	if threadID == "" {
		return appwire.ThreadTurnsListResponse{}, appwire.SessionUnavailable("thread is unavailable")
	}
	return s.appPageTurns(threadID, params.Cursor, params.Limit), nil
}

func (s *Server) handleAppTurnStart(_ context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
	if err := s.requireRootMutationTarget(params.Ref, params.ThreadID); err != nil {
		return appwire.TurnStartResponse{}, err
	}
	input, err := appwire.NormalizeMutationInput(params.Input)
	if err != nil {
		return appwire.TurnStartResponse{}, appwire.InvalidParams(err.Error())
	}
	if !input.HasContent() {
		return appwire.TurnStartResponse{}, appwire.InvalidParams("input is required")
	}
	params.Input = input.Items
	if strings.TrimSpace(params.ClientMutationID) == "" {
		return appwire.TurnStartResponse{}, appwire.InvalidParams("clientMutationId is required")
	}
	s.mu.RLock()
	fn := s.retrySafeTurns.Start
	s.mu.RUnlock()
	if fn == nil {
		return appwire.TurnStartResponse{}, appwire.Unavailable("turn start not available")
	}
	response, err := fn(params)
	// turn/start accepts a durable mutation intent and emits NOTHING: unlike
	// steer/queue/drain/promote/cancel, it does not reflect the durable queue, so
	// no QUEUE_CHANGED announces the pending execution it just recorded. The
	// serve loop's later claim does emit one, but a client reading between accept
	// and claim would not see its own in-flight mutation -- which is the single
	// thing the retry-safe projection exists to show a reconnecting client.
	//
	// Every mutation handler refreshes, not just this one. They all commit to the
	// same store, and a uniform refresh means nobody has to re-derive which of
	// the seven happens to emit. The sample is cheap: the store publishes its
	// committed generation under a narrow mutex it never holds across a write.
	s.refreshFacets(facetQueue)
	return response, agent.NormalizeClientMutationError(params.ClientMutationID, err)
}

func (s *Server) handleAppTurnSteer(_ context.Context, params appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
	if err := s.requireRootMutationTarget(params.Ref, params.ThreadID); err != nil {
		return appwire.TurnSteerResponse{}, err
	}
	input, err := appwire.NormalizeMutationInput(params.Input)
	if err != nil {
		return appwire.TurnSteerResponse{}, appwire.InvalidParams(err.Error())
	}
	if !input.HasContent() {
		return appwire.TurnSteerResponse{}, appwire.InvalidParams("input is required")
	}
	params.Input = input.Items
	if strings.TrimSpace(params.ExpectedTurnID) == "" {
		return appwire.TurnSteerResponse{}, appwire.InvalidParams("expectedTurnId is required")
	}
	if strings.TrimSpace(params.ClientMutationID) == "" {
		return appwire.TurnSteerResponse{}, appwire.InvalidParams("clientMutationId is required")
	}
	s.mu.RLock()
	fn := s.retrySafeTurns.Steer
	s.mu.RUnlock()
	if fn == nil {
		return appwire.TurnSteerResponse{}, appwire.Unavailable("steer not available")
	}
	response, err := fn(params)
	s.refreshFacets(facetQueue)
	return response, agent.NormalizeClientMutationError(params.ClientMutationID, err)
}

func (s *Server) handleAppSandboxEscalationResolve(_ context.Context, params appwire.SandboxEscalationResolveParams) (appwire.EmptyResponse, error) {
	if err := s.requireRootMutationTarget(params.Ref, params.ThreadID); err != nil {
		return appwire.EmptyResponse{}, err
	}
	escalationID := strings.TrimSpace(params.EscalationID)
	if escalationID == "" {
		return appwire.EmptyResponse{}, appwire.InvalidParams("escalationId is required")
	}
	s.mu.RLock()
	fn := s.sandboxEscalationResolveFunc
	s.mu.RUnlock()
	if fn == nil {
		return appwire.EmptyResponse{}, appwire.Unavailable("sandbox escalation resolve not available")
	}
	if err := fn(escalationID, params.Approve); err != nil {
		// The escalation is unknown or already resolved (a stale card, a double
		// click, or a race with turn-interrupt/close). Surface it as a conflict so
		// the client can drop the card rather than retry.
		return appwire.EmptyResponse{}, appwire.Conflict(err.Error())
	}
	return appwire.EmptyResponse{}, nil
}

func (s *Server) handleAppTurnInterrupt(ctx context.Context, params appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
	if err := s.requireRootMutationTarget(params.Ref, params.ThreadID); err != nil {
		return appwire.TurnInterruptResponse{}, err
	}
	if strings.TrimSpace(params.ExpectedTurnID) == "" {
		return appwire.TurnInterruptResponse{}, appwire.InvalidParams("expectedTurnId is required")
	}
	if strings.TrimSpace(params.ClientMutationID) == "" {
		return appwire.TurnInterruptResponse{}, appwire.InvalidParams("clientMutationId is required")
	}
	s.mu.RLock()
	fn := s.retrySafeTurns.Interrupt
	s.mu.RUnlock()
	if fn == nil {
		return appwire.TurnInterruptResponse{}, appwire.Unavailable("interrupt not available")
	}
	response, err := fn(ctx, params)
	s.refreshFacets(facetQueue)
	return response, agent.NormalizeClientMutationError(params.ClientMutationID, err)
}

func (s *Server) handleAppTurnQueue(_ context.Context, params appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
	if err := s.requireRootMutationTarget(params.Ref, ""); err != nil {
		return appwire.TurnQueueResponse{}, err
	}
	input, err := appwire.NormalizeMutationInput(params.Input)
	if err != nil {
		return appwire.TurnQueueResponse{}, appwire.InvalidParams(err.Error())
	}
	if !input.HasContent() {
		return appwire.TurnQueueResponse{}, appwire.InvalidParams("input required")
	}
	params.Input = input.Items
	if strings.TrimSpace(params.ExpectedTurnID) == "" {
		return appwire.TurnQueueResponse{}, appwire.InvalidParams("expectedTurnId is required")
	}
	if strings.TrimSpace(params.ClientMutationID) == "" {
		return appwire.TurnQueueResponse{}, appwire.InvalidParams("clientMutationId is required")
	}
	s.mu.RLock()
	fn := s.retrySafeTurns.Queue
	s.mu.RUnlock()
	if fn == nil {
		return appwire.TurnQueueResponse{}, appwire.Unavailable("queue not available")
	}
	response, err := fn(params)
	s.refreshFacets(facetQueue)
	return response, agent.NormalizeClientMutationError(params.ClientMutationID, err)
}

func (s *Server) handleAppTurnDrainAsSteer(_ context.Context, params appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) {
	if err := s.requireRootMutationTarget(params.Ref, ""); err != nil {
		return appwire.TurnDrainAsSteerResponse{}, err
	}
	input, err := appwire.NormalizeMutationInput(params.Input)
	if err != nil {
		return appwire.TurnDrainAsSteerResponse{}, appwire.InvalidParams(err.Error())
	}
	params.Input = input.Items
	if strings.TrimSpace(params.ExpectedTurnID) == "" {
		return appwire.TurnDrainAsSteerResponse{}, appwire.InvalidParams("expectedTurnId is required")
	}
	if strings.TrimSpace(params.ClientMutationID) == "" {
		return appwire.TurnDrainAsSteerResponse{}, appwire.InvalidParams("clientMutationId is required")
	}
	s.mu.RLock()
	fn := s.retrySafeTurns.Drain
	s.mu.RUnlock()
	if fn == nil {
		return appwire.TurnDrainAsSteerResponse{}, appwire.Unavailable("drain-as-steer not available")
	}
	response, err := fn(params)
	s.refreshFacets(facetQueue)
	return response, agent.NormalizeClientMutationError(params.ClientMutationID, err)
}

// handleAppTurnPromoteQueuedAsSteer validates static request shape and leaves
// active-turn and queue compare-and-commit decisions to the Session callback.
func (s *Server) handleAppTurnPromoteQueuedAsSteer(_ context.Context, params appwire.TurnPromoteQueuedAsSteerParams) (appwire.TurnPromoteQueuedAsSteerResponse, error) {
	if err := s.requireRootMutationTarget(params.Ref, ""); err != nil {
		return appwire.TurnPromoteQueuedAsSteerResponse{}, err
	}
	if params.Index < 0 {
		return appwire.TurnPromoteQueuedAsSteerResponse{}, appwire.InvalidParams("index must be >= 0")
	}
	if strings.TrimSpace(params.ExpectedTurnID) == "" {
		return appwire.TurnPromoteQueuedAsSteerResponse{}, appwire.InvalidParams("expectedTurnId is required")
	}
	if strings.TrimSpace(params.ExpectedEntryID) == "" {
		return appwire.TurnPromoteQueuedAsSteerResponse{}, appwire.InvalidParams("expectedEntryId is required")
	}
	if strings.TrimSpace(params.ClientMutationID) == "" {
		return appwire.TurnPromoteQueuedAsSteerResponse{}, appwire.InvalidParams("clientMutationId is required")
	}
	s.mu.RLock()
	fn := s.retrySafeTurns.Promote
	s.mu.RUnlock()
	if fn == nil {
		return appwire.TurnPromoteQueuedAsSteerResponse{}, appwire.Unavailable("promote-queued-as-steer not available")
	}
	response, err := fn(params)
	s.refreshFacets(facetQueue)
	return response, agent.NormalizeClientMutationError(params.ClientMutationID, err)
}

// handleAppTurnCancelQueued validates static request shape and leaves queue
// compare-and-commit decisions to the Session callback.
func (s *Server) handleAppTurnCancelQueued(_ context.Context, params appwire.TurnCancelQueuedParams) (appwire.TurnCancelQueuedResponse, error) {
	if err := s.requireRootMutationTarget(params.Ref, ""); err != nil {
		return appwire.TurnCancelQueuedResponse{}, err
	}
	if params.Index < 0 {
		return appwire.TurnCancelQueuedResponse{}, appwire.InvalidParams("index must be >= 0")
	}
	if strings.TrimSpace(params.ExpectedEntryID) == "" {
		return appwire.TurnCancelQueuedResponse{}, appwire.InvalidParams("expectedEntryId is required")
	}
	if strings.TrimSpace(params.ClientMutationID) == "" {
		return appwire.TurnCancelQueuedResponse{}, appwire.InvalidParams("clientMutationId is required")
	}
	s.mu.RLock()
	fn := s.retrySafeTurns.Cancel
	s.mu.RUnlock()
	if fn == nil {
		return appwire.TurnCancelQueuedResponse{}, appwire.Unavailable("cancel-queued not available")
	}
	response, err := fn(params)
	s.refreshFacets(facetQueue)
	return response, agent.NormalizeClientMutationError(params.ClientMutationID, err)
}

// handleAppGoalSet handles goal/set. An empty objective clears the goal; both
// set and clear route through the single goalFunc callback (the callback maps an
// empty objective to ClearGoal). Started reports whether the goal loop began
// immediately (idle session) versus after the current turn (a turn is running,
// whose gate is the backstop).
func (s *Server) handleAppGoalSet(_ context.Context, params appwire.GoalSetParams) (appwire.GoalSetResponse, error) {
	if err := s.requireRootMutationTarget(params.Ref, ""); err != nil {
		return appwire.GoalSetResponse{}, err
	}
	s.mu.RLock()
	fn := s.goalFunc
	s.mu.RUnlock()
	if fn == nil {
		return appwire.GoalSetResponse{}, appwire.Unavailable("goal not available")
	}
	started, err := fn(params.Objective)
	if err != nil {
		return appwire.GoalSetResponse{}, err
	}
	// The goal store has no event handle: SetGoal emits nothing when a turn is
	// running, no kick is wired, or an ask is pending, and Clear emits nothing
	// ever. So no event in facetsByEvent can observe this change, and without
	// this refresh a cleared goal would stay on every thread/read for the life
	// of the identity -- the status bar still reporting an objective the user
	// explicitly abandoned.
	//
	// This handler is the change point, so the refresh belongs here. It uses the
	// same sampler the bridge uses, so the goal has one source of truth either
	// way.
	s.refreshFacets(facetGoal)
	return appwire.GoalSetResponse{Started: started}, nil
}

func (s *Server) handleAppThreadCompactStart(ctx context.Context, params appwire.ThreadCompactStartParams) (appwire.EmptyResponse, error) {
	if err := s.requireRootMutationTarget(params.Ref, ""); err != nil {
		return appwire.EmptyResponse{}, err
	}
	s.mu.RLock()
	fn := s.compactFunc
	s.mu.RUnlock()
	if fn == nil {
		return appwire.EmptyResponse{}, appwire.Unavailable("compact not available")
	}
	return appwire.EmptyResponse{}, fn(ctx)
}

func (s *Server) handleAppThreadShutdown(_ context.Context, params appwire.ThreadShutdownParams) (appwire.EmptyResponse, error) {
	if err := s.requireRootMutationTarget(params.Ref, ""); err != nil {
		return appwire.EmptyResponse{}, err
	}
	s.mu.RLock()
	fn := s.shutdownFunc
	s.mu.RUnlock()
	if fn == nil {
		return appwire.EmptyResponse{}, appwire.Unavailable("shutdown not available")
	}
	go fn()
	return appwire.EmptyResponse{}, nil
}

// handleAppThreadClear is unwired on purpose: v2 disabled the Serf clear
// capability pending a retry-safe mutation shape for it. Whatever reinstates it
// has to take the same single-in-flight gate handleClear holds, because two
// clears running at once each replace the SAME live session and only one of the
// two replacements can be current — nothing closes the other, so its env's
// Cleanup() never runs (kata mz2f). The gate lives in handleClear rather than
// around clearFunc because POST /clear is currently its only caller.
func (s *Server) handleAppThreadClear(context.Context, appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
	return appwire.ThreadClearResponse{}, appwire.Unavailable("thread/clear is unavailable in serf-appwire-v2")
}

func (s *Server) handleAppThreadModelSet(_ context.Context, params appwire.ThreadModelSetParams) (appwire.EmptyResponse, error) {
	if err := s.requireRootMutationTarget(params.Ref, ""); err != nil {
		return appwire.EmptyResponse{}, err
	}
	model := strings.TrimSpace(params.Model)
	if model == "" {
		return appwire.EmptyResponse{}, appwire.InvalidParams("model is required")
	}
	if provider := strings.TrimSpace(params.ModelProvider); provider != "" {
		model = provider + "/" + model
	}
	s.mu.RLock()
	processing := s.processing
	reservedTurnID := strings.TrimSpace(s.appReservedTurnID)
	fn := s.modelFunc
	s.mu.RUnlock()
	if processing || reservedTurnID != "" {
		msg := "session is processing"
		if reservedTurnID != "" {
			msg = "turn " + reservedTurnID + " is active"
		}
		return appwire.EmptyResponse{}, appwire.Conflict(msg)
	}
	if fn == nil {
		return appwire.EmptyResponse{}, appwire.Unavailable("model change not available")
	}
	if err := fn(model); err != nil {
		// Every SetModel failure is mapped to InvalidParams deliberately: the
		// hook's error modes today are all validation-shaped (unknown model,
		// not a member of the instance's catalog, unresolvable ref). Revisit
		// this blanket mapping if SetModel grows I/O failure modes (e.g. a live
		// ListModels fetch failing), which would deserve a distinct code.
		return appwire.EmptyResponse{}, appwire.InvalidParams(err.Error())
	}
	return appwire.EmptyResponse{}, nil
}

func (s *Server) handleAppThreadNameSet(_ context.Context, params appwire.ThreadNameSetParams) (appwire.EmptyResponse, error) {
	if err := s.requireRootMutationTarget(params.Ref, ""); err != nil {
		return appwire.EmptyResponse{}, err
	}
	name := strings.TrimSpace(params.Name)
	if name == "" {
		return appwire.EmptyResponse{}, appwire.InvalidParams("name is required")
	}
	s.mu.RLock()
	fn := s.nameFunc
	s.mu.RUnlock()
	if fn == nil {
		return appwire.EmptyResponse{}, appwire.Unavailable("rename not available")
	}
	fn(name)
	return appwire.EmptyResponse{}, nil
}

func (s *Server) handleAppThreadReasoningEffortSet(_ context.Context, params appwire.ThreadReasoningEffortSetParams) (appwire.EmptyResponse, error) {
	if err := s.requireRootMutationTarget(params.Ref, ""); err != nil {
		return appwire.EmptyResponse{}, err
	}
	s.mu.RLock()
	fn := s.reasoningEffortFunc
	s.mu.RUnlock()
	if fn == nil {
		return appwire.EmptyResponse{}, appwire.Unavailable("reasoning effort change not available")
	}
	// Normalize disable-aliases to "" and reject unknown vocabulary, so a typo or
	// direct API call can't persist a provider-rejected effort that breaks later
	// requests.
	effort := llm.NormalizeReasoningEffort(params.ReasoningEffort)
	if effort != "" && llm.ReasoningEffortRank(effort) == 0 {
		return appwire.EmptyResponse{}, appwire.InvalidParams("invalid reasoning effort: " + params.ReasoningEffort)
	}
	fn(effort)
	return appwire.EmptyResponse{}, nil
}

func (s *Server) requireRootMutationTarget(rawRef, threadID string) error {
	target := strings.TrimSpace(threadID)
	if strings.TrimSpace(rawRef) != "" {
		ref, err := appwire.ParseRef(rawRef)
		if err != nil {
			return appwire.InvalidParams(err.Error())
		}
		if ref.SourceID != "local" {
			return appwire.SessionUnavailable("thread is unavailable")
		}
		target = ref.ThreadID
	}
	root := s.appProjectionThreadID()
	if target != "" && target != root {
		return appwire.SessionUnavailable("thread is not served by this daemon: " + target)
	}
	return nil
}

func (s *Server) handleAppTasksList(context.Context, appwire.TaskListParams) (appwire.TaskListResponse, error) {
	s.mu.RLock()
	fn := s.tasksFn
	s.mu.RUnlock()
	if fn == nil {
		return appwire.TaskListResponse{}, nil
	}
	return appwire.TaskListResponse{Data: fn()}, nil
}

func (s *Server) handleAppJobsList(_ context.Context, params appwire.JobsListParams) (appwire.JobsListResponse, error) {
	s.mu.RLock()
	fn := s.jobsFn
	s.mu.RUnlock()
	if fn == nil {
		return appwire.JobsListResponse{}, nil
	}
	data, err := fn(params)
	if err != nil {
		return appwire.JobsListResponse{}, err
	}
	return appwire.JobsListResponse{Data: data}, nil
}

func (s *Server) handleAppJobsOutput(_ context.Context, params appwire.JobsOutputParams) (appwire.JobsOutputResponse, error) {
	s.mu.RLock()
	fn := s.jobOutputFn
	s.mu.RUnlock()
	if fn == nil {
		return appwire.JobsOutputResponse{}, appwire.Unavailable("job output not available")
	}
	data, found, err := fn(params.JobID, params.BeforeBytes, params.MaxBytes)
	if err != nil {
		return appwire.JobsOutputResponse{}, err
	}
	if !found {
		return appwire.JobsOutputResponse{}, appwire.InvalidParams("job not found: " + params.JobID)
	}
	return appwire.JobsOutputResponse{Data: data}, nil
}

func (s *Server) handleAppModelList(ctx context.Context, _ appwire.ModelListParams) (appwire.ModelListResponse, error) {
	s.mu.RLock()
	fn := s.listModelsFunc
	provider := s.status.Profile
	s.mu.RUnlock()
	if fn == nil {
		return appwire.ModelListResponse{}, nil
	}
	models, err := fn(ctx)
	if err != nil {
		return appwire.ModelListResponse{}, err
	}
	out := make([]appwire.ModelDescriptor, 0, len(models))
	for _, model := range models {
		out = append(out, appwire.ModelDescriptor{Provider: provider, Model: model.ID})
	}
	return appwire.ModelListResponse{Data: out}, nil
}

// appThread assembles the thread snapshot. It is a struct copy plus identity:
// every live-session value comes from the materialized envelope, which the
// bridge maintains at the moments those values change.
//
// It must stay that way. This runs under the response cut, which holds
// projectionMu AND deliveryMu, so anything reached from here blocks every other
// connection's capture, every projection commit, and the whole event bridge for
// as long as it takes. It used to pull sixteen live session callbacks from
// here; four of them could block behind a transcript fsync, a synchronous
// jobs.jsonl read, or the session's own mutex.
func (s *Server) appThread() appwire.Thread {
	s.mu.RLock()
	status := s.status
	sourceID := s.appSourceID
	threadID := s.appThreadID
	processing := s.processing
	envelope := s.appEnvelope
	activeTurnID := s.appActiveTurnID
	s.mu.RUnlock()

	if sourceID == "" {
		sourceID = "local"
	}
	if threadID == "" {
		threadID = status.SessionID
	}
	ref := appwire.Ref{SourceID: sourceID, ThreadID: threadID}.String()
	pressure := envelope.ContextPressure
	metrics := envelope.ContextMetrics
	var diagnostics *appwire.SerfDiagnostics
	if envelope.Detailed != nil {
		diagnostics = appDiagnosticsFromDetailedStatus(*envelope.Detailed)
	}
	queue := envelope.Queue
	pendingMutations := envelope.PendingMutations
	goalState := envelope.Goal
	taskAggregate := envelope.Tasks
	workMillis := envelope.WorkMillis
	usage := envelope.Usage
	activeTurnStartedAt := envelope.ActiveTurnStartedAt
	failedToolCalls := envelope.FailedToolCalls
	askPending := envelope.AskPending
	pendingEscalations := envelope.PendingEscalations
	reasoningEffort := envelope.ReasoningEffort
	reasoningEffortLevels := envelope.ReasoningEffortLevels
	supportsReasoning := envelope.SupportsReasoning
	threadName := envelope.Name
	threadPreview := envelope.Preview
	if threadPreview == "" {
		threadPreview = status.SessionID
	}
	return appwire.Thread{
		ID:            threadID,
		SessionID:     status.SessionID,
		Name:          threadName,
		Preview:       threadPreview,
		ModelProvider: status.Model,
		Status:        appwire.ThreadStatus{Type: appStatus(status.State, processing)},
		CWD:           status.WorkingDir,
		Path:          filepath.Base(status.WorkingDir),
		Source:        sourceID,
		Serf: appwire.SerfThread{
			Ref:                   ref,
			Profile:               status.Profile,
			ActiveTurnID:          activeTurnID,
			ContextPressure:       pressure,
			ContextUsed:           metrics.Used,
			ContextWindow:         metrics.Window,
			ContextRemaining:      metrics.Remaining,
			Capabilities:          s.appCapabilities(status.State, processing),
			Diagnostics:           diagnostics,
			Queue:                 queue,
			PendingMutations:      pendingMutations,
			Tasks:                 taskAggregate,
			Goal:                  goalState,
			Usage:                 usage,
			Cost:                  appwire.EstimateCost(status.Model, usage),
			WorkMillis:            workMillis,
			ActiveTurnStartedAt:   activeTurnStartedAt,
			FailedToolCalls:       failedToolCalls,
			AskPending:            askPending,
			PendingEscalations:    pendingEscalations,
			ReasoningEffort:       reasoningEffort,
			ReasoningEffortLevels: reasoningEffortLevels,
			SupportsReasoning:     supportsReasoning,
		},
	}
}

func appDiagnosticsFromDetailedStatus(ds DetailedStatus) *appwire.SerfDiagnostics {
	out := &appwire.SerfDiagnostics{
		Hooks: make(map[string]int, len(ds.Hooks)),
	}
	for _, tool := range ds.Tools {
		out.Tools = append(out.Tools, appwire.SerfToolInfo{Name: tool.Name, Source: tool.Source})
	}
	for _, srv := range ds.MCP {
		out.MCP = append(out.MCP, appwire.SerfMCPServerInfo{Name: srv.Name, Tools: append([]string(nil), srv.Tools...), Status: srv.Status, Error: srv.Error})
	}
	for _, skill := range ds.Skills {
		out.Skills = append(out.Skills, appwire.SerfSkillInfo{Name: skill.Name, Description: skill.Description})
	}
	for _, plugin := range ds.Plugins {
		out.Plugins = append(out.Plugins, appwire.SerfPluginInfo{
			Name:       plugin.Name,
			Version:    plugin.Version,
			SkillCount: plugin.SkillCount,
			AgentCount: plugin.AgentCount,
			HookCount:  plugin.HookCount,
			MCPCount:   plugin.MCPCount,
		})
	}
	maps.Copy(out.Hooks, ds.Hooks)
	for _, job := range ds.Jobs {
		out.Jobs = append(out.Jobs, appwire.SerfJobInfo{
			JobID:            job.JobID,
			JobType:          job.JobType,
			Status:           job.Status,
			Reason:           job.Reason,
			ExhaustionBudget: job.ExhaustionBudget,
			ExhaustionLimit:  job.ExhaustionLimit,
			Resumable:        job.Resumable,
			ExitCode:         job.ExitCode,
			OutputBytes:      job.OutputBytes,
			TranscriptRef:    job.TranscriptRef,
		})
	}
	out.Agents = append(out.Agents, ds.Agents...)
	return out
}

func (s *Server) appCapabilities(state string, processing bool) appwire.ThreadCapabilities {
	s.mu.RLock()
	defer s.mu.RUnlock()
	closed := appStatus(state, processing) == appwire.ThreadStatusClosed
	active := processing || strings.TrimSpace(s.appReservedTurnID) != ""
	steerAvailable := s.steerFunc != nil || s.steerWithImagesFunc != nil
	return appwire.ThreadCapabilities{
		Send:         !active && !closed,
		Steer:        steerAvailable && active && !closed,
		Interrupt:    s.cancelFunc != nil,
		Compact:      s.compactFunc != nil && !closed,
		Clear:        false,
		ForkFromTurn: false,
		Shutdown:     s.shutdownFunc != nil,
		ChangeModel:  s.modelFunc != nil && !closed,
		Rename:       s.nameFunc != nil && !closed,
		// Queue mirrors Steer's "active turn" gate: only meaningful while
		// a turn is in flight or reserved by turn/start (kata 111a).
		Queue: s.queueFunc != nil && active && !closed,
		// Goal is available whenever the engine is wired and the session is
		// open. It is intentionally NOT gated on !active: a goal may be set
		// mid-turn (it arms for the next continuation), unlike Send.
		Goal: s.goalFunc != nil && !closed,
	}
}

func (s *Server) ensureAppProjectorLocked(threadID string) {
	if s.appProjector != nil {
		return
	}
	if threadID == "" {
		threadID = s.appThreadID
	}
	ref := appwire.Ref{SourceID: s.appSourceID, ThreadID: threadID}.String()
	s.appProjector = appprojector.NewAppEventProjector(threadID, ref)
}

func (s *Server) reserveAppTurnIDForStart() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	processing := s.processing
	closed := appStatus(s.status.State, processing) == appwire.ThreadStatusClosed
	if closed {
		return "", appwire.Conflict("session is closed")
	}
	if processing || strings.TrimSpace(s.appReservedTurnID) != "" {
		return "", appwire.Conflict("session is processing")
	}
	s.ensureAppProjectorLocked("")
	turnID := s.appProjector.ReserveTurnID()
	s.appActiveTurnID = turnID
	s.appReservedTurnID = turnID
	return turnID, nil
}

func (s *Server) releaseAppTurnID(turnID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.appProjector != nil {
		s.appProjector.ReleaseReservedTurnID(turnID)
		s.appActiveTurnID = s.appProjector.ActiveTurnID()
	}
	if s.appReservedTurnID == turnID {
		s.appReservedTurnID = ""
	}
}

func appStatus(state string, processing bool) string {
	if processing {
		return appwire.ThreadStatusActive
	}
	switch strings.TrimSpace(state) {
	case appwire.ThreadStatusIdle:
		return appwire.ThreadStatusIdle
	case appwire.ThreadStatusActive:
		return appwire.ThreadStatusActive
	case appwire.ThreadStatusAwaiting:
		return appwire.ThreadStatusAwaiting
	case appwire.ThreadStatusWarning:
		return appwire.ThreadStatusWarning
	case appwire.ThreadStatusSystemError:
		return appwire.ThreadStatusSystemError
	case appwire.ThreadStatusClosed:
		return appwire.ThreadStatusClosed
	case appwire.ThreadStatusNotLoaded:
		return appwire.ThreadStatusNotLoaded
	default:
		return appwire.ThreadStatusIdle
	}
}

func inputFromItems(prompt string, items []appwire.InputItem) (string, []ImageAttachment) {
	parts := []string{}
	if strings.TrimSpace(prompt) != "" {
		parts = append(parts, prompt)
	}
	images := make([]ImageAttachment, 0)
	for _, item := range items {
		switch item.Type {
		case "text", "input_text", "":
			if strings.TrimSpace(item.Text) != "" {
				parts = append(parts, item.Text)
			}
		case "image", "input_image":
			images = append(images, ImageAttachment{
				MediaType: item.MediaType,
				Data:      item.Data,
				Name:      item.Name,
			})
		}
	}
	return strings.Join(parts, "\n"), images
}
