package appsource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

type scriptedAppwireTransport struct {
	recv      chan appwire.Message
	closed    chan struct{}
	closeOnce sync.Once
	recvDone  chan struct{}
	recvOnce  sync.Once
	send      func(context.Context, appwire.Message) error
}

func newScriptedAppwireTransport(send func(context.Context, appwire.Message) error) *scriptedAppwireTransport {
	return &scriptedAppwireTransport{
		recv: make(chan appwire.Message, 16), closed: make(chan struct{}), recvDone: make(chan struct{}), send: send,
	}
}

func (t *scriptedAppwireTransport) Send(ctx context.Context, msg appwire.Message) error {
	if t.send != nil {
		return t.send(ctx, msg)
	}
	return nil
}

func (t *scriptedAppwireTransport) Recv(ctx context.Context) (appwire.Message, error) {
	select {
	case msg := <-t.recv:
		return msg, nil
	case <-t.closed:
		t.recvOnce.Do(func() { close(t.recvDone) })
		return appwire.Message{}, io.EOF
	case <-ctx.Done():
		t.recvOnce.Do(func() { close(t.recvDone) })
		return appwire.Message{}, ctx.Err()
	}
}

func (t *scriptedAppwireTransport) Close() error {
	t.closeOnce.Do(func() { close(t.closed) })
	return nil
}

func respondingTransport(result func(string) (any, error)) *scriptedAppwireTransport {
	var transport *scriptedAppwireTransport
	transport = newScriptedAppwireTransport(func(_ context.Context, msg appwire.Message) error {
		if msg.Request == nil {
			return nil
		}
		value, err := result(msg.Request.Method)
		if err != nil {
			transport.recv <- appwire.ErrorMessage(msg.Request.ID, appwire.InternalError(err.Error()))
		} else {
			transport.recv <- appwire.ResponseMessage(msg.Request.ID, value)
		}
		return nil
	})
	return transport
}

func dialTransport(transport appwire.Transport) appwireDialFunc {
	return func(context.Context, string, *http.Client, http.Header) (appwire.Transport, error) {
		return transport, nil
	}
}

func fuzzScenarioSourceDialSeamsPreserveCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dial := func(context.Context, string, *http.Client, http.Header) (appwire.Transport, error) {
		return nil, errors.New("dial failed")
	}

	codex := NewCodexSource(CodexSourceConfig{Endpoint: "ws://daemon"}, nil)
	codex.dial = dial
	if _, _, err := codex.connect(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("codex connect error = %v", err)
	}

	local := NewLocalDaemonSource("local", nil, nil)
	local.dial = dial
	if err := local.withClient(ctx, rendezvousEntry("ws://daemon"), func(*appwire.Client) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("local withClient error = %v", err)
	}
	entry := rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://daemon", SourceID: "local", ThreadID: "thread"}
	local = NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry { return []LocalDaemonEntry{{Entry: entry}} }, nil)
	local.dial = dial
	if _, err := local.SubscribeThread(ctx, appwire.ThreadReadParams{Ref: "local:thread"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("local SubscribeThread error = %v", err)
	}
}

func rendezvousEntry(endpoint string) rendezvous.Entry {
	return rendezvous.Entry{Endpoint: endpoint}
}

func fuzzScenarioCodexConnectHandshakeFailures(t *testing.T) {
	t.Run("initialize error", func(t *testing.T) {
		transport := respondingTransport(func(method string) (any, error) {
			return nil, errors.New(method + " failed")
		})
		s := NewCodexSource(CodexSourceConfig{Endpoint: "ws://daemon"}, nil)
		s.dial = dialTransport(transport)
		if _, _, err := s.connect(context.Background()); err == nil {
			t.Fatal("connect returned nil")
		}
		select {
		case <-transport.closed:
		default:
			t.Fatal("transport was not closed")
		}
	})

	t.Run("initialize canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		transport := respondingTransport(func(string) (any, error) {
			cancel()
			return nil, errors.New("initialize failed")
		})
		s := NewCodexSource(CodexSourceConfig{Endpoint: "ws://daemon"}, nil)
		s.dial = dialTransport(transport)
		if _, _, err := s.connect(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("connect error = %v", err)
		}
	})

	for _, tc := range []struct {
		name   string
		cancel bool
	}{
		{name: "initialized notification error"},
		{name: "initialized notification canceled", cancel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			transport := respondingTransport(func(string) (any, error) { return map[string]any{}, nil })
			transport.send = func(_ context.Context, msg appwire.Message) error {
				if msg.Request != nil {
					transport.recv <- appwire.ResponseMessage(msg.Request.ID, map[string]any{})
					return nil
				}
				if tc.cancel {
					cancel()
				}
				return errors.New("notify failed")
			}
			s := NewCodexSource(CodexSourceConfig{Endpoint: "ws://daemon"}, nil)
			s.dial = dialTransport(transport)
			_, _, err := s.connect(ctx)
			if err == nil || (tc.cancel && !errors.Is(err, context.Canceled)) {
				t.Fatalf("connect error = %v", err)
			}
		})
	}
}

func fuzzScenarioCodexRPCFailureAndValidationBranches(t *testing.T) {
	dialErr := errors.New("connection refused")
	dial := func(context.Context, string, *http.Client, http.Header) (appwire.Transport, error) {
		return nil, dialErr
	}
	s := NewCodexSource(CodexSourceConfig{Endpoint: "ws://daemon"}, nil)
	s.dial = dial
	ctx := context.Background()
	ref := "codex:thread"
	calls := []func() error{
		func() error { _, err := s.ReadThread(ctx, appwire.ThreadReadParams{Ref: ref}); return err },
		func() error { _, err := s.ListTurns(ctx, appwire.ThreadTurnsListParams{Ref: ref}); return err },
		func() error { _, err := s.StartThread(ctx, appwire.ThreadStartParams{}); return err },
		func() error { _, err := s.ResumeThread(ctx, appwire.ThreadResumeParams{Ref: ref}); return err },
		func() error { _, err := s.ForkThread(ctx, appwire.ThreadForkParams{Ref: ref}); return err },
		func() error {
			_, err := s.StartTurn(ctx, appwire.TurnStartParams{ClientMutationID: "test-mutation", Ref: ref})
			return err
		},
		func() error { _, err := s.ListModels(ctx, appwire.ModelListParams{}); return err },
		func() error { _, err := s.SubscribeThread(ctx, appwire.ThreadReadParams{Ref: ref}); return err },
	}
	for i, call := range calls {
		if err := call(); err == nil {
			t.Fatalf("call %d returned nil", i)
		}
	}

	badInput := []appwire.InputItem{{Type: "unsupported"}}
	if _, err := s.StartTurn(ctx, appwire.TurnStartParams{ClientMutationID: "test-mutation", Ref: ref, Input: badInput}); err == nil {
		t.Fatal("StartTurn accepted invalid input")
	}
	if _, err := s.startTurnWithClient(ctx, nil, appwire.TurnStartParams{ClientMutationID: "test-mutation", Ref: ref, Input: badInput}); err == nil {
		t.Fatal("startTurnWithClient accepted invalid input")
	}
	if _, err := s.SteerTurn(ctx, appwire.TurnSteerParams{ClientMutationID: "test-mutation", Ref: ref, Input: badInput}); err == nil {
		t.Fatal("SteerTurn accepted invalid input")
	}
}

func fuzzScenarioCodexRPCResponseErrors(t *testing.T) {
	newSource := func() *CodexSource {
		transport := respondingTransport(func(method string) (any, error) {
			if method == appwire.MethodInitialize {
				return map[string]any{}, nil
			}
			return nil, errors.New("rpc failed")
		})
		s := NewCodexSource(CodexSourceConfig{Endpoint: "ws://daemon"}, nil)
		s.dial = dialTransport(transport)
		return s
	}
	ctx := context.Background()
	ref := "codex:thread"
	turnTransport := respondingTransport(func(string) (any, error) { return nil, errors.New("rpc failed") })
	turnCtx, cancelTurn := context.WithCancel(ctx)
	turnClient := appwire.NewClient(turnTransport)
	turnClient.Start(turnCtx)
	calls := []func() error{
		func() error { _, err := newSource().StartThread(ctx, appwire.ThreadStartParams{}); return err },
		func() error {
			_, err := newSource().startTurnWithClient(ctx, turnClient, appwire.TurnStartParams{ClientMutationID: "test-mutation", Ref: ref})
			return err
		},
		func() error {
			_, err := newSource().ResumeThread(ctx, appwire.ThreadResumeParams{Ref: ref})
			return err
		},
		func() error { _, err := newSource().ForkThread(ctx, appwire.ThreadForkParams{Ref: ref}); return err },
		func() error { _, err := newSource().ListModels(ctx, appwire.ModelListParams{}); return err },
		func() error {
			_, err := newSource().SubscribeThread(ctx, appwire.ThreadReadParams{Ref: ref})
			return err
		},
	}
	for i, call := range calls {
		if err := call(); err == nil {
			cancelTurn()
			_ = turnClient.Close()
			<-turnTransport.recvDone
			t.Fatalf("call %d returned nil", i)
		}
	}
	cancelTurn()
	_ = turnClient.Close()
	<-turnTransport.recvDone
}

