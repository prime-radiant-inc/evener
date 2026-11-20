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

// wireToolParams reads the parameter schema of every tool the server received.
// The wire body is the oracle for what the builder emitted: a price written here
// would be a second opinion about the same question, which is the defect these
// cases exist to catch.
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

// wireToolDefs reads the tool list a route put on the wire, as the definitions
// ToolsJSONBytes measures: the name, the description, and the parameter schema
// the builder emitted.
func wireToolDefs(t *testing.T, body map[string]any, schemaKey string) []ToolDefinition {
	t.Helper()
	entries, ok := body["tools"].([]any)
	if !ok {
		t.Fatalf("wire tools = %#v, want the tool list", body["tools"])
	}
	if len(entries) == 1 {
		// Google nests its declarations one level down.
		if nested, ok := entries[0].(map[string]any); ok {
			if decls, ok := nested["functionDeclarations"].([]any); ok {
				entries = decls
			}
		}
	}
	out := make([]ToolDefinition, len(entries))
	for i, entry := range entries {
		e, _ := entry.(map[string]any)
		params, ok := e[schemaKey].(map[string]any)
		if !ok {
			t.Fatalf("wire tool %d carries no %q schema: %#v", i, schemaKey, e)
		}
		name, _ := e["name"].(string)
		description, _ := e["description"].(string)
		out[i] = ToolDefinition{Name: name, Description: description, Parameters: params}
	}
	return out
}

// emittedPriceServer answers one message per protocol and records every request
// body, so a case can read the tool list that actually dispatched. Every
// protocol answers here because the price under test is the one each route's own
// builder emits.
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
		case "/messages":
			_, _ = w.Write([]byte(`{"id":"msg_1","model":"model","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
		case "/models/model:generateContent":
			_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2},"modelVersion":"model"}`))
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
// definitions it is handed; the cases under test are the built-in builders.
func emittedPriceRoute(t *testing.T, maxInput int, protocol string, strict bool) (*Client, registry.Resolved, func() []map[string]any) {
	return emittedPriceRouteWithTransportBody(t, maxInput, protocol, strict, nil)
}

