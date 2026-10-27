// Package contextmgr manages a session's context pressure: it tracks cumulative
// token usage, applies progressive compaction layers (observation masking,
// thinking clearing, deterministic checkpoint, LLM summarization) to the
// conversation history, and hosts the pluggable context strategies. Strategies
// reach back to their owning session only through the narrow Host interface,
// which breaks the context⇄session cycle.
package contextmgr

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"primeradiant.com/evener/agent/diagnostic"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// CompactionMeta holds session-level metadata injected into compaction summaries.
// The session populates this before each ManageContext call so that checkpoint
// and summarize have access to data outside the turn history.
type CompactionMeta struct {
	SessionID       string   // session id; non-empty only when the session is persistent (has a stateDir)
	ActivatedSkills []string // skill names activated during this session

	// AvailableTranscriptTools names the transcript tools this session's
	// registry actually serves. Compaction artifacts tell the agent how to
	// recover the detail they folded away, and that instruction may only name
	// tools it can call: a typed agent whose tools: list omits read_transcript,
	// or a session with no state directory at all, must not be pointed at one
	// (ruled 2026-08-06). Empty means the artifact says nothing about recovery.
	AvailableTranscriptTools []string

	// TranscriptKept reports that the session is writing its durable
	// transcript, which holds every message in full. A persistent session
	// whose transcript could not be created has a session id but no kept
	// transcript.
	TranscriptKept bool
}

// compactionPointer is the line a compaction artifact opens with to say that
// earlier history was folded into it and how to recover that history; artifact
// names the artifact ("checkpoint", "summary"). Empty when the session is not
// persistent.
func (m *CompactionMeta) compactionPointer(artifact string) string {
	if m == nil || m.SessionID == "" {
		return ""
	}
	return fmt.Sprintf("Earlier history was compacted into this %s to free context-window headroom. This session's id is %s.%s\n", artifact, m.SessionID, m.TranscriptRecoverySentence())
}

// hasTranscript reports whether the session is writing a durable transcript,
// which holds every message in full.
func (m *CompactionMeta) hasTranscript() bool {
	return m != nil && m.SessionID != "" && m.TranscriptKept
}

// TranscriptRecoverySentence is the trailing instruction a compaction artifact
// appends to tell the agent how to recover folded-away detail, worded from the
// transcript tools the session actually has. Empty when it has none.
func (m *CompactionMeta) TranscriptRecoverySentence() string {
	if m == nil {
		return ""
	}
	hasRead := slices.Contains(m.AvailableTranscriptTools, "read_transcript")
	hasFind := slices.Contains(m.AvailableTranscriptTools, "find_session_transcripts")
	switch {
	case hasRead && hasFind:
		return " Use read_transcript to recover earlier detail, or find_session_transcripts to search it."
	case hasRead:
		return " Use read_transcript to recover earlier detail."
	case hasFind:
		return " Use find_session_transcripts to search this session's transcript."
	}
	return ""
}

type compactionTurnCallbackKey struct{}

// WithCompactionTurnCallback returns a context whose callback handles each
// checkpoint or summary created during that operation. Manager.OnCompactionTurn
// remains the fallback for callers that do not install an operation callback.
func WithCompactionTurnCallback(ctx context.Context, callback func(schema.Turn)) context.Context {
	return context.WithValue(ctx, compactionTurnCallbackKey{}, callback)
}

type postFoldInjectionCallbackKey struct{}

// WithPostFoldInjectionCallback returns a context whose callback receives the
// net turn-count delta a Strategy appends to history AFTER its own fold layers
// run — OODAStrategy's orientation message. Those turns
// land after whatever the fold preserved, so a caller measuring the overall
// before/after turn-count delta across the whole ManageContext call needs this
// reported separately to avoid under-shrinking the N4 in-flight-turn boundary
// by exactly this amount, the same class of bug compactionEmitFunc's
// runPreCompactHook-injected count already corrects for (issue #634).
//
// A strategy that does not self-inject after its fold has nothing to report
// and needs not call reportPostFoldInjection at all; a caller that does not
// install this callback (e.g. a test driving ManageContext directly) is
// unaffected — reportPostFoldInjection silently no-ops.
func WithPostFoldInjectionCallback(ctx context.Context, callback func(int)) context.Context {
	return context.WithValue(ctx, postFoldInjectionCallbackKey{}, callback)
}

