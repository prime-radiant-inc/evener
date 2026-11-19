package responses

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"

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

// A request middleware may replace a tool's parameters after preparation. The
// no-parameters marker describes the value preparation put there, so once the
// value is replaced the marker no longer describes anything: honoring it would
// discard the replacement and send an empty object schema the caller never
// wrote. The replacement goes through ordinary emission instead, while an
// untouched definition still gets the synthesized fallback.
func TestAReplacedSchemaIsNotDiscardedForTheNoParametersFallback(t *testing.T) {
	t.Parallel()
	replacement := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           map[string]any{"path": map[string]any{"type": "string"}},
		"required":             []string{"path"},
	}
	replaced := llm.ToolDefinition{Name: "read_file"}.WithNoParametersDeclared()
	replaced.Parameters = replacement

	// Non-strict route: the replacement is what ships, not an empty object.
	req := userReq("hi")
	req.Tools = []llm.ToolDefinition{replaced}
	body := build(t, req, resolved(nil))
	tool := body["tools"].([]map[string]any)[0]
	params, ok := tool["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("parameters = %#v, want the replacement schema", tool["parameters"])
	}
	if got := requiredNames(t, params); len(got) != 1 || got[0] != "path" {
		t.Fatalf("required = %v, want [path]: the replacement was discarded for the synthesized empty object", got)
	}
	if _, has := params["properties"].(map[string]any)["path"]; !has {
		t.Fatal("the replacement's property is absent: an empty object schema was sent in its place")
	}

	// The marker still applies to an untouched prepared definition, which is the
	// fallback the opposite case has to keep working.
	untouchedReq := userReq("hi")
	untouchedReq.Tools = []llm.ToolDefinition{llm.ToolDefinition{Name: "current_time"}.WithNoParametersDeclared()}
	untouched := build(t, untouchedReq, resolved(nil))
	untouchedParams, ok := untouched["tools"].([]map[string]any)[0]["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("parameters = %#v, want the open empty object", untouched["tools"].([]map[string]any)[0]["parameters"])
	}
	if props, ok := untouchedParams["properties"].(map[string]any); !ok || len(props) != 0 {
		t.Fatalf("properties = %#v, want an empty object", untouchedParams["properties"])
	}
}

func compileWireSchema(t *testing.T, schema map[string]any) *jsonschema.Schema {
	t.Helper()
	encoded, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.Draft = jsonschema.Draft2020
	const uri = "urn:evener:codex-wire-schema"
	if err := compiler.AddResource(uri, bytes.NewReader(encoded)); err != nil {
		t.Fatalf("add resource: %v", err)
	}
	compiled, err := compiler.Compile(uri)
	if err != nil {
		t.Fatalf("compile schema %s: %v", encoded, err)
	}
	return compiled
}

// Codex Responses Lite checks that every schema node declares an explicit type
// in both strict and non-strict tools. When every branch of a typeless union
// guarantees the same scalar type, the enclosing argument node must state that
// type too while accepting every value from every original branch.
func TestCodexResponsesLiteUnionEmitsEnclosingScalarType(t *testing.T) {
	t.Parallel()
	codexCaps := func(c *registry.Caps) {
		c.StrictTools = new(true)
		c.ResponsesLite = new(true)
	}
	cases := []struct {
		name       string
		params     map[string]any
		wantStrict bool
		accepts    []map[string]any
		rejects    []map[string]any
	}{
		{
			name: "strict anyOf of string enums",
			params: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"mode": map[string]any{
						"anyOf": []any{
							map[string]any{"enum": []any{"fast", "safe"}},
							map[string]any{"enum": []any{"careful"}},
						},
					},
				},
				"required": []string{"mode"},
			},
			wantStrict: true,
			accepts: []map[string]any{
				{"mode": "fast"},
				{"mode": "safe"},
				{"mode": "careful"},
			},
			rejects: []map[string]any{
				{"mode": "reckless"},
				{"mode": 42},
			},
		},
		{
			name: "non-strict anyOf alongside optional property",
			params: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"mode": map[string]any{
						"anyOf": []any{
							map[string]any{"enum": []any{"fast", "safe"}},
							map[string]any{"const": "careful"},
						},
					},
					"note": map[string]any{"type": "string"},
				},
				"required": []string{"mode"},
			},
			wantStrict: false,
			accepts: []map[string]any{
				{"mode": "fast"},
				{"mode": "safe"},
				{"mode": "careful"},
				{"mode": "careful", "note": "ok"},
			},
			rejects: []map[string]any{
				{"mode": "reckless"},
				{"mode": true},
			},
		},
		{
			name: "non-strict oneOf of string branches",
			params: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"mode": map[string]any{
						"oneOf": []any{
							map[string]any{"enum": []any{"fast", "safe"}},
							map[string]any{"const": "careful"},
						},
					},
				},
				"required": []string{"mode"},
			},
			wantStrict: false,
			accepts: []map[string]any{
				{"mode": "fast"},
				{"mode": "safe"},
				{"mode": "careful"},
			},
			rejects: []map[string]any{
				{"mode": "reckless"},
				{"mode": 7},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := userReq("hi")
			req.Tools = []llm.ToolDefinition{{Name: "set_mode", Parameters: tc.params}}
			body := build(t, req, resolved(codexCaps))
			input, ok := body["input"].([]any)
			if !ok || len(input) == 0 {
				t.Fatalf("codex lite input missing: %#v", body["input"])
			}
			additional, ok := input[0].(map[string]any)
			if !ok || additional["type"] != "additional_tools" {
				t.Fatalf("input[0] = %#v, want additional_tools item", input[0])
			}
			wireTools := additional["tools"].([]any)
			tool := wireTools[0].(map[string]any)
			if tool["strict"] != tc.wantStrict {
				t.Fatalf("strict = %#v, want %v", tool["strict"], tc.wantStrict)
			}
			params := tool["parameters"].(map[string]any)
			mode := params["properties"].(map[string]any)["mode"].(map[string]any)
			if mode["type"] != "string" {
				t.Fatalf("enclosing mode.type = %#v, want \"string\": Codex Responses Lite requires an explicit type on the enclosing union node", mode["type"])
			}
			compiled := compileWireSchema(t, params)
			for _, valid := range tc.accepts {
				if err := compiled.Validate(valid); err != nil {
					t.Fatalf("codex wire schema rejected %#v: %v", valid, err)
				}
			}
			for _, invalid := range tc.rejects {
				if err := compiled.Validate(invalid); err == nil {
					t.Fatalf("codex wire schema accepted invalid input %#v", invalid)
				}
			}
		})
	}
}

