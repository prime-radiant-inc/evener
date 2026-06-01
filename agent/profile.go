package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
)

// resolveEffortLevels returns reasoning effort levels for the given model.
// It first checks the embedded model catalog for model-specific levels, then
// falls back to the provider default. This allows per-model effort vocabularies
// while maintaining backward compatibility.
func resolveEffortLevels(model string, providerDefault []string) []string {
	if cat := llm.EmbeddedModelCatalog(); cat != nil {
		if mi := cat.GetModelInfo(model); mi != nil && len(mi.ReasoningEffortLevels) > 0 {
			return append([]string(nil), mi.ReasoningEffortLevels...)
		}
	}
	return providerDefault
}

// EnvironmentInfo holds the working directory, platform, version, date, and
// git/workspace details describing the environment an agent runs in.
type EnvironmentInfo struct {
	WorkingDir            string        `json:"working_dir"`
	Platform              string        `json:"platform"`
	OSVersion             string        `json:"os_version"`
	Today                 string        `json:"today"`            // YYYY-MM-DD
	KnowledgeCutoff       string        `json:"knowledge_cutoff"` // YYYY-MM-DD
	IsGitRepo             bool          `json:"is_git_repo"`
	GitBranch             string        `json:"git_branch,omitempty"`
	GitOriginURL          string        `json:"git_origin_url,omitempty"`
	GitModifiedFiles      int           `json:"git_modified_files"`
	GitUntrackedFiles     int           `json:"git_untracked_files"`
	GitRecentCommitTitles []string      `json:"git_recent_commit_titles,omitempty"`
	Workspace             WorkspaceInfo `json:"workspace,omitempty"`
}

// ProviderProfile describes a provider's identity, model, tool definitions,
// and capabilities, and produces derived profiles via WithModel.
type ProviderProfile interface {
	ID() string
	// BehaviorTag returns the stable behavior identity for this profile.
	// It equals the provider type for all providers except openai with the
	// chat-completions style, which returns "openai-compatible". The tag
	// is preserved across WithModel and WithProviderID calls so code that
	// keys on provider-specific behavior can use the tag instead of the id.
	BehaviorTag() string
	Model() string
	ToolDefinitions() []llm.ToolDefinition
	SupportsParallelToolCalls() bool
	ContextWindowSize() int
	ProjectDocFiles() []string
	CheapModel() string
	WithModel(model string) ProviderProfile
	ProviderOptions() map[string]any
	SupportsReasoning() bool
	// ReasoningEffortLevels returns the valid effort strings this provider
	// accepts, in ascending order. Returns an empty slice when the provider
	// does not support reasoning control.
	ReasoningEffortLevels() []string
	SupportsStreaming() bool
	SupportsWebSearch() bool
	DefaultCommandTimeoutMS() int
	KnowledgeCutoff() string
	// ToolNameMap returns the canonical→provider-specific tool name mapping.
	// Returns nil for providers that use canonical names (e.g. Anthropic).
	ToolNameMap() map[string]string
}

type baseProfile struct {
	id              string
	behaviorTag     string
	model           string
	parallel        bool
	contextWindow   int
	toolDefs        []llm.ToolDefinition
	toolNameMap     map[string]string // canonical → provider-specific
	docFiles        []string
	reasoning       bool
	streaming       bool
	defaultTimeout  int
	knowledgeCutoff string
	providerOpts    map[string]any
	effortLevels    []string
	webSearch       bool
	cheapModel      string
}

type toolCapability string

const (
	capabilityFiles            toolCapability = "files"
	capabilityCodexEditing     toolCapability = "codex_editing"
	capabilityExactEditing     toolCapability = "exact_editing"
	capabilityShellSearch      toolCapability = "shell_search"
	capabilityDirectoryListing toolCapability = "directory_listing"
	capabilityAgentControl     toolCapability = "agent_control"
	capabilityWorkflow         toolCapability = "workflow"
	capabilityWebFetch         toolCapability = "web_fetch"
	capabilityWebSearch        toolCapability = "web_search"
)

type profileSpec struct {
	id              string
	behaviorTag     string
	model           string
	parallel        bool
	contextWindow   int
	docFiles        []string
	reasoning       bool
	streaming       bool
	webSearch       bool
	defaultTimeout  int
	knowledgeCutoff string
	defaultEfforts  []string
	resolvedEfforts []string
	providerOpts    map[string]any
	toolNameMap     map[string]string
	capabilities    []toolCapability
	cheapModel      string
}

var (
	openAICodexCapabilities = []toolCapability{
		capabilityFiles,
		capabilityCodexEditing,
		capabilityShellSearch,
		capabilityAgentControl,
		capabilityWorkflow,
		capabilityWebFetch,
	}
	anthropicStyleCapabilities = []toolCapability{
		capabilityFiles,
		capabilityExactEditing,
		capabilityShellSearch,
		capabilityAgentControl,
		capabilityWorkflow,
		capabilityWebFetch,
	}
	geminiStyleCapabilities = []toolCapability{
		capabilityFiles,
		capabilityExactEditing,
		capabilityShellSearch,
		capabilityDirectoryListing,
		capabilityAgentControl,
		capabilityWorkflow,
		capabilityWebFetch,
		capabilityWebSearch,
	}
)

