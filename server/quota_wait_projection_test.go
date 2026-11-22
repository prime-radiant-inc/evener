package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/testwait"
	"primeradiant.com/evener/llm"
)

// quotaWaitChildTask marks the delegate's prompt so the scripted provider can
// meet the usage limit on the child's requests alone, leaving the root's own
// requests healthy.
const quotaWaitChildTask = "QUOTA-WAIT-CHILD delegate task"

// quotaWaitResetIn is how far the usage limit's reset sits in the future when
// the child's call meets it: long enough that the wait stands across the
// mid-wait read below, short enough that the retry goes out and the read
// clears well inside the test's own deadline.
const quotaWaitResetIn = 3 * time.Second

// quotaWaitProvider answers the root's requests from a script. The delegate
// child's first request meets a usage limit naming a reset a few seconds out
// (the real clock governs this server-side fixture: the session's fake clock
// is agent-internal and cannot be injected from here), and every child
// request after the limit cleared answers with a finishing communicate.
//
// recoveryGate, when armed, parks the child's first request after the limit
// instead of answering it: the retry has gone out and the child's turn is
// still active, which is the window a read must prove carries no wait. The
// gate is armed BEFORE the input starts — the retry can reach the provider at
// any instant after the reset — and its wait is cancellable, so a failing
// assertion never parks cleanup. recoveryEntered is closed when the retry
// actually enters the gate, so the test checks the thread read only once the
// child is parked inside the retry.
type quotaWaitProvider struct {
	mu      sync.Mutex
	steps   []func(llm.Request) (llm.Response, error)
	limited atomic.Bool

	// resetIn is how far the usage limit's reset sits in the future for this
	// scenario's child; zero means quotaWaitResetIn. The Stop scenario names
	// an hour: no test outlives it, so the wait cannot end naturally and
	// only the Stop can end the turn the wait belongs to.
	resetIn time.Duration

	recoveryGate    chan struct{}
	recoveryEntered chan struct{}
}

func (*quotaWaitProvider) Name() string { return "openai" }

func (*quotaWaitProvider) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func (p *quotaWaitProvider) script(steps ...func(llm.Request) (llm.Response, error)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.steps = append(p.steps, steps...)
}

// armRecoveryGate installs the gate the child's first post-limit request will
// park on, and returns the channel the test releases it through. Arming
// before the input starts is what makes the gate race-free: the reset can
// fire any time after the limit, and a gate installed after the wait is
// observed can be missed entirely.
func (p *quotaWaitProvider) armRecoveryGate() (release chan struct{}, entered chan struct{}) {
	p.mu.Lock()
	defer p.mu.Unlock()
	release = make(chan struct{})
	entered = make(chan struct{})
	p.recoveryGate = release
	p.recoveryEntered = entered
	return release, entered
}

// recoveryHold parks the child's first request after the limit until the
// test releases it (or the call is cancelled), reporting entry through the
// recovery-entered channel. It reports whether the request was released and
// the caller should answer: false means no gate was armed for it (an unarmed
// provider answers at once) or the call was cancelled.
func (p *quotaWaitProvider) recoveryHold(ctx context.Context) (answer bool) {
	p.mu.Lock()
	hold, entered := p.recoveryGate, p.recoveryEntered
	p.mu.Unlock()
	if hold == nil {
		return true
	}
	p.mu.Lock()
	// One gate answers one retry: the next request (if any) must not park.
	p.recoveryGate = nil
	p.recoveryEntered = nil
	p.mu.Unlock()
	close(entered)
	select {
	case <-hold:
		return true
	case <-ctx.Done():
		return false
	}
}

// quotaUsageLimitError is the provider's usage-limit 429 as the openai adapter
// classifies it: a quota failure that names when the allowance returns.
func quotaUsageLimitError(resetAt time.Time) error {
	body := map[string]any{"error": map[string]any{
		"type":      "usage_limit_reached",
		"message":   "The usage limit has been reached",
		"resets_at": json.Number(strconv.FormatInt(resetAt.Unix(), 10)),
	}}
	err := llm.ErrorFromHTTPStatus("openai", 429, "responses.create failed", body, nil)
	if got, ok := llm.UsageLimitResetAt(err); llm.Kind(err) != llm.KindQuotaExceeded || !ok || !got.Equal(resetAt) {
		panic("fixture: want a quota failure resetting at " + resetAt.String())
	}
	return err
}