func fuzzScenarioCodexInitialAndResumedTurnFailures(t *testing.T) {
	result := func(method string) (any, error) {
		switch method {
		case appwire.MethodInitialize:
			return map[string]any{}, nil
		case appwire.MethodThreadStart:
			return map[string]any{"thread": map[string]any{"id": "thread"}}, nil
		case appwire.MethodThreadResume:
			return map[string]any{"thread": map[string]any{"id": "thread"}}, nil
		default:
			return nil, errors.New("turn failed")
		}
	}
	newSource := func() *CodexSource {
		s := NewCodexSource(CodexSourceConfig{Endpoint: "ws://daemon"}, nil)
		s.dial = dialTransport(respondingTransport(result))
		return s
	}
	input := []appwire.InputItem{{Type: "text", Text: "hello"}}
	if _, err := newSource().StartThread(context.Background(), appwire.ThreadStartParams{Input: input}); err == nil {
		t.Fatal("StartThread returned nil")
	}
	if _, err := newSource().StartTurn(context.Background(), appwire.TurnStartParams{ClientMutationID: "test-mutation", Ref: "codex:thread", Input: input}); err == nil {
		t.Fatal("StartTurn returned nil")
	}

	live := &codexLiveThread{done: make(chan struct{}), subscribers: map[chan appwire.Notification]struct{}{}, closed: true}
	s := newTestCodexSource()
	s.live["thread"] = live
	if got := s.liveThread("thread"); got != nil {
		t.Fatalf("closed live thread = %p", got)
	}

	for _, mapper := range []func(error) error{codexSourceDialError, localDaemonDialError} {
		var wire appwire.WireError
		if err := mapper(context.DeadlineExceeded); !errors.As(err, &wire) {
			t.Fatalf("deadline error = %v", err)
		}
	}
	if err := codexSourceDialError(fakeTimeoutError{}); err == nil {
		t.Fatal("timeout error mapped nil")
	}
}

func fuzzScenarioLocalDaemonRemainingTransportBranches(t *testing.T) {
	ctx := context.Background()
	entry := rendezvousEntry("ws://daemon")
	entry.ThreadID = "thread"
	entry.SourceID = "local"
	entry.Protocol = appwire.ProtocolVersion

	for _, cancelAt := range []string{"initialize", "read"} {
		t.Run("subscribe canceled during "+cancelAt, func(t *testing.T) {
			callCtx, cancel := context.WithCancel(ctx)
			transport := respondingTransport(func(method string) (any, error) {
				if method == appwire.MethodInitialize && cancelAt != "initialize" {
					return appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion}, nil
				}
				cancel()
				return nil, errors.New("failed")
			})
			s := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry { return []LocalDaemonEntry{{Entry: entry}} }, nil)
			s.dial = dialTransport(transport)
			if _, err := s.SubscribeThread(callCtx, appwire.ThreadReadParams{Ref: "local:thread"}); !errors.Is(err, context.Canceled) {
				t.Fatalf("SubscribeThread error = %v", err)
			}
		})
	}

	t.Run("withClient initialize cancellation", func(t *testing.T) {
		callCtx, cancel := context.WithCancel(ctx)
		transport := respondingTransport(func(string) (any, error) { cancel(); return nil, errors.New("failed") })
		s := NewLocalDaemonSource("local", nil, nil)
		s.dial = dialTransport(transport)
		if err := s.withClient(callCtx, entry, func(*appwire.Client) error { return nil }); !errors.Is(err, context.Canceled) {
			t.Fatalf("withClient error = %v", err)
		}
	})

	t.Run("dial failures", func(t *testing.T) {
		dial := func(context.Context, string, *http.Client, http.Header) (appwire.Transport, error) {
			return nil, errors.New("connection refused")
		}
		s := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry { return []LocalDaemonEntry{{Entry: entry}} }, nil)
		s.dial = dial
		if _, err := s.SubscribeThread(ctx, appwire.ThreadReadParams{Ref: "local:thread"}); err == nil {
			t.Fatal("SubscribeThread returned nil")
		}
		if err := s.withClient(ctx, entry, func(*appwire.Client) error { return nil }); err == nil {
			t.Fatal("withClient returned nil")
		}
	})

	t.Run("subscription closes and cancels", func(t *testing.T) {
		for _, publish := range []bool{false, true} {
			callCtx, cancel := context.WithCancel(ctx)
			transport := respondingTransport(func(method string) (any, error) {
				if method == appwire.MethodInitialize {
					return appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion}, nil
				}
				return map[string]any{}, nil
			})
			s := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry { return []LocalDaemonEntry{{Entry: entry}} }, nil)
			s.dial = dialTransport(transport)
			out, err := s.SubscribeThread(callCtx, appwire.ThreadReadParams{Ref: "local:thread"})
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			if publish {
				transport.recv <- appwire.NotificationMessage("event", nil)
				<-out
			}
			cancel()
			<-transport.recvDone
			if _, ok := <-out; ok {
				t.Fatal("subscription remained open")
			}
		}
	})

	t.Run("notification source closes", func(t *testing.T) {
		callCtx, cancel := context.WithCancel(ctx)
		transport := respondingTransport(func(method string) (any, error) {
			if method == appwire.MethodInitialize {
				return appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion}, nil
			}
			return map[string]any{}, nil
		})
		s := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry { return []LocalDaemonEntry{{Entry: entry}} }, nil)
		s.dial = dialTransport(transport)
		out, err := s.SubscribeThread(callCtx, appwire.ThreadReadParams{Ref: "local:thread"})
		if err != nil {
			t.Fatal(err)
		}
		_ = transport.Close()
		<-transport.recvDone
		cancel()
		if _, ok := <-out; ok {
			t.Fatal("subscription remained open")
		}
	})

}

func fuzzScenarioForwardLocalDaemonNotificationCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	forwardLocalDaemonNotification(ctx, make(chan appwire.Notification), appwire.Notification{})
}

func TestLocalDaemonItemPagingForwardsItemAndLegacyModes(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	var reads []appwire.ThreadReadParams
	appserver.HandleTyped(server.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		reads = append(reads, params)
		return appwire.ThreadReadResponse{PageUnit: params.PageUnit, Thread: appwire.Thread{ID: "thread", Evener: appwire.EvenerThread{Ref: "local:thread"}}}, nil
	})
	var lists []appwire.ThreadTurnsListParams
	appserver.HandleTyped(server.Router(), appwire.MethodThreadTurnsList, func(_ context.Context, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
		lists = append(lists, params)
		return appwire.ThreadTurnsListResponse{PageUnit: params.PageUnit}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()

	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{{Entry: rendezvous.Entry{
			Protocol: appwire.ProtocolVersion, Endpoint: "ws" + httpServer.URL[len("http"):],
			SourceID: "local", ThreadID: "thread", SessionID: "thread",
		}}}
	}, httpServer.Client())
	ctx := context.Background()
	if _, err := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: "local:thread", IncludeTurns: true, PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 7}); err != nil {
		t.Fatalf("item ReadThread: %v", err)
	}
	if _, err := source.ListTurns(ctx, appwire.ThreadTurnsListParams{Ref: "local:thread", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 7, Cursor: "opaque-cursor"}); err != nil {
		t.Fatalf("item ListTurns: %v", err)
	}
	if _, err := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: "local:thread", IncludeTurns: true, TurnLimit: 3}); err != nil {
		t.Fatalf("legacy ReadThread: %v", err)
	}
	if _, err := source.ListTurns(ctx, appwire.ThreadTurnsListParams{Ref: "local:thread", Limit: 3, Cursor: "7"}); err != nil {
		t.Fatalf("legacy ListTurns: %v", err)
	}
	if len(reads) != 2 || reads[0].PageUnit != appwire.TranscriptPageUnitItem || reads[0].ItemLimit != 7 || reads[0].TurnLimit != 0 || reads[1].PageUnit != "" || reads[1].ItemLimit != 0 || reads[1].TurnLimit != 3 {
		t.Fatalf("read params forwarded = %+v", reads)
	}
	if len(lists) != 2 || lists[0].PageUnit != appwire.TranscriptPageUnitItem || lists[0].ItemLimit != 7 || lists[0].Cursor != "opaque-cursor" || lists[0].Limit != 0 || lists[1].PageUnit != "" || lists[1].ItemLimit != 0 || lists[1].Cursor != "7" || lists[1].Limit != 3 {
		t.Fatalf("list params forwarded = %+v", lists)
	}
}

