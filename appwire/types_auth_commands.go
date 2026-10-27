package appwire

import "encoding/json"

// AuthListResponse is the result of evener/auth/list.
type AuthListResponse struct {
	Providers []AuthStatusResponse `json:"providers"`
}

// AuthApiKeySetParams is the params for evener/auth/apiKey/set.
type AuthApiKeySetParams struct {
	Provider string `json:"provider"`
	Value    string `json:"value"`
	// ExpectedEndpointFingerprint is the endpoint this client showed the user
	// for Provider (InstanceEntry.endpointFingerprint). The hub refuses the
	// write when the name resolves to a different endpoint by the time it
	// lands: the client's own comparison reads a listing a concurrent change
	// can outdate, so only the hub can make the check and the write one step.
	// Empty asserts nothing, which is what a client that never saw an endpoint
	// sends.
	ExpectedEndpointFingerprint string `json:"expectedEndpointFingerprint,omitempty"`
	// OriginClientId is the client identity the hub echoes into the
	// evener/auth/updated broadcast this write triggers, so the originator can
	// recognize its own echo by id instead of by provider plus timing.
	// Optional: empty (an older build, the TUI) leaves the broadcast without
	// an id and consumers on the provider-plus-timing fallback.
	OriginClientId string `json:"originClientId,omitempty"`
}

// AuthApiKeyClearParams is the params for evener/auth/apiKey/clear.
type AuthApiKeyClearParams struct {
	Provider string `json:"provider"`
	// ExpectedEndpointFingerprint is the endpoint this client showed the user
	// for Provider (InstanceEntry.endpointFingerprint), checked the way
	// AuthApiKeySetParams's is. The clear was confirmed for the row the client
	// listed, so a name another client has re-pointed since must not have its
	// replacement instance's key removed. Empty asserts nothing.
	ExpectedEndpointFingerprint string `json:"expectedEndpointFingerprint,omitempty"`
	// OriginClientId is the client identity the hub echoes into the
	// evener/auth/updated broadcast this clear triggers, so the originator can
	// recognize its own echo by id instead of by provider plus timing.
	// Optional: empty (an older build, the TUI) leaves the broadcast without
	// an id and consumers on the provider-plus-timing fallback.
	OriginClientId string `json:"originClientId,omitempty"`
}

// ApiKeyConditionalSetParams is the params for
// evener/auth/apiKey/conditionalSet: the host-side conditional (compare-and-set)
// credential write (design §07 "credential push"). It replaces the racy
// read-evener/auth/status, classify, then evener/auth/apiKey/set pair: the host
// re-resolves the instance's credential source and effective-configuration
// revision inside one credentialWrite critical section and applies the write
// only when they still match what the client observed. Provider is the
// instance name, and the write lands in the host's own store under it.
type ApiKeyConditionalSetParams struct {
	Provider string `json:"provider"`
	Value    string `json:"value"`
	// ExpectedSource is the ActiveSource the client observed for Provider when
	// it prepared this write (AuthStatusResponse.ActiveSource /
	// InstanceEntry.ActiveSource). The host re-resolves the source under its
	// credential write lock and refuses a non-empty value that no longer
	// matches; empty asserts no source fence. The resolved source also decides
	// the classification (see ApiKeyConditionalSetResponse.Action).
	ExpectedSource string `json:"expectedSource,omitempty"`
	// ExpectedRevision is the ConfigRevision the client observed for Provider.
	// The host re-resolves it under the same lock and refuses a non-empty value
	// that no longer matches, so a write prepared against a configuration that
	// changed underneath it is never applied. Empty asserts no revision fence;
	// the source fence still applies.
	ExpectedRevision string `json:"expectedRevision,omitempty"`
	// OriginClientId is the client identity the hub echoes into the
	// evener/auth/updated broadcast a landed write triggers, so the originator
	// can recognize its own echo by id instead of by provider plus timing.
	// Optional: empty (an older build, the TUI) leaves the broadcast without an
	// id and consumers on the provider-plus-timing fallback.
	OriginClientId string `json:"originClientId,omitempty"`
}

// The Action values of ApiKeyConditionalSetResponse: what the host did with a
// conditional set. A "skipped" is a successful typed response, not a wire
// error — the host refused to write because the instance's live credential is
// one a pushed file-layer key must not shadow, and Reason says why.
const (
	ApiKeyConditionalSetActionAdded   = "added"
	ApiKeyConditionalSetActionUpdated = "updated"
	ApiKeyConditionalSetActionSkipped = "skipped"
)

// ApiKeyConditionalSetResponse is the result of
// evener/auth/apiKey/conditionalSet. Action is one of the
// ApiKeyConditionalSetAction* values, Reason is a human-readable explanation
// (chiefly for a skip), and Status is the instance's post-write
// AuthStatusResponse — the state the host resolved under the same lock, which a
// skip leaves unchanged.
type ApiKeyConditionalSetResponse struct {
	Action string             `json:"action"`
	Reason string             `json:"reason,omitempty"`
	Status AuthStatusResponse `json:"status"`
}

// AuthCredentialJsonSetParams is the params for evener/auth/credentialJson/set:
// a Google credential JSON (service-account key or application-default
// authorized_user file) for a gcp-adc instance.
type AuthCredentialJsonSetParams struct {
	Provider string `json:"provider"`
	Value    string `json:"value"`
	// ExpectedEndpointFingerprint is the endpoint this client showed the user
	// for Provider, checked the same way as AuthApiKeySetParams's.
	ExpectedEndpointFingerprint string `json:"expectedEndpointFingerprint,omitempty"`
	// OriginClientId is the client identity the hub echoes into the
	// evener/auth/updated broadcast this write triggers, so the originator can
	// recognize its own echo by id instead of by provider plus timing.
	// Optional: empty (an older build, the TUI) leaves the broadcast without
	// an id and consumers on the provider-plus-timing fallback.
	OriginClientId string `json:"originClientId,omitempty"`
}

// AuthDeviceStartParams is the params for evener/auth/device/start.
type AuthDeviceStartParams struct {
	Provider string `json:"provider"`
}

