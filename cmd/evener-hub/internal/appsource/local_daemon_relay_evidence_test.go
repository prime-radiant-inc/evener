package appsource

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

// The tests here exercise the source-level legacy relay bookkeeping: the
// pending-relay table ReadThread's subscribe path keeps, the SubscribeThread
// handoff of those pending relays, and the identity guards that refuse to
// admit malformed daemon identities into a relay session.

var legacyRelayReadParams = appwire.ThreadReadParams{
	Ref: "local:thread-1", ThreadID: "thread-1", Subscribe: true,
}

// newLegacyRelaySource answers a full subscribe read through the scripted
// transport, stamping the daemon's thread ref with daemonRef (empty drives the
// request-identity fallback key).
func newLegacyRelaySource(t *testing.T, daemonRef string) *LocalDaemonSource {
	t.Helper()
	entry := rendezvous.Entry{
		Protocol: appwire.ProtocolVersion, Endpoint: "ws://daemon", SourceID: "local",
		ThreadID: "thread-1", SessionID: "thread-1",
	}
	transport := respondingTransport(func(method string) (any, error) {
		switch method {
		case appwire.MethodInitialize:
			return appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion}, nil
		case appwire.MethodThreadRead:
			return appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID:     "thread-1",
				Source: "local",
				Evener: appwire.EvenerThread{Ref: daemonRef},
			}}, nil
		default:
			return nil, fmt.Errorf("unexpected method %q", method)
		}
	})
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{{Entry: entry}}
	}, nil)
	source.dial = dialTransport(transport)
	return source
}

func (s *LocalDaemonSource) pendingLegacyRelay(t *testing.T, key string) legacyRelayRead {
	t.Helper()
	s.relayMu.Lock()
	defer s.relayMu.Unlock()
	pending, ok := s.legacyRelays[key]
	if !ok {
		t.Fatalf("no pending legacy relay under key %q", key)
	}
	return pending
}

func (s *LocalDaemonSource) relaySessionCount() int {
	s.relayMu.Lock()
	defer s.relayMu.Unlock()
	return len(s.relaySessions)
}

// awaitLegacyRelayDrain waits for the pending relay's context cancellation to
// finish its cleanup: the handoff resolves, the lease closes, and the idle
// session cancels its own context.
func awaitLegacyRelayDrain(t *testing.T, session *relaySession) {
	t.Helper()
	select {
	case <-session.ctx.Done():
	case <-t.Context().Done():
		t.Fatal("legacy relay cleanup did not drain before the test context ended")
	}
}

func TestLocalDaemonDirectThreadMutationsAreUnavailable(t *testing.T) {
	t.Parallel()
	source := NewLocalDaemonSourceWithEntries("local", nil, nil)
	ctx := t.Context()
	for name, invoke := range map[string]func() error{
		"start": func() error {
			_, err := source.StartThread(ctx, appwire.ThreadStartParams{})
			return err
		},
		"resume": func() error {
			_, err := source.ResumeThread(ctx, appwire.ThreadResumeParams{})
			return err
		},
		"fork": func() error {
			_, err := source.ForkThread(ctx, appwire.ThreadForkParams{})
			return err
		},
	} {
		err := invoke()
		var wire appwire.WireError
		if !errors.As(err, &wire) {
			t.Fatalf("%s: error %T=%v, want a wire error", name, err, err)
		}
		if wire.Code != appwire.CodeUnavailable {
			t.Fatalf("%s: code = %d, want CodeUnavailable", name, wire.Code)
		}
	}
}

func TestLocalDaemonListThreadsStopsWhenContextCancelsDuringListing(t *testing.T) {
	t.Parallel()
	entry := rendezvous.Entry{
		Protocol: appwire.ProtocolVersion, Endpoint: "ws://daemon", SourceID: "local",
		ThreadID: "t1", SessionID: "t1",
	}
	ctx, cancel := context.WithCancel(context.Background())
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		cancel()
		return []LocalDaemonEntry{{Entry: entry}, {Entry: entry}}
	}, nil)

	_, err := source.ListThreads(ctx, appwire.ThreadListParams{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ListThreads error = %v, want context.Canceled", err)
	}
}