// reportPostFoldInjection is the strategy-side counterpart of
// WithPostFoldInjectionCallback: report n turns net-appended after this
// strategy's own fold layers. n may be negative (e.g. a self-injecting
// strategy that nets out more markers removed than added) or zero (steady
// state: an old marker was replaced by a new one of the same count) — zero is
// a no-op, since the caller's own before/after delta already accounts for it
// correctly.
func reportPostFoldInjection(ctx context.Context, n int) {
	if n == 0 {
		return
	}
	if callback, ok := ctx.Value(postFoldInjectionCallbackKey{}).(func(int)); ok {
		callback(n)
	}
}

type baselineQueryKey struct{}

// WithBaselineQuery returns a context whose query function reports the N4
// in-flight-turn boundary (turnHistoryBaseline), translated into the CURRENT
// history array's indexing as of the moment it's called: adjusted for every
// fold layer's shrinkage seen so far via EventContextCompaction, but not yet
// adjusted for any strategy-level append/removal a Strategy is about to
// perform on its own.
//
// A self-injecting Strategy that replaces an existing marker turn
// (ooda) uses this to tell whether the
// marker it's about to remove sits before or after the boundary: removing a
// pre-boundary marker shifts every in-flight turn left by one, and the
// marker's own re-append (always at the end) does not restore that —
// WithPostFoldInjectionCallback's plain net-delta count can't see this, since
// the removal and the append cancel out numerically to zero (issue #634).
//
// ok is false when the caller installed no tracking (e.g. a test driving
// ManageContext directly) — a strategy that gets ok=false should not attempt
// a positional correction, matching WithPostFoldInjectionCallback's fallback
// of doing nothing when nothing is installed.
func WithBaselineQuery(ctx context.Context, query func() (int, bool)) context.Context {
	return context.WithValue(ctx, baselineQueryKey{}, query)
}

// currentBaseline is the strategy-side counterpart of WithBaselineQuery.
func currentBaseline(ctx context.Context) (int, bool) {
	if query, ok := ctx.Value(baselineQueryKey{}).(func() (int, bool)); ok {
		return query()
	}
	return 0, false
}

type compactionMetaKey struct{}

// WithCompactionMeta returns a context carrying the compaction metadata for
// ONE fold operation. The session's fold staging installs it per call, so
// concurrent fold publishers never share — or write — Manager.Meta: the
// shared field would race across publishers, slice fields included.
// Manager.Meta remains only as the fallback for direct single-threaded
// callers (tests driving layers without a fold context).
func WithCompactionMeta(ctx context.Context, meta CompactionMeta) context.Context {
	return context.WithValue(ctx, compactionMetaKey{}, meta)
}

// metaFor resolves the compaction metadata for one operation: the per-call
// context value when installed (see WithCompactionMeta), else the Manager's
// fallback field.
func (cm *Manager) metaFor(ctx context.Context) *CompactionMeta {
	if meta, ok := ctx.Value(compactionMetaKey{}).(CompactionMeta); ok {
		return &meta
	}
	return &cm.Meta
}

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

// Manager tracks cumulative token usage and applies progressive compaction
// layers to conversation history as context fills up.
type Manager struct {
	profile  *provider.Profile
	client   *llm.Client
	cheap    *cheapmodel.Caller
	cumUsage llm.Usage
	mu       sync.Mutex

	// ResultToolName is the name of the user communication tool (default "communicate").
	// Used to identify communicate tool calls during compaction.
	ResultToolName string

	// AdapterTimeout is the session's auxiliary request policy. Set before use.
	AdapterTimeout *llm.AdapterTimeout

	// Token measurement from the last API response. When available, used instead
	// of char/4 for the bulk of history. Reset to 0 after compaction.
	lastInputTokens     int // exact input token count from last API response
	historyLenAtMeasure int // number of turns when lastInputTokens was recorded

	// assumedWindowDisclosed records that this manager already emitted the
	// unknown-window disclosure: the assumption is news once per session, not
	// once per round.
	assumedWindowDisclosed bool

	// Thresholds are fractions (0.0–1.0) of the context window.
	// ObservationMaskThreshold and ThinkingClearThreshold are not used by the
	// default compaction path (MaybeCompact/ForceCompact) because in-place
	// modifications bust the prompt cache for all providers. They remain here
	// for the session-log strategy, which calls
	// maskObservations/clearThinking directly.
	ObservationMaskThreshold float64
	ThinkingClearThreshold   float64
	// WarnThreshold is the pressure fraction at which the Session nudges the
	// agent to self-compact at its next clean seam (best-effort; the checkpoint
	// and summary layers remain the guarantee).
	WarnThreshold       float64
	CheckpointThreshold float64
	SummarizeThreshold  float64

	PreserveRecentTurns int

	// OnCompactionTurn is called for each compaction turn created (CHECKPOINT or SUMMARY).
	// Set by the session before ManageContext to record compaction turns in the transcript.
	OnCompactionTurn func(schema.Turn)

	// Meta holds session-level metadata for enriching compaction summaries —
	// as the FALLBACK for direct single-threaded callers only (tests driving
	// layers without a fold context). Fold publishers run concurrently and
	// must never write this shared field: they install per-call metadata via
	// WithCompactionMeta instead, resolved by metaFor.
	Meta CompactionMeta
}

