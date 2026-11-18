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

// A parameterless tool that opts out of strict mode is advertised with the open
// empty object, which is what every non-strict route sends and what the local
// validator allows. Closing the fallback before reading the opt-out would
// advertise a schema stricter than the one the tool documents, so the opt-out
// has to be read first.
func TestStrictToolProjectionKeepsAnOptedOutParameterlessToolOpen(t *testing.T) {
	t.Parallel()
	optedOut := false
	req := userReq("hi")
	req.Tools = []llm.ToolDefinition{{Name: "current_time", Strict: &optedOut}}

	body := build(t, req, resolved(strictCaps))
	tool := body["tools"].([]map[string]any)[0]
	if tool["strict"] != false {
		t.Fatalf("strict = %#v, want false: the tool opted out", tool["strict"])
	}
	params, ok := tool["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("parameters = %#v, want an object schema", tool["parameters"])
	}
	if _, has := params["additionalProperties"]; has {
		t.Fatalf("additionalProperties = %#v, want absent: an opted-out tool keeps the open fallback", params["additionalProperties"])
	}
	if props, ok := params["properties"].(map[string]any); !ok || len(props) != 0 {
		t.Fatalf("properties = %#v, want an empty object", params["properties"])
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
		// Snapshot the advertised schema before the build. Serializing it after
		// would compare the emitted schema with whatever the builder left in
		// the map it was handed, so a builder that mutated a branch in place
		// would change both sides of the comparison and the test would pass.
		advertised := jsonOf(t, schema)
		req := userReq("hi")
		req.Tools = []llm.ToolDefinition{{Name: "job_status", Parameters: schema}}

		body := build(t, req, resolved(strictCaps))
		tool := body["tools"].([]map[string]any)[0]
		if tool["strict"] != false {
			t.Fatalf("depth %d: strict = %#v, want false: a property-less combinator branch is not certifiable", depth, tool["strict"])
		}
		// The schema on the wire must be the advertised one, byte for byte.
		if got, want := jsonOf(t, tool["parameters"]), advertised; got != want {
			t.Fatalf("depth %d: the emitted schema is not the advertised one\n got: %s\nwant: %s", depth, got, want)
		}
		sizes = append(sizes, len(jsonOf(t, tool["parameters"])))
		fixtureSizes = append(fixtureSizes, len(advertised))
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
		"identity-bound argument": {
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"payload": map[string]any{"minLength": 0},
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

// A refused tool keeps the schema it advertised, but the provider still
// requires an explicit type on every node. An argument declared
// {"const": "fixed"} next to an optional argument is exactly that shape: the
// refusal is right, because the projection would force the optional argument,
// and the typeless const node would still cost the whole request. The emitted
// schema must state the implied type, keep strict false, and keep the values
// and the required set the tool documents.
func TestRefusedToolStillStatesTheTypeAClosedValueSetImplies(t *testing.T) {
	t.Parallel()
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"payload": map[string]any{"const": "fixed"},
			"note":    map[string]any{"type": "string"},
		},
		"required": []string{"payload"},
	}
	req := userReq("hi")
	req.Tools = []llm.ToolDefinition{{Name: "job_status", Parameters: schema}}

	body := build(t, req, resolved(strictCaps))
	tool := body["tools"].([]map[string]any)[0]
	if tool["strict"] != false {
		t.Fatalf("strict = %#v, want false: an optional argument cannot be expressed in strict mode", tool["strict"])
	}
	params := tool["parameters"].(map[string]any)
	payload := params["properties"].(map[string]any)["payload"].(map[string]any)
	if payload["type"] != "string" {
		t.Fatalf("the emitted argument has type %#v, want \"string\": a provider enforcing the strict subset rejects a typeless node, and additional_tools on this route carries strict:false too", payload["type"])
	}
	if payload["const"] != "fixed" {
		t.Fatalf("const = %#v, want the advertised single value", payload["const"])
	}
	if got, want := requiredNames(t, params), []string{"payload"}; !slices.Equal(got, want) {
		t.Fatalf("required = %v, want the advertised set %v: a refusal keeps what the tool documents", got, want)
	}
	if _, has := params["additionalProperties"]; !has {
		t.Fatal("additionalProperties is missing: the advertised root was explicitly closed")
	}
}

// The explicit-type requirement belongs to the schema dialect, not to the
// strict-tools capability. A route that never offers strict_tools still sends
// this tool to the same endpoint, so a typeless const node costs the whole
// request there too. The type is stated either way, and the route's own flag
// behaviour is untouched: no strict key, no required change.
func TestTypeIsStatedOnARouteWithoutStrictTools(t *testing.T) {
	t.Parallel()
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"payload": map[string]any{"const": "fixed"},
			"note":    map[string]any{"type": "string"},
		},
		"required": []string{"payload"},
	}
	req := userReq("hi")
	req.Tools = []llm.ToolDefinition{{Name: "job_status", Parameters: schema}}

	// A route whose catalog row does not carry the capability: strict_tools is
	// absent, so the builder never enters the strict branch.
	body := build(t, req, resolved(nil))
	tool := body["tools"].([]map[string]any)[0]
	if _, has := tool["strict"]; has {
		t.Fatalf("strict = %#v on a route without the capability, want the field omitted", tool["strict"])
	}
	params := tool["parameters"].(map[string]any)
	payload := params["properties"].(map[string]any)["payload"].(map[string]any)
	if payload["type"] != "string" {
		t.Fatalf("the emitted argument has type %#v, want \"string\": the endpoint checks explicit types even when strict enforcement is off", payload["type"])
	}
	if payload["const"] != "fixed" {
		t.Fatalf("const = %#v, want the advertised single value", payload["const"])
	}
	if got, want := requiredNames(t, params), []string{"payload"}; !slices.Equal(got, want) {
		t.Fatalf("required = %v, want the advertised set %v: stating a type must not force the optional argument", got, want)
	}
}