// AuthDeviceStartResponse carries the device code to display, or Fallback=true
// when the client doesn't support device-code and the caller should use the
// redirect/paste-back flow instead.
type AuthDeviceStartResponse struct {
	Provider        string `json:"provider"`
	FlowID          string `json:"flowId"`
	UserCode        string `json:"userCode"`
	VerificationURL string `json:"verificationUrl"`
	IntervalSeconds int    `json:"intervalSeconds"`
	// ExpiresInSeconds is how long the user code stays valid, so a client can
	// say when it expires instead of repeating the hub's own TTL. Optional:
	// zero (absent) leaves the client on its own wording.
	ExpiresInSeconds int  `json:"expiresInSeconds,omitempty"`
	Fallback         bool `json:"fallback,omitempty"`
}

// AuthDevicePollParams is the params for evener/auth/device/poll.
type AuthDevicePollParams struct {
	Provider string `json:"provider"`
	FlowID   string `json:"flowId"`
	// OriginClientId is the client identity the hub echoes into the
	// evener/auth/updated broadcast an authorized poll triggers, so the
	// originator can recognize its own echo by id instead of by provider plus
	// timing. Optional: empty (an older build, the TUI) leaves the broadcast
	// without an id and consumers on the provider-plus-timing fallback.
	OriginClientId string `json:"originClientId,omitempty"`
}

// AuthDevicePollResponse reports one poll attempt. State is "pending",
// "authorized", "expired", or "restart": the hub could not check the flow
// against the endpoint it was started for, so it discarded the flow and saved
// nothing, and the client starts a fresh device flow (evener/auth/device/start)
// against the endpoint the name resolves to now. Status is nil (the "status"
// key is absent from the wire) except when authorized.
type AuthDevicePollResponse struct {
	State  string              `json:"state"`
	Status *AuthStatusResponse `json:"status,omitempty"`
}

// InstanceEntry is one registry instance with its credential status
// (spec §11.3). ActiveSource and AuthModes speak the registry's vocabulary:
// a source is one of api_key, credential_headers, store, env:<VAR>, oauth,
// adc, or none. A credential value never appears here.
type InstanceEntry struct {
	Name string `json:"name"`
	// Base is the registry id an explicitly-named instance is built on;
	// empty when the instance name is itself the registry id.
	Base       string `json:"base,omitempty"`
	ProviderID string `json:"providerId"`
	Protocol   string `json:"protocol"`
	Surface    string `json:"surface,omitempty"`
	Auth       string `json:"auth"`
	BaseURL    string `json:"baseUrl,omitempty"`
	// EndpointFingerprint is a digest of the complete endpoint this instance
	// resolves, including what BaseURL deliberately leaves out (query
	// parameters and userinfo). A client compares it to notice that the
	// destination changed without the secret-bearing parts crossing the wire.
	EndpointFingerprint string            `json:"endpointFingerprint,omitempty"`
	Vars                map[string]string `json:"vars,omitempty"`
	// APIKeyEnv and CredentialHeader are the AUTHORED api_key_env (its first
	// entry) and credential header (as NAME=VALUE) from providers.toml, so
	// the sheet's form can prefill them. Never the registry's own defaults
	// for an implicit instance, and never a secret: the loader accepts a
	// hand-written literal that both authoring surfaces would refuse
	// (registry.CheckCredentialHeaderValue guards those, not the file). That
	// rule takes $VARIABLE references with at most one auth scheme word
	// ahead of them, and the entry omits any header it refuses.
	APIKeyEnv        string `json:"apiKeyEnv,omitempty"`
	CredentialHeader string `json:"credentialHeader,omitempty"`
	// Implicit is true for an instance with no authored entry in
	// providers.toml: a curated provider that exists because the environment
	// supplies it (an API-key variable, the ADC file) or because the user
	// filed a credential for it through the UI (a stored key, a signed-in
	// Codex record). It says nothing by itself about removal: an instance the
	// environment supplies comes back with it, while one holding the user's
	// credential is taken away by deleting that credential
	// (cmd/evener-hub/app_instances.go's environmentBacked).
	Implicit bool `json:"implicit"`
	// Hidden marks a provider with no resolvable base URL in this
	// environment (its *_BASE_URL variable is unset).
	Hidden         bool     `json:"hidden,omitempty"`
	IsDefault      bool     `json:"isDefault"`
	AuthModes      []string `json:"authModes,omitempty"`
	ActiveSource   string   `json:"activeSource"`
	HasStoredFile  bool     `json:"hasStoredFile,omitempty"`
	HasStoredOAuth bool     `json:"hasStoredOAuth"`
	EnvVar         string   `json:"envVar,omitempty"`
	// ShadowedEnvVar names an environment variable that is set but loses to
	// a higher-precedence credential (api_key, credential_headers, or
	// store, spec §10); empty when no such variable is set, including when
	// an env source is itself what resolves.
	ShadowedEnvVar string `json:"shadowedEnvVar,omitempty"`
	// RenameLeavesRow is true when renaming this instance leaves an instance
	// resolving under its old name, because the environment re-supplies what
	// the rename moves: the row is environment-backed as the removal refusal
	// computes it, or the old name is a curated provider id that re-derives
	// without the user's moved credential (a set variable, the ADC file, or a
	// keyless scheme). The hub computes it (renameLeavesRow) because a client
	// cannot see ADC availability or the curated set; the rename note keys on
	// it.
	RenameLeavesRow bool   `json:"renameLeavesRow,omitempty"`
	StoredEmail     string `json:"storedEmail,omitempty"`
	// ConfigRevision is the same effective credential-configuration revision
	// AuthStatusResponse.ConfigRevision carries for this instance (see its doc).
	// The remote credential push can capture it from an evener/instance/list
	// entry instead of a separate evener/auth/status read (the
	// implicit-provider fallback) and echo it as
	// ApiKeyConditionalSetParams.ExpectedRevision.
	ConfigRevision string `json:"configRevision,omitempty"`
	// CredentialRequired is false when this instance has no credential to
	// look for at all — auth = none or optional-bearer — so an absent
	// credential is not a missing one. It is never omitted: false is the
	// meaningful value, and a client reading an absent field as false would
	// call every instance optional.
	CredentialRequired bool `json:"credentialRequired"`
	// Warnings are the registry's own notes about this instance, chiefly
	// what is missing and how to supply it.
	Warnings []string `json:"warnings,omitempty"`
	// Models is the instance's known models with their effective
	// disabled state, for the sheet's per-model toggles: live ids plus
	// additive overlay/config rows when usable, otherwise static fallback
	// rows, with aliases included. Empty for an instance with no rows.
	Models []InstanceModelEntry `json:"models,omitempty"`
}

