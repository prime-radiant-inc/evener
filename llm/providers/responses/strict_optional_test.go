package responses

import (
	"slices"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// strictCaps turns on the provider's strict-tools capability. The route is the
// native OpenAI Responses one, where StrictTools is set in the catalog.
func strictCaps(c *registry.Caps) { c.StrictTools = new(true) }

// requiredNames reads a projected schema's required list in any of the shapes
// the builders emit, sorted so the comparison is about membership rather than
// declaration order.
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

// Two shapes the rewrite cannot reach escaped the gate: a nullable object
// spelled as a type list, and an object behind a $ref into local $defs. Both
// would have been sent strict with their optional property still optional,
// which OpenAI answers by rejecting the whole request ("Invalid schema for
// function ...: 'required' ... Missing 'limit'") rather than just the tool.
// The projection must send both non-strict with the advertised required sets
// intact.
func TestStrictToolProjectionRejectsShapesTheRewriteCannotReach(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		params map[string]any
		want   []string
	}{
		{
			name: "nullable object type list",
			params: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"event_filter": map[string]any{
						"type":                 []string{"object", "null"},
						"additionalProperties": false,
						"properties": map[string]any{
							"tool_name": map[string]any{"type": "string"},
							"status":    map[string]any{"type": "string"},
						},
						"required": []string{"tool_name"},
					},
				},
				"required": []string{"event_filter"},
			},
			want: []string{"event_filter"},
		},
		{
			name: "object behind a ref into local defs",
			params: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"$defs": map[string]any{
					"target": map[string]any{
						"type":                 "object",
						"additionalProperties": false,
						"properties": map[string]any{
							"job":   map[string]any{"type": "string"},
							"limit": map[string]any{"type": "integer"},
						},
						"required": []string{"job"},
					},
				},
				"properties": map[string]any{
					"target": map[string]any{"$ref": "#/$defs/target"},
				},
				"required": []string{"target"},
			},
			want: []string{"target"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := userReq("hi")
			req.Tools = []llm.ToolDefinition{{Name: "job_watch", Parameters: tc.params}}
			body := build(t, req, resolved(strictCaps))
			tool := body["tools"].([]map[string]any)[0]
			if tool["strict"] != false {
				t.Fatalf("strict = %#v, want false: the rewrite cannot reach this shape, so certifying it would make OpenAI reject the whole request", tool["strict"])
			}
			if got := requiredNames(t, tool["parameters"].(map[string]any)); !slices.Equal(got, tc.want) {
				t.Fatalf("projected required = %v, want exactly %v (optional arguments stay omittable)", got, tc.want)
			}
		})
	}
}

// A tool whose documented arguments include an optional one has no legal
// OpenAI strict projection: strict mode requires every property to be present
// and listed in required, so projecting it would force every call to invent a
// value for an argument the documentation says may be omitted. The
// projection must keep the declared required set and send the tool non-strict.
func TestStrictToolProjectionKeepsOptionalArgumentsOmittable(t *testing.T) {
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

	body := build(t, req, resolved(strictCaps))
	tool := body["tools"].([]map[string]any)[0]
	if tool["strict"] != false {
		t.Fatalf("strict = %#v, want false: a schema with optional arguments cannot be expressed in strict mode", tool["strict"])
	}
	params := tool["parameters"].(map[string]any)
	if got, want := requiredNames(t, params), []string{"file_path"}; !slices.Equal(got, want) {
		t.Fatalf("projected required = %v, want exactly %v (optional arguments stay omittable)", got, want)
	}
}

// A nested object with an optional property is the same illegality one level
// down: strict mode requires every property of every object, not only the root.
//
// The root requires update on purpose. Leaving it optional would let the root
// alone make the schema inexpressible, so the adapter could pass this test
// without ever inspecting the array items, and the nested case would stop being
// load-bearing.
func TestStrictToolProjectionRejectsNestedOptionalProperties(t *testing.T) {
	t.Parallel()
	req := userReq("hi")
	req.Tools = []llm.ToolDefinition{{
		Name: "task_list",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"update": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type":                 "object",
						"additionalProperties": false,
						"properties": map[string]any{
							"id":     map[string]any{"type": "integer"},
							"status": map[string]any{"type": "string"},
						},
						"required": []string{"id"},
					},
				},
			},
			"required": []string{"update"},
		},
	}}

	body := build(t, req, resolved(strictCaps))
	tool := body["tools"].([]map[string]any)[0]
	if tool["strict"] != false {
		t.Fatalf("strict = %#v, want false: a nested optional property is also inexpressible", tool["strict"])
	}
}

