package agent

import (
	"slices"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/llm"
)

// TestLiveConstructionHandsReachabilityExclusionsToTheChild pins that the
// frozen ceiling and the reachability half travel SEPARATELY out of live
// construction, exactly as they do out of cold restore.
//
// The ceiling records two different facts and only one of them can expire. A
// role denial is authorization and never expires. A reachability exclusion is a
// fact about the workspace the ceiling was computed in, and it stops holding as
// soon as a server it named becomes discoverable. The authority on "the child
// can register this now" is the CHILD's registry, so NewSession widens the
// ceiling with effectiveChildToolCeiling against its own registrations.
//
// Live construction used to answer that question from the PARENT's registry and
// fold the answer into the ceiling it handed over. The parent can hold a tool
// the child cannot construct, so a name would enter the child's ceiling on the
// parent's evidence; the child then could not register it, and the ceiling
// promised a capability that does not exist. This replaces the earlier
// assertion that the handed-over ceiling already carried the re-admitted name:
// the name still becomes callable, but only once the child's own registry says
// it can be.
func TestLiveConstructionHandsReachabilityExclusionsToTheChild(t *testing.T) {
	// Not parallel: hermeticInfraEnv calls t.Setenv.
	hermeticInfraEnv(t)

	const (
		recovered  = "svc__echo"  // CLI-only at creation, registered by the child now
		roleDenied = "job_status" // outside this role's allowance and never registered
		ordinary   = "read_file"  // allowed and registered
	)
	root := realTempDirForTest(t)
	s := &Session{env: execenv.NewLocalExecutionEnvironment(root)}
	s.setManagerOnlyTools(nil)
	s.reg = tool.NewRegistry()
	// The PARENT has the tool that was unreachable at creation, which is what its
	// own project or global layer gaining the server produces. Registration here
	// must not be what admits the name into the child's ceiling.
	for _, name := range []string{recovered, ordinary} {
		if err := s.reg.Register(regTool(llm.ToolDefinition{Name: name, Description: "fixture"})); err != nil {
			t.Fatalf("register %q: %v", name, err)
		}
	}

	frozen := &delegatestore.Descriptor{
		ToolNameCeiling:             []string{ordinary},
		CeilingReachabilityExcluded: []string{recovered},
	}
	childConfig := &SessionConfig{}
	agent := &plugin.Agent{Name: "default"}

	if _, _, _, err := s.applyChildToolPolicy(childConfig, agent, frozen, false, nil); err != nil {
		t.Fatalf("applyChildToolPolicy: %v", err)
	}
	got := childConfig.spawn.toolNameCeiling

	if slices.Contains(got, recovered) {
		t.Errorf("%q was re-admitted into the handed-over ceiling from the PARENT's registry: %v", recovered, got)
	}
	if !slices.Equal(got, frozen.ToolNameCeiling) {
		t.Errorf("handed-over ceiling = %v, want the frozen list %v unchanged", got, frozen.ToolNameCeiling)
	}
	if !slices.Equal(childConfig.spawn.ceilingReachabilityExcluded, frozen.CeilingReachabilityExcluded) {
		t.Errorf("handed-over reachability exclusions = %v, want %v", childConfig.spawn.ceilingReachabilityExcluded, frozen.CeilingReachabilityExcluded)
	}
	if slices.Contains(got, roleDenied) {
		t.Errorf("%q was never in this role's ceiling, so construction must not admit it: %v", roleDenied, got)
	}

	// The name becomes callable exactly where it should: when the CHILD's own
	// registry registers it. This is the same function NewSession and the
	// post-restore check both call.
	effective := effectiveChildToolCeiling(childConfig.spawn.toolNameCeiling, childConfig.spawn.ceilingReachabilityExcluded, s.reg)
	if !slices.Contains(effective, recovered) {
		t.Errorf("%q is registered by the child and excluded only for reachability, so the effective ceiling must carry it: %v", recovered, effective)
	}
	if slices.Contains(effective, roleDenied) {
		t.Errorf("%q is outside the role ceiling and in no registry, so no ceiling may carry it: %v", roleDenied, effective)
	}
}
