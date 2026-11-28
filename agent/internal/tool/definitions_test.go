package tool

import (
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/modelavailability"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
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

// descriptionMentions asserts desc carries n at a word boundary, so a longer
// number containing the same digits (1000 vs 100) cannot satisfy the pin.
func descriptionMentions(desc string, n int) bool {
	return regexp.MustCompile(`\b` + strconv.Itoa(n) + `\b`).MatchString(desc)
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

			// No minimum, maximum, or default keywords on max_wait_ms (Manager hub calls reject it).
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
		{"delegate", func() map[string]any { return DefDelegate().Parameters }},
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
	// The shell runtime cap is hidden from models, so the description must not
	// name it.
	desc := DefShell().Description
	for _, capWord := range []string{"max_runtime", "runtime cap", "timeout"} {
		if strings.Contains(desc, capWord) {
			t.Errorf("DefShell description names the runtime cap (%q):\n%q", capWord, desc)
		}
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
	if got := mode["default"]; got != "foreground" {
		t.Fatalf("mode default = %v, want foreground", got)
	}
	if _, exists := props["background"]; exists {
		t.Fatal("legacy background property is still exposed")
	}

	noDetachDef := DefShellWithDetach(false)
	noDetachProps := noDetachDef.Parameters["properties"].(map[string]any)
	noDetachMode, ok := noDetachProps["mode"].(map[string]any)
	if !ok {
		t.Fatal("DefShellWithDetach(false) missing mode property")
	}
	wantNoDetach := []any{"foreground", "background"}
	if got := noDetachMode["enum"]; !reflect.DeepEqual(got, wantNoDetach) {
		t.Fatalf("DefShellWithDetach(false) mode enum = %#v, want %#v", got, wantNoDetach)
	}
	if got := noDetachMode["default"]; got != "foreground" {
		t.Fatalf("DefShellWithDetach(false) mode default = %v, want foreground", got)
	}
	noDetachDesc, _ := noDetachMode["description"].(string)
	if strings.TrimSpace(noDetachDesc) == "" {
		t.Fatal("DefShellWithDetach(false) mode description is empty")
	}
	if strings.Contains(noDetachDef.Description, "detached") || strings.Contains(noDetachDesc, "detached") {
		t.Fatalf("DefShellWithDetach(false) must omit unavailable detached mode from prose: desc=%q mode=%q", noDetachDef.Description, noDetachDesc)
	}
}

func TestDefDelegateParamsAndEnum(t *testing.T) {
	agentTypes := []string{"explorer", "implementer"}
	def := DefDelegateWithSandbox(agentTypes, DelegateSandboxSchema{
		Available: true,
		Modes:     []string{"off", "read-only", "workspace-write", "restricted"},
	})
	if def.Name != "delegate" {
		t.Fatalf("name = %q, want delegate", def.Name)
	}
	if def.Strict == nil || *def.Strict {
		t.Fatalf("Strict = %v, want false", def.Strict)
	}
	props := def.Parameters["properties"].(map[string]any)
	for _, p := range []string{"prompt", "task_list", "agent_type", "model", "reasoning_effort", "result_schema"} {
		if _, ok := props[p]; !ok {
			t.Errorf("DefDelegate missing param %q", p)
		}
	}
	if _, ok := props["task"]; ok {
		t.Errorf("DefDelegate must not have the renamed task param")
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
	if len(req) != 1 || req[0] != "prompt" {
		t.Errorf("required = %v, want [prompt]", req)
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
	wantEffortEnum := []string{"minimal", "low", "medium", "high", "xhigh", "max", "none"}
	if !reflect.DeepEqual(effortEnum, wantEffortEnum) {
		t.Errorf("reasoning_effort enum = %v, want %v", effortEnum, wantEffortEnum)
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
	props := DefDelegate().Parameters["properties"].(map[string]any)
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
	def := DefDelegateWithSandbox([]string{"subagent"}, DelegateSandboxSchema{
		Available: true,
		Modes:     []string{"off", "read-only", "workspace-write", "restricted"},
	})
	props := def.Parameters["properties"].(map[string]any)
	watchParent, ok := props["watch_parent"].(map[string]any)
	if !ok {
		t.Fatal("DefDelegate missing watch_parent")
	}
	if got, _ := watchParent["type"].(string); got != "boolean" {
		t.Fatalf("watch_parent type = %q, want boolean", got)
	}
	if desc, _ := watchParent["description"].(string); strings.TrimSpace(desc) == "" {
		t.Fatal("watch_parent description is empty")
	}
	if !strings.Contains(def.Description, "watch_parent=true") {
		t.Fatalf("delegate description = %q, want %q", def.Description, "watch_parent=true")
	}
	for _, leaked := range []string{"send_and_end_turn", "log_quietly_and_end_turn"} {
		if strings.Contains(def.Description, leaked) {
			t.Fatalf("delegate description must not teach observer reporting (%q): %q", leaked, def.Description)
		}
	}

	noSandbox := DefDelegateWithSandbox(nil, DelegateSandboxSchema{Available: false})
	if _, has := noSandbox.Parameters["properties"].(map[string]any)["sandbox"]; has {
		t.Fatal("delegate schema must omit sandbox property when unavailable")
	}
	if strings.Contains(strings.ToLower(noSandbox.Description), "sandbox") {
		t.Fatalf("delegate description must not announce unavailable sandboxing: %q", noSandbox.Description)
	}
}

// TestDefDelegateHasSandboxParams pins the per-delegate sandbox input surface:
// a `sandbox` string enumerating the four modes and a `sandbox_net` boolean, both
// documenting the no-escalation floor (you may only pick a box at least as
// restrictive as your own) so the model self-selects a legal request.
func TestDefDelegateHasSandboxParams(t *testing.T) {
	props := DefDelegate().Parameters["properties"].(map[string]any)

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
	if _, hasOneOf := DefDelegate().Parameters["oneOf"]; hasOneOf {
		t.Fatal("DefDelegate must not carry a oneOf constraint")
	}
}

func TestDefDelegateNoEnumWhenNoTypes(t *testing.T) {
	def := DefDelegate()
	props := def.Parameters["properties"].(map[string]any)
	at := props["agent_type"].(map[string]any)
	if _, ok := at["enum"]; ok {
		t.Errorf("agent_type must have no enum when no types are available")
	}
}

func TestDefDelegateResultSchemaDescriptionIncludesResumeFailureShape(t *testing.T) {
	props := DefDelegate().Parameters["properties"].(map[string]any)
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
	if !strings.Contains(def.Description, "dlg_...") {
		t.Fatalf("delegate_send description must name dlg_... target format: %q", def.Description)
	}
	renamed := DefDelegateSendNamed("submit_report")
	if !strings.Contains(renamed.Description, "submit_report") || strings.Contains(renamed.Description, DefaultResultToolName) {
		t.Fatalf("DefDelegateSendNamed did not propagate result tool name: %q", renamed.Description)
	}
	combined := def.Description + "\n" + props["to"].(map[string]any)["description"].(string)
	for _, banned := range []string{"job_send_message", "watched", "main"} {
		if strings.Contains(combined, banned) {
			t.Fatalf("delegate_send description must not contain %q: %q", banned, combined)
		}
	}
}

func TestDefTaskListDescriptionStatesInProgressInvariant(t *testing.T) {
	def := DefTaskList(nil)
	want := "One `in_progress` task at a time; complete or defer the current one in the same `update` to switch."
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

// The delegate tool must advertise every effort level its handler accepts.
// A schema narrower than the handler rejected a spawn at "max" at argument
// validation even though createDelegate validates against the full vocabulary.
func TestDefDelegateEffortEnumMatchesHandlerVocabulary(t *testing.T) {
	def := DefDelegate()
	props := def.Parameters["properties"].(map[string]any)
	want := llm.AcceptedReasoningEfforts()

	enum := props["reasoning_effort"].(map[string]any)["enum"].([]string)
	if !reflect.DeepEqual(enum, want) {
		t.Fatalf("reasoning_effort enum = %v, want %v", enum, want)
	}

	item := props["task_list"].(map[string]any)["items"].(map[string]any)
	taskEnum := item["properties"].(map[string]any)["reasoning_effort"].(map[string]any)["enum"].([]string)
	// A seeded task item also offers the "inherit" sentinel, exactly as the
	// session task_list tool does.
	wantTaskEnum := append(slices.Clone(want), "inherit")
	if !reflect.DeepEqual(taskEnum, wantTaskEnum) {
		t.Fatalf("task_list reasoning_effort enum = %v, want %v", taskEnum, wantTaskEnum)
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

func TestDefJobListAdvertisesCommandOutcomeStatuses(t *testing.T) {
	def := DefJobList()
	props, ok := def.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("job_list properties = %T, want map[string]any", def.Parameters["properties"])
	}
	status, ok := props["status"].(map[string]any)
	if !ok {
		t.Fatalf("job_list missing status property")
	}
	items, ok := status["items"].(map[string]any)
	if !ok {
		t.Fatalf("job_list status items = %T, want map[string]any", status["items"])
	}
	enum, ok := items["enum"].([]any)
	if !ok {
		t.Fatalf("job_list status enum = %T, want []any", items["enum"])
	}
	got := map[string]bool{}
	for _, value := range enum {
		if s, ok := value.(string); ok {
			got[s] = true
		}
	}
	// The jobs tool's filter (jobStatusArrayArg) accepts both statuses; the
	// declared schema must advertise them or a caller filtering on the new
	// outcomes is rejected before the handler ever runs.
	if !got["command_exited_nonzero"] || !got["command_killed"] {
		t.Fatalf("job_list status enum = %v, want command_exited_nonzero and command_killed", enum)
	}
	want := map[string]bool{}
	for _, name := range appwire.JobListStatusFilterNames() {
		want[name] = true
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("job_list status enum = %v, want the declared filter set %v", enum, appwire.JobListStatusFilterNames())
	}
	if !strings.Contains(def.Description, "command_exited_nonzero") || !strings.Contains(def.Description, "command_killed") {
		t.Fatalf("job_list description must name the command-outcome statuses: %q", def.Description)
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
	for _, p := range []string{"operation", "watch_id", "source", "output_match", "progress_interval_ms", "events", "event_filter", "every", "after_seconds", "repeat_seconds", "note"} {
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

func TestDefJobWatchOptionalTriggerFieldsAreNullable(t *testing.T) {
	props := DefJobWatch([]string{"communicate"}).Parameters["properties"].(map[string]any)
	for _, name := range []string{"output_match", "events", "event_filter"} {
		t.Run(name, func(t *testing.T) {
			typeValues, ok := props[name].(map[string]any)["type"].([]string)
			if !ok {
				t.Fatalf("%s type = %#v, want nullable type array", name, props[name].(map[string]any)["type"])
			}
			if !slices.Contains(typeValues, "null") {
				t.Fatalf("%s type = %#v, want null", name, typeValues)
			}
		})
	}
}

func TestDefJobWatch_TimerProperties(t *testing.T) {
	def := DefJobWatch(nil)
	props := def.Parameters["properties"].(map[string]any)
	for _, p := range []string{"after_seconds", "repeat_seconds", "note"} {
		prop, ok := props[p].(map[string]any)
		if !ok {
			t.Fatalf("missing property %q", p)
		}
		types, _ := prop["type"].([]string)
		if len(types) != 2 || types[1] != "null" {
			t.Errorf("%s type = %v, want nullable", p, prop["type"])
		}
	}
	source := props["source"].(map[string]any)
	if types, _ := source["type"].([]string); len(types) != 2 || types[1] != "null" {
		t.Errorf("source must be nullable now that timers default it: %v", source["type"])
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

// TestDefCommunicateSchemaNextEnumSurface pins the unified communicate schema:
// message (required), next (optional enum defaulting to send_and_end_turn), and
// output (optional), while end_turn and end_reason are gone. Every calling
// convention the runtime accepts (reply, progress update, quiet wait/ack) must
// pass JSON-schema validation, while {} and unknown/wrongly typed arguments
// argument must not.
func TestDefCommunicateSchemaNextEnumSurface(t *testing.T) {
	for _, tc := range []struct {
		name string
		def  llm.ToolDefinition
	}{
		{name: "default name", def: DefCommunicate()},
		{name: "renamed", def: DefCommunicateNamed("submit_report")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			def := tc.def
			if def.Strict == nil || *def.Strict {
				t.Fatalf("Strict = %v, want an explicit false so optional properties stay optional", def.Strict)
			}
			if got := def.Parameters["type"]; got != "object" {
				t.Fatalf("parameters.type = %v, want object", got)
			}
			if got := def.Parameters["additionalProperties"]; got != false {
				t.Fatalf("parameters.additionalProperties = %v, want false", got)
			}
			props, ok := def.Parameters["properties"].(map[string]any)
			if !ok {
				t.Fatalf("parameters.properties = %T, want map[string]any", def.Parameters["properties"])
			}
			if got, want := slices.Sorted(maps.Keys(props)), []string{"message", "next", "output"}; !slices.Equal(got, want) {
				t.Fatalf("properties = %v, want %v (no end_turn or end_reason)", got, want)
			}

			nextProp, _ := props["next"].(map[string]any)
			if nextProp["type"] != "string" {
				t.Errorf("next.type = %v, want string", nextProp["type"])
			}
			if nextProp["default"] != CommunicateNextSendAndEndTurn {
				t.Errorf("next.default = %v, want %q", nextProp["default"], CommunicateNextSendAndEndTurn)
			}
			wantNextEnum := []string{CommunicateNextSendAndEndTurn, CommunicateNextSendAndContinue, CommunicateNextLogQuietlyAndEndTurn}
			if nextEnum, _ := nextProp["enum"].([]string); !slices.Equal(nextEnum, wantNextEnum) {
				t.Errorf("next.enum = %v, want %v", nextEnum, wantNextEnum)
			}

			output, _ := props["output"].(map[string]any)
			if output["type"] != "object" {
				t.Errorf("output.type = %v, want object", output["type"])
			}
			if output["additionalProperties"] != false {
				t.Errorf("output.additionalProperties = %v, want false", output["additionalProperties"])
			}
			outProps, _ := output["properties"].(map[string]any)
			if got, want := slices.Sorted(maps.Keys(outProps)), []string{"artifacts", "data", "message"}; !slices.Equal(got, want) {
				t.Errorf("output.properties = %v, want %v", got, want)
			}
			if req := schemaRequiredNames(output); len(req) > 0 {
				t.Errorf("output.required = %v, want none so an empty output envelope validates", req)
			}
			if req := schemaRequiredNames(def.Parameters); !slices.Equal(req, []string{"message"}) {
				t.Errorf("top-level required = %v, want [message]", req)
			}

			schema, err := compileSchema(def.Parameters)
			if err != nil {
				t.Fatalf("compile parameters: %v", err)
			}
			for _, args := range []map[string]any{
				{"message": "hi"},
				{"message": "hi", "next": CommunicateNextSendAndContinue},
				{"message": "hi", "next": CommunicateNextSendAndEndTurn},
				{"message": "waiting on go test", "next": CommunicateNextLogQuietlyAndEndTurn},
				{"message": "hi", "output": map[string]any{"data": map[string]any{"k": 1}}},
				{"message": "hi", "output": map[string]any{"message": "", "data": map[string]any{}, "artifacts": []any{}}},
			} {
				if err := schema.Validate(args); err != nil {
					t.Errorf("valid call %v rejected: %v", args, err)
				}
			}
			for _, args := range []map[string]any{
				{},
				{"output": map[string]any{}},
				{"message": "hi", "end_turn": true},
				{"message": "hi", "end_reason": "done"},
				{"message": "hi", "next": "invalid"},
				{"message": 3},
				{"message": "hi", "next": true},
				{"output": "nope"},
				{"output": map[string]any{"artifacts": []any{1}}},
			} {
				if err := schema.Validate(args); err == nil {
					t.Errorf("invalid call %v accepted", args)
				}
			}
		})
	}
}

// TestWithCommunicateDelegateUpdatesClonesWithoutMutatingBase verifies that
// delegate adaptation clones the schema maps without mutating the caller's
// definition and preserves the next enum/default and output schema.
func TestWithCommunicateDelegateUpdatesClonesWithoutMutatingBase(t *testing.T) {
	base := DefCommunicate()
	origBaseDesc := base.Description
	origBaseNextDesc := base.Parameters["properties"].(map[string]any)["next"].(map[string]any)["description"].(string)
	origBaseMsgDesc := base.Parameters["properties"].(map[string]any)["message"].(map[string]any)["description"].(string)

	dlg := WithCommunicateDelegateUpdates(base)

	if base.Description != origBaseDesc {
		t.Fatalf("WithCommunicateDelegateUpdates mutated caller description")
	}
	baseProps := base.Parameters["properties"].(map[string]any)
	if got := baseProps["next"].(map[string]any)["description"].(string); got != origBaseNextDesc {
		t.Fatalf("WithCommunicateDelegateUpdates mutated caller next.description")
	}
	if got := baseProps["message"].(map[string]any)["description"].(string); got != origBaseMsgDesc {
		t.Fatalf("WithCommunicateDelegateUpdates mutated caller message.description")
	}
	if dlg.Description == "" || dlg.Description == base.Description {
		t.Fatalf("WithCommunicateDelegateUpdates did not adapt tool description: %q", dlg.Description)
	}
	for _, def := range []llm.ToolDefinition{base, dlg} {
		if !strings.Contains(def.Description, "`output`") || !strings.Contains(def.Description, "`send_and_end_turn`") {
			t.Errorf("%s description must preserve conditional output rule across result-schema replacement: %q", def.Name, def.Description)
		}
	}
	dlgProps := dlg.Parameters["properties"].(map[string]any)
	dlgNext := dlgProps["next"].(map[string]any)
	if dlgNext["default"] != CommunicateNextSendAndEndTurn {
		t.Errorf("delegate next.default = %v, want %q", dlgNext["default"], CommunicateNextSendAndEndTurn)
	}
	if nextEnum, _ := dlgNext["enum"].([]string); !slices.Equal(nextEnum, schema.CommunicateNextValues) {
		t.Errorf("delegate next.enum = %v, want %v", nextEnum, schema.CommunicateNextValues)
	}
	if !reflect.DeepEqual(dlgProps["output"], baseProps["output"]) {
		t.Errorf("delegate output schema drifted from base")
	}
}

// schemaRequiredNames reads a schema's required list as []string, treating a
// missing or null entry as none. Schemas built in Go hold []string while a
// JSON round-tripped one holds []any.
func schemaRequiredNames(schema map[string]any) []string {
	raw, ok := schema["required"]
	if !ok || raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return []string{fmt.Sprintf("%v", v)}
	}
}

func TestJobListDescriptionKeepsObserverCommunicateGuidance(t *testing.T) {
	def := DefJobList()
	if !strings.Contains(def.Description, `next="send_and_end_turn"`) {
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
	for _, want := range []string{"source you can observe", "Delivery is implicit", `next="send_and_end_turn"`, `next="log_quietly_and_end_turn"`} {
		if !strings.Contains(def.Description, want) {
			t.Fatalf("job_watch description = %q, want %q", def.Description, want)
		}
	}
	props := def.Parameters["properties"].(map[string]any)
	source := props["source"].(map[string]any)
	if _, has := source["default"]; has {
		t.Fatalf("source must not declare schema default (timer-only default), got %v", source["default"])
	}
	sourceDesc := source["description"].(string)
	for _, want := range []string{"self", "parent", "job_id", "dlg_..."} {
		if !strings.Contains(sourceDesc, want) {
			t.Fatalf("source description = %q, want %q", sourceDesc, want)
		}
	}
	if _, has := props["every"].(map[string]any)["default"]; has {
		t.Fatalf("every must not declare schema default, got %v", props["every"].(map[string]any)["default"])
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
	for _, k := range []string{"query", "children_of", "kind", "has_children", "min_turns", "max_turns", "updated_after", "updated_before", "scope", "limit"} {
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
	// format accepts the three public tokens plus null so ref-aware repair can
	// remove job defaults while artifact refs still reject explicit null.
	format := rp["format"].(map[string]any)
	if _, has := format["default"]; has {
		t.Errorf("format must not declare schema default (rejected on artifact/hub refs), got %v", format["default"])
	}
	formatEnum := format["enum"].([]any)
	want := map[string]bool{"outline": true, "markdown": true, "jsonl": true}
	if len(formatEnum) != 4 {
		t.Errorf("format enum = %v, want outline|markdown|jsonl|null", formatEnum)
	}
	for _, value := range formatEnum {
		if value == nil {
			continue
		}
		f, ok := value.(string)
		if !ok || !want[f] {
			t.Errorf("unexpected format value %#v", value)
		}
	}
	for _, name := range []string{"format", "range", "expand_turn"} {
		types, ok := rp[name].(map[string]any)["type"].([]any)
		if !ok || len(types) != 2 || types[1] != "null" {
			t.Errorf("%s type = %#v, want nullable schema", name, rp[name].(map[string]any)["type"])
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
	if req, ok := read.Parameters["required"].([]string); ok && slices.Contains(req, "transcript_ref") {
		t.Errorf("read_transcript must leave transcript_ref optional, got required: %v", req)
	}

	outputMatch := rp["output_match"].(map[string]any)
	outputMatchTypes, ok := outputMatch["type"].([]any)
	if !ok || len(outputMatchTypes) != 2 || outputMatchTypes[0] != "string" || outputMatchTypes[1] != "null" || !strings.Contains(outputMatch["description"].(string), "RE2") {
		t.Errorf("output_match schema = %#v, want nullable RE2 string", outputMatch)
	}
	if outputMatch["maxLength"] != 65_536 {
		t.Errorf("output_match schema = %#v, want 65,536-character maxLength bound", outputMatch)
	}
	contextLines := rp["context_lines"].(map[string]any)
	contextLineTypes, ok := contextLines["type"].([]any)
	if !ok || len(contextLineTypes) != 2 || contextLineTypes[0] != "integer" || contextLineTypes[1] != "null" || contextLines["minimum"] != 0 || contextLines["maximum"] != 10 {
		t.Errorf("context_lines schema = %#v, want nullable integer 0..10", contextLines)
	}
	if _, has := contextLines["default"]; has {
		t.Errorf("context_lines must not declare schema default (requires output_match and rejected on hub refs), got %v", contextLines["default"])
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
	wantEnum := map[string]bool{"create": true, "list": true, "switch": true, "adopt": true, "exit": true, "remove": true, "prune": true, "unlock": true, "dispose": true}
	if len(opEnum) != len(wantEnum) {
		t.Errorf("operation enum = %v, want exactly %v", opEnum, wantEnum)
	}
	for _, v := range opEnum {
		if !wantEnum[v] {
			t.Errorf("unexpected operation enum value %q", v)
		}
	}

	// Per-operation args from spec §2's table, flattened.
	for _, k := range []string{"name", "base_ref", "path", "force", "force_dirty", "delete_branch", "id", "limit", "offset"} {
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

	limitProp, ok := props["limit"].(map[string]any)
	if !ok || limitProp["type"] != "integer" || limitProp["maximum"] != 15 || limitProp["minimum"] != 1 {
		t.Errorf("limit property = %v, want integer type with min 1, max 15", props["limit"])
	}
	offsetProp, ok := props["offset"].(map[string]any)
	if !ok || offsetProp["type"] != "integer" || offsetProp["minimum"] != 0 {
		t.Errorf("offset property = %v, want integer type with min 0", props["offset"])
	}
}

// TestDefManageWorktreeDescriptionCarriesUsagePolicy asserts the description
// states worktrees are for isolated, parallel, or risky work.
func TestDefManageWorktreeDescriptionCarriesUsagePolicy(t *testing.T) {
	def := DefManageWorktree()
	desc := strings.ToLower(def.Description)

	for _, phrase := range []string{"isolated", "parallel", "risky"} {
		if !strings.Contains(desc, phrase) {
			t.Errorf("description missing usage-policy phrase %q; got: %s", phrase, def.Description)
		}
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
// TestDefReadFileSliceParamsDocumented pins the offset/limit semantics: offset
// is a 1-based start line that also accepts negative tail reads, and limit
// defaults to DefaultReadLines lines with limit=1 returning a single long line uncut
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
	if _, has := offset["default"]; has {
		t.Errorf("offset must not declare schema default, got %v", offset["default"])
	}
	if !strings.Contains(offsetDesc, "1-based") || !strings.Contains(offsetDesc, "negative") {
		t.Errorf("offset description should document a 1-based start line and negative tail reads, got: %q", offsetDesc)
	}
	if _, has := limit["default"]; has {
		t.Errorf("limit must not declare schema default, got %v", limit["default"])
	}
	if !descriptionMentions(limitDesc, 2000) || !descriptionMentions(limitDesc, execenv.DefaultReadLines) || !strings.Contains(limitDesc, "48 KB") || !strings.Contains(limitDesc, "uncut") {
		t.Errorf("limit description should document the default of %d (~48 KB cap) and the limit=1 uncut escape, got: %q", execenv.DefaultReadLines, limitDesc)
	}
}

// TestDefReadFileVariantsExposeSharedParameters pins the one shared schema
// contract across all three vision regimes and memory_read: every variant
// carries file_path, offset, limit, and pages with a non-empty description, so
// a session can pass a PDF page selector no matter which regime it selected.
// vision_prompt presence follows the regime, and memory_read keeps scope.
func TestDefReadFileVariantsExposeSharedParameters(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		def        llm.ToolDefinition
		wantVision bool
	}{
		{"read_file", DefReadFile(), true},
		{"read_file-native-vision", DefReadFileNativeVision(), false},
		{"read_file-pdf-vision-only", DefReadFilePDFVisionOnly(), true},
		{"memory_read", DefMemoryRead(), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.def.Parameters["additionalProperties"] != false {
				t.Fatalf("%s additionalProperties = %v, want false", tc.name, tc.def.Parameters["additionalProperties"])
			}
			required(t, tc.def, tc.name, []string{"file_path"})
			props, ok := tc.def.Parameters["properties"].(map[string]any)
			if !ok {
				t.Fatalf("%s properties = %T, want map[string]any", tc.name, tc.def.Parameters["properties"])
			}
			for _, paramName := range []string{"file_path", "offset", "limit", "pages"} {
				param, ok := props[paramName].(map[string]any)
				if !ok {
					t.Fatalf("%s missing the %q property", tc.name, paramName)
				}
				desc, _ := param["description"].(string)
				if strings.TrimSpace(desc) == "" {
					t.Errorf("%s %q carries no description", tc.name, paramName)
				}
			}
			if _, hasVision := props["vision_prompt"]; hasVision != tc.wantVision {
				t.Errorf("%s vision_prompt present = %v, want %v", tc.name, hasVision, tc.wantVision)
			}
			if tc.name == "memory_read" {
				required(t, tc.def, tc.name, []string{"scope"})
			}
		})
	}
}

// TestDefReadFileVisionPromptIsSeparateFromIntent pins the read_file split: the
// image/PDF extraction ask rides its own `vision_prompt` property (the old
// overload put it on `intent`), and `intent` arrives only from the universal
// WithIntentParameter injection, carrying the generic tool-intent description.
func TestDefReadFileVisionPromptIsSeparateFromIntent(t *testing.T) {
	def := DefReadFile()
	props, ok := def.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("read_file properties = %T, want map[string]any", def.Parameters["properties"])
	}
	vp, ok := props["vision_prompt"].(map[string]any)
	if !ok {
		t.Fatalf("read_file missing vision_prompt property; got properties: %v", props)
	}
	if vp["type"] != "string" {
		t.Errorf("vision_prompt type = %v, want string", vp["type"])
	}
	if desc, _ := vp["description"].(string); !strings.Contains(desc, "Image/PDF") {
		t.Errorf("vision_prompt description should scope itself to image/PDF reads, got: %q", desc)
	}
	if _, has := props["intent"]; has {
		t.Errorf("DefReadFile must not define its own intent property (vision prose moved to vision_prompt); got: %v", props["intent"])
	}
	// The tool-level description must name the argument the vision ask belongs
	// in, or callers keep assuming it rides `intent` (roborev #1139).
	if !strings.Contains(def.Description, "vision_prompt") {
		t.Errorf("read_file description should direct image/PDF asks to vision_prompt, got: %q", def.Description)
	}

	// The registered schema is what the model sees: intent is injected by the
	// universal rule, not supplied by the read_file definition.
	regProps, ok := WithIntentParameter(def).Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("registered read_file properties = %T, want map[string]any", WithIntentParameter(def).Parameters["properties"])
	}
	intent, ok := regProps["intent"].(map[string]any)
	if !ok {
		t.Fatalf("registered read_file missing injected intent; got properties: %v", regProps)
	}
	if desc, _ := intent["description"].(string); desc != toolIntentDescription {
		t.Errorf("read_file intent should carry the generic tool-intent description, got: %q", desc)
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
	if cl["default"] != 0 {
		t.Errorf("context_lines default = %v, want 0", cl["default"])
	}
	desc, _ := cl["description"].(string)
	// Pin the actual parameter semantics, not bare digit characters. "0" and
	// "10" individually appear almost anywhere; the meaningful contract is
	// the range form alongside the schema default.
	if !strings.Contains(desc, "0-10") {
		t.Errorf("context_lines description should document the 0-10 range, got: %q", desc)
	}
	if !strings.Contains(strings.ToLower(desc), "context") {
		t.Errorf("context_lines description should mention context lines, got: %q", desc)
	}
}

func TestDefGrepMaxResultsParam(t *testing.T) {
	def := DefGrep()
	props, ok := def.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("grep properties = %T, want map[string]any", def.Parameters["properties"])
	}
	mr, ok := props["max_results"].(map[string]any)
	if !ok {
		t.Fatalf("grep missing max_results property; got properties: %v", props)
	}
	if mr["type"] != "integer" {
		t.Errorf("max_results type = %v, want integer", mr["type"])
	}
	// Pin the schema default from
	// execenv.DefaultGrepMaxResults — the constant every implementation site
	// shares — keeps the advertised default from drifting off the enforced one.
	if mr["default"] != execenv.DefaultGrepMaxResults {
		t.Errorf("max_results default = %v, want %d", mr["default"], execenv.DefaultGrepMaxResults)
	}
}

// TestDefModelListBoundsMatchContract pins model_list's advertised page
// bounds to the named constants the enforcing handler reads
// (modelavailability.DefaultInlineMaxCount/DefaultInlineMaxBytes), so the
// schema, its prose, and the enforced bounds cannot drift apart silently.
func TestDefModelListBoundsMatchContract(t *testing.T) {
	def := DefModelList()
	props, ok := def.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("model_list properties = %T, want map[string]any", def.Parameters["properties"])
	}
	for _, tc := range []struct {
		param string
		deflt int
	}{
		{"max_count", modelavailability.DefaultInlineMaxCount},
		{"max_bytes", modelavailability.DefaultInlineMaxBytes},
	} {
		prop, ok := props[tc.param].(map[string]any)
		if !ok {
			t.Fatalf("model_list missing %s property; got properties: %v", tc.param, props)
		}
		if bound, ok := prop["maximum"].(int); !ok || bound != tc.deflt {
			t.Errorf("model_list %s maximum = %v (%T), want the enforced bound %d", tc.param, prop["maximum"], prop["maximum"], tc.deflt)
		}
		if _, has := prop["default"]; has {
			t.Errorf("model_list %s must not declare schema default (rejected on hub reads), got %v", tc.param, prop["default"])
		}
		desc, _ := prop["description"].(string)
		if !descriptionMentions(desc, tc.deflt) {
			t.Errorf("model_list %s description should document the default of %d, got: %q", tc.param, tc.deflt, desc)
		}
	}

	maxBytesProp, ok := props["max_bytes"].(map[string]any)
	if !ok {
		t.Fatalf("missing max_bytes prop")
	}
	if minBytes, ok := maxBytesProp["minimum"].(int); !ok || minBytes != 512 {
		t.Errorf("model_list max_bytes minimum = %v, want 512", maxBytesProp["minimum"])
	}
}

// TestAllBuiltinParametersCarryDescriptions is the description audit as a
// permanent gate: every property the model sees, nested object and array-item
// properties included, must document itself. A bare parameter name banks on
// the model's training prior naming it the way we do, and the harvested
// FuzzToolArgsValidate corpus records where that fails (a real grep call
// shaped like Claude Code's schema: -i, head_limit). The walk reads the raw
// Def* schemas: the one property WithIntentParameter adds, intent, is
// injected from the constant toolIntentDescription and pinned by the
// read_file intent test, so it cannot lack a description.
func TestAllBuiltinParametersCarryDescriptions(t *testing.T) {
	defs := []llm.ToolDefinition{
		DefReadFile(), DefWriteFile(), DefListDir(), DefEditFile(), DefShell(),
		DefDelegate(), DefDelegateWithSandbox(nil, DelegateSandboxSchema{}),
		DefDelegateSend(), DefModelList(), DefJobWatch(nil),
		DefJobStatus(), DefJobList(), DefJobStop(), DefGrep(), DefGlob(),
		DefApplyPatch(), DefWebFetch(), DefCommunicate(),
		DefTaskList(nil), DefUseSkill(), DefFindSessionTranscripts(), DefMemoryEdit(),
		DefDoctorEvener(), DefManageWorktree(), DefManageWorktreeDisposeOnly(),
		DefReadTranscript(), DefAskUser(), DefUpdateGoal(), DefNotesAgentSet(),
		DefUrlsAdd(), DefUrlsRemove(), DefNotesRead(),
	}
	for _, def := range defs {
		if missing := UndocumentedProperties(def); len(missing) > 0 {
			t.Errorf("%s parameters carry no description: %s", def.Name, strings.Join(missing, ", "))
		}
	}
}

func TestDefEditFileAndMemoryEditSchema(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		def      llm.ToolDefinition
		wantReq  []string
		hasScope bool
	}{
		{name: "edit_file", def: DefEditFile(), wantReq: []string{"file_path", "edits"}, hasScope: false},
		{name: "memory_edit", def: DefMemoryEdit(), wantReq: []string{"file_path", "edits", "scope"}, hasScope: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.def.Name != tc.name {
				t.Fatalf("Name = %q, want %q", tc.def.Name, tc.name)
			}
			if tc.def.Parameters["additionalProperties"] != false {
				t.Fatalf("additionalProperties = %v, want false", tc.def.Parameters["additionalProperties"])
			}
			required(t, tc.def, tc.name, tc.wantReq)
			props := tc.def.Parameters["properties"].(map[string]any)
			if _, has := props["old_string"]; has {
				t.Fatalf("%s must not expose top-level old_string", tc.name)
			}
			if _, has := props["new_string"]; has {
				t.Fatalf("%s must not expose top-level new_string", tc.name)
			}
			if _, has := props["scope"]; has != tc.hasScope {
				t.Fatalf("%s scope present=%v, want %v", tc.name, has, tc.hasScope)
			}
			edits, ok := props["edits"].(map[string]any)
			if !ok || edits["type"] != "array" || edits["minItems"] != 1 {
				t.Fatalf("%s edits schema = %#v", tc.name, props["edits"])
			}
			items := edits["items"].(map[string]any)
			if items["additionalProperties"] != false {
				t.Fatalf("%s edits.items additionalProperties = %v, want false", tc.name, items["additionalProperties"])
			}
			itemProps := items["properties"].(map[string]any)
			for _, k := range []string{"old_string", "new_string", "replace_all"} {
				if _, ok := itemProps[k]; !ok {
					t.Fatalf("%s edits.items missing %q", tc.name, k)
				}
			}
		})
	}
}

// TestDefAskUserNamedPropagatesResultTool verifies ask_user carries the
// configured result tool token and the [answers] reply framing marker.
func TestDefAskUserNamedPropagatesResultTool(t *testing.T) {
	def := DefAskUser()
	for _, want := range []string{"`communicate`", "[answers]"} {
		if !strings.Contains(def.Description, want) {
			t.Fatalf("description missing %q; got: %s", want, def.Description)
		}
	}
	renamed := DefAskUserNamed("submit_report")
	if !strings.Contains(renamed.Description, "`submit_report`") || strings.Contains(renamed.Description, DefaultResultToolName) {
		t.Fatalf("DefAskUserNamed did not propagate result tool name: %q", renamed.Description)
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
	if !ok || len(addReq) != 2 || addReq[0] != "description" || addReq[1] != "prompt" {
		t.Fatalf("add item required = %v, want [description prompt]", addItems["required"])
	}
	if addItems["additionalProperties"] != false {
		t.Fatalf("add item additionalProperties = %v, want false", addItems["additionalProperties"])
	}
	if _, has := addItems["properties"].(map[string]any)["brief"]; has {
		t.Fatal("add item schema must not accept a brief alias")
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
	if updateItems["additionalProperties"] != false {
		t.Fatalf("update item additionalProperties = %v, want false", updateItems["additionalProperties"])
	}
	if _, has := updateItems["properties"].(map[string]any)["brief"]; has {
		t.Fatal("update item schema must not accept a brief alias")
	}
	if top, has := params["required"]; has {
		t.Fatalf("schema must not force-require add/update at top level: %v", top)
	}
}

// TestDefDelegatePromptAndTaskListSchema pins the delegate brief contract: the
// brief parameter is `prompt`, it carries a description (the delegate sees
// nothing but this string and its role prompt), and `task_list` seeds the
// delegate's task list with title+prompt items.
func TestDefDelegatePromptAndTaskListSchema(t *testing.T) {
	props := DefDelegate().Parameters["properties"].(map[string]any)
	prompt := props["prompt"].(map[string]any)
	if prompt["type"] != "string" {
		t.Errorf("prompt type = %v, want string", prompt["type"])
	}
	if desc, _ := prompt["description"].(string); desc == "" {
		t.Error("prompt must carry a description; it is the only input the delegate sees")
	}
	tl, ok := props["task_list"].(map[string]any)
	if !ok {
		t.Fatal("DefDelegate has no task_list param")
	}
	if tl["type"] != "array" {
		t.Errorf("task_list type = %v, want array", tl["type"])
	}
	if desc, _ := tl["description"].(string); desc == "" {
		t.Error("task_list must carry a description")
	}
	items := tl["items"].(map[string]any)
	if items["type"] != "object" {
		t.Errorf("task_list items type = %v, want object", items["type"])
	}
	itemProps := items["properties"].(map[string]any)
	for _, p := range []string{"title", "prompt", "reasoning_effort", "type"} {
		if _, ok := itemProps[p]; !ok {
			t.Errorf("task_list item missing property %q", p)
		}
	}
	req := items["required"].([]string)
	if len(req) != 2 || req[0] != "title" || req[1] != "prompt" {
		t.Errorf("task_list item required = %v, want [title prompt]", req)
	}
	if items["additionalProperties"] != false {
		t.Errorf("task_list items additionalProperties = %v, want false", items["additionalProperties"])
	}
}

func schemaPropAt(t *testing.T, def llm.ToolDefinition, path ...string) map[string]any {
	t.Helper()
	cur := def.Parameters
	for _, seg := range path {
		if seg == "[]" {
			next, ok := cur["items"].(map[string]any)
			if !ok {
				t.Fatalf("%s missing items along %v", def.Name, path)
			}
			cur = next
			continue
		}
		props, ok := cur["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s missing properties along %v", def.Name, path)
		}
		next, ok := props[seg].(map[string]any)
		if !ok {
			t.Fatalf("%s missing property %q along %v", def.Name, seg, path)
		}
		cur = next
	}
	return cur
}

func TestBuiltinStaticDefaultsDeclaredInSchema(t *testing.T) {
	t.Parallel()
	prop := func(def llm.ToolDefinition, path ...string) map[string]any {
		t.Helper()
		return schemaPropAt(t, def, path...)
	}
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"list_dir.depth", prop(DefListDir(), "depth")["default"], 1},
		{"list_dir.offset", prop(DefListDir(), "offset")["default"], 0},
		{"list_dir.limit", prop(DefListDir(), "limit")["default"], 1000},
		{"edit_file.edits[].replace_all", prop(DefEditFile(), "edits", "[]", "replace_all")["default"], false},
		{"shell.mode", prop(DefShell(), "mode")["default"], "foreground"},
		{"delegate.fork_context", prop(DefDelegate(), "fork_context")["default"], false},
		{"delegate.task_list[].reasoning_effort", prop(DefDelegate(), "task_list", "[]", "reasoning_effort")["default"], "inherit"},
		{"delegate.task_list[].type", prop(DefDelegate(), "task_list", "[]", "type")["default"], "implement"},
		{"job_list.include_nested", prop(DefJobList(), "include_nested")["default"], false},
		{"job_list.include_descendants", prop(DefJobList(), "include_descendants")["default"], false},
		{"job_list.limit", prop(DefJobList(), "limit")["default"], 50},
		{"job_list.offset", prop(DefJobList(), "offset")["default"], 0},
		{"job_stop.include_children", prop(DefJobStop(), "include_children")["default"], false},
		{"grep.max_results", prop(DefGrep(), "max_results")["default"], 100},
		{"grep.context_lines", prop(DefGrep(), "context_lines")["default"], 0},
		{"grep.output_mode", prop(DefGrep(), "output_mode")["default"], "content"},
		{"glob.include_ignored", prop(DefGlob(), "include_ignored")["default"], false},
		{"task_list.add[].type", prop(DefTaskList(nil), "add", "[]", "type")["default"], "implement"},
		{"find_session_transcripts.scope", prop(DefFindSessionTranscripts(), "scope")["default"], "current_project"},
		{"find_session_transcripts.limit", prop(DefFindSessionTranscripts(), "limit")["default"], 10},
		{"manage_worktree.limit", prop(DefManageWorktree(), "limit")["default"], 15},
		{"manage_worktree.offset", prop(DefManageWorktree(), "offset")["default"], 0},
		{"communicate.next", prop(DefCommunicate(), "next")["default"], CommunicateNextSendAndEndTurn},
		{"ask_user.questions[].multi_select", prop(DefAskUser(), "questions", "[]", "multi_select")["default"], false},
	}
	for _, tc := range checks {
		if tc.got != tc.want {
			t.Errorf("%s default = %#v, want %#v", tc.name, tc.got, tc.want)
		}
	}
}

// TestContextDependentParamsOmitSchemaDefault verifies that parameters whose
// explicit default behaves differently from omission across supported call
// shapes (negative offsets, artifact refs, multi-op tools, or Manager hub
// branches) intentionally do not declare a JSON Schema "default".
func TestContextDependentParamsOmitSchemaDefault(t *testing.T) {
	t.Parallel()
	checks := []struct {
		name string
		prop map[string]any
	}{
		{"read_file.offset", schemaPropAt(t, DefReadFile(), "offset")},
		{"read_file.limit", schemaPropAt(t, DefReadFile(), "limit")},
		{"memory_read.offset", schemaPropAt(t, DefMemoryRead(), "offset")},
		{"memory_read.limit", schemaPropAt(t, DefMemoryRead(), "limit")},
		{"delegate_send.max_wait_ms", schemaPropAt(t, DefDelegateSend(), "max_wait_ms")},
		{"model_list.max_count", schemaPropAt(t, DefModelList(), "max_count")},
		{"model_list.max_bytes", schemaPropAt(t, DefModelList(), "max_bytes")},
		{"job_watch.source", schemaPropAt(t, DefJobWatch(nil), "source")},
		{"job_watch.every", schemaPropAt(t, DefJobWatch(nil), "every")},
		{"job_stop.max_wait_ms", schemaPropAt(t, DefJobStop(), "max_wait_ms")},
		{"manage_worktree.base_ref", schemaPropAt(t, DefManageWorktree(), "base_ref")},
		{"manage_worktree.orphaned_delegate_days", schemaPropAt(t, DefManageWorktree(), "orphaned_delegate_days")},
		{"read_transcript.format", schemaPropAt(t, DefReadTranscript(), "format")},
		{"read_transcript.context_lines", schemaPropAt(t, DefReadTranscript(), "context_lines")},
	}
	for _, tc := range checks {
		if v, has := tc.prop["default"]; has {
			t.Errorf("%s must omit schema default, got %#v", tc.name, v)
		}
	}
}
