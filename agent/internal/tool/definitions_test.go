package tool

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
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
	return slices.Contains(values, want)
}

// TestSchemaWaitKnobs asserts the one-wait-knob-per-tool invariant: delegate
// creation and shell do not wait; the two wait-capable control tools use
// `max_wait_ms`. No tool carries both, and
// `block`/`block_timeout_ms` are gone
// everywhere. (Supersedes the all-five max_wait_ms unification for shell — see
// docs/superpowers/specs/2026-06-13-max-wait-unification.md.)
func TestSchemaWaitKnobs(t *testing.T) {
	maxWaitTools := []struct {
		name string
		def  func() map[string]any // returns the Parameters map
	}{
		{"delegate_send", func() map[string]any { return DefDelegateSend().Parameters }},
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

	for _, tc := range []struct {
		name string
		def  func() map[string]any
	}{
		{"delegate", func() map[string]any { return DefDelegate(nil).Parameters }},
		{"shell", func() map[string]any { return DefShell().Parameters }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := tc.def()
			if ap, ok := params["additionalProperties"]; !ok || ap != false {
				t.Errorf("%s: additionalProperties = %v, want false", tc.name, ap)
			}
			props := params["properties"].(map[string]any)
			for _, banned := range []string{"max_wait_ms", "block", "block_timeout_ms"} {
				if _, ok := props[banned]; ok {
					t.Errorf("%s: property %q must not exist", tc.name, banned)
				}
			}
		})
	}

	// shell's execution mode replaces the old background boolean.
	t.Run("shell_mode", func(t *testing.T) {
		params := DefShell().Parameters
		props := params["properties"].(map[string]any)
		mode, ok := props["mode"].(map[string]any)
		if !ok {
			t.Fatal("shell: missing required property mode")
		}
		if typ, _ := mode["type"].(string); typ != "string" {
			t.Errorf("shell: mode type = %q, want string", typ)
		}
	})
}

func TestDefShellHasJobParams(t *testing.T) {
	props := DefShell().Parameters["properties"].(map[string]any)
	for _, p := range []string{"command", "description", "mode"} {
		if _, ok := props[p]; !ok {
			t.Errorf("DefShell missing param %q", p)
		}
	}
	for _, banned := range []string{"max_runtime_ms", "timeout_ms", "max_wait_ms", "block_timeout_ms"} {
		if _, ok := props[banned]; ok {
			t.Errorf("DefShell must not have the %q param", banned)
		}
	}
	if got := DefShell().Description; got != "Run a shell command and report stdout, stderr, and exit status." {
		t.Fatalf("DefShell description mismatch:\n%q", got)
	}
}

