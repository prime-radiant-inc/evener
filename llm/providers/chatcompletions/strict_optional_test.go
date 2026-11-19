package chatcompletions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/openaichat"
	"primeradiant.com/evener/llm/registry"
)

func requiredNames(t *testing.T, schema map[string]any) []string {
	t.Helper()
	var out []string
	switch list := schema["required"].(type) {
	case []string:
		out = append(out, list...)
	case []any:
		for _, v := range list {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
	case nil:
		return nil
	default:
		t.Fatalf("required has unexpected type %T", schema["required"])
	}
	slices.Sort(out)
	return out
}

// Chat Completions carries the same strict-mode rule as Responses: every
// property of every object must be present and listed in required. A tool with
// a documented-optional argument therefore has no legal strict projection, and
// the adapter must not set strict on it.
func TestChatStrictToolsKeepOptionalArgumentsOmittable(t *testing.T) {
	t.Parallel()
	req := userReq("hi")
	req.Tools = []llm.ToolDefinition{{
		Name: "read_file",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"file_path":     map[string]any{"type": "string"},
				"offset":        map[string]any{"type": "integer"},
				"limit":         map[string]any{"type": "integer"},
				"vision_prompt": map[string]any{"type": "string"},
			},
			"required": []string{"file_path"},
		},
	}}

	body := build(t, req, resolved(func(c *registry.Caps) { c.StrictTools = new(true) }))
	fn := body["tools"].([]map[string]any)[0]["function"].(map[string]any)
	// Refused, so the route still states that this tool is not enforced
	// strictly. An absent key would leave the endpoint's default in charge of
	// the one property this PR exists to guarantee.
	if strict, has := fn["strict"]; !has || strict != false {
		t.Fatalf("strict = %#v (present %v), want an explicit false: a schema with optional arguments has no strict projection", strict, has)
	}
	params := fn["parameters"].(map[string]any)
	if got, want := requiredNames(t, params), []string{"file_path"}; !slices.Equal(got, want) {
		t.Fatalf("projected required = %v, want exactly %v (optional arguments stay omittable)", got, want)
	}
}

// A tool that opts out with Strict=&false stays non-strict on the chat path
// too, so the definition-level opt-outs the codebase already relies on are not
// silently re-enabled by this adapter.
//
// The schema here is fully required on purpose. A schema with an optional
// argument is already refused by the expressibility gate, so it would pass
// even if the adapter ignored the definition's opt-out. Only a schema that is
// strict-expressible isolates the opt-out: without it the adapter would set
// strict on this tool.
func TestChatStrictToolsHonorPerToolOptOut(t *testing.T) {
	t.Parallel()
	strictFalse := false
	req := userReq("hi")
	req.Tools = []llm.ToolDefinition{{
		Name:   "job_status",
		Strict: &strictFalse,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"target": map[string]any{"type": "string"},
			},
			"required": []string{"target"},
		},
	}}

	caps := func(c *registry.Caps) { c.StrictTools = new(true) }

	// The control: the same schema without the opt-out is strictified, which
	// is what makes the assertion below about the opt-out rather than the gate.
	control := userReq("hi")
	control.Tools = []llm.ToolDefinition{{Name: "job_status", Parameters: req.Tools[0].Parameters}}
	controlFn := build(t, control, resolved(caps))["tools"].([]map[string]any)[0]["function"].(map[string]any)
	if controlFn["strict"] != true {
		t.Fatalf("control strict = %#v, want true: a fully-required schema must be strictified", controlFn["strict"])
	}

	body := build(t, req, resolved(caps))
	fn := body["tools"].([]map[string]any)[0]["function"].(map[string]any)
	// The route offers strict enforcement and this tool opted out of it, so the
	// request says so rather than leaving enforcement to the endpoint's default.
	if strict, has := fn["strict"]; !has || strict != false {
		t.Fatalf("strict = %#v (present %v), want an explicit false for an opted-out tool on a strict-capable route", strict, has)
	}
	if params := fn["parameters"].(map[string]any); params["additionalProperties"] != false || params["required"] == nil {
		t.Fatalf("an opted-out tool must keep its advertised schema: %#v", params)
	}
}

