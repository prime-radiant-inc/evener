package tool

import (
	"fmt"
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
)

func required(t *testing.T, def llm.ToolDefinition, name string, want []string) {
	t.Helper()
	raw, ok := def.Parameters["required"].([]string)
	if !ok {
		t.Fatalf("%s required = %T, want []string", name, def.Parameters["required"])
	}
	props, ok := def.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("%s properties = %T, want map[string]any", name, def.Parameters["properties"])
	}
	for _, param := range want {
		if !containsString(raw, param) {
			t.Fatalf("%s required = %v, want %q", name, raw, param)
		}
		if _, ok := props[param]; !ok {
			t.Fatalf("%s missing required property %q", name, param)
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// TestSchemaWaitKnobs asserts the one-wait-knob-per-tool invariant: shell's wait
// knob is a `background` boolean; the other four wait-capable tools use
// `max_wait_ms`. No tool carries both, and `block`/`block_timeout_ms` are gone
// everywhere. (Supersedes the all-five max_wait_ms unification for shell — see
// docs/superpowers/specs/2026-06-13-max-wait-unification.md.)
func TestSchemaWaitKnobs(t *testing.T) {
	maxWaitTools := []struct {
		name string
		def  func() map[string]any // returns the Parameters map
	}{
		{"delegate", func() map[string]any { return DefDelegate(nil).Parameters }},
		{"delegate_send", func() map[string]any { return DefDelegateSend().Parameters }},
		{"job_read_output", func() map[string]any { return DefJobReadOutput().Parameters }},
		{"job_stop", func() map[string]any { return DefJobStop().Parameters }},
	}

	for _, tc := range maxWaitTools {
		t.Run(tc.name, func(t *testing.T) {
			params := tc.def()

			// additionalProperties:false is required.
			if ap, ok := params["additionalProperties"]; !ok || ap != false {
				t.Errorf("%s: additionalProperties = %v, want false", tc.name, ap)
			}

			props := params["properties"].(map[string]any)

			// background/block/block_timeout_ms must all be gone.
			for _, banned := range []string{"background", "block", "block_timeout_ms"} {
				if _, ok := props[banned]; ok {
					t.Errorf("%s: property %q must not exist", tc.name, banned)
				}
			}

			// "max_wait_ms" must be present with type integer.
			mw, ok := props["max_wait_ms"]
			if !ok {
				t.Errorf("%s: missing required property max_wait_ms", tc.name)
				return
			}
			mwSchema, ok := mw.(map[string]any)
			if !ok {
				t.Errorf("%s: max_wait_ms is not an object", tc.name)
				return
			}
			if typ, _ := mwSchema["type"].(string); typ != "integer" {
				t.Errorf("%s: max_wait_ms type = %q, want integer", tc.name, typ)
			}

			// No minimum, maximum, or default keywords on max_wait_ms.
			for _, banned := range []string{"minimum", "maximum", "default"} {
				if _, ok := mwSchema[banned]; ok {
					t.Errorf("%s: max_wait_ms must not have keyword %q", tc.name, banned)
				}
			}
		})
	}

	// shell is the exception: its single wait knob is `background` (bool); it must
	// NOT carry max_wait_ms/block/block_timeout_ms.
	t.Run("shell", func(t *testing.T) {
		params := DefShell().Parameters
		if ap, ok := params["additionalProperties"]; !ok || ap != false {
			t.Errorf("shell: additionalProperties = %v, want false", ap)
		}
		props := params["properties"].(map[string]any)
		for _, banned := range []string{"max_wait_ms", "block", "block_timeout_ms"} {
			if _, ok := props[banned]; ok {
				t.Errorf("shell: property %q must not exist", banned)
			}
		}
		bg, ok := props["background"].(map[string]any)
		if !ok {
			t.Fatalf("shell: missing required property background")
		}
		if typ, _ := bg["type"].(string); typ != "boolean" {
			t.Errorf("shell: background type = %q, want boolean", typ)
		}
	})
}

func TestDefShellHasJobParams(t *testing.T) {
	props := DefShell().Parameters["properties"].(map[string]any)
	for _, p := range []string{"command", "description", "background", "max_runtime_ms"} {
		if _, ok := props[p]; !ok {
			t.Errorf("DefShell missing param %q", p)
		}
	}
	for _, banned := range []string{"timeout_ms", "max_wait_ms", "block_timeout_ms"} {
		if _, ok := props[banned]; ok {
			t.Errorf("DefShell must not have the %q param", banned)
		}
	}
}

func TestDefDelegateParamsAndEnum(t *testing.T) {
	agentTypes := []string{"explorer", "implementer"}
	def := DefDelegate(agentTypes)
	if def.Name != "delegate" {
		t.Fatalf("name = %q, want delegate", def.Name)
	}
	if def.Strict == nil || *def.Strict {
		t.Fatalf("Strict = %v, want false", def.Strict)
	}
	props := def.Parameters["properties"].(map[string]any)
	for _, p := range []string{"task", "agent_type", "model", "reasoning_effort", "max_wait_ms", "result_schema"} {
		if _, ok := props[p]; !ok {
			t.Errorf("DefDelegate missing param %q", p)
		}
	}
	if _, ok := props["background"]; ok {
		t.Errorf("DefDelegate must not have the removed background param")
	}
	if _, ok := props["block_timeout_ms"]; ok {
		t.Errorf("DefDelegate must not have the removed block_timeout_ms param")
	}
	req := def.Parameters["required"].([]string)
	if len(req) != 1 || req[0] != "task" {
		t.Errorf("required = %v, want [task]", req)
	}
	at := props["agent_type"].(map[string]any)
	enum := at["enum"].([]string)
	if len(enum) != 2 || enum[0] != "explorer" || enum[1] != "implementer" {
		t.Errorf("agent_type enum = %v, want [explorer implementer]", enum)
	}

	agentTypes[0] = "mutated"
	if enum[0] != "explorer" {
		t.Errorf("agent_type enum was not copied: %v", enum)
	}

	effortEnum := props["reasoning_effort"].(map[string]any)["enum"].([]string)
	if len(effortEnum) != 3 || effortEnum[0] != "low" || effortEnum[1] != "medium" || effortEnum[2] != "high" {
		t.Errorf("reasoning_effort enum = %v, want [low medium high]", effortEnum)
	}

	maxWaitDesc := props["max_wait_ms"].(map[string]any)["description"].(string)
	for _, text := range []string{def.Description, maxWaitDesc} {
		if !strings.Contains(text, "delegate_id") {
			t.Fatalf("delegate schema text must mention delegate_id: %q", text)
		}
		if !strings.Contains(text, "job_id") {
			t.Fatalf("delegate schema text must still mention concrete job_id: %q", text)
		}
	}
	if !strings.Contains(def.Description, "delegate_send(to=<delegate_id>)") {
		t.Fatalf("delegate description must show delegate_send follow-up target:\n%s", def.Description)
	}
}

// TestDefDelegateHasDelegationAllowance pins spec §1/§8: delegate exposes a
// delegation_allowance integer property (the grant knob) with the strict-zero
// schema shape the whole job-control surface follows — type integer, no
// minimum/maximum/default keywords (absent/0 = no grant).
func TestDefDelegateHasDelegationAllowance(t *testing.T) {
	props := DefDelegate(nil).Parameters["properties"].(map[string]any)
	da, ok := props["delegation_allowance"]
	if !ok {
		t.Fatalf("DefDelegate missing param delegation_allowance")
	}
	daSchema, ok := da.(map[string]any)
	if !ok {
		t.Fatalf("delegation_allowance is not an object: %T", da)
	}
	if typ, _ := daSchema["type"].(string); typ != "integer" {
		t.Errorf("delegation_allowance type = %q, want integer", daSchema["type"])
	}
	for _, banned := range []string{"minimum", "maximum", "default"} {
		if _, ok := daSchema[banned]; ok {
			t.Errorf("delegation_allowance must not have keyword %q (strict-zero rule)", banned)
		}
	}
	desc, _ := daSchema["description"].(string)
	if strings.TrimSpace(desc) == "" {
		t.Errorf("delegation_allowance must document the grant rule in its description")
	}
}

func TestDefDelegateHasWatchParent(t *testing.T) {
	def := DefDelegate([]string{"subagent"})
	props := def.Parameters["properties"].(map[string]any)
	watchParent, ok := props["watch_parent"].(map[string]any)
	if !ok {
		t.Fatal("DefDelegate missing watch_parent")
	}
	if got, _ := watchParent["type"].(string); got != "boolean" {
		t.Fatalf("watch_parent type = %q, want boolean", got)
	}
	for _, want := range []string{"watch_parent=true", "job_watch(source=\"parent\")", "communicate(end_turn=true)"} {
		if !strings.Contains(def.Description, want) {
			t.Fatalf("delegate description = %q, want %q", def.Description, want)
		}
	}
}

func TestDefDelegateNoEnumWhenNoTypes(t *testing.T) {
	def := DefDelegate(nil)
	props := def.Parameters["properties"].(map[string]any)
	at := props["agent_type"].(map[string]any)
	if _, ok := at["enum"]; ok {
		t.Errorf("agent_type must have no enum when no types are available")
	}
}

func TestDefDelegateResultSchemaDescriptionIncludesResumeFailureShape(t *testing.T) {
	props := DefDelegate(nil).Parameters["properties"].(map[string]any)
	desc := props["result_schema"].(map[string]any)["description"].(string)
	for _, want := range []string{"resumed", "structured_result", "structured_result_reason"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("result_schema description = %q, want %q", desc, want)
		}
	}
}

func TestDefDelegateSendShape(t *testing.T) {
	def := DefDelegateSend()
	if def.Name != "delegate_send" {
		t.Fatalf("name = %q, want delegate_send", def.Name)
	}
	required(t, def, "delegate_send", []string{"to", "message"})
	props := def.Parameters["properties"].(map[string]any)
	if _, ok := props["target"]; ok {
		t.Fatalf("delegate_send must not expose target")
	}
	if _, ok := props["on_finished"]; ok {
		t.Fatalf("delegate_send must not expose on_finished")
	}
	if _, ok := props["on_idle"]; !ok {
		t.Fatalf("delegate_send missing on_idle")
	}
	combined := def.Description + "\n" + props["to"].(map[string]any)["description"].(string)
	for _, banned := range []string{"job_send_message", "watched", "main"} {
		if strings.Contains(combined, banned) {
			t.Fatalf("delegate_send description must not contain %q: %q", banned, combined)
		}
	}
}

func TestDefDelegateSendNoCallerAlias(t *testing.T) {
	def := DefDelegateSend()
	descriptions := []string{def.Description}
	collectSchemaDescriptions(def.Parameters, &descriptions)
	for _, desc := range descriptions {
		if strings.Contains(desc, "caller") {
			t.Fatalf("DefDelegateSend description must describe child delegate messaging only: %q", desc)
		}
	}
}

func collectSchemaDescriptions(value any, descriptions *[]string) {
	switch v := value.(type) {
	case map[string]any:
		if desc, ok := v["description"]; ok {
			*descriptions = append(*descriptions, fmt.Sprint(desc))
		}
		for _, child := range v {
			collectSchemaDescriptions(child, descriptions)
		}
	case []any:
		for _, child := range v {
			collectSchemaDescriptions(child, descriptions)
		}
	case []map[string]any:
		for _, child := range v {
			collectSchemaDescriptions(child, descriptions)
		}
	case []string:
		for _, child := range v {
			collectSchemaDescriptions(child, descriptions)
		}
	}
}

func TestDefJobWatchParamsAndKinds(t *testing.T) {
	def := DefJobWatch([]string{"communicate", "job.notification"})
	if def.Name != "job_watch" {
		t.Fatalf("name = %q, want job_watch", def.Name)
	}
	if def.Strict == nil || *def.Strict {
		t.Fatalf("Strict = %v, want false because job_watch has conditional optional arguments", def.Strict)
	}
	props := def.Parameters["properties"].(map[string]any)
	for _, p := range []string{"operation", "watch_id", "source", "output_match", "progress_interval_ms", "events", "event_filter", "every"} {
		if _, ok := props[p]; !ok {
			t.Errorf("DefJobWatch missing param %q", p)
		}
	}
	req := def.Parameters["required"].([]string)
	if len(req) != 1 || req[0] != "operation" {
		t.Errorf("required = %#v, want [operation]", req)
	}
	// The available event kinds are interpolated into the description.
	if !strings.Contains(def.Description, "communicate") || !strings.Contains(def.Description, "job.notification") {
		t.Errorf("description must enumerate the available event kinds:\n%s", def.Description)
	}
}

func TestDefJobWatchUsesSourceAndOmitsSend(t *testing.T) {
	def := DefJobWatch([]string{"assistant.tool", "communicate", "job.notification"})
	props := def.Parameters["properties"].(map[string]any)
	if _, ok := props["source"]; !ok {
		t.Fatal("DefJobWatch missing source")
	}
	if _, ok := props["target"]; ok {
		t.Fatal("DefJobWatch must not expose legacy target")
	}
	if _, ok := props["send"]; ok {
		t.Fatal("DefJobWatch must not expose public send")
	}
	if strings.Contains(def.Description, "send.to") || strings.Contains(def.Description, "target=") {
		t.Fatalf("DefJobWatch description leaks legacy routing shape: %q", def.Description)
	}
}

func TestOptionalJobInspectionToolsAreNonStrict(t *testing.T) {
	for _, def := range []llm.ToolDefinition{DefJobReadOutput(), DefJobList()} {
		if def.Strict == nil || *def.Strict {
			t.Fatalf("%s Strict = %v, want false so OpenAI does not require optional inspection fields", def.Name, def.Strict)
		}
	}
}

func TestDefJobWatchRequiresOperationAndWatchIDForClear(t *testing.T) {
	def := DefJobWatch([]string{"communicate"})
	props := def.Parameters["properties"].(map[string]any)
	if _, ok := props["operation"]; !ok {
		t.Fatalf("DefJobWatch missing operation")
	}
	if _, ok := props["watch_id"]; !ok {
		t.Fatalf("DefJobWatch missing watch_id")
	}
	if _, ok := props["clear"]; ok {
		t.Fatalf("DefJobWatch must not expose clear")
	}
	if strings.Contains(def.Description, "`*`") || strings.Contains(def.Description, "watched") {
		t.Fatalf("DefJobWatch description must not expose target wildcard or watched alias: %q", def.Description)
	}
}

func TestDefJobWatchDescriptionIncludesImplicitDeliveryContract(t *testing.T) {
	def := DefJobWatch([]string{"communicate"})
	for _, want := range []string{"source you can observe", "Delivery is implicit", "communicate(end_turn=true)"} {
		if !strings.Contains(def.Description, want) {
			t.Fatalf("job_watch description = %q, want %q", def.Description, want)
		}
	}
	props := def.Parameters["properties"].(map[string]any)
	sourceDesc := props["source"].(map[string]any)["description"].(string)
	for _, want := range []string{"self", "parent", "job_id"} {
		if !strings.Contains(sourceDesc, want) {
			t.Fatalf("source description = %q, want %q", sourceDesc, want)
		}
	}
	for _, banned := range []string{"send", "target"} {
		if _, ok := props[banned]; ok {
			t.Fatalf("DefJobWatch must not expose %q", banned)
		}
	}
}

// TestTranscriptToolDefinitions locks the two-tool surface: correct names, strict
// opt-out (so the model omits unused args), find takes no session selector, read takes
// transcript_ref + the format/range/expand_turn knobs.
func TestTranscriptToolDefinitions(t *testing.T) {
	find := DefFindSessionTranscripts()
	read := DefReadSessionTranscript()

	if find.Name != "find_session_transcripts" || read.Name != "read_session_transcript" {
		t.Fatalf("names: %q %q", find.Name, read.Name)
	}
	if find.Strict == nil || *find.Strict || read.Strict == nil || *read.Strict {
		t.Errorf("both transcript tools must set Strict=&false")
	}

	fp := find.Parameters["properties"].(map[string]any)
	if _, hasRef := fp["transcript_ref"]; hasRef {
		t.Errorf("find_session_transcripts must not take transcript_ref (it returns refs)")
	}
	for _, k := range []string{"query", "children_of", "scope", "limit"} {
		if _, ok := fp[k]; !ok {
			t.Errorf("find missing param %q", k)
		}
	}

	rp := read.Parameters["properties"].(map[string]any)
	for _, k := range []string{"transcript_ref", "format", "range", "expand_turn"} {
		if _, ok := rp[k]; !ok {
			t.Errorf("read missing param %q", k)
		}
	}
	// format enum is exactly outline|markdown|jsonl.
	formatEnum := rp["format"].(map[string]any)["enum"].([]string)
	want := map[string]bool{"outline": true, "markdown": true, "jsonl": true}
	if len(formatEnum) != 3 {
		t.Errorf("format enum = %v, want outline|markdown|jsonl", formatEnum)
	}
	for _, f := range formatEnum {
		if !want[f] {
			t.Errorf("unexpected format value %q", f)
		}
	}
}
