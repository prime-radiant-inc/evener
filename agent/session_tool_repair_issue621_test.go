package agent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"

	"primeradiant.com/evener/agent/internal/tool/repair"
)

// issue621SwappedOneOfSchema is the delegate sandbox/sandbox_net oneOf pairing
// rule with the POSITIVE arm ordered first and the `not` arm second — the
// order issue #621 reports. With the arms this way the deepest first cause of a
// violation is arm-internal (/oneOf/0/properties/sandbox/enum); before the fix
// ExplainSchemaError read that leaf keyword against the TOP-LEVEL sandbox
// property and rendered its enum, whose list contains "off" — the very value
// it claimed was disallowed.
const issue621SwappedOneOfSchema = `{
	"type": "object",
	"additionalProperties": false,
	"properties": {
		"task":        {"type": "string"},
		"sandbox":     {"type": "string", "enum": ["off", "read-only", "workspace-write", "restricted"]},
		"sandbox_net": {"type": "boolean"}
	},
	"required": ["task"],
	"oneOf": [
		{"required": ["sandbox", "sandbox_net"],
		 "properties": {"sandbox": {"enum": ["read-only", "workspace-write", "restricted"]}}},
		{"not": {"required": ["sandbox_net"]}}
	]
}`

// issue621NotFirstOneOfSchema is the same rule with the arms in the order
// definitions.go hardcoded (the `not` arm first) — the #618 shape, whose
// deepest cause is the bare /oneOf/0/not. Both orders must attribute the
// failure to the enclosing oneOf; arm order must not decide the message.
const issue621NotFirstOneOfSchema = `{
	"type": "object",
	"additionalProperties": false,
	"properties": {
		"task":        {"type": "string"},
		"sandbox":     {"type": "string", "enum": ["off", "read-only", "workspace-write", "restricted"]},
		"sandbox_net": {"type": "boolean"}
	},
	"required": ["task"],
	"oneOf": [
		{"not": {"required": ["sandbox_net"]}},
		{"required": ["sandbox", "sandbox_net"],
		 "properties": {"sandbox": {"enum": ["read-only", "workspace-write", "restricted"]}}}
	]
}`

// issue621RefWrappedOneOfSchema hides the oneOf behind a $ref, so jsonschema
// reports the deepest cause as /$ref/oneOf/0/required — the form that pins the
// $ref-skipping branch attribution.
const issue621RefWrappedOneOfSchema = `{
	"type": "object",
	"properties": {"a": {"type": "string"}},
	"required": ["a"],
	"$ref": "#/$defs/rule",
	"$defs": {"rule": {"oneOf": [{"required": ["zzz"]}, {"required": ["qqq"]}]}}
}`

// issue621ArmInternalTypeSchema is a root oneOf whose single arm requires a
// present property and narrows that property's type, so the deepest cause is
// the arm-internal type constraint rather than the branch structure.
const issue621ArmInternalTypeSchema = `{
	"type": "object",
	"properties": {"x": {"type": "string"}},
	"required": ["x"],
	"oneOf": [{"required": ["x"], "properties": {"x": {"type": "integer"}}}]
}`

// issue621RefWrappedNotSchema hides a root-level `not` behind a $ref.
const issue621RefWrappedNotSchema = `{
	"type": "object",
	"properties": {"a": {"type": "string"}},
	"required": ["a"],
	"$ref": "#/$defs/rule",
	"$defs": {"rule": {"not": {"required": ["a"]}}}
}`

// issue621RefWrappedPresentEnumSchema hides an arm that narrows a present
// property's enum behind a $ref, while the top-level property keeps the wider
// enum — the shape where a fall-through would announce a list that contains the
// rejected value.
const issue621RefWrappedPresentEnumSchema = `{
	"type": "object",
	"properties": {"x": {"type": "string", "enum": ["off", "a"]}},
	"required": ["x"],
	"$ref": "#/$defs/rule",
	"$defs": {"rule": {"oneOf": [
		{"required": ["x"], "properties": {"x": {"enum": ["a"]}}}
	]}}
}`

