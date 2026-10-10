package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/server"
)

type activityReadBarrierServer struct {
	*clearIdentityServer
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *activityReadBarrierServer) SetThreadActivityReadFunc(fn func(context.Context, appwire.SessionActivityReadParams) (appwire.SessionActivitySummary, error)) {
	s.Server.SetThreadActivityReadFunc(func(ctx context.Context, params appwire.SessionActivityReadParams) (appwire.SessionActivitySummary, error) {
		result, err := fn(ctx, params)
		s.once.Do(func() { close(s.entered); <-s.release })
		return result, err
	})
}

func TestSessionActivityServeAliasReadFencedAcrossClear(t *testing.T) {
	deps, state, args := newClearServeDeps(t)
	barrier := &activityReadBarrierServer{entered: make(chan struct{}), release: make(chan struct{})}
	deps.newServer = func(cfg server.ServerConfig) serveServer {
		barrier.clearIdentityServer = &clearIdentityServer{Server: server.NewServer(cfg), state: state}
		state.srv = barrier.clearIdentityServer
		return barrier
	}
	deps.serveHTTP = func(*http.Server, net.Listener) error {
		old := state.session(0)
		alias := clearThreadRead(t, state.srv).Thread.Evener.Ref
		if alias == "" {
			t.Fatal("no stable workspace alias")
		}
		pending := make(chan error, 1)
		go func() {
			_, err := dispatchDaemonRPC(state.srv, appwire.MethodEvenerThreadActivityRead, appwire.SessionActivityReadParams{Ref: alias})
			pending <- err
		}()
		<-barrier.entered
		if err := state.srv.clear(t.Context(), appwire.ThreadClearParams{Ref: alias, ClientMutationID: "activity-clear", ExpectedInstanceID: old.ID()}); err != nil {
			t.Fatal(err)
		}
		close(barrier.release)
		var wire appwire.WireError
		if err := <-pending; !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
			t.Errorf("old alias read after replacement = %v; want a recoverable stale incarnation", err)
		}
		encoded, _ := json.Marshal(wire.Data)
		var data appwire.ErrorData
		_ = json.Unmarshal(encoded, &data)
		if data.EvenerErrorInfo != appwire.ErrorSessionActivityCursorStale || data.RetryDisposition != appwire.RetryDispositionAutomatic {
			t.Errorf("incarnation error is not recoverable: %+v", wire)
		}
		current := state.session(1)
		for _, method := range []string{appwire.MethodEvenerThreadActivityRead, appwire.MethodEvenerThreadDelegatesList, appwire.MethodEvenerThreadJobsList, appwire.MethodEvenerThreadWatchesList} {
			result, err := dispatchDaemonRPC(state.srv, method, appwire.SessionActivityListParams{Ref: alias})
			if err != nil {
				t.Errorf("replacement %s: %v", method, err)
				continue
			}
			var got appwire.SessionActivityContext
			switch out := result.(type) {
			case appwire.SessionActivitySummary:
				got = out.Context
			case appwire.SessionDelegatesResponse:
				got = out.Context
			case appwire.SessionJobsResponse:
				got = out.Context
			case appwire.SessionWatchesResponse:
				got = out.Context
			}
			if got.SessionID != current.ID() || got.Ref != "local:"+current.ID() {
				t.Errorf("replacement %s context = %+v", method, got)
			}
		}
		state.srv.shutdown()
		return http.ErrServerClosed
	}
	if err := runServeWithDeps(args, deps); err != nil {
		t.Fatal(err)
	}
}

