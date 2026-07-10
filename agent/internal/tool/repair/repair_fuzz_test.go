package repair

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// FuzzRepairJSON checks the narrow JSON-repair contract: independently
// generated valid JSON remains byte-identical, and every claimed repair parses.
func FuzzRepairJSON(f *testing.F) {
	f.Add([]byte(`{"s":"plain"}`), "plain")
	f.Add([]byte(`{"s":"\uD800x"}`), "surrogate")
	f.Add([]byte(`{"s":"\uZZ"}`), "broken escape")
	f.Add([]byte(`{"s":"a\\ub"}`), "escaped backslash")
	f.Add([]byte(`{"s":"\uD83D\uDE00"}`), "emoji")

	f.Fuzz(func(t *testing.T, raw []byte, value string) {
		if len(raw) > 1<<16 || len(value) > 1<<16 {
			return
		}
		input := append([]byte(nil), raw...)
		out, changes := RepairJSON(raw)
		if !bytes.Equal(raw, input) {
			t.Fatal("RepairJSON mutated its input")
		}
		if len(changes) == 0 && !bytes.Equal(out, input) {
			t.Fatalf("RepairJSON changed input without recording a repair: in=%q out=%q", input, out)
		}
		if len(changes) > 0 && !json.Valid(out) {
			t.Fatalf("claimed repair is not valid JSON: in=%q out=%q changes=%+v", input, out, changes)
		}

		valid, err := json.Marshal(map[string]string{"value": value})
		if err != nil {
			t.Fatalf("marshal generated valid JSON: %v", err)
		}
		validOut, validChanges := RepairJSON(valid)
		if !bytes.Equal(validOut, valid) || validChanges != nil {
			t.Fatalf("valid JSON changed: in=%q out=%q changes=%+v", valid, validOut, validChanges)
		}
	})
}

// FuzzRepairArgs checks that normalization produces JSON-serializable output
// without mutating the caller's map.
func FuzzRepairArgs(f *testing.F) {
	f.Add("/tmp/file", "true", "42", "tag", "unknown", false)
	f.Add("", "FALSE", "not-a-number", "", "extra", true)
	f.Add("value", " true ", "3.14", "tag", "other", false)

	f.Fuzz(func(t *testing.T, path, flag, count, tag, extra string, openSchema bool) {
		if len(path)+len(flag)+len(count)+len(tag)+len(extra) > 1<<16 {
			return
		}
		params := map[string]any{
			"type":                 "object",
			"additionalProperties": openSchema,
			"properties": map[string]any{
				"file_path": map[string]any{"type": "string"},
				"flag":      map[string]any{"type": "boolean"},
				"count":     map[string]any{"type": "number"},
				"tags":      map[string]any{"type": "array"},
			},
		}
		args := map[string]any{
			"path":             path,
			"flag":             flag,
			"count":            count,
			"tags":             tag,
			"unknown_" + extra: extra,
		}
		before := make(map[string]any, len(args))
		for key, value := range args {
			before[key] = value
		}

		out, _ := RepairArgs(params, args)
		if !reflect.DeepEqual(args, before) {
			t.Fatalf("RepairArgs mutated input: before=%#v after=%#v", before, args)
		}
		encoded, err := json.Marshal(out)
		if err != nil {
			t.Fatalf("RepairArgs output is not JSON-serializable: %v; output=%#v", err, out)
		}
		var parsed map[string]any
		if err := json.Unmarshal(encoded, &parsed); err != nil {
			t.Fatalf("RepairArgs output did not parse: %v; output=%s", err, encoded)
		}

		outAgain, _ := RepairArgs(params, args)
		if !reflect.DeepEqual(outAgain, out) {
			t.Fatalf("RepairArgs output was not deterministic: first=%#v second=%#v", out, outAgain)
		}

		// These forms cover the safe no-op cases: a native alias, an alias
		// whose canonical field is absent, a competing canonical value, and
		// already-typed values.
		for _, tc := range []struct {
			params map[string]any
			args   map[string]any
		}{
			{
				params: map[string]any{"properties": map[string]any{"path": map[string]any{"type": "string"}}},
				args:   map[string]any{"path": "native"},
			},
			{
				params: map[string]any{"properties": map[string]any{"different": map[string]any{"type": "string"}}},
				args:   map[string]any{"old_str": "unmapped"},
			},
			{
				params: map[string]any{"properties": map[string]any{"file_path": map[string]any{"type": "string"}}},
				args:   map[string]any{"path": "alias", "file_path": "canonical"},
			},
			{
				params: map[string]any{"properties": map[string]any{
					"flag":  map[string]any{"type": "boolean"},
					"count": map[string]any{"type": "number"},
					"tags":  map[string]any{"type": "array"},
				}},
				args: map[string]any{"flag": true, "count": 1.0, "tags": []any{"typed"}},
			},
		} {
			beforeCase := make(map[string]any, len(tc.args))
			for key, value := range tc.args {
				beforeCase[key] = value
			}
			outCase, _ := RepairArgs(tc.params, tc.args)
			if _, err := json.Marshal(outCase); err != nil {
				t.Fatalf("extra RepairArgs case was not serializable: %v", err)
			}
			if !reflect.DeepEqual(tc.args, beforeCase) {
				t.Fatalf("extra RepairArgs case mutated input: before=%#v after=%#v", beforeCase, tc.args)
			}
		}
	})
}

