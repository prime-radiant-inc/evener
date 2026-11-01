package agent

import (
	"context"
	"os"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/envctx"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/clock"
	"primeradiant.com/evener/agent/internal/contextmgr"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/internal/worktree"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// testConfig holds injection points used ONLY by package-internal (package
// agent) tests to make context-strategy selection and compaction thresholds
// deterministic. Never set by app callers; never persisted (json:"-" on the
// parent field).
type testConfig struct {
	// worktree injects failures at the manage_worktree filesystem and sandbox
	// boundaries (see worktreeTestSeams).
	worktree worktreeTestSeams
	// memoryBeforeIO observes the native scope boundary without providing data.
	memoryBeforeIO func(scope, operation string) error
	// memoryRealBudget gives the memory boundary its production read budget
	// instead of the long default tests get (see memoryBoundaryWait).
	memoryRealBudget bool
	// visionSideChannelTimeout supplies an explicit owned deadline only for
	// deterministic package tests. Zero leaves caller deadlines authoritative.
	visionSideChannelTimeout time.Duration
	// afterCommunicateBoundary observes the state transition at a completed
	// communicate boundary. Nil in production.
	afterCommunicateBoundary func(*Session)
	// beforeModelSwitchPublish observes a committed switch's publication
	// phase at its start - after the profile swap released s.mu, before the
	// marker record and the EventModelChanged emit. It is the window a
	// concurrent at-rest pick can commit and publish in, so it is the only
	// place a test can make that interleaving deterministic. Nil in production.
	beforeModelSwitchPublish func()
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
	// turnGateSampledTranscript observes the turn gate after it sampled the
	// attached transcript writer and before it decided the refusal. The hook
	// runs with no session lock held; tests use it to replace and close that
	// writer. Nil in production.
	turnGateSampledTranscript func(*transcript.Writer)
	// delegateDeliveryClassified observes whether an incoming waiterless delivery
	// was deferred to the enclosing ProcessInput drain. Nil in production.
	delegateDeliveryClassified func(*Session, bool)
	// sessionInitFault injects deterministic failures at external initialization
	// boundaries. Nil preserves the production implementation.
	sessionInitFault func(point string) error
	// subagentPrepareFault injects deterministic external-boundary failures into
	// prepareSubagentRun. Nil preserves every production boundary.
	subagentPrepareFault func(point string) error
	// delegateInitialInputAppend observes the real stable-create boundary
	// immediately before the child transcript receives its initial user input.
	delegateInitialInputAppend func(*Session)
	// delegateInlineWaitReady observes the exact context and duration supplied to
	// a stable delegate inline wait. Nil preserves the production wait.
	delegateInlineWaitReady func(context.Context, time.Duration)
	// delegateQuietWatchTickDone observes one quiet-watch hub tick's completed
	// lease work. It fires after runDelegateQuietWatchdogTick returns, so the
	// tick's durable write is already visible to the observer; coalesced and
	// detached ticks do no work and never fire it. Set it before the clock
	// delivers the first tick. Observers must not block: the call runs on the
	// tick's worker goroutine. Nil preserves production.
	delegateQuietWatchTickDone func(lease delegateLease)
	// delegateSendBeforePositiveWaitAdmission observes the boundary immediately
	// before a positive-wait send reserves its start. Nil preserves production.
	delegateSendBeforePositiveWaitAdmission func()
	// delegateSendStartCommitted observes the send start hand-off: the send
	// generation is committed and its run goroutine does not exist yet, while
	// the drive claim is held. Tests use it to drive the child at exactly that
	// point and assert the committed start refuses a second turn.
	delegateSendStartCommitted func(*subagent)
	// delegateSendChildResolved observes a send once it has resolved (and,
	// cold, restored) its child, just before it takes the child's drive
	// guard. Tests use it to make the child busy at that point. Nil in
	// production.
	delegateSendChildResolved func(*subagent)
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
	// rootAttentionRetryCallback observes a root attention retry callback that
	// has passed scheduleRootAttentionRetryLocked's early returns: it took
	// ownership (beginAttentionCallback succeeded), its generation is still
	// current, and the rail is not parked. It fires whether or not that callback
	// then notifies -- the notification blocker is projected by the ownership
	// lease, not by the notify -- so a test that pauses here holds exactly the
	// live retry, and a stale or parked timer never reaches it. A notify hook
	// cannot identify the caller. Nil in production.
	rootAttentionRetryCallback func()
	// delegateRuntimeReclaimClose replaces only the external Session close
	// boundary used by admission-triggered stable-runtime reclamation.
	delegateRuntimeReclaimClose func(*Session)
	// delegateRestoreStat and delegateRestoreReadFile replace only restore-input
	// filesystem reads for this session. Nil preserves the production paths.
	delegateRestoreStat     func(string) (os.FileInfo, error)
	delegateRestoreReadFile func(string) ([]byte, error)
	// subagentRunIteration observes each production subagent input iteration.
	// Tests use it only as a deterministic barrier around continuation decisions.
	subagentRunIteration func(*subagent, int)
	// subagentBeforeSettlement observes the final unlocked boundary before a
	// stable generation enters controller settlement.
	subagentBeforeSettlement func(*subagent)
	// delegateAttentionBeforeDriveClaim observes the owner's selected attention
	// before it claims the retained child for a start. The transcript read has
	// released attentionMu and no child/controller lock is held, so a competing
	// drive may complete while this observer is paused. Nil in production.
	delegateAttentionBeforeDriveClaim func(*subagent)
	// delegateAttentionStartCommitted observes the start hand-off: the attention
	// generation is committed and its run goroutine does not exist yet. Tests use
	// it to drive the child from a second goroutine at exactly that point.
	delegateAttentionStartCommitted func(*subagent)
	// delegateAttentionBeforeGuardRelease observes an attention drive that
	// launched no run, while it still holds the child's drive guard. Tests
	// pause it there so another drive is refused on that guard. Nil in
	// production.
	delegateAttentionBeforeGuardRelease func(*subagent)
	// afterModelHistorySnapshot runs on every request build, just after the
	// round snapshots its history and before a leased delegate turn takes the
	// controller's own snapshot, so a test can land a turn between the two.
	// Nil in production.
	afterModelHistorySnapshot func()
	// subagentAfterFinalStatePublish observes the interval after a retained child
	// publishes terminal state and before it restores its parent notify callback.
	subagentAfterFinalStatePublish func(*subagent)
	// subagentBeforeGenerationAnnounced observes a stable child's finalize
	// tail just before it announces and releases its finished generation:
	// the child has stopped finalizing, the delegate is idle but not yet
	// released. Nil in production.
	subagentBeforeGenerationAnnounced func(*subagent)
	// subagentBeforeGenerationReleased observes the unlocked boundary after
	// announcement execution, before the exact controller finalizing claim is
	// released. An inline result can be acknowledged while this seam is held.
	// Nil in production.
	subagentBeforeGenerationReleased func(*subagent, delegateLease)
	// delegateFinalizationWaitCeiling overrides how long a send waits for a
	// finished generation's finalize tail to release the delegate
	// (delegateFinalizationWaitCeiling), so a test can exercise a tail that
	// never releases. Nil in production.
	delegateFinalizationWaitCeiling *time.Duration
	// delegateSendAwaitingFinalization observes a send that found the
	// delegate still finalizing, just before it waits for the release. Nil
	// in production.
	delegateSendAwaitingFinalization func()

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

	// needsResponseQuietPeriod overrides needsResponseQuietPeriodDefault for
	// tests; zero rests awaiting at the settle itself. Nil keeps the
	// production default.
	needsResponseQuietPeriod *time.Duration

	// needsResponseRestBeforeAnnounce, when set, runs in the quiet-period
	// timer after the session rests awaiting and before STATUS_SETTLED is
	// emitted, with restMu held, so a test can start a turn in that gap.
	needsResponseRestBeforeAnnounce func()

	// turnStartBeforeRestMu, when set, runs in processOneInput right before
	// the turn start takes restMu, so a test knows the lock is the next thing
	// the turn waits on.
	turnStartBeforeRestMu func()

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

	// delegateAttentionGiveUpAfter overrides how many consecutive counted
	// restore failures the attention drive tolerates before it hands a
	// delegate's attention to the root (maxDelegateAttentionRestoreFailures).
	// Zero keeps the default.
	delegateAttentionGiveUpAfter int

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
