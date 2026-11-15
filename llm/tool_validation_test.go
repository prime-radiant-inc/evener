package llm

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestValidateSchemaDocument(t *testing.T) {
	valid := []string{
		// The result_schema from evener issue #6298: array-of-object rows with
		// required, enum and integer properties inside items.
		`{"type":"object","properties":{"counts":{"type":"object"},"notes":{"type":"string"},"rows":{"type":"array","items":{"type":"object","required":["number","verdict","evidence"],"properties":{"canonical":{"type":"integer"},"evidence":{"type":"string"},"number":{"type":"integer"},"verdict":{"enum":["FIXED-ON-MAIN","DUPLICATE"],"type":"string"}}}},"surprises":{"type":"string"}}}`,
		`true`,
		`{"type":"null"}`,
		`{"type":["object","null"]}`,
		`{"properties":{"a":{"enum":[]}}}`,
	}
	for _, doc := range valid {
		var v any
		if err := json.Unmarshal([]byte(doc), &v); err != nil {
			t.Fatal(err)
		}
		if err := ValidateSchemaDocument(v); err != nil {
			t.Errorf("ValidateSchemaDocument(%s): %v", doc, err)
		}
	}

	invalid := map[string]string{
		// The exact shape lunaroute's draft-2020-12 tool validation rejected
		// in issue #6298: a properties value that is a bare type string.
		"property value is a bare type string": `{"type":"object","properties":{"rows":"array"}}`,
		// Draft-07 tuple items: an array where a single schema belongs.
		"items is a tuple array":        `{"type":"object","properties":{"rows":{"type":"array","items":[{"type":"integer"}]}}}`,
		"required is a bare string":     `{"type":"object","required":"rows","properties":{"rows":{"type":"array"}}}`,
		"a bare string is not a schema": `"array"`,
		"enum is not an array":          `{"properties":{"a":{"enum":{"x":1}}}}`,
	}
	for name, doc := range invalid {
		var v any
		if err := json.Unmarshal([]byte(doc), &v); err != nil {
			t.Fatal(err)
		}
		err := ValidateSchemaDocument(v)
		if err == nil {
			t.Errorf("%s: ValidateSchemaDocument(%s) accepted an invalid schema", name, doc)
		}
	}

	// A schema built with Go slices (required as []string, enum as []any) is
	// normalized before validation.
	if err := ValidateSchemaDocument(map[string]any{
		"type":       "object",
		"required":   []string{"rows"},
		"properties": map[string]any{"rows": map[string]any{"type": "array", "enum": []any{"a", "b"}}},
	}); err != nil {
		t.Errorf("ValidateSchemaDocument with Go slice values: %v", err)
	}
}

func TestValidateToolName(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	for _, name := range []string{"t", "Tool_1", "a_b_c", "Z9", strings.Repeat("a", 64)} {
		if err := ValidateToolName(name); err != nil {
			t.Fatalf("ValidateToolName(%q): %v", name, err)
		}
	}

	for _, name := range []string{"", "  ", "1bad", "_bad", "bad-name", "bad.name"} {
		if err := ValidateToolName(name); err == nil {
			t.Fatalf("expected error for name=%q", name)
		}
	}
	long := "a" + strings.Repeat("b", 64) // 65 chars
	if err := ValidateToolName(long); err == nil {
		t.Fatalf("expected error for long name (%d chars)", len(long))
	}
}

func TestRequestValidate_ToolsValidated(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	req := Request{Model: "m", Messages: []Message{User("hi")}}
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	// Invalid tool name.
	req.Tools = []ToolDefinition{{Name: "1bad", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}}
	if err := req.Validate(); err == nil {
		t.Fatalf("expected error for invalid tool name")
	} else {
		if _, ok := errors.AsType[*ConfigurationError](err); !ok {
			t.Fatalf("expected ConfigurationError, got %T (%v)", err, err)
		}
	}

	// Invalid schema root type.
	req.Tools = []ToolDefinition{{Name: "ok", Parameters: map[string]any{"type": "string"}}}
	if err := req.Validate(); err == nil {
		t.Fatalf("expected error for invalid schema root type")
	}

	// Nil schema treated as empty object schema.
	req.Tools = []ToolDefinition{{Name: "ok", Parameters: nil}}
	if err := req.Validate(); err != nil {
		t.Fatalf("expected nil schema to be allowed; got %v", err)
	}
}
