package server

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

func TestServerAppWireThreadClearInvokesConfiguredClear(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "old")
	called := false
	srv.SetClearFunc(func(context.Context, appwire.ThreadClearParams) error {
		called = true
		return nil
	})

	if _, err := srv.handleAppThreadClear(context.Background(), appwire.ThreadClearParams{
		Ref:                "local:old",
		ClientMutationID:   "clear-1",
		ExpectedInstanceID: "old",
	}); err != nil {
		t.Fatalf("handleAppThreadClear: %v", err)
	}
	if !called {
		t.Fatal("clear callback was not called")
	}
}

func TestServerAppWireThreadClearReplaysTheSameReplacement(t *testing.T) {
	stateDir := t.TempDir()
	srv := NewServer(ServerConfig{StateDir: stateDir})
	srv.SetAppIdentity("local", "old")
	calls := 0
	srv.SetClearFunc(func(_ context.Context, params appwire.ThreadClearParams) error {
		calls++
		prepared, err := PrepareAppIdentityForRef("local", "new", params.Ref, "")
		if err != nil {
			return err
		}
		srv.ReplaceAppIdentity(prepared, nil)
		return nil
	})

	params := appwire.ThreadClearParams{Ref: "local:old", ClientMutationID: "clear-1", ExpectedInstanceID: "old"}
	first, err := srv.handleAppThreadClear(context.Background(), params)
	if err != nil {
		t.Fatalf("first clear: %v", err)
	}
	second, err := srv.handleAppThreadClear(context.Background(), params)
	if err != nil {
		t.Fatalf("replayed clear: %v", err)
	}
	if calls != 1 {
		t.Fatalf("clear callback calls = %d, want 1", calls)
	}
	if first.Thread.ID != "new" || second.Thread.ID != "new" {
		t.Fatalf("clear thread ids = (%q, %q), want replacement new", first.Thread.ID, second.Thread.ID)
	}
	if first.Ref != params.Ref || second.Ref != params.Ref {
		t.Fatalf("clear refs = (%q, %q), want stable %q", first.Ref, second.Ref, params.Ref)
	}
	if first.Receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("first disposition = %q, want applied", first.Receipt.Disposition)
	}
	if second.Receipt.Disposition != appwire.MutationDispositionReplayed {
		t.Fatalf("replay disposition = %q, want replayed", second.Receipt.Disposition)
	}
	if second.Receipt.InstanceID != "new" {
		t.Fatalf("replay instance id = %q, want new", second.Receipt.InstanceID)
	}

	// A new server process can replay the durable receipt without invoking a
	// replacement callback again. Installing the current instance under the
	// stable ref models the daemon's normal startup sequence.
	restarted := NewServer(ServerConfig{StateDir: stateDir})
	prepared, err := PrepareAppIdentityForRef("local", "new", params.Ref, "")
	if err != nil {
		t.Fatalf("prepare restarted identity: %v", err)
	}
	restarted.ReplaceAppIdentity(prepared, nil)
	replayed, err := restarted.handleAppThreadClear(context.Background(), params)
	if err != nil {
		t.Fatalf("restart replay: %v", err)
	}
	if replayed.Thread.ID != "new" || replayed.Receipt.Disposition != appwire.MutationDispositionReplayed {
		t.Fatalf("restart replay = (%q, %q), want (new, replayed)", replayed.Thread.ID, replayed.Receipt.Disposition)
	}

	wrongWorkspace := NewServer(ServerConfig{StateDir: stateDir})
	wrongWorkspace.SetAppIdentity("local", "other")
	if _, err := wrongWorkspace.handleAppThreadClear(context.Background(), params); err == nil {
		t.Fatal("clear receipt replayed for a different stable workspace")
	}
}

func TestServerAppWireThreadClearRejectsConcurrentDifferentMutation(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "old")
	entered := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	srv.SetClearFunc(func(_ context.Context, _ appwire.ThreadClearParams) error {
		mu.Lock()
		calls++
		mu.Unlock()
		close(entered)
		<-release
		return nil
	})

	firstResult := make(chan error, 1)
	go func() {
		_, err := srv.handleAppThreadClear(context.Background(), appwire.ThreadClearParams{
			Ref: "local:old", ClientMutationID: "clear-1", ExpectedInstanceID: "old",
		})
		firstResult <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first clear did not enter callback")
	}
	_, err := srv.handleAppThreadClear(context.Background(), appwire.ThreadClearParams{
		Ref: "local:old", ClientMutationID: "clear-2", ExpectedInstanceID: "old",
	})
	if err == nil {
		t.Fatal("concurrent second clear succeeded")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("concurrent clear error = %T %v, want WireError", err, err)
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if !ok || data.ClientMutationID != "clear-2" || data.MutationOutcome != appwire.MutationOutcomeNotAccepted {
		t.Fatalf("concurrent clear error data = %#v, want named notAccepted mutation", wire.Data)
	}
	close(release)
	if err := <-firstResult; err != nil {
		t.Fatalf("first clear: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("clear callback calls = %d, want 1", calls)
	}
}

func TestServerAppWireThreadClearFencesTurnMutationWhileReplacing(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "old")
	entered := make(chan struct{})
	release := make(chan struct{})
	interruptCalls := 0
	srv.SetClearFunc(func(_ context.Context, _ appwire.ThreadClearParams) error {
		close(entered)
		<-release
		prepared, err := PrepareAppIdentityForRef("local", "new", "local:old", "")
		if err != nil {
			return err
		}
		srv.ReplaceAppIdentity(prepared, nil)
		return nil
	})
	srv.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{
		Interrupt: func(context.Context, appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
			interruptCalls++
			return appwire.TurnInterruptResponse{}, nil
		},
	})

	clearResult := make(chan error, 1)
	go func() {
		_, err := srv.handleAppThreadClear(context.Background(), appwire.ThreadClearParams{
			Ref: "local:old", ClientMutationID: "clear-1", ExpectedInstanceID: "old",
		})
		clearResult <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("clear did not enter callback")
	}

	_, err := srv.handleAppTurnInterrupt(context.Background(), appwire.TurnInterruptParams{
		Ref: "local:old", ClientMutationID: "interrupt-1", ExpectedInstanceID: "old",
	})
	if err == nil {
		t.Fatal("turn mutation succeeded while clear was replacing the instance")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("turn mutation error = %T %v, want WireError", err, err)
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if !ok || data.ClientMutationID != "interrupt-1" || data.MutationOutcome != appwire.MutationOutcomeNotAccepted {
		t.Fatalf("turn mutation error data = %#v, want named notAccepted mutation", wire.Data)
	}
	if interruptCalls != 0 {
		t.Fatalf("interrupt callback calls = %d, want 0", interruptCalls)
	}

	close(release)
	if err := <-clearResult; err != nil {
		t.Fatalf("clear: %v", err)
	}
}

