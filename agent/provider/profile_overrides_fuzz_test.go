package provider

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"primeradiant.com/serf/fuzz/edgeseeds"
)

// FuzzProfileOverrides drives the communicate-tool schema decorators
// (WithCommunicateOutputSchema, WithAllowedDecisions) and their helpers
// (replaceCommunicateOutputSchema, addDecisionToSchema, deepCopyJSONMap,
// toStringSlice) over arbitrary output schemas and decision lists. These build
// orchestration-facing tool schemas by deep-copying shared map state — a class
// of code where an accidental shared-reference mutation is both easy to write and
// catastrophic (it would corrupt the base profile reused across sessions). Only
// unit tests with a few fixed schemas touched them.
//
// Oracles (never bare no-panic):
//   - no base mutation: the base profile's serialized tool definitions are
//     byte-identical before and after each decorator runs (the deep-copy contract
//     the code comments explicitly promise).
//   - decoration takes effect: after WithAllowedDecisions with a non-empty,
//     JSON-round-trippable decision list, the communicate tool's
//     output.properties.decision carries those decisions as an enum and "output"
//     is required at the top level.
//   - deepCopyJSONMap round-trips and is independent: the copy is deep-equal to the
//     input and mutating the copy never reaches the input.
//   - toStringSlice agrees across []string and its []any (post-JSON) form.
//
// SAFETY: pure in-memory schema transforms — no network, no I/O, no spawn.
func FuzzProfileOverrides(f *testing.F) {
	f.Add([]byte(`{"type":"object","properties":{"x":{"type":"string"}}}`), "approve\ndeny")
	f.Add([]byte(`{"type":"string","description":"custom"}`), "")
	f.Add([]byte(`{}`), "a\nb\nc")
	f.Add([]byte(`null`), "only")
	f.Add([]byte(`[1,2,3]`), "x")
	for _, s := range edgeseeds.JSON() {
		f.Add(s, "continue\nstop")
	}

	f.Fuzz(func(t *testing.T, schemaBytes []byte, decisionBlob string) {
		// A real profile with the full communicate tool definition.
		base := NewOpenAIProfile("gpt-4")
		baseSnapshot := mustMarshal(t, base.ToolDefinitions())

		// Decode the fuzzed schema; only a JSON object is a valid output schema.
		var outputSchema map[string]any
		if err := json.Unmarshal(schemaBytes, &outputSchema); err != nil || outputSchema == nil {
			outputSchema = nil
		}

		decisions := splitNonEmpty(decisionBlob)

		// Apply WithCommunicateOutputSchema then WithAllowedDecisions (the
		// documented stacking order).
		p1 := WithCommunicateOutputSchema(base, outputSchema)
		assertBaseUnchanged(t, base, baseSnapshot, "WithCommunicateOutputSchema")

		p2 := WithAllowedDecisions(p1, decisions)
		assertBaseUnchanged(t, base, baseSnapshot, "WithAllowedDecisions(base)")
		if p1 != base {
			// p1 is a distinct clone; decorating it must not mutate it either.
			p1Snapshot := mustMarshal(t, p1.ToolDefinitions())
			_ = WithAllowedDecisions(p1, decisions)
			if after := mustMarshal(t, p1.ToolDefinitions()); !bytesEqual(p1Snapshot, after) {
				t.Fatalf("WithAllowedDecisions mutated its receiver profile")
			}
		}

		// When decisions survive the JSON round-trip inside addDecisionToSchema and
		// the communicate tool has a usable output schema, the decoration must take
		// effect: decision enum + required output.
		if len(decisions) > 0 {
			assertDecisionApplied(t, p2, decisions)
		}

		// deepCopyJSONMap: round-trip fidelity + independence from the source.
		if outputSchema != nil {
			cp, err := deepCopyJSONMap(outputSchema)
			if err == nil {
				if !reflect.DeepEqual(cp, outputSchema) {
					t.Fatalf("deepCopyJSONMap not equal to source:\n src=%v\n cp =%v", outputSchema, cp)
				}
				// Re-decode the source independently; mutating the copy must not
				// disturb this reference snapshot.
				ref := mustMarshal(t, outputSchema)
				for k := range cp {
					cp[k] = "MUTATED"
				}
				if after := mustMarshal(t, outputSchema); !bytesEqual(ref, after) {
					t.Fatalf("mutating deepCopyJSONMap output disturbed the source")
				}
			}
		}

		// toStringSlice agrees on []string and the []any form JSON unmarshal yields.
		assertToStringSliceAgrees(t, decisions)
	})
}

func assertBaseUnchanged(t *testing.T, base *Profile, snapshot []byte, op string) {
	t.Helper()
	if after := mustMarshal(t, base.ToolDefinitions()); !bytesEqual(snapshot, after) {
		t.Fatalf("%s mutated the base profile's tool definitions", op)
	}
}

func assertDecisionApplied(t *testing.T, p *Profile, decisions []string) {
	t.Helper()
	def := findToolDef(p, "communicate")
	if def == nil || def.Parameters == nil {
		return
	}
	props, _ := def.Parameters["properties"].(map[string]any)
	output, _ := props["output"].(map[string]any)
	if output == nil {
		return // no output schema to decorate (e.g. nil/empty supplied schema path)
	}
	outProps, _ := output["properties"].(map[string]any)
	if outProps == nil {
		return
	}
	decAny, ok := outProps["decision"]
	if !ok {
		t.Fatalf("decision field missing after WithAllowedDecisions")
	}
	decMap, _ := decAny.(map[string]any)
	enum := toStringSlice(decMap["enum"])
	if len(enum) != len(decisions) {
		t.Fatalf("decision enum has %d values, want %d", len(enum), len(decisions))
	}
	for i, d := range decisions {
		if enum[i] != d {
			t.Fatalf("decision enum[%d] = %q, want %q", i, enum[i], d)
		}
	}
	if !contains(toStringSlice(def.Parameters["required"]), "output") {
		t.Fatalf("output not required at top level after WithAllowedDecisions")
	}
}

func assertToStringSliceAgrees(t *testing.T, ss []string) {
	t.Helper()
	fromStrings := toStringSlice(ss)
	anySlice := make([]any, len(ss))
	for i, s := range ss {
		anySlice[i] = s
	}
	fromAny := toStringSlice(anySlice)
	if len(fromStrings) != len(fromAny) {
		t.Fatalf("toStringSlice length disagreement: %d vs %d", len(fromStrings), len(fromAny))
	}
	for i := range fromStrings {
		if fromStrings[i] != fromAny[i] {
			t.Fatalf("toStringSlice element %d disagreement: %q vs %q", i, fromStrings[i], fromAny[i])
		}
	}
}

func splitNonEmpty(blob string) []string {
	var out []string
	for _, line := range splitLines(blob) {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func bytesEqual(a, b []byte) bool { return bytes.Equal(a, b) }
