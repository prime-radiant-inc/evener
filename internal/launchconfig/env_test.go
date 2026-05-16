package launchconfig

import (
	"testing"
)

type stubCreds struct {
	keys map[string]string
}

func (s stubCreds) APIKeyFor(provider string) (string, string) {
	v, ok := s.keys[provider]
	if !ok {
		return "", "absent"
	}
	return v, "file"
}

func TestToEnv_BaselineSetsRunStateAndProvider(t *testing.T) {
	parent := []string{"PATH=/usr/bin"}
	r := Resolved{Effective: Layer{Model: "anthropic/claude-1", Env: map[string]string{"FOO": "bar"}}}
	creds := stubCreds{keys: map[string]string{"anthropic": "sk-ant-FROM-FILE"}}
	got := ToEnv(EnvInputs{
		Resolved:  r,
		Provider:  "anthropic",
		Creds:     creds,
		ParentEnv: parent,
		RunDir:    "/run",
		StateDir:  "/state",
		HubToken:  "tok",
	})
	want := map[string]string{
		"PATH":              "/usr/bin",
		"FOO":               "bar",
		"SERF_HUB_SPAWNED":  "1",
		"SERF_RUN_DIR":      "/run",
		"SERF_STATE_DIR":    "/state",
		"SERF_HUB_TOKEN":    "tok",
		"ANTHROPIC_API_KEY": "sk-ant-FROM-FILE",
	}
	gotMap := envSliceToMap(got)
	for k, v := range want {
		if gotMap[k] != v {
			t.Errorf("env[%s] = %q, want %q", k, gotMap[k], v)
		}
	}
}

func TestToEnv_PerLaunchEnvBeatsCredsStore(t *testing.T) {
	r := Resolved{Effective: Layer{Env: map[string]string{"ANTHROPIC_API_KEY": "from-overrides"}}}
	creds := stubCreds{keys: map[string]string{"anthropic": "from-file"}}
	got := envSliceToMap(ToEnv(EnvInputs{
		Resolved: r, Provider: "anthropic", Creds: creds,
	}))
	if got["ANTHROPIC_API_KEY"] != "from-overrides" {
		t.Errorf("per-launch env should win: %q", got["ANTHROPIC_API_KEY"])
	}
}

func TestToEnv_NoProviderNoInjection(t *testing.T) {
	creds := stubCreds{keys: map[string]string{"anthropic": "x"}}
	got := envSliceToMap(ToEnv(EnvInputs{Provider: "", Creds: creds}))
	if _, ok := got["ANTHROPIC_API_KEY"]; ok {
		t.Errorf("no provider, no injection; got %v", got)
	}
}

func envSliceToMap(env []string) map[string]string {
	out := map[string]string{}
	for _, kv := range env {
		i := 0
		for ; i < len(kv); i++ {
			if kv[i] == '=' {
				break
			}
		}
		if i >= len(kv) {
			continue
		}
		out[kv[:i]] = kv[i+1:]
	}
	return out
}
