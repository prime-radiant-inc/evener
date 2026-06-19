package agent

import (
	"context"
	"errors"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/goal"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/skill"
	taskpkg "primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/llm"
)

// toolDeps is the dependency surface the core tool handler closures need from
// their owning session. The registerXxxTools helpers capture a *toolDeps
// instead of a *Session, which cuts the tools⇄session back-cycle: the handler
// closures no longer reference the concrete *Session type. Every member
// forwards to an existing *Session method or field, preserving all locking and
// ordering; toolDeps adds no behavior of its own.
type toolDeps struct {
	// emit publishes a session event (best-effort, same as Session.emit).
	emit func(kind events.EventKind, data events.EventData)

	// steering queue access for the communicate handler.
	steer           func(msg string)
	drainSteering   func() []steeringMessage
	prependSteering func(entries []steeringMessage)

	// abort returns a non-nil error when the session is closing (= Session.abortIfClosing).
	abort func(ctx context.Context) error

	// resultToolName is the effective name of the communicate tool.
	resultToolName func() string

	// cmdTimeouts is a live getter for the default and max shell command
	// timeouts. It reads cfg on every call so SetTimeout mutations are visible;
	// the values are NOT snapshotted at registration time.
	cmdTimeouts func() (def, maxTimeout int)

	// readGuard exposes the read-before-write guardrail without leaking the raw
	// readFiles map or its mutex.
	readGuard readGuard

	// taskGuard exposes task-store access and the task reminder bookkeeping,
	// all guarded by the session's own mutex.
	taskGuard taskGuard

	// goalGuard exposes goal-store access. The goal store has its own mutex.
	goalGuard goalGuard

	// web exposes the web tools with the profile and client hidden behind them.
	web webDeps

	// compact-tool surface: forward note mutations, force-compaction requests, and current pressure to the handler.
	setPinnedNote       func(note string)
	requestForceCompact func(instructions string) error
	pressure            func() float64

	// setCommunicateResult records the communicate tool's result on the session
	// (fields stay Session-owned; this is the only writer reachable from the handler).
	setCommunicateResult func(awaitReply bool, message, reply, output string)

	// setCommunicateStructured records the raw output object the model emitted,
	// before communicate canonicalization, for delegate structured_result capture.
	setCommunicateStructured func(raw any)

	// skill looks up a discovered skill by name.
	skill func(name string) (skill.SkillMeta, bool)

	// reasoningEffortLevels is captured once for the task_list tool definition.
	reasoningEffortLevels []string

	// webSearchEnabled is the resolved decision (BehaviorTag == "google") for
	// whether the function-tool web_search should be registered.
	webSearchEnabled bool

	// stateDir and sessionID locate the current session's transcript bucket and
	// transcript file. They are the inputs the transcript tools pass to
	// resolveTranscript; an empty stateDir means state persistence is off, in
	// which case the transcript tools are not advertised.
	stateDir  string
	sessionID string

	// currentMeta returns the live SessionMeta of the current session, used as
	// the render metadata when a transcript read resolves to the current session
	// (a non-current session's meta is loaded from its meta.json instead).
	currentMeta func() schema.SessionMeta
}

// readGuard wraps the read-before-write guardrail. It forwards to the
// Session-owned readFiles map + mutex via TrackRead/ReadBeforeWriteWarning so
// the handlers never touch the raw map.
type readGuard struct {
	trackRead              func(path string)
	readBeforeWriteWarning func(path string) string
}

func (g readGuard) TrackRead(path string) { g.trackRead(path) }

func (g readGuard) ReadBeforeWriteWarning(path string) string {
	return g.readBeforeWriteWarning(path)
}

// taskGuard is a thin facade over Session-owned task state. It uses the same
// s.mu as the rest of the session — it does NOT introduce a second mutex.
type taskGuard struct {
	getOrCreateTaskStore func() *taskpkg.TaskStore
	markUsed             func()
	setReasoningEffort   func(effort string)
}

func (g taskGuard) Store() *taskpkg.TaskStore { return g.getOrCreateTaskStore() }

// MarkUsed records that the task_list tool was invoked this round (updates the
// reminder counters under s.mu).
func (g taskGuard) MarkUsed() { g.markUsed() }

func (g taskGuard) SetReasoningEffort(effort string) { g.setReasoningEffort(effort) }

// goalGuard is a thin lazy-accessor facade over the session's goal store.
// The goal store carries its own mutex (unlike taskGuard which uses s.mu).
type goalGuard struct {
	getOrCreateGoalStore func() *goal.Store
}

