package agent_test

import (
	"encoding/json"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/server"
)

// Only the execution frontier is under test. Unused display facets are empty,
// while execution, queue, metadata and work values come from the real runtime.
type frontierWireSource struct{ s *agent.Session }

func (s frontierWireSource) LatestExecutionTurnID() string { return s.s.LatestExecutionTurnID() }
func (s frontierWireSource) ContextStatus() (server.ContextMetrics, float64) {
	return server.ContextMetrics{}, 0
}
func (s frontierWireSource) DetailedStatus() server.DetailedStatus { return server.DetailedStatus{} }
func (s frontierWireSource) ClientMutationProjection() (appwire.QueueState, []appwire.PendingMutation) {
	return s.s.ClientMutationProjection()
}
func (s frontierWireSource) TaskAggregate() *appwire.TaskAggregate { return nil }
func (s frontierWireSource) WorkMetrics() (int64, *appwire.EvenerUsage, int64) {
	return s.s.WorkMillisSnapshot(), nil, s.s.ActiveTurnStartedAtMillis()
}
func (s frontierWireSource) PendingAskSnapshot() (*appwire.PendingQuestion, *appwire.PendingAskState) {
	return s.s.PendingAskSnapshot()
}
func (s frontierWireSource) FailedToolCalls() (int, bool)                             { return s.s.FailedToolCallsSnapshot() }
func (s frontierWireSource) RestingFailure() *appwire.ThreadFailure                   { return nil }
func (s frontierWireSource) PendingEscalations() []appwire.SandboxEscalationRequested { return nil }
func (s frontierWireSource) ReasoningInfo() (string, []string, bool)                  { return "", nil, false }
func (s frontierWireSource) VisionModel() string                                      { return s.s.VisionModel() }
func (s frontierWireSource) SessionMeta() schema.SessionMeta                          { return s.s.Meta() }