// issue621NestedArmEnumSchema narrows an enum deeper inside the arm than the
// direct arm property branchRequirement renders, while the top-level property
// keeps the wider enum that contains the rejected value.
const issue621NestedArmEnumSchema = `{
	"type": "object",
	"properties": {"opts": {"type": "object", "properties": {"status": {"type": "string", "enum": ["a", "b"]}}}},
	"required": ["opts"],
	"oneOf": [{"required": ["opts"], "properties": {"opts": {"properties": {"status": {"enum": ["a"]}}}}}]
}`

// issue621RootNotTwoNamesSchema forbids the conjunction of two properties.
const issue621RootNotTwoNamesSchema = `{
	"type": "object",
	"properties": {"a": {"type": "string"}, "b": {"type": "string"}},
	"not": {"required": ["a", "b"]}
}`

// issue621MixedRootNotSchema is a root `not` that also constrains a value:
// {"mode": "on"} satisfies it, so "do not send mode" would be false.
const issue621MixedRootNotSchema = `{
	"type": "object",
	"properties": {"mode": {"type": "string"}},
	"required": ["mode"],
	"not": {"required": ["mode"], "properties": {"mode": {"enum": ["off"]}}}
}`

// issue621ArmNotWithEnumSchema is a oneOf whose only arm carries a `not` AND
// narrows a required property's enum.
const issue621ArmNotWithEnumSchema = `{
	"type": "object",
	"properties": {"x": {"type": "string", "enum": ["a", "b"]}, "y": {"type": "string"}},
	"oneOf": [{"required": ["x"], "properties": {"x": {"enum": ["a"]}}, "not": {"required": ["y"]}}]
}`

// issue621StricterRefLimitSchema puts a stricter maxLength on the referenced
// schema than on the top-level property of the same name.
const issue621StricterRefLimitSchema = `{
	"type": "object",
	"properties": {"x": {"type": "string", "maxLength": 100}},
	"required": ["x"],
	"$ref": "#/$defs/rule",
	"$defs": {"rule": {"properties": {"x": {"maxLength": 3}}}}
}`

// issue621StricterArmLimitSchema puts a stricter maxLength on the arm than on
// the top-level property of the same name.
const issue621StricterArmLimitSchema = `{
	"type": "object",
	"properties": {"x": {"type": "string", "maxLength": 100}},
	"oneOf": [{"required": ["x"], "properties": {"x": {"maxLength": 3}}}]
}`

// issue621NotWithSiblingOneOfSchema is a root `not` beside a sibling oneOf
// whose constraints the top-level example does not model.
const issue621NotWithSiblingOneOfSchema = `{
	"type": "object",
	"properties": {"a": {"type": "string"}, "b": {"type": "string"}},
	"required": ["a"],
	"oneOf": [{"required": ["a"]}, {"required": ["b"]}],
	"not": {"required": ["a"]}
}`

// issue621DelegateArgs is the forbidden sandbox/sandbox_net pairing the two
// delegate oneOf schemas above reject.
func issue621DelegateArgs() map[string]any {
	return map[string]any{"task": "ping", "sandbox": "off", "sandbox_net": true}
}

// issue621Validate compiles the schema, validates args with the real
// santhosh-tekuri/jsonschema library (the registry's own path), and returns the
// schema decoded to the map[string]any form ExplainSchemaError takes, the args
// it validated, and the validation error.
func issue621Validate(t *testing.T, schemaJSON string, args map[string]any) (map[string]any, map[string]any, error) {
	t.Helper()
	c := jsonschema.NewCompiler()
	if err := c.AddResource("mem://issue621.json", strings.NewReader(schemaJSON)); err != nil {
		t.Fatalf("add resource: %v", err)
	}
	schema, err := c.Compile("mem://issue621.json")
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
	if _, ok := errors.AsType[*jsonschema.ValidationError](verr); !ok {
		t.Fatalf("not a validation error: %v", verr)
	}
	return params, args, verr
}

