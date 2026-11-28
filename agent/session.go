package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oklog/ulid/v2"

	"primeradiant.com/evener/agent/envctx"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/agent/internal/clock"
	"primeradiant.com/evener/agent/internal/contextmgr"
	"primeradiant.com/evener/agent/internal/goal"
	"primeradiant.com/evener/agent/internal/hooks"
	"primeradiant.com/evener/agent/internal/installid"
	"primeradiant.com/evener/agent/internal/mcp"
	"primeradiant.com/evener/agent/internal/modelavailability"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/mcpconfig"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/skill"
	"primeradiant.com/evener/agent/task"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func (s *Session) emitWithJobTreeRevision(kind events.EventKind, data events.EventData, p *provenance.Causal) {
	if s != nil {
		if rootID, revision, ok := s.nextJobTreeRevision(kind); ok {
			switch payload := data.(type) {
			case events.JobStartedData:
				payload.RootSessionID = rootID
				payload.TreeRevision = revision
				data = payload
			case events.JobFinishedData:
				payload.RootSessionID = rootID
				payload.TreeRevision = revision
				data = payload
			}
			s.maybeAutoSave()
		}
	}
	s.emitWithProvenance(data, p)
	if kind == events.EventJobStarted || kind == events.EventJobFinished {
		s.emitSessionActivityChanged(s.ID(), appwire.SessionActivityResourceJobs)
	}
}

// noteJobTreeShapeChange moves the activity clock for a change that adds or
// removes an entry rather than starting or finishing a job: a delegate
// appearing shifts its owner's sorted delegate list, and a live continuation
// minted before it must be refused rather than applied to a list that moved
// under it. The move is only in memory until persistJobTreeShapeChange runs,
// which is why the two are separate: this one is called while the delegate
// tree's own lock is held.
func (s *Session) noteJobTreeShapeChange() {
	if s == nil {
		return
	}
	if _, _, ok := s.jobActivityClock.nextRevision(); ok {
		s.jobTreeShapeUnsaved.Store(true)
	}
}

// persistJobTreeShapeChange writes the session's metadata when a shape change
// has moved the activity clock since the last save, the same way
// emitWithJobTreeRevision persists the moves a job start or finish makes.
// Without it a restart restores the revision from before the delegate
// appeared, and a continuation minted against the older list is accepted
// against the newer one. It runs off the delegate tree's lock: the save takes
// the session's own locks, which must never be waited on from behind that one.
func (s *Session) persistJobTreeShapeChange() {
	if s == nil {
		return
	}
	if s.jobTreeShapeUnsaved.CompareAndSwap(true, false) {
		s.maybeAutoSave()
	}
}

func (s *Session) nextJobTreeRevision(kind events.EventKind) (string, uint64, bool) {
	if s == nil || (kind != events.EventJobStarted && kind != events.EventJobFinished) {
		return "", 0, false
	}
	return s.jobActivityClock.nextRevision()
}