// The other direction: a fully-required schema is strict-expressible, so the
// adapter still turns strict on and still rewrites the schema.
func TestChatStrictToolsStillStrictifyFullyRequiredSchemas(t *testing.T) {
	t.Parallel()
	req := userReq("hi")
	req.Tools = []llm.ToolDefinition{{
		Name: "write_file",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"file_path": map[string]any{"type": "string"},
				"content":   map[string]any{"type": "string"},
			},
			"required": []string{"file_path", "content"},
		},
	}}

	body := build(t, req, resolved(func(c *registry.Caps) { c.StrictTools = new(true) }))
	fn := body["tools"].([]map[string]any)[0]["function"].(map[string]any)
	if fn["strict"] != true {
		t.Fatalf("strict = %#v, want true for a fully-required schema", fn["strict"])
	}
	if params := fn["parameters"].(map[string]any); params["additionalProperties"] != false {
		t.Fatalf("additionalProperties = %#v, want false (strictified)", params["additionalProperties"])
	}

	t.Run("nested closed object union stays strict and accepts both alternatives", func(t *testing.T) {
		t.Parallel()
		unionSchema := map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"kind": map[string]any{"type": "string"},
			},
			"required": []string{"kind"},
			"anyOf": []any{
				map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"kind": map[string]any{"type": "string", "enum": []any{"a"}},
					},
					"required": []string{"kind"},
				},
				map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"kind": map[string]any{"type": "string", "enum": []any{"b"}},
					},
					"required": []string{"kind"},
				},
			},
		}
		unionReq := userReq("hi")
		unionReq.Tools = []llm.ToolDefinition{{Name: "choose_kind", Parameters: unionSchema}}
		unionBody := build(t, unionReq, resolved(func(c *registry.Caps) { c.StrictTools = new(true) }))
		unionFn := unionBody["tools"].([]map[string]any)[0]["function"].(map[string]any)
		if unionFn["strict"] != true {
			t.Fatalf("strict = %#v, want true for closed object union", unionFn["strict"])
		}
		encoded, err := json.Marshal(unionFn["parameters"])
		if err != nil {
			t.Fatalf("marshal emitted schema: %v", err)
		}
		compiler := jsonschema.NewCompiler()
		compiler.Draft = jsonschema.Draft2020
		const uri = "urn:evener:chat-union-schema"
		if err := compiler.AddResource(uri, bytes.NewReader(encoded)); err != nil {
			t.Fatalf("add resource: %v", err)
		}
		compiled, err := compiler.Compile(uri)
		if err != nil {
			t.Fatalf("compile emitted schema: %v", err)
		}
		for _, valid := range []map[string]any{{"kind": "a"}, {"kind": "b"}} {
			if err := compiled.Validate(valid); err != nil {
				t.Fatalf("emitted schema rejected %#v: %v", valid, err)
			}
		}
		for _, invalid := range []map[string]any{{"kind": "c"}, {"kind": "a", "extra": 1}} {
			if err := compiled.Validate(invalid); err == nil {
				t.Fatalf("emitted schema accepted invalid %#v", invalid)
			}
		}
	})
}

// On a route without strict enforcement a parameterless tool still has to carry
// a parameter schema, because the local validator compiles it against the open
// empty object. Omitting the key leaves the provider reading "no schema" where
// the validator reads "any object", so the wire and the validator would
// disagree about the same tool. The open form is the one the validator uses;
// the closed form belongs only to strict mode, which needs it to admit exactly
// the empty argument set.
func TestChatNonStrictToolsKeepParameterlessToolsOpen(t *testing.T) {
	t.Parallel()
	req := userReq("hi")
	// A prepared definition: this is the case the open schema exists for,
	// because prepareTools compiled the definition against it locally.
	req.Tools = []llm.ToolDefinition{llm.ToolDefinition{Name: "current_time"}.WithNoParametersDeclared()}

	body := build(t, req, resolved(func(c *registry.Caps) { c.StrictTools = new(false) }))
	fn := body["tools"].([]map[string]any)[0]["function"].(map[string]any)
	if _, has := fn["strict"]; has {
		t.Fatalf("strict = %#v, want no strict key on a route without strict tools", fn["strict"])
	}
	params, ok := fn["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("parameters = %#v, want the open empty object the validator compiles against", fn["parameters"])
	}
	if params["type"] != "object" {
		t.Fatalf("type = %#v, want object", params["type"])
	}
	if props, ok := params["properties"].(map[string]any); !ok || len(props) != 0 {
		t.Fatalf("properties = %#v, want an empty object", params["properties"])
	}
	if _, has := params["additionalProperties"]; has {
		t.Fatalf("additionalProperties = %#v, want it absent: an open object admits any key, which is what the validator allows", params["additionalProperties"])
	}
}