// Issue #621/#622: the real caller's attribution must not depend on the oneOf
// arm order, and neither order may render the top-level enum that lists "off" as
// allowed. Either way the failing arm's sibling (`not: {required:
// ["sandbox_net"]}`) still accepts sandbox "off" when sandbox_net is omitted, so
// the failing arm's narrowed enum is not the globally accepted set: the message
// must render the branch-level pairing rule, naming sandbox_net, and never an
// allowed-values list that calls "off" invalid.
func TestExplainSchemaError_OneOfAttributionIsArmOrderIndependent(t *testing.T) {
	for name, schemaJSON := range map[string]string{
		"positive-arm-first": issue621SwappedOneOfSchema,
		"not-arm-first":      issue621NotFirstOneOfSchema,
	} {
		t.Run(name, func(t *testing.T) {
			params, args, verr := issue621Validate(t, schemaJSON, issue621DelegateArgs())
			loc := offendingKeywordLocation(verr)
			if !strings.HasPrefix(loc, "/oneOf") {
				t.Fatalf("offendingKeywordLocation = %q, want a location under the root oneOf", loc)
			}
			msg := repair.ExplainSchemaError("delegate", params, args, offendingField(verr), loc)
			if strings.Contains(msg, "is not one of the allowed values") {
				t.Fatalf("ambiguous oneOf arm enum rendered as a global allowed-values list: %q", msg)
			}
			// The top-level sandbox enum starts "off", which the failing branch
			// narrows away; the message must not reproduce that list.
			if strings.Contains(msg, `"off", "read-only"`) {
				t.Fatalf("message lists the top-level sandbox enum instead of the branch's (issue #621): %q", msg)
			}
			if !strings.Contains(msg, `"sandbox" must be one of "read-only", "workspace-write", "restricted"`) {
				t.Fatalf("message must render the branch's narrowed sandbox enum: %q", msg)
			}
			if !strings.Contains(msg, "sandbox_net") {
				t.Fatalf("message must name the constrained pairing field sandbox_net: %q", msg)
			}
		})
	}
}

// follow-up: end to end through the real library, a oneOf
// behind a $ref is attributed to its combinator (jsonschema reports the cause
// as /$ref/oneOf/...), and — because the branches cannot be resolved off the
// top level — the message stays a generic mismatch without claiming the
// supplied `a` was required.
func TestExplainSchemaError_RefWrappedOneOf(t *testing.T) {
	params, args, verr := issue621Validate(t, issue621RefWrappedOneOfSchema, map[string]any{"a": "x"})
	loc := offendingKeywordLocation(verr)
	if !strings.HasPrefix(loc, "/$ref/oneOf") {
		t.Fatalf("offendingKeywordLocation = %q, want the ref-wrapped oneOf location", loc)
	}
	msg := repair.ExplainSchemaError("probe_tool", params, args, offendingField(verr), loc)
	if !strings.Contains(msg, "arguments did not match the schema") {
		t.Fatalf("$ref-wrapped oneOf failure must explain the generic mismatch: %q", msg)
	}
	if strings.Contains(msg, "Required arguments") {
		t.Fatalf("$ref-wrapped oneOf failure claims supplied arguments are required: %q", msg)
	}
	if strings.Contains(msg, "Example:") {
		t.Fatalf("$ref-wrapped oneOf fallback emitted an example satisfying no branch: %q", msg)
	}
}

// end to end through the real library, an arm-internal type
// constraint must name the offending property's defect instead of claiming the
// failure is not about any single argument's type or value while showing a
// requirement the call already satisfied.
func TestExplainSchemaError_ArmInternalTypeNamesTheProperty(t *testing.T) {
	params, args, verr := issue621Validate(t, issue621ArmInternalTypeSchema, map[string]any{"x": "abc"})
	loc := offendingKeywordLocation(verr)
	if !strings.HasPrefix(loc, "/oneOf") {
		t.Fatalf("offendingKeywordLocation = %q, want a location under the root oneOf", loc)
	}
	msg := repair.ExplainSchemaError("probe_tool", params, args, offendingField(verr), loc)
	if strings.Contains(msg, "Branch 0 requires") {
		t.Fatalf("arm-internal type constraint rendered as a branch requirement: %q", msg)
	}
	if !strings.Contains(msg, `argument "x" has the wrong type or value`) {
		t.Fatalf("arm-internal type constraint must name the offending property and its defect: %q", msg)
	}
	// this path is reachable only when the branch prose could
	// not describe the failure, so its example is not checked against the
	// enclosing combinator and must not be suggested.
	if strings.Contains(msg, "Example:") {
		t.Fatalf("branch-attributed fallback emitted an example it cannot check: %q", msg)
	}
}

