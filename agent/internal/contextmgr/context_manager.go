// Package contextmgr manages a session's context pressure: it tracks cumulative
// token usage, applies progressive compaction layers (observation masking,
// thinking clearing, deterministic checkpoint, LLM summarization) to the
// conversation history, and hosts the pluggable context strategies. Strategies
// reach back to their owning session only through the narrow Host interface,
// which breaks the context⇄session cycle.
package contextmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"primeradiant.com/evener/agent/diagnostic"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/agent/internal/runetrim"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/searchresult"
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
// run — MemoryCrystalsStrategy's crystal bank, RecursiveDistillStrategy's
// distilled-memory banner, OODAStrategy's orientation message. Those turns
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
// (memory-crystals, recursive-distill, ooda) uses this to tell whether the
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
// injection delta via reportPostFoldInjection. Shared by the three
// self-injecting strategies (memory-crystals, recursive-distill, ooda),
// whose marker banners are replaced, not accumulated, on every ManageContext.
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
	// for experimental Strategy implementations that call
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

func (cm *Manager) completeCheap(ctx context.Context, req llm.Request) (llm.Response, error) {
	return cm.cheap.Complete(ctx, cm.currentProfile(), req)
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
		cm.mu.Lock()
		cm.lastInputTokens = 0
		cm.historyLenAtMeasure = 0
		cm.mu.Unlock()
	}

	// Layer 1: Deterministic checkpoint at ≥80%.
	// checkpointed is what this pass's checkpoint folded, which a summary
	// completing the same fold reads the partner's messages from.
	var checkpointed []schema.Turn
	if p >= cm.CheckpointThreshold {
		turnsBefore := len(*history)
		before := cm.estimateTokensFor(prof, *history)
		*history, checkpointed = checkpointFold(*history, cm.PreserveRecentTurns, cm.metaFor(ctx), cm.resultToolName())
		after := cm.estimateTokensFor(prof, *history)
		emitFn(events.EventContextCompaction, events.ContextCompactionData{
			Layer:           "checkpoint",
			TurnsBefore:     turnsBefore,
			TurnsAfter:      len(*history),
			EstTokensBefore: before,
			EstTokensAfter:  after,
		})
		if len(*history) > 0 && (*history)[0].Kind == schema.TurnCheckpoint {
			cm.handleCompactionTurn(ctx, (*history)[0])
		}
		compacted = true
		p, prof = pressure()
	}

	// Layer 2: LLM summarization at ≥90%.
	if p >= cm.SummarizeThreshold && cm.client != nil {
		turnsBefore := len(*history)
		before := cm.estimateTokensFor(prof, *history)
		result, err := cm.summarizeFold(ctx, *history, cm.PreserveRecentTurns, "", checkpointed)
		if err != nil {
			// On error, emit warning but continue with current history.
			emitFn(events.EventWarning, summarizerFailureWarning(err))
		} else {
			*history = result
			after := cm.estimateTokensFor(prof, *history)
			emitFn(events.EventContextCompaction, events.ContextCompactionData{
				Layer:           "summarize",
				TurnsBefore:     turnsBefore,
				TurnsAfter:      len(*history),
				EstTokensBefore: before,
				EstTokensAfter:  after,
			})
			if len(*history) > 0 && (*history)[0].Kind == schema.TurnSummary {
				cm.handleCompactionTurn(ctx, (*history)[0])
			}
			compacted = true
		}
	}

	// Reset token measurement after compaction since history content changed.
	if compacted {
		cm.mu.Lock()
		cm.lastInputTokens = 0
		cm.historyLenAtMeasure = 0
		cm.mu.Unlock()
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
	cm.mu.Lock()
	cm.lastInputTokens = 0
	cm.historyLenAtMeasure = 0
	cm.mu.Unlock()

	// One profile snapshot covers every diagnostic below, for the same reason as
	// MaybeCompact: the readings describe one model's rules, not two half-read.
	prof, _, _ := cm.profileSnapshot()

	// Layer 1: Deterministic checkpoint.
	turnsBefore := len(*history)
	before := cm.estimateTokensFor(prof, *history)
	var checkpointed []schema.Turn
	*history, checkpointed = checkpointFold(*history, cm.PreserveRecentTurns, cm.metaFor(ctx), cm.resultToolName())
	after := cm.estimateTokensFor(prof, *history)
	emitFn(events.EventContextCompaction, events.ContextCompactionData{
		Layer:           "checkpoint",
		TurnsBefore:     turnsBefore,
		TurnsAfter:      len(*history),
		EstTokensBefore: before,
		EstTokensAfter:  after,
	})
	if len(*history) > 0 && (*history)[0].Kind == schema.TurnCheckpoint {
		cm.handleCompactionTurn(ctx, (*history)[0])
	}

	// Layer 2: LLM summarization (only if client is available).
	if cm.client != nil {
		turnsBefore = len(*history)
		before = cm.estimateTokensFor(prof, *history)
		// The summarizer returns the input unchanged for short or unsafe history,
		// which must not count a pre-existing summary as a newly generated one.
		canSummarize := attentionTransparentTurnCount(*history) > cm.PreserveRecentTurns &&
			safeCutoff(*history, attentionTransparentRecentCutoff(*history, cm.PreserveRecentTurns)) >= 0
		result, err := cm.summarizeFold(ctx, *history, cm.PreserveRecentTurns, instructions, checkpointed)
		if err != nil {
			emitFn(events.EventWarning, summarizerFailureWarning(err))
		} else {
			summarized = canSummarize && len(result) > 0 && result[0].Kind == schema.TurnSummary
			*history = result
			after := cm.estimateTokensFor(prof, *history)
			emitFn(events.EventContextCompaction, events.ContextCompactionData{
				Layer:           "summarize",
				TurnsBefore:     turnsBefore,
				TurnsAfter:      len(*history),
				EstTokensBefore: before,
				EstTokensAfter:  after,
			})
			if len(*history) > 0 && (*history)[0].Kind == schema.TurnSummary {
				cm.handleCompactionTurn(ctx, (*history)[0])
			}
		}
	}

	// Reset token measurement after compaction.
	cm.mu.Lock()
	cm.lastInputTokens = 0
	cm.historyLenAtMeasure = 0
	cm.mu.Unlock()
	return summarized
}

// --- Observation masking (Layer 1) ---

// maskObservations replaces tool result content in old turns with one-line summaries.
// Preserves: error results, communicate tool results, already-masked results,
// recent turns, and results where the summary would be longer than the original.
func maskObservations(history []schema.Turn, preserveRecent int, resultToolName string) {
	if len(history) == 0 {
		return
	}

	cutoff := attentionTransparentRecentCutoff(history, preserveRecent)
	if cutoff <= 0 {
		return
	}
	for i := range cutoff {
		t := &history[i]
		if t.Kind != schema.TurnTool && t.Kind != schema.TurnToolResults {
			continue
		}
		// Copy-on-write: the caller's history is typically a fold snapshot
		// whose Turn structs share Message.Content backing arrays and
		// *ToolResultData payloads with the live s.history. Writing through
		// them would leak a LOSING fold's masking into the live history, so
		// a turn that masks anything gets a fresh Content slice and fresh
		// ToolResultData; the shared originals are never written.
		var rebuilt []llm.ContentPart
		for j := range t.Message.Content {
			p := t.Message.Content[j]
			if p.Kind != llm.ContentToolResult || p.ToolResult == nil {
				continue
			}
			tr := p.ToolResult

			// Find the tool call arguments from the preceding assistant turn.
			args := findToolCallArgs(history[:i], tr.ToolCallID)

			if newContent, mask := maskToolResultContent(tr, resultToolName, summarizeToolResult, args); mask {
				if rebuilt == nil {
					rebuilt = append([]llm.ContentPart(nil), t.Message.Content...)
				}
				masked := *tr
				masked.Content = newContent
				rebuilt[j].ToolResult = &masked
			}
		}
		if rebuilt != nil {
			t.Message.Content = rebuilt
		}
	}
}

// maskToolResultContent decides whether a tool result's content should be
// replaced with a one-line summary, and computes that summary. It preserves
// error results, result-tool results, non-string content, and already-masked
// content ("[…]"); it also declines to mask when the summary would not be
// strictly shorter than the original. summarize is injected (summarizeToolResult)
// and args are the matching tool-call arguments. When mask is false newContent
// is empty and the caller leaves the result untouched.
func maskToolResultContent(tr *llm.ToolResultData, resultToolName string, summarize func(name string, content any, isError bool, args json.RawMessage) string, args json.RawMessage) (newContent string, mask bool) {
	// Never mask error results.
	if tr.IsError {
		return "", false
	}
	// Never mask result tool results.
	if tr.Name == resultToolName {
		return "", false
	}

	content, ok := tr.Content.(string)
	if !ok {
		return "", false
	}
	// Skip already-masked results (start with "[").
	if strings.HasPrefix(content, "[") && strings.HasSuffix(strings.TrimSpace(content), "]") {
		return "", false
	}

	summary := summarize(tr.Name, content, tr.IsError, args)
	// Skip masking if the summary is not shorter than the original.
	if len(summary) >= len(content) {
		return "", false
	}
	return summary, true
}

