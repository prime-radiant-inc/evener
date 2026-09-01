package registry

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// instance is a usable named provider (spec §5.1).
type instance struct {
	name     string
	rec      *record
	implicit bool
	rank     int
}

// Instance is the listing view of an instance (spec §5.1, §11.2).
type Instance struct {
	Name             string            `json:"name"`
	ProviderID       string            `json:"provider_id,omitempty"`
	Base             string            `json:"base,omitempty"`
	Protocol         string            `json:"protocol"`
	Surface          string            `json:"surface,omitempty"`
	Auth             string            `json:"auth"`
	BaseURL          string            `json:"base_url,omitempty"`
	Vars             map[string]string `json:"vars,omitempty"`
	DefaultModel     string            `json:"default_model,omitempty"`
	Implicit         bool              `json:"implicit"`
	Hidden           bool              `json:"hidden,omitempty"`
	Default          bool              `json:"default,omitempty"`
	CredentialSource string            `json:"credential_source"`
	// ShadowedEnvVar names an environment variable that is set but loses to
	// a higher-precedence credential (api_key, credential_headers, or
	// store, spec §10); empty when no such variable is set, including when
	// an env source is itself what resolves.
	ShadowedEnvVar string   `json:"shadowed_env_var,omitempty"`
	Warnings       []string `json:"warnings,omitempty"`
}

// envVarName is the spec §6.2 rule: the id uppercased with `-` → `_`.
func envVarName(id string) string {
	return strings.ToUpper(strings.ReplaceAll(id, "-", "_"))
}

// InstanceKeyEnvVar is the environment variable a custom-named instance
// falls back to for its key: the name uppercased with `-` → `_`, plus
// _API_KEY (spec §10). Resolution consults it last, and only for a name
// that is not itself a registry provider id; `evener providers add` names
// it when it has to say which variable to set.
func InstanceKeyEnvVar(name string) string {
	return envVarName(name) + "_API_KEY"
}

