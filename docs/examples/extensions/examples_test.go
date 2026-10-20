package extensions_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/mcpconfig"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/skill"
)

func TestExampleLoaders(t *testing.T) {
	path, err := filepath.Abs("skills/change-summary/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, diagnostics, err := skill.Parse(data, path)
	if err != nil || len(diagnostics) != 0 {
		t.Fatalf("skill parse: %v, diagnostics: %v", err, diagnostics)
	}
	loaded, diagnostics, err := skill.Load(descriptor)
	if err != nil || len(diagnostics) != 0 || loaded.Digest == "" {
		t.Fatalf("skill load: %v, diagnostics: %v", err, diagnostics)
	}
	if descriptor.Meta.Name != "change-summary" {
		t.Fatalf("skill identity: %q", descriptor.Meta.Name)
	}
	data, err = os.ReadFile("commands/summarize-change.md")
	if err != nil {
		t.Fatal(err)
	}
	command, err := plugin.ParseCommand(data, "summarize-change", "")
	if err != nil {
		t.Fatal(err)
	}
	if command.Name != "summarize-change" || command.PluginName != "" {
		t.Fatalf("command identity: %+v", command)
	}
	p, err := plugin.Load("hello-plugin")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Commands) != 1 || len(p.Agents) != 1 || len(p.Hooks) != 1 {
		t.Fatalf("unexpected component inventory: %+v", p)
	}
	if _, ok := p.Commands["hello-plugin:hello"]; !ok {
		t.Fatal("missing namespaced command")
	}
	if _, ok := p.Agents["hello-plugin:reviewer"]; !ok {
		t.Fatal("missing namespaced role")
	}
	// Run the declared exec-form hook, then inspect its machine output contract.
	var config struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string
				Args    []string
			}
		}
	}
	data, err = os.ReadFile("hello-plugin/hooks/hooks.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	h := config.Hooks["SessionStart"][0].Hooks[0]
	out, err := exec.Command(h.Command, h.Args...).Output()
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatal(err)
	}
	if _, ok := result["systemMessage"]; !ok {
		t.Fatal("hook did not return a systemMessage")
	}
	t.Setenv("EVENER_EXAMPLE_MCP_SERVER", "/example/echo-server")
	configs, err := mcpconfig.LoadFile("mcp.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 1 || configs[0].Name != "example" || configs[0].Command != "/example/echo-server" || configs[0].Type != "stdio" {
		t.Fatalf("unexpected MCP config: %+v", configs)
	}
}
