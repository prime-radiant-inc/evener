package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/installid"
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
	profile        ProviderProfile
	resolveProfile func(ref string) (ProviderProfile, error) // cross-provider resolver; may be nil
	env            ExecutionEnvironment
	stateDir       string
	installID      string

	events       chan events.SessionEvent
	eventsMu     sync.RWMutex // guards send-vs-close on events; all sends go through emit()
	eventsClosed bool         // set under eventsMu.Lock immediately before close(events)
	envInfo      EnvironmentInfo

	// --- Synchronization / lock discipline ---
	//
	// The turn loop (ProcessInput → processOneInput) is the primary owner of
	// most mutable state. External callers — SetModel/SetReasoningEffort,
	// Steer/Enqueue/DrainAsSteer, Snapshot/State/DetailedStatus, and Close —
	// run on other goroutines and race it, so the primitives below guard the
	// shared fields. Several collaborators carry their OWN locks and are NOT
	// covered by mu: contextMgr (contextManager.mu), reg (toolRegistry.mu),
	// subagents (subagentManager.mu / per-child sub.mu), taskStore
	// (TaskStore.mu — note it may be SHARED with child sessions when
	// ShareTasksWithChildren is set), and transcript (TranscriptWriter.mu).
	//
	// mu guards: history, state, closing, turns, modelResponses, totalRounds,
	//   sessionEndEmitted, profile (swapped here; read via currentProfile()),
	//   the mutable cfg knobs (ReasoningEffort, command timeouts,
	//   MaxToolRoundsPerInput), cachedSystemPrompt, cachedToolDefs, the
	//   communicate* transient fields, steeringQueue, followups, inputQueue,
	//   loopDetectionCount, the task* reminder counters, depth, and the name*
	//   fields. It does NOT guard reg — the ToolRegistry self-synchronizes.
	//   (readOnlyStreak is mutated only by the loop and is intentionally
	//   lock-free / loop-private; loopDetectionCount is taken under mu.)
	mu sync.Mutex

	// responseSideEffectsMu serializes a response's user-visible side-effect
	// bundle (emit + appendTurn + counter bump) against teardown.
	// LOCK ORDER: responseSideEffectsMu > mu (Close acquires it before mu).
	responseSideEffectsMu sync.Mutex
	toolEventsWG          sync.WaitGroup // in-flight ToolCallStart/End emit pairs; Close() joins before closing events
	sendersWG             sync.WaitGroup // detached event emitters (subagent runs, session namer); Add happens under mu gated on closing so it happens-before Close()'s join
	state                 SessionState
	closing               bool
	turns                 int // user input count (for MaxTurns enforcement)
	modelResponses        int // LLM round-trip count (for meta.json turn_count)
	history               []Turn

	forkParentID   string
	forkDivergence int
	forkLabel      string

	reg *toolRegistry

	steeringQueue []steeringMessage
	followups     []string

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

	// communicate tool state (transient, reset each processOneInput call)
	communicated          bool
	communicateAwaitReply bool
	communicateText       string
	communicateReply      string
	communicateOutput     string

	// subagents
	depth     int
	subagents *subagentManager

	// context management
	contextMgr *contextManager
	strategy   contextStrategy

	// skills discovered at session startup
	skills            map[string]SkillMeta
	embeddedSkillsDir string // temp dir for extracted embedded skills; cleaned up in Close

	// MCP server connections
	mcpMgr   *mcpManager
	mcpTools []llm.ToolDefinition

	// Plugin-provided components
	plugins             []LoadedPlugin
	pendingPluginEvents []events.PluginLoadedData
	hookRunner          *hookRunner
	pluginAgents        map[string]PluginAgent
	pluginMCPConfigs    []MCPServerConfig

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

	name              string
	nameSource        string
	nameUpdated       time.Time
	nameSet           bool
	namePromptPending bool

	nameSessionFromTextFunc func(context.Context, string, string) error

	// closeOnce ensures Close() body runs exactly once.
	closeOnce sync.Once

	// Session-level cancel: Close() cancels in-flight LLM calls.
	sessionCtx context.Context
	cancelFunc context.CancelFunc

	// task list (lazy-init)
	taskStore     *TaskStore
	taskStoreOnce sync.Once

	// task reminder tracking
	taskToolLastRound int  // totalRounds value at last task_list tool call
	taskToolEverUsed  bool // whether task_list has ever been called
	taskNudgeFired    bool // whether the "consider using task_list" nudge has fired
	totalRounds       int  // cumulative tool rounds across all inputs

	// stuck detection
	loopDetectionCount int // how many times loop detection has fired
	readOnlyStreak     int // consecutive rounds with only read-only tool calls

	// transcript writer (nil when StateDir is empty)
	transcript *TranscriptWriter

	// Cached tool definitions.
	cachedToolDefs []llm.ToolDefinition

	systemPromptOverride string
	cachedSystemPrompt   string
	promptSourceLog      []promptSource
}

// ID returns the session's identifier.
func (s *Session) ID() string { return s.id }

// strategyHost forwarders. Each is a one-line forwarder to existing state or
// methods so context strategies can depend on the strategyHost interface
// instead of the concrete *Session type. Emit and WithResponseSideEffects
// forward to the existing emit/withResponseSideEffects so lock and
// side-effect semantics are unchanged.
var _ strategyHost = (*Session)(nil)

