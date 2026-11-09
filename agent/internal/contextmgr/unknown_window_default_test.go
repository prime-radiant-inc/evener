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
	warnings   []events.WarningData
	compaction []events.ContextCompactionData
}

func (s *emitSpy) fn(_ events.EventKind, data events.EventData) {
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

	strategy := &SessionLogStrategy{cm: cm, log: mustNewSessionLog(t, filepath.Join(t.TempDir(), "log.jsonl"))}
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

// TestAssumedWindowDisclosureRefiresOnModelSwitch pins the latch's scope: the
// disclosure names the model it assumed for, so it belongs to a model, not to
// the manager — a mid-session switch to a DIFFERENT unknown-window model
// re-discloses its assumption, while setting the same model again does not.
func TestAssumedWindowDisclosureRefiresOnModelSwitch(t *testing.T) {
	t.Parallel()
	cm := NewManager(unknownGatewayProfile(), nil, cheapmodel.New(nil))
	history := bulkHistory(10, 10_000)
	spy := &emitSpy{}
	cm.MaybeCompact(context.Background(), &history, 0, spy.fn)
	if n := spy.disclosures(); n != 1 {
		t.Fatalf("first model's disclosures = %d, want 1", n)
	}

	other := provider.FromResolved(registry.Resolved{
		Instance: "gw", ModelID: "glm-5-flash", WireID: "glm-5-flash",
		Protocol: registry.ProtocolOpenAIChat, Surface: registry.SurfaceGeneric,
	}, nil)
	cm.SetProfile(other)
	spy2 := &emitSpy{}
	cm.MaybeCompact(context.Background(), &history, 0, spy2.fn)
	if n := spy2.disclosures(); n != 1 {
		t.Fatalf("second model's disclosures = %d, want 1", n)
	}

	// Re-setting the same model is not a switch: no re-disclosure.
	cm.SetProfile(other)
	spy3 := &emitSpy{}
	cm.MaybeCompact(context.Background(), &history, 0, spy3.fn)
	if n := spy3.disclosures(); n != 0 {
		t.Fatalf("same model re-disclosed %d times, want 0", n)
	}
}

// TestStrategyTripwireDropsOnMidPassModelSwitch pins the one-snapshot rule for
// the captured tripwire: the fold gate re-snapshots pressure after the mask
// layers, so a SetProfile landing between the capture and the gate must drop
// the stale verdict — one model's measurement never forces another model's
// fold; the switched-to model's own pass re-captures the tripwire on its
// terms. The mask layer's own event is the deterministic seam: it fires
// after the capture, before the re-snapshot.
func TestStrategyTripwireDropsOnMidPassModelSwitch(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		build func(t *testing.T, cm *Manager) Strategy
	}{
		{"session-log", func(t *testing.T, cm *Manager) Strategy {
			return &SessionLogStrategy{cm: cm, log: mustNewSessionLog(t, filepath.Join(t.TempDir(), "log.jsonl"))}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := llm.NewClient()
			client.Register(&stubSummarizeAdapter{name: "gw", respFn: func(llm.Request) (llm.Response, error) {
				return llm.Response{Message: llm.Assistant("condensed context summary")}, nil
			}})
			// Model A: unknown window, thresholds out of reach, a measurement
			// past the tripwire — only the captured tripwire can force a fold.
			cm := NewManager(unknownGatewayProfile(), client, cheapmodel.New(client))
			cm.CheckpointThreshold = 100
			cm.SummarizeThreshold = 100
			history := bulkHistory(12, 100) // ~1.8K tokens
			cm.RecordInputTokens(UnknownWindowInputTripwire+50_000, len(history))

			// Model B: a different target with a known window so wide its
			// pressure never reaches the out-of-reach thresholds.
			windowB := 1_000_000
			modelB := provider.FromResolved(registry.Resolved{
				Instance: "gw", ModelID: "glm-5-flash", WireID: "glm-5-flash",
				Protocol: registry.ProtocolOpenAIChat, Surface: registry.SurfaceGeneric,
				Caps: registry.Caps{ContextWindow: &windowB},
			}, nil)

			strategy := tc.build(t, cm)
			switched := false
			emit := func(kind events.EventKind, data events.EventData) {
				if kind == events.EventContextCompaction && !switched {
					switched = true
					cm.SetProfile(modelB)
				}
			}
			if err := strategy.ManageContext(context.Background(), &history, 0, emit); err != nil {
				t.Fatalf("ManageContext: %v", err)
			}
			if !switched {
				t.Fatal("the mid-pass switch never ran: the mask layer emitted no event")
			}
			if len(history) != 12 {
				t.Fatalf("stale tripwire folded the switched-to model's history: %d turns (had 12)", len(history))
			}
		})
	}
}

