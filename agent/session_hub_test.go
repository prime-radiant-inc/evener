package agent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// The ordinary runtime must reject a forged Manager extension at execution,
// even when a caller bypasses the advertised schema.
func TestManagerAdmissionOrdinaryForgedDiscovery(t *testing.T) {
	t.Parallel()
	adapter := newMessagingAdapter()
	adapter.script("root", func(req llm.Request) llm.Response {
		return toolCallResponse(llm.ToolCallData{ID: "forged-discovery", Name: "job_list", Arguments: json.RawMessage(`{"hub":{"mode":"discovery"}}`), Type: "function"})
	}, func(req llm.Request) llm.Response { return finalResponse("done") })
	root := newMessagingRoot(t, adapter, nil)
	// TRIPWIRE: scripted responses complete immediately; this only guards a hung request.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := root.ProcessInput(ctx, "start", nil); err != nil {
		t.Fatal(err)
	}
	requests := adapter.waitForRequests(t, "root", 2)
	for _, def := range requests[0].Tools {
		if def.Name == "job_list" {
			if _, present := def.Parameters["properties"].(map[string]any)["hub"]; present {
				t.Fatal("ordinary request exposes Manager arguments")
			}
		}
	}
	result, err := jobListTool(root, map[string]any{"hub": map[string]any{"mode": "discovery"}}, jobToolResultDefaultMaxChar)
	if err == nil {
		t.Fatalf("forged Manager discovery executed as ordinary job_list: %+v", result)
	}
	ordinary := root.reg.ExecuteCall(ctx, root.currentEnv(), llm.ToolCallData{ID: "ordinary", Name: "job_list", Arguments: json.RawMessage(`{}`)})
	if ordinary.IsError {
		t.Fatalf("ordinary job_list failed: %+v", ordinary)
	}
}

type managerStateForUnit struct{ enabled atomic.Bool }

type managerInputForUnit struct {
	managerStateForUnit
	requests []appwire.SessionOperationRequest
}

func (c *managerInputForUnit) Invoke(_ context.Context, request appwire.SessionOperationRequest) (appwire.SessionOperationResponse, error) {
	c.requests = append(c.requests, request)
	if request.Start != nil {
		return appwire.SessionOperationResponse{Start: &appwire.TurnStartResponse{Receipt: appwire.MutationReceipt{ClientMutationID: request.Start.ClientMutationID}}}, nil
	}
	return appwire.SessionOperationResponse{Control: &appwire.SessionControlResponse{Actor: appwire.SessionActor{Kind: "manager", ManagerRef: "local:manager"}}}, nil
}

func TestManagerInputToolUsesBoundTypedOperations(t *testing.T) {
	root := newMessagingRoot(t, newMessagingAdapter(), nil)
	client := &managerInputForUnit{}
	client.enabled.Store(true)
	root.SetHubClient(client)
	ctx := t.Context()
	root.refreshHubState(ctx)
	call := func(name, args string) {
		t.Helper()
		result := root.reg.ExecuteCall(ctx, root.currentEnv(), llm.ToolCallData{ID: name, Name: name, Arguments: json.RawMessage(args)})
		if result.IsError {
			t.Fatalf("%s refused implemented Manager action: %+v", name, result)
		}
	}
	call("delegate_send", `{"to":"local:worker","input_mode":"start","message":"reply","client_mutation_id":"reply-1","expected_instance_id":"instance-1","intent":"Answering the worker question"}`)
	if len(client.requests) != 1 || client.requests[0].Start == nil || client.requests[0].Start.ClientMutationID != "reply-1" || client.requests[0].Start.ManagerProof != nil || len(client.requests[0].Start.Input) != 1 || client.requests[0].Start.Input[0].Text != "reply" {
		t.Fatalf("input tool did not forward typed request: %+v", client.requests)
	}
	call("session_control", `{"action":"approval","arguments":{"ref":"local:worker","escalationId":"esc-1","approve":false},"intent":"Denying the pending sandbox request"}`)
	if len(client.requests) != 2 || client.requests[1].Control == nil || client.requests[1].Control.Approval == nil || client.requests[1].Control.Approval.EscalationID != "esc-1" || client.requests[1].Control.Approval.Approve {
		t.Fatalf("approval tool did not forward typed decision: %+v", client.requests)
	}
	for _, forged := range []struct{ name, args string }{
		{"session_control", `{"action":"approval","arguments":{"ref":"local:worker","escalationId":"esc-1","approve":true},"actor":{"kind":"human"}}`},
		{"delegate_send", `{"to":"local:worker","input_mode":"start","message":1,"client_mutation_id":"reply-2","expected_instance_id":"instance-1"}`},
	} {
		result := root.reg.ExecuteCall(ctx, root.currentEnv(), llm.ToolCallData{ID: "forged", Name: forged.name, Arguments: json.RawMessage(forged.args)})
		if !result.IsError || len(client.requests) != 2 {
			t.Fatalf("forged Manager argument reached hub: %+v, requests=%+v", result, client.requests)
		}
	}
	client.enabled.Store(false)
	root.refreshHubState(ctx)
	if root.reg.Get("session_control") != nil {
		t.Fatal("approval tool survived Manager disable")
	}
	if result := root.reg.ExecuteCall(ctx, root.currentEnv(), llm.ToolCallData{ID: "disabled", Name: "delegate_send", Arguments: json.RawMessage(`{"to":"local:worker","message":"reply","intent":"Answering a worker question"}`)}); !result.IsError {
		t.Fatal("session input fell back to ordinary owned-delegate messaging")
	}
}