// emittedPriceRouteWithTransportBody is emittedPriceRoute with the row's
// transport body constants set, so a case can put a value on the wire that the
// builder does not write. The count-tokens endpoint is pinned unsupported: the
// reading these cases compare is the local estimate, which is the fallback a
// caller actually sizes a request with on a route that has no counter.
func emittedPriceRouteWithTransportBody(t *testing.T, maxInput int, protocol string, strict bool, body map[string]any) (*Client, registry.Resolved, func() []map[string]any) {
	t.Helper()
	srv, bodies := emittedPriceServer(t)
	base := "openai"
	switch protocol {
	case registry.ProtocolAnthropic, registry.ProtocolGoogle:
		base = protocol
	}
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(map[string]registry.Provider{
			"emitted-price": {
				Base:      base,
				Protocol:  protocol,
				Surface:   registry.SurfaceGeneric,
				APIKey:    "unused",
				Transport: registry.Transport{BaseURL: srv.URL, Body: body, CountTokensEndpoint: registry.EndpointUnsupported},
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
	if res.Protocol != protocol {
		t.Fatalf("resolved protocol = %q, want %q: the fixture must dispatch the route under test", res.Protocol, protocol)
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
	emitted := EstimateInputTokensForRoute(probe, req, false).Tokens
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

// A route that substitutes a schema for a parameterless tool has to price that
// substitution before dispatch, on every protocol that does it.
//
// The Anthropic and Google builders write an open empty object where the caller
// declared none, and the price is taken over the list the builder emits. The
// catalog's own count omits them: an input cap between the two readings admits
// the request in preflight and then refuses the dispatch, which is the
// disagreement this pins. Both entry points are covered because both admit
// through the same budget.
func TestAParameterlessToolIsPricedByTheEmittedSchema(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		protocol  string
		schemaKey string
	}{
		{name: "anthropic", protocol: registry.ProtocolAnthropic, schemaKey: "input_schema"},
		{name: "google", protocol: registry.ProtocolGoogle, schemaKey: "parameters"},
	} {
		for _, entry := range []struct {
			name string
			call func(*testing.T, *Client, Request) error
		}{
			{
				name: "complete",
				call: func(t *testing.T, c *Client, req Request) error {
					t.Helper()
					_, err := c.Complete(context.Background(), req)
					return err
				},
			},
			{
				name: "stream",
				call: func(t *testing.T, c *Client, req Request) error {
					t.Helper()
					st, err := c.Stream(context.Background(), req)
					if err != nil {
						return err
					}
					for range st.Events() {
					}
					return st.Close()
				},
			},
		} {
			t.Run(tc.name+"/"+entry.name, func(t *testing.T) {
				t.Parallel()
				defs := substitutedParameterlessCatalog(8)
				advertised := ToolsJSONBytes(defs)
				req := Request{
					Provider:       "emitted-price",
					Model:          "model",
					Messages:       []Message{User("x")},
					Tools:          defs,
					ToolsJSONBytes: advertised,
				}

				// The emitted list, read from the wire, is what the request costs.
				ample, ampleRes, ampleBodies := emittedPriceRoute(t, 1_000_000, tc.protocol, false)
				if _, err := ample.Complete(context.Background(), req); err != nil {
					t.Fatalf("Complete under an ample cap: %v", err)
				}
				dispatched := ampleBodies()
				if len(dispatched) != 1 {
					t.Fatalf("requests on the wire = %d, want 1", len(dispatched))
				}
				wire := ToolsJSONBytes(wireToolDefs(t, dispatched[0], tc.schemaKey))
				admittedReading := EstimateInputTokensForResolved(ampleRes, req).Tokens
				wireReading := EstimateInputTokensForRoute(ampleRes, req, false).Tokens
				if wireReading <= admittedReading {
					t.Fatalf("fixture does not distinguish the two readings: catalog=%d wire=%d",
						admittedReading, wireReading)
				}

				// A cap between the catalog's reading and the emitted one. Preflight
				// has to refuse the request the dispatch refuses.
				limit := capBetweenPrices(t, admittedReading, wireReading)
				c, _, bodies := emittedPriceRoute(t, limit, tc.protocol, false)
				if _, ok := errors.AsType[*ContextBudgetError](entry.call(t, c, req)); !ok {
					t.Fatalf("dispatch admitted the request under a cap of %d, want the budget rejection "+
						"preflight reported: %d request(s) reached the wire", limit, len(bodies()))
				}
				if got := len(bodies()); got != 0 {
					t.Fatalf("requests on the wire = %d, want 0: a refused request must not dispatch", got)
				}

				// The price itself is the emitted list's, measured against the wire
				// rather than restated here.
				if got := EffectiveToolsJSONBytes(req, ampleRes, false); got != wire {
					t.Fatalf("preflight price = %d bytes, want the wire list's %d: the builder writes a schema "+
						"for a parameterless tool, and a price that omits it is smaller than the request that "+
						"dispatches", got, wire)
				}
				// And the price leaves the request alone: the stamp is still the
				// catalog's count.
				if req.ToolsJSONBytes != advertised {
					t.Fatalf("pricing moved the request's stamp to %d, want the catalog's %d",
						req.ToolsJSONBytes, advertised)
				}
			})
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

// substitutedParameterlessCatalog is a catalog whose tools declare no parameters
// and carry a description, so the wire list a route emits and the list
// ToolsJSONBytes measures describe the same tool objects. Every route must
// synthesize a schema for each of them.
func substitutedParameterlessCatalog(n int) []ToolDefinition {
	defs := parameterlessCatalog(n)
	for i := range defs {
		defs[i].Description = "probe tool " + string(rune('a'+i%26))
	}
	return defs
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
	projected := EstimateInputTokensForRoute(strictProbe, req, false).Tokens
	if projected <= advertised {
		t.Fatalf("fixture does not distinguish the two readings: advertised=%d projected=%d",
			advertised, projected)
	}
	// The control the fixture rests on: on a route without the capability the
	// same catalog is sent unchanged, so its price is the advertised one.
	_, relaxedProbe, _ := emittedPriceRoute(t, 1_000_000, registry.ProtocolOpenAIResponses, false)
	if got := EstimateInputTokensForRoute(relaxedProbe, req, false).Tokens; got != advertised {
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
	emitted := EstimateInputTokensForRoute(res, req, false).Tokens
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
