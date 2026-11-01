package contextmgr

import (
	"context"
	"fmt"
	"strings"

	"primeradiant.com/evener/agent/diagnostic"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// replaceSteeringMarkerTurn removes every TurnSteering turn whose text
// begins with marker — counting how many sat before the N4 boundary, not just
// recording the last match's index — appends a fresh steering turn carrying
// text at the end (the strongest recency position, inside the preserved
// window for the next compaction), and reports the baseline-corrected
// injection delta via reportPostFoldInjection. Used by the
// self-injecting ooda strategy, whose marker banner is replaced, not
// accumulated, on every ManageContext.
//
// Each removal before the boundary shifts every in-flight turn left by one,
// and the re-append does not undo that — recording only the last match
// would undercount when an earlier duplicate is before the boundary but the
// last match isn't, or when several are (each shifts left by one more, not
// a flat +1). The net turn-count delta alone (e.g. 0 for a one-for-one swap)
// can't see a pre-boundary removal (issue #634), so one is added back per
// pre-boundary removal.
func replaceSteeringMarkerTurn(ctx context.Context, history *[]schema.Turn, marker, text string) {
	preLen := len(*history)
	baseline, haveBaseline := currentBaseline(ctx)
	removedBeforeBaseline := 0
	filtered := (*history)[:0]
	for i, t := range *history {
		// A banner BEGINS with its marker; a steering turn that merely
		// mentions the marker text is someone else's turn and stays.
		if t.Kind == schema.TurnSteering && strings.HasPrefix(t.Message.Text(), marker) {
			if haveBaseline && i < baseline {
				removedBeforeBaseline++
			}
			continue
		}
		filtered = append(filtered, t)
	}
	*history = filtered

	*history = append(*history, schema.NewTurn(schema.TurnSteering, llm.User(text)))

	reportPostFoldInjection(ctx, len(*history)-preLen+removedBeforeBaseline)
}

// resetMeasurement drops the exact API token measurement so the next pressure
// reading falls back to char/4 estimates that reflect in-place history changes.
func (cm *Manager) resetMeasurement() {
	cm.mu.Lock()
	cm.lastInputTokens = 0
	cm.historyLenAtMeasure = 0
	cm.mu.Unlock()
}

// applyLayer runs one compaction layer over *history and reports it: fn
// returns the new history, EventContextCompaction carries the turn and token
// counts measured under prof, and a turn of headKind at the head of the result
// is handed to handleCompactionTurn. A layer that fails warns and leaves
// *history untouched; applyLayer reports whether the layer ran. Pass an empty
// headKind for in-place layers, which produce no compaction turn.
func (cm *Manager) applyLayer(
	ctx context.Context,
	emitFn func(events.EventKind, events.EventData),
	history *[]schema.Turn,
	prof *provider.Profile,
	layer string,
	headKind schema.TurnKind,
	fn func([]schema.Turn) ([]schema.Turn, error),
) bool {
	turnsBefore := len(*history)
	before := cm.estimateTokensFor(prof, *history)
	result, err := fn(*history)
	if err != nil {
		emitFn(events.EventWarning, summarizerFailureWarning(err))
		return false
	}
	*history = result
	after := cm.estimateTokensFor(prof, *history)
	emitFn(events.EventContextCompaction, events.ContextCompactionData{
		Layer:           layer,
		TurnsBefore:     turnsBefore,
		TurnsAfter:      len(*history),
		EstTokensBefore: before,
		EstTokensAfter:  after,
	})
	if headKind != "" && len(*history) > 0 && (*history)[0].Kind == headKind {
		cm.handleCompactionTurn(ctx, (*history)[0])
	}
	return true
}

// MaybeCompact checks context pressure and applies compaction layers progressively.
// It modifies history in place and emits events for each layer applied.
// Called before each LLM request. A model whose window is unknown is managed
// against AssumedContextWindow (issue #4564), disclosed once through emitFn.
func (cm *Manager) MaybeCompact(
	ctx context.Context,
	history *[]schema.Turn,
	sysPromptChars int,
	emitFn func(events.EventKind, events.EventData),
) {
	// One profile snapshot covers the window check and every diagnostic below:
	// a model switch landing mid-compaction would otherwise bill one layer's
	// numbers by another model's thinking rule and image family.
	prof, _, _ := cm.profileSnapshot()
	cw, _ := effectiveWindowOf(prof)
	if cw <= 0 {
		return
	}
	discloseAssumedWindow(cm, prof, emitFn)

	// Each phase reads pressure and its before/after diagnostics from ONE
	// snapshot (see pressureWithProfile), so a concurrent SetProfile cannot decide
	// a layer by one model and describe it by another.
	pressure := func() (float64, *provider.Profile) { return cm.pressureWithProfile(history, sysPromptChars) }

	p, prof := pressure()
	// Window-independent tripwire (issue #4564): force at least checkpoint
	// pressure when it fires; see unknownWindowTripwireFires.
	if cm.unknownWindowTripwireFires(prof) && p < cm.CheckpointThreshold {
		p = cm.CheckpointThreshold
	}
	compacted := false

	// Invalidate API token measurement before running any layer so that
	// between-layer pressure() calls use char/4 (which reflects in-place
	// mutations). Without this, stale lastInputTokens causes both layers
	// to cascade in a single pass.
	if p >= cm.CheckpointThreshold {
		cm.resetMeasurement()
	}

	// Layer 1: Deterministic checkpoint at ≥80%.
	// checkpointed is what this pass's checkpoint folded, which a summary
	// completing the same fold reads the partner's messages from.
	var checkpointed []schema.Turn
	if p >= cm.CheckpointThreshold {
		cm.applyLayer(ctx, emitFn, history, prof, "checkpoint", schema.TurnCheckpoint, func(h []schema.Turn) ([]schema.Turn, error) {
			var folded []schema.Turn
			h, folded = checkpointFold(h, cm.PreserveRecentTurns, cm.metaFor(ctx), cm.resultToolName())
			checkpointed = folded
			return h, nil
		})
		compacted = true
		p, prof = pressure()
	}

	// Layer 2: LLM summarization at ≥90%.
	if p >= cm.SummarizeThreshold && cm.client != nil {
		if cm.applyLayer(ctx, emitFn, history, prof, "summarize", schema.TurnSummary, func(h []schema.Turn) ([]schema.Turn, error) {
			return cm.summarizeFold(ctx, h, cm.PreserveRecentTurns, "", checkpointed)
		}) {
			compacted = true
		}
	}

	// Reset token measurement after compaction since history content changed.
	if compacted {
		cm.resetMeasurement()
	}
}

// claimAssumedWindowDisclosure hands out the right to emit the unknown-window
// disclosure exactly once per model target: the latch is unset AND the
// caller's captured assumption still governs the manager's current profile —
// same target, and still an unknown window — checked under the same lock that
// latches. A SetProfile that swapped models, or a real window that arrived for
// this one in between, refuses the stale caller without touching the latch;
// the current model's own pass claims it instead.
func (cm *Manager) claimAssumedWindowDisclosure(caller *provider.Profile) bool {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if cm.assumedWindowDisclosed || !assumptionStillGoverns(caller, cm.profile) {
		return false
	}
	cm.assumedWindowDisclosed = true
	return true
}

// assumedWindowWarning is the disclosure that the window this session's
// compaction budgets against is an assumption (issue #4564): the model it
// names, the window assumed in its place, and the configuration that would
// replace the assumption with the fact.
func assumedWindowWarning(prof *provider.Profile) events.WarningData {
	return events.WarningData{
		Message: fmt.Sprintf("context window unknown for %s/%s; compaction budgets against an assumed %d-token window; declare context_window in the provider's model row to use the real window", prof.ID(), prof.Model(), AssumedContextWindow),
		Source:  string(diagnostic.SourceEvener),
		Title:   "Context window unknown",
		Hint:    "Compaction stays live against a conservative assumed window. Declare context_window on the model's row in providers.toml to budget against the real size.",
		Code:    events.WarningCodeContextWindowAssumed,
	}
}

// discloseAssumedWindow emits the unknown-window disclosure when prof budgets
// against the assumed window and this manager has not disclosed it yet
// (issue #4564): the window the caller's layers run against is an assumption,
// not a fact, and the session hears that once per model.
func discloseAssumedWindow(cm *Manager, prof *provider.Profile, emitFn func(events.EventKind, events.EventData)) {
	if _, assumed := effectiveWindowOf(prof); assumed && cm.claimAssumedWindowDisclosure(prof) {
		emitFn(events.EventWarning, assumedWindowWarning(prof))
	}
}

// unknownWindowTripwireFires reports the window-unknown tripwire condition
// (issue #4564): with the window unknown, an exact API measurement already
// past a few hundred K input tokens must force a fold whatever the pressure
// estimate says — the char/4 estimate can badly lag a tokenizer-dense
// history, and threshold scaling can move the ordinary gates out of reach.
// The verdict is one locked snapshot, not a profile check plus a separate
// count read: RecordInputTokensFor installs a count only for the profile that
// produced it, and SetProfile clears the count on a target change, so the
// stored count belongs to the CURRENT profile or to no one. A pass whose
// captured target was switched out has no measurement it may act on —
// reading the count separately could catch the switched-to model's own
// measurement instead — and a window that has since become known governs on
// its own, so the unknown check reads the current profile too.
func (cm *Manager) unknownWindowTripwireFires(captured *provider.Profile) bool {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	_, unknown := effectiveWindowOf(cm.profile)
	return unknown && sameProfileTarget(captured, cm.profile) && cm.lastInputTokens >= UnknownWindowInputTripwire
}

// assumptionStillGoverns reports whether an assumption-state captured under
// captured — a tripwire verdict, or the right to disclose the assumed window —
// still applies to current: the model is the same target, and current still
// budgets against the assumed window. Every profile mutation that can
// invalidate a captured assumption-state changes one of those two facts — a
// switch to another model, or a real window arriving for this one (a live
// listing enriching the same target, which sameProfileTarget alone cannot
// see) — so deriving the verdict from the profiles themselves cannot
// desynchronize the way cached state would. The fold gates re-snapshot
// pressure after their mask layers, and the claim runs under its latch's
// lock; both consult the current profile through this one predicate.
func assumptionStillGoverns(captured, current *provider.Profile) bool {
	_, assumed := effectiveWindowOf(current)
	return assumed && sameProfileTarget(captured, current)
}

// ForceCompact runs all compaction layers unconditionally, regardless of context pressure.
// Used for user-initiated compaction (e.g. /compact command). instructions is forwarded
// to the LLM summarization layer; pass "" for default behavior. Returns true if a summary
// was actually generated (false when there is no client or the history is too short to summarize).
func (cm *Manager) ForceCompact(
	ctx context.Context,
	history *[]schema.Turn,
	instructions string,
	emitFn func(events.EventKind, events.EventData),
) (summarized bool) {
	// Reset token measurement so inter-layer estimates use char/4.
	cm.resetMeasurement()

	// One profile snapshot covers every diagnostic below, for the same reason as
	// MaybeCompact: the readings describe one model's rules, not two half-read.
	prof, _, _ := cm.profileSnapshot()

	// Layer 1: Deterministic checkpoint.
	var checkpointed []schema.Turn
	cm.applyLayer(ctx, emitFn, history, prof, "checkpoint", schema.TurnCheckpoint, func(h []schema.Turn) ([]schema.Turn, error) {
		var folded []schema.Turn
		h, folded = checkpointFold(h, cm.PreserveRecentTurns, cm.metaFor(ctx), cm.resultToolName())
		checkpointed = folded
		return h, nil
	})

	// Layer 2: LLM summarization (only if client is available).
	if cm.client != nil {
		// The summarizer returns the input unchanged for short or unsafe history,
		// which must not count a pre-existing summary as a newly generated one.
		canSummarize := attentionTransparentTurnCount(*history) > cm.PreserveRecentTurns &&
			safeCutoff(*history, attentionTransparentRecentCutoff(*history, cm.PreserveRecentTurns)) >= 0
		ok := cm.applyLayer(ctx, emitFn, history, prof, "summarize", schema.TurnSummary, func(h []schema.Turn) ([]schema.Turn, error) {
			return cm.summarizeFold(ctx, h, cm.PreserveRecentTurns, instructions, checkpointed)
		})
		summarized = ok && canSummarize && len(*history) > 0 && (*history)[0].Kind == schema.TurnSummary
	}

	// Reset token measurement after compaction.
	cm.resetMeasurement()
	return summarized
}