func TestLocalDaemonListThreadsWithoutEntryListing(t *testing.T) {
	t.Parallel()
	source := NewLocalDaemonSourceWithEntries("local", nil, nil)

	out, err := source.ListThreads(t.Context(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ListThreads without a listing: %v", err)
	}
	if len(out.Data) != 0 {
		t.Fatalf("ListThreads without a listing = %d threads, want none", len(out.Data))
	}
}

func TestLocalDaemonMalformedIdentityBlocksRelayAdmission(t *testing.T) {
	t.Parallel()
	entry := rendezvous.Entry{
		Protocol: appwire.ProtocolVersion, Endpoint: "ws://daemon", SourceID: "local",
		ThreadID: "thread one",
	}
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{{Entry: entry, SessionID: "thread one"}}
	}, nil)

	// A roster entry whose identity cannot form a workspace ref announces to
	// nobody: the relay session ref is unparseable, so there is no session.
	source.AnnounceDaemonGone(entry, "")
	if got := source.relaySessionCount(); got != 0 {
		t.Fatalf("relay sessions after malformed announce = %d, want 0", got)
	}

	if _, _, err := source.ResolveRelaySessionWithAdmission(appwire.ThreadReadParams{ThreadID: "thread one"}); err == nil {
		t.Fatal("malformed identity resolved a relay session")
	} else {
		assertSessionUnavailable(t, err, "relay resolution with a malformed identity")
	}
	if _, err := source.ResolveSubscriptionAdmission(appwire.ThreadReadParams{ThreadID: "thread one"}); err == nil {
		t.Fatal("malformed identity resolved a subscription admission")
	}
	if _, err := source.ReadThread(t.Context(), appwire.ThreadReadParams{ThreadID: "thread one", Subscribe: true}); err == nil {
		t.Fatal("malformed identity admitted a subscribe read")
	} else {
		assertSessionUnavailable(t, err, "subscribe read with a malformed identity")
	}
	if got := source.relaySessionCount(); got != 0 {
		t.Fatalf("relay sessions after malformed subscribe = %d, want 0", got)
	}
}

func TestLocalDaemonAcquireRelaySessionRejectsForeignAndEmptyRefs(t *testing.T) {
	t.Parallel()
	source := NewLocalDaemonSourceWithEntries("local", nil, nil)
	for name, ref := range map[string]appwire.Ref{
		"foreign source": {SourceID: "remote", ThreadID: "thread-1"},
		"empty thread":   {SourceID: "local"},
	} {
		if _, err := source.AcquireRelaySession(ref); err == nil {
			t.Fatalf("%s: AcquireRelaySession accepted %v", name, ref)
		} else {
			assertSessionUnavailable(t, err, name)
		}
	}
}

func TestLocalDaemonReadThreadSubscribeErrorClosesLease(t *testing.T) {
	t.Parallel()
	entry := rendezvous.Entry{
		Protocol: appwire.ProtocolVersion, Endpoint: "ws://daemon", SourceID: "local",
		ThreadID: "thread-1", SessionID: "thread-1",
	}
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{{Entry: entry}}
	}, nil)
	source.dial = func(context.Context, string, *http.Client, http.Header) (appwire.Transport, error) {
		return nil, errors.New("connection reset by peer")
	}

	_, err := source.ReadThread(t.Context(), legacyRelayReadParams)
	assertSessionUnavailable(t, err, "subscribe read with a failing dial")
	if got := source.relaySessionCount(); got != 0 {
		t.Fatalf("relay sessions after failed subscribe read = %d, want the idle session closed", got)
	}
}

