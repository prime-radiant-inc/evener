package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/envctx"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/clock"
	"primeradiant.com/evener/agent/internal/contextmgr"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/internal/worktree"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/task"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

// SessionConfig holds the configuration options for an agent session,
// covering tool-round and turn limits, command timeouts, prompt composition,
// context-management strategy, sub-agent behavior, LLM retry and fallback
// settings, and session persistence. Zero-valued fields are filled in by
// applyDefaults where defaults apply.
type SessionConfig struct {
	// LifetimeContext owns this session tree: `evener run` supplies its run
	// context (SIGINT- and --timeout-derived) and `evener serve` its shutdown
	// context, so cancelling either ends the tree's own context immediately
	// rather than when Close finally runs. Nil is the library/test shape --
	// the tree roots at Background and only Close can cancel it. Not persisted.
	LifetimeContext context.Context `json:"-"`
	artifactStore   artifactStore

	// Project is the resolved canonical project identity for this launch. It is
	// separate from the execution environment's active working directory, which
	// may be a linked worktree.
	Project identifier.Project `json:"-"`
	// MaxToolRoundsPerInput caps how many tool-call rounds a single
	// ProcessInput may run before the turn stops with a TURN_LIMIT event.
	// Zero defaults to unlimited; set an explicit positive value to cap it.
	// Negative means unlimited. Loop detection (enabled by default) guards
	// against runaway repeated tool calls even without a round cap.
	MaxToolRoundsPerInput int `json:"max_tool_rounds_per_input,omitempty"`

	// MaxTurns caps the number of user inputs the session will accept over its
	// lifetime; the (N+1)th input stops with a TURN_LIMIT event. Zero means
	// unlimited.
	MaxTurns int `json:"max_turns,omitempty"`

	// DefaultCommandTimeoutMS is the timeout applied to a shell/exec tool call
	// when the model does not request one. Zero defaults to 10000 (10s).
	DefaultCommandTimeoutMS int `json:"default_command_timeout_ms,omitempty"`

	// MaxCommandTimeoutMS is the ceiling on any per-command timeout the model
	// may request. Zero defaults to 600000 (10m).
	MaxCommandTimeoutMS int `json:"max_command_timeout_ms,omitempty"`

	// MaxSubagentDepth limits how deeply sub-agents may spawn further
	// sub-agents (root session is depth 0). Zero defaults to 2.
	MaxSubagentDepth int `json:"max_subagent_depth,omitempty"`

	// MaxConcurrentDelegateTurns bounds concurrently running delegate turns
	// across the whole session tree (the tree-counter cap). Zero defaults to
	// defaultMaxConcurrentDelegateTurns (50). Idle delegates hold no slot.
	MaxConcurrentDelegateTurns int `json:"max_concurrent_delegate_turns,omitempty"`

	// MaxRetainedTerminal bounds how many terminal child records
	// (completed|failed|cancelled|exhausted) the subagent manager retains per
	// parent. Zero defaults to defaultMaxRetainedTerminal (2048).
	MaxRetainedTerminal int `json:"max_retained_terminal,omitempty"`

	// ToolOutputLimits overrides default per-tool truncation behavior.
	ToolOutputLimits map[string]schema.ToolOutputLimit `json:"tool_output_limits,omitempty"`

	// UserInstructionOverride is appended to the end of the system prompt (highest priority).
	UserInstructionOverride string `json:"user_instruction_override,omitempty"`

	// AgentName selects a persona for prompt composition. When set, the persona's
	// role prompt is used instead of the default agent profile. Looked up from
	// built-in agents, then plugin agents.
	AgentName string `json:"agent_name,omitempty"`

	// ReasoningEffort is passed through to the Unified LLM request when non-empty.
	// Valid values are provider-dependent but typically include: low|medium|high.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`

	// SkillsDirs are extra directories to scan for skills (each is treated
	// as a directory whose subdirectories contain SKILL.md files).
	SkillsDirs []string `json:"skills_dirs,omitempty"`

	// MCPConfigFiles are paths to .mcp.json files (--mcp-config flag).
	MCPConfigFiles []string `json:"mcp_config_files,omitempty"`

	// MCPInline are inline MCP server specs (--mcp flag, format: name:command args...).
	MCPInline []string `json:"mcp_inline,omitempty"`

	// PluginDirs are directories to scan for plugins (each contains a plugin.yaml manifest).
	PluginDirs []string `json:"plugin_dirs,omitempty"`

	// SessionStartKind controls the matcher target for plugin SessionStart hooks.
	// Empty means startup for new sessions. Restore paths force resume.
	SessionStartKind plugin.SessionStartKind `json:"-"`

	// SystemPromptFile replaces the built-in base instruction prelude for the
	// top-level session only, while keeping the dynamic sections (tools,
	// environment, role, project docs, etc.) generated by the template system.
	SystemPromptFile string `json:"system_prompt_file,omitempty"`

	// SystemPromptAppend are file paths whose contents are appended to the system prompt.
	// Always applied, even when SystemPromptFile is set (CLI --system-prompt-append flag).
	SystemPromptAppend []string `json:"system_prompt_append,omitempty"`

	// NoProjectPrompts suppresses loading .evener/prompts/ from the project directory.
	// Useful for A/B testing to match Docker container behavior (no project prompts).
	NoProjectPrompts bool `json:"no_project_prompts,omitempty"`

	// AgentsDocPath is the personal AGENTS.md loaded ahead of the project's own
	// instruction docs. Empty resolves <userdirs.DefaultConfigRoot()>/AGENTS.md
	// from the process environment; a hub passes its own concrete path so
	// Settings and the sessions it spawns agree on the file even when a launch
	// overrides XDG_CONFIG_HOME.
	AgentsDocPath string `json:"agents_doc_path,omitempty"`

	// NonInteractive indicates no human is available for questions or confirmation.
	// The task prompt is the complete specification; the agent must make all decisions
	// autonomously. Appends guidance to the system prompt adapting skill behavior.
	NonInteractive bool `json:"non_interactive,omitempty"`

	// TurnEndsProcess indicates the process exits when the current turn's work is
	// drained, as in a one-shot `evener run`: there is no later turn in which a
	// background job could report, so ending the turn kills it. Distinct from
	// NonInteractive, which asks whether a human can be questioned — a long-lived
	// serve session is frequently non-interactive and must never set this.
	//
	// It is persisted so a CHILD session inherits it: children are built from the
	// parent's own toSnapshot, and a delegate of a one-shot run dies with the same
	// process. RESTORE is the other direction — see
	// RestoreSessionConfig.TurnEndsProcess, where the restoring process's answer
	// replaces the stored one, because the same session id resumed by `evener run`
	// dies with its turn and resumed by the daemon does not.
	TurnEndsProcess bool `json:"turn_ends_process,omitempty"`

	// ContextStrategy selects the context management strategy: compact|session-log|ooda.
	// The value "recall" is accepted as a compatibility alias for compact.
	ContextStrategy string `json:"context_strategy,omitempty"`

	// ShareTasksWithChildren, when true, passes the parent's task store to
	// child sessions created for delegate jobs. Both parent and children see
	// the same task list, enabling cross-session task coordination.
	ShareTasksWithChildren bool `json:"share_tasks_with_children,omitempty"`

	// ResultToolName overrides the name of the result tool.
	// When set, all internal references use this name instead of "communicate".
	// Used for A/B testing tool names. Empty means "communicate".
	ResultToolName string `json:"result_tool_name,omitempty"`

	// EnableLoopDetection toggles repeated-tool-call loop detection, which
	// nudges the model with an escalating warning when it repeats the same tool
	// signatures. A nil pointer defaults to enabled; set to a pointer to false
	// to disable.
	EnableLoopDetection *bool `json:"enable_loop_detection,omitempty"`

	// LoopDetectionWindow is the number of recent tool-call signatures examined
	// for a repeating pattern. Zero defaults to 10.
	LoopDetectionWindow int `json:"loop_detection_window,omitempty"`

	// LLMRetryPolicy controls retries for retryable Unified LLM errors (429, 5xx, etc).
	// Nil means use llm.DefaultRetryPolicy().
	//
	// LLMRetryPolicy and LLMSleep are not set by app callers in production; they
	// exist as test-injection points (including from the external agent_test
	// package, which constructs SessionConfig literals through NewSession and so
	// cannot reach an unexported field). They are json:"-" and carry no
	// serialization cost, so they stay on the public struct.
	LLMRetryPolicy *llm.RetryPolicy `json:"-"`
	// LLMSleep is the sleep function used between LLM retries; nil uses the
	// default time.Sleep. A test-injection point (see LLMRetryPolicy above).
	LLMSleep llm.SleepFunc `json:"-"`

	// clock is the session's sole source of time: every time.Now read, sleep,
	// timer, ticker, and watchdog in the turn / job / goal lifecycle routes
	// through it. Nil defaults to clock.Real() (the standard library). It is
	// unexported because the only injector is the package-internal fuzz harness
	// (a deterministically-advanceable fake); production always uses Real(). It
	// is never persisted.
	clock clock.Clock

	// ModelFallbacks is a literal-order chain of "provider/model" identifiers
	// to try when the primary model returns a Permanent-class provider error
	// (403/404/422/etc — see llm.Classify). Empty means no fallback. Kata cxw8.
	ModelFallbacks []string `json:"model_fallbacks,omitempty"`

	// StateDir, when non-empty, enables incremental session persistence.
	// Snapshots are written to <StateDir>/sessions/ and tasks to <StateDir>/tasks/.
	StateDir string `json:"-"`

	// AcquireSessionOwnership reserves a freshly generated session ID for this
	// process before any ID-specific state is persisted. Top-level hosts install
	// the API-log ownership boundary; child and cleared sessions inherit it.
	AcquireSessionOwnership func(sessionID string) error `json:"-"`

	// ExportATIFPath, when non-empty, causes Session.Close to export an ATIF v1.7
	// trajectory JSON file to this path. Only root sessions (spawn.depth==0) export.
	ExportATIFPath string `json:"-"`

	// ExportATIFProviderHandles controls whether ATIF export redacts provider
	// handles or includes raw local diagnostic handles. Empty means redacted.
	ExportATIFProviderHandles string `json:"-"`

	// SystemPromptAsUser, when true, combines the system prompt into the first
	// user message instead of sending it as a separate system/developer message.
	// Workaround for models (e.g. GPT-5.4) that ignore the instructions
	// parameter when given specific task delegations in user messages.
	SystemPromptAsUser bool `json:"system_prompt_as_user,omitempty"`

	// OpenAIResponsesContinuation controls whether OpenAI Responses continuation
	// may be considered. Empty and "off" disable it; "auto" is still gated by
	// endpoint support and continuation eligibility.
	OpenAIResponsesContinuation string `json:"openai_responses_continuation,omitempty"`
	ProviderIdleTimeout         string `json:"provider_idle_timeout,omitempty"`

	// Sandbox is the sandbox mode name (off|read-only|workspace-write|restricted)
	// requested at session start. Empty means off — today's behavior. Carried so a
	// resumed session (M4) re-applies its policy; INERT in M1 (nothing enforces).
	Sandbox string `json:"sandbox,omitempty"`

	// SandboxNet is the network decision (--sandbox-net): nil means the default
	// (on when sandboxed). Only meaningful for a non-off Sandbox. Carried inert in M1.
	SandboxNet *bool `json:"sandbox_net,omitempty"`

	// VisionModel routes the image-description vision side-channel: "" uses the
	// session's active model (the default), "off" disables the side-channel, a
	// bare model resolves on the active provider at call time, and
	// "provider/model" pins a provider instance. Runtime changes go through
	// Session.SetVisionModel, which writes this same field under s.mu.
	VisionModel string `json:"vision_model,omitempty"`

	// ResolveProfile, when non-nil, maps a "provider/model" ref to the
	// corresponding *provider.Profile. Injected by cmd/evener so that
	// Session.SetModel can perform cross-provider switches without
	// importing the provider constructors directly (which would create a
	// cycle). When nil the session falls back to profile.WithModel which
	// only handles same-provider (or strip/keep) refs.
	ResolveProfile func(ref string) (*provider.Profile, error) `json:"-"`

	// spawn holds the fields that only spawnAgent (plus the init-time
	// role-prompt derivation) populates when creating a child session. It is
	// never set by package consumers and never persisted (json:"-"), matching
	// the pre-refactor json:"-" behavior of each individual field.
	spawn spawnConfig `json:"-"`

	// testOnly holds injection points used only by package-internal tests. It is
	// never set by app callers and never persisted (json:"-").
	testOnly testConfig `json:"-"`

	// ForceRealIO opts a session construction back into the real,
	// fsync-bearing I/O paths (jobstore append fsync, transcript header
	// fsync, on-disk installation-ID persistence) that testSpeedIO
	// (session_init.go) otherwise skips by default whenever running under
	// `go test` (testing.Testing() is true for every test binary, including
	// black-box and live/E2E ones outside this package). Package-agent's own
	// tests reach that default through the unexported testOnly.forceRealIO
	// field; this exported twin is the supported escape valve for a test in
	// another package - which cannot reach an unexported field - whose own
	// contract IS that I/O cost or its on-disk durability (e.g. asserting a
	// stable installation ID across a restore, or that a transcript survives
	// an unclean process exit). False in production, where it is inert
	// because testing.Testing() is always false there.
	ForceRealIO bool `json:"-"`
}

