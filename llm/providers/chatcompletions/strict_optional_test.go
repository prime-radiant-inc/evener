package chatcompletions

import (
	"errors"
	"slices"
	"testing"

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

// The chat adapter carries the same parameterless-tool rule as Responses. A
// definition that declares no parameters keeps its nil through preparation, so
// the adapter has to supply the closed empty object itself: omitting
// "parameters" entirely would send strict mode a function with no schema, and
// advertising the open empty object would drop the tool to non-strict.
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