// nextWholeSecond rounds an instant up to the next whole Unix second: the
// provider's resets_at is Unix seconds, so the instant the wait parses back
// is whole, and the expectations compare against the value the session will
// actually hold.
func nextWholeSecond(at time.Time) time.Time {
	whole := time.Unix(at.Unix(), 0)
	if whole.Before(at) {
		whole = whole.Add(time.Second)
	}
	return whole
}

func (p *quotaWaitProvider) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if req.ResponseFormat != nil {
		// The session-namer/summarizer shape: answered inline, never from the
		// script, so background naming cannot consume the root's steps.
		return llm.Response{Message: llm.Assistant(`{"name":"Quota Wait"}`)}, nil
	}
	if len(req.Tools) == 0 {
		return llm.Response{Message: llm.Assistant("## Progress\nThe quota-wait session.")}, nil
	}
	if slices.Contains(userTexts(req), quotaWaitChildTask) {
		if p.limited.CompareAndSwap(true, false) {
			resetIn := p.resetIn
			if resetIn == 0 {
				resetIn = quotaWaitResetIn
			}
			return llm.Response{}, quotaUsageLimitError(nextWholeSecond(time.Now().Add(resetIn)))
		}
		if p.recoveryHold(ctx) {
			return parityCommunicate("quota-child-done", "the delegate finished its work", true), nil
		}
		return llm.Response{}, ctx.Err()
	}
	p.mu.Lock()
	if len(p.steps) == 0 {
		p.mu.Unlock()
		resp := parityResponse(parityCall("quota-root-ack", "communicate", map[string]any{}))
		resp.Provider = p.Name()
		resp.Model = req.Model
		return resp, nil
	}
	step := p.steps[0]
	p.steps = p.steps[1:]
	p.mu.Unlock()
	resp, err := step(req)
	if resp.Provider == "" {
		resp.Provider = p.Name()
	}
	if resp.Model == "" {
		resp.Model = req.Model
	}
	return resp, err
}

// newQuotaWaitProjection builds a served root session whose bridge is
// serve's: root events through BridgeEvent, child events through
// RecordDescendantAppEvent, so the delegate's thread is served by its own
// projection exactly as a pane reads it.
func newQuotaWaitProjection(t *testing.T) (*agent.Session, *Server, *quotaWaitProvider) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "state")
	workDir := filepath.Join(root, "work")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	adapter := &quotaWaitProvider{}
	adapter.limited.Store(true)
	client := llm.NewClient()
	client.Register(adapter)
	retry := &llm.RetryPolicy{MaxRetries: 0}
	sess, err := agent.NewSession(client, provider.NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workDir), agent.SessionConfig{
		StateDir:         stateDir,
		VisionModel:      "off",
		LLMRetryPolicy:   retry,
		LLMSleep:         noSleep,
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	sess.SetClientMutationStartWakeFunc(func() {})
	srv := NewServer(ServerConfig{StateDir: stateDir})
	sess.SetDescendantEventFunc(func(ev events.SessionEvent) { srv.RecordDescendantAppEvent(sess.ID(), ev) })
	wireTranscriptHistory(t, sess, srv)
	sess.ConsumeEventsLossless(func(ev events.SessionEvent) { BridgeEvent(srv, ev, nil) }, func() {})
	t.Cleanup(func() {
		sess.Close()
		srv.Close()
	})
	return sess, srv, adapter
}

// startRootInput runs the root's input in the background and returns its
// completion. The root's input is NOT the delegate's lifecycle: the child
// runs on its own goroutine, so its recovery and stop are judged from the
// child's own events, never from this input's outcome.
func startRootInput(t *testing.T, sess *agent.Session, input string) <-chan error {
	t.Helper()
	run := make(chan error, 1)
	go func() {
		_, err := sess.ProcessInput(context.Background(), input, nil)
		run <- err
	}()
	return run
}

