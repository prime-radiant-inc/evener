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

// TestColdResumeReadmitsAReachabilityExcludedTool pins the reachability
// re-admission: a delegate created while an MCP tool is CLI-only recorded that tool's absence
// in its frozen ceiling, and a cold resume applied the ceiling as a permanent
// whitelist — so after the operator added the same server to the project or
// global config, the resumed child discovered and registered the tool and then
// had it stripped by its own ceiling.
//
// Two directions, and both matter:
//   - a name the ceiling dropped for REACHABILITY comes back once the child can
//     register it, which is this test's red;
//   - a name the ROLE forbids does not come back, which is what stops the fix
//     from becoming a general loosening of the ceiling.
func TestColdResumeReadmitsAReachabilityExcludedTool(t *testing.T) {
	// Not parallel: hermeticInfraEnv calls t.Setenv.
	hermeticInfraEnv(t)

	const (
		recovered  = "svc__echo"  // CLI-only at creation, registered by the child now
		roleDenied = "job_status" // outside this role's allowance and not registered
		ordinary   = "read_file"  // allowed and registered
	)
	root := realTempDirForTest(t)
	s := &Session{env: execenv.NewLocalExecutionEnvironment(root)}
	s.setManagerOnlyTools(nil)
	s.reg = tool.NewRegistry()
	// The child's registry now holds the tool that was unreachable at creation,
	// which is what a project or global layer gaining the server produces.
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

	if !slices.Contains(got, recovered) {
		t.Errorf("%q was excluded only for reachability and the child registers it now, so the resumed ceiling must carry it: %v", recovered, got)
	}
	if slices.Contains(got, roleDenied) {
		t.Errorf("%q was never in this role's ceiling, so resume must not re-admit it: %v", roleDenied, got)
	}
}
