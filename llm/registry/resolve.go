package registry

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"

	"primeradiant.com/evener/internal/valueexpr"
)

// protocolDefaults are the endpoint paths a protocol uses when the
// transport sets none (spec §6.1).
var protocolDefaults = map[string]Transport{
	ProtocolOpenAIChat:      {Endpoint: "/chat/completions", StreamEndpoint: "/chat/completions", ModelsEndpoint: "/models", CountTokensEndpoint: EndpointUnsupported},
	ProtocolOpenAIResponses: {Endpoint: "/responses", StreamEndpoint: "/responses", ModelsEndpoint: "/models", CountTokensEndpoint: EndpointUnsupported},
	ProtocolAnthropic:       {Endpoint: "/messages", StreamEndpoint: "/messages", ModelsEndpoint: "/models", CountTokensEndpoint: "/messages/count_tokens"},
	ProtocolGoogle:          {Endpoint: "/models/{model}:generateContent", StreamEndpoint: "/models/{model}:streamGenerateContent?alt=sse", ModelsEndpoint: "/models", CountTokensEndpoint: "/models/{model}:countTokens"},
}

// nonChatPatterns identifies live listing ids that are not text models
// (spec §5); the one list every listing filter reads, via IsChatModelID.
var nonChatPatterns = []string{"embedding", "whisper", "tts", "dall-e", "moderation", "audio", "transcribe", "image", "realtime", "davinci", "babbage", "sora"}

// IsChatModelID reports whether a live listing id names a text model.
func IsChatModelID(id string) bool {
	lower := strings.ToLower(id)
	if lower == "" {
		return false
	}
	for _, p := range nonChatPatterns {
		if strings.Contains(lower, p) {
			return false
		}
	}
	return true
}

// vertexGlobalOnly names the model families Vertex serves only from the
// global and us/eu endpoints, each with the regional limitation its warning
// states. Anthropic's regional endpoints stop at Sonnet 4.6 (spec §9.4);
// Gemini 3 and later are global-only today.
var vertexGlobalOnly = []vertexGlobalOnlyFamily{
	{detail: "supports Claude Sonnet 4.6 and earlier", patterns: []string{
		"claude-opus-4-7", "claude-opus-4-8", "claude-opus-5", "claude-sonnet-5", "claude-fable-5", "claude-mythos",
	}},
	{detail: "does not serve Gemini 3 or later", patterns: []string{"gemini-3"}},
}

// vertexGlobalOnlyFamily is one such family: the id substrings it covers and
// the regional limitation the warning spells out.
type vertexGlobalOnlyFamily struct {
	detail   string
	patterns []string
}

// vertexGlobalOnlyDetail returns the regional limitation for wireID when it
// names a family Vertex serves only from the global and us/eu endpoints.
func vertexGlobalOnlyDetail(wireID string) (string, bool) {
	for _, family := range vertexGlobalOnly {
		if slices.ContainsFunc(family.patterns, func(p string) bool { return familyCovers(p, wireID) }) {
			return family.detail, true
		}
	}
	return "", false
}

// familyCovers reports whether an id belongs to the family a pattern names:
// the pattern appears at a boundary — followed by the end of the id or by a
// separator — so "gemini-3" covers gemini-3.8-flash and gemini-3-pro-preview
// while a longer number (gemini-30) is a different family.
func familyCovers(pattern, wireID string) bool {
	if pattern == "" {
		return false
	}
	for start := 0; start+len(pattern) <= len(wireID); start++ {
		if !strings.HasPrefix(wireID[start:], pattern) {
			continue
		}
		if rest := wireID[start+len(pattern):]; rest == "" || strings.ContainsRune(".-:@", rune(rest[0])) {
			return true
		}
	}
	return false
}

// IsVertexGlobalOnly reports whether wireID names a model Vertex serves only
// from its global and us/eu endpoints — the same knowledge resolve uses for
// its regional warning, exported for callers that must tell a regional
// "not served here" apart from a genuine access failure.
func IsVertexGlobalOnly(wireID string) bool {
	_, ok := vertexGlobalOnlyDetail(wireID)
	return ok
}

var datedSuffixRe = regexp.MustCompile(`(-\d{8}(-v\d+(:\d+)?)?|@\d{8})$`)

// StripDatedSuffix removes a provider's dated-snapshot suffix from a model id
// ("claude-sonnet-4-5-20250929" → "claude-sonnet-4-5"), covering the trailing
// "-YYYYMMDD", its Bedrock "-vN:N" variant, and Vertex's "@YYYYMMDD". It is
// the rule lookupRow's dated step applies, exported for callers that compare
// a requested id against a provider-reported snapshot. An id carrying no such
// suffix — or one that is nothing but a suffix — comes back unchanged.
func StripDatedSuffix(id string) string {
	if s := datedSuffixRe.ReplaceAllString(id, ""); s != "" {
		return s
	}
	return id
}

const provAlias = "alias"

// ErrModelDisabled marks a Resolve naming a model the config layer disabled:
// the reference names a real row, but nothing may use it. Callers match it
// with errors.Is to tell "disabled by the user" from "unknown" and "hidden".
var ErrModelDisabled = errors.New("model is disabled in providers.toml")

// ParseRef splits "instance/model" on the first slash (spec §7.1); a bare
// model id yields an empty Instance.
func ParseRef(ref string) Ref {
	ref = strings.TrimSpace(ref)
	if i := strings.Index(ref, "/"); i > 0 {
		return Ref{Instance: ref[:i], Model: ref[i+1:]}
	}
	return Ref{Model: ref}
}

type liveListing struct{ rows map[string]Model }

// liveFacts keeps only what the live layer may supply (spec §5).
func liveFacts(m Model) Model {
	out := Model{ID: m.ID, WireID: m.ID, Caps: Caps{
		Tools: m.Caps.Tools, InputModalities: m.Caps.InputModalities, ContextWindow: m.Caps.ContextWindow,
		MaxInputTokens: m.Caps.MaxInputTokens, MaxOutputTokens: m.Caps.MaxOutputTokens, EffortValues: m.Caps.EffortValues, Cost: m.Caps.Cost, Reasoning: m.Caps.Reasoning,
		DefaultEffort: m.Caps.DefaultEffort,
	}}
	if m.Caps.ThinkingAlwaysOn != nil && *m.Caps.ThinkingAlwaysOn {
		out.Caps.ThinkingAlwaysOn = new(true)
	}
	return out
}

// ApplyLive records an instance's live listing. Non-chat ids are dropped and
// only advertised facts are kept; the listing replaces any previous one.
func (r *Registry) ApplyLive(instance string, rows []Model) {
	listing := liveListing{rows: map[string]Model{}}
	for _, m := range rows {
		if !IsChatModelID(m.ID) {
			continue
		}
		listing.rows[m.ID] = liveFacts(m)
	}
	r.liveMu.Lock()
	defer r.liveMu.Unlock()
	if r.live == nil {
		r.live = map[string]liveListing{}
	}
	r.live[instance] = listing
}

