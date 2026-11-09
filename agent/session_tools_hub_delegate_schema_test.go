package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/internal/tool/repair"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm"
)

// managerDelegateHubCall drives one Manager-mode delegate tool call through the
// real pre-dispatch path: the registry's composed schema, argument repair, and
// the schema-error explainer. It returns the prepared call's refusal, empty
// when the call validates.
func managerDelegateHubCall(t *testing.T, args string) string {
	t.Helper()
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	registered := root.reg.Get("delegate")
	if registered == nil {
		t.Fatal("Manager delegate tool not registered")
	}
	res := prepareToolCall(llm.ToolCallData{ID: "hub-call", Name: "delegate", Arguments: json.RawMessage(args)},
		registered, []string{"delegate"}, "delegate", "communicate", "")
	return res.PrevalErr
}

// The hub-branch delegate call accepts reasoning_effort: the wire start
// params and the hub's own launch resolution already carry a spawn-time
// effort, so refusing it at the tool schema rejected a valid argument.
func TestManagerDelegateHubCallAcceptsReasoningEffort(t *testing.T) {
	t.Parallel()
	if refusal := managerDelegateHubCall(t, `{"prompt":"worker task","hub":{"cwd":"/tmp","model":"openai/gpt-5.2","watch":{"completion":true}},"reasoning_effort":"high","intent":"spawn the worker"}`); refusal != "" {
		t.Fatalf("hub-branch call with reasoning_effort was refused: %s", refusal)
	}
}

// An extra owned-delegate argument the hub branch genuinely does not take must
// be refused with the argument named — not silently dropped by argument
// repair, and never explained as a missing supplied argument. The schema's
// prompt parameter was present in every reported failure; the old explanation
// named it anyway.
func TestManagerDelegateHubCallExtraArgumentDoesNotClaimMissingPrompt(t *testing.T) {
	t.Parallel()
	refusal := managerDelegateHubCall(t, `{"prompt":"worker task","hub":{"cwd":"/tmp","model":"openai/gpt-5.2","watch":{"completion":true}},"name":"watch-delivery-fix","intent":"spawn the worker"}`)
	if refusal == "" {
		t.Fatal("hub-branch call with an extra owned-delegate argument was accepted")
	}
	if strings.Contains(refusal, "Required arguments: prompt") {
		t.Fatalf("refusal claimed the supplied prompt argument was missing: %s", refusal)
	}
	if !strings.Contains(refusal, "name") {
		t.Fatalf("refusal did not name the rejected argument: %s", refusal)
	}
}

// A hub call carrying several extras names every rejected argument at once, so
// one retry removes them all instead of discovering one per round trip. An
// out-of-enum value is still a configured argument: the refusal must name it
// rather than silently skip it as a placeholder. isolation is enum-typed on
// every host, unlike sandbox, whose declaration depends on the session's
// sandbox backend.
func TestManagerDelegateHubCallNamesEveryRejectedArgument(t *testing.T) {
	t.Parallel()
	refusal := managerDelegateHubCall(t, `{"prompt":"worker task","hub":{"cwd":"/tmp","model":"openai/gpt-5.2","watch":{"completion":true}},"name":"watch-delivery-fix","agent_type":"default","isolation":"bogus","intent":"spawn the worker"}`)
	if refusal == "" {
		t.Fatal("hub-branch call with extra arguments was accepted")
	}
	for _, field := range []string{"name", "agent_type", "isolation"} {
		if !strings.Contains(refusal, field) {
			t.Fatalf("refusal did not name the rejected argument %s: %s", field, refusal)
		}
	}
}

// The refusal covers the registry's dispatch path too: a call preparation
// never saw — a hook rewrote its arguments mid-flight — reaches ExecuteCall,
// where the same PreValidate must refuse before generic schema validation and
// repair can drop the extra.
func TestManagerDelegateHubExtraRefusedAtRegistryDispatch(t *testing.T) {
	t.Parallel()
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	registered := root.reg.Get("delegate")
	if registered == nil {
		t.Fatal("Manager delegate tool not registered")
	}
	res := root.reg.ExecuteCall(context.Background(), nil, llm.ToolCallData{
		ID:        "dispatch-hub-extra",
		Name:      "delegate",
		Arguments: json.RawMessage(`{"prompt":"worker task","hub":{"cwd":"/tmp","model":"openai/gpt-5.2","watch":{"completion":true}},"name":"watch-delivery-fix","intent":"spawn the worker"}`),
	})
	if !res.IsError {
		t.Fatalf("dispatched hub call with an extra argument was applied: %+v", res)
	}
	if !strings.Contains(res.Output, "name") {
		t.Fatalf("dispatch refusal did not name the rejected argument: %s", res.Output)
	}
	if strings.Contains(res.Output, "Required arguments: prompt") {
		t.Fatalf("dispatch refusal claimed the supplied prompt argument was missing: %s", res.Output)
	}
}

