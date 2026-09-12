package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"reflect"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/llm"
)

func execDelegateForContract(t *testing.T, s *Session, args map[string]any) tool.ExecResult {
	t.Helper()
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal delegate args: %v", err)
	}
	return s.execTool(context.Background(), llm.ToolCallData{
		ID:        "delegate-sandbox-contract",
		Name:      "delegate",
		Arguments: encoded,
	}, "")
}

func requireDelegateSandboxExecError(t *testing.T, result tool.ExecResult, invalidParameters []string) {
	t.Helper()
	if !result.IsError {
		t.Fatalf("delegate exec result IsError = false; result = %#v", result)
	}
	if !result.PrevalOnly {
		t.Fatalf("delegate exec result PrevalOnly = false; result = %#v", result)
	}
	requireDelegateSandboxRequestError(t, result.Err, invalidParameters)
}

func requireNoDelegatePreRepairState(t *testing.T, s *Session) {
	t.Helper()
	requireNoDelegateLaunch(t, s)

	c := s.delegateController
	c.mu.Lock()
	admissionCounts := map[string]int{
		"reservations":      len(c.reservations),
		"input_claims":      len(c.inputClaims),
		"steering_claims":   len(c.steeringClaims),
		"model_claims":      len(c.modelClaims),
		"settlement_claims": len(c.settlementClaims),
		"work":              len(c.work),
		"deliveries":        len(c.deliveries),
		"delivery_claims":   len(c.deliveryClaims),
		"quiet_claims":      len(c.quietClaims),
		"watch_enqueues":    len(c.watchEnqueues),
		"watch_deliveries":  len(c.watchDeliveries),
		"reclamations":      len(c.reclamations),
		"reclaiming":        len(c.reclaiming),
		"run_starts":        len(c.runStarts),
	}
	turnsInUse := c.turnsInUse
	drivesInUse := c.drivesInUse
	nextToken := c.nextToken
	worktreeRoot := c.worktreeRoot
	c.mu.Unlock()
	for name, count := range admissionCounts {
		if count != 0 {
			t.Errorf("delegate controller %s = %d, want 0", name, count)
		}
	}
	if turnsInUse != 0 || drivesInUse != 0 || nextToken != 0 {
		t.Errorf("delegate controller admission counters = {turns:%d drives:%d next_token:%d}, want zero", turnsInUse, drivesInUse, nextToken)
	}

	events, err := c.store.Load()
	if err != nil {
		t.Fatalf("load durable delegate events: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("durable delegate events = %d, want 0", len(events))
	}

	if s.subagents != nil {
		s.subagents.mu.Lock()
		liveRuntimes := len(s.subagents.subs)
		reconstructions := len(s.subagents.reconstructing)
		s.subagents.mu.Unlock()
		if liveRuntimes != 0 || reconstructions != 0 {
			t.Errorf("subagent runtime state = {live:%d reconstructing:%d}, want zero", liveRuntimes, reconstructions)
		}
	}

	if worktreeRoot != "" {
		entries, err := os.ReadDir(worktreeRoot)
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("read delegate worktree root: %v", err)
		}
		if len(entries) != 0 {
			t.Errorf("delegate worktree entries = %d, want 0", len(entries))
		}
	}
}

func TestExecTool_UnsupportedHostRejectsExplicitSandboxControlsBeforeRepair(t *testing.T) {
	tests := []struct {
		name              string
		args              map[string]any
		invalidParameters []string
	}{
		{
			name:              "sandbox",
			args:              map[string]any{"sandbox": "read-only"},
			invalidParameters: []string{"sandbox"},
		},
		{
			name:              "sandbox with nonet suffix",
			args:              map[string]any{"sandbox": "read-only+nonet"},
			invalidParameters: []string{"sandbox"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			s := sbxDelegateSession(t, sandbox.HostFacts{OS: "linux", Home: home})
			args := map[string]any{"prompt": "do work"}
			maps.Copy(args, tc.args)

			result := execDelegateForContract(t, s, args)
			requireDelegateSandboxExecError(t, result, tc.invalidParameters)
			requireNoDelegatePreRepairState(t, s)
		})
	}
}