// testConfig holds injection points used ONLY by package-internal (package
// agent) tests to make context-strategy selection and compaction thresholds
// deterministic. Never set by app callers; never persisted (json:"-" on the
// parent field).
type testConfig struct {
	// visionSideChannelTimeout supplies an explicit owned deadline only for
	// deterministic package tests. Zero leaves caller deadlines authoritative.
	visionSideChannelTimeout time.Duration
	// afterCommunicateBoundary observes the state transition at a completed
	// communicate boundary. Nil in production.
	afterCommunicateBoundary func(*Session)
	// steeringCarrierClaimed observes the drain ladder claiming a steering
	// carrier turn, before that turn is accepted -- the window a Stop or a
	// write fault can land in. Nil in production.
	steeringCarrierClaimed func(turnID string)
	// steeringCarrierClaiming observes a steering carrier claim about to be
	// written, before the store write -- where a test arms a write fault that
	// refuses exactly that claim. Nil in production.
	steeringCarrierClaiming func()
	// beforeSteeringInjectedPublish observes a delivered steer's live event
	// about to be published, immediately after clearAskPendingForResolvingSteer
	// ran -- the ordering RoboRev #1806 member-3 Medium's fix depends on
	// (the server refreshes its ask facet on this event, so the clear must be
	// visible before it fires). Tests use it to sample askPendingCount() at
	// exactly that point. Nil in production.
	beforeSteeringInjectedPublish func()
	// clientMutationStartClaiming observes a durable start claim about to be
	// written, after ProcessClientMutationStart's cheap poison pre-check and
	// before the claim's own refusal -- the window a poisoning lands in. Nil in
	// production.
	clientMutationStartClaiming func()
	// failTurnBeforeRecording, when set, makes a turn fail before its user entry
	// is recorded. It is the one seam that reaches the pre-incorporation,
	// non-transcript failure the start path's give-back keys on: an integration
	// test cannot produce one without closing the session, which closes the
	// claim path too. Nil in production.
	failTurnBeforeRecording func() error
	// clientMutationStartAnnounced observes a start that has claimed and
	// announced, immediately before the turn runs -- the window a close lands in,
	// where the post-run give-back decides whether the claim goes back. Nil in
	// production.
	clientMutationStartAnnounced func()
	// queueHeadClaimInSerializer observes a queue-head claim entering the
	// mutation-store serializer, after the transcript writer was sampled under
	// s.mu. A lock-order test holds Session.mu and fails if this fires. Nil in
	// production.
	queueHeadClaimInSerializer func()
	// queueHeadClaimSampled observes a queue-head claim that has finished its
	// Session.mu work (the transcript writer sample) and is about to enter the
	// serializer. Nil in production.
	queueHeadClaimSampled func()
	// delegateDeliveryClassified observes whether an incoming waiterless delivery
	// was deferred to the enclosing ProcessInput drain. Nil in production.
	delegateDeliveryClassified func(*Session, bool)
	// sessionInitFault injects deterministic failures at external initialization
	// boundaries. Nil preserves the production implementation.
	sessionInitFault func(point string) error
	// subagentPrepareFault injects deterministic external-boundary failures into
	// prepareSubagentRun. Nil preserves every production boundary.
	subagentPrepareFault func(point string) error
	// subagentAfterPrepare runs after spawnAgent has prepared a child but before
	// it tracks the child, allowing tests to reproduce the close race.
	subagentAfterPrepare func(*Session)
	// delegateInitialInputAppend observes the real stable-create boundary
	// immediately before the child transcript receives its initial user input.
	delegateInitialInputAppend func(*Session)
	// delegateInlineWaitReady observes the exact context and duration supplied to
	// a stable delegate inline wait. Nil preserves the production wait.
	delegateInlineWaitReady func(context.Context, time.Duration)
	// delegateSendBeforePositiveWaitAdmission observes the boundary immediately
	// before a positive-wait send reserves its start. Nil preserves production.
	delegateSendBeforePositiveWaitAdmission func()
	// delegateSendStartCommitted observes the send start hand-off: the send
	// generation is committed and its run goroutine does not exist yet, while
	// the drive claim is held. Tests use it to drive the child at exactly that
	// point and assert the committed start refuses a second turn.
	delegateSendStartCommitted func(*subagent)
	// delegateSendStartClaimed observes the committed send start at the earliest
	// point in the window: immediately after ReserveStart and before CommitStart,
	// when the id-keyed claim has been taken but restoreIdleForSend has NOT yet
	// resolved the child. It receives the child SESSION id the claim is keyed by.
	// Tests use it to drive the child before it is resolved and assert the
	// id-keyed claim refuses a second turn.
	delegateSendStartClaimed func(childSessionID string)
	// delegateDeliveryCommitsTaken observes the tool-result boundary after inline
	// delivery commits leave the pending map and before any transcript write.
	delegateDeliveryCommitsTaken func()
	// delegateAttentionReadFold replaces only resident attention verification
	// reads. Nil preserves the production transcript fold.
	delegateAttentionReadFold func(string, string) (delegateAttentionFold, error)
	// delegateAttentionFoldEntries replaces only the in-memory attention fold
	// over restore-retained entries. Nil preserves the production fold.
	delegateAttentionFoldEntries func([]transcript.Entry) (delegateAttentionFold, error)
	// delegateAttentionOpenWriter replaces only transcript resume for attention
	// repair. Nil preserves the production transcript opener.
	delegateAttentionOpenWriter delegateAttentionWriterOpener
	// delegateRuntimeReclaimClose replaces only the external Session close
	// boundary used by admission-triggered stable-runtime reclamation.
	delegateRuntimeReclaimClose func(*Session)
	// delegateRestoreStat and delegateRestoreReadFile replace only restore-input
	// filesystem reads for this session. Nil preserves the production paths.
	delegateRestoreStat     func(string) (os.FileInfo, error)
	delegateRestoreReadFile func(string) ([]byte, error)
	// subagentReserveSlot replaces only the retained-terminal reservation boundary.
	subagentReserveSlot func(*Session) ([]*subagent, error)
	// subagentReserveTreeSlot replaces only the tree-capacity reservation boundary.
	subagentReserveTreeSlot func(*Session) (*treeReservation, bool)
	// subagentStopGated overrides child stop-gating when handled is true.
	subagentStopGated func(*Session, string) (stopped, handled bool)
	// subagentRunIteration observes each production subagent input iteration.
	// Tests use it only as a deterministic barrier around continuation decisions.
	subagentRunIteration func(*subagent, int)
	// subagentBeforeSettlement observes the final unlocked boundary before a
	// stable generation enters controller settlement.
	subagentBeforeSettlement func(*subagent)
	// delegateAttentionStartCommitted observes the start hand-off: the attention
	// generation is committed and its run goroutine does not exist yet. Tests use
	// it to drive the child from a second goroutine at exactly that point.
	delegateAttentionStartCommitted func(*subagent)
	// subagentAfterFinalStatePublish observes the interval after a retained child
	// publishes terminal state and before it restores its parent notify callback.
	subagentAfterFinalStatePublish func(*subagent)

	// registerTool injects deterministic registration failures. Nil preserves
	// direct Registry.Register calls.
	registerTool func(*tool.Registry, tool.RegisteredTool) error

	// execToolCheckpoint observes deterministic dispatch boundaries. Nil is a
	// no-op; fuzz tests use it to transition the session to closing without races.
	execToolCheckpoint func(string)

	// appendCompactionTurn injects transcript append failures. Nil preserves the
	// session transcript writer.
	appendCompactionTurn func(schema.Turn) error

	// beforeHistoryRepairPublish observes the boundary immediately before an
	// orphaned-tool-result repair publishes to s.history. Tests use it only to
	// place deterministic concurrent history mutations in that window. Nil in
	// production.
	beforeHistoryRepairPublish func()

	// beforeFoldSideEffectsFlush observes the boundary between a winning
	// fold's publication (history swap, baseline correction, note claim,
	// transcript commit) and the deferred flush of its remaining side effects
	// (events, session naming, hook user messages). Tests use it only to
	// place deterministic concurrent folds in that window. Nil in production.
	beforeFoldSideEffectsFlush func()

	// beforeFoldTranscriptCommit observes the boundary inside a winning
	// fold's publication after the history swap (and its baseline/note
	// bookkeeping) and immediately before the fold's transcript entries are
	// committed. Tests use it only to place deterministic concurrent turn
	// recordings in that window. Nil in production.
	beforeFoldTranscriptCommit func()

	// beforeRestoredFailureBoundaryDoorRelease observes the restored failure
	// boundary after state publication and before attentionMu is released.
	// Tests use it to prove that a concurrent transcript writer cannot pass the
	// publication door between those operations. Nil in production.
	beforeRestoredFailureBoundaryDoorRelease func()

	// beforeEnvironmentEventPublish observes an environment append at the
	// moment it is about to publish its live event, so a test can state where
	// that publication sits relative to the transcript ordering boundary.
	// Nil in production.
	beforeEnvironmentEventPublish func()

	// afterFoldSupersessionCheck observes a fold flush immediately after it
	// has evaluated whether a newer publication supersedes it and before it
	// runs its last-write-wins side effects. Tests use it only to place a
	// deterministic newer publication in that window. Nil in production.
	afterFoldSupersessionCheck func()

	// worktreeGitRunner replaces only the Git subprocess boundary used by the
	// native worktree lifecycle. Package-agent tests use it to replay the real
	// Session lifecycle against a scripted Git model without launching a host
	// process. Nil preserves the production runner.
	worktreeGitRunner func(context.Context, execenv.ExecutionEnvironment) worktree.GitRunner

	// environmentInfo replaces the host-derived environment snapshot for tests
	// that need a LocalExecutionEnvironment's real filesystem semantics but
	// must not invoke its OS-version subprocess. Nil preserves the production
	// snapshot path.
	environmentInfo func(execenv.ExecutionEnvironment, clock.Clock) schema.EnvironmentInfo

	// contextStrategyOverride, when non-nil, is used instead of creating a
	// strategy from the ContextStrategy string.
	contextStrategyOverride contextmgr.Strategy

	// compactionThresholdScale multiplies all compaction thresholds by this
	// factor. 1.0 = defaults, 0.1 = trigger at 10% of normal pressure. 0 means
	// use defaults.
	compactionThresholdScale float64

	// responsesContinuationSupportRegistry enables deterministic continuation
	// slices without changing production endpoint-family defaults.
	responsesContinuationSupportRegistry map[llm.ResponsesEndpointFamily]llm.ResponsesContinuationSupport

	// responsesContinuationShadowEstimateFunc makes shadow-estimate failure
	// deterministic in package-agent tests.
	responsesContinuationShadowEstimateFunc func(llm.Request) (int, bool)

	// modelCallContextWindowFunc and responsesContinuationHistoryCurrentFunc
	// expose otherwise unreachable model-call decisions to deterministic tests.
	modelCallContextWindowFunc              func(*provider.Profile) int
	responsesContinuationHistoryCurrentFunc func(responsesContinuationHistoryReservation, []schema.Turn) bool

	// childClientFactory, when non-nil, supplies the llm.Client a spawned child
	// (subagent/delegate) session uses instead of reusing the parent's. The fuzz
	// lifecycle harness uses it to give each child its OWN scripted adapter — and
	// thus its own deterministic, pre-recorded response script — so a child's
	// concurrent turn never races the parent's Responder draw sequence (the exact
	// hazard the offline-harness design flags). It is inherited by the child's own
	// config (subCfg := s.cfg), so a grandchild would likewise get a fresh client.
	childClientFactory func() *llm.Client

	// disableDelegateIdleRelease suppresses the non-terminal idle release of a
	// finalized stable delegate's runtime subtree (see
	// releaseIdleRuntimeAfterFinalize). Production always releases, so an idle
	// delegate's process-local resources — stdio MCP server processes above
	// all — do not outlive its generation for the life of the daemon. The
	// warm-supervision and retirement-admission fixtures set this because their
	// subject is the warm-resume machinery itself — a deliberately retained
	// runtime under an explicit in-process mode — not the retention policy the
	// default exercises. It is inherited by child configs, so setting it on a
	// fixture's root session covers its whole delegate tree.
	disableDelegateIdleRelease bool

	// delegateIdleReleaseDelay overrides the production idle-release grace
	// (delegateIdleReleaseDelayDefault) for tests: the idle-release contract
	// test shrinks it to 100ms so the scheduled release fires within its poll
	// window. Nil keeps the production default. Inherited by child configs
	// like every testOnly field.
	delegateIdleReleaseDelay *time.Duration

	// idleTeardownConcurrency overrides the idle-release member teardown's
	// concurrency bound (delegateTeardownConcurrencyDefault) for tests. Nil
	// keeps the production default. Inherited by child configs like every
	// testOnly field.
	idleTeardownConcurrency *int

	// idleTeardownMemberStarted and idleTeardownMemberSettled observe one
	// idle-release member teardown's boundaries, fired with the member
	// session immediately before its teardown body runs and immediately
	// after it returns. Nil in production. Inherited by child configs like
	// every testOnly field.
	idleTeardownMemberStarted func(*Session)
	idleTeardownMemberSettled func(*Session)

	// afterDelegateAttentionRestore observes the wake pass's one vulnerable
	// window: after a cold attention restoration has installed the runtime and
	// before the attention reservation commits, on the root session's own
	// goroutine. Nil in production.
	afterDelegateAttentionRestore func(delegateID string, restored *subagent)

	// namerClient, when non-nil, is the llm.Client the background session namer
	// uses instead of the session's own. The namer runs on a detached goroutine,
	// so routing it through a separate scripted client keeps its draw off the
	// shared Responder — the same race childClientFactory avoids for children —
	// letting the fuzz lifecycle harness exercise the namer goroutine
	// deterministically (launch, decode, state mutation, join) alongside the other
	// ops. Nil in production: the namer uses s.client unchanged.
	namerClient *llm.Client

	// forceSessionNamer, when true, launches the background namer even with an
	// empty StateDir, so the fuzz lifecycle harness can exercise the namer
	// goroutine WITHOUT enabling StateDir (which would autosave a meta file on
	// nearly every op — disk churn the search cannot afford). With StateDir empty,
	// the namer's own persistence (maybeAutoSave, appendSessionNamerLog) no-ops,
	// so only the in-memory goroutine + decode + naming-state mutation run. False
	// in production: the StateDir gate is unchanged.
	forceSessionNamer bool

	// skipGitSnapshot suppresses launch-time git metadata collection for tests
	// whose contract is below the session prompt/environment snapshot layer.
	skipGitSnapshot bool

	// minimalSystemPrompt avoids rendering the large prompt template for tests
	// whose contract is below prompt content. Tool definitions still rebuild.
	minimalSystemPrompt bool

	// minimalWorktreeToolRegistry registers only file tools, the terminal/result
	// tool, and manage_worktree for worktree-focused tests.
	minimalWorktreeToolRegistry bool

	// noSyncJobStore skips jobstore fsyncs for tests whose contract is not crash
	// durability. The event bytes and append/load behavior stay the same.
	noSyncJobStore bool

	// forceRealIO disables the test-binary-wide default (see testSpeedIO in
	// session_init.go) that skips jobstore fsyncs, the transcript header fsync,
	// and on-disk installation-ID persistence whenever running under `go test`.
	// Known setters: BenchmarkNewSession, whose subject IS real
	// session-construction I/O cost (the test-speed default would make it
	// measure something other than what it claims); and
	// TestSession_PopulatesModelRequestMetadata, which asserts the
	// installation_id file exists on the real filesystem.
	forceRealIO bool

	// sandboxProber, when non-nil, supplies the host facts used to RE-RESOLVE a
	// resumed delegate's persisted sandbox policy against its lane. Production
	// leaves it nil and probes the live host (sandbox.RealProber); tests inject a
	// sandbox.FakeProber so the resume path never shells out to bwrap.
	sandboxProber sandbox.Prober
	// fileToolEnforceable replaces the runtime secure-open capability probe for
	// deterministic delegate sandbox tests. Nil probes the live process.
	fileToolEnforceable func() bool

	// envProbes, when non-nil, replaces envctx.DefaultProbes() wholesale for the
	// session's environment-context collector — including the production
	// GitBranch wiring, which is skipped entirely when this is set. Tests use it
	// for a deterministic clock (and nil/no-op pressure probes) instead of the
	// real host clock and git subprocess. Nil in production.
	envProbes *envctx.Probes

	// closeAfterDisposeSweepJoin observes the exact point in Close() immediately
	// AFTER both disposeWG.Wait() and sweepWG.Wait() have returned (step 3 of the
	// close preamble) and BEFORE closeOwnedDelegateRuntimeTree runs. It is the
	// single observation point that proves Close joins in-flight dispose/sweep
	// work: a test holds such work on a controlled gate, and asserts at this seam
	// — from inside the closing goroutine — that the work had already completed
	// before Close reached here. This is the kata 0t1y positive-observation
	// pattern: "Close has not returned yet" is unfalsifiable when Close blocks on
	// a join (a test that only watches Close fail to return stays green with the
	// join deleted, because Close simply proceeds and returns); observing Close
	// at the post-join boundary, with the work demonstrably still in flight,
	// turns a red/green question into a positive fact. Nil in production.
	closeAfterDisposeSweepJoin func()

	// closeAwaitingEnvWork observes Close() at its environment-work join
	// (joinEnvWorkWithinCloseBudget) immediately before it blocks there: the
	// join holds the drained channel of live admitted work, which only the
	// last admission's endEnvWork closes, and the close budget is not yet
	// spent. It is the positive signal a fence test holds admitted work
	// against. A close that is missing the join never calls it and reaches
	// environment cleanup instead; a join with nothing outstanding, or with its
	// budget already spent, does not call it either.
	// TestEnvWorkJoinWaitsAfterSignallingUntilItsBudgetEnds pins that the join
	// really waits once it has called it. Nil in production.
	closeAwaitingEnvWork func()

	// envCleanupObserved observes every environment Close() runs Cleanup on,
	// just before it does, so a test can assert the process-table cleanup ran
	// exactly once and on the environment the session currently holds — never
	// on one it parked (worktreeRestoreEnv), whose scratch is retained without
	// it. Nil in production.
	envCleanupObserved func(execenv.ExecutionEnvironment)

	// swapEnvAfterAdopt observes the point in swapEnvAndRefresh just after the
	// session's scratch moved onto the next environment and before the refresh
	// and install, so a test can begin a close in that window. It receives the
	// context the refresh's git runs under, so a test can also assert that a
	// close cancels that work. Nil in production.
	swapEnvAfterAdopt func(refreshCtx context.Context)

	// scratchUpsertAttempt runs immediately before each manifest upsert in
	// installScratchRetentionFor and registerScratchConsumerRoles, after any
	// lock-contention retry decision, so tests can inject deterministic
	// contention around exact attempts.
	scratchUpsertAttempt func()

	// scratchUpsertAfterLoad runs immediately after each install/register
	// closure reloads the manifest to recompute its rows — the window
	// between that row derivation and the upsert's own update lock, where a
	// concurrent writer's commit must be observable. Nil in production.
	scratchUpsertAfterLoad func()

	// scratchInstallBeforeReset runs in installScratchRetentionFor right
	// before the released-manifest reset — the window where a concurrent
	// terminal release can tombstone the manifest between this install's
	// view of it and the reset's own locked read. Nil in production.
	scratchInstallBeforeReset func()

	// scratchAdoptionBeforeClaim runs at the top of adoptConsumerScratch,
	// before the pool load — the window where a terminal detach can sweep the
	// pool after a dispose-then-adopt replacement's slot read approved the
	// swap and its disposal already discarded the fresh mint. Nil in
	// production.
	scratchAdoptionBeforeClaim func()

	// scratchAdoptionBeforeTransfer runs at the top of adoptRetainedScratchFor,
	// before its own pool load — the second window where a terminal detach can
	// sweep the pool after adoptConsumerScratch already read the consumer row
	// and approved the transfer. Nil in production.
	scratchAdoptionBeforeTransfer func()

	// scratchBeforeUnsandboxedTail runs in adoptResumedRootScratch after the
	// sandbox section and right before the unsandboxed tail's slot read —
	// the window where a concurrent claim's refusal can record the slot's
	// contention between the adoption pass and the tail's own lookup. Nil in
	// production.
	scratchBeforeUnsandboxedTail func()

	// scratchAdoptionAfterClaim runs immediately after adoptRetainedScratchFor
	// claims a pooled handle and before the environment restore installs it —
	// the window where a terminal detach must not release the claimed lease.
	// Nil in production.
	scratchAdoptionAfterClaim func()

	// scratchClaimResolved runs immediately after a pool claim resolves and
	// before the adoption switch classifies it — the window where a concurrent
	// refresh fold must not flip the contention mark between the claim's
	// snapshot and a second lookup. Nil in production.
	scratchClaimResolved func()

	// scratchRestoreAfterAdoption runs in the committed-delegate restore right
	// after the retained-scratch adoption installs (or declines) on the
	// child's environment and before the construction continues — the window
	// where a later construction step can leave further scratch on that
	// environment ahead of a failure. It receives the environment so a test
	// can provision exactly that. Nil in production.
	scratchRestoreAfterAdoption func(env *execenv.LocalExecutionEnvironment)

	// scratchAdoptionBeforeBorrow runs in adoptRetainedScratchFor's
	// wrapper-only branch after the binding snapshot and before the borrow's
	// revalidation — the window where this session's own terminal release
	// can seal, detach, and tombstone the allocation the snapshot approved.
	// Nil in production.
	scratchAdoptionBeforeBorrow func()

	// scratchBorrowAfterRetainedCheck runs inside
	// borrowRetainedScratchIfLive after the disk revalidation reads the
	// directory retained and before the install — the window where a
	// durable reclamation actor (a terminal release, the manifest reset, or
	// the sweeper) can invalidate what the check approved. Nil in production.
	scratchBorrowAfterRetainedCheck func()

	// scratchSwapBeforeUpdate runs inside stageScratchSwapBinding immediately
	// before each UpdateScratchBindings attempt, so a test can make the first
	// attempt stale and exercise the rebase-and-retry loop. Nil in production.
	scratchSwapBeforeUpdate func()

	// scratchRefreshOpenOverride is consulted before each refresh pass's
	// reacquire: a non-nil error replaces the real open for that reference,
	// letting a test deterministically fail the Nth reacquire. Nil in
	// production.
	scratchRefreshOpenOverride func(ref sandbox.ScratchReference, call int) error

	// scratchRefreshBeforeInstall runs after a refresh pass reacquired its
	// handles and before it installs them — the window where a concurrent
	// manifest update or pool publish must be observable, with the refresh's
	// session id. Nil in production.
	scratchRefreshBeforeInstall func(sessionID string)

	// scratchRefreshAfterRecheck runs inside the refresh's install hold,
	// after the revision recheck passes and before the rows land — the exact
	// window a manifest update must not be able to commit inside, with the
	// refresh's session id. Nil in production.
	scratchRefreshAfterRecheck func(sessionID string)

	// scratchTerminalReleaseAfterDetach runs inside the terminal scratch
	// release after the retained pool is detached and before the Released
	// tombstone is written — the exact window an in-flight refresh's seed
	// publish can land in, because the detach takes no manifest lock the
	// refresh's install hold would serialize on. Nil in production.
	scratchTerminalReleaseAfterDetach func()
	// scratchRetirementAfterDetach runs inside releaseRetirementScratch right
	// after the retained pool detaches, the window a racing refresh's seed
	// CAS can land in. It is nil in production and test-only.
	scratchRetirementAfterDetach func()

	// scratchTerminalReleaseAttempt observes each terminal-release attempt
	// at 1, in the same position as the environment pin probe: inside the
	// bounded retry, before the tombstone transaction. Nil in production.
	scratchTerminalReleaseAttempt func(attempt int)

	// scratchRefreshAfterSeedCAS runs inside the refresh's install hold
	// immediately after a pass's seed pool won its publish CAS and before the
	// terminal-seal check — the exact window a terminal detach can sweep the
	// freshly published pool in. Nil in production.
	scratchRefreshAfterSeedCAS func()

	// scratchDetachRetainHook runs before each lease release in the terminal
	// pool sweep, so a test can hold the sweep mid-loop — the exact window a
	// losing seed pass must not release the same handles in. Nil in
	// production.
	scratchDetachRetainHook func()

	// scratchLockBackoff replaces the wall-clock sleep that spaces
	// lock-contention retries in the agent layer (the refresh's re-derive
	// loop and the swap's rebase loop), so tests can sequence deterministically
	// against the schedule. Nil in production.
	scratchLockBackoff func(attempt int)

	// enterWorktreeAfterSwap observes the point in enterWorktree right after
	// the environment swap returned — the earliest point outside the swap a
	// close can land — so a test can run one there against a session whose
	// installed and parked environments are both already recorded. It is NOT
	// a seam between the install and the record: those share one s.mu hold,
	// and a seam between them would have to release it. Nil in production.
	enterWorktreeAfterSwap func()

	// metaFS, when non-nil, replaces the real OS filesystem for every
	// session-meta read/write the Session performs directly (maybeAutoSave's
	// schema.SaveSessionMeta, and the ownership-reload schema.LoadSessionMeta
	// in RestoreSessionFromMetaWithConfig), routing them through the
	// schema.*WithFS variants instead. Nil preserves today's behavior (the
	// package-level OS filesystem schema.SaveSessionMeta/LoadSessionMeta
	// already use). Tests inject afero.NewMemMapFs() to avoid real fsync-bearing
	// meta-file IO; nil in production.
	metaFS afero.Fs

	// notesAutoSaveFault injects a deterministic metadata-persistence failure
	// into the shared-notes mutation paths. Nil preserves the production
	// save; a non-nil return is surfaced as the mutation error and blocks
	// the success journal. Tests use it to prove durability gating.
	notesAutoSaveFault func() error

	// contentWindowClock, when non-nil, is the clock consumeModelStream reads
	// to measure an attempt's content-event window (attemptObservation.
	// ContentWindow). The cap early-stop rule keys on a window of 60 seconds or
	// more, so reproducing a cap-shaped round against the real clock would cost
	// a test a real minute per attempt; a stepped clock reproduces one in
	// milliseconds. It moves nothing else — attempt durations, retry backoff,
	// and the stall classification still read the wall clock. Nil in
	// production, where the window is measured against time.Now.
	//
	// It is a seam of its own rather than the session's clock.Clock because the
	// window measures a provider stream, not session lifecycle time; the two
	// are controlled independently on purpose. The cap-shape e2e case steps
	// this clock 45 virtual seconds per content event, building a 90-second
	// window while the session stays on the real clock — so a cap-shaped round
	// reproduces without also warping turn deadlines, retry backoff, and every
	// watchdog in the job lifecycle.
	//
	// Independence runs the other way too, and that direction is why routing the
	// window through clock.Clock would be a bug rather than a simplification.
	// The fuzz harnesses inject agenttest.FakeClock as the session clock and
	// jump virtual time in large steps at unrelated ops — the delegate sequence
	// fuzzer draws advances of up to five virtual minutes — so any attempt
	// straddling one would read as a 60-second-plus content window and be
	// classified cap-shaped by a clock op that has nothing to do with the
	// stream.
	contentWindowClock func() time.Time
}

