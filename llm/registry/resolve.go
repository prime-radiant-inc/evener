package registry

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"
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

// vertexGlobalOnly lists Claude ids Anthropic serves only from the global
// and us/eu endpoints (spec §9.4: "regional endpoints support Sonnet 4.6
// and earlier").
var vertexGlobalOnly = []string{"claude-opus-4-7", "claude-opus-4-8", "claude-opus-5", "claude-sonnet-5", "claude-fable-5", "claude-mythos"}

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
	transport, warnings := r.buildTransport(rec, Model{}, rec.head.Protocol)
	// rowID/ref "" keep firstPartyEndpoint's canonical resolution row-less
	// and glob-less, mirroring the row-less buildTransport call above -
	// correctly so: ListModels and credential probes, this path's only
	// callers, never build a body, so there is no WebSearch tool for a
	// row-level override to redirect. The base_url and endpoint-path
	// comparison still applies in full.
	if w := r.gateWebSearch(&caps, prov, rec, transport, rec.head.Protocol, "", "", ""); w != "" {
		warnings = append(warnings, w)
	}
	cred, cw := r.credential(rec)
	warnings = append(warnings, cw...)
	credHeaders := map[string]string{}
	for k, v := range rec.head.CredentialHeaders {
		if e, missing := expandEnv(v, r.env); len(missing) == 0 && e != "" {
			credHeaders[k] = e
		}
	}
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
		Transport: transport, Caps: caps, Headers: r.buildHeaders(rec.head.Headers, nil),
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
	if row.AliasOf != "" {
		target, same, err := r.resolveAliasTarget(rec, row.AliasOf)
		if err != nil {
			warnings = append(warnings, "dangling alias: "+err.Error())
		} else {
			seedFromAlias(&caps, &row, target, prov)
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
	for _, layer := range rec.layers {
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
	if !liveApplied {
		r.applyLive(&caps, rec, ref.Model, hit, prov)
	}
	seedFields(&caps, rowProto)

	transport, tw := r.buildTransport(rec, row, rowProto)
	warnings = append(warnings, tw...)
	if w := r.gateWebSearch(&caps, prov, rec, transport, rowProto, canonicalRowID, ref.Model, altID); w != "" {
		warnings = append(warnings, w)
	}
	headers := r.buildHeaders(rec.head.Headers, row.Headers)
	cred, cw := r.credential(rec)
	warnings = append(warnings, cw...)
	credHeaders := map[string]string{}
	for k, v := range rec.head.CredentialHeaders {
		if e, missing := expandEnv(v, r.env); len(missing) == 0 && e != "" {
			credHeaders[k] = e
		}
	}

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
	if transport.HostRule == HostRuleVertexLocation {
		if loc, ok := r.varLookup(rec, transport)("GOOGLE_VERTEX_LOCATION"); ok && loc != "global" && loc != "us" && loc != "eu" {
			for _, p := range vertexGlobalOnly {
				if strings.Contains(hit.wireID, p) {
					warnings = append(warnings, fmt.Sprintf("regional Vertex location %q supports Claude Sonnet 4.6 and earlier; use global, us, or eu for %s", loc, hit.wireID))
					break
				}
			}
		}
	}
	providerID := rec.providerID
	if providerID == "" {
		providerID = rec.name
	}
	return Resolved{
		Instance: rec.name, ProviderID: providerID, Protocol: rowProto, Surface: row.Surface, Transport: transport,
		ModelID: ref.Model, WireID: hit.wireID, Model: row, Caps: caps, Headers: headers,
		Credential: cred, CredentialHeaders: credHeaders, Provenance: prov, Warnings: warnings,
		DefaultModel: rec.head.DefaultModel, CheapModel: rec.head.CheapModel, Synthesized: hit.synthesized,
	}, nil
}

// resolveAliasTarget resolves an alias target through the same machinery:
// a same-provider row on rec, else "provider-id/id" on the curated record.
func (r *Registry) resolveAliasTarget(rec *record, aliasOf string) (Resolved, bool, error) {
	if m, ok := rec.head.Models[aliasOf]; ok && !isGlob(aliasOf) && m.AliasOf == "" {
		res, err := r.resolveOn(rec, Ref{Instance: rec.name, Model: aliasOf}, nil)
		return res, true, err
	}
	if i := strings.Index(aliasOf, "/"); i > 0 {
		if prov, ok := r.curated[aliasOf[:i]]; ok {
			if m, ok := prov.head.Models[aliasOf[i+1:]]; ok && !isGlob(aliasOf[i+1:]) && m.AliasOf == "" {
				res, err := r.resolveOn(prov, Ref{Instance: prov.name, Model: aliasOf[i+1:]}, nil)
				return res, false, err
			}
		}
	}
	return Resolved{}, false, fmt.Errorf("alias_of %q does not name an existing non-alias row", aliasOf)
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

// applyGlobs applies matching glob rows in spec §4.1 order: shorter
// patterns first, target-matching globs before reference-matching ones, each
// glob at most once. Cross-protocol rows take only Fields keys their own
// protocol knows.
func (r *Registry) applyGlobs(c *Caps, row *Model, rows map[string]Model, tag, ref, altID, rowProto string, crossProto bool, prov map[string]string) {
	var globs []string
	for k := range rows {
		if isGlob(k) {
			globs = append(globs, k)
		}
	}
	if len(globs) == 0 {
		return
	}
	globs = sortGlobs(globs)
	applied := map[string]bool{}
	apply := func(g string) {
		applied[g] = true
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
	if altID != "" {
		for _, g := range globs {
			if matchGlob(g, altID) {
				apply(g)
			}
		}
	}
	for _, g := range globs {
		if !applied[g] && matchGlob(g, ref) {
			apply(g)
		}
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
// protocol defaults, and variable substitution (spec §9.1).
func (r *Registry) buildTransport(rec *record, row Model, proto string) (Transport, []string) {
	t := r.transportShape(rec, row, proto)
	// The templates, captured before substitution, are the authoritative list
	// of variables Resolved may expose (URL parts such as a region, resource,
	// or project). Resolved is serialized by `evener models inspect`, so no
	// vars_env value that a template does not reference is ever exposed.
	templates := []string{rec.head.Transport.BaseURL, t.Endpoint, t.StreamEndpoint, t.ModelsEndpoint, t.CountTokensEndpoint}
	if row.Transport != nil && row.Transport.BaseURL != "" {
		templates = append(templates, row.Transport.BaseURL)
	}

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
		for _, m := range placeholderRe.FindAllStringSubmatch(tpl, -1) {
			if v, ok := lookup(m[1]); ok {
				resolved[m[1]] = v
			}
		}
	}
	t.Vars = resolved
	return t, warnings
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
			out = append(out, Ref{Instance: inst.name, Model: id})
		}
	}
	return out
}

// ModelIDs lists an instance's exact catalog rows plus its cached live ids,
// sorted (for `evener models list`).
func (r *Registry) ModelIDs(instance string) ([]string, error) {
	rec, ok := r.recordFor(instance)
	if !ok {
		return nil, fmt.Errorf("unknown instance %q", instance)
	}
	return modelIDs(rec, r.LiveModels(instance)), nil
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