func TestExecTool_UnsupportedHostPreservesBenignRepairWithSandboxControlsOmitted(t *testing.T) {
	home := t.TempDir()
	s := sbxDelegateSession(t, sandbox.HostFacts{OS: "linux", Home: home})
	result := execDelegateForContract(t, s, map[string]any{
		"prompt":              "do work",
		"unrelated_extra_arg": true,
	})
	if result.IsError {
		t.Fatalf("delegate exec with omitted controls and repairable extra arg failed: %#v", result)
	}
	var created stableDelegateCreateResult
	if err := json.Unmarshal([]byte(result.FullOutput), &created); err != nil {
		t.Fatalf("decode delegate create result: %v", err)
	}
	if created.DelegateID == "" {
		t.Fatal("delegate create result omitted durable delegate ID")
	}
	if created.Sandbox != nil {
		t.Fatalf("omitted sandbox controls yielded sandbox report: %#v", created.Sandbox)
	}
}

func TestExecTool_SupportedHostPreservesExplicitSandboxControls(t *testing.T) {
	tests := []struct {
		name        string
		parentMode  sandbox.Mode
		parentNet   bool
		args        map[string]any
		wantMode    sandbox.Mode
		wantNetwork bool
	}{
		{
			name:        "sandbox base mode",
			parentMode:  sandbox.ModeOff,
			parentNet:   true,
			args:        map[string]any{"sandbox": "read-only"},
			wantMode:    sandbox.ModeReadOnly,
			wantNetwork: true,
		},
		{
			name:        "nonet suffix disables network",
			parentMode:  sandbox.ModeReadOnly,
			parentNet:   true,
			args:        map[string]any{"sandbox": "read-only+nonet"},
			wantMode:    sandbox.ModeReadOnly,
			wantNetwork: false,
		},
		{
			name:        "nonet suffix on write-capable mode",
			parentMode:  sandbox.ModeOff,
			parentNet:   true,
			args:        map[string]any{"sandbox": "workspace-write+nonet"},
			wantMode:    sandbox.ModeWorkspaceWrite,
			wantNetwork: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lane, home := t.TempDir(), t.TempDir()
			facts := sbxBwrapFacts(home)
			s := sbxDelegateSession(t, facts)
			if tc.parentMode != sandbox.ModeOff || !tc.parentNet {
				setParentSandboxForContract(t, s, facts, lane, tc.parentMode, tc.parentNet)
				if err := registerStableDelegateTool(s.reg, s); err != nil {
					t.Fatalf("refresh delegate tool schema: %v", err)
				}
			}
			args := map[string]any{"prompt": "do work"}
			maps.Copy(args, tc.args)

			result := execDelegateForContract(t, s, args)
			if result.IsError {
				t.Fatalf("supported delegate sandbox request failed: %#v", result)
			}
			var created stableDelegateCreateResult
			if err := json.Unmarshal([]byte(result.FullOutput), &created); err != nil {
				t.Fatalf("decode delegate create result: %v", err)
			}
			if created.Sandbox == nil {
				t.Fatal("supported sandbox request omitted sandbox report")
			}
			if created.Sandbox.Mode != tc.wantMode.String() || created.Sandbox.Network != tc.wantNetwork {
				t.Fatalf("sandbox result = %#v, want mode=%q network=%v", created.Sandbox, tc.wantMode, tc.wantNetwork)
			}
		})
	}
}