func cloneStringSlice(in []string) []string {
	if in == nil {
		return nil
	}
	return append([]string(nil), in...)
}

func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// cloneAnyMap/cloneAnyValue copy provider option data. Tool schemas use
// cloneSchemaMap via cloneToolDefinition instead.
func cloneAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = cloneAnyValue(v)
	}
	return out
}

func cloneAnyValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		return cloneAnyMap(x)
	case []map[string]any:
		out := make([]map[string]any, len(x))
		for i := range x {
			out[i] = cloneAnyMap(x[i])
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = cloneAnyValue(x[i])
		}
		return out
	case []string:
		return append([]string(nil), x...)
	default:
		return v
	}
}

func cloneToolDefinition(td llm.ToolDefinition) llm.ToolDefinition {
	td.Parameters = cloneSchemaMap(td.Parameters)
	return td
}

func toolDefinitionsForCapabilities(capabilities []toolCapability, efforts []string) []llm.ToolDefinition {
	enabled := make(map[toolCapability]bool, len(capabilities))
	for _, capability := range capabilities {
		enabled[capability] = true
	}

	var defs []llm.ToolDefinition
	add := func(td llm.ToolDefinition) {
		defs = append(defs, cloneToolDefinition(td))
	}

	if enabled[capabilityFiles] {
		add(defReadFile())
	}
	if enabled[capabilityCodexEditing] {
		add(defApplyPatch())
	}
	if enabled[capabilityFiles] {
		add(defWriteFile())
	}
	if enabled[capabilityExactEditing] {
		add(defEditFile())
	}
	if enabled[capabilityShellSearch] {
		add(defShell())
		add(defGrep())
		add(defGlob())
	}
	if enabled[capabilityDirectoryListing] {
		add(defListDir())
	}
	if enabled[capabilityAgentControl] {
		add(defSpawnAgent())
		add(defSendInput())
		add(defWait())
		add(defCloseAgent())
	}
	if enabled[capabilityWorkflow] {
		add(defTaskList(efforts))
	}
	if enabled[capabilityWebFetch] {
		add(defWebFetch())
	}
	if enabled[capabilityWebSearch] {
		add(defWebSearch())
	}
	if enabled[capabilityWorkflow] {
		add(defCommunicate())
		add(defUseSkill())
	}
	return defs
}

func buildBaseProfile(spec profileSpec) baseProfile {
	model := strings.TrimSpace(spec.model)
	efforts := spec.resolvedEfforts
	if efforts == nil {
		efforts = resolveEffortLevels(model, spec.defaultEfforts)
	}

	defaultTimeout := spec.defaultTimeout
	if defaultTimeout == 0 {
		defaultTimeout = 120_000
	}

	return baseProfile{
		id:              spec.id,
		behaviorTag:     spec.behaviorTag,
		model:           model,
		parallel:        spec.parallel,
		contextWindow:   spec.contextWindow,
		docFiles:        cloneStringSlice(spec.docFiles),
		reasoning:       spec.reasoning,
		streaming:       spec.streaming,
		webSearch:       spec.webSearch,
		defaultTimeout:  defaultTimeout,
		knowledgeCutoff: spec.knowledgeCutoff,
		effortLevels:    cloneStringSlice(efforts),
		providerOpts:    cloneAnyMap(spec.providerOpts),
		toolNameMap:     cloneStringMap(spec.toolNameMap),
		toolDefs:        toolDefinitionsForCapabilities(spec.capabilities, efforts),
		cheapModel:      spec.cheapModel,
	}
}

func (p *baseProfile) ID() string          { return p.id }
func (p *baseProfile) BehaviorTag() string { return p.behaviorTag }
func (p *baseProfile) Model() string       { return p.model }
func (p *baseProfile) ToolDefinitions() []llm.ToolDefinition {
	defs := append([]llm.ToolDefinition{}, p.toolDefs...)
	for i, d := range defs {
		if mapped, ok := p.toolNameMap[d.Name]; ok {
			defs[i].Name = mapped
		}
		defs[i] = withPurposeParameter(defs[i])
	}
	return defs
}
func (p *baseProfile) ToolNameMap() map[string]string {
	if len(p.toolNameMap) == 0 {
		return nil
	}
	m := make(map[string]string, len(p.toolNameMap))
	for k, v := range p.toolNameMap {
		m[k] = v
	}
	return m
}

// toolRegistry returns a toolRegistry pre-populated with the profile's tool
// definitions (canonical names) and placeholder executors; the Session wires
// real executors after construction. Reached via newProfileToolRegistry rather
// than the public ProviderProfile interface.
func (p *baseProfile) toolRegistry() *toolRegistry {
	reg := newToolRegistry()
	for _, td := range p.toolDefs {
		_ = reg.Register(registeredTool{
			Tool: llm.Tool{Definition: td},
			Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
				return nil, fmt.Errorf("tool executor not wired")
			},
		})
	}
	return reg
}

