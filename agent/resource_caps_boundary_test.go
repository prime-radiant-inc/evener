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

// promptResourceCaps decodes the resource caps the session hands the
// environment template, from the typed prompt input; ok is false when it
// hands none.
func promptResourceCaps(t *testing.T, s *Session) (renderedResourceCaps, bool) {
	t.Helper()
	data, _ := s.buildPromptData(s.env)
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
	caps, ok := promptResourceCaps(t, sess)
	if !ok {
		t.Fatal("prompt data carries no finite resource payload")
	}
	if caps.CPUs != info.Resources.CPUs || caps.MemoryMB != info.Resources.MemoryMB {
		t.Fatalf("prompt resource payload = %+v, want cpus=%v memory_mb=%d",
			caps, info.Resources.CPUs, info.Resources.MemoryMB)
	}
	// The payload is Go-generated JSON, so the rendered prompt must carry it
	// verbatim, once.
	data, _ := sess.buildPromptData(sess.env)
	if payload := data.ResourceCapsJSON; strings.Count(prompt, payload) != 1 {
		t.Fatalf("rendered prompt carries resource payload %s %d times, want once", payload, strings.Count(prompt, payload))
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
				t.Fatalf("prompt data carries a resource payload for %s resources: %+v", name, caps)
			}
		})
	}
}

// TestRenderedEnvironmentDisclosesResourceCapsAreShared guards the prompt's
// honesty about memory: the caps env_info advertises are the whole
// environment's, shared by every session and delegate in it, not a per-session
// allowance. Issue #496. The disclosure is tied to the caps: when there are none
// to advertise, there is nothing to say they are shared.
func TestRenderedEnvironmentDisclosesResourceCapsAreShared(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		resources *schema.ResourceCaps
		want      bool
	}{
		"finite caps are disclosed as shared": {resources: &schema.ResourceCaps{CPUs: 2, MemoryMB: 4096}, want: true},
		"no caps means no disclosure":         {resources: nil, want: false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			info := schema.EnvironmentInfo{WorkingDir: t.TempDir(), Platform: "linux", Resources: tc.resources}
			sess := newSession(t, withConfig(SessionConfig{
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
			if got := strings.Contains(prompt, "shared by every session and delegate"); got != tc.want {
				t.Fatalf("prompt discloses shared caps = %v, want %v\nprompt:\n%s", got, tc.want, prompt)
			}
		})
	}
}
