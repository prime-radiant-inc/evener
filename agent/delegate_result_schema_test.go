package agent

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/llm"
)

// issue6298Schema is the result_schema quoted in evener issue #6298: a valid
// draft 2020-12 schema whose rows property is an array of objects, including
// required, enum and integer properties inside items.
var issue6298Schema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"counts":    map[string]any{"type": "object"},
		"notes":     map[string]any{"type": "string"},
		"rows":      map[string]any{"type": "array", "items": map[string]any{"type": "object", "required": []any{"number", "verdict", "evidence"}, "properties": map[string]any{"canonical": map[string]any{"type": "integer"}, "evidence": map[string]any{"type": "string"}, "number": map[string]any{"type": "integer"}, "verdict": map[string]any{"enum": []any{"FIXED-ON-MAIN", "DUPLICATE", "OBSOLETE", "HELD-TOOL", "DESIGN", "FEATURE-DEFERRED", "ACTIONABLE", "AMBIGUOUS"}, "type": "string"}}}},
		"surprises": map[string]any{"type": "string"},
	},
}

// launchSchemaDelegate drives root through a real delegate tool call whose
// result_schema is schema, and returns the requests the root and its child
// each made at the LLM boundary. expectChild selects the synchronization: a
// valid schema launches a child whose first model request races the root's
// final request, so it is waited for before the snapshot; an invalid schema
// must launch no child, and waiting on one would hang the tripwire.
func launchSchemaDelegate(t *testing.T, schema map[string]any, expectChild bool) (rootRequests, childRequests []llm.Request) {
	t.Helper()
	adapter := newMessagingAdapter("SCHEMA-CHILD")
	adapter.script("root",
		func(llm.Request) llm.Response {
			return delegateToolResponseWithResultSchema("create-schema", "SCHEMA-CHILD: say whether the build passes.", schema)
		},
		func(llm.Request) llm.Response { return agenttest.FinalResponse("root done") },
	)
	adapter.script("SCHEMA-CHILD", func(llm.Request) llm.Response { return agenttest.FinalResponse("pass") })
	root := newMessagingRoot(t, adapter, nil)
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	if _, err := root.ProcessInput(tripwireCtx(t), "start", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if expectChild {
		adapter.waitForRequests(t, "SCHEMA-CHILD", 1)
	}
	return adapter.waitForRequests(t, "root", 2), adapter.requestsFor("SCHEMA-CHILD")
}

// delegateCallResult returns whether the root's follow-up request carries an
// error tool result for the delegate tool call.
func delegateCallResult(t *testing.T, reqs []llm.Request) (isError bool, found bool) {
	t.Helper()
	for _, req := range reqs[1:] {
		for _, msg := range req.Messages {
			for _, part := range msg.Content {
				if part.Kind == llm.ContentToolResult && part.ToolResult != nil && part.ToolResult.ToolCallID == "create-schema" {
					return part.ToolResult.IsError, true
				}
			}
		}
	}
	return false, false
}

// TestDelegateLaunchRejectsInvalidResultSchema is red-first for issue #6298.
// A delegate launched with a result_schema that is not a valid JSON Schema
// has that schema spliced verbatim into its communicate tool's output
// parameter; every provider that validates tool parameters (lunaroute's
// draft-2020-12 check among them) then rejects the child's first model call
// and the delegate dies before doing any work. The delegate tool call must
// reject the invalid schema instead, so no child session is ever created.
func TestDelegateLaunchRejectsInvalidResultSchema(t *testing.T) {
	t.Parallel()
	for name, schema := range map[string]map[string]any{
		// The exact rejected shape from the lunaroute 400: a properties value
		// that is a bare type string where a schema belongs.
		"property value is a bare type string": {
			"type":       "object",
			"properties": map[string]any{"rows": "array"},
		},
		// Draft-07 tuple items: an array where a single schema belongs.
		"items is a tuple array": {
			"type": "object",
			"properties": map[string]any{
				"rows": map[string]any{"type": "array", "items": []any{map[string]any{"type": "integer"}}},
			},
		},
		// required must be an array of strings, never a bare string.
		"required is a bare string": {
			"type":       "object",
			"required":   "rows",
			"properties": map[string]any{"rows": map[string]any{"type": "array"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rootRequests, childRequests := launchSchemaDelegate(t, schema, false)
			isError, found := delegateCallResult(t, rootRequests)
			if !found {
				t.Fatal("the root's follow-up request carries no tool result for the delegate call")
			}
			if !isError {
				t.Fatal("delegate launch with an invalid result_schema succeeded; the child would carry the invalid tool schema into its first model call")
			}
			if len(childRequests) != 0 {
				t.Fatalf("a child session ran %d model call(s) with the invalid spliced tool schema", len(childRequests))
			}
		})
	}
}

// TestDelegateResultSchemaSplicePreservesSchemaOnValidTool pins the
// result_schema contract for valid schemas (issue #6298): the schema a caller
// binds is spliced into the child's communicate tool as the output parameter
// verbatim, output is required, and the resulting tool parameters stay valid
// under the draft 2020-12 meta-schema — the check a provider that validates
// tool parameters applies to every tool schema it receives.
func TestDelegateResultSchemaSplicePreservesSchemaOnValidTool(t *testing.T) {
	t.Parallel()
	_, childRequests := launchSchemaDelegate(t, issue6298Schema, true)
	if len(childRequests) == 0 {
		t.Fatal("the schema delegate made no model call")
	}
	var communicate *llm.ToolDefinition
	for i := range childRequests[0].Tools {
		if childRequests[0].Tools[i].Name == "communicate" {
			communicate = &childRequests[0].Tools[i]
		}
	}
	if communicate == nil {
		t.Fatal("the child's first request offers no communicate tool")
	}
	if err := llm.ValidateSchemaDocument(communicate.Parameters); err != nil {
		t.Fatalf("the spliced communicate tool fails a draft 2020-12 meta-schema check (a provider that validates tool parameters would reject the request): %v", err)
	}
	props, _ := communicate.Parameters["properties"].(map[string]any)
	output, _ := props["output"].(map[string]any)
	want, err := json.Marshal(issue6298Schema)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("the spliced output parameter drifted from the caller's result_schema:\n got %s\nwant %s", got, want)
	}
	if req := toStringSlice(communicate.Parameters["required"]); slices.Contains(req, "output") {
		t.Fatalf("communicate parameters required = %v, want output optional", req)
	}
}