// newProfileToolRegistry builds the placeholder toolRegistry for a profile.
// Every profile embeds *baseProfile, which carries the unexported toolRegistry
// method, so this keeps the toolRegistry type off the public ProviderProfile
// interface while remaining callable for any ProviderProfile.
func newProfileToolRegistry(p ProviderProfile) *toolRegistry {
	if b, ok := p.(interface{ toolRegistry() *toolRegistry }); ok {
		return b.toolRegistry()
	}
	return newToolRegistry()
}
func (p *baseProfile) SupportsParallelToolCalls() bool { return p.parallel }
func (p *baseProfile) ContextWindowSize() int          { return p.contextWindow }
func (p *baseProfile) ProjectDocFiles() []string {
	return append([]string{}, p.docFiles...)
}
func (p *baseProfile) ProviderOptions() map[string]any { return p.providerOpts }
func (p *baseProfile) SupportsReasoning() bool         { return p.reasoning }
func (p *baseProfile) ReasoningEffortLevels() []string {
	return append([]string(nil), p.effortLevels...)
}
func (p *baseProfile) SupportsStreaming() bool      { return p.streaming }
func (p *baseProfile) SupportsWebSearch() bool      { return p.webSearch }
func (p *baseProfile) DefaultCommandTimeoutMS() int { return p.defaultTimeout }
func (p *baseProfile) KnowledgeCutoff() string      { return p.knowledgeCutoff }
func (p *baseProfile) CheapModel() string {
	if strings.TrimSpace(p.cheapModel) != "" {
		return strings.TrimSpace(p.cheapModel)
	}
	switch p.behaviorTag {
	case "openai":
		return "gpt-4.1-nano"
	case "anthropic":
		return "claude-haiku-4-5-20251001"
	case "google":
		return "gemini-2.5-flash-lite"
	case "glm":
		return "glm-4.7-flash"
	default:
		return p.model
	}
}

// prefixAction is the resolution of a slash-prefixed model string
// "X/Y" passed to WithModel. See decidePrefixAction.
type prefixAction int

const (
	// prefixActionSwitch: switch to a different provider via the existing
	// constructor table. Used when the prefix is a known provider name
	// distinct from the current id.
	prefixActionSwitch prefixAction = iota
	// prefixActionStrip: strip the redundant self-prefix. The remaining
	// bare name is the canonical wire model. Used when prefix == id for
	// providers whose canonical model is unprefixed (openai, kimi, glm,
	// the openrouter-style "openrouter/<upstream>/<model>" case where
	// the canonical wire model is the bare upstream form).
	prefixActionStrip
	// prefixActionKeep: leave the model verbatim — the slash is part of
	// the model namespace, not a provider switch. Used for meta-providers
	// whose model IDs include slashes by convention (openrouter routing
	// to upstreams, minimax canonical "minimax/m2.7").
	prefixActionKeep
)

// decidePrefixAction resolves what WithModel should do with a
// slash-prefixed model string. Three outcomes:
//
//   - openrouter / openrouter-anthropic (by behaviorTag): prefix == instanceName → strip
//     (canonical wire model is the bare upstream form, e.g. "anthropic/claude-3"
//     after stripping "openrouter/"). Switch when the prefix is an
//     unambiguous Serf-internal provider that OpenRouter does NOT
//     route to as an upstream (ollama, kimi, glm, the other
//     openrouter* mode). All other prefixes (anthropic, openai,
//     google, gemini, minimax, deepseek, ...) are upstream model
//     namespaces — keep verbatim.
//   - minimax (by behaviorTag): prefix == "minimax" → keep ("minimax/m2.7" is the
//     canonical wire model on minimax). Other prefixes → switch (a
//     legitimate cross-provider override).
//   - everyone else: prefix == instanceName → strip (existing convenience).
//     Different prefix → switch.
//
// behaviorTag is the stable provider family (e.g. "openrouter", "kimi").
// instanceName is the id of the specific profile instance — may differ from
// behaviorTag when the instance was renamed via WithProviderID.
func decidePrefixAction(behaviorTag, instanceName, prefix string) prefixAction {
	switch behaviorTag {
	case "openrouter", "openrouter-anthropic":
		if prefix == instanceName {
			return prefixActionStrip
		}
		// Unambiguous Serf-internal provider switches are allowed even
		// from meta-provider sessions. Everything else is an upstream
		// namespace.
		switch prefix {
		case "ollama", "kimi", "glm", "openrouter", "openrouter-anthropic":
			return prefixActionSwitch
		}
		return prefixActionKeep
	case "minimax":
		if prefix == "minimax" {
			return prefixActionKeep
		}
		return prefixActionSwitch
	}
	if prefix == instanceName {
		return prefixActionStrip
	}
	return prefixActionSwitch
}