// findToolCallArgs looks backward from the tool result to find the matching
// assistant tool call and return its arguments.
func findToolCallArgs(history []schema.Turn, toolCallID string) json.RawMessage {
	for _, t := range slices.Backward(history) {
		if t.Kind != schema.TurnAssistant {
			continue
		}
		for _, p := range t.Message.Content {
			if p.Kind == llm.ContentToolCall && p.ToolCall != nil && p.ToolCall.ID == toolCallID {
				return p.ToolCall.Arguments
			}
		}
	}
	return nil
}

func parseCommunicateArgs(args json.RawMessage) (endTurn bool, message string) {
	var payload struct {
		Message string `json:"message"`
		EndTurn *bool  `json:"end_turn"`
		Output  *struct {
			Message string `json:"message"`
		} `json:"output"`
	}
	if len(args) == 0 || json.Unmarshal(args, &payload) != nil {
		return false, ""
	}
	message = strings.TrimSpace(payload.Message)
	if message == "" && payload.Output != nil {
		message = strings.TrimSpace(payload.Output.Message)
	}
	if payload.EndTurn == nil {
		return false, ""
	}
	return *payload.EndTurn, message
}

func communicateArgsFromHistory(history []schema.Turn, toolCallID string) (endTurn bool, message string) {
	return parseCommunicateArgs(findToolCallArgs(history, toolCallID))
}

// summarizeToolResult generates a one-line summary for a tool result.
func summarizeToolResult(toolName string, content any, isError bool, args json.RawMessage) string {
	contentStr := fmt.Sprint(content)
	var argsMap map[string]any
	if len(args) > 0 {
		_ = json.Unmarshal(args, &argsMap)
	}

	getArg := func(key string) string {
		if argsMap == nil {
			return ""
		}
		if v, ok := argsMap[key]; ok {
			return fmt.Sprint(v)
		}
		return ""
	}

	// Match the breaker's exact leading parkPrefix (agent/internal/tool/breaker.go).
	// Successful search data can start with the same bytes, so require its error flag.
	if isError && (toolName == "grep" || toolName == "glob" || toolName == "memory_search") &&
		strings.HasPrefix(contentStr, "evener did not execute this call: ") {
		return searchSummary(fmt.Sprintf("%s: %q → refused", toolName, getArg("pattern")), contentStr)
	}

	switch toolName {
	case "read_file":
		path := getArg("file_path")
		lines := countLines(contentStr)
		return fmt.Sprintf("[read_file: %s, %d lines]", path, lines)

	case "shell":
		cmd := getArg("command")
		if len(cmd) > 60 {
			cmd = cmd[:60] + "..."
		}
		exitCode := parseExitCode(contentStr)
		return fmt.Sprintf("[shell: %q → exit %s]", cmd, exitCode)

	case "grep", "memory_search":
		pattern := getArg("pattern")
		matches := searchresult.EntryCount(contentStr)
		return searchSummary(fmt.Sprintf("%s: %q → %d matches", toolName, pattern, matches), contentStr)

	case "glob":
		pattern := getArg("pattern")
		files := searchresult.EntryCount(contentStr)
		return searchSummary(fmt.Sprintf("glob: %q → %d files", pattern, files), contentStr)

	case "edit_file":
		path := getArg("file_path")
		return fmt.Sprintf("[edit_file: %s → OK]", path)

	case "apply_patch":
		return "[apply_patch → OK]"

	case "write_file":
		path := getArg("file_path")
		return fmt.Sprintf("[write_file: %s → OK]", path)

	case "web_fetch":
		url := getArg("url")
		return fmt.Sprintf("[web_fetch: %s → %d chars]", url, len(contentStr))

	case "delegate":
		jobID := extractJSONField(contentStr, "job_id")
		if jobID != "" {
			return fmt.Sprintf("[delegate: %s]", jobID)
		}
		return fmt.Sprintf("[delegate: %d chars]", len(contentStr))

	case "task_list":
		// Presence-based dispatch: derive the verb from whichever array the
		// call carried with at least one entry (add/update, either or both;
		// none = view). getArg stringifies, so an empty array would read as
		// present — inspect argsMap directly instead.
		verb := "view"
		hasAdd, hasUpdate := false, false
		if argsMap != nil {
			if arr, ok := argsMap["add"].([]any); ok && len(arr) > 0 {
				hasAdd = true
			}
			if arr, ok := argsMap["update"].([]any); ok && len(arr) > 0 {
				hasUpdate = true
			}
		}
		switch {
		case hasAdd && hasUpdate:
			verb = "add+update"
		case hasAdd:
			verb = "add"
		case hasUpdate:
			verb = "update"
		}
		tasks := countJSONArrayElements(contentStr)
		return fmt.Sprintf("[task_list: %s → %d tasks]", verb, tasks)

	case "use_skill":
		name := getArg("skill_name")
		return fmt.Sprintf("[use_skill: %s → %d chars]", name, len(contentStr))

	case "communicate":
		// Should never reach here (masked in caller), but be safe.
		return fmt.Sprintf("[communicate: %d chars]", len(contentStr))

	default:
		return fmt.Sprintf("[%s: %d chars]", toolName, len(contentStr))
	}
}

// --- Thinking clearing (Layer 2) ---

// clearThinking removes thinking text from old assistant turns, replacing it
// with a placeholder. Redacted thinking blocks are left untouched.
func clearThinking(history []schema.Turn, preserveRecent int) {
	if len(history) == 0 {
		return
	}

	cutoff := attentionTransparentRecentCutoff(history, preserveRecent)
	if cutoff <= 0 {
		return
	}

	for i := range cutoff {
		t := &history[i]
		if t.Kind != schema.TurnAssistant {
			continue
		}
		// Copy-on-write over the shared *ThinkingData payloads, for the same
		// losing-fold leak maskObservations documents.
		var rebuilt []llm.ContentPart
		for j := range t.Message.Content {
			p := t.Message.Content[j]
			if p.Kind != llm.ContentThinking || p.Thinking == nil {
				continue
			}
			// Skip redacted thinking blocks.
			if p.Thinking.Redacted {
				continue
			}
			// Skip already-cleared thinking.
			if strings.HasPrefix(p.Thinking.Text, "[thinking:") {
				continue
			}
			oldLen := len(p.Thinking.Text)
			if oldLen == 0 {
				continue
			}
			if rebuilt == nil {
				rebuilt = append([]llm.ContentPart(nil), t.Message.Content...)
			}
			cleared := *p.Thinking
			cleared.Text = fmt.Sprintf("[thinking: %d chars]", oldLen)
			rebuilt[j].Thinking = &cleared
		}
		if rebuilt != nil {
			t.Message.Content = rebuilt
		}
	}
}

// --- Deterministic checkpoint (Layer 3) ---

// maxCheckpointChars caps a checkpoint to avoid ballooning the context. The
// checkpoint either feeds into the LLM summarizer (which replaces it) or — if
// the summarizer is unavailable — becomes the sole history the main model sees.
// 60k chars ≈ 15k tokens: fits comfortably in the cheap model's context
// alongside the instruction prefix, and leaves room for preserved recent turns
// in the main model's context. The human partner's messages a model-written
// compaction turn carries share the same cap. The cap yields to those
// messages: none is ever dropped to meet it (fitCheckpointConversation).
const maxCheckpointChars = 60_000

// checkpoint replaces old history with a structured state snapshot.
// Returns a new history slice: [checkpoint_message, ...preserved_recent_turns].
//
// The checkpoint is a deterministic extraction — no LLM call. It captures user
// messages verbatim, agent responses, file/tool metadata, task list, and skills.
// User messages and agent responses are stored as an interleaved Markdown
// conversation for readable round-tripping across repeated compactions.
func checkpoint(history []schema.Turn, preserveRecent int, meta *CompactionMeta, resultToolName string) []schema.Turn {
	result, _ := checkpointFold(history, preserveRecent, meta, resultToolName)
	return result
}

// checkpointFold is checkpoint that also returns the turns the new checkpoint
// turn stands for (history[:cutoff]), or nil when nothing was folded. A
// summary completing the same fold reads the partner's messages from them.
func checkpointFold(history []schema.Turn, preserveRecent int, meta *CompactionMeta, resultToolName string) ([]schema.Turn, []schema.Turn) {
	if attentionTransparentTurnCount(history) <= preserveRecent {
		return history, nil
	}

	cutoff := foldCutoff(history, preserveRecent)
	if cutoff < 0 {
		return history, nil
	}

	data := collectCheckpointData(history, cutoff, resultToolName)
	checkpointTurn := schema.NewTurn(schema.TurnCheckpoint, llm.User(formatCheckpoint(data, meta, maxCheckpointChars)))

	result := make([]schema.Turn, 0, 1+preserveRecent)
	result = append(result, checkpointTurn)
	result = append(result, history[cutoff:]...)
	return result, history[:cutoff:cutoff]
}