func (cm *Manager) handleCompactionTurn(ctx context.Context, turn schema.Turn) {
	if callback, ok := ctx.Value(compactionTurnCallbackKey{}).(func(schema.Turn)); ok {
		callback(turn)
		return
	}
	if cm.OnCompactionTurn != nil {
		cm.OnCompactionTurn(turn)
	}
}

// NewManager creates a Manager with default thresholds. cheap is the session's
// auxiliary caller; it must be the one the rest of the session shares, so that
// a model the provider refuses is learned once for the whole session rather
// than once per compaction layer.
func NewManager(profile *provider.Profile, client *llm.Client, cheap *cheapmodel.Caller) *Manager {
	return &Manager{
		profile:                  profile,
		client:                   client,
		cheap:                    cheap,
		ObservationMaskThreshold: 0.60,
		ThinkingClearThreshold:   0.70,
		WarnThreshold:            0.75,
		CheckpointThreshold:      0.80,
		SummarizeThreshold:       0.95,
		PreserveRecentTurns:      6,
	}
}

func (cm *Manager) resultToolName() string {
	if cm.ResultToolName != "" {
		return cm.ResultToolName
	}
	return "communicate"
}

// AddUsage records token usage from a completed LLM call.
func (cm *Manager) AddUsage(u llm.Usage) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.cumUsage = cm.cumUsage.Add(u)
}

// CumulativeUsage returns accumulated session totals.
func (cm *Manager) CumulativeUsage() llm.Usage {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.cumUsage
}

// SetCumulativeUsage seeds the running total, used on restore to re-hydrate
// persisted per-session token counts before the first response is recorded.
func (cm *Manager) SetCumulativeUsage(u llm.Usage) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.cumUsage = u
}

// RecordInputTokens stores the exact input token count from an API response,
// along with the history length at that point. This enables accurate pressure
// calculation for subsequent turns without relying on the char/4 heuristic.
func (cm *Manager) RecordInputTokens(tokens int, historyLen int) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.lastInputTokens = tokens
	cm.historyLenAtMeasure = historyLen
}

// RecordInputTokensFor records an API measurement only when the manager still
// holds the profile that produced it. A SetProfile that lands while a request is
// in flight already invalidated the measurement that request belonged to, so
// writing it afterwards would attribute one model's count to another -- and the
// estimator reads the target, so the count would be wrong under the new rules too.
func (cm *Manager) RecordInputTokensFor(profile *provider.Profile, tokens int, historyLen int) bool {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if !sameProfileTarget(cm.profile, profile) {
		return false
	}
	cm.lastInputTokens = tokens
	cm.historyLenAtMeasure = historyLen
	return true
}

// SetProfile replaces the provider profile so that ContextWindowSize() and
// other profile-derived values stay current after a model change. An exact
// token count belongs only to the instance, model, and protocol that produced
// it, so changing any part of that target invalidates the measurement.
func (cm *Manager) SetProfile(profile *provider.Profile) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if !sameProfileTarget(cm.profile, profile) {
		cm.lastInputTokens = 0
		cm.historyLenAtMeasure = 0
		// The unknown-window disclosure names the model it assumed for, so
		// it belongs to the outgoing target: a different model — still
		// unknown — discloses its own assumption on its first fold.
		cm.assumedWindowDisclosed = false
	}
	cm.profile = profile
}

func sameProfileTarget(a, b *provider.Profile) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.ID() != b.ID() || a.Model() != b.Model() || a.Protocol() != b.Protocol() {
		return false
	}
	// The estimator reads more of the row than its identity (see
	// EstimatorTargetsEquivalent), so every field it reads has to match before a
	// measurement may survive: the count was taken under the old rules.
	return sameEstimatorTarget(a.Resolved(), b.Resolved())
}