func TestManagerModeKeepsRunningAndIdleOwnedDelegateSend(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "disabled"
		if enabled {
			name = "enabled"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newColdStableDelegateFixture(t, "")
			entered, release := make(chan struct{}), make(chan struct{})
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			fixture.adapter.steps = []func(llm.Request) llm.Response{
				func(llm.Request) llm.Response {
					close(entered)
					<-release
					return finalResponse("first result")
				},
				func(llm.Request) llm.Response { return finalResponse("steering consumed") },
				func(llm.Request) llm.Response { return finalResponse("idle follow-up consumed") },
			}
			root := restoreSupervisionRoot(t, fixture, nil)
			state := &managerStateForUnit{}
			state.enabled.Store(enabled)
			root.SetHubClient(state)
			root.refreshHubState(t.Context())
			start := (delegateRuntime{owner: root}).send(t.Context(), fixture.delegateID, "initial", 0)
			if start.result.Err != nil || start.result.Action != "started" {
				t.Fatalf("start owned delegate: %+v", start.result)
			}
			select {
			case <-entered:
			// TRIPWIRE: scripted model request blocks at this signal only.
			case <-time.After(10 * time.Second):
				t.Fatal("owned delegate never reached running state")
			}
			send := func(message string) {
				t.Helper()
				args, err := json.Marshal(map[string]any{"to": fixture.delegateID, "message": message})
				if err != nil {
					t.Fatal(err)
				}
				result := root.reg.ExecuteCall(t.Context(), root.currentEnv(), llm.ToolCallData{ID: message, Name: "delegate_send", Arguments: args})
				if result.IsError {
					t.Fatalf("owned delegate_send %q with Manager=%v: %+v", message, enabled, result)
				}
			}
			send("while running")
			close(release)
			waitForStableSupervisionRun(t, root, fixture.childID)
			before := delegateAggregateSnapshot(t, root.delegateController, fixture.delegateID).Generation
			send("while idle")
			if after := delegateAggregateSnapshot(t, root.delegateController, fixture.delegateID).Generation; after != before+1 {
				t.Fatalf("idle delegate_send generation = %d, want %d", after, before+1)
			}
			waitForStableSupervisionRun(t, root, fixture.childID)
			if !requestsContain(fixture.adapter.Requests(), "while running") || !requestsContain(fixture.adapter.Requests(), "while idle") {
				t.Fatal("owned running or idle delegate did not consume ordinary message")
			}
		})
	}
}

func (c *managerStateForUnit) State(context.Context) (appwire.ManagerView, error) {
	return appwire.ManagerView{Setting: appwire.ManagerSetting{Enabled: c.enabled.Load()}}, nil
}
func (*managerStateForUnit) Invoke(context.Context, appwire.SessionOperationRequest) (appwire.SessionOperationResponse, error) {
	return appwire.SessionOperationResponse{}, errors.New("unit fixture does not implement hub operations")
}
func (*managerStateForUnit) Watch(context.Context, appwire.SessionWatchRequest) (<-chan appwire.SessionWatchUpdate, error) {
	return nil, errors.New("unit fixture does not implement observations")
}