// preserveBaseOverrides carries forward caller-applied tool-schema
// overrides from `original` onto a freshly-rebuilt profile. The
// constructor for the new profile/model handles model-derived state
// (context window, effort levels, providerOpts, the new provider's
// default toolset); this helper layers caller modifications back on
// top so WithCommunicateOutputSchema / WithAllowedDecisions overrides
// survive both same-provider WithModel rebuilds AND cross-provider
// WithModel switches.
//
// Only the "communicate" tool is preserved — both With* helpers
// modify that tool exclusively. Other tools recompute from the new
// constructor so the new provider's defaults (toolNameMap, etc.) take
// effect.
//
// Handles both *baseProfile and *anthropicProfile shapes; returns
// `rebuilt` unchanged for any other type.
func preserveBaseOverrides(rebuilt, original ProviderProfile) ProviderProfile {
	rebuiltBP := basePtrOf(rebuilt)
	if rebuiltBP == nil {
		return rebuilt
	}
	origBP := basePtrOf(original)
	if origBP == nil {
		return rebuilt
	}

	var origCommunicate *llm.ToolDefinition
	for i := range origBP.toolDefs {
		if origBP.toolDefs[i].Name == "communicate" {
			origCommunicate = &origBP.toolDefs[i]
			break
		}
	}
	if origCommunicate == nil {
		return rebuilt
	}

	defs := append([]llm.ToolDefinition(nil), rebuiltBP.toolDefs...)
	replaced := false
	for i := range defs {
		if defs[i].Name == "communicate" {
			defs[i] = *origCommunicate
			replaced = true
			break
		}
	}
	if !replaced {
		// Rebuilt provider's defaults don't include communicate (unusual
		// for Serf profiles but possible for custom callers); append it.
		defs = append(defs, *origCommunicate)
	}
	rebuiltBP.toolDefs = defs
	return rebuilt
}

// basePtrOf returns the embedded *baseProfile if the profile is a
// *baseProfile or *anthropicProfile, else nil.
func basePtrOf(p ProviderProfile) *baseProfile {
	switch v := p.(type) {
	case *baseProfile:
		return v
	case *anthropicProfile:
		return &v.baseProfile
	}
	return nil
}

// restampInstanceIdentity sets the behaviorTag and id on a freshly rebuilt
// profile so that a renamed instance (where id != behaviorTag, created via
// WithProviderID) keeps its identity across WithModel rebuilds. The rebuild
// constructor derives both id and behaviorTag from the behaviorTag argument
// (ensuring correct tag), but the instance may carry a user-assigned id
// distinct from the tag — re-stamp both so neither drifts.
func restampInstanceIdentity(p ProviderProfile, behaviorTag, id string) ProviderProfile {
	bp := basePtrOf(p)
	if bp == nil {
		return p
	}
	bp.behaviorTag = behaviorTag
	bp.id = id
	return p
}

// rebuildOnSameProviderChange reports whether a same-provider WithModel
// override needs to rebuild the profile via its constructor (rather than
// shallow-cloning) so that model-derived state — context window from
// catalog, providerOpts that depend on the model — is recomputed.
//
// True for providers whose constructors look up per-model state (every
// openai-compat provider; openrouter-anthropic). False for providers
// whose model-derived state is fixed at construction (openai, minimax)
// or handled by their own WithModel (anthropicProfile).
func rebuildOnSameProviderChange(behaviorTag string) bool {
	switch behaviorTag {
	case "kimi", "glm", "openrouter", "ollama", "openrouter-anthropic":
		return true
	}
	return false
}

func (p *baseProfile) WithModel(model string) ProviderProfile {
	model = strings.TrimSpace(model)
	if model == "" {
		model = p.model
	}
	// Parse "provider/model" strings. decidePrefixAction classifies each
	// slashed ref as strip (redundant self-prefix), keep (model namespace
	// slash on meta-providers), or switch (cross-provider, now handled by
	// the Session resolver). WithModel handles strip/keep; cross-provider
	// refs that are NOT handled by a resolver fall through to a shallow
	// clone with the model string unchanged rather than silently stripping.
	if parts := strings.SplitN(model, "/", 2); len(parts) == 2 {
		provider := strings.ToLower(parts[0])
		bareModel := parts[1]
		switch decidePrefixAction(p.behaviorTag, p.id, provider) {
		case prefixActionSwitch:
			// Cross-provider switching is now the Session resolver's job.
			// Fall through with model unchanged — the caller (SetModel or
			// subagents.go) should have resolved this before calling WithModel.
		case prefixActionStrip:
			model = bareModel
		case prefixActionKeep:
			// Leave model unchanged.
		}
	}
	// Same-provider override: rebuild via constructor for providers
	// whose model-derived state needs recomputation, otherwise shallow
	// clone (existing behavior for openai, anthropic-via-baseProfile,
	// google, minimax — their model-derived state is fixed).
	//
	// The rebuild path must preserve any tool-schema overrides applied
	// via WithCommunicateOutputSchema / WithAllowedDecisions on the
	// existing profile. Without this carry-over, Session.SetModel and
	// subagent model overrides would silently revert the communicate
	// schema to its constructor default. We also preserve any
	// providerOpts the caller has layered on, since those can also be
	// override-driven (e.g. test harnesses).
	if rebuildOnSameProviderChange(p.behaviorTag) {
		var rebuilt ProviderProfile
		switch p.behaviorTag {
		case "openrouter-anthropic":
			rebuilt = NewOpenRouterAnthropicProfile(model)
		default:
			rebuilt = NewOpenAICompatProfile(p.behaviorTag, model, 0)
		}
		// Re-stamp the instance identity onto the rebuilt profile so that a
		// renamed instance (id != behaviorTag, via WithProviderID) keeps its
		// id and correctly-derived tag across model changes.
		rebuilt = restampInstanceIdentity(rebuilt, p.behaviorTag, p.id)
		return preserveBaseOverrides(rebuilt, p)
	}
	clone := *p
	clone.model = model
	return &clone
}