// Session holds the state for a single agent conversation, including its
// configuration, LLM client and provider profile, event channel, conversation
// history, registered tools, context-management state, subagents, plugins, MCP
// connections, and persistence settings.
type Session struct {
	conversation    conversationState
	hubClient       SessionHubClient
	hubGeneration   uint64
	hubEnabled      bool
	hubWatchSignal  chan struct{}
	hubWatchCancels map[string]context.CancelFunc
	hubOrdinary     map[string]*tool.RegisteredTool
	// managerOnlyTools names the tools Manager composition registered with no
	// ordinary registration behind them (see composeHubRegistry). Every spawn
	// path sets Manager=false, so a child never composes one, and the delegate
	// tool ceiling must not advertise them.
	managerOnlyTools map[string]bool
	// cliMCPServers names the MCP servers only the CLI layers declare (see
	// initMCP), and cliMCPToolNames the tools they registered. Every spawn path
	// clears MCPConfigFiles and MCPInline, so a child never discovers those
	// servers, and the delegate tool ceiling must not advertise their tools. The
	// server names are kept so a retry that heals one re-derives the tools.
	cliMCPServers   map[string]bool
	cliMCPToolNames map[string]bool
	// oneShotOmittedTools records ceremony tools omitted solely because
	// TurnEndsProcess was true (not denied by spawn policy), so default
	// delegates record eligible notes_* tools as excluded for one-shot mode and
	// regain them on a persistent resume. Guarded by delegateExclusionsMu.
	oneShotOmittedTools map[string]bool
	// cliMCPEvidence is the project-configuration evidence the classification
	// above was derived from: the file the layer-2 project config resolves to,
	// what it contained, and whether it existed. The classification is only
	// sound while this still describes the session's current workspace, so a
	// probe re-verifies it and leaves MCP names eligible when it cannot (see
	// probeCLIEvidenceFresh).
	cliMCPEvidence cliMCPClassificationEvidence
	// rosterExcludesCLIMCP is the ONE roster decision both cached surfaces read:
	// the <available_agents> roster in the system prompt and the delegate tool
	// definition's agent_type description are rendered from this boolean, so the
	// two cannot disagree about which CLI-only MCP names a delegate may use.
	//
	// There is exactly one writer rule, and it is what replaced the shared
	// verdict plus two cache version stamps: the value is set where a
	// workspace-scoped probe outcome can be APPLIED to this session's roster —
	// session init, the environment-swap reset, and request preparation
	// (refreshCachesForExclusionChangeContext). The probe itself never writes
	// here, so a caller whose workspace has moved on cannot erase the decision
	// the installed workspace established.
	//
	// It is guarded by delegateExclusionsMu rather than s.mu because the ceiling
	// is built on both paths: request preparation renders the roster already
	// holding s.mu, and delegate creation derives a ceiling without it.
	rosterExcludesCLIMCP bool
	// delegateExclusionsMu guards the two sets above. They cannot ride s.mu:
	// the delegate-create path computes the ceiling without holding s.mu, while
	// the prompt-render path computes it already holding s.mu, and s.mu is not
	// reentrant. Nothing takes s.mu while holding this lock.
	delegateExclusionsMu sync.RWMutex
	// evidenceProbes names the workspaces that have a CLI-MCP evidence check
	// outstanding: at most one per key, so a workspace whose mount has stopped
	// answering gets ONE parked check rather than one per model request. A
	// request that finds its workspace's key taken answers unknown reachability
	// immediately instead of starting a second check into the same stall, and the
	// goroutine that claimed the key removes it when it finally answers.
	//
	// It is a lock of its own, not a ride on delegateExclusionsMu: an entry is
	// added and removed around a check that takes no other lock, and sharing the
	// read-mostly lock would make every ceiling build wait on map bookkeeping it
	// never reads.
	evidenceProbesMu       sync.Mutex
	evidenceProbes         map[string]struct{}
	hubRegistry            *tool.Registry
	id                     string
	cfg                    SessionConfig
	delegateController     *delegateTreeController
	quietWatch             delegateQuietWatchHost
	delegateRootSessionID  string
	owningDelegateID       string
	ownsDelegateController bool
	// drainHeld is set while this session is inside its delegate finalization
	// drain (drainForFinalization). It is one half of the hold a delegate's
	// row reports, the other half being a managed shell still in the job
	// manager's running map. The count alone cannot say a run is held — a
	// delegate mid-turn with a background shell has the same count — so the
	// row states the hold and uses the count only to phrase it.
	drainHeld atomic.Bool
	// subagentTallyForTest overrides SubagentTally's answer when set. A fresh
	// session with no subagents reports the same empty tally as any other, so
	// a cross-package test that needs to tell two sessions' tally seams apart
	// (SubagentTallyForTest) has no real value to seed short of running a
	// whole delegate tree; this gives it one.
	subagentTallyForTest     *appwire.SubagentTally
	artifactStore            artifactStore
	ownsArtifactStore        bool
	client                   *llm.Client
	cheap                    *cheapmodel.Caller
	profile                  *provider.Profile
	resolveProfile           func(ref string) (*provider.Profile, error) // cross-provider resolver; may be nil
	savedModelFallbacks      bool                                        // true when cfg.ModelFallbacks came from persisted SessionMeta (not explicit CLI override)
	delegateModelDescription string
	modelSnapshot            *modelavailability.Snapshot
	// lastDroppedModelFallbacks records the cfg.ModelFallbacks entries dropped
	// by the most recent SetModel's post-switch revalidation (entries that no
	// longer validate against the new profile). Nil until a switch drops any.
	// The model-switch marker surfaces it as a warning line.
	lastDroppedModelFallbacks []string
	// roundFallbackSource is the round's unprojected request input (system plus
	// expanded history) kept for the fallback chain, whose targets apply their
	// own thinking-replay and tool-result media policy (#4563, #4217).
	// prepareModelRequestWithError writes it and callModelWithFallback takes and
	// clears it on the same turn goroutine; mu guards the exchange. It holds the
	// input rather than a built message slice because only Messages depends on
	// that input: the fallback lays out its own messages when it runs, so a
	// round that never falls back builds one request instead of two (#6442).
	// It carries the round's priced route too, because a round has one handoff,
	// not two: every admission the round takes bills the tool list that handoff
	// measured, and a later round cannot inherit this one's.
	roundFallbackSource    roundFallbackSource
	roundFallbackSourceSet bool
	// rejectedToolResultDocuments holds tool-call IDs whose document payload a
	// provider rejected. Request assembly omits those bytes for the rest of the
	// session (the transcript keeps them); the rejection recovery adds the IDs
	// and retries once. Guarded by mu.
	rejectedToolResultDocuments map[string]struct{}
	// pendingToolResultDocuments holds tool-call IDs a speculative document
	// recovery stripped from the retry before the provider named a document.
	// A successful retry promotes them to rejectedToolResultDocuments; a failed
	// retry rolls them back, so an unrelated invalid request cannot suppress a
	// document the provider accepted (#4465). Guarded by mu.
	pendingToolResultDocuments map[string]struct{}
	// httpClient issues the web_fetch HTTP GET. Nil means the production default
	// (http.DefaultClient); tests set it on their own session to serve a fabricated
	// response through an injected transport without touching the network.
	httpClient httpDoer
	env        execenv.ExecutionEnvironment
	// ownsEnv is true when this session is a child whose execution environment was
	// built FOR it (a working-dir re-root and/or a per-delegate sandbox) rather
	// than shared with its parent. Such an environment owns scratch dirs and
	// file-tool fds no parent cleanup reaches, so the child's teardown settles
	// them; a shared one belongs to the live parent and is left alone. The child
	// records it because a teardown can reach a child no parent bookkeeping names
	// — a runtime the stable controller holds for its own close.
	ownsEnv bool
	// parentSharedEnv is the environment a child was handed when it is the live
	// parent's own object (ownsEnv false) — the one environment this session must
	// never settle and whose scratch it must never take. Nil on a root session and
	// on a child whose environment was built for it.
	parentSharedEnv execenv.ExecutionEnvironment
	clock           clock.Clock
	stateDir        string
	installID       string
	// origin marks how the session was launched: "test" for agentic-testing
	// runs (set from EVENER_SESSION_ORIGIN on fresh create), empty for normal
	// sessions. Preserved across resume from the persisted SessionMeta.Origin.
	// Surfaced back out via Meta().Origin so the hub can classify an
	// all-"test" project into the "Test runs" group.
	origin string
	// strictTranscriptMaxLineBytes caps a child transcript line during resumability
	// checks. Zero means the production default (transcript.DefaultMaxLineBytes); tests
	// set a small value on their own session to exercise the oversized-line path
	// without mutating a shared global.
	strictTranscriptMaxLineBytes int
	events                       chan events.SessionEvent
	eventsMu                     sync.RWMutex // guards send-vs-close on events; all sends go through emit()
	closeCtxMu                   sync.RWMutex
	closeCtx                     context.Context // shared shutdown deadline for blocked authoritative sends
	closeSignal                  chan struct{}
	testOnlyBlockedSendEntered   func()
	eventsClosed                 bool // set under eventsMu.Lock immediately before close(events)
	// descendantEvent is inherited from the spawning root and remains immutable
	// for this Session's lifetime. Children use it to expose their live event
	// stream without acquiring an authoritative consumer for their own channel.
	descendantEvent func(events.SessionEvent)
	// authoritativeConsumer records that something has UNDERTAKEN to drain
	// events and cannot be lied to. ConsumeEventsLossless is the ONLY writer,
	// and it sets this in the same call that starts the drain, so the flag
	// cannot be set without a drain being started. Do not add another way to set
	// it: "lossless with nobody reading" is a permanent wedge, and the point of
	// this shape is that it cannot be spelled. Read under eventsMu.RLock in
	// sendEvent.
	//
	// It is "undertook to drain", NOT "is draining", and the gap is real: the
	// flag is never cleared, so a consume callback that blocks forever or exits
	// via runtime.Goexit leaves the mark standing over a dead loop, and every
	// emitter then wedges once the buffer fills. Nothing here can detect that —
	// the contract is on the consumer, which is why there is exactly one and it
	// is the bridge.
	authoritativeConsumer bool
	envInfo               schema.EnvironmentInfo

	// capabilities is the toolchain measurement the capability preamble renders
	// (see session_capabilities.go). It is taken ONCE, at session start, by one
	// short-timeout probe: the prompt is re-rendered often (model switch, env
	// swap, every restore) and none of those may fork a probe. Its zero value
	// renders "unprobed", which is what a session whose probe could not run
	// truthfully reports. Written during init before the session is shared, read
	// under s.mu by the prompt render.
	capabilities capabilityProbe

	// envCollector assembles per-turn environment Snapshots (cwd, date,
	// sandbox, git branch, resource pressure); envTracker diffs successive
	// Snapshots into the append-only <environment_context> blocks
	// maybeAppendEnvironmentContext injects before every user turn.
	//
	// envCollector is write-once (constructed at session init, never
	// reassigned) and only ever read from the turn-processing goroutine, so it
	// needs no lock. envTracker is NOT single-goroutine-owned: Session.Compact
	// can run on a caller's own goroutine with no idle gate, and
	// resetEnvContextTrackerLocked reassigns the pointer (and implicitly races
	// the turn goroutine's maybeAppendEnvironmentContext, which mutates the
	// pointed-to Tracker's internal state via RenderDiff).
	//
	// Both sides take attentionMu THEN mu, in that order. mu alone is not
	// enough: the tracker advance and the transcript entry it describes are one
	// publication, so a reset that took only mu could replace the tracker
	// between an append's RenderDiff and the entry that carries its block, and
	// the next turn would diff against a baseline no transcript records. See
	// appendEnvironmentContext (which holds the pair across the whole append)
	// and resetEnvContextTrackerLocked's two callers: publishFoldTransaction
	// via foldCommit.resetEnvContextTrackerLocked, and handleCompactionTurn.
	envCollector *envctx.Collector
	envTracker   *envctx.Tracker
	// envContextState mirrors envTracker.State() for Meta()/SessionMeta.EnvContext:
	// nil until the first ENVIRONMENT turn is emitted (or restored from a prior
	// session), so a session that has said nothing about its environment yet
	// persists nothing. Guarded by mu; appendEnvironmentContext advances it with
	// the entry it commits and resetEnvContextTrackerLocked clears it with the
	// tracker.
	envContextState *envctx.State

	launchEnvBaseline envctx.Snapshot // the runtime facts the launch prompt printed
	// notesLastProjected is the last shared-notes block
	// maybeAppendNotesContext appended as a NOTES_CONTEXT turn. The
	// projection appends only when the rendered block differs from this
	// record, so consecutive rounds with no notes change append once rather
	// than re-emitting a limit-sized block every round. Cleared by
	// resetNotesProjectionAfterCompaction when compaction folds history away
	// (the model must re-see the current state), and seeded from restored
	// history on resume (see below). Guarded by mu.
	notesLastProjected string
	// notesEverProjected reports whether this process has ever projected a
	// non-empty notes block. It distinguishes the transition-to-empty case
	// (append the explicit cleared marker so the next model request
	// reflects the cleared list) from the never-populated case (project
	// nothing, keeping a fresh session's history byte-identical). Seeded
	// from restored history on resume alongside notesLastProjected.
	// Guarded by mu.
	notesEverProjected bool

	// Memory environments are independent of workspace policy and cwd.
	memoryMu            sync.Mutex
	memoryEnvs          map[string]*execenv.LocalExecutionEnvironment
	memoryRoots         map[string]*execenv.ConfinedFileRoot
	memoryEnvUsers      map[*execenv.LocalExecutionEnvironment]int
	memoryEnvFlights    map[string]*memoryEnvironmentFlight
	memoryClosed        bool
	memoryLastProjected map[string]memoryProjection
	memoryEverProjected map[string]bool
	// memoryBaseline holds, per scope, the index the session already knows: the
	// last current one projected to it, or the one it wrote or deleted itself.
	// Compaction and any non-current projection clear it; a resumed session
	// starts without one.
	memoryBaseline map[string]memoryIndexBaseline
	// memoryReadPages holds, per scope and page path, what the session last
	// knew of each page it read with memory_read; only these pages get change
	// notices. Compaction keeps them; a resumed session starts without any.
	memoryReadPages map[string]map[string]memoryPageRecord
	// memoryReadPageOrder lists, per scope, the tracked pages from least to
	// most recently read with memory_read; it bounds memoryReadPages.
	memoryReadPageOrder map[string][]string
	memoryIndexFlights  map[string]*memoryIndexFlight
	// The sole automatic reader per scope owns cleanup until it returns.
	// Admission and retirement share memoryMu, including the pre-read window.
	memoryIndexReaders map[string]*execenv.LocalExecutionEnvironment

	// --- Synchronization / lock discipline ---
	//
	// The turn loop (ProcessInput → processOneInput) is the primary owner of
	// most mutable state. External callers — SetModel/SetReasoningEffort,
	// Steer, the client-mutation queue handlers, Snapshot/State/DetailedStatus, and Close —
	// run on other goroutines and race it, so the primitives below guard the
	// shared fields. Several collaborators carry their OWN locks and are NOT
	// covered by mu: contextMgr (contextmgr.Manager.mu), reg (tool.Registry.mu),
	// subagents (subagentManager.mu / per-child sub.mu), taskStore
	// (TaskStore.mu — note it may be SHARED with child sessions when
	// ShareTasksWithChildren is set), and transcript (transcript.Writer.mu).
	//
	// LOCK ORDER (outermost first; a lock to the left is taken before any to its
	// right and never while holding one to its right):
	//   responseSideEffectsMu > mu          (Close takes both)
	//   delegateDeliveryMu > mu
	//   metaSaveMu > mu                     (maybeAutoSave)
	//   goalUpdateMu > mu, notesUpdateMu > mu (emit runs after mu is released)
	// drainAbandonedMu and childCommittedSendMu are leaves: nothing is taken under
	// them.
	//
	// Every field whose comment says "Guarded by mu" (or s.mu) is guarded by mu.
	// The group below is the original core set; later fields carry their own
	// guard in their field comment.
	//
	// mu guards: history, state, closing, turns, turnBudgetWarningEmitted,
	//   modelResponses, totalRounds,
	//   sessionEndEmitted, profile (swapped here; read via currentProfile()),
	//   env (swapped here; read via currentEnv()), envInfo (swapped alongside
	//   env so the two never observe a torn intermediate state), the mutable
	//   cfg knobs (ReasoningEffort, command timeouts, MaxToolRoundsPerInput),
	//   cachedSystemPrompt, cachedToolDefs, the comm communicate-result,
	//   steeringQueue, activeProvenance, inputQueue,
	//   loopDetectionCount, the task* reminder counters, depth, the goalInTurn
	//   flag and kickFunc callback, the naming name-state, envTracker,
	//   envContextState, currentRoundRecorder, salvagedTurnRound, and the worktree
	//   occupancy fields (worktreeRestoreEnv, worktreeCurrentPath,
	//   worktreeCurrentManaged, worktreeGitVersionOK, worktreeLiveWorkStub), and
	//   detachedProcesses. It
	//   does NOT guard reg — the tool.Registry self-synchronizes.
	//
	// mu is a read/write lock. Every mutation of a guarded field takes Lock;
	// a pure read may take RLock only when it touches guarded fields and
	// nothing else, never mutates what it reads, and takes no other lock
	// except one classified in session_emit_lock_guard_test.go as a
	// collaborator whose holder cannot reach Session. Two readers are the emit
	// stamp (activeCausalProvenance) and the context sample (ContextStatus),
	// which run on every event and every sample; sharing keeps them from
	// serializing each other. Readers are still excluded by any writer, so a
	// read observes one whole state and never a write in progress. Go read
	// locks are not recursive: a path that takes RLock and then reaches
	// another Session RLock or Lock can wedge when a writer is pending, so no
	// reader may call another.
	mu sync.RWMutex
	// retirementController is process-owned and published atomically so admission
	// never nests the controller mutex with a Session lock. Nil leaves ordinary
	// non-daemon sessions unchanged; descendants inherit it before starting work.
	retirementController atomic.Pointer[RetirementController]
	// metaSaveMu serializes each metadata snapshot with its write. It is acquired
	// before mu by maybeAutoSave, preventing an older snapshot from waiting behind
	// and then overwriting a newer save from another goroutine.
	metaSaveMu sync.Mutex

	// jobTreeShapeUnsaved records that the activity clock moved for a change
	// in the tree's shape (a delegate created) whose new revision has not
	// reached the session's metadata yet — see noteJobTreeShapeChange.
	jobTreeShapeUnsaved atomic.Bool
	// goalUpdateMu serializes each goal-store mutation with the GOAL_UPDATED event
	// that announces it. It is always acquired before mu; emit runs after mu is
	// released, so observers see mutation order without event emission under mu.
	goalUpdateMu sync.Mutex
	// notesUpdateMu serializes each shared-notes store mutation with the event
	// that announces it, mirroring goalUpdateMu: the mutation and its
	// snapshot capture run under this lock plus mu, and emit runs after both
	// are released, so concurrent saves publish in store order and a stale
	// event never wins at the projector.
	notesUpdateMu sync.Mutex

	// --- native worktree occupancy (spec §7) ---
	//
	// worktreeRestoreEnv is the env saved the first time the session entered a
	// worktree via manage_worktree; exit/remove restore to it. Nil when the
	// session has never entered a worktree through the tool (single saved env,
	// not a stack — spec §7 "env-restore model").
	//
	// worktreeCurrentPath is the worktree path the session currently occupies
	// (empty when at the restore/main root) — managed or path-entered, both
	// (spec §7 "Persistence and resume"). It drives the create-away leave
	// (spec §3 step 7) and the occupancy-lock choreography.
	//
	// worktreeCurrentManaged is true when worktreeCurrentPath is a
	// evener-managed worktree (entered via create, or switch by name/managed
	// path) rather than a non-managed path-entered one — it gates whether the
	// occupancy-lock rule applies and is persisted alongside worktreeCurrentPath
	// (SessionMeta.WorktreeManaged, spec §7).
	//
	// worktreeGitVersionOK memoizes the once-per-session `git version` preflight
	// (spec §3 step 6) so the floor check forks git at most once.
	//
	// worktreeLiveWorkStub is a test-only seam for the remove/prune live-work
	// guard (spec §5 remove step 4): when set, liveWorkUnder calls it instead
	// of its production job-manager/subagent scan (session_tools_worktree.go).
	// Nothing in production code ever sets this field.
	//
	// worktreeDisposeBeforeRemove is a test-only seam for close-time lane
	// disposal (spec §9 step 4): when set, disposeOneDelegateLane calls it with
	// the lane path immediately before the non-force `git worktree remove`, so a
	// test can dirty the lane and exercise the racing-dirty-write downgrade.
	// Nothing in production code ever sets this field.
	//
	// worktreeLaneStat is a test-only seam for the post-refusal present/gone
	// classification in disposeUnchangedLaneMechanics: when set, that stat of
	// <lanePath>/.git routes through it, so a test can inject a non-ENOENT stat
	// failure (EIO/EACCES) and prove a transient error takes the conservative
	// KEEP path rather than being misread as a gone lane. Nil in production.
	//
	// worktreeBeforeStrandedMarkerRelease is a test-only seam called with the lane
	// path immediately before prune releases a stranded own marker, so a test can
	// change who holds the lane at the one moment between deciding to release
	// and releasing. Nil in production.
	worktreeRestoreEnv                  *execenv.LocalExecutionEnvironment
	worktreeCurrentPath                 string
	worktreeCurrentManaged              bool
	worktreeGitVersionOK                bool
	worktreeLiveWorkStub                func(path string) []string
	worktreeDisposeBeforeRemove         func(lanePath string)
	worktreeLaneStat                    func(path string) (os.FileInfo, error)
	worktreeBeforeStrandedMarkerRelease func(lanePath string)
	// worktreeRendezvousDir is a test-only seam naming the rendezvous run
	// directory the owner-liveness probe consults instead of the platform
	// default (see worktreeOwnerRendezvousDir).
	worktreeRendezvousDir string

	// livelockRelease drops this root session's process livelock, taken at
	// init (acquireSessionLivelock). Nil for subagents (their root's process
	// hosts them) and for a root whose take found the flock held — a second
	// process on the same id, the documented can't-tell-apart case, which
	// then runs with liveness visibility only, never correctness.
	livelockRelease func()

	// responseSideEffectsMu serializes a response's user-visible side-effect
	// bundle (emit + appendTurn + counter bump) against teardown.
	// LOCK ORDER: responseSideEffectsMu > mu (Close acquires it before mu).
	responseSideEffectsMu sync.Mutex
	// drainAbandonedMu guards drainAbandonedChildren and drainGraceChildren. It is a lock of its own,
	// not mu, because every drain walk and every drive path reads the set while
	// holding a subagent row's lock, and mu sits above those.
	// LOCK ORDER: drainAbandonedMu is a leaf — nothing is acquired under it.
	drainAbandonedMu sync.Mutex
	// laneEvidenceMu guards the managed-job lane-evidence hold: the count of
	// this session's running managed jobs while it is a delegate child, and
	// the evidence ticker the first job started. A leaf like drainAbandonedMu —
	// the job manager calls in and out of it at lifecycle transitions.
	// LOCK ORDER: laneEvidenceMu is a leaf; the hold's ticker goroutine never
	// re-enters it.
	laneEvidenceMu   sync.Mutex
	laneEvidenceJobs int
	laneEvidenceStop func()
	// drainAbandonedChildren maps a direct child SESSION id to the exact
	// delegate generation the drain gave up waiting on. A resumed generation
	// under the same child session must not inherit the old gate.
	drainAbandonedChildren map[string]drainAbandonedChild
	// drainGraceChildren holds delegates whose first grace window completed and
	// whose second continuous-stall window is in progress. It is transient to one
	// DrainJobTree invocation and generation-scoped for the same reason as the
	// final abandonment record.
	drainGraceChildren map[string]drainGraceChild
	// childCommittedSendMu guards childCommittedSendChildren. It is a leaf lock of
	// its own — nothing is acquired under it — and is deliberately separate from
	// drainAbandonedMu so the send-start claim never couples to drain state.
	// LOCK ORDER: leaf.
	childCommittedSendMu sync.Mutex
	// childCommittedSendChildren holds the child SESSION ids with a committed send
	// start whose owning run has not yet taken the generation over. The claim is
	// keyed by id, not by the child object, so it covers the stretch from
	// CommitStart through restoreIdleForSend and
	// admitReconstructed/AttachRuntime, before the child is drivable (#940). It is
	// held together with (never instead of) the per-subagent `driving` claim: the
	// id-keyed claim covers the pre-resolve stretch, `driving` covers the
	// post-resolve stretch and the handoff to the run.
	childCommittedSendChildren    map[string]struct{}
	toolEventsWG                  sync.WaitGroup                       // in-flight ToolCallStart/End emit pairs; Close() joins before closing events
	sendersWG                     sync.WaitGroup                       // detached event emitters (subagent runs, session namer); Add happens under mu gated on closing so it happens-before Close()'s join
	disposeWG                     sync.WaitGroup                       // in-flight in-turn dispose ops (manage_worktree op=dispose); admitted via beginDispose() under mu gated on closing so the Add happens-before Close()'s join, then Close() joins before draining (spec §P1)
	disposeRetirement             []func()                             // anonymous same-session admissions; guarded by mu, including work begun before controller attachment
	sweepWG                       sync.WaitGroup                       // in-flight P3 open-pass residue sweeps; the open timer callback Adds under mu gated on closing so the Add happens-before Close()'s join, then Close() joins before its own disposal (spec §P3)
	envWork                       map[envWorkID]envWorkRecord          // admitted work that runs commands on the session's environment: a whole manage_worktree call (admitted at its dispatch), a swap's refresh (swapEnvAndRefresh), the cut of a delegate's isolation lane and the rollback that undoes it (prepareIsolation), and the deferred rollback a refused or failed op still owes after that swap returned; admitted under mu gated on closing so the admission happens-before Close()'s join, which Close() runs after its dispose and sweep joins and BEFORE the delegate-tree close, its own lane cleanup, the store closures and the environment cleanup — everything the admitted work is still using. Each record's label lets a close whose bounded join gives up name what it walked past; guarded by mu
	envWorkDrained                chan struct{}                        // closed when the last envWork admission ends, and nil while none is live, so Close()'s join waits on exactly the admissions envWork records; guarded by mu
	abandonedEnvs                 []*execenv.LocalExecutionEnvironment // environments swapped away from that are neither current nor parked (the clone between two enters); a child sharing one can still mint scratch on it, so close retains each; one entry per environment; guarded by mu
	envWorkSeq                    uint64                               // last envWork handle issued; guarded by mu
	laneSweepTimer                clock.Timer                          // one-shot P3 open-pass timer (top-level local sessions only), armed at open and stopped at close; guarded by mu
	laneReLockRetryTimer          clock.Timer                          // one-shot resume re-lock retry timer for a restored subagent coordinator (which has no P3 open timer to piggyback on); armed at resume when a re-lock failed, stopped at close; guarded by mu
	pendingReLock                 []isolationLane                      // own undisposed lanes whose resume re-lock failed and await one retry (P3 open timer for top-level, laneReLockRetryTimer for a subagent coordinator); guarded by mu
	state                         SessionState
	closing                       bool
	turns                         int       // user input count (for MaxTurns enforcement)
	turnBudgetWarningEmitted      bool      // five-turn warning has been queued or recovered from history
	modelResponses                int       // LLM round-trip count (for meta.json turn_count)
	createdAt                     time.Time // stamped once at construction/restore; Meta() reads it rather than re-stamping "now" on every call
	workMillis                    int64     // accumulated wall-clock work time across turns; seeded from SessionMeta on restore, mapped out via Meta()
	turnStartedAt                 time.Time // wall-clock instant the current turn began (stamped at the processing-begin transition); zero when no turn is in flight. Guarded by mu, like workMillis.
	lastTurnEndedAt               time.Time // when the last turn ended (stamped by endTurnLocked); seeded from SessionMeta on restore, mapped out via Meta(). Guarded by mu, like workMillis.
	lastMessage                   string    // the opening of the last agent message the session wrote (S1d, noteAgentMessageLocked); seeded from SessionMeta on restore, mapped out via Meta(). Guarded by mu.
	turnHistoryBaseline           int       // history index one past the opening user turn of the in-flight turn (captured when that turn became durable, or at round 0 for a turn that appended none; adjusted for mid-turn compaction). Turns at or after it are exempt from N4 replay-provenance filtering (fallback rounds keep today's replay semantics). Guarded by mu.
	turnBaselineCaptured          bool      // the opening user turn captured turnHistoryBaseline when it became durable, before UserPromptSubmit could admit steering; round zero keeps that boundary instead of overwriting it with one that already includes the steer. Cleared at each input's start. Guarded by mu.
	turnInputCarriesImages        bool      // the input the in-flight turn accepted carried image attachments. Their bytes stay on the outbound request even for an image-blind target, so the provider can answer them with its actionable refusal; every older user image is projected out for such a target (#7629). Guarded by mu.
	history                       []schema.Turn
	historyRevision               int           // bumped by every publishFoldedHistory publish and every other non-append history mutation (orphaned-tool-result repair, attention-turn replace/remove — see bumpHistoryRevisionLocked), never by an ordinary append. Lets a fold snapshot detect whether a competing publish OR mutation already happened since it started, distinct from the ordinary concurrent appends publishFoldedHistory's merge-back already tolerates. Guarded by mu.
	persistedAppendLog            []schema.Turn // persisted transcript forms of the append/write pairs since the last fold publication, in append order — the exact forms publishFoldTransaction re-appends after its markers. Pruned wholesale by each successful publication. Guarded by mu.
	persistedAppendLogBase        int           // count of pair appends already pruned from persistedAppendLog by fold publications; base+len(log) is the total pair-append count a fold snapshot captures as snapAppends. Guarded by mu.
	newestPublishedFoldRevision   int           // publication sequence (historyRevision at publish) of the newest fold publication, set inside publishFoldTransaction's s.mu window. Last-write-wins deferred effects (compaction naming, task/artifact steering) are suppressed — at flush time and again at async naming completion — for any fold with an older publication revision: suppression binds to PUBLICATION order, never flush order, so an older fold flushing while the newest is published-but-unflushed stays silent, and the newest fold's own flush can never be suppressed. Guarded by mu.
	responsesContinuationDisabled map[responsesContinuationDisabledKey]bool

	// requestMeasurement is the history prefix and request shape the round's
	// model request was prepared from. recordResponseUsage records the
	// provider's input count against it, so a reply that settles after a rewrite
	// cannot charge its count to history it never sent. Guarded by mu.
	requestMeasurement requestMeasurement

	// currentRoundRecorder is the in-flight round's salvage recorder: per retry
	// group, every attempt's stats and the best partial that group streamed.
	// Installed fresh at each round's start (so a round that fails before its
	// model call cannot settle on the previous round's partial) and read by the
	// settlement path after a terminal failure. Guarded by mu.
	currentRoundRecorder *roundRecorder

	// salvagedTurnRound is the totalRounds value at the moment
	// persistSalvagedTurn (Component 3 settlement) most recently appended a
	// salvaged assistant turn to this session's transcript; 0 means never.
	// Overwritten on every salvage, never cleared otherwise. A delegating
	// parent compares it against totalRounds (hasSalvageFromFinalRound) to
	// tell a salvage that explains why the session ultimately stopped from a
	// stale one the session ran past — see hasSalvageFromFinalRound. Guarded
	// by mu.
	salvagedTurnRound int

	fork forkInfo

	reg *tool.Registry

	// closeDrainWarnings holds the lost-result warnings a close drain settled
	// whose bounded publication expired before they reached the stream
	// (session_events.go). The receipts behind them are already abandoned, so
	// these cannot be re-settled: the close gets one retry on the budget left
	// after the worktree removals, and this is the only place they survive.
	closeDrainWarnings atomic.Pointer[[]events.WarningData]
	// closeDrainHooksWG tracks the Notification hooks a close drain dispatched
	// asynchronously on the live cascade (session_events.go). The hook's command
	// may run for as long as its own timeout, so it must not hold the worktree
	// removals that follow the publication; Close joins it here after those
	// removals and before the retries and teardown it can still publish through.
	closeDrainHooksWG sync.WaitGroup

	steeringQueue []steeringMessage
	// steeringInFlight holds the client steers popSteeringHead has taken out of
	// steeringQueue whose incorporation the store has not yet recorded
	// (consumeSteeringMessage finalizes it after the transcript append lands).
	// The store keeps such a steer accepted until then, so
	// reflectDurableClientSteering must not put it back in the queue. The value
	// is "" while the append is in flight, and the steer's terminal state --
	// "incorporated", or "failed" for a skill selection that could not be
	// prepared -- once the transcript holds it and only the store's write
	// failed: recorded, and marked by reconcileRecordedSteering at the input's
	// settle, the next wake, a Stop or restore. Guarded by mu.
	steeringInFlight map[string]string
	// steeringParked is set when an attempt to run pending user steering failed
	// short of the model -- the append refused, the failure record refused,
	// the carrier claim's write refused -- and cleared by the next wake sender
	// (wakePendingUserInput: an accepted client mutation, attach, a store write
	// that landed). While set, a wake the daemon had already buffered before
	// the failure stands down instead of spending a second attempt on the same
	// steer, and no autonomous turn runs while the steer is queued -- the
	// drain ladder's notification and goal rungs, a deferred continuation,
	// the settle's goal kick and the entry gate for a daemon-started
	// notification or continuation all stand down (steeringParkedNow) -- so
	// nothing drains it under a turn id that is not its receipt's. Guarded by
	// mu.
	steeringParked   bool
	visionTurnOwners []*struct{ _ byte }

	// activeProvenance is the causal provenance carried by the input currently
	// being processed. It is stamped onto every event the turn emits, reset to
	// empty on each new external top-level input, and unioned with consumed
	// steering messages' provenance. The zero value means "no watch origin".
	activeProvenance provenance.Causal
	// completedInputProvenance is the active provenance captured at the most
	// recent processing boundary. Subagent follow-up turns use it to preserve
	// watch keys accumulated during the just-finished run.
	completedInputProvenance provenance.Causal

	// inputQueue holds messages submitted via Enqueue while a turn is in
	// flight. Kata 111a: text typed during a running turn returns to the
	// user immediately and is processed as a fresh user turn once the
	// active turn completes. DrainAsSteer (kata 0bq1) collapses any queued
	// messages into a single steering message sent to the in-flight turn.
	// Each entry carries text plus any attached images (kata t5j6) so the
	// composer can queue image-bearing messages alongside text.
	inputQueue []queuedInput
	// queueEventsMu makes an inputQueue mutation and its EventQueueChanged emit
	// atomic to external observers (the client-mutation queue handlers/pop/pushQueueHead).
	// LOCK ORDER: queueEventsMu > mu.
	queueEventsMu sync.Mutex
	// publishedQueueRevision is guarded by queueEventsMu and prevents a late
	// publisher from installing a queue projection older than one already
	// emitted.
	publishedQueueRevision uint64
	// queuePersistMu serializes persistQueuesSnapshot's own snapshot-then-write
	// critical section. inputQueue's mutators already serialize end-to-end via
	// queueEventsMu (held across their persist call too), but steeringQueue's
	// mutators (trySteerEnqueue/drainSteering/prependSteering) release mu before
	// persisting and share no other lock — without this, two concurrent
	// steering mutations could race their disk writes: the slower call's
	// snapshot (read before the lock, in a naive design) could land on disk
	// AFTER a faster call's newer one, reverting the file to a stale state even
	// though both mutations are safely applied in memory. Nesting is strict and
	// one-directional (queuePersistMu, then briefly mu, for the snapshot copy;
	// never the reverse, and never held across any other Session lock), so it
	// cannot invert against queueEventsMu > mu or responseSideEffectsMu > mu.
	queuePersistMu sync.Mutex
	// clientMutations owns the durable retry-safe mutation journal and its
	// materialized client-side queue projection. It has its own serializer;
	// callers must not hold Session.mu while reserving or updating it because
	// persistence performs filesystem I/O.
	clientMutations       *clientMutationStore
	clientMutationsInitMu sync.Mutex
	// clientMutationTranscriptAppend is a deterministic test seam for the
	// durable append boundary. Nil in production.
	clientMutationTranscriptAppend func(schema.Turn) error
	// clientMutationPreAppendFailure injects a deterministic lifecycle failure
	// after a start is claimed but before its user item can be incorporated.
	clientMutationPreAppendFailure func(schema.Turn) error
	// clientMutationFailureRecoveryFault is a deterministic crash seam around
	// the two transcript appends used to materialize a failed accepted start.
	clientMutationFailureRecoveryFault func(string) error
	// clientMutationInterruptJoined observes a same-ID interrupt owner join in
	// deterministic concurrency tests. Nil in production.
	clientMutationInterruptJoined func()
	// restoredClientMutationTurns is the complete transcript identity index
	// captured before ResumeHistory compacts model context.
	restoredClientMutationTurns map[string]string
	restoredClientMutationItems map[string]clientMutationTranscriptItems
	// recoveredTurnID is the ActiveTurnID the durable client-mutation snapshot
	// named when this process restored the session, and only when the pending
	// execution that owns it is a client turn/start. That start is the user
	// turn a dead process left mid-flight, which restore reclaims and re-runs;
	// a follow-up turn/start behind it is the user speaking again, so
	// AcceptClientMutationStart admits it once the inherited turn is actually
	// running (see recoveredTurnRunning).
	//
	// A queue-origin turn (a queued message that was mid-run at the crash)
	// deliberately leaves this empty even though it also leaves ActiveTurnID
	// set: no follow-up is admitted behind it. The claim path prefers starts,
	// so a follow-up start admitted behind an inherited queue turn would wait
	// for a claim that can never come. It stays refused, which is the pre-fix
	// behaviour.
	//
	// It is a per-process fact and is deliberately never persisted. A turn id
	// is never reused, so the field needs no clearing: once the inherited turn
	// ends, no later active turn can equal it again.
	recoveredTurnID string
	// execution is the running execution's bookkeeping (session_execution.go).
	execution executionState
	// failedClosed is set when a served session fails closed
	// (session_fail_closed.go).
	failedClosed failClosedState
	// transcriptCreateErr is why the session's transcript could not be
	// created, nil when it was (or none was wanted). Set once at construction.
	transcriptCreateErr error
	// userInputEntry is the 1-based entry index of the latest USER_INPUT
	// entry the session recorded, 0 before one is; guarded by mu.
	userInputEntry int
	// lastRecorded is the entry ordinal of the session's latest transcript
	// write, unset when it recorded nothing; guarded by mu and meaningful only
	// inside the attentionMu hold that made the write.
	lastRecorded recordedOrdinal
	// openPendingExecutions holds the executions restore found open and left
	// open because pending client work owns them; guarded by mu.
	openPendingExecutions map[string]bool
	// recordedExecutions holds the TurnID of every execution turn this
	// session's transcript records, so one that runs again reopens; guarded
	// by mu.
	recordedExecutions map[string]bool
	// roundID names the open model round, "" when none is open; guarded by
	// mu. See roundIDForModelCall.
	roundID         string
	roundAuthorship *schema.TranscriptAuthorship
	// lastRoundID is the latest round the session opened, and lastRoundEnded
	// whether its EventRoundEnded was emitted; guarded by mu.
	lastRoundID    string
	lastRoundEnded bool
	// sessionStarted is set once SESSION_START is emitted; guarded by mu.
	// Restore can run an execution before then; it is recorded whole before
	// any consumer learns the session exists, so it is not announced.
	sessionStarted bool
	// executionStarted is called with each execution's TurnID before its
	// first entry is recorded; guarded by mu. See SetExecutionStartedFunc.
	executionStarted func(turnID string)
	// transcriptRecorded is the recorded-entry hook installed on every writer
	// the session attaches; guarded by mu. See SetTranscriptRecordedFunc.
	transcriptRecorded func(transcript.Record)
	// holdTranscriptPublications, while set, stops recordTranscriptLocked
	// from publishing each recorded entry's length: the fold publication
	// transaction holds every publication while it may still take its own
	// lines back out (#1158), then publishes the one settled boundary — the
	// published-length handoff is monotone, so an intermediate length that
	// vanished with a retract must never reach it. Guarded by attentionMu,
	// the door every write it gates runs under.
	holdTranscriptPublications bool
	transcriptFrontier         transcriptFrontierBinding
	// executionFrontierMu is a leaf: the append hook never acquires Session.mu
	// or emits while holding it. Sampling takes mu before this leaf.
	executionFrontierMu sync.Mutex
	executionFrontier   events.ExecutionFrontier
	frontierWake        chan struct{}
	frontierPending     []string
	// frontierDeferred holds executions the history fenced behind a pending
	// publication. They enter frontierPending only at that publication handoff,
	// so a buffered wake cannot emit a turn the history cannot read yet.
	frontierDeferred []string
	frontierStopped  bool
	// Armed only by constructors, never by bare Sessions or runtime writes.
	frontierStartupPending bool
	frontierEmitterWG      sync.WaitGroup // lifetime worker, joined at the event-stream close boundary
	// transcriptPublished runs after adoption, outside mu and the append lock,
	// while attentionMu still excludes the next canonical mutation.
	transcriptPublished func(int64, *appwire.ConversationProvenanceState)
	// recoveredTurnClaimReturned bounds the recovered turn's give-back to ONE
	// in-process retry. The first failure of the inherited turn before its prompt
	// is recorded hands its claim back, and the runner wake drives the immediate
	// retry; a SECOND consecutive failure of the same turn leaves the claim
	// claimed, so restart recovery owns it rather than the process spinning on a
	// failure that is plainly not transient. Like recoveredTurnID it is a
	// per-process fact and is deliberately never persisted -- the turn id it
	// guards is never reused, so it needs no clearing.
	recoveredTurnClaimReturned bool
	// clientMutationAppendedTurn flags that a restore-time client-mutation
	// recovery appended turns to the transcript file. Restore consults it
	// after the recovery pass to decide whether the retained transcript
	// entry list must be refreshed from disk. Guarded by mu.
	clientMutationAppendedTurn bool
	// clientMutationStartWake is installed by the lifecycle runner. Start
	// acceptance invokes it only after the durable mutation commit; setting it
	// after restore immediately wakes any accepted start already owned by the
	// journal. Guarded by mu.
	clientMutationStartWake func()

	// pendingUserInputWake is installed by the lifecycle runner, beside
	// clientMutationStartWake and for the same reason: a durable mutation the
	// journal already owns needs something to ask the session to run it. It
	// exists separately from notifyFunc because the two carry different
	// authority -- a notification is autonomous work, and the entry gate
	// refuses those while a question is pending, whereas queued input is the
	// user speaking and is entitled to run. Guarded by mu.
	pendingUserInputWake func()

	// communicate/result tool state (transient, reset each processOneInput call)
	comm communicateResult
	// stopHookActive is the Stop hook's stop_hook_active, per TURN rather than
	// per process: it is false at a turn's first Stop dispatch and true on any
	// dispatch that follows a Stop hook's block in the same turn, and a tool call
	// clears it (execTool), because the work the block asked for has been done.
	// Cleared with comm when the next turn starts. Atomic rather than mu-guarded:
	// a tool call clears it, and a parallel read-only tool batch must not
	// serialize on the session-wide lock to do so.
	stopHookActive atomic.Bool
	// toolHookStop carries a PostToolUse or PostToolUseFailure hook's
	// continue:false out of execTool to the round loop, which ends the turn the
	// same way PostToolBatch's block does -- after the tool results are already
	// persisted. The FACT and the REASON are separate fields because
	// `{"continue":false}` with no stopReason is legal Claude: a bare string
	// would make "stopped, no reason" indistinguishable from "not stopped".
	// execTool runs one call at a time on the round's goroutine (or several in
	// parallel), so the first stop wins and later ones do not overwrite it.
	// Consumed and cleared each tool-executing round. Guarded by mu.
	toolHookStop       bool
	toolHookStopReason string
	// promptHookStop carries a UserPromptSubmit hook's continue:false out of
	// acceptUserInput to the turn loop, which ends the turn before the first
	// model request: the prompt never reaches the model. The reason is already
	// delivered to the user. Consumed and cleared when the turn starts. Guarded
	// by mu.
	promptHookStop string
	// sessionStartHookStop carries a SessionStart hook's continue:false to the
	// first turn. A startup dispatch runs during session init, before any turn
	// exists, and a deferred resume dispatch runs inside the turn that triggered
	// it, so this is kept separately from promptHookStop and is NOT cleared at
	// turn start: it must survive until the turn it stops consumes it. The
	// reason is already delivered to the user. Guarded by mu.
	sessionStartHookStop string
	// hookStoppedInput records that a UserPromptSubmit or SessionStart
	// continue:false ended the whole input, not just the turn. The stop returns
	// ordinary success, so without this the drain would fold the goal gate and
	// schedule a continuation, which runs without those hooks and sends the
	// stopped prompt to the model. Set where the stop is consumed and cleared at
	// the start of every input. Guarded by mu.
	hookStoppedInput bool
	// hookStopReason is the reason a prompt stop recorded, kept for a delegate's
	// terminal packet so it explains why no work ran. Guarded by mu.
	hookStopReason string
	// stopHookReplacedReply records that a Stop hook's continue:false replaced
	// the reply this turn would have delivered. A delegate's terminal packet must
	// carry the hook's text, not the communicated reply it vetoed. Cleared at the
	// start of every turn. Guarded by mu.
	stopHookReplacedReply bool
	// dispatchedToolArgs holds each call's arguments as they were dispatched,
	// after the repair step and any PreToolUse updatedInput. PostToolBatch
	// renders these, so a hook sees the arguments the tool actually ran with
	// rather than the model's original bytes. Replaced each round and guarded
	// by mu.
	dispatchedToolArgs map[string][]byte
	// turnInterrupted records that the turn which just ran ended on a
	// cancellation (an interrupt, a signal, a shutdown). It is set in the
	// interrupt branch before the turn settles idle, because that settle would
	// otherwise make a killed working turn look like an idle prompt: the state
	// becomes SessionIdle and the pending ask is cleared, so the close could
	// not tell them apart afterwards. Monotonic within a turn and cleared at
	// the next turn's start. Guarded by mu.
	turnInterrupted bool
	// latestReport is the most recent terminal communicate this session
	// accepted. Unlike comm it survives the next input's reset, so a drain
	// can restore the report that input erased.
	latestReport communicateResult
	// restBeforeInput is the resting state the session had when the current
	// input started, read only by finishNotificationNoop: a notification wake
	// that turns out to have nothing to deliver runs no turn, so it settles
	// back to it. Guarded by s.mu.
	restBeforeInput SessionState

	// terminalCommunicateAccepted latches that a communicate with
	// next="send_and_end_turn" completed a turn while TurnEndsProcess: the model has
	// explicitly ended the turn that ends the process. Unlike comm it is never
	// reset — the one-shot drain reads it to abandon residue with no live work
	// behind it, and the round loop reads it to finish an empty post-terminal
	// notification turn idle instead of retrying (issue #329). A completion
	// the model was never shown is still delivered after it; watch
	// notifications queued before it are cut when the drain starts (#865).
	// Guarded by mu.
	terminalCommunicateAccepted bool

	// inputsInFlight counts the inputs processInputKindWithProvenance is
	// running. Lock-free, so the delegate controller can read it under its
	// own mutex: a runtime with an input in flight (a drive turn waiting out a
	// usage limit, say) is not quiescent and is never reclaimed.
	inputsInFlight atomic.Int32

	// quotaWait is the model call waiting out a provider usage limit, nil
	// while none waits (session_quota_wait.go). quotaWakes counts the asks
	// (a user message, a model switch) for such a wait to retry now.
	// quotaWaitStamps counts the wait identities this runtime issued. Its
	// value is the sequence part of the most recently issued stamp, minted
	// inside the durable boot generation's range (session_quota_wait.go), so
	// a wait always compares greater than an earlier one even when wall-clock
	// instants collide or step back.
	// steerArrivals counts accepted user steering and steerArrivalsDrained is
	// its value as the latest steering drain began, so a wait can tell a
	// steer that may not be in its request. Guarded by mu.
	quotaWait            *quotaWait
	quotaWakes           uint64
	quotaWaitStamps      uint64
	steerArrivals        uint64
	steerArrivalsDrained uint64

	// askPending is the per-turn pending set of questions posted by ask_user
	// calls this turn (spec §5.1): its length lets a round-boundary check tell
	// whether the round just posted question(s). The transcript remains the
	// durable, renderable record of the questions themselves; this slice exists
	// only for that bookkeeping. Guarded by mu, like comm above. A restored
	// session rebuilds this set from the transcript tail at restore time
	// (deriveRestoredAskPending, session_init.go) so the holds keyed on it —
	// the entry gate, the goal engine's arm-don't-kick paths, Compact's guard
	// — survive a restart (ask-attention-tiering spec §2); it is not itself
	// part of persisted SessionMeta.
	askPending         []askQuestion
	askPendingRevision uint64
	bootGeneration     string

	// askPendingCallArgs holds each pending ask_user call's own arguments
	// (already normalized, spec §5.1's shape), one entry per call, in call
	// order — appended alongside askPending by registerAskTool's Exec and
	// cleared alongside it (clearAskPending). It exists for an external
	// ask-responder (evener run --ask-responder, PendingAskArguments) that
	// needs the full question detail (option details included) a live call
	// carried: Session.Events() is best-effort (session_events.go: "a full
	// buffer drops"), so a caller for whom missing a call would leave state
	// permanently wrong — a real pending question it never answers — must
	// read durable session state instead of the event stream. Guarded by
	// mu, like askPending above. Not persisted or restored: a resumed
	// session's pending questions are read back from askPending/the
	// transcript, and this feature does not (yet) support --resume.
	askPendingCallArgs [][]byte
	askPendingCallIDs  []appwire.PendingAskCall

	// steeringCarrierClaimClientMutationID is the client mutation id of the
	// steer a claimed steering-carrier turn (acceptSteeringCarrierInput) is
	// currently draining, set for the duration of that one call. It tells
	// recordFailedSteeringSelection (session_queue.go) that a selection
	// failure for THIS client mutation id is the carrier's own claimed steer
	// — whose mere acceptance already cleared askPending, so the TurnFailure
	// it records must be tagged schema.TurnFailureInfo.SteeringCarrier too.
	// turnResolvesAskBoundary reads that tag as a resolution boundary on
	// restore.
	// Guarded by mu, like askPending above.
	steeringCarrierClaimClientMutationID string

	// steeringCarrierStoodDowns is the set of steering-carrier turn ids that
	// stood down without running a model turn (processOneInput's swallow of
	// errSteeringCarrierStoodDown). ProcessPendingUserInput reads it to report
	// ran=false truthfully for the carrier it claimed -- a stood-down carrier
	// ran nothing, so the daemon must not keep an interrupt runner armed for it
	// (issue #185 item 3). A SET, not a single slot: one ProcessPendingUserInput
	// call can process more than one carrier (the drain ladder may claim an
	// inline carrier behind the claimed one), so a later stand-down must not
	// erase an earlier one. Reset at the start of each ProcessPendingUserInput
	// carrier run; a stand-down marked elsewhere lingers harmlessly until then
	// (turn ids are unique). Guarded by mu, like askPending above.
	steeringCarrierStoodDowns map[string]struct{}

	// pendingEscalations holds one waiter per in-flight sandbox-exemption escalation
	// (M7), keyed by its opaque id — the channel its tool-exec goroutine parks on
	// plus the redacted card payload. The tool-exec goroutine registers a waiter,
	// emits the human-facing approval card, and blocks; the UI's resolve request
	// (via ResolveSandboxEscalation) sends the decision to exactly that channel and
	// removes it. Cancel-all on turn interrupt and Close drains it to deny. The
	// payload lets the daemon snapshot pending escalations onto thread/read (so a
	// fresh/other client surfaces the card on entry) and report an attention flag in
	// AppWire status (so the owning session lights up cross-session) — both HUMAN-CLIENT
	// surfaces, never the model's. Guarded by s.mu. Deliberately NOT persisted: an
	// escalation is invisible to the model and never replayed, so a crash leaves an
	// interrupted tool call for orphan-repair, not a pending escalation.
	pendingEscalations map[string]*escalationWaiter

	// subscriberCountFn probes whether a human is actually watching this thread
	// over AppWire (the daemon injects it via SetSubscriberCountFunc). A denial in
	// an interactive session with zero live subscribers is treated as final rather
	// than blocking a card no one can answer (reconciliation: zero-subscriber →
	// deny-immediately). Nil means "no probe wired" — treated as zero, so a session
	// with no server attached never blocks. Guarded by s.mu.
	subscriberCountFn func() int

	// subagents
	depth                            int
	delegationAllowance              int               // mu-guarded; allowance to grant further sub-agent delegation levels
	jobActivityClock                 *jobActivityClock // explicitly inherited projection-ordering clock
	treeCounter                      *treeCounter      // tree-wide running delegate-turn counter (spec §4)
	driveCounter                     *treeCounter      // tree-wide drive-down turn counter, budgeted separately from spawns
	subagents                        *subagentManager
	delegateRestoreBeforeSideEffects func(*Session)
	// delegateRestoreAfterCommit observes a cold attention restore after its
	// commit and before the owner chain is restored. Test-only.
	delegateRestoreAfterCommit func(delegateID string)
	// restoredMetaIsSubagent is set from the persisted meta.IsSubagent flag
	// during RestoreSessionFromMetaWithConfig, before initSessionState runs. It
	// exists because cfg.spawn is never persisted (json:"-"): a bare `serve
	// --resume <delegate-id>` restores with an empty spawn carrier, so
	// isSubagentSession() cannot rely on cfg.spawn.parentSessionID alone to keep
	// a resumed subagent from regaining root-only tools (ask_user, spec §7.1).
	// Always false on the fresh-session path. Read without a lock, like cfg —
	// it is set once before the session goes live and never mutated after.
	restoredMetaIsSubagent bool

	// restoredMetaParentSessionID is the spawn parent read from the persisted
	// meta.ParentSessionID during RestoreSessionFromMetaWithConfig, alongside
	// restoredMetaIsSubagent and for the same reason: the empty spawn carrier of
	// a bare `serve --resume <delegate-id>` would otherwise make Meta() rewrite
	// the delegate as parentless, and the hub's tree needs the pair (a session
	// flagged is_subagent with no parent id is hidden from the top level and has
	// no row to nest under). Fork lineage does NOT flow through here — it lives
	// in s.fork. Empty on the fresh-session path. Read without a lock, like cfg —
	// it is set once before the session goes live and never mutated after.
	restoredMetaParentSessionID string
	// restoredScratchTreeRoot is the root whose scratch tree a delegate resumed
	// on its own keeps its scratch in (SessionMeta.RestoredScratchTreeRoot in
	// schema). Set once during restore and read like cfg; empty otherwise.
	restoredScratchTreeRoot string

	// pendingJobNotifs is the durable per-parent queue of pending job-completion
	// notifications. It is drop-safe and drained later by a notification turn.
	// notifyFunc, when set by the server, kicks the drain; it stays nil here and a
	// nil kick is a no-op.
	//
	// Guarded by its own mutex; never taken while holding sub.mu. Where it is
	// held together with the job manager mutex, jm.mu is taken FIRST and this
	// one second.
	pendingJobNotifsMu sync.Mutex
	pendingJobNotifs   []jobNotification
	// jobStatusPolls backs the refusal of repeated job_status calls that carry no news.
	jobStatusPolls jobStatusPolls
	// jobNotifsAwaitingRequest holds the durable completions whose reminder
	// is in the transcript but which no dispatched model request has carried
	// yet. They settle as delivered when one is dispatched; until then they
	// stay owed, so a turn that ends first leaves them to the next wake, which
	// carries them without writing their reminder again. Guarded by
	// pendingJobNotifsMu.
	jobNotifsAwaitingRequest []awaitedJobNotification
	// jobNotifsDelivered counts the job notifications acceptNotificationInput
	// has delivered to the model over the session's lifetime. The one-shot
	// drain reads it across an undisposed-job announcement turn: growth means a
	// completion rode that request, so the reply is an answer, not
	// housekeeping. Guarded by pendingJobNotifsMu.
	jobNotifsDelivered uint64
	// notifyWakeHolds counts in-flight holdJobNotificationWake holds, and
	// notifyWakeDeferred records that a wake was suppressed while held. Guarded
	// by pendingJobNotifsMu.
	notifyWakeHolds    int
	notifyWakeDeferred bool
	notifyFunc         func()
	jobNotifyRetry     backoffTimer
	// jobNotifyCallbacks counts in-flight scheduled notification retry
	// callbacks, including callbacks begun before retirement controller
	// attachment. Guarded by pendingJobNotifsMu.
	jobNotifyCallbacks int

	jobManager *jobManager

	// detachedProcesses contains processes this session explicitly launched with
	// mode:"detached". They are not managed jobs (and must not hold the one-shot
	// drain open), but they remain session-owned for end_turn warnings until the
	// launcher's completion receipt closes. Guarded by mu.
	detachedProcesses []sessionDetachedProcess

	// context management
	contextMgr *contextmgr.Manager
	strategy   contextmgr.Strategy
	// strategyDegraded reports that the strategy's last AfterAction returned
	// an error. While true, a repeat is suppressed so a persistently failing
	// opt-in memory strategy cannot flood the warning channel; a successful
	// AfterAction clears it so a later failure warns again. Guarded by mu.
	strategyDegraded bool

	// skills discovered at session startup
	skills skill.Catalog
	// skillCollisions holds collision diagnostics by catalog name, built once at
	// startup and immutable thereafter. A name can displace several sources.
	skillCollisions map[string][]skill.Diagnostic
	// skillCollisionWarned records catalog names whose collision warnings have
	// already been emitted on a genuine activation this session. Guarded by mu.
	skillCollisionWarned map[string]bool

	// skillLifecycle is activation metadata, separate from the full catalog.
	// Guarded by mu; ordinary instruction bodies are never stored here.
	skillLifecycle schema.SkillLifecycleSnapshot

	// pendingSkillAdmissions holds prepared skill selections whose admission
	// did not reach durable obligations — a metadata save failure while
	// consuming a skill-bearing steering message, whose prose is already
	// durable in the transcript. The next model request drains them before
	// building anything, so the turn either carries the selected instructions
	// or fails visibly; a restore covers the same window from the durable
	// input record (reconcilePendingSkillSelections), and this list is the
	// in-process retry. Guarded by mu.
	pendingSkillAdmissions []*skillActivationBatch

	// MCP server connections
	mcpMgr   *mcp.Manager
	mcpTools []llm.ToolDefinition
	// startupMCPProjectEvidence is the project layer THIS session read at its
	// own startup, populated by initMCP from the same discovery pass that
	// supplied its servers. It is what a sandboxed delegate this session
	// constructs may expand through the process environment: the layer the
	// launch already read, and not one a repository wrote into the workspace
	// afterwards. Written once, under mu, during construction; read when a child
	// is built.
	startupMCPProjectEvidence mcpconfig.LayerEvidence

	// Plugin-provided components
	plugins             []plugin.Instance
	pendingPluginEvents []events.PluginLoadedData
	pendingHookWarnings []events.WarningData
	pendingMCPWarnings  []events.WarningData
	// pendingInstructionWarnings holds the construction-time diagnostic that
	// instruction docs overflowed their budget, until SESSION_START releases it.
	pendingInstructionWarnings []events.WarningData
	// pendingTranscriptWarnings holds construction-time warnings that are
	// genuine and model-facing (transcript creation failing open in NewSession,
	// a held SessionStart-hook turn failing to flush in attachTranscript, and
	// full output retention being unavailable because the artifact store could
	// not be created) until emitSessionStartEnvelope releases them, same as the
	// three plugin/hook/MCP buffers above and for the same reason (kata et0x):
	// SESSION_START is the stream's promised first event, so nothing
	// construction-time may reach a client before it. Flushed via s.emit (not
	// emitDiagnosticWarning) because these are genuine, model-facing warnings —
	// unlike the other three, which are config diagnostics that must not fire
	// the Notification hook.
	pendingTranscriptWarnings []events.WarningData
	hookRunner                *hooks.Runner
	// pluginCommands is the union of every loaded plugin's slash commands
	// (namespaced "plugin-name:command-name") and all evener-wide commands
	// (bare-name keys from .evener/commands and the user-global config dir).
	// Looked up by expandSlashCommand via plugin.ResolveCommand.
	pluginCommands map[string]plugin.Command
	// pendingSessionStartKind defers restore SessionStart hook output until the
	// first accepted real user turn. Deferred delegate restore side effects may run
	// the hook earlier for lifecycle effects; pendingSessionStartResult preserves
	// that model-facing output for the real user turn. pendingSessionStartInFlight
	// makes restore-time execution and first-user-turn delivery mutually exclusive:
	// a user turn that arrives while restore side effects are running waits for the
	// captured result instead of running the hook again. Guarded by mu; waiters use
	// pendingSessionStartCond.
	pendingSessionStartKind     *plugin.SessionStartKind
	pendingSessionStartResult   *hooks.RunResult
	pendingSessionStartInFlight bool
	pendingSessionStartCond     *sync.Cond
	// pendingSessionStartWaitEntered is test-only instrumentation for deterministic
	// rendezvous when a user turn blocks behind in-flight restore hook execution.
	// Nil in production.
	pendingSessionStartWaitEntered func()
	pluginAgents                   map[string]plugin.Agent
	pluginMCPConfigs               []mcpconfig.ServerConfig
	// cliMCPOtherNamespaces is the registered-name prefixes the NON-CLI config
	// layers supply, recorded beside the CLI server sets it qualifies. A CLI
	// tool whose full registered name starts with one of these can also be
	// registered by a server the child keeps, so it is not CLI-only.
	cliMCPOtherNamespaces []string
	// unsupportedPluginHookEvents accumulates all Claude-recognized events
	// declared by loaded plugins that evener does not currently fire.
	// Populated by initPlugins; used by DetailedStatus for diagnostics.
	unsupportedPluginHookEvents map[plugin.HookEvent]bool

	// Tool names registered during session initialization (not custom).
	coreToolNames map[string]bool

	// worktreeDisposeOnly is set at session init for a worktree-isolated
	// coordinator (delegate spawned isolation:"worktree" that itself carries a
	// delegation allowance): the manage_worktree tool is served as the
	// dispose-only variant and the handler refuses every op except dispose
	// (delegate-lane disposal spec §P1 "Availability"). Write-once during init,
	// before the session serves any tool call.
	worktreeDisposeOnly bool

	// Project docs loaded once at session init and cached for lifetime.
	projectDocs          []ProjectDoc
	projectDocsTruncated bool

	// read-before-write guardrail
	readFiles   map[string]bool
	readFilesMu sync.RWMutex
	// priorReads remembers the line ranges already read from each read_file
	// path, with the file's state and the turn of each read. Guarded by
	// readFilesMu.
	priorReads map[string][]priorReadSpan

	// SESSION_END deduplication: emitted exactly once across ProcessInput and Close.
	// Guarded by mu; read and written only in session_end.go.
	sessionEndEmitted bool

	naming sessionName

	nameSessionFromTextFunc func(context.Context, string, string) error

	// closeOnce ensures Close() body runs exactly once.
	closeOnce sync.Once

	// Session-level cancel: Close() cancels in-flight LLM calls.
	sessionCtx context.Context
	cancelFunc context.CancelFunc

	// task list (lazy-init)
	taskStore     *task.TaskStore
	taskStoreOnce sync.Once

	// goal store (lazy-init)
	goalStore     *goal.Store
	goalStoreOnce sync.Once

	// sandboxHostFacts memoizes the host-capability probe that backs re-resolving a
	// resumed delegate's sandbox policy. RealProber.Probe forks ~3 subprocesses
	// (bwrap userns probe, bwrap --help, uname -r); a jobs listing / watch re-
	// assesses every delegate record, so an un-memoized probe forks ~3N subprocesses
	// per listing (worse when a wedged bwrap eats each 3s timeout). Host facts are
	// constant for the process, so the probe runs once per session and is shared.
	sandboxHostFactsOnce  sync.Once
	sandboxHostFactsValue sandbox.HostFacts

	// goalInTurn is true while ProcessInputKind is running an input through to
	// completion. It is guarded by s.mu and exists to close the §7 idle-kick
	// race: SetGoal/ClearGoal read it under s.mu to decide between kicking an
	// idle session and deferring to the running drain-loop gate. ProcessInputKind
	// sets it at entry and clears it as its last act before going idle, so
	// "set goal + read flag" and "clear flag + go idle" are mutually exclusive.
	// It also widens turnInFlightLocked to the whole input call, which is how
	// SetModel decides a pick arrived mid-input and must queue for the boundary.
	goalInTurn bool
	// pendingModels holds the model switches requested while an input was in
	// flight (SetModel's queue path), together with the live listing each
	// pick-time preflight fetched, so the boundary application reuses it
	// instead of enumerating again. Insertion order means nothing: the applier
	// takes the newest entry by issue (popNewestPendingModelLocked), and the
	// processing boundary applies it (applyPendingModelSwitch) so the next turn
	// runs on the newest one that survives its own deferred validation. An
	// empty slice queues nothing. Guarded by s.mu.
	pendingModels []pendingModelSwitch
	// modelPickSeq is the issue counter for model picks: SetModel stamps the
	// current value under s.mu on every admission, so each pick - accepted or
	// rejected - owns a unique, monotonic id. Guarded by s.mu.
	modelPickSeq uint64
	// modelPickAccepted is the issue of the newest pick an acceptance point
	// committed: a profile swap. A pick whose issue is older was superseded by
	// a newer accepted choice and drops at its commit check, and a queued
	// entry older than it is not applied. A store and a busy re-queue both
	// leave it alone: neither has passed the boundary's deferred history
	// check, so neither may cancel another pick. Validation failures never
	// advance it, so a rejected pick cannot cancel an earlier accepted one.
	// Guarded by s.mu.
	modelPickAccepted uint64
	// modelValidating is the pick the boundary has popped and is applying:
	// accepted, but in neither pendingModels nor the profile until the
	// application settles (commit, re-queue, drop, or retraction), every
	// outcome of which replaces the advertised pair itself. ReportedModelRef
	// keeps naming it through that window so a synchronous read racing the
	// boundary cannot regress to the pre-pick model after the acceptance was
	// published - but only while the committed watermark (modelPickAccepted)
	// has not passed it: a newer pick that commits while this one validates
	// owns the advertised pair instead, so the validating pick must not
	// shadow it. Guarded by s.mu.
	modelValidating *pendingModelSwitch
	// modelSwitchPublishMu serializes a switch application's commit through
	// its publication - the profile swap, the marker record, and the
	// EventModelChanged emit - so publications land in the order their swaps
	// committed and a committed application always tells its story: a newer
	// pick that has merely queued can still fail its own deferred validation,
	// and suppressing the committed publication on its behalf would leave the
	// session running a model no event ever named. Lock order: acquired
	// before s.mu, never the other way.
	modelSwitchPublishMu sync.Mutex
	// goalDependentsHeld records that the goal gate just held its continuation
	// because wake-pending dependents (running delegates or non-detached
	// background jobs) guarantee a future wake: the no-progress fold was
	// skipped, so the idle-settle must not immediately re-kick the same goal
	// past the same wait. Set at the gate, consumed (read-and-cleared) by
	// settleGoalOnIdle — which recomputes the predicate so a stale hold whose
	// dependents already drained still kicks — and cleared by SetGoal/ClearGoal
	// (a retarget voids a pending hold). Guarded by s.mu.
	goalDependentsHeld bool
	// kickFunc, when set via SetKickFunc, lets an idle SetGoal start the goal
	// loop immediately by feeding the first continuation prompt back into the
	// serve loop's input channel. It is a callback because the agent module must
	// not import server; serve.go wires it. Guarded by s.mu.
	kickFunc func(prompt string)

	// task reminder tracking
	taskToolLastRound int  // totalRounds value at last task_list tool call
	taskToolEverUsed  bool // whether task_list has ever been called
	totalRounds       int  // cumulative tool rounds across all inputs

	// self-compaction state (compact tool). The transient round request is the
	// per-round trigger only; the durable intent is the generation-owned
	// operation persisted on the skill lifecycle snapshot
	// (skillLifecycle.PendingCompaction), which applyPendingForceCompact reads
	// for instructions and resumeSkillCompaction re-arms after a restart.
	pinnedNoteText string // note awaiting handoff at the next compaction (agent- or elicitor-authored); injected verbatim then cleared
	pinnedNoteGen  uint64 // bumped on every pinnedNoteText set/clear/claim; lets a fold's publication claim consume exactly the note it captured, never a newer one pinned mid-fold. Guarded by mu.
	// shared-notes state (human/agent whiteboards plus URL list)
	agentNote   string              // agent's session whiteboard (a short capsule in lines); persisted via Meta().AgentNote. Guarded by mu.
	sessionURLs []schema.SessionURL // agent-curated session URL list; persisted via Meta().SessionURLs. Guarded by mu.
	// notesCommitted is the last committed notes cut — human note, agent note,
	// URL list, and ever-projected flag installed together. Readers (Meta, the
	// projection snapshot, notes_read) take one atomic load, so they never
	// straddle a mutation and never observe a value whose metadata save has not
	// landed. Publishers store a new cut only at commit points: session
	// construction/restore, after a notes mutation's metadata save returns nil,
	// when SetHumanNote commits, and when a projection marks the store as ever
	// projected. The live fields above remain the staging store for mutators;
	// this pointer is the readers' view. The value is never mutated in place.
	notesCommitted      atomic.Pointer[committedNotesState]
	pendingInstructions string // transient compaction_instructions copy for the round-tail trigger; the persisted forced operation is the durable owner
	forceRequested      bool   // a compaction is requested for this round tail (transient trigger; re-armed from a persisted forced operation at resume)
	nudgedSinceCompact  bool   // warning-nudge latch; reset on any compaction

	// elicitNoteFn overrides the note-elicitation call (tests inject a stub); nil
	// uses contextMgr.ElicitNote (Variant B of the forced-note mechanism — see
	// maybeElicitNoteBeforeCompaction).
	elicitNoteFn func(context.Context, []schema.Turn) (string, error)

	// stuck detection
	loopDetectionCount int // how many times loop detection has fired

	// transcript writer (nil when StateDir is empty, or when opening it failed)
	transcript *transcript.Writer
	// attentionMu serializes transcript-backed attention resolution for this
	// resident Session. The transcript remains the only stored attention state.
	attentionMu               sync.Mutex
	attentionCallbacks        int // guarded by attentionMu; includes pre-retirement-attachment callbacks
	delegateDeliveryMu        sync.Mutex
	delegateDeliveryCommits   map[string][]*delegateToolResultCommit
	pendingDelegateDeliveries []delegateDeliveryPlan
	// attentionFoldCursor is the incremental fold of this Session's own
	// transcript behind its attention reads. Guarded by attentionMu.
	attentionFoldCursor delegateAttentionFoldCursor
	// rootAttentionWakeIDs is a process-local wake cache keyed by unresolved
	// attention IDs from the root transcript. The transcript fold remains the
	// sole durable authority; restart rebuilds this map from that fold.
	rootAttentionWakeIDs map[string]struct{}
	rootAttentionWake    bool
	rootAttentionRetry   backoffTimer
	// rootAttentionPaused records that a permanent provider failure deferred
	// the pending root attention and the session has said so (see
	// finishRootDelegateAttentionTurn); a delivery or a model switch ends
	// the episode. Guarded by attentionMu.
	rootAttentionPaused bool
	// attentionCoveredIDs is the running turn's coverage set. Stage it per
	// round, promote it on settle, and read it at turn finish; the contract
	// lives on stagePresentedAttentionCoverage and unionCoveredAttention.
	// Reset at each turn start.
	attentionCoveredIDs map[string]struct{}
	// attentionCoverageActive marks that the session is inside a turn's model
	// round loop, so mid-turn compactions suppress attentionCoveredIDs while
	// idle between-turn compactions keep still-pending turns intact. Guarded by
	// attentionMu.
	attentionCoverageActive bool
	// attentionFoldedCovered holds pending attention turns that a mid-turn
	// compaction suppressed from re-injection because a settled round of this
	// turn already presented them (attentionCoveredIDs). If any remain
	// durably unconsumed when the turn exits, settleFoldedCoveredAttention
	// restores them into history so the retry wake still sees their content.
	// Guarded by attentionMu; reset with attentionCoveredIDs at turn start.
	attentionFoldedCovered []foldedCoveredAttentionTurn
	// attentionStagedIDs is one round's candidate coverage: the attention
	// the round's built request presents. The round loop promotes it into
	// attentionCoveredIDs only when the round's call settles, so a failed
	// or filtered round never credits an item the model saw in no settled
	// call. Every request build re-stages it.
	attentionStagedIDs map[string]struct{}
	// attentionStagedInlineDeliveries is one round's candidate coverage for
	// delegate results its request presented inline: the delivery ids named by
	// the presented tool-results turns' DelegateDeliveryCommits. A reply that
	// carried a held result as earlier_results consumes it with no attention
	// turn, so its id is the only trace of what the round presented. Promoted
	// and reset with attentionStagedIDs. Guarded by attentionMu.
	attentionStagedInlineDeliveries map[string]struct{}
	// attentionStagedDeltaInlineDeliveries is the subset of
	// attentionStagedInlineDeliveries whose tool-results turn the planned
	// responses-continuation delta carries. A delta presents only turns after
	// its anchor, so an inline delivery staged from an earlier turn was not
	// presented by this call and must not be adopted as if it were. Promoted
	// and reset with the other staged sets. Guarded by attentionMu.
	attentionStagedDeltaInlineDeliveries map[string]struct{}
	// attentionCausesMergedIDs names the attention items whose causes have
	// already joined the running turn's lineage: promoteStagedAttentionCoverage
	// merges an item's cause at the first settled request that presents it, so
	// a later request of the same turn re-presenting the item merges nothing.
	// Reset at each turn start. Guarded by attentionMu.
	attentionCausesMergedIDs map[string]struct{}
	// attentionAcceptedID is the trigger of an accepted attention generation
	// that no settled request has presented yet. The row stays in
	// delegatestore until it is presented and consumed, so this latch only
	// records which row the generation carries. Guarded by attentionMu.
	attentionAcceptedID string
	// attentionAcceptedCause is the cause of attentionAcceptedID, kept for the
	// request that presents it (promoteStagedAttentionCoverage). Guarded by
	// attentionMu.
	attentionAcceptedCause *provenance.Causal
	// attentionAwaitingResolution names the rows a settled request of an
	// earlier turn presented whose journal resolution append failed. The model
	// already read them, so they must not trigger another model request; a
	// paced retry resolves them in delegatestore, and only then may the row
	// leave PendingAttention. Guarded by attentionMu.
	attentionAwaitingResolution map[string]struct{}

	// turnNameRetryMu guards turnNameRetry alone. The paced wake it schedules
	// runs while the session goroutine is mid-stand-down, so it must not queue
	// behind s.mu -- and notify() takes s.mu, which this must never be held
	// across.
	turnNameRetryMu sync.Mutex
	// turnNameRetry paces the re-wake for a notification that stood down
	// unnamed. See scheduleRunningTurnNameRetry (session_active_turn.go).
	turnNameRetry backoffTimer
	// turnNameStoreUnhealthy latches the client-mutation-store failure warning
	// to once per unhealthy episode. See warnStoreUnhealthyOnce.
	turnNameStoreUnhealthy bool
	// releaseRetryMu guards runningTurnReleaseRetry alone, for the same reason
	// turnNameRetryMu guards turnNameRetry: the re-attempt it schedules runs on
	// the clock's goroutine and can reach notify(), which takes s.mu.
	//
	// It is deliberately not turnNameRetryMu's state. Minting and releasing the
	// name fail independently -- a release can be losing writes while nothing
	// is trying to mint, and the reverse -- and one backoff cannot pace both.
	releaseRetryMu sync.Mutex
	// runningTurnReleaseRetry paces re-attempts of a release write the client
	// mutation store refused. See scheduleRunningTurnReleaseRetry
	// (session_active_turn.go).
	runningTurnReleaseRetry runningTurnReleaseRetryState
	// outputReductionMu guards outputReductionWarned alone: the output clamp
	// the session last warned about (see warnOutputReduction), empty while no
	// clamp applies.
	outputReductionMu     sync.Mutex
	outputReductionWarned string
	// delegateAttentionWarnMu guards delegateAttentionWarned alone: per
	// failing delegate-attention action and delegate, the error the session
	// last warned about (see warnDelegateAttentionFailed).
	delegateAttentionWarnMu   sync.Mutex
	delegateAttentionWarned   map[delegateAttentionWarning]string
	delegateAttentionArmIDs   map[string]struct{}
	delegateAttentionArmRetry backoffTimer
	stableAttentionRetry      backoffTimer
	delegateDeliveryWake      bool
	delegateDeliveryPumping   bool
	delegateDeliveryRetry     backoffTimer
	// transcriptReady records that attachTranscript has run, i.e. that the
	// session has finished deciding whether it has a transcript at all. Until
	// then a turn has nowhere to go and is held in pendingTranscriptTurns; a
	// nil writer AFTER the transition is a session that will never have one,
	// and its turns are dropped rather than accumulated. Guarded by s.mu.
	transcriptReady        bool
	pendingTranscriptTurns []schema.Turn

	// Cached tool definitions.
	cachedToolDefs []llm.ToolDefinition
	// cachedToolDefsJSONBytes is cachedToolDefs' serialized length, measured
	// where the defs are built so the token estimator can price them without
	// re-serializing the list every round (#6406). It is written only where
	// cachedToolDefs is, and must describe exactly the list beside it.
	cachedToolDefsJSONBytes int
	// toolDefsGen counts cachedToolDefs rebuilds. A saved input count covers the
	// tool list and system prompt the request was measured with, so settlement
	// reads this to notice a list that changed while the reply streamed.
	// Guarded by mu.
	toolDefsGen int

	systemPromptOverride string
	cachedSystemPrompt   string
	promptSourceLog      []promptSource

	envContextPending atomic.Bool // the next request owes an environment block
	// envContextFoldRemoved is set by a fold that folded the last environment
	// block out of history. That request owes the model a restatement of it, and
	// only that request does: a fold that kept the block still deduplicates.
	envContextFoldRemoved  atomic.Bool
	envContextMCPConnected atomic.Bool // set when round-start MCP redial connects new tools
	systemPromptFrozen     bool        // cachedSystemPrompt is the immutable launch prompt
	persistFallbackPrompt  bool        // a restored header carries none, so meta records it
}

