// Package contextmgr manages a session's context pressure: it tracks cumulative
// token usage, applies progressive compaction layers (deterministic
// checkpoint, LLM summarization) to the
// conversation history, and hosts the pluggable context strategies. Strategies
// reach back to their owning session only through the narrow Host interface,
// which breaks the context⇄session cycle.
package contextmgr

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// CompactionMeta holds session-level metadata injected into compaction summaries.
// The session populates this before each ManageContext call so that checkpoint
// and summarize have access to data outside the turn history.
type CompactionMeta struct {
	SessionID       string   // session id; non-empty only when the session is persistent (has a stateDir)
	ActivatedSkills []string // skill names activated during this session
	// PendingAskTurnIDs is an operation-local snapshot from the session's
	// pending owner. Folds retain that execution tail, not a reconstructed ask.
	PendingAskTurnIDs []string
	// Live history can precede the writer's TurnID placement. Exact call IDs
	// identify the same source there without changing transcript identity.
	PendingAskCallIDs []string

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

	// lastSummaryFailureRepeat is the signature of the most recently
	// published summary failure (warning or daemon log), guarded by mu. An
	// automatic pass failing the same way as the one before it stays quiet —
	// see summary_failure.go.
	lastSummaryFailureRepeat string

	// Thresholds are fractions (0.0–1.0) of the context window. SetCompactAt
	// keeps them ordered: WarnThreshold < CheckpointThreshold <=
	// SummarizeThreshold.
	//
	// WarnThreshold is the pressure fraction at which the Session nudges the
	// agent to self-compact at its next clean seam (best-effort; the checkpoint
	// and summary layers remain the guarantee). CheckpointThreshold is the
	// trigger for automatic compaction. SummarizeThreshold is the backstop
	// that adds an LLM summary when a checkpoint alone did not free enough.
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
	cm := &Manager{
		profile:             profile,
		client:              client,
		cheap:               cheap,
		PreserveRecentTurns: 6,
	}
	cm.SetCompactAt(DefaultCompactAt)
	return cm
}

// DefaultCompactAt is the context-window fraction at which automatic
// compaction triggers unless the user configures another.
const DefaultCompactAt = 0.90

const (
	// selfCompactNudgeAt is the pressure at which the agent is nudged to
	// compact itself; a trigger at or below it pulls the nudge down with it.
	selfCompactNudgeAt = 0.75
	// nudgeLead is how far below the trigger the nudge sits when the trigger
	// is configured at or below selfCompactNudgeAt.
	nudgeLead = 0.05
	// summarizeBackstopAt is the pressure at which a checkpoint is followed by
	// an LLM summary; a trigger at or above it raises the backstop to match.
	summarizeBackstopAt = 0.95
)

// SetCompactAt sets the fraction of the context window at which automatic
// compaction triggers and derives the other two thresholds from it: the
// self-compact nudge stays at 0.75 or sits 0.05 below a lower trigger, and the
// summarize backstop stays at 0.95 or rises to a higher trigger.
func (cm *Manager) SetCompactAt(fraction float64) {
	cm.CheckpointThreshold = fraction
	cm.WarnThreshold = min(selfCompactNudgeAt, max(0, fraction-nudgeLead))
	cm.SummarizeThreshold = max(summarizeBackstopAt, fraction)
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

// HasClient reports whether an LLM client is available (note elicitation and LLM
// summarization both require one).
func (cm *Manager) HasClient() bool { return cm.client != nil }
