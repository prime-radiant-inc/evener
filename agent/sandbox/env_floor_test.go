package sandbox

import (
	"slices"
	"strings"
	"testing"
)

func envValue(env []string, name string) (string, bool) {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == name {
			return v, true
		}
	}
	return "", false
}

func TestEnvFloorDropsAgentAndCloudVars(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"SSH_AUTH_SOCK=/run/user/1000/ssh-agent.sock",
		"AWS_ACCESS_KEY_ID=AKIA",
		"AWS_SESSION_TOKEN=tok",
		"GOOGLE_APPLICATION_CREDENTIALS=/x/creds.json",
		"GCLOUD_PROJECT=p",
		"VAULT_TOKEN=v",
		"HOME=/home/u",
	}
	out := ApplyEnvFloor(in, ResolvedPolicy{Mode: ModeRestricted, CacheStrategy: CacheNone}, "")

	for _, dropped := range []string{"SSH_AUTH_SOCK", "AWS_ACCESS_KEY_ID", "AWS_SESSION_TOKEN", "GOOGLE_APPLICATION_CREDENTIALS", "GCLOUD_PROJECT", "VAULT_TOKEN"} {
		if _, ok := envValue(out, dropped); ok {
			t.Errorf("floor must drop %q: %v", dropped, out)
		}
	}
	// The floor composes on top of the existing scrub — ordinary vars survive.
	if v, ok := envValue(out, "PATH"); !ok || v != "/usr/bin" {
		t.Errorf("floor must keep PATH: %v", out)
	}
	if _, ok := envValue(out, "HOME"); !ok {
		t.Errorf("floor must keep HOME: %v", out)
	}
}

func TestEnvFloorRedirectsCacheWhenSessionPrivate(t *testing.T) {
	tmp := "/tmp/serf-session-xyz"
	in := []string{"GOCACHE=/home/u/.cache/go-build", "CARGO_HOME=/home/u/.cargo", "npm_config_cache=/home/u/.npm"}
	out := ApplyEnvFloor(in, ResolvedPolicy{Mode: ModeWorkspaceWrite, CacheStrategy: CacheSessionPrivate}, tmp)

	for name, wantSuffix := range map[string]string{"GOCACHE": "/gocache", "npm_config_cache": "/npm", "CARGO_HOME": "/cargo"} {
		v, ok := envValue(out, name)
		if !ok || !strings.HasPrefix(v, tmp) || !strings.HasSuffix(v, wantSuffix) {
			t.Errorf("%s must redirect into the session tmp, got %q", name, v)
		}
	}
	if v, _ := envValue(out, "TMPDIR"); v != tmp {
		t.Errorf("TMPDIR must point at the session tmp, got %q", v)
	}
}

func TestEnvFloorKeepsRealCacheUnderOverlay(t *testing.T) {
	// With an overlay cache strategy the real cache paths stay (bwrap overlays
	// them read-real/write-private); the floor must not redirect the env.
	in := []string{"GOCACHE=/home/u/.cache/go-build"}
	out := ApplyEnvFloor(in, ResolvedPolicy{Mode: ModeWorkspaceWrite, CacheStrategy: CacheOverlay}, "/tmp/s")
	if v, _ := envValue(out, "GOCACHE"); v != "/home/u/.cache/go-build" {
		t.Errorf("overlay strategy must not redirect GOCACHE, got %q", v)
	}
}

func TestEnvFloorDropsExternalKubeconfigKeepsInternal(t *testing.T) {
	policy := ResolvedPolicy{
		Mode: ModeWorkspaceWrite, CacheStrategy: CacheNone,
		Git: GitLayout{WorktreeRoot: "/work/proj"},
	}
	external := ApplyEnvFloor([]string{"KUBECONFIG=/home/u/.kube/config"}, policy, "")
	if _, ok := envValue(external, "KUBECONFIG"); ok {
		t.Errorf("floor must drop a worktree-external KUBECONFIG: %v", external)
	}
	internal := ApplyEnvFloor([]string{"KUBECONFIG=/work/proj/kubeconfig"}, policy, "")
	if v, ok := envValue(internal, "KUBECONFIG"); !ok || v != "/work/proj/kubeconfig" {
		t.Errorf("floor must keep an in-worktree KUBECONFIG: %v", internal)
	}
}

func TestEnvFloorReturnsFreshSlice(t *testing.T) {
	in := []string{"PATH=/usr/bin", "SSH_AUTH_SOCK=/x"}
	before := slices.Clone(in)
	_ = ApplyEnvFloor(in, ResolvedPolicy{Mode: ModeRestricted}, "")
	if !slices.Equal(in, before) {
		t.Errorf("ApplyEnvFloor must not mutate its input: %v", in)
	}
}