// checkpointData is the raw material distilled from the to-be-compacted history
// prefix: which files were modified, which skills activated, per-tool call counts,
// the last few shell results, the conversation (user/agent turns), and the
// assistant's working notes.
type checkpointData struct {
	fileWrites       map[string]checkpointWriteStatus
	activatedSkills  map[string]bool
	toolCounts       map[string]int
	lastShellResults []string
	conversation     []checkpointConversationEntry
	workingNotes     []string
	// earlierSummaries are LLM summaries the checkpoint folds, kept whole: a
	// summary is the only record of the conversation it replaced, including
	// the permissions and holds it quotes.
	earlierSummaries []string
}

// collectCompaction carries a previous compaction turn's text into the new
// checkpoint so it survives repeated compactions. From a checkpoint it carries
// the conversation, working notes and earlier summaries it holds. From an LLM
// summary (summary is true) it carries the human partner's messages the summary
// turn holds verbatim, from its own conversation section only, and the rest of
// the summary whole as an earlier summary; the model's own text is not mined
// for conversation or working notes.
//
// This is the deterministic checkpoint's carry, which the default compaction
// paths use. Opt-in strategies that write their own checkpoints (session-log,
// OODA) do not carry earlier summaries yet; that gap is tracked in #4181.
func (d *checkpointData) collectCompaction(text string, summary bool) {
	if summary {
		if !carriesPartnerMessages(text) {
			d.earlierSummaries = append(d.earlierSummaries, text)
			return
		}
		d.conversation = append(d.conversation, extractMarkdownConversation(text)...)
		d.earlierSummaries = append(d.earlierSummaries, withoutMarkdownSection(text, checkpointConversationHeading))
		return
	}
	d.conversation = append(d.conversation, extractCheckpointConversation(text)...)
	d.workingNotes = append(d.workingNotes, extractCheckpointWorkingNotes(text)...)
	d.earlierSummaries = append(d.earlierSummaries, extractCheckpointEarlierSummaries(text)...)
}

// checkpointWriteStatus classifies a write tool call by the outcome of its
// paired tool result. The checkpoint may only report a file as modified when a
// result confirmed the write; an attempted write that failed or whose result is
// absent from the compacted prefix is kept distinct and never inferred as
// completed work.
type checkpointWriteStatus int

const (
	// writeUnconfirmed is the zero value, so a missing fileWrites entry — or any
	// unrecognized state — is never mistaken for confirmed completed work.
	writeUnconfirmed checkpointWriteStatus = iota // no result in the compacted prefix
	writeConfirmed                                // result present and not an error
	writeFailed                                   // result present and IsError
)

// recordWrite keeps a confirmed write from being downgraded by a later attempt
// of the same path, because the file was in fact modified. Otherwise the latest
// outcome wins.
func (d *checkpointData) recordWrite(path string, status checkpointWriteStatus) {
	if prev, ok := d.fileWrites[path]; ok && prev == writeConfirmed {
		return
	}
	d.fileWrites[path] = status
}

// collectCheckpointData walks history[:cutoff] and distills it into a
// checkpointData: modified files, tool counts, shell results, user messages,
// final agent responses, working notes, and activated skills.
func collectCheckpointData(history []schema.Turn, cutoff int, resultToolName string) checkpointData {
	data := checkpointData{
		fileWrites:      map[string]checkpointWriteStatus{},
		activatedSkills: map[string]bool{},
		toolCounts:      map[string]int{},
	}

	for i := range cutoff {
		t := history[i]
		switch t.Kind {
		case schema.TurnCheckpoint, schema.TurnSummary:
			data.collectCompaction(t.Message.Text(), t.Kind == schema.TurnSummary)

		case schema.TurnSteering:
			// Steering the human partner sent during a turn is their message
			// too, and so is a parent agent's steer into its delegate, which
			// carries its own attribution; daemon nudges are not.
			if text := t.Message.Text(); (isPartnerSteering(t) || isParentSteering(t)) && text != "" {
				data.conversation = append(data.conversation, checkpointConversationEntry{Role: "user", Text: text})
			}

		case schema.TurnUserInput:
			text := t.Message.Text()
			if text == "" {
				continue
			}
			// Old-format checkpoint/summary stored as TurnUserInput — carry it
			// just like a typed compaction turn.
			if strings.HasPrefix(text, "[CONTEXT CHECKPOINT]") || strings.HasPrefix(text, summaryMarkerStart) {
				data.collectCompaction(text, strings.HasPrefix(text, summaryMarkerStart))
				continue
			}
			data.conversation = append(data.conversation, checkpointConversationEntry{Role: "user", Text: text})

		case schema.TurnTool, schema.TurnToolResults:
			// Extract terminal communicate results (agent responses to the user).
			// Only an accepted result is a completed reply; a rejected result
			// (IsError) stays out of the conversation.
			for _, p := range t.Message.Content {
				if p.Kind != llm.ContentToolResult || p.ToolResult == nil {
					continue
				}
				if p.ToolResult.Name == resultToolName {
					endTurn, msg := communicateArgsFromHistory(history[:i+1], p.ToolResult.ToolCallID)
					if endTurn && msg != "" && !p.ToolResult.IsError {
						data.conversation = append(data.conversation, checkpointConversationEntry{Role: "agent", Text: msg})
					}
					continue
				}
			}

		case schema.TurnAssistant:
			// Capture assistant analytical text as working notes.
			if text := t.Message.Text(); len(text) > 50 {
				note := text
				if len(note) > 500 {
					note = note[:500] + "..."
				}
				data.workingNotes = append(data.workingNotes, note)
			}
			for _, p := range t.Message.Content {
				if p.Kind == llm.ContentWebSearch {
					data.toolCounts["web_search"]++
					continue
				}
				if p.Kind != llm.ContentToolCall || p.ToolCall == nil {
					continue
				}
				name := p.ToolCall.Name
				data.toolCounts[name]++

				var args map[string]any
				if len(p.ToolCall.Arguments) > 0 {
					_ = json.Unmarshal(p.ToolCall.Arguments, &args)
				}

				switch name {
				case "edit_file", "write_file":
					if path, ok := args["file_path"]; ok {
						data.recordWrite(fmt.Sprint(path), writeOutcome(history, i, cutoff, p.ToolCall.ID))
					}
				case "apply_patch":
					if patch, ok := args["patch"]; ok {
						status := writeOutcome(history, i, cutoff, p.ToolCall.ID)
						for line := range strings.SplitSeq(fmt.Sprint(patch), "\n") {
							line = strings.TrimSpace(line)
							for _, prefix := range []string{"*** Update File: ", "*** Add File: ", "*** Delete File: "} {
								if rest, ok := strings.CutPrefix(line, prefix); ok {
									data.recordWrite(rest, status)
								}
							}
						}
					}
				case "shell":
					if cmd, ok := args["command"]; ok {
						cmdStr := fmt.Sprint(cmd)
						if len(cmdStr) > 60 {
							cmdStr = cmdStr[:60] + "..."
						}
						exitCode := "?"
						for j := i + 1; j < cutoff; j++ {
							if history[j].Kind != schema.TurnTool && history[j].Kind != schema.TurnToolResults {
								continue
							}
							content := findToolResultByCallID(history[j], p.ToolCall.ID)
							if content != "" {
								exitCode = parseExitCode(content)
								break
							}
						}
						data.lastShellResults = append(data.lastShellResults, fmt.Sprintf("  %q → exit %s", cmdStr, exitCode))
					}
				case "use_skill":
					if sn, ok := args["skill_name"]; ok {
						data.activatedSkills[fmt.Sprint(sn)] = true
					}
				}
			}
		}
	}

	return data
}