// FuzzRepairDiagnostics drives the schema and JSON coaching functions with a
// schema that contains every supported example type. Their rendered text must
// remain deterministic and retain the caller's tool name and example marker.
func FuzzRepairDiagnostics(f *testing.F) {
	f.Add("edit_file", "old_string", "unexpected end", uint8(0), true, false)
	f.Add("read_file", "file_path", "invalid escape", uint8(1), false, false)
	f.Add("tool", "", "bad JSON", uint8(2), false, true)

	f.Fuzz(func(t *testing.T, toolName, offendingField, parseErr string, requiredForm uint8, fieldPresent, noOffendingField bool) {
		if len(toolName)+len(offendingField)+len(parseErr) > 1<<16 {
			return
		}
		params := repairDiagnosticParams(requiredForm)
		args := map[string]any{}
		if fieldPresent {
			args[offendingField] = "present"
		}
		if noOffendingField {
			offendingField = ""
		}

		schemaMessage := ExplainSchemaError(toolName, params, args, offendingField)
		if schemaMessage != ExplainSchemaError(toolName, params, args, offendingField) {
			t.Fatalf("schema explanation was not deterministic: %q", schemaMessage)
		}
		if !strings.Contains(schemaMessage, "Example:") {
			t.Fatalf("schema explanation omitted an example: %q", schemaMessage)
		}
		if toolName != "" && !strings.Contains(schemaMessage, toolName) {
			t.Fatalf("schema explanation omitted tool name %q: %q", toolName, schemaMessage)
		}
		if offendingField != "" {
			if fieldPresent && !strings.Contains(schemaMessage, "wrong type or value") {
				t.Fatalf("present offending field was not classified as wrong type: %q", schemaMessage)
			}
			if !fieldPresent && !strings.Contains(schemaMessage, "missing required argument") {
				t.Fatalf("missing offending field was not classified as missing: %q", schemaMessage)
			}
		}

		jsonMessage := ExplainJSONError(toolName, params, parseErr)
		if jsonMessage != ExplainJSONError(toolName, params, parseErr) {
			t.Fatalf("JSON explanation was not deterministic: %q", jsonMessage)
		}
		if !strings.Contains(jsonMessage, "JSON object") {
			t.Fatalf("JSON explanation omitted object guidance: %q", jsonMessage)
		}
	})
}

func repairDiagnosticParams(requiredForm uint8) map[string]any {
	required := []any{"file_path", "count", "enabled", "tags", "options", "untyped", "missing"}
	var value any = required
	switch requiredForm % 3 {
	case 1:
		value = []string{"file_path", "count", "enabled", "tags", "options", "untyped", "missing"}
	case 2:
		value = "not an array"
	}
	return map[string]any{
		"properties": map[string]any{
			"file_path": map[string]any{"type": "string"},
			"count":     map[string]any{"type": "integer"},
			"enabled":   map[string]any{"type": "boolean"},
			"tags":      map[string]any{"type": "array"},
			"options":   map[string]any{"type": "object"},
			"untyped":   "not a schema object",
		},
		"required": value,
	}
}

// FuzzRepairSuggestions verifies that suggestions are stable, always selected
// from the supplied vocabulary, and that unknown-tool messages list no more
// than the documented cap.
func FuzzRepairSuggestions(f *testing.F) {
	f.Add("reed_file", "read_file,write_file,shell", false)
	f.Add("zzzzzz", "read_file,shell", false)
	f.Add("tool_00", "", true)
	f.Add("", "", false)
	f.Add("cafe", "cafe,cafes", false)

	f.Fuzz(func(t *testing.T, requested, joined string, longList bool) {
		if len(requested) > 512 || len(joined) > 4096 {
			return
		}
		available := repairFuzzAvailable(joined, longList)
		suggestion := SuggestToolName(requested, available)
		if suggestion != SuggestToolName(requested, available) {
			t.Fatalf("suggestion was not deterministic: %q", suggestion)
		}
		if suggestion != "" && !repairFuzzContains(available, suggestion) {
			t.Fatalf("suggestion %q is not available in %#v", suggestion, available)
		}

		message := UnknownToolMessage(requested, available)
		if !strings.HasPrefix(message, fmt.Sprintf("unknown tool: %q.", requested)) {
			t.Fatalf("unknown-tool prefix mismatch: %q", message)
		}
		listed := available
		if len(listed) > maxAvailableListed {
			listed = listed[:maxAvailableListed]
		}
		if wantSuffix := "\nAvailable tools: " + strings.Join(listed, ", "); !strings.HasSuffix(message, wantSuffix) {
			t.Fatalf("available-tools suffix mismatch: got=%q want suffix=%q", message, wantSuffix)
		}
	})
}

func repairFuzzAvailable(joined string, longList bool) []string {
	var available []string
	if joined != "" {
		for _, name := range strings.Split(joined, ",") {
			if len(available) == 8 {
				break
			}
			if len(name) > 128 {
				name = name[:128]
			}
			available = append(available, name)
		}
	}
	if longList {
		for i := 0; i < maxAvailableListed+2; i++ {
			available = append(available, fmt.Sprintf("tool_%02d", i))
		}
	}
	return available
}

func repairFuzzContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
