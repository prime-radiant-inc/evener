package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/clock"
	"primeradiant.com/serf/agent/internal/contextmgr"
	"primeradiant.com/serf/agent/internal/goal"
	"primeradiant.com/serf/agent/internal/hooks"
	"primeradiant.com/serf/agent/internal/installid"
	"primeradiant.com/serf/agent/internal/mcp"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/mcpconfig"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/provenance"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/skill"
	"primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

// Session holds the state for a single agent conversation, including its
// configuration, LLM client and provider profile, event channel, conversation
// history, registered tools, context-management state, subagents, plugins, MCP
// connections, and persistence settings.
type Session struct {
	id             string
	cfg            SessionConfig
	client         *llm.Client
	profile        *provider.Profile
	resolveProfile func(ref string) (*provider.Profile, error) // cross-provider resolver; may be nil
	// httpClient issues the web_fetch HTTP GET. Nil means the production default
	// (http.DefaultClient); tests set it on their own session to serve a fabricated
	// response through an injected transport without touching the network.
	httpClient httpDoer
	env        execenv.ExecutionEnvironment
	clock      clock.Clock
	stateDir   string
	installID  string
	// strictTranscriptMaxLineBytes caps a child transcript line during resumability
	// checks. Zero means the production default (transcriptJSONLMaxLineBytes); tests
	// set a small value on their own session to exercise the oversized-line path
	// without mutating a shared global.
	strictTranscriptMaxLineBytes int
	// delegateRestoreResumeHistory builds the resume history for a delegate
	// restore preflight. Nil means the production default (ResumeHistory); tests
	// set it on their own session to observe/shape the strict-preflight entries
	// without mutating a shared global that concurrent restores would invoke.
	delegateRestoreResumeHistory func([]transcript.Entry) []schema.Turn

	events       chan events.SessionEvent
	eventsMu     sync.RWMutex // guards send-vs-close on events; all sends go through emit()
	eventsClosed bool         // set under eventsMu.Lock immediately before close(events)
	envInfo      schema.EnvironmentInfo

	// --- Synchronization / lock discipline ---
	//
	// The turn loop (ProcessInput → processOneInput) is the primary owner of
	// most mutable state. External callers — SetModel/SetReasoningEffort,
	// Steer/Enqueue/DrainAsSteer, Snapshot/State/DetailedStatus, and Close —
	// run on other goroutines and race it, so the primitives below guard the
	// shared fields. Several collaborators carry their OWN locks and are NOT
	// covered by mu: contextMgr (contextmgr.Manager.mu), reg (tool.Registry.mu),
	// subagents (subagentManager.mu / per-child sub.mu), taskStore
	// (TaskStore.mu — note it may be SHARED with child sessions when
	// ShareTasksWithChildren is set), and transcript (transcript.Writer.mu).
	//
	// mu guards: history, state, closing, turns, modelResponses, totalRounds,
	//   sessionEndEmitted, profile (swapped here; read via currentProfile()),
	//   env (swapped here; read via currentEnv()), envInfo (swapped alongside
	//   env so the two never observe a torn intermediate state), the mutable
	//   cfg knobs (ReasoningEffort, command timeouts, MaxToolRoundsPerInput),
	//   cachedSystemPrompt, cachedToolDefs, the comm communicate-result,
	//   steeringQueue, activeProvenance, followups, inputQueue,
	//   loopDetectionCount, the task* reminder counters, depth, the goalInTurn
	//   flag and kickFunc callback, the naming name-state, and the worktree
	//   occupancy fields (worktreeRestoreEnv, worktreeCurrentPath,
	//   worktreeCurrentManaged, worktreeGitVersionOK, worktreeLiveWorkStub). It
	//   does NOT guard reg — the tool.Registry self-synchronizes.
	mu sync.Mutex

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
	// serf-managed worktree (entered via create, or switch by name/managed
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
	worktreeRestoreEnv     *execenv.LocalExecutionEnvironment
	worktreeCurrentPath    string
	worktreeCurrentManaged bool
	worktreeGitVersionOK   bool
	worktreeLiveWorkStub   func(path string) []string

	// responseSideEffectsMu serializes a response's user-visible side-effect
	// bundle (emit + appendTurn + counter bump) against teardown.
	// LOCK ORDER: responseSideEffectsMu > mu (Close acquires it before mu).
	responseSideEffectsMu         sync.Mutex
	toolEventsWG                  sync.WaitGroup // in-flight ToolCallStart/End emit pairs; Close() joins before closing events
	sendersWG                     sync.WaitGroup // detached event emitters (subagent runs, session namer); Add happens under mu gated on closing so it happens-before Close()'s join
	state                         SessionState
	closing                       bool
	turns                         int // user input count (for MaxTurns enforcement)
	modelResponses                int // LLM round-trip count (for meta.json turn_count)
	history                       []schema.Turn
	responsesContinuationDisabled map[responsesContinuationDisabledKey]bool

	fork forkInfo

	reg *tool.Registry

	steeringQueue []steeringMessage
	followups     []string

	// activeProvenance is the causal provenance carried by the input currently
	// being processed. It is stamped onto every event the turn emits, reset to
	// empty on each new external top-level input, and unioned with consumed
	// steering messages' provenance. The zero value means "no watch origin".
	activeProvenance provenance.Causal
	// completedInputProvenance is the active provenance captured at the most
	// recent processing boundary. Subagent follow-up turns use it to preserve
	// watch keys accumulated during the just-finished run.
	completedInputProvenance provenance.Causal
	// activeEntryKind names the entry currently being processed. It lets tools
	// distinguish ordinary user turns from watch-delivery callbacks without
	// duplicating provenance state.
	activeEntryKind EntryKind

	// inputQueue holds messages submitted via Enqueue while a turn is in
	// flight. Kata 111a: text typed during a running turn returns to the
	// user immediately and is processed as a fresh user turn once the
	// active turn completes. DrainAsSteer (kata 0bq1) collapses any queued
	// messages into a single steering message sent to the in-flight turn.
	// Each entry carries text plus any attached images (kata t5j6) so the
	// composer can queue image-bearing messages alongside text.
	inputQueue []queuedInput
	// queueEventsMu makes an inputQueue mutation and its EventQueueChanged emit
	// atomic to external observers (Enqueue/DrainAsSteer/pop/pushQueueHead).
	// LOCK ORDER: queueEventsMu > mu.
	queueEventsMu sync.Mutex

	// communicate/result tool state (transient, reset each processOneInput call)
	comm communicateResult
	// watchCallbackDelivered is a per-processOneInput latch. A watch-origin turn
	// may callback via delegate_send(to=caller) or terminal communicate; once one
	// route succeeds, the other must not duplicate the parent steer.
	watchCallbackDelivered bool

	// subagents
	depth                            int
	delegationAllowance              int          // mu-guarded; allowance to grant further sub-agent delegation levels
	treeCounter                      *treeCounter // tree-wide running delegate-turn counter (spec §4)
	subagents                        *subagentManager
	delegateRestoreAfterClaim        func()
	delegateRestoreBeforeTrack       func()
	delegateRestoreBeforeSideEffects func(*Session)

	// pendingJobNotifs is the durable per-parent queue of pending job-completion
	// notifications. It is drop-safe and drained later by a notification turn.
	// notifyFunc, when set by the server, kicks the drain; it stays nil here and a
	// nil kick is a no-op.
	//
	// Guarded by its own mutex; never taken while holding sub.mu or the manager
	// mutex.
	pendingJobNotifsMu sync.Mutex
	pendingJobNotifs   []jobNotification
	notifyFunc         func()
	jobNotifyRetry     notificationRetry

	jobManager *jobManager

	// context management
	contextMgr *contextmgr.Manager
	strategy   contextmgr.Strategy

	// skills discovered at session startup
	skills            map[string]skill.SkillMeta
	embeddedSkillsDir string // temp dir for extracted embedded skills; cleaned up in Close

	// MCP server connections
	mcpMgr   *mcp.Manager
	mcpTools []llm.ToolDefinition

	// Plugin-provided components
	plugins             []plugin.Instance
	pendingPluginEvents []events.PluginLoadedData
	pendingHookWarnings []events.WarningData
	hookRunner          *hooks.Runner
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
	// unsupportedPluginHookEvents accumulates all Claude-recognized events
	// declared by loaded plugins that serf does not currently fire.
	// Populated by initPlugins; used by DetailedStatus for diagnostics.
	unsupportedPluginHookEvents map[plugin.HookEvent]bool

	// Tool names registered during session initialization (not custom).
	coreToolNames map[string]bool

	// Project docs loaded once at session init and cached for lifetime.
	projectDocs          []ProjectDoc
	projectDocsTruncated bool

	// read-before-write guardrail
	readFiles   map[string]bool
	readFilesMu sync.RWMutex

	// SESSION_END deduplication: emitted exactly once across ProcessInput and Close.
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

	// goalInTurn is true while ProcessInputKind is running an input through to
	// completion. It is guarded by s.mu and exists to close the §7 idle-kick
	// race: SetGoal/ClearGoal read it under s.mu to decide between kicking an
	// idle session and deferring to the running drain-loop gate. ProcessInputKind
	// sets it at entry and clears it as its last act before going idle, so
	// "set goal + read flag" and "clear flag + go idle" are mutually exclusive.
	goalInTurn bool
	// kickFunc, when set via SetKickFunc, lets an idle SetGoal start the goal
	// loop immediately by feeding the first continuation prompt back into the
	// serve loop's input channel. It is a callback because the agent module must
	// not import server; serve.go wires it. Guarded by s.mu.
	kickFunc func(prompt string)

	// task reminder tracking
	taskToolLastRound int  // totalRounds value at last task_list tool call
	taskToolEverUsed  bool // whether task_list has ever been called
	taskNudgeFired    bool // whether the "consider using task_list" nudge has fired
	totalRounds       int  // cumulative tool rounds across all inputs

	// self-compaction state (compact tool)
	pinnedNote          string // note awaiting handoff at the next compaction (agent- or elicitor-authored); injected verbatim then cleared
	pendingInstructions string // compaction_instructions awaiting the round-tail force
	forceRequested      bool   // a compact tool call is pending this round
	nudgedSinceCompact  bool   // warning-nudge latch; reset on any compaction

	// elicitNoteFn overrides the note-elicitation call (tests inject a stub); nil
	// uses contextMgr.ElicitNote (Variant B of the forced-note mechanism — see
	// maybeElicitNoteBeforeCompaction).
	elicitNoteFn func(context.Context, []schema.Turn) (string, error)

	// stuck detection
	loopDetectionCount int // how many times loop detection has fired

	// transcript writer (nil when StateDir is empty)
	transcript *transcript.Writer

	// Cached tool definitions.
	cachedToolDefs []llm.ToolDefinition

	systemPromptOverride string
	cachedSystemPrompt   string
	promptSourceLog      []promptSource
}