// A direct Complete or Stream call never went through preparation, so its nil
// Parameters is the caller's own declaration that the tool takes no arguments.
// The local validator that needs the open empty object is not in this path, and
// synthesizing one would tell the provider the tool accepts arbitrary keys — a
// change to the caller's request that nothing here needs to make. Omit the key,
// as every adapter did before the fallback existed.
func TestChatDirectCallKeepsParameterlessToolWithoutParameters(t *testing.T) {
	t.Parallel()
	req := userReq("hi")
	req.Tools = []llm.ToolDefinition{{Name: "current_time"}}
	body := build(t, req, resolved(func(c *registry.Caps) { c.StrictTools = new(false) }))
	fn := body["tools"].([]map[string]any)[0]["function"].(map[string]any)
	if params, has := fn["parameters"]; has {
		t.Fatalf("parameters = %#v, want the key omitted: a direct call's nil is the caller's no-argument declaration", params)
	}

	// A strict route is different, and not a counterexample: the closed empty
	// object admits exactly {}, which is the same argument set "no parameters"
	// names, so stating it changes nothing the caller declared. The open object
	// this test rejects would have admitted arbitrary keys instead.
	strictBody := build(t, req, resolved(func(c *registry.Caps) { c.StrictTools = new(true) }))
	strictFn := strictBody["tools"].([]map[string]any)[0]["function"].(map[string]any)
	params, ok := strictFn["parameters"].(map[string]any)
	if !ok || params["additionalProperties"] != false {
		t.Fatalf("parameters = %#v, want the closed empty object: it admits exactly the empty argument set", strictFn["parameters"])
	}
}

// The chat adapter carries the same parameterless-tool rule as Responses:
// preparation stores the open empty object and records the parameterless
// declaration separately, so on a strict route the adapter replaces it with the
// closed empty object and keeps the tool strict.
func TestChatStrictToolsKeepParameterlessToolsClosed(t *testing.T) {
	t.Parallel()
	req := userReq("hi")
	req.Tools = []llm.ToolDefinition{llm.ToolDefinition{Name: "current_time"}.WithNoParametersDeclared()}

	body := build(t, req, resolved(func(c *registry.Caps) { c.StrictTools = new(true) }))
	fn := body["tools"].([]map[string]any)[0]["function"].(map[string]any)
	if fn["strict"] != true {
		t.Fatalf("strict = %#v, want true: a parameterless tool has nothing to force", fn["strict"])
	}
	params, ok := fn["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("parameters = %#v, want a closed empty object: strict mode needs a schema to validate against", fn["parameters"])
	}
	if params["additionalProperties"] != false {
		t.Fatalf("additionalProperties = %#v, want false", params["additionalProperties"])
	}
	if props, ok := params["properties"].(map[string]any); !ok || len(props) != 0 {
		t.Fatalf("properties = %#v, want an empty object", params["properties"])
	}
	if got := requiredNames(t, params); len(got) != 0 {
		t.Fatalf("required = %v, want empty", got)
	}
}

// A tool schema that contains itself has no wire form: json.Marshal refuses it
// with a cycle error. Eligibility is decided over the caller's own tree before
// any copy exists, so following the loop here exhausts the stack and takes the
// process rather than the request. The builder must refuse the request instead,
// and it must do so for both shapes a Go caller can build — a map that holds
// itself and a slice whose element refers back.
func TestCyclicToolSchemaIsRefusedAtTheBuilder(t *testing.T) {
	t.Parallel()
	cyclicMap := func() map[string]any {
		root := map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           map[string]any{"child": nil},
			"required":             []string{"child"},
		}
		root["properties"].(map[string]any)["child"] = root
		return root
	}
	cyclicSlice := func() map[string]any {
		branches := []any{nil}
		root := map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]map[string]any{
				"child": {"anyOf": branches},
			},
			"required": []string{"child"},
		}
		branches[0] = root
		return root
	}
	for _, tc := range []struct {
		name   string
		params map[string]any
	}{
		{"map cycle", cyclicMap()},
		{"slice cycle", cyclicSlice()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := userReq("hi")
			req.Tools = []llm.ToolDefinition{{Name: "looping", Parameters: tc.params}}
			res := resolved(func(c *registry.Caps) { c.StrictTools = new(true) })

			_, err := (&Protocol{}).BuildBody(llm.ShapeRequest(req, res), res)
			if !errors.Is(err, openaichat.ErrCyclicSchema) {
				t.Fatalf("error = %v, want %v: a schema that contains itself has no wire form",
					err, openaichat.ErrCyclicSchema)
			}
			var cfgErr *llm.ConfigurationError
			if !errors.As(err, &cfgErr) {
				t.Fatalf("error type = %T, want *llm.ConfigurationError so retries do not treat a cyclic schema as transient", err)
			}
			if cfgErr.Retryable() || llm.Classify(err) != llm.ErrorClassPermanent {
				t.Fatalf("cyclic schema classified as retryable=%v class=%v, want permanent non-retryable", cfgErr.Retryable(), llm.Classify(err))
			}
		})
	}

	// Control: a shared acyclic subtree is legal Go and must still build, so the
	// refusal is about a loop rather than about reuse.
	shared := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           map[string]any{"label": map[string]any{"type": "string"}},
		"required":             []string{"label"},
	}
	req := userReq("hi")
	req.Tools = []llm.ToolDefinition{{
		Name: "shared",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           map[string]any{"a": shared, "b": shared},
			"required":             []string{"a", "b"},
		},
	}}
	res := resolved(func(c *registry.Caps) { c.StrictTools = new(true) })
	body, err := (&Protocol{}).BuildBody(llm.ShapeRequest(req, res), res)
	if err != nil {
		t.Fatalf("a shared acyclic subtree must build: %v", err)
	}
	if body["tools"] == nil {
		t.Fatal("the shared-subtree tool must still reach the wire")
	}
}

