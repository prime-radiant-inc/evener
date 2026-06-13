package tool

import (
	"strings"
	"testing"
)

// TestSchemaMaxWaitUnification asserts the post-unification invariants for all
// five wait-capable tools (spec §0.1-§0.4, §2):
//   - "background" and "block" are absent (additionalProperties:false rejects them)
//   - "block_timeout_ms" is absent from all five tools
//   - "max_wait_ms" is present in each tool's properties with type "integer"
//   - No minimum/maximum/default on max_wait_ms in any tool
func TestSchemaMaxWaitUnification(t *testing.T) {
	tools := []struct {
		name string
		def  func() map[string]any // returns the Parameters map
	}{
		{"shell", func() map[string]any { return DefShell().Parameters }},
		{"delegate", func() map[string]any { return DefDelegate(nil).Parameters }},
		{"job_send_message", func() map[string]any { return DefJobSendMessage().Parameters }},
		{"job_read_output", func() map[string]any { return DefJobReadOutput().Parameters }},
		{"job_stop", func() map[string]any { return DefJobStop().Parameters }},
	}

	for _, tc := range tools {
		t.Run(tc.name, func(t *testing.T) {
			params := tc.def()

			// additionalProperties:false is required.
			if ap, ok := params["additionalProperties"]; !ok || ap != false {
				t.Errorf("%s: additionalProperties = %v, want false", tc.name, ap)
			}

			props := params["properties"].(map[string]any)

			// "background" and "block" must be gone.
			for _, banned := range []string{"background", "block"} {
				if _, ok := props[banned]; ok {
					t.Errorf("%s: property %q must not exist", tc.name, banned)
				}
			}

			// "block_timeout_ms" must be gone.
			if _, ok := props["block_timeout_ms"]; ok {
				t.Errorf("%s: property %q must not exist", tc.name, "block_timeout_ms")
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
}

func TestDefShellHasJobParams(t *testing.T) {
	props := DefShell().Parameters["properties"].(map[string]any)
	for _, p := range []string{"command", "description", "max_wait_ms", "max_runtime_ms"} {
		if _, ok := props[p]; !ok {
			t.Errorf("DefShell missing param %q", p)
		}
	}
	if _, ok := props["timeout_ms"]; ok {
		t.Errorf("DefShell must not have the old timeout_ms param")
	}
	if _, ok := props["background"]; ok {
		t.Errorf("DefShell must not have the removed background param")
	}
	if _, ok := props["block_timeout_ms"]; ok {
		t.Errorf("DefShell must not have the removed block_timeout_ms param")
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

func TestDefJobSendMessageParams(t *testing.T) {
	def := DefJobSendMessage()
	if def.Name != "job_send_message" {
		t.Fatalf("name = %q, want job_send_message", def.Name)
	}
	props := def.Parameters["properties"].(map[string]any)
	for _, p := range []string{"target", "message", "on_finished", "max_wait_ms"} {
		if _, ok := props[p]; !ok {
			t.Errorf("DefJobSendMessage missing param %q", p)
		}
	}
	if _, ok := props["background"]; ok {
		t.Errorf("DefJobSendMessage must not have the removed background param")
	}
	if _, ok := props["block_timeout_ms"]; ok {
		t.Errorf("DefJobSendMessage must not have the removed block_timeout_ms param")
	}
	req := def.Parameters["required"].([]string)
	if len(req) != 2 || req[0] != "target" || req[1] != "message" {
		t.Errorf("required = %v, want [target message]", req)
	}
	of := props["on_finished"].(map[string]any)
	enum := of["enum"].([]string)
	if len(enum) != 2 || enum[0] != "resume" || enum[1] != "fail" {
		t.Errorf("on_finished enum = %v, want [resume fail]", enum)
	}
}

func TestDefJobSendMessageDescriptionKeepsDirectTargetsSmall(t *testing.T) {
	def := DefJobSendMessage()
	props := def.Parameters["properties"].(map[string]any)
	targetDesc := props["target"].(map[string]any)["description"].(string)
	combined := def.Description + "\n" + targetDesc
	for _, want := range []string{"delegate job_id", "caller"} {
		if !strings.Contains(combined, want) {
			t.Fatalf("job_send_message description = %q, want %q", combined, want)
		}
	}
	if strings.Contains(combined, "watched") {
		t.Fatalf("job_send_message description must not advertise watched: %q", combined)
	}
	if strings.Contains(combined, "main") {
		t.Fatalf("job_send_message description must not mention main: %q", combined)
	}
}

func TestDefJobWatchParamsAndKinds(t *testing.T) {
	def := DefJobWatch([]string{"assistant.message", "job.notification"})
	if def.Name != "job_watch" {
		t.Fatalf("name = %q, want job_watch", def.Name)
	}
	props := def.Parameters["properties"].(map[string]any)
	for _, p := range []string{"target", "output_match", "progress_interval_ms", "events", "every", "send", "clear"} {
		if _, ok := props[p]; !ok {
			t.Errorf("DefJobWatch missing param %q", p)
		}
	}
	req := def.Parameters["required"].([]string)
	if len(req) != 1 || req[0] != "target" {
		t.Errorf("required = %#v, want [target]", req)
	}
	// The available event kinds are interpolated into the description.
	if !strings.Contains(def.Description, "assistant.message") || !strings.Contains(def.Description, "job.notification") {
		t.Errorf("description must enumerate the available event kinds:\n%s", def.Description)
	}
}

func TestDefJobWatchDescriptionIncludesCoalesceContract(t *testing.T) {
	def := DefJobWatch([]string{"assistant.message"})
	if !strings.Contains(def.Description, "coalesce") || !strings.Contains(def.Description, "busy") {
		t.Fatalf("job_watch description must mention coalescing and busy-target behavior:\n%s", def.Description)
	}
	props := def.Parameters["properties"].(map[string]any)
	sendProps := props["send"].(map[string]any)["properties"].(map[string]any)
	toDesc := sendProps["to"].(map[string]any)["description"].(string)
	for _, want := range []string{"caller", "watched", "concrete watched"} {
		if !strings.Contains(toDesc, want) {
			t.Fatalf("send.to description = %q, want %q", toDesc, want)
		}
	}
	if strings.Contains(toDesc, "main") {
		t.Fatalf("send.to description must not mention main: %q", toDesc)
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