type notificationRetry struct {
	active     bool
	delay      time.Duration
	generation int
}

const (
	jobNotificationRetryInitialDelay = 250 * time.Millisecond
	jobNotificationRetryMaxDelay     = 5 * time.Second
)

func (s *Session) enqueueJobNotification(n jobNotification) {
	s.pendingJobNotifsMu.Lock()
	defer s.pendingJobNotifsMu.Unlock()
	s.pendingJobNotifs = append(s.pendingJobNotifs, n)
}

func (s *Session) enqueueJobNotificationAndNotify(n jobNotification) {
	s.enqueueJobNotification(n)
	s.notify()
}

func (s *Session) requeueJobNotifications(notifs []jobNotification) {
	if len(notifs) == 0 {
		return
	}
	s.pendingJobNotifsMu.Lock()
	s.pendingJobNotifs = append(notifs, s.pendingJobNotifs...)
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

// peekNotifications reports how many notifications are pending WITHOUT draining
// them. The drain-loop tail uses it to decide whether to run a notification turn
// next; the actual drain stays in acceptNotificationInput so the queue is consumed
// exactly once, inside the turn that surfaces it.
func (s *Session) peekNotifications() int {
	s.pendingJobNotifsMu.Lock()
	defer s.pendingJobNotifsMu.Unlock()
	return len(s.pendingJobNotifs)
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
	pending := len(s.pendingJobNotifs) > 0
	s.pendingJobNotifsMu.Unlock()
	if pending {
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

func (s *Session) scheduleJobNotificationRetryLocked() {
	if s.jobNotifyRetry.active {
		return
	}
	delay := s.jobNotifyRetry.delay
	if delay <= 0 {
		delay = jobNotificationRetryInitialDelay
	}
	s.jobNotifyRetry.active = true
	s.jobNotifyRetry.generation++
	generation := s.jobNotifyRetry.generation
	s.sclock().AfterFunc(delay, func() {
		s.pendingJobNotifsMu.Lock()
		if s.jobNotifyRetry.generation != generation {
			s.pendingJobNotifsMu.Unlock()
			return
		}
		s.jobNotifyRetry.active = false
		pending := len(s.pendingJobNotifs) > 0
		nextDelay := delay * 2
		if nextDelay > jobNotificationRetryMaxDelay {
			nextDelay = jobNotificationRetryMaxDelay
		}
		if pending {
			s.jobNotifyRetry.delay = nextDelay
		} else {
			s.jobNotifyRetry.delay = jobNotificationRetryInitialDelay
		}
		s.pendingJobNotifsMu.Unlock()
		if pending {
			s.notify()
		}
	})
}

func (s *Session) resetJobNotificationRetry() {
	s.pendingJobNotifsMu.Lock()
	if len(s.pendingJobNotifs) > 0 {
		s.pendingJobNotifsMu.Unlock()
		return
	}
	s.jobNotifyRetry.generation++
	s.jobNotifyRetry.active = false
	s.jobNotifyRetry.delay = jobNotificationRetryInitialDelay
	s.pendingJobNotifsMu.Unlock()
}

// communicateResult records whether and how the agent delivered a result via
// the communicate/result tool during the current ProcessInput call. It is
// transient — reset at the top of each call, then read back by Communicated,
// CommunicateOutput, and the turn loop's deliver step. Guarded by s.mu.
type communicateResult struct {
	called     bool // terminal communicate/result was invoked this turn
	text       string
	reply      string
	output     string // canonical structured output (CommunicateOutput)
	structured any    // raw args["output"] object before communicate canonicalization
}

// sessionName holds the session's auto-generated display name and its
// provenance. The namer goroutine (session_namer.go) assigns it; Meta and
// Snapshot read it. Guarded by s.mu.
type sessionName struct {
	value         string    // display name ("" until assigned)
	source        string    // provenance tag (sessionNameSource*)
	updated       time.Time // when value last changed
	set           bool      // a name has been assigned
	promptPending bool      // a naming LLM call is in flight
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

// The contextmgr.Host seam is satisfied by the ctxHost adapter (context_host.go),
// not by *Session directly: contextmgr.Host requires exported Emit / Snapshot /
// WithResponseSideEffects methods, and we do not want those on the public Session
// surface, so the adapter forwards them to the Session's internal methods.

// StateDir returns the session's configured state directory.
func (s *Session) StateDir() string { return s.stateDir }

// Profile returns the session's current provider profile.
func (s *Session) Profile() *provider.Profile { return s.currentProfile() }

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

// SetReasoningEffort updates the reasoning effort used for future LLM calls.
// Takes effect on the next request (spec).
func (s *Session) SetReasoningEffort(effort string) {
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		return
	}
	// Normalize disable-aliases (none/off/...) to "" so a runtime "none" omits
	// reasoning effort rather than forwarding the literal to the provider, matching
	// the CLI resolver.
	s.cfg.ReasoningEffort = llm.NormalizeReasoningEffort(effort)
	s.mu.Unlock()
	// Flush meta.json so a daemon crash before the next happy-path turn
	// boundary doesn't leave on-disk cfg stale. Kata wnfz. maybeAutoSave
	// re-acquires s.mu via s.Meta(), so the lock must be released first.
	s.maybeAutoSave()
}

// resolveProfileForRef resolves a model ref to a *provider.Profile. When the
// ref is classified as a cross-provider switch (prefixActionSwitch) AND the
// session has a resolver, the resolver is called. Otherwise the current
// profile's WithModel is used (handles same-provider, strip, and keep cases).
func (s *Session) resolveProfileForRef(base *provider.Profile, ref string) (*provider.Profile, bool, error) {
	if base.CrossProviderRef(ref) && s.resolveProfile != nil {
		resolved, err := s.resolveProfile(ref)
		if err != nil {
			return nil, false, err
		}
		if resolved != nil {
			return resolved, true, nil
		}
	}
	return base.WithModel(ref), false, nil
}

// reapplyProviderSpecificTools updates the live tool registry when the session
// switches between providers. Currently the only provider-specific function
// tool is the Gemini web_search executor:
//   - switching TO a google-tag profile: register the real web_search executor
//   - switching AWAY from a google-tag profile: remove web_search from the
//     registry so it doesn't collide with the adapter-injected server tool
//     used by OpenAI/Anthropic native web search.
func (s *Session) reapplyProviderSpecificTools(oldTag, newTag string) {
	switch {
	case newTag == "google" && oldTag != "google":
		// Switching to Gemini: wire the real web_search executor.
		_ = s.reg.Register(tool.RegisteredTool{
			Tool: llm.Tool{Definition: tool.DefWebSearch()},
			Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
				query := fmt.Sprint(args["query"])
				return s.webSearch(ctx, query)
			},
		})
	case oldTag == "google" && newTag != "google":
		// Switching away from Gemini: remove the function tool so non-Gemini
		// providers can use their own native web-search mechanism.
		s.reg.Remove("web_search")
	}
}

// SetModel changes the model used for future LLM calls.
// Takes effect on the next request.
func (s *Session) SetModel(model string) {
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		return
	}
	oldTag := s.profile.BehaviorTag()
	nextProfile, crossProvider, err := s.resolveProfileForRef(s.profile, model)
	if err != nil {
		s.mu.Unlock()
		return
	}
	if crossProvider {
		nextProfile = nextProfile.WithCommunicateOverridesFrom(s.profile)
	}
	client := s.client
	s.mu.Unlock()

	nextProfile = resolveLiveModelProfileWithTimeout(client, nextProfile)

	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		return
	}
	newTag := nextProfile.BehaviorTag()
	s.profile = nextProfile
	if s.contextMgr != nil {
		s.contextMgr.SetProfile(s.profile)
	}
	if crossProvider && s.reg != nil {
		s.reapplyProviderSpecificTools(oldTag, newTag)
	}
	s.rebuildToolDefsCache()
	s.refreshSystemPromptCache(s.env) // already holding s.mu; currentEnv() would deadlock
	s.mu.Unlock()
	// Flush meta.json so a daemon crash before the next happy-path turn
	// boundary doesn't leave on-disk model stale. Kata wnfz. maybeAutoSave
	// re-acquires s.mu via s.Meta(), so the lock must be released first.
	s.maybeAutoSave()
}

