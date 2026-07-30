package server

import (
	"strings"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
)

// threadEnvelope is the daemon's materialized view of the live session state a
// thread snapshot reports -- everything about a thread except its identity and
// its turns, both of which are already materialized elsewhere.
//
// It exists so a read costs a struct copy. thread/read takes its response cut
// inside the projection gate (see appThreadReadSnapshot and
// TestServerAppWireReadCutTakesTheSnapshotInsideTheSubscription), so anything
// the read touches is touched while projectionMu and deliveryMu are held. Every
// field here used to be PULLED from a live session callback at that moment,
// which put a transcript fsync, a synchronous jobs.jsonl read, and the session's
// own mutex under the gate; now the read copies this value and reaches nothing.
//
// THE ONE RULE: a field here has exactly one writer, refreshFacets, and exactly
// one reader, the copy in appThread/handleStatus. Never wire a callback beside a
// field as a fallback: a field with two sources is how a stale value survives a
// refactor while still reading as correct.
//
// Slices and pointers stored here are owned by the envelope once written.
// refreshFacets always stores a freshly sampled value and never mutates one in
// place, so a reader that copies the struct can hand the slices straight out.
type threadEnvelope struct {
	ContextPressure       float64
	ContextMetrics        ContextMetrics
	Detailed              *DetailedStatus
	Queue                 appwire.QueueState
	PendingMutations      []appwire.PendingMutation
	Tasks                 *appwire.TaskAggregate
	Goal                  *appwire.GoalState
	Usage                 *appwire.SerfUsage
	WorkMillis            int64
	ActiveTurnStartedAt   int64
	FailedToolCalls       *int
	AskPending            bool
	PendingEscalations    []appwire.SandboxEscalationRequested
	ReasoningEffort       string
	ReasoningEffortLevels []string
	SupportsReasoning     bool
	// Name and Preview are the only two things appThread reads out of
	// schema.SessionMeta. Storing the two strings rather than the whole struct is
	// deliberate: SessionMeta has roughly a dozen other fields (turn counts,
	// pinned notes, worktree paths) that change constantly and silently, and
	// storing them would create a dozen values this envelope claims to keep
	// current and does not.
	Name    string
	Preview string
}

// ThreadEnvelopeSource supplies the live session values the thread envelope
// reports. cmd/serf/serve.go implements it over the running agent.Session.
//
// The bridge samples it at the moments those values change -- never on a read.
// That is the whole point of the interface: it collapses sixteen separately
// injected read-time callbacks into one seam with one caller, so a reader can
// see at a glance where session state enters the daemon's projection and can be
// sure it is not the read path.
type ThreadEnvelopeSource interface {
	ContextPressure() float64
	ContextMetrics() ContextMetrics
	DetailedStatus() DetailedStatus
	ClientMutationProjection() (appwire.QueueState, []appwire.PendingMutation)
	TaskAggregate() *appwire.TaskAggregate
	GoalStatus() (status string, iterations int, ok bool)
	WorkMetrics() (workMillis int64, usage *appwire.SerfUsage, activeTurnStartedAt int64)
	FailedToolCalls() (count int, measured bool)
	AskPending() bool
	PendingEscalations() []appwire.SandboxEscalationRequested
	ReasoningInfo() (effort string, levels []string, supportsReasoning bool)
	SessionMeta() schema.SessionMeta
}

// envelopeFacet names one independently sampled group of envelope fields. A
// facet is the unit the event map works in: an event declares which facets it
// can have moved, and only those are re-sampled.
//
// The grouping follows what one call to the source returns, so a facet can
// never be half-written.
type envelopeFacet uint32

const (
	facetContext envelopeFacet = 1 << iota
	facetDiagnostics
	facetQueue
	facetTasks
	facetGoal
	facetWork
	facetFailures
	facetAsk
	facetEscalations
	facetReasoning
	facetMeta
)

// facetAll is every facet. Used for the seed at identity install, which is the
// one moment every value changes at once because the session itself changed.
const facetAll = facetContext | facetDiagnostics | facetQueue | facetTasks |
	facetGoal | facetWork | facetFailures | facetAsk | facetEscalations |
	facetReasoning | facetMeta