// end to end through the real library, a `$ref`-wrapped
// root `not` must not append a top-level example — the referenced `not` is what
// was violated, and minimalExample is not checked against it.
func TestExplainSchemaError_RefWrappedNot(t *testing.T) {
	params, args, verr := issue621Validate(t, issue621RefWrappedNotSchema, map[string]any{"a": "x"})
	loc := offendingKeywordLocation(verr)
	if !strings.HasPrefix(loc, "/$ref/not") {
		t.Fatalf("offendingKeywordLocation = %q, want the ref-wrapped not location", loc)
	}
	msg := repair.ExplainSchemaError("probe_tool", params, args, offendingField(verr), loc)
	if !strings.Contains(msg, "arguments did not match the schema") {
		t.Fatalf("$ref-wrapped not failure must explain the generic mismatch: %q", msg)
	}
	if strings.Contains(msg, "Example:") {
		t.Fatalf("$ref-wrapped not fallback emitted an unchecked example: %q", msg)
	}
}

// end to end through the real library, a present property
// whose arm-internal enum sits behind a $ref must stay the generic mismatch —
// falling through would read the TOP-LEVEL enum and print an allowed-values
// list containing the rejected value.
func TestExplainSchemaError_RefWrappedPresentEnum(t *testing.T) {
	params, args, verr := issue621Validate(t, issue621RefWrappedPresentEnumSchema, map[string]any{"x": "off"})
	loc := offendingKeywordLocation(verr)
	if !strings.HasPrefix(loc, "/$ref/oneOf") {
		t.Fatalf("offendingKeywordLocation = %q, want a ref-wrapped oneOf location", loc)
	}
	if got := offendingField(verr); got != "x" {
		t.Fatalf("offendingField = %q, want %q", got, "x")
	}
	msg := repair.ExplainSchemaError("probe_tool", params, args, offendingField(verr), loc)
	if strings.Contains(msg, "is not one of the allowed values") {
		t.Fatalf("$ref-wrapped arm enum rendered a top-level allowed-values list containing the rejected value: %q", msg)
	}
	if !strings.Contains(msg, "arguments did not match the schema") {
		t.Fatalf("$ref-wrapped arm enum must fall back to the generic mismatch: %q", msg)
	}
}

// end to end through the real library, an arm enum the
// branch prose does not render (here nested deeper than the arm's own required
// property) must not be read against the top-level property — whose wider enum
// contains the rejected value. The message must name the property without
// printing an allowed-values list.
func TestExplainSchemaError_NestedArmEnumStaysGeneric(t *testing.T) {
	params, args, verr := issue621Validate(t, issue621NestedArmEnumSchema, map[string]any{"opts": map[string]any{"status": "b"}})
	loc := offendingKeywordLocation(verr)
	if !strings.HasPrefix(loc, "/oneOf") {
		t.Fatalf("offendingKeywordLocation = %q, want a location under the root oneOf", loc)
	}
	msg := repair.ExplainSchemaError("probe_tool", params, args, offendingField(verr), loc)
	// The top-level enum is wider than the arm's and contains the rejected "b";
	// reproducing it would announce a disallowed value. The arm's own narrowed
	// enum (issue #622) excludes "b" and is the list to render.
	if strings.Contains(msg, `"a", "b"`) {
		t.Fatalf("nested arm enum printed the top-level allowed-values list containing the rejected value: %q", msg)
	}
	if strings.Contains(msg, "Branch 0 requires") {
		t.Fatalf("nested arm enum rendered a branch requirement the call already satisfied: %q", msg)
	}
	if !strings.Contains(msg, `"a"`) {
		t.Fatalf("nested arm enum must render the arm's narrowed list: %q", msg)
	}
	if !strings.Contains(msg, "opts.status") {
		t.Fatalf("nested arm enum must name the offending property: %q", msg)
	}
}

