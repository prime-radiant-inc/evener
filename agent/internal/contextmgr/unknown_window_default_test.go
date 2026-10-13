package contextmgr

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// The unknown-window contract of issue #4564: a model whose window nothing
// vouches for (no user row, no catalog row, no live listing) still gets
// context management — budgeted against a conservative assumed window, with
// a window-independent tripwire behind it — while catalog and declared
// windows keep governing exactly as before.

// emitSpy records what MaybeCompact emits across one or more calls. The
// manager calls emitFn synchronously, so no locking is needed.
type emitSpy struct {
	kinds      []events.EventKind
	warnings   []events.WarningData
	compaction []events.ContextCompactionData
}

func (s *emitSpy) fn(kind events.EventKind, data events.EventData) {
	s.kinds = append(s.kinds, kind)
	if w, ok := data.(events.WarningData); ok {
		s.warnings = append(s.warnings, w)
	}
	if c, ok := data.(events.ContextCompactionData); ok {
		s.compaction = append(s.compaction, c)
	}
}

func (s *emitSpy) disclosures() int {
	n := 0
	for _, w := range s.warnings {
		if w.Code == events.WarningCodeContextWindowAssumed {
			n++
		}
	}
	return n
}

// unknownGatewayProfile is the genuinely-unknown fixture issue #4564 targets:
// a custom gateway serving a model id no user row, catalog row, or live
// listing vouches for, so the window resolves to 0.
func unknownGatewayProfile() *provider.Profile {
	profile := provider.FromResolved(registry.Resolved{
		Instance: "gw", ModelID: "deepseek-4.1-flash", WireID: "deepseek-4.1-flash",
		Protocol: registry.ProtocolOpenAIChat, Surface: registry.SurfaceGeneric,
	}, nil)
	if profile.ContextWindowSize() != 0 {
		panic("unknownGatewayProfile: fixture resolved a window")
	}
	return profile
}

// bulkHistory builds alternating user/assistant turns of reps × "token " each
// — ~reps×1.5 tokens per turn by the char/4 estimate.
func bulkHistory(turns, reps int) []schema.Turn {
	history := make([]schema.Turn, 0, turns)
	for i := range turns {
		text := strings.Repeat("token ", reps)
		if i%2 == 1 {
			history = append(history, schema.NewTurn(schema.TurnAssistant, llm.Assistant(text)))
		} else {
			history = append(history, schema.NewTurn(schema.TurnUserInput, llm.User(text)))
		}
	}
	return history
}

// TestUnknownWindowTripwireFoldsOnMeasuredInput pins the window-independent
// half of issue #4564: with the window unknown, an exact API measurement past
// a few hundred K input tokens forces the checkpoint fold even when the
// configured thresholds are out of reach — the pressure estimate must never
// be the only thing standing between an unknown-window session and unbounded
// growth.
func TestUnknownWindowTripwireFoldsOnMeasuredInput(t *testing.T) {
	t.Parallel()
	cm := NewManager(unknownGatewayProfile(), nil, cheapmodel.New(nil))
	// No pressure estimate can reach these; only the tripwire can fold.
	cm.CheckpointThreshold = 100
	cm.SummarizeThreshold = 100

	history := bulkHistory(12, 100) // ~1.8K tokens
	measured := UnknownWindowInputTripwire + 50_000
	cm.RecordInputTokens(measured, len(history))

	spy := &emitSpy{}
	cm.MaybeCompact(context.Background(), &history, 0, spy.fn)
	if len(history) >= 12 {
		t.Fatalf("tripwire left %d turns (had 12): no fold on a %d-token measurement", len(history), measured)
	}
	if len(spy.compaction) == 0 {
		t.Fatal("tripwire fold emitted no compaction event")
	}
	if got := cm.LastInputTokens(); got != 0 {
		t.Fatalf("LastInputTokens after the tripwire fold = %d, want 0 (reset with the fold)", got)
	}
}

