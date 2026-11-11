package agent

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// FrontierGoalWireHarness exposes only test-owned producer gates to the
// external agent_test package, which can import server without an import cycle.
type FrontierGoalWireHarness struct {
	Session         *Session
	RecordEntered   <-chan struct{}
	ProviderEntered <-chan struct{}
	ResumeRecord    func()
	ResumeProvider  func()
}

func NewFrontierGoalWireHarness(t *testing.T) FrontierGoalWireHarness {
	t.Helper()
	s, adapter := newExecutionSession(t)
	fs := installFrontierTestWriter(t, s)
	record, provider := newFrontierFileGate(t), newFrontierFileGate(t)
	fs.mu.Lock()
	fs.writeGate = record
	fs.continuationOnly = true
	fs.mu.Unlock()
	s.getOrCreateGoalStore().Set("opaque-frontier-goal", time.Now())
	adapter.script(
		func(context.Context) (llm.Response, error) {
			return agenttest.ToolCallResponse(writeFileCall("frontier-work", "frontier-progress.txt", "opaque-work")), nil
		},
		func(context.Context) (llm.Response, error) {
			return agenttest.FinalResponse("opaque-first-answer"), nil
		},
		func(context.Context) (llm.Response, error) {
			provider.wait()
			return agenttest.ToolCallResponse(updateGoalCall("frontier-complete", "complete")), nil
		},
		func(context.Context) (llm.Response, error) { return agenttest.FinalResponse("opaque-last-answer"), nil },
	)
	return FrontierGoalWireHarness{Session: s, RecordEntered: record.entered, ProviderEntered: provider.entered, ResumeRecord: record.resume, ResumeProvider: provider.resume}
}

type FrontierAttentionWireHarness struct {
	Session         *Session
	ProviderEntered <-chan struct{}
	ResumeProvider  func()
	AppendAttention func() error
	ReadBackReads   func() int32
}

func NewFrontierAttentionWireHarness(t *testing.T, mode string) FrontierAttentionWireHarness {
	t.Helper()
	s, adapter := newExecutionSession(t)
	priorRuns := 1
	if mode == "compaction" {
		priorRuns = 8
	}
	for range priorRuns {
		if _, err := s.ProcessInput(t.Context(), "opaque-wire-prior-input", nil); err != nil {
			t.Fatal(err)
		}
	}
	armOneRootAttention(t, s, "dlg_wire_first", "delegate:wire-first-frontier")
	if mode == "compaction" {
		s.elicitNoteFn = func(context.Context, []schema.Turn) (string, error) { return "", nil }
		s.contextMgr.CheckpointThreshold = 0.5
		s.contextMgr.SummarizeThreshold = 2.0
		s.contextMgr.PreserveRecentTurns = 6
		forcePressureAbove(t, s, 0.85)
	}
	provider := newFrontierFileGate(t)
	adapter.script(func(context.Context) (llm.Response, error) {
		provider.wait()
		return agenttest.FinalResponse("opaque-wire-attention-answer"), nil
	})
	var reads atomic.Int32
	appendAttention := func() error {
		if mode == "failed-read-back" {
			updateSessionTestConfig(s, func(cfg *testConfig) {
				cfg.delegateAttentionReadFold = func(path, id string) (delegateAttentionFold, error) {
					if reads.Add(1) == 2 {
						return delegateAttentionFold{}, errFrontierFileFault
					}
					return readDelegateAttentionFold(path, id)
				}
			})
		}
		_, err := s.appendDelegateNotificationDurably("delegate:wire-second-frontier", "opaque-wire-second-attention")
		return err
	}
	return FrontierAttentionWireHarness{Session: s, ProviderEntered: provider.entered, ResumeProvider: provider.resume,
		AppendAttention: appendAttention, ReadBackReads: reads.Load}
}