// end to end through the real library, a root `not` over a
// multi-name required list must state the conjunction — not/required rejects
// only when every listed property is present.
func TestExplainSchemaError_RootNotMultipleNames(t *testing.T) {
	params, args, verr := issue621Validate(t, issue621RootNotTwoNamesSchema, map[string]any{"a": "x", "b": "y"})
	loc := offendingKeywordLocation(verr)
	if loc != "/not" {
		t.Fatalf("offendingKeywordLocation = %q, want %q", loc, "/not")
	}
	msg := repair.ExplainSchemaError("probe_tool", params, args, offendingField(verr), loc)
	if !strings.Contains(msg, `do not send all of "a", "b" together`) {
		t.Fatalf("multi-name root not must state the conjunction: %q", msg)
	}
}

// end to end through the real library, a root `not` that
// also constrains a value must not be rendered as an unconditional
// prohibition with an example that includes the forbidden property.
func TestExplainSchemaError_MixedRootNot(t *testing.T) {
	params, args, verr := issue621Validate(t, issue621MixedRootNotSchema, map[string]any{"mode": "off"})
	loc := offendingKeywordLocation(verr)
	if loc != "/not" {
		t.Fatalf("offendingKeywordLocation = %q, want %q", loc, "/not")
	}
	msg := repair.ExplainSchemaError("probe_tool", params, args, offendingField(verr), loc)
	if strings.Contains(msg, "do not send") {
		t.Fatalf("mixed root not rendered an unconditional prohibition: %q", msg)
	}
	if strings.Contains(msg, "Example:") {
		t.Fatalf("mixed root not appended an example contradicting its own advice: %q", msg)
	}
}

// end to end through the real library, an arm that carries
// both a `not` and an enum-constrained required property must render both, so
// the failing enum is still named.
func TestExplainSchemaError_ArmNotWithEnum(t *testing.T) {
	params, args, verr := issue621Validate(t, issue621ArmNotWithEnumSchema, map[string]any{"x": "b"})
	loc := offendingKeywordLocation(verr)
	if !strings.HasPrefix(loc, "/oneOf/0/properties/x/enum") {
		t.Fatalf("offendingKeywordLocation = %q, want the arm's x enum", loc)
	}
	msg := repair.ExplainSchemaError("probe_tool", params, args, offendingField(verr), loc)
	if !strings.Contains(msg, `"x" must be one of "a"`) {
		t.Fatalf("arm `not` hid the failing enum: %q", msg)
	}
	if !strings.Contains(msg, `do not send "y"`) {
		t.Fatalf("arm `not` prohibition must still be stated: %q", msg)
	}
}

// end to end through the real library, an arm carrying both
// a `not` and a required property must name the property when it is omitted —
// the prohibition alone leaves the caller with nothing actionable.
func TestExplainSchemaError_ArmNotWithOmittedRequired(t *testing.T) {
	params, args, verr := issue621Validate(t, issue621ArmNotWithEnumSchema, map[string]any{})
	loc := offendingKeywordLocation(verr)
	if loc != "/oneOf/0/required" {
		t.Fatalf("offendingKeywordLocation = %q, want the arm's required list", loc)
	}
	msg := repair.ExplainSchemaError("probe_tool", params, args, offendingField(verr), loc)
	if !strings.Contains(msg, `send all of "x"`) {
		t.Fatalf("arm `not` hid the missing required property: %q", msg)
	}
}

// end to end through the real library, a root `not` whose
// top-level required shape contains every forbidden property must not emit an
// example that violates the rule it states.
func TestExplainSchemaError_RootNotExampleThatViolatesIt(t *testing.T) {
	const schemaJSON = `{
		"type": "object",
		"properties": {"a": {"type": "string"}},
		"required": ["a"],
		"not": {"required": ["a"]}
	}`
	params, args, verr := issue621Validate(t, schemaJSON, map[string]any{"a": "x"})
	loc := offendingKeywordLocation(verr)
	if loc != "/not" {
		t.Fatalf("offendingKeywordLocation = %q, want %q", loc, "/not")
	}
	msg := repair.ExplainSchemaError("probe_tool", params, args, offendingField(verr), loc)
	if !strings.Contains(msg, `do not send "a"`) {
		t.Fatalf("root not must state its prohibition: %q", msg)
	}
	if strings.Contains(msg, "Example:") {
		t.Fatalf("root not emitted an example that violates the rule it states: %q", msg)
	}
}