func TestManagerRequestActualPolicyMatrix(t *testing.T) {
	for _, name := range []string{"job_list", "job_status"} {
		for _, mode := range []string{"present", "absent", "denied", "allowlist", "zero-allowance"} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				adapter := newMessagingAdapter()
				adapter.script("root", func(llm.Request) llm.Response { return finalResponse("before") }, func(llm.Request) llm.Response { return finalResponse("enabled") }, func(llm.Request) llm.Response { return finalResponse("disabled") })
				root := newMessagingRoot(t, adapter, nil)
				root.mu.Lock()
				switch mode {
				case "absent":
					root.reg.Remove(name)
				case "denied":
					root.cfg.spawn.deniedToolNames = []string{name}
					root.reg.Remove(name)
				case "allowlist":
					root.cfg.spawn.allowedToolNames = []string{"communicate"}
					root.reg.Remove(name)
				case "zero-allowance":
					root.delegationAllowance = 0
					root.reg.Remove("delegate")
				}
				root.rebuildToolDefsCache()
				root.mu.Unlock()
				state := &managerStateForUnit{}
				root.SetHubClient(state)
				// TRIPWIRE: three scripted responses finish promptly; this guards a hung request.
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				for _, enabled := range []bool{false, true, false} {
					state.enabled.Store(enabled)
					if _, err := root.ProcessInput(ctx, "capture request", nil); err != nil {
						t.Fatal(err)
					}
					if enabled && (mode == "denied" || mode == "allowlist" || mode == "absent") {
						args := json.RawMessage(`{}`)
						if mode != "absent" {
							if name == "job_list" {
								args = json.RawMessage(`{"hub":{"mode":"discovery"}}`)
							} else {
								args = json.RawMessage(`{"hub":{"ref":"local:a"}}`)
							}
						}
						if result := root.reg.ExecuteCall(ctx, root.currentEnv(), llm.ToolCallData{ID: "policy-execution", Name: name, Arguments: args}); !result.IsError {
							t.Fatalf("unavailable branch executed: %+v", result)
						}
					}
				}
				requests := adapter.waitForRequests(t, "root", 3)
				for index, req := range requests {
					var control *llm.ToolDefinition
					for _, definition := range req.Tools {
						if definition.Name == "session_control" {
							copy := definition
							control = &copy
						}
					}
					if (control != nil) != (index == 1 && mode != "allowlist") {
						t.Fatalf("stage %d: session_control exposure = %v", index, control != nil)
					}
					if control != nil {
						props := control.Parameters["properties"].(map[string]any)
						actions := props["action"].(map[string]any)["enum"]
						encoded, _ := json.Marshal(actions)
						if string(encoded) != `["approval","resume","fork","clear","model","vision_model","reasoning_effort","compact","drain_queue","promote_queued","cancel_queued","rename","goal","human_notes","remove_url","archive","delete","pin","unpin","seen"]` {
							t.Fatalf("session_control actions differ from the twenty implemented owners: %s", encoded)
						}
					}
					if name == "job_list" && mode == "present" {
						stage := []string{"off", "on", "disabled"}[index]
						agenttest.CheckGolden(t, filepath.Join("testdata", "messagingprompt", "manager-"+stage+".md"), managerRequestGolden(req), *updatePromptGoldens, "Regenerate with go test ./ -run TestManagerRequestActualPolicyMatrix -update-prompt and inspect the diff.")
					}
					var found *llm.ToolDefinition
					for _, def := range req.Tools {
						if def.Name == name {
							copy := def
							found = &copy
						}
					}
					denied := mode == "denied" || mode == "allowlist"
					expected := !denied && (mode != "absent" || index == 1)
					if (found != nil) != expected {
						t.Fatalf("stage %d, exposed=%v want %v", index, found != nil, expected)
					}
					if found != nil {
						_, hub := found.Parameters["properties"].(map[string]any)["hub"]
						if hub != (index == 1) {
							t.Fatalf("stage %d hub=%v", index, hub)
						}
					}
					if mode == "zero-allowance" {
						for _, def := range req.Tools {
							if def.Name == "delegate" {
								t.Fatal("Manager granted ordinary delegation")
							}
						}
					}
				}
				// Execution still rejects a stale-schema forged branch after disable.
				call := llm.ToolCallData{ID: "forged-after-disable", Name: name, Arguments: json.RawMessage(`{"hub":{"mode":"discovery","ref":"local:a"}}`)}
				if result := root.reg.ExecuteCall(ctx, root.currentEnv(), call); !result.IsError {
					t.Fatal("stale Manager call executed after disable")
				}
				if mode == "absent" && root.reg.Get(name) != nil {
					t.Fatal("synthetic registration survived disable")
				}
			})
		}
	}
}