// formatCheckpoint renders collected checkpoint data into the [CONTEXT CHECKPOINT]
// … [END CHECKPOINT] block: the fixed metadata sections (transcript pointer,
// modified files, tool counts, recent shell results, activated skills), then the
// conversation and working notes fit into maxChars by shedding the oldest working
// notes first, then the agent's own replies, and never the human partner's
// messages (fitCheckpointConversation).
func formatCheckpoint(data checkpointData, meta *CompactionMeta, maxChars int) string {
	// Build the fixed-size sections first (metadata), then fill remaining
	// budget with user messages and agent responses.
	var fixed strings.Builder

	// Transcript pointer — tells the model how to access full history via transcript tools.
	fixed.WriteString(meta.compactionPointer("checkpoint"))

	// Files modified: only writes whose paired result confirmed success. Failed
	// and result-missing attempts render distinctly, so the checkpoint never
	// reports an attempted write as completed work.
	modified := make([]string, 0, len(data.fileWrites))
	attempted := make([]string, 0, len(data.fileWrites))
	for f, status := range data.fileWrites {
		switch status {
		case writeConfirmed:
			modified = append(modified, f)
		case writeFailed:
			attempted = append(attempted, f+" (failed)")
		default:
			attempted = append(attempted, f+" (no result)")
		}
	}
	sort.Strings(modified)
	sort.Strings(attempted)
	if len(modified) > 0 {
		fmt.Fprintf(&fixed, "Files modified: %s\n", strings.Join(modified, ", "))
	}
	if len(attempted) > 0 {
		fmt.Fprintf(&fixed, "Files attempted but not applied: %s\n", strings.Join(attempted, ", "))
	}

	// Tool call counts.
	if total := sumCounts(data.toolCounts); total > 0 {
		fmt.Fprintf(&fixed, "Actions taken: %d tool calls (", total)
		toolNames := make([]string, 0, len(data.toolCounts))
		for name := range data.toolCounts {
			toolNames = append(toolNames, name)
		}
		sort.Strings(toolNames)
		for i, name := range toolNames {
			if i > 0 {
				fixed.WriteString(", ")
			}
			fmt.Fprintf(&fixed, "%d %s", data.toolCounts[name], name)
		}
		fixed.WriteString(")\n")
	}

	// Last 3 shell results.
	if len(data.lastShellResults) > 0 {
		start := 0
		if len(data.lastShellResults) > 3 {
			start = len(data.lastShellResults) - 3
		}
		fixed.WriteString("Last shell results:\n")
		for _, r := range data.lastShellResults[start:] {
			fixed.WriteString(r)
			fixed.WriteString("\n")
		}
	}

	// Activated skills.
	allSkills := map[string]bool{}
	for s := range data.activatedSkills {
		allSkills[s] = true
	}
	if meta != nil {
		for _, s := range meta.ActivatedSkills {
			allSkills[s] = true
		}
	}
	if len(allSkills) > 0 {
		skillNames := make([]string, 0, len(allSkills))
		for s := range allSkills {
			skillNames = append(skillNames, s)
		}
		sort.Strings(skillNames)
		fmt.Fprintf(&fixed, "Activated skills: %s\n", strings.Join(skillNames, ", "))
	}

	fixedStr := fixed.String()

	// Budget for variable content (conversation + working notes as Markdown).
	// Reserve space for [CONTEXT CHECKPOINT], [END CHECKPOINT], fixed sections,
	// earlier summaries, and the Markdown section headings. The variable
	// content always gets at least minVariableBudget.
	const minVariableBudget = 1000
	frame := len("[CONTEXT CHECKPOINT]\n") + len(fixedStr) + len("[END CHECKPOINT]\n") + 200

	// Earlier summaries get up to half the checkpoint, and never so much that
	// the variable content's floor would push it past maxChars. The newest are
	// kept, the last one trimmed from its tail: summaries put permissions and
	// holds first.
	summaryBudget := min(maxChars/2, maxChars-frame-minVariableBudget)
	summaries := data.earlierSummaries
	summariesMarkdown := renderCheckpointEarlierSummaries(summaries)
	for len(summaries) > 1 && len(summariesMarkdown) > summaryBudget {
		summaries = summaries[1:]
		summariesMarkdown = renderCheckpointEarlierSummaries(summaries)
	}
	if len(summaries) == 1 && len(summariesMarkdown) > summaryBudget {
		summary := truncateRendered(summaries[0], summaryBudget, func(text string) int {
			return len(renderCheckpointEarlierSummaries([]string{text}))
		})
		summariesMarkdown = renderCheckpointEarlierSummaries([]string{summary})
		if len(summariesMarkdown) > summaryBudget {
			// The fixed metadata leaves no room for even a trimmed summary.
			summariesMarkdown = ""
		}
	}

	variableBudget := max(maxChars-frame-len(summariesMarkdown), minVariableBudget)

	// Encode conversation and working notes as Markdown. Shed oldest notes
	// first — they may be shed entirely — then fit the conversation into what
	// remains (fitCheckpointConversation).
	conversation := cleanCheckpointConversation(data.conversation)
	workingNotes := data.workingNotes
	conversationMarkdown := renderCheckpointConversation(conversation)
	notesMarkdown := renderCheckpointWorkingNotes(workingNotes)
	for len(workingNotes) > 0 && len(conversationMarkdown)+len(notesMarkdown) > variableBudget {
		workingNotes = workingNotes[1:] // drop oldest note first
		notesMarkdown = renderCheckpointWorkingNotes(workingNotes)
	}
	conversationMarkdown = renderCheckpointConversation(fitCheckpointConversation(conversation, variableBudget-len(notesMarkdown), meta.hasTranscript()))

	// Assemble final checkpoint.
	var b strings.Builder
	b.WriteString("[CONTEXT CHECKPOINT]\n")
	// Earlier summaries come first so the summarizer, which reads a previous
	// compaction from its head, sees them before anything else.
	b.WriteString(summariesMarkdown)
	b.WriteString(fixedStr)
	if conversationMarkdown != "" {
		b.WriteString("\n")
		b.WriteString(conversationMarkdown)
	}
	if notesMarkdown != "" {
		b.WriteString("\n")
		b.WriteString(notesMarkdown)
	}
	b.WriteString("[END CHECKPOINT]\n")
	return b.String()
}

// partnerMessageEdgeChars is how much of each end of a human partner's
// message a middle trim keeps. A message no longer than both edges together is
// a short instruction and is never trimmed.
const partnerMessageEdgeChars = 1000

// fitCheckpointConversation fits conversation entries into budget without
// ever dropping the human partner's messages (#4281). It sheds the agent's own
// replies first, oldest first. If the partner's messages alone still exceed
// the budget, it trims the middle of the longest ones (pasted logs, say), each
// at most once and never below partnerMessageEdgeChars at each end, with a
// note saying where the full text is. Short instructions are never touched,
// nor is a message trimming would not shorten, nor any message when the
// session keeps no transcript to recover it from (recoverable false); when
// what is left still exceeds the budget, the budget yields.
//
// conversation must already be cleaned (cleanCheckpointConversation), so the
// rendered slice and the entries line up.
func fitCheckpointConversation(conversation []checkpointConversationEntry, budget int, recoverable bool) []checkpointConversationEntry {
	// The rendering is the section heading plus one block per entry, so its
	// length is the sum of the entries' block lengths and the heading's.
	blockLen := func(entry checkpointConversationEntry) int {
		return len(renderCheckpointConversation([]checkpointConversationEntry{entry})) - len(checkpointConversationHeading+"\n\n")
	}
	rendered := len(renderCheckpointConversation(conversation))
	// Shed the agent's own replies, oldest first, until the rest fits.
	kept := make([]checkpointConversationEntry, 0, len(conversation))
	for _, entry := range conversation {
		if rendered > budget && entry.Role != "user" {
			rendered -= blockLen(entry)
			continue
		}
		kept = append(kept, entry)
	}
	if rendered <= budget || !recoverable {
		return kept
	}
	// Only the partner's messages are left: trim the longest first, each once.
	long := make([]int, 0, len(kept))
	for i, entry := range kept {
		if len(entry.Text) > 2*partnerMessageEdgeChars {
			long = append(long, i)
		}
	}
	slices.SortStableFunc(long, func(a, b int) int { return len(kept[b].Text) - len(kept[a].Text) })
	for _, i := range long {
		if rendered <= budget {
			break
		}
		text := kept[i].Text
		// Keep what the budget allows, leaving room for the note.
		keep := max(2*partnerMessageEdgeChars, len(text)-(rendered-budget)-len(trimmedMessageNote(len(text))))
		trimmed := middleTrim(text, keep/2)
		if len(trimmed) >= len(text) {
			continue // the note would cost what the trim saves
		}
		before := blockLen(kept[i])
		kept[i].Text = trimmed
		rendered += blockLen(kept[i]) - before
	}
	return kept
}

// middleTrim keeps edge characters at each end of text, cut on rune
// boundaries, and puts the trimmed-message note between them.
func middleTrim(text string, edge int) string {
	if 2*edge >= len(text) {
		return text
	}
	head := runetrim.Cut(text, edge)
	tail := string(runetrim.TrimLeadingPartial([]byte(text[len(text)-edge:])))
	return head + trimmedMessageNote(len(text)-len(head)-len(tail)) + tail
}

