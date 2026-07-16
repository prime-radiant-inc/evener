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
		"GNUPGHOME=/home/u/.gnupg-alt",
		"DOCKER_HOST=unix:///run/docker.sock",
		"HOME=/home/u",
	}
	out := ApplyEnvFloor(in, ResolvedPolicy{Mode: ModeRestricted, CacheStrategy: CacheNone}, "")
	for _, dropped := range []string{"SSH_AUTH_SOCK", "AWS_ACCESS_KEY_ID", "AWS_SESSION_TOKEN", "GOOGLE_APPLICATION_CREDENTIALS", "GCLOUD_PROJECT", "VAULT_TOKEN", "GNUPGHOME", "DOCKER_HOST"} {
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

// A colon-separated KUBECONFIG (kubectl merges every entry) must be dropped when
// ANY absolute entry lands outside the granted roots: keeping the var would leave
// the external cluster config merged into the sandboxed session's kubeconfig.
func TestEnvFloorDropsKubeconfigListWithExternalEntry(t *testing.T) {
	policy := ResolvedPolicy{
		Mode: ModeWorkspaceWrite, CacheStrategy: CacheNone,
		Git: GitLayout{WorktreeRoot: "/work/proj"},
	}
	// One in-worktree entry, one external entry: the external one must poison the
	// whole var, so the floor drops it.
	mixed := ApplyEnvFloor([]string{"KUBECONFIG=/work/proj/kc:/home/u/.kube/config"}, policy, "")
	if _, ok := envValue(mixed, "KUBECONFIG"); ok {
		t.Errorf("floor must drop a KUBECONFIG list containing an external entry: %v", mixed)
	}
	// All entries in-worktree: the var survives.
	internal := ApplyEnvFloor([]string{"KUBECONFIG=/work/proj/a:/work/proj/b"}, policy, "")
	if v, ok := envValue(internal, "KUBECONFIG"); !ok || v != "/work/proj/a:/work/proj/b" {
		t.Errorf("floor must keep a KUBECONFIG list whose entries are all in-worktree: %v", internal)
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

func TestApplySessionScratchEnvReplacesBothVariablesOnly(t *testing.T) {
	in := []string{
		"TMPDIR=/ambient/tmp",
		"SERF_SCRATCH_DIR=/ambient/serf",
		"HOME=/home/jesse",
		"GOCACHE=/cache/go",
		"npm_config_cache=/cache/npm",
		"CARGO_HOME=/cache/cargo",
	}
	out := ApplySessionScratchEnv(in, "/tmp/serf-sandbox-owned")
	for _, name := range []string{"TMPDIR", "SERF_SCRATCH_DIR"} {
		if got, _ := envValue(out, name); got != "/tmp/serf-sandbox-owned" {
			t.Fatalf("%s = %q, want session scratch", name, got)
		}
	}
	for name, want := range map[string]string{
		"HOME": "/home/jesse", "GOCACHE": "/cache/go",
		"npm_config_cache": "/cache/npm", "CARGO_HOME": "/cache/cargo",
	} {
		if got, _ := envValue(out, name); got != want {
			t.Fatalf("%s = %q, want unchanged %q", name, got, want)
		}
	}
}

func TestEnvFloorScratchPreservesSecurityFilters(t *testing.T) {
	in := []string{
		"SSH_AUTH_SOCK=/run/ssh-agent.sock",
		"AWS_ACCESS_KEY_ID=AKIA",
		"GOOGLE_APPLICATION_CREDENTIALS=/outside/google.json",
		"GCLOUD_PROJECT=secret-project",
		"VAULT_TOKEN=secret",
		"KUBECONFIG=/outside/kubeconfig",
		"TMPDIR=/ambient/tmp",
		"SERF_SCRATCH_DIR=/ambient/serf",
		"HOME=/home/jesse",
		"GOCACHE=/cache/go",
		"npm_config_cache=/cache/npm",
		"CARGO_HOME=/cache/cargo",
	}
	policy := ResolvedPolicy{
		Mode: ModeWorkspaceWrite, CacheStrategy: CacheNone,
		Git: GitLayout{WorktreeRoot: "/workspace"},
	}
	out := ApplyEnvFloor(in, policy, "/tmp/serf-sandbox-owned")
	for _, name := range []string{
		"SSH_AUTH_SOCK", "AWS_ACCESS_KEY_ID", "GOOGLE_APPLICATION_CREDENTIALS",
		"GCLOUD_PROJECT", "VAULT_TOKEN", "KUBECONFIG",
	} {
		if _, ok := envValue(out, name); ok {
			t.Errorf("security filter retained %s: %v", name, out)
		}
	}
	for _, name := range []string{"TMPDIR", "SERF_SCRATCH_DIR"} {
		if got, _ := envValue(out, name); got != "/tmp/serf-sandbox-owned" {
			t.Errorf("%s = %q, want session scratch", name, got)
		}
	}
	for name, want := range map[string]string{
		"HOME": "/home/jesse", "GOCACHE": "/cache/go",
		"npm_config_cache": "/cache/npm", "CARGO_HOME": "/cache/cargo",
	} {
		if got, _ := envValue(out, name); got != want {
			t.Errorf("%s = %q, want unchanged %q", name, got, want)
		}
	}
}
