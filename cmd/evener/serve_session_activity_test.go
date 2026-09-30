package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sync"
	"testing"

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
		var data struct {
			Info  string `json:"evenerErrorInfo"`
			Retry string `json:"retryDisposition"`
		}
		_ = json.Unmarshal(encoded, &data)
		if data.Info != "sessionActivityCursorStale" || data.Retry != "automatic" {
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
