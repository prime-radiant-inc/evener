package provider

import (
	"strings"

	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
)

// resolveEffortLevels returns reasoning effort levels for the given model.
// It first checks the embedded model catalog for model-specific levels, then
// falls back to the provider default. This allows per-model effort vocabularies
// while maintaining backward compatibility.
func resolveEffortLevels(model string, providerDefault []string) []string {
	if cat := llm.EmbeddedModelCatalog(); cat != nil {
		// LookupModelInfo canonicalizes the "[1m]" suffix and dated snapshots so a
		// 1M-context or dated model still resolves its family's real levels.
		if mi := cat.LookupModelInfo(model); mi != nil && len(mi.ReasoningEffortLevels) > 0 {
			return append([]string(nil), mi.ReasoningEffortLevels...)
		}
	}
	return providerDefault
}

// Profile describes a provider's identity, model, tool definitions, and
// capabilities, and produces derived profiles via WithModel and the With*
// decorator functions. Construct one with NewOpenAIProfile or
// ResolveProfileFromConfig; the zero value is not usable.
type Profile struct {
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
	// cheapProvider routes auxiliary "side calls" (naming, summarization,
	// web_fetch Q&A) to a different provider instance than the main model. Empty
	// means same provider as the main model. Set via WithCheapModel("provider/model").
	cheapProvider string
}

type toolCapability string

