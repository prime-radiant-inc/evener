package agent

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/sandbox"
	"primeradiant.com/serf/llm"
)

func netEnv(t *testing.T, netOn bool) *execenv.LocalExecutionEnvironment {
	t.Helper()
	home := t.TempDir()
	cwd := sandbox.MaterializeWorkspace(t, sandbox.MainCheckout)
	net := netOn
	rp, err := sandbox.Resolve(
		sandbox.SandboxPolicy{Mode: sandbox.ModeWorkspaceWrite, Network: &net},
		sandbox.HostFacts{OS: "linux", Home: home, BwrapPath: "/usr/bin/bwrap", BwrapCapable: true},
		cwd)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	w, err := sandbox.NewWrapper(rp, "/usr/bin/bwrap", t.TempDir())
	if err != nil {
		t.Fatalf("NewWrapper: %v", err)
	}
	env := execenv.NewLocalExecutionEnvironment(cwd)
	env.Sandbox = &rp
	env.Wrapper = w
	return env
}

func TestNetOffDisablesToolEgress(t *testing.T) {
	off := netEnv(t, false)
	for _, tool := range []string{"web_fetch", "web_search"} {
		err := egressDeniedByNet(off, tool)
		if err == nil {
			t.Errorf("%s must be denied under net=off", tool)
			continue
		}
		var de *sandbox.DeniedError
		if !errors.As(err, &de) {
			t.Errorf("%s denial must be a *sandbox.DeniedError, got %T", tool, err)
		} else if de.Tool != tool {
			t.Errorf("denial Tool = %q, want %q", de.Tool, tool)
		}
	}
}

func TestNetOnAllowsToolEgress(t *testing.T) {
	on := netEnv(t, true)
	if err := egressDeniedByNet(on, "web_fetch"); err != nil {
		t.Errorf("net=on must allow tool egress, got %v", err)
	}
}

func TestUnsandboxedAllowsToolEgress(t *testing.T) {
	// A plain (non-sandboxed) env must never gate egress — byte-identical behavior.
	plain := execenv.NewLocalExecutionEnvironment(t.TempDir())
	if err := egressDeniedByNet(plain, "web_fetch"); err != nil {
		t.Errorf("an unsandboxed env must not gate egress, got %v", err)
	}
}

// Provider-native web search (the server-side web egress the provider runs for
// the model) is part of the tool plane net=off governs. Under net=off the model
// request must NOT carry WebSearch=true even when the profile supports it, else
// egress leaks through a path the user cannot inspect.
func TestBuildModelRequest_NetOffDisablesProviderWebSearch(t *testing.T) {
	profile := provider.NewOpenAIProfile("gpt-5") // SupportsWebSearch() == true
	if !profile.SupportsWebSearch() {
		t.Fatal("fixture profile must support web search")
	}
	msgs := []llm.Message{llm.User("hi")}

	off := &Session{env: netEnv(t, false)}
	if off.buildModelRequest(profile, "sys", msgs, nil, "").WebSearch {
		t.Errorf("net=off must disable provider-native web search even when the profile supports it")
	}

	on := &Session{env: netEnv(t, true)}
	if !on.buildModelRequest(profile, "sys", msgs, nil, "").WebSearch {
		t.Errorf("net=on must keep provider-native web search available")
	}

	// Unsandboxed: the profile capability passes through unchanged (byte-identical).
	plain := &Session{env: execenv.NewLocalExecutionEnvironment(t.TempDir())}
	if !plain.buildModelRequest(profile, "sys", msgs, nil, "").WebSearch {
		t.Errorf("an unsandboxed session must pass the profile web-search capability through unchanged")
	}
}

// A mid-session cross-provider switch to a google-tag profile dynamically
// re-registers the web_search function tool. That executor must still honor
// net=off egress denial — otherwise a SetModel makes serf's Gemini web search
// reachable in a session whose network egress the user turned off.
func TestReapplyProviderTools_GoogleWebSearchEgressDeniedUnderNetOff(t *testing.T) {
	s := &Session{env: netEnv(t, false), reg: tool.NewRegistry()}
	s.reapplyProviderSpecificTools("openai", "google")

	rt := s.reg.Get("web_search")
	if rt == nil {
		t.Fatal("web_search must be registered after switching to a google-tag profile")
	}
	_, err := rt.Exec(context.Background(), s.env, map[string]any{"query": "x"})
	var de *sandbox.DeniedError
	if !errors.As(err, &de) {
		t.Fatalf("google web_search under net=off must be egress-denied, got %v", err)
	}
	if de.Tool != "web_search" {
		t.Errorf("denial Tool = %q, want web_search", de.Tool)
	}
}
