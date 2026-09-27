package agent

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/clock"
	"primeradiant.com/evener/agent/schema"
)

type renderedResourceCaps struct {
	CPUs     float64 `json:"cpus"`
	MemoryMB int64   `json:"memory_mb"`
}

// promptResourceCaps decodes the resource caps the environment block renders,
// from the typed prompt input; ok is false when the session renders none.
func promptResourceCaps(t *testing.T, s *Session) (renderedResourceCaps, bool) {
	t.Helper()
	data := s.buildPromptData(s.env)
	if data.ResourceCapsJSON == "" {
		return renderedResourceCaps{}, false
	}
	var caps renderedResourceCaps
	decoder := json.NewDecoder(strings.NewReader(data.ResourceCapsJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&caps); err != nil {
		t.Fatalf("decode resource caps %q: %v", data.ResourceCapsJSON, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		t.Fatalf("resource caps %q have trailing data: %v", data.ResourceCapsJSON, err)
	}
	return caps, true
}

type maskedResourceFixtureEnv struct {
	*resourceFixtureEnv
}

func (e *maskedResourceFixtureEnv) ExecCommand(context.Context, string, int, string, map[string]string) (execenv.ExecResult, error) {
	// This is the model-facing boundary: the enforced wrapper masks /sys, so the
	// old shell probe exits successfully without returning cgroup facts.
	return execenv.ExecResult{ExitCode: 0}, nil
}

func TestRenderedEnvironmentUsesTrustedStructuredResourcesWhenModelShellMasked(t *testing.T) {
	t.Parallel()
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
	_, warning := sess.renderSystemPrompt(sess.env)
	if warning != "" {
		t.Fatalf("render system prompt: %s", warning)
	}
	caps, ok := promptResourceCaps(t, sess)
	if !ok {
		t.Fatal("rendered environment omitted finite resource payload")
	}
	if caps.CPUs != info.Resources.CPUs || caps.MemoryMB != info.Resources.MemoryMB {
		t.Fatalf("rendered resource payload = %+v, want cpus=%v memory_mb=%d",
			caps, info.Resources.CPUs, info.Resources.MemoryMB)
	}
}

func TestRenderedEnvironmentOmitsUnknownOrUnlimitedResources(t *testing.T) {
	t.Parallel()
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
			_, warning := sess.renderSystemPrompt(sess.env)
			if warning != "" {
				t.Fatalf("render system prompt: %s", warning)
			}
			if caps, ok := promptResourceCaps(t, sess); ok {
				t.Fatalf("rendered environment resource payload for %s resources: %+v", name, caps)
			}
		})
	}
}