// SetTimeout changes the default command timeout for shell tool invocations.
// Takes effect on the next tool execution.
func (s *Session) SetTimeout(timeoutMS int) {
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		return
	}
	s.cfg.DefaultCommandTimeoutMS = timeoutMS
	s.mu.Unlock()
	// Flush meta.json so a daemon crash before the next happy-path turn
	// boundary doesn't leave on-disk cfg stale. Kata wnfz. maybeAutoSave
	// re-acquires s.mu via s.Meta(), so the lock must be released first.
	s.maybeAutoSave()
}

func (s *Session) applyModelRequestMetadata(profile *provider.Profile, req *llm.Request) {
	if req == nil {
		return
	}
	openAIPromptCacheSupported := profile.BehaviorTag() == "openai" && openAIModelSupports24hPromptCache(req.Model)
	if strings.TrimSpace(s.id) != "" {
		req.SessionID = s.id
		req.ThreadID = s.id
		if openAIPromptCacheSupported && strings.TrimSpace(req.PromptCacheKey) == "" {
			req.PromptCacheKey = "serf-session-" + s.id
		}
	}
	if openAIPromptCacheSupported && strings.TrimSpace(req.PromptCacheRetention) == "" {
		req.PromptCacheRetention = "24h"
	}
	if strings.TrimSpace(s.installID) != "" {
		if req.ClientMetadata == nil {
			req.ClientMetadata = map[string]string{}
		}
		req.ClientMetadata[installid.CodexInstallationIDMetadataKey] = s.installID
	}
}