// InstanceModelEntry is one row of an instance's model inventory: the
// catalog id and whether the config layer disabled it, alias rows included.
// The Providers pane's instance sheet renders one toggle per row; every
// listed id names a row setModelDisabled can write, and a cross-provider
// alias toggles on this instance without touching the provider it names.
type InstanceModelEntry struct {
	ID       string `json:"id"`
	Disabled bool   `json:"disabled,omitempty"`
}

// ProviderDescriptor is a registry provider the add form can build on: its
// id and display name, the protocol and auth scheme it defaults to, and the
// variables its URL templates read. Vars maps a template placeholder name
// to the environment variable name it is fed by, so a typed override can be
// sent keyed by the name the registry actually substitutes. It is
// registry.Transport.VarsEnv restricted to the placeholders some URL
// template reads or a host rule consumes (Registry.TemplateVarsEnv): a
// vars_env entry nothing substitutes, such as a credential's own variable,
// gets no input. VarsEnv is the same environment-variable names alone,
// sorted. It stays a list because v3
// peers — a TUI built before Vars existed — decode it as one, and
// ProtocolVersion is compared exactly, so a wire shape cannot change under
// v3; new readers use Vars.
type ProviderDescriptor struct {
	ID        string            `json:"id"`
	Name      string            `json:"name,omitempty"`
	Protocol  string            `json:"protocol"`
	Auth      string            `json:"auth"`
	VarsEnv   []string          `json:"varsEnv,omitempty"`
	Vars      map[string]string `json:"vars,omitempty"`
	APIKeyEnv []string          `json:"apiKeyEnv,omitempty"`
	Implicit  bool              `json:"implicit"`
	AuthModes []string          `json:"authModes,omitempty"`
	// Setup is safe discovery metadata for an addressable implicit provider
	// or existing instance, not membership in InstanceListResponse.Instances.
	// Hidden implicit providers retain setup even before their destination is
	// configured; nil means the ID is not yet addressable as an instance.
	Setup *InstanceEntry `json:"setup,omitempty"`
}

// InstanceListResponse is the result of evener/instance/list. Diagnostics
// carries the providers.toml load error, the user-layer note, stray OAuth
// records and load warnings; WritesRefused says the hub has no registry to
// write against — the file could not be read, or none has loaded yet — so no
// instance may be written until that is fixed (spec §10).
type InstanceListResponse struct {
	Instances          []InstanceEntry      `json:"instances"`
	AvailableProviders []ProviderDescriptor `json:"availableProviders"`
	Diagnostics        []string             `json:"diagnostics,omitempty"`
	UserLayer          string               `json:"userLayer,omitempty"`
	WritesRefused      bool                 `json:"writesRefused,omitempty"`
}

// InstanceCreateParams is the params for evener/instance/create. APIKeyEnv
// is a variable name and CredentialHeader must reference a $VAR: secrets
// never cross this boundary (spec §11.2).
type InstanceCreateParams struct {
	Name             string            `json:"name"`
	Base             string            `json:"base"`
	BaseURL          string            `json:"baseUrl,omitempty"`
	Protocol         string            `json:"protocol,omitempty"`
	Surface          string            `json:"surface,omitempty"`
	Vars             map[string]string `json:"vars,omitempty"`
	APIKeyEnv        string            `json:"apiKeyEnv,omitempty"`
	CredentialHeader string            `json:"credentialHeader,omitempty"`
	// OriginClientId is the client identity the hub echoes into the
	// evener/auth/updated broadcast this create triggers, so the originator
	// recognizes its own echo by id instead of refetching as if another client
	// changed the list. Optional: empty (an older build, the TUI) leaves the
	// broadcast without an id.
	OriginClientId string `json:"originClientId,omitempty"`
}

