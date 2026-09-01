package registry

import (
	"errors"
	"fmt"
	"maps"
	"net/url"
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
// produced, with config layers, glob rows, environment overrides, host
// rules, and alias imports all folded in - is its provider's own
// first-party endpoint: a vendor's hosted tools run on the vendor's own
// infrastructure, so a resolution reaching anywhere else does not get to
// carry them (WebSearch's gate, resolve.go's gateWebSearch, applies this;
// spec §10's credential endpoint stop is the analogous rule for the API
// key).
//
// The judgment is first-party by construction, not by override-detection:
// config is compositional, so the channels that can redirect a request are
// combinatorial and enumerating them cannot converge. Instead the
// provider's curated layers alone (r.curated[rec.providerID]: the
// snapshot/cache and overlay data, never the user's config or environment)
// are resolved through the same transport machinery into the canonical
// transport for (proto, rowID), and the actual transport must equal it:
// base_url componentwise (sameEndpointURL) and each request-carrying
// endpoint path exactly (Endpoint, StreamEndpoint, CountTokensEndpoint;
// ModelsEndpoint is excluded because ListModels is a bodyless GET that can
// never carry WebSearch). Any difference means the request lands somewhere
// the vendor's own data does not describe. An override that reproduces the
// canonical endpoint verbatim compares equal, so spec §10's "copying the
// default is not different" needs no special case; neither does trusting a
// curated record, whose actual resolution is the canonical one unless the
// environment redirects it - which must strip, and does, by comparing
// unequal. A record with no curated provider at all (a from-scratch
// [providers.X] with its own base_url) has no vendor endpoint to diverge
// from and is trivially first-party; only its own explicit web_search can
// grant the capability anyway.
//
// The canonical resolution admits values from the actual resolution's own
// variable lookup only where they cannot move the request off the vendor's
// infrastructure (canonicalVarLookup): path-position vars pass through
// verbatim, authority-position vars resolve only from curated defaults or
// a host rule's own derivation and otherwise stay unexpanded, failing the
// comparison against any actually-resolved URL - fail closed.
//
// ref and altID feed the canonical glob replay (canonicalRow) so curated
// glob rows match the same ids they matched in the actual resolution;
// rowID names the resolved row - the alias target's row when an alias
// imported its transport (resolveOn's canonicalRowID). All three empty is
// ResolveInstance's model-less path, judged row-less on both sides.
// TestResolve_WebSearchEndpointGate and TestResolve_WebSearchCanonicalGate
// pin the case catalog this has to get right.
func (r *Registry) firstPartyEndpoint(rec *record, transport Transport, proto, rowID, ref, altID string) bool {
	base, ok := r.curated[rec.providerID]
	if !ok {
		return true
	}
	var row Model
	if rowID != "" || ref != "" {
		row = r.canonicalRow(base, ref, altID, rowID, proto)
	}
	canonical := r.transportShape(base, row, proto)
	lookup := r.canonicalVarLookup(rec, base, canonical.BaseURL)
	noEnv := func(string) (string, bool) { return "", false }
	baseURL, _, _ := r.resolveBaseURLVia(canonical, lookup, noEnv)
	canonical.BaseURL = baseURL
	for _, field := range []*string{&canonical.Endpoint, &canonical.StreamEndpoint, &canonical.CountTokensEndpoint} {
		expanded, _ := expandTemplate(*field, lookup)
		*field = expanded
	}
	return sameEndpointURL(transport.BaseURL, canonical.BaseURL) &&
		transport.Endpoint == canonical.Endpoint &&
		transport.StreamEndpoint == canonical.StreamEndpoint &&
		transport.CountTokensEndpoint == canonical.CountTokensEndpoint
}

// canonicalRow replays the provider's curated layers alone into the row the
// canonical resolution resolves: the merged curated exact row, plus every
// curated glob row applied in the order resolveOn applies them (top-level
// globs once per layer tag, the layer's own globs, then the exact row),
// matching against the same reference and alias-target ids the actual
// resolution matched. base's layers are curated by construction, so no
// LayerConfig contribution - the channel the gate exists to judge - can
// reach the result.
func (r *Registry) canonicalRow(base *record, ref, altID, rowID, proto string) Model {
	var row Model
	if rowID != "" {
		if m, ok := base.head.Models[rowID]; ok {
			row = cloneModel(m)
		}
	}
	// The caps and provenance sinks are discarded: only the transport
	// fields applyGlobs and applyRowScalars merge matter here.
	caps := Caps{}
	prov := map[string]string{}
	crossProto := proto != base.head.Protocol
	seenTag := map[string]bool{}
	for _, layer := range base.layers {
		if !seenTag[layer.tag] {
			seenTag[layer.tag] = true
			r.applyGlobs(&caps, &row, r.topGlobs[layer.tag], layer.tag, ref, altID, proto, crossProto, prov)
		}
		r.applyGlobs(&caps, &row, layer.rows, layer.tag, ref, altID, proto, crossProto, prov)
		if rowID != "" {
			if lr, ok := layer.rows[rowID]; ok {
				applyRowScalars(&row, lr, layer.tag, prov)
			}
		}
	}
	return row
}

// canonicalVarLookup supplies variable values for the canonical resolution
// of the base_url template tpl. Path-position vars take the actual
// resolution's own value (rec's full lookup: user vars, environment,
// curated defaults) - both sides then expand identically, and a path value
// can never rewrite the authority the template terminated before the var
// began. Authority-position vars (authorityVars) take only the provider's
// curated default, never a user- or environment-supplied value; a host
// rule may still derive one (resolveBaseURLVia's vertex-location case
// derives vertexHost(GOOGLE_VERTEX_LOCATION) - a path-position lookup -
// when this lookup declines GOOGLE_VERTEX_HOST), and a var with neither
// default nor derivation stays unexpanded, so the canonical URL cannot
// equal any actually-resolved one: fail closed.
func (r *Registry) canonicalVarLookup(rec, base *record, tpl string) func(string) (string, bool) {
	authority := authorityVars(tpl)
	actual := r.varLookup(rec)
	defaults := r.defaultVarLookup(base)
	return func(name string) (string, bool) {
		if authority[name] {
			return defaults(name)
		}
		return actual(name)
	}
}

// authorityVars names the {VAR} placeholders in tpl's authority region -
// at or before the end of scheme://host[:port], where an expanded value
// could supply or alter the scheme, host, or port. A template that does
// not begin with a literal scheme starts with a placeholder that expands
// to one ({BASE_URL}, {GOOGLE_VERTEX_HOST}, {OLLAMA_HOST}), so its
// authority begins at position 0. A placeholder at or past the first
// path, query, or fragment delimiter cannot escape into the authority -
// the template already terminated it - and is not listed.
func authorityVars(tpl string) map[string]bool {
	out := map[string]bool{}
	start := 0
	if i := strings.Index(tpl, "://"); i >= 0 {
		start = i + 3
	}
	end := len(tpl)
	for _, d := range "/?#" {
		if i := strings.IndexRune(tpl[start:], d); i >= 0 && start+i < end {
			end = start + i
		}
	}
	for _, m := range placeholderRe.FindAllStringSubmatchIndex(tpl, -1) {
		if m[0] < end {
			out[tpl[m[2]:m[3]]] = true
		}
	}
	return out
}

// sameEndpointURL reports whether two resolved base URLs name the same
// endpoint. Trailing slashes are trimmed first, mirroring the HTTP
// builder's own strings.TrimRight(BaseURL, "/") (protocolhttp.URL) rather
// than inventing a broader canonicalization. Equal strings are the same
// endpoint - an unresolved template equals only the identically unresolved
// template - and anything else must parse on both sides and match on every
// URL component, where a bare trailing "?" or "#" differs from their
// absence (ForceQuery, Fragment) even with nothing after them, and the
// escaped path preserves percent-encoding rather than comparing decoded
// forms.
func sameEndpointURL(a, b string) bool {
	a, b = strings.TrimRight(a, "/"), strings.TrimRight(b, "/")
	if a == b {
		return true
	}
	ua, errA := url.Parse(a)
	ub, errB := url.Parse(b)
	if errA != nil || errB != nil {
		return false
	}
	return ua.Scheme == ub.Scheme && ua.Opaque == ub.Opaque &&
		ua.User.String() == ub.User.String() && ua.Host == ub.Host &&
		ua.EscapedPath() == ub.EscapedPath() && ua.ForceQuery == ub.ForceQuery &&
		ua.RawQuery == ub.RawQuery && ua.Fragment == ub.Fragment
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