// SnapshotLive returns the cached live listings by instance, for carriers
// like a registry reload that must preserve them across a fresh object.
// ApplyLive restores each entry; the round trip keeps exactly the chat ids
// with their advertised facts.
func (r *Registry) SnapshotLive() map[string][]Model {
	r.liveMu.RLock()
	defer r.liveMu.RUnlock()
	out := make(map[string][]Model, len(r.live))
	for instance, listing := range r.live {
		rows := make([]Model, 0, len(listing.rows))
		for _, id := range sortedKeys(listing.rows) {
			rows = append(rows, listing.rows[id])
		}
		out[instance] = rows
	}
	return out
}

// LiveModels returns the cached live listing of an instance, sorted by id.
func (r *Registry) LiveModels(instance string) []Model {
	r.liveMu.RLock()
	defer r.liveMu.RUnlock()
	listing, ok := r.live[instance]
	if !ok {
		return nil
	}
	out := make([]Model, 0, len(listing.rows))
	for _, id := range sortedKeys(listing.rows) {
		out = append(out, listing.rows[id])
	}
	return out
}

func (r *Registry) liveRow(instance, id string) *Model {
	r.liveMu.RLock()
	defer r.liveMu.RUnlock()
	if m, ok := r.live[instance].rows[id]; ok {
		return &m
	}
	return nil
}

type lookupHit struct {
	rowID       string
	wireID      string
	step        string
	synthesized bool
}

// lookupRow is spec §7.2: exact row, region prefix stripped, dated suffix
// removed, live listing, else synthesized. Steps 1–2 use the row's wire id;
// the rest send the reference verbatim.
func (r *Registry) lookupRow(rec *record, model string) lookupHit {
	rows := rec.head.Models
	if m, ok := rows[model]; ok && !isGlob(model) {
		wire := m.WireID
		if wire == "" {
			wire = model
		}
		return lookupHit{rowID: model, wireID: wire, step: "row"}
	}
	if s := stripRegionPrefix(model); s != model {
		if _, ok := rows[s]; ok {
			return lookupHit{rowID: s, wireID: model, step: "region"}
		}
	}
	if s := StripDatedSuffix(model); s != model {
		if _, ok := rows[s]; ok {
			return lookupHit{rowID: s, wireID: model, step: "dated"}
		}
	}
	if r.liveRow(rec.name, model) != nil {
		return lookupHit{wireID: model, step: "live"}
	}
	return lookupHit{wireID: model, step: "synthesized", synthesized: true}
}