func openAIModelSupports24hPromptCache(model string) bool {
	model = strings.TrimSpace(model)
	return openAIModelFamilyMatch(model, "gpt-5") || openAIModelFamilyMatch(model, "gpt-4.1")
}

func openAIModelFamilyMatch(model, family string) bool {
	if model == family {
		return true
	}
	return strings.HasPrefix(model, family+"-") || strings.HasPrefix(model, family+".")
}

// Communicated reports whether communicate was called during the most recent
// ProcessInput invocation.
func (s *Session) Communicated() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.comm.called
}

// CommunicateOutput returns the canonical structured output from the most recent
// communicate call in the current ProcessInput invocation.
func (s *Session) CommunicateOutput() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.comm.output
}

// CommunicateStructured returns the raw structured output object from the most
// recent communicate call, before normalizeNodeOutput canonicalization.
func (s *Session) CommunicateStructured() any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.comm.structured
}

// extractOriginalPrompt returns the text of the first user input in the session history.
// If compaction removed it, falls back to the SubagentTask from config.
func (s *Session) extractOriginalPrompt() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.history {
		if t.Kind == schema.TurnUserInput {
			return t.Message.Text()
		}
	}
	return s.cfg.spawn.subagentTask
}

func (s *Session) appendTurn(kind schema.TurnKind, m llm.Message) {
	t := schema.NewTurn(kind, m)
	s.mu.Lock()
	s.history = append(s.history, t)
	s.mu.Unlock()
	if err := s.transcript.Append(t); err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("transcript write failed: %v", err)})
	}
}