// awaitRootEnd waits for the root's input to finish successfully — the input
// gets an explicit scripted end in every scenario, so an error here is a
// broken fixture, not a claim about the child.
func awaitRootEnd(t *testing.T, run <-chan error) {
	t.Helper()
	select {
	case err := <-run:
		if err != nil {
			t.Fatalf("the root's input: %v", err)
		}
	case <-time.After(30 * time.Second): // Tripwire, same ceiling as testwait's.
		t.Fatal("the root's input never finished")
	}
}

// readChildThread takes a fresh read of the child delegate's own thread, the
// path a pane opened or reloaded mid-wait uses.
func readChildThread(t *testing.T, srv *Server, childID string) appwire.ThreadReadResponse {
	t.Helper()
	response, err := srv.appThreadReadSnapshotChecked(appwire.ThreadReadParams{ThreadID: childID, IncludeTurns: true, ItemLimit: appwire.TranscriptItemPageLimit})
	if err != nil {
		t.Fatalf("thread/read of the waiting delegate: %v", err)
	}
	return response
}

// childWaitStartAnnouncement returns the child's wait-start announcement as
// the projection recorded it for its subscribers, nil when none is recorded.
func childWaitStartAnnouncement(t *testing.T, srv *Server, childID string) *appwire.ThreadModelRetryParams {
	t.Helper()
	for _, sequenced := range srv.AppNotificationsAfter(0, childID) {
		if sequenced.Notification.Method != appwire.NotifyEvenerThreadModelRetry {
			continue
		}
		params := notificationParams[appwire.ThreadModelRetryParams](t, sequenced)
		if params.ResumesAt != 0 {
			return &params
		}
	}
	return nil
}

// childWakeAnnouncements returns the child's wake announcements: the
// zero-delay retries that end a wait, identified by the wait's stamp and the
// absence of a resume instant.
func childWakeAnnouncements(t *testing.T, srv *Server, childID string) []appwire.ThreadModelRetryParams {
	t.Helper()
	var wakes []appwire.ThreadModelRetryParams
	for _, sequenced := range srv.AppNotificationsAfter(0, childID) {
		if sequenced.Notification.Method != appwire.NotifyEvenerThreadModelRetry {
			continue
		}
		params := notificationParams[appwire.ThreadModelRetryParams](t, sequenced)
		if params.ResumesAt == 0 && params.WaitStartedAt != 0 {
			wakes = append(wakes, params)
		}
	}
	return wakes
}

// awaitChildWaitStart blocks until the child's wait is fully published: the
// wait's agent state, and — the ordering the read depends on — the wait-start
// MODEL_RETRY committed to the child's projection. The state publishes first
// and the event lands after it, so waiting on the state alone reads a
// projection that can still be missing the wait.
func awaitChildWaitStart(t *testing.T, sess *agent.Session, srv *Server) string {
	t.Helper()
	var childID string
	testwait.Until(t, "the delegate waits out the usage limit", func() bool {
		srv.mu.RLock()
		for id := range srv.appDescendants {
			childID = id
		}
		srv.mu.RUnlock()
		if childID == "" {
			return false
		}
		return sess.DescendantQuotaWaits([]string{childID})[childID] != nil
	})
	testwait.Until(t, "the wait's start announcement is committed to the child's projection", func() bool {
		return childWaitStartAnnouncement(t, srv, childID) != nil
	})
	return childID
}