// TestDisclosureClaimRejectsStaleProfile pins the latch claim's atomicity: a
// caller that captured an unknown-window model BEFORE a switch must neither
// emit nor latch — the claim validates the caller's model under the same lock
// that latches, so the switched-to model's own pass still hears its
// assumption exactly once.
func TestDisclosureClaimRejectsStaleProfile(t *testing.T) {
	t.Parallel()
	cm := NewManager(unknownGatewayProfile(), nil, cheapmodel.New(nil))

	// The stale caller captured model A before the switch; the switch to a
	// different unknown-window model resets the latch.
	staleCaller := unknownGatewayProfile()
	modelB := provider.FromResolved(registry.Resolved{
		Instance: "gw", ModelID: "glm-5-flash", WireID: "glm-5-flash",
		Protocol: registry.ProtocolOpenAIChat, Surface: registry.SurfaceGeneric,
	}, nil)
	cm.SetProfile(modelB)

	var emitted []events.WarningData
	emit := func(kind events.EventKind, data events.EventData) {
		if w, ok := data.(events.WarningData); ok {
			emitted = append(emitted, w)
		}
	}
	discloseAssumedWindow(cm, staleCaller, emit)
	if len(emitted) != 0 {
		t.Fatalf("stale caller emitted %d disclosures after the switch, want 0", len(emitted))
	}

	// The latch must still be unset for the switched-to model: its own pass
	// discloses exactly once, not zero — the stale caller must not have
	// consumed the latch.
	history := bulkHistory(10, 10_000)
	spy := &emitSpy{}
	cm.MaybeCompact(context.Background(), &history, 0, spy.fn)
	if n := spy.disclosures(); n != 1 {
		t.Fatalf("the switched-to model's own pass disclosed %d times, want 1: the stale caller consumed its latch", n)
	}
}

// knownWindowArrival builds the same-target enrichment a live listing
// supplies: the gateway model's real window, on the identical resolved row.
func knownWindowArrival() *provider.Profile {
	window := 1_000_000
	enriched := provider.FromResolved(registry.Resolved{
		Instance: "gw", ModelID: "deepseek-4.1-flash", WireID: "deepseek-4.1-flash",
		Protocol: registry.ProtocolOpenAIChat, Surface: registry.SurfaceGeneric,
		Caps: registry.Caps{ContextWindow: &window},
	}, nil)
	if enriched.ContextWindowSize() != window {
		panic("knownWindowArrival: fixture lost its window")
	}
	return enriched
}

// TestStrategyTripwireDropsWhenWindowArrivesMidPass pins the third mid-pass
// seam: a live listing can enrich the SAME model with a real window, and
// sameProfileTarget is window-blind, so the swap keeps the measurement and the
// target match. A tripwire captured while the window was unknown must not
// force a fold under a profile that now knows its window — a known window
// governs on its own. The mask layer's own event is the deterministic seam,
// as in the model-switch test above.
func TestStrategyTripwireDropsWhenWindowArrivesMidPass(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		build func(t *testing.T, cm *Manager) Strategy
	}{
		{"session-log", func(t *testing.T, cm *Manager) Strategy {
			return &SessionLogStrategy{cm: cm, log: mustNewSessionLog(t, filepath.Join(t.TempDir(), "log.jsonl"))}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := llm.NewClient()
			client.Register(&stubSummarizeAdapter{name: "gw", respFn: func(llm.Request) (llm.Response, error) {
				return llm.Response{Message: llm.Assistant("condensed context summary")}, nil
			}})
			// The model is unknown-window, thresholds are out of reach, and a
			// measurement past the tripwire is planted — only the captured
			// tripwire can force a fold.
			cm := NewManager(unknownGatewayProfile(), client, cheapmodel.New(client))
			cm.CheckpointThreshold = 100
			cm.SummarizeThreshold = 100
			history := bulkHistory(12, 100) // ~1.8K tokens
			cm.RecordInputTokens(UnknownWindowInputTripwire+50_000, len(history))

			strategy := tc.build(t, cm)
			arrived := false
			emit := func(kind events.EventKind, data events.EventData) {
				if kind == events.EventContextCompaction && !arrived {
					arrived = true
					cm.SetProfile(knownWindowArrival())
				}
			}
			if err := strategy.ManageContext(context.Background(), &history, 0, emit); err != nil {
				t.Fatalf("ManageContext: %v", err)
			}
			if !arrived {
				t.Fatal("the mid-pass enrichment never ran: the mask layer emitted no event")
			}
			if len(history) != 12 {
				t.Fatalf("tripwire captured under the unknown window folded a history whose window had arrived: %d turns (had 12)", len(history))
			}
		})
	}
}

