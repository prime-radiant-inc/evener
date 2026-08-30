package provider

import (
	"maps"
	"strings"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// Profile is what the agent reads about the model it drives (spec §7.5):
// the registry's Resolved record plus the tool definitions, doc files, and
// per-session overrides that follow from its surface. Construct one with
// Resolve or FromResolved; the zero value is not usable.
type Profile struct {
	res      registry.Resolved
	registry *registry.Registry

	toolDefs      []llm.ToolDefinition
	toolNameMap   map[string]string // canonical → provider-specific
	docFiles      []string
	contextWindow int // WithContextWindow override; 0 means the row's
	cheapModel    string
	// cheapProvider routes auxiliary "side calls" (naming, summarization,
	// web_fetch Q&A) to a different instance than the main model. Empty means
	// the main model's instance. Set via WithCheapModel("instance/model").
	cheapProvider string
}

// FromResolved wraps a resolved record. r re-resolves for WithModel and
// CrossProviderRef; nil means the embedded registry.
func FromResolved(res registry.Resolved, r *registry.Registry) *Profile {
	if r == nil {
		r = EmbeddedRegistry()
	}
	p := &Profile{res: res, registry: r}
	p.docFiles, p.toolNameMap = surfaceConventions(res.Surface)
	p.toolDefs = toolDefinitionsForCapabilities(p.capabilities(), p.ReasoningEffortLevels())
	return p
}

// NewOpenAIProfile is the openai/<model> profile on the embedded registry:
// the fixture every session test starts from and CoreToolNames' input. It
// panics only when the embedded registry itself fails to load.
func NewOpenAIProfile(model string) *Profile {
	p, err := Resolve(EmbeddedRegistry(), "openai/"+strings.TrimSpace(model))
	if err != nil {
		panic("provider: NewOpenAIProfile: " + err.Error())
	}
	return p
}

// surfaceConventions are the trained-for vendor conventions (spec §7.5):
// the project doc files and the tool names a surface expects.
func surfaceConventions(surface string) (docFiles []string, toolNameMap map[string]string) {
	switch surface {
	case registry.SurfaceOpenAI:
		return []string{"AGENTS.md", ".codex/instructions.md"}, map[string]string{"shell": "exec_command", "grep": "grep_files", "glob": "find_files"}
	case registry.SurfaceAnthropic:
		return []string{"CLAUDE.md", "AGENTS.md"}, nil
	case registry.SurfaceGoogle:
		return []string{"GEMINI.md", "AGENTS.md"}, map[string]string{"shell": "run_shell_command", "grep": "grep_search", "list_dir": "list_directory"}
	default:
		return []string{"AGENTS.md"}, nil
	}
}

// capabilities is the surface's tool set; the web_search function tool is a
// google-protocol arrangement (spec §7.5) and rides only with it.
func (p *Profile) capabilities() []toolCapability {
	var caps []toolCapability
	switch p.res.Surface {
	case registry.SurfaceAnthropic:
		caps = anthropicStyleCapabilities
	case registry.SurfaceGoogle:
		caps = geminiStyleCapabilities
	default:
		caps = openAICodexCapabilities
	}
	googleWebSearch := p.res.Protocol == registry.ProtocolGoogle && registry.BoolValue(p.res.Caps.WebSearch)
	out := make([]toolCapability, 0, len(caps))
	for _, c := range caps {
		if c == capabilityWebSearch && !googleWebSearch {
			continue
		}
		out = append(out, c)
	}
	return out
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

// ID is the instance name; Model the requested model id.
func (p *Profile) ID() string { return p.res.Instance }

// Model is the model id the profile drives, as the caller asked for it.
func (p *Profile) Model() string { return p.res.ModelID }

// Resolved is the registry record the profile wraps.
func (p *Profile) Resolved() registry.Resolved { return p.res }

// Surface is the agent-facing vendor family the model was trained for; it,
// Protocol, and ProviderID are the three registry keys the agent branches on
// (spec §7.5).
func (p *Profile) Surface() string { return p.res.Surface }

// Protocol is the wire protocol the profile's instance speaks.
func (p *Profile) Protocol() string { return p.res.Protocol }

// ProviderID is the registry provider id behind the instance.
func (p *Profile) ProviderID() string { return p.res.ProviderID }

// ToolDefinitions returns the profile's tool schemas by their canonical names.
// Provider-specific renaming (via ToolNameMap) and the shared intent parameter
// are applied by the agent when advertising tools to the model, not here.
func (p *Profile) ToolDefinitions() []llm.ToolDefinition {
	return append([]llm.ToolDefinition{}, p.toolDefs...)
}

// ToolNameMap returns the canonical→provider-specific tool name mapping.
// Returns nil for surfaces that use canonical names (e.g. Anthropic).
func (p *Profile) ToolNameMap() map[string]string {
	if len(p.toolNameMap) == 0 {
		return nil
	}
	m := make(map[string]string, len(p.toolNameMap))
	maps.Copy(m, p.toolNameMap)
	return m
}

// ProjectDocFiles returns the project-doc filenames this surface loads from
// the working directory (e.g. CLAUDE.md, AGENTS.md), in priority order.
func (p *Profile) ProjectDocFiles() []string {
	return append([]string{}, p.docFiles...)
}

// SupportsParallelToolCalls reports whether the model may emit multiple tool
// calls in a single response. Every protocol the agent drives does.
func (p *Profile) SupportsParallelToolCalls() bool { return true }

// SupportsStreaming reports whether the model streams. Every protocol the
// agent drives does.
func (p *Profile) SupportsStreaming() bool { return true }

// DefaultCommandTimeoutMS is the default shell command timeout.
func (p *Profile) DefaultCommandTimeoutMS() int { return 120_000 }

// ContextWindowSize is the row's window, or 0 when unknown (spec §7.3): the
// context manager applies no compaction budget until a live listing or a
// user row supplies one.
func (p *Profile) ContextWindowSize() int {
	if p.contextWindow > 0 {
		return p.contextWindow
	}
	if p.res.Caps.ContextWindow != nil {
		return *p.res.Caps.ContextWindow
	}
	return 0
}

// MaxOutputTokens is the row's output cap, or 0 when the row has none — the
// protocol's own default then governs.
func (p *Profile) MaxOutputTokens() int {
	if p.res.Caps.MaxOutputTokens != nil {
		return *p.res.Caps.MaxOutputTokens
	}
	return 0
}

// SupportsReasoning is false only for an explicit reasoning = false row.
func (p *Profile) SupportsReasoning() bool { return !p.res.Caps.ReasoningDisabled() }

// ReasoningEffortLevels is the row's effort ladder; empty passes any
// requested effort through unchanged (spec §7.4).
func (p *Profile) ReasoningEffortLevels() []string {
	if p.res.Caps.ReasoningDisabled() {
		return nil
	}
	return cloneStringSlice(p.res.Caps.EffortValues)
}

// SupportsWebSearch reports whether the row serves provider-native web search.
func (p *Profile) SupportsWebSearch() bool { return registry.BoolValue(p.res.Caps.WebSearch) }

// KnowledgeCutoff is the model's training knowledge-cutoff date (YYYY-MM-DD),
// or "" when the row carries none.
func (p *Profile) KnowledgeCutoff() string { return registry.StringValue(p.res.Caps.KnowledgeCutoff) }

// Cost is the row's price per million tokens, or nil when unpriced.
func (p *Profile) Cost() *registry.Cost { return p.res.Caps.Cost }

// InputModalities lists what the model accepts ("text", "image", "pdf", …).
func (p *Profile) InputModalities() []string { return cloneStringSlice(p.res.Caps.InputModalities) }

// Warnings are the registry's notices for this reference (an uncatalogued
// model, an unresolved variable, a hidden provider).
func (p *Profile) Warnings() []string { return cloneStringSlice(p.res.Warnings) }

// ProviderOptions are the protocol extras the agent adds (spec §7.5):
// parallel tool calls on Responses and the safety settings on Gemini.
// Everything else a request needs is a capability the registry already
// carries, so the other protocols get nothing.
func (p *Profile) ProviderOptions() map[string]any {
	switch p.res.Protocol {
	case registry.ProtocolOpenAIResponses:
		return map[string]any{registry.ProtocolOpenAIResponses: map[string]any{"parallel_tool_calls": true}}
	case registry.ProtocolGoogle:
		return map[string]any{registry.ProtocolGoogle: map[string]any{"safetySettings": []map[string]any{
			{"category": "HARM_CATEGORY_HARASSMENT", "threshold": "BLOCK_ONLY_HIGH"},
			{"category": "HARM_CATEGORY_HATE_SPEECH", "threshold": "BLOCK_ONLY_HIGH"},
			{"category": "HARM_CATEGORY_SEXUALLY_EXPLICIT", "threshold": "BLOCK_ONLY_HIGH"},
			{"category": "HARM_CATEGORY_DANGEROUS_CONTENT", "threshold": "BLOCK_ONLY_HIGH"},
		}}}
	}
	return nil
}

// CheapModel is the configured cheap model, else the instance's curated or
// configured cheap_model, else the model itself.
func (p *Profile) CheapModel() string {
	if m := strings.TrimSpace(p.cheapModel); m != "" {
		return m
	}
	if p.res.CheapModel != "" {
		return p.res.CheapModel
	}
	return p.Model()
}

// ConfiguredCheapModel returns the auxiliary model explicitly set via
// WithCheapModel, or "" if none was configured. Unlike CheapModel it does not
// fall back to the instance's cheap_model, so callers can detect whether a
// cheap model was configured at all (e.g. to decide whether to run session
// naming).
func (p *Profile) ConfiguredCheapModel() string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(p.cheapModel)
}

// CheapProvider returns the instance name that auxiliary side calls should
// route to: the explicitly configured cross-instance cheap provider, or the
// main profile's own instance when none is set (the default).
func (p *Profile) CheapProvider() string {
	if p == nil {
		return ""
	}
	if cp := strings.TrimSpace(p.cheapProvider); cp != "" {
		return cp
	}
	return p.ID()
}

// CheapModelRef returns the (instance, model) pair for auxiliary side calls,
// resolving the instance via CheapProvider and the model via CheapModel. Sites
// that issue a cheap completion route on this pair so the cheap model can live
// on a different instance than the main model.
func (p *Profile) CheapModelRef() (provider, model string) {
	if p == nil {
		return "", ""
	}
	if model := p.ConfiguredCheapModel(); model != "" {
		return p.CheapProvider(), model
	}
	return p.ID(), p.Model()
}

// CheapModelRefString returns the configured cheap model as a WithCheapModel ref
// ("instance/model" when cross-instance, else the bare model), or "" when no
// cheap model is configured. It is the persistable form: feeding the result back
// to WithCheapModel reproduces the routing, so it survives evener resume. Unlike
// CheapModelRef it does NOT fall back to the instance's cheap_model — an empty
// result means "not configured", matching ConfiguredCheapModel.
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

// WithResolved returns a copy carrying a fresh record for the same instance
// (after a live listing was applied); the window override, the communicate
// schema, and the cheap-model routing stay. Rebuilding from the record is what
// resyncs the task_list effort enum to the new ladder.
func (p *Profile) WithResolved(res registry.Resolved) *Profile {
	if p == nil {
		return nil
	}
	next := FromResolved(res, p.registry)
	next.contextWindow = p.contextWindow
	return next.WithCommunicateOverridesFrom(p).withCheapModelFrom(p)
}

// CrossProviderRef reports whether ref ("<prefix>/<model>") names another
// instance: the prefix differs from this instance and this instance does not
// serve the whole ref as a model id — a namespaced id the instance serves
// (OpenRouter's "anthropic/claude-opus-5") stays on the instance. Such a ref
// is the session resolver's job, not WithModel's.
func (p *Profile) CrossProviderRef(ref string) bool {
	ref = strings.TrimSpace(ref)
	prefix, _, ok := strings.Cut(ref, "/")
	if !ok || strings.EqualFold(prefix, p.ID()) {
		return false
	}
	res, err := p.registry.Resolve(p.ID() + "/" + ref)
	return err != nil || res.Synthesized
}

// WithModel returns the profile for another model on the same instance,
// re-resolved so every cap follows the model; a redundant self-prefix is
// stripped, a cross-instance ref is kept verbatim for the session resolver,
// and an unresolvable id (the Codex allowlist) is kept verbatim so the
// membership check reports it.
func (p *Profile) WithModel(model string) *Profile {
	model = strings.TrimSpace(model)
	if model == "" {
		model = p.Model()
	}
	if prefix, rest, ok := strings.Cut(model, "/"); ok {
		switch {
		case strings.EqualFold(prefix, p.ID()):
			model = rest
		case p.CrossProviderRef(model):
			return p.withModelID(model)
		}
	}
	res, err := p.registry.Resolve(p.ID() + "/" + model)
	if err != nil {
		return p.withModelID(model)
	}
	next := FromResolved(res, p.registry)
	return next.WithCommunicateOverridesFrom(p).withCheapModelFrom(p)
}

// withModelID renames the model on a shallow clone, for a reference the
// registry cannot resolve on this instance: the caller that asked for it owns
// reporting why.
func (p *Profile) withModelID(model string) *Profile {
	clone := *p
	clone.res.ModelID, clone.res.WireID = model, model
	return &clone
}

// withCheapModelFrom carries the cheap-model routing (set via WithCheapModel)
// from original onto p. A re-resolve starts from the record alone, so it must
// restore them or side calls (naming, summarization, web-fetch) lose their
// configured cheap model.
func (p *Profile) withCheapModelFrom(original *Profile) *Profile {
	if p == nil || original == nil {
		return p
	}
	p.cheapModel = original.cheapModel
	p.cheapProvider = original.cheapProvider
	return p
}

// WithCommunicateOverridesFrom returns a copy of p whose communicate tool
// definition is replaced by original's, when original defines one.
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
		// A surface whose defaults don't include communicate (unusual for
		// Evener profiles but possible for custom callers); append it.
		defs = append(defs, *origCommunicate)
	}
	p.toolDefs = defs
	return p
}