// spawnConfig holds the SessionConfig fields that only spawnAgent (plus the
// init-time role-prompt derivation in applyAgentRolePromptOverride) populates
// when creating a child session. They are never set by package consumers and
// never persisted: the parent field is json:"-", so the whole struct drops on
// marshal and is the zero spawnConfig on unmarshal. Restored sessions
// reconstruct parent linkage from the transcript header, NOT from this struct;
// do NOT add json tags or repopulate these on restore, or restored subagents
// would gain a non-zero depth and break ATIF root-export gating and the
// subagent-management-is-top-level guards.
type spawnConfig struct {
	// sessionID is the controller-reserved child session identity. Empty makes a
	// non-delegate session mint its own identity.
	sessionID string

	// delegateController is the single root-owned authority inherited by every
	// child session in the live tree.
	delegateController *delegateTreeController
	// retirementController is inherited before initialization can launch work.
	retirementController *RetirementController

	// delegateRootSessionID identifies the root session that owns the inherited
	// controller. It is stable across every child construction in the tree.
	delegateRootSessionID string

	// owningDelegateID is the immutable stable delegate identity that owns this
	// child session. It is empty on the root session.
	owningDelegateID string

	// subscriberCount preserves the root daemon's live observer probe for child
	// escalation decisions.
	subscriberCount func() int

	// parentSessionID links sub-agent sessions to their parent (set by spawnAgent).
	parentSessionID string

	// parentToolCallID is the tool call ID that spawned this sub-agent session.
	parentToolCallID string

	// parentItemID is the provider/tool item ID that spawned this sub-agent session.
	parentItemID string

	// parentJobActivity reports parent-observable child progress for the stable
	// delegate that owns this session.
	parentJobActivity func(delegateID, phase string)

	// descendantEvent reports every event emitted by this session to the root
	// daemon. It is inherited unchanged by descendants, so one callback observes
	// the whole in-process tree without consuming any child's event channel.
	descendantEvent func(events.SessionEvent)

	// parentDelegateID is the durable delegate handle that owns this child
	// session in its parent.
	parentDelegateID string

	// forwardJobEvent lets child job managers send nested job events to the
	// parent manager. The forwarding behavior is installed by later phases.
	forwardJobEvent func(jobstore.Event) error

	// parentSteer routes runtime alias messages from a live sub-agent to its
	// caller, carrying the message's causal watch provenance so the caller's
	// injection is attributable to the watch delivery that produced it, and the
	// events.SteeringKind* naming what was sent so the caller's transcript
	// labels it from ground truth.
	parentSteer func(string, *provenance.Causal, string) error

	// parentSystemNotification routes a child-owned restart notice up the live
	// session tree to the callback receiver.
	parentSystemNotification func(receiverSessionID, message string) bool

	// parentWatchGranted allows this child to watch its immediate parent through
	// the stable controller. It is non-transitive and does not grant delegate.
	parentWatchGranted bool

	// subagentTask is the task description passed to delegate.
	subagentTask string

	// inheritedContext seeds a delegate's transcript once, at construction.
	// NewSession consumes it; descendants start clean unless they also opt in.
	inheritedContext []transcript.Entry

	// depth is the sub-agent nesting depth (0 for root sessions).
	depth int

	// delegationAllowance is the number of additional sub-agent delegation
	// levels this session is permitted to grant. The delegate restore
	// descriptor (DelegateRestoreDescriptor.DelegationAllowance) carries it
	// across a delegate resume; never populated by json unmarshal (json:"-"
	// on the parent struct, like its siblings).
	delegationAllowance int

	// driveCounter is the tree-wide drive-down notification-turn counter,
	// minted and inherited exactly like treeCounter but budgeted separately
	// (defaultMaxConcurrentDriveTurns) so drives can never starve spawns.
	driveCounter *treeCounter

	// treeCounter is the tree-wide running delegate-turn counter. Created once
	// by the root session (when parentSessionID == "") and inherited by all
	// child sessions via spawnConfig. reserve/release are wired into the
	// spawn/resume/drive paths (reserveTreeSlot) and the finalize/abandon paths.
	treeCounter *treeCounter

	// jobActivityClock is inherited explicitly by descendants and orders only
	// shell-job activity projections across the live tree.
	jobActivityClock *jobActivityClock

	// sharedTaskStore, when non-nil, is used instead of creating a per-session
	// task store. Set by spawnAgent when ShareTasksWithChildren is true.
	sharedTaskStore *task.TaskStore
	// sharedTaskStoreOwnerSessionID identifies the session whose durable task
	// file backs sharedTaskStore. Descendants propagate it with the exact pointer.
	sharedTaskStoreOwnerSessionID string

	// rolePromptOverride and the three fields below carry internal prompt and
	// session shaping for restricted subagents and reviewer runs.
	rolePromptOverride   string
	activatedSkillBodies []string
	// frozenSkillMetadata is the typed provenance of this delegate's role
	// preloads, seeded into the skill lifecycle inventory once the permanent
	// prompt is admitted. It never grants ordinary invocation authorization.
	frozenSkillMetadata []schema.FrozenSkillPreload
	allowedToolNames    []string
	deniedToolNames     []string
	// toolNameCeiling is the durable stable-delegate capability ceiling carried
	// into construction. NewSession applies it after all intrinsic tools and
	// ordinary spawn policy so the model-facing cache cannot exceed the ceiling.
	toolNameCeiling         []string
	communicateOutputSchema map[string]any

	// isolation is "worktree" for a delegate spawned with
	// delegate(isolation:"worktree") (native worktree tools spec §9); empty
	// otherwise. session_init.go reads it to unconditionally deny
	// manage_worktree after (and regardless of) the base tool policy,
	// including all-tools agent types — the one piece of §9 step 2's deny
	// that allowedToolNames/deniedToolNames cannot express on their own.
	isolation string
}