// TestDisclosureClaimRejectsKnownWindowArrival pins the claim's half of the
// same seam: a caller that captured the model while its window was unknown
// must not announce an assumed window after the same model's real window
// arrived — a known window is never disclosed as assumed.
func TestDisclosureClaimRejectsKnownWindowArrival(t *testing.T) {
	t.Parallel()
	cm := NewManager(unknownGatewayProfile(), nil, cheapmodel.New(nil))

	// The caller captured the model while its window was unknown; a live
	// listing then enriched the same target with its real window.
	staleCaller := unknownGatewayProfile()
	cm.SetProfile(knownWindowArrival())

	var emitted []events.WarningData
	emit := func(kind events.EventKind, data events.EventData) {
		if w, ok := data.(events.WarningData); ok {
			emitted = append(emitted, w)
		}
	}
	discloseAssumedWindow(cm, staleCaller, emit)
	if len(emitted) != 0 {
		t.Fatalf("announced an unknown window for a model whose current profile knows it: %d disclosures, want 0", len(emitted))
	}
}

// TestEstimateUsageMarksAssumedWindow pins the meter's honesty (#4596): the
// assumed window reaches clients as plain fact unless the flag rides with it.
// EstimateUsage is the wire's source (Session.ContextMetrics feeds the hub
// envelope), so Assumed must be true exactly while the budgeting window is the
// assumption — and flip false the moment a real window arrives, the same
// assumptionStillGoverns discipline the tripwire and the disclosure follow.
func TestEstimateUsageMarksAssumedWindow(t *testing.T) {
	history := []schema.Turn{schema.NewTurn(schema.TurnUserInput, llm.User("hello world"))}

	cm := NewManager(unknownGatewayProfile(), nil, cheapmodel.New(nil))
	if got := cm.EstimateUsage(history, 0); got.Window != AssumedContextWindow || !got.Assumed {
		t.Fatalf("EstimateUsage() = %+v, want window %d with Assumed true", got, AssumedContextWindow)
	}

	known := NewManager(testProfile("anthropic", "claude-opus-4-6", 1_000_000), nil, cheapmodel.New(nil))
	if got := known.EstimateUsage(history, 0); got.Window != 1_000_000 || got.Assumed {
		t.Fatalf("EstimateUsage() = %+v, want the known window with Assumed false", got)
	}

	// A live listing can enrich the same model mid-session; the assumption
	// must not survive the arrival.
	cm.SetProfile(knownWindowArrival())
	if got := cm.EstimateUsage(history, 0); got.Window != 1_000_000 || got.Assumed {
		t.Fatalf("EstimateUsage() = %+v, want the arrived window with Assumed false", got)
	}
}

// TestTripwireRejectsAnotherModelsMeasurement pins the tripwire's pairing
// property: the verdict must come from one snapshot in which the measurement
// still belongs to the profile the pass decided on. The interleaving that
// defeats a separate count read: the A-pass captures its (unknown-window)
// profile, then SetProfile(B) clears A's measurement and
// RecordInputTokensFor(B, …) installs B's own count — the A-pass must not
// read B's count and fold on it.
func TestTripwireRejectsAnotherModelsMeasurement(t *testing.T) {
	// The A-pass: an unknown-window profile and the snapshot it captured.
	cm := NewManager(unknownGatewayProfile(), nil, cheapmodel.New(nil))
	captured, _, _ := cm.profileSnapshot()
	if _, unknown := effectiveWindowOf(captured); !unknown {
		t.Fatal("fixture: the gateway profile must resolve an unknown window")
	}

	// The interleaving: A is switched out for a KNOWN-window model, and the
	// new model records a measurement far past the tripwire.
	b := testProfile("openai", "gpt-5", 1_000_000)
	cm.SetProfile(b)
	if !cm.RecordInputTokensFor(b, UnknownWindowInputTripwire+50_000, 0) {
		t.Fatal("fixture: the new model's measurement was refused")
	}

	// The A-pass's tripwire, evaluated on its captured profile: B's count is
	// not A's measurement, so it must not fire — on the pre-fix head it does.
	if cm.unknownWindowTripwireFires(captured) {
		t.Fatal("the A-pass tripwire fired on the switched-to model's measurement")
	}

	// B's own pass, on its own captured profile: a known window never trips,
	// whatever the count — its ordinary thresholds govern.
	if cm.unknownWindowTripwireFires(b) {
		t.Fatal("a known-window pass tripped the unknown-window wire")
	}
}