// end to end through the real library, a limit reached
// inside an arm or behind a $ref must not be read against the top-level
// property — the nested schema's limit is the stricter one.
func TestExplainSchemaError_StricterNestedLimit(t *testing.T) {
	for name, tc := range map[string]struct {
		schemaJSON string
		wantLoc    string
		named      bool
		limit      string
	}{
		// The arm is resolved to its own schema (issue #622), so the arm's
		// stricter limit is reported, never the top-level property's. The $ref
		// cannot be resolved here, so the message is the bare generic mismatch.
		"arm":         {issue621StricterArmLimitSchema, "/oneOf/0/properties/x/maxLength", true, "maxLength (3)"},
		"ref-wrapped": {issue621StricterRefLimitSchema, "/$ref/properties/x/maxLength", false, ""},
	} {
		t.Run(name, func(t *testing.T) {
			params, args, verr := issue621Validate(t, tc.schemaJSON, map[string]any{"x": "abcd"})
			loc := offendingKeywordLocation(verr)
			if loc != tc.wantLoc {
				t.Fatalf("offendingKeywordLocation = %q, want %q", loc, tc.wantLoc)
			}
			if got := offendingField(verr); got != "x" {
				t.Fatalf("offendingField = %q, want %q (the test must exercise a present property)", got, "x")
			}
			msg := repair.ExplainSchemaError("probe_tool", params, args, offendingField(verr), loc)
			if strings.Contains(msg, "maxLength (100)") {
				t.Fatalf("nested stricter limit (%s) was read against the top-level property: %q", loc, msg)
			}
			if tc.limit != "" && !strings.Contains(msg, tc.limit) {
				t.Fatalf("message must report the arm's own limit %q: %q", tc.limit, msg)
			}
			if tc.limit == "" && strings.Contains(msg, "maxLength") {
				t.Fatalf("unresolved $ref failure must not report a limit: %q", msg)
			}
			if tc.named && !strings.Contains(msg, `argument "x"`) {
				t.Fatalf("message must still name the offending property: %q", msg)
			}
			if !tc.named && !strings.Contains(msg, "arguments did not match the schema") {
				t.Fatalf("unresolved $ref failure must be the generic mismatch, got: %q", msg)
			}
		})
	}
}

// end to end through the real library, a root `not` beside a
// sibling oneOf must not emit a top-level example that the sibling can
// invalidate.
func TestExplainSchemaError_RootNotWithSiblingOneOf(t *testing.T) {
	params, args, verr := issue621Validate(t, issue621NotWithSiblingOneOfSchema, map[string]any{"a": "x"})
	loc := offendingKeywordLocation(verr)
	if loc != "/not" {
		t.Fatalf("offendingKeywordLocation = %q, want %q", loc, "/not")
	}
	msg := repair.ExplainSchemaError("probe_tool", params, args, offendingField(verr), loc)
	if !strings.Contains(msg, `do not send "a"`) {
		t.Fatalf("root not must state its prohibition: %q", msg)
	}
	if strings.Contains(msg, "Example:") {
		t.Fatalf("root not emitted an example its sibling oneOf can invalidate: %q", msg)
	}
}

// issue621RefRequiredSchema hides a plain `required` behind a $ref whose name
// the top-level schema also requires, so an unresolved fall-through would
// report the top-level list instead of the referenced one.
const issue621RefRequiredSchema = `{
	"type": "object",
	"properties": {"a": {"type": "string"}, "b": {"type": "string"}},
	"required": ["a"],
	"$ref": "#/$defs/rule",
	"$defs": {"rule": {"required": ["b"]}}
}`

// end to end through the real library, a $ref-wrapped
// failure whose leaf is not a combinator must not fall through to the top-level
// schema and report a supplied field as required.
func TestExplainSchemaError_RefWrappedNonCombinator(t *testing.T) {
	params, args, verr := issue621Validate(t, issue621RefRequiredSchema, map[string]any{"a": "x"})
	loc := offendingKeywordLocation(verr)
	if loc != "/$ref/required" {
		t.Fatalf("offendingKeywordLocation = %q, want %q", loc, "/$ref/required")
	}
	msg := repair.ExplainSchemaError("probe_tool", params, args, offendingField(verr), loc)
	if !strings.Contains(msg, "arguments did not match the schema") {
		t.Fatalf("$ref failure must fall back to the generic mismatch: %q", msg)
	}
	if strings.Contains(msg, "Required arguments") || strings.Contains(msg, "Example:") {
		t.Fatalf("$ref failure emitted top-level required/example guidance: %q", msg)
	}
}