// sameEstimatorTarget compares the resolved fields the token estimator reads. The
// rules belong to the estimator, so the comparison lives with them
// (llm.EstimatorTargetsEquivalent) and this only names what it is for here.
func sameEstimatorTarget(a, b registry.Resolved) bool {
	return llm.EstimatorTargetsEquivalent(a, b)
}

// currentProfile returns the active profile under cm.mu so reads do not race
// SetProfile (called from Session.SetModel). The profile pointer is swapped
// atomically; a caller uses the returned value for the duration of one operation.
func (cm *Manager) currentProfile() *provider.Profile {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.profile
}

// LastInputTokens returns the most recently recorded input token count.
func (cm *Manager) LastInputTokens() int {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.lastInputTokens
}

// Pressure returns the current context pressure as a fraction of the window.
// It may exceed 1.0 when the estimated usage is larger than the window.
func (cm *Manager) Pressure(history []schema.Turn, sysPromptChars int) float64 {
	return cm.estimatePressure(history, sysPromptChars)
}

// estimatePressure calculates what fraction of the context window is in use.
// Uses actual API-reported token counts when available, falling back to char/4.
// One profile snapshot covers the window, the measurement, and the estimate, so
// a model switch (SetProfile) cannot pair one model's window with another's
// tokens.
func (cm *Manager) estimatePressure(history []schema.Turn, sysPromptChars int) float64 {
	prof, lastTokens, measuredLen := cm.profileSnapshot()
	return cm.pressureFromSnapshot(prof, lastTokens, measuredLen, history, sysPromptChars)
}

// pressureWithProfile reads pressure and the profile it was measured against from
// ONE snapshot, so a compaction layer's decision and that layer's before/after
// diagnostics describe one model even if SetProfile lands in between.
func (cm *Manager) pressureWithProfile(history *[]schema.Turn, sysPromptChars int) (float64, *provider.Profile) {
	prof, lastTokens, measuredLen := cm.profileSnapshot()
	return cm.pressureFromSnapshot(prof, lastTokens, measuredLen, *history, sysPromptChars), prof
}

// pressureFromSnapshot is estimatePressure's core for callers that already hold a
// (profile, measurement) snapshot. The compaction phases read pressure and their
// before/after diagnostics from one snapshot apiece, so a concurrent SetProfile
// cannot decide a layer by one model and describe it by another.
func (cm *Manager) pressureFromSnapshot(prof *provider.Profile, lastTokens, measuredLen int, history []schema.Turn, sysPromptChars int) float64 {
	cw, _ := effectiveWindowOf(prof)
	if cw <= 0 {
		return 0
	}
	used := cm.estimateUsedTokensFor(prof, lastTokens, measuredLen, history, sysPromptChars)
	return pressureOf(used, cw)
}

// contextWindowOf returns the profile's context window, or 0 when the manager
// has no profile. A Manager built without one (NewManager(nil, ...), as the
// session-log strategy tests do) has no window to read, and the readers below
// report "unknown" for that rather than crashing.
func contextWindowOf(prof *provider.Profile) int {
	if prof == nil {
		return 0
	}
	return prof.ContextWindowSize()
}

// effectiveWindowOf is the window the budget sites read: the profile's real
// window when it has one, and AssumedContextWindow when the window is
// unknown. A nil profile — a manager without a model, as test harnesses
// build — has nothing to assume against and stays (0, false).
func effectiveWindowOf(prof *provider.Profile) (window int, assumed bool) {
	if cw := contextWindowOf(prof); cw > 0 {
		return cw, false
	}
	if prof == nil {
		return 0, false
	}
	return AssumedContextWindow, true
}

// AssumedContextWindow is the window the manager budgets against when the
// model's real one is unknown — no user row declares it, the catalog has no
// row, and no live listing supplied one (issue #4564). A zero window used to
// mean "no limit": compaction and the usage warning went dead, and every
// round resent the full accumulated history. Unknown instead assumes a
// window small enough that compaction errs toward folding too early. 128K is
// the floor of current frontier text models' advertised windows, so the
// assumption undershoots only sub-128K models, whose oversized requests fail
// at the API either way; the disclosure names the remedy — declare
// context_window on the provider's model row.
const AssumedContextWindow = 128_000

