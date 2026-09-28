package repair

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// Issue #622: constraintMessage reads the failing property's enum off the
// container's TOP-LEVEL property. When the enum is narrowed inside an
// anyOf/allOf branch (/anyOf/0/properties/sandbox/enum) that announces the
// top-level list, which here literally contains the rejected value "off" —
// the same misdirection as #621, but reached because a root anyOf/allOf keeps
// its leaf keyword. The branch's own narrowed enum is the list that rejected
// the value, so the message must render that.
const branchEnumSchemaTemplate = `{
	"type": "object",
	"properties": {
		"task": {"type": "string"},
		"sandbox": {"type": "string", "enum": ["off", "read-only", "workspace-write", "restricted"]},
		"sandbox_net": {"type": "boolean"}
	},
	"required": ["task"],
	"COMBINATOR": [
		{"required": ["sandbox", "sandbox_net"],
		 "properties": {"sandbox": {"enum": ["read-only", "workspace-write", "restricted"]}}}
	]
}`

// branchEnumValidate compiles the schema, validates with the real
// santhosh-tekuri/jsonschema library (the registry's own path), and returns the
// decoded schema, the deepest cause's instance location and keyword location —
// the exact values offendingField and offendingKeywordLocation hand
// ExplainSchemaError.
func branchEnumValidate(t *testing.T, schemaJSON string, args map[string]any) (map[string]any, string, string) {
	t.Helper()
	c := jsonschema.NewCompiler()
	if err := c.AddResource("mem://branch_enum.json", strings.NewReader(schemaJSON)); err != nil {
		t.Fatalf("add resource: %v", err)
	}
	schema, err := c.Compile("mem://branch_enum.json")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	var params map[string]any
	if err := json.Unmarshal([]byte(schemaJSON), &params); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	verr := schema.Validate(args)
	if verr == nil {
		t.Fatalf("schema unexpectedly accepted args %v", args)
	}
	var ve *jsonschema.ValidationError
	if !errors.As(verr, &ve) {
		t.Fatalf("not a validation error: %v", verr)
	}
	for len(ve.Causes) > 0 {
		ve = ve.Causes[0]
	}
	return params, strings.Trim(ve.InstanceLocation, "/"), ve.KeywordLocation
}

// A branch-narrowed enum under a root anyOf/allOf must render the branch's own
// allowed-values list, never the top-level list that contains the rejected
// value (issue #622).
func TestExplainSchemaError_BranchEnumReadsBranchSchema(t *testing.T) {
	t.Parallel()
	args := map[string]any{"task": "ping", "sandbox": "off", "sandbox_net": true}
	for _, comb := range []string{"anyOf", "allOf"} {
		t.Run(comb, func(t *testing.T) {
			schemaJSON := strings.Replace(branchEnumSchemaTemplate, "COMBINATOR", comb, 1)
			params, field, loc := branchEnumValidate(t, schemaJSON, args)
			if field != "sandbox" {
				t.Fatalf("offendingField = %q, want %q", field, "sandbox")
			}
			if loc != "/"+comb+"/0/properties/sandbox/enum" {
				t.Fatalf("offendingKeywordLocation = %q, want the branch-level sandbox enum", loc)
			}
			msg := ExplainSchemaError("delegate", params, args, field, loc)
			if strings.Contains(msg, `"off", "read-only"`) {
				t.Fatalf("branch-level enum rendered the top-level sandbox enum that lists the rejected value: %q", msg)
			}
			if !strings.Contains(msg, `is not one of the allowed values: "read-only", "workspace-write", "restricted"`) {
				t.Fatalf("message must render the branch's narrowed sandbox enum: %q", msg)
			}
		})
	}
}
