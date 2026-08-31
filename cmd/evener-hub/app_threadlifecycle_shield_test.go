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

// ctxHonoringSource stands in for the daemon RPC surface behind thread/start:
// like a real network call, each method fails once the context it was given
// has been canceled. It also records whether the shielded sequence carried a
// deadline, and lets a test fail or block individual legs to simulate hub
// shutdown at each await point.
type ctxHonoringSource struct {
	*scriptedAppSource
	turnStarted  bool
	sawDeadline  bool
	readErr      error
	startTurnErr error
	blockTurn    bool
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

func (s *ctxHonoringSource) StartTurn(ctx context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
	if err := ctx.Err(); err != nil {
		return appwire.TurnStartResponse{}, err
	}
	_, s.sawDeadline = ctx.Deadline()
	if s.blockTurn {
		// A wedged daemon: never answers, only honors cancellation.
		<-ctx.Done()
		return appwire.TurnStartResponse{}, ctx.Err()
	}
	if s.startTurnErr != nil {
		return appwire.TurnStartResponse{}, s.startTurnErr
	}
	s.turnStarted = true
	return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn-1"}}, nil
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

// TestThreadStart_DisconnectMidSequenceStillCompletesThread pins the admitted-
// request contract for thread/start: once the mutation is admitted, a client
// disconnect must not abandon the spawn → read → initial-turn sequence
// half-progressed. The thread completes fully formed (including the initial
// turn) so reconnect resync finds it via thread/list.
func TestThreadStart_DisconnectMidSequenceStillCompletesThread(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	spawner := &disconnectingSpawner{disconnect: cancel}
	cfg := hubcore.WebConfig{LaunchConfigRoot: t.TempDir(), PluginRoot: t.TempDir(), Spawner: spawner}
	sources := appsource.NewRegistry()
	src := &ctxHonoringSource{scriptedAppSource: &scriptedAppSource{id: "local", thread: appwire.Thread{ID: "T1"}}}
	sources.Add(src)

	resp, err := hubThreadStart(ctx, cfg, sources, appwire.ThreadStartParams{
		CWD:   t.TempDir(),
		Model: "openai/gpt-5",
		Input: []appwire.InputItem{{Type: "input_text", Text: "hello"}},
	})
	if err != nil {
		t.Fatalf("thread/start abandoned after mid-sequence disconnect: %v", err)
	}
	if resp.Thread.ID != "T1" {
		t.Fatalf("thread read did not complete after disconnect: %+v", resp.Thread)
	}
	if !src.turnStarted || resp.Turn.ID != "turn-1" {
		t.Fatalf("initial turn abandoned after disconnect: started=%v turn=%+v", src.turnStarted, resp.Turn)
	}
	// The shield sheds only peer-lifetime cancellation, not lifecycle bounds:
	// the detached sequence must carry its own deadline (spec, thread/start
	// disposition), so a wedge cannot park the worker with no cancel path.
	if !src.sawDeadline {
		t.Fatal("shielded sequence carried no deadline; want an explicit bound")
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

	cfg := hubcore.WebConfig{LaunchConfigRoot: t.TempDir(), PluginRoot: t.TempDir(), Spawner: &recordingSpawner{}}
	sources := appsource.NewRegistry()
	src := &ctxHonoringSource{scriptedAppSource: &scriptedAppSource{id: "local", thread: appwire.Thread{ID: "T1"}}, blockTurn: true}
	sources.Add(src)

	done := make(chan error, 1)
	go func() {
		_, err := hubThreadStart(context.Background(), cfg, sources, appwire.ThreadStartParams{
			CWD:   t.TempDir(),
			Model: "openai/gpt-5",
			Input: []appwire.InputItem{{Type: "input_text", Text: "hello"}},
		})
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
	start := func(t *testing.T, spawner hubcore.Spawner, src appsource.Source) (appwire.ThreadStartResponse, error) {
		t.Helper()
		cfg := hubcore.WebConfig{LaunchConfigRoot: t.TempDir(), PluginRoot: t.TempDir(), Spawner: spawner}
		sources := appsource.NewRegistry()
		sources.Add(src)
		return hubThreadStart(context.Background(), cfg, sources, appwire.ThreadStartParams{
			CWD:   t.TempDir(),
			Model: "openai/gpt-5",
			Input: []appwire.InputItem{{Type: "input_text", Text: "hello"}},
		})
	}

	t.Run("spawn", func(t *testing.T) {
		// Shutdown during the rendezvous wait: the spawn errors out. The
		// handler reports it; a daemon that did launch stays discoverable
		// via the roster on restart (accepted retry residue).
		src := &ctxHonoringSource{scriptedAppSource: &scriptedAppSource{id: "local", thread: appwire.Thread{ID: "T1"}}}
		_, err := start(t, &failingSpawner{err: errors.New("rendezvous wait aborted: shutting down")}, src)
		if err == nil {
			t.Fatal("spawn failure swallowed; want loud error")
		}
		if src.turnStarted {
			t.Fatal("initial turn ran despite failed spawn")
		}
	})

	t.Run("read", func(t *testing.T) {
		// Shutdown between spawn and read: the read fails, but the spawn
		// already happened — the handler must still return the thread ref it
		// knows, so the client (or resync) can find the spawned session.
		src := &ctxHonoringSource{scriptedAppSource: &scriptedAppSource{id: "local", thread: appwire.Thread{ID: "T1"}}, readErr: errors.New("connection closed: shutting down")}
		resp, err := start(t, &recordingSpawner{}, src)
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
		src := &ctxHonoringSource{scriptedAppSource: &scriptedAppSource{id: "local", thread: appwire.Thread{ID: "T1"}}, startTurnErr: errors.New("connection closed: shutting down")}
		_, err := start(t, &recordingSpawner{}, src)
		if err == nil {
			t.Fatal("initial-turn failure swallowed; want loud error")
		}
	})
}

// failingSpawner fails every spawn, standing in for a rendezvous wait cut
// short by hub shutdown.
type failingSpawner struct {
	recordingSpawner
	err error
}

func (f *failingSpawner) Spawn(context.Context, hubcore.SpawnRequest) (rendezvous.Entry, error) {
	return rendezvous.Entry{}, f.err
}
