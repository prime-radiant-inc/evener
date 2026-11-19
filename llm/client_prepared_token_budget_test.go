package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

// wireToolParams is the parameter schema of each tool as the server received
// it. The wire body is the oracle for what the builder emitted: a price written
// here would be a second opinion about the same question, which is the defect
// this file exists to catch.
func wireToolParams(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	raw, ok := body["tools"].([]any)
	if !ok {
		t.Fatalf("wire tools = %#v, want the tool list", body["tools"])
	}
	out := make([]map[string]any, len(raw))
	for i, entry := range raw {
		e, _ := entry.(map[string]any)
		params, _ := e["parameters"].(map[string]any)
		out[i] = params
	}
	return out
}

// emittedPriceServer answers the Responses and Chat endpoints with one assistant
// message, records every body, and reports token counting as unsupported so a
// caller's reading is the local estimate.
func emittedPriceServer(t *testing.T) (*httptest.Server, func() []map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/responses":
			_, _ = w.Write([]byte(`{"id":"resp_1","model":"m","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
		case "/chat/completions":
			_, _ = w.Write([]byte(`{"id":"chatcmpl-1","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]any(nil), bodies...)
	}
}

// emittedPriceRoute builds a client dispatching to an instance of the given
// protocol that carries maxInput, has no registered override, and reaches the
// test server. No override is registered because an override route marshals the
// definitions it is handed; the case under test is the built-in builder.
func emittedPriceRoute(t *testing.T, maxInput int, protocol string, strict bool) (*Client, registry.Resolved, func() []map[string]any) {
	t.Helper()
	srv, bodies := emittedPriceServer(t)
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(map[string]registry.Provider{
			"emitted-price": {
				Base:      "openai",
				Protocol:  protocol,
				Surface:   registry.SurfaceGeneric,
				APIKey:    "unused",
				Transport: registry.Transport{BaseURL: srv.URL},
				Caps: registry.Caps{
					ContextWindow:   new(1_000_000),
					MaxInputTokens:  new(maxInput),
					MaxOutputTokens: new(4_096),
					StrictTools:     new(strict),
				},
			},
		}),
	)
	if err != nil {
		t.Fatalf("registry.Load: %v", err)
	}
	c := NewClient(WithRegistry(r))
	res, err := c.Resolve("emitted-price/model")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Caps.MaxInputTokens == nil || *res.Caps.MaxInputTokens != maxInput {
		t.Fatalf("resolved max_input = %v, want %d", res.Caps.MaxInputTokens, maxInput)
	}
	return c, res, bodies
}

// capBetweenPrices returns a cap strictly between two input prices, settling the
// safety reserve the cap itself decides.
func capBetweenPrices(t *testing.T, lower, upper int) int {
	t.Helper()
	if upper <= lower {
		t.Fatalf("fixture does not straddle a cap: lower=%d upper=%d", lower, upper)
	}
	limit := lower + minimumTokenSafetyReserve + (upper-lower)/2
	for range 16 {
		next := lower + SafetyReserveTokens(limit) + (upper-lower)/2
		if next == limit {
			break
		}
		limit = next
	}
	safety := SafetyReserveTokens(limit)
	if lower+safety >= limit {
		t.Fatalf("cap %d does not admit the lower price %d", limit, lower)
	}
	if upper+safety <= limit {
		t.Fatalf("cap %d does not refuse the upper price %d", limit, upper)
	}
	return limit
}

// A parameterless tool's substituted schema has to reach admission, not just the
// pricing helper.
//
// The caller advertises tools with no parameters, so a count measured over the
// request as written omits the schema the builder supplies. A cap between the
// two prices must refuse the request: admitting it on the smaller count
// dispatches a request the budget would have rejected.
func TestAdmissionRefusesARequestBilledBeforeTheSubstitution(t *testing.T) {
	defs := parameterlessCatalog(24)
	req := Request{
		Provider: "emitted-price",
		Model:    "model",
		Messages: []Message{User("x")},
		Tools:    defs,
	}
	_, probe, _ := emittedPriceRoute(t, 1_000_000, registry.ProtocolOpenAIResponses, false)
	advertised := EstimateInputTokensForResolved(probe, req).Tokens
	emitted := EstimateInputTokensForResolved(probe, priceEmittedToolSchemas(req, probe)).Tokens
	if emitted <= advertised {
		t.Fatalf("fixture does not distinguish the two readings: advertised=%d emitted=%d", advertised, emitted)
	}
	limit := capBetweenPrices(t, advertised, emitted)

	// A cap that admits the advertised price and not the emitted one.
	c, _, bodies := emittedPriceRoute(t, limit, registry.ProtocolOpenAIResponses, false)
	_, err := c.Complete(context.Background(), req)
	var budgetErr *ContextBudgetError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("error = %v, want *ContextBudgetError: the request was admitted on %d input tokens, the "+
			"price of the list before the schema is substituted (%d once emitted), and %d request(s) reached "+
			"the wire", err, advertised, emitted, len(bodies()))
	}
	if budgetErr.Limit != "max_input" || budgetErr.Maximum != limit {
		t.Fatalf("budget error = %+v, want max_input limit %d", budgetErr, limit)
	}
	if budgetErr.InputTokens != emitted+SafetyReserveTokens(limit) {
		t.Fatalf("admitted input = %d, want %d: the count without the substituted schema is %d",
			budgetErr.InputTokens, emitted+SafetyReserveTokens(limit), advertised+SafetyReserveTokens(limit))
	}
	if got := len(bodies()); got != 0 {
		t.Fatalf("requests on the wire = %d, want 0: a blocked request must not dispatch", got)
	}

	// The same request under a cap above both prices goes out, and the schema on
	// the wire is the one the price described.
	open, _, openBodies := emittedPriceRoute(t, 1_000_000, registry.ProtocolOpenAIResponses, false)
	if _, err := open.Complete(context.Background(), req); err != nil {
		t.Fatalf("Complete under an ample cap: %v", err)
	}
	if got := len(openBodies()); got != 1 {
		t.Fatalf("requests on the wire = %d, want 1", got)
	}
	wire := wireToolParams(t, openBodies()[0])
	if len(wire) != len(defs) {
		t.Fatalf("wire carries %d tools, want %d", len(wire), len(defs))
	}
	for i := range wire {
		if wire[i] == nil {
			t.Fatalf("wire tool %d carries no parameters: the builder substituted a schema for it, and that is "+
				"exactly the schema the price must include", i)
		}
	}
}