// Emit forwards to the session's internal emit, sending a session event with
// the given payload. The kind argument satisfies the strategyHost interface
// contract; the emitted event's Kind is derived from the payload.
func (s *Session) Emit(kind events.EventKind, data events.EventData) { s.emit(kind, data) }

// WithResponseSideEffects forwards to the session's internal
// withResponseSideEffects, running fn with the response side-effect semantics
// unchanged.
func (s *Session) WithResponseSideEffects(ctx context.Context, fn func()) error {
	return s.withResponseSideEffects(ctx, fn)
}

// StateDir returns the session's configured state directory.
func (s *Session) StateDir() string { return s.stateDir }

// Profile returns the session's current provider profile.
func (s *Session) Profile() ProviderProfile { return s.currentProfile() }

// currentProfile returns the active profile under s.mu so reads never race
// SetModel's swap (s.profile is reassigned under s.mu). Callers that already
// hold s.mu must read s.profile directly instead of calling this.
func (s *Session) currentProfile() ProviderProfile {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.profile
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
	s.cfg.ReasoningEffort = strings.TrimSpace(effort)
	s.mu.Unlock()
	// Flush meta.json so a daemon crash before the next happy-path turn
	// boundary doesn't leave on-disk cfg stale. Kata wnfz. maybeAutoSave
	// re-acquires s.mu via s.Meta(), so the lock must be released first.
	s.maybeAutoSave()
}

// resolveProfileForRef resolves a model ref to a ProviderProfile. When the
// ref is classified as a cross-provider switch (prefixActionSwitch) AND the
// session has a resolver, the resolver is called. Otherwise the current
// profile's WithModel is used (handles same-provider, strip, and keep cases).
func (s *Session) resolveProfileForRef(base ProviderProfile, ref string) (ProviderProfile, bool, error) {
	if parts := strings.SplitN(ref, "/", 2); len(parts) == 2 {
		provider := strings.ToLower(parts[0])
		action := decidePrefixAction(base.BehaviorTag(), base.ID(), provider)
		if action == prefixActionSwitch && s.resolveProfile != nil {
			resolved, err := s.resolveProfile(ref)
			if err != nil {
				return nil, false, err
			}
			if resolved != nil {
				return resolved, true, nil
			}
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
		_ = s.reg.Register(registeredTool{
			Tool: llm.Tool{Definition: defWebSearch()},
			Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
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
		nextProfile = preserveBaseOverrides(nextProfile, s.profile)
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
	s.refreshSystemPromptCache()
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

func (s *Session) applyModelRequestMetadata(profile ProviderProfile, req *llm.Request) {
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
	return s.communicated
}

// CommunicateOutput returns the canonical structured output from the most recent
// communicate call in the current ProcessInput invocation.
func (s *Session) CommunicateOutput() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.communicateOutput
}

// extractOriginalPrompt returns the text of the first user input in the session history.
// If compaction removed it, falls back to the SubagentTask from config.
func (s *Session) extractOriginalPrompt() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.history {
		if t.Kind == TurnUserInput {
			return t.Message.Text()
		}
	}
	return s.cfg.spawn.subagentTask
}

func (s *Session) appendTurn(kind TurnKind, m llm.Message) {
	t := NewTurn(kind, m)
	s.mu.Lock()
	s.history = append(s.history, t)
	s.mu.Unlock()
	if err := s.transcript.Append(t); err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("transcript write failed: %v", err)})
	}
}

// appendAssistantTurn appends an assistant turn that carries the full response
// metadata (usage stats and response ID) alongside the message content.
func (s *Session) appendAssistantTurn(resp llm.Response) {
	t := Turn{
		Kind:       TurnAssistant,
		Message:    resp.Message,
		Timestamp:  time.Now().UTC(),
		Usage:      resp.Usage,
		ResponseID: resp.ID,
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
	if err := SaveSessionMeta(s.stateDir, meta); err != nil {
		s.emit(events.EventWarning, events.WarningData{
			Message: fmt.Sprintf("auto-save failed: %v", err),
		})
	}
}

// applyThresholdScale scales all compaction thresholds on cm by the given
// factor. A factor of 0 or 1 leaves defaults unchanged.
//
// Thresholds are clamped to a minimum of 0.20 so that aggressive scaling
// doesn't collapse all layers into a narrow band.
func applyThresholdScale(cm *contextManager, scale float64) {
	if scale > 0 && scale != 1.0 {
		clamp := func(v float64) float64 {
			if v < 0.20 {
				return 0.20
			}
			return v
		}
		cm.ObservationMaskThreshold = clamp(cm.ObservationMaskThreshold * scale)
		cm.ThinkingClearThreshold = clamp(cm.ThinkingClearThreshold * scale)
		cm.CheckpointThreshold = clamp(cm.CheckpointThreshold * scale)
		cm.SummarizeThreshold = clamp(cm.SummarizeThreshold * scale)
	}
}

// TranscriptPath returns the path to this session's transcript JSONL file,
// or empty string if state persistence is not enabled.
func (s *Session) TranscriptPath() string {
	if s.stateDir == "" {
		return ""
	}
	return filepath.Join(s.stateDir, sessionsSubdir, s.id+".transcript.jsonl")
}