// NewOpenAIProfile returns a ProviderProfile for OpenAI using the given model.
func NewOpenAIProfile(model string) ProviderProfile {
	bp := buildBaseProfile(profileSpec{
		id:              "openai",
		behaviorTag:     providercfg.BehaviorTag("openai", string(providercfg.StyleResponses)),
		model:           model,
		parallel:        true,
		contextWindow:   400_000,
		docFiles:        []string{"AGENTS.md", ".codex/instructions.md"},
		reasoning:       true,
		streaming:       true,
		webSearch:       true,
		defaultTimeout:  120_000,
		knowledgeCutoff: "2025-06-01",
		defaultEfforts:  []string{"low", "medium", "high", "xhigh"},
		providerOpts: map[string]any{
			"openai": map[string]any{
				"parallel_tool_calls": true,
			},
		},
		toolNameMap: map[string]string{
			"shell": "exec_command",
			"grep":  "grep_files",
			"glob":  "list_dir",
		},
		capabilities: openAICodexCapabilities,
	})
	return &bp
}

const anthropicSuffix1M = "[1m]"
const anthropicBeta1M = "context-1m-2025-08-07"

// anthropicProviderOpts builds a fresh providerOpts map for the Anthropic
// profile. When has1M is true the 1M-context beta header is included.
func anthropicProviderOpts(has1M bool) map[string]any {
	opts := map[string]any{
		// Prevent truncated tool-call JSON on large code/test edits.
		"max_tokens": 16384,
	}
	if has1M {
		opts["beta_headers"] = anthropicBeta1M
	}
	return map[string]any{
		"anthropic": opts,
	}
}

// anthropicProfile embeds baseProfile and overrides WithModel to re-derive
// contextWindow and providerOpts from the model string.
type anthropicProfile struct {
	baseProfile
}

func (p *anthropicProfile) WithModel(model string) ProviderProfile {
	model = strings.TrimSpace(model)
	if model == "" {
		model = p.model
	}
	// Parse "provider/model" strings. Strip the redundant self-prefix
	// ("anthropic/..."). Cross-provider refs (decidePrefixAction returns
	// prefixActionSwitch) are now handled by the Session resolver; fall
	// through with model unchanged so the caller can surface the error or
	// handle the switch at the session level.
	if parts := strings.SplitN(model, "/", 2); len(parts) == 2 {
		provider := strings.ToLower(parts[0])
		bareModel := parts[1]
		if provider == "anthropic" {
			model = bareModel
		}
		// prefixActionSwitch refs for non-anthropic providers: leave model
		// unchanged — cross-provider switching is the Session resolver's job.
	}
	clone := *p
	clone.model = model
	has1M := strings.HasSuffix(model, anthropicSuffix1M)
	if has1M {
		clone.contextWindow = 1_000_000
	} else {
		clone.contextWindow = 200_000
	}
	clone.providerOpts = anthropicProviderOpts(has1M)
	return &clone
}

// NewAnthropicProfile returns a ProviderProfile for Anthropic using the given
// model. The context window is 1,000,000 when the model carries the 1M-context
// suffix and 200,000 otherwise.
func NewAnthropicProfile(model string) ProviderProfile {
	model = strings.TrimSpace(model)
	has1M := strings.HasSuffix(model, anthropicSuffix1M)
	ctxWindow := 200_000
	if has1M {
		ctxWindow = 1_000_000
	}
	bp := buildBaseProfile(profileSpec{
		id:              "anthropic",
		behaviorTag:     providercfg.BehaviorTag("anthropic", ""),
		model:           model,
		parallel:        true,
		contextWindow:   ctxWindow,
		docFiles:        []string{"CLAUDE.md", "AGENTS.md"},
		reasoning:       true,
		streaming:       true,
		webSearch:       true,
		defaultTimeout:  120_000,
		knowledgeCutoff: "2025-04-01",
		defaultEfforts:  []string{"low", "medium", "high", "max"},
		providerOpts:    anthropicProviderOpts(has1M),
		capabilities:    anthropicStyleCapabilities,
	})
	return &anthropicProfile{
		baseProfile: bp,
	}
}