func TestLocalDaemonReadThreadSubscribeStoresLegacyRelay(t *testing.T) {
	t.Parallel()

	t.Run("stores the pending relay under the daemon ref", func(t *testing.T) {
		t.Parallel()
		source := newLegacyRelaySource(t, "local:thread-1")
		ctx, cancel := context.WithCancel(context.Background())
		response, err := source.ReadThread(ctx, legacyRelayReadParams)
		if err != nil {
			t.Fatalf("subscribe read: %v", err)
		}
		if response.Thread.Evener.Ref != "local:thread-1" {
			t.Fatalf("read response ref = %q, want the daemon's ref", response.Thread.Evener.Ref)
		}
		pending := source.pendingLegacyRelay(t, "local:thread-1")
		if pending.lease == nil || pending.handoff == nil || pending.stop == nil {
			t.Fatalf("pending relay = %+v, want lease, handoff, and stopper", pending)
		}

		session := relaySessionFor(t, source)
		cancel()
		awaitLegacyRelayDrain(t, session)

		source.relayMu.Lock()
		_, stillPending := source.legacyRelays["local:thread-1"]
		source.relayMu.Unlock()
		if stillPending {
			t.Fatal("cancelled subscribe read left a pending legacy relay")
		}
		session.mu.Lock()
		closed := session.closed
		session.mu.Unlock()
		if !closed {
			t.Fatal("cancelled pending relay left its session open")
		}
		if pending.handoff.Abort() {
			t.Fatal("cleaned-up handoff reported a win on abort")
		}
	})

	t.Run("falls back to the request identity without a daemon ref", func(t *testing.T) {
		t.Parallel()
		source := newLegacyRelaySource(t, "")
		ctx := t.Context()
		if _, err := source.ReadThread(ctx, legacyRelayReadParams); err != nil {
			t.Fatalf("subscribe read: %v", err)
		}
		source.pendingLegacyRelay(t, "local:thread-1")
	})

	t.Run("resubscribe resolves the previous pending relay", func(t *testing.T) {
		t.Parallel()
		source := newLegacyRelaySource(t, "local:thread-1")
		firstCtx := t.Context()
		if _, err := source.ReadThread(firstCtx, legacyRelayReadParams); err != nil {
			t.Fatalf("first subscribe read: %v", err)
		}
		firstPending := source.pendingLegacyRelay(t, "local:thread-1")
		// The hub resolves the first handoff before re-reading, freeing the
		// command gate for the second read.
		if !firstPending.handoff.Commit() {
			t.Fatal("first handoff did not commit")
		}

		secondCtx, cancelSecond := context.WithCancel(context.Background())
		second, err := source.ReadThread(secondCtx, legacyRelayReadParams)
		if err != nil {
			t.Fatalf("second subscribe read: %v", err)
		}
		if second.Thread.Evener.Ref != "local:thread-1" {
			t.Fatalf("second read ref = %q, want the daemon's ref", second.Thread.Evener.Ref)
		}
		// The previous pending relay was resolved and replaced by this read's.
		if firstPending.handoff.Abort() {
			t.Fatal("previous handoff was not resolved by the resubscribe")
		}
		pending := source.pendingLegacyRelay(t, "local:thread-1")
		if pending.handoff == nil || pending.lease == nil {
			t.Fatalf("replacement pending relay = %+v, want lease and handoff", pending)
		}

		session := relaySessionFor(t, source)
		cancelSecond()
		awaitLegacyRelayDrain(t, session)
		source.relayMu.Lock()
		_, stillPending := source.legacyRelays["local:thread-1"]
		source.relayMu.Unlock()
		if stillPending {
			t.Fatal("cancelled resubscribe left a pending legacy relay")
		}
	})
}

