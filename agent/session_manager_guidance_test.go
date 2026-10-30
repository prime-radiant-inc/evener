package agent

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/llm"
)

func TestManagerRequestAllBranches(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, manager, ordinary string
	}{
		{"job_list", `{"hub":{"mode":"discovery"}}`, `{}`},
		{"job_status", `{"hub":{"ref":"local:worker"}}`, `{"target":"job_worker"}`},
		{"read_transcript", `{"hub":{"ref":"local:worker"}}`, `{"transcript_ref":"job:job_worker"}`},
		{"model_list", `{"hub":{"host":"local"}}`, `{}`},
		{"job_watch", `{"operation":"create","hub":{"ref":"local:worker","completion":true}}`, `{"operation":"create","source":"self","after_seconds":60}`},
		{"delegate_send", `{"to":"local:worker","input_mode":"start","message":"sentinel","client_mutation_id":"m1","expected_instance_id":"i1"}`, `{"to":"dlg_worker","message":"sentinel"}`},
		{"delegate", `{"prompt":"sentinel","hub":{"cwd":"/workspace","model":"fake/model","watch":{"events":[],"completion":true}}}`, `{"prompt":"sentinel"}`},
		{"job_stop", `{"hub":{"mode":"shutdown","arguments":{"ref":"local:worker"}}}`, `{"target":"job_worker"}`},
		{"session_control", `{"action":"rename","arguments":{"ref":"local:worker","name":"sentinel"}}`, ""},
	}
	for _, mode := range []string{"all", "read-only", "control-only", "none", "denied", "ordinary"} {
		t.Run(mode, func(t *testing.T) {
			allowed := []string(nil)
			switch mode {
			case "read-only":
				allowed = []string{"communicate", "job_list", "job_status", "read_transcript"}
			case "control-only":
				allowed = []string{"communicate", "session_control"}
			case "none":
				allowed = []string{"communicate"}
			}
			adapter := newMessagingAdapter()
			adapter.script("root", func(llm.Request) llm.Response { return agenttest.FinalResponse("capture") })
			root := newMessagingRoot(t, adapter, func(cfg *SessionConfig) {
				cfg.Manager = mode != "ordinary"
				cfg.spawn.allowedToolNames = allowed
				if mode == "denied" {
					for _, tc := range cases {
						cfg.spawn.deniedToolNames = append(cfg.spawn.deniedToolNames, tc.name)
					}
				}
			})
			if _, err := root.ProcessInput(t.Context(), "capture", nil); err != nil {
				t.Fatal(err)
			}
			req := adapter.waitForRequests(t, "root", 1)[0]
			if mode == "all" || mode == "ordinary" {
				if !slices.ContainsFunc(req.Tools, func(td llm.ToolDefinition) bool { return td.Name == "exec_command" }) || slices.ContainsFunc(req.Tools, func(td llm.ToolDefinition) bool { return td.Name == "shell" }) {
					t.Fatal("provider tool renaming changed")
				}
			}
			if mode != "all" && mode != "ordinary" {
				agenttest.CheckGolden(t, filepath.Join("testdata", "messagingprompt", "manager-"+mode+".md"), managerRequestGolden(req), *updatePromptGoldens, "Regenerate with go test ./agent -run TestManagerRequestAllBranches -update-prompt and review the diff.")
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					index := slices.IndexFunc(req.Tools, func(td llm.ToolDefinition) bool { return td.Name == tc.name })
					want := mode != "denied" && (allowed == nil || slices.Contains(allowed, tc.name)) && (mode != "ordinary" || tc.ordinary != "")
					if (index >= 0) != want {
						t.Fatalf("advertised=%t, want %t", index >= 0, want)
					}
					if index < 0 {
						return
					}
					compiled := compileProjectionSchema(t, req.Tools[index].Parameters)
					validate := func(raw string) error {
						var args map[string]any
						if err := json.Unmarshal([]byte(raw), &args); err != nil {
							t.Fatal(err)
						}
						args["intent"] = "sentinel"
						return compiled.Validate(args)
					}
					if err := validate(tc.manager); mode != "ordinary" && err != nil {
						t.Errorf("advertised schema rejects implemented Manager branch: %v", err)
					} else if mode == "ordinary" && err == nil {
						t.Error("ordinary request advertises Manager branch")
					}
					if tc.ordinary != "" {
						// These directly registered reads are absent from the provider
						// profile, so Manager composition offers only their hub branch.
						ordinary := mode == "ordinary" || tc.name != "read_transcript" && tc.name != "model_list"
						if err := validate(tc.ordinary); (err == nil) != ordinary {
							t.Errorf("ordinary branch lost: %v", err)
						}
					}
				})
			}
		})
	}
}
