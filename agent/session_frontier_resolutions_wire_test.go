package agent_test

import (
	"errors"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/server"
)

func TestExecutionFrontierResolutionProducersWire(t *testing.T) {
	t.Parallel()
	for _, producer := range []struct {
		name      string
		stabilize bool
	}{
		{name: "resolution-helper"},
		{name: "stop-stabilization", stabilize: true},
	} {
		for _, retained := range []bool{false, true} {
			name := producer.name + "/clean"
			if retained {
				name = producer.name + "/retained"
			}
			t.Run(name, func(t *testing.T) {
				h := agent.NewFrontierResolutionWireHarness(t, producer.stabilize, retained)
				s := h.Session
				srv, read, drain := newFrontierWire(t, s)
				previous := s.LatestExecutionTurnID()
				done := make(chan error, 1)
				go func() {
					defer close(done)
					_, err := s.ProcessInputKind(t.Context(), "", nil, agent.EntryNotification)
					done <- err
				}()
				t.Cleanup(func() { h.ResumeProvider(); <-done })
				awaitFrontierWireBarrier(t, h.ProviderEntered)
				pending := drain()
				current := h.RunningExecutionID()
				if previous == "" || current == "" || current == previous {
					t.Fatalf("held attention execution = %q, previous = %q", current, previous)
				}
				if metadata, history := read(false), read(true); metadata.LatestExecutionTurnID != previous || history.LatestExecutionTurnID != previous {
					t.Fatalf("unrecorded wake metadata/history = %q/%q, want previous %q", metadata.LatestExecutionTurnID, history.LatestExecutionTurnID, previous)
				}
				for _, turn := range h.RecordedTurns() {
					if turn.TurnID == current {
						t.Fatalf("wake recorded an entry before the resolution producer: kind=%s, span=%s", turn.Kind, turn.TurnKind)
					}
				}

				err := h.Produce()
				if retained {
					if !errors.Is(err, transcript.ErrRetainedUnsynced) {
						t.Fatalf("retained producer outcome = %v, want ErrRetainedUnsynced", err)
					}
					if syncs, truncate := h.FaultsRemaining(); syncs != 0 || truncate {
						t.Fatalf("fault face did not run: unspent Sync failures=%d, truncate failure=%t", syncs, truncate)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				wantReopens := int32(0)
				if producer.stabilize {
					wantReopens = 1
				}
				if got := h.ReopenCalls(); got != wantReopens {
					t.Fatalf("actual writer reopens = %d, want %d", got, wantReopens)
				}
				var execution []schema.Turn
				for _, turn := range h.RecordedTurns() {
					if turn.TurnID == current {
						execution = append(execution, turn)
					}
				}
				if len(execution) != 1 {
					t.Fatalf("held execution has %d recorded entries, want only the resolution", len(execution))
				}
				first := execution[0]
				if first.Kind != schema.TurnAttentionResolution || first.TurnKind != schema.TurnSpanExecution || first.OriginalOrdinal != nil {
					t.Fatalf("actual first record kind/span/copy = %s/%s/%v", first.Kind, first.TurnKind, first.OriginalOrdinal)
				}
				if resolution := first.AttentionResolution; resolution == nil || resolution.AttentionID != h.TargetAttentionID || resolution.Disposition != "discarded" || resolution.ResumeGeneration != 0 {
					t.Fatalf("actual resolution metadata = %+v", resolution)
				}

				// Observe every real event so retained-write diagnostics are not lost
				// inside the shared harness's convenience drain. First-record events
				// stay withheld until the previous/new wire disagreement is checked.
				seen := append([]events.SessionEvent(nil), pending...)
				observe := func(ev events.SessionEvent) {
					seen = append(seen, ev)
					if ev.Kind == events.EventExecutionFirstRecorded {
						pending = append(pending, ev)
					} else {
						server.BridgeEvent(srv, ev, nil)
					}
				}
				observeAvailable := func() {
					for {
						select {
						case ev, open := <-s.Events():
							if !open {
								return
							}
							observe(ev)
						default:
							return
						}
					}
				}
				observeAvailable()
				if metadata, history := read(false), read(true); metadata.LatestExecutionTurnID != previous || history.LatestExecutionTurnID != current || s.LatestExecutionTurnID() != current {
					t.Fatalf("before typed bridge metadata/history/runtime = %q/%q/%q, want previous/new %q/%q", metadata.LatestExecutionTurnID, history.LatestExecutionTurnID, s.LatestExecutionTurnID(), previous, current)
				}
				var signal *events.SessionEvent
				for signal == nil {
					for _, ev := range pending {
						if data, ok := ev.Data.(events.ExecutionFirstRecordedData); ok && data.TurnID == current {
							snapshot := ev
							signal = &snapshot
							break
						}
					}
					if signal != nil {
						break
					}
					select {
					case ev := <-s.Events():
						observe(ev)
					case <-time.After(30 * time.Second): // TRIPWIRE: only queued delivery remains while the provider is held; 30s guards a lost signal.
						t.Fatal("resolution committed without delivering its first-record event")
					}
				}
				server.BridgeEvent(srv, *signal, nil)
				if metadata, history := read(false), read(true); metadata.LatestExecutionTurnID != current || metadata.Thread.Evener.LatestExecutionTurnID != current || history.LatestExecutionTurnID != current {
					t.Fatalf("after actual typed bridge metadata/row/history = %q/%q/%q, want %q", metadata.LatestExecutionTurnID, metadata.Thread.Evener.LatestExecutionTurnID, history.LatestExecutionTurnID, current)
				}
				h.ResumeProvider()
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				s.Close()
				observeAvailable()
				var signals, diagnostics int
				for _, ev := range seen {
					if data, ok := ev.Data.(events.ExecutionFirstRecordedData); ok && data.TurnID == current {
						signals++
					}
					if _, ok := ev.Data.(events.WarningData); ok {
						diagnostics++
					}
				}
				if signals != 1 {
					t.Fatalf("actual first-record events = %d, want one", signals)
				}
				if retained && !producer.stabilize && diagnostics == 0 {
					t.Fatal("retained resident resolution produced no warning event")
				}
				t.Logf("producer=%s retained=%t, first=ATTENTION_RESOLUTION execution, reopens=%d, signals=%d, warningEvents=%d", producer.name, retained, h.ReopenCalls(), signals, diagnostics)
			})
		}
	}
}
