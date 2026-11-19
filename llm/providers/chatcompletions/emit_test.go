package chatcompletions

import (
	"encoding/json"
	"reflect"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// chatRouteOn returns a resolved Chat row with the strict-tools capability as
// given, so a test names the route it prices.
func chatRouteOn(strict bool) registry.Resolved {
	row := registry.Resolved{Instance: "chat-emit", ModelID: "m", WireID: "m"}
	row.Protocol = registry.ProtocolOpenAIChat
	row.Caps.StrictTools = new(strict)
	return row
}

// characteristicCatalog is the set of shapes the builder's per-tool decision
// treats differently: a parameterless tool, an opt-out, a strict tool whose
// schema gains bytes under projection, and one whose projection is a pure
// reorder.
func characteristicCatalog() []llm.ToolDefinition {
	return []llm.ToolDefinition{
		{Name: "parameterless"},
		{Name: "opted_out", Strict: new(false)},
		{
			// Certified for strict mode, and the projection states the type the
			// enum implies: the one shape here the gate both accepts and grows.
			Name: "grows",
			Parameters: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties":           map[string]any{"mode": map[string]any{"enum": []any{"read", "write"}}},
				"required":             []string{"mode"},
			},
		},
		{
			Name: "reorders",
			Parameters: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"b": map[string]any{"type": "string"},
					"a": map[string]any{"type": "string"},
				},
				"required": []string{"b", "a"},
			},
		},
	}
}

// wireParams reads the parameters key of each tool from a built body, with nil
// for a tool whose key the builder omitted.
func wireParams(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	raw, ok := body["tools"].([]map[string]any)
	if !ok {
		t.Fatalf("body tools = %#v, want the wire tool list", body["tools"])
	}
	out := make([]map[string]any, len(raw))
	for i, entry := range raw {
		fn, _ := entry["function"].(map[string]any)
		params, _ := fn["parameters"].(map[string]any)
		out[i] = params
	}
	return out
}

// The deriver has to answer with the schemas the builder puts on the wire.
//
// Every price this change computes rests on that agreement: a deriver that
// disagreed with the builder would price a list the request never carries, which
// is the defect it exists to fix. The check is pairwise against the built body
// rather than against a second expression of the rules, and it covers presence
// or absence of the parameters key as well as its contents, because the builder
// omits it for a parameterless tool and that omission is zero bytes.
func TestTheDeriverMatchesTheBuiltWireSchemas(t *testing.T) {
	t.Parallel()
	for _, strict := range []bool{true, false} {
		res := chatRouteOn(strict)
		defs := characteristicCatalog()
		body, err := buildBody(llm.Request{Messages: []llm.Message{llm.User("hi")}, Tools: defs}, res, false)
		if err != nil {
			t.Fatalf("buildBody strict=%v: %v", strict, err)
		}
		wire := wireParams(t, body)

		emitted := defs
		if sent, changed := (&Protocol{}).EmitToolSchemas(defs, res); changed {
			emitted = sent
		}
		if len(wire) != len(emitted) {
			t.Fatalf("strict=%v: wire carries %d tools, deriver covers %d", strict, len(wire), len(emitted))
		}
		for i := range wire {
			assertSameWireSchema(t, strict, emitted[i].Name, wire[i], emitted[i].Parameters)
		}
	}
}

// assertSameWireSchema fails unless the schema the builder emitted and the
// schema the deriver prices are the same schema.
//
// Absence matches absence, because the builder omits the parameters key for a
// parameterless tool and that omission is zero bytes. When both sides are
// present the comparison is for serialized length rather than for a deep
// equality: the projection sorts a required list, so a catalog whose list is
// already complete is emitted as a permutation of itself, and the deriver
// deliberately reports that as unchanged (SameSerializedLength). Comparing the
// values would fail a case the price handles correctly.
func assertSameWireSchema(t *testing.T, strict bool, name string, wire, priced map[string]any) {
	t.Helper()
	if (wire != nil) != (priced != nil) {
		t.Fatalf("strict=%v tool %q: builder emits parameters present=%v, deriver reports present=%v",
			strict, name, wire != nil, priced != nil)
	}
	if wire == nil {
		return
	}
	a, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("marshal wire: %v", err)
	}
	b, err := json.Marshal(priced)
	if err != nil {
		t.Fatalf("marshal emitted: %v", err)
	}
	if len(a) != len(b) {
		t.Fatalf("strict=%v tool %q: builder emits %s (%d bytes), deriver reports %s (%d bytes)",
			strict, name, a, len(a), b, len(b))
	}
}