func TestLocalDaemonItemCandidatesMaterializeAuthenticatedSnapshot(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	var requests []appwire.ThreadTurnsListParams
	items := make([]appwire.ThreadItem, 41)
	for i := range items {
		position := appwire.ThreadItemPosition{Entry: 0, Item: uint32(i)}
		items[i] = appwire.ThreadItem{
			Type:          "agentMessage",
			ID:            fmt.Sprintf("item-%02d", i),
			TranscriptKey: fmt.Sprintf("daemon-key-%02d", i),
			Position:      &position,
			TurnID:        "turn-1",
			Text:          fmt.Sprintf("text-%02d", i),
		}
	}
	appserver.HandleTyped(server.Router(), appwire.MethodThreadTurnsList, func(_ context.Context, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
		requests = append(requests, params)
		return appwire.ThreadTurnsListResponse{
			Data: []appwire.Turn{{ID: "turn-1", Items: items, ItemsView: appwire.TurnItemsViewFragment}}, PageUnit: appwire.TranscriptPageUnitItem,
		}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()

	entry := rendezvous.Entry{
		Protocol:     appwire.ProtocolVersion,
		Endpoint:     "ws" + httpServer.URL[len("http"):],
		SourceID:     "local",
		ThreadID:     "thread",
		SessionID:    "thread",
		WorkspaceRef: "local:thread",
		InstanceID:   "instance-1",
		HubToken:     "authenticated-token",
	}
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{{Entry: entry}}
	}, httpServer.Client())

	first, err := source.ReadItemCandidates(context.Background(), appwire.ThreadReadParams{
		Ref:       "local:thread",
		PageUnit:  appwire.TranscriptPageUnitItem,
		ItemLimit: 40,
	})
	if err != nil {
		t.Fatalf("initial candidate read: %v", err)
	}
	if len(first.Candidates.Candidates) != 40 || first.Candidates.Candidates[0].Item.ID != "item-01" || first.Candidates.Candidates[39].Item.ID != "item-40" {
		t.Fatalf("initial candidates = %d (%q..%q), want item-01..item-40", len(first.Candidates.Candidates), first.Candidates.Candidates[0].Item.ID, first.Candidates.Candidates[39].Item.ID)
	}
	if first.Identity.ThreadRef != "local:thread" || first.Identity.Incarnation == "" || first.Identity.ProjectionVersion == 0 {
		t.Fatalf("candidate identity = %+v, want authenticated thread identity", first.Identity)
	}
	if first.Candidates.OlderCursor == "" {
		t.Fatal("initial candidate page has no older cursor")
	}
	retained, err := json.Marshal(retainedItemSnapshotStates(source.itemSnapshots))
	if err != nil {
		t.Fatalf("marshal retained local paging state: %v", err)
	}
	if strings.Contains(string(retained), "text-40") {
		t.Fatalf("retained local paging state contains transcript payload (serialized bytes=%d)", len(retained))
	}
	boundary, err := appitempaging.DecodeCursor(first.Candidates.OlderCursor, first.Identity)
	if err != nil {
		t.Fatalf("decode source cursor: %v", err)
	}
	if boundary != (appwire.ThreadItemPosition{Entry: 0, Item: 1}) {
		t.Fatalf("source cursor boundary = %+v, want entry 0 item 1", boundary)
	}

	second, err := source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{
		Ref:       "local:thread",
		PageUnit:  appwire.TranscriptPageUnitItem,
		ItemLimit: 40,
		Cursor:    first.Candidates.OlderCursor,
	})
	if err != nil {
		t.Fatalf("older candidate read: %v", err)
	}
	if len(second.Candidates.Candidates) != 1 || second.Candidates.Candidates[0].Item.ID != "item-00" || !second.Exhausted || second.Candidates.OlderCursor != "" {
		t.Fatalf("older candidates = %+v, want only exhausted item-00", second)
	}
	for i, request := range requests {
		if request.PageUnit != appwire.TranscriptPageUnitTurn || request.Cursor != "" || request.ItemLimit != 0 {
			t.Fatalf("materialization request %d = %+v, want authenticated legacy turn request without browser cursor", i, request)
		}
	}
	nativeIdentity := appitempaging.CursorIdentity{
		ThreadRef: "local:thread", Incarnation: "daemon-native", ProjectionVersion: 1,
	}
	nativeCursor, err := appitempaging.EncodeCursor(nativeIdentity, appwire.ThreadItemPosition{Entry: 0, Item: 1})
	if err != nil {
		t.Fatalf("encode native cursor: %v", err)
	}
	partialResponse := appwire.ThreadReadResponse{
		Thread: appwire.Thread{ID: "thread", Evener: appwire.EvenerThread{Ref: "local:thread"}, Turns: []appwire.Turn{{
			ID: "turn-1", Items: items[1:], ItemsView: appwire.TurnItemsViewFragment, HasEarlierItems: true,
		}}},
		PageUnit:    appwire.TranscriptPageUnitItem,
		OlderCursor: nativeCursor,
	}
	continuitySource := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{{Entry: entry}}
	}, httpServer.Client())
	bounded, err := continuitySource.ItemCandidatesFromRead(context.Background(), appwire.ThreadReadParams{
		Ref: "local:thread", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 40,
	}, partialResponse)
	if err != nil {
		t.Fatalf("seed bounded candidate tail: %v", err)
	}
	if len(bounded.Candidates.Candidates) != 40 || bounded.Candidates.Candidates[0].Item.ID != "item-01" || bounded.Candidates.Candidates[39].Item.ID != "item-40" {
		t.Fatalf("bounded candidate tail = %+v, want item-01..item-40", bounded.Candidates.Candidates)
	}
	oldCursor, err := appitempaging.EncodeCursor(bounded.Identity, bounded.Candidates.Candidates[0].Position)
	if err != nil {
		t.Fatalf("encode bounded candidate cursor: %v", err)
	}
	suffixResponse := partialResponse
	suffixResponse.Thread.Turns = []appwire.Turn{{
		ID: "turn-1", Items: items[21:], ItemsView: appwire.TurnItemsViewFragment, HasEarlierItems: true,
	}}
	suffixResponse.OlderCursor, err = appitempaging.EncodeCursor(nativeIdentity, *items[21].Position)
	if err != nil {
		t.Fatalf("encode shifted native cursor: %v", err)
	}
	suffix, err := continuitySource.ItemCandidatesFromRead(context.Background(), appwire.ThreadReadParams{
		Ref: "local:thread", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 20,
	}, suffixResponse)
	if err != nil {
		t.Fatalf("observe bounded suffix: %v", err)
	}
	if len(suffix.Candidates.Candidates) != 20 || suffix.Candidates.Candidates[0].Item.ID != "item-21" || suffix.Candidates.Candidates[19].Item.ID != "item-40" {
		t.Fatalf("bounded suffix = %+v, want item-21..item-40", suffix.Candidates.Candidates)
	}
	if suffix.Identity != bounded.Identity {
		t.Fatalf("bounded suffix identity = %+v, want unchanged %+v", suffix.Identity, bounded.Identity)
	}
	fullResponse := partialResponse
	fullResponse.Thread.Turns = []appwire.Turn{{
		ID: "turn-1", Items: items, ItemsView: appwire.TurnItemsViewFragment,
	}}
	fullResponse.OlderCursor = ""
	full, err := continuitySource.ItemCandidatesFromRead(context.Background(), appwire.ThreadReadParams{
		Ref: "local:thread", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 40,
	}, fullResponse)
	if err != nil {
		t.Fatalf("observe full materialization: %v", err)
	}
	if full.Identity != bounded.Identity {
		t.Fatalf("bounded-to-full identity = %+v, want unchanged %+v", full.Identity, bounded.Identity)
	}
	continued, err := continuitySource.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{
		Ref: "local:thread", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 40, Cursor: oldCursor,
	})
	if err != nil {
		t.Fatalf("continue original bounded cursor after full materialization: %v", err)
	}
	if len(continued.Candidates.Candidates) != 1 || continued.Candidates.Candidates[0].Item.ID != "item-00" {
		t.Fatalf("continued bounded cursor = %+v, want item-00", continued.Candidates.Candidates)
	}
	partial, err := source.ItemCandidatesFromRead(context.Background(), appwire.ThreadReadParams{
		Ref: "local:thread", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 40,
	}, partialResponse)
	if err != nil {
		t.Fatalf("convert partial atomic read: %v", err)
	}
	if partial.Exhausted || partial.Candidates.OlderCursor != "" || len(partial.Candidates.Candidates) != 40 {
		t.Fatalf("converted partial atomic read = %+v, want 40 source-owned non-exhausted candidates", partial)
	}
	validPartialResponse := partialResponse
	validPartialResponse.OlderCursor = nativeCursor
	partial, err = source.ItemCandidatesFromRead(context.Background(), appwire.ThreadReadParams{
		Ref: "local:thread", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 40,
	}, validPartialResponse)
	if err != nil {
		t.Fatalf("convert valid partial atomic read: %v", err)
	}
	partialCursor, err := appitempaging.EncodeCursor(partial.Identity, partial.Candidates.Candidates[0].Position)
	if err != nil {
		t.Fatalf("encode converted partial cursor: %v", err)
	}
	partialOlder, err := source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{
		Ref: "local:thread", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 40, Cursor: partialCursor,
	})
	if err != nil {
		t.Fatalf("backfill converted partial cursor: %v", err)
	}
	if len(partialOlder.Candidates.Candidates) != 1 || partialOlder.Candidates.Candidates[0].Item.ID != "item-00" {
		t.Fatalf("converted partial backfill = %+v, want item-00", partialOlder.Candidates.Candidates)
	}

	entry.InstanceID = "instance-2"
	if _, err := source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{
		Ref:       "local:thread",
		PageUnit:  appwire.TranscriptPageUnitItem,
		ItemLimit: 40,
		Cursor:    first.Candidates.OlderCursor,
	}); err == nil {
		t.Fatal("cursor from a different authenticated daemon instance was accepted")
	}
}

