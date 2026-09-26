package agent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"

	"primeradiant.com/evener/agent/internal/tool/repair"
)

// validateJSON compiles schemaJSON, validates argsJSON against it, and returns
// the validation error (failing the test if there is none).
func validateJSON(t *testing.T, schemaJSON, argsJSON string) (map[string]any, map[string]any, error) {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("probe.json", strings.NewReader(schemaJSON)); err != nil {
		t.Fatalf("add resource: %v", err)
	}
	compiled, err := compiler.Compile("probe.json")
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	var params, args map[string]any
	if err := json.Unmarshal([]byte(schemaJSON), &params); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		t.Fatalf("unmarshal args: %v", err)
	}
	verr := compiled.Validate(args)
	if _, ok := errors.AsType[*jsonschema.ValidationError](verr); !ok {
		t.Fatalf("expected a validation error, got %v", verr)
	}
	return params, args, verr
}

// Issue #623 end-to-end through the real cause tree: when a oneOf fails
// because the arguments match MORE THAN ONE arm (oneOf means exactly-one), the
// validator's deepest cause is the bare /oneOf node with no per-arm children,
// so offendingKeywordLocation reports "/oneOf" — the discriminator
// ExplainSchemaError uses to render the over-match recovery instead of branch
// requirements the arguments already satisfy, and to omit an example that
// would match zero branches. This test pins that discriminator against the
// validator, not just against a hardcoded location.
func TestOffendingKeywordLocation_MultipleMatchOneOfRendersOverMatch(t *testing.T) {
	t.Parallel()
	const schemaJSON = `{"type":"object","properties":{"a":{"type":"string"}},` +
		`"oneOf":[{"required":["a"]},{"required":["a"]}]}`
	params, args, verr := validateJSON(t, schemaJSON, `{"a":"x"}`)
	if loc := offendingKeywordLocation(verr); loc != "/oneOf" {
		t.Fatalf("multiple-match deepest location = %q, want %q (the bare-combinator signal #623 relies on)", loc, "/oneOf")
	}
	msg := repair.ExplainSchemaError("probe_tool", params, args, offendingField(verr), offendingKeywordLocation(verr))
	if strings.Contains(msg, "Branch 0 requires") || strings.Contains(msg, "Branch 1 requires") {
		t.Fatalf("rendered branch requirements the args already satisfy (issue #623): %q", msg)
	}
	if !strings.Contains(msg, "matched more than one branch") || !strings.Contains(msg, "satisfy exactly one") {
		t.Fatalf("message must name the over-match and its recovery: %q", msg)
	}
	if strings.Contains(msg, "Example:") {
		t.Fatalf("over-match message must not append an example matching zero branches: %q", msg)
	}
}

// A nested oneOf failure is not an over-match of the outer oneOf: the deepest
// cause is the inner combinator ("/oneOf/0/oneOf"), so the message must keep
// the outer branch enumeration rather than falsely claiming multiple outer
// branches matched (roborev finding 1), pinned against the real validator.
func TestOffendingKeywordLocation_NestedOneOfNoMatchNotOverMatch(t *testing.T) {
	t.Parallel()
	const schemaJSON = `{"type":"object","oneOf":[` +
		`{"oneOf":[{"required":["a"]},{"required":["a"]}]},` +
		`{"required":["b"]}]}`
	params, args, verr := validateJSON(t, schemaJSON, `{"a":"x"}`)
	if loc := offendingKeywordLocation(verr); loc != "/oneOf/0/oneOf" {
		t.Fatalf("nested deepest location = %q, want %q", loc, "/oneOf/0/oneOf")
	}
	msg := repair.ExplainSchemaError("probe_tool", params, args, offendingField(verr), offendingKeywordLocation(verr))
	if strings.Contains(msg, "matched more than one branch") {
		t.Fatalf("nested oneOf failure falsely claimed an outer over-match (roborev finding 1): %q", msg)
	}
	if !strings.Contains(msg, `send all of "b"`) {
		t.Fatalf("outer no-match must still describe its describable branch requirement: %q", msg)
	}
	if strings.Contains(msg, "Example:") {
		t.Fatalf("nested oneOf no-match must not append an example matching zero branches (roborev follow-up): %q", msg)
	}
}
