package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/internal/appserver"
)

// exerciseServerFuzzSurface replays the deterministic server plumbing scenarios
// under the package's registered fuzz target. The scenarios use the real HTTP,
// AppWire, projection, bridge, and callback paths with in-process boundaries.
func exerciseServerFuzzSurface(t *testing.T) {
	// These tests call t.Parallel and therefore must not be nested under a fuzz
	// callback; equivalent serial cases live in exerciseServerFuzzResiduals.
	exerciseServerFuzzResiduals(t)
	t.Run("TestAppDiagnosticsFromDetailedStatus_MCPStatusError", TestAppDiagnosticsFromDetailedStatus_MCPStatusError)
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
	t.Run("TestClearEndpoint", TestClearEndpoint)
	t.Run("TestClearEndpoint_FuncError", TestClearEndpoint_FuncError)
	t.Run("TestClearEndpoint_NoFunc", TestClearEndpoint_NoFunc)
	t.Run("TestClear_409WhileProcessing", TestClear_409WhileProcessing)
	t.Run("TestClear_OKWhenIdle", TestClear_OKWhenIdle)
	t.Run("TestCompactEndpoint", TestCompactEndpoint)
	t.Run("TestCompactEndpoint_Error", TestCompactEndpoint_Error)
	t.Run("TestCompactEndpoint_MethodNotAllowed", TestCompactEndpoint_MethodNotAllowed)
	t.Run("TestCompactEndpoint_NoFunc", TestCompactEndpoint_NoFunc)
	t.Run("TestDaemonRouterMatchesCatalog", TestDaemonRouterMatchesCatalog)
	t.Run("TestDaemonThreadReadWindowsAndTurnsListPagesToHead", TestDaemonThreadReadWindowsAndTurnsListPagesToHead)
	t.Run("TestDrainAsSteerEndpoint_ClosedSession", TestDrainAsSteerEndpoint_ClosedSession)
	t.Run("TestDrainAsSteerEndpoint_InvalidJSON", TestDrainAsSteerEndpoint_InvalidJSON)
	t.Run("TestDrainAsSteerEndpoint_NoContent", TestDrainAsSteerEndpoint_NoContent)
	t.Run("TestDrainAsSteerEndpoint_NoFunc", TestDrainAsSteerEndpoint_NoFunc)
	t.Run("TestDrainAsSteerEndpoint_RejectsEmpty", TestDrainAsSteerEndpoint_RejectsEmpty)
	t.Run("TestDrainAsSteerEndpoint_RejectsWhenIdle", TestDrainAsSteerEndpoint_RejectsWhenIdle)
	t.Run("TestDrainAsSteerEndpoint_WithInputBypassesEmptyQueue", TestDrainAsSteerEndpoint_WithInputBypassesEmptyQueue)
	t.Run("TestHandleAppThreadReasoningEffortSet_CallsFuncWithTrimmedValue", TestHandleAppThreadReasoningEffortSet_CallsFuncWithTrimmedValue)
	t.Run("TestHandleAppThreadReasoningEffortSet_NoneNormalizesToEmpty", TestHandleAppThreadReasoningEffortSet_NoneNormalizesToEmpty)
	t.Run("TestHandleAppThreadReasoningEffortSet_RejectsUnknownEffort", TestHandleAppThreadReasoningEffortSet_RejectsUnknownEffort)
	t.Run("TestHandleAppThreadReasoningEffortSet_UnavailableWhenFuncUnset", TestHandleAppThreadReasoningEffortSet_UnavailableWhenFuncUnset)
	t.Run("TestHandleStatus_PendingAskOverlaysLiveFunc", TestHandleStatus_PendingAskOverlaysLiveFunc)
	t.Run("TestHandleStatus_PendingAskTrueFalseTrueAfterRestart", TestHandleStatus_PendingAskTrueFalseTrueAfterRestart)
	t.Run("TestInputEndpoint_Accepted", TestInputEndpoint_Accepted)
	t.Run("TestInputEndpoint_ClosedSessionConflict", TestInputEndpoint_ClosedSessionConflict)
	t.Run("TestInputEndpoint_Conflict", TestInputEndpoint_Conflict)
	t.Run("TestInputEndpoint_EmptyTextAndNoImages", TestInputEndpoint_EmptyTextAndNoImages)
	t.Run("TestInputEndpoint_FullChannel", TestInputEndpoint_FullChannel)
	t.Run("TestInputEndpoint_ImageOnly", TestInputEndpoint_ImageOnly)
	t.Run("TestInputEndpoint_InvalidJSON", TestInputEndpoint_InvalidJSON)
	t.Run("TestInputEndpoint_TextAndImage", TestInputEndpoint_TextAndImage)
	t.Run("TestIntegration_InputToAppwire", TestIntegration_InputToAppwire)
	t.Run("TestIntegration_StatusUpdates", TestIntegration_StatusUpdates)
	t.Run("TestInterruptEndpoint", TestInterruptEndpoint)
	t.Run("TestInterruptEndpoint_MethodNotAllowed", TestInterruptEndpoint_MethodNotAllowed)
	t.Run("TestInterruptEndpoint_NoCancelFunc", TestInterruptEndpoint_NoCancelFunc)
	t.Run("TestMergeAppThreadItem", TestMergeAppThreadItem)
	t.Run("TestModelEndpoint", TestModelEndpoint)
	t.Run("TestModelEndpoint_EmptyModel", TestModelEndpoint_EmptyModel)
	t.Run("TestModelEndpoint_InvalidJSON", TestModelEndpoint_InvalidJSON)
	t.Run("TestModelEndpoint_NoFunc", TestModelEndpoint_NoFunc)
	t.Run("TestModelsEndpoint", TestModelsEndpoint)
	t.Run("TestModelsEndpoint_Error", TestModelsEndpoint_Error)
	t.Run("TestModelsEndpoint_MethodNotAllowed", TestModelsEndpoint_MethodNotAllowed)
	t.Run("TestModelsEndpoint_NoFunc", TestModelsEndpoint_NoFunc)
	t.Run("TestQueueEndpoint_Accepted", TestQueueEndpoint_Accepted)
	t.Run("TestQueueEndpoint_FuncError", TestQueueEndpoint_FuncError)
	t.Run("TestQueueEndpoint_InvalidJSON", TestQueueEndpoint_InvalidJSON)
	t.Run("TestQueueEndpoint_NoFunc", TestQueueEndpoint_NoFunc)
	t.Run("TestQueueEndpoint_RejectsEmptyText", TestQueueEndpoint_RejectsEmptyText)
	t.Run("TestQueueEndpoint_RejectsWhenIdle", TestQueueEndpoint_RejectsWhenIdle)
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
	t.Run("TestServerAppWireThreadReadExposesActiveTurnIDWhenTranscriptWins", TestServerAppWireThreadReadExposesActiveTurnIDWhenTranscriptWins)
	t.Run("TestServerAppWireThreadReadIncludesInProgressDeltas", TestServerAppWireThreadReadIncludesInProgressDeltas)
	t.Run("TestServerAppWireThreadReadIncludesProjectedTurns", TestServerAppWireThreadReadIncludesProjectedTurns)
	t.Run("TestServerAppWireThreadReadIncludesWorkMetrics", TestServerAppWireThreadReadIncludesWorkMetrics)
	t.Run("TestServerAppWireThreadReadMergesCompletionItemsWithDeltas", TestServerAppWireThreadReadMergesCompletionItemsWithDeltas)
	t.Run("TestServerAppWireThreadReadOmitsWorkMetricsWhenUnwired", TestServerAppWireThreadReadOmitsWorkMetricsWhenUnwired)
	t.Run("TestServerAppWireThreadReadReturnsStatus", TestServerAppWireThreadReadReturnsStatus)
	t.Run("TestServerAppWireThreadReadSubscribesForNotifications", TestServerAppWireThreadReadSubscribesForNotifications)
	t.Run("TestServerAppWireThreadReadUsesCommunicateAsAssistantMessage", TestServerAppWireThreadReadUsesCommunicateAsAssistantMessage)
	t.Run("TestServerAppWireThreadReadUsesTranscriptWhenReplayBufferDroppedPrefix", TestServerAppWireThreadReadUsesTranscriptWhenReplayBufferDroppedPrefix)
	t.Run("TestServerAppWireThreadShutdownInvokesCallback", TestServerAppWireThreadShutdownInvokesCallback)
	t.Run("TestServerAppWireTurnDrainAsSteerDispatchesInputAtomically", TestServerAppWireTurnDrainAsSteerDispatchesInputAtomically)
	t.Run("TestServerAppWireTurnDrainAsSteerDispatchesWhenQueued", TestServerAppWireTurnDrainAsSteerDispatchesWhenQueued)
	t.Run("TestServerAppWireTurnDrainAsSteerRejectsReservedTurn", TestServerAppWireTurnDrainAsSteerRejectsReservedTurn)
	t.Run("TestServerAppWireTurnDrainAsSteerRequiresQueuedMessages", TestServerAppWireTurnDrainAsSteerRequiresQueuedMessages)
	t.Run("TestServerAppWireTurnDrainAsSteerThroughSessionProducesImageBearingSteer", TestServerAppWireTurnDrainAsSteerThroughSessionProducesImageBearingSteer)
	t.Run("TestServerAppWireTurnInterruptRequiresActiveTurnID", TestServerAppWireTurnInterruptRequiresActiveTurnID)
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
	t.Run("TestServerAppWireTurnSteerRejectsMismatchedTurnID", TestServerAppWireTurnSteerRejectsMismatchedTurnID)
	t.Run("TestServerAppWireTurnSteerRequiresTurnID", TestServerAppWireTurnSteerRequiresTurnID)
	t.Run("TestServerHubTokenAllowsMatchingBearer", TestServerHubTokenAllowsMatchingBearer)
	t.Run("TestServerHubTokenRejectsMissingBearer", TestServerHubTokenRejectsMissingBearer)
	t.Run("TestServerSameOriginGuardAllowsLocalhostAlias", TestServerSameOriginGuardAllowsLocalhostAlias)
	t.Run("TestServerSameOriginGuardRejectsBadHost", TestServerSameOriginGuardRejectsBadHost)
	t.Run("TestServerSameOriginGuardRejectsBadOrigin", TestServerSameOriginGuardRejectsBadOrigin)
	t.Run("TestShutdown_503WhenUnregistered", TestShutdown_503WhenUnregistered)
	t.Run("TestShutdown_InvokesCallback", TestShutdown_InvokesCallback)
	t.Run("TestShutdown_RejectsGET", TestShutdown_RejectsGET)
	t.Run("TestShutdown_WritesResponseBeforeCallback", TestShutdown_WritesResponseBeforeCallback)
	t.Run("TestStatusCapabilities_QueueGatedByProcessing", TestStatusCapabilities_QueueGatedByProcessing)
	t.Run("TestStatusEndpoint_ContextPressure", TestStatusEndpoint_ContextPressure)
	t.Run("TestStatusEndpoint_DetailedStatus", TestStatusEndpoint_DetailedStatus)
	t.Run("TestStatusEndpoint_Idle", TestStatusEndpoint_Idle)
	t.Run("TestStatusEndpoint_MethodNotAllowed", TestStatusEndpoint_MethodNotAllowed)
	t.Run("TestStatusEndpoint_NoDetailedStatusFunc", TestStatusEndpoint_NoDetailedStatusFunc)
	t.Run("TestStatusEndpoint_WorkMetrics", TestStatusEndpoint_WorkMetrics)
	t.Run("TestStatusReportsAwaitingAndSendCapability", TestStatusReportsAwaitingAndSendCapability)
	t.Run("TestStatus_IncludesWorkingDir", TestStatus_IncludesWorkingDir)
	t.Run("TestSteerEndpoint", TestSteerEndpoint)
	t.Run("TestSteerEndpoint_EmptyText", TestSteerEndpoint_EmptyText)
	t.Run("TestSteerEndpoint_MethodNotAllowed", TestSteerEndpoint_MethodNotAllowed)
	t.Run("TestSteerEndpoint_NoFunc", TestSteerEndpoint_NoFunc)
	t.Run("TestSubmitContinuation", TestSubmitContinuation)
	t.Run("TestSubmitContinuation_DropIfFull", TestSubmitContinuation_DropIfFull)
	t.Run("TestSubmitNotification_DropIfFull", TestSubmitNotification_DropIfFull)
	t.Run("TestSubmitNotification_PushesEntryNotification", TestSubmitNotification_PushesEntryNotification)
	t.Run("TestTasksEndpoint", TestTasksEndpoint)
	t.Run("TestTasksEndpoint_MethodNotAllowed", TestTasksEndpoint_MethodNotAllowed)
	t.Run("TestTasksEndpoint_NoFunc", TestTasksEndpoint_NoFunc)
	t.Run("TestUseTranscriptTurns", TestUseTranscriptTurns)
}