// A delegate waiting out a usage limit must be readable, not only
// live-broadcast: thread/read serves the child's own stored projection, so
// the wait's start announcement (MODEL_RETRY) must materialize the same facts
// the root session's failure facet samples — the resume instant and the
// wait's stamp — and the wake must clear them. The announcement itself is
// what carries the chip's elapsed sample to a pane over the subscription
// replay, stamped with the same identity. A pane opened or reloaded mid-wait
// shows nothing without this: the sidebar row says the delegate waits while
// its own thread says nothing at all.
func TestDelegateReadRestoresTheQuotaWait(t *testing.T) {
	t.Parallel()
	sess, srv, adapter := newQuotaWaitProjection(t)
	// The recovery gate is armed BEFORE the input starts: the child's retry
	// can reach the provider at any instant after the reset, and a gate
	// installed after the wait is observed can be missed entirely — the retry
	// would settle and the turn end before the read below could prove the
	// wake's clear.
	release, entered := adapter.armRecoveryGate()
	// The root's first request creates the delegate, and after the delegate
	// finishes it answers once more so the root's input ends on an explicit
	// success. The child's first request meets the limit, so the child waits
	// inside its generation turn.
	adapter.script(func(llm.Request) (llm.Response, error) {
		return parityResponse(parityCall("quota-delegate", "delegate", map[string]any{"prompt": quotaWaitChildTask})), nil
	}, func(llm.Request) (llm.Response, error) {
		return parityCommunicate("quota-root-done", "the delegated work is done", true), nil
	})
	run := startRootInput(t, sess, "delegate the quota-wait work")

	childID := awaitChildWaitStart(t, sess, srv)

	// Read the child's own thread fresh, mid-wait. The stored projection must
	// carry the wait the row already knows about.
	read := readChildThread(t, srv, childID)
	failure := read.Thread.Evener.Failure
	if failure == nil {
		t.Fatal("a fresh read of the waiting delegate carries no failure summary")
	}
	if failure.ResumesAt == 0 {
		t.Errorf("read resumesAt = %d, want the reset instant the wait named", failure.ResumesAt)
	}
	if failure.WaitStartedAt == 0 {
		t.Errorf("read waitStartedAt = %d, want the wait's stamp (the identity its announcements carry)", failure.WaitStartedAt)
	}
	// The wait-start announcement the pane's subscription replays carries the
	// same stamp and resume instant, and the chip's elapsed sample.
	announcement := childWaitStartAnnouncement(t, srv, childID)
	if announcement == nil {
		t.Fatal("the child's wait-start announcement was never recorded for its subscribers")
	}
	if announcement.WaitStartedAt != failure.WaitStartedAt {
		t.Errorf("announcement stamp = %d, want the read summary's %d", announcement.WaitStartedAt, failure.WaitStartedAt)
	}
	if announcement.ResumesAt != failure.ResumesAt {
		t.Errorf("announcement resumesAt = %d, want the read summary's %d", announcement.ResumesAt, failure.ResumesAt)
	}

	// The wait ends when the retry goes out. The daemon announces the retry
	// ahead of the dispatch, so once the retry parks inside the gate the wake
	// is already recorded: the reads below land post-reset and post-wake,
	// mid-retry, with the child's turn still active — the wake, not a turn
	// boundary, is what must have cleared the stored wait, and the wake's
	// announcement says so before the read is checked. (A read between the
	// reset and the wake — a drive queues for its slot past the reset before
	// its wake goes out — is covered below by
	// TestDelegateReadServesTheStoredWaitPastItsReset.)
	select {
	case <-entered:
	case <-time.After(30 * time.Second): // Tripwire, same ceiling as testwait's.
		t.Fatal("the delegate's retry never reached the recovery gate")
	}
	wakes := childWakeAnnouncements(t, srv, childID)
	if len(wakes) != 1 || wakes[0].WaitStartedAt != failure.WaitStartedAt || wakes[0].ResumesAt != 0 || wakes[0].DelayMS != 0 {
		t.Fatalf("the child's wake announcements = %+v, want exactly one for the wait's stamp %d with no resume instant and no delay", wakes, failure.WaitStartedAt)
	}
	testwait.Until(t, "a fresh read mid-retry carries no wait", func() bool {
		return readChildThread(t, srv, childID).Thread.Evener.Failure == nil
	})
	if status := readChildThread(t, srv, childID).Thread.Status.Type; status != appwire.ThreadStatusActive {
		t.Fatalf("the child's status mid-retry = %q, want active: the clear was proven in the retry's window", status)
	}
	close(release)
	awaitRootEnd(t, run)
}

