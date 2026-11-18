package agent

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/testwait"
	"primeradiant.com/evener/llm"
)

func delegateActivityRows(t *testing.T, s *Session) []appwire.SessionDelegate {
	t.Helper()
	page, err := s.ListActivityDelegates(t.Context(), appwire.SessionActivityListParams{
		Ref:   encodeRef("", s.ID()),
		Scope: appwire.SessionActivityScopeSubtree,
	})
	if err != nil {
		t.Fatalf("read delegate activity: %v", err)
	}
	return page.Delegates
}

func requireDelegateActivityRow(t *testing.T, rows []appwire.SessionDelegate, delegateID string) appwire.SessionDelegate {
	t.Helper()
	for _, row := range rows {
		if row.DelegateID == delegateID {
			return row
		}
	}
	t.Fatalf("no activity row for delegate %s among %d rows", delegateID, len(rows))
	return appwire.SessionDelegate{}
}

// A delegate whose run is held open by its finalization drain because its own
// background shell is still running must say so on the row a client reads
// (#6693). Before the join, the row read exactly like a delegate mid-turn: the
// same running lifecycle, with LatestActivityAt frozen for the whole hold.
//
// The wait is not synthesized: the fixture drives a real delegate through a
// real background shell that is genuinely still running, and the assertion
// reads the same collection read the hub serves. Nothing completes the job.
func TestDelegateActivityRowReportsTheOwnedJobItsDrainWaitsOn(t *testing.T) {
	t.Parallel()
	fixture := newOwnedJobDrainFixture(t)

	row := requireDelegateActivityRow(t, delegateActivityRows(t, fixture.parent), fixture.result.DelegateID)
	if row.Terminal {
		t.Fatal("the held delegate's row reads terminal; the case under test is a live row that shows no reason")
	}
	if !row.DrainHeldByJobs {
		t.Fatal("row drainHeldByJobs is unset while the drain holds the run open; nothing on the row says why the delegate is still running")
	}
	if row.RunningJobCount != 1 {
		t.Fatalf("row runningJobCount = %d, want 1 while the drain holds the run open", row.RunningJobCount)
	}
	started, err := time.Parse(time.RFC3339Nano, row.OldestRunningJobStartedAt)
	if err != nil {
		t.Fatalf("row oldestRunningJobStartedAt = %q, want an RFC3339Nano start: %v", row.OldestRunningJobStartedAt, err)
	}
	if started.IsZero() {
		t.Fatal("row oldestRunningJobStartedAt is the zero time; a running job always states when it started")
	}
}

// The other direction: a delegate that is genuinely mid-turn with no job of
// its own reports no wait, so the field cannot be satisfied by always
// reporting something. The delegate is held inside its provider call, which is
// the state the old row could not distinguish from the drain hold.
func TestDelegateActivityRowWithNoRunningJobReportsNoWait(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	hold := make(chan struct{})
	var enteredOnce sync.Once
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(hold) }) }
	adapter := newFakeAdapter("openai", []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			enteredOnce.Do(func() { close(entered) })
			<-hold
			return agenttest.FinalResponse("thought with no job")
		},
	}...)
	client := llm.NewClient()
	client.Register(adapter)
	root, err := NewSession(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 1,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() {
		release()
		root.Close()
	})

	result := root.createDelegate(context.Background(), delegateArgs{Task: "think with no job"})
	if result.Err != nil {
		t.Fatalf("createDelegate: %v", result.Err)
	}
	select {
	case <-entered:
	case <-time.After(30 * time.Second): // TRIPWIRE: scripted provider steps answer in process; only a hang reaches it.
		t.Fatal("the delegate never reached its provider call")
	}

	row := requireDelegateActivityRow(t, delegateActivityRows(t, root), result.DelegateID)
	if row.Terminal {
		t.Fatal("the mid-turn delegate's row reads terminal; the negative needs a live row")
	}
	if row.RunningJobCount != 0 {
		t.Fatalf("row runningJobCount = %d on a delegate with no job, want the field absent", row.RunningJobCount)
	}
	if row.DrainHeldByJobs {
		t.Fatal("row drainHeldByJobs is set on a delegate with no job; the hold cannot be satisfied by always reporting one")
	}
	if row.OldestRunningJobStartedAt != "" {
		t.Fatalf("row oldestRunningJobStartedAt = %q on a delegate with no job, want it absent", row.OldestRunningJobStartedAt)
	}
}