func TestDefShellHasExecutionMode(t *testing.T) {
	props := DefShell().Parameters["properties"].(map[string]any)
	mode, ok := props["mode"].(map[string]any)
	if !ok {
		t.Fatal("DefShell missing mode property")
	}
	if got := mode["type"]; got != "string" {
		t.Fatalf("mode type = %v, want string", got)
	}
	want := []any{"foreground", "background", "detached"}
	if got := mode["enum"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("mode enum = %#v, want %#v", got, want)
	}
	if _, exists := props["background"]; exists {
		t.Fatal("legacy background property is still exposed")
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
	for _, p := range []string{"task", "agent_type", "model", "reasoning_effort", "result_schema"} {
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
	if _, ok := props["max_wait_ms"]; ok {
		t.Errorf("DefDelegate must not expose creation max_wait_ms")
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

	if !strings.Contains(def.Description, "delegate_id") {
		t.Fatalf("delegate schema text must mention delegate_id: %q", def.Description)
	}
	if strings.Contains(def.Description, "job_id") || strings.Contains(def.Description, "started job") {
		t.Fatalf("delegate schema text must not expose an activation job identity: %q", def.Description)
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

// TestDefDelegateHasSandboxParams pins the per-delegate sandbox input surface:
// a `sandbox` string enumerating the four modes and a `sandbox_net` boolean, both
// documenting the no-escalation floor (you may only pick a box at least as
// restrictive as your own) so the model self-selects a legal request.
func TestDefDelegateHasSandboxParams(t *testing.T) {
	props := DefDelegate(nil).Parameters["properties"].(map[string]any)

	sb, ok := props["sandbox"].(map[string]any)
	if !ok {
		t.Fatal("DefDelegate missing sandbox param")
	}
	if typ, _ := sb["type"].(string); typ != "string" {
		t.Errorf("sandbox type = %q, want string", sb["type"])
	}
	enum, ok := sb["enum"].([]string)
	if !ok {
		t.Fatalf("sandbox enum = %T, want []string", sb["enum"])
	}
	// DefDelegate is the portable full-capability surface: every mode plus
	// its +nonet variant, plus bare "nonet" (net-only tightening).
	wantEnum := []string{
		"off",
		"read-only", "read-only+nonet",
		"workspace-write", "workspace-write+nonet",
		"restricted", "restricted+nonet",
		"nonet",
	}
	if !reflect.DeepEqual(enum, wantEnum) {
		t.Errorf("sandbox enum = %v, want %v", enum, wantEnum)
	}
	sbDesc, _ := sb["description"].(string)
	if !strings.Contains(sbDesc, "at least as confining") {
		t.Errorf("sandbox description must explain the no-escalation floor, got %q", sbDesc)
	}
	// The modes must be glossed, not merely enumerated.
	for _, want := range []string{"read-only", "workspace-write", "restricted", "no writes", "working tree", "+nonet"} {
		if !strings.Contains(sbDesc, want) {
			t.Errorf("sandbox description must define the modes (missing %q), got %q", want, sbDesc)
		}
	}

	// The combined-enum surface has no separate sandbox_net property and no
	// oneOf pairing constraint: an invalid combo is unrepresentable.
	if _, hasNet := props["sandbox_net"]; hasNet {
		t.Fatal("DefDelegate must not expose a separate sandbox_net param")
	}
	if _, hasOneOf := DefDelegate(nil).Parameters["oneOf"]; hasOneOf {
		t.Fatal("DefDelegate must not carry a oneOf constraint")
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
	if got := len(props); got != 3 {
		t.Fatalf("delegate_send properties len = %d, want exactly 3 (to, message, max_wait_ms)", got)
	}
	for _, want := range []string{"to", "message", "max_wait_ms"} {
		if _, ok := props[want]; !ok {
			t.Fatalf("delegate_send missing property %q", want)
		}
	}
	if _, ok := props["target"]; ok {
		t.Fatalf("delegate_send must not expose target")
	}
	if _, ok := props["on_finished"]; ok {
		t.Fatalf("delegate_send must not expose on_finished")
	}
	if _, ok := props["on_idle"]; ok {
		t.Fatalf("delegate_send must not expose on_idle")
	}
	if !strings.Contains(def.Description, "started/resumed automatically") {
		t.Fatalf("delegate_send description must explain automatic idle restart: %q", def.Description)
	}
	combined := def.Description + "\n" + props["to"].(map[string]any)["description"].(string)
	for _, banned := range []string{"job_send_message", "watched", "main"} {
		if strings.Contains(combined, banned) {
			t.Fatalf("delegate_send description must not contain %q: %q", banned, combined)
		}
	}
}

func TestDefDelegateSendDescriptionDistinguishesCallerFromFinalReport(t *testing.T) {
	def := DefDelegateSend()
	for _, want := range []string{"controlling caller", "non-terminal", "communicate"} {
		if !strings.Contains(def.Description, want) {
			t.Fatalf("DefDelegateSend description = %q, want %q", def.Description, want)
		}
	}
}

func TestDefTaskListDescriptionStatesInProgressInvariant(t *testing.T) {
	def := DefTaskList(nil)
	want := "Only one task may be in_progress at a time; to start a new one, complete or defer the current one in the same update array."
	if !strings.Contains(def.Description, want) {
		t.Fatalf("DefTaskList description = %q, want to contain %q", def.Description, want)
	}
}

func TestDefTaskListEffortEnumIncludesInherit(t *testing.T) {
	def := DefTaskList([]string{"low", "medium", "high"})
	want := []string{"low", "medium", "high", "inherit"}
	props := def.Parameters["properties"].(map[string]any)
	for _, arrayName := range []string{"add", "update"} {
		arraySchema := props[arrayName].(map[string]any)
		item := arraySchema["items"].(map[string]any)
		schema := item["properties"].(map[string]any)["reasoning_effort"].(map[string]any)
		enum, ok := schema["enum"].([]string)
		if !ok {
			t.Fatalf("%s reasoning_effort enum missing: %#v", arrayName, schema)
		}
		if !reflect.DeepEqual(enum, want) {
			t.Fatalf("%s reasoning_effort enum = %v, want %v", arrayName, enum, want)
		}
	}
}

func TestDefUpdateGoalShape(t *testing.T) {
	def := DefUpdateGoal()
	if def.Name != "update_goal" {
		t.Fatalf("name = %q, want update_goal", def.Name)
	}
	required(t, def, "update_goal", []string{"status"})
	props := def.Parameters["properties"].(map[string]any)
	status, ok := props["status"].(map[string]any)
	if !ok {
		t.Fatalf("update_goal missing status property")
	}
	if status["type"] != "string" {
		t.Fatalf("status type = %v, want string", status["type"])
	}
	enum, ok := status["enum"].([]string)
	if !ok {
		t.Fatalf("status enum = %T, want []string", status["enum"])
	}
	if !slices.Contains(enum, "complete") || !slices.Contains(enum, "blocked") {
		t.Fatalf("status enum = %v, want complete and blocked", enum)
	}
}

func TestDefDelegateSendDescribesContextualCallerRoute(t *testing.T) {
	def := DefDelegateSend()
	props := def.Parameters["properties"].(map[string]any)
	to := props["to"].(map[string]any)["description"].(string)
	if !strings.Contains(def.Description, "caller") || !strings.Contains(to, "caller") {
		t.Fatalf("DefDelegateSend caller route missing: description=%q to=%q", def.Description, to)
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
	for _, def := range []llm.ToolDefinition{DefJobStatus(), DefJobList(), DefReadTranscript()} {
		if def.Strict == nil || *def.Strict {
			t.Fatalf("%s Strict = %v, want false so OpenAI does not require optional inspection fields", def.Name, def.Strict)
		}
	}
}

func TestJobListDescriptionKeepsObserverCommunicateGuidance(t *testing.T) {
	def := DefJobList()
	if !strings.Contains(def.Description, "communicate(end_turn=true)") {
		t.Fatalf("%s description = %q, want observer sidecar communicate report path", def.Name, def.Description)
	}
	if !strings.Contains(def.Description, "audit or diagnosis") {
		t.Fatalf("%s description = %q, want audit/diagnosis guidance", def.Name, def.Description)
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
	for _, want := range []string{"source you can observe", "`dlg_...`", "Delivery is implicit", "communicate(end_turn=true)"} {
		if !strings.Contains(def.Description, want) {
			t.Fatalf("job_watch description = %q, want %q", def.Description, want)
		}
	}
	props := def.Parameters["properties"].(map[string]any)
	sourceDesc := props["source"].(map[string]any)["description"].(string)
	for _, want := range []string{"self", "parent", "job_id", "dlg_..."} {
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

// TestTranscriptToolDefinitions locks the public transcript surface: find discovers
// sessions and read handles both session and job references without exposing the
// retired API-log reader options.
func TestTranscriptToolDefinitions(t *testing.T) {
	find := DefFindSessionTranscripts()
	read := DefReadTranscript()

	if find.Name != "find_session_transcripts" || read.Name != "read_transcript" {
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
	for _, k := range []string{
		"transcript_ref", "format", "range", "expand_turn", "offset_bytes", "output_match", "context_lines",
	} {
		if _, ok := rp[k]; !ok {
			t.Errorf("read missing param %q", k)
		}
	}
	for _, retired := range []string{"source", "attempt_id", "body", "max_bytes"} {
		if _, ok := rp[retired]; ok {
			t.Errorf("read still exposes retired param %q", retired)
		}
	}
	for _, forbidden := range []string{"read_session_transcript", "api_log", "max_bytes"} {
		if strings.Contains(read.Description, forbidden) {
			t.Errorf("read description still exposes retired surface %q: %s", forbidden, read.Description)
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

	expandDescription := rp["expand_turn"].(map[string]any)["description"].(string)
	for _, contract := range []string{"any semantic Turn N", "byte-paged", "transcript_v2_jsonl"} {
		if !strings.Contains(expandDescription, contract) {
			t.Errorf("expand_turn description = %q, want public contract %q", expandDescription, contract)
		}
	}
	if !strings.Contains(read.Description, "fixed 16 KiB") {
		t.Errorf("read description does not explain fixed expansion pages: %q", read.Description)
	}

	outputMatch := rp["output_match"].(map[string]any)
	if outputMatch["type"] != "string" || !strings.Contains(outputMatch["description"].(string), "RE2") {
		t.Errorf("output_match schema = %#v, want RE2 string", outputMatch)
	}
	if outputMatch["maxLength"] != 65_536 || !strings.Contains(outputMatch["description"].(string), "65,536") {
		t.Errorf("output_match schema = %#v, want documented 65,536-character envelope bound", outputMatch)
	}
	contextLines := rp["context_lines"].(map[string]any)
	if contextLines["type"] != "integer" || contextLines["minimum"] != 0 || contextLines["maximum"] != 10 {
		t.Errorf("context_lines schema = %#v, want integer 0..10", contextLines)
	}
	for _, want := range []string{"session ref", "job:", "artifact:", "output_match", "context_lines", "retained_start_bytes", "job_status"} {
		if !strings.Contains(read.Description, want) {
			t.Errorf("read description does not name retained evidence contract %q: %s", want, read.Description)
		}
	}
}

// TestDefManageWorktreeShape asserts the manage_worktree tool definition
// against spec §2's args-by-operation table: a single tool with an
// operation enum and the per-operation optional/required args flattened
// into one schema (mirroring task_list's action pattern).
func TestDefManageWorktreeShape(t *testing.T) {
	def := DefManageWorktree()

	if def.Name != "manage_worktree" {
		t.Fatalf("Name = %q, want manage_worktree", def.Name)
	}
	if def.Parameters["additionalProperties"] != false {
		t.Errorf("additionalProperties = %v, want false", def.Parameters["additionalProperties"])
	}

	required(t, def, "manage_worktree", []string{"operation"})

	props, ok := def.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %T, want map[string]any", def.Parameters["properties"])
	}

	opProp, ok := props["operation"].(map[string]any)
	if !ok {
		t.Fatalf("operation property = %T, want map[string]any", props["operation"])
	}
	opEnum, ok := opProp["enum"].([]string)
	if !ok {
		t.Fatalf("operation enum = %T, want []string", opProp["enum"])
	}
	wantEnum := map[string]bool{"create": true, "list": true, "switch": true, "adopt": true, "exit": true, "remove": true, "prune": true, "dispose": true}
	if len(opEnum) != len(wantEnum) {
		t.Errorf("operation enum = %v, want exactly %v", opEnum, wantEnum)
	}
	for _, v := range opEnum {
		if !wantEnum[v] {
			t.Errorf("unexpected operation enum value %q", v)
		}
	}

	// Per-operation args from spec §2's table, flattened.
	for _, k := range []string{"name", "base_ref", "path", "force", "force_dirty", "delete_branch", "id"} {
		if _, ok := props[k]; !ok {
			t.Errorf("manage_worktree missing param %q", k)
		}
	}

	forceProp, ok := props["force"].(map[string]any)
	if !ok || forceProp["type"] != "boolean" {
		t.Errorf("force property = %v, want boolean type", props["force"])
	}
	deleteBranchProp, ok := props["delete_branch"].(map[string]any)
	if !ok || deleteBranchProp["type"] != "boolean" {
		t.Errorf("delete_branch property = %v, want boolean type", props["delete_branch"])
	}
}

// TestDefManageWorktreeDescriptionCarriesUsagePolicy asserts the description
// carries spec §2's usage-policy paragraph: worktrees are for isolated,
// parallel, or risky work, not ordinary branch creation/switching.
func TestDefManageWorktreeDescriptionCarriesUsagePolicy(t *testing.T) {
	def := DefManageWorktree()
	desc := strings.ToLower(def.Description)

	for _, phrase := range []string{"isolated", "parallel", "risky"} {
		if !strings.Contains(desc, phrase) {
			t.Errorf("description missing usage-policy phrase %q; got: %s", phrase, def.Description)
		}
	}
	if !strings.Contains(desc, "not") || !strings.Contains(desc, "ordinary") {
		t.Errorf("description must say this is not for ordinary branch work; got: %s", def.Description)
	}
	if !strings.Contains(desc, "plain git") && !strings.Contains(desc, "git commands") {
		t.Errorf("description must point to plain git commands for ordinary branch work; got: %s", def.Description)
	}
}

// TestDefAskUserSchema locks the ask_user input schema to spec §4.2: questions
// 1-4 per call, each with optional header/question/options (2-5 of {label,
// detail, recommended?}), multi_select, why, and if_unanswered.
func TestDefAskUserSchema(t *testing.T) {
	def := DefAskUser()
	if def.Name != "ask_user" {
		t.Fatalf("name = %q", def.Name)
	}
	if def.Strict == nil || *def.Strict {
		t.Fatalf("Strict = %v, want false", def.Strict)
	}
	if def.Parameters["additionalProperties"] != false {
		t.Errorf("additionalProperties = %v, want false", def.Parameters["additionalProperties"])
	}
	required(t, def, "ask_user", []string{"questions"})

	params := def.Parameters
	qs := params["properties"].(map[string]any)["questions"].(map[string]any)
	if qs["minItems"] != 1 || qs["maxItems"] != 4 {
		t.Fatalf("questions bounds = %v/%v", qs["minItems"], qs["maxItems"])
	}
	item := qs["items"].(map[string]any)
	props := item["properties"].(map[string]any)
	for _, k := range []string{"header", "question", "options", "multi_select", "why", "if_unanswered"} {
		if _, ok := props[k]; !ok {
			t.Fatalf("missing question property %q", k)
		}
	}
	if props["header"].(map[string]any)["type"] != "string" {
		t.Fatal("header is not a string")
	}
	if _, ok := props["header"].(map[string]any)["maxLength"]; ok {
		t.Fatal("header must not have a hard maxLength")
	}
	opts := props["options"].(map[string]any)
	if opts["minItems"] != 2 || opts["maxItems"] != 5 {
		t.Fatalf("options bounds = %v/%v", opts["minItems"], opts["maxItems"])
	}
	optProps := opts["items"].(map[string]any)["properties"].(map[string]any)
	for _, k := range []string{"label", "detail", "recommended"} {
		if _, ok := optProps[k]; !ok {
			t.Fatalf("missing option property %q", k)
		}
	}
	req := item["required"].([]string)
	want := []string{"question", "options"}
	if !reflect.DeepEqual(req, want) {
		t.Fatalf("required = %v, want %v", req, want)
	}
}

// TestDefGrepContextLinesParam: DefGrep must document a context_lines integer
// parameter (0-10, default 0) so the model can request surrounding lines, per
// the contract-surface rule that a new model-facing param is documented in the
// same commit that adds it.
// TestDefReadFileSliceParamsDocumented pins the offset/limit prose: offset is
// a 1-based start line, limit is a line count, and the default limit is 2000
// (matching LocalExecutionEnvironment.ReadFile's actual defaults).
func TestDefReadFileSliceParamsDocumented(t *testing.T) {
	def := DefReadFile()
	props, ok := def.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("read_file properties = %T, want map[string]any", def.Parameters["properties"])
	}
	offset, ok := props["offset"].(map[string]any)
	if !ok {
		t.Fatalf("read_file missing offset property; got properties: %v", props)
	}
	limit, ok := props["limit"].(map[string]any)
	if !ok {
		t.Fatalf("read_file missing limit property; got properties: %v", props)
	}
	offsetDesc, _ := offset["description"].(string)
	limitDesc, _ := limit["description"].(string)
	if !strings.Contains(offsetDesc, "1-based") {
		t.Errorf("offset description should document it's a 1-based start line, got: %q", offsetDesc)
	}
	if !strings.Contains(limitDesc, "line count") {
		t.Errorf("limit description should document it's a line count, got: %q", limitDesc)
	}
	if !strings.Contains(limitDesc, "2000") {
		t.Errorf("limit description should document the default of 2000, got: %q", limitDesc)
	}
}

func TestDefGrepContextLinesParam(t *testing.T) {
	def := DefGrep()
	props, ok := def.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("grep properties = %T, want map[string]any", def.Parameters["properties"])
	}
	cl, ok := props["context_lines"].(map[string]any)
	if !ok {
		t.Fatalf("grep missing context_lines property; got properties: %v", props)
	}
	if cl["type"] != "integer" {
		t.Errorf("context_lines type = %v, want integer", cl["type"])
	}
	desc, _ := cl["description"].(string)
	// Pin the actual parameter semantics, not bare digit characters. "0" and
	// "10" individually appear almost anywhere; the meaningful contracts are
	// the range form and the explicit default marker.
	if !strings.Contains(desc, "0-10") {
		t.Errorf("context_lines description should document the 0-10 range, got: %q", desc)
	}
	if !strings.Contains(desc, "default 0") {
		t.Errorf("context_lines description should document the default of 0, got: %q", desc)
	}
	if !strings.Contains(desc, "context") {
		t.Errorf("context_lines description should mention context lines, got: %q", desc)
	}
}

// TestDefAskUserDescriptionIsSpecVerbatim pins the description to spec §4.4's
// key contract points: yields the floor, no timeout, batching, the reply
// contract, and the "no Other option" rule.
func TestDefAskUserDescriptionIsSpecVerbatim(t *testing.T) {
	def := DefAskUser()
	for _, want := range []string{
		"Asking yields the floor",
		"no timeout",
		"several `ask_user` calls may share the round",
		"a `communicate` in the same round still delivers its message",
		"Do not add an \"Other\" or free-text option",
		"First try to resolve the question yourself with tools",
	} {
		if !strings.Contains(def.Description, want) {
			t.Fatalf("description missing %q; got: %s", want, def.Description)
		}
	}
}

func TestDefDelegateWithSandboxIncludesVerifiedModelChoices(t *testing.T) {
	def := DefDelegateWithSandbox(nil, DelegateSandboxSchema{
		Available:        true,
		Modes:            []string{"off"},
		ModelDescription: "Verified at startup (snapshot v1): openai/gpt-5, vertex/gemini-2.5.",
	})
	props := def.Parameters["properties"].(map[string]any)
	model := props["model"].(map[string]any)
	if got := model["description"].(string); !strings.Contains(got, "openai/gpt-5") {
		t.Fatalf("model description = %q", got)
	}
}

func TestDefDelegateWithoutSandboxIncludesVerifiedModelChoices(t *testing.T) {
	def := DefDelegateWithSandbox(nil, DelegateSandboxSchema{
		ModelDescription: "Verified at startup (snapshot v1): openai/gpt-5.",
	})
	props := def.Parameters["properties"].(map[string]any)
	model := props["model"].(map[string]any)
	if got := model["description"].(string); !strings.Contains(got, "openai/gpt-5") {
		t.Fatalf("model description = %q", got)
	}
}

func TestDefModelListIsBoundedReadOnlyContract(t *testing.T) {
	def := DefModelList()
	if def.Name != "model_list" || def.Parameters["additionalProperties"] != false {
		t.Fatalf("definition = %#v", def)
	}
}

// TestDefTaskList_PresenceBased pins the combined-tool schema: no action
// property, add/update arrays optional, update items require only id, no
// top-level required list (a bare call is a view), and Strict explicitly
// false (strict-mode normalization would force-requires nested update
// fields, reintroducing forced status/depends_on values).
func TestDefTaskList_PresenceBased(t *testing.T) {
	def := DefTaskList([]string{"low", "high"})
	params := def.Parameters
	props := params["properties"].(map[string]any)
	if _, has := props["action"]; has {
		t.Fatal("schema must not have an action property")
	}
	if def.Strict == nil || *def.Strict {
		t.Fatal("DefTaskList must set Strict: false explicitly")
	}
	add, has := props["add"]
	if !has {
		t.Fatal("schema must have an add property")
	}
	addItems := add.(map[string]any)["items"].(map[string]any)
	addReq, ok := addItems["required"].([]string)
	if !ok || len(addReq) != 3 || addReq[0] != "type" || addReq[1] != "description" || addReq[2] != "prompt" {
		t.Fatalf("add item required = %v, want [type description prompt]", addItems["required"])
	}
	update, has := props["update"]
	if !has {
		t.Fatal("schema must have an update property")
	}
	updateItems := update.(map[string]any)["items"].(map[string]any)
	updateReq, ok := updateItems["required"].([]string)
	if !ok || len(updateReq) != 1 || updateReq[0] != "id" {
		t.Fatalf("update item required = %v, want [id]", updateItems["required"])
	}
	if top, has := params["required"]; has {
		t.Fatalf("schema must not force-require add/update at top level: %v", top)
	}
}

func TestDefDelegateWaitSchema(t *testing.T) {
	def := DefDelegateWait()
	if def.Name != "delegate_wait" {
		t.Fatalf("name = %q", def.Name)
	}
	props := def.Parameters["properties"].(map[string]any)
	targets, ok := props["targets"].(map[string]any)
	if !ok || targets["type"] != "array" {
		t.Fatalf("targets = %v, want an array of delegate ids", props["targets"])
	}
	wait, ok := props["max_wait_ms"].(map[string]any)
	if !ok || wait["type"] != "integer" {
		t.Fatalf("max_wait_ms = %v, want integer", props["max_wait_ms"])
	}
	if req, _ := def.Parameters["required"].([]string); len(req) != 0 {
		t.Errorf("required = %v, want nothing required: the budget defaults to minutes", req)
	}
	if desc, _ := def.Description, ""; desc == "" {
		t.Error("delegate_wait needs a description")
	}
}
