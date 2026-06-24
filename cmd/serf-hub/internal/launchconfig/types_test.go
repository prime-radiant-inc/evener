package launchconfig

import (
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestLayerTOMLRoundTrip(t *testing.T) {
	input := `
schema = 1
model = "openai/gpt-5"
fast_cheap_model = "openai/gpt-5-mini"
agent = "default"
reasoning_effort = "medium"
context_strategy = "compact"
openai_responses_continuation = "auto"
max_rounds = 200
max_subagent_depth = 1
no_project_prompts = false
app_replay_size = 4096
skills_dirs = ["/a", "/b"]
plugin_dirs = ["/p"]
mcp_configs = ["/c"]
system_prompt_append = ["/s"]

[[mcps]]
name = "github"
command = "gh-mcp"
args = ["--token-from-env", "GITHUB_TOKEN"]

[env]
FOO = "bar"
`
	var got Layer
	if _, err := toml.Decode(input, &got); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.Schema != 1 {
		t.Errorf("Schema = %d, want 1", got.Schema)
	}
	if got.Model != "openai/gpt-5" {
		t.Errorf("Model = %q, want openai/gpt-5", got.Model)
	}
	if got.FastCheapModel != "openai/gpt-5-mini" {
		t.Errorf("FastCheapModel = %q, want openai/gpt-5-mini", got.FastCheapModel)
	}
	if got.MaxRounds == nil || *got.MaxRounds != 200 {
		t.Errorf("MaxRounds = %v, want 200", got.MaxRounds)
	}
	if got.OpenAIResponsesContinuation != "auto" {
		t.Errorf("OpenAIResponsesContinuation = %q, want auto", got.OpenAIResponsesContinuation)
	}
	if got.NoProjectPrompts == nil || *got.NoProjectPrompts != false {
		t.Errorf("NoProjectPrompts = %v, want false set", got.NoProjectPrompts)
	}
	if len(got.SkillsDirs) != 2 || got.SkillsDirs[0] != "/a" {
		t.Errorf("SkillsDirs = %v, want [/a /b]", got.SkillsDirs)
	}
	if len(got.MCPs) != 1 || got.MCPs[0].Name != "github" {
		t.Errorf("MCPs = %v, want one github entry", got.MCPs)
	}
	if got.Env["FOO"] != "bar" {
		t.Errorf("Env[FOO] = %q, want bar", got.Env["FOO"])
	}
}

func TestLayerOmitEmptyOnEncode(t *testing.T) {
	l := Layer{Model: "openai/gpt-5"}
	var buf strings.Builder
	if err := toml.NewEncoder(&buf).Encode(l); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "max_rounds") {
		t.Errorf("encoded output should omit max_rounds when nil:\n%s", out)
	}
	if !strings.Contains(out, `model = "openai/gpt-5"`) {
		t.Errorf("encoded output missing model:\n%s", out)
	}
}