// drainHoldFixture is a parent whose child starts a background shell and then
// parks inside its provider call, so a test can read the child's row while the
// shell is already running and the drain has not yet taken hold. It records the
// parent's own event stream: the stream a pushed delegate/updated frame reaches.
type drainHoldFixture struct {
	parent      *Session
	child       *subagent
	env         *heldJobEnvironment
	drainClock  *ownedJobDrainClock
	recorder    sessionEventRecorder
	delegateID  string
	runDone     <-chan struct{}
	releaseStep chan struct{}
	releaseOnce sync.Once
	stop        chan struct{}
}

func (f *drainHoldFixture) releaseProviderCall() {
	f.releaseOnce.Do(func() { close(f.releaseStep) })
}

func newDrainHoldFixture(t *testing.T) *drainHoldFixture {
	t.Helper()
	stepHeld := make(chan struct{})
	releaseStep := make(chan struct{})
	drainClock := newOwnedJobDrainClock()
	adapter := newFakeAdapter("openai", []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(llm.ToolCallData{
				ID:        "held-shell",
				Name:      "shell",
				Arguments: json.RawMessage(`{"command":"held shell","mode":"background"}`),
				Type:      "function",
			})
		},
		func(llm.Request) llm.Response {
			close(stepHeld)
			<-releaseStep
			return agenttest.FinalResponse("waiting for the held shell")
		},
		func(llm.Request) llm.Response {
			return agenttest.FinalResponse("held shell handled")
		},
	}...)
	client := llm.NewClient()
	client.Register(adapter)
	env := newHeldJobEnvironment(t.TempDir())
	parent, err := NewSession(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")), env, SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 1,
		clock:            drainClock,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	f := &drainHoldFixture{
		parent:      parent,
		env:         env,
		drainClock:  drainClock,
		releaseStep: releaseStep,
		stop:        make(chan struct{}),
	}
	stream := parent.Events()
	go func() {
		for {
			select {
			case event, ok := <-stream:
				if !ok {
					return
				}
				f.recorder.record(event)
			case <-f.stop:
				return
			}
		}
	}()
	// Cleanups run in reverse of registration, so the run order is: stop the
	// recorder, release the blocked provider step, release the held shell,
	// then close the session. The step must be released before Close or the
	// delegate's stop spends its join budget on a channel that ignores
	// cancellation, and the shell must be gone before the session's state
	// directory is removed.
	t.Cleanup(parent.Close)
	t.Cleanup(env.releaseJob)
	t.Cleanup(f.releaseProviderCall)
	t.Cleanup(func() { close(f.stop) })

	result := parent.createDelegate(context.Background(), delegateArgs{Task: "run a held background shell"})
	if result.Err != nil {
		t.Fatalf("createDelegate: %v", result.Err)
	}
	_, childID, err := decodeRef(result.TranscriptRef)
	if err != nil {
		t.Fatalf("decodeRef(%q): %v", result.TranscriptRef, err)
	}
	child := parent.subagents.get(childID)
	if child == nil {
		t.Fatalf("subagent %s not found", childID)
	}
	f.child = child
	f.delegateID = result.DelegateID
	child.mu.Lock()
	f.runDone = child.done
	child.mu.Unlock()

	select {
	case <-env.started:
	case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
		t.Fatal("the child shell did not start")
	}
	select {
	case <-stepHeld:
	case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
		t.Fatal("the child did not reach its provider call after starting the shell")
	}
	return f
}

// recordedHoldFrames returns the delegate/updated payloads the parent's own
// stream carried for delegateID, in order.
func recordedHoldFrames(recorder *sessionEventRecorder, delegateID string) []events.DelegateUpdatedData {
	var out []events.DelegateUpdatedData
	for _, event := range recorder.snapshot() {
		if event.Kind != events.EventDelegateUpdated {
			continue
		}
		data, ok := event.Data.(events.DelegateUpdatedData)
		if !ok || data.DelegateID != delegateID {
			continue
		}
		out = append(out, data)
	}
	return out
}

