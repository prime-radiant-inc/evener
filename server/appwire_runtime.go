package server

import (
	"bufio"
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/internal/appprojector"
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/llm"
)

func (s *Server) AppServer() *appserver.Server {
	return s.appServer
}

func (s *Server) SetAppIdentity(sourceID, threadID string) {
	if sourceID == "" {
		sourceID = "local"
	}
	ref := appwire.Ref{SourceID: sourceID, ThreadID: threadID}.String()
	s.appServer.CommitProjection(func() []appserver.SequencedNotification {
		s.mu.Lock()
		s.appSourceID = sourceID
		s.appThreadID = threadID
		s.appIdentityGeneration++
		s.appProjector = appprojector.NewAppEventProjector(threadID, ref)
		s.appTurns = &appTurnSnapshot{threadID: threadID, limit: s.appTurns.limit}
		s.appActiveTurnID = ""
		s.appReservedTurnID = ""
		s.appLastStampedFailedToolCalls = nil
		s.mu.Unlock()
		return nil
	})
}

func (s *Server) SetTranscriptPathFunc(fn func() string) {
	s.mu.Lock()
	s.transcriptPathFn = fn
	s.mu.Unlock()
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
			params = s.stampFailureCountOnItemCompleted(item.Method, params)
			params = stampAppNotificationTarget(params, threadID, ref)
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
	s.mu.RLock()
	fn := s.failedToolCallsFn
	s.mu.RUnlock()
	if fn == nil {
		return params
	}
	count, measured := fn()
	if !measured {
		return params
	}
	status.FailedToolCalls = &count
	return status
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
	s.mu.RLock()
	fn := s.failedToolCallsFn
	s.mu.RUnlock()
	if fn == nil {
		return params
	}
	count, measured := fn()
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
	appserver.HandleTyped(router, appwire.MethodModelList, s.handleAppModelList)
	appserver.HandleTyped(router, appwire.MethodThreadTurnsList, s.handleAppThreadTurnsList)
}

func (s *Server) handleAppThreadList(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	return appwire.ThreadListResponse{Data: []appwire.Thread{s.appThread()}}, nil
}

func (s *Server) handleAppThreadRead(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	if !params.Subscribe {
		return s.appThreadReadSnapshot(params)
	}
	var response appwire.ThreadReadResponse
	var readErr error
	captured := appserver.CaptureSubscription(
		ctx,
		params.ReplaceSubscription,
		s.appProjectionThreadID,
		s.appNotifier.CurrentSequence,
		func() bool {
			response, readErr = s.appThreadReadSnapshot(params)
			return readErr == nil
		},
	)
	if readErr != nil {
		return appwire.ThreadReadResponse{}, readErr
	}
	if !captured {
		return appwire.ThreadReadResponse{}, appwire.SessionUnavailable("thread subscription is unavailable")
	}
	return response, nil
}

func (s *Server) appThreadReadSnapshot(params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	thread := s.appThread()
	var olderCursor string
	if params.IncludeTurns {
		var err error
		if params.TurnLimit <= 0 {
			var turns []appwire.Turn
			turns, err = s.appAllTurns(thread.ID)
			thread.Turns, olderCursor = appwire.WindowTurns(turns, params.TurnLimit)
		} else {
			thread.Turns, olderCursor, err = s.appLatestTurns(thread.ID, params.TurnLimit)
		}
		if err != nil {
			return appwire.ThreadReadResponse{}, err
		}
	}
	return appwire.ThreadReadResponse{Thread: thread, OlderCursor: olderCursor}, nil
}

func (s *Server) appProjectionThreadID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.appThreadID != "" {
		return s.appThreadID
	}
	return s.status.SessionID
}

