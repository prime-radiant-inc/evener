package agent

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/clock"
	"primeradiant.com/evener/agent/schema"
)

type maskedResourceFixtureEnv struct {
	*resourceFixtureEnv
}

func (e *maskedResourceFixtureEnv) ExecCommand(context.Context, string, int, string, map[string]string) (execenv.ExecResult, error) {
	// This is the model-facing boundary: the enforced wrapper masks /sys, so the
	// old shell probe exits successfully without returning cgroup facts.
	return execenv.ExecResult{ExitCode: 0}, nil
}

func TestEnvironmentPromptUsesTrustedStructuredResourcesWhenModelShellMasked(t *testing.T) {
	env := &maskedResourceFixtureEnv{resourceFixtureEnv: newResourceFixtureEnv(t, resourceFixtureV2("100000 100000", "2147483648"))}
	info := envInfoFromEnv(env, clock.Real())
	if info.Resources == nil || info.Resources.CPUs != 1 || info.Resources.MemoryMB != 2048 {
		t.Fatalf("trusted resource snapshot = %+v, want finite fixture caps", info.Resources)
	}

	sess := newSession(t, withConfig(SessionConfig{
		NoProjectPrompts: true,
		testOnly: testConfig{
			skipGitSnapshot: true,
			environmentInfo: func(execenv.ExecutionEnvironment, clock.Clock) schema.EnvironmentInfo {
				return info
			},
		},
	}))
	prompt, warning := sess.renderSystemPrompt(sess.env)
	if warning != "" {
		t.Fatalf("render system prompt: %s", warning)
	}
	for _, want := range []string{"CPUs: 1", "Memory: 2048 MB"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("assembled environment prompt missing %q:\n%s", want, prompt)
		}
	}
}
func TestEnvironmentPromptOmitsUnknownOrUnlimitedResources(t *testing.T) {
	for name, resources := range map[string]*schema.ResourceCaps{
		"unknown":   nil,
		"unlimited": &schema.ResourceCaps{},
	} {
		t.Run(name, func(t *testing.T) {
			info := schema.EnvironmentInfo{WorkingDir: t.TempDir(), Platform: "linux", Resources: resources}
			sess := newSession(t, withConfig(SessionConfig{
				NoProjectPrompts: true,
				testOnly: testConfig{
					skipGitSnapshot: true,
					environmentInfo: func(execenv.ExecutionEnvironment, clock.Clock) schema.EnvironmentInfo {
						return info
					},
				},
			}))
			prompt, warning := sess.renderSystemPrompt(sess.env)
			if warning != "" {
				t.Fatalf("render system prompt: %s", warning)
			}
			for _, omitted := range []string{"CPUs:", "Memory:"} {
				if strings.Contains(prompt, omitted) {
					t.Fatalf("prompt must omit %q for %s resources:\n%s", omitted, name, prompt)
				}
			}
		})
	}
}

func TestDelegationPromptIncludesResourceCoTenantDoctrine(t *testing.T) {
	const doctrine = "Before running data-heavy work concurrently, price it against these CPU and memory caps, treating your own context and transcript heap as an invisible co-tenant."

	prompt := renderPromptForTest(t, NewOpenAIProfile("gpt-5.4"), promptData{})
	if !strings.Contains(prompt, doctrine) {
		t.Fatalf("assembled prompt missing resource co-tenant doctrine %q", doctrine)
	}
}
