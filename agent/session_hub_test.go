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
