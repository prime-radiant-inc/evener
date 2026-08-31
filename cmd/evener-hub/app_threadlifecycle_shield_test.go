package hub

import (
	"context"
	"errors"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/rendezvous"
)

// ctxHonoringSource wraps scriptedAppSource with a ReadThread that behaves
// like a real network call — it fails once the context it was given has been
// canceled (the base fake has no ctx hook for reads). Turn behavior is
// scripted per test through the base fake's startTurn closure.
type ctxHonoringSource struct {
	*scriptedAppSource
	readErr error
}

func (s *ctxHonoringSource) ReadThread(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	if err := ctx.Err(); err != nil {
		return appwire.ThreadReadResponse{}, err
	}
	if s.readErr != nil {
		return appwire.ThreadReadResponse{}, s.readErr
	}
	return s.scriptedAppSource.ReadThread(ctx, params)
}

// disconnectingSpawner simulates the client dropping its connection while the
// spawn is in flight, after the mutation has been admitted.
type disconnectingSpawner struct {
	recordingSpawner
	disconnect context.CancelFunc
}

func (d *disconnectingSpawner) Spawn(ctx context.Context, req hubcore.SpawnRequest) (rendezvous.Entry, error) {
	d.disconnect()
	return d.recordingSpawner.Spawn(ctx, req)
}

// startShieldedThread runs hubThreadStart with the fixed launch scaffolding
// the shield tests share; only ctx, the spawner, and the source vary.
func startShieldedThread(t *testing.T, ctx context.Context, spawner hubcore.Spawner, src appsource.Source) (appwire.ThreadStartResponse, error) {
	t.Helper()
	cfg := hubcore.WebConfig{LaunchConfigRoot: t.TempDir(), PluginRoot: t.TempDir(), Spawner: spawner}
	sources := appsource.NewRegistry()
	sources.Add(src)
	return hubThreadStart(ctx, cfg, sources, appwire.ThreadStartParams{
		CWD:   t.TempDir(),
		Model: "openai/gpt-5",
		Input: []appwire.InputItem{{Type: "input_text", Text: "hello"}},
	})
}

// TestThreadStart_DisconnectMidSequenceStillCompletesThread pins the admitted-
// request contract for thread/start: once the mutation is admitted, a client
// disconnect must not abandon the spawn → read → initial-turn sequence
// half-progressed. The thread completes fully formed (including the initial
// turn) so reconnect resync finds it via thread/list.
func TestThreadStart_DisconnectMidSequenceStillCompletesThread(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src := &ctxHonoringSource{scriptedAppSource: &scriptedAppSource{
		id:     "local",
		thread: appwire.Thread{ID: "T1"},
		startTurn: func(ctx context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
			if err := ctx.Err(); err != nil {
				return appwire.TurnStartResponse{}, err
			}
			return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn-1"}}, nil
		},
	}}

	resp, err := startShieldedThread(t, ctx, &disconnectingSpawner{disconnect: cancel}, src)
	if err != nil {
		t.Fatalf("thread/start abandoned after mid-sequence disconnect: %v", err)
	}
	if resp.Thread.ID != "T1" {
		t.Fatalf("thread read did not complete after disconnect: %+v", resp.Thread)
	}
	if resp.Turn.ID != "turn-1" {
		t.Fatalf("initial turn abandoned after disconnect: %+v", resp.Turn)
	}
}

// TestThreadStart_DetachedSequenceDeadlineBoundsAWedge pins the lifecycle
// bound the shield must keep: WithoutCancel may not leave the admitted
// sequence unbounded. A daemon that never answers the initial turn is cut off
// by the detached deadline instead of parking the worker forever.
func TestThreadStart_DetachedSequenceDeadlineBoundsAWedge(t *testing.T) {
	orig := threadStartDetachedTimeout
	threadStartDetachedTimeout = 100 * time.Millisecond
	t.Cleanup(func() { threadStartDetachedTimeout = orig })

	src := &ctxHonoringSource{scriptedAppSource: &scriptedAppSource{
		id:     "local",
		thread: appwire.Thread{ID: "T1"},
		startTurn: func(ctx context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
			// A wedged daemon: never answers, only honors cancellation.
			<-ctx.Done()
			return appwire.TurnStartResponse{}, ctx.Err()
		},
	}}

	done := make(chan error, 1)
	go func() {
		_, err := startShieldedThread(t, context.Background(), &recordingSpawner{}, src)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want context.DeadlineExceeded from the detached bound", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("thread/start hung on a wedged daemon; detached sequence has no effective deadline")
	}
}

// TestThreadStart_ShutdownAtEachAwaitPointConverges simulates hub shutdown
// biting at each await point of the detached sequence (spawn, read, initial
// turn) and pins that the handler always concludes with a state reconnect
// resync can converge on — never a hang, never a half-finished response that
// hides the spawned thread.
func TestThreadStart_ShutdownAtEachAwaitPointConverges(t *testing.T) {
	failingTurn := func(ctx context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		return appwire.TurnStartResponse{}, errors.New("connection closed: shutting down")
	}

	t.Run("spawn", func(t *testing.T) {
		// Shutdown during the rendezvous wait: the spawn errors out. The
		// handler reports it; a daemon that did launch stays discoverable
		// via the roster on restart (accepted retry residue).
		turnStarted := false
		src := &ctxHonoringSource{scriptedAppSource: &scriptedAppSource{
			id: "local", thread: appwire.Thread{ID: "T1"},
			startTurn: func(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
				turnStarted = true
				return appwire.TurnStartResponse{}, nil
			},
		}}
		spawner := &fakeRPCSpawner{spawn: func(context.Context, hubcore.SpawnRequest) (rendezvous.Entry, error) {
			return rendezvous.Entry{}, errors.New("rendezvous wait aborted: shutting down")
		}}
		if _, err := startShieldedThread(t, context.Background(), spawner, src); err == nil {
			t.Fatal("spawn failure swallowed; want loud error")
		}
		if turnStarted {
			t.Fatal("initial turn ran despite failed spawn")
		}
	})

	t.Run("read", func(t *testing.T) {
		// A failed read between spawn and turn (e.g. the daemon connection
		// died): the spawn already happened, so the handler must degrade to
		// the thread ref it knows — not abandon — and the client (or resync)
		// can then find the spawned session.
		src := &ctxHonoringSource{
			scriptedAppSource: &scriptedAppSource{
				id: "local", thread: appwire.Thread{ID: "T1"},
				startTurn: func(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
					return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn-1"}}, nil
				},
			},
			readErr: errors.New("connection closed: shutting down"),
		}
		resp, err := startShieldedThread(t, context.Background(), &recordingSpawner{}, src)
		if err != nil {
			t.Fatalf("read-leg failure must degrade, not abandon: %v", err)
		}
		if resp.Thread.ID == "" {
			t.Fatalf("degraded response lost the spawned thread: %+v", resp.Thread)
		}
	})

	t.Run("initialTurn", func(t *testing.T) {
		// Shutdown between read and initial turn: the turn fails loudly.
		// The thread itself is fully spawned and readable, so resync
		// converges via thread/list; only the initial input needs a retry.
		src := &ctxHonoringSource{scriptedAppSource: &scriptedAppSource{
			id: "local", thread: appwire.Thread{ID: "T1"}, startTurn: failingTurn,
		}}
		if _, err := startShieldedThread(t, context.Background(), &recordingSpawner{}, src); err == nil {
			t.Fatal("initial-turn failure swallowed; want loud error")
		}
	})
}