// UnknownWindowInputTripwire is the exact input-token measurement at which an
// unknown-window session folds regardless of the window math (issue #4564): a
// few hundred K. The tripwire is independent of the assumed window and of the
// configured thresholds, so growth stays bounded even if the assumption or
// the thresholds are wrong. A known window governs on its own, so the
// tripwire never fires for one.
const UnknownWindowInputTripwire = 200_000

// resolvedTargetOf returns the profile's resolved registry row, or the zero row
// when there is no profile: the estimator then falls back to the names the turns
// carry, exactly as the targetless estimator did before the profile-aware
// accounting.
func resolvedTargetOf(prof *provider.Profile) registry.Resolved {
	if prof == nil {
		return registry.Resolved{}
	}
	return prof.Resolved()
}

// profileSnapshot reads the active profile together with the API measurement
// that belongs to it, in one critical section. SetProfile swaps the profile and
// clears the measurement in one section of the same lock, so reading them in
// separate sections could pair one model's window with another model's cleared
// measurement.
func (cm *Manager) profileSnapshot() (*provider.Profile, int, int) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.profile, cm.lastInputTokens, cm.historyLenAtMeasure
}

// estimateUsedTokens is the pure token-accounting core shared by estimatePressure
// and EstimateUsage: it returns the estimated input tokens for a history given the
// last API-reported token count and the history length at which it was measured.
// When a measurement is available and still applies (lastTokens > 0 and the
// measured length is within the current history), it uses the measurement as a
// baseline and estimates only the turns appended since; otherwise it falls back to
// the char/4 heuristic over the whole history plus the system prompt. It takes a
// fresh profile snapshot for callers that hold none.
func (cm *Manager) estimateUsedTokens(lastTokens, measuredLen int, history []schema.Turn, sysPromptChars int) int {
	return cm.estimateUsedTokensFor(cm.currentProfile(), lastTokens, measuredLen, history, sysPromptChars)
}

// estimateUsedTokensFor is the snapshot-taking core: the caller passes the same
// profile it read the window from, so a model switch mid-computation cannot
// combine one model's window with another model's accounting.
func (cm *Manager) estimateUsedTokensFor(prof *provider.Profile, lastTokens, measuredLen int, history []schema.Turn, sysPromptChars int) int {
	if lastTokens > 0 && measuredLen <= len(history) {
		// Use the known token count as baseline, then estimate only new turns.
		return lastTokens + cm.estimateTokensFor(prof, history[measuredLen:])
	}
	// Fall back to char/4 for everything.
	return cm.estimateTokensFor(prof, history) + sysPromptChars/4
}

// EstimatePressure returns the estimated fraction of context window in use.
func (cm *Manager) EstimatePressure(history []schema.Turn, sysPromptChars int) float64 {
	return cm.estimatePressure(history, sysPromptChars)
}

// EstimateStatus returns the estimated context usage and pressure from one
// (profile, measurement) snapshot and one pass over history.
func (cm *Manager) EstimateStatus(history []schema.Turn, sysPromptChars int) (schema.ContextMetrics, float64) {
	prof, lastTokens, measuredLen := cm.profileSnapshot()
	window, assumed := effectiveWindowOf(prof)
	if window <= 0 {
		return schema.ContextMetrics{}, 0
	}
	used := cm.estimateUsedTokensFor(prof, lastTokens, measuredLen, history, sysPromptChars)
	return schema.ContextMetrics{
		Used:      used,
		Window:    window,
		Remaining: max(window-used, 0),
		Assumed:   assumed,
	}, pressureOf(used, window)
}

// pressureOf reports used as a fraction of window. A zero or negative window
// has no fraction to report. The result may exceed 1.0 when used exceeds
// window.
func pressureOf(used, window int) float64 {
	if window <= 0 {
		return 0
	}
	return float64(used) / float64(window)
}

// EstimateUsage returns the estimated used, total, and remaining context
// tokens, with Assumed marking a Window that is the unknown-window assumption
// rather than a size anything vouches for. It delegates to EstimateStatus, so
// the window, the measurement, and the estimate come from one snapshot.
func (cm *Manager) EstimateUsage(history []schema.Turn, sysPromptChars int) schema.ContextMetrics {
	metrics, _ := cm.EstimateStatus(history, sysPromptChars)
	return metrics
}