// partnerMessagesMarkdown renders the human partner's messages from the turns
// a compaction folds (history[:cutoff]), verbatim, under the checkpoint's cap
// and shedding rules. A model-written compaction turn carries them beside its
// own text, so its permissions and holds stay in front of the model whatever
// the model wrote (#4173). It reads them through the checkpoint's own
// collector, so they come from raw user turns and from previous compaction
// turns alike.
//
// "Verbatim" means each message's text unchanged apart from surrounding
// whitespace, which cleanCheckpointConversation trims and the checkpoint's
// own extraction (parseMarkdownBlocks) trims again on every round trip.
// Fitting follows the checkpoint's rule (fitCheckpointConversation): no
// partner message is ever dropped; past the tighter of maxCheckpointChars and
// the ceiling the longest are middle-trimmed with a pointer to the transcript,
// and short ones are never touched.
//
// Only the partner's messages are carried. An earlier summary a folded
// checkpoint holds is input to the new summary, which supersedes it; carrying
// every earlier summary forward as well would grow each summary turn with
// every compaction. Checkpoints written by the opt-in session-log strategy
// keep no conversation section, so nothing is recovered from them here
// (#4181).
//
// Past ceiling characters (partnerCarryCeiling; 0 means none) the oldest
// messages are left out, newest kept, and returned as summarized: the
// summarizer is given each of them to summarize, quoting any permission or
// hold per the summary prompt, and the turn reminds the model where to read
// them verbatim. The newest message is held to the ceiling too: when it alone
// exceeds it, its middle is trimmed (transcript permitting), from the message
// as written, so the note counts what it lost rather than what an earlier trim
// had already taken.
// The ceiling applies with or without a kept transcript: it keeps the session
// inside the model's window, and the left-out messages are still summarized,
// every one (condensePartnerMessages), as the #4309 ruling chose. Without a
// transcript the reminder only says they were summarized.
func partnerMessagesMarkdown(history []schema.Turn, cutoff int, resultToolName string, recoverable bool, ceiling int) (markdown string, summarized []checkpointConversationEntry) {
	var partner []checkpointConversationEntry
	for _, entry := range cleanCheckpointConversation(collectCheckpointData(history, cutoff, resultToolName).conversation) {
		if entry.Role == "user" {
			partner = append(partner, entry)
		}
	}
	// The carry is chosen from the messages as written, newest first, and
	// only then fitted; the messages left to the summary go to it as written.
	kept := partner
	if ceiling > 0 {
		heading := len(checkpointConversationHeading + "\n\n")
		size, n := heading, 0
		for _, entry := range slices.Backward(partner) {
			block := len(renderCheckpointConversation([]checkpointConversationEntry{entry})) - heading
			if n > 0 && size+block > ceiling {
				break
			}
			size += block
			n++
		}
		if size > ceiling && !recoverable {
			// The newest alone overflows and cannot be trimmed: summarize it
			// with the rest.
			n = 0
		}
		summarized = partner[:len(partner)-n]
		kept = partner[len(partner)-n:]
	}
	// The carry is fitted to the tighter of the checkpoint's cap and the
	// ceiling, so a message both bounds would cut is trimmed once, by the bound
	// that binds, and the note it gets counts what the message as written lost.
	budget := maxCheckpointChars
	if ceiling > 0 {
		budget = min(budget, ceiling)
	}
	// The newest message as written, for the ceiling's trim below.
	newestAsWritten := ""
	if len(kept) > 0 {
		newestAsWritten = kept[len(kept)-1].Text
	}
	partner = fitCheckpointConversation(kept, budget, recoverable)
	if len(partner) == 0 {
		return "", summarized
	}
	if size := len(renderCheckpointConversation(partner)); ceiling > 0 && size > ceiling {
		// Only the newest can be over, and only when the fit stopped at its
		// floor for a message's kept edges rather than at the ceiling: trim its
		// middle to fit, note included. The cut comes from the message as
		// written, never from what the fit left of it, and the note's length is
		// the one that count has: cutting an already-cut text would cut the
		// middle of the note it wrote and leave a count that understates what
		// was removed.
		newest := &partner[len(partner)-1]
		edge := (len(newest.Text) - (size - ceiling) - len(trimmedMessageNote(len(newestAsWritten)))) / 2
		if trimmed := middleTrim(newestAsWritten, max(edge, 1)); len(trimmed) < len(newest.Text) {
			newest.Text = trimmed
		}
	}
	return renderCheckpointConversation(partner), summarized
}

// minSummarizingWindow is the smallest context window compaction summarizes
// on. A 50,000-token window leaves a summarizer request about 36,000 tokens
// of input (a quarter of the window for the reply, then the client's safety
// reserve), well above what a request carries: at most maxHistoryChars of
// history (~20,000 tokens) plus the summary instructions. Smaller windows are
// not worth sizing requests for; on them the checkpoint stands (#4321).
const minSummarizingWindow = 50_000

// summarizingWindowUsable reports whether every route a summary may take
// accepts at least the input a minSummarizingWindow-token window allows. Below
// that the deterministic checkpoint, which keeps the partner's messages and
// an earlier summary, stands instead of a summary. Unknown windows impose
// nothing.
func summarizingWindowUsable(prof *provider.Profile) bool {
	if prof == nil {
		return true
	}
	least := summarizerInputLimit(minSummarizingWindow, 0)
	// The session model and the configured cheap model it falls back from.
	for _, route := range [][2]int{
		{prof.ContextWindowSize(), prof.MaxInputTokens()},
		{prof.CheapContextWindowSize(), prof.CheapMaxInputTokens()},
	} {
		if input := summarizerInputLimit(route[0], route[1]); input > 0 && input < least {
			return false
		}
	}
	return true
}

// summarizerInputLimit is how many tokens of input a summarizer request may
// carry on a model with this window and input cap (0 for unknown): what the
// window leaves after a quarter for the reply, held to the input cap, less the
// safety reserve the client keeps on top of the input. It is 0 when neither
// is known.
func summarizerInputLimit(window, maxInput int) int {
	limit := 0
	if window > 0 {
		limit = window - window/4
	}
	if maxInput > 0 && (limit == 0 || maxInput < limit) {
		limit = maxInput
	}
	if limit == 0 {
		return 0
	}
	smallest := window
	if maxInput > 0 && (smallest == 0 || maxInput < smallest) {
		smallest = maxInput
	}
	return max(limit-llm.SafetyReserveTokens(smallest), 1)
}

// condensePartnerMessages fits the partner's messages a summary will not quote
// (each a line of text) into budget characters for the summarizer's input.
// When they overflow it, they are summarized in batches of at most budget
// characters, each batch summary quoting their permissions and holds, and the
// batch summaries are condensed the same way until they fit, so every message
// reaches a summarizer however many there are. If they will not shrink to fit,
// it fails rather than cut any short.
func (cm *Manager) condensePartnerMessages(ctx context.Context, prof *provider.Profile, lines []string, budget int) (string, error) {
	const maxRounds = 4 // each round shrinks the text; four reach any realistic volume
	var route summarizationRoute
	var role string
	for round := 0; ; round++ {
		total := 0
		for _, line := range lines {
			total += len(line)
		}
		if total <= budget {
			return strings.Join(lines, ""), nil
		}
		if round == maxRounds {
			break
		}
		var summaries []string
		for start := 0; start < len(lines); {
			end, size := start, 0
			for end < len(lines) && (end == start || size+len(lines[end]) <= budget) {
				size += len(lines[end])
				end++
			}
			resp, err := cm.completeSummarization(ctx, prof, partnerBatchPrompt+strings.Join(lines[start:end], ""), func(reply llm.Response) error {
				if strings.TrimSpace(reply.Text()) == "" {
					return errSummaryEmptyBatch
				}
				return checkNotCutOff(reply)
			})
			if err != nil {
				return "", err
			}
			route, role = resp.route, resp.role
			// A long summary is split like a long message, so the next round's
			// batches stay within budget.
			parts := splitMessage(strings.TrimSpace(resp.Text()), budget/3, 400)
			for i, part := range parts {
				label := "Summary of earlier messages: "
				if len(parts) > 1 {
					label = fmt.Sprintf("Summary of earlier messages (part %d of %d): ", i+1, len(parts))
				}
				summaries = append(summaries, label+part+"\n")
			}
			start = end
		}
		lines = summaries
	}
	// Summaries that will not shrink to fit are never cut short: failing here
	// fails the summary, and the checkpoint, which keeps every partner
	// message, stands.
	return "", summaryFailure(errSummaryUncondensable, route, role)
}

// splitMessage splits text into parts of at most size bytes, each cut at the
// last paragraph, line or sentence break before the limit when there is one,
// else on a rune boundary, and each repeating the last overlap bytes of the
// part before it, so an instruction that crosses a cut is whole in one part.
func splitMessage(text string, size, overlap int) []string {
	var parts []string
	for len(text) > size {
		part := runetrim.Cut(text, size)
		if part == "" {
			part = text[:size] // not valid UTF-8 at the cut; keep every byte
		}
		for _, br := range []string{"\n\n", "\n", ". "} {
			if i := strings.LastIndex(part, br); i > size/2 {
				part = part[:i+len(br)]
				break
			}
		}
		parts = append(parts, part)
		next := max(len(part)-overlap, 1)
		for next < len(part) && !utf8.RuneStart(text[next]) {
			next++
		}
		text = text[next:]
	}
	return append(parts, text)
}

