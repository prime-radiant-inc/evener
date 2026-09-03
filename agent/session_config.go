package agent

import (
	"context"
	"os"
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
	// LifetimeContext owns this session tree when supplied by a one-shot run.
	// Nil preserves daemon/background ownership and is not persisted.
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
	// visionSideChannelTimeout overrides the production vision timeout only for
	// deterministic package tests. Zero preserves the production timeout.
	visionSideChannelTimeout time.Duration
	// beforeTerminalCommunicateAccept observes the exact production boundary
	// after Stop hooks accept communicate and before its terminal notification
	// cut is captured. Tests use it only to place deterministic finalize/cut
	// ordering barriers. Nil in production.
	beforeTerminalCommunicateAccept func()
	// afterCommunicateBoundary observes the state transition at a completed
	// communicate boundary. Nil in production.
	afterCommunicateBoundary func(*Session)
	// delegateDeliveryClassified observes whether an incoming waiterless delivery
	// was deferred to the enclosing ProcessInput drain. Nil in production.
	delegateDeliveryClassified func(*Session, bool)
	// terminalCutAfterManagerLock observes captureTerminalNotificationCut after
	// it owns jm.mu and before it reads durable/running/queue state. It permits a
	// concurrent finalizer to prove which side of the cut owns the notification.
	// Nil in production.
	terminalCutAfterManagerLock func()
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

	// metaFS, when non-nil, replaces the real OS filesystem for every
	// session-meta read/write the Session performs directly (maybeAutoSave's
	// schema.SaveSessionMeta, and the ownership-reload schema.LoadSessionMeta
	// in RestoreSessionFromMetaWithConfig), routing them through the
	// schema.*WithFS variants instead. Nil preserves today's behavior (the
	// package-level OS filesystem schema.SaveSessionMeta/LoadSessionMeta
	// already use). Tests inject afero.NewMemMapFs() to avoid real fsync-bearing
	// meta-file IO; nil in production.
	metaFS afero.Fs

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
	parentSteer func(string, *provenance.Causal, string)

	// parentSystemNotification routes a child-owned restart notice up the live
	// session tree to the callback receiver.
	parentSystemNotification func(receiverSessionID, message string) bool

	// parentWatchGranted allows this child to watch its immediate parent through
	// the stable controller. It is non-transitive and does not grant delegate.
	parentWatchGranted bool

	// subagentTask is the task description passed to delegate.
	subagentTask string

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
	allowedToolNames     []string
	deniedToolNames      []string
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
		Sandbox:                     s.Sandbox,
		SandboxNet:                  s.SandboxNet,
		VisionModel:                 s.VisionModel,
	}
}