type sessionDetachedProcess struct {
	pid  int
	done <-chan struct{}
}

// runningTurnReleaseRetryState paces the re-attempts of a release write the
// client mutation store refused. It adds an attempt count to the backoffTimer
// because this loop is finite: see runningTurnReleaseRetryLimit. Nothing
// consults the timer's `active` flag -- every re-attempt is armed by the
// failure of the one before it, so there is never a second arrival to
// coalesce, and the generation alone strands a timer whose write has since
// landed.
type runningTurnReleaseRetryState struct {
	attempts int
	backoffTimer
}

const (
	jobNotificationRetryInitialDelay = 250 * time.Millisecond
	jobNotificationRetryMaxDelay     = 5 * time.Second
	// turnNameStoreRetryMaxDelay is the ceiling for the stand-down's re-wake
	// when the client mutation STORE is what refused, rather than a name
	// somebody holds. The two resolve on different timescales: a held name comes
	// back when the turn holding it ends, so seconds is right, while an
	// unwritable state dir is fixed by a human and may never be. Every re-wake
	// costs a failed MkdirAll+TempFile+Write+Sync+Rename cycle, so polling a
	// broken mount at the seconds ceiling is unbounded churn for an answer that
	// is not coming. Backing off further bounds that cost without reintroducing
	// the stranded wake that giving up entirely would (kata ajg5).
	turnNameStoreRetryMaxDelay = 5 * time.Minute
	// runningTurnReleaseRetryLimit bounds the re-attempts of a release write
	// the client mutation store refused. The stand-down's re-wake can poll a
	// broken mount forever because each poll costs only a wake; this loop holds
	// the session's turn name, which no later turn can take until the write
	// lands, so it ends -- and it ends in a diagnostic rather than in silence.
	// Eight attempts against the jobNotificationRetryMaxDelay ceiling is a
	// little over twenty seconds of trying: long enough to ride out a transient
	// write error, short enough not to sit on a state dir that will never be
	// writable (kata fbmy).
	runningTurnReleaseRetryLimit = 8
)

