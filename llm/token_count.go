package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"image"
	"math"
	"os"
	"strings"

	"primeradiant.com/evener/llm/registry"

	// Registered for their image.DecodeConfig side effects: media token
	// estimation decodes inline image bytes to read width/height.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
)

const fallbackMediaTokens = 256

// TokenCountSource identifies how an InputTokenCount was produced.
type TokenCountSource string

const (
	// TokenCountSourceLocalEstimate marks counts computed by the local,
	// deterministic estimator rather than the provider.
	TokenCountSourceLocalEstimate TokenCountSource = "local_estimate"
	// TokenCountSourceProvider marks counts returned by the provider's exact
	// token-counting endpoint.
	TokenCountSourceProvider TokenCountSource = "provider"
)

// ErrInputTokenCountUnsupported is returned by an InputTokenCounter when the
// provider has no exact token-counting support, signaling callers to fall back
// to the local estimate.
var ErrInputTokenCountUnsupported = errors.New("input token count unsupported")

// InputTokenCount reports input tokens for a request. Exact=false means the
// count is a local estimate; exact provider responses and normal post-call
// Usage remain authoritative.
type InputTokenCount struct {
	Tokens   int              `json:"tokens"`
	Exact    bool             `json:"exact"`
	Source   TokenCountSource `json:"source"`
	Provider string           `json:"provider,omitempty"`
	Model    string           `json:"model,omitempty"`
	Raw      map[string]any   `json:"raw,omitempty"`
}

// InputTokenCounter is implemented by adapters that support provider-side
// preflight token counting for full llm.Request inputs.
type InputTokenCounter interface {
	CountInputTokens(ctx context.Context, req Request) (InputTokenCount, error)
}

// EstimateInputTokens returns a deterministic local estimate for a request.
// It never counts inline media bytes as text.
//
// The unsigned-thinking rule is decided from the provider and model names,
// because a bare request carries no resolved row. Callers that have resolved
// the request's target should use EstimateInputTokensForResolved instead: the
// name rule cannot recognize a curated OpenAI-compatible chat provider whose
// name carries no marker, nor an instance alias, and both replay unsigned
// thinking text.
func EstimateInputTokens(req Request) InputTokenCount {
	return estimateInputTokens(targetFromNames(req.Provider, req.Model), req)
}

// EstimateInputTokensForResolved returns the local estimate with the
// unsigned-thinking rule decided by the target's resolved row — its protocol
// and reasoning capabilities — rather than by name. Callers that have resolved
// the request's target should prefer this.
func EstimateInputTokensForResolved(res registry.Resolved, req Request) InputTokenCount {
	return estimateInputTokens(targetFromResolved(res, req.Provider, req.Model), req)
}

func estimateInputTokens(t targetInfo, req Request) InputTokenCount {
	options, _ := req.ProviderOptions[registry.ProtocolOpenAIChat].(map[string]any)
	if _, ok := options["reasoning"]; ok && !t.reasoningOff {
		t.chatUsesDetails = true
	}
	tokens := estimateMessagesInputTokens(t, req.Messages)
	tokens += req.billedToolJSONBytes() / 4
	if req.ResponseFormat != nil {
		b, _ := json.Marshal(req.ResponseFormat)
		tokens += len(b) / 4
	}
	return InputTokenCount{
		Tokens:   tokens,
		Exact:    false,
		Source:   TokenCountSourceLocalEstimate,
		Provider: req.Provider,
		Model:    req.Model,
	}
}

// billedToolJSONBytes is the tool-catalog length the local estimate bills req
// for, or 0 when the request sends no tools.
//
// A request with no catalog can still send tools: the Chat Completions and
// standard Responses builders write the request's own tools and then copy a
// ProviderOptions tools value over the body, and a pinned "tools" transport
// constant is applied after both and overrides the option, so the constant is
// the whole tool list on such a route. Nothing else on such a request names
// those bytes, so only a recorded replacement price describes them. A
// request with neither a catalog nor a replacement keeps the zero charge it has
// always had.
func (req Request) billedToolJSONBytes() int {
	if len(req.Tools) > 0 {
		if req.ToolsJSONBytes > 0 {
			return req.ToolsJSONBytes
		}
		return ToolsJSONBytes(req.Tools)
	}
	if req.emittedToolReplacement && req.recordsEmission() {
		return req.emittedToolCount
	}
	return 0
}

// priceEmittedToolSchemas replaces req's tool count with the price of the list
// the route's own builder will emit.
//
// A builder changes a tool schema before sending it: a closed value set gains
// the explicit type its provider requires, a parameterless tool gains a
// synthesized schema, and a strict-capable route closes an empty one. Pricing
// the definitions as they were received therefore prices a list other than the
// one that dispatches, and near the input cap that difference decides admission
// in both directions: a request can be admitted over budget, or refused while it
// fits.
//
// The protocol owns the answer because the protocol owns the builder, so this
// asks it rather than restating its rules (ToolSchemaDeriver). A count is reused
// when the builder changes nothing, which is the common case: a catalog that
// already carries every type and no parameterless tool prices identically before
// and after, and re-measuring it every dispatch would repeat a marshal the
// cached count exists to avoid.
//
// Assigning the measurement rather than adjusting the incoming count is what
// makes this idempotent. A count measured over the substituted form is already
// close to the emitted one, so adjusting it by the difference between the two
// lists looks like it reuses work — but the adjustment describes the count it
// started from, and a second call over the result starts from the emitted count
// and adds the same difference again. Middleware may price a prepared request
// and forward it, and admission then prices it once more; a second application
// of a difference is a count larger than the list either of them sends, and an
// input cap between the two refuses a request that fits. Measuring once and
// assigning cannot compound.
//
// A count produced for one target is not a count for another. The same request
// can be priced against a strict Responses row and then reused on a non-strict
// Chat route, and the second route's decision may leave the definitions
// unchanged — the case a deriver reports as "nothing to change" and which would
// then leave the first route's charge standing for a schema this one never
// emits. So the target and the unprojected baseline are recorded with the count,
// and a price for a different target starts from the baseline rather than from
// another target's answer.
func priceEmittedToolSchemas(req Request, res registry.Resolved) Request {
	// Retargeting starts from the list, not from the previous route's emission.
	// The baseline is the count the serialized list measures on its own, so
	// restoring it is exact rather than approximate.
	//
	// This runs before every branch that can return early, because each of them
	// leaves the request priced by whatever answered last. Returning before the
	// restore would keep a count produced for a different target — an inflated
	// local reading, or a request that fits this route rejected by a cap that
	// belonged to the other one.
	req = restoreToolCountBaseline(req, res)
	// The replacement providerToolsReplacement reports — a pinned "tools"
	// transport constant, else the ProviderOptions value — is the list the route
	// sends instead of the catalog the builder projected, so it is the list to
	// price. It is read before the empty-catalog check below: that value is the
	// whole tool list for a request whose catalog is empty, and returning there
	// would price no tools at all on a request that dispatches them.
	if replacement, ok := providerToolsReplacement(req, res); ok {
		return priceReplacementToolList(req, res, replacement)
	}
	if len(req.Tools) == 0 {
		return req
	}
	// A count this target's builder already produced is the answer. Re-deriving
	// the emission and re-marshaling the catalog would repeat exactly the work
	// the recorded count exists to avoid.
	if !req.emittedToolReplacement && req.emittedToolPriceApplies(res) {
		return req
	}
	p, ok := ProtocolFor(res.Protocol)
	if !ok {
		return req
	}
	deriver, ok := p.(ToolSchemaDeriver)
	if !ok {
		// A protocol with no deriver has no builder that rewrites a schema, but
		// its builder still substitutes an open empty object for a parameterless
		// tool. Admission reaches those bytes through the parameterless
		// adjustment, so this price has to include them too — otherwise an input
		// cap between the two readings passes a request that dispatch refuses.
		// Recording the result against the target is what makes admission charge
		// those bytes once: effectiveToolsJSONBytes adds the same delta to an
		// unrecorded count, and a second charge refuses a request that fits.
		baseline := req.toolCountBaseline()
		return req.withToolEmission(res, effectiveToolsJSONBytes(req, res, false), baseline, false)
	}
	emitted, changed := deriver.EmitToolSchemas(req.Tools, res)
	if !changed {
		// The builder emits the list it was handed, so the count that describes
		// that list is the price. Recording it is what lets a second price of
		// the same request answer from the record rather than deriving the
		// emission and walking the catalog again. An unstamped request carries no
		// count to record, and the next reader measures it.
		if req.ToolsJSONBytes > 0 {
			return req.withToolEmission(res, req.ToolsJSONBytes, req.ToolsJSONBytes, false)
		}
		return req
	}
	// Read before withToolEmission overwrites the count: the baseline is the
	// count that describes the list as this request carries it, while the
	// recorded count is this target's emission.
	baseline := req.toolCountBaseline()
	return req.withToolEmission(res, ToolsJSONBytes(emitted), baseline, false)
}