func (s *Session) appendTurnDurably(kind schema.TurnKind, m llm.Message) error {
	t := schema.NewTurn(kind, m)
	if err := s.transcript.AppendDurable(t); err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("transcript write failed: %v", err)})
		return err
	}
	s.mu.Lock()
	s.history = append(s.history, t)
	s.mu.Unlock()
	return nil
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

// appendAssistantTurn appends an assistant turn that carries the full response
// metadata (usage stats and response ID) alongside the message content.
func (s *Session) appendAssistantTurn(resp llm.Response, finalAttempt ModelAttemptMetadata) {
	t := schema.Turn{
		Kind:                            schema.TurnAssistant,
		Message:                         resp.Message,
		Timestamp:                       s.sclock().Now().UTC(),
		Usage:                           resp.Usage,
		ResponseID:                      resp.ID,
		ResponseIDHash:                  finalAttempt.ResponseIDHash,
		ResponseProvider:                resp.Provider,
		ResponseModel:                   resp.Model,
		ResponseRequestModel:            finalAttempt.RequestModel,
		ResponseEndpoint:                finalAttempt.EndpointURL,
		ResponseStorageScopeFingerprint: finalAttempt.StorageScopeFingerprint,
		ResponseRequestFingerprint:      finalAttempt.RequestFingerprint,
		ResponseContextMarker:           finalAttempt.ContextMarker,
	}
	s.mu.Lock()
	s.history = append(s.history, t)
	s.mu.Unlock()
	if err := s.transcript.Append(t); err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("transcript write failed: %v", err)})
	}
}

// maybeAutoSave persists the session metadata if StateDir is configured.
// Writes only lightweight SessionMeta (~500 bytes), not the full history.
// The conversation history is already durably recorded by the transcript JSONL.
func (s *Session) maybeAutoSave() {
	if s.stateDir == "" {
		return
	}
	meta := s.Meta()
	if err := schema.SaveSessionMeta(s.stateDir, meta); err != nil {
		s.emit(events.EventWarning, events.WarningData{
			Message: fmt.Sprintf("auto-save failed: %v", err),
		})
	}
}

// sessionsSubdir is the directory, under a session's StateDir, where its
// per-session files live: the transcript and log JSONL written by package
// agent, alongside the meta.json/snapshot written by package schema (which
// keeps its own private copy of this name).
const sessionsSubdir = "sessions"

// TranscriptPath returns the path to this session's transcript JSONL file,
// or empty string if state persistence is not enabled.
func (s *Session) TranscriptPath() string {
	if s.stateDir == "" {
		return ""
	}
	return filepath.Join(s.stateDir, sessionsSubdir, s.id+".transcript.jsonl")
}