func TestLocalDaemonSubscribeThreadLegacyRelayArms(t *testing.T) {
	t.Parallel()

	t.Run("listen failure aborts the pending handoff and closes the lease", func(t *testing.T) {
		t.Parallel()
		source := newLegacyRelaySource(t, "local:thread-1")
		readCtx := t.Context()
		if _, err := source.ReadThread(readCtx, legacyRelayReadParams); err != nil {
			t.Fatalf("subscribe read: %v", err)
		}
		pending := source.pendingLegacyRelay(t, "local:thread-1")

		cancelledCtx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := source.SubscribeThread(cancelledCtx, legacyRelayReadParams); !errors.Is(err, context.Canceled) {
			t.Fatalf("SubscribeThread with a cancelled context = %v, want context.Canceled", err)
		}
		if pending.handoff.Commit() {
			t.Fatal("handoff remained resolvable after the listen failure aborted it")
		}
		if got := source.relaySessionCount(); got != 0 {
			t.Fatalf("relay sessions after listen failure = %d, want the idle session closed", got)
		}
		source.relayMu.Lock()
		_, stillPending := source.legacyRelays["local:thread-1"]
		source.relayMu.Unlock()
		if stillPending {
			t.Fatal("SubscribeThread left the pending relay it took")
		}
	})

	t.Run("subscription ends when its lease is released", func(t *testing.T) {
		t.Parallel()
		source := newLegacyRelaySource(t, "local:thread-1")
		readCtx := t.Context()
		if _, err := source.ReadThread(readCtx, legacyRelayReadParams); err != nil {
			t.Fatalf("subscribe read: %v", err)
		}

		subscribeCtx := t.Context()
		out, err := source.SubscribeThread(subscribeCtx, legacyRelayReadParams)
		if err != nil {
			t.Fatalf("SubscribeThread: %v", err)
		}

		session := relaySessionFor(t, source)
		session.mu.Lock()
		var leaseID uint64
		for id := range session.leases {
			leaseID = id
		}
		session.mu.Unlock()
		if leaseID == 0 {
			t.Fatal("subscription session holds no lease")
		}
		// Another actor closing the lease (as the hub does when it retires
		// the subscription's owner) ends the listener's delivery stream while
		// the subscription context is still alive.
		session.releaseLease(leaseID)

		if _, ok := <-out; ok {
			t.Fatal("subscription channel stayed open after the lease was released")
		}
		session.mu.Lock()
		closed := session.closed
		session.mu.Unlock()
		if !closed {
			t.Fatal("session stayed open after its last lease was released")
		}
	})
}

func TestLocalDaemonStopDelegateForwardsToRootDaemon(t *testing.T) {
	t.Parallel()
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	var recorded []appwire.DelegateStopParams
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerDelegateStop, func(_ context.Context, params appwire.DelegateStopParams) (appwire.DelegateStopResponse, error) {
		recorded = append(recorded, params)
		return appwire.DelegateStopResponse{Outcome: appwire.DelegateStopStopping}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()

	entry := rendezvous.Entry{
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws" + httpServer.URL[len("http"):],
		SourceID:  "local",
		ThreadID:  "thread-1",
		SessionID: "thread-1",
	}
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{{Entry: entry}}
	}, httpServer.Client())

	if _, err := source.StopDelegate(t.Context(), appwire.DelegateStopParams{Ref: "local:missing", DelegateID: "dlg_1"}); err == nil {
		t.Fatal("StopDelegate reached a daemon for an unknown thread")
	} else {
		assertSessionUnavailable(t, err, "StopDelegate with an unknown thread")
	}

	out, err := source.StopDelegate(t.Context(), appwire.DelegateStopParams{
		Ref: "local:thread-1", ThreadID: "thread-1", DelegateID: "dlg_1",
	})
	if err != nil {
		t.Fatalf("StopDelegate: %v", err)
	}
	if out.Outcome != appwire.DelegateStopStopping {
		t.Fatalf("StopDelegate outcome = %q, want the daemon's answer", out.Outcome)
	}
	if len(recorded) != 1 || recorded[0].DelegateID != "dlg_1" {
		t.Fatalf("forwarded delegate stops = %+v, want the delegate id verbatim", recorded)
	}
}

