package agent

import (
	"encoding/json"
	"strings"
	"testing"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"

	"primeradiant.com/evener/agent/internal/tool/repair"
)

// issue622SchemaJSON is the reproduction schema from issue #622 with the two
// oneOf arms ordered so jsonschema's deepest cause is the branch-level enum
// (/oneOf/0/properties/sandbox/enum, instance /sandbox) rather than the
// not-arm. The top-level sandbox enum allows "off"; the branch forbids it.
const issue622SchemaJSON = `{
  "type": "object",
  "properties": {
    "task": {"type": "string"},
    "sandbox": {"type": "string", "enum": ["off", "read-only", "workspace-write", "restricted"]},
    "sandbox_net": {"type": "boolean"}
  },
  "required": ["task"],
  "oneOf": [
    {"required": ["sandbox", "sandbox_net"],
     "properties": {"sandbox": {"enum": ["read-only", "workspace-write", "restricted"]}}},
    {"not": {"required": ["sandbox_net"]}}
  ]
}`

// TestOffendingKeywordLocationBranchEnum pins the production plumbing end to
// end: validation of the issue #622 schema yields the deepest cause's
// KeywordLocation, and ExplainSchemaError renders the branch-level pairing rule
// rather than an allowed-values list. The failing arm's sibling
// (`not: {required: ["sandbox_net"]}`) accepts sandbox "off" while sandbox_net
// is omitted, so the failing arm's narrowed enum is not the globally accepted
// set; the top-level enum, which lists "off" as allowed, must never appear.
func TestOffendingKeywordLocationBranchEnum(t *testing.T) {
	c := jsonschema.NewCompiler()
	if err := c.AddResource("issue622.json", strings.NewReader(issue622SchemaJSON)); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile("issue622.json")
	if err != nil {
		t.Fatal(err)
	}
	var inst any
	if err := json.Unmarshal([]byte(`{"task":"ping","sandbox":"off","sandbox_net":true}`), &inst); err != nil {
		t.Fatal(err)
	}
	verr := s.Validate(inst)
	if verr == nil {
		t.Fatal("schema unexpectedly accepted the args")
	}
	if got := offendingField(verr); got != "sandbox" {
		t.Fatalf("offendingField = %q, want %q", got, "sandbox")
	}
	const wantLoc = "/oneOf/0/properties/sandbox/enum"
	if got := offendingKeywordLocation(verr); got != wantLoc {
		t.Fatalf("offendingKeywordLocation = %q, want %q", got, wantLoc)
	}
	var params map[string]any
	if err := json.Unmarshal([]byte(issue622SchemaJSON), &params); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"task": "ping", "sandbox": "off", "sandbox_net": true}
	msg := repair.ExplainSchemaError("delegate", params, args, offendingField(verr), offendingKeywordLocation(verr))
	if strings.Contains(msg, "is not one of the allowed values") {
		t.Fatalf("ambiguous oneOf arm enum rendered as a global allowed-values list: %q", msg)
	}
	if strings.Contains(msg, `"off", "read-only"`) {
		t.Fatalf("message reproduced the top-level sandbox enum: %q", msg)
	}
	if !strings.Contains(msg, "oneOf constraint") {
		t.Fatalf("message must name the oneOf constraint: %q", msg)
	}
	if !strings.Contains(msg, `"sandbox" must be one of "read-only", "workspace-write", "restricted"`) {
		t.Fatalf("message must render the branch's narrowed sandbox enum: %q", msg)
	}
	if !strings.Contains(msg, "sandbox_net") {
		t.Fatalf("message must name the constrained pairing field sandbox_net: %q", msg)
	}
}

// notFailureSchemaJSON has a oneOf arm that is a bare `not`: it fails when its
// inner enum matches.
const notFailureSchemaJSON = `{
  "type": "object",
  "properties": {
    "task": {"type": "string"},
    "color": {"type": "string", "enum": ["red", "green", "blue"]}
  },
  "required": ["task"],
  "oneOf": [
    {"not": {"properties": {"color": {"enum": ["red"]}}}}
  ]
}`

// TestNotFailureKeywordLocationIsTheCombinator refutes the review's "not
// fallback can render contradictory guidance" concern as unreachable: a failing
// `not` surfaces as keyword `not` at the combinator (instance location empty),
// never as an inner enum location such as
// /oneOf/0/not/properties/color/enum. The negation fallback in
// constraintFieldSchema is therefore defensive only — real jsonschema output
// takes the isBranchKeyword path, not constraintMessage.
func TestNotFailureKeywordLocationIsTheCombinator(t *testing.T) {
	c := jsonschema.NewCompiler()
	if err := c.AddResource("notfail.json", strings.NewReader(notFailureSchemaJSON)); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile("notfail.json")
	if err != nil {
		t.Fatal(err)
	}
	var inst any
	if err := json.Unmarshal([]byte(`{"task":"t","color":"red"}`), &inst); err != nil {
		t.Fatal(err)
	}
	verr := s.Validate(inst)
	if verr == nil {
		t.Fatal("schema unexpectedly accepted the args")
	}
	if got := offendingKeywordLocation(verr); got != "/oneOf/0/not" {
		t.Fatalf("offendingKeywordLocation = %q, want %q (inner enum location is never emitted)", got, "/oneOf/0/not")
	}
	if got := offendingField(verr); got != "" {
		t.Fatalf("offendingField = %q, want empty (no property location for a not failure)", got)
	}
}
