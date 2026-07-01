package agent

import (
	"reflect"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/plugin"
)

func TestS5Cov_CloneMaps(t *testing.T) {
	if cloneMap(nil) != nil {
		t.Error("cloneMap(nil) should be nil")
	}
	if cloneShallowMap(nil) != nil {
		t.Error("cloneShallowMap(nil) should be nil")
	}
	in := map[string]any{"a": "x", "n": float64(2)}
	if got := cloneMap(in); !reflect.DeepEqual(got, in) {
		t.Errorf("cloneMap round-trip = %v, want %v", got, in)
	}
	shallow := cloneShallowMap(in)
	if !reflect.DeepEqual(shallow, in) {
		t.Errorf("cloneShallowMap = %v, want %v", shallow, in)
	}
	// Mutating the clone must not touch the original.
	shallow["a"] = "y"
	if in["a"] != "x" {
		t.Error("cloneShallowMap should not alias the source")
	}
}

func TestS5Cov_LocalEnvPolicyName(t *testing.T) {
	cases := map[execenv.EnvVarPolicy]string{
		execenv.EnvPolicyAll:      "all",
		execenv.EnvPolicyNone:     "none",
		execenv.EnvPolicyCoreOnly: "core_only",
		execenv.EnvPolicyDefault:  "default",
	}
	for policy, want := range cases {
		env := execenv.NewLocalExecutionEnvironment(t.TempDir())
		env.EnvPolicy = policy
		if got := localEnvPolicyName(env); got != want {
			t.Errorf("localEnvPolicyName(%v) = %q, want %q", policy, got, want)
		}
	}
	// A non-local environment has no policy label.
	if got := localEnvPolicyName(nil); got != "" {
		t.Errorf("nil env policy name = %q, want empty", got)
	}
}

func TestS5Cov_LocalEnvPolicyFromName(t *testing.T) {
	for _, name := range []string{"all", "none", "core_only", "default"} {
		if _, ok := localEnvPolicyFromName(name); !ok {
			t.Errorf("localEnvPolicyFromName(%q) should be recognized", name)
		}
	}
	if _, ok := localEnvPolicyFromName("bogus"); ok {
		t.Error("unknown policy name should not be recognized")
	}
}

func TestS5Cov_FrozenSubagentToolNames(t *testing.T) {
	if got := frozenSubagentToolNames(true, nil, nil); len(got) != 1 || got[0] != "*" {
		t.Errorf("allTools → %v, want [*]", got)
	}
	if got := frozenSubagentToolNames(false, []string{"read_file"}, nil); len(got) != 1 || got[0] != "read_file" {
		t.Errorf("allowed → %v", got)
	}
	if got := frozenSubagentToolNames(false, nil, []string{"shell"}); got != nil {
		t.Errorf("deny-only → %v, want nil", got)
	}
	if got := frozenSubagentToolNames(false, nil, nil); got != nil {
		t.Errorf("default → %v, want nil", got)
	}
}

func TestS5Cov_RestoreFrozenSkillBodies(t *testing.T) {
	// No names, no bodies → (nil, nil).
	if bodies, err := restoreFrozenSkillBodies(nil, nil); err != nil || bodies != nil {
		t.Errorf("empty → %v, %v", bodies, err)
	}
	// Names without bodies → error.
	if _, err := restoreFrozenSkillBodies([]string{"tdd"}, nil); err == nil {
		t.Error("names without bodies should error")
	}
	// Bodies without names → error.
	if _, err := restoreFrozenSkillBodies(nil, []string{"body"}); err == nil {
		t.Error("bodies without names should error")
	}
	// Count mismatch → error.
	if _, err := restoreFrozenSkillBodies([]string{"a", "b"}, []string{"body"}); err == nil {
		t.Error("count mismatch should error")
	}
	// A blank body → error.
	if _, err := restoreFrozenSkillBodies([]string{"a"}, []string{"  "}); err == nil {
		t.Error("blank body should error")
	}
	// Success.
	bodies, err := restoreFrozenSkillBodies([]string{"a", "b"}, []string{"ba", "bb"})
	if err != nil || len(bodies) != 2 {
		t.Errorf("success case failed: %v %v", bodies, err)
	}
}

func TestS5Cov_SubagentNeedsCommunicateNudge(t *testing.T) {
	if !subagentNeedsCommunicateNudge(nil) {
		t.Error("nil agent should need the nudge")
	}
	if !subagentNeedsCommunicateNudge(&plugin.Agent{PluginName: "builtin", Name: "subagent"}) {
		t.Error("builtin subagent should need the nudge")
	}
	if subagentNeedsCommunicateNudge(&plugin.Agent{PluginName: "builtin", Name: "explorer"}) {
		t.Error("a named agent should not need the nudge")
	}
}