// providerToolsReplacement returns the tool list res's route writes over the
// catalog, and whether there is one: the row's transport body constant when it
// pins tools, else the request's ProviderOptions value.
//
// Every builder whose protocol's options carry tools writes the request's own
// tools and then copies ProviderOptions over the body: Chat Completions, the
// standard Responses route, Anthropic (name, description, input_schema), and
// Google (functionDeclarations). An explicit option is therefore a list that
// dispatches on all four. The transport's body constants are applied after the
// builder and after the option (protocolhttp.Prepare), and they override
// whatever stands at the same path, so a row that pins tools replaces the
// option: the constant is the wire list, and pricing the option charges bytes
// the request never sends — under an input cap between the two prices that
// refuses a request which fits, and the local reading reports the inflated
// count. The option is therefore read only when the transport leaves tools
// alone.
//
// The constant is read from the literal "tools" key, because that is the path
// whose value is a tool list. A dotted path such as "tools.0" makes the body
// constant layer create a parent object instead, which no route reads as a list.
//
// A Codex Responses Lite route is excluded: its tools ride inside the input as
// an additional_tools item, and a top-level tools value is not the catalog that
// endpoint reads.
func providerToolsReplacement(req Request, res registry.Resolved) (any, bool) {
	switch res.Protocol {
	case registry.ProtocolOpenAIChat, registry.ProtocolOpenAIResponses,
		registry.ProtocolAnthropic, registry.ProtocolGoogle:
	default:
		return nil, false
	}
	if res.Protocol == registry.ProtocolOpenAIResponses && registry.BoolValue(res.Caps.ResponsesLite) {
		return nil, false
	}
	if pinned, ok := res.Transport.Body["tools"]; ok {
		return pinned, true
	}
	options, _ := req.ProviderOptions[res.Protocol].(map[string]any)
	replacement, ok := options["tools"]
	return replacement, ok
}

// priceReplacementToolList prices the tools value a route writes over the
// catalog — a transport constant or a ProviderOptions option — recording the
// catalog's own count as the baseline.
//
// The measurement normalizes the replacement to the representation ToolsJSONBytes
// measures, so one outgoing tools array prices the same however the caller
// supplied it (see replacementToolListJSONBytes). The baseline stays the
// catalog's count so a retarget to a route that does not read this protocol's
// options restores the list that route actually sends, and the recorded emission
// keeps the parameterless-schema delta off a list that is not a catalog of
// definitions.
func priceReplacementToolList(req Request, res registry.Resolved, replacement any) Request {
	baseline := req.toolCountBaseline()
	measured := replacementToolListJSONBytes(replacement, res.Protocol)
	if measured < 0 {
		// The encoder rejects the replacement, so it has no wire form to price;
		// the count the request arrived with stands, exactly as ToolsJSONBytes
		// bills nothing for a list the encoder rejects.
		return req
	}
	return req.withToolEmission(res, measured, baseline, true)
}

// replacementToolListJSONBytes returns the byte length a replacement tool list
// is billed by: the count ToolsJSONBytes would report for the same tools
// expressed as a catalog, or a negative value when the encoder rejects the
// replacement.
//
// One outgoing tools array must have one price. The catalog path measures a
// ToolDefinition list — name, description, parameters — while the wire entry
// carries more: Chat nests those fields under a function key beside a type,
// Responses states the type at the top level, Anthropic spells the schema
// input_schema, and Google nests every declaration under functionDeclarations.
// Charging a replacement for its wire spelling therefore prices the same array
// higher than the catalog path prices it, and a cap between the two counts
// rejects only the request that supplied its tools through the option. So a
// function entry this repository's builders produce is normalized back to its
// definition, and the list of them is measured once.
//
// A provider-native tool has no definition to normalize to: web_search,
// computer_use, and google_search are objects the endpoint recognizes rather
// than functions it calls. Those entries are kept as they are and counted by
// their own encoded bytes, because the request dispatches exactly those bytes.
//
// Anything else — a value that is not a list of objects, an entry whose fields
// this package does not model — is measured as it stands. The count is then
// whatever the encoder says it is, which is the same tolerance ToolsJSONBytes
// has for a list it cannot split.
func replacementToolListJSONBytes(replacement any, protocol string) int {
	items, ok := replacementToolItems(replacement, protocol)
	if !ok {
		return encodeJSONLength(replacement)
	}
	return encodeJSONLength(items)
}

// replacementToolItems returns what a replacement tool list is measured by: one
// item per tool, each either a ToolDefinition normalized from a wire entry or
// the entry itself when it names no function this package models.
//
// Google's entries group their declarations, so a group is flattened here: the
// catalog the builder would have written from those declarations is a flat list,
// and that is the list ToolsJSONBytes measures. A group that declares no
// functions — google_search is one — stands in for the whole entry.
func replacementToolItems(replacement any, protocol string) ([]any, bool) {
	switch list := replacement.(type) {
	case []any:
		return replacementItemsOf(list, protocol)
	case []map[string]any:
		entries := make([]any, len(list))
		for i := range list {
			entries[i] = list[i]
		}
		return replacementItemsOf(entries, protocol)
	default:
		return nil, false
	}
}

// replacementItemsOf normalizes each entry of a wire tools array.
func replacementItemsOf(entries []any, protocol string) ([]any, bool) {
	out := make([]any, 0, len(entries))
	for _, entry := range entries {
		declarations, grouped := googleFunctionDeclarations(entry, protocol)
		if grouped {
			out = append(out, declarations...)
			continue
		}
		if def, ok := replacementToolDefinition(entry, protocol); ok {
			out = append(out, def)
			continue
		}
		out = append(out, entry)
	}
	return out, true
}

// googleFunctionDeclarations returns the declarations a Gemini tools entry
// groups, and whether the entry is one. Only the Google protocol has this shape:
// every other protocol's entries are tools themselves.
func googleFunctionDeclarations(entry any, protocol string) ([]any, bool) {
	if protocol != registry.ProtocolGoogle {
		return nil, false
	}
	obj, ok := entry.(map[string]any)
	if !ok {
		return nil, false
	}
	decls, ok := replacementToolItems(obj["functionDeclarations"], registry.ProtocolGoogle)
	if !ok {
		return nil, false
	}
	return decls, true
}

// replacementToolDefinition reads one wire tools entry as the definition the
// catalog path measures, reporting false for a tool with no such form.
//
// The shapes are the ones this repository's builders write, per protocol: Chat
// nests the fields under a function key beside type=function, Responses states
// them at the top level of the entry, Anthropic spells the schema input_schema,
// and Google's declarations (flattened by replacementToolItems) spell it
// parameters. A name is required as well as a schema, so a provider-native
// object that happens to carry a "parameters" field is not mistaken for a
// function.
func replacementToolDefinition(entry any, protocol string) (ToolDefinition, bool) {
	obj, ok := entry.(map[string]any)
	if !ok {
		return ToolDefinition{}, false
	}
	fields := obj
	schemaKey := "parameters"
	switch protocol {
	case registry.ProtocolOpenAIChat:
		// Chat is the one protocol whose entries are opaque to their own
		// fields: type=function guards the nested object, so a native tool
		// such as web_search cannot be mistaken for a function.
		if typ, _ := obj["type"].(string); typ != "function" {
			return ToolDefinition{}, false
		}
		nested, ok := obj["function"].(map[string]any)
		if !ok {
			return ToolDefinition{}, false
		}
		fields = nested
	case registry.ProtocolOpenAIResponses:
		if typ, has := obj["type"].(string); has && typ != "function" {
			return ToolDefinition{}, false
		}
	case registry.ProtocolAnthropic:
		schemaKey = "input_schema"
	}
	parameters, ok := fields[schemaKey].(map[string]any)
	if !ok {
		return ToolDefinition{}, false
	}
	name := stringField(fields, "name")
	if name == "" {
		return ToolDefinition{}, false
	}
	return ToolDefinition{
		Name:        name,
		Description: stringField(fields, "description"),
		Parameters:  parameters,
	}, true
}

// stringField reads a string field of a wire entry, empty when it is absent or
// spelled otherwise.
func stringField(obj map[string]any, key string) string {
	s, _ := obj[key].(string)
	return s
}

// encodeJSONLength returns the encoded length of v, or a negative value when the
// encoder rejects it.
func encodeJSONLength(v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		return -1
	}
	return len(b)
}

// withToolEmission records count as the price res's builder produced, over
// baseline, and whether the price describes a ProviderOptions replacement rather
// than the catalog's emission. The record is what lets a later price restore the
// list's own count instead of keeping another target's answer.
func (req Request) withToolEmission(res registry.Resolved, count, baseline int, replacement bool) Request {
	req.ToolsJSONBytes = count
	req.emittedToolTarget = emittedToolTargetFor(res)
	req.emittedToolBaseline = baseline
	req.emittedToolCount = count
	req.emittedToolReplacement = replacement
	req.emittedToolCatalog = toolCatalogIdentity{defs: req.Tools}
	return req
}