func (c *SessionConfig) applyDefaults() {
	if strings.TrimSpace(c.ProviderIdleTimeout) == "" {
		c.ProviderIdleTimeout = "10m"
	}
	// MaxToolRoundsPerInput: zero or negative means unlimited. The previous
	// default of 200 killed long-running agentic sessions doing real work from
	// a single prompt. Loop detection (enabled by default) still guards against
	// runaway repeated tool calls; an explicit --max-rounds N cap is still
	// honored when set.
	if c.MaxToolRoundsPerInput == 0 {
		c.MaxToolRoundsPerInput = -1
	}
	if c.DefaultCommandTimeoutMS <= 0 {
		c.DefaultCommandTimeoutMS = 10_000
	}
	if c.MaxCommandTimeoutMS <= 0 {
		c.MaxCommandTimeoutMS = 600_000
	}
	if c.MaxSubagentDepth <= 0 {
		// Default 2: a root session's delegation allowance derives from this, so 2
		// lets a delegate itself delegate one level (grant allowance 1) by default.
		c.MaxSubagentDepth = 2
	}
	if c.MaxConcurrentDelegateTurns <= 0 {
		c.MaxConcurrentDelegateTurns = defaultMaxConcurrentDelegateTurns
	}
	if c.MaxRetainedTerminal <= 0 {
		c.MaxRetainedTerminal = defaultMaxRetainedTerminal
	}
	if c.EnableLoopDetection == nil {
		v := true
		c.EnableLoopDetection = &v
	}
	if c.LoopDetectionWindow <= 0 {
		c.LoopDetectionWindow = 10
	}
	if c.clock == nil {
		c.clock = clock.Real()
	}
}