// The other direction: a schema whose every object lists every property is
// strict-expressible, so the projection still turns strict mode on and still
// rewrites the schema.
func TestStrictToolProjectionStillStrictifiesFullyRequiredSchemas(t *testing.T) {
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

	body := build(t, req, resolved(strictCaps))
	tool := body["tools"].([]map[string]any)[0]
	if tool["strict"] != true {
		t.Fatalf("strict = %#v, want true for a fully-required schema", tool["strict"])
	}
	params := tool["parameters"].(map[string]any)
	if params["additionalProperties"] != false {
		t.Fatalf("additionalProperties = %#v, want false (strictified)", params["additionalProperties"])
	}
	if got, want := requiredNames(t, params), []string{"content", "file_path"}; !slices.Equal(got, want) {
		t.Fatalf("projected required = %v, want %v", got, want)
	}
}

// A tool declaring no parameters at all is the empty object, which strict mode
// accepts once it is explicitly closed. Synthesizing the open
// {"type":"object","properties":{}} instead leaves the root uncertifiable, so a
// parameterless tool would drop to non-strict and go out accepting any keys —
// looser than the closed empty object it advertised. It must stay strict, and
// the schema on the wire must be that closed empty object.
func TestStrictToolProjectionKeepsParameterlessToolsClosed(t *testing.T) {
	t.Parallel()
	req := userReq("hi")
	req.Tools = []llm.ToolDefinition{{Name: "current_time"}}

	body := build(t, req, resolved(strictCaps))
	tool := body["tools"].([]map[string]any)[0]
	if tool["strict"] != true {
		t.Fatalf("strict = %#v, want true: a parameterless tool has nothing to force", tool["strict"])
	}
	params, ok := tool["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("parameters = %#v, want an object schema", tool["parameters"])
	}
	if params["type"] != "object" {
		t.Fatalf("type = %#v, want object", params["type"])
	}
	if params["additionalProperties"] != false {
		t.Fatalf("additionalProperties = %#v, want false: an open root is not a strict-expressible schema", params["additionalProperties"])
	}
	if props, ok := params["properties"].(map[string]any); !ok || len(props) != 0 {
		t.Fatalf("properties = %#v, want an empty object", params["properties"])
	}
	if got := requiredNames(t, params); len(got) != 0 {
		t.Fatalf("required = %v, want empty", got)
	}
}