// Null placeholders are repair's to drop, not the hub-branch refusal's to
// reject: a model that sends null for fields it means to leave out gets the
// same healed call it always did — the refusal names only configured
// arguments.
func TestManagerDelegateHubCallNullPlaceholdersAreNotRefused(t *testing.T) {
	t.Parallel()
	if refusal := managerDelegateHubCall(t, `{"prompt":"worker task","hub":{"cwd":"/tmp","model":"openai/gpt-5.2","watch":{"completion":true}},"name":null,"agent_type":null,"intent":"spawn the worker"}`); refusal != "" {
		t.Fatalf("hub-branch call with null placeholders was refused: %s", refusal)
	}
}

// An empty string on an enum-typed optional is the same placeholder habit:
// the isolation enum does not list it, repair drops it, and the refusal must
// not name it.
func TestManagerDelegateHubCallEmptyEnumPlaceholderIsNotRefused(t *testing.T) {
	t.Parallel()
	if refusal := managerDelegateHubCall(t, `{"prompt":"worker task","hub":{"cwd":"/tmp","model":"openai/gpt-5.2","watch":{"completion":true}},"isolation":"","intent":"spawn the worker"}`); refusal != "" {
		t.Fatalf("hub-branch call with an empty enum placeholder was refused: %s", refusal)
	}
}

// A null hub object is the ordinary branch's placeholder, not a hub creation:
// the refusal must not misclassify the call and refuse its owned-delegate
// arguments.
func TestManagerDelegateOrdinaryCallWithNullHubIsNotRefused(t *testing.T) {
	t.Parallel()
	if refusal := managerDelegateHubCall(t, `{"prompt":"worker task","hub":null,"name":"worker-lane","intent":"spawn the worker"}`); refusal != "" {
		t.Fatalf("ordinary call carrying a null hub was refused: %s", refusal)
	}
}

// The hub-only variant has one branch, so its property descriptions must
// coach hub creation rather than the owned-delegate arguments that branch
// refuses: a Manager whose only path is hub creation would otherwise be told
// to put steps in task_list, use fork_context and isolation, and that an
// absent effort inherits from a parent that does not exist.
func TestManagerDelegateHubOnlyDescriptionsCoachHubCreation(t *testing.T) {
	t.Parallel()
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	root.mu.Lock()
	root.profile = &provider.Profile{}
	root.rebuildToolDefsCache()
	root.mu.Unlock()
	registered := root.reg.Get("delegate")
	if registered == nil {
		t.Fatal("Manager delegate tool not registered")
	}
	props, _ := registered.Definition.Parameters["properties"].(map[string]any)
	prompt, _ := props["prompt"].(map[string]any)["description"].(string)
	for _, field := range []string{"task_list", "fork_context", "isolation"} {
		if strings.Contains(prompt, field) {
			t.Fatalf("hub-only prompt description coaches the refused argument %s", field)
		}
	}
	effort, _ := props["reasoning_effort"].(map[string]any)["description"].(string)
	if !strings.Contains(effort, "launch default") {
		t.Fatalf("hub-only effort description does not name the hub's launch default: %s", effort)
	}
}

// A hub-only profile judges placeholders against the owned-delegate property
// schemas, exactly like the ordinary variant: its pruned top level does not
// declare isolation, but repair still drops an empty-enum value there as an
// unknown key, so the refusal must not name it.
func TestManagerDelegateHubOnlyEmptyEnumPlaceholderIsNotRefused(t *testing.T) {
	t.Parallel()
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	root.mu.Lock()
	root.profile = &provider.Profile{}
	root.rebuildToolDefsCache()
	root.mu.Unlock()
	registered := root.reg.Get("delegate")
	if registered == nil {
		t.Fatal("Manager delegate tool not registered")
	}
	res := prepareToolCall(llm.ToolCallData{ID: "hub-only-enum", Name: "delegate",
		Arguments: json.RawMessage(`{"prompt":"worker task","hub":{"cwd":"/tmp","model":"openai/gpt-5.2","watch":{"completion":true}},"isolation":"","intent":"spawn the worker"}`),
	}, registered, []string{"delegate"}, "delegate", "communicate", "")
	if res.PrevalErr != "" {
		t.Fatalf("hub-only call with an empty enum placeholder was refused: %s", res.PrevalErr)
	}
}

