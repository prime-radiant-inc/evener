package agent

import (
	"context"
	"strings"
	"testing"
)

// Absent means "inherit the default grant"; an explicit 0 means a leaf. The
// two must stay distinguishable through decoding.
func TestDecodeDelegateArgs_AllowanceAbsentIsDefaultAndZeroIsLeaf(t *testing.T) {
	absent, err := decodeDelegateArgs(map[string]any{"prompt": "p"})
	if err != nil {
		t.Fatal(err)
	}
	if absent.DelegationAllowance != nil {
		t.Fatalf("absent delegation_allowance decoded as %d, want unset", *absent.DelegationAllowance)
	}
	for _, n := range []float64{0, 1, 3} {
		got, err := decodeDelegateArgs(map[string]any{"prompt": "p", "delegation_allowance": n})
		if err != nil {
			t.Fatalf("delegation_allowance %v: %v", n, err)
		}
		if got.DelegationAllowance == nil || *got.DelegationAllowance != int(n) {
			t.Fatalf("delegation_allowance %v decoded as %v, want %d", n, got.DelegationAllowance, int(n))
		}
	}
	if _, err := decodeDelegateArgs(map[string]any{"prompt": "p", "delegation_allowance": float64(-1)}); err == nil || !strings.Contains(err.Error(), "invalid_request") {
		t.Fatalf("negative delegation_allowance: err = %v, want invalid_request", err)
	}
}

func TestDefaultDelegateGrant(t *testing.T) {
	for own, want := range map[int]int{0: 0, 1: 0, 2: 1, 4: 3} {
		if got := defaultDelegateGrant(own); got != want {
			t.Errorf("defaultDelegateGrant(%d) = %d, want %d", own, got, want)
		}
	}
}

// A delegate created without delegation_allowance inherits one level below
// its granter, so it can delegate in turn; 0 has to be asked for.
func TestCreateDelegate_DefaultGrantIsOneBelowTheGranter(t *testing.T) {
	for _, tc := range []struct {
		name          string
		rootAllowance int
		args          delegateArgs
		wantAllowance int
		wantDelegate  bool
	}{
		{"default at allowance 2", 2, delegateArgs{Task: "brief", AgentType: "subagent"}, 1, true},
		{"default at allowance 1 is a leaf", 1, delegateArgs{Task: "brief", AgentType: "subagent"}, 0, false},
		{"explicit zero is a leaf", 2, delegateArgs{Task: "brief", AgentType: "subagent", DelegationAllowance: new(0)}, 0, false},
		{"explicit grant", 2, delegateArgs{Task: "brief", AgentType: "subagent", DelegationAllowance: new(1)}, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, _, _ := newDelegateResourceBootstrapSession(t)
			root.mu.Lock()
			root.delegationAllowance = tc.rootAllowance
			root.mu.Unlock()
			result := root.createDelegate(context.Background(), tc.args)
			if result.Err != nil {
				t.Fatalf("createDelegate: %v", result.Err)
			}
			root.delegateController.mu.Lock()
			durable := root.delegateController.durable[result.DelegateID]
			live := root.delegateController.live[result.DelegateID]
			root.delegateController.mu.Unlock()
			if durable == nil || durable.Descriptor.DelegationAllowance != tc.wantAllowance {
				t.Fatalf("descriptor allowance = %+v, want %d", durable, tc.wantAllowance)
			}
			if live == nil || live.binding == nil || live.binding.runtime == nil {
				t.Fatal("no live child session")
			}
			names := live.binding.runtime.reg.RegisteredNames()
			if names["delegate"] != tc.wantDelegate || names["job_watch"] != tc.wantDelegate {
				t.Errorf("child delegate=%v job_watch=%v, want both %v", names["delegate"], names["job_watch"], tc.wantDelegate)
			}
		})
	}
}