func (s *Session) enqueueJobNotification(n jobNotification) {
	s.pendingJobNotifsMu.Lock()
	defer s.pendingJobNotifsMu.Unlock()
	s.appendOrFoldJobNotificationLocked(n)
}

// appendOrFoldJobNotificationLocked appends n to the pending queue, or, for a
// timer tick whose watch already has a pending non-terminal entry, adds its
// fires to that entry instead. Non-timer notifications take the fast path.
// The folded entry takes the UNION of both lineages: the notification turn
// stamps the union of what it delivers, so keeping only the survivor's
// provenance would narrow the turn's lineage every time a tick folded.
// The caller holds pendingJobNotifsMu; this must never take jm.mu, because
// the established order is jm.mu then pendingJobNotifsMu.
func (s *Session) appendOrFoldJobNotificationLocked(n jobNotification) {
	if n.JobID == "" {
		// A self timer is about no one job: the recheck it schedules is news
		// for every running target.
		s.jobStatusPolls.forgetAll()
	} else {
		s.jobStatusPolls.forget(n.JobID)
	}
	if n.WatchID != "" && !n.Terminal {
		for i := range s.pendingJobNotifs {
			p := &s.pendingJobNotifs[i]
			if p.WatchID == n.WatchID && !p.Terminal {
				p.Fires += n.Fires
				p.Provenance = provenance.Union(p.Provenance, n.Provenance)
				return
			}
		}
	}
	// A long-running-jobs reminder is a snapshot of the jobs running when it was
	// built, so a newer one replaces any still undelivered rather than queueing
	// behind it listing jobs that may have finished.
	if n.isLongRunningJobsReminder() {
		s.pendingJobNotifs = slices.DeleteFunc(s.pendingJobNotifs, jobNotification.isLongRunningJobsReminder)
	}
	s.pendingJobNotifs = append(s.pendingJobNotifs, n)
}

func (s *Session) enqueueJobNotificationAndNotify(n jobNotification) {
	s.pendingJobNotifsMu.Lock()
	s.appendOrFoldJobNotificationLocked(n)
	held := s.notifyWakeHolds > 0
	if held {
		s.notifyWakeDeferred = true
	}
	s.pendingJobNotifsMu.Unlock()
	if !held {
		s.notify()
	}
}

func (s *Session) consumeJobNotification(jobID string) {
	if jobID == "" {
		return
	}
	s.pendingJobNotifsMu.Lock()
	kept := s.pendingJobNotifs[:0]
	for _, notification := range s.pendingJobNotifs {
		if notification.JobID == jobID && !notification.isWatch() {
			continue
		}
		kept = append(kept, notification)
	}
	s.pendingJobNotifs = kept
	s.pendingJobNotifsMu.Unlock()
}

// holdJobNotificationWake defers the notification wake until the returned
// release runs, which then wakes ONCE if anything was queued meanwhile.
// Notices produced by a single event belong in a single notification turn: an
// idle session woken per notice takes one turn for a watch settling and another
// for the completion that settled it, telling the model the same news twice.
//
// A hold taken for one job also defers a concurrent job's wake, which is safe:
// release always fires whatever was deferred, so a wake is delayed by the
// length of the hold and never dropped.
func (s *Session) holdJobNotificationWake() func() {
	s.pendingJobNotifsMu.Lock()
	s.notifyWakeHolds++
	s.pendingJobNotifsMu.Unlock()
	return func() {
		s.pendingJobNotifsMu.Lock()
		s.notifyWakeHolds--
		wake := s.notifyWakeHolds == 0 && s.notifyWakeDeferred
		if wake {
			s.notifyWakeDeferred = false
		}
		s.pendingJobNotifsMu.Unlock()
		if wake {
			s.notify()
		}
	}
}

func (s *Session) requeueJobNotifications(notifs []jobNotification) {
	if len(notifs) == 0 {
		return
	}
	s.pendingJobNotifsMu.Lock()
	rest := s.pendingJobNotifs
	s.pendingJobNotifs = nil
	for i := range notifs {
		s.appendOrFoldJobNotificationLocked(notifs[i])
	}
	for i := range rest {
		s.appendOrFoldJobNotificationLocked(rest[i])
	}
	s.scheduleJobNotificationRetryLocked()
	s.pendingJobNotifsMu.Unlock()
}

func (s *Session) drainJobNotifications() []jobNotification {
	s.pendingJobNotifsMu.Lock()
	defer s.pendingJobNotifsMu.Unlock()
	drained := s.pendingJobNotifs
	s.pendingJobNotifs = nil
	return drained
}

// stableChildTurnNeedsLease reports whether this turn is running on a stable
// delegate child without a run lease in context.
//
// Both notification deferral and the unleased projection filter ask it so they
// cannot drift. A lease in context is not this predicate's business: admission
// validates the lease itself against the controller, because a lease can be
// stale or fenced while still being present.
//
// The delegate identity is the structural test, not a flag: owningDelegateID is
// set exactly on a stable delegate's child session, and an ephemeral subagent
// carries none, so the ephemeral notification drive keeps its fallback. The
// controller check is what makes the refusal actionable -- without a controller
// there is no generation to hold and no admission to consult.
func (s *Session) stableChildTurnNeedsLease(ctx context.Context) bool {
	if !stableDelegateChild(s) {
		return false
	}
	if _, ok := ctx.Value(delegateRunLeaseContextKey{}).(delegateLease); ok {
		return false
	}
	return true
}

// THE RULE. A watch notice becomes model-visible only at a LEASED presentation.
// Every path that would write one into a turn's reminder must therefore either
// carry a lease or persist durable delegate attention and defer the notice, and
// it must do so BEFORE the write.
//
// The rule is about the WRITE, not the queue. Admission that reads the queue is
// blind by construction: a watch notice has no terminal generation, so
// awaitRequestForJobNotifications settles it before the request is prepared.
// Both writers -- the opening accept and the round-boundary inject -- ask here
// instead, so the split lives in exactly one place.
//
// A refusal that stops the whole turn is wrong even when it is right about the
// watch notice: a batch can hold a recovered shell completion beside a watch
// notice, and the completion has no lease to inherit but is perfectly
// deliverable. So the operation is a SPLIT, not a verdict: defer the watch
// notices to durable attention, keep the rest, and let the ordinary turn carry
// and settle what it legitimately owns.
//
// No flag distinguishes "this notice may run without a lease". The structural
// predicate is the child's ownership (stableChildTurnNeedsLease) plus the
// notice's kind (jobNotification.isWatch).

// deferUnleasedWatchNotices is THE RULE's one split point. On a leased turn it
// does nothing: the lease is the presentation a watch notice waits for, so the
// whole batch rides the turn. On an unleased stable-delegate turn it partitions
// the batch two ways:
//
//   - An ordinary notice stays in kept and proceeds normally. It has no lease to
//     inherit but is perfectly deliverable, and it is the only half some turns
//     own.
//   - Every watch notice goes to durable leased attention under the deferral
//     identity (persistDeferredWatchNotice). A leased generation then presents
//     it.
//
// Order is persist-then-suppress. Suppressing first and persisting after leaves
// a window in which the notice is neither queued nor durable, which loses it.
// A notice whose append fails is NOT left in the deliverable set: the writer
// would put it into this unleased reminder and settle it before a leased
// generation could present it, which is the leak this split exists to close. It
// goes back on the queue instead, owed and untouched, while the next attempt
// retries the append. An error the child can see beats a silently swallowed
// failure report, and the ordinary completions beside it still ride this turn.
//
// It returns the notices the turn may carry.
func (s *Session) deferUnleasedWatchNotices(ctx context.Context, jobNotifs []deliverableJobNotification) []deliverableJobNotification {
	if !s.stableChildTurnNeedsLease(ctx) {
		return jobNotifs
	}
	var retry []jobNotification
	kept := jobNotifs[:0:0]
	for _, d := range jobNotifs {
		if !d.notification.isWatch() {
			kept = append(kept, d)
			continue
		}
		appended, err := s.persistDeferredWatchNotice(d.notification)
		if err != nil {
			s.emit(warningDataFromError("persist deferred watch notice", err))
			if !appended {
				// Not durable, so it must not ride this reminder. Back on the
				// queue, owed and untouched, for the next attempt to make it
				// durable. A notice that IS durable is not re-offered here: that
				// would present it unleased as well as to the leased run that
				// row already waits for.
				retry = append(retry, d.notification)
				continue
			}
		}
	}
	s.requeueJobNotifications(retry)
	return kept
}

// deferredWatchAttentionPrefix marks the durable attention rows this half
// creates. An unleased projection reads it to hold exactly its own unresolved
// watch rows out of a request (dropUnleasedWatchAttentionTurns).
const deferredWatchAttentionPrefix = "watchdefer:"

// deferredWatchAttentionID is the durable attention identity of a watch notice
// this half defers (LIFE-01, #2340).
//
// It is derived from the notice's own durable facts so a retry of the SAME
// delivery derives the SAME id: appendDelegateNotificationDurably treats an
// identical identity and content as a no-op and a reused identity with different
// content as corruption.
//
// The watch, the job and the reason are NOT enough to name a delivery. A
// repeating timer fires the same watch, for the same job, for the same reason,
// every interval; keyed on those three alone the second fire reuses the first
// fire's resolved row, arms no wake, and the notice is silently dropped, while a
// fire whose rendered body changed ('2 times since your last turn') instead
// collides as conflicting content. So the identity carries the delivery: the
// fire's own ordinal for a timer tick, or the delivery id on the notice's
// lineage for a condition fire. A notice with neither -- a send-rail diagnostic
// -- keeps the coarser identity, because one repeated failure really is one item
// and it has no delivery of its own to tell apart.
func deferredWatchAttentionID(n jobNotification) string {
	scope := n.WatchID
	if scope == "" {
		scope = n.OriginWatchID
	}
	if scope == "" {
		scope = n.JobID
	}
	sum := sha256.Sum256([]byte(n.Reason))
	id := deferredWatchAttentionPrefix + scope + ":" + n.JobID + ":" + hex.EncodeToString(sum[:8])
	if n.DeliveryOrdinal > 0 {
		return id + ":" + strconv.Itoa(n.DeliveryOrdinal)
	}
	if deliveryID := provenance.LatestDeliveryID(n.Provenance); deliveryID != "" {
		return id + ":" + deliveryID
	}
	return id
}

