package agent

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/plugin"
)

// toolShapedMention matches the two forms a shipped prompt uses to name a tool:
// a snake_case identifier (use_skill, delegate_send — no English word looks like
// that), and a backticked bare name (`shell`, `communicate`), which is how the
// prompts distinguish the tool from the ordinary word.
var toolShapedMention = regexp.MustCompile("`([a-z][a-z0-9_]*)`|\\b([a-z][a-z0-9]*(?:_[a-z0-9]+)+)\\b")

// mentionsAllowedWithoutTheTool records the audited exceptions: a prompt that
// names a tool it does NOT have, because it is talking about the name rather
// than instructing a call. Every entry needs a reason; anything not listed here
// must be in the agent's own surface.
var mentionsAllowedWithoutTheTool = map[string][]string{
	// The doctor's forensic contract is that a tool NAME appearing in assistant
	// text is not evidence the tool ran. It has to print the name to say so.
	"internal/bundled/agents/doctor.md": {"delegate_send"},
}

// TestBundledPromptsOnlyNameToolsTheirAgentHas is the bundled half of the rule
// ruled 2026-08-06: a canned instruction may not name a tool the session does
// not have. A shipped role prompt is a canned instruction that outlives every
// registry change around it, and its own tools: frontmatter decides what the
// agent can call — so the two must agree. The doctor named `use_skill` while
// its allowlist withheld it: an instruction the agent could only fail.
func TestBundledPromptsOnlyNameToolsTheirAgentHas(t *testing.T) {
	t.Parallel()
	registered := newTestSession(t).reg.RegisteredNames()

	var findings []string
	for source, agent := range bundledTypedAgentsForTest(t) {
		surface := bundledAgentSurfaceForTest(agent, registered)
		for _, name := range mentionedToolNames(agent.SystemPrompt, registered) {
			if surface[name] || hasString(mentionsAllowedWithoutTheTool[source], name) {
				continue
			}
			findings = append(findings, source+": "+name)
		}
	}
	if len(findings) > 0 {
		sort.Strings(findings)
		t.Fatalf("%d shipped prompt(s) instruct a tool their own agent cannot call. Add "+
			"the tool to that agent's tools: list, reword the prompt tool-free, or — if "+
			"the prompt only talks ABOUT the name — record it in "+
			"mentionsAllowedWithoutTheTool with a reason:\n%s",
			len(findings), strings.Join(findings, "\n"))
	}
}

// bundledAgentSurfaceForTest returns the tool names a delegate running this
// agent can actually call, via the same policy the spawn path uses.
func bundledAgentSurfaceForTest(agent plugin.Agent, registered map[string]bool) map[string]bool {
	allTools, allowed, denied := baseSubagentToolPolicy(&agent, true)
	surface := map[string]bool{}
	if allTools || len(allowed) == 0 {
		for name := range registered {
			surface[name] = true
		}
		for _, name := range denied {
			delete(surface, name)
		}
		return surface
	}
	for _, name := range allowed {
		surface[name] = true
	}
	// RestrictKeepingResultTool never removes the result tool.
	surface["communicate"] = true
	return surface
}

// mentionedToolNames returns the registered tool names a prompt body names.
func mentionedToolNames(body string, registered map[string]bool) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range toolShapedMention.FindAllStringSubmatch(body, -1) {
		name := m[1]
		if name == "" {
			name = m[2]
		}
		if !registered[name] || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}