// oauthRecordPath is where the Codex transport keeps an instance's OAuth
// record (spec §9.5): auth/<instance>.json under the state root.
func oauthRecordPath(stateRoot, instance string) string {
	return filepath.Join(stateRoot, "auth", instance+".json")
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// adcAvailable reports whether application-default credentials can be found
// without the network: the GOOGLE_APPLICATION_CREDENTIALS file or the
// well-known gcloud file. The metadata server is never probed (spec §5.1).
func adcAvailable(env func(string) (string, bool)) bool {
	if p, ok := env("GOOGLE_APPLICATION_CREDENTIALS"); ok && p != "" {
		return fileExists(p)
	}
	home, _ := env("HOME")
	if home == "" {
		return false
	}
	return fileExists(filepath.Join(home, ".config", "gcloud", "application_default_credentials.json"))
}

// effectiveAPIKeyEnv applies the endpoint stop (spec §10): an explicit
// instance whose literal base_url names a different endpoint from its base
// does not inherit the base's api_key_env; its own api_key_env always
// counts. "Different" is judged against the base's URL both as resolved in
// this environment and with the curated defaults alone, so copying the
// default URL verbatim is not different even when an env override is set.
func (r *Registry) effectiveAPIKeyEnv(rec *record) []string {
	if rec.curated || rec.providerID == "" || rec.ownBaseURL == "" {
		return rec.head.APIKeyEnv
	}
	base := r.curated[rec.providerID]
	own, _, _ := r.resolveBaseURL(rec, rec.head.Transport)
	live, _, _ := r.resolveBaseURL(base, base.head.Transport)
	defaults, _, _ := r.resolveBaseURLWith(base, base.head.Transport, r.defaultVarLookup(base))
	if own == live || own == defaults {
		return rec.head.APIKeyEnv
	}
	return rec.ownAPIKeyEnv
}

// firstPartyEndpoint reports whether the endpoint a specific resolution
// actually reaches - transport, the fully resolved Transport buildTransport
// produced (row-level overrides included, not just the instance's own
// rec.head.Transport), resolved for proto against row rowID - is its
// provider's (r.curated[rec.providerID], which is rec itself for a curated
// record) own first-party endpoint: a vendor's hosted tools run on the
// vendor's own infrastructure, so a resolution reaching anywhere else does
// not get to carry them (WebSearch's gate, resolve.go's gateWebSearch,
// applies this; spec §10's credential endpoint stop is the analogous rule
// for the API key). TestResolve_WebSearchEndpointGate pins the case
// catalog this has to get right; the invariants below are the ones a
// reader would not derive from the code alone.
//
// ownOverride is true when anything rec's own config replaced the
// provider's base_url template outright - literally (rec.ownBaseURL) or on
// the resolved row (row.Transport's own BaseURL) - rather than merely
// supplying values for it. base_url is compared against the provider's own
// template resolved with only its curated vars (spec §10's "after
// substituting the curated defaults"); trailing slashes are trimmed on
// both sides first, mirroring protocolhttp.URL's own
// strings.TrimRight(BaseURL, "/") rather than inventing a broader
// canonicalization. A provider whose template needs only
// deployment-specific variables with no curated default (Vertex, Bedrock)
// has no default to diverge from - the template alone being intact is
// first-party there, unless the record redirects the one variable that is
// not deployment-specific (vertexHostOverridden).
//
// A resolved base_url is not the whole story: its own endpoint,
// stream_endpoint, or count_tokens_endpoint can send the same
// web-search-bearing request body elsewhere while base_url stays
// canonical. All three are judged the same way base_url is: against what
// buildTransport produces for the provider's own record with no instance
// overrides, for proto (which already applies the cross-protocol rule,
// spec §4.2, so a legitimately cross-protocol instance is judged against
// its own protocol's defaults) and the matching curated row
// (base.head.Models[rowID]) rather than no row at all - a non-curated
// instance that inherits a curated row with its own transport (a custom
// instance with base = "google-vertex" resolving a Claude model, whose row
// genuinely carries the vertex-anthropic preset from the models.dev
// npm-to-preset conversion) is judged against that row, not a row-less
// baseline that could never reproduce it; rowID == "" (ResolveInstance's
// model-less path) falls back to the row-less comparison a provider-level
// resolution needs anyway. ModelsEndpoint is excluded from this class:
// ListModels is always a bodyless GET, so it can never carry WebSearch.
//
// A curated record (rec.curated) is trusted outright once base_url checks
// out, endpoint comparison included: it is compared against itself
// (base == rec), so the comparison would be trivially true anyway, but
// skipping it also skips the row lookup, which matters when rowID names a
// row the curated record's own config (not a user's) supplies.
func (r *Registry) firstPartyEndpoint(rec *record, transport Transport, proto, rowID string, ownOverride bool) bool {
	base, ok := r.curated[rec.providerID]
	if !ok {
		return true
	}
	own := strings.TrimRight(transport.BaseURL, "/")
	if own == "" {
		return true
	}
	defaults, missing, _ := r.resolveBaseURLWith(base, base.head.Transport, r.defaultVarLookup(base))
	defaults = strings.TrimRight(defaults, "/")
	switch {
	case len(missing) > 0:
		// rec.curated is a belt-and-suspenders guard, not load-bearing
		// against today's data (a curated record never folds a
		// LayerConfig layer, so ownOverride/vertexHostOverridden are
		// already false for one) - kept explicit so a future curated row
		// with its own base_url or GOOGLE_VERTEX_HOST is trusted the same
		// way the vendor's provider-level template already is.
		if !rec.curated && (ownOverride || r.vertexHostOverridden(rec)) {
			return false
		}
	case own != defaults:
		return false
	}
	if rec.curated {
		return true
	}
	var row Model
	if rowID != "" {
		row = base.head.Models[rowID]
	}
	canonical, _ := r.buildTransport(base, row, proto)
	return transport.Endpoint == canonical.Endpoint &&
		transport.StreamEndpoint == canonical.StreamEndpoint &&
		transport.CountTokensEndpoint == canonical.CountTokensEndpoint
}

// vertexHostOverridden reports whether rec's own vars (never the curated
// default, since google-vertex-anthropic/google-vertex define none) supply
// a literal GOOGLE_VERTEX_HOST directly, bypassing vertexHost(LOCATION) -
// the derivation the vertex-location host rule exists to perform
// (resolveBaseURLWith's HostRuleVertexLocation case, load.go). Unlike
// GOOGLE_VERTEX_PROJECT/LOCATION, which are genuinely deployment-specific,
// GOOGLE_VERTEX_HOST has exactly one correct value for a given location;
// supplying a different one is a redirect the vars-only carve-out above
// would otherwise miss, since it leaves rec.ownBaseURL empty (the template
// is technically untouched). A supplied value that happens to equal the
// derivation's own answer is not an override (copying the default
// verbatim is not "different", spec §10).
//
// This check is Vertex-specific by necessity, not by choice: it is the
// only host rule with both a missing curated base_url default and a
// provider that sets web_search today. A future provider sharing that
// shape - no curated default, host routed through its own env var, kept
// template - would need an equivalent check of its own; nothing here
// generalizes to it automatically.
func (r *Registry) vertexHostOverridden(rec *record) bool {
	if rec.head.Transport.HostRule != HostRuleVertexLocation {
		return false
	}
	raw := r.varLookupWith(rec, func(string) {})
	given, ok := raw("GOOGLE_VERTEX_HOST")
	if !ok {
		return false
	}
	loc, ok := raw("GOOGLE_VERTEX_LOCATION")
	return !ok || given != vertexHost(loc)
}

// envCandidates lists, in the order credential resolution tries them, every
// environment variable name that could supply rec's key: its effective
// api_key_env, then (only for a name that is not itself a registry id, spec
// §10) the name derived from the instance name. It never reads the
// environment; it only says which variables would matter if they were set,
// so both credential (which stops at the first hit) and shadowedEnvVar
// (which wants to know about one even when something else already won) can
// share the one list.
func (r *Registry) envCandidates(rec *record) []string {
	var out []string
	out = append(out, r.effectiveAPIKeyEnv(rec)...)
	if _, isRegistryID := r.curated[rec.name]; !isRegistryID {
		out = append(out, InstanceKeyEnvVar(rec.name))
	}
	return out
}

// consumedEnvVars names the environment variable(s) a winning api_key or
// credential_headers expression itself expanded (a "$VAR" reference), so
// shadowedEnvVar can tell "this is what resolved the credential" apart from
// "this lost." Empty for a literal value (no "$") and for every other
// source, which consumes no expression.
func consumedEnvVars(rec *record, source string) []string {
	switch source {
	case "api_key":
		refs, _, _ := ScanConfigValue(rec.head.APIKey)
		return refs
	case "credential_headers":
		refs, _, _ := ScanConfigValue(rec.head.CredentialHeaders["Authorization"])
		return refs
	default:
		return nil
	}
}

// shadowedEnvVar names an environment variable that is set but loses to
// cred, the credential that actually resolved (spec §10: api_key >
// credential_headers > store > env). Only those three sources can shadow
// anything: oauth-openai-codex and gcp-adc are terminal branches in
// credential that never consult api_key_env at all (whether they resolve,
// giving "oauth"/"adc", or not, giving "none" the same as every other
// unresolved scheme), so naming a candidate against any of them - including
// "none" - would blame a source that was never actually in contention.
// Empty when nothing shadows it: no remaining candidate is set, or an env
// source is itself what won.
func (r *Registry) shadowedEnvVar(rec *record, cred Credential) string {
	switch cred.Source {
	case "api_key", "credential_headers", "store":
	default:
		return ""
	}
	consumed := consumedEnvVars(rec, cred.Source)
	for _, name := range r.envCandidates(rec) {
		if slices.Contains(consumed, name) {
			continue
		}
		if v, ok := r.env(name); ok && v != "" {
			return name
		}
	}
	return ""
}

// credential resolves an instance's credential in spec §10's order and
// returns the "no credential" warnings (none for the none/optional-bearer
// schemes). It never performs I/O beyond a file-existence check.
func (r *Registry) credential(rec *record) (Credential, []string) {
	h := rec.head
	optional := h.Transport.Auth == AuthNone || h.Transport.Auth == AuthOptionalBearer
	none := func(reason string) (Credential, []string) {
		if optional {
			return Credential{Source: "none"}, nil
		}
		return Credential{Source: "none"}, []string{reason}
	}
	switch h.Transport.Auth {
	case AuthOAuthOpenAICodex:
		if fileExists(oauthRecordPath(r.stateRoot, rec.name)) {
			return Credential{Source: "oauth"}, nil
		}
		return none(fmt.Sprintf("no credential (run `evener openai login --instance %s`)", rec.name))
	case AuthGCPADC:
		if adcAvailable(r.env) {
			return Credential{Source: "adc"}, nil
		}
		return none("no credential (no application-default credentials; run `gcloud auth application-default login` or set GOOGLE_APPLICATION_CREDENTIALS)")
	}
	if h.APIKey != "" {
		v, missing := expandEnv(h.APIKey, r.env)
		if len(missing) > 0 {
			return none(fmt.Sprintf("no credential (%s unset)", strings.Join(missing, ", ")))
		}
		return Credential{Value: v, Source: "api_key"}, nil
	}
	if auth, ok := h.CredentialHeaders["Authorization"]; ok && auth != "" {
		v, missing := expandEnv(auth, r.env)
		if len(missing) > 0 {
			return none(fmt.Sprintf("no credential (%s unset)", strings.Join(missing, ", ")))
		}
		return Credential{Value: v, Source: "credential_headers"}, nil
	}
	if r.creds != nil {
		if v, ok := r.creds.Lookup(rec.name); ok && v != "" {
			return Credential{Value: v, Source: "store"}, nil
		}
	}
	for _, name := range r.envCandidates(rec) {
		if v, ok := r.env(name); ok && v != "" {
			return Credential{Value: v, Source: "env:" + name}, nil
		}
	}
	return none("no credential")
}

// computeInstances derives the instance set (spec §5.1): every explicit
// entry, plus every curated implicit provider that is not shadowed, not
// hidden, and whose credential resolves without the network.
func (r *Registry) computeInstances() {
	rank := map[string]int{}
	for i, id := range r.defaultOrder {
		rank[id] = i
	}
	custom := len(r.defaultOrder)
	r.instances = map[string]*instance{}
	for name, rec := range r.explicit {
		pos, ok := rank[name]
		if !ok {
			pos = custom
		}
		r.instances[name] = &instance{name: name, rec: rec, rank: pos}
	}
	for id, rec := range r.curated {
		if rec.head.Implicit == nil || !*rec.head.Implicit || rec.head.Hidden {
			continue
		}
		if _, shadowed := r.explicit[id]; shadowed {
			continue
		}
		if cred, _ := r.credential(rec); cred.Source == "none" && rec.head.Transport.Auth != AuthNone && rec.head.Transport.Auth != AuthOptionalBearer {
			continue
		}
		pos, ok := rank[id]
		if !ok {
			pos = custom // pseudo-providers: after every default_order entry
		}
		r.instances[id] = &instance{name: id, rec: rec, implicit: true, rank: pos}
	}
}

// rankedInstances orders instances by spec §5.1: default_order position
// (a shadowing explicit entry keeps its id's rank), then every other
// instance by name.
func (r *Registry) rankedInstances() []*instance {
	out := make([]*instance, 0, len(r.instances))
	for _, inst := range r.instances {
		out = append(out, inst)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].rank != out[j].rank {
			return out[i].rank < out[j].rank
		}
		return out[i].name < out[j].name
	})
	return out
}