// A Stop while the delegate waits ends the turn as an ordinary cancellation:
// no wake announces a retry, so the stored wait must not outlive the turn it
// belonged to. The boundary status that ends the child's working span clears
// it, exactly as the root's failure facet is re-sampled at every turn
// boundary — otherwise a delegate stopped mid-wait reads as waiting forever.
// The reset this scenario names sits an hour out, past anything the test
// outlives: no natural retry can end the wait, so the Stop is the only thing
// that can end the turn the wait belongs to — the Stop's outcome and the
// absence of any wake announcement say the clear can only be the boundary
// one.
func TestDelegateReadClearsTheQuotaWaitWhenTheTurnEnds(t *testing.T) {
	t.Parallel()
	sess, srv, adapter := newQuotaWaitProjection(t)
	// The reset is an hour out: the timer cannot fire inside the test, so the
	// wait stands until the Stop ends its turn. The root's first request
	// creates the delegate; the child's first request meets the limit and the
	// child waits. After the Stop returns from the delegate tool, the root's
	// input answers once more and ends on an explicit success — its outcome
	// says nothing about the child.
	adapter.resetIn = time.Hour
	adapter.script(func(llm.Request) (llm.Response, error) {
		return parityResponse(parityCall("quota-delegate", "delegate", map[string]any{"prompt": quotaWaitChildTask})), nil
	}, func(llm.Request) (llm.Response, error) {
		return parityCommunicate("quota-root-done", "the stopped delegate's work is cancelled", true), nil
	})
	run := startRootInput(t, sess, "delegate the quota-wait work")

	childID := awaitChildWaitStart(t, sess, srv)
	if read := readChildThread(t, srv, childID); read.Thread.Evener.Failure == nil {
		t.Fatal("precondition: a fresh read of the waiting delegate carries no wait to clear")
	}

	// The user's Stop for the one delegate this tree holds.
	delegates, err := sess.ListActivityDelegates(context.Background(), appwire.SessionActivityListParams{Ref: "local:" + sess.ID()})
	if err != nil {
		t.Fatalf("listing the tree's delegates: %v", err)
	}
	if len(delegates.Delegates) != 1 || delegates.Delegates[0].DelegateID == "" {
		t.Fatalf("the tree's delegates = %+v, want exactly the one waiting delegate", delegates.Delegates)
	}
	outcome, err := sess.StopDelegateRun(delegates.Delegates[0].DelegateID)
	if err != nil {
		t.Fatalf("stopping the waiting delegate: %v", err)
	}
	if outcome != appwire.DelegateStopStopping {
		t.Fatalf("StopDelegateRun outcome = %q, want stopping: the delegate's waiting turn is what the Stop must end", outcome)
	}

	// The child's own thread settles the Stop: its projection stops waiting
	// and its status leaves active. Judged from the child's reads, not the
	// parent's input.
	testwait.Until(t, "the stopped delegate's read carries no wait", func() bool {
		return readChildThread(t, srv, childID).Thread.Evener.Failure == nil
	})
	testwait.Until(t, "the stopped delegate's status leaves active", func() bool {
		return readChildThread(t, srv, childID).Thread.Status.Type != appwire.ThreadStatusActive
	})
	// No wake announced a retry — the Stop ended the turn inside the wait —
	// so the boundary clear is the only clear that can have run.
	if wakes := childWakeAnnouncements(t, srv, childID); len(wakes) != 0 {
		t.Fatalf("the stopped delegate's wait was woken, not stopped: %+v", wakes)
	}
	awaitRootEnd(t, run)
}

