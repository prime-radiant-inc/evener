package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
)

const verboseE2EChildEnv = "SERF_TEST_VERBOSE_E2E_CHILD"

// TestServeVerboseSurvivesAnUnreadStderr drives the REAL serve path -- real
// deps.bridge, real projection, real --verbose wiring -- against the stderr a
// daemon actually gets when it is launched with its output piped somewhere
// nobody reads.
//
// It exists because everything else pins a PART. TestVerboseTeeOutlivesTheBridgeDrain
// pins the tee pattern, TestServeInstallsANonBlockingVerboseObserver pins the
// observer through an injected writer. Neither covers serve.go's own use of
// them: reverting serve.go's teardown defer to close the tee WITHOUT waiting --
// the exact crash measured at 8/40 -- left ./cmd/serf green, and so did throwing
// the verboseOut seam away and going back to a synchronous encoder on os.Stderr.
// A pin that survives a wholesale revert of the thing it guards is pinning the
// scaffolding.
//
// It runs the daemon in a SUBPROCESS because the failure is a panic on the
// bridge's own goroutine. That cannot be recovered, only observed from outside,
// and observing it is the whole point: an unrecovered panic there takes the
// daemon down and skips every deferred shutdown step.
//
// Two failures, one child:
//
//   - A SYNCHRONOUS observer stalls once the unread stderr pipe fills, so the
//     child never reports progress on stdout. Caught by the first budget.
//   - A teardown that does not wait for the drain panics with "send on closed
//     channel". Caught by the exit status and the captured output.
func TestServeVerboseSurvivesAnUnreadStderr(t *testing.T) {
	if os.Getenv(verboseE2EChildEnv) == "1" {
		runVerboseE2EChild(t)
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run", "^TestServeVerboseSurvivesAnUnreadStderr$", "-test.v")
	cmd.Env = append(os.Environ(), verboseE2EChildEnv+"=1")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	// Deliberately a pipe we do not read yet. This is the condition the tee
	// exists for, and it cannot be reproduced against a file or a terminal.
	stderrRead, stderrWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	cmd.Stderr = stderrWrite

	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	stderrWrite.Close()

	progress := make(chan string, 1)
	scanErr := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if strings.Contains(scanner.Text(), verboseE2EProgress) {
				select {
				case progress <- scanner.Text():
				default:
				}
			}
		}
		scanErr <- scanner.Err()
	}()

	stalled := false
	select {
	case <-progress:
	case <-time.After(30 * time.Second):
		stalled = true
	}

	// Only now start draining stderr, so the pipe was genuinely unread while
	// the daemon was working. Draining lets the child's own teardown finish.
	drained := make(chan string, 1)
	go func() {
		out, _ := io.ReadAll(stderrRead)
		drained <- string(out)
	}()

	// Drain stdout to completion before calling Wait: Wait closes the pipe as
	// soon as it sees the child exit, and racing that close against the scan
	// goroutine's own Read produces "file already closed" instead of a clean
	// EOF. os/exec requires all reads from a StdoutPipe to finish before Wait
	// is called.
	var scanFailed error
	select {
	case scanFailed = <-scanErr:
	case <-time.After(60 * time.Second):
		_ = cmd.Process.Kill()
		<-scanErr
		scanFailed = errors.New("child never exited")
	}

	exitErr := cmd.Wait()
	stderrRead.Close()
	childStderr := <-drained

	if scanFailed != nil {
		t.Fatalf("scanning child stdout: %v\nchild stderr:\n%s", scanFailed, tailOf(childStderr))
	}

	if stalled {
		t.Fatalf("the daemon made no progress with its stderr unread: the authoritative "+
			"consumer is coupled to whatever reads the logs.\nchild stderr:\n%s", tailOf(childStderr))
	}
	if strings.Contains(childStderr, "send on closed channel") {
		t.Fatalf("the daemon PANICKED on shutdown: the verbose sink was torn down while the "+
			"bridge drain was still delivering.\nchild stderr:\n%s", tailOf(childStderr))
	}
	if exitErr != nil {
		t.Fatalf("child serve run failed: %v\nchild stderr:\n%s", exitErr, tailOf(childStderr))
	}
}

const verboseE2EProgress = "VERBOSE-E2E-EVENTS-EMITTED"

// verboseE2EEmitTarget is well past the session's 256-deep buffer and the tee's
// 1024, so reaching it means the daemon kept consuming with its stderr unread.
const verboseE2EEmitTarget = 4000