// InstanceEditParams is the params for evener/instance/edit. Editing an
// implicit instance writes a shadowing entry carrying only these fields
// (spec §11.3). This wire shape is part of evener-appwire-v4; fields carried
// forward retain their established meanings, and v4 peers negotiate this
// shape before using it.
//
// EMPTY MEANS UNCHANGED, not "clear", for BaseURL, Protocol and Surface: an
// empty value leaves the stored one alone. That preserves the pre-v4 field
// semantics (#711) — BaseURL cannot be emptied to mean "clear" without
// changing what a v3 `baseUrl: ""` meant to a peer before the v4 upgrade.
//
// ClearBaseURL is the v4 way to reach a clear: when true, it drops the
// authored base_url override and goes back to the registry default, lifting
// spec §10's credential-inheritance stop, which keys on a literal base_url.
// BaseURL and ClearBaseURL are never both meaningful in the same request: send
// one or the other.
//
// The rename and credential fields obey the same rule. NewName renames
// the instance (empty means unchanged). APIKeyEnv/ClearAPIKeyEnv and
// CredentialHeader/ClearCredentialHeader set or drop the authored
// api_key_env and credential_headers; CredentialHeader is NAME=VALUE with a
// $VAR value, exactly as InstanceCreateParams takes it. ClearProtocol and
// ClearSurface drop an authored protocol or surface so the base provider's
// value applies again, the same shape as ClearBaseURL. A Vars entry
// whose value is empty DELETES that variable: a blank var never meant
// anything, so the empty value is free to mean "remove". Each set/clear
// pair follows BaseURL/ClearBaseURL: never both meaningful in one request.
type InstanceEditParams struct {
	Name                  string            `json:"name"`
	NewName               string            `json:"newName,omitempty"`
	BaseURL               string            `json:"baseUrl,omitempty"`
	ClearBaseURL          bool              `json:"clearBaseUrl,omitempty"`
	Protocol              string            `json:"protocol,omitempty"`
	ClearProtocol         bool              `json:"clearProtocol,omitempty"`
	Surface               string            `json:"surface,omitempty"`
	ClearSurface          bool              `json:"clearSurface,omitempty"`
	Vars                  map[string]string `json:"vars,omitempty"`
	APIKeyEnv             string            `json:"apiKeyEnv,omitempty"`
	ClearAPIKeyEnv        bool              `json:"clearApiKeyEnv,omitempty"`
	CredentialHeader      string            `json:"credentialHeader,omitempty"`
	ClearCredentialHeader bool              `json:"clearCredentialHeader,omitempty"`
	// ExpectedEndpointFingerprint is the endpoint this client showed the user
	// for Name (InstanceEntry.endpointFingerprint), checked the way
	// InstanceRemoveParams's is. The edit is applied to the row the client
	// listed, so a name another client has re-pointed since - or replaced with a
	// different instance - must not have its replacement edited or renamed.
	// Empty asserts nothing.
	ExpectedEndpointFingerprint string `json:"expectedEndpointFingerprint,omitempty"`
	// OriginClientId is the client identity the hub echoes into the
	// evener/auth/updated broadcast this edit triggers, so the originator
	// recognizes its own echo by id instead of refetching as if another client
	// changed the list. Optional: empty (an older build, the TUI) leaves the
	// broadcast without an id.
	OriginClientId string `json:"originClientId,omitempty"`
}

// InstanceRemoveParams is the params for evener/instance/remove.
type InstanceRemoveParams struct {
	Name string `json:"name"`
	// ExpectedEndpointFingerprint is the endpoint this client showed the user
	// for Name (InstanceEntry.endpointFingerprint), checked the way
	// AuthApiKeySetParams's is. The removal was confirmed for the row the
	// client listed, so a name another client has re-pointed since must not
	// have its replacement instance removed. Empty asserts nothing.
	ExpectedEndpointFingerprint string `json:"expectedEndpointFingerprint,omitempty"`
	// OriginClientId is the client identity the hub echoes into the
	// evener/auth/updated broadcast this removal triggers, so the originator
	// recognizes its own echo by id instead of refetching as if another client
	// changed the list. Optional: empty (an older build, the TUI) leaves the
	// broadcast without an id.
	OriginClientId string `json:"originClientId,omitempty"`
}

// InstanceSetDefaultParams is the params for evener/instance/setDefault.
type InstanceSetDefaultParams struct {
	Name string `json:"name"`
	// OriginClientId is the client identity the hub echoes into the
	// evener/auth/updated broadcast this change triggers, so the originator
	// recognizes its own echo by id instead of refetching as if another client
	// changed the list. Optional: empty (an older build, the TUI) leaves the
	// broadcast without an id.
	OriginClientId string `json:"originClientId,omitempty"`
}

// InstanceRefreshModelsParams is the params for
// evener/instance/refreshModels: fetch the instance's live listing, then
// answer with its live-authoritative or static-fallback model list.
type InstanceRefreshModelsParams struct {
	Name string `json:"name"`
	// OriginClientId is the client identity the hub echoes into the
	// evener/auth/updated broadcast this refresh triggers, so the originator
	// recognizes its own echo by id instead of refetching as if another client
	// changed the list. Optional: empty (an older build, the TUI) leaves the
	// broadcast without an id.
	OriginClientId string `json:"originClientId,omitempty"`
}

// InstanceSetModelDisabledParams is the params for
// evener/instance/setModelDisabled. It writes an explicit disabled bool on
// the named row (never deletes it), so the choice survives catalog
// refreshes. A glob id is refused: the sheet toggles exact rows only.
type InstanceSetModelDisabledParams struct {
	Name     string `json:"name"`
	Model    string `json:"model"`
	Disabled bool   `json:"disabled"`
	// OriginClientId is the client identity the hub echoes into the
	// evener/auth/updated broadcast this toggle triggers, so the originator
	// recognizes its own echo by id instead of refetching as if another client
	// changed the list. Optional: empty (an older build, the TUI) leaves the
	// broadcast without an id.
	OriginClientId string `json:"originClientId,omitempty"`
}

// CommandDescriptor describes one slash command — plugin-provided or
// evener-wide — for catalog/autocomplete display. Name is unqualified;
// PluginName disambiguates when more than one plugin defines the same command
// name.
type CommandDescriptor struct {
	Name         string `json:"name"`
	PluginName   string `json:"pluginName,omitempty"`
	Description  string `json:"description,omitempty"`
	ArgumentHint string `json:"argumentHint,omitempty"`
	// Source is "plugin", "user", or "project". "project" is returned by the
	// spawn-scoped catalog (project commands are cwd-dependent); it never
	// appears in the hub-wide catalog.
	Source string `json:"source,omitempty"`
}

// CommandListResponse is the result of evener/command/list.
type CommandListResponse struct {
	Commands []CommandDescriptor `json:"commands"`
}