// The stored wait outlives its own reset until its wake. The reset can pass
// while the child still queues for the drive slot the retry retakes — the
// daemon retakes the slot after the timer, before the wake — and a pane
// reloaded in that window must still see the wait. The projection is storage,
// not policy: the wait's lifetime is the daemon's to end, by a wake or by the
// turn's boundary, never the deadline's, so a read must not discard the wait
// because its deadline has passed. Installing the wait with an already-past
// reset reproduces the exact stored state that window reads — the same summary
// a mid-wait install leaves behind once its reset passes — without reaching
// for the agent-internal drive scheduling from here. The wait rides a running
// turn, as every real wait does: the delegate's execution is started before
// the wait is injected, so the read proves an active turn can carry a stored
// wait past its deadline — a fixture that served the wait on an idle thread
// could not defend against a projection that serves waits only while its
// turn runs.
func TestDelegateReadServesTheStoredWaitPastItsReset(t *testing.T) {
	srv := NewServer(ServerConfig{})
	t.Cleanup(srv.Close)
	serveRootWithoutHistory(t, srv, "root")
	const childID = "quota-wait-child"
	// The child's history is what its running turn rides on: without it the
	// read's activeTurnId is always empty, and the assertions below could
	// not tie the wait to the turn it belongs to.
	childPath := writeDelegateTranscript(t, childID, "the delegate's work")
	srv.SetDescendantTranscriptPathFunc(func(threadID string) string {
		if threadID == childID {
			return childPath
		}
		return ""
	})
	srv.RecordDescendantAppEvent("root", threadEvent(childID, events.SessionStartData{WorkingDir: "/work"}))
	srv.RecordDescendantAppEvent("root", threadEvent(childID, events.ExecutionStartedData{TurnID: "quota-wait-turn"}))
	resumesAt := time.Now().Add(-time.Second).UnixMilli()
	srv.RecordDescendantAppEvent("root", threadEvent(childID, events.ModelRetryData{
		Attempt:       1,
		DelayMS:       time.Hour.Milliseconds(),
		ErrorClass:    "quota_exceeded",
		WaitStartedAt: 1,
		ResumesAt:     resumesAt,
		WaitFailure: &appwire.ThreadFailure{
			Title:         "waiting out the usage limit",
			ResumesAt:     resumesAt,
			WaitStartedAt: 1,
		},
	}))

	// The reset is already past when the read lands, and no wake is recorded:
	// the read still serves the wait on the running turn it belongs to.
	read := readChildThread(t, srv, childID)
	if status := read.Thread.Status.Type; status != appwire.ThreadStatusActive {
		t.Fatalf("the waiting delegate's status = %q, want active: the wait rides a running turn", status)
	}
	if got := read.Thread.Evener.ActiveTurnID; got != "quota-wait-turn" {
		t.Fatalf("the waiting delegate's activeTurnId = %q, want the running turn the wait rides", got)
	}
	failure := read.Thread.Evener.Failure
	if failure == nil {
		t.Fatal("a read past the reset dropped the stored wait before any wake: the deadline is not the wait's to end")
	}
	if failure.ResumesAt != resumesAt || failure.WaitStartedAt != 1 {
		t.Fatalf("read failure summary = %+v, want the announced resume instant and stamp", failure)
	}

	// The wake clears it: the zero-delay retry for the same stamp.
	srv.RecordDescendantAppEvent("root", threadEvent(childID, events.ModelRetryData{
		Attempt:       1,
		DelayMS:       0,
		WaitStartedAt: 1,
	}))
	// The wake ends the wait inside the still-running turn: no boundary was
	// reached, so the clear is the wake's alone.
	if status := readChildThread(t, srv, childID).Thread.Status.Type; status != appwire.ThreadStatusActive {
		t.Fatalf("the woken delegate's status = %q, want still active: the wake clears the wait without ending the turn", status)
	}
	if got := readChildThread(t, srv, childID).Thread.Evener.ActiveTurnID; got != "quota-wait-turn" {
		t.Fatalf("the woken delegate's activeTurnId = %q, want the turn still running under the wait's clear", got)
	}
	if failure := readChildThread(t, srv, childID).Thread.Evener.Failure; failure != nil {
		t.Fatalf("a read after the wake = %+v, want no wait", failure)
	}
}