func exerciseServerFuzzResiduals(t *testing.T) {
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
	s.SetPendingEscalationsSnapshotFunc(func() []appwire.SandboxEscalationRequested {
		return []appwire.SandboxEscalationRequested{{EscalationID: "esc_1"}}
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
	exerciseHTTPResiduals()
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
	s.SetTranscriptPathFunc(func() string { return " " })
	_, _ = s.appTurnsFromTranscript()

	_, _ = s.handleAppTurnStart(ctx, appwire.TurnStartParams{})
	for i := 0; i < cap(s.inputCh); i++ {
		s.inputCh <- InputMessage{Text: "full"}
	}
	_, _ = s.handleAppTurnStart(ctx, appwire.TurnStartParams{Input: []appwire.InputItem{{Text: "x"}}})
	for len(s.inputCh) > 0 {
		<-s.inputCh
	}
	_, _ = s.handleAppTurnSteer(ctx, appwire.TurnSteerParams{})
	_, _ = s.handleAppTurnSteer(ctx, appwire.TurnSteerParams{Input: []appwire.InputItem{{Text: "x"}}, ExpectedTurnID: "turn"})
	s.SetSteerFunc(func(string) {})
	_, _ = s.handleAppTurnSteer(ctx, appwire.TurnSteerParams{Input: []appwire.InputItem{{Text: "x"}}, ExpectedTurnID: "turn"})

	_, _ = s.handleAppTurnQueue(ctx, appwire.TurnQueueParams{})
	s.SetState(appwire.ThreadStatusClosed)
	_, _ = s.handleAppTurnQueue(ctx, appwire.TurnQueueParams{Input: []appwire.InputItem{{Text: "x"}}})
	s.SetState("")
	s.SetProcessing(true)
	_, _ = s.handleAppTurnQueue(ctx, appwire.TurnQueueParams{Input: []appwire.InputItem{{Type: "image"}}})
	s.SetQueueWithImagesFunc(func(string, []ImageAttachment) error { return errors.New("queue") })
	_, _ = s.handleAppTurnQueue(ctx, appwire.TurnQueueParams{Input: []appwire.InputItem{{Type: "image"}}})
	s.SetQueueFunc(nil)
	_, _ = s.handleAppTurnQueue(ctx, appwire.TurnQueueParams{Input: []appwire.InputItem{{Text: "x"}}})
	s.SetQueueFunc(func(string) error { return errors.New("queue") })
	_, _ = s.handleAppTurnQueue(ctx, appwire.TurnQueueParams{Input: []appwire.InputItem{{Text: "x"}}})

	s.SetState(appwire.ThreadStatusClosed)
	s.SetProcessing(false)
	_, _ = s.handleAppTurnDrainAsSteer(ctx, appwire.TurnDrainAsSteerParams{})
	s.SetState("")
	s.SetProcessing(false)
	_, _ = s.handleAppTurnDrainAsSteer(ctx, appwire.TurnDrainAsSteerParams{})
	s.SetProcessing(true)
	s.SetDrainAsSteerFunc(nil)
	_, _ = s.handleAppTurnDrainAsSteer(ctx, appwire.TurnDrainAsSteerParams{})
	s.SetDrainAsSteerFunc(func() error { return nil })
	_, _ = s.handleAppTurnDrainAsSteer(ctx, appwire.TurnDrainAsSteerParams{Input: []appwire.InputItem{{Text: "x"}}})
	s.SetDrainAsSteerWithInputFunc(func(string, []ImageAttachment) error { return errors.New("drain") })
	_, _ = s.handleAppTurnDrainAsSteer(ctx, appwire.TurnDrainAsSteerParams{Input: []appwire.InputItem{{Text: "x"}}})

	s.SetGoalFunc(func(string) (bool, error) { return false, errors.New("goal") })
	_, _ = s.handleAppGoalSet(ctx, appwire.GoalSetParams{})
	_, _ = s.handleAppThreadCompactStart(ctx, appwire.ThreadCompactStartParams{})
	s.SetCompactFunc(func(context.Context) error { return nil })
	_, _ = s.handleAppThreadCompactStart(ctx, appwire.ThreadCompactStartParams{})
	_, _ = s.handleAppThreadShutdown(ctx, appwire.ThreadShutdownParams{})
	s.SetProcessing(true)
	_, _ = s.handleAppThreadClear(ctx, appwire.ThreadClearParams{})
	s.SetProcessing(false)
	s.SetClearFunc(nil)
	_, _ = s.handleAppThreadClear(ctx, appwire.ThreadClearParams{})
	s.SetClearFunc(func(context.Context) error { return errors.New("clear") })
	_, _ = s.handleAppThreadClear(ctx, appwire.ThreadClearParams{})
	s.SetClearFunc(func(context.Context) error { return nil })
	_, _ = s.handleAppThreadClear(ctx, appwire.ThreadClearParams{})
	_, _ = s.handleAppThreadModelSet(ctx, appwire.ThreadModelSetParams{})
	s.SetModelFunc(nil)
	_, _ = s.handleAppThreadModelSet(ctx, appwire.ThreadModelSetParams{Model: "m"})
	_, _ = s.handleAppThreadNameSet(ctx, appwire.ThreadNameSetParams{})
	_, _ = s.handleAppThreadNameSet(ctx, appwire.ThreadNameSetParams{Name: "name"})
	s.SetNameFunc(func(string) {})
	_, _ = s.handleAppThreadNameSet(ctx, appwire.ThreadNameSetParams{Name: "name"})
	_, _ = s.handleAppTasksList(ctx, appwire.TaskListParams{})
	_, _ = s.handleAppModelList(ctx, appwire.ModelListParams{})
	s.SetListModelsFunc(func(context.Context) ([]ModelsResponseItem, error) { return nil, errors.New("models") })
	_, _ = s.handleAppModelList(ctx, appwire.ModelListParams{})

	s.SetGoalStatusFunc(func() (string, int, bool) { return "active", 1, true })
	s.SetQueuePreviewFunc(func() []string { return []string{"queued"} })
	s.SetPendingEscalationFunc(func() bool { return true })
	_ = s.appThread()
	plain := NewServer(ServerConfig{})
	plain.appSourceID = ""
	plain.SetQueueDepthFunc(func() int { return 2 })
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
	_ = projectTranscriptTurn([]byte("{"), "turn", 0, nil)
	ch := make(chan events.SessionEvent, 1)
	ch <- events.SessionEvent{Kind: events.EventSessionEnd}
	close(ch)
	Bridge(NewServer(ServerConfig{}), ch)
}

func exerciseHTTPResiduals() {
	s := NewServer(ServerConfig{})
	call := func(method, path, body string) {
		s.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, path, strings.NewReader(body)))
	}
	for _, path := range []string{"/queue", "/drain-as-steer", "/clear", "/model", "/input"} {
		call(http.MethodGet, path, "")
	}
	s.SetState(appwire.ThreadStatusClosed)
	call(http.MethodPost, "/queue", `{"text":"x"}`)
	s.SetState("")
	s.SetProcessing(true)
	s.SetDrainAsSteerFunc(func() error { return nil })
	call(http.MethodPost, "/drain-as-steer", `{"text":"x"}`)
	s.SetDrainAsSteerWithInputFunc(func(string, []ImageAttachment) error { return errors.New("drain") })
	call(http.MethodPost, "/drain-as-steer", `{"text":"x"}`)
	call(http.MethodPost, "/clear", "")
	call(http.MethodPost, "/model", `{}`)
	s.SetPendingEscalationFunc(func() bool { return true })
	call(http.MethodGet, "/status", "")
}