// A schema the gate refuses must reach the wire as advertised, so its size
// cannot grow with nesting no matter what an adapter does while building. The
// measurement has to be of the emitted parameters, not of the input fixture:
// serializing the fixture would still pass if the builder expanded the schema.
func TestRefusedNestingSchemaIsEmittedUnchanged(t *testing.T) {
	t.Parallel()
	nesting := func(depth int) map[string]any {
		var inner map[string]any
		for range depth {
			node := map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"a": map[string]any{"type": "string"},
					"b": map[string]any{"type": "string"},
				},
				"required": []string{"a", "b"},
				"anyOf": []any{
					map[string]any{"type": "object", "required": []string{"a"}},
					map[string]any{"type": "object", "required": []string{"b"}},
				},
			}
			if inner != nil {
				node["properties"].(map[string]any)["inner"] = inner
				node["required"] = []string{"a", "b", "inner"}
			}
			inner = node
		}
		return inner
	}

	sizes := make([]int, 0, 6)
	fixtureSizes := make([]int, 0, 6)
	for _, depth := range []int{1, 2, 4, 8, 16, 32} {
		schema := nesting(depth)
		req := userReq("hi")
		req.Tools = []llm.ToolDefinition{{Name: "job_status", Parameters: schema}}

		body := build(t, req, resolved(strictCaps))
		tool := body["tools"].([]map[string]any)[0]
		if tool["strict"] != false {
			t.Fatalf("depth %d: strict = %#v, want false: a property-less combinator branch is not certifiable", depth, tool["strict"])
		}
		// The schema on the wire must be the advertised one, byte for byte.
		if got, want := jsonOf(t, tool["parameters"]), jsonOf(t, schema); got != want {
			t.Fatalf("depth %d: the emitted schema is not the advertised one\n got: %s\nwant: %s", depth, got, want)
		}
		sizes = append(sizes, len(jsonOf(t, tool["parameters"])))
		fixtureSizes = append(fixtureSizes, len(jsonOf(t, schema)))
	}
	// The emitted size tracks the advertised size exactly, so doubling the
	// depth may at most double what goes on the wire.
	for i := 1; i < len(sizes); i++ {
		if sizes[i] > 2*sizes[i-1]+64 {
			t.Fatalf("emitted schema size %d at step %d exceeds twice the previous %d: growth is not linear", sizes[i], i, sizes[i-1])
		}
		if sizes[i] != fixtureSizes[i] {
			t.Fatalf("step %d: emitted %d bytes, advertised %d: the builder changed the schema", i, sizes[i], fixtureSizes[i])
		}
	}
}

// A required name the object never declares makes the schema illegal under
// strict mode, and the rewrite cannot repair it: it rewrites required only
// where it transforms the node, so a closed nullable object would carry the
// stale name to the provider and be rejected. The tool is sent non-strict with
// its advertised schema, and the stale name is not silently deleted — that
// would change what the caller asked for.
func TestStrictToolProjectionSendsMismatchedRequiredUnchanged(t *testing.T) {
	t.Parallel()
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"target": map[string]any{
				"type":                 []string{"object", "null"},
				"additionalProperties": false,
				"properties": map[string]any{
					"id": map[string]any{"type": "string"},
				},
				"required": []string{"id", "scope"},
			},
		},
		"required": []string{"target"},
	}
	req := userReq("hi")
	req.Tools = []llm.ToolDefinition{{Name: "job_status", Parameters: schema}}

	body := build(t, req, resolved(strictCaps))
	tool := body["tools"].([]map[string]any)[0]
	if tool["strict"] != false {
		t.Fatalf("strict = %#v, want false: a required name the object never declares is not expressible in strict mode", tool["strict"])
	}
	if got, want := jsonOf(t, tool["parameters"]), jsonOf(t, schema); got != want {
		t.Fatalf("the emitted schema must be the advertised one, with the stale required name intact\n got: %s\nwant: %s", got, want)
	}
}

// A required argument declared with an empty schema admits every JSON value,
// and an array declared without items admits every element. Neither can be
// verified against strict mode, so a tool carrying one is sent non-strict with
// its advertised schema rather than certified and sent to a provider that
// enforces the strict subset.
func TestStrictToolProjectionRefusesUnconstrainedArguments(t *testing.T) {
	t.Parallel()
	schemas := map[string]map[string]any{
		"unconstrained argument": {
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"payload": map[string]any{},
			},
			"required": []string{"payload"},
		},
		"array without items": {
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"tags": map[string]any{"type": "array"},
			},
			"required": []string{"tags"},
		},
		"annotation-only argument": {
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"payload": map[string]any{"description": "Free-form payload"},
			},
			"required": []string{"payload"},
		},
	}
	for name, schema := range schemas {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			req := userReq("hi")
			req.Tools = []llm.ToolDefinition{{Name: "job_status", Parameters: schema}}

			body := build(t, req, resolved(strictCaps))
			tool := body["tools"].([]map[string]any)[0]
			if tool["strict"] != false {
				t.Fatalf("strict = %#v, want false: this schema constrains too little to be verified", tool["strict"])
			}
			if got, want := jsonOf(t, tool["parameters"]), jsonOf(t, schema); got != want {
				t.Fatalf("the emitted schema must be the advertised one\n got: %s\nwant: %s", got, want)
			}
		})
	}
}
