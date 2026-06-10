package agent_test

import (
	"encoding/json"
	"testing"

	"primeradiant.com/serf/agent/events"
)

// payloadJSONMap marshals an event payload to JSON and decodes it back into a
// map[string]any, mirroring how the payload rides the wire. It lets these
// tests assert the json-tag and numeric-coercion contract of each payload
// struct directly.
func payloadJSONMap(t *testing.T, data events.EventData) map[string]any {
	t.Helper()
	b, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	return m
}

func TestPluginLoadedData_JSONShape(t *testing.T) {
	data := events.PluginLoadedData{
		Name: "test", Dir: "/tmp", SkillCount: 2, AgentCount: 1, MCPCount: 3,
	}
	dm := payloadJSONMap(t, data)
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

func TestHookStartData_JSONShape(t *testing.T) {
	data := events.HookStartData{
		Event: "PreToolUse", HookType: "command", Matcher: "Write", PluginName: "my-plugin",
	}
	dm := payloadJSONMap(t, data)
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

func TestHookEndData_JSONShape(t *testing.T) {
	data := events.HookEndData{
		Event: "PostToolUse", HookType: "prompt", Matcher: "*",
		PluginName: "test-plugin", ExitCode: 0, DurationMS: 150,
	}
	dm := payloadJSONMap(t, data)
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
	data := events.HookEndData{
		Event: "PreToolUse", HookType: "command", Matcher: "Write",
		PluginName: "err-plugin", ExitCode: 2, DurationMS: 50,
	}
	dm := payloadJSONMap(t, data)
	if dm["exit_code"] != float64(2) {
		t.Errorf("exit_code = %v, want 2", dm["exit_code"])
	}
}

func TestEventKindConstants(t *testing.T) {
	// Verify the new event kinds have the expected string values.
	if events.EventJobStarted != "JOB_STARTED" {
		t.Errorf("EventJobStarted = %q, want %q", events.EventJobStarted, "JOB_STARTED")
	}
	if events.EventJobFinished != "JOB_FINISHED" {
		t.Errorf("EventJobFinished = %q, want %q", events.EventJobFinished, "JOB_FINISHED")
	}
	if events.EventHookStart != "HOOK_START" {
		t.Errorf("EventHookStart = %q, want %q", events.EventHookStart, "HOOK_START")
	}
	if events.EventHookEnd != "HOOK_END" {
		t.Errorf("EventHookEnd = %q, want %q", events.EventHookEnd, "HOOK_END")
	}
}