// ApplyThresholdScale scales all compaction thresholds on cm by the given
// factor. A factor of 0 or 1 leaves defaults unchanged.
//
// Thresholds are clamped to a minimum of 0.20 so that aggressive scaling
// doesn't collapse all layers into a narrow band.
func ApplyThresholdScale(cm *Manager, scale float64) {
	if scale > 0 && scale != 1.0 {
		clamp := func(v float64) float64 {
			if v < 0.20 {
				return 0.20
			}
			return v
		}
		cm.ObservationMaskThreshold = clamp(cm.ObservationMaskThreshold * scale)
		cm.ThinkingClearThreshold = clamp(cm.ThinkingClearThreshold * scale)
		cm.WarnThreshold = clamp(cm.WarnThreshold * scale)
		cm.CheckpointThreshold = clamp(cm.CheckpointThreshold * scale)
		cm.SummarizeThreshold = clamp(cm.SummarizeThreshold * scale)
	}
}

// estimateTokens estimates token count for turns using the char/4 heuristic.
// The manager's profile knows the target's protocol and reasoning capabilities,
// so thinking text is billed exactly for the adapters that replay it: history
// accounting that kept it at zero let compaction defer past the window an
// oversized request would cross. It takes a fresh profile snapshot for callers
// that hold none.
func (cm *Manager) estimateTokens(turns []schema.Turn) int {
	return cm.estimateTokensFor(cm.currentProfile(), turns)
}

// estimateTokensFor is the snapshot-taking estimator: the caller passes the
// same profile whose window its reading used.
func (cm *Manager) estimateTokensFor(prof *provider.Profile, turns []schema.Turn) int {
	messages := make([]llm.Message, 0, len(turns))
	for _, t := range turns {
		if t.Kind == schema.TurnAttentionResolution {
			continue
		}
		messages = append(messages, t.Message)
	}
	return llm.EstimateMessagesInputTokensForResolved(resolvedTargetOf(prof), messages).Tokens
}

func attentionTransparentTurnCount(history []schema.Turn) int {
	count := 0
	for _, turn := range history {
		if turn.Kind != schema.TurnAttentionResolution {
			count++
		}
	}
	return count
}

func attentionTransparentHistory(history []schema.Turn) []schema.Turn {
	if attentionTransparentTurnCount(history) == len(history) {
		return history
	}
	visible := make([]schema.Turn, 0, len(history))
	for _, turn := range history {
		if turn.Kind != schema.TurnAttentionResolution {
			visible = append(visible, turn)
		}
	}
	return visible
}

func attentionTransparentRecentCutoff(history []schema.Turn, preserveRecent int) int {
	if preserveRecent <= 0 {
		return len(history)
	}
	seen := 0
	for i, turn := range slices.Backward(history) {
		if turn.Kind == schema.TurnAttentionResolution {
			continue
		}
		seen++
		if seen == preserveRecent {
			return i
		}
	}
	return 0
}

// --- MaybeCompact orchestrator ---

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

// --- Observation masking (Layer 1) ---

// --- Thinking clearing (Layer 2) ---

// --- Deterministic checkpoint (Layer 3) ---

// foldCutoff is where a compaction of history that keeps preserveRecent
// recent turns cuts it: history[:cutoff] is folded. It is -1 when no safe cut
// exists. The checkpoint and the summarizer both cut with it.
func foldCutoff(history []schema.Turn, preserveRecent int) int {
	return safeCutoff(history, attentionTransparentRecentCutoff(history, preserveRecent))
}

// --- LLM summarization (Layer 4) ---

// HasClient reports whether an LLM client is available (note elicitation and LLM
// summarization both require one).
func (cm *Manager) HasClient() bool { return cm.client != nil }

// summarizeWithLLM calls the cheap model to generate a narrative summary of
// old history. Replaces old history with a single user message containing the
// summary, preserving the most recent turns.
func (cm *Manager) summarizeWithLLM(ctx context.Context, history []schema.Turn, preserveRecent int) ([]schema.Turn, error) {
	return cm.summarizeWithLLMSteered(ctx, history, preserveRecent, "")
}

// summarizeWithLLMSteered is like summarizeWithLLM but accepts optional caller
// instructions that replace the default mandatory-sections prompt when non-empty.
func (cm *Manager) summarizeWithLLMSteered(ctx context.Context, history []schema.Turn, preserveRecent int, instructions string) ([]schema.Turn, error) {
	return cm.summarizeFold(ctx, history, preserveRecent, instructions, nil)
}

