package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func TestExecutionFrontierAttentionFirstPersistDuringHeldProvider(t *testing.T) {
	t.Parallel()
	for _, failReadBack := range []bool{false, true} {
		name := "clean"
		if failReadBack {
			name = "failed-read-back"
		}
		t.Run(name, func(t *testing.T) {
			s, adapter := newExecutionSession(t)
			if _, err := s.ProcessInput(t.Context(), "opaque-prior-input", nil); err != nil {
				t.Fatal(err)
			}
			previous := s.LatestExecutionTurnID()
			if previous == "" {
				t.Fatal("fixture recorded no previous execution")
			}
			armOneRootAttention(t, s, "dlg_first", "delegate:first-frontier")
			gate := newFrontierFileGate(t)
			adapter.script(func(context.Context) (llm.Response, error) {
				gate.wait()
				return agenttest.FinalResponse("opaque-attention-response"), nil
			})
			done := make(chan error, 1)
			go func() {
				_, err := s.ProcessInputKind(t.Context(), "", nil, EntryNotification)
				done <- err
			}()
			awaitFrontierGate(t, gate)
			s.mu.Lock()
			current := s.execution.turnID
			s.mu.Unlock()
			if current == "" || current == previous || s.LatestExecutionTurnID() != previous {
				t.Fatal("attention wake did not reach an unrecorded running execution")
			}
			var reads atomic.Int32
			if failReadBack {
				updateSessionTestConfig(s, func(cfg *testConfig) {
					cfg.delegateAttentionReadFold = func(path, id string) (delegateAttentionFold, error) {
						if reads.Add(1) == 2 {
							return delegateAttentionFold{}, errFrontierFileFault
						}
						return readDelegateAttentionFold(path, id)
					}
				})
			}
			_, err := s.appendDelegateNotificationDurably("delegate:second-frontier", "opaque-second-attention")
			if failReadBack != errors.Is(err, errFrontierFileFault) || (!failReadBack && err != nil) {
				t.Fatalf("attention append outcome = %v, failed read-back %t", err, failReadBack)
			}
			if failReadBack && reads.Load() != 2 {
				t.Fatalf("read-back fault was not triggered, reads = %d", reads.Load())
			}
			if got := s.LatestExecutionTurnID(); got != current {
				t.Errorf("attention first-persist frontier = %q, want %q", got, current)
			}
			var first *schema.Turn
			for _, turn := range transcriptTurnsOf(t, s) {
				if turn.TurnID == current {
					snapshot := turn
					first = &snapshot
					break
				}
			}
			if first == nil || first.TurnKind != schema.TurnSpanExecution {
				t.Fatalf("attention wake's first recorded entry = %+v", first)
			}
		waitForSignal:
			for {
				select {
				case ev := <-s.Events():
					if data, ok := ev.Data.(events.ExecutionFirstRecordedData); !ok || data.TurnID != current {
						continue
					}
					break waitForSignal
				case <-time.After(30 * time.Second): // TRIPWIRE: only queued delivery remains while the provider is held; 30s guards a lost signal.
					t.Fatal("attention first persist emitted no signal while provider held")
				}
			}
			updateSessionTestConfig(s, func(cfg *testConfig) { cfg.delegateAttentionReadFold = nil })
			if failReadBack {
				if _, err := s.appendDelegateNotificationDurably("delegate:second-frontier", "opaque-second-attention"); err != nil {
					t.Fatal(err)
				}
			}
			gate.resume()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			s.Close()
			for ev := range s.Events() {
				if data, ok := ev.Data.(events.ExecutionFirstRecordedData); ok && data.TurnID == current {
					t.Fatal("attention retry emitted a second first-record signal")
				}
			}
		})
	}
}

func TestExecutionFrontierAutomaticCompactionFirstPersist(t *testing.T) {
	t.Parallel()
	s, adapter := newExecutionSession(t)
	for range 8 {
		if _, err := s.ProcessInput(t.Context(), "opaque-compaction-history", nil); err != nil {
			t.Fatal(err)
		}
	}
	previous := s.LatestExecutionTurnID()
	s.elicitNoteFn = func(context.Context, []schema.Turn) (string, error) { return "", nil }
	s.contextMgr.CheckpointThreshold = 0.5
	s.contextMgr.SummarizeThreshold = 2.0
	s.contextMgr.PreserveRecentTurns = 6
	forcePressureAbove(t, s, 0.85)
	armOneRootAttention(t, s, "dlg_compact", "delegate:compact-frontier")
	gate := newFrontierFileGate(t)
	adapter.script(func(context.Context) (llm.Response, error) {
		gate.wait()
		return agenttest.FinalResponse("opaque-compaction-response"), nil
	})
	done := make(chan error, 1)
	go func() {
		_, err := s.ProcessInputKind(t.Context(), "", nil, EntryNotification)
		done <- err
	}()
	awaitFrontierGate(t, gate)
	s.mu.Lock()
	current := s.execution.turnID
	s.mu.Unlock()
	if current == "" || current == previous {
		t.Fatal("compaction fixture did not open a new attention execution")
	}
	var first *schema.Turn
	var preservedExecutions int
	for _, turn := range transcriptTurnsOf(t, s) {
		if first == nil && turn.TurnID == current && turn.OriginalOrdinal == nil {
			snapshot := turn
			first = &snapshot
		}
		if turn.OriginalOrdinal != nil && turn.TurnKind == schema.TurnSpanExecution && turn.TurnID != current {
			preservedExecutions++
		}
	}
	if first == nil || first.Kind != schema.TurnCheckpoint || first.TurnKind != schema.TurnSpanExecution {
		t.Fatalf("attention execution's first persisted entry = %+v, want checkpoint", first)
	}
	if preservedExecutions == 0 {
		t.Fatal("automatic compaction replayed no older first-execution entries")
	}
	if got := s.LatestExecutionTurnID(); got != current {
		t.Errorf("frontier after real checkpoint and old execution copies = %q, want %q", got, current)
	}
	for {
		select {
		case ev := <-s.Events():
			data, ok := ev.Data.(events.ExecutionFirstRecordedData)
			if !ok || data.TurnID != current {
				continue
			}
			gate.resume()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			s.Close()
			for ev := range s.Events() {
				if data, ok := ev.Data.(events.ExecutionFirstRecordedData); ok && data.TurnID == current {
					t.Fatal("preserved execution copy emitted another first-record signal")
				}
			}
			return
		case <-time.After(30 * time.Second): // TRIPWIRE: the checkpoint is recorded; 30s guards queued delivery before the held provider finishes.
			t.Fatal("automatic checkpoint emitted no first-record signal")
		}
	}
}