// appAllTurns materializes the full ordered turn list (oldest-first), choosing
// the transcript-file turns over the notification-derived turns when richer.
func (s *Server) appAllTurns(threadID string) ([]appwire.Turn, error) {
	notificationTurns := appTurnsFromNotifications(s.AppNotificationsAfter(0, threadID))
	transcriptTurns, err := s.appTurnsFromTranscript()
	if err != nil {
		return nil, err
	}
	if useTranscriptTurns(transcriptTurns, notificationTurns) {
		return transcriptTurns, nil
	}
	return notificationTurns, nil
}

func (s *Server) appNotificationTurns(threadID string) ([]appwire.Turn, *appTurnSnapshot) {
	s.mu.RLock()
	snapshot := s.appTurns
	generation := s.appIdentityGeneration
	s.mu.RUnlock()
	return s.appNotificationTurnsForIdentity(threadID, snapshot, generation)
}

func (s *Server) appNotificationTurnsForIdentity(threadID string, snapshot *appTurnSnapshot, generation uint64) ([]appwire.Turn, *appTurnSnapshot) {
	if snapshot == nil || snapshot.threadID != threadID || !s.appReadIdentityCurrent(threadID, snapshot, generation) {
		return nil, snapshot
	}
	for {
		window := s.appNotifier.RetainedWindow(threadID)
		turns := snapshot.ReconcileAndSnapshot(window.LowerSeq, window.Records)
		if !s.appReadIdentityCurrent(threadID, snapshot, generation) {
			return nil, snapshot
		}
		if s.appNotifier.RetainedWindowCurrent(window.UpperSeq) {
			if !s.appReadIdentityCurrent(threadID, snapshot, generation) {
				return nil, snapshot
			}
			return turns, snapshot
		}
	}
}

func (s *Server) appReadIdentity(threadID string) (*appTurnSnapshot, uint64, func() string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.appThreadID != threadID || s.appTurns == nil || s.appTurns.threadID != threadID {
		return nil, 0, nil, false
	}
	return s.appTurns, s.appIdentityGeneration, s.transcriptPathFn, true
}

func (s *Server) appReadIdentityCurrent(threadID string, snapshot *appTurnSnapshot, generation uint64) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.appThreadID == threadID && s.appTurns == snapshot && s.appIdentityGeneration == generation
}

func transcriptPathFrom(fn func() string) string {
	if fn == nil {
		return ""
	}
	return strings.TrimSpace(fn())
}