func TestLocalDaemonNativeSuccessorCursorIdentityMismatchStales(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "native-daemon", SourceID: "local"})
	nativeIdentityOne := appitempaging.CursorIdentity{ThreadRef: "local:thread", Incarnation: "daemon-one", ProjectionVersion: 1}
	nativeIdentityTwo := appitempaging.CursorIdentity{ThreadRef: "local:thread", Incarnation: "daemon-two", ProjectionVersion: 1}
	nativeCursorOne, err := appitempaging.EncodeCursor(nativeIdentityOne, appwire.ThreadItemPosition{Entry: 0})
	if err != nil {
		t.Fatalf("encode request native cursor: %v", err)
	}
	nativeCursorTwo, err := appitempaging.EncodeCursor(nativeIdentityTwo, appwire.ThreadItemPosition{Entry: 0})
	if err != nil {
		t.Fatalf("encode successor native cursor: %v", err)
	}
	appserver.HandleTyped(server.Router(), appwire.MethodThreadTurnsList, func(_ context.Context, _ appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
		response := positionedItemReadResponse(1, 2, "")
		response.Thread.Turns[0].ItemsView = appwire.TurnItemsViewFragment
		return appwire.ThreadTurnsListResponse{Data: response.Thread.Turns, PageUnit: appwire.TranscriptPageUnitItem, NextCursor: nativeCursorTwo}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()
	entry := rendezvous.Entry{
		Protocol: appwire.ProtocolVersion, Endpoint: "ws" + httpServer.URL[len("http"):], SourceID: "local",
		ThreadID: "thread", SessionID: "thread", WorkspaceRef: "local:thread", InstanceID: "instance-1",
	}
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry { return []LocalDaemonEntry{{Entry: entry}} }, httpServer.Client())
	first, err := source.ItemCandidatesFromRead(context.Background(), appwire.ThreadReadParams{Ref: "local:thread", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 4}, positionedItemReadResponse(3, 4, nativeCursorOne))
	if err != nil {
		t.Fatalf("bounded item read: %v", err)
	}
	browserCursor, err := appitempaging.EncodeCursor(first.Identity, first.Candidates.Candidates[0].Position)
	if err != nil {
		t.Fatalf("encode browser cursor: %v", err)
	}
	_, err = source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{Ref: "local:thread", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 2, Cursor: browserCursor})
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) {
		t.Fatalf("successor identity mismatch error = %T %v, want typed stale cursor", err, err)
	}
	stale := false
	switch data := wireErr.Data.(type) {
	case appwire.ErrorData:
		stale = data.EvenerErrorInfo == appwire.ErrorTranscriptItemCursorStale
	case map[string]any:
		stale = data["evenerErrorInfo"] == string(appwire.ErrorTranscriptItemCursorStale)
	}
	if !stale {
		t.Fatalf("successor identity mismatch error data = %#v, want stale cursor", wireErr.Data)
	}
}

func TestLocalDaemonNativeItemCursorBridgeRebasesBoundary(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "native-daemon", SourceID: "local"})
	nativeIdentity := appitempaging.CursorIdentity{ThreadRef: "local:thread", Incarnation: "daemon-incarnation", ProjectionVersion: 1}
	nativeCursor, err := appitempaging.EncodeCursor(nativeIdentity, appwire.ThreadItemPosition{Entry: 0, Item: 1})
	if err != nil {
		t.Fatalf("encode native cursor: %v", err)
	}
	var requests []appwire.ThreadTurnsListParams
	appserver.HandleTyped(server.Router(), appwire.MethodThreadTurnsList, func(_ context.Context, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
		requests = append(requests, params)
		response := positionedItemReadResponse(1, 2, "")
		response.Thread.Turns[0].ItemsView = appwire.TurnItemsViewFragment
		return appwire.ThreadTurnsListResponse{Data: response.Thread.Turns, PageUnit: appwire.TranscriptPageUnitItem}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()
	entry := rendezvous.Entry{
		Protocol: appwire.ProtocolVersion, Endpoint: "ws" + httpServer.URL[len("http"):], SourceID: "local",
		ThreadID: "thread", SessionID: "thread", WorkspaceRef: "local:thread", InstanceID: "instance-1",
	}
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry { return []LocalDaemonEntry{{Entry: entry}} }, httpServer.Client())
	response := positionedItemReadResponse(1, 4, nativeCursor)
	first, err := source.ItemCandidatesFromRead(context.Background(), appwire.ThreadReadParams{Ref: "local:thread", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 4}, response)
	if err != nil {
		t.Fatalf("bounded item read: %v", err)
	}
	browserCursor, err := appitempaging.EncodeCursor(first.Identity, appwire.ThreadItemPosition{Entry: 3, Item: 0})
	if err != nil {
		t.Fatalf("encode browser cursor: %v", err)
	}
	older, err := source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{
		Ref: "local:thread", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 2, Cursor: browserCursor,
	})
	if err != nil {
		t.Fatalf("rebased continuation: %v", err)
	}
	if len(older.Candidates.Candidates) != 2 || older.Candidates.Candidates[0].Position.Entry != 1 || older.Candidates.Candidates[1].Position.Entry != 2 {
		t.Fatalf("rebased candidates = %+v, want positions 1 and 2", older.Candidates.Candidates)
	}
	if len(requests) != 1 {
		t.Fatalf("native list calls = %d, want 1", len(requests))
	}
	if requests[0].Cursor == nativeCursor {
		t.Fatal("native continuation replayed retained token without rebasing")
	}
	gotBefore, err := appitempaging.DecodeCursor(requests[0].Cursor, nativeIdentity)
	if err != nil {
		t.Fatalf("decode rebased native cursor: %v", err)
	}
	if gotBefore != (appwire.ThreadItemPosition{Entry: 3, Item: 0}) {
		t.Fatalf("native cursor boundary = %+v, want browser boundary item 3", gotBefore)
	}
	if !older.Exhausted {
		t.Fatal("rebased continuation reported another page after item 1..2")
	}
}

func TestLocalDaemonNativeCursorFenceRotatesAndRecoversAfterDestructiveReset(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "native-daemon", SourceID: "local"})
	nativeIdentityOne := appitempaging.CursorIdentity{ThreadRef: "local:thread", Incarnation: "daemon-one", ProjectionVersion: 1}
	nativeIdentityTwo := appitempaging.CursorIdentity{ThreadRef: "local:thread", Incarnation: "daemon-two", ProjectionVersion: 1}
	nativeCursorOne, err := appitempaging.EncodeCursor(nativeIdentityOne, appwire.ThreadItemPosition{Entry: 0})
	if err != nil {
		t.Fatalf("encode first native cursor: %v", err)
	}
	nativeCursorTwo, err := appitempaging.EncodeCursor(nativeIdentityTwo, appwire.ThreadItemPosition{Entry: 0})
	if err != nil {
		t.Fatalf("encode second native cursor: %v", err)
	}
	var sawFirst bool
	var sawSecond bool
	appserver.HandleTyped(server.Router(), appwire.MethodThreadTurnsList, func(_ context.Context, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
		if _, err := appitempaging.DecodeCursor(params.Cursor, nativeIdentityOne); err == nil {
			sawFirst = true
			return appwire.ThreadTurnsListResponse{}, appwire.TranscriptItemCursorStale()
		}
		if _, err := appitempaging.DecodeCursor(params.Cursor, nativeIdentityTwo); err == nil {
			sawSecond = true
			response := positionedItemReadResponse(0, 4, "")
			response.Thread.Turns[0].ItemsView = appwire.TurnItemsViewFragment
			return appwire.ThreadTurnsListResponse{Data: response.Thread.Turns, PageUnit: appwire.TranscriptPageUnitItem}, nil
		}
		return appwire.ThreadTurnsListResponse{}, appwire.TranscriptItemCursorStale()
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()
	entry := rendezvous.Entry{
		Protocol: appwire.ProtocolVersion, Endpoint: "ws" + httpServer.URL[len("http"):], SourceID: "local",
		ThreadID: "thread", SessionID: "thread", WorkspaceRef: "local:thread", InstanceID: "instance-1",
	}
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry { return []LocalDaemonEntry{{Entry: entry}} }, httpServer.Client())
	first, err := source.ItemCandidatesFromRead(context.Background(), appwire.ThreadReadParams{Ref: "local:thread", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 5}, positionedItemReadResponseFor([]string{"item-10", "item-11", "item-12", "item-13", "item-14"}, []uint64{10, 11, 12, 13, 14}, nativeCursorOne))
	if err != nil {
		t.Fatalf("initial bounded item read: %v", err)
	}
	oldCursor, err := appitempaging.EncodeCursor(first.Identity, first.Candidates.Candidates[0].Position)
	if err != nil {
		t.Fatalf("encode browser cursor: %v", err)
	}
	fresh, err := source.ItemCandidatesFromRead(context.Background(), appwire.ThreadReadParams{Ref: "local:thread", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 5}, positionedItemReadResponseFor([]string{"item-10", "item-11", "item-12", "item-13", "item-14"}, []uint64{10, 11, 12, 13, 14}, nativeCursorTwo))
	if err != nil {
		t.Fatalf("observe destructive bounded response: %v", err)
	}
	if fresh.Identity == first.Identity {
		t.Fatalf("destructive bounded response identity = %+v, want rotation from %+v", fresh.Identity, first.Identity)
	}
	freshCursor, err := appitempaging.EncodeCursor(fresh.Identity, fresh.Candidates.Candidates[0].Position)
	if err != nil {
		t.Fatalf("encode fresh browser cursor: %v", err)
	}
	_, err = source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{Ref: "local:thread", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 5, Cursor: oldCursor})
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) {
		t.Fatalf("destructive native continuation error = %T %v, want typed stale cursor", err, err)
	}
	stale := false
	switch data := wireErr.Data.(type) {
	case appwire.ErrorData:
		stale = data.EvenerErrorInfo == appwire.ErrorTranscriptItemCursorStale
	case map[string]any:
		stale = data["evenerErrorInfo"] == string(appwire.ErrorTranscriptItemCursorStale)
	}
	if !stale {
		t.Fatalf("destructive native continuation error data = %#v, want stale cursor", wireErr.Data)
	}
	if sawFirst || sawSecond {
		t.Fatalf("stale old hub cursor reached native daemon: first %v/second %v", sawFirst, sawSecond)
	}
	older, err := source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{Ref: "local:thread", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 5, Cursor: freshCursor})
	if err != nil {
		t.Fatalf("fresh browser cursor recovery: %v", err)
	}
	if !sawSecond || sawFirst {
		t.Fatalf("fresh native identity observations = first %v/second %v, want second only", sawFirst, sawSecond)
	}
	if len(older.Candidates.Candidates) != 5 || older.Candidates.Candidates[0].Position.Entry != 0 || older.Candidates.Candidates[4].Position.Entry != 4 {
		t.Fatalf("fresh recovery candidates = %+v, want exact positions 0..4", older.Candidates.Candidates)
	}
}