// The negative that pins what the count cannot say: a delegate that is
// genuinely still working, with a real background shell of its own running, is
// NOT held by its drain. Its row must say nothing — no hold, no count — so the
// wait cannot be satisfied by "always report something", and a reader is not
// told a working delegate is parked.
func TestDelegateActivityRowMidTurnWithAShellReportsNoHold(t *testing.T) {
	t.Parallel()
	fixture := newDrainHoldFixture(t)

	// The shell really is running, so the row's silence is a statement about
	// the hold and not about a missing job.
	liveOwnedShellJobID(t, fixture.child.sess.jobManager)
	row := requireDelegateActivityRow(t, delegateActivityRows(t, fixture.parent), fixture.delegateID)
	if row.Terminal {
		t.Fatal("the mid-turn delegate's row reads terminal; the negative needs a live row")
	}
	if row.DrainHeldByJobs {
		t.Fatal("row drainHeldByJobs is set on a delegate still mid-turn; a running shell is not a drain hold")
	}
	if row.RunningJobCount != 0 || row.OldestRunningJobStartedAt != "" {
		t.Fatalf("row states count %d / oldest %q on a delegate with no hold, want both absent",
			row.RunningJobCount, row.OldestRunningJobStartedAt)
	}
}

// A row already loaded before the drain takes hold must learn the wait without
// a repeat read, and learn that it ended (#6693). Entering the drain appends no
// durable delegate event and moves no durable field, so the frame published on
// the hold transition is the only carrier: this asserts the same payload a
// loaded client row applies, in order, held then cleared.
func TestDelegateActivityRowLearnsTheDrainHoldWithoutARepeatRead(t *testing.T) {
	t.Parallel()
	fixture := newDrainHoldFixture(t)

	// The loaded row, read while the delegate is still mid-turn.
	before := requireDelegateActivityRow(t, delegateActivityRows(t, fixture.parent), fixture.delegateID)
	if before.DrainHeldByJobs {
		t.Fatal("the row already reports a hold before the drain started; the test cannot show a transition")
	}

	// End the provider call. The child's turn now finishes with its own shell
	// still running, so its finalization drain takes the run and holds it.
	fixture.releaseProviderCall()
	testwait.Until(t, "a pushed frame to carry the drain hold", func() bool {
		for _, frame := range recordedHoldFrames(&fixture.recorder, fixture.delegateID) {
			if frame.DrainHeldByJobs {
				return true
			}
		}
		return false
	})

	var held events.DelegateUpdatedData
	for _, frame := range recordedHoldFrames(&fixture.recorder, fixture.delegateID) {
		if frame.DrainHeldByJobs {
			held = frame
			break
		}
	}
	if held.RunningJobCount != 1 {
		t.Fatalf("held frame runningJobCount = %d, want the one job the drain waits on", held.RunningJobCount)
	}
	if _, err := time.Parse(time.RFC3339Nano, held.OldestRunningJobStartedAt); err != nil {
		t.Fatalf("held frame oldestRunningJobStartedAt = %q, want an RFC3339Nano start: %v", held.OldestRunningJobStartedAt, err)
	}

	// The read agrees with the frame the client applied: one fact, two carriers.
	during := requireDelegateActivityRow(t, delegateActivityRows(t, fixture.parent), fixture.delegateID)
	if !during.DrainHeldByJobs || during.RunningJobCount != held.RunningJobCount ||
		during.OldestRunningJobStartedAt != held.OldestRunningJobStartedAt {
		t.Fatalf("read row (hold %v, count %d, oldest %q) disagrees with the pushed frame (hold %v, count %d, oldest %q)",
			during.DrainHeldByJobs, during.RunningJobCount, during.OldestRunningJobStartedAt,
			held.DrainHeldByJobs, held.RunningJobCount, held.OldestRunningJobStartedAt)
	}

	// Let the shell finish. The drain releases the run, and a later frame says
	// the hold is gone, so a loaded row does not keep claiming the wait.
	fixture.env.releaseJob()
	select {
	case <-fixture.runDone:
	case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
		t.Fatal("the delegate run did not finish after its shell completed")
	}
	testwait.Until(t, "a frame after the hold that no longer carries it", func() bool {
		seenHeld := false
		for _, frame := range recordedHoldFrames(&fixture.recorder, fixture.delegateID) {
			if frame.DrainHeldByJobs {
				seenHeld = true
				continue
			}
			if seenHeld {
				return true
			}
		}
		return false
	})
}