// recordFor finds what a reference's instance half names: an explicit
// instance, an implicit instance, or any curated implicit provider id
// (resolvable without a credential, spec §5.2).
func (r *Registry) recordFor(name string) (*record, bool) {
	if inst, ok := r.instances[name]; ok {
		return inst.rec, true
	}
	if rec, ok := r.curated[name]; ok && rec.head.Implicit != nil && *rec.head.Implicit {
		return rec, true
	}
	return nil, false
}

// validateDefault enforces spec §5.1 at load: `default` must name an
// explicit instance or a curated implicit id.
func (r *Registry) validateDefault() error {
	if r.userDefault == "" {
		return nil
	}
	if _, ok := r.explicit[r.userDefault]; ok {
		return nil
	}
	if rec, ok := r.curated[r.userDefault]; ok && rec.head.Implicit != nil && *rec.head.Implicit {
		return nil
	}
	return fmt.Errorf("default = %q names neither an explicit instance nor an implicit provider (add a [providers.%s] entry)", r.userDefault, r.userDefault)
}

// DefaultInstance picks the default instance (spec §5.1): `default` when it
// is an instance here; else the first ranked instance with a default model.
// A `default` that is a credential-less or hidden implicit id warns and
// falls through.
func (r *Registry) DefaultInstance() (string, []string, error) {
	var warnings []string
	if r.userDefault != "" {
		if _, ok := r.instances[r.userDefault]; ok {
			return r.userDefault, nil, nil
		}
		rec := r.curated[r.userDefault]
		switch {
		case rec == nil:
			return "", nil, fmt.Errorf("default = %q is not an instance", r.userDefault)
		case rec.head.Hidden:
			warnings = append(warnings, fmt.Sprintf("default = %q: provider is hidden (base URL variable unset); falling through", r.userDefault))
		default:
			warnings = append(warnings, fmt.Sprintf("default = %q: no credential in this environment; falling through", r.userDefault))
		}
	}
	ranked := r.rankedInstances()
	if len(ranked) == 0 {
		return "", warnings, errors.New("no default instance: set `default` in providers.toml or export a provider key")
	}
	var without []string
	for _, inst := range ranked {
		if inst.rec.head.DefaultModel != "" {
			return inst.name, warnings, nil
		}
		without = append(without, inst.name)
	}
	first := ranked[0].name
	return "", warnings, fmt.Errorf("%s has no default model; pass `%s/<model>` or set `default` (instances without one: %s)", first, first, strings.Join(without, ", "))
}

