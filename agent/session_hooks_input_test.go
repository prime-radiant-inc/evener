package agent

import (
	"testing"

	"primeradiant.com/evener/agent/plugin"
)

// TestHookInput_SubagentIdentity proves hookInput reports a truthful subagent
// identity: the child session id and its agent type. A root session reports
// neither, because Claude leaves both empty at the root.
func TestHookInput_SubagentIdentity(t *testing.T) {
	root := newSession(t)
	if in := root.hookInput(plugin.HookStop); in.AgentID != "" || in.AgentType != "" {
		t.Errorf("root identity must be empty, got %q/%q", in.AgentID, in.AgentType)
	}

	child := newSession(t)
	child.cfg.spawn.parentSessionID = root.ID()
	child.cfg.spawn.agentType = "toolsmith:toolsmith"
	in := child.hookInput(plugin.HookStop)
	if in.AgentID != child.ID() {
		t.Errorf("AgentID = %q, want %q", in.AgentID, child.ID())
	}
	if in.AgentType != "toolsmith:toolsmith" {
		t.Errorf("AgentType = %q, want toolsmith:toolsmith", in.AgentType)
	}
}

// TestHookInput_DefaultSubagentReportsSubagent: an unnamed delegate's agent type
// is "subagent", never empty and never the descriptor's "default" sentinel.
func TestHookInput_DefaultSubagentReportsSubagent(t *testing.T) {
	root := newSession(t)
	child := newSession(t)
	child.cfg.spawn.parentSessionID = root.ID()
	child.cfg.spawn.agentType = subagentHookAgentType("default", "subagent")
	if got := child.hookInput(plugin.HookStop).AgentType; got != "subagent" {
		t.Errorf("AgentType = %q, want subagent", got)
	}
}

func TestSubagentHookAgentType(t *testing.T) {
	cases := []struct{ requested, name, want string }{
		{"toolsmith:toolsmith", "toolsmith", "toolsmith:toolsmith"},
		{"", "subagent", "subagent"},
		{"default", "subagent", "subagent"},
		{"  ", "subagent", "subagent"},
	}
	for _, c := range cases {
		if got := subagentHookAgentType(c.requested, c.name); got != c.want {
			t.Errorf("subagentHookAgentType(%q,%q) = %q, want %q", c.requested, c.name, got, c.want)
		}
	}
}
