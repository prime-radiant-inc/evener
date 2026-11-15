package agent

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// FrontierResolutionWireHarness exposes test-owned gates and actual producer
// calls to agent_test, which can wire a real server without an import cycle.
type FrontierResolutionWireHarness struct {
	Session            *Session
	TargetAttentionID  string
	ProviderEntered    <-chan struct{}
	ResumeProvider     func()
	Produce            func() error
	RunningExecutionID func() string
	RecordedTurns      func() []schema.Turn
	ReopenCalls        func() int32
	FaultsRemaining    func() (int, bool)
}

func NewFrontierResolutionWireHarness(t *testing.T, stabilize, retained bool) FrontierResolutionWireHarness {
	t.Helper()
	s, adapter := newExecutionSession(t)
	if _, err := s.ProcessInput(t.Context(), "opaque-resolution-prior-input", nil); err != nil {
		t.Fatal(err)
	}
	const target = "delegate:frontier-unarmed-resolution-target"
	if _, err := s.appendDelegateNotificationDurably(target, "opaque-unarmed-resolution-target"); err != nil {
		t.Fatal(err)
	}
	// Only the other item arms this attention-only wake. Resolving target must
	// not cancel the model request or consume the wake that admitted it.
	armOneRootAttention(t, s, "dlg_resolution_trigger", "delegate:frontier-resolution-trigger")
	provider := newFrontierFileGate(t)
	adapter.script(func(context.Context) (llm.Response, error) {
		provider.wait()
		return agenttest.FinalResponse("opaque-resolution-answer"), nil
	})
	var fs *frontierTestFS
	if retained {
		if stabilize {
			fs = &frontierTestFS{Fs: afero.NewOsFs()}
		} else {
			fs = installFrontierTestWriter(t, s)
		}
	}
	armFaults := func() {
		if fs != nil {
			fs.mu.Lock()
			fs.failSyncs = 2 // The append's own Sync and its recovery barrier both fail.
			fs.failTruncate = true
			fs.mu.Unlock()
		}
	}
	var reopens atomic.Int32
	if stabilize {
		updateSessionTestConfig(s, func(cfg *testConfig) {
			cfg.delegateAttentionOpenWriter = func(path, id string) (*transcript.Writer, []transcript.Entry, error) {
				var writer *transcript.Writer
				var entries []transcript.Entry
				var err error
				if fs == nil {
					writer, entries, err = transcript.OpenWriterForSession(path, id)
				} else {
					writer, entries, err = transcript.OpenWriterForSessionWithFS(fs, path, id)
				}
				if err == nil {
					reopens.Add(1)
					// Opening and validating must not spend the write's faults.
					armFaults()
				}
				return writer, entries, err
			}
		})
	}
	produce := func() error {
		if stabilize {
			// Exercise stop's real stabilization write, not Stop cancellation.
			return s.stabilizeAttentionForStop(target)
		}
		armFaults()
		// This goes through the free appendDelegateAttentionResolutions helper.
		return s.resolveAttentionDurably([]string{target}, delegateAttentionDiscarded)
	}
	return FrontierResolutionWireHarness{
		Session: s, TargetAttentionID: target, ProviderEntered: provider.entered, ResumeProvider: provider.resume, Produce: produce,
		RunningExecutionID: func() string {
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.execution.turnID
		},
		RecordedTurns: func() []schema.Turn { return transcriptTurnsOf(t, s) },
		ReopenCalls:   reopens.Load,
		FaultsRemaining: func() (int, bool) {
			if fs == nil {
				return 0, false
			}
			fs.mu.Lock()
			defer fs.mu.Unlock()
			return fs.failSyncs, fs.failTruncate
		},
	}
}