// A strict route's schema has to reach admission the same way the substitution
// does.
//
// The catalog is identical on both routes; only the capability differs. Under a
// cap between the projected price and the advertised one, the strict-capable
// route must refuse and the route without the capability must admit. That pair is
// what proves the price follows the emitted bytes rather than simply growing
// every request: the second half is the same request, the same cap, and the same
// catalog, priced by a builder that sends it unchanged.
//
// The catalog is parameterless declarations, which is where the capability
// changes the bytes on this protocol: a certified schema is projected without
// growing it, so only the closed empty object a strict route substitutes costs
// more than the open one every route sends.
func TestAdmissionPricesTheStrictProjectionAndOnlyOnACapableRoute(t *testing.T) {
	defs := preparedParameterlessCatalog(24)
	req := Request{
		Provider: "emitted-price",
		Model:    "model",
		Messages: []Message{User("x")},
		Tools:    defs,
	}
	_, strictProbe, _ := emittedPriceRoute(t, 1_000_000, registry.ProtocolOpenAIResponses, true)
	advertised := EstimateInputTokensForResolved(strictProbe, req).Tokens
	projected := EstimateInputTokensForResolved(strictProbe, priceEmittedToolSchemas(req, strictProbe)).Tokens
	if projected <= advertised {
		t.Fatalf("fixture does not distinguish the two readings: advertised=%d projected=%d",
			advertised, projected)
	}
	// The control the fixture rests on: on a route without the capability the
	// same catalog is sent unchanged, so its price is the advertised one.
	_, relaxedProbe, _ := emittedPriceRoute(t, 1_000_000, registry.ProtocolOpenAIResponses, false)
	if got := EstimateInputTokensForResolved(relaxedProbe, priceEmittedToolSchemas(req, relaxedProbe)).Tokens; got != advertised {
		t.Fatalf("route without strict enforcement priced the catalog at %d input tokens, want the advertised %d",
			got, advertised)
	}

	limit := capBetweenPrices(t, advertised, projected)

	strict, _, strictBodies := emittedPriceRoute(t, limit, registry.ProtocolOpenAIResponses, true)
	_, strictErr := strict.Complete(context.Background(), req)
	if _, ok := errors.AsType[*ContextBudgetError](strictErr); !ok {
		t.Fatalf("strict route error = %v, want *ContextBudgetError: it admitted the request on the advertised "+
			"price %d, the projection costs %d, and %d request(s) reached the wire",
			strictErr, advertised, projected, len(strictBodies()))
	}

	relaxed, _, relaxedBodies := emittedPriceRoute(t, limit, registry.ProtocolOpenAIResponses, false)
	if _, err := relaxed.Complete(context.Background(), req); err != nil {
		t.Fatalf("route without the capability refused the request: %v: its builder sends this catalog "+
			"unchanged, so its price is %d under a cap of %d", err, advertised, limit)
	}
	if got := len(relaxedBodies()); got != 1 {
		t.Fatalf("relaxed route requests on the wire = %d, want 1", got)
	}
	// And the control route's wire really is unprojected, or the pair above
	// compares two routes that emit the same thing.
	for i, params := range wireToolParams(t, relaxedBodies()[0]) {
		if _, closed := params["additionalProperties"]; closed {
			t.Fatalf("relaxed route wire tool %d is projected (additionalProperties present); the pair must "+
				"compare a projected route against an unprojected one", i)
		}
	}
}

