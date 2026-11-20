package agent

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/mcpconfig"
	"primeradiant.com/evener/internal/testwait"
)

// singleFlightTripwire bounds how long a request may take when the answer is
// meant to be immediate. A passing run answers in microseconds; this fires only
// on a request that spawned or joined another check.
const singleFlightTripwire = 10 * time.Second

// mountStallEnv is an environment whose git invocation ignores cancellation,
// which is what a stalled mount does: the process or the filesystem simply
// never answers, and nothing in the call observes the caller giving up. It
// embeds a real local environment, so every other call behaves.
//
// This is deliberately NOT stalledGitEnv: that fixture returns when its context
// ends, which is a check that CAN stop. The defect under test needs one that
// cannot, because a check that stops on its own leaves nothing outstanding.
type mountStallEnv struct {
	execenv.ExecutionEnvironment
	// started is closed once git has been asked, so the test can assert the
	// check reached the I/O rather than returning at an earlier comparison.
	started chan struct{}
	// firstReturned is closed when the first blocked call finally answers, which
	// is how the test knows the mount came back.
	firstReturned chan struct{}
	// release is closed by the test to make the mount answer; ExecCommand never
	// consults its context.
	release chan struct{}
	// calls counts how many checks actually reached the I/O, which is the
	// "outstanding probes" number the single flight bounds.
	calls atomic.Int32
}

func (e *mountStallEnv) ExecCommand(context.Context, string, int, string, map[string]string) (execenv.ExecResult, error) {
	e.calls.Add(1)
	select {
	case <-e.started:
	default:
		close(e.started)
	}
	<-e.release
	select {
	case <-e.firstReturned:
	default:
		close(e.firstReturned)
	}
	return execenv.ExecResult{}, errors.New("stalled mount finally answered")
}

// TestProbeKeepsOneOutstandingCheckPerWorkspace pins that a workspace whose
// mount stops answering gets ONE stuck check, not one per model request.
//
// The request path probes the CLI-MCP layers before every request. The check
// reads config layers and forks git, none of which observes cancellation, so a
// stalled mount leaves the abandoned goroutine parked in the read. Without a
// per-workspace guard every later request starts another goroutine into the
// same stall and waits its own full budget for an answer that is not coming —
// so one dead mount accumulates a parked check and a 12s wait per round.
//
// The guard is a key per workspace: at most one outstanding check for a given
// workspace, a later request for it answering "unknown reachability" (false)
// immediately, and a different workspace checking on its own.
func TestProbeKeepsOneOutstandingCheckPerWorkspace(t *testing.T) {
	// Not parallel: hermeticInfraEnv calls t.Setenv.
	hermeticInfraEnv(t)
	root := realTempDirForTest(t)
	runInfraGit(t, root, "init", "-q")
	writeInfraFile(t, filepath.Join(root, ".evener", "mcp.json"), `{"mcpServers":{"svc":{"command":"/bin/a"}}}`, 0o644)
	local := execenv.NewLocalExecutionEnvironment(root)
	// REAL evidence, captured from this environment: a hand-built value leaves
	// the global layer's recorded path empty while hermeticInfraEnv resolves a
	// nonempty one, so the check would answer at the global comparison and never
	// reach the git call the stall lives in.
	_, _, evidence, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidenceForProjectEnv(local, nil, nil, nil, false, false)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}

	stalled := &mountStallEnv{ExecutionEnvironment: local, started: make(chan struct{}), firstReturned: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(stalled.release) }) }
	// A failing run must still unblock the parked goroutine: it holds no session
	// state, but leaving it blocked past the test makes the failure harder to
	// read than the assertion that produced it.
	t.Cleanup(release)

	s := &Session{env: stalled}
	s.setCLIMCPToolNames(map[string]bool{"svc__echo": true})
	s.setCLIMCPServers(map[string]bool{"svc": true}, evidence)

	// One request gives up while the check is still stuck. The check itself
	// stays parked, exactly as a read on a stalled mount does.
	bounded, cancel := context.WithTimeout(context.Background(), freshnessRequestBound)
	if s.probeCLIEvidenceFresh(bounded, stalled) {
		t.Fatal("a check that could not finish reported current evidence")
	}
	cancel()
	select {
	case <-stalled.started:
	case <-time.After(singleFlightTripwire): // TRIPWIRE: the fixture never reached the stall
		t.Fatal("the check never reached the git call, so this fixture did not exercise a stall")
	}
	if got := stalled.calls.Load(); got != 1 {
		t.Fatalf("checks reaching the I/O = %d, want 1 before any repeated request", got)
	}

	// Repeated requests for the SAME workspace answer immediately, from the
	// unknown-reachability branch, without starting a second check. The contexts
	// carry no bound at all: a request that starts or joins another check blocks
	// here instead, which is the defect.
	for i := range 3 {
		answered := make(chan bool, 1)
		go func() { answered <- s.probeCLIEvidenceFresh(context.Background(), stalled) }()
		select {
		case got := <-answered:
			if got {
				t.Errorf("request %d reported current evidence for a workspace whose check never answered", i+1)
			}
		case <-time.After(singleFlightTripwire): // TRIPWIRE: a second check was started or joined
			t.Fatalf("request %d waited on a check already outstanding for this workspace", i+1)
		}
	}
	if got := stalled.calls.Load(); got != 1 {
		t.Errorf("outstanding checks for one workspace = %d, want at most 1", got)
	}

	// A DIFFERENT workspace is a different question, so the stall there cannot
	// suppress the check here.
	otherRoot := realTempDirForTest(t)
	runInfraGit(t, otherRoot, "init", "-q")
	writeInfraFile(t, filepath.Join(otherRoot, ".evener", "mcp.json"), `{"mcpServers":{"svc":{"command":"/bin/a"}}}`, 0o644)
	healthy := execenv.NewLocalExecutionEnvironment(otherRoot)
	_, _, healthyEvidence, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidenceForProjectEnv(healthy, nil, nil, nil, false, false)
	if err != nil {
		t.Fatalf("discover healthy: %v", err)
	}
	if !s.boundedEvidenceCurrent(context.Background(), cliMCPClassificationEvidence{layers: healthyEvidence}, healthy) {
		t.Error("a stalled check in one workspace suppressed a healthy workspace's check")
	}

	// Once the stuck check answers it stops being outstanding, so the workspace
	// can be probed again rather than staying permanently unknown.
	release()
	select {
	case <-stalled.firstReturned:
	case <-time.After(singleFlightTripwire): // TRIPWIRE: the released mount never answered
		t.Fatal("the released mount never answered, so the removal below could not be observed")
	}
	testwait.Until(t, "the finished check to leave the in-flight set", func() bool {
		s.probeCLIEvidenceFresh(context.Background(), stalled)
		return stalled.calls.Load() >= 2
	})
}
