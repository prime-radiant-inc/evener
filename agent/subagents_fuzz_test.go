//go:build serffuzz

package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/skill"
	taskpkg "primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/llm"
)

type safzEnumerableAdapter struct {
	*agenttest.ScriptedAdapter
	models []llm.ModelInfo
}

func (a *safzEnumerableAdapter) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return append([]llm.ModelInfo(nil), a.models...), nil
}

// safzResolveProfile is the parent's cross-provider resolver. A "prefix/model"
// ref that the base profile treats as a provider switch reaches this; refs
// carrying "boom" error (driving the resolve-error branch), any other resolves
// (driving the cross-provider WithCommunicateOverridesFrom branch).
func safzResolveProfile(ref string) (*provider.Profile, error) {
	if strings.Contains(ref, "boom") {
		return nil, fmt.Errorf("safz resolve error for %q", ref)
	}
	return NewOpenAIProfile("gpt-5"), nil
}

// safzSkillBody is the on-disk SKILL.md a parent registers so the agent-skill
// population branch (ResolveSkillContent -> a non-empty body -> frozen skill
// descriptor) is reachable; without a real body ResolveSkillContent returns ""
// and the branch is skipped.
const safzSkillBody = `---
name: safz-skill
description: a fuzz skill
---

The safz fuzz skill body, exercising frozen-skill population and restore.
`

// safzRegisterAgents installs a spread of plugin agent shapes so a fuzzed
// agent_type exercises every branch of the agent-config lookup: all-tools, an
// explicit tool allow-list, a custom (non-builtin) system prompt, a builtin
// prompt, a model override, and a tasked agent. Called on a freshly built parent
// before any concurrent use, so the direct map write is race-free.
func safzRegisterAgents(sess *Session) {
	add := func(a plugin.Agent) { sess.pluginAgents[a.Name] = a }
	add(plugin.Agent{Name: "safz_alltools", AllTools: true, PluginName: "safz"})
	add(plugin.Agent{Name: "safz_tools", Tools: []string{"read_file", "grep"}, PluginName: "safz"})
	add(plugin.Agent{Name: "safz_prompt", SystemPrompt: "custom role prompt", PluginName: "safz", Description: "d"})
	add(plugin.Agent{Name: "safz_builtinprompt", SystemPrompt: "builtin role prompt", PluginName: "builtin"})
	add(plugin.Agent{Name: "safz_model", Model: "gpt-5.3", SystemPrompt: "model agent", PluginName: "safz"})
	add(plugin.Agent{Name: "safz_badmodel", Model: "no_such_model_zzz", SystemPrompt: "bad model agent", PluginName: "safz"})
	add(plugin.Agent{Name: "safz_crossmodel", Model: "anthropic/claude-x", SystemPrompt: "cross model agent", PluginName: "safz"})
	add(plugin.Agent{Name: "safz_crossbad", Model: "anthropic/boom", SystemPrompt: "cross bad model agent", PluginName: "safz"})
	add(plugin.Agent{Name: "safz_tasks", PluginName: "safz", Tasks: []taskpkg.TaskTemplate{{Title: "t1", Prompt: "do step 1"}}})
	add(plugin.Agent{Name: "safz_skilled", SystemPrompt: "skilled role prompt", PluginName: "safz", Skills: []string{"safz-skill"}})
}

// safzRegisterSkill writes a real SKILL.md and registers it on the parent so a
// skilled agent's Skills resolve to a non-empty body (the map write happens on a
// freshly built parent before any concurrent use, so it is race-free).
func safzRegisterSkill(t *testing.T, sess *Session) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(path, []byte(safzSkillBody), 0o600); err != nil {
		t.Fatalf("safzRegisterSkill: write skill: %v", err)
	}
	sess.skills = map[string]skill.SkillMeta{"safz-skill": {Name: "safz-skill", SkillFile: path}}
}

// safzNewParent builds a parent Session wired for offline subagent orchestration:
// a scripted parent adapter, a per-child scripted adapter (via the childClient
// factory seam), an injected fake clock, and an event drain so a spawning run
// never blocks on emit. env selects the execution environment (a Local env when
// the target needs the working_dir override + env-policy branches; a DenyEnv when
// a child actually runs, so no real process/disk is ever touched).
func safzNewParent(t *testing.T, clk *agenttest.FakeClock, maxDepth int, childScript []int, env execenv.ExecutionEnvironment) *Session {
	t.Helper()
	client := llm.NewClient()
	client.Register(&safzEnumerableAdapter{
		ScriptedAdapter: &agenttest.ScriptedAdapter{Provider: "openai", Responder: func(llm.Request) llm.Response {
			return agenttest.FinalResponse("parent")
		}},
		models: []llm.ModelInfo{{ID: "gpt-5"}, {ID: "gpt-5.2"}, {ID: "gpt-5.3"}},
	})
	cfg := SessionConfig{
		MaxSubagentDepth:      maxDepth,
		clock:                 clk,
		MaxToolRoundsPerInput: 6,
		LLMSleep:              func(context.Context, time.Duration) error { return nil },
		ResolveProfile:        safzResolveProfile,
	}
	cfg.testOnly.childClientFactory = func() *llm.Client {
		cc := llm.NewClient()
		cc.Register(&safzEnumerableAdapter{
			ScriptedAdapter: &agenttest.ScriptedAdapter{Provider: "openai", Responder: newChildResponder(childScript)},
			models:          []llm.ModelInfo{{ID: "gpt-5"}, {ID: "gpt-5.2"}, {ID: "gpt-5.3"}},
		})
		return cc
	}
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), env, cfg)
	if err != nil {
		t.Fatalf("safzNewParent: NewSession: %v", err)
	}
	drainDone := make(chan struct{})
	go func() {
		for range sess.Events() {
		}
		close(drainDone)
	}()
	t.Cleanup(func() {
		sess.Close()
		<-drainDone
	})
	safzRegisterAgents(sess)
	safzRegisterSkill(t, sess)
	return sess
}

// safzWaitDone blocks until the subagent's initial run finalizes (goes idle).
func safzWaitDone(t *testing.T, sub *subagent) {
	t.Helper()
	<-sub.done
}