// TestKnownWindowTripwireStaysDead pins the tripwire's other half: a known
// window governs on its own, so the same large exact measurement never forces
// a fold the window's own thresholds did not ask for.
func TestKnownWindowTripwireStaysDead(t *testing.T) {
	t.Parallel()
	profile := provider.WithContextWindow(unknownGatewayProfile(), 1_000_000)
	cm := NewManager(profile, nil, cheapmodel.New(nil))
	cm.CheckpointThreshold = 100 // unreachable by design, as above

	history := bulkHistory(12, 100)
	measured := UnknownWindowInputTripwire + 50_000
	cm.RecordInputTokens(measured, len(history))

	spy := &emitSpy{}
	cm.MaybeCompact(context.Background(), &history, 0, spy.fn)
	if len(history) != 12 {
		t.Fatalf("tripwire folded a known-window session: %d turns (had 12), %d compaction events", len(history), len(spy.compaction))
	}
	if got := cm.LastInputTokens(); got != measured {
		t.Fatalf("LastInputTokens = %d, want the measurement undisturbed at %d", got, measured)
	}
}

// TestCatalogWindowNeverAssumed pins that the assumption only ever covers the
// genuinely unknown: a catalog model keeps its real window — the metrics and
// the fold thresholds read the catalog value, and the disclosure never fires.
func TestCatalogWindowNeverAssumed(t *testing.T) {
	t.Parallel()
	profile := NewOpenAIProfile("gpt-5.2")
	if profile.ContextWindowSize() <= 0 {
		t.Fatalf("fixture lost its catalog window: %d", profile.ContextWindowSize())
	}
	cm := NewManager(profile, nil, cheapmodel.New(nil))

	history := bulkHistory(12, 100)
	if m := cm.EstimateUsage(history, 0); m.Window != profile.ContextWindowSize() {
		t.Fatalf("EstimateUsage window = %d, want the catalog's %d", m.Window, profile.ContextWindowSize())
	}
	spy := &emitSpy{}
	cm.MaybeCompact(context.Background(), &history, 0, spy.fn)
	if n := spy.disclosures(); n != 0 {
		t.Fatalf("catalog model disclosed an assumed window %d times, want 0", n)
	}
}

// TestDeclaredWindowBeatsAssumption pins the precedence: a declared
// context_window on the model's row is the window. An otherwise-unknown model
// with a declared window folds at the declared size, never against the
// default, and is not disclosed as assumed.
func TestDeclaredWindowBeatsAssumption(t *testing.T) {
	t.Parallel()
	profile := provider.WithContextWindow(unknownGatewayProfile(), 27_500)
	cm := NewManager(profile, nil, cheapmodel.New(nil))

	history := bulkHistory(8, 2_000) // ~24K tokens: ~87% of the declared 27.5K
	if p := cm.Pressure(history, 0); p < cm.CheckpointThreshold {
		t.Fatalf("Pressure against the declared window = %v, want ≥ %v", p, cm.CheckpointThreshold)
	}
	spy := &emitSpy{}
	cm.MaybeCompact(context.Background(), &history, 0, spy.fn)
	if len(history) >= 8 {
		t.Fatalf("declared-window session did not fold: %d turns (had 8)", len(history))
	}
	if n := spy.disclosures(); n != 0 {
		t.Fatalf("declared window disclosed as assumed %d times, want 0", n)
	}
	if m := cm.EstimateUsage(history, 0); m.Window != 27_500 {
		t.Fatalf("post-fold metrics window = %d, want the declared 27500", m.Window)
	}
}

