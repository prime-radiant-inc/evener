package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/sandbox"
)

func TestStableDelegateCreateTool_UnsupportedSandboxNamesAllRequiredChanges(t *testing.T) {
	_, home := sbxLane(t)
	s := sbxDelegateSession(t, sandbox.HostFacts{OS: "linux", Home: home})

	raw, err := stableDelegateCreateTool(context.Background(), s, map[string]any{
		"task":        "do work",
		"sandbox":     "read-only",
		"sandbox_net": true,
	}, 8192)
	message := ""
	if err != nil {
		message = err.Error()
	} else {
		var result map[string]any
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			t.Fatalf("unmarshal delegate result: %v (%s)", err, raw)
		}
		message, _ = result["error"].(string)
	}
	if !strings.Contains(message, "only --sandbox off is supported") {
		t.Fatalf("sandbox refusal = %q, want host capability refusal", message)
	}
	if !strings.Contains(message, `change sandbox to "off"`) || !strings.Contains(message, "omit sandbox_net") {
		t.Fatalf("first sandbox refusal = %q, want both sandbox mode change and sandbox_net omission", message)
	}
}

func delegateDefinitionProperties(t *testing.T, s *Session) (map[string]any, string) {
	t.Helper()
	params, description := delegateDefinitionParameters(t, s)
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("delegate properties = %T, want map[string]any", params["properties"])
	}
	return props, description
}

func delegateDefinitionParameters(t *testing.T, s *Session) (map[string]any, string) {
	t.Helper()
	s.rebuildToolDefsCache()
	for _, def := range s.cachedToolDefs {
		if def.Name != "delegate" {
			continue
		}
		return def.Parameters, def.Description
	}
	t.Fatal("delegate definition not found")
	return nil, ""
}

func TestDelegateSchemaOmitsUnsupportedSandboxControls(t *testing.T) {
	_, home := sbxLane(t)
	s := sbxDelegateSession(t, sandbox.HostFacts{OS: "linux", Home: home})
	props, description := delegateDefinitionProperties(t, s)
	for _, name := range []string{"sandbox", "sandbox_net"} {
		if _, ok := props[name]; ok {
			t.Errorf("unsupported-host schema exposes %q", name)
		}
	}
	if !strings.Contains(description, "cannot enforce per-delegate sandboxing") {
		t.Fatalf("unsupported-host description = %q, want capability explanation", description)
	}
}

func TestDelegateSchemaHonorsParentConfinementAndNetwork(t *testing.T) {
	tests := []struct {
		name          string
		parentMode    sandbox.Mode
		parentNetwork bool
		wantModes     []string
		wantNetworks  []bool
	}{
		{name: "off parent", parentMode: sandbox.ModeOff, parentNetwork: true, wantModes: []string{"off", "read-only", "workspace-write", "restricted"}},
		{name: "read-only parent", parentMode: sandbox.ModeReadOnly, parentNetwork: true, wantModes: []string{"read-only"}},
		{name: "workspace-write parent", parentMode: sandbox.ModeWorkspaceWrite, parentNetwork: true, wantModes: []string{"read-only", "workspace-write", "restricted"}},
		{name: "restricted parent", parentMode: sandbox.ModeRestricted, parentNetwork: true, wantModes: []string{"restricted"}},
		{name: "network-off parent", parentMode: sandbox.ModeWorkspaceWrite, parentNetwork: false, wantModes: []string{"read-only", "workspace-write", "restricted"}, wantNetworks: []bool{false}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lane, home := sbxLane(t)
			s := sbxDelegateSession(t, sbxBwrapFacts(home))
			setParentSandboxForContract(t, s, sbxBwrapFacts(home), lane, tc.parentMode, tc.parentNetwork)
			props, _ := delegateDefinitionProperties(t, s)
			sandboxSchema := props["sandbox"].(map[string]any)
			if got := sandboxSchema["enum"]; !reflect.DeepEqual(got, tc.wantModes) {
				t.Fatalf("sandbox enum = %#v, want %#v", got, tc.wantModes)
			}
			netSchema := props["sandbox_net"].(map[string]any)
			if len(tc.wantNetworks) == 0 {
				if _, ok := netSchema["enum"]; ok {
					t.Fatalf("sandbox_net unexpectedly constrained: %#v", netSchema["enum"])
				}
			} else if got := netSchema["enum"]; !reflect.DeepEqual(got, tc.wantNetworks) {
				t.Fatalf("sandbox_net enum = %#v, want %#v", got, tc.wantNetworks)
			}
			params, _ := delegateDefinitionParameters(t, s)
			_, hasConditionalNetworkRule := params["oneOf"]
			if got, want := hasConditionalNetworkRule, tc.parentMode == sandbox.ModeOff; got != want {
				t.Fatalf("off-parent network condition present = %v, want %v", got, want)
			}
		})
	}
}

