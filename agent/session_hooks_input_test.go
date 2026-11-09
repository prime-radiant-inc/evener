package agent

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent/plugin"
)

// TestHookInput_SubagentIdentity proves hookInput reports a truthful subagent
// identity through the real spawn path: the child session id and the agent type
// the spawn resolved, with the root session reporting neither. Driving
// prepareSubagentRun (rather than assigning the spawn fields by hand) is what
// exercises the agent-type computation in prepareSubagentRunFromSelection.
func TestHookInput_SubagentIdentity(t *testing.T) {
	parent := newSession(t)
	if in := parent.hookInput(plugin.HookStop); in.AgentID != "" || in.AgentType != "" {
		t.Errorf("root identity must be empty, got %q/%q", in.AgentID, in.AgentType)
	}

	// An unnamed spawn is the default subagent, so its agent_type is the
	// resolved name "subagent" — never empty and never the "default" sentinel.
	prepared, err := parent.prepareSubagentRun(context.Background(), "task", "", parent.currentEnv().WorkingDirectory(), 0, "", "", nil, nil)
	if err != nil {
		t.Fatalf("prepareSubagentRun: %v", err)
	}
	defer prepared.disposeUnadopted()

	in := prepared.sub.sess.hookInput(plugin.HookStop)
	if in.AgentID != prepared.sub.sess.ID() {
		t.Errorf("AgentID = %q, want %q", in.AgentID, prepared.sub.sess.ID())
	}
	if in.AgentType != "subagent" {
		t.Errorf("AgentType = %q, want subagent", in.AgentType)
	}
}

// TestHookInput_PluginAgentIdentity proves a spawn that names a plugin agent
// reports the plugin-scoped catalog key, the same value a matcher targets.
func TestHookInput_PluginAgentIdentity(t *testing.T) {
	parent := newSession(t)
	parent.pluginAgents["toolsmith:toolsmith"] = plugin.Agent{
		Name:         "toolsmith",
		PluginName:   "toolsmith",
		SystemPrompt: "you are a toolsmith",
	}

	prepared, err := parent.prepareSubagentRun(context.Background(), "task", "", parent.currentEnv().WorkingDirectory(), 0, "toolsmith:toolsmith", "", nil, nil)
	if err != nil {
		t.Fatalf("prepareSubagentRun: %v", err)
	}
	defer prepared.disposeUnadopted()

	if got := prepared.sub.sess.hookInput(plugin.HookStop).AgentType; got != "toolsmith:toolsmith" {
		t.Errorf("AgentType = %q, want toolsmith:toolsmith", got)
	}
}

func TestSubagentHookAgentType(t *testing.T) {
	cases := []struct{ requested, name, want string }{
		{"toolsmith:toolsmith", "toolsmith", "toolsmith:toolsmith"},
		{"", "subagent", "subagent"},
		{"default", "subagent", "subagent"},
		{"  ", "subagent", "subagent"},
		// A legacy frozen descriptor and an unnamed plugin agent can both carry
		// an empty agent name; the type must still never be empty inside a
		// subagent.
		{"", "", "subagent"},
		{"default", "", "subagent"},
	}
	for _, c := range cases {
		if got := subagentHookAgentType(c.requested, c.name); got != c.want {
			t.Errorf("subagentHookAgentType(%q,%q) = %q, want %q", c.requested, c.name, got, c.want)
		}
	}
}