// partnerBatchPrompt asks the summarizer to condense a batch of the human
// partner's messages that a compaction will not quote.
const partnerBatchPrompt = `You are condensing messages from your human partner that a context compaction will not quote. Summarize what they asked for and told you, as briefly as keeps it usable. ` + partnerAuthorityRule + `

`

// partnerCarryCeiling bounds the partner's messages a model-written
// compaction turn carries verbatim to a quarter of the model's context window,
// in characters at the char/4 token estimate the context manager uses
// elsewhere: a quarter of W tokens is W characters. Compaction starts at 80% of
// the window; a carry of at most a quarter leaves the rest for the system
// prompt and tools, the model's own summary, the preserved recent turns and the
// work until the next compaction, so the carry alone can never push a compacted
// context back over the threshold. It is 0, no ceiling, only when there is no
// profile to read a window from; an unknown window is assumed like the rest of
// the fold (issue #4564), so the assumption bounds the carry too.
func partnerCarryCeiling(prof *provider.Profile) int {
	cw, _ := effectiveWindowOf(prof)
	return cw
}

// partnerCarry renders the partner's messages a model-written compaction turn
// carries (from history[:cutoff]), held to ceiling (0 for none), the reminder
// its opening adds when some are left to the summary instead, and those
// messages, which the summarizer must be given.
func (cm *Manager) partnerCarry(ctx context.Context, history []schema.Turn, cutoff, ceiling int) (reminder, carry string, summarized []checkpointConversationEntry) {
	meta := cm.metaFor(ctx)
	carry, summarized = partnerMessagesMarkdown(history, cutoff, cm.resultToolName(), meta.hasTranscript(), ceiling)
	if len(summarized) > 0 {
		reminder = partnerCarryReminder(len(summarized), meta)
	}
	return reminder, carry, summarized
}

// foldCutoff is where a compaction of history that keeps preserveRecent
// recent turns cuts it: history[:cutoff] is folded. It is -1 when no safe cut
// exists. The checkpoint and the summarizer both cut with it.
func foldCutoff(history []schema.Turn, preserveRecent int) int {
	return safeCutoff(history, attentionTransparentRecentCutoff(history, preserveRecent))
}