// The hold is a per-row fact whose samples can overtake each other on the way
// to a client, and the projection revision cannot order them: both edges arrive
// at the revision the row already carries. The source assigns a sequence where
// it samples the hold, and every carrier must report the same number for the
// same sample — otherwise a client has no order to obey and a delayed sample
// wins (#6693).
func TestDelegateDrainHoldCarriesItsSourceSequence(t *testing.T) {
	t.Parallel()
	fixture := newDrainHoldFixture(t)

	fixture.releaseProviderCall()
	testwait.Until(t, "a pushed frame to carry the drain hold", func() bool {
		for _, frame := range recordedHoldFrames(&fixture.recorder, fixture.delegateID) {
			if frame.DrainHeldByJobs {
				return true
			}
		}
		return false
	})

	var held events.DelegateUpdatedData
	for _, frame := range recordedHoldFrames(&fixture.recorder, fixture.delegateID) {
		if frame.DrainHeldByJobs {
			held = frame
			break
		}
	}
	if held.HoldSequence == 0 {
		t.Fatal("the held frame carries no hold sequence; a client would have no order to apply")
	}

	// The read samples the same unchanged hold, so it must reuse the sequence
	// rather than mint a new one: one fact, one order, whichever carrier.
	during := requireDelegateActivityRow(t, delegateActivityRows(t, fixture.parent), fixture.delegateID)
	if during.HoldSequence != held.HoldSequence {
		t.Fatalf("read row holdSequence = %d while the frame said %d; the two carriers disagree about the same sample",
			during.HoldSequence, held.HoldSequence)
	}

	// The release is a later sample of the same hold, so it must carry a later
	// sequence: a client that saw the wait can then tell the clearing is newer.
	fixture.env.releaseJob()
	select {
	case <-fixture.runDone:
	case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
		t.Fatal("the delegate run did not finish after its shell completed")
	}
	testwait.Until(t, "a frame after the hold that no longer carries it", func() bool {
		seenHeld := false
		for _, frame := range recordedHoldFrames(&fixture.recorder, fixture.delegateID) {
			if frame.DrainHeldByJobs {
				seenHeld = true
				continue
			}
			if seenHeld {
				return true
			}
		}
		return false
	})
	var cleared events.DelegateUpdatedData
	seenHeld := false
	for _, frame := range recordedHoldFrames(&fixture.recorder, fixture.delegateID) {
		if frame.DrainHeldByJobs {
			seenHeld = true
			continue
		}
		if seenHeld {
			cleared = frame
			break
		}
	}
	if cleared.HoldSequence <= held.HoldSequence {
		t.Fatalf("the clearing frame's holdSequence = %d, want later than the held sample's %d; a client would treat the release as stale",
			cleared.HoldSequence, held.HoldSequence)
	}
}

// twoShellDrainEnvironment releases its shells one at a time, so a delegate can
// be left parked in its drain on a second owned shell after the first finishes.
// The single-shell heldJobEnvironment cannot express that: releasing its only
// shell ends the drain, and the drain's own exit publishes the frame.
type twoShellDrainEnvironment struct {
	execenv.ExecutionEnvironment
	firstStarted  chan struct{}
	secondStarted chan struct{}
	releaseFirst  chan struct{}
	releaseSecond chan struct{}
	firstOnce     sync.Once
	secondOnce    sync.Once
	startMu       sync.Mutex
	started       int
}

func newTwoShellDrainEnvironment(dir string) *twoShellDrainEnvironment {
	return &twoShellDrainEnvironment{
		ExecutionEnvironment: execenv.NewLocalExecutionEnvironment(dir),
		firstStarted:         make(chan struct{}),
		secondStarted:        make(chan struct{}),
		releaseFirst:         make(chan struct{}),
		releaseSecond:        make(chan struct{}),
	}
}

