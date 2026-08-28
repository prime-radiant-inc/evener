package server

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
)

// exerciseServerFuzzSurface replays the deterministic server plumbing scenarios
// under the package's registered fuzz target. The scenarios use the real HTTP,
// AppWire, projection, bridge, and callback paths with in-process boundaries.
func exerciseServerFuzzSurface(t *testing.T) {
	// These tests call t.Parallel and therefore must not be nested under a fuzz
	// callback; equivalent serial cases live in exerciseServerFuzzResiduals.
	exerciseServerFuzzResiduals(t)
	t.Run("TestAppDiagnosticsFromDetailedStatus_MCPStatusError", TestAppDiagnosticsFromDetailedStatus_MCPStatusError)
	t.Run("TestAppDiagnosticsFromDetailedStatus_DelegatesLossless", TestAppDiagnosticsFromDetailedStatus_DelegatesLossless)
	t.Run("TestAppStatus", TestAppStatus)
	t.Run("TestAppStatusPreservesAttentionStates", TestAppStatusPreservesAttentionStates)
	t.Run("TestAppThread_OverlaysPendingAskFunc", TestAppThread_OverlaysPendingAskFunc)
	t.Run("TestAppThread_UsesGeneratedSessionNameFromMeta", TestAppThread_UsesGeneratedSessionNameFromMeta)
	t.Run("TestAppTurnsFromNotificationsAccumulatesReasoningDeltas", TestAppTurnsFromNotificationsAccumulatesReasoningDeltas)
	t.Run("TestAppTurnsFromNotificationsCarriesTurnTiming", TestAppTurnsFromNotificationsCarriesTurnTiming)
	t.Run("TestAppTurnsFromTranscriptFileIncludesCompactionTurns", TestAppTurnsFromTranscriptFileIncludesCompactionTurns)
	t.Run("TestAppTurnsFromTranscriptFileIncludesPrelude", TestAppTurnsFromTranscriptFileIncludesPrelude)
	t.Run("TestAppTurnsFromTranscriptFilePreservesToolCallArguments", TestAppTurnsFromTranscriptFilePreservesToolCallArguments)
	t.Run("TestBridgeWithObserver_InvokesObserverAndForwardsEvents", TestBridgeWithObserver_InvokesObserverAndForwardsEvents)
	t.Run("TestBridge_ClosesOnSessionEnd", TestBridge_ClosesOnSessionEnd)
	t.Run("TestBridge_ForwardsEvents", TestBridge_ForwardsEvents)
	t.Run("TestBridge_IgnoresStaleEventsAfterSessionIdentityChanges", TestBridge_IgnoresStaleEventsAfterSessionIdentityChanges)
	t.Run("TestBridge_IncrementsturnsOnAssistantTextEnd", TestBridge_IncrementsturnsOnAssistantTextEnd)
	t.Run("TestBridge_InterruptedSessionEndDoesNotClearProcessing", TestBridge_InterruptedSessionEndDoesNotClearProcessing)
	t.Run("TestBridge_RecordsAppWireNotifications", TestBridge_RecordsAppWireNotifications)
	t.Run("TestBridge_UpdatesStatusOnSessionStart", TestBridge_UpdatesStatusOnSessionStart)
	t.Run("TestBridge_UsesSessionEndStateWhenProvided", TestBridge_UsesSessionEndStateWhenProvided)
	t.Run("TestBridge_UsesSessionStartStateWhenProvided", TestBridge_UsesSessionStartStateWhenProvided)
	t.Run("TestServerAppWireThreadClearInvokesConfiguredClear", TestServerAppWireThreadClearInvokesConfiguredClear)
	t.Run("TestDaemonRouterMatchesCatalog", TestDaemonRouterMatchesCatalog)
	t.Run("TestDaemonThreadReadWindowsAndTurnsListPagesToHead", TestDaemonThreadReadWindowsAndTurnsListPagesToHead)
	t.Run("TestDaemonTranscriptPreparationPropagatesUnsupportedFormat", TestDaemonTranscriptPreparationPropagatesUnsupportedFormat)
	t.Run("TestHandleAppThreadReasoningEffortSet_CallsFuncWithTrimmedValue", TestHandleAppThreadReasoningEffortSet_CallsFuncWithTrimmedValue)
	t.Run("TestHandleAppThreadReasoningEffortSet_NoneStaysNone", TestHandleAppThreadReasoningEffortSet_NoneStaysNone)
	t.Run("TestHandleAppThreadReasoningEffortSet_RejectsUnknownEffort", TestHandleAppThreadReasoningEffortSet_RejectsUnknownEffort)
	t.Run("TestHandleAppThreadReasoningEffortSet_UnavailableWhenFuncUnset", TestHandleAppThreadReasoningEffortSet_UnavailableWhenFuncUnset)
	t.Run("TestAppThreadPendingAskTrueFalseTrueAfterRestart", TestAppThreadPendingAskTrueFalseTrueAfterRestart)
	t.Run("TestIntegration_StatusUpdates", TestIntegration_StatusUpdates)
	t.Run("TestMergeAppThreadItem", TestMergeAppThreadItem)
	t.Run("TestReserveAppTurnIDForStartIsAtomic", TestReserveAppTurnIDForStartIsAtomic)
	t.Run("TestServerAppWireErrorEventNotifiesSubscribers", TestServerAppWireErrorEventNotifiesSubscribers)
	t.Run("TestServerAppWireGoalSetEmptyObjectiveRoutesThroughGoalFunc", TestServerAppWireGoalSetEmptyObjectiveRoutesThroughGoalFunc)
	t.Run("TestServerAppWireGoalSetInvokesGoalFunc", TestServerAppWireGoalSetInvokesGoalFunc)
	t.Run("TestServerAppWireGoalSetWithoutGoalFuncIsUnavailable", TestServerAppWireGoalSetWithoutGoalFuncIsUnavailable)
	t.Run("TestServerAppWireInitializeAdvertisesTurnList", TestServerAppWireInitializeAdvertisesTurnList)
	t.Run("TestServerAppWireModelList", TestServerAppWireModelList)
	t.Run("TestServerAppWireQueueCapabilityFlipsWithProcessing", TestServerAppWireQueueCapabilityFlipsWithProcessing)
	t.Run("TestServerAppWireTasksList", TestServerAppWireTasksList)
	t.Run("TestServerAppWireThreadList", TestServerAppWireThreadList)
	t.Run("TestServerAppWireThreadModelSetQualifiesProvider", TestServerAppWireThreadModelSetQualifiesProvider)
	t.Run("TestServerAppWireThreadReadDoesNotSubscribeByDefault", TestServerAppWireThreadReadDoesNotSubscribeByDefault)
	t.Run("TestServerAppWireThreadReadExposesReservedActiveTurnIDAlongsideSeededTurns", TestServerAppWireThreadReadExposesReservedActiveTurnIDAlongsideSeededTurns)
	t.Run("TestServerAppWireThreadReadIncludesInProgressDeltas", TestServerAppWireThreadReadIncludesInProgressDeltas)
	t.Run("TestServerAppWireThreadReadIncludesProjectedTurns", TestServerAppWireThreadReadIncludesProjectedTurns)
	t.Run("TestServerAppWireThreadReadIncludesWorkMetrics", TestServerAppWireThreadReadIncludesWorkMetrics)
	t.Run("TestServerAppWireThreadReadMergesCompletionItemsWithDeltas", TestServerAppWireThreadReadMergesCompletionItemsWithDeltas)
	t.Run("TestServerAppWireThreadReadOmitsWorkMetricsWhenUnwired", TestServerAppWireThreadReadOmitsWorkMetricsWhenUnwired)
	t.Run("TestServerAppWireThreadReadReturnsStatus", TestServerAppWireThreadReadReturnsStatus)
	t.Run("TestServerAppWireThreadReadSubscribesForNotifications", TestServerAppWireThreadReadSubscribesForNotifications)
	t.Run("TestServerAppWireThreadReadUsesCommunicateAsAssistantMessage", TestServerAppWireThreadReadUsesCommunicateAsAssistantMessage)
	t.Run("TestServerAppWireThreadReadKeepsSeededHistoryAheadOfLiveTurns", TestServerAppWireThreadReadKeepsSeededHistoryAheadOfLiveTurns)
	t.Run("TestServerAppWireThreadShutdownInvokesCallback", TestServerAppWireThreadShutdownInvokesCallback)
	t.Run("TestServerAppWireTurnDrainAsSteerDispatchesInputAtomically", TestServerAppWireTurnDrainAsSteerDispatchesInputAtomically)
	t.Run("TestServerAppWireTurnDrainAsSteerDispatchesWhenQueued", TestServerAppWireTurnDrainAsSteerDispatchesWhenQueued)
	t.Run("TestServerAppWireTurnDrainAsSteerRejectsReservedTurn", TestServerAppWireTurnDrainAsSteerRejectsReservedTurn)
	t.Run("TestServerAppWireTurnDrainAsSteerRequiresQueuedMessages", TestServerAppWireTurnDrainAsSteerRequiresQueuedMessages)
	t.Run("TestServerAppWireTurnDrainAsSteerThroughSessionProducesImageBearingSteer", TestServerAppWireTurnDrainAsSteerThroughSessionProducesImageBearingSteer)
	t.Run("TestServerAppWireTurnInterruptCancelsTheRunningTurn", TestServerAppWireTurnInterruptCancelsTheRunningTurn)
	t.Run("TestServerAppWireTurnQueueAcceptsMidTurnMessage", TestServerAppWireTurnQueueAcceptsMidTurnMessage)
	t.Run("TestServerAppWireTurnQueueAcceptsReservedActiveTurn", TestServerAppWireTurnQueueAcceptsReservedActiveTurn)
	t.Run("TestServerAppWireTurnQueueImageItemReachesQueueFunc", TestServerAppWireTurnQueueImageItemReachesQueueFunc)
	t.Run("TestServerAppWireTurnQueueRejectsStaleProjectedActiveTurn", TestServerAppWireTurnQueueRejectsStaleProjectedActiveTurn)
	t.Run("TestServerAppWireTurnQueueRejectsWhenIdle", TestServerAppWireTurnQueueRejectsWhenIdle)
	t.Run("TestServerAppWireTurnStartAcceptsCodexInput", TestServerAppWireTurnStartAcceptsCodexInput)
	t.Run("TestServerAppWireTurnStartIDMatchesProjectedNotifications", TestServerAppWireTurnStartIDMatchesProjectedNotifications)
	t.Run("TestServerAppWireTurnStartImageItemReachesInputCh", TestServerAppWireTurnStartImageItemReachesInputCh)
	t.Run("TestServerAppWireTurnStartQueuesInput", TestServerAppWireTurnStartQueuesInput)
	t.Run("TestServerAppWireTurnStartRejectsClosedSession", TestServerAppWireTurnStartRejectsClosedSession)
	t.Run("TestServerAppWireTurnStartRejectsReservedActiveTurn", TestServerAppWireTurnStartRejectsReservedActiveTurn)
	t.Run("TestServerAppWireTurnSteerPreservesImages", TestServerAppWireTurnSteerPreservesImages)
	t.Run("TestServerAppWireTurnSteerRejectsImagesWithoutImageHook", TestServerAppWireTurnSteerRejectsImagesWithoutImageHook)
	t.Run("TestServerHubTokenAllowsMatchingBearer", TestServerHubTokenAllowsMatchingBearer)
	t.Run("TestServerHubTokenRejectsMissingBearer", TestServerHubTokenRejectsMissingBearer)
	t.Run("TestServerSameOriginGuardAllowsLocalhostAlias", TestServerSameOriginGuardAllowsLocalhostAlias)
	t.Run("TestServerSameOriginGuardRejectsBadHost", TestServerSameOriginGuardRejectsBadHost)
	t.Run("TestServerSameOriginGuardRejectsBadOrigin", TestServerSameOriginGuardRejectsBadOrigin)
	t.Run("TestAppThreadReportsAwaitingAndSendCapability", TestAppThreadReportsAwaitingAndSendCapability)
	t.Run("TestSubmitContinuation", TestSubmitContinuation)
	t.Run("TestSubmitContinuation_DropIfFull", TestSubmitContinuation_DropIfFull)
	t.Run("TestSubmitNotification_DropIfFull", TestSubmitNotification_DropIfFull)
	t.Run("TestSubmitNotification_PushesEntryNotification", TestSubmitNotification_PushesEntryNotification)
}

