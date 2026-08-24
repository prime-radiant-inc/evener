package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"encoding/xml"
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

func parseRenderedEnvironmentResourceCaps(t *testing.T, prompt string) (*renderedResourceCaps, bool) {
	t.Helper()

	const openEnvironment = "<environment>"
	const closeEnvironment = "</environment>"
	var section strings.Builder
	capturing := false
	found := false
	scanner := bufio.NewScanner(strings.NewReader(prompt))
	for scanner.Scan() {
		line := scanner.Text()
		if line == openEnvironment {
			if capturing || found {
				t.Fatal("rendered prompt contains multiple environment sections")
			}
			capturing = true
		}
		if !capturing {
			continue
		}
		section.WriteString(line)
		section.WriteByte('\n')
		if line == closeEnvironment {
			capturing = false
			found = true
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan rendered prompt: %v", err)
	}
	if capturing || !found {
		t.Fatal("rendered prompt has no complete environment section")
	}

	var environment struct {
		ResourceCaps []string `xml:"resource_caps"`
	}
	if err := xml.Unmarshal([]byte(section.String()), &environment); err != nil {
		t.Fatalf("parse rendered environment section: %v", err)
	}
	if len(environment.ResourceCaps) == 0 {
		return nil, false
	}
	if len(environment.ResourceCaps) != 1 {
		t.Fatalf("rendered environment has %d resource payloads, want 1", len(environment.ResourceCaps))
	}

	var caps renderedResourceCaps
	decoder := json.NewDecoder(strings.NewReader(environment.ResourceCaps[0]))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&caps); err != nil {
		t.Fatalf("parse rendered resource payload: %v", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		t.Fatalf("rendered resource payload has trailing data: %v", err)
	}
	return &caps, true
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
	caps, ok := parseRenderedEnvironmentResourceCaps(t, prompt)
	if !ok {
		t.Fatal("rendered environment omitted finite resource payload")
	}
	if caps.CPUs != info.Resources.CPUs || caps.MemoryMB != info.Resources.MemoryMB {
		t.Fatalf("rendered resource payload = %+v, want cpus=%v memory_mb=%d",
			caps, info.Resources.CPUs, info.Resources.MemoryMB)
	}
}

func TestRenderedEnvironmentOmitsUnknownOrUnlimitedResources(t *testing.T) {
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
			prompt, warning := sess.renderSystemPrompt(sess.env)
			if warning != "" {
				t.Fatalf("render system prompt: %s", warning)
			}
			if caps, ok := parseRenderedEnvironmentResourceCaps(t, prompt); ok {
				t.Fatalf("rendered environment resource payload for %s resources: %+v", name, caps)
			}
		})
	}
}