// The chat builder reads the same marker, so the same rule applies: a schema
// replaced after preparation is the caller's value and ships as written. On a
// strict route, discarding it for the closed empty object would send a schema
// nobody wrote and drop the replacement's properties from enforcement.
func TestChatReplacedSchemaIsNotDiscardedForTheNoParametersFallback(t *testing.T) {
	t.Parallel()
	replacement := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           map[string]any{"path": map[string]any{"type": "string"}},
		"required":             []string{"path"},
	}
	replaced := llm.ToolDefinition{Name: "read_file"}.WithNoParametersDeclared()
	replaced.Parameters = replacement

	for name, mutate := range map[string]func(c *registry.Caps){
		"strict route":     func(c *registry.Caps) { c.StrictTools = new(true) },
		"non-strict route": func(c *registry.Caps) { c.StrictTools = new(false) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			req := userReq("hi")
			req.Tools = []llm.ToolDefinition{replaced}
			fn := build(t, req, resolved(mutate))["tools"].([]map[string]any)[0]["function"].(map[string]any)
			params, ok := fn["parameters"].(map[string]any)
			if !ok {
				t.Fatalf("parameters = %#v, want the replacement schema", fn["parameters"])
			}
			if got := requiredNames(t, params); len(got) != 1 || got[0] != "path" {
				t.Fatalf("required = %v, want [path]: the replacement was discarded for a synthesized empty object", got)
			}
			if _, has := params["properties"].(map[string]any)["path"]; !has {
				t.Fatal("the replacement's property is absent: an empty object schema was sent in its place")
			}
		})
	}
}