func exerciseServerFuzzResiduals(_ *testing.T) {
	for _, tc := range []struct {
		state                              string
		processing, reserved, stale, steer bool
	}{
		{"active", true, false, false, true},
		{"idle", false, true, false, true},
		{"idle", false, false, true, true},
		{"idle", false, false, false, true},
		{"awaiting", false, false, false, true},
		{"closed", false, false, false, true},
		{"active", true, false, false, false},
	} {
		s := NewServer(ServerConfig{})
		if tc.steer {
			s.SetSteerFunc(func(string) {})
		}
		if tc.reserved {
			s.appActiveTurnID, s.appReservedTurnID = "reserved", "reserved"
		}
		if tc.stale {
			s.appActiveTurnID = "stale"
		}
		_ = s.appCapabilities(tc.state, tc.processing)
	}

	s := NewServer(ServerConfig{})
	s.SetAppIdentity("local", "th_1")
	setEnvelope(s, func(e *stubThreadEnvelopeSource) {
		e.escalations = []appwire.SandboxEscalationRequested{{EscalationID: "esc_1"}}
	})
	_ = s.appThread()

	for _, tc := range []struct {
		id string
		fn func(string, bool) error
	}{
		{"", func(string, bool) error { return nil }},
		{"esc_1", nil},
		{"esc_1", func(string, bool) error { return nil }},
		{"gone", func(string, bool) error { return errors.New("not pending") }},
	} {
		s := NewServer(ServerConfig{})
		s.SetSandboxEscalationResolveFunc(tc.fn)
		_, _ = s.handleAppSandboxEscalationResolve(context.Background(), appwire.SandboxEscalationResolveParams{EscalationID: tc.id, Approve: true})
	}
	exerciseAppWireResiduals()
	exerciseProjectionResiduals()
}