// TestUnknownWindowOptInStrategiesStayLive pins #4564's rule on the opt-in
// strategies, whose own window gates used to skip ManageContext entirely on
// an unknown window: each now budgets against the assumed window — folding a
// history that has outgrown it and disclosing the assumption exactly once —
// and the shared latch keeps the disclosure once per manager across passes.
// ooda rides session-log's ManageContext, so the same gate covers it.
func TestUnknownWindowOptInStrategiesStayLive(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		build func(t *testing.T, cm *Manager) Strategy
	}{
		{"session-log", func(t *testing.T, cm *Manager) Strategy {
			return &SessionLogStrategy{cm: cm, log: mustNewSessionLog(t, filepath.Join(t.TempDir(), "log.jsonl"))}
		}},
		{"obs-mask", func(_ *testing.T, cm *Manager) Strategy { return NewObsMaskStrategy(cm) }},
		{"checkpoint-pred", func(_ *testing.T, cm *Manager) Strategy { return NewCheckpointPredStrategy(cm) }},
		{"ooda", func(t *testing.T, cm *Manager) Strategy {
			sls, err := NewSessionLogStrategy(cm, &fakeStrategyHost{stateDir: t.TempDir(), id: "sess", profile: cm.currentProfile()})
			if err != nil {
				t.Fatalf("NewSessionLogStrategy: %v", err)
			}
			return &OODAStrategy{SessionLogStrategy: sls}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := llm.NewClient()
			client.Register(&stubSummarizeAdapter{name: "gw", respFn: func(llm.Request) (llm.Response, error) {
				return llm.Response{Message: llm.Assistant("condensed context summary")}, nil
			}})
			cm := NewManager(unknownGatewayProfile(), client, cheapmodel.New(client))
			strategy := tc.build(t, cm)

			history := bulkHistory(10, 10_000) // ~150K tokens: past every layer's gate against the assumed 128K
			before := len(history)
			spy := &emitSpy{}
			if err := strategy.ManageContext(context.Background(), &history, 0, spy.fn); err != nil {
				t.Fatalf("ManageContext: %v", err)
			}
			if len(spy.compaction) == 0 {
				t.Fatal("strategy emitted no compaction events under the assumed window")
			}
			if len(history) >= before {
				t.Fatalf("strategy folded nothing: %d turns (had %d)", len(history), before)
			}
			if n := spy.disclosures(); n != 1 {
				t.Fatalf("assumption disclosures = %d, want exactly 1", n)
			}

			spy2 := &emitSpy{}
			if err := strategy.ManageContext(context.Background(), &history, 0, spy2.fn); err != nil {
				t.Fatalf("second ManageContext: %v", err)
			}
			if n := spy2.disclosures(); n != 0 {
				t.Fatalf("second pass disclosed the assumption %d times, want 0", n)
			}
		})
	}
}

// TestUnknownWindowStrategyTripwireForcesFold pins the tripwire on a
// strategy path: with the window unknown and the configured thresholds out
// of reach, an exact measurement past the tripwire still forces the
// strategy's own fold — the same guarantee MaybeCompact carries. The mask
// layers answer ordinary pressure; the tripwire guarantees the fold.
func TestUnknownWindowStrategyTripwireForcesFold(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	client.Register(&stubSummarizeAdapter{name: "gw", respFn: func(llm.Request) (llm.Response, error) {
		return llm.Response{Message: llm.Assistant("condensed context summary")}, nil
	}})
	cm := NewManager(unknownGatewayProfile(), client, cheapmodel.New(client))
	// No pressure estimate can reach these; only the tripwire can fold.
	cm.CheckpointThreshold = 100
	cm.SummarizeThreshold = 100

	history := bulkHistory(12, 100) // ~1.8K tokens
	measured := UnknownWindowInputTripwire + 50_000
	cm.RecordInputTokens(measured, len(history))

	strategy := NewObsMaskStrategy(cm)
	spy := &emitSpy{}
	if err := strategy.ManageContext(context.Background(), &history, 0, spy.fn); err != nil {
		t.Fatalf("ManageContext: %v", err)
	}
	if len(history) >= 12 {
		t.Fatalf("strategy tripwire left %d turns (had 12): no fold on a %d-token measurement", len(history), measured)
	}
	if len(spy.compaction) == 0 {
		t.Fatal("strategy tripwire fold emitted no compaction event")
	}
	if got := cm.LastInputTokens(); got != 0 {
		t.Fatalf("LastInputTokens after the tripwire fold = %d, want 0 (reset with the fold)", got)
	}
}