// runVerboseE2EChild is the daemon half. It keeps defaultServeDeps' real bridge
// and real server -- overriding either is what let the earlier tests miss this.
func runVerboseE2EChild(t *testing.T) {
	deps := defaultServeDeps()
	deps.ensureConfigDirs = func() error { return nil }
	deps.seedMarketplaces = func() error { return nil }
	deps.newClient = func(string, io.Writer) (*llm.Client, providercfg.Config, bool, func() error, error) {
		client := llm.NewClient()
		client.Register(&closedStreamAdapter{})
		return client, providercfg.Config{
			Default:   "openai",
			Instances: []providercfg.InstanceConfig{{Name: "openai", Type: "openai"}},
		}, true, func() error { return nil }, nil
	}

	var live *agent.Session
	newSession := func(client *llm.Client, profile *provider.Profile, env execenv.ExecutionEnvironment, cfg agent.SessionConfig) (*agent.Session, error) {
		// This detector measures the live bridge and verbose sink. Metadata
		// autosave has its own durability contract and would add synchronous
		// disk backpressure to the setter counter without changing event delivery.
		cfg.StateDir = ""
		sess, err := agent.NewSession(client, profile, env, cfg)
		if err == nil {
			live = sess
		}
		return sess, err
	}
	deps.newSession = newSession
	deps.newClearSession = newSession

	// The REAL bridge, with latency added to the observer call and nothing else.
	// Everything under test still runs: ConsumeEventsLossless, BridgeEvent, the
	// tee serve.go itself built, and serve.go's own teardown.
	//
	// The latency is what makes the ordering OBSERVABLE rather than a race. The
	// drain empties a closed session's buffered tail in microseconds, so an
	// unsynchronised teardown usually wins by luck; a loaded daemon does not
	// have that luck, and neither should the test. With ~0.3ms per event a full
	// 256-event tail takes ~75ms, which is an eternity next to the microseconds
	// between Session.Close and serve's teardown defer.
	realBridge := defaultServeDeps().bridge
	deps.bridge = func(s serveServer, sess *agent.Session, observer func(events.SessionEvent), onDrained func()) {
		realBridge(s, sess, func(ev events.SessionEvent) {
			time.Sleep(300 * time.Microsecond)
			observer(ev)
		}, onDrained)
	}

	serveCtx, cancelServe := context.WithCancel(context.Background())
	deps.notifyContext = func(context.Context, ...os.Signal) (context.Context, context.CancelFunc) {
		return serveCtx, cancelServe
	}

	deps.serveHTTP = func(*http.Server, net.Listener) error {
		// Emit CONCURRENTLY with shutdown rather than before it. A burst that
		// finishes first lets the drain catch up, and then there is no buffered
		// tail at teardown and nothing to crash into -- which is exactly how an
		// earlier version of this test passed against the bug. A turn still in
		// flight when the daemon is asked to stop is also the realistic case.
		stop := make(chan struct{})
		emitting := make(chan struct{})
		var emitted atomic.Int64
		go func() {
			defer close(emitting)
			for {
				select {
				case <-stop:
					return
				default:
				}
				live.SetReasoningEffort("high")
				live.SetReasoningEffort("low")
				emitted.Add(2)
			}
		}()

		// Progress is measured by EMITS COMPLETED, never by elapsed time. A
		// timer says the test goroutine is alive; it says nothing about the
		// daemon, and a time-based signal here passed happily against a
		// synchronous observer that had already wedged the session.
		//
		// This is the real liveness check: delivery is lossless, so a stalled
		// consumer fills the 256-deep buffer and then BLOCKS the emitter. The
		// counter stops climbing and the parent's budget expires. With the tee
		// in place the consumer never stalls, because it drops instead.
		deadline := time.Now().Add(20 * time.Second)
		for emitted.Load() < verboseE2EEmitTarget && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if emitted.Load() >= verboseE2EEmitTarget {
			fmt.Fprintln(os.Stdout, verboseE2EProgress)
		}
		// Cancel while the emitter is still in flight so shutdown still has a
		// buffered tail to drain. Then stop and join the producer here: once
		// Session.Close marks the session closing, SetReasoningEffort is a no-op,
		// so leaving this loop alive until test cleanup creates a hot spin that
		// competes with the drain whose lifetime the test is measuring.
		cancelServe()
		close(stop)
		<-emitting
		return http.ErrServerClosed
	}

	args := []string{
		"--model", "openai/gpt-test",
		"--addr", "127.0.0.1:0",
		"--dir", t.TempDir(),
		"--state-dir", t.TempDir(),
		"--run-dir", t.TempDir(),
		"--no-project-prompts",
		"--verbose",
	}
	if err := runServeWithDeps(args, deps); err != nil {
		t.Fatalf("serve: %v", err)
	}
}

func tailOf(s string) string {
	const limit = 1500
	if len(s) <= limit {
		return s
	}
	return "..." + s[len(s)-limit:]
}
