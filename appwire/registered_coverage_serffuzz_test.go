//go:build serffuzz

package appwire

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"
	"primeradiant.com/serf/envvars"
)

// runRegisteredCoverageSuite makes the registered deterministic replay cover
// the same real package behavior as the ordinary suite. Keeping this explicit
// avoids init-time behavior and leaves normal fuzz callbacks independent.
func runRegisteredCoverageSuite(t *testing.T) {
	t.Helper()
	coverFailureBranches(t)
	tests := []struct {
		name string
		fn   func(*testing.T)
	}{
		{"CatalogMethodNamePartition", TestCatalogMethodNamePartition},
		{"ClientBuffersBurst", TestClientBuffersBurstLargerThanLegacyCapWithoutOverflow},
		{"ClientClose", TestClientClose},
		{"ClientFailPending", TestClientFailPendingPreservesRequestID},
		{"ClientNotificationOverflow", TestClientFailsPendingWhenNotificationsOverflow},
		{"ClientGoalSet", TestClientGoalSetRoundTrip},
		{"ClientInitialize", TestClientInitializeCachesFeatures},
		{"ClientPingFailure", TestClientKeepaliveTearsDownOnPingFailure},
		{"ClientPongTimeout", TestClientKeepaliveTearsDownOnPongTimeout},
		{"ClientNotify", TestClientNotify},
		{"ClientSendError", TestClientRequestSendErrorClearsPending},
		{"ClientWireError", TestClientRequestSurfacesWireError},
		{"ClientWrappers", TestClientRequestWrappersRoundTrip},
		{"ClientRoutes", TestClientRoutesResponsesAndNotifications},
		{"ClientUpgrade", TestClientUpgradeRoundTrip},
		{"ClientWithoutPinger", TestClientWithoutPingerHasNoKeepalive},
		{"CodexGolden", TestCodexItemDecodeGolden},
		{"DiagnosticCause", TestDiagnosticCauseJSONRoundTrip},
		{"DiagnosticOmit", TestDiagnosticCauseOmitEmpty},
		{"ErrorEncoding", TestErrorResponseEncoding},
		{"Cost", TestEstimateCost_FormatsToCents},
		{"CostNil", TestEstimateCost_NilUsageReturnsEmpty},
		{"CostUnpriced", TestEstimateCost_UnpricedModelReturnsEmpty},
		{"ExpectedTurnID", TestExpectedTurnIDUsed},
		{"FrameRecorder", TestFrameRecorderRoundTrip},
		{"FrameRecorderJSONL", TestFrameRecorderWritesJSONL},
		{"IDLess", TestIDLessFrameRoundTrips},
		{"InputParams", TestInputParamAccessors},
		{"InputText", TestInputTextPrefersFirstNonBlank},
		{"InstanceCreate", TestInstanceCreateParamsJSONRoundTrip},
		{"InstanceList", TestInstanceListResponseJSONRoundTrip},
		{"ItemStatus", TestItemStatusHelpers},
		{"LaunchConfig", TestLaunchConfigLayerMarshalModelFallbacks},
		{"MessageGolden", TestMessageDecodeGolden},
		{"MessageID", TestMessageIDString},
		{"MessageKind", TestMessageKind},
		{"MessageMarshal", TestMessageMarshalDispatch},
		{"MessageUnmarshal", TestMessageUnmarshalDispatch},
		{"MethodCatalog", TestMethodCatalogWellFormed},
		{"MethodParams", TestMethodParamsGolden},
		{"MethodScope", TestMethodScopeRouted},
		{"ModelRecent", TestModelListResponseRecentJSONRoundTrip},
		{"ModelRecentOmit", TestModelListResponseRecentOmitEmpty},
		{"RecorderBadPath", TestNewFrameRecorderRejectsBadPath},
		{"NilRecorder", TestNilFrameRecorderIsNoOp},
		{"NotificationCatalog", TestNotificationCatalogWellFormed},
		{"Notification", TestNotificationRoundTrip},
		{"PageClamped", TestPageTurnsEmptyAndClamped},
		{"PageBackward", TestPageTurnsWalksBackwardToHead},
		{"ParseRef", TestParseRefRejectsUnsafeValues},
		{"PendingRef", TestPendingTargetRefFallsBackToThreadID},
		{"RecorderRoot", TestRecorderStateRoot},
		{"RefRoundTrip", TestRefRoundTrip},
		{"RefString", TestRefString},
		{"RejectJSONRPC", TestRejectsJSONRPCField},
		{"RequestParams", TestRequestMessageParamsEncoded},
		{"Request", TestRequestRoundTrip},
		{"Sandbox", TestSandboxEscalationWireKeys},
		{"DiagnosticsJobs", TestSerfDiagnosticsJobsJSONRoundTrip},
		{"ThreadMetrics", TestSerfThreadMetricsJSONRoundTrip},
		{"ThreadMetricsOmit", TestSerfThreadMetricsOmitEmpty},
		{"AskPending", TestSerfThread_AskPendingRoundTrips},
		{"Usage", TestSerfUsageFromLLM_MapsFields},
		{"UsageNil", TestSerfUsageFromLLM_NilWhenAllZero},
		{"ThreadItemMarshal", TestThreadItemMarshalUsesCodexItemTypes},
		{"OutputImages", TestThreadItemOutputImagesJSONRoundTrip},
		{"ThreadItemUnmarshal", TestThreadItemUnmarshalUsesCodexItemTypes},
		{"ThreadNameCatalog", TestThreadNameSetInCatalog},
		{"ThreadStatus", TestThreadStatusHelpersUseCodexVocabulary},
		{"PagingSanity", TestTurnPagingEquivalenceSanity},
		{"TurnStatus", TestTurnStatusHelpersUseCodexVocabulary},
		{"UsageCost", TestTurnUsageCostJSONRoundTrip},
		{"UsageCostOmit", TestTurnUsageCostOmitEmpty},
		{"WSLimit", TestWSTransportReadLimitCoversMaxComposerImages},
		{"WSLarge", TestWSTransportReceivesLargeAppWireMessage},
		{"WSRecorder", TestWSTransportRecordsFrames},
		{"WSRoundTrip", TestWSTransportRoundTrip},
		{"WindowLatest", TestWindowTurnsBoundsToLatest},
		{"WindowUnbounded", TestWindowTurnsUnboundedWhenLimitZeroOrLarger},
		{"WireErrors", TestWireErrorConstructors},
		{"WireRegistry", TestWireTypeRegistryCoverage},
		{"ZeroRecorder", TestZeroValueFrameRecorderIsNoOp},
	}
	for _, test := range tests {
		t.Run(test.name, test.fn)
	}
}