func (e *twoShellDrainEnvironment) finishFirst() {
	e.firstOnce.Do(func() { close(e.releaseFirst) })
}

func (e *twoShellDrainEnvironment) finishSecond() {
	e.secondOnce.Do(func() { close(e.releaseSecond) })
}

func (e *twoShellDrainEnvironment) StreamCommand(_ context.Context, _, _ string, _ map[string]string, out io.Writer) (*execenv.StreamHandle, error) {
	e.startMu.Lock()
	e.started++
	which := e.started
	e.startMu.Unlock()
	started := e.firstStarted
	release := e.releaseFirst
	label := "first"
	if which > 1 {
		started = e.secondStarted
		release = e.releaseSecond
		label = "second"
	}
	close(started)
	return &execenv.StreamHandle{
		Wait: func() (int, error) {
			<-release
			_, _ = out.Write([]byte(label + " shell complete"))
			return 0, nil
		},
		Signal: func() {
			e.finishFirst()
			e.finishSecond()
		},
	}, nil
}

// One of two owned shells finishing changes the hold's phrasing without moving
// drainHeld or any durable delegate field, so nothing else publishes it. A row
// already loaded must still learn that the wait dropped from two jobs to one,
// and must stop claiming a shell wait once only descendant work is left (#6693).
//
// The fixture drives a real owned shell to completion and asserts the frame the
// parent's stream carried afterwards; the read is the independent check that
// the same fact is what the source now reports.
func TestOwnedShellLeavingTheRunningMapPublishesTheHold(t *testing.T) {
	t.Parallel()
	env := newTwoShellDrainEnvironment(t.TempDir())
	stepHeld := make(chan struct{})
	releaseStep := make(chan struct{})
	adapter := newFakeAdapter("openai", []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(llm.ToolCallData{
				ID:        "first-shell",
				Name:      "shell",
				Arguments: json.RawMessage(`{"command":"first shell","mode":"background"}`),
				Type:      "function",
			})
		},
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(llm.ToolCallData{
				ID:        "second-shell",
				Name:      "shell",
				Arguments: json.RawMessage(`{"command":"second shell","mode":"background"}`),
				Type:      "function",
			})
		},
		func(llm.Request) llm.Response {
			close(stepHeld)
			<-releaseStep
			return agenttest.FinalResponse("both shells started")
		},
		func(llm.Request) llm.Response { return agenttest.FinalResponse("owned shells handled") },
	}...)
	client := llm.NewClient()
	client.Register(adapter)
	parent, err := NewSession(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")), env, SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 1,
		clock:            newOwnedJobDrainClock(),
		testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	var recorder sessionEventRecorder
	stop := make(chan struct{})
	var releaseStepOnce sync.Once
	releaseProviderStep := func() { releaseStepOnce.Do(func() { close(releaseStep) }) }
	stream := parent.Events()
	go func() {
		for {
			select {
			case event, ok := <-stream:
				if !ok {
					return
				}
				recorder.record(event)
			case <-stop:
				return
			}
		}
	}()
	t.Cleanup(parent.Close)
	t.Cleanup(env.finishSecond)
	t.Cleanup(env.finishFirst)
	t.Cleanup(func() {
		releaseProviderStep()
		close(stop)
	})

	result := parent.createDelegate(context.Background(), delegateArgs{Task: "run two held background shells"})
	if result.Err != nil {
		t.Fatalf("createDelegate: %v", result.Err)
	}
	select {
	case <-env.secondStarted:
	case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
		t.Fatal("the delegate never started its second shell")
	}
	select {
	case <-stepHeld:
	case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
		t.Fatal("the delegate did not reach its provider call after starting both shells")
	}

	// End the turn: both owned shells are still running, so the drain takes the
	// run and holds it on two jobs.
	releaseProviderStep()
	testwait.Until(t, "a pushed frame carrying the two-job hold", func() bool {
		for _, frame := range recordedHoldFrames(&recorder, result.DelegateID) {
			if frame.DrainHeldByJobs && frame.RunningJobCount == 2 {
				return true
			}
		}
		return false
	})
	heldCount := len(recordedHoldFrames(&recorder, result.DelegateID))

	// The first shell finishes; the second keeps the drain parked, so the drain
	// publishes nothing and drainHeld does not move. Only the removal itself can
	// tell a loaded row that the wait shrank to one job.
	env.finishFirst()
	testwait.Until(t, "a frame reporting the reduced hold", func() bool {
		for _, frame := range recordedHoldFrames(&recorder, result.DelegateID)[heldCount:] {
			if frame.DrainHeldByJobs && frame.RunningJobCount == 1 {
				return true
			}
		}
		return false
	})

	// The read agrees with the frame, so the two carriers cannot drift.
	row := requireDelegateActivityRow(t, delegateActivityRows(t, parent), result.DelegateID)
	if !row.DrainHeldByJobs || row.RunningJobCount != 1 {
		t.Fatalf("read row after the first shell left = hold %v, count %d; want the one-job hold",
			row.DrainHeldByJobs, row.RunningJobCount)
	}

	// The second shell ends the drain entirely, so the row stops claiming it.
	// Only a clear published after this point counts: finishing the first shell
	// can also publish a temporary clear while the drain runs a notification
	// turn, and that clear says nothing about the second shell having left.
	beforeSecond := len(recordedHoldFrames(&recorder, result.DelegateID))
	env.finishSecond()
	// Synchronize on the read, which is the state the running map actually
	// reports, rather than on a frame: a clear published before finalization
	// removed the second shell would let the assertion run while the row still
	// legitimately holds one job.
	var done appwire.SessionDelegate
	testwait.Until(t, "a read row that no longer reports the shell wait", func() bool {
		done = requireDelegateActivityRow(t, delegateActivityRows(t, parent), result.DelegateID)
		return !done.DrainHeldByJobs && done.RunningJobCount == 0
	})

	// And the frame the second shell's completion published says the same, so
	// the two carriers cannot drift.
	testwait.Until(t, "a frame from the second shell's completion that no longer holds", func() bool {
		for _, frame := range recordedHoldFrames(&recorder, result.DelegateID)[beforeSecond:] {
			if !frame.DrainHeldByJobs && frame.RunningJobCount == 0 {
				return true
			}
		}
		return false
	})
}