// Generate's admission budget must agree with the schema the Chat Completions
// adapter emits for a parameterless tool: closed on a strict-capable route,
// open when strict is disabled or the tool opts out.
func TestChatGenerateParameterlessBudgetMatchesEmittedSchema(t *testing.T) {
	t.Parallel()
	strictOff := false
	cases := []struct {
		name        string
		strictTools *bool
		toolStrict  *bool
		wantClosed  bool
	}{
		{name: "strict built-in chat emits closed schema", strictTools: new(true), wantClosed: true},
		{name: "opted-out tool keeps open schema", strictTools: new(true), toolStrict: &strictOff, wantClosed: false},
		{name: "non-strict chat route keeps open schema", strictTools: new(false), wantClosed: false},
	}

	const chatOK = `{"id":"chatcmpl-1","model":"gpt-budget-probe","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(chatOK))
			}))
			defer srv.Close()

			probeDef := llm.ToolDefinition{Name: "current_time", Strict: tc.toolStrict}.WithNoParametersDeclared()
			probeReq := userReq("hello")
			probeReq.Tools = []llm.ToolDefinition{probeDef}
			probeBody := build(t, probeReq, resolved(func(c *registry.Caps) { c.StrictTools = tc.strictTools }))
			fn := probeBody["tools"].([]map[string]any)[0]["function"].(map[string]any)
			emittedParams := fn["parameters"].(map[string]any)
			_, hasClosed := emittedParams["additionalProperties"]
			if hasClosed != tc.wantClosed {
				t.Fatalf("emitted additionalProperties present = %v, want %v: %#v", hasClosed, tc.wantClosed, emittedParams)
			}

			const instance = "budget-chat"
			const model = "gpt-budget-probe"
			loadClient := func(maxInput *int) *llm.Client {
				r, err := registry.Load(
					registry.WithOffline(true),
					registry.WithoutCache(),
					registry.WithNoUserLayer(),
					registry.WithStateRoot(t.TempDir()),
					registry.WithEnv(func(string) (string, bool) { return "", false }),
					registry.WithInstances(map[string]registry.Provider{
						instance: {
							Base:      "openai",
							Protocol:  registry.ProtocolOpenAIChat,
							APIKey:    "test-key",
							Caps:      registry.Caps{StrictTools: tc.strictTools, MaxInputTokens: maxInput},
							Transport: registry.Transport{BaseURL: srv.URL},
						},
					}),
				)
				if err != nil {
					t.Fatalf("registry.Load: %v", err)
				}
				return llm.NewClient(llm.WithRegistry(r))
			}

			generous := 10_000_000
			res, err := loadClient(&generous).Resolve(instance + "/" + model)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			refDef := llm.ToolDefinition{Name: "current_time", Strict: tc.toolStrict, Parameters: emittedParams}
			refReq := llm.Request{
				Model:    model,
				Provider: instance,
				Messages: []llm.Message{llm.User("hello")},
				Tools:    []llm.ToolDefinition{refDef},
			}
			wirePrice := llm.EstimateInputTokensForResolved(res, refReq).Tokens
			tight := wirePrice + llm.SafetyReserveTokens(wirePrice) - 1
			if got := wirePrice + llm.SafetyReserveTokens(tight); got != tight+1 {
				t.Fatalf("reserve at %d makes wire price %d, want %d", tight, got, tight+1)
			}

			prompt := "hello"
			noRetry := llm.RetryPolicy{MaxRetries: 0}
			before := requests.Load()
			_, err = llm.Generate(context.Background(), llm.GenerateOptions{
				Client:      loadClient(&tight),
				Provider:    instance,
				Model:       model,
				Prompt:      &prompt,
				RetryPolicy: &noRetry,
				Tools:       []llm.Tool{{Definition: llm.ToolDefinition{Name: "current_time", Strict: tc.toolStrict}}},
			})
			var budgetErr *llm.ContextBudgetError
			if !errors.As(err, &budgetErr) || budgetErr.Limit != "max_input" {
				t.Fatalf("Generate was not refused at tight ceiling %d: err = %v, requests = %d", tight, err, requests.Load()-before)
			}
			if requests.Load() != before {
				t.Fatal("request reached HTTP server despite exceeding max_input under emitted schema")
			}

			exact := tight + 1
			before = requests.Load()
			if _, err := llm.Generate(context.Background(), llm.GenerateOptions{
				Client:      loadClient(&exact),
				Provider:    instance,
				Model:       model,
				Prompt:      &prompt,
				RetryPolicy: &noRetry,
				Tools:       []llm.Tool{{Definition: llm.ToolDefinition{Name: "current_time", Strict: tc.toolStrict}}},
			}); err != nil {
				t.Fatalf("Generate refused at exact emitted ceiling %d: %v", exact, err)
			}
			if requests.Load() != before+1 {
				t.Fatal("admitted request did not reach HTTP server")
			}

			before = requests.Load()
			directReq := llm.Request{
				Provider: instance,
				Model:    model,
				Messages: []llm.Message{llm.User(prompt)},
				Tools:    []llm.ToolDefinition{probeDef},
			}
			if _, err := loadClient(&tight).Complete(context.Background(), directReq); !errors.As(err, &budgetErr) || budgetErr.Limit != "max_input" {
				t.Fatalf("direct unstamped Complete was not refused at tight ceiling %d: err = %v, requests = %d", tight, err, requests.Load()-before)
			}
			if requests.Load() != before {
				t.Fatal("direct unstamped request reached HTTP server despite exceeding max_input under emitted schema")
			}
			counted, err := loadClient(&generous).CountInputTokens(context.Background(), directReq)
			if err != nil {
				t.Fatalf("CountInputTokens: %v", err)
			}
			if counted.Tokens != wirePrice {
				t.Fatalf("CountInputTokens = %d, want emitted schema price %d", counted.Tokens, wirePrice)
			}
		})
	}
}
