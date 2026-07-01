package agent

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/hooks"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/llm"
)

// TestW3Init_PrepareSubagentRun_ChildSessionError covers the arm where building
// the child Session fails: the injected child-client factory yields a nil client,
// so NewSession rejects it.
func TestW3Init_PrepareSubagentRun_ChildSessionError(t *testing.T) {
	t.Parallel()
	cfg := SessionConfig{
		MaxSubagentDepth: 1,
		testOnly:         testConfig{childClientFactory: func() *llm.Client { return nil }},
	}
	s := newSession(t, withConfig(cfg))

	_, err := s.prepareSubagentRun(context.Background(), "task", "", "", 0, "", "", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "llm client is nil") {
		t.Fatalf("err = %v, want nil-client child session error", err)
	}
}

// TestW3Init_PrepareSubagentRun_SkillResolveSkipped covers the arm where a named
// agent references a skill that cannot be resolved: the unresolved skill is
// skipped and the spawn still prepares.
func TestW3Init_PrepareSubagentRun_SkillResolveSkipped(t *testing.T) {
	t.Parallel()
	s := newSession(t)
	s.pluginAgents["w3agent"] = plugin.Agent{
		Name:         "w3agent",
		PluginName:   "test",
		SystemPrompt: "you are a w3 test agent",
		Skills:       []string{"w3-nonexistent-skill"},
	}

	prepared, err := s.prepareSubagentRun(context.Background(), "task", "", "", 0, "w3agent", "", nil, nil)
	if err != nil {
		t.Fatalf("prepareSubagentRun: %v", err)
	}
	if prepared == nil || prepared.sub == nil || prepared.sub.sess == nil {
		t.Fatal("expected a prepared subagent run")
	}
	if len(prepared.frozenSkillNames) != 0 {
		t.Fatalf("frozenSkillNames = %v, want none (unresolved skill skipped)", prepared.frozenSkillNames)
	}
	releasePreparedTreeSlot(prepared)
	prepared.sub.sess.Close()
}

// w3init_stopHookSession builds a subagent whose session carries a SubagentStop
// command hook emitting the given JSON, so runSubagentStopHook can be exercised.
func w3init_stopHookSession(t *testing.T, hookJSON string) *subagent {
	t.Helper()
	subSess := newSession(t, withSteps(func(llm.Request) llm.Response { return finalResponse("ok") }))
	runner := hooks.NewRunner(nil, "")
	runner.Add(plugin.HookSubagentStop, plugin.RegisteredHook{
		Matcher: "*",
		Type:    "command",
		Command: "printf '%s' '" + hookJSON + "'",
		Timeout: 5,
	})
	subSess.hookRunner = runner
	return &subagent{id: subSess.id, sess: subSess, status: SubagentRunning}
}

// TestW3Init_RunSubagentStopHook_BlockedWithContext covers the blocked path: the
// hook delivers model context and a user message, then blocks completion with a
// reason, driving a follow-up input turn.
func TestW3Init_RunSubagentStopHook_BlockedWithContext(t *testing.T) {
	t.Parallel()
	hookJSON := `{"decision":"block","reason":"address the feedback","hookSpecificOutput":{"additionalContext":"model context note"},"systemMessage":"user visible note"}`
	sub := w3init_stopHookSession(t, hookJSON)

	_, err := sub.runSubagentStopHook(context.Background(), "done", nil, nil)
	if err != nil {
		t.Fatalf("runSubagentStopHook: %v", err)
	}
}

// TestW3Init_RunSubagentStopHook_BlockedDefaultReason covers the blocked path with
// an empty reason, which falls back to the default block-reason message.
func TestW3Init_RunSubagentStopHook_BlockedDefaultReason(t *testing.T) {
	t.Parallel()
	hookJSON := `{"decision":"block"}`
	sub := w3init_stopHookSession(t, hookJSON)

	_, err := sub.runSubagentStopHook(context.Background(), "done", nil, nil)
	if err != nil {
		t.Fatalf("runSubagentStopHook: %v", err)
	}
}
