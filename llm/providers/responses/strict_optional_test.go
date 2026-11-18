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
