package agent

import (
	"context"
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

// mentionsAllowedWithoutTheTool records the audited exceptions: prompt text
// that names a tool the agent does NOT have, because it is talking about the
// name rather than instructing a call. Every entry needs a reason; anything not
// listed here must be in the agent's own surface.
var mentionsAllowedWithoutTheTool = map[string][]string{
	// The doctor's forensic contract is that a tool NAME appearing in assistant
	// text is not evidence the tool ran. It has to print the name to say so.
	"internal/bundled/agents/doctor.md": {"delegate_send"},
	// communicate.md.tmpl names the job_watch FRAME kind, not the tool: a
	// delegate spawned with watch_parent receives watch frames without ever
	// being able to create a watch, and that paragraph tells it how to answer
	// one.
	"*": {"job_watch"},
}

// TestShippedPromptsOnlyNameToolsTheSessionHas is the prompt half of the rule
// ruled 2026-08-06: a canned instruction may not name a tool the session does
// not have. It sweeps the ASSEMBLED system prompt of a real delegate for each
// shipped agent type — base sections, provider appends, and the agent's own
// role body, exactly what the model is handed — against that delegate's real
// registry. Sweeping the role body alone cannot see this: the base sections are
// the bulk of the prompt and were the bulk of the bug (a page of transcript-tool
// instructions handed to ten agents that have no transcript tool).
func TestShippedPromptsOnlyNameToolsTheSessionHas(t *testing.T) {
	t.Parallel()
	parent := promptSweepParentSession(t)

	var findings []string
	for source, agent := range bundledTypedAgentsForTest(t) {
		agentType := agent.Name
		parent.pluginAgents[agentType] = agent

		prepared, err := parent.prepareSubagentRun(context.Background(), "task", "", "", 0, agentType, "", nil, nil)
		if err != nil {
			t.Fatalf("prepareSubagentRun(%s): %v", agentType, err)
		}
		child := prepared.sub.sess
		surface := child.reg.RegisteredNames()
		for _, visible := range child.providerVisibleToolNames(child.reg.Names()) {
			surface[visible] = true
		}
		prompt := stripToolInventory(child.cachedSystemPrompt)
		if strings.TrimSpace(prompt) == "" {
			t.Fatalf("%s: rendered no system prompt to sweep", source)
		}
		for _, name := range mentionedToolNames(prompt, parent.reg.RegisteredNames()) {
			if surface[name] ||
				hasString(mentionsAllowedWithoutTheTool[source], name) ||
				hasString(mentionsAllowedWithoutTheTool["*"], name) {
				continue
			}
			findings = append(findings, source+" ("+agentType+"): "+name)
		}
		releasePreparedTreeSlot(prepared)
		child.Close()
	}
	if len(findings) > 0 {
		sort.Strings(findings)
		t.Fatalf("%d assembled prompt(s) instruct a tool the agent cannot call. Gate the "+
			"section on {{ if .HasTool \"<name>\" }}, add the tool to that agent's tools: "+
			"list, or — if the text only talks ABOUT the name — record it in "+
			"mentionsAllowedWithoutTheTool with a reason:\n%s",
			len(findings), strings.Join(findings, "\n"))
	}
}

// promptSweepParentSession is a root session that renders REAL system prompts
// (no minimalSystemPrompt shortcut) and can delegate, so its children render
// theirs too.
func promptSweepParentSession(t *testing.T) *Session {
	t.Helper()
	s := newSession(t, withConfig(SessionConfig{
		MaxSubagentDepth: 3,
		NoProjectPrompts: true,
		testOnly:         testConfig{skipGitSnapshot: true, noSyncJobStore: true},
	}))
	s.delegationAllowance = 2
	return s
}

// stripToolInventory removes the tools section's two generated name lists. They
// are an inventory, not an instruction — and the second one exists precisely to
// tell the model which provider tools it CANNOT call here, so sweeping it would
// flag the very mechanism that reports unavailability.
func stripToolInventory(prompt string) string {
	var out []string
	inInventory := false
	for line := range strings.SplitSeq(prompt, "\n") {
		switch {
		case strings.HasPrefix(line, "Currently callable tools:"),
			strings.HasPrefix(line, "Provider tools currently unavailable here:"):
			inInventory = true
			continue
		case inInventory && strings.HasPrefix(line, "- `"):
			continue
		default:
			inInventory = false
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
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

// TestBundledPromptBodiesOnlyNameToolsTheirAgentHas keeps the cheap, direct
// check on the role bodies themselves, so a bad frontmatter/body pair is named
// as such rather than found buried in an assembled prompt.
func TestBundledPromptBodiesOnlyNameToolsTheirAgentHas(t *testing.T) {
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
		t.Fatalf("%d shipped role prompt(s) instruct a tool their own agent cannot call:\n%s",
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
	// RestrictKeepingResultTool never removes the result tool; a session may
	// rename it, so ask the default rather than hardcoding "communicate".
	surface[(&Session{}).resultToolName()] = true
	return surface
}
