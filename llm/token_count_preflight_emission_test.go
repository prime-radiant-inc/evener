package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
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

// declaredEmissionRoute builds a client dispatching protocol to the test server
// under maxInput, with no registered override, so the built-in builder serves
// the request.
func declaredEmissionRoute(t *testing.T, maxInput int, protocol string) (*Client, registry.Resolved, func() []map[string]any) {
	return declaredEmissionRouteWithBody(t, maxInput, protocol, nil)
}

// declaredEmissionRouteWithBody is declaredEmissionRoute with the row's
// transport body constants set, so a test can put a tools value on the wire
// that the builder does not write. Every protocol answers here, and the
// count-tokens endpoint is pinned unsupported: the reading these cases
// compare is the local estimate, which is the fallback a caller sizes a
// request with on a route that has no counter.
func declaredEmissionRouteWithBody(t *testing.T, maxInput int, protocol string, body map[string]any) (*Client, registry.Resolved, func() []map[string]any) {
	t.Helper()
	srv, bodies := declaredEmissionServer(t)
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

// A route that substitutes a schema for a parameterless tool has to price that
// substitution before dispatch.
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
				defs := declaredEmissionCatalog(8)
				advertised := ToolsJSONBytes(defs)
				req := Request{
					Provider:       "emitted-price",
					Model:          "model",
					Messages:       []Message{User("x")},
					Tools:          defs,
					ToolsJSONBytes: advertised,
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
				wireReading := EstimateInputTokensForRoute(ampleRes, req, false).Tokens
				if wireReading <= admittedReading {
					t.Fatalf("fixture does not distinguish the two readings: catalog=%d wire=%d",
						admittedReading, wireReading)
				}

				// A cap between the catalog's reading and the emitted one. Preflight
				// has to refuse the request the dispatch refuses.
				limit := capBetweenPrices(t, admittedReading, wireReading)
				c, _, bodies := declaredEmissionRoute(t, limit, tc.protocol)
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