// persistDeferredWatchNotice makes one watch notice durable delegate attention
// and arms its wake, so a leased generation presents it after this turn declines
// to carry it. It is the deferral half of THE RULE above.
//
// The two steps are reported separately because they fail differently. A failed
// APPEND means the notice is not durable, so the caller must keep it owed. A
// failed ARM means the notice IS already durable -- the row exists and a leased
// generation will present it when something arms it -- and returning it to the
// deliverable set would be worse than useless: the next writer would put the
// same notice into an unleased reminder while its durable row stayed pending, so
// it would be presented unleased AND presented again by the leased run. The arm
// retry owns that recovery; the caller must not re-offer the notice.
func (s *Session) persistDeferredWatchNotice(n jobNotification) (appended bool, err error) {
	attentionID := deferredWatchAttentionID(n)
	content := s.formatJobNotificationReminder([]deliverableJobNotification{{notification: n}})
	if s.delegateController != nil {
		// Register the notice's cause before the item can be presented: a
		// runaway-watch diagnostic carries the chain the breaker stopped, and the
		// response event must descend from it or the breaker reads that response
		// as a fresh trigger and fires again.
		s.delegateController.attentionCauses.remember(attentionID, n.Provenance)
	}
	if n.JobID == "" {
		s.jobStatusPolls.forgetAll()
	} else {
		s.jobStatusPolls.forget(n.JobID)
	}
	if _, err := s.appendDelegateNotificationDurably(attentionID, content); err != nil {
		if s.delegateController != nil {
			s.delegateController.attentionCauses.forget(attentionID)
		}
		return false, err
	}
	return true, s.armDelegateAttention(attentionID)
}

// peekNotifications reports how many notifications are still owed to the model
// WITHOUT draining them: those queued, and those whose reminder no dispatched
// request has carried yet. The drain-loop tail uses it to decide whether to run
// a notification turn next; the actual drain stays in acceptNotificationInput so
// the queue is consumed exactly once, inside the turn that surfaces it.
func (s *Session) peekNotifications() int {
	s.pendingJobNotifsMu.Lock()
	defer s.pendingJobNotifsMu.Unlock()
	return s.owedJobNotificationsLocked()
}

// owedJobNotificationsLocked counts the notifications a notification wake still
// has to bring to the model. The caller holds pendingJobNotifsMu.
func (s *Session) owedJobNotificationsLocked() int {
	return len(s.pendingJobNotifs) + len(s.jobNotifsAwaitingRequest)
}

// countJobNotificationsDelivered records n job notifications delivered to the
// model by the turn that just accepted them.
func (s *Session) countJobNotificationsDelivered(n int) {
	if n <= 0 {
		return
	}
	s.pendingJobNotifsMu.Lock()
	defer s.pendingJobNotifsMu.Unlock()
	s.jobNotifsDelivered += uint64(n)
}

// jobNotificationsDelivered reports the lifetime count of job notifications
// delivered to the model.
func (s *Session) jobNotificationsDelivered() uint64 {
	s.pendingJobNotifsMu.Lock()
	defer s.pendingJobNotifsMu.Unlock()
	return s.jobNotifsDelivered
}

// SetNotifyFunc registers the callback the server uses to wake an idle session
// when a job notification is pending. It mirrors SetKickFunc: the agent module
// must not import server, so serve.go wires this callback into the server's input
// channel.
func (s *Session) SetNotifyFunc(f func()) {
	s.mu.Lock()
	s.notifyFunc = f
	s.mu.Unlock()
	if f == nil {
		return
	}
	s.pendingJobNotifsMu.Lock()
	pending := s.owedJobNotificationsLocked() > 0
	s.pendingJobNotifsMu.Unlock()
	// The USER's steering and queued input both count as pending work here for
	// the same reason the rest does. Either one accepted durably and then lost
	// to a crash is rebuilt by restoreDurableClientMutationQueues, and nothing
	// else asks the session to run: the client may never retry, and the round
	// loop only drains steering inside a turn that is not going to start on its
	// own. Registering the callback is the moment a wake can provably be
	// delivered.
	//
	// Daemon-authored steering is deliberately NOT counted. It is injected
	// during startup, before the user has said anything, and it is context for
	// their first turn rather than work of its own -- so waking for it would
	// start a turn nobody asked for, ahead of the opening prompt.
	//
	// A steer parked by a Stop (SteeringHeld) is NOT counted: it is waiting on
	// the user, not work in progress, and waking for it at attach would restart
	// the session and deliver the steer the user just stopped -- the open
	// steering rail issue #174 closes (issue #146, Option C — park in place).
	if pending || s.hasRunnableUserSteering() || s.queueDepth() > 0 || s.hasPendingDelegateDeliveries() || s.pendingRootDelegateAttention() || s.hasPendingDelegateAttention() || (s.jobManager != nil && s.jobManager.hasPendingStableWatchSettlementRetry()) {
		f()
	}
}

func (s *Session) notify() {
	s.mu.Lock()
	f := s.notifyFunc
	s.mu.Unlock()
	if f != nil {
		f()
	}
}

// beginJobNotifyCallback retains local ownership of one scheduled notification
// retry callback even when the process controller is attached after that
// callback starts. Source receipt consumption and retry-generation resets do
// not settle an outstanding unlocked callback: the count registered here is
// projected by the local collector until the callback returns.
func (s *Session) beginJobNotifyCallback() (func(), error) {
	release, err := s.beginRetirementMutation("notification")
	if err != nil {
		return nil, err
	}
	s.pendingJobNotifsMu.Lock()
	s.jobNotifyCallbacks++
	s.pendingJobNotifsMu.Unlock()
	return func() {
		s.pendingJobNotifsMu.Lock()
		s.jobNotifyCallbacks--
		s.pendingJobNotifsMu.Unlock()
		release()
	}, nil
}

func (s *Session) scheduleJobNotificationRetryLocked() {
	if s.jobNotifyRetry.active {
		return
	}
	delay, generation := s.jobNotifyRetry.arm()
	s.sclock().AfterFunc(delay, func() {
		// A reset cannot cancel this one-shot. Reject an already superseded
		// firing before admission would create a lease and reset idle eligibility.
		s.pendingJobNotifsMu.Lock()
		stale := s.jobNotifyRetry.superseded(generation)
		s.pendingJobNotifsMu.Unlock()
		if stale {
			return
		}
		release, err := s.beginJobNotifyCallback()
		if err != nil {
			// Admission was refused for this one-shot firing (a real TryClaim
			// preparing window). Do not consume the only firing: clear the
			// armed flag and synchronously re-arm under the owner lock so the
			// backoff chain survives the refusal and a later firing still
			// delivers the retained source. Only a still-current generation
			// owns the armed flag: a superseded one was invalidated by
			// resetJobNotificationRetry or a newer schedule, and re-arming it
			// would resurrect a stale wake the owner already settled.
			s.pendingJobNotifsMu.Lock()
			if !s.jobNotifyRetry.superseded(generation) {
				s.jobNotifyRetry.active = false
				s.scheduleJobNotificationRetryLocked()
			}
			s.pendingJobNotifsMu.Unlock()
			return
		}
		defer release()
		s.pendingJobNotifsMu.Lock()
		// A reset may have landed between the precheck and admission.
		if !s.jobNotifyRetry.fire(generation) {
			s.pendingJobNotifsMu.Unlock()
			return
		}
		pending := s.owedJobNotificationsLocked() > 0
		s.jobNotifyRetry.settle(pending, delay, jobNotificationRetryMaxDelay)
		s.pendingJobNotifsMu.Unlock()
		if pending {
			s.notify()
		}
	})
}

func (s *Session) resetJobNotificationRetry() {
	s.pendingJobNotifsMu.Lock()
	if s.owedJobNotificationsLocked() > 0 {
		s.pendingJobNotifsMu.Unlock()
		return
	}
	s.jobNotifyRetry.reset()
	s.pendingJobNotifsMu.Unlock()
}

// communicateBoundaryPhase tracks the lifecycle of any visible terminal
// communicate/result reply within a ProcessInput call.
type communicateBoundaryPhase uint8

const (
	commBoundaryNone communicateBoundaryPhase = iota
	commBoundarySettled
	commBoundaryStopPending
	commBoundaryStopBlocked
)

// communicateResult records whether and how the agent delivered a result via
// the communicate/result tool during the current ProcessInput call. It is
// transient — reset at the top of each call, then read back by communicated,
// communicateOutput, and the turn loop's deliver step. Guarded by s.mu.
type communicateResult struct {
	phase             communicateBoundaryPhase
	updated           bool // non-terminal communicate (next="send_and_continue") was invoked this turn
	text              string
	reply             string
	output            string // canonical structured output (communicateOutput)
	structured        any    // raw args["output"] object before communicate canonicalization
	stopPendingCallID string // explicit winner whose observation awaits the Stop decision
	roundQuiet        bool   // quiet yield was accepted in the current tool round
	roundVisible      bool   // a visible terminal communicate was attempted in the current tool round
}

func (c communicateResult) hasAccepted() bool { return c.phase != commBoundaryNone }

func (c communicateResult) isUnblocked() bool {
	return c.phase == commBoundarySettled || c.phase == commBoundaryStopPending
}

func (c communicateResult) isSettled() bool { return c.phase == commBoundarySettled }

func (c communicateResult) quietBoundary() bool {
	if !c.roundQuiet {
		return false
	}
	switch c.phase {
	case commBoundaryNone:
		return true
	case commBoundaryStopBlocked:
		return !c.roundVisible
	default:
		return false
	}
}

// sessionName holds the session's auto-generated display name and its
// provenance. The namer goroutine (session_namer.go) assigns it; Meta and
// Snapshot read it. Guarded by s.mu.
type sessionName struct {
	value          string    // display name ("" until assigned)
	source         string    // provenance tag (sessionNameSource*)
	updated        time.Time // when value last changed
	set            bool      // a name has been assigned
	promptPending  bool      // a naming LLM call is in flight
	pending        int       // registered naming attempts, including compaction refreshes
	quotaExhausted bool      // current model's allowance is spent; stop naming until SetModel
}

// forkInfo records a session's fork lineage — where it diverged from a parent
// session. Set once at construction and read by Meta/Snapshot; the zero value
// means "not a fork." Immutable after construction.
type forkInfo struct {
	parentID   string // parent session ID ("" if not a fork)
	divergence int    // turn index at which this session diverged
	label      string // human-facing fork label
}

// ID returns the session's identifier.
func (s *Session) ID() string { return s.id }

func (s *Session) apiLogContext(ctx context.Context) context.Context {
	return llm.WithAPILogContext(ctx, s.id)
}

// sessionContext returns the session's own context — the one Close cancels —
// as the parent for work that must stop when the session closes. A Session
// built without one (a bare test fixture) gets a background context, so every
// caller can derive unconditionally.
func (s *Session) sessionContext() context.Context {
	if s.sessionCtx != nil {
		return s.sessionCtx
	}
	return context.Background()
}

// The contextmgr.Host seam is satisfied by the ctxHost adapter (context_host.go),
// not by *Session directly: contextmgr.Host requires exported Emit / Snapshot /
// WithResponseSideEffects methods, and we do not want those on the public Session
// surface, so the adapter forwards them to the Session's internal methods.

// StateDir returns the session's configured state directory.
func (s *Session) StateDir() string { return s.stateDir }

// Profile returns the session's current provider profile.
func (s *Session) Profile() *provider.Profile { return s.currentProfile() }

// ReportedModelRef is the model a client should be shown for this session
// right now: the newest pick accepted for the turn boundary when one is
// queued, else the pick whose boundary application is still validating and
// that no committed pick has superseded, else the committed profile's pair.
// thread/model/set's hook samples it inside the
// server's critical section, so a pick accepted mid-turn is readable from
// thread/read before its boundary commits it - the read half of the acceptance
// SetModel publishes as EventNextModelChanged - and stays readable through
// the application itself, whose settle is what replaces the pair. A pick
// newer than the validating one that commits mid-application owns the sample,
// so the pair never regresses from a committed choice to the pick it
// superseded.
func (s *Session) ReportedModelRef() (model, provider string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if newest := s.newestPendingModelIndexLocked(); newest >= 0 {
		queued := s.pendingModels[newest].profile
		return queued.Model(), queued.ID()
	}
	if validating := s.modelValidating; validating != nil && validating.seq > s.modelPickAccepted {
		return validating.profile.Model(), validating.profile.ID()
	}
	p := s.profile
	return p.Model(), p.ID()
}

// ReasoningEffort returns the session's current reasoning-effort configuration
// (already normalized by SetReasoningEffort; empty means unset/default).
func (s *Session) ReasoningEffort() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.ReasoningEffort
}

// currentProfile returns the active profile under s.mu so reads never race
// SetModel's swap (s.profile is reassigned under s.mu). Callers that already
// hold s.mu must read s.profile directly instead of calling this.
func (s *Session) currentProfile() *provider.Profile {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.profile
}

// currentEnv returns the active execution environment under s.mu so reads
// never race a future env swap (a locked swap helper reassigns s.env under
// s.mu). Callers that already hold s.mu must read s.env directly instead of
// calling this — s.mu is not reentrant.
func (s *Session) currentEnv() execenv.ExecutionEnvironment {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.env
}

// Client returns the session's LLM client.
func (s *Session) Client() *llm.Client { return s.client }

// CostFor is the $/Mtok cost of the instance/model reference ref, resolved on
// the session's own registry (spec §7.5). Nil when ref does not resolve or the
// row carries no cost — the caller renders nothing rather than a zero.
func (s *Session) CostFor(ref string) *registry.Cost {
	res, err := s.client.Resolve(ref)
	if err != nil {
		return nil
	}
	return res.Caps.Cost
}

// SetReasoningEffort updates the reasoning effort used for future LLM calls.
// Takes effect on the next request (spec).
func (s *Session) SetReasoningEffort(effort string) error {
	release, err := s.beginRetirementMutation("admission")
	if err != nil {
		return err
	}
	defer release()
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		return nil
	}
	// Normalize disable-aliases (off/false/...) to the canonical "none" so a
	// runtime off stays an explicit off through buildModelRequest, matching
	// the CLI resolver.
	s.cfg.ReasoningEffort = llm.NormalizeReasoningEffort(effort)
	normalized := s.cfg.ReasoningEffort
	s.mu.Unlock()
	s.emit(events.ReasoningEffortChangedData{ReasoningEffort: normalized})
	// Flush meta.json so a daemon crash before the next happy-path turn
	// boundary doesn't leave on-disk cfg stale. Kata wnfz. maybeAutoSave
	// re-acquires s.mu via s.Meta(), so the lock must be released first.
	return s.saveMeta()
}

// modelSwitchTarget is a switch's requested model together with the instance
// the caller named, if any. The two halves are kept apart because a model id
// may itself contain slashes: OpenRouter serves "anthropic/claude-opus-5" as
// one of its own model ids, so the first segment of a joined ref does not say
// which half was the instance. An empty provider means the caller named only a
// ref and the current instance decides (SetModel's historic reading).
type modelSwitchTarget struct {
	provider string
	model    string
}

// ref renders the target as the instance/model reference the resolver and the
// live-listing calls take.
func (t modelSwitchTarget) ref() string {
	if t.provider == "" {
		return t.model
	}
	return t.provider + "/" + t.model
}

// explicit reports whether the caller named the instance, in which case it
// decides the target outright rather than the current profile.
func (t modelSwitchTarget) explicit() bool { return t.provider != "" }

// crossProviderFrom reports whether the target moves the session off base's
// instance. An explicitly named instance decides it: the current instance may
// serve the same model id under a namespaced name, and that must not keep it.
func (t modelSwitchTarget) crossProviderFrom(base *provider.Profile) bool {
	if t.explicit() {
		return !strings.EqualFold(t.provider, base.ID())
	}
	return base.CrossProviderRef(t.model)
}

// resolveProfileForRef resolves a model ref to a *provider.Profile. When the
// ref is classified as a cross-provider switch AND the session has a resolver,
// the resolver is called. Otherwise the current profile's WithModel is used
// (handles same-provider, strip, and keep cases).
func (s *Session) resolveProfileForRef(base *provider.Profile, ref string) (*provider.Profile, bool, error) {
	return s.resolveModelSwitchProfile(base, modelSwitchTarget{model: ref})
}

// resolveModelSwitchProfile resolves a switch target to the profile it lands
// on. A target whose instance the caller named resolves on that instance, so a
// model the current instance also serves under a namespaced id keeps neither
// the model nor the instance: the reader picked the direct row. A target that
// names only a ref keeps the historic classification (CrossProviderRef, then
// WithModel).
func (s *Session) resolveModelSwitchProfile(base *provider.Profile, target modelSwitchTarget) (*provider.Profile, bool, error) {
	if target.crossProviderFrom(base) {
		var resolved *provider.Profile
		if s.resolveProfile != nil {
			var err error
			resolved, err = s.resolveProfile(target.ref())
			if err != nil {
				return nil, false, err
			}
		}
		if resolved != nil {
			if cheapModelRef := base.CheapModelRefString(); cheapModelRef != "" {
				resolved = provider.WithCheapModel(resolved, cheapModelRef)
			}
			return resolved, true, nil
		}
		if target.explicit() {
			return nil, false, fmt.Errorf("provider %q is not configured", target.provider)
		}
	}
	return base.WithModel(target.ref()), false, nil
}

// pendingModelSwitch is a switch picked mid-input, waiting for the turn
// boundary. The primed instance and listing are the ones the pick-time
// preflight fetched; the boundary's applyModelSwitch reuses them so a queued
// switch costs one live enumeration, not two. The seq is the pick generation
// SetModel stamped, which fences the boundary application against superseding
// picks. popNewestPendingModelLocked's bool reports whether anything was queued.
type pendingModelSwitch struct {
	target         modelSwitchTarget
	primedInstance string
	primedListing  *llm.ModelListing
	seq            uint64
	// profile is the enriched profile the pick resolved to, the same one the
	// at-rest path commits, and the one source of the pick's identity: the
	// pair a client is shown while the pick waits for its boundary
	// (ReportedModelRef) and the profile a commit publication that lands after
	// this pick's acceptance re-announces from, so the visible selection moves
	// back to the queue's newest choice instead of resting on the
	// just-committed model until the pick's own boundary.
	profile *provider.Profile
}

// modelSwitchFence bounds one switch application's commit to the pick that
// started it. A zero value fences nothing (the immediate test seam).
type modelSwitchFence struct {
	// pickSeq is the issue SetModel stamped on this pick. At the commit
	// point an issue that the acceptance watermark (modelPickAccepted) has
	// passed means a newer accepted pick owns the outcome: the application
	// drops instead of overwriting it.
	pickSeq uint64
	// queueWhenBusy re-queues the switch at the commit point instead of
	// committing when a turn is in flight: the at-rest pickers' "land it at
	// the next boundary" fallback, so a turn that started while the pick was
	// still validating never has the profile swapped under it. The pending
	// applier leaves it false - its own input call is the busy thing.
	queueWhenBusy bool
}

// SetModel changes the model used for future LLM calls. An explicit provider
// pins the target instance unambiguously (essential for upstream-namespaced
// model IDs like OpenRouter's "anthropic/claude-opus-5"). An empty provider
// ("") parses model as a bare model on the active instance or an
// "instance/model" ref.
//
// Takes effect on the next request. Returns an error (without changing any
// session state) when model is empty or cannot be resolved to a profile, when
// the current history contains content the target can't represent (see
// unrepresentableHistoryKinds), or when the target model fails live
// membership validation (see validateModelSwitchMembership). On a successful
// switch, cfg.ModelFallbacks entries that no longer validate against the new
// profile are dropped and reported in the model-switch marker.
//
// A switch requested while an input is in flight does not swap the profile
// under the running turn's rounds: it is validated as far as a moving session
// allows, then queued and applied at the turn boundary, so the next turn -
// typically the message the user was typing when they picked - runs on the new
// model and the running turn finishes on the one it started with. Acceptance
// publishes at once - EventNextModelChanged to subscribers, ReportedModelRef
// to thread/read - the same immediate reflection an effort change gets; the
// boundary's application then publishes the commit as EventModelChanged.
// History representability is re-checked at that boundary against the settled
// history; a switch that fails there surfaces as a transcript warning, is
// dropped rather than retried, and its acceptance is retracted.
func (s *Session) SetModel(provider, model string) error {
	release, admissionErr := s.beginRetirementMutation("admission")
	if admissionErr != nil {
		return admissionErr
	}
	defer release()
	target := modelSwitchTarget{provider: strings.TrimSpace(provider), model: strings.TrimSpace(model)}
	if target.model == "" {
		return errors.New("model is required")
	}
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		return nil
	}
	s.modelPickSeq++
	pickSeq := s.modelPickSeq
	if !s.turnInFlightLocked() {
		s.mu.Unlock()
		return s.applyModelSwitch(target, "", nil, modelSwitchFence{pickSeq: pickSeq, queueWhenBusy: true})
	}
	s.mu.Unlock()
	return s.queueModelSwitch(target, pickSeq)
}

// turnInFlightLocked reports whether an input is being processed, from its
// admission through the last drained turn. The state covers the rounds
// themselves; goalInTurn widens it to the whole input call, including pre-turn
// work and the gaps between a call's drained turns. Callers hold s.mu.
func (s *Session) turnInFlightLocked() bool {
	return s.state == SessionProcessing || s.goalInTurn
}

// queueModelSwitch records a mid-input switch for the turn boundary. The
// pick-time validation mirrors the at-rest path as far as a moving session
// permits: the ref must resolve (a typo or unknown instance errors now, at
// the picker), and membership is preflighted against a live listing. The
// unrepresentable-history preflight is deliberately skipped - history is
// being appended to by the running turn - and re-run by the boundary's
// applyModelSwitch against settled history, reusing the listing this preflight
// fetched so the boundary pays no second enumeration.
// A pick that passes and is stored is accepted, and acceptance is published
// immediately (announceNextModel) even though the boundary applies it later:
// an accepted pick the composer cannot see until the boundary reads to the
// user as a pick that never happened.
func (s *Session) queueModelSwitch(target modelSwitchTarget, pickSeq uint64) error {
	primedInstance, primedListing := s.primeCodexRoster(target.ref())
	fence := modelSwitchFence{pickSeq: pickSeq, queueWhenBusy: true}
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		return nil
	}
	if !s.turnInFlightLocked() {
		// The input ended while the preflight ran: the session is at rest, so
		// the switch can apply directly instead of waiting for a boundary
		// that already fired.
		s.mu.Unlock()
		return s.applyModelSwitch(target, primedInstance, primedListing, fence)
	}
	nextProfile, _, err := s.resolveModelSwitchProfile(s.profile, target)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	client := s.client
	s.mu.Unlock()

	// The returned profile is the one the enumeration enriched with the live
	// listing's rows - the same profile the at-rest path commits, and the one
	// the accepted pick advertises: a gateway's live listing is the only place
	// a model's reasoning tiers may appear, and announcing the pre-listing
	// profile would show the composer an empty ladder until the boundary.
	nextProfile, primedInstance, primedListing, err = resolveModelSwitchTarget(client, nextProfile, s.id, *s.providerAdapterTimeout(), primedInstance, primedListing)
	if err != nil {
		return err
	}

	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		return nil
	}
	if !s.turnInFlightLocked() {
		s.mu.Unlock()
		return s.applyModelSwitch(target, primedInstance, primedListing, fence)
	}
	// Last pick wins, but only among picks that pass their own validation.
	// Storing neither accepts the pick nor displaces an older queued one: the
	// boundary's deferred history check still runs against settled history, so
	// a store that advanced the acceptance watermark would let a pick that is
	// about to be rejected cancel another, which is the outcome this queue
	// exists to avoid. The watermark here only skips a store a newer pick has
	// already committed; ordering is the pop's job (see
	// popNewestPendingModelLocked), so a slow older pick still queues and can
	// apply if the newer one fails its deferred check.
	var announce *provider.Profile
	if pickSeq > s.modelPickAccepted {
		s.enqueuePendingModelLocked(pendingModelSwitch{target: target, primedInstance: primedInstance, primedListing: primedListing, seq: pickSeq, profile: nextProfile})
		// A call waiting out the old model's usage limit retries on this
		// one now rather than at the turn boundary it is waiting to reach.
		s.wakeQuotaWaitLocked()
		// The queue owns the visible choice, so only the entry the queue would
		// pop next is announced: an older pick whose preflight finishes after a
		// newer one queued announces nothing, because the newer pick's own
		// announcement still stands and the late arrival must not move the
		// composer's label backwards.
		if newest := s.newestPendingModelIndexLocked(); newest >= 0 && s.pendingModels[newest].seq == pickSeq {
			announce = s.pendingModels[newest].profile
		}
	}
	s.mu.Unlock()
	if announce != nil {
		// The pick is accepted: the next turn runs on it, and a subscriber's
		// label plus thread/read must say so now - the same accept-time
		// reflection an effort change gets - instead of first at the
		// boundary's commit publication.
		s.announceNextModel(announce)
	}
	return nil
}

