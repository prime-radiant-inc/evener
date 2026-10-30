package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

// TestCommunicate_ProfileOverridesPreservedByResultToolName catches adopting
// the default definition instead of the profile's schema when the tool is renamed.
func TestCommunicate_ProfileOverridesPreservedByResultToolName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		replaceOutput bool
		decisions     []string
		fields        map[string]string
		required      []string
		output        string
		invalidOutput []string
	}{
		{
			name:          "no overrides",
			fields:        map[string]string{"message": "string", "data": "object", "artifacts": "array"},
			required:      []string{"message", "data", "artifacts"},
			output:        `{"message":"structured-sentinel","data":{},"artifacts":[]}`,
			invalidOutput: []string{`{}`},
		},
		{
			name:          "output replacement",
			replaceOutput: true,
			fields:        map[string]string{"plan": "string"},
			required:      []string{"plan"},
			output:        `{"plan":"plan-sentinel"}`,
			invalidOutput: []string{`{}`, `{"plan":42}`, `{"plan":"p","data":{}}`},
		},
		{
			name:          "allowed decisions",
			decisions:     []string{"approved", "rejected"},
			fields:        map[string]string{"message": "string", "data": "object", "artifacts": "array", "decision": "string"},
			required:      []string{"message", "data", "artifacts", "decision"},
			output:        `{"message":"","data":{},"artifacts":[],"decision":"approved"}`,
			invalidOutput: []string{`{"message":"","data":{},"artifacts":[]}`, `{"message":"","data":{},"artifacts":[],"decision":"unknown"}`},
		},
		{
			name:          "replacement and decisions",
			replaceOutput: true,
			decisions:     []string{"approved", "rejected"},
			fields:        map[string]string{"plan": "string", "decision": "string"},
			required:      []string{"plan", "decision"},
			output:        `{"plan":"plan-sentinel","decision":"approved"}`,
			invalidOutput: []string{`{"plan":"p"}`, `{"decision":"approved"}`, `{"plan":"p","decision":"unknown"}`},
		},
	}
	for _, name := range []string{"communicate", "respond"} {
		for _, tc := range cases {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				p := NewOpenAIProfile("gpt-5.2")
				if tc.replaceOutput {
					p = WithCommunicateOutputSchema(p, map[string]any{
						"type":                 "object",
						"additionalProperties": false,
						"properties":           map[string]any{"plan": map[string]any{"type": "string"}},
						"required":             []string{"plan"},
					})
				}
				p = WithAllowedDecisions(p, tc.decisions)
				before := mustJSON(t, p.ToolDefinitions())
				var output map[string]any
				if err := json.Unmarshal([]byte(tc.output), &output); err != nil {
					t.Fatal(err)
				}
				args := map[string]any{"message": "visible-sentinel", "end_turn": true, "output": output}
				rawArgs := mustMarshalJSON(t, args)
				f := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
					func(llm.Request) llm.Response {
						return agenttest.ToolCallResponse(llm.ToolCallData{ID: "result-schema", Name: name, Arguments: rawArgs, Type: "function"})
					},
				}}
				sess := newSession(t, withAdapter(f), withProfile(p), withConfig(SessionConfig{
					ResultToolName: name,
					AgentsDocPath:  filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
					testOnly:       testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
				}))
				registered := sess.reg.Get(name)
				if registered == nil || registered.Definition.Name != name {
					t.Fatalf("result tool %q not registered under its configured name", name)
				}
				assertResultToolOutputSchema(t, registered.Definition, tc.fields, tc.required, tc.decisions)
				if err := registered.Schema.Validate(args); err != nil {
					t.Errorf("valid output rejected: %v", err)
				}
				if err := registered.Schema.Validate(map[string]any{"message": "m", "end_turn": true}); err == nil {
					t.Error("missing output accepted")
				}
				for _, raw := range tc.invalidOutput {
					var invalid any
					if err := json.Unmarshal([]byte(raw), &invalid); err != nil {
						t.Fatal(err)
					}
					if err := registered.Schema.Validate(map[string]any{"message": "m", "end_turn": true, "output": invalid}); err == nil {
						t.Errorf("invalid output accepted: %s", raw)
					}
				}
				// An interactive root adds end_reason after adopting the profile.
				props := registered.Definition.Parameters["properties"].(map[string]any)
				if _, ok := props["end_reason"]; !ok {
					t.Error("end_reason overlay lost")
				}
				after := mustJSON(t, p.ToolDefinitions())
				if before != after {
					t.Error("session registration mutated the canonical profile")
				}
				if t.Failed() {
					return
				}
				if _, err := sess.ProcessInput(context.Background(), "input-sentinel", nil); err != nil {
					t.Fatalf("ProcessInput: %v", err)
				}
				if !sess.Communicated() || !reflect.DeepEqual(sess.CommunicateStructured(), output) {
					t.Fatalf("terminal structured output = %#v, want %#v", sess.CommunicateStructured(), output)
				}
				requests := f.Requests()
				if len(requests) != 1 {
					t.Fatalf("model requests = %d, want one accepted terminal result", len(requests))
				}
				var advertised *llm.ToolDefinition
				for _, def := range requests[0].Tools {
					if def.Name == name {
						if advertised != nil {
							t.Fatalf("result tool %q advertised twice", name)
						}
						advertised = &def
					}
				}
				if advertised == nil {
					t.Fatalf("result tool %q not advertised", name)
				}
				assertResultToolOutputSchema(t, *advertised, tc.fields, tc.required, tc.decisions)
			})
		}
	}
}

func assertResultToolOutputSchema(t *testing.T, def llm.ToolDefinition, fields map[string]string, required, decisions []string) {
	t.Helper()
	if !slices.Contains(toStringSlice(def.Parameters["required"]), "output") {
		t.Error("output is not required")
	}
	props, _ := def.Parameters["properties"].(map[string]any)
	output, _ := props["output"].(map[string]any)
	if output["type"] != "object" || output["additionalProperties"] != false {
		t.Errorf("output object constraints changed: %v", output)
	}
	outProps, _ := output["properties"].(map[string]any)
	if len(outProps) != len(fields) {
		t.Errorf("output properties = %v, want fields %v", outProps, fields)
	}
	for field, typ := range fields {
		prop, _ := outProps[field].(map[string]any)
		if prop["type"] != typ {
			t.Errorf("output.%s type = %v, want %s", field, prop["type"], typ)
		}
	}
	gotRequired := slices.Clone(toStringSlice(output["required"]))
	wantRequired := slices.Clone(required)
	slices.Sort(gotRequired)
	slices.Sort(wantRequired)
	if !slices.Equal(gotRequired, wantRequired) {
		t.Errorf("output.required = %v, want %v", gotRequired, wantRequired)
	}
	decision, _ := outProps["decision"].(map[string]any)
	if got := toStringSlice(decision["enum"]); !slices.Equal(got, decisions) {
		t.Errorf("decision enum = %v, want %v", got, decisions)
	}
}

func TestCommunicate_ResultToolNameWithoutProfileDefinition(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"communicate", "respond"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			reg := tool.NewRegistry()
			if err := registerCommunicateTool(reg, &toolDeps{resultToolName: func() string { return name }}); err != nil {
				t.Fatal(err)
			}
			registered := reg.Get(name)
			if registered == nil || registered.Definition.Name != name {
				t.Fatalf("fallback result tool %q not registered", name)
			}
			assertResultToolOutputSchema(t, registered.Definition,
				map[string]string{"message": "string", "data": "object", "artifacts": "array"},
				[]string{"message", "data", "artifacts"}, nil)
		})
	}
}
