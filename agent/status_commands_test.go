package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/appwire"
)

func TestDetailedStatusLoadedCommands(t *testing.T) {
	s := newSession(t)
	s.pluginCommands = map[string]plugin.Command{
		"pkg:z": {Name: "z", PluginName: "pkg", Source: "plugin", Body: "PRIVATE_BODY", File: "/private/command.md"},
		"a":     {Name: "a", Source: "project", Description: "loaded winner", ArgumentHint: "[input]"},
	}
	ds := s.DetailedStatus()
	if len(ds.Commands) != 2 || ds.Commands[0] != (appwire.CommandDescriptor{Name: "a", Source: "project", Description: "loaded winner", ArgumentHint: "[input]"}) || ds.Commands[1].PluginName != "pkg" {
		t.Fatalf("loaded sorted descriptors = %#v", ds.Commands)
	}
	raw, err := json.Marshal(ds.Commands)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "PRIVATE_BODY") || strings.Contains(string(raw), "/private/") {
		t.Fatalf("command source leaked into completion: %s", raw)
	}
	ds.Commands[0].Description = "changed copy"
	if s.DetailedStatus().Commands[0].Description != "loaded winner" {
		t.Fatal("status mutated loaded inventory")
	}
	s.pluginCommands = nil
	if s.DetailedStatus().Commands == nil {
		t.Fatal("loaded empty inventory must be explicit")
	}
}