// LaunchConfigLayer is the wire-level partial layer (every field optional;
// pointer-typed scalars so "not set" is distinguishable from zero).
type LaunchConfigLayer struct {
	Schema                      *int              `json:"schema,omitempty"`
	Model                       string            `json:"model,omitempty"`
	FastCheapModel              string            `json:"fastCheapModel,omitempty"`
	Agent                       string            `json:"agent,omitempty"`
	ReasoningEffort             string            `json:"reasoningEffort,omitempty"`
	ContextStrategy             string            `json:"contextStrategy,omitempty"`
	OpenAIResponsesContinuation string            `json:"openAIResponsesContinuation,omitempty"` //nolint:tagliatelle // codex wire spells the AI/ATIF initialisms all-caps
	ProviderIdleTimeout         string            `json:"providerIdleTimeout,omitempty"`
	Sandbox                     string            `json:"sandbox,omitempty"`
	SandboxNet                  *bool             `json:"sandboxNet,omitempty"`
	MaxRounds                   *int              `json:"maxRounds,omitempty"`
	MaxSubagentDepth            *int              `json:"maxSubagentDepth,omitempty"`
	MaxConcurrentDelegateTurns  *int              `json:"maxConcurrentDelegateTurns,omitempty"`
	MaxRetainedTerminal         *int              `json:"maxRetainedTerminal,omitempty"`
	NonInteractive              *bool             `json:"nonInteractive,omitempty"`
	AppReplaySize               *int              `json:"appReplaySize,omitempty"`
	SkillsDirs                  []string          `json:"skillsDirs,omitempty"`
	PluginDirs                  []string          `json:"pluginDirs,omitempty"`
	MCPConfigs                  []string          `json:"mcpConfigs,omitempty"`
	SystemPromptMode            string            `json:"systemPromptMode,omitempty"`
	SystemPromptFile            string            `json:"systemPromptFile,omitempty"`
	SystemPromptText            string            `json:"systemPromptText,omitempty"`
	SystemPromptAppendMode      string            `json:"systemPromptAppendMode,omitempty"`
	SystemPromptAppendFile      string            `json:"systemPromptAppendFile,omitempty"`
	SystemPromptAppendText      string            `json:"systemPromptAppendText,omitempty"`
	SystemPromptAppend          []string          `json:"systemPromptAppend,omitempty"`
	ModelFallbacks              []string          `json:"modelFallbacks,omitempty"`
	EnabledPlugins              *[]string         `json:"enabledPlugins,omitempty"`
	MCPs                        []MCPServerSpec   `json:"mcps,omitempty"`
	Env                         map[string]string `json:"env,omitempty"`
	Verbose                     *bool             `json:"verbose,omitempty"`
	APILog                      *bool             `json:"apiLog,omitempty"`
	TraceFile                   string            `json:"traceFile,omitempty"`
	CPUProfile                  string            `json:"cpuProfile,omitempty"`
	ExportATIFPath              string            `json:"exportATIFPath,omitempty"`            //nolint:tagliatelle // codex wire spells the AI/ATIF initialisms all-caps
	ExportATIFProviderHandles   string            `json:"exportATIFProviderHandles,omitempty"` //nolint:tagliatelle // codex wire spells the AI/ATIF initialisms all-caps
}

func (l LaunchConfigLayer) MarshalJSON() ([]byte, error) {
	type alias LaunchConfigLayer
	a := alias(l)
	a.ModelFallbacks = nil
	raw, err := marshalLaunchConfig(a)
	if err != nil {
		return nil, err
	}
	if l.ModelFallbacks == nil {
		return raw, nil
	}
	var obj map[string]json.RawMessage
	if err := unmarshalLaunchConfig(raw, &obj); err != nil {
		return nil, err
	}
	modelFallbacks, err := marshalModelFallbacks(l.ModelFallbacks)
	if err != nil {
		return nil, err
	}
	obj["modelFallbacks"] = modelFallbacks
	return marshalLaunchConfig(obj)
}

var (
	marshalLaunchConfig   = json.Marshal
	unmarshalLaunchConfig = json.Unmarshal
	marshalModelFallbacks = json.Marshal
)

