package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"slices"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/sandbox"
)

func TestStableDelegateCreateTool_UnsupportedSandboxReportsInvalidParameters(t *testing.T) {
	_, home := sbxLane(t)
	s := sbxDelegateSession(t, sandbox.HostFacts{OS: "linux", Home: home})

	_, err := stableDelegateCreateTool(context.Background(), s, map[string]any{
		"task":        "do work",
		"sandbox":     "read-only",
		"sandbox_net": true,
	}, 8192)
	requireDelegateSandboxRequestError(t, err, []string{"sandbox", "sandbox_net"})
	var refusal *sandbox.RefusalError
	if !errors.As(err, &refusal) {
		t.Fatalf("error type = %T, want wrapped *sandbox.RefusalError", err)
	}
	if refusal.Mode != sandbox.ModeReadOnly || !refusal.Net {
		t.Fatalf("sandbox refusal = {Mode:%v Net:%v}, want {Mode:%v Net:true}", refusal.Mode, refusal.Net, sandbox.ModeReadOnly)
	}
	requireNoDelegateLaunch(t, s)
}

func requireNoDelegateLaunch(t *testing.T, s *Session) {
	t.Helper()
	c := s.delegateController
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.durable) != 0 || len(c.live) != 0 || len(c.reservations) != 0 {
		t.Fatalf("delegate controller state after rejection = {durable:%d live:%d reservations:%d}, want all zero", len(c.durable), len(c.live), len(c.reservations))
	}
}

func requireDelegateSandboxRequestError(t *testing.T, err error, invalidParameters []string) {
	t.Helper()
	if err == nil {
		t.Fatal("sandbox request was accepted")
	}
	var requestErr *delegateSandboxRequestError
	if !errors.As(err, &requestErr) {
		t.Fatalf("error type = %T, want *delegateSandboxRequestError", err)
	}
	if requestErr.Class != delegateSandboxErrorClassInvalidRequest {
		t.Errorf("error class = %q, want %q", requestErr.Class, delegateSandboxErrorClassInvalidRequest)
	}
	if !reflect.DeepEqual(requestErr.InvalidParameters, invalidParameters) {
		t.Errorf("invalid parameters = %#v, want %#v", requestErr.InvalidParameters, invalidParameters)
	}
}

func delegateDefinitionProperties(t *testing.T, s *Session) map[string]any {
	t.Helper()
	params := delegateDefinitionParameters(t, s)
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("delegate properties = %T, want map[string]any", params["properties"])
	}
	return props
}

func delegateDefinitionParameters(t *testing.T, s *Session) map[string]any {
	t.Helper()
	s.rebuildToolDefsCache()
	for _, def := range s.cachedToolDefs {
		if def.Name != "delegate" {
			continue
		}
		return def.Parameters
	}
	t.Fatal("delegate definition not found")
	return nil
}

func compileDelegateParameters(t *testing.T, parameters map[string]any) *jsonschema.Schema {
	t.Helper()
	encoded, err := json.Marshal(parameters)
	if err != nil {
		t.Fatalf("marshal delegate parameters: %v", err)
	}
	compiler := jsonschema.NewCompiler()
	const resource = "mem://delegate-parameters.json"
	if err := compiler.AddResource(resource, bytes.NewReader(encoded)); err != nil {
		t.Fatalf("add delegate parameter schema: %v", err)
	}
	compiled, err := compiler.Compile(resource)
	if err != nil {
		t.Fatalf("compile delegate parameter schema: %v", err)
	}
	return compiled
}

func requireDelegateSchemaValidation(t *testing.T, compiled *jsonschema.Schema, args map[string]any, wantValid bool) {
	t.Helper()
	err := compiled.Validate(args)
	if gotValid := err == nil; gotValid != wantValid {
		t.Fatalf("delegate schema valid = %v, want %v (error: %v)", gotValid, wantValid, err)
	}
}

func TestDelegateSchemaOmitsUnsupportedSandboxControls(t *testing.T) {
	_, home := sbxLane(t)
	s := sbxDelegateSession(t, sandbox.HostFacts{OS: "linux", Home: home})
	params := delegateDefinitionParameters(t, s)
	props := delegateDefinitionProperties(t, s)
	for _, name := range []string{"sandbox", "sandbox_net"} {
		if _, ok := props[name]; ok {
			t.Errorf("unsupported-host schema exposes %q", name)
		}
	}
	compiled := compileDelegateParameters(t, params)
	requireDelegateSchemaValidation(t, compiled, map[string]any{"task": "do work"}, true)
	requireDelegateSchemaValidation(t, compiled, map[string]any{"task": "do work", "sandbox": "off"}, false)
	requireDelegateSchemaValidation(t, compiled, map[string]any{"task": "do work", "sandbox_net": false}, false)
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
			params := delegateDefinitionParameters(t, s)
			props := delegateDefinitionProperties(t, s)
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
			_, hasConditionalNetworkRule := params["oneOf"]
			if got, want := hasConditionalNetworkRule, tc.parentMode == sandbox.ModeOff; got != want {
				t.Fatalf("off-parent network condition present = %v, want %v", got, want)
			}

			compiled := compileDelegateParameters(t, params)
			for _, mode := range []string{"off", "read-only", "workspace-write", "restricted"} {
				requireDelegateSchemaValidation(t, compiled, map[string]any{"task": "do work", "sandbox": mode}, slices.Contains(tc.wantModes, mode))
			}
			nonOffMode := tc.wantModes[len(tc.wantModes)-1]
			for _, network := range []bool{false, true} {
				wantValid := len(tc.wantNetworks) == 0 || slices.Contains(tc.wantNetworks, network)
				requireDelegateSchemaValidation(t, compiled, map[string]any{"task": "do work", "sandbox": nonOffMode, "sandbox_net": network}, wantValid)
			}
			if tc.parentMode == sandbox.ModeOff {
				requireDelegateSchemaValidation(t, compiled, map[string]any{"task": "do work", "sandbox": "off", "sandbox_net": false}, false)
				requireDelegateSchemaValidation(t, compiled, map[string]any{"task": "do work", "sandbox_net": false}, false)
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
		name              string
		parentMode        sandbox.Mode
		parentNet         bool
		args              map[string]any
		invalidParameters []string
	}{
		{
			name:              "off with explicit network",
			parentMode:        sandbox.ModeOff,
			parentNet:         true,
			args:              map[string]any{"sandbox": "off", "sandbox_net": false},
			invalidParameters: []string{"sandbox", "sandbox_net"},
		},
		{
			name:              "looser mode than parent",
			parentMode:        sandbox.ModeRestricted,
			parentNet:         true,
			args:              map[string]any{"sandbox": "workspace-write"},
			invalidParameters: []string{"sandbox"},
		},
		{
			name:              "network escalation",
			parentMode:        sandbox.ModeWorkspaceWrite,
			parentNet:         false,
			args:              map[string]any{"sandbox": "restricted", "sandbox_net": true},
			invalidParameters: []string{"sandbox_net"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lane, home := sbxLane(t)
			s := sbxDelegateSession(t, sbxBwrapFacts(home))
			setParentSandboxForContract(t, s, sbxBwrapFacts(home), lane, tc.parentMode, tc.parentNet)
			args := map[string]any{"task": "do work"}
			maps.Copy(args, tc.args)
			_, err := stableDelegateCreateTool(context.Background(), s, args, 8192)
			requireDelegateSandboxRequestError(t, err, tc.invalidParameters)
			requireNoDelegateLaunch(t, s)
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
	requireDelegateSandboxRequestError(t, err, []string{"sandbox_net"})
	requireNoDelegateLaunch(t, s)
}