func validatedTranscriptPath(threadID, path string) string {
	if path == "" {
		return ""
	}
	header := transcriptHeader(path, 128<<20)
	if header.SessionID != "" && header.SessionID != threadID {
		return ""
	}
	return path
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

func (s *Server) transcriptPath() string {
	s.mu.RLock()
	fn := s.transcriptPathFn
	s.mu.RUnlock()
	if fn == nil {
		return ""
	}
	return strings.TrimSpace(fn())
}

func (s *Server) appLatestTurns(threadID string, limit int) ([]appwire.Turn, string, error) {
	if limit <= 0 {
		turns, err := s.appAllTurns(threadID)
		if err != nil {
			return nil, "", err
		}
		turns, cursor := appwire.WindowTurns(turns, limit)
		return turns, cursor, nil
	}
	snapshot, generation, pathFn, ok := s.appReadIdentity(threadID)
	if !ok {
		return nil, "", nil
	}
	notificationTurns, _ := s.appNotificationTurnsForIdentity(threadID, snapshot, generation)
	if !s.appReadIdentityCurrent(threadID, snapshot, generation) {
		return nil, "", nil
	}
	path := validatedTranscriptPath(threadID, transcriptPathFrom(pathFn))
	if !s.appReadIdentityCurrent(threadID, snapshot, generation) {
		return nil, "", nil
	}
	if path == "" {
		turns, cursor := appwire.WindowTurns(notificationTurns, limit)
		return turns, cursor, nil
	}
	transcriptTurns, cursor, err := transcriptTurnCache.LatestFromFile(path, 128<<20, limit, projectBoundedDaemonTranscriptTurn)
	if err != nil {
		return nil, "", err
	}
	if !s.appReadIdentityCurrent(threadID, snapshot, generation) {
		return nil, "", nil
	}
	transcriptCount := len(transcriptTurns)
	if cursor != "" {
		if older, err := strconv.Atoi(cursor); err == nil {
			transcriptCount += older
		}
	}
	authoritative := transcriptCount > 0 && (len(notificationTurns) == 0 || transcriptCount > len(notificationTurns) || notificationTurns[0].ID != "turn_1")
	if authoritative {
		return transcriptTurns, cursor, nil
	}
	turns, cursor := appwire.WindowTurns(notificationTurns, limit)
	return turns, cursor, nil
}

func (s *Server) appPageTurns(threadID, cursor string, limit int) (appwire.ThreadTurnsListResponse, error) {
	if limit <= 0 {
		turns, err := s.appAllTurns(threadID)
		if err != nil {
			return appwire.ThreadTurnsListResponse{}, err
		}
		return appwire.PageTurns(turns, cursor, limit), nil
	}
	snapshot, generation, pathFn, ok := s.appReadIdentity(threadID)
	if !ok {
		return appwire.ThreadTurnsListResponse{}, nil
	}
	notificationTurns, _ := s.appNotificationTurnsForIdentity(threadID, snapshot, generation)
	if !s.appReadIdentityCurrent(threadID, snapshot, generation) {
		return appwire.ThreadTurnsListResponse{}, nil
	}
	path := validatedTranscriptPath(threadID, transcriptPathFrom(pathFn))
	if !s.appReadIdentityCurrent(threadID, snapshot, generation) {
		return appwire.ThreadTurnsListResponse{}, nil
	}
	if path == "" {
		return appwire.PageTurns(notificationTurns, cursor, limit), nil
	}
	page, err := transcriptTurnCache.PageFromFile(path, 128<<20, cursor, limit, projectBoundedDaemonTranscriptTurn)
	if err != nil {
		return appwire.ThreadTurnsListResponse{}, err
	}
	transcriptCount, err := transcriptTurnCache.TurnCountFromFile(path, 128<<20, projectBoundedDaemonTranscriptTurn)
	if err != nil {
		return appwire.ThreadTurnsListResponse{}, err
	}
	if !s.appReadIdentityCurrent(threadID, snapshot, generation) {
		return appwire.ThreadTurnsListResponse{}, nil
	}
	authoritative := transcriptCount > 0 && (len(notificationTurns) == 0 || transcriptCount > len(notificationTurns) || notificationTurns[0].ID != "turn_1")
	if authoritative {
		return appwire.ThreadTurnsListResponse{Data: page.Turns, NextCursor: page.NextCursor}, nil
	}
	return appwire.PageTurns(notificationTurns, cursor, limit), nil
}

// handleAppThreadTurnsList pages turns backward (older) for lazy transcript
// loading. The web client seeds the latest window via thread/read(TurnLimit)
// and walks back with this as the user scrolls up.
func (s *Server) handleAppThreadTurnsList(_ context.Context, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
	return s.appPageTurns(s.appThread().ID, params.Cursor, params.Limit)
}

func (s *Server) appTurnsFromTranscript() ([]appwire.Turn, error) {
	path := s.transcriptPath()
	if path == "" {
		return nil, nil
	}
	return appTurnsFromTranscriptFile(path)
}

func (s *Server) handleAppTurnStart(_ context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
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
	return response, agent.NormalizeClientMutationError(params.ClientMutationID, err)
}

func (s *Server) handleAppTurnSteer(_ context.Context, params appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
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
	return response, agent.NormalizeClientMutationError(params.ClientMutationID, err)
}

func (s *Server) handleAppSandboxEscalationResolve(_ context.Context, params appwire.SandboxEscalationResolveParams) (appwire.EmptyResponse, error) {
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
	return response, agent.NormalizeClientMutationError(params.ClientMutationID, err)
}

func (s *Server) handleAppTurnQueue(_ context.Context, params appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
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
	return response, agent.NormalizeClientMutationError(params.ClientMutationID, err)
}

func (s *Server) handleAppTurnDrainAsSteer(_ context.Context, params appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) {
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
	return response, agent.NormalizeClientMutationError(params.ClientMutationID, err)
}

// handleAppTurnPromoteQueuedAsSteer validates static request shape and leaves
// active-turn and queue compare-and-commit decisions to the Session callback.
func (s *Server) handleAppTurnPromoteQueuedAsSteer(_ context.Context, params appwire.TurnPromoteQueuedAsSteerParams) (appwire.TurnPromoteQueuedAsSteerResponse, error) {
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
	return response, agent.NormalizeClientMutationError(params.ClientMutationID, err)
}

// handleAppTurnCancelQueued validates static request shape and leaves queue
// compare-and-commit decisions to the Session callback.
func (s *Server) handleAppTurnCancelQueued(_ context.Context, params appwire.TurnCancelQueuedParams) (appwire.TurnCancelQueuedResponse, error) {
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
	return response, agent.NormalizeClientMutationError(params.ClientMutationID, err)
}

// handleAppGoalSet handles goal/set. An empty objective clears the goal; both
// set and clear route through the single goalFunc callback (the callback maps an
// empty objective to ClearGoal). Started reports whether the goal loop began
// immediately (idle session) versus after the current turn (a turn is running,
// whose gate is the backstop).
func (s *Server) handleAppGoalSet(_ context.Context, params appwire.GoalSetParams) (appwire.GoalSetResponse, error) {
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
	return appwire.GoalSetResponse{Started: started}, nil
}

func (s *Server) handleAppThreadCompactStart(ctx context.Context, _ appwire.ThreadCompactStartParams) (appwire.EmptyResponse, error) {
	s.mu.RLock()
	fn := s.compactFunc
	s.mu.RUnlock()
	if fn == nil {
		return appwire.EmptyResponse{}, appwire.Unavailable("compact not available")
	}
	return appwire.EmptyResponse{}, fn(ctx)
}

func (s *Server) handleAppThreadShutdown(context.Context, appwire.ThreadShutdownParams) (appwire.EmptyResponse, error) {
	s.mu.RLock()
	fn := s.shutdownFunc
	s.mu.RUnlock()
	if fn == nil {
		return appwire.EmptyResponse{}, appwire.Unavailable("shutdown not available")
	}
	go fn()
	return appwire.EmptyResponse{}, nil
}

func (s *Server) handleAppThreadClear(context.Context, appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
	return appwire.ThreadClearResponse{}, appwire.Unavailable("thread/clear is unavailable in serf-appwire-v2")
}

func (s *Server) handleAppThreadModelSet(_ context.Context, params appwire.ThreadModelSetParams) (appwire.EmptyResponse, error) {
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

func (s *Server) handleAppTasksList(context.Context, appwire.TaskListParams) (appwire.TaskListResponse, error) {
	s.mu.RLock()
	fn := s.tasksFn
	s.mu.RUnlock()
	if fn == nil {
		return appwire.TaskListResponse{}, nil
	}
	return appwire.TaskListResponse{Data: fn()}, nil
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

func (s *Server) appThread() appwire.Thread {
	s.mu.RLock()
	status := s.status
	sourceID := s.appSourceID
	threadID := s.appThreadID
	processing := s.processing
	pfn := s.pressureFn
	cmfn := s.contextMetricsFn
	dfn := s.detailedStatusFn
	qpfn := s.queuePreviewFn
	qdfn := s.queueDepthFn
	qifn := s.queueIDsFn
	qtfn := s.queueTextsFn
	cmpfn := s.clientMutationProjectionFn
	gsfn := s.goalStatusFn
	wmfn := s.workMetricsFn
	ftcfn := s.failedToolCallsFn
	tafn := s.taskAggregateFn
	metafn := s.sessionMetaFn
	pafn := s.pendingAskFn
	pesfn := s.pendingEscalationsSnapshotFn
	rifn := s.reasoningInfoFn
	activeTurnID := s.appActiveTurnID
	s.mu.RUnlock()

	if sourceID == "" {
		sourceID = "local"
	}
	if threadID == "" {
		threadID = status.SessionID
	}
	ref := appwire.Ref{SourceID: sourceID, ThreadID: threadID}.String()
	pressure := status.ContextPressure
	if pfn != nil {
		pressure = pfn()
	}
	metrics := ContextMetrics{
		Used:      status.ContextUsed,
		Window:    status.ContextWindow,
		Remaining: status.ContextRemaining,
	}
	if cmfn != nil {
		metrics = cmfn()
	}
	var diagnostics *appwire.SerfDiagnostics
	if dfn != nil {
		ds := dfn()
		diagnostics = appDiagnosticsFromDetailedStatus(ds)
	}
	queue := appwire.QueueState{}
	var pendingMutations []appwire.PendingMutation
	if cmpfn != nil {
		queue, pendingMutations = cmpfn()
	} else {
		if qpfn != nil {
			if preview := qpfn(); len(preview) > 0 {
				queue.Preview = append([]string(nil), preview...)
				queue.Depth = len(preview)
			}
		}
		if qifn != nil && queue.Depth > 0 {
			if ids := qifn(); len(ids) == queue.Depth {
				queue.IDs = append([]string(nil), ids...)
			}
		}
		if qtfn != nil && queue.Depth > 0 {
			if texts := qtfn(); len(texts) == queue.Depth {
				queue.Texts = append([]string(nil), texts...)
			}
		}
		// Fall back to depthFn when preview isn't wired (some tests stub only
		// the depth callback). Without this we'd silently drop authoritative
		// depth information.
		if queue.Depth == 0 && qdfn != nil {
			queue.Depth = qdfn()
		}
	}
	var goalState *appwire.GoalState
	if gsfn != nil {
		if status, iterations, ok := gsfn(); ok {
			goalState = &appwire.GoalState{Status: status, Iterations: iterations}
		}
	}
	var taskAggregate *appwire.TaskAggregate
	if tafn != nil {
		taskAggregate = tafn()
	}
	var workMillis int64
	var usage *appwire.SerfUsage
	var activeTurnStartedAt int64
	if wmfn != nil {
		workMillis, usage, activeTurnStartedAt = wmfn()
	}
	// The live session's own running failure count. Absent unless the daemon
	// actually measured it — the strip must not vouch for a session nobody
	// counted, and an unmeasured zero would read as "nothing went wrong".
	failedToolCalls := status.FailedToolCalls
	if ftcfn != nil {
		if count, measured := ftcfn(); measured {
			failedToolCalls = &count
		} else {
			failedToolCalls = nil
		}
	}
	askPending := status.PendingAsk
	if pafn != nil {
		askPending = pafn()
	}
	var pendingEscalations []appwire.SandboxEscalationRequested
	if pesfn != nil {
		pendingEscalations = pesfn()
		// Stamp each snapshot card with this thread's identifiers so the client can
		// route it exactly like a live notification.
		for i := range pendingEscalations {
			pendingEscalations[i].ThreadID = threadID
			pendingEscalations[i].Ref = ref
		}
	}
	var reasoningEffort string
	var reasoningEffortLevels []string
	var supportsReasoning bool
	if rifn != nil {
		reasoningEffort, reasoningEffortLevels, supportsReasoning = rifn()
	}
	var meta schema.SessionMeta
	if metafn != nil {
		meta = metafn()
	}
	threadName := strings.TrimSpace(meta.Name)
	threadPreview := strings.TrimSpace(schema.SessionDisplayName(meta))
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