// A Draft 7 tuple writes its positions as an array under items, and the
// fallback walk used to visit items only when its value was a single schema.
// The gate refuses such a schema — the tuple form is a representation the walk
// does not read — so the tool is sent with the schema it advertised, and the
// explicit-type supply is the only thing that can type the positions on a
// route that checks explicit types even with strict enforcement off.
//
// The assertions pin what a refusal must keep: the tuple order, each position's
// enum members, and the tool's non-strict setting.
func TestRefusedDraft7TupleStillTypesItsPositions(t *testing.T) {
	t.Parallel()
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"pair": map[string]any{
				"type": "array",
				"items": []any{
					map[string]any{"enum": []any{"left", "right"}},
					map[string]any{"enum": []any{"up", "down"}},
				},
			},
		},
		"required": []string{"pair"},
	}
	req := userReq("hi")
	req.Tools = []llm.ToolDefinition{{Name: "job_status", Parameters: schema}}

	body := build(t, req, resolved(strictCaps))
	tool := body["tools"].([]map[string]any)[0]
	if tool["strict"] != false {
		t.Fatalf("strict = %#v, want false: the tuple form is not a shape the projection can read", tool["strict"])
	}
	params := tool["parameters"].(map[string]any)
	pair := params["properties"].(map[string]any)["pair"].(map[string]any)
	positions, ok := pair["items"].([]any)
	if !ok {
		t.Fatalf("items = %#v, want the advertised tuple list: a refusal keeps the schema it advertised", pair["items"])
	}
	if len(positions) != 2 {
		t.Fatalf("tuple positions = %d, want 2: the advertised tuple length is part of the schema", len(positions))
	}
	for i, want := range [][]string{{"left", "right"}, {"up", "down"}} {
		pos := positions[i].(map[string]any)
		if pos["type"] != "string" {
			t.Fatalf("position %d has type %#v, want \"string\": a provider enforcing the strict subset rejects a typeless node, and this route checks explicit types with strict enforcement off too", i, pos["type"])
		}
		members, ok := pos["enum"].([]any)
		if !ok || len(members) != len(want) {
			t.Fatalf("position %d enum = %#v, want the advertised %v", i, pos["enum"], want)
		}
		for j, member := range want {
			if members[j] != member {
				t.Fatalf("position %d enum = %#v, want the advertised %v: stating a type must not reorder or drop members", i, members, want)
			}
		}
	}
	if got, want := requiredNames(t, params), []string{"pair"}; !slices.Equal(got, want) {
		t.Fatalf("required = %v, want the advertised set %v: stating a type must not force anything", got, want)
	}
}

// A route configured strict_tools = false must say so on every tool. The native
// endpoint normalizes an omitted flag into strict enforcement, so omitting it
// here would run the tool strictly anyway and force the optional argument the
// gate just refused to force — the #6200 harm reintroduced by configuration.
//
// The control is the same row with the capability unstated: a compatible
// endpoint that does not implement the field must still receive no flag.
func TestStrictToolsFalseRouteStatesTheFlagSoTheEndpointCannotNormalizeIt(t *testing.T) {
	t.Parallel()
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"action":    map[string]any{"type": "string"},
			"tab_index": map[string]any{"type": "integer"},
		},
		"required": []string{"action"},
	}
	req := userReq("hi")
	req.Tools = []llm.ToolDefinition{{Name: "use_browser", Parameters: schema}}

	// The row an operator turned strict mode off on: the capability is stated,
	// and it is false.
	off := build(t, req, resolved(func(c *registry.Caps) { c.StrictTools = new(false) }))
	tool := off["tools"].([]map[string]any)[0]
	strict, has := tool["strict"]
	if !has {
		t.Fatal("strict is absent on a strict_tools = false route: the native endpoint normalizes an omitted flag into strict enforcement, which forces tab_index on every call")
	}
	if strict != false {
		t.Fatalf("strict = %#v, want false", strict)
	}
	params := tool["parameters"].(map[string]any)
	if got, want := requiredNames(t, params), []string{"action"}; !slices.Equal(got, want) {
		t.Fatalf("required = %v, want the advertised set %v: turning strict mode off must not force the optional argument", got, want)
	}
	if _, has := params["additionalProperties"]; !has {
		t.Fatal("additionalProperties is missing: the advertised root was explicitly closed and must survive")
	}

	// Control: a row that states no capability is a compatible endpoint that
	// does not implement the field. It keeps receiving no flag.
	unstated := build(t, req, resolved(nil))
	if _, has := unstated["tools"].([]map[string]any)[0]["strict"]; has {
		t.Fatalf("strict = %#v on a route that states no capability, want the field omitted", unstated["tools"].([]map[string]any)[0]["strict"])
	}
}

// The same route with the capability stated and on still certifies a fully
// expressible schema, so stating the flag does not become a refusal to enforce.
func TestStrictToolsTrueRouteStillCertifiesFullyRequiredSchemas(t *testing.T) {
	t.Parallel()
	req := userReq("hi")
	req.Tools = []llm.ToolDefinition{{Name: "write_file", Parameters: map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           map[string]any{"file_path": map[string]any{"type": "string"}},
		"required":             []string{"file_path"},
	}}}
	body := build(t, req, resolved(strictCaps))
	tool := body["tools"].([]map[string]any)[0]
	if tool["strict"] != true {
		t.Fatalf("strict = %#v, want true for a fully-required schema on a strict-capable row", tool["strict"])
	}
	if params := tool["parameters"].(map[string]any); params["additionalProperties"] != false {
		t.Fatalf("additionalProperties = %#v, want false (strictified)", params["additionalProperties"])
	}
}
