package main

import (
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/server"
)

// TestDefaultBridgeRegistersAsTheAuthoritativeConsumer pins the wiring that
// makes the daemon's feed lossless, at the one place that decides it.
//
// serve.go's bridge is the single caller of ConsumeEventsLossless in the
// repository. Everything else -- every subagent, every delegate, `serf run`,
// the dev tools -- gets best-effort delivery, which is what keeps an unread
// channel from wedging its emitters. So this dep is the whole of the daemon's
// side of the contract, and if it silently reverted to ranging Events() the
// projection would go back to losing events under load with nothing to say so.
//
// The test drives MORE events than the session's 256-deep buffer holds, past a
// deliberately slow consumer, which is the only condition under which lossy and
// lossless differ at all. The default suite never reaches it: a scripted
// provider emits far fewer events than a real model, so the lossy wiring passes
// everything else in this package.
func TestDefaultBridgeRegistersAsTheAuthoritativeConsumer(t *testing.T) {
	const emitted = 400

	sess, err := agent.NewSession(llm.NewClient(), provider.NewOpenAIProfile("gpt-5.4-mini"),
		execenv.NewLocalExecutionEnvironment(t.TempDir()), agent.SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	var mu sync.Mutex
	efforts := 0
	// Called synchronously: the dep returns once attached, which is the
	// property under test. Wrapping it in `go` would reintroduce exactly the
	// startup window this asserts is closed.
	attach := func() {
		defaultServeDeps().bridge(server.NewServer(server.ServerConfig{}), sess, func(ev events.SessionEvent) {
			if ev.Kind != events.EventReasoningEffortChanged {
				return
			}
			mu.Lock()
			efforts++
			n := efforts
			mu.Unlock()
			// Fall behind early on so the buffer genuinely fills and the
			// emitter genuinely has to wait. A consumer that always keeps up
			// makes lossy and lossless indistinguishable.
			if n < 32 {
				time.Sleep(time.Millisecond)
			}
		})
	}
	// The dep must RETURN once attached; a blocking implementation would hang
	// here until the package timeout instead of naming the broken contract.
	attached := make(chan struct{})
	go func() { defer close(attached); attach() }()
	select {
	case <-attached:
	case <-time.After(10 * time.Second):
		t.Fatal("the bridge dep did not return once attached; the startup window is open again")
	}

	// SetReasoningEffort emits exactly one REASONING_EFFORT_CHANGED per call,
	// alternating so each one is a real change.
	emitDone := make(chan struct{})
	go func() {
		defer close(emitDone)
		for i := range emitted {
			if i%2 == 0 {
				sess.SetReasoningEffort("high")
			} else {
				sess.SetReasoningEffort("low")
			}
		}
	}()
	select {
	case <-emitDone:
	case <-time.After(60 * time.Second):
		t.Fatal("emitting past the buffer wedged the session: the bridge is not draining")
	}

	sess.Close()
	deadline := time.Now().Add(60 * time.Second)
	for {
		mu.Lock()
		n := efforts
		mu.Unlock()
		if n >= emitted || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if efforts != emitted {
		t.Fatalf("bridge saw %d of %d effort changes: the daemon's feed dropped %d",
			efforts, emitted, emitted-efforts)
	}
}