type coverageCoordinator struct{ handle *coverageHandle }

func (c coverageCoordinator) Register(string, string, string) PendingHandle { return c.handle }

type coverageHandle struct{ failed bool }

func (h *coverageHandle) Fail(string) { h.failed = true }

func coverFailureBranches(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runClientKeepalive(ctx, nil, func() error { return nil }, time.Hour, time.Hour)

	failedTransport := &sendErrTransport{memoryTransport: newMemoryTransport(), err: errors.New("send")}
	c := NewClient(failedTransport)
	h := &coverageHandle{}
	c.SetPendingCoordinator(coverageCoordinator{handle: h})
	_ = c.TurnSteer(context.Background(), TurnSteerParams{Input: []InputItem{{Text: "x"}}})
	_ = c.TurnDrainAsSteer(context.Background(), TurnDrainAsSteerParams{Ref: "local:t"})
	if !h.failed {
		t.Fatal("pending handle was not failed")
	}

	tr := newMemoryTransport()
	c = NewClient(tr)
	ctx, cancel = context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- c.Request(ctx, "cancel", nil, nil) }()
	<-tr.writes
	cancel()
	<-errCh

	tr = newMemoryTransport()
	c = NewClient(tr)
	go func() { errCh <- c.Request(context.Background(), "bad", nil, new(any)) }()
	written := <-tr.writes
	c.pendingMu.Lock()
	c.pending[written.Request.ID.String()].ch <- Message{}
	c.pendingMu.Unlock()
	<-errCh
	go func() { errCh <- c.Request(context.Background(), "marshal", nil, new(any)) }()
	written = <-tr.writes
	c.pendingMu.Lock()
	c.pending[written.Request.ID.String()].ch <- ResponseMessage(written.Request.ID, make(chan int))
	c.pendingMu.Unlock()
	<-errCh

	_, _ = (ID{}).MarshalJSON()
	var id ID
	_ = id.UnmarshalJSON(nil)
	_ = id.UnmarshalJSON([]byte("null"))
	_ = json.Unmarshal([]byte(`"named"`), &id)
	_ = id.String()
	_, _ = ParseRef("s:a..b")
	func() {
		defer func() { _ = recover() }()
		_ = mustRaw(make(chan int))
	}()

	oldFrame := unmarshalMessageFrame
	unmarshalMessageFrame = func([]byte, any) error { return errors.New("typed") }
	for _, raw := range []string{`{"error":{}}`, `{"result":{}}`, `{"id":1,"method":"x"}`, `{"method":"x"}`} {
		var m Message
		_ = json.Unmarshal([]byte(raw), &m)
	}
	unmarshalMessageFrame = oldFrame

	defer func(old func(any) ([]byte, error)) { marshalRecordedFrame = old }(marshalRecordedFrame)
	marshalRecordedFrame = func(any) ([]byte, error) { return nil, errors.New("marshal") }
	(&FrameRecorder{f: os.Stdout}).RecordSend(nil)

	t.Setenv(envvars.SERFRecordAppwire.Name, "1")
	root := t.TempDir()
	t.Setenv(envvars.SERFStateDir.Name, root)
	rec := newEnvFrameRecorder()
	if rec != nil {
		_ = rec.Close()
	}
	badRoot := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(badRoot, []byte("x"), 0o600)
	t.Setenv(envvars.SERFStateDir.Name, badRoot)
	_ = newEnvFrameRecorder()
	oldHome := frameRecorderHomeDir
	t.Setenv(envvars.SERFStateDir.Name, "")
	frameRecorderHomeDir = func() (string, error) { return "", errors.New("home") }
	_ = recorderStateRoot()
	frameRecorderHomeDir = oldHome

	oldMarshal, oldUnmarshal, oldFallbacks := marshalLaunchConfig, unmarshalLaunchConfig, marshalModelFallbacks
	marshalLaunchConfig = func(any) ([]byte, error) { return nil, errors.New("marshal") }
	_, _ = (LaunchConfigLayer{}).MarshalJSON()
	marshalLaunchConfig = oldMarshal
	unmarshalLaunchConfig = func([]byte, any) error { return errors.New("decode") }
	_, _ = (LaunchConfigLayer{ModelFallbacks: []string{}}).MarshalJSON()
	unmarshalLaunchConfig = oldUnmarshal
	marshalModelFallbacks = func(any) ([]byte, error) { return nil, errors.New("fallbacks") }
	_, _ = (LaunchConfigLayer{ModelFallbacks: []string{}}).MarshalJSON()
	marshalModelFallbacks = oldFallbacks

	oldWSMarshal := marshalWSMessage
	marshalWSMessage = func(any) ([]byte, error) { return nil, errors.New("marshal") }
	_ = (&WSTransport{}).Send(context.Background(), Message{})
	marshalWSMessage = oldWSMarshal
	oldPing := pingWebSocket
	pingWebSocket = func(*websocket.Conn, context.Context) error { return errors.New("ping") }
	_ = (&WSTransport{}).Ping(context.Background())
	pingWebSocket = oldPing
	_, _ = DialWebSocketWithHeaders(context.Background(), "://bad", nil, nil)
	oldRead, oldUnmarshalWS := readWebSocket, unmarshalWSMessage
	readWebSocket = func(*websocket.Conn, context.Context) (websocket.MessageType, []byte, error) {
		return 0, nil, errors.New("read")
	}
	_, _ = (&WSTransport{}).Recv(context.Background())
	readWebSocket = func(*websocket.Conn, context.Context) (websocket.MessageType, []byte, error) {
		return websocket.MessageText, []byte("{}"), nil
	}
	unmarshalWSMessage = func([]byte, any) error { return errors.New("decode") }
	_, _ = (&WSTransport{}).Recv(context.Background())
	readWebSocket, unmarshalWSMessage = oldRead, oldUnmarshalWS
}
