package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/responses"
	"primeradiant.com/evener/llm/registry"
)

var managerComposedSchemaNames = []string{"job_list", "job_status", "read_transcript", "model_list", "job_watch", "delegate_send", "delegate", "job_stop", "session_control"}

// Capture the actual agent request, then use the production Sol route's shaping,
// Responses body builder and JSON encoding. No auth store or network is used.
func managerProviderSchemas(t *testing.T) (*Session, map[string]map[string]any) {
	t.Helper()
	adapter := newMessagingAdapter()
	adapter.script("root", func(llm.Request) llm.Response { return agenttest.FinalResponse("captured") })
	root := newMessagingRoot(t, adapter, func(cfg *SessionConfig) { cfg.Manager = true })
	root.SetHubClient(&managerStateForUnit{})
	if _, err := root.ProcessInput(t.Context(), "capture Manager tool definitions", nil); err != nil {
		t.Fatal(err)
	}
	req := adapter.waitForRequests(t, "root", 1)[0]
	r, err := registry.Load(registry.WithNoUserLayer(), registry.WithoutCache(), registry.WithOffline(true), registry.WithStateRoot(t.TempDir()), registry.WithEnv(func(string) (string, bool) { return "", false }))
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Resolve("openai-codex/gpt-6.1-sol")
	if err != nil {
		t.Fatal(err)
	}
	if res.Protocol != registry.ProtocolOpenAIResponses || !registry.BoolValue(res.Caps.StrictTools) || !registry.BoolValue(res.Caps.ResponsesLite) {
		t.Fatalf("Sol route changed: protocol=%s capabilities=%+v", res.Protocol, res.Caps)
	}
	req = llm.ShapeRequest(req, res)
	req.Model = res.ModelID
	body, err := (&responses.Protocol{}).BuildBody(req, res)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Input []struct {
			Type  string `json:"type"`
			Tools []struct {
				Name       string         `json:"name"`
				Parameters map[string]any `json:"parameters"`
				Strict     *bool          `json:"strict"`
			} `json:"tools"`
		} `json:"input"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&wire); err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]any{}
	for _, item := range wire.Input {
		if item.Type != "additional_tools" {
			continue
		}
		for _, definition := range item.Tools {
			if slices.Contains(managerComposedSchemaNames, definition.Name) {
				if out[definition.Name] != nil {
					t.Fatalf("duplicate Manager tool %s", definition.Name)
				}
				if definition.Strict == nil || *definition.Strict {
					t.Errorf("serialized %s must explicitly preserve its non-strict argument schema", definition.Name)
				} else {
					t.Logf("serialized %s strict:%v", definition.Name, *definition.Strict)
				}
				for _, advertised := range req.Tools {
					if advertised.Name != definition.Name {
						continue
					}
					before, err := json.Marshal(advertised.Parameters)
					if err != nil {
						t.Fatal(err)
					}
					after, err := json.Marshal(definition.Parameters)
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(before, after) {
						t.Errorf("serialized %s changed the advertised argument constraints", definition.Name)
					}
				}
				out[definition.Name] = definition.Parameters
			}
		}
	}
	for _, name := range managerComposedSchemaNames {
		if out[name] == nil || root.reg.Get(name) == nil || root.reg.Get(name).Schema == nil {
			t.Fatalf("missing registered or serialized Manager tool %s", name)
		}
	}
	return root, out
}

func TestManagerProviderSchemaPreservesNumericBounds(t *testing.T) {
	t.Parallel()
	root, wire := managerProviderSchemas(t)
	for _, name := range []string{"delegate_send", "job_status"} {
		raw, err := json.Marshal(wire[name])
		if err != nil {
			t.Fatal(err)
		}
		serialized, err := jsonschema.CompileString("https://manager.test/numeric/"+name+".json", string(raw))
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			revision string
			valid    bool
		}{
			{"-1", false},
			{"0", true},
			{"18446744073709551615", true},
			{"18446744073709551616", false},
		} {
			t.Run(name+"/"+tc.revision, func(t *testing.T) {
				pair := map[string]any{"expected_ask_revision": json.Number(tc.revision), "expected_ask_boot_generation": "boot-sentinel"}
				var args map[string]any
				if name == "delegate_send" {
					args = pair
					args["to"], args["input_mode"], args["message"] = "local:worker", "queue", "answer-sentinel"
					args["client_mutation_id"], args["expected_instance_id"] = "mutation-sentinel", "instance-sentinel"
				} else {
					pair["ref"] = "local:worker"
					args = map[string]any{"hub": pair}
				}
				args["intent"] = "numeric sentinel"
				for boundary, schema := range map[string]*jsonschema.Schema{"registered": root.reg.Get(name).Schema, "serialized": serialized} {
					if err := schema.Validate(args); (err == nil) != tc.valid {
						t.Errorf("%s schema valid=%v, want %v: %v", boundary, err == nil, tc.valid, err)
					}
				}
			})
		}
	}
}

func TestManagerProviderSchemasHaveExplicitTypes(t *testing.T) {
	t.Parallel()
	root, wire := managerProviderSchemas(t)
	for _, name := range managerComposedSchemaNames {
		t.Run(name, func(t *testing.T) {
			// Check both sides: strictification must not hide a bad source node.
			registered, err := json.Marshal(root.reg.Get(name).Definition.Parameters)
			if err != nil {
				t.Fatal(err)
			}
			var source map[string]any
			if err := json.Unmarshal(registered, &source); err != nil {
				t.Fatal(err)
			}
			checkManagerSchemaTypes(t, source, "registered")
			checkManagerSchemaTypes(t, wire[name], "responses.input.additional_tools.parameters")
		})
	}
}

// This is the specific explicit-type contract exposed by the live rejection,
// not an implementation or claim of OpenAI's full supported-schema validator.
// Follow schema keywords only, never enum values or other instance annotations.
func checkManagerSchemaTypes(t *testing.T, schema map[string]any, path string) {
	t.Helper()
	if schema["type"] == nil {
		t.Errorf("%s: schema node has no explicit type", path)
	}
	for _, keyword := range []string{"properties", "$defs", "patternProperties"} {
		if children, ok := schema[keyword].(map[string]any); ok {
			for name, child := range children {
				checkManagerSchemaTypes(t, child.(map[string]any), path+"."+keyword+"."+name)
			}
		}
	}
	for _, keyword := range []string{"items", "not", "additionalProperties"} {
		if child, ok := schema[keyword].(map[string]any); ok {
			checkManagerSchemaTypes(t, child, path+"."+keyword)
		}
	}
	for _, keyword := range []string{"anyOf", "oneOf", "allOf"} {
		if children, ok := schema[keyword].([]any); ok {
			for i, child := range children {
				checkManagerSchemaTypes(t, child.(map[string]any), fmt.Sprintf("%s.%s[%d]", path, keyword, i))
			}
		}
	}
}
