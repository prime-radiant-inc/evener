package agent

import (
	"context"
	"slices"
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

func TestEnvironmentSectionDataUsesTrustedStructuredResourcesWhenModelShellMasked(t *testing.T) {
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
	data := sess.buildPromptData(sess.env)
	if data.CPUs != info.Resources.CPUs || data.MemoryMB != info.Resources.MemoryMB {
		t.Fatalf("environment section resources = (%v, %d), want trusted snapshot (%v, %d)",
			data.CPUs, data.MemoryMB, info.Resources.CPUs, info.Resources.MemoryMB)
	}
	if data.CPUs == 0 || data.MemoryMB == 0 {
		t.Fatalf("finite resource data selected omission branch: %+v", data)
	}
	if _, warning := sess.renderSystemPrompt(sess.env); warning != "" {
		t.Fatalf("render system prompt: %s", warning)
	}
	if !slices.ContainsFunc(sess.promptSourceLog, func(source promptSource) bool {
		return source.Label == "embedded:prompts/sections/environment.md.tmpl"
	}) {
		t.Fatalf("environment section source was not selected: %+v", sess.promptSourceLog)
	}
}

func TestEnvironmentSectionDataSelectsOmissionForUnknownOrUnlimitedResources(t *testing.T) {
	for name, resources := range map[string]*schema.ResourceCaps{
		"unknown":   nil,
		"unlimited": {},
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
			data := sess.buildPromptData(sess.env)
			if data.CPUs != 0 || data.MemoryMB != 0 {
				t.Fatalf("environment section selected finite-resource branch for %s resources: (%v, %d)",
					name, data.CPUs, data.MemoryMB)
			}
		})
	}
}
