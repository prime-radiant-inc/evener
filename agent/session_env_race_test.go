package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/llm"
)

// testSwapEnv is a test-only stand-in for the locked swap helper Task 5
// introduces (swapEnvAndRefreshLocked). It assigns s.env under s.mu so this
// race test can exercise a mutating s.env against concurrent currentEnv()
// reads before that production helper exists. Not for use outside tests.
func (s *Session) testSwapEnv(env execenv.ExecutionEnvironment) {
	s.mu.Lock()
	s.env = env
	s.mu.Unlock()
}

// TestSession_CurrentEnv_NoRaceWithConcurrentSwap hammers currentEnv()'s known
// read sites — tool dispatch (execTool -> reg.ExecuteCall, hookInput), and
// delegate env inheritance (the stable create path) — against a goroutine
// concurrently swapping s.env via testSwapEnv. Before the currentEnv()
// conversion, every one of these was an unguarded `s.env` field read racing
// the swap under `-race`.
func TestSession_CurrentEnv_NoRaceWithConcurrentSwap(t *testing.T) {
	if !raceDetectorEnabled {
		t.Skip("race-detector stress test; run with -race")
	}
	t.Parallel()
	// Both swapped environments share the session's working directory: stable
	// construction freezes the directory in describe and re-reads the
	// environment in prepareIsolation, so a swap to a different directory
	// between those reads is a legitimate refusal, not a race.
	dir := t.TempDir()
	sess := newSession(t, withDir(dir))

	envA := execenv.NewLocalExecutionEnvironment(dir)
	envB := execenv.NewLocalExecutionEnvironment(dir)
	if err := envA.Initialize(); err != nil {
		t.Fatalf("envA.Initialize: %v", err)
	}
	if err := envB.Initialize(); err != nil {
		t.Fatalf("envB.Initialize: %v", err)
	}

	const swapIterations = 2000
	const toolIterations = 30
	const spawnIterations = 30

	var wg sync.WaitGroup

	// Swapper: repeatedly reassigns s.env, the mutation this whole accessor
	// exists to make safe.
	wg.Go(func() {
		for i := range swapIterations {
			if i%2 == 0 {
				sess.testSwapEnv(envA)
			} else {
				sess.testSwapEnv(envB)
			}
		}
	})

	// Status/hook events: exercises hookInput's CWD read directly, in a tight
	// loop (no subprocess or filesystem I/O) to maximize overlap with the
	// swapper above.
	wg.Go(func() {
		for range swapIterations {
			_ = sess.hookInput(plugin.HookNotification)
		}
	})

	// Tool dispatch: exercises execTool's reg.ExecuteCall(ctx, currentEnv(), call).
	wg.Go(func() {
		for i := range toolIterations {
			sess.execTool(context.Background(), llm.ToolCallData{
				ID:        fmt.Sprintf("c%d", i),
				Name:      "shell",
				Arguments: json.RawMessage(`{"command":"true"}`),
			}, "")
		}
	})

	// Child creation: exercises the delegate create path's env resolution. Each
	// prepared run is discarded immediately rather than launched, since we only
	// care about the env read here.
	var prepareErr error
	wg.Go(func() {
		for range spawnIterations {
			_, started, _, prepared, err := buildCommittedDelegate(sess, delegateArgs{Task: "noop"}, committedDelegateLane{})
			if err != nil {
				prepareErr = err
				return
			}
			prepared.disposeUnadopted()
			endUnlaunchedDelegate(sess, started)
		}
	})

	wg.Wait()
	if prepareErr != nil {
		t.Fatalf("delegate create path: %v", prepareErr)
	}
}
