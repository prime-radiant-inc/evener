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

// quotaWaitProvider answers the root's requests from a script. The delegate
// child's first request meets a usage limit naming a reset a few seconds out
// (the real clock governs this server-side fixture: the session's fake clock
// is agent-internal and cannot be injected from here), and every child
// request after the limit cleared answers with a finishing communicate.
type quotaWaitProvider struct {
	mu      sync.Mutex
	steps   []func(llm.Request) (llm.Response, error)
	limited atomic.Bool
	// holdRecovered, when non-nil, parks the child's first request after the
	// limit on the channel: the retry has gone out and the turn is still
	// active, which is the window a read must prove carries no wait.
	holdRecovered chan struct{}
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
			return llm.Response{}, quotaUsageLimitError(nextWholeSecond(time.Now().Add(quotaWaitResetIn)))
		}
		if hold := p.holdRecovered; hold != nil {
			p.mu.Lock()
			p.holdRecovered = nil
			p.mu.Unlock()
			<-hold
		}
		return parityCommunicate("quota-child-done", "the delegate finished its work", true), nil
	}
	p.mu.Lock()
	if len(p.steps) == 0 {
		p.mu.Unlock()
		return llm.Response{Message: llm.Assistant("unscripted request")}, nil
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

// quotaWaitResetIn is how far the usage limit's reset sits in the future when
// the child's call meets it: long enough that the wait stands across the
// mid-wait read below, short enough that the retry goes out and the read
// clears well inside the test's own deadline.
const quotaWaitResetIn = 3 * time.Second

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
	ctx := context.Background()
	// The root's first request creates the delegate, and after the delegate
	// finishes it answers once more so the root's turn ends cleanly. The
	// child's first request meets the limit, so the child waits inside its
	// generation turn.
	adapter.script(func(llm.Request) (llm.Response, error) {
		return parityResponse(parityCall("quota-delegate", "delegate", map[string]any{"prompt": quotaWaitChildTask})), nil
	}, func(llm.Request) (llm.Response, error) {
		return parityCommunicate("quota-root-done", "the delegated work is done", true), nil
	})
	run := make(chan error, 1)
	go func() {
		_, err := sess.ProcessInput(ctx, "delegate the quota-wait work", nil)
		run <- err
	}()

	// The child's wait is agent state first: the root answers
	// DescendantQuotaWaits for the waiting child (the sidebar's path today).
	// The read is the pane's, and the fact under test.
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

	// Read the child's own thread fresh, mid-wait. The stored projection must
	// carry the wait the row already knows about.
	read := readChildThread(t, srv, childID)
	failure := read.Thread.Evener.Failure
	if failure == nil {
		t.Fatal("a fresh read of the waiting delegate carries no failure summary")
	}
	if failure.ResumesAt == 0 {
		t.Errorf("read resumesAt = 0, want the reset instant the wait named")
	}
	if failure.WaitStartedAt == 0 {
		t.Errorf("read waitStartedAt = 0, want the wait's stamp (the identity its announcements carry)")
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

	// The wait ends when the retry goes out: the reset passes and the child
	// retries on the recovered quota. The recovered answer is held so the read
	// below lands mid-retry, while the child's turn is still active — the
	// wake, not the turn's end, is what must have cleared the stored wait.
	recovered := make(chan struct{})
	adapter.mu.Lock()
	adapter.holdRecovered = recovered
	adapter.mu.Unlock()
	testwait.Until(t, "the delegate's retry goes out and a fresh read carries no wait", func() bool {
		return readChildThread(t, srv, childID).Thread.Evener.Failure == nil
	})
	if status := readChildThread(t, srv, childID).Thread.Status.Type; status != appwire.ThreadStatusActive {
		t.Fatalf("the child's status mid-retry = %q, want active: the clear was proven in the retry's window", status)
	}
	close(recovered)
	select {
	case err := <-run:
		if err != nil {
			t.Fatalf("the root's input after the delegate finished: %v", err)
		}
	default:
		t.Fatal("the root's input ended before the delegate's wait did")
	}
}

// A Stop while the delegate waits ends the turn as an ordinary cancellation:
// no wake announces a retry, so the stored wait must not outlive the turn it
// belonged to. The boundary status that ends the child's working span clears
// it, exactly as the root's failure facet is re-sampled at every turn
// boundary — otherwise a delegate stopped mid-wait reads as waiting forever.
func TestDelegateReadClearsTheQuotaWaitWhenTheTurnEnds(t *testing.T) {
	t.Parallel()
	sess, srv, adapter := newQuotaWaitProjection(t)
	ctx := context.Background()
	// The root's one request creates the delegate; the child's first request
	// meets the limit and the child waits. The root's own turn is held open
	// by the delegate tool call, never answered: the Stop ends it.
	adapter.script(func(llm.Request) (llm.Response, error) {
		return parityResponse(parityCall("quota-delegate", "delegate", map[string]any{"prompt": quotaWaitChildTask})), nil
	})
	run := make(chan error, 1)
	go func() {
		_, err := sess.ProcessInput(ctx, "delegate the quota-wait work", nil)
		run <- err
	}()

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
	if read := readChildThread(t, srv, childID); read.Thread.Evener.Failure == nil {
		t.Fatal("precondition: a fresh read of the waiting delegate carries no wait to clear")
	}

	// The user's Stop for the one delegate this tree holds.
	delegates, err := sess.ListActivityDelegates(ctx, appwire.SessionActivityListParams{Ref: "local:" + sess.ID()})
	if err != nil {
		t.Fatalf("listing the tree's delegates: %v", err)
	}
	if len(delegates.Delegates) != 1 || delegates.Delegates[0].DelegateID == "" {
		t.Fatalf("the tree's delegates = %+v, want exactly the one waiting delegate", delegates.Delegates)
	}
	if _, err := sess.StopDelegateRun(delegates.Delegates[0].DelegateID); err != nil {
		t.Fatalf("stopping the waiting delegate: %v", err)
	}

	testwait.Until(t, "the stopped delegate's read carries no wait", func() bool {
		return readChildThread(t, srv, childID).Thread.Evener.Failure == nil
	})
	select {
	case err := <-run:
		if err == nil {
			t.Fatal("the root's input the Stop ended reported success")
		}
	default:
		t.Fatal("the root's input ended before the delegate's wait did")
	}
}