// truncateRendered shortens text (via truncate, which appends an ellipsis) until
// its rendered form fits in available bytes. Truncating never increases the
// rendered length, so the largest fitting prefix is found by binary search.
func truncateRendered(text string, available int, render func(string) int) string {
	if render(text) <= available {
		return text
	}
	lo, hi := 0, len(text)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if render(truncate(text, mid)) <= available {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return truncate(text, lo)
}

// findToolResultByCallID finds a tool result in a TurnTool by its ToolCallID
// and returns the string content, or "" if not found.
func findToolResultByCallID(t schema.Turn, toolCallID string) string {
	for _, p := range t.Message.Content {
		if p.Kind == llm.ContentToolResult && p.ToolResult != nil && p.ToolResult.ToolCallID == toolCallID {
			if s, ok := p.ToolResult.Content.(string); ok {
				return s
			}
		}
	}
	return ""
}

// writeOutcome classifies the write tool call at history[callIndex] by its
// paired result. A result shed from the compacted prefix leaves the outcome
// unconfirmed rather than inferring success.
func writeOutcome(history []schema.Turn, callIndex, cutoff int, toolCallID string) checkpointWriteStatus {
	result := findToolResultData(history, callIndex+1, cutoff, toolCallID)
	switch {
	case result == nil:
		return writeUnconfirmed
	case result.IsError:
		return writeFailed
	default:
		return writeConfirmed
	}
}

// findToolResultData scans history[from:cutoff] for the tool result part that
// answers toolCallID and returns it, or nil when the prefix holds no result.
func findToolResultData(history []schema.Turn, from, cutoff int, toolCallID string) *llm.ToolResultData {
	for j := from; j < cutoff; j++ {
		if history[j].Kind != schema.TurnTool && history[j].Kind != schema.TurnToolResults {
			continue
		}
		for _, p := range history[j].Message.Content {
			if p.Kind == llm.ContentToolResult && p.ToolResult != nil && p.ToolResult.ToolCallID == toolCallID {
				return p.ToolResult
			}
		}
	}
	return nil
}

func sumCounts(m map[string]int) int {
	total := 0
	for _, v := range m {
		total += v
	}
	return total
}

// --- LLM summarization (Layer 4) ---

// summarizationRoute is one (provider, model) the summarizer tries in order: the
// configured cheap model on its (possibly cross-provider) cheap provider first,
// then the active model on the main provider as a fallback.
type summarizationRoute struct {
	provider string
	model    string
}

func summarizationModels(profile *provider.Profile) []summarizationRoute {
	if profile == nil {
		return nil
	}
	active := strings.TrimSpace(profile.Model())
	configured := strings.TrimSpace(profile.ConfiguredCheapModel())

	var routes []summarizationRoute
	if configured != "" {
		routes = append(routes, summarizationRoute{provider: profile.CheapProvider(), model: configured})
	}
	if active != "" {
		activeRoute := summarizationRoute{provider: profile.ID(), model: active}
		// Skip a fallback identical to the cheap route (same provider + model).
		if len(routes) == 0 || routes[0] != activeRoute {
			routes = append(routes, activeRoute)
		}
	}
	return routes
}

func shouldFallbackSummarizationModel(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if llm.Classify(err) != llm.ErrorClassPermanent {
		return false
	}
	switch llm.Kind(err) {
	case llm.KindInvalidRequest, llm.KindNotFound, llm.KindAccessDenied:
		return true
	default:
		return false
	}
}

// summarizationResult retains the selected route for validation that spans
// multiple otherwise successful model calls, such as partner condensation.
type summarizationResult struct {
	llm.Response
	route summarizationRoute
	role  string
}

// completeSummarization runs prompt on the summarizer's routes. The cheap
// caller owns the first rung — it picks the configured cheap model, or the
// session model when none is configured, and handles a refused cheap model
// itself. This adds the second rung: summarization treats a wider class of
// failures than a model refusal as worth another model, and retries on the
// session model — but only when the cheap caller has not already run it.
//
// accept, when non-nil, judges a reply; a rejected reply also falls through to
// the session model, and is returned as an error if that reply is rejected
// too. A rejected reply was still a successful API call, so the attempt group
// settles on the API error alone.
func (cm *Manager) completeSummarization(ctx context.Context, profile *provider.Profile, prompt string, accept func(llm.Response) error) (summarizationResult, error) {
	routes := summarizationModels(profile)
	if len(routes) == 0 {
		return summarizationResult{}, errSummaryMissingModel
	}
	judge := func(resp llm.Response) error {
		if accept == nil {
			return nil
		}
		return accept(resp)
	}
	req := llm.Request{Messages: []llm.Message{llm.User(prompt)}, AdapterTimeout: cm.AdapterTimeout}
	callCtx, attemptGroupScope := llm.BeginAPIAttemptGroupScope(ctx)
	resp, ranSessionModel, apiErr := cm.cheap.CompleteConfigured(callCtx, profile, req)
	var rejected error
	if apiErr == nil {
		rejected = judge(resp)
	}
	route, role := routes[0], "cheap"
	if ranSessionModel {
		route, role = summarizationRoute{provider: profile.ID(), model: profile.Model()}, "session"
	}
	describe := func() *summaryFailureError {
		if apiErr != nil {
			return summaryFailure(apiErr, route, role)
		}
		return summaryFailure(rejected, route, role)
	}
	failure := describe()
	if !ranSessionModel && len(routes) > 1 && (rejected != nil || apiErr != nil && shouldFallbackSummarizationModel(ctx, apiErr)) {
		// More than one route means the second is the session model, distinct
		// from the configured cheap one; the caller ran only the cheap one.
		sessionRoute := routes[len(routes)-1]
		req.Provider, req.Model = sessionRoute.provider, sessionRoute.model
		resp, apiErr = cm.client.Complete(callCtx, req)
		rejected = nil
		if apiErr == nil {
			rejected = judge(resp)
		}
		route, role = sessionRoute, "session"
		previous := failure
		failure = describe()
		if failure != nil && previous != nil {
			failure.detail.Previous = previous.detail.Previous
			failure.detail.Previous = append(failure.detail.Previous, previous.detail.Terminal)
			if len(failure.detail.Previous) > 2 {
				failure.detail.Previous = failure.detail.Previous[len(failure.detail.Previous)-2:]
			}
		}
	}
	attemptGroupScope.SettleResult(apiErr)
	result := summarizationResult{Response: resp, route: route, role: role}
	if failure != nil {
		return result, failure
	}
	return result, nil
}

// partnerAuthorityRule keeps a summary from adding or dropping the human
// partner's permissions, approvals, holds and stops. The continuing agent
// treats the summary as the record of what it may do, so a paraphrase that
// hardens into a new hold, or drops a standing permission, changes its
// behavior (#4173). Both summary prompts carry it, and caller instructions
// cannot override it. Both put these quotes first in the summary, so when a
// re-compaction caps an oversized previous summary to its head, the quotes are
// what survives.
const partnerAuthorityRule = `Quote every permission, approval, hold or stop your human partner gave, word for word from their "User:" message, and say which of their messages it came from. Carry forward only what the conversation contains: never add one it lacks, never turn a question, a suggestion or your own caution into one, and never drop one that is still in force. An earlier compaction counts only for what it quotes. If there are none, say "None."`

// permissionsSection is the default summary's leading section, where the
// summary quotes the human partner's permissions and holds.
const permissionsSection = "Permissions and Holds"

// defaultSummaryPrefix is the instruction block used when no caller instructions
// are provided. It mandates eight specific sections, permissions and holds
// first, and directs the LLM to err on the side of verbosity.
var defaultSummaryPrefix = `You are performing a CONTEXT CHECKPOINT COMPACTION. This session is being continued from a previous conversation that ran out of context. Create a detailed handoff summary that another instance of yourself will use to seamlessly continue the work.

Your summary MUST include ALL of the following sections, in this order:

## ` + permissionsSection + `
` + partnerAuthorityRule + `

## Conversation Timeline
Reproduce user messages and agent replies in chronological, interleaved order. Preserve user messages verbatim. Summarize agent replies only when needed for brevity, but keep commitments, decisions, and final answers clear.

## Progress
What has been accomplished so far. Be specific about:
- Files created or modified (full paths)
- Specific changes made to each file
- Tests written or run and their results
- Commands executed and their outcomes

## Key Decisions
Important decisions made during the session and why. Include:
- Architecture or design choices
- Trade-offs considered
- User preferences or constraints discovered

## Current State
Precisely what was being worked on when context ran out:
- What file was being edited
- What problem was being debugged
- What test was failing

## Pending Work
Clear, actionable next steps that remain. Be specific enough that the next instance can immediately start working.

## Analytical Findings
Specific technical discoveries made during the session:
- What algorithms, approaches, or parameter values were found to work
- What debugging insights were gained (root causes, validated hypotheses)
- What code patterns, data structures, or API behaviors were discovered
- Include specific values, numbers, and names — not vague summaries

## Critical Context
Any data, file paths, variable names, error messages, API details, or other specific information needed to continue. Focus on information that CANNOT be re-derived from reading the codebase.

Be thorough and structured. Err on the side of including too much rather than too little — lost context is expensive, extra tokens are cheap.

`

// summarySections are the section names the default summary prompt requires,
// read from the prompt itself so the reply check and the prompt cannot drift.
var summarySections = promptSectionNames(defaultSummaryPrefix)

// promptSectionNames returns the names of a prompt's "## " headings, in order.
func promptSectionNames(prompt string) []string {
	var names []string
	for line := range strings.Lines(prompt) {
		if name, ok := strings.CutPrefix(strings.TrimSpace(line), "## "); ok {
			names = append(names, name)
		}
	}
	return names
}

// checkNotCutOff rejects a summarization reply the model cut off at its output
// limit: it may have dropped what only it records, a hold among the partner
// messages a carry left out, now or in an earlier summary it replaces. Like
// any rejected reply it falls back to the session model, and if that is cut
// off too the summary fails and the checkpoint stands.
func checkNotCutOff(reply llm.Response) error {
	if reply.Finish.Reason == llm.FinishReasonLength {
		return errSummaryOutputLimit
	}
	return nil
}

// checkSummaryReply rejects a summarization reply that cannot stand in for the
// history it folds: an empty one, or, under the default prompt, one with none
// of the required sections with content under it. Weak models sometimes
// answer the prompt in character ("I'll read the plan, then...") instead of
// summarizing (#3978). One filled section is enough, so a summary that drops
// some sections still keeps what it has. Caller instructions replace the
// sections, so under them only emptiness is checked.
//
// The permissions-and-holds section is not required either. Its presence
// would not show the quotes are faithful or complete (a reply can say "None."
// and drop them), and rejecting a summary for lacking it would discard
// everything else the summary holds (TestSummarizeWithLLM_StoresReplyWithRequiredSection
// pins that one filled section is accepted). The guarantee that the human
// partner's permissions and holds survive compaction does not rest on this
// check or on the model: by ruling on #4173, the summary turn carries the
// partner's messages verbatim beside the reply (partnerMessagesMarkdown).
func checkSummaryReply(reply, instructions string) error {
	if strings.TrimSpace(reply) == "" {
		return errSummaryEmptyReply
	}
	if instructions != "" {
		return nil
	}
	// Once a section heading appears, any later line is content — subheadings,
	// bold labels and "#3978" lines included — except another section heading.
	// The permissions section restates the human partner's own words, so
	// content there alone carries none of the folded work: a reply that is
	// only "## Permissions and Holds\nNone." is no summary.
	// A heading inside a fenced block is quoted text, not a section.
	// This reads the reply as the model wrote it and edits nothing: the framing a
	// model may echo back around its own text stays in the stored turn.
	inSection := false
	var fence fenceTracker
	// The lines are read as written: the reply's own indentation is Markdown's,
	// and trimming it would turn an indented block into a fence.
	for raw := range strings.Lines(reply) {
		line := strings.TrimSpace(raw)
		// The tracker reads the line as written: a delimiter indented by four
		// spaces is code inside the block, not a delimiter.
		if fence.step(raw) {
			continue
		}
		// A model sometimes echoes the framing around its own heading. Read through
		// the markers at the line's edges so the heading still counts; a marker
		// inside the line's own text is that text. An indented line is code: it is
		// content under a section, never one.
		if section, rest, ok := summarySectionHeading(withoutEdgeFraming(line)); ok && !fence.inside() && !indentedCode(raw) {
			inSection = !strings.EqualFold(section, permissionsSection)
			// The heading's own text counts only if it is text: the edge markers
			// are read through above, but markup can hold one inside it
			// ("**Progress: [END SUMMARY]**"), and a heading with nothing but
			// framing under it has summarized nothing.
			if inSection && rest != "" && !markerOnly(rest) {
				return nil
			}
			continue
		}
		// A line of framing markers is not content: quoting the framing is not
		// summarizing the folded history, whether the markers sit on their own line
		// or inside a fenced block.
		if inSection && line != "" && !markerOnly(line) {
			return nil
		}
	}
	return errSummaryMissingSection
}

// summarySectionHeading reports whether line is a heading naming one of the
// summary sections, and returns the section name as written and any text after
// it on the same line:
// "## Progress", "### progress", "**Progress**", "## Progress: fixed it",
// "**Progress:** fixed it". It needs heading markup: a bare "Progress" is
// prose, and "#3978" (no space after the #) is not a heading.
func summarySectionHeading(line string) (section, rest string, ok bool) {
	body := line
	switch {
	case strings.HasPrefix(line, "#"):
		body = strings.TrimLeft(line, "#")
		if !strings.HasPrefix(body, " ") {
			return "", "", false
		}
	case !strings.HasPrefix(line, "**"):
		return "", "", false
	}
	body = strings.ReplaceAll(body, "**", "")
	name, rest, _ := strings.Cut(body, ":")
	name = strings.TrimSpace(name)
	if !slices.ContainsFunc(summarySections, func(section string) bool { return strings.EqualFold(name, section) }) {
		return "", "", false
	}
	return name, strings.TrimSpace(rest), true
}

// buildSummaryPrompt constructs the full LLM prompt for context compaction.
// When instructions are non-empty the prompt is instruction-led: the
// mandatory-sections block is replaced by the caller's directive.
// When instructions are empty the default prompt is used unchanged.
func buildSummaryPrompt(historyText, instructions string) string {
	if instructions != "" {
		return `You are performing a CONTEXT CHECKPOINT COMPACTION for an agent continuing its own work.

## CALLER INSTRUCTIONS (these take precedence over the general guidance, never over your human partner's permissions and holds)
` + instructions + `

Follow the caller instructions above when deciding what to preserve verbatim and what to drop or condense. Where they conflict with the general guidance below, the caller instructions win, except over the rule on your human partner's permissions and holds. Still produce a coherent handoff: keep decisions, current state, and actionable next steps. Do not invent content.

` + partnerAuthorityRule + ` Put this first in your summary; the caller instructions do not override it.

` + historyText
	}
	return defaultSummaryPrefix + historyText
}

// isPartnerSteering reports whether a steering turn is the human partner's own
// message, sent during a turn, rather than a daemon nudge. The checkpoint's
// collector and the summarizer's "User:" label both use it, so they agree on
// whose words a steer is.
func isPartnerSteering(t schema.Turn) bool {
	return t.Kind == schema.TurnSteering && t.SteeringSource == events.SteeringSourceUser
}

// isParentSteering reports whether a steering turn is the delegate's parent
// agent speaking into the running turn (delegate_send). Compaction keeps it
// like the partner's own messages, since it is the instruction the delegate
// works from, but it is not the human partner's: only isPartnerSteering
// answers for them.
func isParentSteering(t schema.Turn) bool {
	return t.Kind == schema.TurnSteering && t.SteeringSource == events.SteeringSourceParent
}

// noteElicitationPrompt asks the model, at compaction time, to enumerate the
// details that must survive verbatim — explicitly targeting the kinds a lossy
// summary erodes (opaque tokens, exact numbers, hashes, names). Used by the
// forced-note-at-compaction mechanism: the reply is pinned as the note before the
// compaction folds the history, so those details are re-stamped verbatim rather
// than decaying through successive summaries.
const noteElicitationPrompt = `Your conversation context is about to be COMPACTED — most of it will be replaced by a lossy summary. Before that happens, list everything that MUST survive VERBATIM for the work to continue.

Focus especially on details a summary tends to drop or paraphrase: exact tokens, IDs, hashes, version tags, exact numbers and thresholds, file/column/endpoint names, and specific decisions. Preserve exact strings — do not abbreviate or round.

Output a concise bullet list of the must-keep items, nothing else.`

// HasClient reports whether an LLM client is available (note elicitation and LLM
// summarization both require one).
func (cm *Manager) HasClient() bool { return cm.client != nil }

// noteElicitChars caps the history rendered into the elicitation prompt.
const noteElicitChars = 80_000

// loadedSkillsElicitSection lists the session's successfully loaded skills and
// the selection-block protocol for note elicitation, so the model can say which
// skills to reload after compaction. Empty when nothing is loaded — with no
// inventory there is nothing a selection could authorize.
func loadedSkillsElicitSection(loaded []schema.SkillInventorySummary) string {
	if len(loaded) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n--- SKILLS LOADED IN THIS SESSION ---\n")
	for _, s := range loaded {
		fmt.Fprintf(&b, "- %s — %s\n", s.Name, s.Description)
	}
	b.WriteString("\nCompaction may drop these skills' instruction bodies. After the bullet list, " +
		"emit exactly one <skill-reload-selection> block containing a JSON object with a " +
		"\"reload_skills\" array of the exact names to reload after compaction (use [] to reload none).")
	return b.String()
}

// ElicitNote asks the model to enumerate the must-survive-verbatim details from
// the given history, for pinning as a note before a compaction (Variant B of the
// forced-note-at-compaction mechanism). The caller passes the prefix the
// compaction is about to fold (the recent preserved turns survive verbatim and
// need no rescuing). Returns the model's bullet list. Falls back across the
// configured summarization models on transient errors, like the summarizer.
// loaded carries the session's successfully loaded skills so the model can also
// choose which to reload after compaction.
func (cm *Manager) ElicitNote(ctx context.Context, history []schema.Turn, loaded []schema.SkillInventorySummary) (string, error) {
	if cm.client == nil {
		return "", errors.New("note elicitation requires an LLM client")
	}
	prof := cm.currentProfile()
	if len(summarizationModels(prof)) == 0 {
		return "", errors.New("no model available for note elicitation")
	}
	prompt := noteElicitationPrompt + loadedSkillsElicitSection(loaded) + "\n\n--- CONVERSATION SO FAR ---\n" + renderHistoryForElicit(history, noteElicitChars)
	resp, err := cm.completeSummarization(ctx, prof, prompt, nil)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Text()), nil
}