func TestSessionActivityServeAliasSubscribersFollowClear(t *testing.T) {
	deps, state, args := newClearServeDeps(t)
	deps.serveHTTP = func(*http.Server, net.Listener) error {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		host := httptest.NewServer(http.HandlerFunc(state.srv.AppServer().ServeWebSocket))
		defer host.Close()
		open := func() *appwire.Client {
			transport, err := appwire.DialWebSocket(ctx, "ws"+host.URL[len("http"):], host.Client())
			if err != nil {
				t.Fatal(err)
			}
			client := appwire.NewClient(transport)
			client.Start(ctx)
			if _, err := client.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
				t.Fatal(err)
			}
			return client
		}
		activity, transcript := open(), open()
		defer activity.Close()
		defer transcript.Close()
		old := state.session(0)
		alias := clearThreadRead(t, state.srv).Thread.Evener.Ref
		if _, err := activity.ThreadRead(ctx, appwire.ThreadReadParams{Ref: alias, Subscribe: true}); err != nil {
			t.Fatal(err)
		}
		if _, err := transcript.ThreadRead(ctx, appwire.ThreadReadParams{Ref: alias, Subscribe: true, IncludeTurns: true, ItemLimit: 40}); err != nil {
			t.Fatal(err)
		}
		if got := state.srv.AppSubscriberCount(old.ID()); got != 2 {
			t.Fatalf("mounted subscribers=%d", got)
		}
		if _, err := activity.ThreadClear(ctx, appwire.ThreadClearParams{Ref: alias, ClientMutationID: "activity-subs-clear", ExpectedInstanceID: old.ID()}); err != nil {
			t.Fatal(err)
		}
		current := state.session(1)
		for _, client := range []*appwire.Client{activity, transcript} {
			for {
				select {
				case note := <-client.Notifications():
					if note.Method != appwire.NotifyEvenerThreadResync {
						continue
					}
					var params appwire.ThreadResyncParams
					if err := json.Unmarshal(note.Params, &params); err != nil {
						t.Fatal(err)
					}
					if params.ThreadID != current.ID() || params.Ref != alias {
						t.Fatalf("alias resync=%+v", params)
					}
					goto resynced
				case <-ctx.Done():
					t.Fatal("mounted alias subscriber lost its clear resync")
				}
			}
		resynced:
			summary, err := client.ThreadActivityRead(ctx, appwire.SessionActivityReadParams{Ref: alias})
			if err != nil {
				t.Fatal(err)
			}
			if summary.Context.SessionID != current.ID() {
				t.Fatalf("alias owner=%+v", summary.Context)
			}
		}
		if got := state.srv.AppSubscriberCount(current.ID()); got != 2 {
			t.Fatalf("replacement subscribers=%d", got)
		}
		var cut uint64
		for _, record := range state.srv.AppNotificationsAfter(0, current.ID()) {
			cut = max(cut, record.Seq)
		}
		// A former descendant still names the old physical owner even though its
		// logical routing ref remains the same workspace alias.
		state.srv.RecordDescendantAppEvent(old.ID(), events.SessionEvent{Kind: events.EventSessionActivityChanged, SessionID: "retired-child", Data: events.SessionActivityChangedData{ThreadID: old.ID(), Ref: alias, SessionID: old.ID(), Resources: []appwire.SessionActivityResource{appwire.SessionActivityResourceJobs}}})
		for _, record := range state.srv.AppNotificationsAfter(cut, current.ID()) {
			if record.Notification.Method == appwire.NotifyEvenerThreadActivityChanged {
				t.Fatal("retired descendant published through reused alias")
			}
		}
		if _, err := activity.ThreadUnsubscribe(ctx, appwire.ThreadUnsubscribeParams{Ref: alias}); err != nil {
			t.Fatal(err)
		}
		if got := state.srv.AppSubscriberCount(current.ID()); got != 1 {
			t.Fatalf("activity disposal removed transcript: %d", got)
		}
		state.srv.RecordAppEvent(events.SessionEvent{Kind: events.EventSessionActivityChanged, SessionID: current.ID(), Data: events.SessionActivityChangedData{ThreadID: current.ID(), Ref: "local:" + current.ID(), SessionID: current.ID(), Resources: []appwire.SessionActivityResource{appwire.SessionActivityResourceJobs}}})
		for {
			select {
			case note := <-transcript.Notifications():
				if note.Method != appwire.NotifyEvenerThreadActivityChanged {
					continue
				}
				var params appwire.SessionActivityChangedParams
				if err := json.Unmarshal(note.Params, &params); err != nil {
					t.Fatal(err)
				}
				if params.SessionID != current.ID() || params.Ref != alias {
					t.Fatalf("replacement transcript notice=%+v", params)
				}
				state.srv.shutdown()
				return http.ErrServerClosed
			case <-ctx.Done():
				t.Fatal("transcript no longer receives replacement activity")
			}
		}
	}
	if err := runServeWithDeps(args, deps); err != nil {
		t.Fatal(err)
	}
}