// NewGeminiProfile returns a ProviderProfile for Google Gemini using the given
// model.
func NewGeminiProfile(model string) ProviderProfile {
	bp := buildBaseProfile(profileSpec{
		id:              "google",
		behaviorTag:     providercfg.BehaviorTag("google", ""),
		model:           model,
		parallel:        true,
		contextWindow:   1_000_000,
		docFiles:        []string{"GEMINI.md", "AGENTS.md"},
		reasoning:       true,
		streaming:       true,
		webSearch:       true,
		defaultTimeout:  120_000,
		knowledgeCutoff: "2025-03-01",
		defaultEfforts:  []string{"low", "medium", "high"},
		providerOpts: map[string]any{
			"gemini": map[string]any{
				"safetySettings": []map[string]any{
					{"category": "HARM_CATEGORY_HARASSMENT", "threshold": "BLOCK_ONLY_HIGH"},
					{"category": "HARM_CATEGORY_HATE_SPEECH", "threshold": "BLOCK_ONLY_HIGH"},
					{"category": "HARM_CATEGORY_SEXUALLY_EXPLICIT", "threshold": "BLOCK_ONLY_HIGH"},
					{"category": "HARM_CATEGORY_DANGEROUS_CONTENT", "threshold": "BLOCK_ONLY_HIGH"},
				},
			},
		},
		toolNameMap: map[string]string{
			"shell":    "run_shell_command",
			"grep":     "grep_search",
			"list_dir": "list_directory",
		},
		capabilities: geminiStyleCapabilities,
	})
	return &bp
}

// NewMiniMaxProfile returns a ProviderProfile for MiniMax using the given model.
func NewMiniMaxProfile(model string) ProviderProfile {
	bp := buildBaseProfile(profileSpec{
		id:              "minimax",
		behaviorTag:     providercfg.BehaviorTag("minimax", ""),
		model:           model,
		parallel:        true,
		contextWindow:   204_800,
		docFiles:        []string{"CLAUDE.md", "AGENTS.md"},
		reasoning:       true,
		streaming:       true,
		defaultTimeout:  120_000,
		knowledgeCutoff: "2025-06-01",
		defaultEfforts:  []string{"low", "medium", "high", "max"},
		capabilities:    anthropicStyleCapabilities,
	})
	return &bp
}

// NewOpenRouterAnthropicProfile creates a profile that routes any OpenRouter-
// served model through OpenRouter's Anthropic-Messages-compatible endpoint
// (https://openrouter.ai/api/v1/messages).
//
// Use this for models whose native tool-call format is Anthropic-style XML
// (notably minimax/minimax-m2.7). Routing them through OpenRouter's OpenAI
// chat/completions endpoint produces corrupt tool_calls.arguments where XML
// fragments leak into the JSON args string; the Anthropic endpoint keeps the
// model's native format end-to-end.
//
// The provider id is "openrouter-anthropic" which is served by
// llm/providers/openrouter_anthropic — it wraps the standard Anthropic
// adapter with the OpenRouter base URL and OPENROUTER_API_KEY.
// resolveOpenRouterAnthropicWebSearch resolves SupportsWebSearch for an
// openrouter-anthropic profile via the same three-step lookup as
// resolveOpenRouterAnthropicCtxAndEfforts, but with explicit precedence
// tracking so step 3 (the bare-upstream-stripped fallback) cannot
// overwrite an authoritative step 1 / step 2 decision.
//
// Returns the resolved value or `defaultWS` when no step set ws.
func resolveOpenRouterAnthropicWebSearch(lookup func(string) *llm.ModelInfo, model string, defaultWS bool) bool {
	ws := defaultWS
	resolved := false

	// Step 1: openrouter-prefixed entry. Authoritative — sets ws when
	// the field is explicitly present, suppresses later steps either way.
	prefixedHit := false
	if mi := lookup("openrouter/" + model); mi != nil {
		prefixedHit = true
		if mi.SupportsWebSearch != nil {
			ws = *mi.SupportsWebSearch
			resolved = true
		}
	}

	// Step 2: bare-direct entry, only when step 1 missed.
	if !prefixedHit {
		if mi := lookup(model); mi != nil {
			if mi.SupportsWebSearch != nil {
				ws = *mi.SupportsWebSearch
				resolved = true
			}
		}
	}

	// Step 3: bare-upstream-stripped fallback. Only fills when no
	// earlier step resolved ws — never overwrites an authoritative
	// answer.
	if !resolved {
		if _, after, hasSlash := strings.Cut(model, "/"); hasSlash && after != "" {
			if mi := lookup(after); mi != nil {
				if mi.SupportsWebSearch != nil {
					ws = *mi.SupportsWebSearch
				}
			}
		}
	}

	return ws
}