func TestLocalDaemonNativeCursorFencePreservesSameIncarnationTailAppend(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "native-daemon", SourceID: "local"})
	nativeIdentity := appitempaging.CursorIdentity{ThreadRef: "local:thread", Incarnation: "daemon-one", ProjectionVersion: 1}
	nativeCursor, err := appitempaging.EncodeCursor(nativeIdentity, appwire.ThreadItemPosition{Entry: 0})
	if err != nil {
		t.Fatalf("encode native cursor: %v", err)
	}
	var sawNative bool
	appserver.HandleTyped(server.Router(), appwire.MethodThreadTurnsList, func(_ context.Context, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
		if _, err := appitempaging.DecodeCursor(params.Cursor, nativeIdentity); err != nil {
			return appwire.ThreadTurnsListResponse{}, appwire.TranscriptItemCursorStale()
		}
		sawNative = true
		response := positionedItemReadResponse(0, 0, "")
		response.Thread.Turns[0].ItemsView = appwire.TurnItemsViewFragment
		return appwire.ThreadTurnsListResponse{Data: response.Thread.Turns, PageUnit: appwire.TranscriptPageUnitItem}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()
	entry := rendezvous.Entry{
		Protocol: appwire.ProtocolVersion, Endpoint: "ws" + httpServer.URL[len("http"):], SourceID: "local",
		ThreadID: "thread", SessionID: "thread", WorkspaceRef: "local:thread", InstanceID: "instance-1",
	}
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry { return []LocalDaemonEntry{{Entry: entry}} }, httpServer.Client())
	initial := positionedItemReadResponseFor([]string{"item-10", "item-11", "item-12", "item-13", "item-14"}, []uint64{10, 11, 12, 13, 14}, nativeCursor)
	first, err := source.ItemCandidatesFromRead(context.Background(), appwire.ThreadReadParams{Ref: "local:thread", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 5}, initial)
	if err != nil {
		t.Fatalf("initial bounded item read: %v", err)
	}
	oldCursor, err := appitempaging.EncodeCursor(first.Identity, first.Candidates.Candidates[0].Position)
	if err != nil {
		t.Fatalf("encode browser cursor: %v", err)
	}
	tail := positionedItemReadResponseFor([]string{"item-10", "item-11", "item-12", "item-13", "item-14", "item-15"}, []uint64{10, 11, 12, 13, 14, 15}, nativeCursor)
	second, err := source.ItemCandidatesFromRead(context.Background(), appwire.ThreadReadParams{Ref: "local:thread", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 6}, tail)
	if err != nil {
		t.Fatalf("observe same-incarnation tail append: %v", err)
	}
	if second.Identity != first.Identity {
		t.Fatalf("tail append identity = %+v, want unchanged %+v", second.Identity, first.Identity)
	}
	if _, err := source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{Ref: "local:thread", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 5, Cursor: oldCursor}); err != nil {
		t.Fatalf("same-incarnation tail continuation: %v", err)
	}
	if !sawNative {
		t.Fatal("same-incarnation tail continuation did not reach native daemon")
	}
}

func TestLocalDaemonNativeItemCursorBridgeFailsSafely(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "native-daemon", SourceID: "local"})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadTurnsList, func(_ context.Context, _ appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
		response := positionedItemReadResponse(0, 0, "")
		response.Thread.Turns[0].ItemsView = appwire.TurnItemsViewFragment
		return appwire.ThreadTurnsListResponse{Data: response.Thread.Turns, PageUnit: appwire.TranscriptPageUnitItem}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()
	var entry = rendezvous.Entry{
		Protocol: appwire.ProtocolVersion, Endpoint: "ws" + httpServer.URL[len("http"):], SourceID: "local",
		ThreadID: "thread", SessionID: "thread", WorkspaceRef: "local:thread", InstanceID: "instance-1",
	}
	newSource := func(t *testing.T) (*LocalDaemonSource, string) {
		t.Helper()
		source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry { return []LocalDaemonEntry{{Entry: entry}} }, httpServer.Client())
		response := positionedItemReadResponse(0, 1, "native-secret")
		first, err := source.ItemCandidatesFromRead(context.Background(), appwire.ThreadReadParams{Ref: "local:thread", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 2}, response)
		if err != nil {
			t.Fatalf("bounded item read: %v", err)
		}
		browserCursor, err := appitempaging.EncodeCursor(first.Identity, first.Candidates.Candidates[0].Position)
		if err != nil {
			t.Fatalf("encode browser cursor: %v", err)
		}
		return source, browserCursor
	}
	assertStale := func(t *testing.T, err error, secret string) {
		t.Helper()
		var wireErr appwire.WireError
		if !errors.As(err, &wireErr) {
			t.Fatalf("error = %T %v, want typed stale cursor", err, err)
		}
		data, ok := wireErr.Data.(appwire.ErrorData)
		if !ok || data.EvenerErrorInfo != appwire.ErrorTranscriptItemCursorStale {
			t.Fatalf("error data = %#v, want stale cursor", wireErr.Data)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("stale error leaked opaque token %q: %v", secret, err)
		}
	}
	listParams := func(cursor string) appwire.ThreadTurnsListParams {
		return appwire.ThreadTurnsListParams{Ref: "local:thread", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 2, Cursor: cursor}
	}

	t.Run("retained token loss", func(t *testing.T) {
		source, browserCursor := newSource(t)
		state, ok := source.itemSnapshots.get("local:thread")
		if !ok {
			t.Fatal("native state was not retained")
		}
		state.NativeCursor = ""
		source.itemSnapshots.put("local:thread", state)
		_, err := source.ListItemCandidates(context.Background(), listParams(browserCursor))
		assertStale(t, err, "native-secret")
	})
	t.Run("daemon identity mismatch", func(t *testing.T) {
		source, browserCursor := newSource(t)
		entry.InstanceID = "instance-2"
		defer func() { entry.InstanceID = "instance-1" }()
		_, err := source.ListItemCandidates(context.Background(), listParams(browserCursor))
		assertStale(t, err, "native-secret")
	})
	t.Run("malformed native token", func(t *testing.T) {
		source, browserCursor := newSource(t)
		_, err := source.ListItemCandidates(context.Background(), listParams(browserCursor))
		assertStale(t, err, "native-secret")
	})
	t.Run("absent non-native state", func(t *testing.T) {
		source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry { return []LocalDaemonEntry{{Entry: entry}} }, httpServer.Client())
		_, err := source.ListItemCandidates(context.Background(), listParams("browser-cursor"))
		assertStale(t, err, "browser-cursor")
	})
}

func TestLocalDaemonNativeCursorStalesAfterSnapshotStateEviction(t *testing.T) {
	const target = "evicted-thread"
	entries := make([]LocalDaemonEntry, 0, defaultItemSnapshotStateEntries+1)
	for i := range defaultItemSnapshotStateEntries + 1 {
		threadID := target
		if i > 0 {
			threadID = fmt.Sprintf("evictor-%02d", i)
		}
		entries = append(entries, LocalDaemonEntry{Entry: rendezvous.Entry{
			Protocol: appwire.ProtocolVersion, Endpoint: "ws://unused.test", SourceID: "local", ThreadID: threadID, SessionID: threadID,
			WorkspaceRef: "local:" + threadID, InstanceID: "instance-1",
		}})
	}
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry { return entries }, nil)
	first, err := source.ItemCandidatesFromRead(context.Background(), appwire.ThreadReadParams{Ref: "local:" + target, PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 2}, positionedItemReadResponse(0, 1, "native-target"))
	if err != nil {
		t.Fatalf("initial bounded item read: %v", err)
	}
	browserCursor, err := appitempaging.EncodeCursor(first.Identity, first.Candidates.Candidates[0].Position)
	if err != nil {
		t.Fatalf("encode browser cursor: %v", err)
	}
	for i := 1; i < len(entries); i++ {
		threadID := entries[i].Entry.ThreadID
		if _, err := source.ItemCandidatesFromRead(context.Background(), appwire.ThreadReadParams{Ref: "local:" + threadID, PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 2}, positionedItemReadResponse(0, 1, "native-evictor")); err != nil {
			t.Fatalf("bounded item read %s: %v", threadID, err)
		}
	}
	_, err = source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{Ref: "local:" + target, PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 2, Cursor: browserCursor})
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) {
		t.Fatalf("evicted native cursor error = %T %v, want typed stale cursor", err, err)
	}
	data, ok := wireErr.Data.(appwire.ErrorData)
	if !ok || data.EvenerErrorInfo != appwire.ErrorTranscriptItemCursorStale {
		t.Fatalf("evicted native cursor error data = %#v, want stale cursor", wireErr.Data)
	}
}

func newLocalDaemonItemTransitionSource(t *testing.T, items []appwire.ThreadItem) (*LocalDaemonSource, func([]appwire.ThreadItem)) {
	t.Helper()
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	currentItems := items
	appserver.HandleTyped(server.Router(), appwire.MethodThreadTurnsList, func(_ context.Context, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
		itemsView := appwire.TurnItemsViewFull
		if params.PageUnit == appwire.TranscriptPageUnitItem {
			itemsView = appwire.TurnItemsViewFragment
		}
		return appwire.ThreadTurnsListResponse{
			Data:     []appwire.Turn{{ID: "turn-1", Items: currentItems, ItemsView: itemsView}},
			PageUnit: params.PageUnit,
		}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	t.Cleanup(httpServer.Close)
	entry := rendezvous.Entry{
		Protocol: appwire.ProtocolVersion, Endpoint: "ws" + httpServer.URL[len("http"):],
		SourceID: "local", ThreadID: "thread", SessionID: "thread", WorkspaceRef: "local:thread",
		InstanceID: "transition-instance",
	}
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{{Entry: entry}}
	}, httpServer.Client())
	return source, func(next []appwire.ThreadItem) { currentItems = next }
}