// preparedParameterlessCatalog is tools prepared the way Generate prepares them:
// a no-argument declaration carrying the open empty object, so the strict
// decision is the only thing that can replace it.
func preparedParameterlessCatalog(n int) []ToolDefinition {
	defs := parameterlessCatalog(n)
	for i := range defs {
		defs[i] = defs[i].WithNoParametersDeclared()
	}
	return defs
}

// The dispatcher hands the wire the list the price describes.
//
// Admission prices a local copy, so this pins that the substitution the price is
// computed over is not written into the request: the builder still substitutes at
// build time, and the count admission took describes what it will produce.
func TestAdmissionPricesWithoutRewritingTheRequest(t *testing.T) {
	defs := parameterlessCatalog(2)
	c, res, bodies := emittedPriceRoute(t, 1_000_000, registry.ProtocolOpenAIResponses, false)

	req := Request{
		Provider: "emitted-price",
		Model:    "model",
		Messages: []Message{User("x")},
		Tools:    defs,
	}
	// The caller's own definitions stay untouched by the price.
	if _, err := c.Complete(context.Background(), req); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	for i, d := range req.Tools {
		if d.Parameters != nil {
			t.Fatalf("the price rewrote the caller's tool %d to %v; a price is a measurement, not a request "+
				"edit", i, d.Parameters)
		}
	}

	if got := len(bodies()); got != 1 {
		t.Fatalf("requests on the wire = %d, want 1", got)
	}
	wire := wireToolParams(t, bodies()[0])
	if len(wire) != len(defs) {
		t.Fatalf("wire carries %d tools, want %d", len(wire), len(defs))
	}
	p, ok := ProtocolFor(res.Protocol)
	if !ok {
		t.Fatalf("no protocol for %q", res.Protocol)
	}
	emitted, changed := p.(ToolSchemaDeriver).EmitToolSchemas(defs, res)
	if !changed {
		t.Fatal("fixture does not make the builder change anything")
	}
	for i := range wire {
		if wire[i] == nil {
			t.Fatalf("wire tool %d carries no parameters, want the substituted schema", i)
		}
	}
	// The wire schema for each tool is the schema the deriver reported, which is
	// what the price was measured over.
	for i := range emitted {
		a, _ := json.Marshal(wire[i])
		b, _ := json.Marshal(emitted[i].Parameters)
		if !bytes.Equal(a, b) {
			t.Fatalf("wire tool %d parameters = %s, priced over %s", i, a, b)
		}
	}
}

// The override branch must not be priced through a protocol's deriver.
//
// An override marshals the definition it is handed, so its received count is its
// wire count. Pricing it through the deriver would bill an emitted list the wire
// never carries, and the count the adapter observes would describe bytes it does
// not send.
func TestAnOverrideServedRouteKeepsTheAdvertisedCount(t *testing.T) {
	defs := parameterlessCatalog(8)
	adapter := &recordingPriceAdapter{name: "emitted-price"}
	// The client resolves the instance to a Responses row, and an override
	// registered under that name serves the dispatch instead of the protocol.
	c, _, _ := emittedPriceRoute(t, 1_000_000, registry.ProtocolOpenAIResponses, true)
	c.Register(adapter)

	req := Request{
		Provider: "emitted-price",
		Model:    "model",
		Messages: []Message{User("x")},
		Tools:    defs,
	}
	// A stamped request, so the count the adapter observes is a value the price
	// could have changed rather than one it simply left at zero.
	req.ToolsJSONBytes = ToolsJSONBytes(defs)
	if _, err := c.Complete(context.Background(), req); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	dispatched := adapter.last()
	advertisedBytes := ToolsJSONBytes(defs)
	if got := dispatched.ToolsJSONBytes; got != advertisedBytes {
		t.Fatalf("override observed ToolsJSONBytes = %d, want the advertised %d: an override marshals the "+
			"definitions it is handed, so a protocol's emitted count describes a list it never sends",
			got, advertisedBytes)
	}
	if got := ToolsJSONBytes(dispatched.Tools); got != advertisedBytes {
		t.Fatalf("override received a list measuring %d bytes, want the advertised %d", got, advertisedBytes)
	}
	for i, d := range dispatched.Tools {
		if d.Parameters != nil {
			t.Fatalf("override received parameters for tool %d, want the caller's nil", i)
		}
	}
}