func TestStableDelegateCreateTool_UnsupportedSandboxReportsInvalidParameters(t *testing.T) {
	_, home := sbxLane(t)
	s := sbxDelegateSession(t, sandbox.HostFacts{OS: "linux", Home: home})

	_, err := stableDelegateCreateTool(context.Background(), s, map[string]any{
		"prompt":      "do work",
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
	if _, ok := props["sandbox"]; ok {
		t.Errorf("unsupported-host schema exposes %q", "sandbox")
	}
	compiled := compileDelegateParameters(t, params)
	requireDelegateSchemaValidation(t, compiled, map[string]any{"prompt": "do work"}, true)
	requireDelegateSchemaValidation(t, compiled, map[string]any{"prompt": "do work", "sandbox": "off"}, false)
	requireDelegateSchemaValidation(t, compiled, map[string]any{"prompt": "do work", "sandbox_net": false}, false)
}

func TestDelegateSchemaHonorsParentConfinementAndNetwork(t *testing.T) {
	tests := []struct {
		name               string
		parentMode         sandbox.Mode
		parentNetwork      bool
		parentWriteBlocked bool
		wantSandboxEnum    []string
	}{
		{name: "off parent", parentMode: sandbox.ModeOff, parentNetwork: true, wantSandboxEnum: []string{"off", "read-only", "read-only+nonet", "workspace-write", "workspace-write+nonet", "restricted", "restricted+nonet"}},
		{name: "read-only parent", parentMode: sandbox.ModeReadOnly, parentNetwork: true, wantSandboxEnum: []string{"read-only", "read-only+nonet", "nonet"}},
		{name: "workspace-write parent", parentMode: sandbox.ModeWorkspaceWrite, parentNetwork: true, wantSandboxEnum: []string{"read-only", "read-only+nonet", "workspace-write", "workspace-write+nonet", "restricted", "restricted+nonet", "nonet"}},
		{name: "restricted parent", parentMode: sandbox.ModeRestricted, parentNetwork: true, wantSandboxEnum: []string{"restricted", "restricted+nonet", "nonet"}},
		{name: "write-blocked read-only parent", parentMode: sandbox.ModeReadOnly, parentNetwork: true, parentWriteBlocked: true, wantSandboxEnum: []string{"read-only", "read-only+nonet", "nonet"}},
		{name: "write-blocked restricted parent", parentMode: sandbox.ModeRestricted, parentNetwork: true, parentWriteBlocked: true, wantSandboxEnum: []string{"nonet"}},
		{name: "write-blocked workspace-write parent", parentMode: sandbox.ModeWorkspaceWrite, parentNetwork: true, parentWriteBlocked: true, wantSandboxEnum: []string{"read-only", "read-only+nonet", "nonet"}},
		{name: "network-off parent", parentMode: sandbox.ModeWorkspaceWrite, parentNetwork: false, wantSandboxEnum: []string{"read-only+nonet", "workspace-write+nonet", "restricted+nonet"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lane, home := sbxLane(t)
			s := sbxDelegateSession(t, sbxBwrapFacts(home))
			setParentSandboxForContractWithWriteBlocked(t, s, sbxBwrapFacts(home), lane, tc.parentMode, tc.parentNetwork, tc.parentWriteBlocked)
			params := delegateDefinitionParameters(t, s)
			props := delegateDefinitionProperties(t, s)
			if len(tc.wantSandboxEnum) == 0 {
				if _, ok := props["sandbox"]; ok {
					t.Fatalf("sandbox property exposed for parent with no accepted explicit modes: %#v", props["sandbox"])
				}
				compiled := compileDelegateParameters(t, params)
				requireDelegateSchemaValidation(t, compiled, map[string]any{"prompt": "do work", "sandbox": "restricted"}, false)
				requireDelegateSchemaValidation(t, compiled, map[string]any{"prompt": "do work", "sandbox_net": false}, false)
				return
			}
			if _, ok := props["sandbox_net"]; ok {
				t.Fatalf("schema exposes a sandbox_net property; the combined enum replaced it: %#v", props["sandbox_net"])
			}
			if _, hasOneOf := params["oneOf"]; hasOneOf {
				t.Fatalf("schema carries a oneOf constraint; the combined enum replaced it: %#v", params["oneOf"])
			}
			sandboxSchema := props["sandbox"].(map[string]any)
			if got := sandboxSchema["enum"]; !reflect.DeepEqual(got, tc.wantSandboxEnum) {
				t.Fatalf("sandbox enum = %#v, want %#v", got, tc.wantSandboxEnum)
			}

			compiled := compileDelegateParameters(t, params)
			// Every advertised combined value is schema-valid; an off+nonet combo
			// (which the combined enum never lists) is schema-invalid, and a legacy
			// sandbox_net property is rejected by additionalProperties:false.
			for _, value := range tc.wantSandboxEnum {
				requireDelegateSchemaValidation(t, compiled, map[string]any{"prompt": "do work", "sandbox": value}, true)
			}
			requireDelegateSchemaValidation(t, compiled, map[string]any{"prompt": "do work", "sandbox": "off+nonet"}, false)
			requireDelegateSchemaValidation(t, compiled, map[string]any{"prompt": "do work", "sandbox_net": false}, false)
		})
	}
}

func TestExecTool_WriteBlockedParentSchemaAndRuntimeContract(t *testing.T) {
	lane, home := sbxLane(t)
	facts := sbxBwrapFacts(home)
	s := sbxDelegateSession(t, facts)
	setParentSandboxForContractWithWriteBlocked(t, s, facts, lane, sandbox.ModeRestricted, true, true)
	if err := registerStableDelegateTool(s.reg, s); err != nil {
		t.Fatalf("refresh delegate tool schema: %v", err)
	}

	params := delegateDefinitionParameters(t, s)
	props := delegateDefinitionProperties(t, s)
	sandboxSchema, ok := props["sandbox"].(map[string]any)
	if !ok {
		t.Fatal("write-blocked restricted parent schema omitted the sandbox property that carries the net-only control")
	}
	if got := sandboxSchema["enum"]; !reflect.DeepEqual(got, []string{"nonet"}) {
		t.Fatalf("write-blocked restricted parent sandbox enum = %#v, want [\"nonet\"] (net-only tightening only)", got)
	}
	if _, ok := props["sandbox_net"]; ok {
		t.Fatal("write-blocked restricted parent schema exposes a sandbox_net property; the combined enum replaced it")
	}
	var wiredProps map[string]any
	for _, def := range s.profileWireToolDefs() {
		if def.Name != "delegate" {
			continue
		}
		wiredProps, _ = def.Parameters["properties"].(map[string]any)
		break
	}
	if wiredProps == nil {
		t.Fatal("provider wire definitions omitted delegate")
	}
	if _, ok := wiredProps["sandbox"]; !ok {
		t.Fatal("provider wire schema omitted the sandbox property that carries the net-only control")
	}
	if _, ok := wiredProps["sandbox_net"]; ok {
		t.Fatal("provider wire schema exposes a sandbox_net property; the combined enum replaced it")
	}
	compiled := compileDelegateParameters(t, params)
	requireDelegateSchemaValidation(t, compiled, map[string]any{"prompt": "do work", "sandbox": "restricted"}, false)
	requireDelegateSchemaValidation(t, compiled, map[string]any{"prompt": "do work", "sandbox_net": false}, false)
	requireDelegateSchemaValidation(t, compiled, map[string]any{"prompt": "do work", "sandbox": "nonet"}, true)

	// "restricted" is not in the advertised enum, so schema prevalidation rejects
	// it before the handler ever sees it: a PrevalOnly error with no delegate launch.
	restricted := execDelegateForContract(t, s, map[string]any{"prompt": "do work", "sandbox": "restricted"})
	if !restricted.IsError || !restricted.PrevalOnly {
		t.Fatalf("restricted exec result = %#v, want a prevalidation rejection (IsError=true, PrevalOnly=true)", restricted)
	}
	requireNoDelegatePreRepairState(t, s)

	netOnly := execDelegateForContract(t, s, map[string]any{"prompt": "do work", "sandbox": "nonet"})
	if netOnly.IsError {
		t.Fatalf("write-blocked parent net-only tightening failed: %#v", netOnly)
	}
	var created stableDelegateCreateResult
	if err := json.Unmarshal([]byte(netOnly.FullOutput), &created); err != nil {
		t.Fatalf("decode net-only delegate result: %v", err)
	}
	if created.ChildSessionID == "" {
		t.Fatal("net-only delegate result omitted child session id")
	}
	child := s.getSub(created.ChildSessionID)
	if child == nil {
		t.Fatalf("net-only delegate %q is not resident", created.ChildSessionID)
	}
	local, ok := child.sess.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok || local.Sandbox == nil || !local.Sandbox.Enforced() {
		t.Fatalf("net-only child sandbox = %#v, want enforced local sandbox", child.sess.currentEnv())
	}
	if local.Sandbox.Mode != sandbox.ModeRestricted || !local.Sandbox.WriteBlocked || local.Sandbox.Network {
		t.Fatalf("net-only child sandbox = mode:%s writeBlocked:%t network:%t, want restricted/write-blocked/network-off", local.Sandbox.Mode, local.Sandbox.WriteBlocked, local.Sandbox.Network)
	}
}

func setParentSandboxForContract(t *testing.T, s *Session, facts sandbox.HostFacts, cwd string, mode sandbox.Mode, network bool) {
	setParentSandboxForContractWithWriteBlocked(t, s, facts, cwd, mode, network, false)
}

func setParentSandboxForContractWithWriteBlocked(t *testing.T, s *Session, facts sandbox.HostFacts, cwd string, mode sandbox.Mode, network, writeBlocked bool) {
	t.Helper()
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: mode, Network: &network}, facts, cwd)
	if err != nil {
		t.Fatalf("resolve parent sandbox: %v", err)
	}
	env := execenv.NewLocalExecutionEnvironment(cwd)
	env.Sandbox = &rp
	env.Sandbox.WriteBlocked = writeBlocked
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
			args := map[string]any{"prompt": "do work"}
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
		"prompt":  "do work",
		"sandbox": "nonet",
	}, 8192)
	requireDelegateSandboxRequestError(t, err, []string{"sandbox"})
	requireNoDelegateLaunch(t, s)
}