// A tool whose only projection difference is the order of its required list must
// be reported unchanged.
//
// The projection sorts each object's required list, so an already complete list
// is emitted as a permutation of itself and the bytes are the same. Reporting a
// change would re-marshal the whole catalog on every dispatch against the cached
// count that exists to avoid exactly that marshal.
func TestAReorderOnlyProjectionIsReportedUnchanged(t *testing.T) {
	t.Parallel()
	res := chatRouteOn(true)
	defs := []llm.ToolDefinition{{
		Name: "write_file",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"path":    map[string]any{"type": "string"},
				"content": map[string]any{"type": "string"},
			},
			// Out of order: the projection sorts it, and the two serialize to
			// the same length.
			"required": []string{"path", "content"},
		},
		Strict: new(true),
	}}
	emitted, changed := (&Protocol{}).EmitToolSchemas(defs, res)
	if changed {
		t.Fatalf("deriver reported a change for a reorder-only projection (emitted %v)", emitted)
	}
	// The control: a catalog the projection really does grow is reported, so a
	// comparator that answered "same" for everything cannot pass above.
	// The schema is certified for strict mode — already closed, with every
	// property required — and the enum is a closed value set with no stated type,
	// so the projection states the type it implies. That is the byte growth this
	// control needs; a schema the gate refuses keeps its advertised bytes.
	growing := []llm.ToolDefinition{{
		Name: "write_file",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           map[string]any{"mode": map[string]any{"enum": []any{"read", "write"}}},
			"required":             []string{"mode"},
		},
		Strict: new(true),
	}}
	if _, changed := (&Protocol{}).EmitToolSchemas(growing, res); !changed {
		t.Fatal("deriver reported no change for a catalog the projection closes")
	}
}

// A route without the capability must report no change at all, so an
// unsubstituted request keeps the price it already had.
func TestANonStrictChatRouteReportsNoChange(t *testing.T) {
	t.Parallel()
	res := chatRouteOn(false)
	if _, changed := (&Protocol{}).EmitToolSchemas(characteristicCatalog(), res); changed {
		t.Fatal("a route without strict enforcement reported a change: its builder sends the definitions it " +
			"was handed")
	}
}

// The builder's own output must not depend on whether the deriver ran, since the
// deriver measures a copy and is never asked to send anything.
func TestTheDeriverLeavesTheInputUntouched(t *testing.T) {
	t.Parallel()
	defs := characteristicCatalog()
	before := deepClone(t, defs)
	if _, changed := (&Protocol{}).EmitToolSchemas(defs, chatRouteOn(true)); !changed {
		t.Fatal("fixture does not make the deriver change anything")
	}
	// Compare the serialized forms: a Strict pointer differs by address between
	// the original and a round-tripped clone, which is not a rewrite.
	got, err := json.Marshal(defs)
	if err != nil {
		t.Fatalf("marshal defs: %v", err)
	}
	want, err := json.Marshal(before)
	if err != nil {
		t.Fatalf("marshal clone: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("deriver rewrote its input: got %s, want %s", got, want)
	}
}

// deepClone round-trips a definition list through JSON so a comparison has no
// shared maps.
func deepClone(t *testing.T, defs []llm.ToolDefinition) []llm.ToolDefinition {
	t.Helper()
	b, err := json.Marshal(defs)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out []llm.ToolDefinition
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}