// recordingPriceAdapter records the request it is handed.
type recordingPriceAdapter struct {
	name string
	mu   sync.Mutex
	reqs []Request
}

func (a *recordingPriceAdapter) Name() string { return a.name }

func (a *recordingPriceAdapter) Complete(_ context.Context, req Request) (Response, error) {
	a.mu.Lock()
	a.reqs = append(a.reqs, req)
	a.mu.Unlock()
	return Response{Message: Assistant("ok")}, nil
}

func (a *recordingPriceAdapter) Stream(context.Context, Request) (Stream, error) {
	return nil, ErrStreamUnsupported
}

func (a *recordingPriceAdapter) last() Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.reqs[len(a.reqs)-1]
}

// CountInputTokens is what a caller sizes a request with, so the reading has to
// describe the body that dispatches.
//
// A route whose builder substitutes a schema emits more than the advertised
// list. A reading over the advertised list is smaller than the request that goes
// out, so a caller that fits its request to the reading meets the shortfall as a
// context-budget rejection from Complete. Chat Completions reports token counting
// as unsupported, which is what makes the local reading the answer here.
func TestCountInputTokensPricesWhatTheRouteEmits(t *testing.T) {
	t.Parallel()
	defs := strictGrowthCatalog(8)
	c, res, _ := emittedPriceRoute(t, 1_000_000, registry.ProtocolOpenAIChat, true)
	req := Request{
		Provider: "emitted-price",
		Model:    "model",
		Messages: []Message{User("x")},
		Tools:    defs,
	}

	got, err := c.CountInputTokens(context.Background(), req)
	if err != nil {
		t.Fatalf("CountInputTokens: %v", err)
	}
	advertised := EstimateInputTokensForResolved(res, req).Tokens
	emitted := EstimateInputTokensForResolved(res, priceEmittedToolSchemas(req, res)).Tokens
	if emitted <= advertised {
		t.Fatalf("fixture does not distinguish the two readings: advertised=%d emitted=%d",
			advertised, emitted)
	}
	if got.Tokens != emitted {
		t.Fatalf("CountInputTokens = %d, want %d: a caller sizing a request from this count would fit "+
			"it to %d and then meet the shortfall as a budget rejection from Complete",
			got.Tokens, emitted, advertised)
	}
}

// An unsubstituted request must still price exactly as it did before.
//
// A catalog that carries every schema its builder will send has nothing to
// substitute and nothing to project on a route without the capability, so the
// price must be the advertised count. A fix that shifted every price would show
// here as a count that moved when the wire did not.
func TestAnUnsubstitutedRequestPricesAsBefore(t *testing.T) {
	t.Parallel()
	defs := unstructuredCatalog()
	advertised := ToolsJSONBytes(defs)
	req := Request{
		Provider:       "emitted-price",
		Model:          "model",
		Messages:       []Message{User("x")},
		Tools:          defs,
		ToolsJSONBytes: advertised,
	}

	// A route without strict enforcement sends every schema as advertised.
	if got := priceEmittedToolSchemas(req, responsesRowOn(false)); got.ToolsJSONBytes != advertised {
		t.Fatalf("unsubstituted count = %d, want the advertised %d: this catalog carries every schema its "+
			"builder sends", got.ToolsJSONBytes, advertised)
	}
	// A strict route must not report a change either: these required lists are
	// already complete, so the projection only reorders them and the bytes are
	// the same.
	if got := priceEmittedToolSchemas(req, responsesRowOn(true)); got.ToolsJSONBytes != advertised {
		t.Fatalf("strict count = %d, want the advertised %d: the projection only reorders an already complete "+
			"required list, so the emitted bytes are the same", got.ToolsJSONBytes, advertised)
	}
	// And the estimate over the priced request equals the estimate over the
	// original, which is the reading every caller already had.
	res := responsesRowOn(true)
	if a, b := EstimateInputTokensForResolved(res, req).Tokens,
		EstimateInputTokensForResolved(res, priceEmittedToolSchemas(req, res)).Tokens; a != b {
		t.Fatalf("estimate moved from %d to %d for a catalog whose emitted bytes are unchanged", a, b)
	}
}