func exactRowIDs(rec *record) []string {
	var ids []string
	for id := range rec.head.Models {
		if !isGlob(id) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// Resolve is the single lookup path (spec §7): reference → instance record →
// row → layered caps → transport, headers, credential → derived caps.
func (r *Registry) Resolve(ref string) (Resolved, error) {
	pr := ParseRef(ref)
	if pr.Model == "" {
		return Resolved{}, fmt.Errorf("%q: empty model reference", ref)
	}
	var warnings []string
	if pr.Instance == "" {
		name, w, err := r.DefaultInstance()
		if err != nil {
			return Resolved{}, err
		}
		pr.Instance = name
		warnings = append(warnings, w...)
	}
	rec, ok := r.recordFor(pr.Instance)
	if !ok {
		return Resolved{}, r.unknownInstance(pr.Instance)
	}
	return r.resolveOn(rec, pr, warnings)
}

// unknownInstance reports a reference or ResolveInstance call naming an
// instance the registry does not have, listing the available ones.
func (r *Registry) unknownInstance(name string) error {
	names := make([]string, 0, len(r.instances))
	for _, inst := range r.rankedInstances() {
		names = append(names, inst.name)
	}
	return fmt.Errorf("unknown instance %q (available: %s)", name, strings.Join(names, ", "))
}

// resetLayerFields clears caps.Fields and purges any "Fields.*" provenance
// entries already recorded, for a layer that changes the record's declared
// protocol mid-chain (fold's cross-protocol rule, load.go). Shared by
// resolveOn and ResolveInstance so neither leaves a stale Fields provenance
// entry attributing a value to a layer whose contribution was wiped.
func resetLayerFields(caps *Caps, prov map[string]string) {
	caps.Fields = nil
	for k := range prov {
		if strings.HasPrefix(k, "Fields.") {
			delete(prov, k)
		}
	}
}

// ResolveInstance resolves an instance without a model: what a model-less
// call (ListModels, a credential probe) needs — protocol, transport,
// headers, credential, and the provider-level caps — with no row
// (spec §8.1: ListModels takes a Resolved). ModelID and WireID stay empty.
func (r *Registry) ResolveInstance(name string) (Resolved, error) {
	rec, ok := r.recordFor(name)
	if !ok {
		return Resolved{}, r.unknownInstance(name)
	}
	caps := Caps{}
	prov := map[string]string{}
	for _, layer := range rec.layers {
		if layer.resetFields {
			resetLayerFields(&caps, prov)
		}
		mergeCaps(&caps, layer.provider, layer.tag+"/provider", prov)
	}
	seedFields(&caps, rec.head.Protocol)
	transport, hostDerived, warnings := r.buildTransport(rec, Model{}, rec.head.Protocol)
	// rowID/ref "" keep firstPartyEndpoint's canonical resolution row-less
	// and glob-less, mirroring the row-less buildTransport call above -
	// correctly so: ListModels and credential probes, this path's only
	// callers, never build a body, so there is no WebSearch tool for a
	// row-level override to redirect. The base_url and endpoint-path
	// comparison still applies in full.
	if w := r.gateWebSearch(&caps, prov, rec, transport, rec.head.Protocol, "", "", ""); w != "" {
		warnings = append(warnings, w)
	}
	cred, credHeaders, cw := r.resolveCredentials(rec)
	warnings = append(warnings, cw...)
	if rec.head.Hidden {
		warnings = append(warnings, "hidden: provider has no resolvable base URL or protocol")
	}
	warnings = append(warnings, rec.notes...)
	providerID := rec.providerID
	if providerID == "" {
		providerID = rec.name
	}
	return Resolved{
		Instance: rec.name, ProviderID: providerID, Protocol: rec.head.Protocol, Surface: rec.head.Surface,
		Transport: transport, HostDerivedByRule: hostDerived, Caps: caps, Headers: r.buildHeaders(rec.head.Headers, nil),
		Credential: cred, CredentialHeaders: credHeaders, Provenance: prov, Warnings: warnings,
		ShadowedEnvVar: r.shadowedEnvVar(rec, cred),
		DefaultModel:   rec.head.DefaultModel, CheapModel: rec.head.CheapModel,
	}, nil
}

// webSearchExplicit reports whether prov attributes Caps.WebSearch to a
// deliberate, individually considered choice in the record's own
// providers.toml entry: an instance-wide setting (tag "config/provider")
// or an exact single-model override (`[providers.X.models."id"]`, tag
// "config/row"). A glob-matched value (tag "config/glob:<pattern>",
// whether from a top-level `[models."<glob>"]` or a provider-scoped
// `[providers.X.models."<glob>"]` - the tag does not distinguish them) does
// not count, even though it also originates in the user's own config: a
// pattern match was never considered against this specific instance or
// model, so trusting it would let a broad rule silently re-enable a
// stripped capability on an endpoint its author never looked at.
func webSearchExplicit(prov map[string]string) bool {
	tag := prov["WebSearch"]
	return tag == LayerConfig+"/provider" || tag == LayerConfig+"/row"
}

// gateWebSearch enforces that WebSearch - a platform-side capability, not a
// wire-protocol fact (spec §4.2) - survives only on a record reaching its
// provider's first-party endpoint (firstPartyEndpoint), unless the
// record's own config set it explicitly (webSearchExplicit), in which case
// that value always wins.
//
// A rejected WebSearch becomes an explicit false, never nil: every protocol
// adapter's own gate treats caps.WebSearch == nil as permissive ("no
// catalog opinion either way, trust the caller's own request flag"), so
// nil'ing a capability this gate means to deny is fail-open for any caller
// that does not separately re-derive it through BoolValue.
//
// The same fail-open reasoning covers a WebSearch no layer ever set: off
// the first-party endpoint a nil is normalized to an explicit false too,
// silently - no layer's value was overturned, so the warning and the
// provenance repoint stay reserved for a stripped true.
//
// The rewrite - prov's entry repointed at the gate, a warning returned -
// happens only when the gate is the reason the value is false: caps.WebSearch
// was true before it ran. When an earlier, non-config layer already set
// false for its own reason (amazon-bedrock's *anthropic.* row glob: the
// Messages endpoint simply lacks the capability), the gate agrees with the
// outcome but is not why it holds, so it leaves Provenance naming the real
// reason and returns no warning.
//
// Returns the warning naming why WebSearch was stripped; empty when
// nothing fired, including when the value was already false or only ever
// nil (the normalization is silent).
func (r *Registry) gateWebSearch(caps *Caps, prov map[string]string, rec *record, transport Transport, proto, rowID, ref, altID string) string {
	if webSearchExplicit(prov) || r.firstPartyEndpoint(rec, transport, proto, rowID, ref, altID) {
		return ""
	}
	if caps.WebSearch == nil {
		caps.WebSearch = new(false)
		return ""
	}
	if !*caps.WebSearch {
		return ""
	}
	caps.WebSearch = new(false)
	prov["WebSearch"] = "gate/first-party"
	// A from-scratch record has no curated provider id; the warning names
	// the instance itself there.
	name := rec.providerID
	if name == "" {
		name = rec.name
	}
	return fmt.Sprintf("web_search disabled: this endpoint is not %s's first-party API (set web_search = true on the instance to opt back in)", name)
}

func (r *Registry) resolveOn(rec *record, ref Ref, warnings []string) (Resolved, error) {
	res, err := r.resolveLayers(rec, ref, warnings)
	if err != nil {
		return Resolved{}, err
	}
	if BoolValue(res.Model.Disabled) {
		return Resolved{}, fmt.Errorf("%s/%s: %w (set by %s)", rec.name, ref.Model, ErrModelDisabled, res.Provenance["Disabled"])
	}
	return res, nil
}

// resolveLayers replays one reference on a record — layers, aliases, live
// facts, globs, derivation — without judging the row's Disabled flag. The
// gate lives with the callers: resolveOn turns the flag into an error for
// Resolve, while alias seeding needs the facts of a target a flag disables
// (a cross-provider alias resolves from a target whose own connection
// disabled it).
func (r *Registry) resolveLayers(rec *record, ref Ref, warnings []string) (Resolved, error) {
	hit := r.lookupRow(rec, ref.Model)
	if hit.synthesized && rec.head.Transport.Auth == AuthOAuthOpenAICodex {
		return Resolved{}, fmt.Errorf("%s/%s: unknown model on the Codex transport (valid: %s)", rec.name, ref.Model, strings.Join(exactRowIDs(rec), ", "))
	}
	prov := map[string]string{"model": hit.step}
	if hit.rowID != "" {
		prov["model"] = hit.step + ":" + hit.rowID
	}
	row := Model{ID: ref.Model, WireID: hit.wireID}
	if hit.rowID != "" {
		row = cloneModel(rec.head.Models[hit.rowID])
		row.WireID = hit.wireID
	}
	// Surface, Family, and Headers are rebuilt by the replay so glob rows
	// interleave with row entries in layer order.
	row.Surface, row.Family, row.Headers = "", "", nil

	caps := Caps{}
	altID := ""
	if hit.rowID != "" && hit.rowID != ref.Model {
		altID = hit.rowID
	}
	// canonicalRowID names the row the endpoint gate (gateWebSearch,
	// firstPartyEndpoint) compares transport against. It starts as the
	// resolved row's own id, but a same-provider alias that imports its
	// target's transport below is judged against the target's row instead
	// (base.head.Models has no entry under the alias's own id - a
	// user-chosen name, not a curated one - so looking it up there would
	// find nothing and fall back to a row-less, and wrong, baseline).
	canonicalRowID := hit.rowID
	// Layer 0: alias seeding (spec §4.2).
	aliasLockstep := false
	var aliasDisabled *bool
	var aliasDefault *bool
	if row.AliasOf != "" {
		target, same, err := r.resolveAliasTarget(rec, row.AliasOf)
		if err != nil {
			// A dangling alias stays a warning: the row resolves from what
			// it says itself.
			warnings = append(warnings, "dangling alias: "+err.Error())
		} else {
			seedFromAlias(&caps, &row, target, prov)
			if same {
				// Lockstep: a same-provider alias follows its target's
				// Disabled verdict, so its own exact-row and glob flags
				// never apply — a target the user disabled cannot resolve
				// through the alias at all. Remember the verdict now; the
				// replay below is reverted to it.
				if BoolValue(target.Model.Disabled) {
					return Resolved{}, fmt.Errorf("%s/%s: %w (alias of %s)", rec.name, ref.Model, ErrModelDisabled, row.AliasOf)
				}
				aliasLockstep = true
				aliasDisabled = clonePointer(target.Model.Disabled)
			} else {
				// A cross-provider alias carries its own flag on this
				// instance: the target's verdict is only the default, and
				// a flag the replay below finds overrides it, so this
				// connection can disable or re-enable the model alone.
				aliasDefault = clonePointer(target.Model.Disabled)
			}
			if same && rec.head.Models[hit.rowID].Protocol == "" && rec.head.Models[hit.rowID].Transport == nil {
				canonicalRowID = target.Model.ID
				row.Protocol = target.Model.Protocol
				if target.Model.Transport != nil {
					t := cloneTransport(*target.Model.Transport)
					row.Transport = &t
				}
				if row.Protocol != "" || row.Transport != nil {
					row.Hidden = false // spec §4.2: an alias import that supplies a transport un-hides the row
				}
			}
			if altID == "" {
				altID = target.Model.ID
			}
		}
	}
	rowProto := row.Protocol
	if rowProto == "" {
		rowProto = rec.head.Protocol
	}
	crossProto := rowProto != rec.head.Protocol

	seenTag := map[string]bool{}
	liveApplied := false
	// Missing top-level tags replay at their true layer positions,
	// interleaved with the present layers: snapshot extras before the
	// first layer, overlay extras before live and before config, config
	// extras after live. An overlay extra never lands after config
	// rows, so curated values cannot overwrite user config; a config
	// extra still lands after live, so user config wins over live
	// facts on implicit records too.
	layerOrder := map[string]int{LayerSnapshot: 0, LayerOverlay: 1, LayerConfig: 2}
	applyExtrasBefore := func(tag string) {
		if seenTag[tag] || len(r.topGlobs[tag]) == 0 {
			return
		}
		if tag == LayerConfig {
			return
		}
		r.applyGlobs(&caps, &row, r.topGlobs[tag], tag, ref.Model, altID, rowProto, crossProto, prov)
	}
	applyConfigExtras := func() {
		if seenTag[LayerConfig] || len(r.topGlobs[LayerConfig]) == 0 {
			return
		}
		r.applyGlobs(&caps, &row, r.topGlobs[LayerConfig], LayerConfig, ref.Model, altID, rowProto, crossProto, prov)
	}
	for _, layer := range rec.layers {
		// Missing tags that sort before this layer replay first.
		for _, tag := range []string{LayerSnapshot, LayerOverlay} {
			if !seenTag[tag] && layerOrder[tag] < layerOrder[layer.tag] {
				applyExtrasBefore(tag)
				seenTag[tag] = true
			}
		}
		if layer.tag == LayerConfig && !liveApplied {
			r.applyLive(&caps, rec, ref.Model, hit, prov)
			liveApplied = true
		}
		if layer.resetFields {
			resetLayerFields(&caps, prov)
		}
		pc := layer.provider
		if crossProto {
			pc.Fields = nil
		}
		mergeCaps(&caps, pc, layer.tag+"/provider", prov)
		if !seenTag[layer.tag] {
			seenTag[layer.tag] = true
			r.applyGlobs(&caps, &row, r.topGlobs[layer.tag], layer.tag, ref.Model, altID, rowProto, crossProto, prov)
		}
		r.applyGlobs(&caps, &row, layer.rows, layer.tag, ref.Model, altID, rowProto, crossProto, prov)
		if hit.rowID != "" {
			if lr, ok := layer.rows[hit.rowID]; ok {
				mergeCaps(&caps, lr.Caps, layer.tag+"/row", prov)
				applyRowScalars(&row, lr, layer.tag+"/row", prov)
			}
		}
	}
	// Missing snapshot/overlay tags that sort after every present
	// layer replay before live; missing config replays after live.
	for _, tag := range []string{LayerSnapshot, LayerOverlay} {
		if !seenTag[tag] {
			applyExtrasBefore(tag)
			seenTag[tag] = true
		}
	}
	if !liveApplied {
		r.applyLive(&caps, rec, ref.Model, hit, prov)
	}
	applyConfigExtras()
	seedFields(&caps, rowProto)
	if aliasLockstep {
		row.Disabled = aliasDisabled
		if aliasDisabled == nil {
			delete(prov, "Disabled")
		} else {
			prov["Disabled"] = provAlias
		}
	} else if aliasDefault != nil && row.Disabled == nil {
		// The cross-provider alias carries no flag of its own on this
		// instance, so the verdict it inherits from its target stands; a
		// flag the replay found already won.
		row.Disabled = aliasDefault
		prov["Disabled"] = provAlias
	}
	transport, hostDerived, tw := r.buildTransport(rec, row, rowProto)
	warnings = append(warnings, tw...)
	if w := r.gateWebSearch(&caps, prov, rec, transport, rowProto, canonicalRowID, ref.Model, altID); w != "" {
		warnings = append(warnings, w)
	}
	headers := r.buildHeaders(rec.head.Headers, row.Headers)
	cred, credHeaders, cw := r.resolveCredentials(rec)
	warnings = append(warnings, cw...)

	derive(&caps, &row, deriveInput{Protocol: rowProto, Synthesized: hit.synthesized, ProviderSurface: rec.head.Surface, ProviderFamily: rec.head.Family}, prov)

	if hit.synthesized {
		warnings = append(warnings, "model not in catalog")
	}
	if row.Hidden {
		warnings = append(warnings, "hidden: this provider does not serve this row")
	}
	if rec.head.Hidden {
		warnings = append(warnings, "hidden: provider has no resolvable base URL or protocol")
	}
	warnings = append(warnings, rec.notes...)
	// Only the endpoint the host rule derived from the location is regional
	// Vertex's to explain: a transport addressed to a route the config built is
	// somewhere else, and that location is not its problem.
	if loc, derived := VertexLocationDerived(transport, hostDerived); derived && loc != "global" && loc != "us" && loc != "eu" {
		if detail, known := vertexGlobalOnlyDetail(hit.wireID); known {
			warnings = append(warnings, fmt.Sprintf("regional Vertex location %q %s; use global, us, or eu for %s", loc, detail, hit.wireID))
		}
	}
	providerID := rec.providerID
	if providerID == "" {
		providerID = rec.name
	}
	return Resolved{
		Instance: rec.name, ProviderID: providerID, Protocol: rowProto, Surface: row.Surface, Transport: transport,
		HostDerivedByRule: hostDerived,
		ModelID:           ref.Model, WireID: hit.wireID, Model: row, Caps: caps, Headers: headers,
		Credential: cred, CredentialHeaders: credHeaders, Provenance: prov, Warnings: warnings,
		DefaultModel: rec.head.DefaultModel, CheapModel: rec.head.CheapModel, Synthesized: hit.synthesized,
	}, nil
}

// resolveAliasTarget resolves an alias target through the same machinery:
// a same-provider row on rec, else "provider-id/id" on the target's
// instance record when one exists (so user-layer flags like Disabled
// apply), else the curated record. aliasTargetRow applies the
// alias-target acceptance rules both resolve paths share: an exact
// non-alias row on the record, else a provider-id/id reference. A glob
// pattern never names a target, on either side of the slash.
func (r *Registry) aliasTargetRow(rec *record, aliasOf string) (*record, string, bool) {
	if m, ok := rec.head.Models[aliasOf]; ok && !isGlob(aliasOf) && m.AliasOf == "" {
		return rec, aliasOf, true
	}
	if i := strings.Index(aliasOf, "/"); i > 0 {
		id := aliasOf[i+1:]
		if isGlob(aliasOf[:i]) || isGlob(id) {
			return nil, "", false
		}
		if target, ok := r.recordFor(aliasOf[:i]); ok {
			if m, ok := target.head.Models[id]; ok && m.AliasOf == "" {
				return target, id, true
			}
		}
		// No explicit or implicit instance by that name: fall back to
		// the curated record, the way load-time aliasTarget validates.
		// recordFor already covers implicit curated ids, so this is
		// only the non-implicit curated remainder.
		if prov, ok := r.curated[aliasOf[:i]]; ok {
			if m, ok := prov.head.Models[id]; ok && m.AliasOf == "" {
				return prov, id, true
			}
		}
	}
	return nil, "", false
}

func (r *Registry) resolveAliasTarget(rec *record, aliasOf string) (Resolved, bool, error) {
	target, id, ok := r.aliasTargetRow(rec, aliasOf)
	if !ok {
		return Resolved{}, false, fmt.Errorf("alias_of %q does not name an existing non-alias row", aliasOf)
	}
	// The replay hands back the target's facts even when the target's own
	// flag disables it: a cross-provider alias seeds from that target either
	// way, and the caller refuses a same-provider one.
	res, err := r.resolveLayers(target, Ref{Instance: target.name, Model: id}, nil)
	return res, target == rec, err
}

// seedFromAlias copies the target's facts, surface, and family in as the
// alias row's layer 0 (spec §4.2).
func seedFromAlias(c *Caps, row *Model, target Resolved, prov map[string]string) {
	facts := Caps{
		ContextWindow: target.Caps.ContextWindow, MaxInputTokens: target.Caps.MaxInputTokens, MaxOutputTokens: target.Caps.MaxOutputTokens, Tools: target.Caps.Tools,
		StructuredOutput: target.Caps.StructuredOutput, Sampling: target.Caps.Sampling, Reasoning: target.Caps.Reasoning,
		ReasoningControls: target.Caps.ReasoningControls, EffortValues: target.Caps.EffortValues,
		DefaultEffort:   target.Caps.DefaultEffort,
		InputModalities: target.Caps.InputModalities, KnowledgeCutoff: target.Caps.KnowledgeCutoff, Cost: target.Caps.Cost,
	}
	mergeCaps(c, facts, provAlias, prov)
	if target.Surface != "" {
		row.Surface = target.Surface
		prov["Surface"] = provAlias
	}
	if target.Model.Family != "" {
		row.Family = target.Model.Family
		prov["Family"] = provAlias
	}
}

// orderedGlobKeys returns matching glob keys (target first) in spec §4.1 order:
// shorter patterns first, each pattern at most once. applyGlobs and modelDisabled
// share it so the two replays cannot disagree about matching order.
func orderedGlobKeys(rows map[string]Model, ref, altID string) []string {
	var globs []string
	for k := range rows {
		if isGlob(k) {
			globs = append(globs, k)
		}
	}
	globs = sortGlobs(globs)
	var out []string
	seen := map[string]bool{}
	for _, id := range []string{altID, ref} {
		if id == "" {
			continue
		}
		for _, g := range globs {
			if !seen[g] && matchGlob(g, id) {
				seen[g] = true
				out = append(out, g)
			}
		}
	}
	return out
}

// applyGlobs applies matching glob rows in spec §4.1 order: shorter
// patterns first, target-matching globs before reference-matching ones, each
// glob at most once. Cross-protocol rows take only Fields keys their own
// protocol knows.
func (r *Registry) applyGlobs(c *Caps, row *Model, rows map[string]Model, tag, ref, altID, rowProto string, crossProto bool, prov map[string]string) {
	if len(rows) == 0 {
		return
	}
	apply := func(g string) {
		gr := rows[g]
		gc := gr.Caps
		if crossProto && len(gc.Fields) > 0 {
			table := prunable[rowProto]
			filtered := map[string]bool{}
			for k, v := range gc.Fields {
				if _, ok := table[k]; ok {
					filtered[k] = v
				}
			}
			gc.Fields = filtered
		}
		mergeCaps(c, gc, tag+"/glob:"+g, prov)
		applyRowScalars(row, gr, tag+"/glob:"+g, prov)
	}
	for _, g := range orderedGlobKeys(rows, ref, altID) {
		apply(g)
	}
}

// applyRowScalars overlays a layer row's or glob row's scalars onto the
// resolved row. Replay order per layer is globs then the exact row, so within
// a layer the exact row wins and across layers the later layer wins (spec
// §4.1). A glob row's transport (the Codex `gpt-5.6*` row's `body` constants,
// spec §6.2) merges field-wise onto the row transport; glob rows carry
// neither a protocol nor a preset (the parser rejects both), so nothing here
// needs preset expansion.
func applyRowScalars(row *Model, src Model, tag string, prov map[string]string) {
	if src.Surface != "" {
		row.Surface = src.Surface
		prov["Surface"] = tag
	}
	if src.Family != "" {
		row.Family = src.Family
		prov["Family"] = tag
	}
	if len(src.Headers) > 0 {
		row.Headers = mergeStringMap(row.Headers, src.Headers)
	}
	if src.Disabled != nil {
		row.Disabled = clonePointer(src.Disabled)
		prov["Disabled"] = tag
	}
	if src.Transport != nil {
		if row.Transport == nil {
			row.Transport = &Transport{}
		} else {
			c := cloneTransport(*row.Transport)
			row.Transport = &c
		}
		mergeTransport(row.Transport, *src.Transport)
	}
}

// applyLive merges the instance's live facts for the reference (or the
// matched wire id) between the curated and user layers (spec §5).
func (r *Registry) applyLive(c *Caps, rec *record, model string, hit lookupHit, prov map[string]string) {
	lr := r.liveRow(rec.name, model)
	if lr == nil && hit.wireID != model {
		lr = r.liveRow(rec.name, hit.wireID)
	}
	if lr == nil {
		return
	}
	mergeCaps(c, lr.Caps, LayerLive, prov)
}

// transportShape assembles the unexpanded transport for rec resolving row on
// proto: the record's head transport under the cross-protocol rule, the row
// transport merged on top, and the protocol's default endpoints filled in.
// It is the shared front half of buildTransport and of the canonical
// first-party resolution (firstPartyEndpoint, instances.go), which expand
// it with different variable lookups.
func (r *Registry) transportShape(rec *record, row Model, proto string) Transport {
	t := cloneTransport(rec.head.Transport)
	if proto != rec.head.Protocol {
		clearProtocolTransport(&t)
	}
	if row.Transport != nil {
		mergeTransport(&t, *row.Transport)
	}
	d := protocolDefaults[proto]
	setIfEmpty := func(dst *string, def string) {
		if *dst == "" {
			*dst = def
		}
	}
	setIfEmpty(&t.Endpoint, d.Endpoint)
	setIfEmpty(&t.StreamEndpoint, d.StreamEndpoint)
	setIfEmpty(&t.ModelsEndpoint, d.ModelsEndpoint)
	setIfEmpty(&t.CountTokensEndpoint, d.CountTokensEndpoint)
	return t
}

// buildTransport applies the cross-protocol rule, the row transport, the
// protocol defaults, and variable substitution (spec §9.1). It also reports
// whether a host rule derived the base URL's authority (Resolved.HostDerivedByRule).
func (r *Registry) buildTransport(rec *record, row Model, proto string) (Transport, bool, []string) {
	t := r.transportShape(rec, row, proto)
	// The templates the URL is built from, captured before substitution, are
	// the authoritative list of variables Resolved may expose (URL parts such
	// as a region, resource, or project): the effective base URL template —
	// the row's own when it has one, else the provider's — and the endpoint
	// templates. Resolved is serialized by `evener models inspect`, so no
	// vars_env value that the URL does not use is ever exposed.
	baseURLTemplate := t.BaseURL
	templates := []string{baseURLTemplate, t.Endpoint, t.StreamEndpoint, t.ModelsEndpoint, t.CountTokensEndpoint}

	var warnings, varWarnings []string
	// One lookup serves the base URL, the endpoint templates, and the Vars
	// scan, so a user `vars` entry with an unset $ENV reference is reported
	// once however many templates read it (spec §10).
	lookup := r.varLookupWith(rec, t, func(w string) { varWarnings = append(varWarnings, w) })
	url, missing, hostWarnings := r.resolveBaseURLWith(rec, t, lookup)
	t.BaseURL = url
	warnings = append(warnings, hostWarnings...)
	for _, field := range []*string{&t.Endpoint, &t.StreamEndpoint, &t.ModelsEndpoint, &t.CountTokensEndpoint} {
		expanded, m := expandTemplate(*field, lookup)
		*field = expanded
		missing = append(missing, m...)
	}
	slices.Sort(missing)
	for _, name := range slices.Compact(missing) {
		varWarnings = append(varWarnings, "unresolved variable "+name)
	}
	slices.Sort(varWarnings)
	warnings = append(warnings, slices.Compact(varWarnings)...)
	resolved := map[string]string{}
	for _, tpl := range templates {
		for _, name := range templatePlaceholders(tpl) {
			if v, ok := lookup(name); ok {
				resolved[name] = v
			}
		}
	}
	// A host the vertex-location rule derived from the location used the
	// location as surely as a path placeholder would have, so it is exposed
	// too (and the regional warning reads it). A host supplied directly used
	// no location. Only a {GOOGLE_VERTEX_HOST} in the base URL's authority —
	// the part of the request URL that names the endpoint — makes the rule its
	// author, and only when the rule would accept the location: a location it
	// refuses leaves the placeholder unresolved and derives no endpoint.
	var hostDerived bool
	if t.HostRule == HostRuleVertexLocation && authorityVars(baseURLTemplate)["GOOGLE_VERTEX_HOST"] {
		_, supplied := lookup("GOOGLE_VERTEX_HOST")
		loc, located := lookup("GOOGLE_VERTEX_LOCATION")
		hostDerived = !supplied && located && validVertexLocation(loc)
		if hostDerived {
			resolved["GOOGLE_VERTEX_LOCATION"] = loc
		}
	}
	t.Vars = resolved
	return t, hostDerived, warnings
}

// templatePlaceholders names the {VARIABLE} placeholders a URL template reads.
func templatePlaceholders(tpl string) []string {
	matches := placeholderRe.FindAllStringSubmatch(tpl, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out
}

// resolveCredentials expands a record's credential fields once per
// resolution: the Authorization header's expansion feeds both the credential
// and the header map, so a failing command runs once (a second run could
// also race the first and split the outcome between credential and header).
func (r *Registry) resolveCredentials(rec *record) (Credential, map[string]string, []string) {
	authKey, auth, authOK, unresolved := r.authorization(rec)
	cred, cw := r.credentialWithAuth(rec, auth, authOK, unresolved)
	credHeaders, hw := r.expandCredentialHeaders(rec.head.CredentialHeaders, authKey, auth, authOK, unresolved)
	return cred, credHeaders, append(cw, hw...)
}

// expandCredentialHeaders builds the resolved credential-header map. The
// key authHeaderKey resolved — the Authorization header, whatever case its
// author wrote — arrives pre-expanded from resolveCredentials, so its
// command expressions run once per resolution; every other header expands
// here. A header that expands to no credential material (an empty value or
// a bare auth scheme word) is not present — it drops with a warning naming
// it — while a raw-empty value is spec §10's removal of an inherited header
// and stays silent.
func (r *Registry) expandCredentialHeaders(headers map[string]string, authKey, auth string, authOK bool, authUnresolved []valueexpr.Unresolved) (map[string]string, []string) {
	credHeaders := map[string]string{}
	var warnings []string
	for k, v := range headers {
		if v == "" {
			continue
		}
		if authOK && k == authKey {
			switch {
			case len(authUnresolved) > 0:
				warnings = append(warnings, fmt.Sprintf("credential header %q: %s", k, missingReason(authUnresolved)))
			case auth == "":
				warnings = append(warnings, fmt.Sprintf("credential header %q: expands to an empty value", k))
			case schemeOnly(auth):
				warnings = append(warnings, fmt.Sprintf("credential header %q: expands to nothing but an auth scheme word", k))
			default:
				credHeaders[k] = auth
			}
			continue
		}
		if e, missing := expandEnv(v, r.env); len(missing) == 0 && e != "" {
			credHeaders[k] = e
		} else if len(missing) > 0 {
			warnings = append(warnings, fmt.Sprintf("credential header %q: %s", k, missingReason(missing)))
		} else {
			warnings = append(warnings, fmt.Sprintf("credential header %q: expands to an empty value", k))
		}
	}
	return credHeaders, warnings
}

// buildHeaders merges header layers and applies spec §10: an unset $VAR
// drops the header; an empty value removes an inherited header.
func (r *Registry) buildHeaders(layers ...map[string]string) map[string]string {
	merged := map[string]string{}
	for _, layer := range layers {
		maps.Copy(merged, layer)
	}
	out := map[string]string{}
	for k, v := range merged {
		if v == "" {
			continue
		}
		// A header whose reference or command resolves to nothing drops out
		// silently by design: display headers carry no credential, so there is
		// no auth failure to explain — unlike expandCredentialHeaders, which
		// warns for exactly that reason.
		expanded, missing := expandEnv(v, r.env)
		if len(missing) > 0 || expanded == "" {
			continue
		}
		out[k] = expanded
	}
	return out
}

// FindModel lists the instances that serve a model id, in default ranking
// (spec §7.5). It never performs network I/O.
func (r *Registry) FindModel(id string) []Ref {
	var out []Ref
	for _, inst := range r.rankedInstances() {
		if hit := r.lookupRow(inst.rec, id); !hit.synthesized {
			if r.recordMayDisable(inst.rec) && r.modelDisabled(inst.rec, Ref{Instance: inst.name, Model: id}, hit) {
				continue
			}
			out = append(out, Ref{Instance: inst.name, Model: id})
		}
	}
	return out
}

// modelDisabled replays just the Disabled flag for a reference: every
// matching glob in spec §4.1 order (via orderedGlobKeys, the same order
// applyGlobs replays) then the exact row per layer, later layers after
// earlier ones, so the last writer wins — the same outcome resolveOn's full
// replay reaches. Alias rows follow the same rules as there: a same-provider
// alias takes its target's verdict outright, and a cross-provider alias
// takes it as the default its own flag overrides. Cheap enough for browse
// paths like FindModel that must not pay for a full resolve per candidate.
func (r *Registry) modelDisabled(rec *record, ref Ref, hit lookupHit) bool {
	altID := ""
	if hit.rowID != "" && hit.rowID != ref.Model {
		altID = hit.rowID
	}
	// A cross-provider alias carries its own flag on this instance, so the
	// verdict it inherits from its target is only the default: it stands when
	// the alias's own rows set nothing in the replay below. Globs replay
	// against the reference and the target's row id the way resolveOn's
	// altID does, so a provider-scoped glob written for the model the alias
	// names applies here too and the two answers cannot drift.
	var inherited *bool
	if hit.rowID != "" {
		if aliasOf := rec.head.Models[hit.rowID].AliasOf; aliasOf != "" {
			if v, same, targetID, ok := r.aliasTargetVerdict(rec, aliasOf); ok {
				if same {
					return v
				}
				inherited = &v
				if altID == "" {
					altID = targetID
				}
			}
		}
	}
	var disabled, set bool
	// Mirror resolveOn's interleave: missing tags replay at their true
	// layer positions (snapshot → overlay → config), never bunched
	// after the loop — so a curated overlay Disabled value cannot
	// overwrite user config here either, and FindModel agrees with
	// Resolve.
	seenTag := map[string]bool{}
	applyTop := func(tag string) {
		for _, g := range orderedGlobKeys(r.topGlobs[tag], ref.Model, altID) {
			if d := r.topGlobs[tag][g].Disabled; d != nil {
				disabled, set = *d, true
			}
		}
	}
	layerOrder := map[string]int{LayerSnapshot: 0, LayerOverlay: 1, LayerConfig: 2}
	for _, layer := range rec.layers {
		// Missing tags that sort before this layer replay first, at
		// their true positions — never bunched after config rows.
		for _, tag := range []string{LayerSnapshot, LayerOverlay, LayerConfig} {
			if !seenTag[tag] && layerOrder[tag] < layerOrder[layer.tag] && len(r.topGlobs[tag]) > 0 {
				seenTag[tag] = true
				applyTop(tag)
			}
		}
		if !seenTag[layer.tag] {
			seenTag[layer.tag] = true
			applyTop(layer.tag)
		}
		for _, g := range orderedGlobKeys(layer.rows, ref.Model, altID) {
			if d := layer.rows[g].Disabled; d != nil {
				disabled, set = *d, true
			}
		}
		if hit.rowID != "" {
			if lr, ok := layer.rows[hit.rowID]; ok && lr.Disabled != nil {
				disabled, set = *lr.Disabled, true
			}
		}
	}
	for _, tag := range []string{LayerSnapshot, LayerOverlay, LayerConfig} {
		if !seenTag[tag] && len(r.topGlobs[tag]) > 0 {
			seenTag[tag] = true
			applyTop(tag)
		}
	}
	if !set && inherited != nil {
		return *inherited
	}
	return disabled
}

// aliasTargetVerdict reports the Disabled verdict an alias inherits from its
// target — the target row's own effective replay — and whether that target
// lives on the same record. A same-provider alias follows the verdict
// outright; a cross-provider one treats it as the default its own flag
// overrides. targetID names the target row, which the caller needs to match
// the same globs the full replay does. It answers ok=false for a dangling
// alias, whose own replay then applies as before. Acceptance matches
// resolveAliasTarget (an exact non-alias row, same provider or
// provider-id/id) but without paying for a full resolve.
func (r *Registry) aliasTargetVerdict(rec *record, aliasOf string) (disabled, same bool, targetID string, ok bool) {
	target, id, ok := r.aliasTargetRow(rec, aliasOf)
	if !ok {
		return false, false, "", false
	}
	return r.modelDisabled(target, Ref{Instance: target.name, Model: id}, lookupHit{rowID: id, wireID: id, step: "row"}), target == rec, id, true
}

// AliasTarget resolves a model id to the row a toggle writes: the id
// itself, unless it names an alias — a same-provider alias's flag lives on
// its target, while a cross-provider alias carries its own row on this
// instance — or a region-prefixed / dated-suffix variant (then the canonical
// row the variant resolves to — one logical model, one toggleable row). A
// glob id, a dangling alias, an unknown model, and an unknown instance are
// errors.
func (r *Registry) AliasTarget(instance, model string) (Ref, error) {
	rec, ok := r.recordFor(instance)
	if !ok {
		return Ref{}, r.unknownInstance(instance)
	}
	if isGlob(model) {
		return Ref{}, fmt.Errorf("model %q is a glob: the sheet toggles exact rows only", model)
	}
	if m, ok := rec.head.Models[model]; ok && m.AliasOf != "" {
		return r.aliasTargetRef(rec, model, m)
	}
	hit := r.lookupRow(rec, model)
	if hit.synthesized {
		return Ref{}, fmt.Errorf("model %q is not a known model of instance %q", model, instance)
	}
	// A region-prefixed or dated-suffix variant resolves to its canonical
	// row: the toggle writes that row, so one logical model keeps one
	// toggleable row instead of authoring a new exact row per variant.
	// When that canonical row is itself an alias, the write routes through
	// the same decision the exact-alias branch above makes: a same-provider
	// alias writes its target, while a cross-provider one writes its own
	// row here. Live-only ids carry no rowID; they fall through to the
	// verbatim reference the steps above already validated.
	if hit.rowID != "" {
		if m, ok := rec.head.Models[hit.rowID]; ok && m.AliasOf != "" {
			return r.aliasTargetRef(rec, hit.rowID, m)
		}
		return Ref{Instance: rec.name, Model: hit.rowID}, nil
	}
	return Ref{Instance: rec.name, Model: model}, nil
}

// aliasTargetRef resolves one alias row to the Ref a toggle writes. A
// same-provider alias writes its target row: one connection, one model, one
// flag (lockstep). A cross-provider alias writes the alias's own row on this
// instance instead — the config layer cannot author the target's record, and
// the read path lets that flag override the verdict inherited from the
// target — so each connection carries its own choice. rowID names the alias
// row itself, which a region-prefixed or dated-suffix spelling reaches
// without matching it.
func (r *Registry) aliasTargetRef(rec *record, rowID string, alias Model) (Ref, error) {
	target, id, ok := r.aliasTargetRow(rec, alias.AliasOf)
	if !ok {
		return Ref{}, fmt.Errorf("alias_of %q does not name an existing non-alias row", alias.AliasOf)
	}
	if target != rec {
		return Ref{Instance: rec.name, Model: rowID}, nil
	}
	return Ref{Instance: rec.name, Model: id}, nil
}

// recordMayDisable reports whether any layer of rec or the top-level glob
// rows set Disabled at all — or any alias row names a cross-provider
// target that may itself be disabled. Browse paths check this once before
// replaying per row: with no flag anywhere every answer is false. The
// cross-provider walk follows aliasTargetRow's acceptance (an exact
// non-alias row) with a visited set, so a mutual alias cycle terminates
// instead of overflowing the stack.
func (r *Registry) recordMayDisable(rec *record) bool {
	return r.recordMayDisableSeen(rec, map[*record]bool{})
}

func (r *Registry) recordMayDisableSeen(rec *record, seen map[*record]bool) bool {
	if seen[rec] {
		return false
	}
	seen[rec] = true
	for _, layer := range rec.layers {
		for _, m := range layer.rows {
			if m.Disabled != nil {
				return true
			}
		}
	}
	for _, rows := range r.topGlobs {
		for _, m := range rows {
			if m.Disabled != nil {
				return true
			}
		}
	}
	for _, m := range rec.head.Models {
		if m.AliasOf == "" {
			continue
		}
		if i := strings.Index(m.AliasOf, "/"); i > 0 {
			id := m.AliasOf[i+1:]
			if isGlob(m.AliasOf[:i]) || isGlob(id) {
				continue
			}
			target, ok := r.recordFor(m.AliasOf[:i])
			if !ok || target == rec || seen[target] {
				continue
			}
			row, ok := target.head.Models[id]
			if !ok || row.AliasOf != "" {
				continue
			}
			if r.recordMayDisableSeen(target, seen) {
				return true
			}
		}
	}
	return false
}

// InstanceModels lists an instance's known models with their effective
// disabled state, sorted by id, for the Providers pane's per-model toggles:
// exact catalog rows plus cached live ids (the same set ModelIDs lists),
// alias rows included.
//
// An alias row names a model the provider serves under another id (the Codex
// family aliases gpt-5.6-sol/terra/luna onto openai's rows), and the picker
// offers those names - so the list of what can be toggled has to carry them
// too. Every listed row is toggleable: AliasTarget names the row a toggle
// writes. A dangling alias — the target is gone, so load hides the row —
// stays out, because no row a toggle could write exists for it. A
// same-provider alias writes its target, so every spelling of one model on
// one connection shares one flag. A cross-provider alias writes its own row
// on this instance instead, so each connection carries its own choice; the
// row below reports what that choice comes to (modelDisabled): the target's
// verdict is the default, and a flag on the alias's own row overrides it.
//
// A toggle on a live-only id authors an exact config row, which precedes live
// lookup, so the exception takes effect.
func (r *Registry) InstanceModels(instance string) ([]InstanceModel, error) {
	rec, ids, err := r.instanceRecordIDs(instance)
	if err != nil {
		return nil, err
	}
	out := make([]InstanceModel, 0, len(ids))
	mayDisable := r.recordMayDisable(rec)
	for _, id := range ids {
		hit := r.lookupRow(rec, id)
		if hit.rowID != "" {
			if aliasOf := rec.head.Models[hit.rowID].AliasOf; aliasOf != "" {
				if _, _, ok := r.aliasTargetRow(rec, aliasOf); !ok {
					// A dangling alias — load hides the row, with a warning —
					// names nothing a toggle could write: AliasTarget refuses
					// it, so listing it would render a switch that can only
					// fail. The row is not a model the provider serves, so it
					// stays out of the inventory.
					continue
				}
			}
		}
		disabled := false
		if mayDisable {
			disabled = r.modelDisabled(rec, Ref{Instance: instance, Model: id}, hit)
		}
		out = append(out, InstanceModel{ID: id, Disabled: disabled})
	}
	return out, nil
}

// instanceRecordIDs resolves an instance to its record plus its known
// model ids (exact catalog rows plus cached live ids, sorted) — the
// prologue InstanceModels and ModelIDs share.
func (r *Registry) instanceRecordIDs(instance string) (*record, []string, error) {
	rec, ok := r.recordFor(instance)
	if !ok {
		return nil, nil, fmt.Errorf("unknown instance %q", instance)
	}
	return rec, modelIDs(rec, r.LiveModels(instance)), nil
}

// ModelIDs lists an instance's exact catalog rows plus its cached live ids,
// sorted (for `evener models list`).
func (r *Registry) ModelIDs(instance string) ([]string, error) {
	_, ids, err := r.instanceRecordIDs(instance)
	return ids, err
}

// CatalogModelIDs lists a curated provider's exact catalog rows plus its
// cached live ids, sorted. It needs no instance, so `evener models list`
// can show a provider nobody has configured (spec §11.1's --all).
func (r *Registry) CatalogModelIDs(id string) ([]string, error) {
	rec, ok := r.curated[id]
	if !ok {
		return nil, fmt.Errorf("unknown provider %q", id)
	}
	return modelIDs(rec, r.LiveModels(id)), nil
}

func modelIDs(rec *record, live []Model) []string {
	seen := map[string]bool{}
	for _, id := range exactRowIDs(rec) {
		seen[id] = true
	}
	for _, m := range live {
		seen[m.ID] = true
	}
	return sortedKeys(seen)
}

// ResolveCatalog resolves a model against a curated provider record
// directly, for listings that browse the catalog rather than name an
// instance. An id that is not an instance still resolves, with a warning
// saying so; Resolve stays strict (spec §5.2).
func (r *Registry) ResolveCatalog(id, model string) (Resolved, error) {
	rec, ok := r.curated[id]
	if !ok {
		return Resolved{}, fmt.Errorf("unknown provider %q", id)
	}
	var warnings []string
	if _, ok := r.instances[id]; !ok {
		warnings = append(warnings, "not an instance: add a [providers."+id+"] entry or export its key")
	}
	return r.resolveOn(rec, Ref{Instance: id, Model: model}, warnings)
}
