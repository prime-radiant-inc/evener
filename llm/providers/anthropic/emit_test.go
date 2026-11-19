package anthropic

import (
	"encoding/json"
	"reflect"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// anthropicEmitCatalog holds the shapes the builder's per-tool decision treats
// differently, mixed so the deriver has to keep unchanged entries at their own
// index: a parameterless tool that gains the open empty object, one carrying a
// root combinator that loses it, and one the builder emits as it arrived.
func anthropicEmitCatalog() []llm.ToolDefinition {
	return []llm.ToolDefinition{
		{Name: "parameterless"},
		{
			Name: "combinator",
			Parameters: map[string]any{
				"anyOf": []any{
					map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}},
					map[string]any{"type": "object", "properties": map[string]any{"b": map[string]any{"type": "string"}}},
				},
			},
		},
		{
			Name: "plain",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"path": map[string]any{"type": "string"}},
				"required":   []string{"path"},
			},
		},
	}
}

// The deriver has to answer with the input schemas the builder puts on the wire.
//
// A deriver that disagreed with the builder would price a list the request never
// carries. The check is pairwise against the built tools rather than against a
// second expression of the rules, and the catalog mixes changed and unchanged
// tools so an entry dropped or reordered by the deriver shows up at its index.
func TestTheDeriverMatchesTheBuiltInputSchemas(t *testing.T) {
	t.Parallel()
	defs := anthropicEmitCatalog()
	built := toAnthropicTools(defs)

	sent, changed := (&Protocol{}).EmitToolSchemas(defs, registry.Resolved{})
	if !changed {
		t.Fatal("fixture does not make the deriver change anything")
	}
	emitted := sent
	if len(built) != len(emitted) {
		t.Fatalf("builder emits %d tools, deriver covers %d", len(built), len(emitted))
	}
	for i := range built {
		name, _ := built[i]["name"].(string)
		if name != emitted[i].Name {
			t.Fatalf("index %d: builder emits %q, deriver prices %q", i, name, emitted[i].Name)
		}
		wire, _ := built[i]["input_schema"].(map[string]any)
		assertSameInputSchema(t, name, wire, emitted[i].Parameters)
	}
}

// assertSameInputSchema fails unless the schema the builder emitted and the
// schema the deriver prices are the same schema.
//
// When both sides are present the comparison is for serialized length rather than
// for a deep equality: the deriver reports a byte-identical schema as unchanged
// (SameSerializedLength), so a value comparison would fail a case the price
// handles correctly. Absence cannot happen for Anthropic, which rejects a tool
// without input_schema, but the check states it so a builder that started
// omitting one would fail here rather than silently price zero bytes.
func assertSameInputSchema(t *testing.T, name string, wire, priced map[string]any) {
	t.Helper()
	if wire == nil || priced == nil {
		t.Fatalf("tool %q: builder emits input_schema present=%v, deriver prices present=%v",
			name, wire != nil, priced != nil)
	}
	a, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("tool %q: marshal built input_schema: %v", name, err)
	}
	b, err := json.Marshal(priced)
	if err != nil {
		t.Fatalf("tool %q: marshal priced input_schema: %v", name, err)
	}
	if len(a) != len(b) {
		t.Fatalf("tool %q: builder emits %s (%d bytes), deriver prices %s (%d bytes)",
			name, a, len(a), b, len(b))
	}
}

// A catalog the builder emits byte-identically must be reported unchanged.
//
// Reporting a change would re-marshal the whole catalog on every dispatch
// against the cached count that exists to avoid exactly that marshal.
func TestACombinatorFreeCatalogIsReportedUnchanged(t *testing.T) {
	t.Parallel()
	defs := []llm.ToolDefinition{{
		Name: "write_file",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"path": map[string]any{"type": "string"}},
			"required":   []string{"path"},
		},
	}}
	params := defs[0].Parameters
	if emitted, changed := (&Protocol{}).EmitToolSchemas(defs, registry.Resolved{}); changed {
		t.Fatalf("deriver reported a change for a schema the builder emits as it arrived (emitted %v)", emitted)
	}
	// The control: the same list with a root combinator is reported, so a
	// comparator that answered "same" for everything cannot pass above.
	control := []llm.ToolDefinition{{
		Name:       "write_file",
		Parameters: map[string]any{"allOf": []any{params}},
	}}
	if _, changed := (&Protocol{}).EmitToolSchemas(control, registry.Resolved{}); !changed {
		t.Fatal("deriver reported no change for a schema whose root combinator the builder strips")
	}
}

// An empty catalog has nothing to reprice, and saying so keeps the caller's
// cached count in place.
func TestAnEmptyCatalogReportsNoChange(t *testing.T) {
	t.Parallel()
	emitted, changed := (&Protocol{}).EmitToolSchemas(nil, registry.Resolved{})
	if changed || emitted != nil {
		t.Fatalf("deriver answered (%v, %v) for an empty catalog, want (nil, false)", emitted, changed)
	}
}

// The deriver measures a copy and is never asked to send anything, so the
// builder's own output must not depend on whether it ran.
func TestTheDeriverLeavesTheInputUntouched(t *testing.T) {
	t.Parallel()
	defs := anthropicEmitCatalog()
	before := anthropicClone(t, defs)
	if _, changed := (&Protocol{}).EmitToolSchemas(defs, registry.Resolved{}); !changed {
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

// anthropicClone round-trips a definition list through JSON so a comparison has
// no shared maps.
func anthropicClone(t *testing.T, defs []llm.ToolDefinition) []llm.ToolDefinition {
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
