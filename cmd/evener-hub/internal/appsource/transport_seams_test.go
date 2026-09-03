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
			Data: []appwire.Turn{{ID: "turn-1", Items: items, ItemsView: appwire.TurnItemsViewFull}},
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
	partialResponse := appwire.ThreadReadResponse{
		Thread: appwire.Thread{ID: "thread", Evener: appwire.EvenerThread{Ref: "local:thread"}, Turns: []appwire.Turn{{
			ID: "turn-1", Items: items[1:], ItemsView: appwire.TurnItemsViewFragment, HasEarlierItems: true,
		}}},
		PageUnit:    appwire.TranscriptPageUnitItem,
		OlderCursor: "daemon-native-cursor",
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
	continued, err := continuitySource.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{
		Ref: "local:thread", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 40, Cursor: oldCursor,
	})
	if err != nil {
		t.Fatalf("continue with original bounded cursor: %v", err)
	}
	if len(continued.Candidates.Candidates) != 1 || continued.Candidates.Candidates[0].Item.ID != "item-00" {
		t.Fatalf("original bounded cursor result = %+v, want item-00", continued.Candidates.Candidates)
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

func TestLocalDaemonItemCandidatesSynthesizeLegacyV3MaterializedMetadata(t *testing.T) {
	turns := []appwire.Turn{
		{ID: "turn-0", Items: []appwire.ThreadItem{
			{Type: "agentMessage", ID: "item-0-0", Text: "oldest"},
			{Type: "agentMessage", ID: "item-0-1", Text: "older"},
		}},
		{ID: "turn-1", Items: []appwire.ThreadItem{
			{Type: "agentMessage", ID: "item-1-0", Text: "middle"},
		}},
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

	assertCandidates := func(label string, candidates []appitempaging.TranscriptItemCandidate, wantIDs []string, wantPositions []appwire.ThreadItemPosition) {
		t.Helper()
		if len(candidates) != len(wantIDs) || len(wantIDs) != len(wantPositions) {
			t.Fatalf("%s candidate count = %d, want %d", label, len(candidates), len(wantIDs))
		}
		for i, candidate := range candidates {
			if candidate.Item.ID != wantIDs[i] || candidate.Position != wantPositions[i] || candidate.Item.Position == nil || *candidate.Item.Position != wantPositions[i] {
				t.Fatalf("%s candidate %d = %+v, want id %q position %+v", label, i, candidate, wantIDs[i], wantPositions[i])
			}
			wantKey := appitempaging.TranscriptItemKey(candidate.TurnID, wantPositions[i])
			if candidate.Item.TranscriptKey != wantKey || candidate.Item.TurnID != candidate.TurnID {
				t.Fatalf("%s candidate %d identity = key %q item turn %q, want key %q turn %q", label, i, candidate.Item.TranscriptKey, candidate.Item.TurnID, wantKey, candidate.TurnID)
			}
		}
	}

	latest, err := source.ReadItemCandidates(context.Background(), appwire.ThreadReadParams{
		Ref: "local:legacy-thread", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 3,
	})
	if err != nil {
		t.Fatalf("legacy-v3 latest item page: %v", err)
	}
	assertCandidates("latest", latest.Candidates.Candidates,
		[]string{"item-1-0", "item-2-0", "item-2-1"},
		[]appwire.ThreadItemPosition{{Entry: 1, Item: 0}, {Entry: 2, Item: 0}, {Entry: 2, Item: 1}})
	if latest.Candidates.OlderCursor == "" {
		t.Fatal("legacy-v3 latest page has no hub-owned continuation")
	}
	older, err := source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{
		Ref: "local:legacy-thread", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 3, Cursor: latest.Candidates.OlderCursor,
	})
	if err != nil {
		t.Fatalf("legacy-v3 older item page: %v", err)
	}
	assertCandidates("older", older.Candidates.Candidates,
		[]string{"item-0-0", "item-0-1"},
		[]appwire.ThreadItemPosition{{Entry: 0, Item: 0}, {Entry: 0, Item: 1}})
	if !older.Exhausted || older.Candidates.OlderCursor != "" {
		t.Fatalf("legacy-v3 older page = %+v, want exhausted terminal page", older)
	}

	wantNativeCursors := []string{"", "legacy-older", "", "legacy-older"}
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

	if result, err := source.ItemCandidatesFromRead(context.Background(), appwire.ThreadReadParams{
		Ref: "local:metadata-thread", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 1,
	}, appwire.ThreadReadResponse{Thread: appwire.Thread{Turns: []appwire.Turn{{ID: "native-item-turn", Items: []appwire.ThreadItem{{
		Type: "agentMessage", ID: "native-unpositioned",
	}}}}}}); err == nil {
		t.Fatalf("native item-mode response without metadata returned %+v", result)
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