// MCPServerSpec mirrors launchconfig.MCPServerSpec on the wire.
type MCPServerSpec struct {
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

// LaunchConfigResolved is the wire representation of launchconfig.Resolved.
type LaunchConfigResolved struct {
	Effective   LaunchConfigLayer            `json:"effective"`
	Layers      map[string]LaunchConfigLayer `json:"layers"`
	Provenance  map[string]string            `json:"provenance"`
	Repo        *RepoLaunchConfigStatus      `json:"repo,omitempty"`
	Diagnostics []LaunchConfigDiagnostic     `json:"diagnostics,omitempty"`
}

type RepoLaunchConfigStatus struct {
	Path    string `json:"path"`
	Hash    string `json:"hash,omitempty"`
	Trust   string `json:"trust"`
	Preview string `json:"preview,omitempty"`
}

type LaunchConfigDiagnostic struct {
	Layer   string `json:"layer"`
	Field   string `json:"field"`
	Message string `json:"message"`
}

type LaunchConfigResolveParams struct {
	CWD             string             `json:"cwd"`
	LaunchOverrides *LaunchConfigLayer `json:"launchOverrides,omitempty"`
}

type LaunchConfigGetLayerParams struct {
	CWD   string `json:"cwd"`
	Layer string `json:"layer"` // "global" | "project"
}

type LaunchConfigSetLayerParams struct {
	CWD    string            `json:"cwd"`
	Layer  string            `json:"layer"`
	Config LaunchConfigLayer `json:"config"`
}

type LaunchConfigTrustRepoParams struct {
	CWD  string `json:"cwd"`
	Hash string `json:"hash"`
}

// PluginCheckNowResponse is the result of evener/plugin/checkNow: it runs one
// auto-upgrade daemon pass (refresh every marketplace, then upgrade every
// autoUpgrade-enabled plugin) on demand and reports what happened. Updated
// holds "<plugin>@<marketplace>" refs actually upgraded (no-ops omitted);
// Errors holds any per-marketplace/per-plugin failures — failures are
// isolated and never fail the request itself.
type PluginCheckNowResponse struct {
	Updated []string `json:"updated,omitempty"`
	Errors  []string `json:"errors,omitempty"`
}

// PluginPreviewParams requests the launch plugin inventory for a working
// directory and optional per-launch overrides. Preview starts no session and
// runs no plugin code; for a requested bundled plugin it readies the same
// store a launch publishes into, staging and removing a marked copy, so it
// fails wherever the launch it describes would. Readying that store creates
// the bundled directory under the plugin root when it is missing, and the
// directory stays behind — with the lock file the cache keeps in it — once the
// staged copy is removed; a destination holding content the running binary did
// not publish is reported as the conflict a launch would set aside, and left
// exactly where it is.
type PluginPreviewParams struct {
	CWD             string             `json:"cwd"`
	LaunchOverrides *LaunchConfigLayer `json:"launchOverrides,omitempty"`
}

// SpawnSlashCatalogParams requests the slash catalog a spawn with these
// inputs would load. Field names and shapes match ThreadStartParams exactly:
// when this call and a thread/start agree on all three, the menu shows what
// that start would load. Model, effort, access mode, and prompt text do not
// affect the inventory and are deliberately absent.
type SpawnSlashCatalogParams struct {
	CWD             string             `json:"cwd"`
	Harness         string             `json:"harness,omitempty"`
	LaunchOverrides *LaunchConfigLayer `json:"launchOverrides,omitempty"`
}

// SpawnSlashCatalogResponse is the pre-session slash inventory: the commands
// and skills a session started with the params would offer. Row shapes reuse
// CommandDescriptor and EvenerSkillInfo verbatim so the web composer merges
// them with mergeSlashCommands unchanged.
type SpawnSlashCatalogResponse struct {
	Commands []CommandDescriptor `json:"commands"`
	Skills   []EvenerSkillInfo   `json:"skills,omitempty"`
}

// PluginPreviewResponse is the launch plugin inventory and structured
// diagnostics returned by evener/plugin/preview.
type PluginPreviewResponse struct {
	Plugins         []PluginLaunchCandidate `json:"plugins"`
	Diagnostics     []PluginDiagnostic      `json:"diagnostics,omitempty"`
	SelectionErrors []PluginSelectionError  `json:"selectionErrors,omitempty"`
}

type PluginLaunchCandidate struct {
	Name         string `json:"name"`
	Version      string `json:"version,omitempty"`
	Description  string `json:"description,omitempty"`
	Source       string `json:"source"`
	Marketplace  string `json:"marketplace,omitempty"`
	Path         string `json:"path,omitempty"`
	Selected     bool   `json:"selected"`
	SkillCount   int    `json:"skillCount"`
	AgentCount   int    `json:"agentCount"`
	CommandCount int    `json:"commandCount"`
	HookCount    int    `json:"hookCount"`
	MCPCount     int    `json:"mcpCount"`
}

type PluginDiagnostic struct {
	Name    string `json:"name,omitempty"`
	Path    string `json:"path,omitempty"`
	Source  string `json:"source,omitempty"`
	Message string `json:"message"`
}

type PluginSelectionError struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// MarketplaceSourceInput is the wire shape of a marketplace source. Kind
// selects which of Repo/URL/Path applies: "github" (Repo, e.g. "owner/repo"),
// "url" (URL, a git remote), "directory" (Path, referenced in place, no
// clone), or "git-subdir" (URL+Path, a sparse clone of one subdirectory).
// Ref/Sha optionally pin a git-backed source to a branch/tag or commit.
type MarketplaceSourceInput struct {
	Kind string `json:"kind"`
	Repo string `json:"repo,omitempty"`
	URL  string `json:"url,omitempty"`
	Path string `json:"path,omitempty"`
	Ref  string `json:"ref,omitempty"`
	Sha  string `json:"sha,omitempty"`
}

// MarketplaceEntry is the wire representation of one registered marketplace.
type MarketplaceEntry struct {
	Name            string                 `json:"name"`
	Source          MarketplaceSourceInput `json:"source"`
	InstallLocation string                 `json:"installLocation,omitempty"`
	LastUpdated     int64                  `json:"lastUpdated"`
}

// MarketplaceListResponse is the result of evener/marketplace/list. Every
// marketplace mutation (add/edit/remove/refresh) also returns this, so a
// client can re-render from the response without a separate list round-trip.
type MarketplaceListResponse struct {
	Marketplaces []MarketplaceEntry `json:"marketplaces"`
}

// MarketplaceUnregisteredCloneRemainsData is the WireError.Data payload for
// ErrorMarketplaceUnregisteredCloneRemains: the removal APPLIED (Applied
// carries the updated marketplace list, with the target already gone) but
// removing its clone from disk failed. Clients should reconcile from Applied
// instead of treating the removal as rejected - the same rule
// KeybindingsPostRenameData carries for keybindings. Re-listing to build
// Applied is itself a fresh read that can fail on its own account, unrelated
// to the removal that already landed; when it does, AppliedUnavailable is
// true and Applied is the zero value - a client must not read that as "every
// marketplace gone" and must not retry the removal as a fresh attempt either,
// since it already applied.
type MarketplaceUnregisteredCloneRemainsData struct {
	EvenerErrorInfo    ErrorInfo               `json:"evenerErrorInfo"`
	Applied            MarketplaceListResponse `json:"applied"`
	AppliedUnavailable bool                    `json:"appliedUnavailable,omitempty"`
}

// MarketplaceRemoveAppliedData is the WireError.Data payload for
// ErrorMarketplaceRemoveApplied: the removal APPLIED (the unregister save
// landed and the clone cleanup that follows it completed) but the fresh read
// that would carry the updated list to the caller failed. AppliedUnavailable
// is true on this path - the failed read is the whole reason this outcome
// exists - so a consumer reconciling the removal must not read the missing
// list as "every marketplace gone", and must not retry: the removal already
// applied, so a retry finds ErrMarketplaceNotFound. Applied is deliberately
// absent (unlike MarketplaceUnregisteredCloneRemainsData, whose read can
// succeed): no snapshot was read, and the wire carries no substitute for one.
// The consumer binding is deliberately deferred to the marketplace
// reconciliation successors (#1954 SDK, #1960 web).
type MarketplaceRemoveAppliedData struct {
	EvenerErrorInfo    ErrorInfo `json:"evenerErrorInfo"`
	AppliedUnavailable bool      `json:"appliedUnavailable,omitempty"`
}

// MarketplaceAddParams is the params for evener/marketplace/add. Name is
// optional; when empty, the marketplace manifest's own name is used.
type MarketplaceAddParams struct {
	Name   string                 `json:"name,omitempty"`
	Source MarketplaceSourceInput `json:"source"`
}

// MarketplaceEditParams is the params for evener/marketplace/edit (spec
// 2026-09-07 §3). NewName renames the registered marketplace (empty means
// unchanged); Source replaces its source and re-fetches it (absent means
// unchanged). Installed plugins are unaffected beyond being re-keyed under
// the new name.
type MarketplaceEditParams struct {
	Name    string                  `json:"name"`
	NewName string                  `json:"newName,omitempty"`
	Source  *MarketplaceSourceInput `json:"source,omitempty"`
}

// MarketplaceNameParams identifies one registered marketplace by name — the
// params shape for evener/marketplace/remove and evener/marketplace/refresh.
type MarketplaceNameParams struct {
	Name string `json:"name"`
}

// MarketplaceCatalogPlugin is one plugin entry parsed from a marketplace's
// catalog (.claude-plugin/marketplace.json), as returned by
// evener/marketplace/browse.
type MarketplaceCatalogPlugin struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Category    string `json:"category,omitempty"`
	Homepage    string `json:"homepage,omitempty"`
	Author      string `json:"author,omitempty"`
}