// facetsByEvent maps a session event to the envelope facets it can have moved.
//
// THIS TABLE IS THE FRESHNESS CONTRACT. A facet is current exactly when every
// event that can move it appears here against that facet. An event absent from
// the table samples nothing, which is what keeps the hot path free: token
// deltas, tool output deltas and reasoning deltas move no envelope field and
// must never be listed.
//
// It is one table in one file on purpose. The alternative -- a publish call at
// each of the ~70 places inside agent and its subsystems where these values
// actually change -- spreads the same contract across eight packages with no
// compiler net over it, where a forgotten site yields a wire field that is
// permanently stale and still reads as plausible.
//
// The cost of that choice, stated plainly: sampling here is a pull relocated
// from read time to change time, not a value carried on the event. Five facets
// (queue, tasks, meta, escalations, and reasoning's level/support half) already
// have events carrying their values today and could be converted to true
// carriers one at a time later, without touching the read path.
var facetsByEvent = map[events.EventKind]envelopeFacet{
	// THE THREE CHECKPOINTS. A session opening or closing restates everything
	// about it, and so does a turn boundary.
	//
	// TURN_ENDED is facetAll deliberately, and it is what makes a whole class of
	// gap structurally impossible rather than individually remembered. Several
	// values move DURING a turn with no event of their own: the reasoning effort
	// is reassigned every model round from the task store and again by loop
	// detection, MCP servers record connection errors, the goal tool's terminal
	// verdict is deferred to the next gate. Worse, the failed-turn return path
	// emits ONLY this event, so anything not re-read here would freeze until the
	// next cleanly-ending turn -- which never comes if the user stops after a
	// failure. Sampling everything once per turn costs one jobs.jsonl read and
	// one task-store read per turn, which is the right price for that.
	events.EventSessionStart: facetAll,
	events.EventSessionEnd:   facetAll,
	events.EventTurnEnded:    facetAll,

	// Queue mutations. QUEUE_CHANGED is the direct announcement; the turn-start
	// events are here because a queued entry is CONSUMED into a turn without a
	// QUEUE_CHANGED of its own, and because the client-mutation store's
	// pending-execution set transitions on incorporation and completion.
	//
	// USER_INPUT carries facetMeta because an unnamed thread's preview falls back
	// to meta.OriginalPrompt, which is derived from the first user turn in
	// history: without it a new thread lists as its raw session id for its whole
	// first turn.
	events.EventQueueChanged:     facetQueue,
	events.EventUserInput:        facetQueue | facetWork | facetContext | facetAsk | facetMeta,
	events.EventSteeringInjected: facetQueue | facetAsk | facetContext,

	// A completed tool call appends its result to history (context), can be the
	// ask_user whose completion sets the pending-ask bit, and can be a task tool.
	//
	// facetFailures is the one row where the write-then-emit ordering does NOT
	// hold: TOOL_CALL_END is emitted from execToolBatch, and the write that
	// advances the transcript writer's counter happens afterwards in
	// persistToolResults (agent/session_lifecycle.go). So this sample can be one
	// tool call behind. That is not a regression -- the read-time pull it
	// replaced sampled at the same instant, inside the commit for this same
	// event -- and TURN_ENDED re-reads the facet after persistence, so the figure
	// is exact by the turn boundary. It is listed here because the count usually
	// HAS advanced (from earlier calls in the batch) and the finer-grained stamp
	// is worth having; not because the ordering guarantee applies.
	events.EventToolCallEnd: facetFailures | facetAsk | facetTasks | facetContext,

	// A model response moves cumulative usage and context pressure. Compaction
	// rewrites history outright, and an idle /compact has no turn boundary to
	// rescue the preview it can change.
	events.EventAssistantTextEnd:  facetContext | facetWork,
	events.EventContextCompaction: facetContext | facetMeta,
	events.EventCompactionTurn:    facetContext,

	// Values whose events carry them today.
	events.EventTaskUpdated:            facetTasks,
	events.EventSessionNameChanged:     facetMeta,
	events.EventModelChanged:           facetReasoning | facetContext | facetDiagnostics,
	events.EventReasoningEffortChanged: facetReasoning,

	// Goal state. Neither SetGoal nor Clear emits anything -- the goal store has
	// no event handle at all -- so goal/set refreshes facetGoal at the handler
	// (see handleAppGoalSet). These two cover the engine's own transitions.
	events.EventGoalContinuation: facetGoal | facetQueue | facetAsk,
	events.EventGoalEnded:        facetGoal,

	// Sandbox escalations block a tool call awaiting a human.
	events.EventSandboxEscalationRequested: facetEscalations,
	events.EventSandboxEscalationResolved:  facetEscalations,

	// Jobs are part of DetailedStatus. A running job's OutputBytes advances with
	// no event at all (agent/jobs.go stamps it live from the output buffer), so
	// that one number lags by up to one job event. It is a progress counter, not
	// a correctness field: no notification announces diagnostics, so nothing
	// about the response cut depends on it, and no consumer treats it as
	// terminal.
	events.EventJobStarted:   facetDiagnostics,
	events.EventJobFinished:  facetDiagnostics,
	events.EventPluginLoaded: facetDiagnostics,
}

