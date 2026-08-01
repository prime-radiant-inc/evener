package agent

import (
	"sort"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/plugin"
)

// bundledTypedAgentsForTest returns every agent shipped inside the binary that
// declares an explicit tools: allowlist — the built-in agents plus the
// coordinator-workflow plugin's agents — keyed by a label naming its source.
func bundledTypedAgentsForTest(t *testing.T) map[string]plugin.Agent {
	t.Helper()
	out := map[string]plugin.Agent{}
	builtins, err := builtinAgents()
	if err != nil {
		t.Fatalf("builtinAgents: %v", err)
	}
	for name, agent := range builtins {
		out["internal/bundled/agents/"+name+".md"] = agent
	}
	for name, agent := range coordinatorWorkflowPublicAgentsForTest(t) {
		out["internal/bundled/plugins/coordinator-workflow/agents/"+name+".md"] = agent
	}
	return out
}

// TestBundledAgentToolListsNameRegisteredTools keeps a shipped agent's tools:
// frontmatter honest. That list is an ALLOWLIST: baseSubagentToolPolicy hands
// it to Registry.RestrictKeepingResultTool, which DELETES every registered tool
// not in the list and does nothing at all for a listed name it cannot find. A
// name that is not a registered tool is therefore a silent no-op — the agent
// ships without the capability its own frontmatter claims, no error is raised,
// and nothing reports it. Kata eb5m found the coordinator naming
// job_read_output, unregistered since cf84923c6 (2026-06-23).
func TestBundledAgentToolListsNameRegisteredTools(t *testing.T) {
	registered := newTestSession(t).reg.RegisteredNames()
	var findings []string
	for source, agent := range bundledTypedAgentsForTest(t) {
		for _, name := range agent.Tools {
			if !registered[name] {
				findings = append(findings, source+": "+name)
			}
		}
	}
	if len(findings) > 0 {
		sort.Strings(findings)
		t.Fatalf("bundled agents name %d tool(s) that are not in the registry. A "+
			"tools: entry is an allowlist intersected with the registry, so an "+
			"unregistered name grants nothing and reports nothing (kata eb5m):\n%s",
			len(findings), strings.Join(findings, "\n"))
	}
}

// TestCoordinatorWorkflowCoordinatorCanReadAJob pins the capability the
// coordinator lost when job_read_output was unregistered and its frontmatter
// was not updated: with no job_status and no read_transcript, a coordinator
// that starts a job can never see the job's state or its output, and the
// delegate result is the only evidence it ever gets. Those two tools are the
// landed replacement surface (docs/job-control.md, "Reading job output").
func TestCoordinatorWorkflowCoordinatorCanReadAJob(t *testing.T) {
	t.Parallel()
	coord := coordinatorWorkflowAgentForTest(t, "coordinator")
	for _, want := range []string{"job_status", "read_transcript"} {
		if !hasString(coord.Tools, want) {
			t.Fatalf("coordinator tools = %+v, want %q so it can read a job it started", coord.Tools, want)
		}
	}
}