func newFrontierWire(t *testing.T, s *agent.Session) (*server.Server, func(bool) appwire.ThreadHistoryReadResponse, func() []events.SessionEvent) {
	t.Helper()
	srv := server.NewServer(server.ServerConfig{})
	t.Cleanup(srv.Close)
	prepared, err := server.PrepareAppIdentity("local", s.ID(), s.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	srv.WireTranscriptHistory(s)
	srv.ReplaceAppIdentity(prepared.WithRecordedLength(s.TranscriptRecordedLength()).WithBootGeneration("1"), nil)
	srv.SetThreadEnvelopeSource(frontierWireSource{s: s})
	// While the producer is held, drain every queued real event through Bridge.
	// The first-record backstop is withheld so it cannot mask an early goal row.
	drain := func() []events.SessionEvent {
		var recorded []events.SessionEvent
		if len(s.Events()) == cap(s.Events()) {
			t.Error("fixture filled the event buffer before its controlled drain")
		}
		for {
			select {
			case ev, open := <-s.Events():
				if !open {
					return recorded
				}
				if ev.Kind == events.EventExecutionFirstRecorded {
					recorded = append(recorded, ev)
				} else {
					server.BridgeEvent(srv, ev, nil)
				}
			default:
				return recorded
			}
		}
	}
	s.SetExecutionStartedFunc(func(id string) {
		drain()
		srv.SetProcessingTurn(id)
	})
	conn := srv.AppServer().NewConnection("goal-gap-wire")
	response := conn.HandleMessage(t.Context(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize,
		appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	if response.Kind() != appwire.MessageResponse {
		t.Fatalf("initialization outcome = %v", response.Kind())
	}
	read := func(includeTurns bool) appwire.ThreadHistoryReadResponse {
		t.Helper()
		message := conn.HandleMessage(t.Context(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodThreadHistoryRead,
			appwire.ThreadReadParams{Ref: "local:" + s.ID(), IncludeTurns: includeTurns}))
		if message.Kind() != appwire.MessageResponse {
			t.Fatalf("versioned read outcome = %+v", message)
		}
		encoded, err := json.Marshal(message.Response.Result)
		if err != nil {
			t.Fatal(err)
		}
		var result appwire.ThreadHistoryReadResponse
		if err := json.Unmarshal(encoded, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	drain()
	return srv, read, drain
}

func awaitFrontierWireBarrier(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(30 * time.Second): // TRIPWIRE: the scripted producer's local barrier normally signals immediately; 30s guards a hang.
		t.Fatal("frontier producer did not reach its barrier")
	}
}

func TestGoalContinuationFrontierWireAcrossEmitToRecordGap(t *testing.T) {
	t.Parallel()
	h := agent.NewFrontierGoalWireHarness(t)
	s := h.Session
	_, read, drain := newFrontierWire(t, s)
	done := make(chan error, 1)
	go func() {
		defer close(done)
		_, err := s.ProcessInput(t.Context(), "opaque-goal-input", nil)
		done <- err
	}()
	t.Cleanup(func() { h.ResumeRecord(); h.ResumeProvider(); <-done })
	awaitFrontierWireBarrier(t, h.RecordEntered)
	previous := s.LatestExecutionTurnID()
	if previous == "" {
		t.Fatal("goal fixture recorded no previous execution")
	}
	drain()
	if metadata := read(false); metadata.LatestExecutionTurnID != previous {
		t.Fatalf("pre-record metadata frontier = %q, previous = %q", metadata.LatestExecutionTurnID, previous)
	}
	h.ResumeRecord()
	awaitFrontierWireBarrier(t, h.ProviderEntered)
	drain()
	metadata, history := read(false), read(true)
	if history.LatestExecutionTurnID == "" || history.LatestExecutionTurnID == previous {
		t.Fatalf("held goal history did not advance: previous = %q, history = %q", previous, history.LatestExecutionTurnID)
	}
	if metadata.LatestExecutionTurnID != history.LatestExecutionTurnID {
		t.Errorf("held goal metadata = %q, recorded history = %q", metadata.LatestExecutionTurnID, history.LatestExecutionTurnID)
	}
	h.ResumeProvider()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	s.Close()
	drain()
}

func TestExecutionFrontierFirstPersistWireBeforeAndAfterSignal(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"attention", "failed-read-back", "compaction"} {
		t.Run(mode, func(t *testing.T) {
			h := agent.NewFrontierAttentionWireHarness(t, mode)
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
			if got := read(false).LatestExecutionTurnID; previous == "" || got != previous {
				t.Fatalf("before-signal metadata = %q, previous = %q", got, previous)
			}
			if mode != "compaction" {
				if got := read(true).LatestExecutionTurnID; got != previous {
					t.Fatalf("unrecorded attention execution moved history to %q", got)
				}
				err := h.AppendAttention()
				if mode == "failed-read-back" {
					if err == nil || h.ReadBackReads() != 2 {
						t.Fatalf("read-back fault not reached: reads = %d, error = %v", h.ReadBackReads(), err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				pending = append(pending, drain()...)
			}
			history := read(true)
			current := history.LatestExecutionTurnID
			if current == "" || current == previous || s.LatestExecutionTurnID() != current {
				t.Fatalf("first persisted execution did not advance: previous = %q, runtime = %q, history = %q", previous, s.LatestExecutionTurnID(), current)
			}
			if got := read(false).LatestExecutionTurnID; got != previous {
				t.Fatalf("another event masked the signal: metadata = %q, previous = %q", got, previous)
			}
			var signal *events.SessionEvent
			for _, ev := range pending {
				if data, ok := ev.Data.(events.ExecutionFirstRecordedData); ok && data.TurnID == current {
					snapshot := ev
					signal = &snapshot
				}
			}
			for signal == nil {
				select {
				case ev := <-s.Events():
					if data, ok := ev.Data.(events.ExecutionFirstRecordedData); ok {
						if data.TurnID == current {
							signal = &ev
						}
					} else {
						server.BridgeEvent(srv, ev, nil)
					}
				case <-time.After(30 * time.Second): // TRIPWIRE: only queued delivery remains while the provider is held; 30s guards a lost signal.
					t.Fatalf("recorded history = %q, metadata = %q, first-record signal absent", current, read(false).LatestExecutionTurnID)
				}
			}
			server.BridgeEvent(srv, *signal, nil)
			if got := read(false).LatestExecutionTurnID; got != current {
				t.Errorf("after-signal metadata = %q, history = %q", got, current)
			}
			h.ResumeProvider()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			s.Close()
			drain()
		})
	}
}
