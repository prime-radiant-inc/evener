package agent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"

	"primeradiant.com/evener/agent/internal/tool/repair"
)

// Issue #623 end-to-end through the real cause tree: when a oneOf fails
// because the arguments match MORE THAN ONE arm (oneOf means exactly-one), the
// validator's deepest cause is the bare /oneOf node with no per-arm children,
// so offendingKeyword reports "oneOf" — the discriminator ExplainSchemaError
// uses to render the over-match recovery instead of branch requirements the
// arguments already satisfy. This test pins that discriminator against the
// validator, not just against a hardcoded keyword.
func TestOffendingKeyword_MultipleMatchOneOfRendersOverMatch(t *testing.T) {
	const schemaJSON = `{"type":"object","properties":{"a":{"type":"string"}},` +
		`"oneOf":[{"required":["a"]},{"required":["a"]}]}`
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("multi.json", strings.NewReader(schemaJSON)); err != nil {
		t.Fatalf("add resource: %v", err)
	}
	compiled, err := compiler.Compile("multi.json")
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	var params, args map[string]any
	if err := json.Unmarshal([]byte(schemaJSON), &params); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	if err := json.Unmarshal([]byte(`{"a":"x"}`), &args); err != nil {
		t.Fatalf("unmarshal args: %v", err)
	}
	err = compiled.Validate(args)
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected a validation error for the multiple-match args, got %v", err)
	}
	if kw := offendingKeyword(err); kw != "oneOf" {
		t.Fatalf("multiple-match deepest keyword = %q, want %q (the bare-combinator signal #623 relies on)", kw, "oneOf")
	}
	msg := repair.ExplainSchemaError("probe_tool", params, args, offendingField(err), offendingKeyword(err))
	if strings.Contains(msg, "Branch 0 requires") || strings.Contains(msg, "Branch 1 requires") {
		t.Fatalf("rendered branch requirements the args already satisfy (issue #623): %q", msg)
	}
	if !strings.Contains(msg, "matched more than one branch") || !strings.Contains(msg, "satisfy exactly one") {
		t.Fatalf("message must name the over-match and its recovery: %q", msg)
	}
}