func managerRequestGolden(request llm.Request) []byte {
	var out strings.Builder
	for _, message := range request.Messages {
		if message.Role == llm.RoleSystem {
			out.WriteString(message.Text())
			out.WriteByte('\n')
		}
	}
	// Runtime facts and installed skill paths vary by machine, not by Manager mode.
	text := regexp.MustCompile(`(?s)<environment>.*?</environment>`).ReplaceAllString(out.String(), "<environment>\n[runtime facts]\n</environment>")
	text = regexp.MustCompile(`\[/[^\]\n]+/doctoring-evener\]`).ReplaceAllString(text, "[skill-path/doctoring-evener]")
	var defs []llm.ToolDefinition
	for _, definition := range request.Tools {
		if definition.Name == "job_list" || definition.Name == "job_status" {
			defs = append(defs, definition)
		}
	}
	schemas, _ := json.MarshalIndent(defs, "", "  ")
	return []byte(text + "\n# Implemented Manager tool schemas\n\n```json\n" + string(schemas) + "\n```\n")
}

// These are composition/execution unit tests. The hub package separately
// proves actual provider requests and tool reads over real authenticated sockets.
func TestManagerRequestCompositionMatrix(t *testing.T) {
	for _, name := range []string{"job_list", "job_status"} {
		for _, mode := range []string{"present", "absent", "denied", "allowlist", "ceiling", "profile-omitted", "zero-allowance"} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				root := newSession(t)
				baseline := root.reg.Get(name)
				root.mu.Lock()
				switch mode {
				case "absent":
					root.reg.Remove(name)
				case "denied":
					root.cfg.spawn.deniedToolNames = []string{name}
					root.reg.Remove(name)
				case "allowlist":
					root.cfg.spawn.allowedToolNames = []string{"communicate"}
					root.reg.Remove(name)
				case "ceiling":
					root.cfg.spawn.toolNameCeiling = []string{"communicate"}
					root.reg.Remove(name)
				case "profile-omitted":
					root.profile = &provider.Profile{}
				case "zero-allowance":
					root.delegationAllowance = 0
					root.reg.Remove("delegate")
				}
				root.hubEnabled = true
				root.rebuildToolDefsCache()
				root.mu.Unlock()
				registered := root.reg.Get(name)
				denied := mode == "denied" || mode == "allowlist" || mode == "ceiling"
				if denied {
					if registered != nil {
						t.Fatal("explicitly denied tool was restored")
					}
					if _, err := root.invokeHubRead(t.Context(), name, map[string]any{"hub": map[string]any{"mode": "discovery", "ref": "local:a"}}); err == nil {
						t.Fatal("denied execution accepted")
					}
					for _, def := range root.ToolDefinitions() {
						if def.Name == name {
							t.Fatal("denied tool exposed")
						}
					}
				} else {
					if registered == nil {
						t.Fatal("Manager tool missing")
					}
					hub := map[string]any{"mode": "discovery"}
					ordinary := map[string]any{}
					mixed := map[string]any{"hub": hub, "limit": 1}
					if name == "job_status" {
						hub = map[string]any{"ref": "local:a"}
						ordinary["target"] = "job_test"
						mixed = map[string]any{"hub": hub, "target": "job_test"}
					}
					if err := registered.Schema.Validate(map[string]any{"hub": hub}); err != nil {
						t.Fatalf("Manager schema: %v", err)
					}
					if err := registered.Schema.Validate(mixed); err == nil {
						t.Fatal("schema accepts mixed arguments")
					}
					onlyManager := mode == "absent" || mode == "profile-omitted"
					if err := registered.Schema.Validate(ordinary); (err != nil) != onlyManager {
						t.Fatalf("ordinary schema, mode %s: %v", mode, err)
					}
					if onlyManager {
						if _, err := registered.Exec(t.Context(), root.currentEnv(), ordinary); err == nil {
							t.Fatal("synthetic registration grants ordinary execution")
						}
					}
					if mode == "zero-allowance" && root.reg.Get("delegate") != nil {
						t.Fatal("Manager granted ordinary delegation")
					}
				}
				root.mu.Lock()
				root.hubEnabled = false
				root.rebuildToolDefsCache()
				root.mu.Unlock()
				after := root.reg.Get(name)
				if mode == "absent" || denied {
					if after != nil {
						t.Fatal("synthetic registration survived disable")
					}
				} else {
					if after == nil {
						t.Fatal("ordinary registration removed")
					}
					if _, ok := after.Definition.Parameters["properties"].(map[string]any)["hub"]; ok {
						t.Fatal("Manager schema survived disable")
					}
					if after.Limit != baseline.Limit {
						t.Fatal("ordinary output limit changed")
					}
				}
			})
		}
	}
}
