package main

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm"
)

// wedgedReadFileEnvironment parks any read_file of a path containing
// wedgeMarker inside the tool layer forever. ReadFile takes NO context — the
// interface has no cancellation channel at all — so a delegate parked in it is
// genuinely unkillable by ctx, which is the field shape of #369: a delegate
// wedged inside an uncancellable find_files walk, immune to the stop its parent
// requested.
//
// release is closed only by the test's own cleanup, never by the session's stop
// or kill path: that is what makes the wedge uncancellable rather than merely
// slow, while still letting the goroutine exit at the end of the test.
type wedgedReadFileEnvironment struct {
	execenv.ExecutionEnvironment
	entered   chan struct{}
	release   chan struct{}
	enterOnce sync.Once
	freeOnce  sync.Once
}

const wedgeMarker = "WEDGED-TOOL-CALL"

func newWedgedReadFileEnvironment() *wedgedReadFileEnvironment {
	return &wedgedReadFileEnvironment{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (e *wedgedReadFileEnvironment) ReadFile(path string, offsetLine, limitLines *int) (string, error) {
	if !strings.Contains(path, wedgeMarker) {
		return e.ExecutionEnvironment.ReadFile(path, offsetLine, limitLines)
	}
	e.enterOnce.Do(func() { close(e.entered) })
	<-e.release
	return "", nil
}

func (e *wedgedReadFileEnvironment) free() {
	e.freeOnce.Do(func() { close(e.release) })
}

func (e *wedgedReadFileEnvironment) isWedged() bool {
	select {
	case <-e.entered:
		return true
	default:
		return false
	}
}

// installWedgedReadFileEnvironment routes every session's read_file through
// wedge, leaving the rest of the execution environment real.
func installWedgedReadFileEnvironment(t *testing.T, wedge *wedgedReadFileEnvironment) {
	t.Helper()
	oldNewSession := runNewSession
	runNewSession = func(client *llm.Client, profile *provider.Profile, env execenv.ExecutionEnvironment, cfg agent.SessionConfig) (*agent.Session, error) {
		wedge.ExecutionEnvironment = env
		return oldNewSession(client, profile, wedge, cfg)
	}
	t.Cleanup(func() {
		runNewSession = oldNewSession
		wedge.free()
	})
}

// shrinkDrainStallTimeout shortens the drain's stall bound for the duration of
// the test. The bound is two stacked windows of real session-clock time in
// production (four minutes at the shipped default), which no CI test can wait
// out; the shapes under test are about ordering, not about the number.
func shrinkDrainStallTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := agent.DrainStallTimeout
	agent.DrainStallTimeout = d
	t.Cleanup(func() { agent.DrainStallTimeout = old })
}

// shrinkCloseBudget shortens the close cascade's shared budget. Close's joins
// are bounded by it, and a wedged tool call makes them run to expiry every time,
// so at the shipped 30s each of these scenarios would cost half a minute of
// pure waiting. What they assert is that the budget is HONOURED, not its size.
func shrinkCloseBudget(t *testing.T, d time.Duration) {
	t.Helper()
	old := agent.LaneClosePassBudget
	agent.LaneClosePassBudget = d
	t.Cleanup(func() { agent.LaneClosePassBudget = old })
}

var wedgeDelegateIDPattern = regexp.MustCompile(`dlg_[A-Za-z0-9_-]+`)

func scriptedReadFileCall(id, path string) llm.ToolCallData {
	args, _ := json.Marshal(map[string]any{"file_path": path})
	return llm.ToolCallData{ID: id, Name: "read_file", Arguments: args, Type: "function"}
}

func scriptedJobStopCall(id, target string) llm.ToolCallData {
	args, _ := json.Marshal(map[string]any{"target": target})
	return llm.ToolCallData{ID: id, Name: "job_stop", Arguments: args, Type: "function"}
}

// TestRunExitsWhenADelegateIsWedgedInAnUncancellableToolCall is the whole
// #317/#369 field shape driven through the real `evener run`: a one-shot run
// whose delegate is parked inside a tool call nothing can cancel, whose stop
// request therefore degrades to stop_pending forever, and whose root then
// issues its terminal communicate. The run must print the root's answer and its
// run path must complete — the drain giving up is worthless if Close() then
// blocks on the same wedged goroutine, which is exactly the contradiction the
// two reviews of #378 could not settle between them.
func TestRunExitsWhenADelegateIsWedgedInAnUncancellableToolCall(t *testing.T) {
	const (
		rootPrompt = "ROOT-WEDGE-PROMPT"
		childTask  = "CHILD-TASK-READ the wedged path"
		finalMsg   = "WEDGED-RUN-ANSWER"
	)
	// One stall window. The drain needs two of them (the predicate's, then the
	// watchdog's), so the whole give-up costs about a second of real time here.
	shrinkDrainStallTimeout(t, 500*time.Millisecond)
	shrinkCloseBudget(t, 3*time.Second)
	wedge := newWedgedReadFileEnvironment()
	installWedgedReadFileEnvironment(t, wedge)

	var stopResults []string
	step := func(req llm.Request) llm.Response {
		text := requestFullText(req)
		if !strings.Contains(text, rootPrompt) {
			// The delegate: park inside the uncancellable read and never return.
			return scriptedToolCalls(scriptedReadFileCall("cr_1", "/tmp/"+wedgeMarker+"/never-returns"))
		}
		switch {
		case !strings.Contains(text, childTask):
			return scriptedDelegateCall("del_1", childTask)
		case !strings.Contains(text, `"job_stop"`) && !strings.Contains(text, "stop_pending"):
			// The delegate is dispatched. Wait for it to actually reach the wedge
			// before asking for the stop, so the stop lands on a target that
			// cannot honour it — the sam-cell-seg ordering. A scripted step may
			// never block (the provider serializes every session's model call
			// under one mutex, so blocking here would deadlock the delegate's own
			// turn), so poll with a cheap real tool round instead.
			if !wedge.isWedged() {
				return scriptedToolCalls(scriptedForegroundShellCall("poll_wedge", "sleep 0.05"))
			}
			id := wedgeDelegateIDPattern.FindString(text)
			if id == "" {
				t.Errorf("delegate tool result carried no delegate id:\n%s", text)
				return scriptedCommunicate(finalMsg)
			}
			return scriptedToolCalls(scriptedJobStopCall("stop_1", id))
		default:
			// The stop came back unhonoured; give the run its terminal answer,
			// which is where the field runs then sat for 109 minutes.
			stopResults = append(stopResults, requestDeliveredText(req))
			return scriptedCommunicate(finalMsg)
		}
	}
	steps := make([]func(llm.Request) llm.Response, 0, 64)
	for range 64 {
		steps = append(steps, step)
	}
	installRunScriptedProvider(t, &scriptedProvider{name: "openai", steps: steps})

	var stdout, stderr bytes.Buffer
	// TRIPWIRE: with the stall bound shrunk to 500ms the drain's whole give-up
	// costs about a second, and Close() has its own budget on top. 90s only
	// fires on the hang this test exists to adjudicate.
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	started := time.Now()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, runConfig{
			prompt:  rootPrompt + ": delegate the read and answer " + finalMsg,
			model:   "openai/gpt-test",
			workDir: t.TempDir(),
			stdout:  &stdout,
			stderr:  &stderr,
		})
	}()

	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		t.Fatalf("run() never returned with a delegate wedged in an uncancellable tool call after %s: the process is held exactly as in #317/#369 and only an external kill would end it\nstderr: %s", time.Since(started), stderr.String())
	}
	if err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, stderr.String())
	}
	if len(stopResults) == 0 {
		t.Fatal("the root never saw a job_stop result: the scenario did not reach the stop-requested shape")
	}
	if !strings.Contains(stdout.String(), finalMsg) {
		t.Fatalf("stdout = %q, want the root's answer %q: giving up on the delegate is worthless if the answer is never printed", stdout.String(), finalMsg)
	}
}