// resolveOpenRouterAnthropicCtxAndEfforts handles the context-window and
// effort-levels resolution for openrouter-anthropic profiles. Same
// three-step precedence as the web-search resolver: step 1 prefixed,
// step 2 bare-direct (only when step 1 misses), step 3
// bare-upstream-stripped (fallback only — never overwrites earlier).
func resolveOpenRouterAnthropicCtxAndEfforts(lookup func(string) *llm.ModelInfo, model string, defaultCtx int, defaultEfforts []string) (int, []string) {
	ctx := defaultCtx
	efforts := defaultEfforts

	ctxResolved := false
	prefixedHit := false
	if mi := lookup("openrouter/" + model); mi != nil {
		prefixedHit = true
		if mi.ContextWindow > 0 {
			ctx = mi.ContextWindow
			ctxResolved = true
		}
		if len(mi.ReasoningEffortLevels) > 0 {
			efforts = append([]string(nil), mi.ReasoningEffortLevels...)
		}
	}

	if !prefixedHit {
		if mi := lookup(model); mi != nil {
			if mi.ContextWindow > 0 {
				ctx = mi.ContextWindow
				ctxResolved = true
			}
			if len(mi.ReasoningEffortLevels) > 0 {
				efforts = append([]string(nil), mi.ReasoningEffortLevels...)
			}
		}
	}

	// Step 3 fallback: fills both ctx and efforts only when no earlier
	// step provided them.
	if _, after, hasSlash := strings.Cut(model, "/"); hasSlash && after != "" {
		if mi := lookup(after); mi != nil {
			if !ctxResolved && mi.ContextWindow > 0 {
				ctx = mi.ContextWindow
			}
			if efforts == nil && len(mi.ReasoningEffortLevels) > 0 {
				efforts = append([]string(nil), mi.ReasoningEffortLevels...)
			}
		}
	}

	return ctx, efforts
}

// NewOpenRouterAnthropicProfile returns a ProviderProfile for the
// openrouter-anthropic provider using the given model, resolving the context
// window, reasoning effort levels, and web-search support from the embedded
// model catalog.
func NewOpenRouterAnthropicProfile(model string) ProviderProfile {
	model = strings.TrimSpace(model)
	// Resolve catalog metadata. The openrouter-anthropic profile draws
	// from up to three places:
	//
	//   - "openrouter/<model>" — prefixed entries are SPARSE: they
	//     carry context window and pricing but typically omit
	//     capability flags. Treat them as positive-only: missing
	//     fields are not authoritative, so a missing supports_web_search
	//     does NOT flip our `true` default off.
	//   - "<model>" — bare-direct entries (no openrouter prefix). These
	//     are AUTHORITATIVE: when present they reflect explicit decisions
	//     about what the model supports. supports_web_search:false on
	//     "minimax/minimax-m2.7" must surface as `false` here.
	//   - "<upstream-stripped>" — the model with a single leading
	//     "<upstream>/" segment removed (e.g. "anthropic/claude-sonnet-4-5"
	//     → "claude-sonnet-4-5"). Used as a final fallback to pick up
	//     serf-shipped effort overrides keyed under the bare upstream
	//     form. Treat as positive-only — generic upstream entries shouldn't
	//     override a default OFF based on a missing field.
	contextWindow := 128_000
	ws := true // default to web search on (Anthropic models support it)
	defaultEfforts := []string{"low", "medium", "high", "max"}
	var efforts []string

	cat := llm.EmbeddedModelCatalog()
	if cat != nil {
		ws = resolveOpenRouterAnthropicWebSearch(cat.GetModelInfo, model, ws)
		contextWindow, efforts = resolveOpenRouterAnthropicCtxAndEfforts(cat.GetModelInfo, model, contextWindow, efforts)
	}

	if efforts == nil {
		efforts = resolveEffortLevels(model, defaultEfforts)
	}
	bp := buildBaseProfile(profileSpec{
		id:              "openrouter-anthropic",
		behaviorTag:     providercfg.BehaviorTag("openrouter-anthropic", ""),
		model:           model,
		parallel:        true,
		contextWindow:   contextWindow,
		docFiles:        []string{"CLAUDE.md", "AGENTS.md"},
		reasoning:       true,
		streaming:       true,
		webSearch:       ws,
		defaultTimeout:  120_000,
		knowledgeCutoff: "2025-06-01",
		defaultEfforts:  defaultEfforts,
		resolvedEfforts: efforts,
		providerOpts: map[string]any{
			"anthropic": map[string]any{
				"max_tokens": 16384,
			},
		},
		capabilities: anthropicStyleCapabilities,
	})
	return &bp
}

// suppressBareCatalogLookup reports whether a provider should skip the
// bare-name catalog lookup (step 2 in the resolver). The hazard is
// inheriting an unrelated provider's entry when the bare model name
// happens to match.
//
// Only ollama is suppressed: local Ollama models and bare upstream-API
// entries are unrelated by intent — falling back to a coincidental name
// match (e.g. "claude-3-haiku") would silently mask real context
// truncation. OpenRouter is NOT suppressed because it explicitly routes
// to upstreams whose models often appear in the catalog only under
// their bare upstream keys (e.g. "minimax/minimax-m2.7"); inheriting
// the upstream's metadata is the right behavior. The prefixed-first
// precedence still protects OpenRouter from overlap cases like
// "openrouter/deepseek/deepseek-r1" vs bare "deepseek/deepseek-r1".
func suppressBareCatalogLookup(behaviorTag string) bool {
	return behaviorTag == "ollama"
}