// toSnapshot projects the persisted wire fields of a SessionConfig into a
// schema.ConfigSnapshot, dropping the engine-only json:"-" fields that are never
// serialized. The field set mirrors schema.ConfigSnapshot exactly; the converter
// round-trip test guards against any field being dropped or misrouted.
func (c SessionConfig) toSnapshot() schema.ConfigSnapshot {
	return schema.ConfigSnapshot{
		MaxToolRoundsPerInput:       c.MaxToolRoundsPerInput,
		MaxTurns:                    c.MaxTurns,
		DefaultCommandTimeoutMS:     c.DefaultCommandTimeoutMS,
		MaxCommandTimeoutMS:         c.MaxCommandTimeoutMS,
		MaxSubagentDepth:            c.MaxSubagentDepth,
		MaxConcurrentDelegateTurns:  c.MaxConcurrentDelegateTurns,
		MaxRetainedTerminal:         c.MaxRetainedTerminal,
		ToolOutputLimits:            c.ToolOutputLimits,
		UserInstructionOverride:     c.UserInstructionOverride,
		AgentName:                   c.AgentName,
		ReasoningEffort:             c.ReasoningEffort,
		SkillsDirs:                  c.SkillsDirs,
		MCPConfigFiles:              c.MCPConfigFiles,
		MCPInline:                   c.MCPInline,
		PluginDirs:                  c.PluginDirs,
		SystemPromptFile:            c.SystemPromptFile,
		SystemPromptAppend:          c.SystemPromptAppend,
		NoProjectPrompts:            c.NoProjectPrompts,
		AgentsDocPath:               c.AgentsDocPath,
		NonInteractive:              c.NonInteractive,
		TurnEndsProcess:             c.TurnEndsProcess,
		ContextStrategy:             c.ContextStrategy,
		ShareTasksWithChildren:      c.ShareTasksWithChildren,
		ResultToolName:              c.ResultToolName,
		EnableLoopDetection:         c.EnableLoopDetection,
		LoopDetectionWindow:         c.LoopDetectionWindow,
		ModelFallbacks:              c.ModelFallbacks,
		SystemPromptAsUser:          c.SystemPromptAsUser,
		OpenAIResponsesContinuation: c.OpenAIResponsesContinuation,
		ProviderIdleTimeout:         c.ProviderIdleTimeout,
		Sandbox:                     c.Sandbox,
		SandboxNet:                  c.SandboxNet,
		VisionModel:                 c.VisionModel,
	}
}