// unstructuredCatalog is every tool with parameters and no strict projection
// work left to do.
func unstructuredCatalog() []ToolDefinition {
	return []ToolDefinition{
		{
			Name:        "write_file",
			Description: "write a file",
			Parameters: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"path":    map[string]any{"type": "string"},
					"content": map[string]any{"type": "string"},
				},
				"required": []string{"content", "path"},
			},
		},
		{
			Name:        "read_file",
			Description: "read a file",
			Parameters: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties":           map[string]any{"path": map[string]any{"type": "string"}},
				"required":             []string{"path"},
			},
		},
	}
}

// strictGrowthCatalog is tools whose parameters gain bytes under a strict
// projection: a closed scalar value set gains the explicit type it implies, and
// the object is closed with its required list stated.
//
// The schema is certified for strict mode on purpose. A projection only runs on
// a schema the gate can verify it preserves — an object whose properties are
// already all required and whose root is already closed — and a fixture outside
// that set would price as unchanged and pin nothing. The enum is what makes the
// projection cost bytes here.
func strictGrowthCatalog(n int) []ToolDefinition {
	defs := make([]ToolDefinition, n)
	for i := range defs {
		defs[i] = ToolDefinition{
			Name: "write_file_" + string(rune('a'+i%26)) + string(rune('0'+i/26)),
			Parameters: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"path": map[string]any{"type": "string"},
					// A closed value set with no stated type: the projection
					// states the type it implies, which is the byte growth this
					// catalog exists to price.
					"mode": map[string]any{"enum": []any{"read", "write"}},
				},
				"required": []string{"path", "mode"},
			},
		}
	}
	return defs
}

// The emitted price and the parameterless delta describe the same bytes, and
// they must be charged once.
//
// Admission prices the list the builder emits, which already carries the
// substituted schemas for a parameterless tool, and then applies the adapter's
// parameterless byte adjustment. Applied to the same request, the second step
// would charge those bytes again: an input cap between the two counts refuses a
// request that fits, and the count a caller sizes with is larger than the request
// that dispatches. The recorded target is what tells the two apart, so this
// pins each of the four paths that reach the budget.
func TestTheParameterlessDeltaIsChargedOnceOverAnEmittedPrice(t *testing.T) {
	t.Parallel()
	defs := preparedParameterlessCatalog(4)
	strict, relaxed := responsesRowOn(true), responsesRowOn(false)
	advertised := ToolsJSONBytes(defs)

	p, ok := ProtocolFor(strict.Protocol)
	if !ok {
		t.Fatalf("no protocol for %q", strict.Protocol)
	}
	sent, changed := p.(ToolSchemaDeriver).EmitToolSchemas(defs, strict)
	if !changed {
		t.Fatal("fixture does not make the builder change anything")
	}
	emitted := ToolsJSONBytes(sent)
	if emitted == advertised {
		t.Fatalf("fixture does not distinguish the two counts: both %d", advertised)
	}

	// Priced for this target: the emitted count is the whole charge.
	priced := priceEmittedToolSchemas(Request{Tools: defs, ToolsJSONBytes: advertised}, strict)
	if got := effectiveToolsJSONBytes(priced, strict, false); got != emitted {
		t.Fatalf("admitted input prices the catalog at %d, want the emitted %d: the parameterless delta was "+
			"charged a second time over a count that already includes it", got, emitted)
	}
	// Unpriced: the delta is how this path reaches the same number.
	if got := effectiveToolsJSONBytes(Request{Tools: defs, ToolsJSONBytes: advertised}, strict, false); got != emitted {
		t.Fatalf("unpriced admission prices the catalog at %d, want the emitted %d", got, emitted)
	}
	// Retargeted: the other route's own answer, not the strict charge.
	retargeted := priceEmittedToolSchemas(priced, relaxed)
	if got := effectiveToolsJSONBytes(retargeted, relaxed, false); got != advertised {
		t.Fatalf("retargeted admission prices the catalog at %d, want the advertised %d: this route sends the "+
			"definitions it was handed", got, advertised)
	}
	// An override marshals the definitions it is handed, so the delta is the only
	// charge and the advertised stamp is the baseline.
	if got := effectiveToolsJSONBytes(Request{Tools: defs, ToolsJSONBytes: advertised}, strict, true); got != advertised {
		t.Fatalf("override admission prices the catalog at %d, want the advertised %d", got, advertised)
	}
}