// summarizeFold is summarizeWithLLMSteered for a summary that completes a
// fold a checkpoint began this pass. checkpointed is what that checkpoint, at
// history[0], folded: the checkpoint sheds conversation under its own budget,
// which earlier summaries and agent replies share, so the partner's messages
// the summary carries are read from those turns, plus every later turn the
// summary folds, under the carry's own cap. With checkpointed nil they are
// read from the turns the summary folds.
func (cm *Manager) summarizeFold(ctx context.Context, history []schema.Turn, preserveRecent int, instructions string, checkpointed []schema.Turn) ([]schema.Turn, error) {
	if attentionTransparentTurnCount(history) <= preserveRecent {
		return history, nil
	}
	cutoff := foldCutoff(history, preserveRecent)
	if cutoff < 0 {
		return history, nil
	}

	// One profile serves the whole fold: the window check and every request
	// it sends, so a model switch mid-fold cannot redirect its requests to a
	// model the check never saw.
	sumProfile := cm.currentProfile()
	if !summarizingWindowUsable(sumProfile) {
		// The checkpoint already at history[0] stands in place of a summary.
		return history, nil
	}

	// Cap the history text: ~80k chars ≈ 20k tokens, which with the
	// instruction prefix and the reply fits every route summarizingWindowUsable
	// admits.
	const maxHistoryChars = 80_000

	// The checkpoint at history[0] stands for checkpointed; the summary also
	// folds whatever follows it up to cutoff, including turns the pre-compact
	// hook pushed out of the preserved tail.
	partnerSource := history[:cutoff]
	if checkpointed != nil {
		partnerSource = append(slices.Clip(checkpointed), history[1:cutoff]...)
	}
	reminder, carry, summarized := cm.partnerCarry(ctx, partnerSource, len(partnerSource), partnerCarryCeiling(sumProfile))

	var b strings.Builder
	// The partner's messages the turn will not quote come first, so the
	// summarizer reads every one of them whatever else the budget cuts; past
	// half the history budget they arrive condensed in bounded batches
	// (condensePartnerMessages).
	if len(summarized) > 0 {
		// Each message goes in whole; one longer than a part is split into
		// overlapping, numbered parts rather than cut short, small enough that
		// two share a batch.
		var lines []string
		for _, entry := range summarized {
			parts := splitMessage(entry.Text, maxHistoryChars/6, 400)
			for i, part := range parts {
				label := "User: "
				if len(parts) > 1 {
					label = fmt.Sprintf("User (part %d of %d of one message): ", i+1, len(parts))
				}
				lines = append(lines, label+part+"\n")
			}
		}
		condensed, err := cm.condensePartnerMessages(ctx, sumProfile, lines, maxHistoryChars/2)
		if err != nil {
			return nil, err
		}
		b.WriteString("Your human partner's messages that the handoff will not quote, oldest first; summarize them:\n")
		b.WriteString(condensed)
		b.WriteString("\nThe conversation:\n")
	}
	for i := range cutoff {
		t := history[i]
		switch t.Kind {
		case schema.TurnUserInput:
			// Preserve user messages verbatim. Cap at 5k to protect the cheap
			// model's context — normal messages are well under this. On the
			// MaybeCompact and ForceCompact paths the checkpoint layer has
			// already folded these turns, so their user messages arrive inside
			// the checkpoint (a previous compaction) instead of here.
			b.WriteString("User: ")
			b.WriteString(truncate(t.Message.Text(), 5000))
			b.WriteString("\n")
		case schema.TurnCheckpoint, schema.TurnSummary:
			// A previous compaction is the only record of the conversation it
			// folded. It gets up to a checkpoint's size, so a deterministic
			// checkpoint (earlier summaries first, then the newest folded
			// conversation) always arrives whole; the rest of the budget stays
			// for the turns it precedes. A longer one keeps its head, where
			// both summary prompts put the permissions and holds it quotes.
			// Left-out partner messages written first take their share of
			// the budget from it.
			b.WriteString("Previous compaction: ")
			b.WriteString(truncate(t.Message.Text(), min(maxCheckpointChars, max(maxHistoryChars-b.Len(), 0))))
			b.WriteString("\n")
		case schema.TurnAssistant:
			// Extract communicate calls so the summarizer sees how the agent
			// talked to the user, while still distinguishing questions from
			// non-await replies.
			text := t.Message.Text()
			if text != "" {
				b.WriteString("Assistant: ")
				b.WriteString(truncate(text, 500))
				b.WriteString("\n")
			}
			for _, p := range t.Message.Content {
				if p.Kind == llm.ContentToolCall && p.ToolCall != nil && p.ToolCall.Name == cm.resultToolName() {
					endTurn, msg := parseCommunicateArgs(p.ToolCall.Arguments)
					if msg == "" {
						continue
					}
					switch {
					case endTurn:
						fmt.Fprintf(&b, "Agent Message: %s\n", msg)
					default:
						fmt.Fprintf(&b, "Agent Status: %s\n", msg)
					}
				}
			}
		case schema.TurnTool, schema.TurnToolResults:
			for _, p := range t.Message.Content {
				if p.Kind == llm.ContentToolResult && p.ToolResult != nil {
					fmt.Fprintf(&b, "Tool(%s): %s\n", p.ToolResult.Name, truncate(fmt.Sprint(p.ToolResult.Content), 200))
				}
			}
		case schema.TurnSteering:
			// Labeled for the summary prompt's quoting rule.
			if isPartnerSteering(t) {
				b.WriteString("User: ")
			} else {
				b.WriteString("System: ")
			}
			b.WriteString(t.Message.Text())
			b.WriteString("\n")
		}
		// The cap is checked after each turn, so the history can run past it
		// by the last turn written. Every turn kind but steering has its own
		// cap (a previous compaction's is maxCheckpointChars), which
		// bounds that overrun; it is a soft limit protecting the summarizer's
		// context, not a hard one.
		if b.Len() > maxHistoryChars {
			b.WriteString("\n[... truncated ...]\n")
			break
		}
	}

	prompt := buildSummaryPrompt(b.String(), instructions)

	resp, err := cm.completeSummarization(ctx, sumProfile, prompt, func(reply llm.Response) error {
		if err := checkSummaryReply(reply.Text(), instructions); err != nil {
			return err
		}
		return checkNotCutOff(reply)
	})
	if err != nil {
		return nil, err
	}

	// The turn is the model's reply, bounded by the provider's output limit,
	// plus the partner's messages, bounded by maxCheckpointChars; the preserved
	// recent turns sit outside it. One combined budget would mean truncating
	// the reply or shedding partner messages below the cap the #4173 ruling
	// chose for them.
	summaryText := summaryTurnStart + cm.metaFor(ctx).compactionPointer("summary") + reminder +
		renderCompactionModelText(summaryModelTextHeading, resp.Text()) + carry + summaryMarkerEnd
	summaryTurn := schema.NewTurn(schema.TurnSummary, llm.User(summaryText))

	result := make([]schema.Turn, 0, 1+preserveRecent)
	result = append(result, summaryTurn)
	result = append(result, history[cutoff:]...)
	return result, nil
}