// restoreToolCountBaseline returns req priced by the list it carries rather than
// by another target's emission, and drops the record of that emission.
//
// It is the same restore for every caller that can be handed a pre-priced
// request: the deriver path, the local reading, and admission. An override
// adapter sends the definitions it was handed, so for it the baseline is the
// price. A route whose protocol has no deriver starts from the same baseline
// and then adds the schema its builder substitutes for a parameterless tool.
//
// A record only describes a request whose tool count is still the count that
// record produced. A caller that replaces Tools and stamps the replacement makes
// ToolsJSONBytes describe bytes this record never measured, and restoring the
// recorded baseline over the new stamp would price the discarded catalog: the
// replacement's own count wins, and an unstamped replacement is measured by
// whoever reads it.
func restoreToolCountBaseline(req Request, res registry.Resolved) Request {
	if req.emittedToolTarget == (emittedToolTargetKey{}) {
		return req
	}
	if !req.recordsEmission() {
		// The record describes a count this request no longer carries.
		return dropReplacedToolRecord(req)
	}
	// A substitution is a property of the options this request carries now, not
	// of the target, so it is re-decided from the list on every price: keeping
	// the recorded count because the target still matches would leave a
	// replacement's price standing after the option that produced it was gone.
	// Another target's charge is not this route's answer either.
	if req.emittedToolReplacement || req.emittedToolTarget != emittedToolTargetFor(res) {
		return dropRecordedToolEmission(req)
	}
	return req
}

// recordsEmission reports whether req carries a record that still describes its
// tool count: a target is recorded, ToolsJSONBytes is still the count that
// record produced, and Tools is still the catalog it measured. A caller that
// replaced Tools and stamped the replacement leaves a count the record never
// measured, and that case is excluded here — as is a rebuilt catalog that
// happens to serialize to the same number of bytes, which only the catalog
// identity can tell apart from the one the record measured.
func (req Request) recordsEmission() bool {
	if req.emittedToolTarget == (emittedToolTargetKey{}) {
		return false
	}
	return req.ToolsJSONBytes == req.emittedToolCount && req.emittedToolCatalog.names(req.Tools)
}

// toolCountBaseline is the count req's tool list measures without the current
// route's schema decision applied.
//
// The recorded baseline wins over the incoming count on a repeat price of the
// same target, because by then the incoming count is that target's emission
// rather than the list's own measurement. A request that arrived unmeasured is
// measured now.
func (req Request) toolCountBaseline() int {
	if req.emittedToolBaseline > 0 {
		return req.emittedToolBaseline
	}
	if req.ToolsJSONBytes > 0 {
		return req.ToolsJSONBytes
	}
	return ToolsJSONBytes(req.Tools)
}

// dropRecordedToolEmission returns req priced by the list it carries, whatever
// target the recorded emission names.
//
// It is for a route that makes no schema decision: an override adapter marshals
// the definition it is handed, so no protocol's emission describes its wire
// list. The target comparison cannot see that on its own — an override keeps the
// resolved row it dispatches through, so a request priced for that row's
// protocol still matches the recorded target and would keep a charge the
// override never incurs.
func dropRecordedToolEmission(req Request) Request {
	if req.emittedToolTarget == (emittedToolTargetKey{}) {
		return req
	}
	if !req.recordsEmission() {
		return dropReplacedToolRecord(req)
	}
	req.ToolsJSONBytes = req.emittedToolBaseline
	return clearToolEmissionRecord(req)
}

// clearToolEmissionRecord forgets the recorded emission, leaving ToolsJSONBytes
// as the caller set it.
func clearToolEmissionRecord(req Request) Request {
	req.emittedToolTarget = emittedToolTargetKey{}
	req.emittedToolBaseline = 0
	req.emittedToolCount = 0
	req.emittedToolReplacement = false
	req.emittedToolCatalog = toolCatalogIdentity{}
	return req
}

// dropReplacedToolRecord forgets a record whose count no longer describes this
// request, keeping the replacement's own count.
//
// The caller replaced Tools and stamped the replacement, so that stamp is the
// price rather than the discarded catalog's baseline. A replacement the caller
// did not measure is measured here: zero means "measure me" to every reader of
// the field, and measuring once is what the stamp exists to avoid repeating.
func dropReplacedToolRecord(req Request) Request {
	req = clearToolEmissionRecord(req)
	if req.ToolsJSONBytes == 0 {
		req.ToolsJSONBytes = ToolsJSONBytes(req.Tools)
	}
	return req
}

// RestoreToolBaseline returns req priced by the tool list it carries, whatever
// target the recorded emission names.
//
// It is for a route that makes no schema decision about the list it is handed:
// an override adapter marshals the definition it receives, so the count it
// dispatches with is the list's own measurement. A caller that prices such a
// route through a protocol's builder records a charge the override never
// incurs, and both of the fallback history estimates it drives would then
// describe a catalog the wire never carries.
func RestoreToolBaseline(req Request) Request {
	return dropRecordedToolEmission(req)
}

// emittedToolTargetKey names the target whose schema decision a count was
// produced by. Only the parts a deriver reads belong here: the protocol that
// owns the builder, the capability that decides whether it projects at all, and
// the Codex Responses Lite flag that decides whether a certified schema still
// needs every node's type stated. A row that differs only in fields no builder
// reads prices identically and is the same target.
type emittedToolTargetKey struct {
	protocol    string
	strictTools bool
	// responsesLite is read by the Responses builder alone; it decides whether a
	// certified union keeps strict mode or falls back to non-strict emission
	// with its enclosing type stated, which is a different set of bytes.
	responsesLite bool
}

// emittedToolTargetFor returns the key a count produced for res is recorded
// under.
func emittedToolTargetFor(res registry.Resolved) emittedToolTargetKey {
	return emittedToolTargetKey{
		protocol:    res.Protocol,
		strictTools: registry.BoolValue(res.Caps.StrictTools),
		// Every protocol reads it as false, because only the Responses row states
		// the capability; a Chat row therefore never differs from another Chat
		// row by this field.
		responsesLite: registry.BoolValue(res.Caps.ResponsesLite),
	}
}

// toolCatalogIdentity names the catalog an emission count was measured over.
//
// A byte count is not an identity. A caller can rebuild a catalog from the
// schemas a builder synthesized and happen to measure exactly the count that
// builder emitted, and a record keyed by its count alone would then accept the
// rebuilt catalog as the one it measured and restore the smaller baseline over
// it. The catalog occurrence is what tells the two apart: a rebuilt list is a
// different slice with its own definitions.
//
// Holding the slice is also what keeps the identity sound: the record keeps the
// catalog it measured alive, so its address cannot be reused by a later list
// while the record still names it. The comparison reads the first definition's
// address and the length, because a slice header is not an identity — a shorter
// prefix of one list is a different catalog, and a copy of a header that covers
// the same definitions is the same one.
type toolCatalogIdentity struct {
	defs []ToolDefinition
}

// names reports whether defs is the catalog this identity was taken from.
func (i toolCatalogIdentity) names(defs []ToolDefinition) bool {
	return len(i.defs) == len(defs) && (len(defs) == 0 || &i.defs[0] == &defs[0])
}

// PriceToolSchemas returns req with its tool count describing the list res's
// builder emits, so a caller that estimates before admission bills the same
// list admission will.
//
// Every estimate taken over a request whose tools a builder rewrites needs this:
// a full-history estimate computed over the advertised catalog and then carried
// past a delta's admission is smaller than the list that dispatches, and the
// larger of the two is the one the budget takes.
//
// A route whose builder rewrites nothing is priced by its own list: a protocol
// that synthesizes schemas without a ToolSchemaDeriver gets the
// parameterless-schema delta, and one that changes nothing is recorded at the
// count it was handed.
//
// This function cannot tell an override-served route from a built-in one, and an
// override marshals the definitions it is handed. A caller that knows an
// override serves the request must call RestoreToolBaseline instead, which
// prices the list the override receives.
//
// A request with no tools of its own can still send some: a ProviderOptions
// tools value, or a pinned "tools" transport body constant, supplies the entire
// tool list on the Chat and standard Responses routes, and that list is priced
// here against the catalog's own count as the baseline (see
// providerToolsReplacement). A request with neither a catalog nor a replacement
// returns unchanged.
func PriceToolSchemas(req Request, res registry.Resolved) Request {
	return priceEmittedToolSchemas(req, res)
}

// ToolsJSONBytes returns the byte length of defs' serialized form: the length
// the local estimate bills a tool list by. A caller that builds one tool list
// and prices many requests over it can measure the list once and stamp
// Request.ToolsJSONBytes, which leaves the estimate byte-identical while
// skipping the marshal on every request (#6406). An empty list is 0, and a
// list the encoder rejects is 0 too — the same share today's estimate bills
// for it, since json.Marshal returns no bytes alongside its error.
func ToolsJSONBytes(defs []ToolDefinition) int {
	if len(defs) == 0 {
		return 0
	}
	b, _ := json.Marshal(defs)
	return len(b)
}

// closedEmptyToolParameters is the closed empty object schema emitted by
// strict-capable built-in OpenAI adapters for a parameterless tool that has not
// opted out of strict mode.
func closedEmptyToolParameters() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           map[string]any{},
		"required":             []string{},
	}
}

// protocolClosesStrictParameterlessTools reports whether the built-in adapter
// for res emits closedEmptyToolParameters for non-opted-out parameterless tools.
func protocolClosesStrictParameterlessTools(res registry.Resolved) bool {
	if !registry.BoolValue(res.Caps.StrictTools) {
		return false
	}
	switch res.Protocol {
	case registry.ProtocolOpenAIResponses, registry.ProtocolOpenAIChat:
		return true
	default:
		return false
	}
}