const (
	capabilityFiles            toolCapability = "files"
	capabilityCodexEditing     toolCapability = "codex_editing"
	capabilityExactEditing     toolCapability = "exact_editing"
	capabilityShellSearch      toolCapability = "shell_search"
	capabilityDirectoryListing toolCapability = "directory_listing"
	capabilityJobControl       toolCapability = "job_control"
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

// Keep in sync with agent.WatchEventKindNames / agent.modelEventKinds. The
// provider package cannot import agent, but provider-advertised job_watch must
// describe the same model-facing event vocabulary as the registered tool.
var jobWatchEventKindNames = []string{"assistant.tool", "communicate", "job.notification"}

var (
	openAICodexCapabilities = []toolCapability{
		capabilityFiles,
		capabilityCodexEditing,
		capabilityShellSearch,
		capabilityJobControl,
		capabilityWorkflow,
		capabilityWebFetch,
	}
	anthropicStyleCapabilities = []toolCapability{
		capabilityFiles,
		capabilityExactEditing,
		capabilityShellSearch,
		capabilityJobControl,
		capabilityWorkflow,
		capabilityWebFetch,
	}
	geminiStyleCapabilities = []toolCapability{
		capabilityFiles,
		capabilityExactEditing,
		capabilityShellSearch,
		capabilityDirectoryListing,
		capabilityJobControl,
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
// tool.CloneSchemaMap via cloneToolDefinition instead.
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
	td.Parameters = tool.CloneSchemaMap(td.Parameters)
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
		add(tool.DefReadFile())
	}
	if enabled[capabilityCodexEditing] {
		add(tool.DefApplyPatch())
	}
	if enabled[capabilityFiles] {
		add(tool.DefWriteFile())
	}
	if enabled[capabilityExactEditing] {
		add(tool.DefEditFile())
	}
	if enabled[capabilityShellSearch] {
		add(tool.DefShell())
		add(tool.DefGrep())
		add(tool.DefGlob())
	}
	if enabled[capabilityDirectoryListing] {
		add(tool.DefListDir())
	}
	if enabled[capabilityJobControl] {
		add(tool.DefJobStatus())
		add(tool.DefJobList())
		add(tool.DefJobStop())
		add(tool.DefDelegate(nil))
		add(tool.DefJobWatch(jobWatchEventKindNames))
		add(tool.DefDelegateSend())
	}
	if enabled[capabilityWorkflow] {
		add(tool.DefTaskList(efforts))
	}
	if enabled[capabilityWebFetch] {
		add(tool.DefWebFetch())
	}
	if enabled[capabilityWebSearch] {
		add(tool.DefWebSearch())
	}
	if enabled[capabilityWorkflow] {
		add(tool.DefCommunicate())
		add(tool.DefUseSkill())
	}
	return defs
}

func buildBaseProfile(spec profileSpec) Profile {
	model := strings.TrimSpace(spec.model)
	efforts := spec.resolvedEfforts
	if efforts == nil {
		efforts = resolveEffortLevels(model, spec.defaultEfforts)
	}

	defaultTimeout := spec.defaultTimeout
	if defaultTimeout == 0 {
		defaultTimeout = 120_000
	}

	return Profile{
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

// ID returns the profile identifier, typically "provider/model".
func (p *Profile) ID() string { return p.id }

// BehaviorTag returns the stable behavior identity for this profile.
// It equals the provider type for all providers except openai with the
// chat-completions style, which returns "openai-compatible". The tag
// is preserved across WithModel and WithProviderID calls so code that
// keys on provider-specific behavior can use the tag instead of the id.
func (p *Profile) BehaviorTag() string { return p.behaviorTag }

// Model returns the model name this profile drives.
func (p *Profile) Model() string { return p.model }

// ToolDefinitions returns the profile's tool schemas by their canonical names.
// Provider-specific renaming (via ToolNameMap) and the shared purpose parameter
// are applied by the agent when advertising tools to the model, not here.
func (p *Profile) ToolDefinitions() []llm.ToolDefinition {
	return append([]llm.ToolDefinition{}, p.toolDefs...)
}

// ToolNameMap returns the canonical→provider-specific tool name mapping.
// Returns nil for providers that use canonical names (e.g. Anthropic).
func (p *Profile) ToolNameMap() map[string]string {
	if len(p.toolNameMap) == 0 {
		return nil
	}
	m := make(map[string]string, len(p.toolNameMap))
	for k, v := range p.toolNameMap {
		m[k] = v
	}
	return m
}

// SupportsParallelToolCalls reports whether the model may emit multiple
// tool calls in a single response.
func (p *Profile) SupportsParallelToolCalls() bool { return p.parallel }

// ContextWindowSize returns the model's context window in tokens.
func (p *Profile) ContextWindowSize() int { return p.contextWindow }

// ProjectDocFiles returns the project-doc filenames this provider loads
// from the working directory (e.g. CLAUDE.md, AGENTS.md), in priority order.
func (p *Profile) ProjectDocFiles() []string {
	return append([]string{}, p.docFiles...)
}

// ProviderOptions returns provider-specific request options passed through
// to the LLM call.
func (p *Profile) ProviderOptions() map[string]any { return p.providerOpts }

// SupportsReasoning reports whether the model accepts a reasoning-effort
// control.
func (p *Profile) SupportsReasoning() bool { return p.reasoning }

// ReasoningEffortLevels returns the valid effort strings this provider
// accepts, in ascending order. Returns an empty slice when the provider
// does not support reasoning control.
func (p *Profile) ReasoningEffortLevels() []string {
	return append([]string(nil), p.effortLevels...)
}

// SupportsStreaming reports whether the provider supports streaming responses.
func (p *Profile) SupportsStreaming() bool { return p.streaming }

// SupportsWebSearch reports whether the provider offers a native web-search tool.
func (p *Profile) SupportsWebSearch() bool { return p.webSearch }

// DefaultCommandTimeoutMS returns the provider's preferred default shell
// command timeout in milliseconds.
func (p *Profile) DefaultCommandTimeoutMS() int { return p.defaultTimeout }

// KnowledgeCutoff returns the model's training knowledge-cutoff date (YYYY-MM-DD).
func (p *Profile) KnowledgeCutoff() string { return p.knowledgeCutoff }

// CheapModel returns a cheaper model from the same provider for auxiliary
// work such as session naming and summarization.
func (p *Profile) CheapModel() string {
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

// ConfiguredCheapModel returns the auxiliary model explicitly set via
// WithCheapModel, or "" if none was configured. Unlike CheapModel it does not
// fall back to a provider default, so callers can detect whether a cheap model
// was configured at all (e.g. to decide whether to run session naming).
func (p *Profile) ConfiguredCheapModel() string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(p.cheapModel)
}

// CheapProvider returns the provider instance name that auxiliary side calls
// should route to: the explicitly configured cross-provider cheap provider, or
// the main profile's own id when none is set (same-provider, the default).
func (p *Profile) CheapProvider() string {
	if p == nil {
		return ""
	}
	if cp := strings.TrimSpace(p.cheapProvider); cp != "" {
		return cp
	}
	return p.ID()
}

// CheapModelRef returns the (provider, model) pair for auxiliary side calls,
// resolving the provider via CheapProvider and the model via CheapModel. Sites
// that issue a cheap completion route on this pair so the cheap model can live
// on a different provider than the main model.
func (p *Profile) CheapModelRef() (provider, model string) {
	return p.CheapProvider(), p.CheapModel()
}

// CheapModelRefString returns the configured cheap model as a WithCheapModel ref
// ("provider/model" when cross-provider, else the bare model), or "" when no
// cheap model is configured. It is the persistable form: feeding the result back
// to WithCheapModel reproduces the routing, so it survives serf resume. Unlike
// CheapModelRef it does NOT fall back to a provider default — an empty result
// means "not configured", matching ConfiguredCheapModel.
func (p *Profile) CheapModelRefString() string {
	if p == nil {
		return ""
	}
	model := strings.TrimSpace(p.cheapModel)
	if model == "" {
		return ""
	}
	if cp := strings.TrimSpace(p.cheapProvider); cp != "" {
		return cp + "/" + model
	}
	return model
}

// WithLiveModelInfo returns a copy of the profile updated with model metadata
// queried live from the provider. A positive context window, a non-empty set of
// reasoning-effort levels, reasoning support, and web-search support each
// override the constructor-derived value when present in info; absent fields
// leave the profile unchanged.
func (p *Profile) WithLiveModelInfo(info llm.ModelInfo) *Profile {
	if p == nil {
		return nil
	}
	clone := *p
	if info.ContextWindow > 0 {
		clone.contextWindow = info.ContextWindow
	}
	if len(info.ReasoningEffortLevels) > 0 {
		clone.effortLevels = append([]string(nil), info.ReasoningEffortLevels...)
		// Keep the effort-enum tool schema (task_list) in sync with the live
		// levels, or the model sees the constructor enum instead.
		defs := append([]llm.ToolDefinition(nil), clone.toolDefs...)
		for i := range defs {
			if defs[i].Name == "task_list" {
				defs[i] = tool.DefTaskList(clone.effortLevels)
			}
		}
		clone.toolDefs = defs
	}
	if info.SupportsReasoning {
		clone.reasoning = true
	}
	if info.SupportsWebSearch != nil {
		clone.webSearch = *info.SupportsWebSearch
	}
	return &clone
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

// CrossProviderRef reports whether ref ("<prefix>/<model>") selects a provider
// different from p's — one that WithModel cannot resolve on its own, so a
// session-level resolver must handle it. It is false for a bare model, for a
// redundant self-prefix, and for a meta-provider's upstream namespace (which
// WithModel keeps verbatim).
func (p *Profile) CrossProviderRef(ref string) bool {
	parts := strings.SplitN(ref, "/", 2)
	if len(parts) != 2 {
		return false
	}
	prefix := strings.ToLower(parts[0])
	return decidePrefixAction(p.behaviorTag, p.id, prefix) == prefixActionSwitch
}

// WithCommunicateOverridesFrom carries forward caller-applied tool-schema
// overrides from original onto p, a freshly-rebuilt profile. The constructor for
// the new profile/model handles model-derived state (context window, effort
// levels, providerOpts, the new provider's default toolset); this layers caller
// modifications back on top so WithCommunicateOutputSchema / WithAllowedDecisions
// overrides survive both same-provider WithModel rebuilds AND cross-provider
// switches performed by a resolver.
//
// Only the "communicate" tool is preserved — both With* helpers modify that tool
// exclusively. Other tools recompute from the new constructor so the new
// provider's defaults (toolNameMap, etc.) take effect.
//
// p is returned unchanged when either profile is nil.
// withCheapModelFrom carries the cheap-model routing (set via WithCheapModel)
// from original onto p. A constructor rebuild resets these to empty, so a
// rebuild-based WithModel must restore them or side calls (naming, summarization,
// web-fetch) lose their configured cheap model.
func (p *Profile) withCheapModelFrom(original *Profile) *Profile {
	if p == nil || original == nil {
		return p
	}
	p.cheapModel = original.cheapModel
	p.cheapProvider = original.cheapProvider
	return p
}

func (p *Profile) WithCommunicateOverridesFrom(original *Profile) *Profile {
	if p == nil || original == nil {
		return p
	}

	var origCommunicate *llm.ToolDefinition
	for i := range original.toolDefs {
		if original.toolDefs[i].Name == "communicate" {
			origCommunicate = &original.toolDefs[i]
			break
		}
	}
	if origCommunicate == nil {
		return p
	}

	defs := append([]llm.ToolDefinition(nil), p.toolDefs...)
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
	p.toolDefs = defs
	return p
}

// restampInstanceIdentity sets the behaviorTag and id on a freshly rebuilt
// profile so that a renamed instance (where id != behaviorTag, created via
// WithProviderID) keeps its identity across WithModel rebuilds. The rebuild
// constructor derives both id and behaviorTag from the behaviorTag argument
// (ensuring correct tag), but the instance may carry a user-assigned id
// distinct from the tag — re-stamp both so neither drifts.
func restampInstanceIdentity(p *Profile, behaviorTag, id string) *Profile {
	if p == nil {
		return nil
	}
	p.behaviorTag = behaviorTag
	p.id = id
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
// or handled by the dedicated anthropic branch of WithModel.
func rebuildOnSameProviderChange(behaviorTag string) bool {
	switch behaviorTag {
	case "kimi", "glm", "openrouter", "ollama", "openrouter-anthropic":
		return true
	}
	return false
}

// WithModel returns a copy of this profile that drives a different model.
func (p *Profile) WithModel(model string) *Profile {
	model = strings.TrimSpace(model)
	if model == "" {
		model = p.model
	}

	// Anthropic re-derives all model-dependent state from the model string (the
	// [1m] suffix selects the 1M-context beta; effort levels and the tool schemas
	// that embed the effort enum vary per model). It rebuilds via the constructor
	// rather than shallow-cloning, so a model switch can't leave a stale
	// max-capable effort set or task_list enum on a model capped at high.
	if p.behaviorTag == "anthropic" {
		// Strip the redundant "anthropic/" self-prefix; cross-provider refs are
		// the Session resolver's job, so leave them unchanged here.
		if parts := strings.SplitN(model, "/", 2); len(parts) == 2 {
			provider := strings.ToLower(parts[0])
			if provider == "anthropic" {
				model = parts[1]
			}
		}
		rebuilt := restampInstanceIdentity(newAnthropicProfile(model), p.behaviorTag, p.id)
		return rebuilt.WithCommunicateOverridesFrom(p).withCheapModelFrom(p)
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
	// clone (existing behavior for openai, anthropic-via-Profile,
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
		var rebuilt *Profile
		switch p.behaviorTag {
		case "openrouter-anthropic":
			rebuilt = newOpenRouterAnthropicProfile(model)
		default:
			rebuilt = newOpenAICompatProfile(p.behaviorTag, model, 0)
		}
		// Re-stamp the instance identity onto the rebuilt profile so that a
		// renamed instance (id != behaviorTag, via WithProviderID) keeps its
		// id and correctly-derived tag across model changes.
		rebuilt = restampInstanceIdentity(rebuilt, p.behaviorTag, p.id)
		return rebuilt.WithCommunicateOverridesFrom(p).withCheapModelFrom(p)
	}
	clone := *p
	clone.model = model
	return &clone
}

// NewOpenAIProfile returns a *Profile for OpenAI using the given model.
func NewOpenAIProfile(model string) *Profile {
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

// anthropicDefaultEfforts is the fallback effort-level set for Anthropic models
// not found in the catalog. Catalog models resolve their own per-model levels.
var anthropicDefaultEfforts = []string{"low", "medium", "high", "max"}

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

// newAnthropicProfile returns a *Profile for Anthropic using the given model.
// The context window is 1,000,000 when the model carries the 1M-context suffix
// and 200,000 otherwise. Profile.WithModel re-derives both the context window
// and provider options for the anthropic behavior tag.
func newAnthropicProfile(model string) *Profile {
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
		defaultEfforts:  anthropicDefaultEfforts,
		providerOpts:    anthropicProviderOpts(has1M),
		capabilities:    anthropicStyleCapabilities,
	})
	return &bp
}

// newGeminiProfile returns a *Profile for Google Gemini using the given
// model.
func newGeminiProfile(model string) *Profile {
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

// newMiniMaxProfile returns a *Profile for MiniMax using the given model.
func newMiniMaxProfile(model string) *Profile {
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

// newKimiAnthropicProfile returns a *Profile for the Kimi coding plan using the
// given model, talking to Kimi's Anthropic-compatible endpoint.
func newKimiAnthropicProfile(model string) *Profile {
	bp := buildBaseProfile(profileSpec{
		id:              "kimi-anthropic",
		behaviorTag:     providercfg.BehaviorTag("kimi-anthropic", ""),
		model:           model,
		parallel:        true,
		contextWindow:   262_144,
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

// newOpenRouterAnthropicProfile creates a profile that routes any OpenRouter-
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

// newOpenRouterAnthropicProfile returns a *Profile for the
// openrouter-anthropic provider using the given model, resolving the context
// window, reasoning effort levels, and web-search support from the embedded
// model catalog.
func newOpenRouterAnthropicProfile(model string) *Profile {
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
	// The "[1m]" suffix selects the 1M-context beta, just as on the direct
	// Anthropic profile. The GetModelInfo-based resolver above can't see it (the
	// suffix isn't a catalog key), so set the window AND the beta header
	// explicitly for a qualified/dated "[1m]" ref like
	// "anthropic/claude-opus-4-5-20251101[1m]" — otherwise Serf budgets 1M but
	// never requests it.
	has1M := strings.HasSuffix(model, anthropicSuffix1M)
	if has1M {
		contextWindow = 1_000_000
	}
	anthropicOpts := map[string]any{"max_tokens": 16384}
	if has1M {
		anthropicOpts["beta_headers"] = anthropicBeta1M
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
		providerOpts:    map[string]any{"anthropic": anthropicOpts},
		capabilities:    anthropicStyleCapabilities,
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
// lookup precedence used by newOpenAICompatProfile, returning the first
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

// newOpenAICompatProfile creates a profile for OpenAI-compatible providers
// (kimi, glm, openrouter, ollama, etc.). If contextWindow is 0, it's looked
// up from the embedded model catalog; if still unknown, defaults to 128K.
//
// The catalog lookup tries up to three forms in order, see
// resolveOpenAICompatCatalogModel for the precedence contract.
//
// The wire model name is always the bare value; only the catalog lookup
// is broadened.
func newOpenAICompatProfile(id, model string, contextWindow int) *Profile {
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
