package agent_test

import (
	"testing"

	"primeradiant.com/serf/agent"
)

func TestPluginLoadedData_DataMap(t *testing.T) {
	ev := agent.SessionEvent{Kind: agent.EventPluginLoaded, Data: agent.PluginLoadedData{
		Name: "test", Dir: "/tmp", SkillCount: 2, AgentCount: 1, MCPCount: 3,
	}}
	dm := ev.DataMap()
	if dm == nil {
		t.Fatal("DataMap returned nil")
	}
	if dm["name"] != "test" {
		t.Errorf("name = %v, want %q", dm["name"], "test")
	}
	if dm["dir"] != "/tmp" {
		t.Errorf("dir = %v, want %q", dm["dir"], "/tmp")
	}
	if dm["skill_count"] != float64(2) {
		t.Errorf("skill_count = %v, want 2", dm["skill_count"])
	}
	if dm["agent_count"] != float64(1) {
		t.Errorf("agent_count = %v, want 1", dm["agent_count"])
	}
	if dm["mcp_count"] != float64(3) {
		t.Errorf("mcp_count = %v, want 3", dm["mcp_count"])
	}
}

func TestHookStartData_DataMap(t *testing.T) {
	ev := agent.SessionEvent{Kind: agent.EventHookStart, Data: agent.HookStartData{
		Event: "PreToolUse", HookType: "command", Matcher: "Write", PluginName: "my-plugin",
	}}
	dm := ev.DataMap()
	if dm == nil {
		t.Fatal("DataMap returned nil")
	}
	if dm["event"] != "PreToolUse" {
		t.Errorf("event = %v, want %q", dm["event"], "PreToolUse")
	}
	if dm["hook_type"] != "command" {
		t.Errorf("hook_type = %v, want %q", dm["hook_type"], "command")
	}
	if dm["matcher"] != "Write" {
		t.Errorf("matcher = %v, want %q", dm["matcher"], "Write")
	}
	if dm["plugin_name"] != "my-plugin" {
		t.Errorf("plugin_name = %v, want %q", dm["plugin_name"], "my-plugin")
	}
}

func TestHookEndData_DataMap(t *testing.T) {
	ev := agent.SessionEvent{Kind: agent.EventHookEnd, Data: agent.HookEndData{
		Event: "PostToolUse", HookType: "prompt", Matcher: "*",
		PluginName: "test-plugin", ExitCode: 0, DurationMS: 150,
	}}
	dm := ev.DataMap()
	if dm == nil {
		t.Fatal("DataMap returned nil")
	}
	if dm["event"] != "PostToolUse" {
		t.Errorf("event = %v, want %q", dm["event"], "PostToolUse")
	}
	if dm["hook_type"] != "prompt" {
		t.Errorf("hook_type = %v, want %q", dm["hook_type"], "prompt")
	}
	if dm["matcher"] != "*" {
		t.Errorf("matcher = %v, want %q", dm["matcher"], "*")
	}
	if dm["plugin_name"] != "test-plugin" {
		t.Errorf("plugin_name = %v, want %q", dm["plugin_name"], "test-plugin")
	}
	if dm["exit_code"] != float64(0) {
		t.Errorf("exit_code = %v, want 0", dm["exit_code"])
	}
	if dm["duration_ms"] != float64(150) {
		t.Errorf("duration_ms = %v, want 150", dm["duration_ms"])
	}
}

func TestHookEndData_NonZeroExitCode(t *testing.T) {
	ev := agent.SessionEvent{Kind: agent.EventHookEnd, Data: agent.HookEndData{
		Event: "PreToolUse", HookType: "command", Matcher: "Write",
		PluginName: "err-plugin", ExitCode: 2, DurationMS: 50,
	}}
	dm := ev.DataMap()
	if dm["exit_code"] != float64(2) {
		t.Errorf("exit_code = %v, want 2", dm["exit_code"])
	}
}

func TestEventKindConstants(t *testing.T) {
	// Verify the new event kinds have the expected string values.
	if agent.EventHookStart != "HOOK_START" {
		t.Errorf("EventHookStart = %q, want %q", agent.EventHookStart, "HOOK_START")
	}
	if agent.EventHookEnd != "HOOK_END" {
		t.Errorf("EventHookEnd = %q, want %q", agent.EventHookEnd, "HOOK_END")
	}
}
