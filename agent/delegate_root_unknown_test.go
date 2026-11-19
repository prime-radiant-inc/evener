package agent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/mcpconfig"
)

// TestUnresolvableGitRootKeepsCLINamesEligible pins that a fresh root lookup
// carries the resolver's definitive flag, so a resolution that could not answer
// stays distinguishable from a confirmed "no repository".
//
// The two empties have opposite consequences. Confirmed absence is a fact the
// classification may rest on. A resolution that timed out or was cancelled says
// only that it could not ask, and a worktree holding a matching MCP fallback may
// be sitting right there — so the evidence must read as unknown, the exclusion
// must not apply, and the child must get to try.
//
// The environment used for the second half answers every git invocation with a
// failure and no exit status, which is the shape a timeout produces: no verdict
// about whether this is a repository, as distinct from git running and saying it
// is not one.
//
// The fixture is TWO steps on ONE piece of evidence, and the first step is what
// makes the second mean anything. Evidence assembled by hand with an empty global
// layer would fail freshness at the global comparison before the Git root was
// ever consulted, so the test would pass with the inconclusive-lookup handling
// deleted — proving nothing about the case its name claims. So the evidence here
// comes from real discovery in a workspace that is genuinely, definitively
// outside Git, that evidence is shown to APPLY the exclusion, and only then is
// the root made unresolvable.
func TestUnresolvableGitRootKeepsCLINamesEligible(t *testing.T) {
	// Not parallel: hermeticInfraEnv calls t.Setenv.
	hermeticInfraEnv(t)
	workspace := realTempDirForTest(t)
	local := execenv.NewLocalExecutionEnvironment(workspace)

	// Discovery in a confirmed non-repository: the project layer is absent and
	// the resolver said so, which is the state the classification may rest on.
	_, _, evidence, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidenceForProjectEnv(local, nil, nil, nil, false, false)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if root, definitive := execenv.GitRootOrEmptyFreshDefinitive(local, local.WorkingDirectory()); root != "" || !definitive {
		t.Fatalf("fixture is wrong: root=%q definitive=%v, want no root but a verdict", root, definitive)
	}
	if !evidence.EvidenceCurrent(local) {
		t.Fatalf("fixture is wrong: evidence from a confirmed non-repository does not verify, so the exclusion below could not be applied: %+v", evidence)
	}

	// Step one: this evidence APPLIES the exclusion. Without this the second half
	// would pass for the wrong reason.
	confirmed := &Session{env: local}
	confirmed.setManagerOnlyTools(nil)
	confirmed.setCLIMCPServers(map[string]bool{"svc": true}, nil, evidence)
	confirmed.setCLIMCPToolNames(map[string]bool{"svc__echo": true})
	confirmed.refreshCLIEvidenceFreshnessContext(context.Background())
	if got := confirmed.removeChildUnreachableTools([]string{"svc__echo", "read_file"}, ""); slices.Contains(got, "svc__echo") {
		t.Fatalf("fixture is wrong: a confirmed absent project layer did not apply the exclusion, so the second half could not show it being lifted: %v", got)
	}

	// Step two: the SAME evidence, in the SAME workspace, but the root can no
	// longer be resolved. That is not a change of mind about the repository — it
	// is an inability to ask — so the exclusion must lift.
	unresolvable := &timeoutEnv{wd: workspace}
	if root, definitive := execenv.GitRootOrEmptyFreshDefinitive(unresolvable, unresolvable.WorkingDirectory()); root != "" || definitive {
		t.Fatalf("fixture is wrong: root=%q definitive=%v, want no root and no verdict", root, definitive)
	}
	s := &Session{env: unresolvable}
	s.setManagerOnlyTools(nil)
	s.setCLIMCPServers(map[string]bool{"svc": true}, nil, evidence)
	s.setCLIMCPToolNames(map[string]bool{"svc__echo": true})
	s.refreshCLIEvidenceFreshnessContext(context.Background())

	got := s.removeChildUnreachableTools([]string{"svc__echo", "read_file"}, "")
	if !slices.Contains(got, "svc__echo") {
		t.Errorf("an unresolvable Git root was treated as confirmed absence, so %q stayed excluded; unknown reachability must let the child try: %v", "svc__echo", got)
	}
}

// TestDiscoveryFindsAProjectConfigAddedAfterStartup pins that discovery resolves
// the project layer from the workspace's CURRENT root, not from the answer the
// environment remembered at startup.
//
// A session can be launched before its workspace is a repository, and a
// delegate is often constructed with the parent's own environment. That
// environment memoizes the Git root, so a discovery that reads the memo answers
// "no repository" for the rest of the session — while the parent's freshness
// check, which resolves freshly, sees the new root and makes the tool eligible.
// The roster then advertises a tool the child does not have, and the child skips
// a project config sitting on disk.
//
// The fixture primes the memo by resolving the root BEFORE the repository
// exists. Without that priming the test would pass on a fresh resolution for the
// wrong reason, which is the vacuous shape the rest of this file exists to
// avoid.
func TestDiscoveryFindsAProjectConfigAddedAfterStartup(t *testing.T) {
	// Not parallel: hermeticInfraEnv calls t.Setenv.
	hermeticInfraEnv(t)
	workspace := realTempDirForTest(t)
	local := execenv.NewLocalExecutionEnvironment(workspace)

	// Prime the per-environment memo while the workspace is NOT a repository.
	// This is the answer a session launched now would remember.
	if root := execenv.GitRootOrEmpty(local, local.WorkingDirectory()); root != "" {
		t.Fatalf("fixture is wrong: %q already resolves to a root %q", workspace, root)
	}
	// Discovery at startup sees nothing, which is correct at this moment.
	_, _, before, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidenceForProjectEnv(local, nil, nil, nil, false, false)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if before.Project.Path != "" {
		t.Fatalf("fixture is wrong: the project layer is already recorded as %q", before.Project.Path)
	}

	// The repository and its project MCP config appear around the running
	// session.
	runInfraGit(t, workspace, "init", "-q")
	layer := filepath.Join(workspace, ".evener", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(layer), 0o755); err != nil {
		t.Fatal(err)
	}
	writeInfraFile(t, layer, `{"mcpServers":{"svc":{"command":"/bin/svc"}}}`, 0o644)

	// Discovery against the SAME environment must now find it. A discovery that
	// reads the memo reports no project layer, and the child cannot register the
	// server the roster advertises.
	configs, _, after, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidenceForProjectEnv(local, nil, nil, nil, false, false)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if after.Project.Path != layer {
		t.Errorf("discovery on a live session did not resolve the project layer that now exists at %q; the environment's memoized root answered instead: %+v", layer, after.Project)
	}
	var found bool
	for _, cfg := range configs {
		if cfg.Name == "svc" {
			found = true
		}
	}
	if !found {
		t.Errorf("the project server that appeared after startup is not in the discovered configuration, so a child cannot register it: %+v", configs)
	}
}