// issue621RefTypedPropertySchema requires a property whose own schema is a
// $ref to an enum the example placeholder would violate.
const issue621RefTypedPropertySchema = `{
	"type": "object",
	"properties": {"mode": {"$ref": "#/$defs/mode"}},
	"required": ["mode"],
	"$defs": {"mode": {"enum": ["on"]}},
	"oneOf": [{"required": ["mode"]}, {"required": ["other"]}]
}`

// end to end through the real library, the example gate must
// see a $ref-typed required property as constrained — the example's placeholder
// is rejected by the referenced enum. The validator surfaces the property
// failure first for this schema, so the gate is pinned at the repair layer in
// TestExplainSchemaError_OneOfExampleOmitsUnmodeledPropertyConstraint; here we
// only assert the real location so the shape stays covered.
func TestExplainSchemaError_RefTypedProperty(t *testing.T) {
	params, args, verr := issue621Validate(t, issue621RefTypedPropertySchema, map[string]any{"mode": "off"})
	if loc := offendingKeywordLocation(verr); !strings.Contains(loc, "$ref") {
		t.Fatalf("offendingKeywordLocation = %q, want a location through the $ref", loc)
	}
	msg := repair.ExplainSchemaError("probe_tool", params, args, offendingField(verr), offendingKeywordLocation(verr))
	if !strings.Contains(msg, "mode") {
		t.Fatalf("message must name the offending property: %q", msg)
	}
}

// end to end through the real library, a bare /$ref location
// (a reference to a false schema) must take the generic mismatch.
func TestExplainSchemaError_BareRefLocation(t *testing.T) {
	const schemaJSON = `{
		"type": "object",
		"properties": {"a": {"type": "string"}},
		"required": ["a"],
		"$ref": "#/$defs/rule",
		"$defs": {"rule": false}
	}`
	params, args, verr := issue621Validate(t, schemaJSON, map[string]any{"a": "x"})
	if loc := offendingKeywordLocation(verr); loc != "/$ref" {
		t.Fatalf("offendingKeywordLocation = %q, want %q", loc, "/$ref")
	}
	msg := repair.ExplainSchemaError("probe_tool", params, args, offendingField(verr), offendingKeywordLocation(verr))
	if !strings.Contains(msg, "arguments did not match the schema") {
		t.Fatalf("bare /$ref failure must fall back to the generic mismatch: %q", msg)
	}
	if strings.Contains(msg, "Required arguments") || strings.Contains(msg, "Example:") {
		t.Fatalf("bare /$ref failure emitted top-level required/example guidance: %q", msg)
	}
}

// issue621DynamicRefSchema reaches a constrained property through $dynamicRef,
// which resolves like $ref.
const issue621DynamicRefSchema = `{
	"type": "object",
	"properties": {"task": {"$dynamicRef": "#task"}},
	"required": ["task"],
	"$defs": {"task": {"$dynamicAnchor": "task", "type": "string", "minLength": 4}}
}`

// end to end through the real library, a constraint reached
// through $dynamicRef must be treated as a reference — not read against the
// top-level property, and with no unverified example.
func TestExplainSchemaError_DynamicRef(t *testing.T) {
	params, args, verr := issue621Validate(t, issue621DynamicRefSchema, map[string]any{"task": "abc"})
	loc := offendingKeywordLocation(verr)
	msg := repair.ExplainSchemaError("probe_tool", params, args, offendingField(verr), loc)
	if strings.Contains(msg, "minLength") {
		t.Fatalf("$dynamicRef failure stated a top-level constraint (%s): %q", loc, msg)
	}
	if strings.Contains(msg, "Example:") {
		t.Fatalf("$dynamicRef failure emitted an unverified example: %q", msg)
	}
}