// Instances lists every instance in default ranking with its credential
// source and warnings (spec §11.2).
func (r *Registry) Instances() []Instance {
	def, _, _ := r.DefaultInstance()
	var out []Instance
	for _, inst := range r.rankedInstances() {
		cred, warns := r.credential(inst.rec)
		h := inst.rec.head
		base := ""
		if !inst.rec.curated && inst.rec.providerID != inst.name {
			base = inst.rec.providerID
		}
		baseURL := ""
		if !h.Hidden {
			baseURL, _, _ = r.resolveBaseURL(inst.rec, h.Transport)
		}
		out = append(out, Instance{
			Name: inst.name, ProviderID: inst.rec.providerID, Base: base, Protocol: h.Protocol, Surface: h.Surface,
			Auth: h.Transport.Auth, BaseURL: baseURL, Vars: maps.Clone(inst.rec.userVars), DefaultModel: h.DefaultModel,
			Implicit: inst.implicit, Hidden: h.Hidden, Default: inst.name == def,
			CredentialSource: cred.Source, Warnings: warns,
			ShadowedEnvVar: r.shadowedEnvVar(inst.rec, cred),
		})
	}
	return out
}

// Instance returns one instance's listing view.
func (r *Registry) Instance(name string) (Instance, bool) {
	for _, inst := range r.Instances() {
		if inst.Name == name {
			return inst, true
		}
	}
	return Instance{}, false
}

// StateRoot is the state root the registry was loaded with: OAuth records
// and the catalog cache live under it, and the Codex authenticator must
// read the same directory (spec §9.5).
func (r *Registry) StateRoot() string { return r.stateRoot }

// StrayOAuthRecords lists auth/<name>.json records under the state root
// whose <name> is not an instance on the Codex transport (spec §9.5, §14.1).
// Nothing reads such a record, so each notice says how to remove it.
func (r *Registry) StrayOAuthRecords() []string {
	dir := filepath.Join(r.stateRoot, "auth")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || e.IsDir() {
			continue
		}
		if rec, found := r.recordFor(name); found && rec.head.Transport.Auth == AuthOAuthOpenAICodex {
			continue
		}
		out = append(out, fmt.Sprintf("stray OAuth record %s: %q is not an instance on the Codex transport; remove it with `evener openai logout --instance %s`", filepath.Join(dir, e.Name()), name, name))
	}
	sort.Strings(out)
	return out
}
