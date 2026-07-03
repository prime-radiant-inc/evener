package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/llm"
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
// subagent env inheritance (prepareSubagentRun) — against a goroutine
// concurrently swapping s.env via testSwapEnv. Before the currentEnv()
// conversion, every one of these was an unguarded `s.env` field read racing
// the swap under `-race`.
func TestSession_CurrentEnv_NoRaceWithConcurrentSwap(t *testing.T) {
	t.Parallel()
	sess := newSession(t)

	envA := execenv.NewLocalExecutionEnvironment(t.TempDir())
	envB := execenv.NewLocalExecutionEnvironment(t.TempDir())
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
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < swapIterations; i++ {
			if i%2 == 0 {
				sess.testSwapEnv(envA)
			} else {
				sess.testSwapEnv(envB)
			}
		}
	}()

	// Status/hook events: exercises hookInput's CWD read directly, in a tight
	// loop (no subprocess or filesystem I/O) to maximize overlap with the
	// swapper above.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < swapIterations; i++ {
			_ = sess.hookInput(plugin.HookNotification)
		}
	}()

	// Tool dispatch: exercises execTool's reg.ExecuteCall(ctx, currentEnv(), call).
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < toolIterations; i++ {
			sess.execTool(context.Background(), llm.ToolCallData{
				ID:        fmt.Sprintf("c%d", i),
				Name:      "shell",
				Arguments: json.RawMessage(`{"command":"true"}`),
			})
		}
	}()

	// Child creation: exercises prepareSubagentRun's subEnv resolution. Each
	// prepared run is discarded immediately (mirrors spawnAgent's own error
	// path) rather than launched, since we only care about the env read here.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < spawnIterations; i++ {
			prepared, err := sess.prepareSubagentRun(context.Background(), "noop", "", "", 1, "", "", nil, nil)
			if err != nil {
				continue
			}
			releasePreparedTreeSlot(prepared)
			prepared.sub.sess.Close()
		}
	}()

	wg.Wait()
}
