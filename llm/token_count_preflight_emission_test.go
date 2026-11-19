package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

// declaredEmissionServer answers one message per protocol and records every
// request body, so a test can read the tool list that actually dispatched.
func declaredEmissionServer(t *testing.T) (*httptest.Server, func() []map[string]any) {
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

// declaredEmissionRoute builds a client dispatching protocol to the test server
// under maxInput, with no registered override, so the built-in builder serves
// the request.
func declaredEmissionRoute(t *testing.T, maxInput int, protocol string) (*Client, registry.Resolved, func() []map[string]any) {
	t.Helper()
	srv, bodies := declaredEmissionServer(t)
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(map[string]registry.Provider{
			"emitted-price": {
				Base:      protocol,
				Protocol:  protocol,
				Surface:   registry.SurfaceGeneric,
				APIKey:    "unused",
				Transport: registry.Transport{BaseURL: srv.URL},
				Caps: registry.Caps{
					ContextWindow:   new(1_000_000),
					MaxInputTokens:  new(maxInput),
					MaxOutputTokens: new(4_096),
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
	if res.Protocol != protocol {
		t.Fatalf("resolved protocol = %q, want %q: the fixture must dispatch the route under test", res.Protocol, protocol)
	}
	return c, res, bodies
}

// declaredEmissionCatalog is a catalog whose tools declare no parameters and
// carry a description, so the wire list a route emits and the list
// ToolsJSONBytes measures describe the same tool objects.
func declaredEmissionCatalog(n int) []ToolDefinition {
	defs := parameterlessCatalog(n)
	for i := range defs {
		defs[i].Description = "probe tool " + string(rune('a'+i%26))
	}
	return defs
}

// wireToolDefs reads the tool list a route put on the wire, as the definitions
// ToolsJSONBytes measures: the name, the description, and the parameter schema
// the builder emitted. The wire is the oracle for the emitted list; a price
// written here would be a second opinion about the same question.
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

// A route whose protocol has no schema deriver still substitutes a schema for a
// parameterless tool, so a price taken before dispatch has to include it.
//
// The Anthropic and Google builders write an open empty object where the caller
// declared none. Admission reaches those bytes through the parameterless
// adjustment, while the declared count omits them: an input cap between the two
// readings admits the request in preflight and then refuses the dispatch, which
// is the disagreement this pins. Both entry points are covered because both
// admit through the same budget.
func TestAParameterlessPreflightPricesARouteWithoutADeriver(t *testing.T) {
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
				defs := declaredEmissionCatalog(8)
				req := Request{
					Provider:       "emitted-price",
					Model:          "model",
					Messages:       []Message{User("x")},
					Tools:          defs,
					ToolsJSONBytes: ToolsJSONBytes(defs),
				}

				// The emitted list, read from the wire, is what the request costs.
				ample, ampleRes, ampleBodies := declaredEmissionRoute(t, 1_000_000, tc.protocol)
				if _, err := ample.Complete(context.Background(), req); err != nil {
					t.Fatalf("Complete under an ample cap: %v", err)
				}
				dispatched := ampleBodies()
				if len(dispatched) != 1 {
					t.Fatalf("requests on the wire = %d, want 1", len(dispatched))
				}
				wire := ToolsJSONBytes(wireToolDefs(t, dispatched[0], tc.schemaKey))
				admittedReading := EstimateInputTokensForResolved(ampleRes, req).Tokens
				wireReq := req
				wireReq.ToolsJSONBytes = wire
				wireReading := EstimateInputTokensForResolved(ampleRes, wireReq).Tokens
				if wireReading <= admittedReading {
					t.Fatalf("fixture does not distinguish the two readings: admitted=%d wire=%d",
						admittedReading, wireReading)
				}

				// A cap between the declared reading and the emitted one. Preflight
				// has to refuse the request the dispatch refuses.
				limit := capBetweenPrices(t, admittedReading, wireReading)
				c, res, bodies := declaredEmissionRoute(t, limit, tc.protocol)
				if _, ok := errors.AsType[*ContextBudgetError](entry.call(t, c, req)); !ok {
					t.Fatalf("dispatch admitted the request under a cap of %d, want the budget rejection "+
						"preflight reported: %d request(s) reached the wire", limit, len(bodies()))
				}
				if got := len(bodies()); got != 0 {
					t.Fatalf("requests on the wire = %d, want 0: a refused request must not dispatch", got)
				}
				preflight := EstimateInputTokensForResolved(res, PriceToolSchemas(req, res)).Tokens
				if preflight != wireReading {
					t.Fatalf("preflight reading = %d input tokens, want the emitted list's %d: a caller that "+
						"sizes the request from this number fits it to one the dispatch then refuses",
						preflight, wireReading)
				}

				// And the price itself is the emitted list's, measured against the
				// wire rather than restated here.
				if got := PriceToolSchemas(req, ampleRes).ToolsJSONBytes; got != wire {
					t.Fatalf("preflight price = %d bytes, want the wire list's %d: the builder writes a schema "+
						"for a parameterless tool, and a price that omits it is smaller than the request that "+
						"dispatches", got, wire)
				}
			})
		}
	}
}

// An unchanged emission is still a price, so it is recorded against the catalog
// and the target.
//
// A route whose builder changes nothing emits the list it was handed, and the
// count that describes that list is the price. Leaving it unrecorded makes every
// later price of the same request derive the emission and walk the catalog
// again, which is the cost the count exists to avoid. The record also keeps the
// retarget rule intact, because the baseline is this list's own count.
func TestAnUnchangedEmissionIsRecorded(t *testing.T) {
	t.Parallel()
	res := responsesRowOn(true)
	defs := []ToolDefinition{{
		Name: "write_file",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"path":    map[string]any{"type": "string"},
				"content": map[string]any{"type": "string"},
			},
			// Already certifiable; the projection only sorts this list, so the
			// emitted bytes are the declared ones.
			"required": []string{"path", "content"},
		},
	}}
	advertised := ToolsJSONBytes(defs)
	req := Request{Tools: defs, ToolsJSONBytes: advertised}

	priced := PriceToolSchemas(req, res)
	if priced.ToolsJSONBytes != advertised {
		t.Fatalf("fixture does not leave the count alone: priced=%d advertised=%d",
			priced.ToolsJSONBytes, advertised)
	}
	if !priced.emittedToolPriceApplies(res) {
		t.Fatalf("no price recorded for a catalog this route emits at its declared %d bytes: every later "+
			"price of the same request derives the emission and walks the catalog again", advertised)
	}
}

