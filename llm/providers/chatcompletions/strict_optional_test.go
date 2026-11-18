package chatcompletions

import (
	"slices"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func requiredNames(t *testing.T, schema map[string]any) []string {
	t.Helper()
	var out []string
	switch list := schema["required"].(type) {
	case []string:
		out = append(out, list...)
	case []any:
		for _, v := range list {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
	case nil:
		return nil
	default:
		t.Fatalf("required has unexpected type %T", schema["required"])
	}
	slices.Sort(out)
	return out
}

// Chat Completions carries the same strict-mode rule as Responses: every
// property of every object must be present and listed in required. A tool with
// a documented-optional argument therefore has no legal strict projection, and
// the adapter must not set strict on it (#6200).
func TestChatStrictToolsKeepOptionalArgumentsOmittable(t *testing.T) {
	t.Parallel()
	req := userReq("hi")
	req.Tools = []llm.ToolDefinition{{
		Name: "read_file",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"file_path":     map[string]any{"type": "string"},
				"offset":        map[string]any{"type": "integer"},
				"limit":         map[string]any{"type": "integer"},
				"vision_prompt": map[string]any{"type": "string"},
			},
			"required": []string{"file_path"},
		},
	}}

	body := build(t, req, resolved(func(c *registry.Caps) { c.StrictTools = new(true) }))
	fn := body["tools"].([]map[string]any)[0]["function"].(map[string]any)
	if _, has := fn["strict"]; has {
		t.Fatalf("strict = %#v, want no strict key: a schema with optional arguments cannot be expressed in strict mode", fn["strict"])
	}
	params := fn["parameters"].(map[string]any)
	if got, want := requiredNames(t, params), []string{"file_path"}; !slices.Equal(got, want) {
		t.Fatalf("projected required = %v, want exactly %v (optional arguments stay omittable)", got, want)
	}
}

// A tool that opts out with Strict=&false stays non-strict on the chat path
// too, so the definition-level opt-outs the codebase already relies on are not
// silently re-enabled by this adapter.
func TestChatStrictToolsHonorPerToolOptOut(t *testing.T) {
	t.Parallel()
	strictFalse := false
	req := userReq("hi")
	req.Tools = []llm.ToolDefinition{{
		Name:       "job_list",
		Strict:     &strictFalse,
		Parameters: map[string]any{"type": "object", "properties": map[string]any{"offset": map[string]any{"type": "integer"}}},
	}}

	body := build(t, req, resolved(func(c *registry.Caps) { c.StrictTools = new(true) }))
	fn := body["tools"].([]map[string]any)[0]["function"].(map[string]any)
	if _, has := fn["strict"]; has {
		t.Fatalf("strict = %#v, want no strict key for an opted-out tool", fn["strict"])
	}
	if params := fn["parameters"].(map[string]any); params["additionalProperties"] != nil {
		t.Fatalf("an opted-out tool must keep its advertised schema: %#v", params)
	}
}

// The other direction: a fully-required schema is strict-expressible, so the
// adapter still turns strict on and still rewrites the schema.
func TestChatStrictToolsStillStrictifyFullyRequiredSchemas(t *testing.T) {
	t.Parallel()
	req := userReq("hi")
	req.Tools = []llm.ToolDefinition{{
		Name: "write_file",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file_path": map[string]any{"type": "string"},
				"content":   map[string]any{"type": "string"},
			},
			"required": []string{"file_path", "content"},
		},
	}}

	body := build(t, req, resolved(func(c *registry.Caps) { c.StrictTools = new(true) }))
	fn := body["tools"].([]map[string]any)[0]["function"].(map[string]any)
	if fn["strict"] != true {
		t.Fatalf("strict = %#v, want true for a fully-required schema", fn["strict"])
	}
	if params := fn["parameters"].(map[string]any); params["additionalProperties"] != false {
		t.Fatalf("additionalProperties = %#v, want false (strictified)", params["additionalProperties"])
	}
}