func TestLocalDaemonItemSnapshotBoundedToCompleteTransitions(t *testing.T) {
	item := func(id string, ordinal uint32) appwire.ThreadItem {
		position := appwire.ThreadItemPosition{Entry: 0, Item: ordinal}
		return appwire.ThreadItem{
			Type: "agentMessage", ID: id, TranscriptKey: "key-" + id, Position: &position, TurnID: "turn-1", Text: id,
		}
	}
	read := func(items []appwire.ThreadItem, olderCursor string) appwire.ThreadReadResponse {
		return appwire.ThreadReadResponse{
			Thread: appwire.Thread{ID: "thread", Evener: appwire.EvenerThread{Ref: "local:thread"}, Turns: []appwire.Turn{{
				ID: "turn-1", Items: items, ItemsView: appwire.TurnItemsViewFragment, HasEarlierItems: olderCursor != "",
			}}},
			PageUnit: appwire.TranscriptPageUnitItem, OlderCursor: olderCursor,
		}
	}

	t.Run("nonempty bounded B,C then full X,B,C", func(t *testing.T) {
		source, setItems := newLocalDaemonItemTransitionSource(t, []appwire.ThreadItem{item("X", 0), item("B", 1), item("C", 2)})
		bounded, err := source.ItemCandidatesFromRead(context.Background(), appwire.ThreadReadParams{Ref: "local:thread"}, read([]appwire.ThreadItem{item("B", 1), item("C", 2)}, "daemon-cursor"))
		if err != nil {
			t.Fatalf("bounded conversion: %v", err)
		}
		oldCursor, err := appitempaging.EncodeCursor(bounded.Identity, bounded.Candidates.Candidates[0].Position)
		if err != nil {
			t.Fatalf("encode bounded cursor: %v", err)
		}
		full, err := source.ItemCandidatesFromRead(context.Background(), appwire.ThreadReadParams{Ref: "local:thread"}, read([]appwire.ThreadItem{item("X", 0), item("B", 1), item("C", 2)}, ""))
		if err != nil {
			t.Fatalf("full conversion: %v", err)
		}
		if full.Identity.Incarnation != bounded.Identity.Incarnation {
			t.Fatalf("bounded-to-full incarnation = %q, want unchanged %q", full.Identity.Incarnation, bounded.Identity.Incarnation)
		}
		setItems([]appwire.ThreadItem{item("X", 0), item("B", 1), item("C", 2)})
		continued, err := source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{Ref: "local:thread", Cursor: oldCursor, ItemLimit: 40})
		if err != nil {
			t.Fatalf("continue cursor after full materialization: %v", err)
		}
		if len(continued.Candidates.Candidates) != 1 || continued.Candidates.Candidates[0].Item.ID != "X" {
			t.Fatalf("continued bounded cursor = %+v, want X", continued.Candidates.Candidates)
		}
	})

	t.Run("empty bounded then full", func(t *testing.T) {
		source, setItems := newLocalDaemonItemTransitionSource(t, []appwire.ThreadItem{item("X", 0)})
		bounded, err := source.ItemCandidatesFromRead(context.Background(), appwire.ThreadReadParams{Ref: "local:thread"}, read(nil, "daemon-cursor"))
		if err != nil {
			t.Fatalf("empty bounded conversion: %v", err)
		}
		oldCursor, err := appitempaging.EncodeCursor(bounded.Identity, appwire.ThreadItemPosition{Entry: 0, Item: 0})
		if err != nil {
			t.Fatalf("encode empty bounded cursor: %v", err)
		}
		full, err := source.ItemCandidatesFromRead(context.Background(), appwire.ThreadReadParams{Ref: "local:thread"}, read([]appwire.ThreadItem{item("X", 0)}, ""))
		if err != nil {
			t.Fatalf("full conversion after empty bounded: %v", err)
		}
		if full.Identity.Incarnation == bounded.Identity.Incarnation {
			t.Fatalf("empty-bounded-to-full incarnation = %q, want rotation from %q", full.Identity.Incarnation, bounded.Identity.Incarnation)
		}
		setItems([]appwire.ThreadItem{item("X", 0)})
		if _, err := source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{Ref: "local:thread", Cursor: oldCursor, ItemLimit: 40}); err == nil {
			t.Fatal("cursor from empty bounded prefix was accepted after full prefix materialization")
		}
	})

	t.Run("full unchanged and append preserve incarnation", func(t *testing.T) {
		source, _ := newLocalDaemonItemTransitionSource(t, []appwire.ThreadItem{item("A", 0), item("B", 1)})
		first, err := source.ItemCandidatesFromRead(context.Background(), appwire.ThreadReadParams{Ref: "local:thread"}, read([]appwire.ThreadItem{item("A", 0), item("B", 1)}, ""))
		if err != nil {
			t.Fatalf("initial full conversion: %v", err)
		}
		unchanged, err := source.ItemCandidatesFromRead(context.Background(), appwire.ThreadReadParams{Ref: "local:thread"}, read([]appwire.ThreadItem{item("A", 0), item("B", 1)}, ""))
		if err != nil {
			t.Fatalf("unchanged full conversion: %v", err)
		}
		if unchanged.Identity.Incarnation != first.Identity.Incarnation {
			t.Fatalf("unchanged full incarnation = %q, want %q", unchanged.Identity.Incarnation, first.Identity.Incarnation)
		}
		appended, err := source.ItemCandidatesFromRead(context.Background(), appwire.ThreadReadParams{Ref: "local:thread"}, read([]appwire.ThreadItem{item("A", 0), item("B", 1), item("C", 2)}, ""))
		if err != nil {
			t.Fatalf("appended full conversion: %v", err)
		}
		if appended.Identity.Incarnation != first.Identity.Incarnation {
			t.Fatalf("appended full incarnation = %q, want %q", appended.Identity.Incarnation, first.Identity.Incarnation)
		}
	})

	t.Run("full A,B then bounded disjoint C,D", func(t *testing.T) {
		source, _ := newLocalDaemonItemTransitionSource(t, []appwire.ThreadItem{item("A", 0), item("B", 1)})
		nativeIdentity := appitempaging.CursorIdentity{ThreadRef: "local:thread", Incarnation: "daemon-incarnation", ProjectionVersion: 1}
		nativeOlderCursor, err := appitempaging.EncodeCursor(nativeIdentity, appwire.ThreadItemPosition{Entry: 0, Item: 2})
		if err != nil {
			t.Fatalf("encode native older cursor: %v", err)
		}
		complete, err := source.ItemCandidatesFromRead(context.Background(), appwire.ThreadReadParams{Ref: "local:thread"}, read([]appwire.ThreadItem{item("A", 0), item("B", 1)}, ""))
		if err != nil {
			t.Fatalf("complete conversion: %v", err)
		}
		bounded, err := source.ItemCandidatesFromRead(context.Background(), appwire.ThreadReadParams{Ref: "local:thread"}, read([]appwire.ThreadItem{item("C", 2), item("D", 3)}, nativeOlderCursor))
		if err != nil {
			t.Fatalf("bounded disjoint conversion: %v", err)
		}
		if bounded.Identity.Incarnation != complete.Identity.Incarnation {
			t.Fatalf("complete [A,B] -> bounded disjoint [C,D] incarnation = %q, want unchanged %q", bounded.Identity.Incarnation, complete.Identity.Incarnation)
		}
		browserCursor, err := appitempaging.EncodeCursor(bounded.Identity, bounded.Candidates.Candidates[0].Position)
		if err != nil {
			t.Fatalf("encode bounded browser cursor: %v", err)
		}
		continued, err := source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{Ref: "local:thread", Cursor: browserCursor, ItemLimit: 40})
		if err != nil {
			t.Fatalf("continue bounded disjoint cursor: %v", err)
		}
		if len(continued.Candidates.Candidates) != 2 || continued.Candidates.Candidates[0].Item.ID != "A" || continued.Candidates.Candidates[1].Item.ID != "B" {
			t.Fatalf("continued bounded disjoint cursor = %+v, want A,B", continued.Candidates.Candidates)
		}
	})

	t.Run("full A,B then bounded disjoint C,D with rewritten hidden prefix", func(t *testing.T) {
		source, _ := newLocalDaemonItemTransitionSource(t, []appwire.ThreadItem{item("X", 0), item("Y", 1)})
		nativeIdentity := appitempaging.CursorIdentity{ThreadRef: "local:thread", Incarnation: "daemon-incarnation", ProjectionVersion: 1}
		nativeOlderCursor, err := appitempaging.EncodeCursor(nativeIdentity, appwire.ThreadItemPosition{Entry: 0, Item: 2})
		if err != nil {
			t.Fatalf("encode native older cursor: %v", err)
		}
		complete, err := source.ItemCandidatesFromRead(context.Background(), appwire.ThreadReadParams{Ref: "local:thread"}, read([]appwire.ThreadItem{item("A", 0), item("B", 1)}, ""))
		if err != nil {
			t.Fatalf("complete conversion: %v", err)
		}
		bounded, err := source.ItemCandidatesFromRead(context.Background(), appwire.ThreadReadParams{Ref: "local:thread"}, read([]appwire.ThreadItem{item("C", 2), item("D", 3)}, nativeOlderCursor))
		if err != nil {
			t.Fatalf("bounded disjoint conversion: %v", err)
		}
		if bounded.Identity.Incarnation == complete.Identity.Incarnation {
			t.Fatalf("rewritten hidden prefix preserved incarnation %q", bounded.Identity.Incarnation)
		}
	})
}