// enqueuePendingModelLocked records a switch for the boundary applier. Order is
// decided at the pop by issue, not by insertion: an older pick can reach this
// point after a newer one queued (a slow preflight), and it must not become the
// newest choice merely by arriving last. Callers hold s.mu.
func (s *Session) enqueuePendingModelLocked(entry pendingModelSwitch) {
	s.pendingModels = append(s.pendingModels, entry)
}

// popNewestPendingModelLocked removes and returns the newest queued switch, the
// one whose choice the next turn should run on. Entries strictly older than the
// newest committed pick are dropped: that newer pick already owns the outcome,
// so an older one must not apply over it. Callers hold s.mu.
func (s *Session) popNewestPendingModelLocked() (pendingModelSwitch, bool) {
	live := s.pendingModels[:0]
	for _, entry := range s.pendingModels {
		// Strictly older than the newest committed pick, so that pick owns the
		// outcome and this one must not apply over it.
		if entry.seq < s.modelPickAccepted {
			continue
		}
		live = append(live, entry)
	}
	s.pendingModels = live
	newest := s.newestPendingModelIndexLocked()
	if newest < 0 {
		s.pendingModels = nil
		return pendingModelSwitch{}, false
	}
	entry := s.pendingModels[newest]
	s.pendingModels = append(s.pendingModels[:newest], s.pendingModels[newest+1:]...)
	return entry, true
}

// newestPendingModelIndexLocked is the one selection rule for which pending
// pick owns the session's next turn: newest by issue, skipping the picks a
// committed newer one superseded. popNewestPendingModelLocked applies it after
// pruning superseded entries; ReportedModelRef applies it without touching the
// queue. Callers hold s.mu.
func (s *Session) newestPendingModelIndexLocked() int {
	newest := -1
	for i, entry := range s.pendingModels {
		if entry.seq < s.modelPickAccepted {
			continue
		}
		if newest < 0 || entry.seq > s.pendingModels[newest].seq {
			newest = i
		}
	}
	return newest
}

// applyPendingModelSwitch applies queued switches once no rounds remain. Its
// callers are the input loop's two ownership points: before admitting each
// turn (where the previous turn's deferred settlement has already run) and at
// the idle tail after the input drains. The guard is the PROCESSING state
// alone, not turnInFlightLocked, because goalInTurn is still set at both
// sites for the input call that owns them. A failed application is reported
// as a warning and dropped, not retried: the invalid choice stays visible in
// the transcript while a re-pick re-enters the normal path. A superseded
// application drops silently, and the loop then re-reads the queue: a newer
// pick may have been accepted while the superseded one was still fetching,
// and it must still land before the turn the caller is about to admit.
func (s *Session) applyPendingModelSwitch() {
	s.applyQueuedModelSwitches(false)
}

// applyQueuedModelSwitches is the shared pending applier. inWaitingCall is
// its one caller that applies while the state is still processing: a model
// call that waited out a usage limit (waitOutUsageLimit). The switch is what
// lets that call retry on a model the spent allowance may not limit; the
// call owns the input, and its round has started nothing the new profile
// could be swapped under.
//
// The applier takes no retirement-admission lease of its own. Every caller
// runs inside the input's "turn" lease: both input-loop sites are inside
// processInputKindWithProvenance, which holds that lease through its deferred
// apply, and the quota wait's call runs inside that input's rounds.
// RetirementController.TryClaim enters "preparing" only while no lease is
// active, so admission cannot close under this call. A nested lease here
// could only ever be refused after the pop, with the pick already out of the
// queue - the shape that lost an accepted pick in #6973.
func (s *Session) applyQueuedModelSwitches(inWaitingCall bool) {
	for {
		s.mu.Lock()
		if s.state == SessionProcessing && !inWaitingCall {
			s.mu.Unlock()
			return
		}
		pending, ok := s.popNewestPendingModelLocked()
		// The popped pick is accepted but in neither the queue nor the profile
		// for the whole application, so hold it as the advertised pair from the
		// pop's own mutex hold: the thread/model/set hook's synchronous refresh
		// samples concurrently, and a sample landing between two separate holds
		// would find the pick in neither place and regress thread/read to the
		// pre-pick model after the acceptance was published. Every settle below
		// replaces the advertised pair itself - the committed profile, a
		// re-queued entry, or the retraction.
		if ok {
			s.modelValidating = &pending
		}
		s.mu.Unlock()
		if !ok {
			return
		}
		err := s.applyModelSwitch(pending.target, pending.primedInstance, pending.primedListing, modelSwitchFence{pickSeq: pending.seq})
		s.clearModelValidating()
		if err != nil {
			s.emit(events.WarningData{
				Message: "queued model switch to " + pending.target.ref() + " did not apply: " + err.Error(),
				Source:  "model_switch",
			})
			// The pick's own acceptance announcement named a model the session
			// will not run. With nothing else queued, retract it to what the
			// session actually runs so no surface keeps showing the failed
			// pick; an older pick still queued announces itself when its own
			// application settles here.
			s.mu.Lock()
			stillQueued := s.newestPendingModelIndexLocked() >= 0
			s.mu.Unlock()
			if !stillQueued {
				s.announceNextModel(s.currentProfile())
			}
		}
	}
}

// clearModelValidating retires the validating pick's hold on the advertised
// pair. The caller has settled that application, and its outcome - the
// committed profile, a re-queued entry, or the retraction announcement - now
// owns the advertised pair on its own.
func (s *Session) clearModelValidating() {
	s.mu.Lock()
	s.modelValidating = nil
	s.mu.Unlock()
}

// announceNextModel publishes the session's next-turn model as an
// EventNextModelChanged: the named profile's pair and reasoning ladder, over
// the context settlement current right now. The running turn's profile is
// untouched - the context figures stay the running model's, so a subscriber's
// meter does not re-basis against a model that has not started a turn; the
// boundary's EventModelChanged re-settles them against the new profile. The
// caller holds no locks: the emit blocks on the authoritative consumer, and
// that consumer reaches session state.
func (s *Session) announceNextModel(profile *provider.Profile) {
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	// The figures are what thread/read answers for this same state
	// (ContextStatus): one memoized sample, so a subscriber's meter and the
	// status projection can never disagree about the running model's context.
	usage, pressure := s.ContextStatus()
	s.emit(events.NextModelChangedData{
		Provider:               profile.ID(),
		Model:                  profile.Model(),
		ReasoningEffortLevels:  profile.ReasoningEffortLevels(),
		SupportsReasoning:      profile.SupportsReasoning(),
		DefaultReasoningEffort: profileDefaultReasoningEffort(profile),
		ContextUsed:            usage.Used,
		ContextWindow:          usage.Window,
		ContextPressure:        pressure,
		ContextAssumed:         usage.Assumed,
		CompactionThreshold:    usage.CompactionThreshold,
	})
}

// applyModelSwitch is SetModel's committed switch: resolve, validate against
// settled history, swap the profile, record the marker turn, publish
// EventModelChanged, and persist. The caller owns the retirement admission -
// SetModel and the pending applier each hold one. A primed listing the caller
// already fetched (the queue's pick-time preflight) is reused instead of a
// fresh enumeration; nil primes here. The fence bounds the commit to the pick
// that started it: a superseded pick drops, and an at-rest pick that finds a
// turn in flight at the commit point queues for that turn's boundary instead
// of swapping under it. The commit through the publication is serialized under
// modelSwitchPublishMu, so the marker and event land in the order the swaps
// committed; supersession is decided once, before the swap, so a committed
// application always publishes - a newer pick that has only queued can still
// fail its own deferred validation. The profile a commit replaces is read at
// the commit itself: an earlier pick's application can move the session
// through another provider while this one validates, so the
// communicate-override carry, the provider-tool reconciliation, and the
// marker's from-claim follow the transition that actually happened, never the
// pick-time classification.
func (s *Session) applyModelSwitch(target modelSwitchTarget, primedInstance string, primedListing *llm.ModelListing, fence modelSwitchFence) error {
	if primedListing == nil {
		// A named Codex model may exist only in the target instance's live
		// roster. List it before s.mu: the strict cross-provider resolve below
		// runs under the lock and must stay a pure registry lookup, while the
		// live rows this applies are what let it accept a model the bundled
		// roster lags.
		primedInstance, primedListing = s.primeCodexRoster(target.ref())
	}
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		return nil
	}
	nextProfile, _, err := s.resolveModelSwitchProfile(s.profile, target)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	// Unrepresentable-history preflight: reject before any state changes when
	// the target can't faithfully carry content kinds already in history.
	if kinds := unrepresentableHistoryKinds(s.history, nextProfile.Protocol()); len(kinds) > 0 {
		s.mu.Unlock()
		return fmt.Errorf("cannot switch to %s: history contains content unrepresentable by that target (%s)", nextProfile.ID(), formatContentKinds(kinds))
	}
	client := s.client
	s.mu.Unlock()

	// Live model list is fetched once and reused for both metadata fill and
	// the membership preflight below (see resolveModelSwitchTarget) — this
	// rejects before any state changes when the target instance can
	// enumerate its models and the requested model isn't among them. The
	// instance and listing are kept for the fence's re-queue below.
	nextProfile, listedInstance, listing, err := resolveModelSwitchTarget(client, nextProfile, s.id, *s.providerAdapterTimeout(), primedInstance, primedListing)
	if err != nil {
		return err
	}

	// The publication lock spans the commit through the publication, so a
	// second application cannot commit - and so cannot publish - inside this
	// application's swap-to-publication window: publications land in the order
	// their swaps committed, and a committed swap always tells its own story.
	// Supersession is decided once, below, before the swap - a pick the
	// watermark has passed drops without committing, and a newer pick that has
	// merely queued must not suppress a committed publication, because that
	// queued pick can still fail its own deferred validation and leave nothing
	// to correct the subscribers with.
	s.modelSwitchPublishMu.Lock()
	defer s.modelSwitchPublishMu.Unlock()
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		return nil
	}
	if fence.pickSeq != 0 && s.modelPickAccepted > fence.pickSeq {
		// A newer accepted pick superseded this one while it validated; the
		// newer choice owns the outcome, so this application drops.
		s.mu.Unlock()
		return nil
	}
	if fence.queueWhenBusy && s.turnInFlightLocked() {
		// A turn started while this at-rest pick was still validating. Queue
		// for that turn's boundary instead of swapping the profile under it,
		// carrying the listing this validation already fetched. Queuing is not
		// accepting: the boundary re-runs the history check against the
		// history the in-flight turn appended, so this pick can still be
		// rejected there and must not cancel an older one in the meantime.
		// The pop orders entries by issue, so it still applies before any
		// older queued pick that survives.
		s.enqueuePendingModelLocked(pendingModelSwitch{target: target, primedInstance: listedInstance, primedListing: listing, seq: fence.pickSeq, profile: nextProfile})
		s.wakeQuotaWaitLocked()
		s.mu.Unlock()
		// Queued is accepted for the next boundary, so the pick is published
		// the same moment the queue path above publishes one.
		s.announceNextModel(nextProfile)
		return nil
	}
	// Committing accepts the pick.
	if fence.pickSeq > s.modelPickAccepted {
		s.modelPickAccepted = fence.pickSeq
	}
	// The profile this commit actually replaces is the one the session runs
	// right now: an earlier pick's application may have moved the session
	// through another provider while this one validated, so every
	// transition-relative fact - the communicate-override carry, the
	// provider-tool reconciliation, and the marker's from-claim - is read
	// here, under the commit lock, never from the pick-time snapshot.
	commitPrev := s.profile
	if target.crossProviderFrom(commitPrev) {
		nextProfile = nextProfile.WithCommunicateOverridesFrom(commitPrev)
	}
	s.profile = nextProfile
	// The namer's spent-allowance latch was learned against the profile being
	// replaced, so it does not survive the swap (see
	// forgetSessionNamerQuotaExhaustionLocked).
	s.forgetSessionNamerQuotaExhaustionLocked()
	if s.contextMgr != nil {
		s.contextMgr.SetProfile(s.profile)
	}
	s.rebuildToolDefsCache()
	// Knowledge cutoff must be recomputed BEFORE the prompt-cache refresh
	// below, or the cached prompt keeps claiming the launch model's cutoff
	// forever (G15).
	s.envInfo.KnowledgeCutoff = nextProfile.KnowledgeCutoff()
	promptWarning, _ := s.refreshSystemPromptCache(s.env) // already holding s.mu; currentEnv() would deadlock
	// Post-swap fallback re-validation: cfg.ModelFallbacks entries that no
	// longer validate against the new profile are dropped; their names are
	// surfaced in the switch marker's warning line below.
	s.lastDroppedModelFallbacks = s.revalidateModelFallbacksLocked()
	// Context-window-shrink warning: the new profile may have a smaller
	// window than the old one, so estimated usage against the just-applied
	// profile (contextMgr.SetProfile above) may already exceed its
	// compaction threshold. Blocking the switch would strand users on
	// expensive models; the marker just warns.
	var usage ContextMetrics
	var contextPressure float64
	contextWarningPct := 0
	contextWarning := false
	contextWindowAssumed := false
	assumedWindowTokens := 0
	if s.contextMgr != nil {
		// One EstimateUsage read carries the pressure AND whether the window it
		// is measured against is the unknown-window assumption, so the marker
		// can name the assumption instead of presenting a percentage of 128K as
		// a fact about the new model's real window (#4598 follow-up).
		usage = s.contextMgr.EstimateUsage(s.history, len(s.cachedSystemPrompt))
		if usage.Window > 0 {
			contextPressure = float64(usage.Used) / float64(usage.Window)
			if contextPressure >= s.contextMgr.CheckpointThresholdFor(nextProfile) {
				contextWarning = true
				contextWarningPct = int(contextPressure*100 + 0.5)
				contextWindowAssumed = usage.Assumed
				assumedWindowTokens = usage.Window
			}
		}
	}
	// Snapshot the facts the marker needs and leave the lock before the
	// history scan: the count walks every turn, and the lock is shared with
	// turn processing.
	markerHistory := append([]schema.Turn(nil), s.history...)
	droppedFallbacks := append([]string(nil), s.lastDroppedModelFallbacks...)
	s.mu.Unlock()
	markerText := buildModelSwitchMarkerText(modelSwitchMarkerParams{
		oldProfile:                commitPrev,
		nextProfile:               nextProfile,
		contextWarning:            contextWarning,
		contextWarningPct:         contextWarningPct,
		contextUsedTokens:         usage.Used,
		compactionThresholdTokens: usage.CompactionThreshold,
		contextWindowAssumed:      contextWindowAssumed,
		assumedWindowTokens:       assumedWindowTokens,
		droppedFallbacks:          droppedFallbacks,
		orphanedToolResultMedia:   orphanedToolResultMediaCount(markerHistory, commitPrev, nextProfile),
	})
	if hook := s.cfg.testOnly.beforeModelSwitchPublish; hook != nil {
		hook()
	}
	s.reportPromptRenderFailure(promptWarning)
	// Persisted marker turn (N5): a new schema.Turn kind rendered as a
	// systemMessage by both projection paths and excluded from
	// expandHistory. Must be appended (and thus visible to a replaying
	// client) before the live-only EventModelChanged notification below.
	marker := schema.NewTurn(schema.TurnModelSwitch, llm.System(markerText))
	marker.ModelSwitch = &schema.ModelSwitchInfo{
		OldProvider: commitPrev.ID(), OldModel: commitPrev.Model(),
		NewProvider: nextProfile.ID(), NewModel: nextProfile.Model(),
	}
	marker.ModelSwitch.ContextWindow, marker.ModelSwitch.ContextWindowSource = budgetingWindow(nextProfile)
	s.recordTurn(marker, marker)
	s.emit(events.ModelChangedData{
		OldProvider:            commitPrev.ID(),
		OldModel:               commitPrev.Model(),
		NewProvider:            nextProfile.ID(),
		NewModel:               nextProfile.Model(),
		ReasoningEffortLevels:  nextProfile.ReasoningEffortLevels(),
		SupportsReasoning:      nextProfile.SupportsReasoning(),
		DefaultReasoningEffort: profileDefaultReasoningEffort(nextProfile),
		MarkerText:             markerText,
		ContextUsed:            usage.Used,
		ContextWindow:          usage.Window,
		ContextPressure:        contextPressure,
		ContextAssumed:         usage.Assumed,
		CompactionThreshold:    usage.CompactionThreshold,
	})
	// A pick accepted while this switch was publishing is still the queue's
	// newest choice, and its acceptance was already published. The commit
	// frame above names the model the session runs now, so without this the
	// advertised selection rests on the committed model until the pick's own
	// boundary - burying the acceptance. Re-announce the newest accepted pick
	// here, under the publication lock, where no further commit can land
	// between the two and undo it.
	s.mu.Lock()
	newest := s.newestPendingModelIndexLocked()
	var queuedPick *provider.Profile
	if newest >= 0 {
		queuedPick = s.pendingModels[newest].profile
	}
	s.mu.Unlock()
	if queuedPick != nil {
		s.announceNextModel(queuedPick)
	}
	// Flush meta.json so a daemon crash before the next happy-path turn
	// boundary doesn't leave on-disk model stale. Kata wnfz. maybeAutoSave
	// re-acquires s.mu via s.Meta(), so the lock must be released first.
	s.maybeAutoSave()
	s.resumeRootAttentionAfterModelSwitch()
	return nil
}

// primeCodexRoster applies the target instance's live listing when a named
// model is a Codex id the bundled roster does not carry. The switch's own
// resolve runs under s.mu, so the network call lives here instead, outside the
// lock; a failure leaves the resolve to report its authoritative error.
func (s *Session) primeCodexRoster(ref string) (string, *llm.ModelListing) {
	ref = strings.TrimSpace(ref)
	instance, model, ok := strings.Cut(ref, "/")
	if !ok || instance == "" || s.client == nil {
		return "", nil
	}
	s.mu.Lock()
	base := s.profile
	s.mu.Unlock()
	if base != nil && strings.EqualFold(instance, base.ID()) {
		return "", nil
	}
	r := s.client.Registry()
	if base != nil {
		if res, err := r.ResolveInstanceModelFacts(base.ID(), ref); err == nil && !res.Synthesized && !registry.BoolValue(res.Model.Disabled) {
			// The current instance already serves this namespaced id; the
			// first segment is a model namespace, not a provider switch.
			return "", nil
		}
	}
	if !r.HasInstance(instance) {
		// A namespaced model on the current instance (an OpenRouter
		// "anthropic/..." id) is not a provider switch. Resolving the raw
		// ref would name the anthropic instance and mint its credential.
		return "", nil
	}
	if _, err := r.ResolveInstanceModelFacts(instance, model); err == nil || !errors.Is(err, registry.ErrCodexModelNotListed) {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), llm.LiveModelListTimeout)
	defer cancel()
	listing, err := s.client.Models(s.apiLogContext(ctx), instance)
	if err != nil || !listing.Usable {
		return "", nil
	}
	return instance, &listing
}

// SetVisionModel changes the vision side-channel routing for future image
// reads. The ref is "" (describe with the session's active model), "off"
// (disable the side-channel), a bare model on the active provider, or
// "provider/model" to pin a provider instance, which must be registered in
// the client. It takes effect on the next image read and persists with the
// session config; it never alters the active model itself.
func (s *Session) SetVisionModel(ref string) error {
	release, admissionErr := s.beginRetirementMutation("admission")
	if admissionErr != nil {
		return admissionErr
	}
	defer release()
	ref = strings.TrimSpace(ref)
	ref = canonicalVisionModelOff(ref)
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		return nil
	}
	if err := s.validateVisionModelRefLocked(ref); err != nil {
		s.mu.Unlock()
		return err
	}
	old := s.cfg.VisionModel
	oldCanonical := canonicalVisionModelOff(old)
	if oldCanonical == ref {
		canonicalized := old != oldCanonical
		if canonicalized {
			s.cfg.VisionModel = oldCanonical
		}
		s.mu.Unlock()
		if canonicalized {
			s.maybeAutoSave()
		}
		return nil
	}
	s.cfg.VisionModel = ref
	s.rebuildToolDefsCache()
	s.mu.Unlock()
	s.emit(events.VisionModelChangedData{OldVisionModel: oldCanonical, NewVisionModel: ref})
	s.maybeAutoSave()
	return nil
}

// canonicalVisionModelOff normalizes only the complete bare off sentinel.
// Provider-qualified refs such as "off/model" must remain ordinary refs.
func canonicalVisionModelOff(ref string) string {
	if !strings.Contains(ref, "/") && isVisionDisabledSetting(ref) {
		return visionModelOff
	}
	return ref
}

func (s *Session) validateVisionModelRefLocked(ref string) error {
	if ref == "" || (!strings.Contains(ref, "/") && isVisionDisabledSetting(ref)) {
		return nil
	}
	prov, model, ok := strings.Cut(ref, "/")
	if ok && (prov == "" || model == "") {
		return fmt.Errorf("invalid vision model ref %q: want \"model\" or \"provider/model\"", ref)
	}
	if ok && !strings.EqualFold(prov, s.profile.ID()) && !s.client.HasProvider(prov) {
		return fmt.Errorf("vision model provider %q is not configured or has no credential (active provider %q)", prov, s.profile.ID())
	}
	return nil
}

// VisionModel returns the session's configured vision side-channel setting
// ("", "off", or a model ref) for thread-read snapshots.
func (s *Session) VisionModel() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.VisionModel
}