// emittedParameterlessParameters returns the schema a built-in adapter emits
// for a tool with no declared parameters, or nil when the adapter leaves
// t.Parameters unchanged (including omitting a direct caller's nil).
func emittedParameterlessParameters(t ToolDefinition, res registry.Resolved) map[string]any {
	if !t.DeclaresNoParameters() && t.Parameters != nil {
		return nil
	}
	if protocolClosesStrictParameterlessTools(res) && (t.Strict == nil || *t.Strict) {
		return closedEmptyToolParameters()
	}
	if t.Parameters == nil {
		switch res.Protocol {
		case registry.ProtocolOpenAIResponses, registry.ProtocolAnthropic, registry.ProtocolGoogle:
			return defaultToolParameters()
		}
	}
	return nil
}

// parameterlessToolsJSONDelta returns the net serialized byte delta introduced
// by the resolved built-in adapter substituting closed or open empty schemas for
// parameterless tools in defs. Overrides return 0.
func parameterlessToolsJSONDelta(defs []ToolDefinition, res registry.Resolved, override bool) int {
	if len(defs) == 0 || override {
		return 0
	}
	delta := 0
	for i := range defs {
		emitted := emittedParameterlessParameters(defs[i], res)
		if emitted == nil {
			continue
		}
		declaredBytes := ToolsJSONBytes(defs[i : i+1])
		adjusted := defs[i]
		adjusted.Parameters = emitted
		emittedBytes := ToolsJSONBytes([]ToolDefinition{adjusted})
		if declaredBytes > 0 && emittedBytes > 0 {
			delta += emittedBytes - declaredBytes
		}
	}
	return delta
}

// effectiveToolsJSONBytes returns the tool-catalog byte length priced against
// the resolved adapter's wire emission. Both caller-stamped declared-catalog
// lengths and unstamped requests receive the parameterless-schema delta once.
//
// A request the deriver has already priced for this target is left alone. That
// price was measured over the list the builder emits, which carries the
// substituted schemas the delta here describes, so adding the delta to it
// charges the same bytes twice — and an input cap between the two counts
// refuses a request that fits. The recorded target is what distinguishes the
// two. An override leaves the definitions as they were handed over and records
// nothing, so it takes the delta. A route whose protocol has no deriver records
// a price that already includes the schema its builder substitutes for a
// parameterless tool, so adding the delta to it would charge those bytes twice.
//
// A request with no catalog but a recorded replacement — a transport tools
// constant or a ProviderOptions tools value — prices that replacement, which is
// the whole tool list such a request sends. The delta is not applied to it: the
// replacement is the wire list, not a catalog a builder substitutes schemas
// into.
func effectiveToolsJSONBytes(req Request, res registry.Resolved, override bool) int {
	if !req.pricesTools(res, override) {
		return 0
	}
	if !override && req.emittedToolPriceApplies(res) {
		return req.ToolsJSONBytes
	}
	base := req.ToolsJSONBytes
	if base <= 0 {
		base = ToolsJSONBytes(req.Tools)
	}
	return base + parameterlessToolsJSONDelta(req.Tools, res, override)
}

// withEffectiveToolPrice returns req with ToolsJSONBytes describing the tool
// list res's route emits, for a request that has a tool price to apply.
// Requests with nothing to price are returned unchanged.
func withEffectiveToolPrice(req Request, res registry.Resolved, override bool) Request {
	if req.pricesTools(res, override) {
		req.ToolsJSONBytes = effectiveToolsJSONBytes(req, res, override)
	}
	return req
}

// pricesTools reports whether this request has a tool price to apply for res: a
// catalog, or a recorded replacement — a transport tools constant or a
// ProviderOptions tools value — that is the wire list on a request whose catalog
// is empty.
func (req Request) pricesTools(res registry.Resolved, override bool) bool {
	if len(req.Tools) > 0 {
		return true
	}
	return !override && req.emittedToolReplacement && req.emittedToolPriceApplies(res)
}

// emittedToolPriceApplies reports whether the recorded emission describes the
// count this request carries for res: the record exists, ToolsJSONBytes is
// still the count that record produced, and the target is this one.
//
// A caller that replaced the catalog and stamped the replacement leaves a count
// the record did not produce, so the record describes a list this request no
// longer carries and its price must not be reused.
func (req Request) emittedToolPriceApplies(res registry.Resolved) bool {
	return req.recordsEmission() && req.emittedToolTarget == emittedToolTargetFor(res)
}

// EstimateMessagesInputTokens estimates only a message list. It is useful for
// history-only accounting that does not yet have a full Request. Its
// unsigned-thinking rule is the name fallback, since a bare message list
// carries no target; callers that hold a resolved row should use
// EstimateMessagesInputTokensForResolved.
func EstimateMessagesInputTokens(messages []Message) InputTokenCount {
	return estimateMessageList(targetFromNames("", ""), messages)
}

// EstimateMessagesInputTokensForResolved estimates a message list for a
// resolved target, deciding the unsigned-thinking rule from the row's protocol
// and reasoning capabilities. History accounting (the context manager's
// compaction pressure) uses it so the thinking text those adapters replay is
// counted.
func EstimateMessagesInputTokensForResolved(res registry.Resolved, messages []Message) InputTokenCount {
	return estimateMessageList(targetFromResolved(res, "", ""), messages)
}

// TokenPartEstimator prices messages one at a time for ONE resolved target, so a
// caller that walks its own list — a session's turn history, say — prices the
// turns it already holds without projecting them into a []Message.
//
// Summing Parts over a list and calling Total once is that list's estimate: the
// chars are summed first and divided by four once (see
// estimateMessagesInputTokens), so no prefix of the list can round differently
// from the whole. See TestTokenPartEstimatorMatchesMessageList, which pins that
// the per-message walk prices what EstimateMessagesInputTokensForResolved
// prices.
type TokenPartEstimator struct {
	target targetInfo
}

// TokenPartEstimatorFor returns a per-message pricer for res. Building one reads
// the resolved row, so a caller prices a list with one estimator rather than one
// per message.
func TokenPartEstimatorFor(res registry.Resolved) TokenPartEstimator {
	return TokenPartEstimator{target: targetFromResolved(res, "", "")}
}

// Parts returns m's share of the estimate: its char count and its count of parts
// billed at a fixed token price.
func (e TokenPartEstimator) Parts(m Message) TokenParts {
	chars, counted := estimateMessageInputParts(e.target, m)
	return TokenParts{Chars: chars, Counted: counted}
}

// PartsForeignReasoning is Parts for a foreign turn: no opaque state rides.
func (e TokenPartEstimator) PartsForeignReasoning(m Message) TokenParts {
	e.target.stripOpaqueThinking = true
	return e.Parts(m)
}

// TokenParts is the additive decomposition of a local token estimate.
type TokenParts struct {
	// Chars is the summed character count, divided by four once in Total.
	Chars int
	// Counted is the parts billed at a fixed token price rather than by size
	// (images, audio, documents), summed directly.
	Counted int
}

// Total is the estimate those parts describe. It is the same arithmetic
// estimateMessagesInputTokens performs, in the same order.
func (p TokenParts) Total() int {
	return p.Counted + p.Chars/4
}

// Add returns the parts of the concatenated message lists.
func (p TokenParts) Add(other TokenParts) TokenParts {
	return TokenParts{Chars: p.Chars + other.Chars, Counted: p.Counted + other.Counted}
}

func estimateMessageList(t targetInfo, messages []Message) InputTokenCount {
	return InputTokenCount{
		Tokens: estimateMessagesInputTokens(t, messages),
		Exact:  false,
		Source: TokenCountSourceLocalEstimate,
	}
}