// safeCutoff adjusts a cutoff index so the preserved turns don't start with a
// TurnTool or TurnSteering, which would produce invalid message ordering.
// While tracing a tool result back to its assistant tool call, the scan crosses
// presentational markers and steering turns persisted inside that exchange; a
// standalone marker remains a valid cutoff position unless the lookahead also
// crosses steering. TurnTool without a preceding assistant tool_call is invalid.
// TurnSteering after a checkpoint/summary (both user-role) produces consecutive
// user messages that some APIs reject.
// Returns -1 if no safe position exists; callers should skip compaction.
func safeCutoff(history []schema.Turn, cutoff int) int {
	tracingToolResult := false
	crossedSteering := false
	for i := cutoff; i < len(history); i++ {
		k := history[i].Kind
		if k == schema.TurnHookCompleted || k == schema.TurnAttentionResolution {
			continue
		}
		if k == schema.TurnSteering {
			crossedSteering = true
			continue
		}
		tracingToolResult = k == schema.TurnTool || k == schema.TurnToolResults
		break
	}

	for cutoff > 0 && cutoff < len(history) {
		k := history[cutoff].Kind
		if k == schema.TurnTool || k == schema.TurnToolResults {
			tracingToolResult = true
			cutoff--
			continue
		}
		if k == schema.TurnSteering || ((k == schema.TurnHookCompleted || k == schema.TurnAttentionResolution) && (tracingToolResult || crossedSteering)) {
			cutoff--
			continue
		}
		break
	}
	if cutoff <= 0 {
		return -1
	}
	return cutoff
}

// --- Helper functions ---