// unchangedEmissionProtocol is a deriver stand-in whose builder changes nothing,
// counting the derivations a caller performs. Every registered protocol derives,
// so a route that reports an unchanged list this way is registered by the test
// rather than resolved from a real instance.
type unchangedEmissionProtocol struct{ stubProtocol }

// unchangedEmissionDerivations counts the derivations the stand-in performed.
// It is package-level so a test binary run more than once in one process keeps
// counting the same stand-in.
var unchangedEmissionDerivations int32

func (unchangedEmissionProtocol) EmitToolSchemas([]ToolDefinition, registry.Resolved) ([]ToolDefinition, bool) {
	atomic.AddInt32(&unchangedEmissionDerivations, 1)
	return nil, false
}

// unchangedDeriverRegistration keeps the package-wide protocol registry from
// panicking when the binary is run more than once in one process.
var unchangedDeriverRegistration sync.Once

// unchangedEmissionProtocolID names the registered stand-in.
const unchangedEmissionProtocolID = "test-proto-unchanged-emission"

// A second price for the same target derives nothing.
//
// The first price records the unchanged emission, so the second one answers from
// that record. Without the record it re-derives the emission and walks the
// catalog again, for a request whose answer is already known.
func TestASecondPriceForTheSameTargetDerivesNothing(t *testing.T) {
	// Not parallel: RegisterProtocol adds to llm's package-wide protocols
	// registry (no unregister, so entries persist for the process).
	atomic.StoreInt32(&unchangedEmissionDerivations, 0)
	unchangedDeriverRegistration.Do(func() {
		RegisterProtocol(unchangedEmissionProtocol{stubProtocol{id: unchangedEmissionProtocolID}})
	})
	res := registry.Resolved{Instance: "emitted-price", ModelID: "model", WireID: "model"}
	res.Protocol = unchangedEmissionProtocolID

	defs := parameterlessCatalog(2)
	req := Request{Tools: defs, ToolsJSONBytes: ToolsJSONBytes(defs)}
	once := PriceToolSchemas(req, res)
	twice := PriceToolSchemas(once, res)

	if got := atomic.LoadInt32(&unchangedEmissionDerivations); got != 1 {
		t.Fatalf("derived the emission %d time(s) for two prices of the same request, want 1: the second "+
			"price repeats the schema copy and walk the first one's record exists to avoid", got)
	}
	if twice.ToolsJSONBytes != once.ToolsJSONBytes {
		t.Fatalf("second price = %d bytes, first = %d: the two describe the same unchanged list",
			twice.ToolsJSONBytes, once.ToolsJSONBytes)
	}
}