func TestLocalDaemonItemPagingIsolatesSharedWorkspaceAliases(t *testing.T) {
	const (
		rootThreadID  = "root-session"
		childThreadID = "child-session"
		workspaceRef  = "local:root-session"
	)
	itemsForThread := func(threadID string) []appwire.ThreadItem {
		items := make([]appwire.ThreadItem, 3)
		for i := range items {
			position := appwire.ThreadItemPosition{Entry: 0, Item: uint32(i)}
			items[i] = appwire.ThreadItem{
				Type:          "agentMessage",
				ID:            fmt.Sprintf("%s-item-%d", threadID, i),
				TranscriptKey: fmt.Sprintf("%s-key-%d", threadID, i),
				Position:      &position,
				TurnID:        threadID + "-turn",
				Text:          fmt.Sprintf("%s-text-%d", threadID, i),
			}
		}
		return items
	}

	server := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	var (
		requestsMu sync.Mutex
		requests   []appwire.ThreadTurnsListParams
	)
	appserver.HandleTyped(server.Router(), appwire.MethodThreadTurnsList, func(_ context.Context, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
		requestsMu.Lock()
		requests = append(requests, params)
		requestsMu.Unlock()
		switch params.ThreadID {
		case rootThreadID, childThreadID:
			return appwire.ThreadTurnsListResponse{Data: []appwire.Turn{{
				ID: params.ThreadID + "-turn", Items: itemsForThread(params.ThreadID), ItemsView: appwire.TurnItemsViewFull,
			}}}, nil
		default:
			return appwire.ThreadTurnsListResponse{}, fmt.Errorf("unexpected thread ID %q", params.ThreadID)
		}
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()

	daemonEntry := rendezvous.Entry{
		Protocol:     appwire.ProtocolVersion,
		Endpoint:     "ws" + httpServer.URL[len("http"):],
		SourceID:     "local",
		ThreadID:     rootThreadID,
		WorkspaceRef: workspaceRef,
		InstanceID:   "shared-daemon-instance",
		HubToken:     "authenticated-token",
	}
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{
			{Entry: daemonEntry, SessionID: rootThreadID},
			{Entry: daemonEntry, SessionID: childThreadID, ReadOnlyAlias: true, OwnerSessionID: rootThreadID},
		}
	}, httpServer.Client())

	readPage := func(ref, cursor string) (ItemCandidateResult, error) {
		return source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{
			Ref: ref, PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 2, Cursor: cursor,
		})
	}
	assertItemIDs := func(label string, result ItemCandidateResult, want ...string) {
		t.Helper()
		got := make([]string, len(result.Candidates.Candidates))
		for i, candidate := range result.Candidates.Candidates {
			got[i] = candidate.Item.ID
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("%s item IDs = %v, want %v", label, got, want)
		}
	}

	rootFirst, err := readPage(workspaceRef, "")
	if err != nil {
		t.Fatalf("root page 1: %v", err)
	}
	assertItemIDs("root page 1", rootFirst, "root-session-item-1", "root-session-item-2")
	if rootFirst.Candidates.OlderCursor == "" {
		t.Fatal("root page 1 has no older cursor")
	}

	childRef := appwire.Ref{SourceID: "local", ThreadID: childThreadID}.String()
	childFirst, err := readPage(childRef, "")
	if err != nil {
		t.Fatalf("child page 1: %v", err)
	}
	assertItemIDs("child page 1", childFirst, "child-session-item-1", "child-session-item-2")
	if childFirst.Candidates.OlderCursor == "" {
		t.Fatal("child page 1 has no older cursor")
	}

	rootSecond, err := readPage(workspaceRef, rootFirst.Candidates.OlderCursor)
	if err != nil {
		t.Fatalf("root page 2 after child page 1: %v", err)
	}
	assertItemIDs("root page 2", rootSecond, "root-session-item-0")
	childSecond, err := readPage(childRef, childFirst.Candidates.OlderCursor)
	if err != nil {
		t.Fatalf("child page 2 after root page 2: %v", err)
	}
	assertItemIDs("child page 2", childSecond, "child-session-item-0")

	if rootFirst.Identity.ThreadRef != workspaceRef {
		t.Fatalf("root paging thread ref = %q, want %q", rootFirst.Identity.ThreadRef, workspaceRef)
	}
	if childFirst.Identity.ThreadRef != childRef {
		t.Fatalf("child paging thread ref = %q, want %q", childFirst.Identity.ThreadRef, childRef)
	}
	requestsMu.Lock()
	defer requestsMu.Unlock()
	wantThreadIDs := []string{rootThreadID, childThreadID, rootThreadID, childThreadID}
	if len(requests) != len(wantThreadIDs) {
		t.Fatalf("daemon materialization calls = %d, want %d: %+v", len(requests), len(wantThreadIDs), requests)
	}
	for i, request := range requests {
		if request.Ref != workspaceRef || request.ThreadID != wantThreadIDs[i] {
			t.Fatalf("daemon materialization call %d route = ref %q thread %q, want ref %q thread %q", i, request.Ref, request.ThreadID, workspaceRef, wantThreadIDs[i])
		}
		if request.PageUnit != appwire.TranscriptPageUnitTurn || request.Cursor != "" {
			t.Fatalf("daemon materialization call %d paging = %+v, want uncursored turn page", i, request)
		}
	}
}

func TestLocalDaemonItemCandidatesMaterializeNativePagesChronologically(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	turns := make([]appwire.Turn, 31)
	for i := range turns {
		position := appwire.ThreadItemPosition{Entry: uint64(i), Item: 0}
		turns[i] = appwire.Turn{ID: fmt.Sprintf("turn-%02d", i), Items: []appwire.ThreadItem{{
			Type: "agentMessage", ID: fmt.Sprintf("item-%02d", i), TranscriptKey: fmt.Sprintf("daemon-key-%02d", i), Position: &position,
			TurnID: fmt.Sprintf("turn-%02d", i), Text: fmt.Sprintf("text-%02d", i),
		}}}
	}
	appserver.HandleTyped(server.Router(), appwire.MethodThreadTurnsList, func(_ context.Context, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
		if params.Cursor == "" {
			return appwire.ThreadTurnsListResponse{Data: turns[1:], NextCursor: "older"}, nil
		}
		if params.Cursor == "older" {
			return appwire.ThreadTurnsListResponse{Data: turns[:1]}, nil
		}
		return appwire.ThreadTurnsListResponse{}, fmt.Errorf("unexpected native cursor %q", params.Cursor)
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()
	entry := rendezvous.Entry{
		Protocol: appwire.ProtocolVersion, Endpoint: "ws" + httpServer.URL[len("http"):], SourceID: "local", ThreadID: "thread", SessionID: "thread",
		WorkspaceRef: "local:thread", InstanceID: "instance-1", HubToken: "authenticated-token",
	}
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry { return []LocalDaemonEntry{{Entry: entry}} }, httpServer.Client())

	result, err := source.ReadItemCandidates(context.Background(), appwire.ThreadReadParams{Ref: "local:thread", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 1})
	if err != nil {
		t.Fatalf("multi-page item read: %v", err)
	}
	if len(result.Candidates.Candidates) != 1 || result.Candidates.Candidates[0].Item.ID != "item-30" {
		t.Fatalf("latest multi-page item = %+v, want newest item-30", result.Candidates.Candidates)
	}
}

func TestLocalDaemonItemCandidatesRejectLegacyV3MaterializedMetadata(t *testing.T) {
	turns := []appwire.Turn{
		{ID: "turn-0", Items: []appwire.ThreadItem{
			{Type: "agentMessage", ID: "item-0-0", Text: "oldest"},
			{Type: "agentMessage", ID: "item-0-1", Text: "older"},
		}},
		// A turn-shaped projection can contain no items and can omit decoded
		// transcript entries that project to no turn at all. The following item's
		// turn ordinal therefore cannot identify its absolute transcript entry.
		{ID: "turn-with-zero-items"},
		{ID: "turn-2", Items: []appwire.ThreadItem{
			{Type: "agentMessage", ID: "item-2-0", Text: "newer"},
			{Type: "agentMessage", ID: "item-2-1", Text: "newest"},
		}},
	}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "legacy-v3-daemon", SourceID: "local"})
	var (
		requests    []appwire.ThreadTurnsListParams
		authHeaders []string
	)
	appserver.HandleTyped(server.Router(), appwire.MethodThreadTurnsList, func(_ context.Context, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
		requests = append(requests, params)
		switch params.Cursor {
		case "":
			return appwire.ThreadTurnsListResponse{Data: turns[1:], NextCursor: "legacy-older"}, nil
		case "legacy-older":
			return appwire.ThreadTurnsListResponse{Data: turns[:1]}, nil
		default:
			return appwire.ThreadTurnsListResponse{}, fmt.Errorf("unexpected native cursor %q", params.Cursor)
		}
	})
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeaders = append(authHeaders, r.Header.Get("Authorization"))
		server.ServeWebSocket(w, r)
	}))
	defer httpServer.Close()
	entry := rendezvous.Entry{
		Protocol: appwire.ProtocolVersion, Endpoint: "ws" + httpServer.URL[len("http"):], SourceID: "local", ThreadID: "legacy-thread", SessionID: "legacy-thread",
		WorkspaceRef: "local:legacy-thread", InstanceID: "legacy-v3-instance", HubToken: "authenticated-token",
	}
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry { return []LocalDaemonEntry{{Entry: entry}} }, httpServer.Client())

	result, err := source.ReadItemCandidates(context.Background(), appwire.ThreadReadParams{
		Ref: "local:legacy-thread", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 3,
	})
	if err == nil || !strings.Contains(err.Error(), "unpositioned item") {
		t.Fatalf("legacy-v3 materialized item page = (%+v, %v), want unpositioned item identity error", result, err)
	}

	wantNativeCursors := []string{"", "legacy-older"}
	if len(requests) != len(wantNativeCursors) {
		t.Fatalf("legacy-v3 daemon calls = %d, want %d: %+v", len(requests), len(wantNativeCursors), requests)
	}
	if len(authHeaders) != len(requests) {
		t.Fatalf("legacy-v3 authenticated WebSockets = %d, want %d", len(authHeaders), len(requests))
	}
	for i, request := range requests {
		if request.Ref != "local:legacy-thread" || request.ThreadID != "legacy-thread" || request.PageUnit != appwire.TranscriptPageUnitTurn || request.ItemsView != string(appwire.TurnItemsViewFull) || request.Cursor != wantNativeCursors[i] {
			t.Fatalf("legacy-v3 daemon call %d = %+v, want shared route, resolved thread, full turn materialization, cursor %q", i, request, wantNativeCursors[i])
		}
		if authHeaders[i] != "Bearer authenticated-token" {
			t.Fatalf("legacy-v3 daemon call %d authorization = %q, want bearer token", i, authHeaders[i])
		}
	}
}

