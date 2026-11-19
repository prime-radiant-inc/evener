package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/hooks"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/llm"
)

// A close-dispatched Notification hook must not hold the work that follows the
// publication that dispatched it.
//
// The hook is a user's command and may run for its whole configured timeout.
// Dispatching it synchronously on the live cascade made
// warnIncompleteCloseBeforeDisposal block until that cascade expired, so the
// worktree removals that run next started on a spent budget and left collectible
// isolation lanes on disk even though the event consumer was healthy.
//
// The hook here blocks until the test releases it, and the ordering is a
// rendezvous rather than a window: the adapter signals its entry on a channel,
// then waits for a second channel the test closes only after the assertions that
// the hook was still in flight. A synchronous dispatch cannot satisfy those
// assertions by being fast or slow.
func TestIncompleteCloseHookDoesNotHoldTheWorkAfterItsPublication(t *testing.T) {
	// Not parallel: shortenCloseCascadeBudget and ObserveCloseDrainPublication
	// are package-wide. The budget is generous because nothing here waits it out:
	// it only has to outlast this test's own bookkeeping, and a publication slice
	// that expired mid-test would be indistinguishable from a released one on the
	// error alone.
	shortenCloseCascadeBudget(t, 20*time.Second)

	gate := newGatedNotificationHookClient()
	client := llm.NewClient()
	client.Register(gate)
	runner := hooks.NewRunner(client, "gpt-5.2")
	runner.Add(plugin.HookNotification, plugin.RegisteredHook{
		Matcher: "*",
		Type:    "prompt",
		Prompt:  "report $MESSAGE",
	})

	s := &Session{id: "sess_async_hook", events: make(chan events.SessionEvent, 8)}
	s.env = execenv.NewLocalExecutionEnvironment(t.TempDir())
	s.hookRunner = runner
	// The production policy, so the hook's lifecycle events are published the way
	// a real close publishes them.
	s.installRunnerEventCallback(runner)

	var published context.Context
	ObserveCloseDrainPublication = func(bound context.Context) { published = bound }
	defer func() { ObserveCloseDrainPublication = nil }()

	cascade := tripwireCtx(t)
	dispatched := make(chan struct{})
	go func() {
		defer close(dispatched)
		s.warnIncompleteCloseBeforeDisposal(cascade, "delegate tree", context.DeadlineExceeded)
	}()

	// The premise: the hook really reached its command, so everything below is
	// about a dispatch in flight rather than one that never happened.
	select {
	case <-gate.started:
	// TRIPWIRE: the adapter is in-process and both channels are this test's; this
	// ceiling only fires if the publication never dispatched the hook at all.
	case <-time.After(10 * time.Second):
		t.Fatal("the incomplete-close publication never dispatched its Notification hook")
	}

	// The publication returns while the hook is still running. A synchronous
	// dispatch cannot: the adapter stays parked until release below.
	select {
	case <-dispatched:
	// TRIPWIRE: the hook is provably in flight and this test owns its release, so
	// this ceiling only fires if the publication is waiting for it.
	case <-time.After(10 * time.Second):
		t.Fatal("the incomplete-close publication did not return while its Notification hook was still " +
			"running: a slow hook spends the cascade the worktree removals that follow need")
	}

	// The step after the publication runs on a LIVE cascade, which is the property
	// the removals rely on.
	if err := cascade.Err(); err != nil {
		t.Fatalf("the cascade ended while the hook was still running (%v): the publication spent the "+
			"budget the removals that follow need", err)
	}
	if published == nil {
		t.Fatal("the publication reported no bound context, so nothing below can say whether the hook's " +
			"own sends were made on a live deadline")
	}
	// The publication context outlives the hook it dispatched: the hook's lifecycle
	// and output sends are made through this bound, and releasing it at the
	// publication would hand them a deadline nobody expired. A release reads as
	// Canceled, which is what distinguishes it from a slice that ran out.
	if errors.Is(published.Err(), context.Canceled) {
		t.Fatal("the publication context was released while the hook it dispatched was still running: a " +
			"send that hook makes through the bound would observe a cancellation the publication promised " +
			"was live")
	}

	// And the close joins the hook: nothing below the join may run while the hook
	// can still publish.
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		s.waitCloseDrainHooks()
	}()
	select {
	case <-joined:
		t.Fatal("waitCloseDrainHooks returned while the hook it tracks was still running: the teardown " +
			"that follows the join would close a stream the hook is still publishing on")
	default:
	}

	gate.releaseHook()
	select {
	case <-joined:
	// TRIPWIRE: the release above is the only thing the hook waits for.
	case <-time.After(10 * time.Second):
		t.Fatal("waitCloseDrainHooks did not return after the hook finished: the close would wait out " +
			"the rest of its cascade on a dispatch that is already over")
	}
	// Released exactly once, and only now: the release is what stops the context
	// from leaking for the life of the process. Canceled specifically, so an
	// expired slice cannot stand in for the release this test is asserting.
	if err := published.Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("the publication context was not released after its hook finished (%v): every close "+
			"that dispatched a hook would leak one", err)
	}

	// The hook really ran to completion on that bound: its start and its end both
	// reached the stream.
	started, ended := hookLifecycleOnStream(s, plugin.HookNotification)
	if !started || !ended {
		t.Fatalf("the Notification hook's lifecycle reached the stream as start=%t end=%t, want both: "+
			"the dispatch must run the hook, not merely start one", started, ended)
	}
}

// hookLifecycleOnStream reports whether the session's channel carried a start
// and an end for the named hook event.
func hookLifecycleOnStream(s *Session, event plugin.HookEvent) (started, ended bool) {
	for len(s.events) > 0 {
		ev := <-s.events
		switch data := ev.Data.(type) {
		case events.HookStartData:
			started = started || (ev.Kind == events.EventHookStart && data.Event == string(event))
		case events.HookEndData:
			ended = ended || (ev.Kind == events.EventHookEnd && data.Event == string(event))
		}
	}
	return started, ended
}

// gatedNotificationHookClient is a prompt-hook LLM client that reports entering
// the call and then waits to be released, so a test can hold a close-dispatched
// hook in flight while it asserts what runs beside the dispatch.
type gatedNotificationHookClient struct {
	started  chan struct{}
	release  chan struct{}
	start    sync.Once
	released sync.Once
}

func newGatedNotificationHookClient() *gatedNotificationHookClient {
	return &gatedNotificationHookClient{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (*gatedNotificationHookClient) Name() string { return "openai" }

func (c *gatedNotificationHookClient) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	c.start.Do(func() { close(c.started) })
	select {
	case <-c.release:
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
	return llm.Response{
		Provider: "openai",
		Model:    req.Model,
		Message:  llm.Assistant("the hook ran"),
		Finish:   llm.FinishReason{Reason: llm.FinishReasonStop},
	}, nil
}

func (*gatedNotificationHookClient) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

// releaseHook lets the hook's command return, exactly once.
func (c *gatedNotificationHookClient) releaseHook() {
	c.released.Do(func() { close(c.release) })
}