// CountInputTokens resolves the request's target and uses its exact counter
// when available. Targets without exact support fall back to the local
// estimate. The count is taken over the shaped request, so it describes the
// input body Complete would send. It intentionally does not apply completion
// admission: callers need the exact count to decide how an oversized request
// should be reduced before dispatch.
func (c *Client) CountInputTokens(ctx context.Context, req Request) (InputTokenCount, error) {
	if err := req.Validate(); err != nil {
		return InputTokenCount{}, err
	}
	if req.AdapterTimeout == nil {
		req.AdapterTimeout = new(DefaultAdapterTimeout())
	}
	t, err := c.dispatchTarget(req)
	if err != nil {
		return InputTokenCount{}, err
	}
	// The instance a request resolves to is not evidence about the model, so the
	// estimator keeps the caller's own provider name -- empty when the caller gave
	// none -- rather than the name the dispatch happened to use.
	callerProvider := req.Provider
	req.Provider = t.name
	if t.resolved {
		req = ShapeRequest(req, t.res)
	}
	// A request may arrive priced for the route that built it — a caller
	// preprices against a strict Responses row and then counts the same request
	// on the route that will actually serve it. The restore happens here, before
	// the branch below decides whether this target has a deriver, because that
	// answer needs a count that describes this request's own list rather than
	// another route's emission. Doing it inside the deriver left a route with no
	// deriver and an override-served route holding the previous target's charge,
	// which is a count for a catalog neither of them sends. An unresolved
	// override target is included: its row is the zero one, which can never be
	// the target a pre-priced count was recorded under, so the restore is the
	// correct answer for it too.
	if t.override != nil {
		// An override marshals the definition it is handed, so no protocol's
		// emission describes its wire list and the recorded charge has to go.
		// The baseline restore would decide the same thing for this route and is
		// therefore not run beside it.
		req = dropRecordedToolEmission(req)
	} else {
		req = restoreToolCountBaseline(req, t.res)
	}
	localEstimate := func() InputTokenCount {
		// The local reading is the fallback for a route with no exact counter,
		// which is exactly the caller who sizes a request from it and then
		// dispatches. A protocol's builder rewrites the tool list before sending
		// it, so measuring the advertised list here would answer with a number
		// smaller than the request that dispatches, and a caller who fits its
		// request to that number meets the shortfall as a context-budget
		// rejection from Complete. The emitted list is the one to price, and
		// only a protocol route has one: an override marshals the definition it
		// was handed, so the list restored above is its wire list.
		priced := req
		if t.override == nil && t.resolved {
			priced = priceEmittedToolSchemas(priced, t.res)
		}
		return localInputEstimate(priced, callerProvider, t)
	}

	// countExactly is the exact-count call of whichever half of the target
	// serves the request; nil when an override offers no exact counter.
	var countExactly func(context.Context) (InputTokenCount, error)
	if t.override == nil {
		countExactly = func(ctx context.Context) (InputTokenCount, error) {
			tokens, err := t.protocol.CountTokens(ctx, req, t.res)
			return InputTokenCount{Tokens: tokens, Exact: true, Source: TokenCountSourceProvider, Provider: t.name, Model: req.Model}, err
		}
	} else if counter, ok := t.override.(InputTokenCounter); ok {
		countExactly = func(ctx context.Context) (InputTokenCount, error) { return counter.CountInputTokens(ctx, req) }
	}
	if countExactly == nil {
		out := localEstimate()
		out.Provider = t.name
		return out, nil
	}

	opCtx, operation := c.beginProviderOperation(ctx)
	out, err := countExactly(opCtx)
	operation.settle(opCtx, err)
	if err != nil {
		if errors.Is(err, ErrInputTokenCountUnsupported) {
			estimate := localEstimate()
			estimate.Provider = t.name
			return estimate, nil
		}
		return InputTokenCount{}, RewriteErrorProvider(err, t.name)
	}
	if out.Source == "" {
		out.Source = TokenCountSourceProvider
	}
	if out.Source == TokenCountSourceProvider {
		out.Exact = true
	}
	if out.Provider == "" {
		out.Provider = t.name
	}
	if out.Model == "" {
		out.Model = req.Model
	}
	return out, nil
}

func estimateMessagesInputTokens(t targetInfo, messages []Message) int {
	chars := 0
	tokens := 0
	for _, m := range messages {
		c, counted := estimateMessageInputParts(t, m)
		chars += c
		tokens += counted
	}
	return tokens + chars/4
}

func estimateMessageInputParts(t targetInfo, m Message) (int, int) {
	chars := len(m.Name) + len(m.ToolCallID)
	tokens := 0
	var chatReasoning chatReasoningEstimate
	for _, p := range m.Content {
		switch p.Kind {
		case ContentText:
			chars += len(p.Text)
		case ContentImage:
			if p.Image != nil {
				tokens += estimateImageTokens(t, p.Image)
			}
		case ContentAudio:
			tokens += fallbackMediaTokens
		case ContentDocument:
			if p.Document != nil {
				tokens += fallbackMediaTokens + len(p.Document.URL)/4 + len(p.Document.MediaType)/4 + len(p.Document.FileName)/4
			}
		case ContentToolCall:
			if p.ToolCall != nil {
				chars += len(p.ToolCall.ID)
				chars += len(p.ToolCall.Name)
				args := len(p.ToolCall.Arguments) // Zero allocations on the common path.
				if p.ToolCall.ArgumentsSpelling != "" && (t.protocol == registry.ProtocolOpenAIResponses || t.protocol == registry.ProtocolOpenAIChat || t.namedChat) {
					args = len(strings.TrimSpace(p.ToolCall.WireArguments()))
				}
				chars += args
				if t.protocol == registry.ProtocolOpenAIResponses {
					chars += len(strings.TrimSpace(p.ToolCall.ItemID))
				}
			}
		case ContentToolResult:
			if p.ToolResult != nil {
				chars += len(p.ToolResult.ToolCallID)
				chars += len(p.ToolResult.Name)
				if t.billsToolResultMedia(p.ToolResult) {
					if IsDocumentMediaType(p.ToolResult.ImageMediaType) {
						// A PDF is not a raster: charge it like a document
						// rather than spending a decode that cannot succeed.
						tokens += fallbackMediaTokens + len(p.ToolResult.ImageMediaType)/4
					} else {
						tokens += estimateImageTokens(t, &ImageData{Data: p.ToolResult.ImageData, MediaType: p.ToolResult.ImageMediaType})
					}
				}
				switch x := p.ToolResult.Content.(type) {
				case string:
					chars += len(x)
				case []byte:
					chars += len(x)
				default:
					b, _ := json.Marshal(x)
					chars += len(b)
				}
			}
		case ContentThinking, ContentRedThinking:
			if t.stripOpaqueThinking {
				// The blob never rides and a reasoning-disabled row builds no
				// item at all; the remaining plaintext bills as built.
				if p.Kind == ContentThinking && p.Thinking != nil && !t.reasoningOff {
					kept := *p.Thinking
					kept.EncryptedContent = ""
					chars += responsesReasoningChars(&kept)
				}
			} else if t.protocol == registry.ProtocolOpenAIChat || t.namedChat {
				chatReasoning.add(t, p)
			} else {
				chars += thinkingReplayChars(t, p)
			}
		case ContentWebSearch:
			if p.WebSearch != nil {
				chars += len(p.WebSearch.Query)
				chars += len(p.WebSearch.Raw)
			}
		default:
			b, _ := json.Marshal(p)
			chars += len(b)
		}
	}
	return chars + chatReasoning.chars(t), tokens
}

// thinkingReplayChars counts the characters a thinking part contributes to the
// outgoing request. A resolved row that names its protocol is billed for exactly
// what that protocol's builder puts on the wire; every other target keeps the
// conservative billing that counts every shape, with the text-only case decided
// by the target's unsignedThinking (unsignedThinkingReplayed for a resolved row,
// unsignedThinkingReplayedByName for a caller that passed only names).
//
//   - anthropic/request.go replays a thinking block for a part that carries text
//     (the text plus the signature, blanked when the value is really an
//     OpenAI-compatible wire field name) and a redacted_thinking block for
//     ContentRedThinking; an encrypted blob never rides, and a part with no text
//     is skipped;
//   - responses/input.go replays a non-compat encrypted blob, and a finished
//     plaintext item only when it carries native typed parts; a bare Text
//     projection stays display-only;
//   - chatcompletions/messages.go replays the part's text (kept off the wire by a
//     declared non-reasoning row that does not replay thinking as text) and the
//     compat reasoning_details array (kept off the wire by any declared
//     non-reasoning row), and drops a redacted part and an opaque Responses blob;
//   - the Google adapter emits no thinking shape at all.
//
// Chat reasoning is priced across the message by estimateMessageInputParts.
func thinkingReplayChars(target targetInfo, p ContentPart) int {
	if p.Thinking == nil {
		return 0
	}
	t := p.Thinking
	if target.resolved && target.protocol != "" {
		switch target.protocol {
		case registry.ProtocolAnthropic:
			if p.Kind == ContentRedThinking {
				return len(t.Text)
			}
			return anthropicThinkingChars(t)
		case registry.ProtocolOpenAIResponses:
			if p.Kind != ContentThinking || target.reasoningOff {
				// responses/input.go emits the reasoning item for a ContentThinking
				// part alone, so a redacted part rides as nothing -- blob, id and
				// summaries included.
				return 0
			}
			return responsesReasoningChars(t)
		default:
			// The Google adapter and every other protocol along this path emit
			// no thinking shape.
			return 0
		}
	}
	if p.Kind == ContentRedThinking {
		return len(t.Text)
	}
	if t.EncryptedContent != "" {
		// An OpenAI-compatible encrypted reasoning_details array is replayed by
		// the chat adapter together with the separately parsed text
		// (chatcompletions/messages.go), and the Anthropic adapter replays the
		// text while ignoring the blob. The opaque OpenAI Responses blob is the
		// other shape: it replays with its summary and id and puts no text on
		// the wire, so only that shape bills them.
		if IsOpenAICompatEncryptedReasoning(t.EncryptedContent) {
			return len(t.EncryptedContent) + len(t.Text)
		}
		chars := len(t.EncryptedContent) + len(t.ID)
		for _, s := range t.Summary {
			chars += len(s)
		}
		return chars
	}
	if t.Signature != "" {
		if IsOpenAICompatReasoningField(t.Signature) {
			return len(t.Text)
		}
		return len(t.Text) + len(t.Signature)
	}
	if target.unsignedThinking {
		return len(t.Text)
	}
	return 0
}

// anthropicThinkingChars is what anthropic/request.go emits for a ContentThinking
// part: nothing when the part carries no text (an empty thinking block is invalid
// continuation state), otherwise the text plus the signature -- except an
// OpenAI-compatible wire field name, which is not an Anthropic signature and
// rides unsigned.
func anthropicThinkingChars(t *ThinkingData) int {
	if t.Text == "" {
		return 0
	}
	if IsOpenAICompatReasoningField(t.Signature) {
		return len(t.Text)
	}
	return len(t.Text) + len(t.Signature)
}