// resolveOpenAICompatCatalogModel runs the OpenAI-compatible catalog
// lookup precedence used by NewOpenAICompatProfile, returning the first
// matching ModelInfo or nil. Pulled out as a pure function so it can be
// unit-tested with a fake catalog without depending on which specific
// entries the embedded catalog ships.
//
// Precedence (first hit wins):
//  1. "<behaviorTag>/<model>" exact — covers openrouter (incl. overlapping cases
//     like "openrouter/deepseek/deepseek-r1" whose bare form
//     "deepseek/deepseek-r1" is a different provider's entry) and any
//     tagged ollama variant the catalog ships with its tag (e.g.
//     "ollama/llama3:8b")
//  2. bare model name — covers kimi/glm (unprefixed catalog keys) and
//     openrouter (which routes to upstreams whose models often only have
//     bare entries, e.g. "minimax/minimax-m2.7"). SKIPPED for ollama:
//     local Ollama models and bare upstream entries are unrelated by
//     intent, so a bare match (e.g. asking ollama for "claude-3-haiku")
//     would silently inherit Anthropic's 200K context window.
//  3. "<behaviorTag>/<base>" where base is `model` with any ":<tag>" suffix
//     stripped — covers typical Ollama tagged variants whose catalog
//     entry is the untagged family ("llama3.1:8b" -> "ollama/llama3.1")
func resolveOpenAICompatCatalogModel(lookup func(string) *llm.ModelInfo, behaviorTag, model string) *llm.ModelInfo {
	if mi := lookup(behaviorTag + "/" + model); mi != nil {
		return mi
	}
	if !suppressBareCatalogLookup(behaviorTag) {
		if mi := lookup(model); mi != nil {
			return mi
		}
	}
	if base, _, hasTag := strings.Cut(model, ":"); hasTag && base != "" {
		if mi := lookup(behaviorTag + "/" + base); mi != nil {
			return mi
		}
	}
	return nil
}

// NewOpenAICompatProfile creates a profile for OpenAI-compatible providers
// (kimi, glm, openrouter, ollama, etc.). If contextWindow is 0, it's looked
// up from the embedded model catalog; if still unknown, defaults to 128K.
//
// The catalog lookup tries up to three forms in order, see
// resolveOpenAICompatCatalogModel for the precedence contract.
//
// The wire model name is always the bare value; only the catalog lookup
// is broadened.
func NewOpenAICompatProfile(id, model string, contextWindow int) ProviderProfile {
	model = strings.TrimSpace(model)
	var catModel *llm.ModelInfo
	if cat := llm.EmbeddedModelCatalog(); cat != nil {
		catModel = resolveOpenAICompatCatalogModel(cat.GetModelInfo, id, model)
	}
	if contextWindow == 0 && catModel != nil && catModel.ContextWindow > 0 {
		contextWindow = catModel.ContextWindow
	}
	if contextWindow == 0 {
		contextWindow = 128_000
	}
	defaultEfforts := []string{"low", "medium", "high"}
	var efforts []string
	if catModel != nil && len(catModel.ReasoningEffortLevels) > 0 {
		efforts = append([]string(nil), catModel.ReasoningEffortLevels...)
	} else {
		efforts = resolveEffortLevels(model, defaultEfforts)
	}
	// MiniMax via OpenRouter uses reasoning_details for multi-turn reasoning
	// (not OpenAI's reasoning_content). Set the provider option that tells the
	// openai-compat adapter to serialize/deserialize reasoning_details.
	// Gated on behaviorTag=="openrouter" so other providers that route through
	// this constructor (e.g. ollama, where a user could legitimately have a
	// model named under a "minimax/..." namespace) don't get the
	// OpenRouter-specific option injected.
	var providerOpts map[string]any
	if id == "openrouter" && strings.HasPrefix(model, "minimax/") {
		providerOpts = map[string]any{
			"openai-compatible": map[string]any{
				"reasoning": map[string]any{"enabled": true},
			},
		}
	}
	bp := buildBaseProfile(profileSpec{
		id:              id,
		behaviorTag:     providercfg.BehaviorTag(id, ""),
		model:           model,
		parallel:        true,
		contextWindow:   contextWindow,
		docFiles:        []string{"AGENTS.md"},
		reasoning:       true,
		streaming:       true,
		webSearch:       false,
		defaultTimeout:  120_000,
		knowledgeCutoff: "2025-06-01",
		defaultEfforts:  defaultEfforts,
		resolvedEfforts: efforts,
		providerOpts:    providerOpts,
		toolNameMap:     nil,
		capabilities:    openAICodexCapabilities,
	})
	return &bp
}

func envInfoFromEnv(env ExecutionEnvironment) EnvironmentInfo {
	wd := ""
	plat := ""
	osv := ""
	if env != nil {
		wd = env.WorkingDirectory()
		plat = env.Platform()
		osv = env.OSVersion()
	}
	return EnvironmentInfo{
		WorkingDir: wd,
		Platform:   plat,
		OSVersion:  osv,
		Today:      time.Now().UTC().Format("2006-01-02"),
		Workspace:  ScanWorkspace(wd),
	}
}