// SetThreadEnvelopeSource installs the seam the bridge samples session state
// through. Install it before serving anything. A nil source leaves the envelope
// at whatever was last written, which is the correct answer for a daemon with no
// session attached.
func (s *Server) SetThreadEnvelopeSource(src ThreadEnvelopeSource) {
	s.mu.Lock()
	s.appEnvelopeSource = src
	s.mu.Unlock()
}

// RefreshThreadEnvelope re-samples every facet. It is the seed: the one moment
// every envelope value changes together is the moment the session behind them
// changes, and that is not something any single event announces.
func (s *Server) RefreshThreadEnvelope() {
	s.refreshFacets(facetAll)
}

// refreshThreadEnvelopeForEvent samples the facets ev can have moved.
//
// It runs in the bridge, BEFORE RecordAppEvent projects ev. That ordering is
// the same one applySessionEventStatus relies on and it is what makes the
// response cut correct: where a session writes its state before emitting the
// event that announces it, an envelope refreshed here can only lead its own
// notification, never lag it. Refreshing after the commit would invert exactly
// the invariant this work exists to protect.
//
// That write-then-emit guarantee is the session's, not this function's, and it
// does not hold everywhere -- see the facetFailures note on TOOL_CALL_END. Where
// it does not hold, the sample is no worse than the read-time pull it replaced,
// because that pull ran inside the commit for the very same event.
func (s *Server) refreshThreadEnvelopeForEvent(ev events.SessionEvent) {
	facets, ok := facetsByEvent[ev.Kind]
	if !ok {
		return
	}
	s.refreshFacets(facets)
}