func responsesReasoningChars(t *ThinkingData) int {
	encrypted := !t.ResponsesIncomplete && t.EncryptedContent != "" && !IsOpenAICompatEncryptedReasoning(t.EncryptedContent)
	if !encrypted && !t.ReplayableResponsesPlaintext() {
		return 0
	}
	chars := len(t.EncryptedContent)
	if encrypted {
		for _, s := range t.Summary {
			if s = strings.TrimSpace(s); s != "" {
				chars += len(s)
			}
		}
	} else {
		for _, part := range t.ResponsesSummaryParts {
			chars += len(part.Text)
		}
		for _, part := range t.ResponsesContentParts {
			chars += len(part.Text)
		}
	}
	if id := strings.TrimSpace(t.ID); id != "" {
		chars += len(id)
	}
	return chars
}

// The Chat writer chooses a destination for assembled text per message. Its
// first text detail may already contain the signed text it sends.
type chatReasoningEstimate struct {
	displayChars, scalarChars, detailChars int
	detailArrays                           []string
}

type reasoningTextPresence bool

func (p *reasoningTextPresence) UnmarshalJSON(raw []byte) error {
	// The decoder passes a validated token. Only nonempty strings supply text.
	*p = len(raw) > 2 && raw[0] == '"'
	return nil
}

func (r *chatReasoningEstimate) add(target targetInfo, p ContentPart) {
	if p.Kind != ContentThinking || p.Thinking == nil {
		return
	}
	r.displayChars += len(p.Thinking.Text)
	r.scalarChars += len(p.Thinking.ChatReplayText(true))
	if target.reasoningOff || !IsOpenAICompatEncryptedReasoning(p.Thinking.EncryptedContent) {
		return
	}
	r.detailChars += len(p.Thinking.EncryptedContent)
	if !target.chatThinkingAsText {
		r.detailArrays = append(r.detailArrays, p.Thinking.EncryptedContent)
	}
}

func (r chatReasoningEstimate) countsDisplay(target targetInfo) bool {
	if r.displayChars == 0 || target.chatThinkingAsText {
		return target.chatThinkingAsText
	}
	// The writer reads exact object keys, even when legacy array validation
	// accepts a case-folded type. Ignore unrelated fields such as opaque data.
	for _, raw := range r.detailArrays {
		var details []struct {
			Type string                `json:"type"`
			Text reasoningTextPresence `json:"text"`
		}
		if jsonv2.Unmarshal([]byte(raw), &details, jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true)) != nil {
			continue
		}
		for _, detail := range details {
			if detail.Type == "reasoning.text" {
				return !bool(detail.Text)
			}
		}
	}
	return target.unsignedThinking
}

func (r chatReasoningEstimate) chars(target targetInfo) int {
	if !target.chatThinkingAsText && !target.chatUsesDetails && r.detailChars == 0 {
		if target.unsignedThinking {
			return r.scalarChars
		}
		return 0
	}
	if r.countsDisplay(target) {
		return r.detailChars + r.displayChars
	}
	return r.detailChars
}

// targetInfo is what the local estimator knows about the request's target: the
// names the media estimates key on, and whether the target's adapter replays a
// thinking part that carries text and no replay metadata.
type targetInfo struct {
	provider, model string
	protocol        string
	surface         string
	family          string
	// resolved marks a target built from a registry row. The row's instance is
	// an alias it was reached through, not vendor identity, so the media-family
	// name rule reads a resolved target's model name alone -- unless the caller
	// supplied the provider itself, which is evidence the caller chose to give.
	resolved bool
	// providerFromCaller marks a provider name that came from the caller rather
	// than from the row's instance.
	providerFromCaller bool
	// imageDetail is the row's configured image detail, which the adapter applies
	// to images that carry none of their own.
	imageDetail string
	// reasoningOff marks a row that declares reasoning = false: the chat adapter
	// keeps the reasoning fields and the reasoning_details array off the wire
	// (chatcompletions/messages.go).
	reasoningOff       bool
	chatThinkingAsText bool
	chatUsesDetails    bool
	namedChat          bool
	// dropsToolResultImages marks a resolved row whose adapter leaves a
	// tool-result image off the wire entirely (dropsToolResultImages).
	dropsToolResultImages bool
	// seenToolResultDocuments marks a resolved row that both declares pdf input
	// and speaks a protocol whose tool-result shape carries a document. The
	// estimate prices a document only for such a row (#4465).
	seenToolResultDocuments bool
	// seesToolResultImages marks a resolved row whose model declares image
	// input on a protocol that delivers tool-result images. Request assembly
	// drops images the row cannot see (#4217), so the estimate prices none.
	seesToolResultImages bool
	unsignedThinking     bool
	stripOpaqueThinking  bool // a foreign turn's reasoning, priced without its opaque half
}

// vendorNameBasis is the provider name the media-family name rule may read. A
// resolved row's instance name says where the row was reached, not who made the
// model -- any gateway can be called anything -- so a resolved target offers only
// its model name; a caller that passed names alone has nothing else to go on.
func (t targetInfo) vendorNameBasis() string {
	if t.resolved {
		// The row's own facts decide when it has any: a provider name -- even one
		// the caller supplied -- that merely repeats an instance alias must not
		// outrank the protocol the row actually speaks.
		if t.surface != "" || t.family != "" || t.protocol != "" {
			return ""
		}
		// With nothing else to go on, a name the caller supplied is evidence and
		// the row's instance alias is not.
		if !t.providerFromCaller {
			return ""
		}
	}
	return t.provider
}

// mediaFamily is the image-token family for the target, resolved from the most
// particular fact available to the least: the row's recorded model family, then
// its surface, then the model's own name, and only then the wire protocol. A
// tokenizer family is a property of the model, not of the endpoint that serves
// it: OpenRouter serves anthropic/claude-* and google/gemini-* rows over
// openai-chat, a row the registry never classified still carries a name that says
// which model it is, and a resolver can fill the surface from the provider when
// the row omits it -- so the family, which is the model's own, answers before the
// surface. The protocol answers last because it describes only the endpoint; the
// name rule is the last resort for callers that passed names only.
func (t targetInfo) mediaFamily() string {
	if f := mediaFamilyFromModelFamily(t.family); f != "" {
		return f
	}
	if genericModelFamily(t.family) {
		// The registry classifies this family as deliberately generic (gpt-oss:
		// its rows declare text-only input), so the empty result above is a
		// decision, not an absence: neither the surface, the model name, nor the
		// wire protocol may put an image family back.
		return ""
	}
	switch t.surface {
	case registry.SurfaceAnthropic:
		return "anthropic"
	case registry.SurfaceOpenAI:
		return "openai"
	case registry.SurfaceGoogle:
		return "google"
	}
	if f := providerTokenFamily(t.vendorNameBasis(), t.model); f != "" {
		return f
	}
	switch t.protocol {
	case registry.ProtocolAnthropic:
		return "anthropic"
	case registry.ProtocolOpenAIChat, registry.ProtocolOpenAIResponses:
		return "openai"
	case registry.ProtocolGoogle:
		return "google"
	}
	return ""
}

// gptOSSName is the family spelling the registry classifies as deliberately
// generic: its rows declare text-only input, so neither the family map nor the
// name rule may claim a vendor's image tokenizer for it.
const gptOSSName = "gpt-oss"

// mediaFamilyFromModelFamily maps the registry's model family to the image-token
// family it bills as. The rule mirrors the registry's own family → surface rule
// (llm/registry §6.1) so one classification serves both: claude* and
// gemini*/gemma* keep their vendors' rules, gpt*/o/o-mini/o-pro are OpenAI's,
// and gpt-oss is deliberately generic — its rows declare text-only input, so no
// image rule is claimed for it. A family with no rule of its own (llama,
// minimax, kimi, deepseek) returns "" so its caller keeps looking.
func mediaFamilyFromModelFamily(family string) string {
	f := strings.ToLower(strings.TrimSpace(family))
	switch {
	case strings.HasPrefix(f, "claude"):
		return "anthropic"
	case genericModelFamily(f):
		return ""
	case strings.HasPrefix(f, "gpt"), f == "o", f == "o-mini", f == "o-pro":
		return "openai"
	case strings.HasPrefix(f, "gemini"), strings.HasPrefix(f, "gemma"):
		return "google"
	default:
		return ""
	}
}

// genericModelFamily reports whether the registry classifies a family as
// deliberately generic -- a family whose rows carry no vendor tokenizer rules at
// all, so no image family may be inferred for them from anywhere else.
func genericModelFamily(family string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(family)), gptOSSName)
}

// targetFromNames builds the name-based view used by the exported entry points
// that take a bare Request or message list.
func targetFromNames(provider, model string) targetInfo {
	name := strings.ToLower(strings.TrimSpace(provider))
	return targetInfo{provider: provider, model: model, unsignedThinking: unsignedThinkingReplayedByName(provider, model),
		namedChat: name == registry.ProtocolOpenAIChat || name == "openai-compatible"}
}