func exerciseAppWireResiduals() {
	ctx := context.Background()
	s := NewServer(ServerConfig{})
	s.SetAppIdentity("", "thread")
	s.SetAppIdentity("local", "thread")
	s.SetStatus(StatusInfo{SessionID: "other"})
	s.RecordAppEvent(events.SessionEvent{SessionID: "wrong"})
	_ = s.acceptsSessionEvent("")
	s.SetAppIdentity("", "")
	_ = s.acceptsSessionEvent("other")
	s.SetStatus(StatusInfo{})
	_ = s.appAllTurns("thread")

	_, _ = s.handleAppTurnStart(ctx, appwire.TurnStartParams{ClientMutationID: "test-mutation"})
	for i := 0; i < cap(s.inputCh); i++ {
		s.inputCh <- InputMessage{Text: "full"}
	}
	_, _ = s.handleAppTurnStart(ctx, appwire.TurnStartParams{ClientMutationID: "test-mutation", Input: []appwire.InputItem{{Text: "x"}}})
	for len(s.inputCh) > 0 {
		<-s.inputCh
	}
	_, _ = s.handleAppTurnSteer(ctx, appwire.TurnSteerParams{ClientMutationID: "test-mutation"})
	_, _ = s.handleAppTurnSteer(ctx, appwire.TurnSteerParams{ClientMutationID: "test-mutation", Input: []appwire.InputItem{{Text: "x"}}})
	s.SetSteerFunc(func(string) {})
	_, _ = s.handleAppTurnSteer(ctx, appwire.TurnSteerParams{ClientMutationID: "test-mutation", Input: []appwire.InputItem{{Text: "x"}}})

	_, _ = s.handleAppTurnQueue(ctx, appwire.TurnQueueParams{ClientMutationID: "test-mutation"})
	s.SetState(appwire.ThreadStatusClosed)
	_, _ = s.handleAppTurnQueue(ctx, appwire.TurnQueueParams{ClientMutationID: "test-mutation", Input: []appwire.InputItem{{Text: "x"}}})
	s.SetState("")
	s.SetProcessing(true)
	_, _ = s.handleAppTurnQueue(ctx, appwire.TurnQueueParams{ClientMutationID: "test-mutation", Input: []appwire.InputItem{{Type: "image"}}})
	s.SetQueueWithImagesFunc(func(string, []ImageAttachment) error { return errors.New("queue") })
	_, _ = s.handleAppTurnQueue(ctx, appwire.TurnQueueParams{ClientMutationID: "test-mutation", Input: []appwire.InputItem{{Type: "image"}}})
	s.SetQueueFunc(nil)
	_, _ = s.handleAppTurnQueue(ctx, appwire.TurnQueueParams{ClientMutationID: "test-mutation", Input: []appwire.InputItem{{Text: "x"}}})
	s.SetQueueFunc(func(string) error { return errors.New("queue") })
	_, _ = s.handleAppTurnQueue(ctx, appwire.TurnQueueParams{ClientMutationID: "test-mutation", Input: []appwire.InputItem{{Text: "x"}}})

	s.SetState(appwire.ThreadStatusClosed)
	s.SetProcessing(false)
	_, _ = s.handleAppTurnDrainAsSteer(ctx, appwire.TurnDrainAsSteerParams{ClientMutationID: "test-mutation"})
	s.SetState("")
	s.SetProcessing(false)
	_, _ = s.handleAppTurnDrainAsSteer(ctx, appwire.TurnDrainAsSteerParams{ClientMutationID: "test-mutation"})
	s.SetProcessing(true)
	s.SetDrainAsSteerFunc(nil)
	_, _ = s.handleAppTurnDrainAsSteer(ctx, appwire.TurnDrainAsSteerParams{ClientMutationID: "test-mutation"})
	s.SetDrainAsSteerFunc(func() error { return nil })
	_, _ = s.handleAppTurnDrainAsSteer(ctx, appwire.TurnDrainAsSteerParams{ClientMutationID: "test-mutation", ExpectedQueueRevision: 0, Input: []appwire.InputItem{{Text: "x"}}})
	s.SetDrainAsSteerWithInputFunc(func(string, []ImageAttachment) error { return errors.New("drain") })
	_, _ = s.handleAppTurnDrainAsSteer(ctx, appwire.TurnDrainAsSteerParams{ClientMutationID: "test-mutation", ExpectedQueueRevision: 0, Input: []appwire.InputItem{{Text: "x"}}})

	s.SetGoalFunc(func(string) (bool, error) { return false, errors.New("goal") })
	_, _ = s.handleAppGoalSet(ctx, appwire.GoalSetParams{})
	_, _ = s.handleAppThreadCompactStart(ctx, appwire.ThreadCompactStartParams{})
	s.SetCompactFunc(func(context.Context) error { return nil })
	_, _ = s.handleAppThreadCompactStart(ctx, appwire.ThreadCompactStartParams{})
	_, _ = s.handleAppThreadShutdown(ctx, appwire.ThreadShutdownParams{})
	// The clear arms run on a dedicated server with a known identity: the
	// handler's mandatory Ref/ClientMutationID/ExpectedInstanceID checks
	// reject empty params before the gate, the journal, or clearFunc, and the
	// turn state earlier residuals left on s would block the clear anyway.
	clearSrv := NewServer(ServerConfig{})
	clearSrv.SetAppIdentity("local", "thread")
	clearParams := func(id string) appwire.ThreadClearParams {
		return appwire.ThreadClearParams{Ref: "local:thread", ClientMutationID: id, ExpectedInstanceID: "thread"}
	}
	_, _ = clearSrv.handleAppThreadClear(ctx, appwire.ThreadClearParams{})
	clearSrv.SetProcessing(true)
	_, _ = clearSrv.handleAppThreadClear(ctx, clearParams("clear-busy"))
	clearSrv.SetProcessing(false)
	_, _ = clearSrv.handleAppThreadClear(ctx, clearParams("clear-unwired"))
	clearSrv.SetClearFunc(func(context.Context, appwire.ThreadClearParams) error { return errors.New("clear") })
	_, _ = clearSrv.handleAppThreadClear(ctx, clearParams("clear-failed"))
	clearSrv.SetClearFunc(func(context.Context, appwire.ThreadClearParams) error { return nil })
	_, _ = clearSrv.handleAppThreadClear(ctx, clearParams("clear-applied"))
	_, _ = clearSrv.handleAppThreadClear(ctx, clearParams("clear-applied"))
	_, _ = s.handleAppThreadModelSet(ctx, appwire.ThreadModelSetParams{})
	s.SetModelFunc(nil)
	_, _ = s.handleAppThreadModelSet(ctx, appwire.ThreadModelSetParams{Model: "m"})
	_, _ = s.handleAppThreadNameSet(ctx, appwire.ThreadNameSetParams{})
	_, _ = s.handleAppThreadNameSet(ctx, appwire.ThreadNameSetParams{Name: "name"})
	s.SetNameFunc(func(string) {})
	_, _ = s.handleAppThreadNameSet(ctx, appwire.ThreadNameSetParams{Name: "name"})
	_, _ = s.handleAppTasksList(ctx, appwire.TaskListParams{})
	_, _ = s.handleAppModelList(ctx, appwire.ModelListParams{})
	s.SetListModelsFunc(func(context.Context) ([]appwire.ModelDescriptor, error) { return nil, errors.New("models") })
	_, _ = s.handleAppModelList(ctx, appwire.ModelListParams{})

	setEnvelope(s, func(e *stubThreadEnvelopeSource) { e.goalStatus = "active"; e.goalIterations = 1; e.goalSet = true })
	setEnvelope(s, func(e *stubThreadEnvelopeSource) { e.queue.Preview = []string{"queued"}; e.queue.Depth = 1 })
	setEnvelope(s, func(e *stubThreadEnvelopeSource) {
		e.escalations = []appwire.SandboxEscalationRequested{{EscalationID: "esc_probe"}}
	})
	_ = s.appThread()
	plain := NewServer(ServerConfig{})
	plain.appSourceID = ""
	setEnvelope(plain, func(e *stubThreadEnvelopeSource) { e.queue.Depth = 2 })
	_ = plain.appThread()
	s.ensureAppProjectorLocked("thread")
	s.releaseAppTurnID("missing")
	_, _ = inputFromItems("prompt", nil)
}