func TestLocalDaemonMaterializedCompatibilityPreservesStrictMetadataBoundary(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "metadata-v3-daemon", SourceID: "local"})
	var turns []appwire.Turn
	appserver.HandleTyped(server.Router(), appwire.MethodThreadTurnsList, func(_ context.Context, _ appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
		return appwire.ThreadTurnsListResponse{Data: turns}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()
	entry := rendezvous.Entry{
		Protocol: appwire.ProtocolVersion, Endpoint: "ws" + httpServer.URL[len("http"):], SourceID: "local", ThreadID: "metadata-thread", SessionID: "metadata-thread",
		WorkspaceRef: "local:metadata-thread", InstanceID: "metadata-v3-instance", HubToken: "authenticated-token",
	}
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry { return []LocalDaemonEntry{{Entry: entry}} }, httpServer.Client())
	read := func() (ItemCandidateResult, error) {
		return source.ReadItemCandidates(context.Background(), appwire.ThreadReadParams{
			Ref: "local:metadata-thread", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 10,
		})
	}

	originalPosition := appwire.ThreadItemPosition{Entry: 12, Item: 34}
	turns = []appwire.Turn{{ID: "modern-turn", Items: []appwire.ThreadItem{{
		Type: "agentMessage", ID: "modern-item", TurnID: "modern-item-turn", TranscriptKey: "modern-original-key", Position: &originalPosition,
	}}}}
	modern, err := read()
	if err != nil {
		t.Fatalf("complete modern metadata: %v", err)
	}
	if len(modern.Candidates.Candidates) != 1 {
		t.Fatalf("modern candidates = %+v, want one", modern.Candidates.Candidates)
	}
	modernItem := modern.Candidates.Candidates[0].Item
	if modernItem.TranscriptKey != "modern-original-key" || modernItem.Position == nil || *modernItem.Position != originalPosition || modernItem.TurnID != "modern-item-turn" {
		t.Fatalf("modern metadata changed = %+v", modernItem)
	}
	continuationCursor, err := appitempaging.EncodeCursor(modern.Identity, originalPosition)
	if err != nil {
		t.Fatalf("encode modern continuation cursor: %v", err)
	}
	turns = []appwire.Turn{
		{ID: "legacy-before", Items: []appwire.ThreadItem{{Type: "agentMessage", ID: "legacy-before-item"}}},
		{ID: "legacy-zero-items"},
		{ID: "legacy-after", Items: []appwire.ThreadItem{{Type: "agentMessage", ID: "legacy-after-item"}}},
	}
	continued, continuationErr := source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{
		Ref: "local:metadata-thread", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 10, Cursor: continuationCursor,
	})
	if continuationErr == nil || !strings.Contains(continuationErr.Error(), "unpositioned item") {
		t.Fatalf("legacy materialized continuation = (%+v, %v), want unpositioned item identity error", continued, continuationErr)
	}

	for _, test := range []struct {
		name  string
		items []appwire.ThreadItem
	}{
		{name: "mixed legacy and modern", items: []appwire.ThreadItem{
			{Type: "agentMessage", ID: "legacy-item"},
			{Type: "agentMessage", ID: "modern-item", TranscriptKey: "modern-key", Position: &appwire.ThreadItemPosition{Entry: 0, Item: 1}},
		}},
		{name: "position only", items: []appwire.ThreadItem{{
			Type: "agentMessage", ID: "position-only", Position: &appwire.ThreadItemPosition{Entry: 0, Item: 0},
		}}},
		{name: "transcript key only", items: []appwire.ThreadItem{{
			Type: "agentMessage", ID: "key-only", TranscriptKey: "existing-key",
		}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			turns = []appwire.Turn{{ID: "malformed-turn", Items: test.items}}
			if result, readErr := read(); readErr == nil {
				t.Fatalf("malformed materialized metadata returned %+v", result)
			}
		})
	}

	initialParams := appwire.ThreadReadParams{Ref: "local:metadata-thread", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 1}
	for _, test := range []struct {
		name     string
		response appwire.ThreadReadResponse
	}{
		{name: "explicit item mode", response: appwire.ThreadReadResponse{
			PageUnit: appwire.TranscriptPageUnitItem,
			Thread: appwire.Thread{Turns: []appwire.Turn{{ID: "native-item-turn", Items: []appwire.ThreadItem{{
				Type: "agentMessage", ID: "native-unpositioned",
			}}}}},
		}},
		{name: "partial legacy cursor", response: appwire.ThreadReadResponse{
			OlderCursor: "legacy-older",
			Thread: appwire.Thread{Turns: []appwire.Turn{{ID: "partial-legacy-turn", Items: []appwire.ThreadItem{{
				Type: "agentMessage", ID: "partial-legacy-unpositioned",
			}}}}},
		}},
		{name: "partial legacy fragment", response: appwire.ThreadReadResponse{
			Thread: appwire.Thread{Turns: []appwire.Turn{{
				ID: "fragment-legacy-turn", ItemsView: appwire.TurnItemsViewFragment, HasEarlierItems: true,
				Items: []appwire.ThreadItem{{Type: "agentMessage", ID: "fragment-legacy-unpositioned"}},
			}}},
		}},
		{name: "complete legacy with zero-item and invisible entries", response: appwire.ThreadReadResponse{
			Thread: appwire.Thread{Turns: []appwire.Turn{
				{ID: "legacy-before", Items: []appwire.ThreadItem{{Type: "agentMessage", ID: "legacy-before-item"}}},
				{ID: "legacy-zero-items"},
				{ID: "legacy-after", Items: []appwire.ThreadItem{{Type: "agentMessage", ID: "legacy-after-item"}}},
			}},
		}},
		{name: "initial mixed legacy and modern", response: appwire.ThreadReadResponse{
			Thread: appwire.Thread{Turns: []appwire.Turn{{ID: "mixed-initial-turn", Items: []appwire.ThreadItem{
				{Type: "agentMessage", ID: "legacy-item"},
				{Type: "agentMessage", ID: "modern-item", TranscriptKey: "modern-key", Position: &appwire.ThreadItemPosition{Entry: 0, Item: 1}},
			}}}},
		}},
		{name: "initial position only", response: appwire.ThreadReadResponse{
			Thread: appwire.Thread{Turns: []appwire.Turn{{ID: "position-only-initial-turn", Items: []appwire.ThreadItem{{
				Type: "agentMessage", ID: "position-only", Position: &appwire.ThreadItemPosition{Entry: 0, Item: 0},
			}}}}},
		}},
		{name: "initial transcript key only", response: appwire.ThreadReadResponse{
			Thread: appwire.Thread{Turns: []appwire.Turn{{ID: "key-only-initial-turn", Items: []appwire.ThreadItem{{
				Type: "agentMessage", ID: "key-only", TranscriptKey: "existing-key",
			}}}}},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if result, err := source.ItemCandidatesFromRead(context.Background(), initialParams, test.response); err == nil {
				t.Fatalf("strict initial metadata boundary returned %+v", result)
			}
		})
	}
}

func TestLocalDaemonItemPagingCycleErrorDoesNotLeakNativeCursor(t *testing.T) {
	const secret = "local-secret-cursor"
	respond := func(method string) (any, error) {
		switch method {
		case appwire.MethodInitialize:
			return appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion}, nil
		case appwire.MethodThreadTurnsList:
			return appwire.ThreadTurnsListResponse{NextCursor: secret}, nil
		default:
			return nil, fmt.Errorf("unexpected method %q", method)
		}
	}
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{{Entry: rendezvous.Entry{
			Protocol: appwire.ProtocolVersion, Endpoint: "ws://daemon", SourceID: "local", ThreadID: "thread", SessionID: "thread", WorkspaceRef: "local:thread",
		}}}
	}, nil)
	source.dial = func(context.Context, string, *http.Client, http.Header) (appwire.Transport, error) {
		return respondingTransport(respond), nil
	}
	_, err := source.materializeLocalDaemonTurns(context.Background(), "local:thread", "thread", "full")
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("cycle error = %v, want non-leaking error", err)
	}
}

func fuzzScenarioLocalDaemonInternalHandshakeErrorFallbacks(t *testing.T) {
	internal := appwire.InternalError("semantic failure")
	if got := localDaemonInitializeError(internal); got == nil {
		t.Fatal("initialize error mapped nil")
	}
	if got := localDaemonSubscribeReadError(internal); got == nil {
		t.Fatal("subscribe error mapped nil")
	}
}