func TestCommandSelectionGatedOnCommandInputCapability(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		caps    appwire.ThreadCapabilities
		wantErr bool
	}{
		{name: "command input unsupported", caps: appwire.ThreadCapabilities{SkillInput: true, CommandInput: false}, wantErr: true},
		{name: "command input supported", caps: appwire.ThreadCapabilities{SkillInput: true, CommandInput: true}, wantErr: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv, rec := startSkillInputGateDaemon(t, tc.caps, nil)
			source := skillInputGateSource(t, srv)
			input := []appwire.InputItem{
				{Type: "text", Text: "REQUEST_713"},
				{Type: "command", Name: "plan"},
			}
			_, err := source.StartTurn(t.Context(), appwire.TurnStartParams{
				Ref: "local:th_1", ClientMutationID: "mutation", ExpectedInstanceID: "sess_1", Input: input,
			})
			if tc.wantErr {
				var wire appwire.WireError
				if !errors.As(err, &wire) {
					t.Fatalf("StartTurn error %T=%v, want a wire error", err, err)
				}
				if wire.Code != appwire.CodeInvalidParams {
					t.Fatalf("StartTurn code = %d, want CodeInvalidParams", wire.Code)
				}
				if rec.startCount() != 0 {
					t.Fatal("gated command selection reached turn/start")
				}
			} else if err != nil {
				t.Fatalf("StartTurn with command input support: %v", err)
			}
			if rec.readCount() != 1 {
				t.Fatalf("capability reads = %d, want the gate's single read", rec.readCount())
			}
		})
	}
}

func TestLocalDaemonMutationEntryErrorClassification(t *testing.T) {
	t.Parallel()
	plain := errors.New("entry lookup failed")
	if got := localDaemonMutationEntryError("mut-1", plain); !errors.Is(got, plain) {
		t.Fatalf("plain error = %v, want it unchanged", got)
	}
	wrongCode := appwire.InternalError("semantic failure")
	if got := localDaemonMutationEntryError("mut-1", wrongCode); got == nil || !errors.As(got, &appwire.WireError{}) {
		t.Fatalf("wrong-code wire error = %v, want it unchanged", got)
	}
	otherInfo := appwire.WireError{
		Code:    appwire.CodeUnavailable,
		Message: "host detached",
		Data:    appwire.ErrorData{EvenerErrorInfo: appwire.ErrorHostDetached},
	}
	if got := localDaemonMutationEntryError("mut-1", otherInfo); got == nil {
		t.Fatal("unavailable wire error with foreign info was replaced")
	}
	noData := appwire.WireError{Code: appwire.CodeUnavailable, Message: "session gone"}
	if got := localDaemonMutationEntryError("mut-1", noData); got == nil {
		t.Fatal("unavailable wire error without data was replaced")
	}

	admission := appwire.WireError{
		Code:    appwire.CodeUnavailable,
		Message: "thread not found",
		Data:    appwire.ErrorData{EvenerErrorInfo: appwire.ErrorSessionUnavailable},
	}
	got := localDaemonMutationEntryError("mut-1", admission)
	var wire appwire.WireError
	if !errors.As(got, &wire) {
		t.Fatalf("admission refusal = %T=%v, want a wire error", got, got)
	}
	data, ok := appwire.ErrorDataOf(wire.Data)
	if !ok || data.ClientMutationID != "mut-1" || data.MutationOutcome != appwire.MutationOutcomeNotAccepted {
		t.Fatalf("admission refusal data = %+v, want the mutation marked not accepted", data)
	}
}

func TestDaemonInitializeErrorUnwraps(t *testing.T) {
	t.Parallel()
	wrapped := errors.New("handshake failed")
	err := DaemonInitializeError{Err: wrapped}
	if !errors.Is(err, wrapped) {
		t.Fatal("DaemonInitializeError does not unwrap to its cause")
	}
}

func TestLocalDaemonSubscribeReadErrorClassification(t *testing.T) {
	t.Parallel()
	semantic := appwire.InternalError("semantic failure")
	preserved := localDaemonSubscribeReadError(semantic)
	if preserved == nil {
		t.Fatal("wire subscribe read error was dropped")
	}
	var wire appwire.WireError
	if !errors.As(preserved, &wire) || wire.Code != appwire.CodeInternalError {
		t.Fatalf("wire subscribe read error = %v, want the daemon's verdict preserved", preserved)
	}

	mapped := localDaemonSubscribeReadError(errors.New("connection reset by peer"))
	assertSessionUnavailable(t, mapped, "transport-shaped subscribe read failure")

	plain := errors.New("benign failure")
	if got := localDaemonSubscribeReadError(plain); !errors.Is(got, plain) {
		t.Fatalf("plain subscribe read error = %v, want it unchanged", got)
	}
}