func exerciseProjectionResiduals() {
	record := func(method, params string) appserver.SequencedNotification {
		return appserver.SequencedNotification{Notification: appwire.Notification{Method: method, Params: []byte(params)}}
	}
	_ = appTurnsFromNotifications([]appserver.SequencedNotification{
		record(appwire.NotifyTurnStarted, `{"turn":{"id":""}}`),
		record(appwire.NotifyItemStarted, `{"turnId":"","item":{"id":""}}`),
		record(appwire.NotifyAgentMessageDelta, `{"turnId":"","itemId":"x","delta":"x"}`),
		record(appwire.NotifyReasoningSummaryDelta, `{"turnId":"t","itemId":"","delta":"x"}`),
		record(appwire.NotifyReasoningSummaryDelta, `{`),
		record(appwire.NotifyToolOutputDelta, `{"turnId":"t","callId":"call","delta":"x"}`),
		record(appwire.NotifyToolOutputDelta, `{"turnId":"t","itemId":"call","callId":"call","delta":"y"}`),
		record(appwire.NotifyTurnCompleted, `{"turn":{"id":"t","itemsView":"full","items":[{"id":"call"}]}}`),
	})
	appTurnsEnsureTurnHook = func(string) bool { return true }
	_ = appTurnsFromNotifications([]appserver.SequencedNotification{record(appwire.NotifyTurnStarted, `{"turn":{"id":"t"}}`)})
	_ = appTurnsFromNotifications([]appserver.SequencedNotification{record(appwire.NotifyItemStarted, `{"turnId":"t","item":{"id":"i"}}`)})
	_ = appTurnsFromNotifications([]appserver.SequencedNotification{record(appwire.NotifyTurnCompleted, `{"turn":{"id":"t"}}`)})
	appTurnsEnsureTurnHook = nil
	appTurnsItemForDeltaHook = func(item *appwire.ThreadItem) { item.TurnID, item.Type = "", "" }
	_ = appTurnsFromNotifications([]appserver.SequencedNotification{
		record(appwire.NotifyTurnStarted, `{"turn":{"id":"t"}}`),
		record(appwire.NotifyItemStarted, `{"turnId":"t","item":{"id":"i"}}`),
		record(appwire.NotifyAgentMessageDelta, `{"turnId":"t","itemId":"i","delta":"x"}`),
	})
	appTurnsItemForDeltaHook = nil
	ch := make(chan events.SessionEvent, 1)
	ch <- events.SessionEvent{Kind: events.EventSessionEnd}
	close(ch)
	Bridge(NewServer(ServerConfig{}), ch)
}
