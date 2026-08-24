package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/clock"
	"primeradiant.com/evener/agent/schema"
)

type resourceProbeTestEnv struct {
	*execenv.LocalExecutionEnvironment
	probeOutput string
	calls       []string
}

func (e *resourceProbeTestEnv) Platform() string { return "linux" }

func (e *resourceProbeTestEnv) ExecCommand(_ context.Context, command string, _ int, _ string, _ map[string]string) (execenv.ExecResult, error) {
	e.calls = append(e.calls, command)
	return execenv.ExecResult{Stdout: e.probeOutput, ExitCode: 0}, nil
}

func TestEnvInfoFromEnvCollectsEffectiveResourceCaps(t *testing.T) {
	env := &resourceProbeTestEnv{
		LocalExecutionEnvironment: execenv.NewLocalExecutionEnvironment(t.TempDir()),
		probeOutput:               "cpu_quota=150000\ncpu_period=100000\nmemory_bytes=2147483648\n",
	}

	info := envInfoFromEnv(env, clock.Real())
	encoded, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshal environment info: %v", err)
	}
	got := string(encoded)
	for _, want := range []string{`"cpus":1.5`, `"memory_mb":2048`} {
		if !strings.Contains(got, want) {
			t.Fatalf("environment info missing %s: %s", want, got)
		}
	}
	if len(env.calls) != 1 {
		t.Fatalf("resource probe calls = %d, want one real environment command", len(env.calls))
	}
}

func TestEnvInfoFromEnvOmitsUnlimitedResourceCaps(t *testing.T) {
	env := &resourceProbeTestEnv{
		LocalExecutionEnvironment: execenv.NewLocalExecutionEnvironment(t.TempDir()),
		probeOutput:               "cpu_quota=max\ncpu_period=100000\nmemory_bytes=max\n",
	}

	info := envInfoFromEnv(env, clock.Real())
	encoded, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshal environment info: %v", err)
	}
	got := string(encoded)
	for _, unwanted := range []string{`"cpus"`, `"memory_mb"`} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("unlimited resource cap %s should be omitted: %s", unwanted, got)
		}
	}
}

func TestSessionPromptIncludesEffectiveResourceCaps(t *testing.T) {
	const encodedInfo = `{"working_dir":"/tmp/project","platform":"linux","os_version":"test","today":"2026-08-24","cpus":1.5,"memory_mb":2048}`

	sess := newSession(t, withConfig(SessionConfig{
		MaxSubagentDepth: 1,
		testOnly: testConfig{
			skipGitSnapshot: true,
			environmentInfo: func(execenv.ExecutionEnvironment, clock.Clock) schema.EnvironmentInfo {
				var info schema.EnvironmentInfo
				if err := json.Unmarshal([]byte(encodedInfo), &info); err != nil {
					t.Fatalf("decode environment fixture: %v", err)
				}
				return info
			},
		},
	}))

	prompt, warning := sess.renderSystemPrompt(sess.env)
	if warning != "" {
		t.Fatalf("render system prompt: %s", warning)
	}
	for _, want := range []string{"CPUs: 1.5", "Memory: 2048 MB"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("assembled prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestDelegationPromptIncludesResourceCoTenantDoctrine(t *testing.T) {
	const doctrine = "Before running data-heavy work concurrently, price it against these CPU and memory caps, treating your own context and transcript heap as an invisible co-tenant."

	prompt := renderPromptForTest(t, NewOpenAIProfile("gpt-5.4"), promptData{})
	if !strings.Contains(prompt, doctrine) {
		t.Fatalf("assembled prompt missing resource co-tenant doctrine %q", doctrine)
	}
}