// The drain parks the run on its owned jobs, but it also runs turns to handle a
// completion or its own announcement. During such a turn the delegate is
// working, so its row must stop claiming the shell wait and say what the turn
// is doing; the wait resumes as the drain parks again. Without this a fresh read
// in that window replaces the delegate's current work with the wait.
func TestDrainTurnSuspendsTheRowHoldAndRestoresIt(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	client := llm.NewClient()
	client.Register(newFakeAdapter("openai", []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("idle") },
	}...))
	session, err := NewSession(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{
		StateDir: stateDir,
		testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(session.Close)

	// The parking hold, as the finalization drain sets it.
	session.drainHeld.Store(true)
	seen := make([]bool, 0, 1)
	wrapped := session.drainTurnHidesHold(func(context.Context, string, []ImageAttachment, EntryKind) (string, error) {
		seen = append(seen, session.drainHeld.Load())
		return "turn done", nil
	})
	res, err := wrapped(t.Context(), "", nil, EntryNotification)
	if err != nil {
		t.Fatalf("the wrapped drain turn failed: %v", err)
	}
	if res != "turn done" {
		t.Fatalf("the wrapped turn returned %q, want the turn's own result", res)
	}
	if len(seen) != 1 || seen[0] {
		t.Fatalf("the hold during the turn = %v, want it suspended for the whole turn", seen)
	}
	if !session.drainHeld.Load() {
		t.Fatal("the hold was not restored after the turn; the row would stop reporting a wait that still holds")
	}

	// A session that was not parked keeps it clear: the wrapper restores what it
	// found rather than asserting the hold.
	session.drainHeld.Store(false)
	var duringClear bool
	if _, err := session.drainTurnHidesHold(func(context.Context, string, []ImageAttachment, EntryKind) (string, error) {
		duringClear = session.drainHeld.Load()
		return "", nil
	})(t.Context(), "", nil, EntryNotification); err != nil {
		t.Fatalf("the wrapped turn failed: %v", err)
	}
	if duringClear || session.drainHeld.Load() {
		t.Fatal("the wrapper set a hold on a session that was not parked")
	}
}
