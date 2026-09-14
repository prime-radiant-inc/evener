package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// heldLegAdapter parks the FIRST model call until the test releases it and
// answers every call with a terminal communicate. It is the skill guard's
// provider "hold" in miniature: a turn whose in-flight leg the test can drain
// a queue into before the leg returns.
type heldLegAdapter struct {
	entered chan struct{} // closed when the first call arrives
	release chan struct{} // closed by the test to let the first call return

	mu       sync.Mutex
	requests []llm.Request
}

func newHeldLegAdapter() *heldLegAdapter {
	return &heldLegAdapter{entered: make(chan struct{}), release: make(chan struct{})}
}

func (a *heldLegAdapter) Name() string { return "openai" }

func (a *heldLegAdapter) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	a.mu.Lock()
	a.requests = append(a.requests, req)
	n := len(a.requests)
	a.mu.Unlock()
	if n == 1 {
		close(a.entered)
		<-a.release
	}
	resp := finalResponse(fmt.Sprintf("leg %d done", n))
	resp.Provider = a.Name()
	resp.Model = req.Model
	return resp, nil
}

func (a *heldLegAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func (a *heldLegAdapter) Requests() []llm.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]llm.Request{}, a.requests...)
}

// boundaryTransitions renders the events that tell a client where the session
// stands -- turn openings and session ends with the state they carry -- in
// stream order, so a failure names what a client would have seen.
func boundaryTransitions(seen []events.SessionEvent) []string {
	var out []string
	for _, ev := range seen {
		switch ev.Kind {
		case events.EventTurnStarted:
			out = append(out, "turn_started:"+ev.Data.(events.TurnStartedData).TurnID)
		case events.EventUserInput:
			out = append(out, "user_input:"+ev.Data.(events.UserInputData).StableTurnID)
		case events.EventSessionEnd:
			data := ev.Data.(events.SessionEndData)
			out = append(out, "session_end:"+data.Reason+":"+data.State)
		}
	}
	return out
}

// TestDrainAsSteerKeepsTheTurnOpenUntilItsSteeringLegRuns is the measured
// failure from issue #1308 (CI run 34900212155): the queue was drained as
// steering while the turn's model call was in flight, that call came back a
// terminal communicate, and the turn settled idle -- EventSessionEnd with
// state idle, the wire's "offer a fresh turn" -- while the drained text was
// still undispatched. A wake opened the carrier turn ~0.9s later. Any client
// reading the status offered a fresh turn on a session that still owed the
// user a leg.
//
// The contract this pins: the input that drained the queue is not over until
// the steering it produced has run. The steering leg runs inside the same
// ProcessInput call, and the only EventSessionEnd of that call comes after the
// carrier turn opened.
func TestDrainAsSteerKeepsTheTurnOpenUntilItsSteeringLegRuns(t *testing.T) {
	adapter := newHeldLegAdapter()
	s := newTestSessionForEnvctx(t, withAdapter(adapter))

	var mu sync.Mutex
	var seen []events.SessionEvent
	drained := make(chan struct{})
	s.ConsumeEventsLossless(func(ev events.SessionEvent) {
		mu.Lock()
		seen = append(seen, ev)
		mu.Unlock()
	}, func() { close(drained) })
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := s.ProcessInput(context.Background(), "open a long turn for the queue", nil)
		done <- err
	}()
	select {
	case <-adapter.entered:
	// TRIPWIRE: scripted in-process adapter, no real I/O; only a genuine hang gets here.
	case <-time.After(10 * time.Second):
		t.Fatal("the first model call never arrived")
	}

	queueOneMutation(t, s, "cm-queued-second-pass", "second pass")
	revision := s.clientMutations.snapshot().QueueRevision
	drain, err := s.AcceptClientMutationDrainAsSteer(appwire.TurnDrainAsSteerParams{
		ClientMutationID:      "cm-drain-mid-leg",
		ExpectedQueueRevision: revision,
	})
	if err != nil {
		t.Fatalf("AcceptClientMutationDrainAsSteer: %v", err)
	}
	if drain.Receipt.TurnID == "" {
		t.Fatalf("drain receipt carries no turn id: %+v", drain.Receipt)
	}
	close(adapter.release)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ProcessInput: %v", err)
		}
	// TRIPWIRE: both legs answer from the scripted adapter; only a genuine hang gets here.
	case <-time.After(10 * time.Second):
		t.Fatal("ProcessInput never returned")
	}

	// Join the event stream before reading it: ProcessInput returns before the
	// consumer necessarily saw its last events.
	s.Close()
	<-drained
	mu.Lock()
	transitions := boundaryTransitions(seen)
	mu.Unlock()

	requests := adapter.Requests()
	if len(requests) != 2 || !requestContainsText(requests[1], "second pass") {
		t.Fatalf("ProcessInput returned after %d model call(s) with the drained steering leg undispatched; the session settled idle mid-turn (transitions: %s)",
			len(requests), strings.Join(transitions, " "))
	}

	carrierOpened := -1
	var sessionEnds []int
	for i, tr := range transitions {
		switch {
		case tr == "turn_started:"+drain.Receipt.TurnID:
			carrierOpened = i
		case strings.HasPrefix(tr, "session_end:"):
			sessionEnds = append(sessionEnds, i)
		}
	}
	if carrierOpened < 0 {
		t.Fatalf("the drain's turn %s never opened (transitions: %s)", drain.Receipt.TurnID, strings.Join(transitions, " "))
	}
	if len(sessionEnds) != 1 || sessionEnds[0] < carrierOpened {
		t.Fatalf("the input ended before its drained steering leg ran; want exactly one session end after %s opened (transitions: %s)",
			drain.Receipt.TurnID, strings.Join(transitions, " "))
	}
	// idle or awaiting is settleTerminalState's call; what matters here is that
	// the one session end is the clean completion of BOTH legs.
	if got := transitions[sessionEnds[0]]; !strings.HasPrefix(got, "session_end:input_complete:") {
		t.Fatalf("session end = %s, want input_complete once both legs ran", got)
	}
}