// targetFromResolved builds the exact view from a resolved registry row. Names
// the caller did not supply come from the row, so media estimation uses the
// resolved identity rather than the generic fallback.
func targetFromResolved(res registry.Resolved, provider, model string) targetInfo {
	providerFromCaller := strings.TrimSpace(provider) != ""
	if !providerFromCaller {
		provider = res.Instance
	}
	if strings.TrimSpace(model) == "" {
		model = res.ModelID
	}
	// The name fallback reads the caller's provider, never the row's instance
	// alias: the alias says where the row was reached, not who made the model.
	fallbackProvider := provider
	if !providerFromCaller {
		fallbackProvider = ""
	}
	return targetInfo{
		provider: provider, model: model,
		protocol: res.Protocol, surface: res.Surface, family: res.Model.Family,
		resolved: true, providerFromCaller: providerFromCaller,
		imageDetail:           registry.StringValue(res.Caps.ImageDetail),
		reasoningOff:          res.Caps.ReasoningDisabled(),
		chatThinkingAsText:    registry.BoolValue(res.Caps.ThinkingAsText),
		chatUsesDetails:       registry.StringValue(res.Caps.ReasoningField) == "reasoning_details",
		dropsToolResultImages: dropsToolResultImages(res),
		seenToolResultDocuments: !res.Synthesized && res.Caps.AcceptsModality("pdf") &&
			deliversToolResultDocuments(res.Protocol),
		// The estimate follows the same visibility decision request shaping
		// makes, including its rejection of synthesized rows (#4217).
		seesToolResultImages: RowSeesToolResultMedia(res, "image/png"),
		unsignedThinking:     unsignedThinkingReplayed(res, fallbackProvider, model),
	}
}

// dropsToolResultImages reports whether the resolved row's adapter leaves a
// tool-result image off the wire. The Anthropic and Responses builders emit one
// whenever the result carries bytes, cap or no cap. The chat builder emits the
// result's text alone: Chat Completions has no portable image-bearing
// tool-message representation, so its MultimodalToolResults cap only decides
// whether the image fields are stripped before serialization
// (chatcompletions/messages.go). The Google builder emits the image inside the
// function response when the row declares the cap and refuses the request
// without it (google/request.go).
func dropsToolResultImages(res registry.Resolved) bool {
	switch res.Protocol {
	case registry.ProtocolOpenAIChat:
		return true
	case registry.ProtocolGoogle:
		return !registry.BoolValue(res.Caps.MultimodalToolResults)
	default:
		return false
	}
}

// billsToolResultMedia reports whether the target's adapter puts this tool
// result's bytes on the wire, so the estimate prices only media the request
// carries. Image media follows the image rule (dropsToolResultImages). A
// document rides inline on Anthropic only (its tool_result content carries a
// document block); on Responses and Google it takes the user-message
// side-channel instead. Chat drops tool-result media entirely. An unknown
// protocol (a name-based target) cannot be ruled out, so it bills like the
// permissive default.
func (t targetInfo) billsToolResultMedia(tr *ToolResultData) bool {
	if len(tr.ImageData) == 0 && tr.ImageMediaType == "" {
		return false
	}
	mt := strings.ToLower(strings.TrimSpace(tr.ImageMediaType))
	if IsImageMediaType(mt) {
		if t.resolved {
			// A resolved row's estimate follows the same decision request
			// assembly makes: no bytes are sent to a row that cannot see them
			// (#4217), so none are billed.
			return t.seesToolResultImages
		}
		return !t.dropsToolResultImages
	}
	if IsDocumentMediaType(mt) {
		if strings.TrimSpace(t.protocol) == "" {
			// An unknown protocol (a name-based target) cannot be ruled out.
			return !t.dropsToolResultImages
		}
		if t.resolved {
			// A resolved row's estimate follows the builder's gate: a document
			// rides only for a row that declares pdf input (below), so none is
			// billed otherwise (#4465).
			return t.seenToolResultDocuments
		}
		return deliversToolResultDocuments(t.protocol)
	}
	return !t.dropsToolResultImages
}

// DeliversToolResultImages reports whether the resolved row's adapter puts
// tool-result image bytes on the wire. It answers whether a session model
// receives tool-result images natively, so consumers deciding between inline
// vision and an out-of-band description share one truth with token
// estimation.
func DeliversToolResultImages(res registry.Resolved) bool {
	return !dropsToolResultImages(res)
}

// DeliversToolResultMedia reports whether the row's adapter delivers
// tool-result bytes of this media type in a form the model can consume. Image
// media follows the per-protocol image rule. Documents are delivered natively
// on Anthropic only: its tool_result content accepts a document block, while
// the Responses builder gates tool-result bytes on image/* (image_input.go),
// Google's functionResponse omits non-image media, and chat has no
// image-bearing tool-message representation at all — those routes keep the
// user-message side-channel (#4465). An empty media type reads as the image
// default the tool layer applies.
func DeliversToolResultMedia(res registry.Resolved, mediaType string) bool {
	if IsImageMediaType(mediaType) {
		return DeliversToolResultImages(res)
	}
	if IsDocumentMediaType(mediaType) {
		return deliversToolResultDocuments(res.Protocol)
	}
	return false
}

// IsDocumentMediaType reports whether a tool result's media type is a document
// the tool layer produces. read_file hands PDFs through unchanged
// (agent/internal/tool/registry.go) and every other non-image media type is
// rejected there, so application/pdf is the document shape. Both the delivery
// predicates here and the Anthropic builder consult this one definition.
func IsDocumentMediaType(mt string) bool {
	return strings.ToLower(strings.TrimSpace(mt)) == "application/pdf"
}

// deliversToolResultDocuments reports whether the row's protocol represents a
// document inside a tool result. Anthropic's tool_result content accepts a
// document content block; the other protocols' tool-result shapes do not carry
// one, so they route documents through the side-channel.
func deliversToolResultDocuments(protocol string) bool {
	return protocol == registry.ProtocolAnthropic
}

// IsImageMediaType reports whether a tool result's media type is a raster
// image the inline path handles. An empty media type reads as the image
// default the tool layer applies. Request assembly, the delivery predicates
// and the switch marker's orphan count share this one definition.
func IsImageMediaType(mediaType string) bool {
	mt := strings.ToLower(strings.TrimSpace(mediaType))
	return mt == "" || strings.HasPrefix(mt, "image/")
}

// DeliversUserMessageDocuments reports whether the row's adapter represents
// this document part in a user message in a form the model can consume.
// Inline bytes ride every document-capable wire: the Chat builder writes a
// file part (chatcompletions/messages.go), the Responses builder writes
// input_file (responses/input.go), the Anthropic builder writes a document
// content block (anthropic/request.go; the Messages API accepts base64
// application/pdf user messages with no beta header, #4209), and Google
// writes inlineData/fileData. A URL-only document rides Anthropic's url
// source, Google's fileData, and the Responses builder's pass-through; Chat
// Completions has no URL file mechanism, so a URL-only or payload-less
// document is not delivered there. This is a different position from
// DeliversToolResultMedia: only Anthropic also carries a document inside a
// tool result, so Responses, Google and Chat drop tool-result documents to
// avoid double delivery alongside the side-channel.
func DeliversUserMessageDocuments(res registry.Resolved, doc *DocumentData) bool {
	if doc == nil || (len(doc.Data) == 0 && doc.URL == "") {
		return false
	}
	switch res.Protocol {
	case registry.ProtocolAnthropic, registry.ProtocolOpenAIResponses, registry.ProtocolGoogle:
		return true
	case registry.ProtocolOpenAIChat:
		return len(doc.Data) > 0
	default:
		return false
	}
}

// RowSeesToolResultMedia reports whether a resolved row's advertised facts let
// the model receive tool-result bytes of this media type inline: the row
// declares the media's input modality (image for image media, pdf for a
// document) and its adapter delivers tool-result media of this type. A
// synthesized row (one whose model id matched no catalog row) inherits another
// row's facts and cannot vouch for the named model, so it never qualifies —
// the same rule the profile layer applies for images. Facts depth only, so a
// caller that resolved the row without materializing credentials can still
// answer the native-vision question.
func RowSeesToolResultMedia(res registry.Resolved, mediaType string) bool {
	if res.Synthesized {
		return false
	}
	if !DeliversToolResultMedia(res, mediaType) {
		return false
	}
	mt := strings.ToLower(strings.TrimSpace(mediaType))
	if mt == "" || strings.HasPrefix(mt, "image/") {
		return res.Caps.AcceptsModality("image")
	}
	if IsDocumentMediaType(mt) {
		return res.Caps.AcceptsModality("pdf")
	}
	return false
}

// unsignedThinkingReplayed reports whether the adapter the resolved target
// selects replays a ContentThinking part that carries text and no replay
// metadata:
//
//   - the Anthropic Messages adapter emits the block it is given as
//     {"type":"thinking","thinking":text} (anthropic/request.go);
//   - the OpenAI-compatible chat adapter replays it on the reasoning field, or
//     merges it into content for thinking_as_text (chatcompletions/messages.go);
//   - the OpenAI Responses adapter re-sends plaintext reasoning (#6709) too,
//     but only when the item carries native typed parts; Google drops the part.
//
// A row with no resolved protocol falls back to names, because such a row still
// knows what it was named. Those are the effective provider and model — the row's
// own only where the caller supplied none — so a partially resolved row cannot
// drop the vendor the request names and undercount what a name-based caller
// bills correctly.
func unsignedThinkingReplayed(res registry.Resolved, provider, model string) bool {
	switch res.Protocol {
	case registry.ProtocolAnthropic:
		return true
	case registry.ProtocolOpenAIChat:
		// One replay policy with request assembly: bill what the builder sends.
		return ReplaysAssistantThinking(res)
	case "":
		return unsignedThinkingReplayedByName(provider, model)
	default:
		return false
	}
}