// configFromSnapshot rebuilds a SessionConfig from its persisted wire fields.
// Engine-only fields (StateDir, ResolveProfile, retry policy, spawn linkage,
// test hooks) are not persisted and are left zero for the caller to repopulate —
// matching the pre-carve behavior, where those json:"-" fields were always zero
// after loading a meta.json or snapshot from disk.
func configFromSnapshot(s schema.ConfigSnapshot) SessionConfig {
	return SessionConfig{
		MaxToolRoundsPerInput:       s.MaxToolRoundsPerInput,
		MaxTurns:                    s.MaxTurns,
		DefaultCommandTimeoutMS:     s.DefaultCommandTimeoutMS,
		MaxCommandTimeoutMS:         s.MaxCommandTimeoutMS,
		MaxSubagentDepth:            s.MaxSubagentDepth,
		MaxConcurrentDelegateTurns:  s.MaxConcurrentDelegateTurns,
		MaxRetainedTerminal:         s.MaxRetainedTerminal,
		ToolOutputLimits:            s.ToolOutputLimits,
		UserInstructionOverride:     s.UserInstructionOverride,
		AgentName:                   s.AgentName,
		ReasoningEffort:             s.ReasoningEffort,
		SkillsDirs:                  s.SkillsDirs,
		MCPConfigFiles:              s.MCPConfigFiles,
		MCPInline:                   s.MCPInline,
		PluginDirs:                  s.PluginDirs,
		SystemPromptFile:            s.SystemPromptFile,
		SystemPromptAppend:          s.SystemPromptAppend,
		NoProjectPrompts:            s.NoProjectPrompts,
		AgentsDocPath:               s.AgentsDocPath,
		NonInteractive:              s.NonInteractive,
		TurnEndsProcess:             s.TurnEndsProcess,
		ContextStrategy:             s.ContextStrategy,
		ShareTasksWithChildren:      s.ShareTasksWithChildren,
		ResultToolName:              s.ResultToolName,
		EnableLoopDetection:         s.EnableLoopDetection,
		LoopDetectionWindow:         s.LoopDetectionWindow,
		ModelFallbacks:              s.ModelFallbacks,
		SystemPromptAsUser:          s.SystemPromptAsUser,
		OpenAIResponsesContinuation: s.OpenAIResponsesContinuation,
		ProviderIdleTimeout:         s.ProviderIdleTimeout,
		Sandbox:                     s.Sandbox,
		SandboxNet:                  s.SandboxNet,
		VisionModel:                 s.VisionModel,
	}
}

// ParseProviderIdleTimeout parses a positive Go duration. Empty selects ten minutes.
func ParseProviderIdleTimeout(value string) (time.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return 10 * time.Minute, nil
	}
	d, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("provider_idle_timeout must be a positive duration (for example 10m): %q", value)
	}
	return d, nil
}