// The ordinary variant's effort description must state the hub branch's
// absent-effort default too: the base "inherits from parent" text alone
// would tell a hub caller the new session inherits the parent's effort,
// where the hub's launch default applies.
func TestManagerDelegateEffortDescriptionStatesHubDefault(t *testing.T) {
	t.Parallel()
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	registered := root.reg.Get("delegate")
	if registered == nil {
		t.Fatal("Manager delegate tool not registered")
	}
	hubBranch := hubBranchArgumentNames(tool.WithIntentParameter(registered.Definition).Parameters)
	effort, _ := hubBranch["reasoning_effort"].(map[string]any)
	description, _ := effort["description"].(string)
	if !strings.Contains(description, "launch default") {
		t.Fatalf("ordinary variant's effort description does not state the hub default: %s", description)
	}
}

// The explainer must not misattribute a hub-branch schema failure to the
// supplied prompt against the real composed schema the session registers, not
// only the mirrored fixture in the repair package. The session-level refusals
// above are intercepted by PreValidate before schema validation, so this is
// the one place the registered definition's own failure reaches the
// explainer.
func TestManagerDelegateHubExtraSchemaExplanationDoesNotNameSuppliedPrompt(t *testing.T) {
	t.Parallel()
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	registered := root.reg.Get("delegate")
	if registered == nil {
		t.Fatal("Manager delegate tool not registered")
	}
	args := map[string]any{
		"prompt": "worker task",
		"hub":    map[string]any{"cwd": "/tmp", "model": "openai/gpt-5.2", "watch": map[string]any{"completion": true}},
		"name":   "watch-delivery-fix",
	}
	err := registered.Schema.Validate(args)
	if err == nil {
		t.Fatal("hub-branch call with an extra argument validated against the composed schema")
	}
	msg := repair.ExplainSchemaError("delegate", registered.Definition.Parameters, args,
		offendingField(err), offendingKeywordLocation(err))
	if strings.Contains(msg, "Required arguments: prompt") {
		t.Fatalf("explanation claimed the supplied prompt argument was missing: %s", msg)
	}
	if !strings.Contains(msg, "reasoning_effort") {
		t.Fatalf("explanation did not render the hub arm's accepted-argument set: %s", msg)
	}
}

// A hub-branch call that validates still has to reach the wire with its
// reasoning effort: the create request the hub receives carries it, so the
// spawned session starts at the requested effort rather than the launch
// default.
func TestManagerDelegateHubCreatePassesReasoningEffortToTheSpawn(t *testing.T) {
	t.Parallel()
	worker := newTestSession(t)
	client := &managerCreationClient{worker: worker}
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	root.SetHubClient(client)
	out, err := root.createHubDelegate(t.Context(), map[string]any{
		"prompt":           "worker task",
		"reasoning_effort": "high",
		"hub":              map[string]any{"cwd": t.TempDir(), "model": "openai/gpt-5.2", "watch": map[string]any{"events": []string{}, "completion": true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, ok := out.(managerCreateOutcome)
	if !ok || created.Created.Status != "created" || created.Input.Status != "submitted" {
		t.Fatalf("creation stages = %#v", out)
	}
	if len(client.createReqs) != 1 || client.createReqs[0].ReasoningEffort != "high" {
		t.Fatalf("create requests = %+v, want one carrying reasoning_effort high", client.createReqs)
	}
}

// A Manager profile without the ordinary delegate surface composes a closed
// hub-creation schema. Without the prevalidation refusal, argument repair
// would silently drop the undeclared extra and start a session the caller
// believes it named; the refusal must fire before repair can, naming the
// argument instead.
func TestManagerDelegateHubOnlyCallExtraArgumentNamesTheRejectedArgument(t *testing.T) {
	t.Parallel()
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	// Drop the profile's delegate definition so only the Manager hub branch
	// composes, mirroring the composition matrix's profile-omitted mode.
	root.mu.Lock()
	root.profile = &provider.Profile{}
	root.rebuildToolDefsCache()
	root.mu.Unlock()
	registered := root.reg.Get("delegate")
	if registered == nil {
		t.Fatal("Manager delegate tool not registered")
	}
	res := prepareToolCall(llm.ToolCallData{ID: "hub-only", Name: "delegate",
		Arguments: json.RawMessage(`{"prompt":"worker task","hub":{"cwd":"/tmp","model":"openai/gpt-5.2","watch":{"completion":true}},"name":"watch-delivery-fix","intent":"spawn the worker"}`),
	}, registered, []string{"delegate"}, "delegate", "communicate", "")
	if res.PrevalErr == "" {
		t.Fatal("hub-only call with an extra owned-delegate argument was accepted")
	}
	if strings.Contains(res.PrevalErr, "Required arguments: prompt") || strings.Contains(res.PrevalErr, "Required arguments: hub") {
		t.Fatalf("refusal claimed a supplied required argument was missing: %s", res.PrevalErr)
	}
	if !strings.Contains(res.PrevalErr, "name") {
		t.Fatalf("refusal did not name the rejected argument: %s", res.PrevalErr)
	}
	if len(res.Changes) != 0 {
		t.Fatalf("refusal still recorded argument changes: %+v", res.Changes)
	}
}