// TestRunExitsWithALiveDelegateItNeverStopped is #317's own acceptance test, and
// the shape three of its four documented field cases actually had: a one-shot
// run, NO background shells, a delegate still live that the root never stopped
// and never awaited (video-processing xXLyvs9's 26-minute dead tail,
// count-dataset-tokens MCJx5Pg's advisory verification delegate,
// filter-js YUcy8do's busy review delegate). #378's predicate required a pending
// stop request, so none of them were reached.
//
// The design this pins: in a one-shot run the drain begins at the instant the
// root issued its terminal communicate, and from that instant the delegate
// subtree is treated as stop-requested. See delegateAbandonedByDrain.
func TestRunExitsWithALiveDelegateItNeverStopped(t *testing.T) {
	const (
		rootPrompt = "ROOT-NEVER-STOPS-PROMPT"
		childTask  = "CHILD-TASK-ADVISORY review"
		finalMsg   = "NEVER-STOPPED-RUN-ANSWER"
	)
	shrinkDrainStallTimeout(t, 500*time.Millisecond)
	shrinkCloseBudget(t, 3*time.Second)
	wedge := newWedgedReadFileEnvironment()
	installWedgedReadFileEnvironment(t, wedge)

	step := func(req llm.Request) llm.Response {
		text := requestFullText(req)
		if !strings.Contains(text, rootPrompt) {
			return scriptedToolCalls(scriptedReadFileCall("cr_1", "/tmp/"+wedgeMarker+"/never-returns"))
		}
		if !strings.Contains(text, childTask) {
			return scriptedDelegateCall("del_1", childTask)
		}
		// The root never stops and never awaits the delegate: it just answers,
		// which is precisely what the field roots did.
		return scriptedCommunicate(finalMsg)
	}
	steps := make([]func(llm.Request) llm.Response, 0, 16)
	for range 16 {
		steps = append(steps, step)
	}
	installRunScriptedProvider(t, &scriptedProvider{name: "openai", steps: steps})

	var stdout, stderr bytes.Buffer
	// TRIPWIRE: see the sibling test. 90s only fires on the #317 hang itself.
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	started := time.Now()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, runConfig{
			prompt:  rootPrompt + ": delegate an advisory review and answer " + finalMsg,
			model:   "openai/gpt-test",
			workDir: t.TempDir(),
			stdout:  &stdout,
			stderr:  &stderr,
		})
	}()

	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		t.Fatalf("run() never returned with a live delegate the root never stopped after %s: this is #317's primary shape and only an external kill would end it\nstderr: %s", time.Since(started), stderr.String())
	}
	if err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, stderr.String())
	}
	// Without this the test could pass for the wrong reason: a delegate that
	// never reached the wedge would simply have finished, and the run would
	// have concluded with nothing to abandon.
	if !wedge.isWedged() {
		t.Fatal("the delegate never entered the uncancellable read, so this run had no live delegate to conclude in spite of")
	}
	if !strings.Contains(stdout.String(), finalMsg) {
		t.Fatalf("stdout = %q, want the root's answer %q", stdout.String(), finalMsg)
	}
	// The abandonment is announced, not silent: an operator must be able to see
	// that a delegate was dropped rather than having finished.
	if !strings.Contains(stderr.String(), "no longer waiting on delegate") {
		t.Fatalf("stderr never announced the abandonment:\n%s", stderr.String())
	}
}