// MarketplaceBrowseParams is the params for evener/marketplace/browse.
type MarketplaceBrowseParams struct {
	Name string `json:"name"`
}

// MarketplaceBrowseResponse is the result of evener/marketplace/browse: the
// marketplace's catalog metadata plus its plugin list.
type MarketplaceBrowseResponse struct {
	Name        string                     `json:"name"`
	Description string                     `json:"description,omitempty"`
	Plugins     []MarketplaceCatalogPlugin `json:"plugins"`
}

// PluginEntry is the wire representation of one installed plugin.
type PluginEntry struct {
	Plugin       string `json:"plugin"`
	Marketplace  string `json:"marketplace"`
	Version      string `json:"version"`
	Enabled      bool   `json:"enabled"`
	AutoUpgrade  bool   `json:"autoUpgrade"`
	Broken       bool   `json:"broken"`
	InstallPath  string `json:"installPath"`
	GitCommitSha string `json:"gitCommitSha,omitempty"`
	InstalledAt  int64  `json:"installedAt"`
	LastUpdated  int64  `json:"lastUpdated"`
	// UpdateAvailable is true when the last evener/plugin/checkUpdates found a
	// newer version: a newer remote commit for a git-backed plugin, or new
	// contents in its marketplace, which the check refreshes first, for one
	// stored in its marketplace's own repo. Absent means no known update: no check has run,
	// the check found the plugin current or could not read its source, or the
	// plugin is used in place from a directory. Clients offer Upgrade only when
	// it is true.
	UpdateAvailable bool `json:"updateAvailable,omitempty"`
}

// PluginListResponse is the result of evener/plugin/list. Every plugin
// mutation (install/upgrade/remove/enable/disable/setAutoUpgrade) also
// returns this, so a client can re-render from the response without a
// separate list round-trip.
type PluginListResponse struct {
	Plugins []PluginEntry `json:"plugins"`
}

// PluginRefParams identifies one plugin by its registry key (plugin name +
// marketplace name) — the params shape for evener/plugin/install (naming the
// catalog entry to install), and evener/plugin/{upgrade,remove,enable,disable}
// (naming the already-installed entry to act on).
type PluginRefParams struct {
	Plugin      string `json:"plugin"`
	Marketplace string `json:"marketplace"`
}

// PluginSetAutoUpgradeParams is the params for evener/plugin/setAutoUpgrade.
type PluginSetAutoUpgradeParams struct {
	Plugin      string `json:"plugin"`
	Marketplace string `json:"marketplace"`
	AutoUpgrade bool   `json:"autoUpgrade"`
}

// SettingsOverviewResponse is the result of evener/settings/overview: the field
// bag behind five settings sections whose only data path today is Go-template
// variables rendered server-side — General, Hub, Storage, Agents, and the
// probed half of MCP servers (cmd/evener-hub/templates/partials/settings/
// {general,hub,storage,agents,mcp}.html) — replacing cmd/evener-hub/
// web_settings.go's settingsData for exactly those five. Every field is sourced
// from the same computation the legacy template used; see each sub-type's doc
// comment for the exact web_settings.go citation. A field the legacy template
// never rendered is left off rather than invented — also noted on the sub-type
// that would otherwise carry it.
//
// The other ten settings sections (providers/credentials, evener launch,
// in-repo trust, per-project override, marketplaces/plugins, plugin/skill dirs,
// the MCP config editable half, theme, transcript, display, notifications) are
// out of scope: they already have their own wire methods or land on a different
// task's new store. Nothing here is per-project.
type SettingsOverviewResponse struct {
	Hub           *SettingsHubOverview     `json:"hub,omitempty"`
	Storage       *SettingsStorageOverview `json:"storage,omitempty"`
	Agents        []SettingsAgentEntry     `json:"agents,omitempty"`
	McpDiscovered *SettingsMCPOverview     `json:"mcpDiscovered,omitempty"`
}