// refreshFacets samples the named facets from the installed source and stores
// them. Sampling happens OUTSIDE s.mu: a source call can take the session's own
// mutex, the task store's, or read jobs.jsonl, and holding s.mu across that
// would put session I/O back under a lock the read path takes. The store is a
// separate, purely in-memory critical section.
func (s *Server) refreshFacets(facets envelopeFacet) {
	s.mu.RLock()
	src := s.appEnvelopeSource
	threadID := s.appThreadID
	sourceID := s.appSourceID
	s.mu.RUnlock()
	if src == nil {
		return
	}
	stampSourceID := sourceID
	if stampSourceID == "" {
		stampSourceID = "local"
	}
	ref := appwire.Ref{SourceID: stampSourceID, ThreadID: threadID}.String()

	var next threadEnvelope
	if facets&facetContext != 0 {
		next.ContextPressure = src.ContextPressure()
		next.ContextMetrics = src.ContextMetrics()
	}
	if facets&facetDiagnostics != 0 {
		detailed := src.DetailedStatus()
		next.Detailed = &detailed
	}
	if facets&facetQueue != 0 {
		next.Queue, next.PendingMutations = src.ClientMutationProjection()
	}
	if facets&facetTasks != 0 {
		next.Tasks = src.TaskAggregate()
	}
	if facets&facetGoal != 0 {
		if status, iterations, ok := src.GoalStatus(); ok {
			next.Goal = &appwire.GoalState{Status: status, Iterations: iterations}
		}
	}
	if facets&facetWork != 0 {
		next.WorkMillis, next.Usage, next.ActiveTurnStartedAt = src.WorkMetrics()
	}
	if facets&facetFailures != 0 {
		// An unmeasured count stays absent. A zero here would let a daemon that
		// never counted anything claim in the session's own chrome that the run
		// was clean.
		if count, measured := src.FailedToolCalls(); measured {
			next.FailedToolCalls = &count
		}
	}
	if facets&facetAsk != 0 {
		next.AskPending = src.AskPending()
	}
	if facets&facetEscalations != 0 {
		next.PendingEscalations = src.PendingEscalations()
		// Stamp each card with this thread's identifiers HERE, at the write, so
		// the read hands out the stored slice without touching it. Stamping on
		// the read would mutate installed state through the copy every reader
		// takes.
		for i := range next.PendingEscalations {
			next.PendingEscalations[i].ThreadID = threadID
			next.PendingEscalations[i].Ref = ref
		}
	}
	if facets&facetReasoning != 0 {
		next.ReasoningEffort, next.ReasoningEffortLevels, next.SupportsReasoning = src.ReasoningInfo()
	}
	if facets&facetMeta != 0 {
		meta := src.SessionMeta()
		next.Name = strings.TrimSpace(meta.Name)
		next.Preview = strings.TrimSpace(schema.SessionDisplayName(meta))
	}

	s.mu.Lock()
	// Re-test the identity in the same hold as the write, exactly as
	// applySessionEventStatus does, and drop a sample that no longer describes
	// this daemon's session.
	//
	// Sampling above ran outside s.mu and took no projection lock, so unlike the
	// read-time pull this replaced it is NOT mutually excluded from
	// ReplaceAppIdentity's commit -- and in production it genuinely interleaves,
	// because /clear runs that commit while the outgoing session's bridge is
	// still draining and only closes it afterwards. Storing a sample taken
	// before the replacement would publish the retired session's title, queue,
	// failure count and escalation cards under its successor's identity, with
	// nothing afterwards to correct it. The fix is NOT to sample under the gate:
	// that is the I/O this change exists to move off it.
	if s.appThreadID == threadID && s.appSourceID == sourceID {
		s.appEnvelope.assign(facets, next)
	}
	s.mu.Unlock()
}

// assign copies exactly the named facets out of next. Writing through one
// method keeps the facet-to-field mapping in a single place: a field added to
// the struct without a line here is a field that is sampled and then dropped,
// which is far easier to see in six lines of assignment than spread across the
// sampler.
func (e *threadEnvelope) assign(facets envelopeFacet, next threadEnvelope) {
	if facets&facetContext != 0 {
		e.ContextPressure = next.ContextPressure
		e.ContextMetrics = next.ContextMetrics
	}
	if facets&facetDiagnostics != 0 {
		e.Detailed = next.Detailed
	}
	if facets&facetQueue != 0 {
		e.Queue = next.Queue
		e.PendingMutations = next.PendingMutations
	}
	if facets&facetTasks != 0 {
		e.Tasks = next.Tasks
	}
	if facets&facetGoal != 0 {
		e.Goal = next.Goal
	}
	if facets&facetWork != 0 {
		e.WorkMillis = next.WorkMillis
		e.Usage = next.Usage
		e.ActiveTurnStartedAt = next.ActiveTurnStartedAt
	}
	if facets&facetFailures != 0 {
		e.FailedToolCalls = next.FailedToolCalls
	}
	if facets&facetAsk != 0 {
		e.AskPending = next.AskPending
	}
	if facets&facetEscalations != 0 {
		e.PendingEscalations = next.PendingEscalations
	}
	if facets&facetReasoning != 0 {
		e.ReasoningEffort = next.ReasoningEffort
		e.ReasoningEffortLevels = next.ReasoningEffortLevels
		e.SupportsReasoning = next.SupportsReasoning
	}
	if facets&facetMeta != 0 {
		e.Name = next.Name
		e.Preview = next.Preview
	}
}