// Generate's admission budget must agree with the schema the Responses adapter
// actually emits for a parameterless tool: closed on a strict-capable route,
// open when strict is disabled or the tool opts out.
func TestGenerateParameterlessBudgetMatchesEmittedSchema(t *testing.T) {
	t.Parallel()
	strictOff := false
	cases := []struct {
		name        string
		strictTools *bool
		toolStrict  *bool
		wantClosed  bool
	}{
		{name: "strict built-in responses emits closed schema", strictTools: new(true), wantClosed: true},
		{name: "opted-out tool keeps open schema", strictTools: new(true), toolStrict: &strictOff, wantClosed: false},
		{name: "non-strict responses route keeps open schema", strictTools: new(false), wantClosed: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var hitServer bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hitServer = true
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(responseJSON))
			}))
			defer srv.Close()

			// Probe the adapter's actual emitted schema as the reference.
			probeDef := llm.ToolDefinition{Name: "current_time", Strict: tc.toolStrict}.WithNoParametersDeclared()
			probeReq := userReq("hello")
			probeReq.Tools = []llm.ToolDefinition{probeDef}
			probeBody := build(t, probeReq, resolved(func(c *registry.Caps) { c.StrictTools = tc.strictTools }))
			emittedParams := probeBody["tools"].([]map[string]any)[0]["parameters"].(map[string]any)
			_, hasClosed := emittedParams["additionalProperties"]
			if hasClosed != tc.wantClosed {
				t.Fatalf("emitted additionalProperties present = %v, want %v: %#v", hasClosed, tc.wantClosed, emittedParams)
			}

			const instance = "budget-responses"
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
							APIKey:    "test-key",
							Caps:      registry.Caps{StrictTools: tc.strictTools, MaxInputTokens: maxInput},
							Transport: registry.Transport{BaseURL: srv.URL, CountTokensEndpoint: registry.EndpointUnsupported},
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
				t.Fatalf("Generate was not refused at tight ceiling %d (one token below emitted schema price): err = %v, hitServer = %v", tight, err, hitServer)
			}
			if hitServer {
				t.Fatal("request reached HTTP server despite exceeding max_input under emitted schema")
			}

			// Exact ceiling admits the request (proving open-schema cases are not
			// over-billed by the closed schema's 43 extra bytes).
			exact := tight + 1
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
			if !hitServer {
				t.Fatal("admitted request did not reach HTTP server")
			}

			// Direct unstamped Client.Complete and CountInputTokens must agree with
			// the same emitted-schema boundary.
			hitServer = false
			directReq := llm.Request{
				Provider: instance,
				Model:    model,
				Messages: []llm.Message{llm.User(prompt)},
				Tools:    []llm.ToolDefinition{probeDef},
			}
			if _, err := loadClient(&tight).Complete(context.Background(), directReq); !errors.As(err, &budgetErr) || budgetErr.Limit != "max_input" {
				t.Fatalf("direct unstamped Complete was not refused at tight ceiling %d: err = %v, hitServer = %v", tight, err, hitServer)
			}
			if hitServer {
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