// SettingsHubOverview is the Settings → General / Settings → Hub section
// (cmd/evener-hub/templates/partials/settings/{general,hub}.html). Fields
// mirror cmd/evener-hub/web_settings.go's renderSettingsPartial settingsData
// construction. General.html's "State dir" row is not a field here — see
// SettingsStorageOverview.StateDir, which the frontend reads for it instead.
type SettingsHubOverview struct {
	// Version is the running hub's version string.
	// Source: web_settings.go settingsData.HubVersion (the package Version constant).
	Version string `json:"version,omitempty"`
	// Commit is the git commit the binary was built from; empty in dev builds.
	// Source: web_settings.go settingsData.HubCommit (buildinfo.GitSHA).
	Commit string `json:"commit,omitempty"`
	// BuildChannel is buildinfo.BuildChannel(): release, snapshot, or dev.
	BuildChannel string `json:"buildChannel,omitempty"`
	// ListenAddr is the hub HTTP server's bind address.
	// Source: web_settings.go settingsData.HubAddr (cfg.HubAddr).
	ListenAddr string `json:"listenAddr,omitempty"`
	// RunDir is the per-PID rendezvous directory the hub watches for live daemons.
	// Source: web_settings.go settingsData.RunDir (cfg.RunDir).
	RunDir string `json:"runDir,omitempty"`
	// SpawnTimeout is how long the hub waits for a daemon to report ready after
	// spawn, rendered as a Go duration string. Source: cfg.SpawnTimeout
	// (hub.toml spawn_timeout).
	SpawnTimeout string `json:"spawnTimeout,omitempty"`
	// BearerTokenAge is a human-readable age of the hub's auth-token file (e.g.
	// "created 3d ago" / "just now"), empty if unavailable.
	// Source: web_settings.go settingsData.BearerTokenAge (fileAgeHuman over
	// hubedge.TokenFileName under HubStateRoot).
	BearerTokenAge string `json:"bearerTokenAge,omitempty"`
	// PastIndex is nil only when no past-session index is configured
	// (cfg.Past == nil) — e.g. a minimal/test hub config.
	PastIndex *SettingsPastIndexOverview `json:"pastIndex,omitempty"`
	// DaemonIdleTimeoutMillis is the Hub's configured idle-retirement deadline
	// for spawned daemons, in integer milliseconds; zero means automatic
	// retirement is disabled. No omitempty: zero is a real configured state,
	// not "unknown". Source: cfg.DaemonIdleTimeout (hub.toml
	// daemon_idle_timeout, default 1h).
	DaemonIdleTimeoutMillis int64 `json:"daemonIdleTimeoutMillis"`
}

// SettingsPastIndexOverview describes the past-session SQLite index. Settings
// → General renders Path/Size/PerPage; Settings → Storage renders Path/Size/
// Count — both from this same object (the frontend reads hub.pastIndex for
// both pages; the value is not duplicated under Storage).
// Source: web_settings.go settingsData.PastIndexPath/PastIndexSize/
// PastPerPage/PastCount.
type SettingsPastIndexOverview struct {
	// Path is the past-index SQLite file path, tilde-shortened against $HOME.
	// Source: web_settings.go tildeHome(cfg.PastIndexPath).
	Path string `json:"path,omitempty"`
	// Size is a human-readable file size (e.g. "48 MB"), empty if the file
	// does not exist yet. Source: web_settings.go fileSizeHuman(cfg.PastIndexPath).
	Size string `json:"size,omitempty"`
	// PerPage is the configured /past results-per-page.
	// Source: web_settings.go settingsData.PastPerPage (cfg.PastPerPage).
	PerPage int `json:"perPage,omitempty"`
	// Count is the total number of indexed session metas, all-time — NOT a
	// count of currently-live/running sessions. The legacy storage.html
	// template's own copy calls this "currently tracking N sessions", which is
	// this same all-time indexed total. A genuine live-daemon count exists
	// (cfg.Roster) but is intentionally not surfaced here: no legacy settings
	// template ever rendered one.
	// Source: web_settings.go settingsData.PastCount (len(cfg.Past.AllMetas())).
	Count int `json:"count,omitempty"`
}

// SettingsStorageOverview is the Settings → Storage section (cmd/evener-hub/
// templates/partials/settings/storage.html). RunDir and the past-index
// path/size/count that storage.html also renders are not duplicated here —
// see SettingsHubOverview / SettingsPastIndexOverview, which the frontend
// reads for those (single source of truth; both live in the one overview
// response already).
type SettingsStorageOverview struct {
	// StateDir is the root directory for hub state: auth token, credentials,
	// and project sub-directories.
	// Source: web_settings.go settingsData.StateDir (cfg.StateDir).
	StateDir string `json:"stateDir,omitempty"`
}

// SettingsAgentEntry is one row in Settings → Agents (cmd/evener-hub/templates/
// partials/settings/agents.html) — today always exactly the three built-in
// agent names compiled into the binary (defaultPersona.txt etc.).
// Source: web_settings.go renderSettingsPartial's agentNames/agents
// construction.
type SettingsAgentEntry struct {
	Name string `json:"name"`
	// EditPath is an editor:// deep link to the agent's definition file. Always
	// empty today: built-in agents have no on-disk file to open. Kept for
	// shape parity with web_settings.go's agentDisplay.EditPath in case a
	// future on-disk agent source populates it.
	EditPath string `json:"editPath,omitempty"`
}

// SettingsMCPServerEntry is one probed MCP server row in Settings → MCP
// servers' "Discovered servers" list (cmd/evener-hub/templates/partials/
// settings/mcp.html) — the probed/read-only half; the editable half (MCP
// config file list, inline server CRUD) rides the existing launch-config
// wire (evener/launch/getLayer + evener/launch/setLayer), not this method.
// Source: web_settings.go discoverMCPsForSettings's mcpDisplay, itself
// sourced from agent/mcpprobe.Result. Command, Args, Tools, Agents, and
// EditPath exist on mcpDisplay but are never rendered by mcp.html's
// discovered-servers block, so they are omitted here too.
type SettingsMCPServerEntry struct {
	Name      string `json:"name"`
	Transport string `json:"transport,omitempty"`
	Status    string `json:"status,omitempty"`
	Error     string `json:"error,omitempty"`
}

// SettingsMCPOverview is the Settings → MCP servers section's probed half.
// Source: web_settings.go mcpConfigPathForSettings + discoverMCPsForSettings.
// A missing MCP config file is the empty state (Servers empty, Error ""),
// matching discoverMCPsForSettings; Error is populated only on a real parse
// failure (e.g. malformed mcp.json), mirroring settingsData.McpsError.
//
// Each server probe (agent/mcpprobe.Probe) runs under its own bounded
// per-server timeout in parallel with the others, so this section's total
// latency stays bounded regardless of server count — see mcpprobe's package
// doc for the exact bound; this handler adds no further timeout on top of it.
type SettingsMCPOverview struct {
	Servers []SettingsMCPServerEntry `json:"servers,omitempty"`
	Error   string                   `json:"error,omitempty"`
}