// buildModelSwitchMarkerText renders the persisted model-switch marker text:
// "Switched model: <old provider/model> → <new provider/model>", with
// warning lines appended when estimated context usage exceeds the new
// profile's compaction threshold and/or the switch dropped now-invalid
// cfg.ModelFallbacks entries (N5, decision rows "Transcript marker" /
// "Context-window shrink" / "Model-fallbacks after a switch").
// When the new profile's window is unknown the warning percentage is measured
// against contextmgr.AssumedContextWindow, so that warning names the assumed
// window (the "~" the clients mark an assumed window figure with) rather than
// reading as a fact about the model's real size.
// modelSwitchMarkerParams collects the inputs to buildModelSwitchMarkerText so
// the call site names each field rather than relying on positional order.
type modelSwitchMarkerParams struct {
	oldProfile                *provider.Profile
	nextProfile               *provider.Profile
	contextWarning            bool
	contextWarningPct         int
	contextUsedTokens         int
	compactionThresholdTokens int
	// contextWindowAssumed and assumedWindowTokens describe the window the
	// warning percentage is measured against when it is the unknown-window
	// assumption, not a size anything vouches for.
	contextWindowAssumed bool
	assumedWindowTokens  int
	droppedFallbacks     []string
	// orphanedToolResultMedia counts tool-result media payloads already in the
	// transcript that the new target cannot see. Request assembly drops their
	// bytes (#4217); the marker makes that drop visible instead of silent. The
	// same count covers images and documents once a protocol carries one.
	orphanedToolResultMedia int
}

func buildModelSwitchMarkerText(p modelSwitchMarkerParams) string {
	lines := []string{fmt.Sprintf("Switched model: %s/%s → %s/%s", p.oldProfile.ID(), p.oldProfile.Model(), p.nextProfile.ID(), p.nextProfile.Model())}
	if p.contextWarning {
		if p.compactionThresholdTokens > 0 {
			lines = append(lines, fmt.Sprintf("Warning: context usage (~%d tokens) is over %s/%s's compaction threshold (%d tokens).", p.contextUsedTokens, p.nextProfile.ID(), p.nextProfile.Model(), p.compactionThresholdTokens))
		} else if p.contextWindowAssumed {
			lines = append(lines, fmt.Sprintf("Warning: context usage is at ~%d%% of %s's assumed context window (~%d tokens), over the compaction threshold.", p.contextWarningPct, p.nextProfile.ID(), p.assumedWindowTokens))
		} else {
			lines = append(lines, fmt.Sprintf("Warning: context usage is at ~%d%% of %s's context window, over the compaction threshold.", p.contextWarningPct, p.nextProfile.ID()))
		}
	}
	if len(p.droppedFallbacks) > 0 {
		lines = append(lines, fmt.Sprintf("Warning: dropped model fallbacks no longer valid for %s: %s", p.nextProfile.ID(), strings.Join(p.droppedFallbacks, ", ")))
	}
	if p.orphanedToolResultMedia > 0 {
		lines = append(lines, fmt.Sprintf("Warning: %s read earlier in this session cannot be seen by %s; they stay in the transcript but are omitted from requests.", pluralizedUnit(p.orphanedToolResultMedia, "tool-result media item"), p.nextProfile.ID()))
	}
	return strings.Join(lines, "\n")
}

// orphanedToolResultMediaCount counts the tool-result media payloads already in
// history that from saw and to cannot: those are the attachments this switch
// puts out of the model's view. The delivery predicates decide per media type,
// so the count covers images and, on a protocol that carries one, documents.
func orphanedToolResultMediaCount(history []schema.Turn, from, to *provider.Profile) int {
	if from == nil || to == nil {
		return 0
	}
	count := 0
	for _, t := range history {
		for _, p := range t.Message.Content {
			if p.Kind != llm.ContentToolResult || p.ToolResult == nil || len(p.ToolResult.ImageData) == 0 {
				continue
			}
			mt := p.ToolResult.ImageMediaType
			if llm.RowSeesToolResultMedia(from.Resolved(), mt) && !llm.RowSeesToolResultMedia(to.Resolved(), mt) {
				count++
			}
		}
	}
	return count
}

// Rename sets a user-chosen session title. It records NameSource="user" so the
// auto-namers (prompt + compaction) will never overwrite it — shouldApplySession
// NameLocked and shouldNameFromCompaction both reject any source that is not
// "prompt"/"compaction". Persists meta so the name survives a daemon crash.
func (s *Session) Rename(name string) error {
	release, err := s.beginRetirementMutation("admission")
	if err != nil {
		return err
	}
	defer release()
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		return nil
	}
	s.naming.value = name
	s.naming.source = sessionNameSourceUser
	s.naming.updated = s.sclock().Now().UTC()
	s.naming.set = true
	s.mu.Unlock()
	s.emit(events.SessionNameChangedData{Name: name, Source: sessionNameSourceUser})
	// maybeAutoSave re-acquires s.mu via s.Meta(); must not hold the lock here.
	s.maybeAutoSave()
	return nil
}

func (s *Session) applyModelRequestMetadata(req *llm.Request) {
	if req == nil {
		return
	}
	// The prompt-cache fields go on every request: llm.ShapeRequest drops the
	// ones the resolved row cannot send (spec §7.5, two independent Fields
	// gates), so nothing here has to know which endpoint accepts which.
	if strings.TrimSpace(s.id) != "" {
		req.SessionID = s.id
		req.ThreadID = s.id
		if strings.TrimSpace(req.PromptCacheKey) == "" {
			req.PromptCacheKey = "evener-session-" + s.id
		}
		if strings.TrimSpace(req.PromptCacheRetention) == "" {
			req.PromptCacheRetention = "24h"
		}
	}
	if strings.TrimSpace(s.installID) != "" {
		if req.ClientMetadata == nil {
			req.ClientMetadata = map[string]string{}
		}
		req.ClientMetadata[installid.CodexInstallationIDMetadataKey] = s.installID
	}
}

// communicated reports whether communicate was called during the most recent
// ProcessInput invocation.
func (s *Session) communicated() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.comm.hasAccepted()
}

// communicateUpdate reports whether a NON-terminal communicate (next="send_and_continue")
// was called during the most recent ProcessInput invocation. A delegate's
// missing-report reminder can then acknowledge the update instead of claiming
// the delegate never called the result tool (#3734).
func (s *Session) communicateUpdate() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.comm.updated
}

// communicateOutput returns the canonical structured output from the most recent
// communicate call in the current ProcessInput invocation.
func (s *Session) communicateOutput() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.comm.output
}

// communicateStructured returns the raw structured output object from the most
// recent communicate call, before normalizeNodeOutput canonicalization.
func (s *Session) communicateStructured() any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.comm.structured
}

// communicatedReply returns the reply of the terminal communicate the current
// input accepted, and whether it accepted one.
func (s *Session) communicatedReply() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.comm.reply, s.comm.hasAccepted()
}

func (s *Session) quietBoundaryLocked() bool {
	return s.comm.quietBoundary()
}

func (s *Session) communicatedUnblockedLocked() bool {
	return s.comm.isUnblocked()
}

func (s *Session) communicatedUnblocked() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.communicatedUnblockedLocked()
}

func (s *Session) communicatedSettledLocked() bool {
	return s.comm.isSettled()
}

func (s *Session) communicatedSettled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.communicatedSettledLocked()
}

// quietlyYielded reports a quiet boundary, not a visible final reply.
func (s *Session) quietlyYielded() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.quietBoundaryLocked()
}

func (s *Session) beginCommunicateRound() {
	s.mu.Lock()
	s.comm.roundQuiet = false
	s.comm.roundVisible = false
	s.mu.Unlock()
}

func (s *Session) recordQuietYield() {
	s.mu.Lock()
	s.comm.roundQuiet = true
	s.mu.Unlock()
}

func (s *Session) clearQuietBoundaryForBatchStop() {
	s.mu.Lock()
	if s.quietBoundaryLocked() {
		s.comm.roundQuiet = false
	}
	s.mu.Unlock()
}

func (s *Session) blockStopBoundary() {
	s.mu.Lock()
	if s.comm.hasAccepted() {
		s.comm.phase = commBoundaryStopBlocked
	}
	s.mu.Unlock()
}

func (s *Session) settleCommunicateBoundary(replacement *string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.comm.hasAccepted() {
		return false
	}
	if id := s.comm.stopPendingCallID; id != "" {
		s.execution.acceptedCommunicateCallID = id
		s.execution.replacementReply = replacement
	}
	wasPending := s.comm.phase == commBoundaryStopPending || s.comm.phase == commBoundaryStopBlocked
	s.comm.phase = commBoundarySettled
	s.comm.roundQuiet = false
	s.comm.stopPendingCallID = ""
	s.latestReport = s.comm
	return wasPending
}

func (s *Session) discardBlockedForQuietLocked() {
	if s.comm.phase != commBoundaryStopBlocked {
		return
	}
	s.comm = communicateResult{
		updated:    s.comm.updated,
		roundQuiet: s.comm.roundQuiet,
	}
}

// acceptCommunicateTerminal is the atomic terminal-result writer for the
// communicate tool (issue #570). A call's message, reply, canonical output,
// and raw structured value are accepted together under s.mu — the first
// completed terminal call wins BOTH slots, so a later competing call can never
// pair its structured value with another call's message or fill the winner's
// still-empty structured slot. It returns whether this call won; losers report
// accepted:false. The stable-delegate lease, when the context carries one, is
// recorded after the lock is released.
func (s *Session) acceptCommunicateTerminal(ctx context.Context, message, reply, output string, structured any) bool {
	s.mu.Lock()
	if s.comm.hasAccepted() {
		s.comm.roundVisible = true
		s.mu.Unlock()
		return false
	}
	stopPending := s.hasReplyStopHook()
	phase := commBoundarySettled
	if stopPending {
		phase = commBoundaryStopPending
	}
	s.comm = communicateResult{
		phase:        phase,
		updated:      s.comm.updated,
		text:         message,
		reply:        reply,
		output:       output,
		structured:   structured,
		roundVisible: true,
	}
	if stopPending {
		s.comm.stopPendingCallID = callIDFromContext(ctx)
	} else {
		s.latestReport = s.comm
	}
	s.mu.Unlock()
	if !stopPending {
		s.recordDelegateTerminalSeen(ctx)
	}
	return true
}

func (s *Session) recordDelegateTerminalSeen(ctx context.Context) {
	lease, stableRun := ctx.Value(delegateRunLeaseContextKey{}).(delegateLease)
	if stableRun && s.delegateController != nil {
		_ = s.delegateController.recordTerminalSeen(lease)
	}
}

// extractOriginalPrompt returns a delegate's assignment or the first user
// input of a root session. Inherited conversation does not redefine the task.
func (s *Session) extractOriginalPrompt() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.isSubagentSession() && s.cfg.spawn.subagentTask != "" {
		return s.cfg.spawn.subagentTask
	}
	for _, t := range s.history {
		if t.Kind == schema.TurnUserInput {
			// The first user input may carry machinery note parts (the
			// attachment persistence note); OriginalPrompt feeds titles and
			// search, so project the user's own words only.
			return apptranscript.UserFacingText(t.Message)
		}
	}
	return s.cfg.spawn.subagentTask
}

func (s *Session) appendTurn(kind schema.TurnKind, m llm.Message) {
	s.appendTurnWithTranscriptMessage(kind, m, m)
}

// maybeAppendEnvironmentContext records an ENVIRONMENT turn when the observed
// environment differs from the last emission (or on the first call ever). It
// is the single choke point every user-input path calls immediately before
// appending its TurnUserInput turn, so the context precedes the prompt it
// applies to. Append-only by construction (RenderDiff never edits a prior
// turn), so provider prompt caches survive it. A no-op when the session has
// no envCollector (e.g. a bare struct literal built directly by a test that
// bypasses NewSession/RestoreSessionFromMetaWithConfig).
//
// envTracker is read and mutated (RenderDiff) under attentionMu and mu because
// Session.Compact can run resetEnvContextTrackerLocked on a caller's own
// goroutine concurrently with this method running on the turn-processing
// goroutine — see the field's doc comment.
func (s *Session) maybeAppendEnvironmentContext() error {
	return s.appendEnvironmentContextWithPlacement(true, false)
}

func (s *Session) maybeAppendEnvironmentContextBeforeTrailingSteering() error {
	return s.appendEnvironmentContextWithPlacement(true, true)
}

func isTrailingSteeringTurn(turn schema.Turn) bool {
	return turn.Kind == schema.TurnSteering
}

func insertBeforeTrailingSteering(history []schema.Turn, turn schema.Turn) ([]schema.Turn, bool) {
	idx := len(history)
	for i, h := range slices.Backward(history) {
		if isTrailingSteeringTurn(h) {
			idx = i
			continue
		}
		if h.Kind != schema.TurnModelSwitch {
			break
		}
	}
	if idx == len(history) {
		return append(history, turn), false
	}
	return slices.Insert(history, idx, turn), true
}

// envRuntimeDrifted reports whether a runtime fact the launch prompt stated has moved.
func (s *Session) envRuntimeDrifted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.envTracker != nil && s.envTracker.RuntimeDrifted(s.envRuntimeSnapshot(envctx.Snapshot{}))
}

// communicateContractDrifted reports whether a restored legacy session has not yet
// received the active communicate contract superseding its frozen prompt.
func (s *Session) communicateContractDrifted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.envTracker != nil && s.envTracker.CommunicateContractDrifted(s.communicateContractLine())
}

// armEnvContext owes the next request a fresh environment block.
func (s *Session) armEnvContext() { s.envContextPending.Store(true) }

// needsFullInventoryBaseline reports whether launchEnvBaseline lacks runtime
// tool/model inventory (legacy baselineless session, even if GitBranch was seeded).
func (s *Session) needsFullInventoryBaseline() bool {
	return s.launchEnvBaseline.Model == "" && s.launchEnvBaseline.CallableTools == ""
}

// envContextDue reports whether the next request owes an environment block:
// an arm landed (a swap, a restore or a fold), or a stated fact moved.
func (s *Session) envContextDue() bool {
	if s.envContextPending.Load() {
		return true
	}
	return s.envRuntimeDrifted()
}

// envRuntimeSnapshot fills the runtime facts the launch prompt states once. Callers hold s.mu.
func (s *Session) envRuntimeSnapshot(snap envctx.Snapshot) envctx.Snapshot {
	var skills []string
	for _, d := range s.skills.ModelEntries() {
		if s.skillAvailableToSession(d.CatalogName) {
			skills = append(skills, d.CatalogName)
		}
	}
	snap.Model, snap.KnowledgeCutoff, snap.CommitTrailer = s.profile.Model(), s.envInfo.KnowledgeCutoff, s.commitTrailerPrompt()
	if s.cfg.TurnEndsProcess {
		snap.RunLifetime = "one-shot (process exits when turn drains; background shell jobs require job_watch to survive the turn and are stopped at exit)"
	} else {
		snap.RunLifetime = "persistent"
	}
	if le, ok := s.env.(*execenv.LocalExecutionEnvironment); ok {
		snap.ScratchDir = le.SessionScratchDir()
	}
	snap.CallableTools, snap.AvailableSkills = envctx.JoinNames(toolNamesFromDefinitions(s.cachedToolDefs)), envctx.JoinNames(skills)
	snap.CommunicateContract = s.communicateContractLine()
	return snap
}

func (s *Session) communicateContractLine() string {
	name := s.resultToolName()
	if name == "" {
		return ""
	}
	return fmt.Sprintf(`%s uses required "message" and optional "next" (%q default, %q for progress updates, %q with a brief note to end quietly); this supersedes retired "end_turn" and "{}" instructions from earlier prompts`,
		name, schema.CommunicateNextSendAndEndTurn, schema.CommunicateNextSendAndContinue, schema.CommunicateNextLogQuietlyAndEndTurn)
}

// appendEnvironmentContext can persist recovery context without publishing a
// live event: restore seeds its projection from the completed transcript.
func (s *Session) appendEnvironmentContext(publishEvent bool) error {
	return s.appendEnvironmentContextWithPlacement(publishEvent, false)
}

func (s *Session) appendEnvironmentContextWithPlacement(publishEvent, beforeTrailingGoal bool) error {
	if s.envCollector == nil {
		return nil
	}
	wasPending := s.envContextPending.Swap(false)
	snap := s.envCollector.Collect(envctx.Inputs{
		Cwd:     s.currentEnv().WorkingDirectory(),
		Sandbox: s.cfg.Sandbox,
	})

	s.attentionMu.Lock()
	s.mu.Lock()
	tracker := s.envTracker
	// Shutdown has claimed the session: its terminal boundary is published
	// under this same door, so an append that gets here is one whose entry and
	// event would land behind SESSION_END -- context for a session that is
	// over, in a transcript whose writer is about to close. Refuse silently,
	// like the two early returns beside this one: the caller is a turn the
	// close is already ending, and failing it would only make a dying turn
	// unwind state the close is about to discard.
	if tracker == nil || s.closingOrClosedLocked() {
		s.mu.Unlock()
		s.attentionMu.Unlock()
		if wasPending {
			s.envContextPending.Store(true)
		}
		return nil
	}
	snap = s.envRuntimeSnapshot(snap)
	before := *tracker
	block := tracker.RenderDiff(snap)
	var st envctx.State
	if block != "" {
		st = tracker.State()
	}
	s.mu.Unlock()
	if block == "" {
		// Nothing is owed: the last block still states every fact.
		s.attentionMu.Unlock()
		return nil
	}

	// Persist the identity with the entry. Model history can be compacted, so
	// its length cannot name a durable transcript turn.
	turn := schema.NewTurn(schema.TurnEnvironment, llm.UserMachinery(block))
	turn.StableTurnID = "turn_environment_" + ulid.Make().String()
	// The environment tracker is a durability owner: it may only claim the
	// model saw a turn once that turn is a record, or a restart renders every
	// later block as a diff against a baseline the model never received. So the
	// write goes through AppendSynced (records AND syncs, else reports) via the
	// pair helper, which resolves to one of three things (see
	// appendTurnAfterTranscriptWriteLocked):
	//   - nil (durable): the pair committed history + pair log, and the tracker
	//     advance below stands;
	//   - a retained record (ErrRetainedUnsynced): the whole line is in the
	//     file, so the pair ADOPTS it and returns nil — the tracker still
	//     advances, and the next fsync settles the debt. The entry is recorded
	//     once; the tracker is NOT rewound and the block is NOT re-emitted,
	//     which is what keeps a restart from carrying the environment twice;
	//   - any other error: nothing was recorded, so the tracker rewinds to the
	//     last state the model saw and the next turn re-renders the observation.
	// RenderDiff advanced the tracker before the write so it could render the
	// diff; attentionMu holds the pair whole against a fold publication exactly
	// as the clean path's is held.
	err := s.appendTurnAfterTranscriptWriteLocked(
		turn,
		func() error { return s.writeTranscriptSyncedLocked(turn) },
		func() {
			if beforeTrailingGoal {
				var inserted bool
				s.history, inserted = insertBeforeTrailingSteering(s.history, turn)
				if inserted {
					s.bumpHistoryRevisionLocked()
				}
				return
			}
			s.history = append(s.history, turn)
		},
	)
	committed := err == nil
	if !committed {
		s.mu.Lock()
		s.envTracker = &before
		s.mu.Unlock()
		if wasPending {
			s.envContextPending.Store(true)
		}
	}
	if committed {
		// Persist tracker state so resume stays silent when nothing changed.
		s.mu.Lock()
		s.envContextState = &st
		s.mu.Unlock()
	}
	if committed && publishEvent {
		// The entry and the event announcing it are one publication, and the
		// transcript door is the statement that says so: the entry is in history
		// before the event exists, so no reader can observe the event ahead of
		// the entry that explains it, and the door keeps a fold from
		// committing its markers between those two steps.
		//
		// The event is a trigger, not a snapshot: when the root session's bridge
		// handles this event it samples current context
		// (server/thread_envelope.go's facetsByEvent), so the sample can reflect
		// state a later publication moved.
		if hook := s.cfg.testOnly.beforeEnvironmentEventPublish; hook != nil {
			hook()
		}
		s.emit(events.EnvironmentData{TurnID: turn.StableTurnID, Text: block})
	}
	s.attentionMu.Unlock()
	if committed {
		s.maybeAutoSave()
	}
	// A NOT-RECORDED failure (a partial line, or a clean rollback) rejects the
	// input: the turn is rolled back and its provisional count checkpointed
	// (TestRejectedInputDoesNotPersistItsProvisionalTurn). A retained record —
	// in the file but not yet durable — is adopted instead (committed above,
	// err nil), never rejected, or the next turn would re-render and duplicate
	// the entry a restart already holds. Either way the sync-failure diagnostic
	// is on the writer's warning queue; surface it here, outside the door.
	s.surfaceTranscriptWarnings()
	if err != nil {
		s.emit(events.WarningData{Message: fmt.Sprintf("transcript write failed: %v", err)})
	}
	return err
}

// resetEnvContextTrackerLocked clears the environment-context tracker when a
// CHECKPOINT/SUMMARY turn replaces history: compaction drops any previously
// appended ENVIRONMENT turns from the model-visible history (they are
// model-bound content folded away like any other turn), so the model loses
// whatever drift was last reported. Rebuilding the tracker at its zero state
// (HasSent=false) makes the next maybeAppendEnvironmentContext call re-emit a
// full block rather than staying silent on an environment the model can no
// longer see anything about.
//
// Requires attentionMu and mu, in that order, so a fold and an environment
// append cannot publish different tracker generations together. It reports
// whether it changed anything, so a caller that has released both locks can
// persist the new state.
func (s *Session) resetEnvContextTrackerLocked() bool {
	if s.envTracker == nil {
		return false
	}
	s.envTracker = envctx.NewTracker(envctx.State{})
	s.envTracker.SeedRuntime(s.launchEnvBaseline)
	if s.systemPromptFrozen && s.needsFullInventoryBaseline() {
		s.envTracker.RequireFullInventory()
	}
	for _, turn := range s.history {
		if turn.Kind == schema.TurnEnvironment {
			s.envTracker.ReplayBlock(turn.Message.Text())
		}
	}
	s.envTracker.ResetState()
	state := envctx.State{}
	s.envContextState = &state
	s.armEnvContext()                   // the fold took the last block out of history
	s.envContextFoldRemoved.Store(true) // and this request owes the restatement
	return true
}

// appendTurnWithTranscriptMessage keeps the live model context and the durable
// semantic transcript distinct when a tool exposes explicitly private evidence.
func (s *Session) appendTurnWithTranscriptMessage(kind schema.TurnKind, live, persisted llm.Message) {
	t := schema.NewTurn(kind, live)
	persistedTurn := t
	persistedTurn.Message = persisted
	s.recordTurn(t, persistedTurn)
}

// appendTurnAfterTranscriptWrite runs one durability-first history-append/
// transcript-write pair atomically under attentionMu: write commits the
// turn's transcript entry (attentionMu already held — use the Locked write
// variants), and appendLocked appends it to s.history, plus any flags that
// must travel with the append, under s.mu. Holding attentionMu across the
// pair keeps it whole relative to a fold's publication transaction: the pair
// lands either entirely before the publish — the turn is in the fold's
// snapshot or merged tail, and its pre-marker entry
// gets a post-marker copy — or entirely after it, where its entry follows
// the markers on its own. A half-done pair could otherwise leave a
// pre-marker entry for a turn the publish never saw (lost on restart) or a
// post-marker entry racing the transaction's own tail rewrite (duplicated on
// restart). On write error nothing is appended; the error returns for the
// caller to report outside the locks.
//
// persisted is the exact transcript form write commits. It is recorded in
// the session's pair log so a fold publication can re-append that same form
// after its compaction markers — never the live turn, whose tool results
// deliberately retain the private evidence the persisted projection replaces
// with a placeholder.
func (s *Session) appendTurnAfterTranscriptWrite(persisted schema.Turn, write func() error, appendLocked func()) error {
	err := func() error {
		s.attentionMu.Lock()
		defer s.attentionMu.Unlock()
		return s.appendTurnAfterTranscriptWriteLocked(persisted, write, appendLocked)
	}()
	// A durable write whose whole line landed but did not sync returns nil —
	// the entry is a record, appended above — and leaves its sync failure on
	// the writer's warning queue. Surface it here, outside attentionMu: a
	// warning fires the notification hook, which must not run while this
	// session holds the transcript lock that hook's own records need.
	s.surfaceTranscriptWarnings()
	return err
}