// renderHistoryForElicit flattens turns into a plain-text transcript for the
// note-elicitation prompt, capped at maxChars and keeping the MOST RECENT content
// (it walks from the tail). Unlike the summarizer's rendering it includes
// tool-call arguments and FULL, untruncated tool-result content: the elicitor's
// whole job is to recover exact opaque values (tokens, IDs, hashes, paths) that
// in a coding agent live in tool output, so per-result truncation would defeat
// it — the maxChars budget is what bounds total size.
func renderHistoryForElicit(history []schema.Turn, maxChars int) string {
	var parts []string
	total := 0
	for _, h := range slices.Backward(history) {
		s := renderTurnForElicit(h)
		if s == "" {
			continue
		}
		if total+len(s) > maxChars {
			if rem := maxChars - total; rem > 0 {
				parts = append(parts, "[... earlier history truncated ...]\n"+s[len(s)-rem:])
			}
			break
		}
		parts = append(parts, s)
		total += len(s)
	}
	// parts were collected tail-first; reverse to chronological order.
	for l, r := 0, len(parts)-1; l < r; l, r = l+1, r-1 {
		parts[l], parts[r] = parts[r], parts[l]
	}
	return strings.Join(parts, "")
}

// renderTurnForElicit renders one turn as labeled plain text, including tool-call
// arguments and tool-result content — the parts that carry the exact values the
// elicitor must preserve. Returns "" for turns with no textual content.
func renderTurnForElicit(t schema.Turn) string {
	if t.Kind == schema.TurnAttentionResolution {
		return ""
	}
	var b strings.Builder
	for _, p := range t.Message.Content {
		switch p.Kind {
		case llm.ContentText:
			if p.Text != "" {
				b.WriteString(p.Text)
				b.WriteString("\n")
			}
		case llm.ContentToolCall:
			if p.ToolCall != nil {
				fmt.Fprintf(&b, "Tool call %s(%s)\n", p.ToolCall.Name, strings.TrimSpace(string(p.ToolCall.Arguments)))
			}
		case llm.ContentToolResult:
			if p.ToolResult != nil {
				fmt.Fprintf(&b, "Tool result %s: %s\n", p.ToolResult.Name, fmt.Sprint(p.ToolResult.Content))
			}
		}
	}
	if strings.TrimSpace(b.String()) == "" {
		return ""
	}
	label := ""
	switch t.Kind {
	case schema.TurnUserInput:
		label = "User: "
	case schema.TurnAssistant:
		label = "Assistant: "
	case schema.TurnSteering, schema.TurnCheckpoint, schema.TurnSummary:
		label = "Context: "
	}
	return label + b.String()
}

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

	truncText := func(s string, limit int) string {
		if len(s) > limit {
			return s[:limit] + "..."
		}
		return s
	}

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
			text := t.Message.Text()
			if len(text) > 5000 {
				text = text[:5000] + "..."
			}
			b.WriteString("User: ")
			b.WriteString(text)
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
			b.WriteString(truncText(t.Message.Text(), min(maxCheckpointChars, max(maxHistoryChars-b.Len(), 0))))
			b.WriteString("\n")
		case schema.TurnAssistant:
			// Extract communicate calls so the summarizer sees how the agent
			// talked to the user, while still distinguishing questions from
			// non-await replies.
			text := t.Message.Text()
			if text != "" {
				b.WriteString("Assistant: ")
				b.WriteString(truncText(text, 500))
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
					content := fmt.Sprint(p.ToolResult.Content)
					if len(content) > 200 {
						content = content[:200] + "..."
					}
					fmt.Fprintf(&b, "Tool(%s): %s\n", p.ToolResult.Name, content)
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

func countLines(s string) int {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// searchSummary is a search result's one-line summary, "[head]", with the
// notes the tool put after its entries (searchresult.Split) added inside the
// brackets, each without brackets of its own, so a compacted search still
// says it was cut short or left paths out. The registry appends its breaker
// nudge the same way, after a blank line, so the notes end where that nudge
// begins (agent/internal/tool IsBreakerNudge): the nudge is not a note about
// the search, and a result with no notes of its own would otherwise compact
// with it read as one (#4223). Every block after the entries is kept, not
// only the first: a result can carry two when the registry's character cap
// cuts inside the tool's own note (#4460), and the marker it appends after
// that fragment is the part a compacted search must not lose.
func searchSummary(head, output string) string {
	_, notes := searchresult.Split(output)
	parts := []string{head}
	for line := range strings.SplitSeq(notes, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if tool.IsBreakerNudge(line) {
			break
		}
		parts = append(parts, strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"))
	}
	return "[" + strings.Join(parts, "; ") + "]"
}

func parseExitCode(shellOutput string) string {
	// Look for "exit_code=N" (raw shell output) or "exit N" (masked summary format).
	for line := range strings.SplitSeq(shellOutput, "\n") {
		// Raw format: exit_code=0
		if _, rest, ok := strings.Cut(line, "exit_code="); ok {
			end := 0
			for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
				end++
			}
			if end > 0 {
				return rest[:end]
			}
		}
		// Masked format: → exit 0] or "exit 0"
		if _, rest, ok := strings.Cut(line, "exit "); ok {
			end := 0
			for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
				end++
			}
			if end > 0 {
				return rest[:end]
			}
		}
	}
	return "?"
}

func extractJSONField(s, field string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return ""
	}
	if v, ok := m[field]; ok {
		return fmt.Sprint(v)
	}
	return ""
}

func countJSONArrayElements(s string) int {
	var arr []any
	if err := json.Unmarshal([]byte(s), &arr); err != nil {
		return 0
	}
	return len(arr)
}