// exampleFromMessage extracts the `Example: {...}` object a message suggests, so
// a test can check the property that matters — the example satisfies the schema
// — rather than the gate's internal decisions.
func exampleFromMessage(t *testing.T, msg string) (map[string]any, bool) {
	t.Helper()
	i := strings.LastIndex(msg, "Example: ")
	if i < 0 {
		return nil, false
	}
	rest := msg[i+len("Example: "):]
	depth := 0
	inString := false
	escaped := false
	for j, r := range rest {
		switch {
		case escaped:
			escaped = false
		case r == '\\' && inString:
			escaped = true
		case r == '"':
			inString = !inString
		case inString:
		case r == '{':
			depth++
		case r == '}':
			depth--
			if depth == 0 {
				var obj map[string]any
				if err := json.Unmarshal([]byte(rest[:j+1]), &obj); err != nil {
					t.Fatalf("example is not a JSON object: %v (%q)", err, rest[:j+1])
				}
				return obj, true
			}
		}
	}
	return nil, false
}

// the message's example is a suggested retry, so whenever one
// is emitted it must satisfy the schema. Asserting that property directly —
// rather than any gate decision — is what keeps a future gate honest.
func TestExplainSchemaError_EmittedExampleSatisfiesSchema(t *testing.T) {
	cases := []struct {
		name       string
		schemaJSON string
		args       map[string]any
	}{
		{
			"root minProperties with a presence-only oneOf",
			`{
				"type": "object",
				"minProperties": 1,
				"oneOf": [
					{"required": ["a"], "oneOf": [{}, {}]},
					{"not": {"required": ["b"]}}
				]
			}`,
			map[string]any{"a": 1, "b": 1},
		},
		{
			"delegate arm-order-swapped oneOf",
			issue621SwappedOneOfSchema,
			map[string]any{"task": "ping", "sandbox": "off", "sandbox_net": true},
		},
		{
			"nested constrained required property",
			`{
				"type": "object",
				"properties": {
					"opts": {"type": "object", "required": ["status"],
						"properties": {"status": {"type": "string", "enum": ["a"]}}}
				},
				"required": ["opts"],
				"oneOf": [{"required": ["opts"]}, {"required": ["other"]}]
			}`,
			map[string]any{"opts": map[string]any{"status": "b"}},
		},
		{
			"arm-internal failure falling back to the present-field path",
			issue621ArmInternalTypeSchema,
			map[string]any{"x": "abc"},
		},
		{
			"property-level $ref whose referenced enum rejects the placeholder",
			issue621RefTypedPropertySchema,
			map[string]any{"mode": "off"},
		},
		{
			// A declared draft-07 dialect ignores $ref siblings, so an arm's own
			// `required` does not apply and the example must not be certified
			// against it. The registry compiles tool schemas through
			// NewCompiler without pinning a draft, so an MCP/plugin schema that
			// declares draft-07 reaches this path.
			"draft-07 arm whose $ref sibling is ignored",
			`{
				"$schema": "http://json-schema.org/draft-07/schema#",
				"type": "object",
				"properties": {"a": {"type": "string"}, "b": {"type": "boolean"}},
				"required": ["a"],
				"definitions": {"rule": {"properties": {"a": {"const": "..."}}}},
				"oneOf": [
					{"not": {"required": ["b"]}},
					{"$ref": "#/definitions/rule", "required": ["c"]}
				]
			}`,
			map[string]any{"a": "x", "b": true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params, args, verr := issue621Validate(t, tc.schemaJSON, tc.args)
			msg := repair.ExplainSchemaError("probe_tool", params, args, offendingField(verr), offendingKeywordLocation(verr))
			example, ok := exampleFromMessage(t, msg)
			if !ok {
				t.Logf("no object example emitted: %q", msg)
				return
			}
			c := jsonschema.NewCompiler()
			if err := c.AddResource("mem://example.json", strings.NewReader(tc.schemaJSON)); err != nil {
				t.Fatalf("add resource: %v", err)
			}
			schema, err := c.Compile("mem://example.json")
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			if err := schema.Validate(example); err != nil {
				t.Fatalf("emitted example %v does not satisfy the schema: %v (message: %q)", example, err, msg)
			}
		})
	}
}
