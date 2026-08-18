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

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providercfg"
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
// the exact crash measured at 8/40 -- left ./cmd/evener green, and so did throwing
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
//     child's emit counter stops climbing, its stall detector gives up, and it
//     exits without ever reporting progress on stdout.
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

	// The child reports its own verdict: the progress line on success, or a
	// clean exit WITHOUT that line when its stall detector concluded the
	// consumer wedged. So the parent waits on the child, never on a tight
	// wall-clock budget of its own -- a starved CI runner can take minutes to
	// finish a healthy run, and only the child can tell slow from stuck. The
	// backstop covers a child wedged outside its own detector's reach.
	stalled := false
	scanDone := false
	var scanFailed error
	select {
	case <-progress:
	case err := <-scanErr:
		scanFailed = err
		scanDone = true
		stalled = true
	case <-time.After(verboseE2EParentBackstop):
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
	if !scanDone {
		select {
		case scanFailed = <-scanErr:
		case <-time.After(verboseE2EParentBackstop):
			_ = cmd.Process.Kill()
			<-scanErr
			scanFailed = errors.New("child never exited")
		}
	}

	exitErr := cmd.Wait()
	stderrRead.Close()
	childStderr := <-drained

	if scanFailed != nil {
		t.Fatalf("scanning child stdout: %v\nchild stderr:\n%s", scanFailed, tailOf(childStderr))
	}

	if stalled {
		t.Fatalf("the daemon did not reach the progress target with its stderr unread: the authoritative "+
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

// verboseE2EStallWindow is how long the child tolerates ZERO movement on the
// emit counter before concluding the consumer wedged. A wedged consumer fills
// the lossless channel and blocks the emitter permanently, so any window works
// for detection; this one is generous because a starved CI runner can leave the
// emitter unscheduled for whole seconds without anything being wrong.
const verboseE2EStallWindow = 30 * time.Second

// verboseE2EParentBackstop bounds each parent-side wait on a child that has
// stopped answering entirely: hung before serveHTTP, or hung in teardown (whose
// own drain budget is 30s). It is deliberately far past anything CPU starvation
// produces, because the child self-reports both success and stall.
const verboseE2EParentBackstop = 2 * time.Minute

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

	// The REAL bridge, with exactly one post-liveness observer call parked until
	// serve's teardown starts. Everything under test still runs:
	// ConsumeEventsLossless, BridgeEvent, the tee serve.go itself built, and
	// serve.go's own teardown. The channel handshake makes the buffered shutdown
	// tail deterministic without rate-limiting the liveness phase.
	gateNextObserver := make(chan struct{}, 1)
	teardownObserverParked := make(chan struct{})
	releaseTeardownObserver := make(chan struct{})
	teardownObserverDelivered := make(chan struct{})
	// Every REASONING_EFFORT_CHANGED delivery the bridge makes, so the gate
	// below can be armed only once the bridge has caught up with the producer.
	var effortDelivered atomic.Int64
	realBridge := defaultServeDeps().bridge
	deps.bridge = func(s serveServer, sess *agent.Session, observer func(events.SessionEvent), onDrained func()) {
		realBridge(s, sess, func(ev events.SessionEvent) {
			if ev.Kind == events.EventReasoningEffortChanged {
				effortDelivered.Add(1)
			}
			select {
			case <-gateNextObserver:
				close(teardownObserverParked)
				<-releaseTeardownObserver
				observer(ev)
				close(teardownObserverDelivered)
			default:
				observer(ev)
			}
		}, onDrained)
	}
	realDrainWaitExpiry := deps.drainWaitExpiry
	deps.drainWaitExpiry = func() <-chan time.Time {
		close(releaseTeardownObserver)
		return realDrainWaitExpiry()
	}

	serveCtx, cancelServe := context.WithCancel(context.Background())
	deps.notifyContext = func(context.Context, ...os.Signal) (context.Context, context.CancelFunc) {
		return serveCtx, cancelServe
	}

	var teardownObserverArmed bool
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
		// counter stops climbing FOREVER, the stall window expires, and the
		// child exits without reporting progress. With the tee in place the
		// consumer never stalls, because it drops instead.
		//
		// The give-up condition is a counter that stopped moving, never total
		// elapsed time: a starved CI runner climbs slowly but keeps climbing,
		// and a total-time deadline here failed healthy runs under load.
		lastSeen := int64(-1)
		lastClimb := time.Now()
		for {
			n := emitted.Load()
			if n >= verboseE2EEmitTarget {
				break
			}
			if n != lastSeen {
				lastSeen = n
				lastClimb = time.Now()
			} else if time.Since(lastClimb) >= verboseE2EStallWindow {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		reachedTarget := emitted.Load() >= verboseE2EEmitTarget
		if reachedTarget {
			// Stop the hot producer before parking the bridge. A producer blocked on
			// the full lossless event channel holds the session's event read lock,
			// which would prevent shutdown from closing the session. With the bridge
			// still unimpeded here, the producer always joins.
			close(stop)
			<-emitting

			// Let the bridge finish the producer's backlog before arming the
			// gate. The gate parks the bridge on its NEXT delivery, and a park
			// with the 256-deep event channel still full wedges the daemon for
			// good: shutdown's Session.Close emits SessionEnd BEFORE the
			// teardown defer whose drainWaitExpiry releases the gate, and that
			// emit blocks forever on a full channel whose only consumer is
			// parked. Nothing in production parks the bridge -- the wedge is
			// this harness's own -- so the gate must only ever close over an
			// empty buffer. The wait is an exact delivery count, not a
			// deadline: the bridge is still unimpeded here, so it always
			// catches up, however starved the machine.
			for effortDelivered.Load() < emitted.Load() {
				time.Sleep(time.Millisecond)
			}

			gateNextObserver <- struct{}{}
			teardownObserverArmed = true
			live.SetReasoningEffort("high")
			<-teardownObserverParked
			fmt.Fprintln(os.Stdout, verboseE2EProgress)
		}
		// Once the post-liveness observer is parked, cancel. Teardown's drain wait
		// releases that observer, proving the tee remains live until the
		// authoritative tail is delivered.
		cancelServe()
		if !reachedTarget {
			close(stop)
			<-emitting
		}
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
	if teardownObserverArmed {
		select {
		case <-teardownObserverDelivered:
		default:
			t.Fatal("serve returned before delivering the gated teardown event: the verbose sink " +
				"was torn down without waiting for the authoritative bridge drain")
		}
	}
}

func tailOf(s string) string {
	const limit = 1500
	if len(s) <= limit {
		return s
	}
	return "..." + s[len(s)-limit:]
}
