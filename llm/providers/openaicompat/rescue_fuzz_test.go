package openaicompat

import (
	"encoding/json"
	"testing"
)

// FuzzRescueClaudeXMLArgs drives rescueClaudeXMLArgs — the gnarly recovery
// parser that un-mangles Claude/MiniMax XML <parameter> tags leaking into a
// tool call's JSON arguments field — directly over fuzzed input bytes. The
// function is pure (no I/O, no network), so the raw fuzz string IS a fully
// contract-honoring input; any oracle failure here is a real product bug.
//
// Oracles:
//   - never panics for arbitrary input (floor);
//   - deterministic: the same input always yields byte-identical output;
//   - valid-JSON-on-rescue: if the output differs from the input, a rescue
//     fired and the result MUST be well-formed JSON (the function only diverges
//     from the passthrough by returning json.Marshal of the repaired object);
//   - idempotence: rescue is a fixed point — re-running rescue on its own
//     output reproduces that output, because a successful rescue strips the
//     XML <parameter> opens and parses embedded JSON, leaving nothing to redo.
func FuzzRescueClaudeXMLArgs(f *testing.F) {
	seeds := []string{
		"",
		`{}`,
		`{"action":"append"}`,
		// The canonical corruption from the doc comment: closed tag.
		`{"action":"append\">\n<parameter name=\"tasks\">[{\"id\":1}]</parameter>"}`,
		// Missing close tag.
		`{"action":"update\">\n<parameter name=\"updates\">[{\"a\":1}]"}`,
		// Single-quoted attribute name.
		`{"action":"go\"><parameter name='items'>[1,2,3]</parameter>"}`,
		// Multiple parameters in one value.
		`{"cmd":"run\"><parameter name=\"a\">1</parameter><parameter name=\"b\">2</parameter>"}`,
		// Uppercase tag.
		`{"cmd":"x\"><PARAMETER NAME=\"y\">true</PARAMETER>"}`,
		// Second-order rescue: a JSON-encoded array in a string value.
		`{"tasks":"[{\"id\":1},{\"id\":2}]"}`,
		// JSON-encoded object in a string value.
		`{"payload":"{\"k\":\"v\"}"}`,
		// A genuine string that merely starts with '[' — must NOT be mangled.
		`{"note":"[draft] hello"}`,
		// Collision: parent key wins over an extracted param of the same name.
		`{"action":"append\"><parameter name=\"action\">nope</parameter>"}`,
		// Non-string values present.
		`{"n":5,"b":true,"arr":[1,2],"s":"plain"}`,
		// XML tag but no `">` boundary before it.
		`{"v":"<parameter name=\"z\">hi</parameter>"}`,
		// Param value that is itself invalid JSON, kept as string.
		`{"v":"a\"><parameter name=\"p\">not json {[</parameter>"}`,
		// Not JSON at all.
		`not json`,
		`[1,2,3]`,
		`{"choices":`,
		// Tab / newline after "parameter".
		"{\"v\":\"a\\\"><parameter\tname=\\\"p\\\">1</parameter>\"}",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		out := rescueClaudeXMLArgs(raw)

		// Oracle: determinism.
		if again := rescueClaudeXMLArgs(raw); again != out {
			t.Fatalf("rescueClaudeXMLArgs not deterministic:\n input=%q\n out1 =%q\n out2 =%q", raw, out, again)
		}

		// Oracle: a fired rescue must produce well-formed JSON. When the output
		// equals the input the function took the passthrough path and makes no
		// well-formedness claim (raw may be arbitrary bytes).
		if out != raw {
			if !json.Valid([]byte(out)) {
				t.Fatalf("rescue produced invalid JSON:\n input=%q\n output=%q", raw, out)
			}
			// The repair path only ever emits json.Marshal of a top-level
			// object, so a fired rescue must decode as a JSON object.
			var parsed map[string]any
			if err := json.Unmarshal([]byte(out), &parsed); err != nil {
				t.Fatalf("rescued output is not a JSON object:\n input=%q\n output=%q\n err=%v", raw, out, err)
			}
		}

		// Oracle: idempotence — rescue is a fixed point.
		if fixed := rescueClaudeXMLArgs(out); fixed != out {
			t.Fatalf("rescueClaudeXMLArgs is not idempotent:\n input =%q\n once  =%q\n twice =%q", raw, out, fixed)
		}
	})
}