func (s *Session) appendTurnAfterTranscriptWriteLocked(persisted schema.Turn, write func() error, appendLocked func()) error {
	// A write reports one of three things (see transcript.AppendSynced, and the
	// ordinary doors' recorded-or-nil): nil is recorded; ErrRetainedUnsynced is
	// ALSO recorded — the whole line is in the file — but not yet durable, so
	// the turn is adopted here (never re-appended, which would duplicate it on
	// restart) and the next fsync settles the debt; any other error recorded
	// nothing, so nothing is appended. Returning nil for the adopted case is
	// what keeps an owner from discarding its state and re-appending; the
	// retained diagnostic is on the writer's warning queue, surfaced outside the
	// lock by the caller's surfaceTranscriptWarnings.
	s.mu.Lock()
	s.lastRecorded = recordedOrdinal{}
	s.mu.Unlock()
	if err := write(); err != nil && !errors.Is(err, transcript.ErrRetainedUnsynced) {
		return err
	}
	s.mu.Lock()
	appendLocked()
	s.logPairPersistedLocked(persisted)
	s.markLastPairOrdinalLocked()
	s.mu.Unlock()
	return nil
}

// logPairPersistedLocked records the persisted transcript form of one
// append/write pair for publishFoldTransaction's post-marker rewrite.
// Callers hold s.mu inside their pair's attentionMu hold; the transaction
// prunes the log at every successful publication.
func (s *Session) logPairPersistedLocked(persisted schema.Turn) {
	s.persistedAppendLog = append(s.persistedAppendLog, persisted)
}

// markLastPairOrdinalLocked stamps the most recently logged pair with the
// entry ordinal and model its write was recorded with, when it was: the copy
// a fold re-appends after its markers carries them as OriginalOrdinal and
// Model. The caller holds
// s.mu inside the attentionMu hold that cleared lastRecorded before the pair's
// write, so lastRecorded is that write's own record.
func (s *Session) markLastPairOrdinalLocked() {
	n := len(s.persistedAppendLog)
	if n == 0 || !s.lastRecorded.recorded {
		return
	}
	ordinal := s.lastRecorded.ordinal
	s.persistedAppendLog[n-1].OriginalOrdinal = &ordinal
	// The copy keeps the model its original was recorded with.
	s.persistedAppendLog[n-1].Model = s.lastRecorded.model
}

// tombstoneLastPairPersistedLocked replaces the most recently logged pair with
// an empty marker turn. The caller has just learned that pair's write recorded
// nothing, and the pair log feeds publishFoldTransaction's post-marker
// rewrite: a canceled round's unrecorded results riding that rewrite would
// become durable state a restart derives from. The replacement is a tombstone
// rather than a removal because a fold may hold a positional snapshot of this
// log taken under s.mu while the write was still in flight: removing the
// entry would shift every later pair into the dropped position, so a pair
// recorded after the failed one would fall below the snapshot's rewrite
// boundary and vanish from the resumed history. The tombstone is safe
// because the caller holds s.mu inside the same attentionMu hold that
// logged the pair, so the last entry is that pair's own.
func (s *Session) tombstoneLastPairPersistedLocked() {
	if n := len(s.persistedAppendLog); n > 0 {
		s.persistedAppendLog[n-1] = schema.Turn{}
	}
}

func (s *Session) appendTurnWithDurableTranscriptMessage(kind schema.TurnKind, live, persisted llm.Message) error {
	return s.appendPairedTurnVia(kind, live, persisted, s.writeTranscriptDurableLocked)
}

func (s *Session) appendPairedTurnVia(kind schema.TurnKind, live, persisted llm.Message, write func(schema.Turn) error) error {
	t := schema.NewTurn(kind, live)
	persistedTurn := t
	persistedTurn.Message = persisted
	err := s.appendTurnAfterTranscriptWrite(
		persistedTurn,
		func() error { return write(persistedTurn) },
		func() { s.history = append(s.history, t) },
	)
	if err != nil {
		s.emit(events.WarningData{Message: fmt.Sprintf("transcript write failed: %v", err)})
	}
	return err
}

// recordTurn adds a turn to the live model history and writes its persisted
// counterpart to the durable transcript — one atomic pair under attentionMu
// (append first, then the entry), for the same publication-transaction
// wholeness appendTurnAfterTranscriptWrite documents. The two turns differ
// only when a tool exposes explicitly private evidence; every other caller
// passes the same turn twice. It reports the entry's record.
func (s *Session) recordTurn(live, persisted schema.Turn) transcript.Record {
	return s.recordTurnOn(noBound, func(w events.WarningData) { s.emit(w) }, live, persisted)
}

// recordTurnOn is recordTurn with the sink a recording failure is reported on.
// A hook completion recorded on a close publication bound passes a sink that
// publishes on that bound AND cannot redispatch the hook: the failure is an
// EventWarning, and the ordinary warning path fires the Notification hook, so a
// writer that keeps refusing would dispatch that hook again on every completion
// it could not record. Diagnosis, not a fresh dispatch.
//
// bound is the publication bound the caller's record runs under, nil on the
// ordinary path. It reaches the fail-closed announcement too: a poisoned writer
// latches a refusal, and announcing it on the ordinary path would hold a full
// authoritative buffer until the whole close cascade expired.
func (s *Session) recordTurnOn(bound context.Context, warn func(events.WarningData), live, persisted schema.Turn) transcript.Record {
	live.SkillState = live.SkillState.Clone()
	persisted.SkillState = persisted.SkillState.Clone()
	s.attentionMu.Lock()
	s.mu.Lock()
	s.history = append(s.history, live)
	s.logPairPersistedLocked(persisted)
	s.lastRecorded = recordedOrdinal{}
	s.mu.Unlock()
	rec, err := s.recordTranscriptLocked(persisted, transcript.DoorBuffered, transcript.PlaceSession)
	if err == nil {
		s.mu.Lock()
		s.markLastPairOrdinalLocked()
		s.mu.Unlock()
	} else {
		// The write recorded nothing: the ordinary Append door returns an
		// error only when no complete line was recorded (a whole line that
		// landed but did not sync returns nil and is retained), so the pair
		// just logged must not ride the fold rewrite tail back in after the
		// markers as durable state the live side never settled. The live
		// turn stays for the caller's own failure handling.
		s.mu.Lock()
		s.tombstoneLastPairPersistedLocked()
		s.mu.Unlock()
	}
	s.attentionMu.Unlock()
	if err != nil {
		warn(events.WarningData{Message: fmt.Sprintf("transcript write failed: %v", err)})
	}
	s.surfaceTranscriptWarningsOn(bound, warn)
	return rec
}

// The transcript writer cannot exist for the whole of a session's life. Its
// header carries the rendered system prompt and the agent's starting task list,
// so it can only be opened once initSessionState has loaded plugins, skills and
// tools — and SessionStart hooks run inside that same window, producing turns
// while there is still nowhere to put them. transcript.Writer.Append is nil-safe
// (a session with no state directory has to be able to write without every call
// site nil-checking), which makes such a write indistinguishable from a
// successful one: it returns nil and the turn is gone. That is how kata qm9y
// lost every SessionStart hook exit with a green test suite.
//
// writeTranscript and writeTranscriptDurable are the only two doors to the
// writer, so no call site has to know which side of that window it is on. A turn
// written before attachTranscript is held; everything after goes straight
// through. Both report a held turn as written (nil error), which is accurate —
// it has not failed, it has not been flushed yet.
//
// A poisoned writer (transcript.ErrWriterPoisoned) is the opposite case and
// needs no special handling here: an append that failed partway and could not
// be rolled back leaves the writer refusing every later append, so each one
// returns that error through these same doors and reaches the caller's ordinary
// transcript-failure path — the warning, and for a turn-bearing caller the
// aborted turn. That is the intended behaviour for a transcript nothing further
// can safely be added to: every turn fails loudly until the session is
// restarted against the records the file still holds. What must never happen is
// the silent success a nil writer gives, which is why the writer answers with an
// error rather than dropping the turn.

// writeTranscriptLocked records t in the transcript through the buffered door
// with attentionMu held, holding t if the transcript writer is not yet
// attached. Callers such as the fold publication transaction commit entries
// under the same attentionMu hold that decided the publish, so no other
// writer's entry can interleave between the publish and the fold's compaction
// markers.
func (s *Session) writeTranscriptLocked(t schema.Turn) error {
	_, err := s.recordTranscriptLocked(t, transcript.DoorBuffered, transcript.PlaceSession)
	return err
}

// recordTranscriptLocked is the one door every session write to its transcript
// goes through, with attentionMu held: it stamps the entry with the session's
// configured model, holds a write through any door but the synced one made
// before the writer is attached (see holdTurnUntilTranscriptReady; the synced
// door refuses one instead), and otherwise appends through door with the given
// placement, reporting whether the entry was recorded. A held turn reports not
// recorded, with no error.
func (s *Session) recordTranscriptLocked(t schema.Turn, door transcript.Door, place transcript.Placement) (transcript.Record, error) {
	t = s.withEntryModel(t)
	if door == transcript.DoorSynced {
		s.mu.Lock()
		ready := s.transcriptReady
		s.mu.Unlock()
		if !ready {
			return transcript.Record{}, errors.New("transcript not ready: a synced write cannot be held before attach")
		}
	} else if s.holdTurnUntilTranscriptReady(t) {
		return transcript.Record{}, nil
	}
	writer := s.attachedTranscript()
	rec, err := writer.Record(t, transcript.RecordOptions{Door: door, Place: place})
	if err != nil && writer.Poisoned() {
		// The writer refuses everything from here: a served session stops.
		_ = s.failClosed(errTranscriptRefusesRecords())
	}
	s.mu.Lock()
	s.noteRecordedLocked(rec)
	s.mu.Unlock()
	if rec.Recorded && !s.holdTranscriptPublications {
		s.publishTranscriptLocked(rec.Offset + rec.Length)
	}
	return rec, err
}

// withEntryModel stamps t with the model the session is configured with, so
// the entry's usage can be priced from the entry alone, unless t already
// names one.
func (s *Session) withEntryModel(t schema.Turn) schema.Turn {
	if t.Model == "" {
		if profile := s.currentProfile(); profile != nil {
			t.Model = profile.Model()
		}
	}
	return t
}

// writeTranscriptDurableLocked records t in the transcript through the durable
// door with attentionMu held, fsyncing an attached writer before returning (and
// holding t if the transcript writer is not yet attached). Callers such as
// appendTurnAfterTranscriptWrite invoke it inside their attentionMu hold and
// drain retained-entry warnings once the lock releases.
func (s *Session) writeTranscriptDurableLocked(t schema.Turn) error {
	_, err := s.recordTranscriptLocked(t, transcript.DoorDurable, transcript.PlaceSession)
	return err
}

// writeTranscriptDeferredLocked records a turn whose fsync is left to the
// writer's SyncInterval gate: a failed write still rolls back and reports, so
// the turn is recorded or not recorded exactly as through the durable door,
// but a recorded turn is not fsynced on its own. It is the per-round door —
// the assistant turn — because a turn with many tool rounds would otherwise
// pay one fsync per round. The debt is settled by the next append that passes
// the interval, a synced write (the turn's completion, a delivered message, a
// terminal job result), or Close. A power crash can drop what the interval
// still covers; a process crash drops nothing handed to the OS.
//
// A turn whose loss a reader would see needs the durable or synced door
// instead: this one promises recorded, not durable.
func (s *Session) writeTranscriptDeferredLocked(t schema.Turn) error {
	_, err := s.recordTranscriptLocked(t, transcript.DoorDeferred, transcript.PlaceSession)
	return err
}

// writeTranscriptSyncedLocked is the durability owner's write: it records AND
// establishes durability, returning an error unless the entry is both a record
// and synced. Durability owners (the environment producer, skill carriers,
// delivery-commit records, client-mutation recovery, steering, the delegate
// seed) use it through the pair helper; ordinary producers use
// writeTranscriptDurableLocked and never inspect durability.
//
// Unlike the other doors it does NOT hold a turn before the transcript is
// attached: a held turn is neither recorded nor synced, and the flush at attach
// is a buffered append, so returning nil would tell an owner a turn is durable
// that a crash before attach would lose. A pre-attach synced write is an error
// the owner keeps its obligation pending on. (No production owner runs before
// attach — the tracker guard and the delegate seed's read-back both preclude it
// — so this is a fail-closed guard, not a live path.)
func (s *Session) writeTranscriptSyncedLocked(t schema.Turn) error {
	_, err := s.recordTranscriptLocked(t, transcript.DoorSynced, transcript.PlaceSession)
	return err
}

func (s *Session) attachedTranscript() *transcript.Writer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transcript
}

// drainTranscriptWarnings formats every pending retained-entry diagnostic and
// hands it to sink. A retained entry (whole line in the file, no fsync) is a
// record the durable append committed and returned nil for, so its sync failure
// reaches a client only here. The one formatter serves all three sinks (the two
// write-site emits and the pre-SESSION_START buffer). Emitting MUST run outside
// attentionMu and s.mu: an EventWarning fires the notification hook, which takes
// locks a transcript write is holding — the emit sinks call this after the
// locks release.
func (s *Session) drainTranscriptWarnings(sink func(events.WarningData)) {
	for _, err := range s.attachedTranscript().DrainWarnings() {
		sink(events.WarningData{Message: fmt.Sprintf("transcript write failed: %v", err)})
	}
}

// surfaceTranscriptWarnings emits every pending retained-entry diagnostic. Its
// callers have already released the transcript locks.
func (s *Session) surfaceTranscriptWarnings() {
	s.drainTranscriptWarnings(func(w events.WarningData) { s.emit(w) })
	s.announceFailClosed()
}

// surfaceTranscriptWarningsOn is surfaceTranscriptWarnings for a caller that owns
// the sink its diagnostics must reach.
//
// A poisoned transcript retains a diagnostic for every append it refused, so a
// hook completion recorded on a close publication bound would publish a burst of
// warnings -- and the ordinary warning path fires the Notification hook for each
// of them, re-entering the dispatcher whose own write just failed. The sink the
// caller supplied is the diagnostic path, which cannot do that.
func (s *Session) surfaceTranscriptWarningsOn(bound context.Context, warn func(events.WarningData)) {
	s.drainTranscriptWarnings(warn)
	s.announceFailClosedOn(bound)
}

func (s *Session) closeAttachedTranscript() error {
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	return s.attachedTranscript().Close()
}

// holdTurnUntilTranscriptReady queues t and reports true when the session has
// not yet reached attachTranscript. The caller snapshots the attached writer
// under the same session lock after this readiness check.
func (s *Session) holdTurnUntilTranscriptReady(t schema.Turn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.transcriptReady {
		return false
	}
	s.pendingTranscriptTurns = append(s.pendingTranscriptTurns, t)
	return true
}

// attachTranscript installs the session's transcript writer and releases the
// turns recorded before it existed. It is the single readiness transition and
// must run on every construction path, including the ones that produce no
// writer: w is nil for a session with no state directory and for one whose
// writer could not be opened, and marking those ready is what stops their turns
// accumulating for the session's lifetime.
func (s *Session) attachTranscript(w *transcript.Writer, existing []transcript.Entry) {
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	s.mu.Lock()
	s.setTranscriptLocked(w)
	s.seedExecutionFrontier(existing)
	s.transcriptReady = true
	held := s.pendingTranscriptTurns
	s.pendingTranscriptTurns = nil
	s.mu.Unlock()
	s.registerTranscriptFrontier()
	for _, t := range held {
		rec, err := w.Record(t, transcript.RecordOptions{Door: transcript.DoorBuffered, Place: transcript.PlaceSession})
		s.mu.Lock()
		s.noteRecordedLocked(rec)
		s.mu.Unlock()
		if err != nil {
			// Buffered, not emitted directly (kata et0x): attachTranscript always
			// runs before its caller's emitSessionStartEnvelope, so SESSION_START
			// has not fired yet — same reasoning as the NewSession transcript-
			// create-failed warning above it in the buffer's doc comment.
			s.pendingTranscriptWarnings = append(s.pendingTranscriptWarnings, events.WarningData{Message: fmt.Sprintf("transcript write failed: %v", err)})
		}
	}
	s.publishTranscriptLocked(w.RecordedLength())
	// A replayed append whose whole line landed but did not sync returned nil
	// and queued its diagnostic on the writer; buffer it the same way, since
	// SESSION_START has not fired yet.
	s.drainTranscriptWarnings(func(warning events.WarningData) {
		s.pendingTranscriptWarnings = append(s.pendingTranscriptWarnings, warning)
	})
}

// sclock returns the session's injected clock. Production always sets s.clock
// via NewSession/loadSession (defaulting to clock.Real()); a few tests build a
// *Session struct literal directly without one, so this falls back to a real
// clock rather than nil-panicking. The fuzz harness always injects a fake, so it
// never sees the fallback.
func (s *Session) sclock() clock.Clock {
	if s.clock == nil {
		return clock.Real()
	}
	return s.clock
}

// assistantHistoryMessage makes malformed tool arguments replayable in semantic
// history without changing the provider response used for tool validation: the
// raw bytes are kept in RawArguments so the durable record still shows what
// the model actually sent, while Arguments carries the {} form every provider
// round-trip needs.
func assistantHistoryMessage(message llm.Message) llm.Message {
	var content []llm.ContentPart
	for i, part := range message.Content {
		if part.ToolCall == nil || len(part.ToolCall.Arguments) == 0 || json.Valid(part.ToolCall.Arguments) {
			continue
		}
		if content == nil {
			content = append([]llm.ContentPart(nil), message.Content...)
		}
		call := *part.ToolCall
		call.RawArguments = string(call.Arguments)
		call.Arguments = json.RawMessage(`{}`)
		content[i].ToolCall = &call
	}
	if content != nil {
		message.Content = content
	}
	return message
}

// appendAssistantTurn appends an assistant turn that carries the full response
// metadata (usage stats and response ID) alongside the message content.
func (s *Session) appendAssistantTurn(resp llm.Response, finalAttempt ModelAttemptMetadata) error {
	return s.appendAssistantTurnWithAcceptance(resp, finalAttempt, false, false, false)
}

func (s *Session) appendAssistantTurnWithAcceptance(resp llm.Response, finalAttempt ModelAttemptMetadata, acceptedReply, quietYield, quietCompanion bool) error {
	t := schema.Turn{
		AcceptedReply:                   acceptedReply,
		QuietYield:                      quietYield,
		QuietCompanion:                  quietCompanion,
		Kind:                            schema.TurnAssistant,
		Message:                         assistantHistoryMessage(resp.Message),
		Timestamp:                       s.sclock().Now().UTC(),
		Usage:                           resp.Usage,
		ResponseID:                      resp.ID,
		ResponseIDHash:                  finalAttempt.ResponseIDHash,
		ResponseProvider:                resp.Provider,
		ResponseModel:                   resp.Model,
		ResponseRequestModel:            finalAttempt.RequestModel,
		AttemptGroupID:                  finalAttempt.AttemptGroupID,
		ResponseEndpointFamily:          finalAttempt.EndpointFamily,
		ResponseProtocol:                finalAttempt.Protocol,
		ResponseEndpoint:                finalAttempt.EndpointURL,
		ResponseStorageScopeFingerprint: finalAttempt.StorageScopeFingerprint,
		ResponseRequestFingerprint:      finalAttempt.RequestFingerprint,
		ResponseContextMarker:           finalAttempt.ContextMarker,
		ResponseContextBoundary:         finalAttempt.ContextBoundary,
		ResultToolName:                  s.resultToolNameOverride(),
		RoundID:                         s.roundIDForModelCall(),
	}
	t.Authorship = s.roundTranscriptAuthorship()
	if acceptedReply {
		t.CausalKnown, t.Causal = true, s.activeCausalProvenance()
	}
	err := func() error {
		s.attentionMu.Lock()
		defer s.attentionMu.Unlock()
		if request := finalAttempt.conversation; request != nil {
			// Compaction may publish while the provider is running. Base the
			// delta on the current canonical frontier under the transcript door,
			// but keep exactly the inputs that this request actually presented.
			s.mu.Lock()
			p := request.provenance
			p.FrontierID = s.conversation.frontierID
			t.ConversationFrontier = s.conversationDeltaLocked(p.RequestID, request.inputs)
			if t.ConversationFrontier != nil {
				p.FrontierID = t.ConversationFrontier.ID
			}
			t.ConversationProvenance = &p
			s.mu.Unlock()
		}
		return s.appendTurnAfterTranscriptWriteLocked(
			t,
			func() error { return s.writeTranscriptDeferredLocked(t) },
			func() {
				s.history = append(s.history, t)
				// The round's entry is recorded: the next model call is a new
				// round (a pause_turn continuation, a bare-text retry, the next
				// tool round).
				s.roundID = ""
				s.noteAgentMessageLocked(lastAgentText(t.Message))
			},
		)
	}()
	s.surfaceTranscriptWarnings()
	if err != nil {
		s.emit(events.WarningData{Message: fmt.Sprintf("transcript write failed: %v", err)})
	}
	return err
}

// maybeAutoSave persists the session metadata if StateDir is configured.
// Writes only lightweight SessionMeta (~500 bytes), not the full history.
// The conversation history is already durably recorded by the transcript JSONL.
func (s *Session) maybeAutoSave() {
	if err := s.autoSaveMeta(); err != nil {
		s.emit(events.WarningData{
			Message: fmt.Sprintf("auto-save failed: %v", err),
		})
	}
}

// autoSaveMeta persists the session metadata, reporting the write outcome so
// mutation paths can refuse to journal success for a write that never landed.
// maybeAutoSave keeps the warn-only contract for the non-mutation callers.
//
// autoSaveMetaLocked is the same write for callers that already hold
// metaSaveMu (a mutation+rollback critical section): the lock must stay held
// from the save attempt through the rollback, so a concurrent maybeAutoSave
// cannot snapshot a transient mutation between a failed save and its restore
// and persist it to disk.
func (s *Session) autoSaveMeta() error {
	if s.stateDir == "" {
		return nil
	}
	s.metaSaveMu.Lock()
	defer s.metaSaveMu.Unlock()
	return s.saveSessionMetaLocked()
}

func (s *Session) autoSaveMetaLocked() error {
	if s.stateDir == "" {
		return nil
	}
	return s.saveSessionMetaLocked()
}

func (s *Session) saveSessionMetaLocked() error {
	// The metadata write is the durability point for a notes mutation whose
	// value is still staged in the live store, so persist the live store.
	// Meta() reports the published committed cut to readers; handing that to
	// this write would persist the pre-mutation notes and lose the mutation on
	// the next restore.
	human, agentNote, urls, _ := s.notesLiveSnapshot()
	meta := s.metaWithNotes(human, agentNote, urls)
	if fs := s.cfg.testOnly.metaFS; fs != nil {
		return schema.SaveSessionMetaWithFS(fs, s.stateDir, meta)
	}
	return schema.SaveSessionMeta(s.stateDir, meta)
}

// TranscriptPath returns the path to this session's transcript JSONL file,
// or empty string if state persistence is not enabled.
func (s *Session) TranscriptPath() string {
	if s.stateDir == "" {
		return ""
	}
	return schema.SessionTranscriptPath(s.stateDir, s.id)
}