func setParentSandboxForContract(t *testing.T, s *Session, facts sandbox.HostFacts, cwd string, mode sandbox.Mode, network bool) {
	t.Helper()
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: mode, Network: &network}, facts, cwd)
	if err != nil {
		t.Fatalf("resolve parent sandbox: %v", err)
	}
	env := execenv.NewLocalExecutionEnvironment(cwd)
	env.Sandbox = &rp
	s.mu.Lock()
	s.env = env
	s.mu.Unlock()
}

func TestStableDelegateCreateTool_RejectsAffectedSandboxCombinations(t *testing.T) {
	tests := []struct {
		name       string
		parentMode sandbox.Mode
		parentNet  bool
		args       map[string]any
		want       []string
	}{
		{
			name:       "off with explicit network",
			parentMode: sandbox.ModeOff,
			parentNet:  true,
			args:       map[string]any{"sandbox": "off", "sandbox_net": false},
			want:       []string{`no effect with sandbox="off"`, "omit sandbox_net"},
		},
		{
			name:       "looser mode than parent",
			parentMode: sandbox.ModeRestricted,
			parentNet:  true,
			args:       map[string]any{"sandbox": "workspace-write"},
			want:       []string{"not at least as confining", "restricted"},
		},
		{
			name:       "network escalation",
			parentMode: sandbox.ModeWorkspaceWrite,
			parentNet:  false,
			args:       map[string]any{"sandbox": "restricted", "sandbox_net": true},
			want:       []string{"network off", "sandbox_net=false"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lane, home := sbxLane(t)
			s := sbxDelegateSession(t, sbxBwrapFacts(home))
			setParentSandboxForContract(t, s, sbxBwrapFacts(home), lane, tc.parentMode, tc.parentNet)
			args := map[string]any{"task": "do work"}
			for key, value := range tc.args {
				args[key] = value
			}
			_, err := stableDelegateCreateTool(context.Background(), s, args, 8192)
			if err == nil {
				t.Fatal("invalid sandbox combination was accepted")
			}
			message := err.Error()
			for _, want := range tc.want {
				if !strings.Contains(message, want) {
					t.Errorf("error = %q, want substring %q", message, want)
				}
			}
		})
	}
}

func TestStableDelegateCreateTool_UnsupportedHostRejectsNetworkOnly(t *testing.T) {
	_, home := sbxLane(t)
	s := sbxDelegateSession(t, sandbox.HostFacts{OS: "linux", Home: home})
	_, err := stableDelegateCreateTool(context.Background(), s, map[string]any{
		"task":        "do work",
		"sandbox_net": false,
	}, 8192)
	if err == nil {
		t.Fatal("unsupported-host sandbox_net-only request was accepted")
	}
	message := err.Error()
	for _, want := range []string{"cannot be enforced", "no sandbox backend", "omit sandbox_net"} {
		if !strings.Contains(message, want) {
			t.Errorf("error = %q, want substring %q", message, want)
		}
	}
}