func TestServerAppWireThreadClearRejectsStaleInstanceAndUnresolvedWork(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "old")
	srv.SetClearFunc(func(context.Context, appwire.ThreadClearParams) error { return nil })

	srv.SetProcessing(true)
	_, err := srv.handleAppThreadClear(context.Background(), appwire.ThreadClearParams{
		Ref: "local:old", ClientMutationID: "clear-busy", ExpectedInstanceID: "old",
	})
	if err == nil {
		t.Fatal("clear succeeded while a turn was unresolved")
	}
	srv.SetProcessing(false)

	_, err = srv.handleAppThreadClear(context.Background(), appwire.ThreadClearParams{
		Ref: "local:old", ClientMutationID: "clear-stale", ExpectedInstanceID: "not-old",
	})
	if err == nil {
		t.Fatal("clear succeeded with a stale instance")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("stale clear error = %T %v, want WireError", err, err)
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if !ok || data.MutationOutcome != appwire.MutationOutcomeNotAccepted {
		t.Fatalf("stale clear error data = %#v, want notAccepted", wire.Data)
	}
}

func TestServerRejectsOldInstanceTurnMutationsAfterClear(t *testing.T) {
	tests := []struct {
		name string
		call func(*Server) error
	}{
		{
			name: "start",
			call: func(srv *Server) error {
				_, err := srv.handleAppTurnStart(context.Background(), appwire.TurnStartParams{
					Ref:                "local:old",
					ClientMutationID:   "start-old",
					ExpectedInstanceID: "old",
					Input:              []appwire.InputItem{{Type: "text", Text: "late start"}},
				})
				return err
			},
		},
		{
			name: "steer",
			call: func(srv *Server) error {
				_, err := srv.handleAppTurnSteer(context.Background(), appwire.TurnSteerParams{
					Ref:                "local:old",
					ClientMutationID:   "steer-old",
					ExpectedInstanceID: "old",
					Input:              []appwire.InputItem{{Type: "text", Text: "late steer"}},
				})
				return err
			},
		},
		{
			name: "interrupt",
			call: func(srv *Server) error {
				_, err := srv.handleAppTurnInterrupt(context.Background(), appwire.TurnInterruptParams{
					Ref:                "local:old",
					ClientMutationID:   "interrupt-old",
					ExpectedInstanceID: "old",
				})
				return err
			},
		},
		{
			name: "queue",
			call: func(srv *Server) error {
				_, err := srv.handleAppTurnQueue(context.Background(), appwire.TurnQueueParams{
					Ref:                "local:old",
					ClientMutationID:   "queue-old",
					ExpectedInstanceID: "old",
					Input:              []appwire.InputItem{{Type: "text", Text: "late queue"}},
				})
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := NewServer(ServerConfig{})
			srv.SetAppIdentity("local", "old")
			prepared, err := PrepareAppIdentityForRef("local", "new", "local:old", "")
			if err != nil {
				t.Fatalf("prepare replacement: %v", err)
			}
			srv.ReplaceAppIdentity(prepared, nil)
			calls := 0
			srv.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{
				Start: func(appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
					calls++
					return appwire.TurnStartResponse{}, nil
				},
				Steer: func(appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
					calls++
					return appwire.TurnSteerResponse{}, nil
				},
				Interrupt: func(context.Context, appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
					calls++
					return appwire.TurnInterruptResponse{}, nil
				},
				Queue: func(appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
					calls++
					return appwire.TurnQueueResponse{}, nil
				},
			})

			err = tt.call(srv)
			if err == nil {
				t.Fatal("old-generation mutation succeeded")
			}
			var wire appwire.WireError
			if !errors.As(err, &wire) {
				t.Fatalf("old-generation mutation error = %T %v, want WireError", err, err)
			}
			data, ok := wire.Data.(appwire.ErrorData)
			if !ok || data.MutationOutcome != appwire.MutationOutcomeNotAccepted || data.RetryDisposition != appwire.RetryDispositionNone {
				t.Fatalf("old-generation mutation error data = %#v, want notAccepted", wire.Data)
			}
			if calls != 0 {
				t.Fatalf("old-generation mutation callback calls = %d, want 0", calls)
			}
		})
	}
}
