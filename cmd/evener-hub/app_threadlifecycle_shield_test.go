package hub

import (
	"context"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/rendezvous"
)

// ctxHonoringSource stands in for the daemon RPC surface behind thread/start:
// like a real network call, each method fails once the context it was given
// has been canceled.
type ctxHonoringSource struct {
	*scriptedAppSource
	turnStarted bool
}

func (s *ctxHonoringSource) ReadThread(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	if err := ctx.Err(); err != nil {
		return appwire.ThreadReadResponse{}, err
	}
	return s.scriptedAppSource.ReadThread(ctx, params)
}

func (s *ctxHonoringSource) StartTurn(ctx context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
	if err := ctx.Err(); err != nil {
		return appwire.TurnStartResponse{}, err
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
}