// ReplaysAssistantThinking reports whether the resolved row's adapter sends
// assistant thinking back as model-visible request content. An openai-chat
// target replays its full history unless the row declares reasoning = false;
// a thinking_as_text row merges it into assistant content instead. Anthropic,
// Google and OpenAI Responses replay theirs under provenance rules.
func ReplaysAssistantThinking(res registry.Resolved) bool {
	if res.Protocol != registry.ProtocolOpenAIChat {
		return true
	}
	return !res.Caps.ReasoningDisabled() || registry.BoolValue(res.Caps.ThinkingAsText)
}

// unsignedThinkingReplayedByName is the fallback for callers with no resolved
// row. The provider name is consulted first, so a model name cannot override a
// provider that selects a non-replaying adapter; a Claude model is the last
// resort, for an alias whose name selects nothing.
//
// It can only be as good as the names: a curated OpenAI-compatible chat
// provider whose name carries no marker (ollama, groq, zai, moonshotai, a
// non-Claude openrouter row) and an instance alias that hides its base are both
// under-billed here. Callers holding a resolved row avoid that by using
// EstimateInputTokensForResolved or EstimateMessagesInputTokensForResolved.
func unsignedThinkingReplayedByName(provider, model string) bool {
	p := strings.ToLower(strings.TrimSpace(provider))
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.Contains(p, "anthropic"):
		// Also google-vertex-anthropic and anthropic-compatible, which the
		// Anthropic protocol serves.
		return true
	case strings.Contains(p, "google") || strings.Contains(p, "gemini"):
		// google-compatible names the Google protocol, which drops the part.
		return false
	case strings.Contains(p, "compat") || strings.Contains(p, "openai-chat"):
		return true
	case strings.Contains(p, "openai"):
		// A bare Text projection never replays on Responses; only native typed parts do.
		return false
	case strings.Contains(m, "claude"):
		return true
	default:
		return false
	}
}

func estimateImageTokens(t targetInfo, img *ImageData) int {
	if img == nil {
		return 0
	}
	family := t.mediaFamily()
	detail := imageDetail(t.protocol, img.Detail, t.imageDetail)
	if family == "openai" && strings.EqualFold(strings.TrimSpace(detail), "low") {
		// OpenAI bills a low-detail image a fixed 85 tokens without reading its
		// dimensions, so an image whose dimensions cannot be decoded locally (a
		// remote URL) is still estimable -- and exactly -- when the detail that
		// rides the wire is low. The undecoded fallback below would overbill it.
		return 85
	}
	width, height, ok := imageDimensions(img)
	if !ok {
		return fallbackMediaTokens + len(img.URL)/4 + len(img.MediaType)/4 + len(img.Detail)/4
	}
	switch family {
	case "google":
		return estimateGoogleImageTokens(width, height)
	case "anthropic":
		return estimateAnthropicImageTokens(width, height)
	case "openai":
		return estimateOpenAIImageTokens(width, height)
	default:
		return fallbackMediaTokens + len(img.MediaType)/4 + len(img.Detail)/4
	}
}

func imageDimensions(img *ImageData) (int, int, bool) {
	if len(img.Data) > 0 {
		cfg, _, err := image.DecodeConfig(bytes.NewReader(img.Data))
		if err == nil && cfg.Width > 0 && cfg.Height > 0 {
			return cfg.Width, cfg.Height, true
		}
		return 0, 0, false
	}
	u := strings.TrimSpace(img.URL)
	if !IsLocalPath(u) {
		return 0, 0, false
	}
	f, err := os.Open(ExpandTilde(u))
	if err != nil {
		return 0, 0, false
	}
	defer func() { _ = f.Close() }()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, 0, false
	}
	return cfg.Width, cfg.Height, true
}

func providerTokenFamily(provider, model string) string {
	p := strings.ToLower(strings.TrimSpace(provider))
	m := strings.ToLower(strings.TrimSpace(model))
	// Namespaced ids are the same model: openai/gpt-4o and openai.o4 are OpenAI's,
	// exactly like their bare spellings, so the namespace comes off once for every
	// branch below.
	m = strings.TrimPrefix(strings.TrimPrefix(m, "openai/"), "openai.")
	// gpt-oss is deliberately generic in the registry (§6.1) — its rows declare
	// text-only input — so no spelling of it may claim OpenAI's image rules. The
	// family map already refuses it for a resolved row, but that refusal is
	// invisible here: "gpt-" matches the OpenAI branch, and a gateway's
	// openai/gpt-oss name matches the leading-"o" one, so without this the name
	// stage re-claims the tokenizer the family map just declined.
	if strings.Contains(m, gptOSSName) {
		return ""
	}
	switch {
	case p == "google" || p == "gemini" || strings.Contains(m, "gemini") || strings.Contains(m, "gemma"):
		return "google"
	case strings.Contains(p, "anthropic") || strings.Contains(m, "claude"):
		return "anthropic"
	case strings.Contains(p, "openai") || strings.HasPrefix(m, "gpt-") || oSeriesModelName(m):
		return "openai"
	default:
		return ""
	}
}

// localInputEstimate is CountInputTokens' local half: a resolved target estimates
// from its row, and the provider name it may read as vendor evidence is the
// caller's own, never the instance the request resolved to.
func localInputEstimate(req Request, callerProvider string, t dispatchTarget) InputTokenCount {
	if !t.resolved {
		return EstimateInputTokens(req)
	}
	estimateReq := req
	estimateReq.Provider = callerProvider
	estimateReq = withEffectiveToolPrice(estimateReq, t.res, t.override != nil)
	return EstimateInputTokensForResolved(t.res, estimateReq)
}

// EstimatorTargetsEquivalent reports whether two resolved rows are one estimator
// target: the fields the local estimate reads decide -- including the model id,
// which the media-family name rule reads when the row's own facts do not -- so a
// caller that keeps a measurement keyed to a target does not have to re-list them
// and drift when the rules grow.
func EstimatorTargetsEquivalent(a, b registry.Resolved) bool {
	if a.Protocol != b.Protocol || a.Surface != b.Surface || a.Model.Family != b.Model.Family {
		return false
	}
	if a.ModelID != b.ModelID {
		return false
	}
	if registry.BoolValue(a.Caps.ThinkingAsText) != registry.BoolValue(b.Caps.ThinkingAsText) {
		return false
	}
	if a.Protocol == registry.ProtocolOpenAIChat && !a.Caps.ReasoningDisabled() && !registry.BoolValue(a.Caps.ThinkingAsText) &&
		(registry.StringValue(a.Caps.ReasoningField) == "reasoning_details") != (registry.StringValue(b.Caps.ReasoningField) == "reasoning_details") {
		return false
	}
	if registry.StringValue(a.Caps.ImageDetail) != registry.StringValue(b.Caps.ImageDetail) {
		return false
	}
	if registry.BoolValue(a.Caps.MultimodalToolResults) != registry.BoolValue(b.Caps.MultimodalToolResults) {
		return false
	}
	// Effective tool-result visibility decides how much media a request carries
	// and what the estimate prices, so a metadata refresh that changes it must
	// invalidate a recorded measurement (#4217).
	for _, mediaType := range []string{"image/png", "application/pdf"} {
		if RowSeesToolResultMedia(a, mediaType) != RowSeesToolResultMedia(b, mediaType) {
			return false
		}
	}
	return a.Caps.ReasoningDisabled() == b.Caps.ReasoningDisabled()
}

// oSeriesModelName matches the OpenAI o-series ids the name rule may claim: "o"
// alone, an "o-" prefix (o-mini, o-pro), "o" followed by a digit (o1, o3,
// o4-mini), and the namespaced spellings of those (openai/o3, openai.o3). A bare
// leading "o" is not an OpenAI claim: open-mistral, olmo, openchat, openrouter/*
// and ollama/* are other models behind other protocols.
func oSeriesModelName(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	m = strings.TrimPrefix(m, "openai/")
	m = strings.TrimPrefix(m, "openai.")
	if m == "o" || strings.HasPrefix(m, "o-") {
		return true
	}
	return len(m) >= 2 && m[0] == 'o' && m[1] >= '0' && m[1] <= '9'
}

func estimateGoogleImageTokens(width, height int) int {
	if width <= 384 && height <= 384 {
		return 258
	}
	return 258 * ceilDiv(width, 768) * ceilDiv(height, 768)
}

func estimateAnthropicImageTokens(width, height int) int {
	return ceilDiv(width, 28) * ceilDiv(height, 28)
}

func estimateOpenAIImageTokens(width, height int) int {
	w, h := scaleWithin(float64(width), float64(height), 2048)
	short := math.Min(w, h)
	if short > 768 {
		scale := 768 / short
		w *= scale
		h *= scale
	}
	return 85 + 170*ceilDiv(int(math.Ceil(w)), 512)*ceilDiv(int(math.Ceil(h)), 512)
}

func scaleWithin(width, height, maxSide float64) (float64, float64) {
	long := math.Max(width, height)
	if long <= maxSide {
		return width, height
	}
	scale := maxSide / long
	return width * scale, height * scale
}

func ceilDiv(n, d int) int {
	if n <= 0 {
		return 0
	}
	return (n + d - 1) / d
}
