package agent

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/agent/internal/tool"
)

func TestManagerQuestionSchemaExponentBoundary(t *testing.T) {
	for _, name := range []string{"delegate_send", "job_status"} {
		for _, number := range []json.Number{"1e1000001", "1e-1000001"} {
			for _, route := range []string{"registered", "prepared", "repaired", "hook"} {
				t.Run(name+"/"+number.String()+"/"+route, func(t *testing.T) {
					call, _ := numberHookOriginalCall(t, name)
					var args map[string]any
					if err := json.Unmarshal(call.Arguments, &args); err != nil {
						t.Fatal(err)
					}
					fields := args
					if name == "job_status" {
						fields = args["hub"].(map[string]any)
					}
					fields["expected_ask_revision"] = number
					cfg := SessionConfig{Manager: true}
					if route == "hook" {
						pluginDir, _ := numberHookPlugin(t, args)
						cfg.PluginDirs = []string{pluginDir}
					} else {
						call.Arguments = []byte(numberHookJSON(t, args))
					}
					root := newMessagingRoot(t, newMessagingAdapter(), func(out *SessionConfig) {
						out.Manager, out.PluginDirs = cfg.Manager, cfg.PluginDirs
					})
					client := &managerQuestionClient{}
					root.SetHubClient(client)
					defer func() {
						if p := recover(); p != nil {
							t.Errorf("selected number panicked on %s: %T", route, p)
						}
						if len(client.requests) != 0 {
							t.Errorf("invalid selected number forwarded: %s", numberHookJSON(t, client.requests))
						}
					}()
					registered := root.reg.Get(name)
					var result tool.ExecResult
					switch route {
					case "registered":
						result = root.reg.ExecuteCall(t.Context(), root.currentEnv(), call)
					case "prepared", "repaired":
						if route == "repaired" {
							call.Arguments = call.Arguments[:len(call.Arguments)-1]
						}
						prepared := prepareToolCall(call, registered, []string{name}, name, "communicate", "")
						if prepared.PrevalErr != "" {
							return
						}
						result = root.reg.ExecutePreparedCall(t.Context(), root.currentEnv(), prepared.Call)
					case "hook":
						result = root.execTool(t.Context(), call, "")
					}
					if !result.IsError {
						t.Errorf("invalid selected number was not refused on %s", route)
					}
				})
			}
		}
	}
}
