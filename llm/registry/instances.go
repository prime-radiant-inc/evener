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

	"primeradiant.com/evener/internal/valueexpr"
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

// ProviderRenameLeavesInstance reports whether a rename that frees the curated
// provider id would leave an instance resolving under that id. A rename moves
// the user's own layers - the authored providers.toml entry, the stored
// credential and the OAuth record - away from the old name; what it cannot
// move is the curated definition and the host environment. So the id re-derives
// an instance exactly when the curated provider actually derives one - the
// same condition computeInstances uses: it is marked implicit and not hidden -
// and needs no credential the rename moves: a keyless scheme (auth none or
// optional-bearer), a set api_key_env variable, or the ADC file under gcp-adc.
// The vocabulary is the ownership predicate's (environmentBacked): only
// env:<VAR> and adc count as the environment's, so an inline api_key or
// credential_headers expression is never a re-derivation here. No curated
// record carries either (the catalog conversion sets only api_key_env), and a
// present higher-priority expression is terminal in credential() anyway, so it
// never falls through to the candidates below. A non-implicit or hidden
// curated provider resolves no instance of its own, and the OAuth scheme's
// only credential is the record the rename moves, so all answer false. False
// for an id that is not curated.
//
// This is the rule the hub's instance listing computes once (renameLeavesRow)
// so the rename note does not have to infer it from InstanceEntry, which
// cannot see ADC availability or the curated set.
func (r *Registry) ProviderRenameLeavesInstance(id string) bool {
	rec, ok := r.curated[id]
	if !ok || rec.head.Hidden {
		return false
	}
	// computeInstances derives a row only for a curated record marked
	// implicit; a non-implicit provider id resolves no instance on its own, so
	// freeing the name leaves nothing to re-derive.
	if rec.head.Implicit == nil || !*rec.head.Implicit {
		return false
	}
	switch rec.head.Transport.Auth {
	case AuthNone, AuthOptionalBearer:
		return true
	case AuthOAuthOpenAICodex:
		// The record is the only credential this scheme reads, and the rename
		// moves it.
		return false
	case AuthGCPADC:
		return adcAvailable(r.env)
	}
	// A present inline credential expression is terminal in credential():
	// whether it resolves or its variables are unset, it is the row's
	// credential and never the environment's (the ownership predicate allow-lists
	// only env:<VAR> and adc), so it is not a rename re-derivation and it does
	// not fall through to the api_key_env candidates below. A present
	// expression whose variables are unset means the row resolves nothing at
	// all, which is also false.
	if rec.head.APIKey != "" || authHeaderKey(rec.head.CredentialHeaders, authHeaderName(r.listingTransport(rec))) != "" {
		return false
	}
	for _, name := range r.effectiveAPIKeyEnv(rec) {
		if v, ok := r.env(name); ok && v != "" {
			return true
		}
	}
	return false
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
	defaults, _, _ := r.resolveBaseURLWith(base, base.head.Transport, r.defaultVarLookup(base, base.head.Transport))
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
// [providers.X] with its own base_url) has no vendor data to prove
// anything against, so it is gated the same way rather than trusted:
// nothing non-explicit survives - gateWebSearch normalizes its nil to a
// silent false and strips a glob-granted true - and the instance's own
// explicit web_search remains the grant.
//
// The canonical resolution admits values from the actual resolution's own
// variable lookup only where they cannot move the request off the vendor's
// infrastructure (canonicalVarLookup): path-position vars pass through
// verbatim; authority-affecting vars - template-authority placeholders and
// the inputs the host rule derives the authority from - resolve only from
// curated defaults, the rule's own derivation, or a rule input whose
// validated shape proves it harmless (a label-shaped Vertex location), and
// otherwise stay unexpanded, failing the comparison against any
// actually-resolved URL - fail closed.
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
		return false
	}
	var row Model
	if rowID != "" || ref != "" {
		row = r.canonicalRow(base, ref, altID, rowID, proto)
	}
	canonical := r.transportShape(base, row, proto)
	lookup := r.canonicalVarLookup(rec, base, canonical)
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
// of the transport shape's base_url template. Path-position vars take the
// actual resolution's own value (rec's full lookup: user vars, environment,
// curated defaults) - both sides then expand identically, and a path value
// can never rewrite the authority the template terminated before the var
// began. Authority-affecting vars - a placeholder inside the template's
// authority region (authorityVars), or any input the shape's host RULE
// consumes to derive the authority (hostRuleAuthorityVars; template
// position cannot see those, and a poisoned derivation input rewrites the
// host on both sides identically, comparing equal) - never take a raw user
// or environment value. They resolve from the provider's curated default,
// or from the actual value only when the rule's own input validation
// proves it cannot move the derivation off the vendor's domain
// (hostRuleInputAdmissible: a label-shaped Vertex location). Anything else
// stays unexpanded, so the canonical URL cannot equal any actually-resolved
// one: fail closed.
func (r *Registry) canonicalVarLookup(rec, base *record, shape Transport) func(string) (string, bool) {
	authority := authorityVars(shape.BaseURL)
	for _, name := range hostRuleAuthorityVars(shape.HostRule) {
		authority[name] = true
	}
	actual := r.varLookup(rec, shape)
	defaults := r.defaultVarLookup(base, shape)
	return func(name string) (string, bool) {
		if authority[name] {
			if v, ok := actual(name); ok && hostRuleInputAdmissible(shape.HostRule, name, v) {
				return v, true
			}
			return defaults(name)
		}
		return actual(name)
	}
}

