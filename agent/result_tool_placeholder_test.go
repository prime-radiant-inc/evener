package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

// profilePlaceholderPointer identifies the executor buildProfileToolRegistry
// installs for profile definitions it seeds. Every placeholder uses that one
// executor (the named profilePlaceholderExecutor), so a registered tool whose
// Exec has this code pointer was never wired.
func profilePlaceholderPointer(t *testing.T) uintptr {
	t.Helper()
	reg := buildProfileToolRegistry([]llm.ToolDefinition{{Name: "placeholder_probe", Description: "placeholder probe"}})
	probe := reg.Get("placeholder_probe")
	if probe == nil || probe.Exec == nil {
		t.Fatal("buildProfileToolRegistry did not seed the probe tool")
	}
	return reflect.ValueOf(probe.Exec).Pointer()
}

// TestResultToolAdvertisementHasNoUnwiredPlaceholder pins issue #4906: renaming
// the result tool must replace the canonical communicate registration, not
// leave it advertised alongside the configured name with the profile registry's
// placeholder executor. The default name keeps its canonical registration.
func TestResultToolAdvertisementHasNoUnwiredPlaceholder(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		cfg          SessionConfig
		wantResult   string
		absentResult string
	}{
		{"empty name keeps communicate", SessionConfig{}, tool.DefaultResultToolName, ""},
		{"explicit default keeps communicate", SessionConfig{ResultToolName: tool.DefaultResultToolName}, tool.DefaultResultToolName, ""},
		{"renamed result tool replaces communicate", SessionConfig{ResultToolName: "respond"}, "respond", tool.DefaultResultToolName},
	}
	placeholder := profilePlaceholderPointer(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sess, err := newResultToolSession(t, NewOpenAIProfile("gpt-5.2"), tc.cfg)
			if err != nil {
				t.Fatalf("NewSession: %v", err)
			}
			t.Cleanup(func() { sess.Close() })

			advertised := sess.ToolDefinitions()
			names := make(map[string]bool, len(advertised))
			for _, def := range advertised {
				names[def.Name] = true
			}
			if !names[tc.wantResult] {
				t.Fatalf("advertised tools do not include the configured result tool %q: %v", tc.wantResult, names)
			}
			if tc.absentResult != "" {
				if names[tc.absentResult] {
					t.Errorf("advertised tools include %q alongside the configured result tool %q", tc.absentResult, tc.wantResult)
				}
				if sess.reg.Get(tc.absentResult) != nil {
					t.Errorf("registry still holds %q alongside the configured result tool %q", tc.absentResult, tc.wantResult)
				}
			}
			requireResultToolShape(t, sess, tc.wantResult)
			for _, def := range advertised {
				if def.Name != tc.wantResult {
					continue
				}
				props, _ := def.Parameters["properties"].(map[string]any)
				for _, want := range []string{"message", "end_turn"} {
					if _, ok := props[want]; !ok {
						t.Errorf("advertised result tool %q lacks communicate's %q parameter: %v", tc.wantResult, want, props)
					}
				}
				break
			}

			res := sess.reg.ExecuteCall(context.Background(), sess.currentEnv(), llm.ToolCallData{
				ID:        "result-tool-call",
				Name:      tc.wantResult,
				Arguments: json.RawMessage(`{"message":"advertised result tool","end_turn":true,"output":{"message":"","data":{},"artifacts":[]}}`),
			})
			if res.IsError {
				t.Fatalf("dispatching the configured result tool %q failed: %s", tc.wantResult, res.Output)
			}
			var body map[string]any
			if err := json.Unmarshal([]byte(res.Output), &body); err != nil {
				t.Fatalf("decode result tool output %q: %v", res.Output, err)
			}
			if body["accepted"] != true {
				t.Fatalf("result tool %q was not accepted: %v", tc.wantResult, body)
			}
			if !sess.Communicated() {
				t.Fatalf("dispatching the configured result tool %q did not capture a result", tc.wantResult)
			}

			// The prompt's unavailable-provider-tool list compares profile tools
			// with the advertised set; the renamed result tool must not make the
			// canonical name look unavailable.
			data, _ := sess.buildPromptData(sess.currentEnv())
			if slices.Contains(data.UnavailableProfileToolNames, tool.DefaultResultToolName) {
				t.Errorf("prompt lists %q as an unavailable provider tool although the configured result tool is %q", tool.DefaultResultToolName, tc.wantResult)
			}

			canonicalByWire := make(map[string]string)
			for canonical, wire := range sess.profile.ToolNameMap() {
				canonicalByWire[wire] = canonical
			}
			for _, def := range advertised {
				canonical := def.Name
				if mapped, ok := canonicalByWire[def.Name]; ok {
					canonical = mapped
				}
				registered := sess.reg.Get(canonical)
				if registered == nil || registered.Exec == nil {
					t.Errorf("advertised tool %q has no registered executor", def.Name)
					continue
				}
				if reflect.ValueOf(registered.Exec).Pointer() == placeholder {
					t.Errorf("advertised tool %q keeps the profile registry's placeholder executor", def.Name)
				}
			}
		})
	}
}

// TestInvalidResultToolNameRefusesSession pins the configuration-error half of
// issue #4906's fix: ResultToolName is validated only when the result tool is
// registered, so a failed registration must refuse session construction rather
// than leave a session whose advertised set has no result tool at all.
func TestInvalidResultToolNameRefusesSession(t *testing.T) {
	t.Parallel()
	sess, err := newResultToolSession(t, NewOpenAIProfile("gpt-5.2"), SessionConfig{ResultToolName: "respond-result"})
	if err == nil {
		var names []string
		for _, def := range sess.ToolDefinitions() {
			names = append(names, def.Name)
		}
		sess.Close()
		t.Fatalf("NewSession accepted the invalid result tool name %q; advertised tools: %v", "respond-result", names)
	}
	if _, ok := errors.AsType[*llm.ConfigurationError](err); !ok {
		t.Fatalf("NewSession error is %T, want a *llm.ConfigurationError for the invalid tool name", err)
	}
}
