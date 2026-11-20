package agent

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/mcpconfig"
)

// probeIsolationTripwire bounds how long an unrelated probe may take while
// another one is stuck. The call under test short-circuits without I/O (no
// classified names), so a passing run finishes in microseconds; this only fires
// on a genuine queue.
const probeIsolationTripwire = 10 * time.Second

// TestProbeDoesNotQueueBehindAnotherWorkspacesStall pins that a probe is ONE
// caller's question, so a stall in one workspace cannot charge another.
//
// The old design coalesced probes through a slot keyed on an environment
// revision. Two rounds of fixes went into deciding who could occupy or replace
// that slot, and the last defect was exactly this: a probe stalled on the
// workspace the session LEFT charged every request in the workspace it moved TO,
// because the sampler joined whatever occupied the slot and only discarded the
// answer afterwards.
//
// There is no slot now. Each call runs its own probe, so a stuck read for one
// workspace holds nothing but its own goroutine, and a caller asking a different
// question gets its own answer.
func TestProbeDoesNotQueueBehindAnotherWorkspacesStall(t *testing.T) {
	// Not parallel: hermeticInfraEnv calls t.Setenv.
	hermeticInfraEnv(t)
	root := realTempDirForTest(t)
	runInfraGit(t, root, "init", "-q")
	writeInfraFile(t, root+"/.evener/mcp.json", `{"mcpServers":{"svc":{"command":"/bin/a"}}}`, 0o644)
	local := execenv.NewLocalExecutionEnvironment(root)
	_, _, evidence, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidenceForProjectEnv(local, nil, nil, nil, false, false)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	stalled := stalledGitEnv{ExecutionEnvironment: local, started: make(chan struct{})}

	// A probe against the stalled workspace is in flight and never finishes. Its
	// goroutine is deliberately left running: it holds no session state, only a
	// buffered channel nobody reads.
	go func() {
		(&Session{}).boundedEvidenceCurrent(context.Background(), cliMCPClassificationEvidence{layers: evidence}, stalled)
	}()
	select {
	case <-stalled.started:
	case <-time.After(probeIsolationTripwire): // TRIPWIRE: the fixture never reached the stall
		t.Fatal("the stalled probe never reached its git call, so this fixture did not exercise a stall")
	}

	// A different question must be answered on its own rather than waiting for the
	// stuck probe. No classified names is the cheapest such question: it
	// short-circuits with no I/O at all.
	other := &Session{}
	answered := make(chan bool, 1)
	go func() {
		answered <- other.probeCLIEvidenceFresh(context.Background(), nil)
	}()
	select {
	case got := <-answered:
		if got {
			t.Error("a session with no classified names answered \"exclude\"")
		}
	case <-time.After(probeIsolationTripwire): // TRIPWIRE: a probe queued behind another workspace's stall
		t.Fatal("a probe for one workspace waited on a probe stuck in another")
	}
}