// hostRuleAuthorityVars names the variables a host rule consumes to
// produce the authority. The rule's derivation runs on these values, so
// they are authority-affecting wherever they appear: GOOGLE_VERTEX_LOCATION
// sits in path position in the Vertex template, yet vertexHost builds the
// host from it. OLLAMA_BASE_URL is deliberately absent - the ollama-host
// rule reads it from the environment, which the canonical resolution
// already severs (resolveBaseURLVia's env parameter).
func hostRuleAuthorityVars(rule string) []string {
	switch rule {
	case HostRuleVertexLocation:
		return []string{"GOOGLE_VERTEX_HOST", "GOOGLE_VERTEX_LOCATION"}
	case HostRuleOllamaHost:
		return []string{"OLLAMA_HOST"}
	}
	return nil
}

// hostRuleInputAdmissible reports whether a host rule's derivation input
// may take the user's own value on the canonical side: only when its
// shape proves the derivation cannot leave the vendor's domain. A Vertex
// location that is a single hostname label (validVertexLocation) composes
// into *.googleapis.com and nothing else. No other rule input qualifies -
// GOOGLE_VERTEX_HOST and OLLAMA_HOST are the authority itself, and only a
// curated default or the derivation may supply those.
func hostRuleInputAdmissible(rule, name, value string) bool {
	return rule == HostRuleVertexLocation && name == "GOOGLE_VERTEX_LOCATION" && validVertexLocation(value)
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
// than inventing a broader canonicalization.
//
// Any "?" or "#" anywhere in either RAW string rejects outright, before
// any equality: the builder concatenates the endpoint path onto the raw
// base, so either delimiter sends the endpoint into the query or fragment
// instead of the path - and the parsed forms cannot be trusted to see
// that (url.URL has no ForceFragment: "https://h" and "https://h#" parse
// identically while building different requests). No curated base
// resolves to a URL containing either delimiter
// (TestCuratedBaseURLsCarryNoQueryOrFragment pins that, embedded catalog
// and fixture both), so the reject can never misjudge vendor data.
//
// Past that, equal strings are the same endpoint - an unresolved template
// equals only the identically unresolved template - and anything else
// must parse on both sides and match on scheme, userinfo, host, and the
// escaped path, which preserves percent-encoding rather than comparing
// decoded forms.
func sameEndpointURL(a, b string) bool {
	a, b = strings.TrimRight(a, "/"), strings.TrimRight(b, "/")
	if strings.ContainsAny(a, "?#") || strings.ContainsAny(b, "?#") {
		return false
	}
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
		ua.EscapedPath() == ub.EscapedPath()
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
func consumedEnvVars(rec *record, t Transport, source string) []string {
	switch source {
	case "api_key":
		refs, _, _ := ScanConfigValue(rec.head.APIKey)
		return refs
	case "credential_headers":
		refs, _, _ := ScanConfigValue(rec.head.CredentialHeaders[authHeaderKey(rec.head.CredentialHeaders, authHeaderName(t))])
		return refs
	default:
		return nil
	}
}

// shadowedEnvVar names an environment variable that is set but loses to
// cred, the credential that actually resolved (spec §10: api_key >
// credential_headers > store > env). Only those three sources can shadow
// anything, and only outside the gcp-adc scheme: oauth-openai-codex and
// gcp-adc are terminal branches in credential that never consult
// api_key_env at all - gcp-adc's own store lookup is keyed by instance
// name, not by any api_key_env/InstanceKeyEnvVar candidate - so naming a
// candidate against either of them, for any of their sources ("oauth",
// "adc", "store", or "none"), would blame a variable that was never
// actually in contention.
// Empty when nothing shadows it: no remaining candidate is set, or an env
// source is itself what won.
func (r *Registry) shadowedEnvVar(rec *record, t Transport, cred Credential) string {
	if t.Auth == AuthGCPADC {
		return ""
	}
	switch cred.Source {
	case "api_key", "credential_headers", "store":
	default:
		return ""
	}
	consumed := consumedEnvVars(rec, t, cred.Source)
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
// schemes). It performs no I/O beyond a file-existence check and the
// $(command) expressions an api_key or credential-header value carries,
// which the shared evaluator resolves against the process environment with
// its process-wide cache (spec §10). The Authorization header expands only
// when this record's credential can actually come from it — the oauth and
// adc schemes return before the header branch, and an api_key outranks it —
// because the listing calls credential for every instance and must not
// mint (or stall on) a command that resolution alone reads; the resolution
// path passes the one expansion it already made through credentialWithAuth
// (resolveCredentials), so there the header runs once per resolution.
func (r *Registry) credential(rec *record) (Credential, []string) {
	h := rec.head
	// The listing reads the default row's transport, the launch a bare
	// instance name makes; the guard and the branches below read the same
	// one, so the oauth and adc terminals and the header's own name agree.
	t := r.listingTransport(rec)
	if t.Auth == AuthOAuthOpenAICodex || t.Auth == AuthGCPADC || h.APIKey != "" {
		return r.credentialWithAuth(rec, authExpansion{}, t, false)
	}
	return r.credentialWithAuth(rec, r.authorization(rec, t), t, false)
}

// authHeaderName is the header the auth scheme of the transport a launch
// resolves writes the credential to: the author's auth_header for header
// auth, Authorization for every other scheme — the same choice the
// transport layer makes for the wire, so the credential-carrying entry is
// the entry the scheme sends.
func authHeaderName(t Transport) string {
	if t.Auth == AuthHeader && t.AuthHeader != "" {
		return t.AuthHeader
	}
	return "Authorization"
}

// listingTransport is the transport a bare launch of the instance would
// use: the canonical shape of the default model row — the provider's own
// transport with the row's overrides and the cross-protocol rule applied —
// or the provider's own when no default row exists. Rows are per model and
// the listing is per instance, so this is the launch shape the spawn gate's
// refusal judges.
func (r *Registry) listingTransport(rec *record) Transport {
	row, ok := rec.head.Models[rec.head.DefaultModel]
	if !ok || isGlob(rec.head.DefaultModel) || row.Transport == nil {
		return rec.head.Transport
	}
	proto := row.Protocol
	if proto == "" {
		proto = rec.head.Protocol
	}
	return r.transportShape(rec, row, proto)
}

// authHeaderKey resolves which credential-header key carries the named
// auth header's value: header names are case-insensitive on the wire, so
// an author may write any case. The exact-case key wins — a present-but-
// empty one is spec §10's removal and resolves as absence — and among case
// variants the lexicographically first, so the choice is deterministic and
// authorization, expandCredentialHeaders, and consumedEnvVars all read the
// same entry.
func authHeaderKey(headers map[string]string, name string) string {
	if v, ok := headers[name]; ok {
		if v == "" {
			return ""
		}
		return name
	}
	first := ""
	for k, v := range headers {
		if v == "" || !strings.EqualFold(k, name) {
			continue
		}
		if first == "" || k < first {
			first = k
		}
	}
	return first
}

// authExpansion is the one expansion of the record's Authorization
// credential header: the header key it read (authHeaderKey), the expanded
// value, the pieces that never resolved, and whether the value's only
// possible material is a bare auth scheme word.
type authExpansion struct {
	key        string
	expanded   string
	present    bool
	unresolved []valueexpr.Unresolved
	noMaterial bool
}

// authorization expands the record's auth credential header once,
// shared by credential() and resolveCredentials so its command expressions
// run once per resolution, and reports which header key it expanded so the
// header map and the credential always read the same entry.
// The transport a launch resolves names the header: the resolve paths pass
// their row-merged transport, the listing the default row's.
func (r *Registry) authorization(rec *record, t Transport) authExpansion {
	key := authHeaderKey(rec.head.CredentialHeaders, authHeaderName(t))
	if key == "" {
		return authExpansion{}
	}
	raw := rec.head.CredentialHeaders[key]
	expanded, unresolved := expandEnv(raw, r.env)
	return authExpansion{key: key, expanded: expanded, present: true, unresolved: unresolved, noMaterial: r.schemeWordDefault(raw)}
}

// schemeWordDefault reports whether a raw credential field's — the
// Authorization credential header's, or api_key's — only possible material
// is auth scheme words, and only when a reference's default supplied it.
// Minted and environment-supplied bytes are data — a command's all-letters
// output is a token, not a scheme word — and a pure literal is the author's
// own key material, trusted exactly like any hand-typed secret; the judged
// cases are authored defaults that assemble to nothing but scheme words —
// "${KEY:-Bearer}", or "Bearer ${KEY:-Basic}" with KEY missing — where
// authored default text stands in for a credential and carries none.
func (r *Registry) schemeWordDefault(raw string) bool {
	pieces, err := valueexpr.Pieces(raw)
	if err != nil {
		return false
	}
	var material strings.Builder
	filledByDefault := false
	for _, p := range pieces {
		switch p.Kind {
		case valueexpr.PieceLit:
			material.WriteString(p.Lit)
		case valueexpr.PieceRef:
			if v, ok := r.env(p.Ref.Name); ok && v != "" {
				return false
			}
			if p.Ref.HasDefault {
				filledByDefault = true
				material.WriteString(p.Ref.Default)
			}
		case valueexpr.PieceCommand:
			return false
		}
	}
	assembled := strings.TrimSpace(material.String())
	if !filledByDefault || assembled == "" {
		return false
	}
	for token := range strings.FieldsSeq(assembled) {
		if !isAuthSchemeWord(token) {
			return false
		}
	}
	return true
}

// credentialWithAuth is credential with the auth header's expansion
// supplied, so the resolution path that also builds the credential header
// map never runs the header's command expressions twice. The transport a
// launch resolves governs the scheme branches — oauth and adc are terminal,
// none and optional-bearer need no credential — under the same transport
// that names the header. suppressAuthReason drops the no-credential reason
// from a failing auth header: the resolution path passes it because its
// header loop reports the same failure naming the header — one condition,
// one warning — while the listing path builds no header map and passes
// false to keep the reason.
func (r *Registry) credentialWithAuth(rec *record, auth authExpansion, t Transport, suppressAuthReason bool) (Credential, []string) {
	h := rec.head
	optional := t.Auth == AuthNone || t.Auth == AuthOptionalBearer
	none := func(reason string) (Credential, []string) {
		if optional {
			return Credential{Source: "none"}, nil
		}
		return Credential{Source: "none"}, []string{reason}
	}
	switch t.Auth {
	case AuthOAuthOpenAICodex:
		if fileExists(oauthRecordPath(r.stateRoot, rec.name)) {
			return Credential{Source: "oauth"}, nil
		}
		return none(fmt.Sprintf("no credential (run `evener openai login --instance %s`)", rec.name))
	case AuthGCPADC:
		// A credential JSON stored under the instance name (a service-account
		// key or an authorized_user file the hub accepted) outranks the ADC
		// file, so a hub host needs neither gcloud nor variables (spec §4.2).
		// A store entry that is not a JSON object — a stale API key left
		// over from before this instance used gcp-adc, say — is not a
		// credential this scheme can use; it must not shadow a working ADC
		// file, so it falls through with a warning instead of being
		// returned (roborev F2).
		var warn []string
		if r.creds != nil {
			if v, ok := r.creds.Lookup(rec.name); ok && v != "" {
				err := CheckCredentialJSON([]byte(v))
				if err == nil {
					return Credential{Value: v, Source: "store"}, nil
				}
				warn = append(warn, fmt.Sprintf("credentials-store entry for %q is not a credential JSON evener can use (%v); it is ignored for gcp-adc: clear it (evener/auth/apiKey/clear) or replace it with a service-account or authorized_user JSON", rec.name, err))
			}
		}
		if adcAvailable(r.env) {
			return Credential{Source: "adc"}, warn
		}
		cred, reasons := none("no credential (no application-default credentials; run `gcloud auth application-default login` or set GOOGLE_APPLICATION_CREDENTIALS, or store a credential JSON for the instance)")
		return cred, append(warn, reasons...)
	}
	if h.APIKey != "" {
		v, missing := expandEnv(h.APIKey, r.env)
		if len(missing) > 0 {
			// The authored layer is present and terminal, and its variable is
			// unset: say so, because "none" alone reads as "nothing is
			// configured here" to every caller that decides whether a stored key
			// would ever be sent.
			cred, warns := none(fmt.Sprintf("no credential (%s)", missingReason(missing)))
			cred.AuthoredLayer = "api_key"
			return cred, warns
		}
		if v == "" {
			// An empty ${VAR:-} default resolved to nothing: an empty
			// credential never resolves as a present one.
			return none("no credential (api_key expands to an empty value)")
		}
		if r.schemeWordDefault(h.APIKey) {
			// A default that fills in a bare scheme word is authored
			// placeholder text, not key material — the same rule the
			// Authorization header applies — so it resolves as no
			// credential with a warning, never as a present one whose
			// value would reach the wire as the word alone.
			return none("no credential (api_key expands to nothing but an auth scheme word)")
		}
		return Credential{Value: v, Source: "api_key"}, nil
	}
	if auth.present {
		noneAuth := none
		if suppressAuthReason {
			noneAuth = func(string) (Credential, []string) { return Credential{Source: "none"}, nil }
		}
		if len(auth.unresolved) > 0 {
			cred, warns := noneAuth(fmt.Sprintf("no credential (%s)", missingReason(auth.unresolved)))
			cred.AuthoredLayer = "credential_headers"
			return cred, warns
		}
		if auth.expanded == "" {
			return noneAuth(fmt.Sprintf("no credential (the %s credential header expands to an empty value)", auth.key))
		}
		if auth.noMaterial {
			return noneAuth(fmt.Sprintf("no credential (the %s credential header expands to nothing but an auth scheme word)", auth.key))
		}
		return Credential{Value: auth.expanded, Source: "credential_headers"}, nil
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
// hidden, and whose credential resolves without the network. The curated
// layers author no $(command) expressions, so a derived row never runs one
// here; explicit rows are listed whatever their credential resolves to.
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
		auth := r.listingTransport(rec).Auth
		if cred, _ := r.credential(rec); cred.Source == "none" && auth != AuthNone && auth != AuthOptionalBearer {
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
// source and warnings (spec §11.2). The auth scheme is the one the same
// row-merged transport picks — the launch a bare instance name makes — so
// a default row overriding auth moves the listing's scheme with it.
func (r *Registry) Instances() []Instance {
	def, _, _ := r.DefaultInstance()
	var out []Instance
	for _, inst := range r.rankedInstances() {
		cred, warns := r.credential(inst.rec)
		t := r.listingTransport(inst.rec)
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
			Auth: t.Auth, BaseURL: baseURL, Vars: maps.Clone(inst.rec.userVars), DefaultModel: h.DefaultModel,
			Implicit: inst.implicit, Hidden: h.Hidden, Default: inst.name == def,
			CredentialSource: cred.Source, Warnings: warns,
			ShadowedEnvVar: r.shadowedEnvVar(inst.rec, t, cred),
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