// Store returns the session's goal store, initializing it if needed.
func (g goalGuard) Store() *goal.Store { return g.getOrCreateGoalStore() }

// webDeps holds the bound web tool functions. The profile and client stay
// hidden inside the closures captured here.
type webDeps struct {
	fetch  func(ctx context.Context, rawURL, question string) (any, error)
	search func(ctx context.Context, query string) (any, error)
}

// newToolDeps builds the tool dependency surface from a session. Every member
// is a forwarder to an existing method or field, so behavior and locking are
// unchanged. Built once in registerCoreTools.
func newToolDeps(s *Session) *toolDeps {
	return &toolDeps{
		emit:            s.emit,
		steer:           s.Steer,
		drainSteering:   s.drainSteeringForTurn,
		prependSteering: s.prependSteering,
		abort:           s.abortIfClosing,
		resultToolName:  s.resultToolName,
		cmdTimeouts: func() (int, int) {
			return s.cfg.DefaultCommandTimeoutMS, s.cfg.MaxCommandTimeoutMS
		},
		readGuard: readGuard{
			trackRead:              s.trackReadFile,
			readBeforeWriteWarning: s.readBeforeWriteWarning,
		},
		taskGuard: taskGuard{
			getOrCreateTaskStore: s.getOrCreateTaskStore,
			markUsed: func() {
				s.mu.Lock()
				s.taskToolEverUsed = true
				s.taskToolLastRound = s.totalRounds
				s.mu.Unlock()
			},
			setReasoningEffort: s.SetReasoningEffort,
		},
		goalGuard: goalGuard{
			getOrCreateGoalStore: s.getOrCreateGoalStore,
		},
		web: webDeps{
			fetch:  s.webFetch,
			search: s.webSearch,
		},
		setPinnedNote:       s.setPinnedNote,
		requestForceCompact: s.requestForceCompact,
		pressure:            s.ContextPressure,
		setCommunicateResult: func(awaitReply bool, message, reply, output string) {
			s.mu.Lock()
			s.comm = communicateResult{
				called:     true,
				awaitReply: awaitReply,
				text:       message,
				reply:      reply,
				output:     output,
			}
			s.mu.Unlock()
		},
		setCommunicateStructured: func(raw any) {
			s.mu.Lock()
			s.comm.structured = raw
			s.mu.Unlock()
		},
		skill: func(name string) (skill.SkillMeta, bool) {
			meta, ok := s.skills[name]
			return meta, ok
		},
		reasoningEffortLevels: s.profile.ReasoningEffortLevels(),
		webSearchEnabled:      s.profile.BehaviorTag() == "google",
		stateDir:              s.stateDir,
		sessionID:             s.id,
		currentMeta:           s.Meta,
	}
}

// newProfileToolRegistry builds a tool registry seeded with the profile's
// canonical tool definitions and placeholder executors; registerCoreTools wires
// the real executors afterward. The registry is keyed by canonical tool names —
// provider-specific renaming is applied only when advertising tools to the
// model (see rebuildToolDefsCache).
func newProfileToolRegistry(p *provider.Profile) *tool.Registry {
	reg := tool.NewRegistry()
	if p == nil {
		return reg
	}
	for _, td := range p.ToolDefinitions() {
		_ = reg.Register(tool.RegisteredTool{
			Tool: llm.Tool{Definition: td},
			Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
				return nil, errors.New("tool executor not wired")
			},
		})
	}
	return reg
}

func registerCoreTools(reg *tool.Registry, s *Session) error {
	deps := newToolDeps(s)
	if err := registerFileTools(reg, deps); err != nil {
		return err
	}
	if err := registerShellTools(reg, s, deps); err != nil {
		return err
	}
	if err := registerJobTools(reg, s, deps); err != nil {
		return err
	}
	registerTaskTools(reg, deps)
	registerGoalTools(reg, deps)
	registerCompactTool(reg, deps)
	registerWebTools(reg, deps)
	registerCommunicateTool(reg, deps)
	registerSkillTool(reg, deps)
	// Transcript tools are advertised only when state persistence is enabled:
	// without a state dir there is no transcript file to read and no bucket to
	// resolve refs against.
	if deps.stateDir != "" {
		for _, rt := range transcriptTools(deps) {
			if err := reg.Register(rt); err != nil {
				return err
			}
		}
	}
	return nil
}
