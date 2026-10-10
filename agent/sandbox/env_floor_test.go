package sandbox

import (
	"path/filepath"
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
	tmp := "/tmp/evener-session-xyz"
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

// Under the session-private strategy only the session scratch is writable, and
// an ambient GOMODCACHE can name any directory, so it redirects into the scratch
// exactly like GOCACHE, whatever GOPATH says.
func TestEnvFloorRedirectsGoModCacheWhenSessionPrivate(t *testing.T) {
	tmp := "/tmp/evener-session-xyz"
	in := []string{"GOPATH=/custom/gopath", "GOMODCACHE=/custom/gopath/pkg/mod"}
	out := ApplyEnvFloor(in, ResolvedPolicy{Mode: ModeRestricted, CacheStrategy: CacheSessionPrivate}, tmp)

	v, ok := envValue(out, "GOMODCACHE")
	if !ok || !strings.HasPrefix(v, tmp) {
		t.Errorf("GOMODCACHE must redirect into the session tmp under a custom GOPATH, got %q", v)
	}
}

// Go keeps the checksum database's tree heads in $GOPATH/pkg/sumdb, a path
// GOMODCACHE does not move, and a cold download needs to write there (#4177).
// Go writes only to the first GOPATH entry, so the session scratch goes first.
// Where the spawned layer reads anywhere, the ambient GOPATH follows for
// GOPATH-mode source lookups: the spawn's own GOPATH when it has one, else the
// one resolved at session start, which covers a GOPATH set only with
// `go env -w`. Restricted mode cannot read it, so it gets the scratch alone.
func TestEnvFloorPutsScratchFirstOnGoPathWhenSessionPrivate(t *testing.T) {
	tmp := "/tmp/evener-session-xyz"
	scratchGoPath := tmp + "/gopath"
	sep := string(filepath.ListSeparator)
	readAnywhere := func(host HostFacts) ResolvedPolicy {
		return ResolvedPolicy{Mode: ModeWorkspaceWrite, CacheStrategy: CacheSessionPrivate, Spawned: AccessScope{Read: ReadAnywhere}, resolveHost: host}
	}
	restricted := ResolvedPolicy{Mode: ModeRestricted, CacheStrategy: CacheSessionPrivate, Spawned: AccessScope{Read: ReadWorktreeOnly}, resolveHost: HostFacts{GoPath: "/from/go/env"}}
	for _, tc := range []struct {
		name   string
		policy ResolvedPolicy
		in     []string
		want   string
	}{
		{"spawn env", readAnywhere(HostFacts{GoPath: "/from/go/env"}), []string{"GOPATH=/custom/a" + sep + "relative" + sep + "/custom/b"}, scratchGoPath + sep + "/custom/a" + sep + "/custom/b"},
		{"go env -w", readAnywhere(HostFacts{GoPath: "/from/go/env"}), []string{"HOME=/home/u"}, scratchGoPath + sep + "/from/go/env"},
		{"default", readAnywhere(HostFacts{Home: "/home/u"}), []string{"HOME=/home/u"}, scratchGoPath + sep + "/home/u/go"},
		{"no home", readAnywhere(HostFacts{}), []string{"HOME=/home/u"}, scratchGoPath},
		// A clean environment (EnvPolicyNone) carries neither the host's GOPATH
		// nor anything a go command could find its env file or default with, so
		// the host's settings stay out of it too.
		{"clean env", readAnywhere(HostFacts{Home: "/home/u", GoPath: "/from/go/env"}), nil, scratchGoPath},
		{"restricted", restricted, []string{"GOPATH=/custom/a"}, scratchGoPath},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := ApplyEnvFloor(tc.in, tc.policy, tmp)
			// envValue reads the first GOPATH, so a kept ambient entry fails here too.
			if v, _ := envValue(out, "GOPATH"); v != tc.want {
				t.Errorf("GOPATH = %q, want %q", v, tc.want)
			}
			// A spawn site may floor an env that was already floored; the scratch
			// entry must not repeat.
			again := ApplyEnvFloor(out, tc.policy, tmp)
			if v, _ := envValue(again, "GOPATH"); v != tc.want {
				t.Errorf("re-floored GOPATH = %q, want %q", v, tc.want)
			}
		})
	}
}

func TestEnvFloorKeepsRealCacheUnderOverlay(t *testing.T) {
	// With an overlay cache strategy the real cache paths the overlay serves stay
	// (bwrap overlays them read-real/write-private); the floor must not redirect.
	in := []string{"GOCACHE=/home/u/.cache/go-build"}
	out := ApplyEnvFloor(in, ResolvedPolicy{Mode: ModeWorkspaceWrite, CacheStrategy: CacheOverlay, CacheRoots: []string{"/home/u/.cache"}}, "/tmp/s")
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
		"EVENER_SCRATCH_DIR=/ambient/evener",
		"HOME=/home/jesse",
		"GOCACHE=/cache/go",
		"npm_config_cache=/cache/npm",
		"CARGO_HOME=/cache/cargo",
	}
	out := ApplySessionScratchEnv(in, "/tmp/evener-sandbox-owned")
	for _, name := range []string{"TMPDIR", "EVENER_SCRATCH_DIR"} {
		if got, _ := envValue(out, name); got != "/tmp/evener-sandbox-owned" {
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
		"EVENER_SCRATCH_DIR=/ambient/evener",
		"HOME=/home/jesse",
		"GOCACHE=/cache/go",
		"npm_config_cache=/cache/npm",
		"CARGO_HOME=/cache/cargo",
	}
	policy := ResolvedPolicy{
		Mode: ModeWorkspaceWrite, CacheStrategy: CacheNone,
		Git: GitLayout{WorktreeRoot: "/workspace"},
	}
	out := ApplyEnvFloor(in, policy, "/tmp/evener-sandbox-owned")
	for _, name := range []string{
		"SSH_AUTH_SOCK", "AWS_ACCESS_KEY_ID", "GOOGLE_APPLICATION_CREDENTIALS",
		"GCLOUD_PROJECT", "VAULT_TOKEN", "KUBECONFIG",
	} {
		if _, ok := envValue(out, name); ok {
			t.Errorf("security filter retained %s: %v", name, out)
		}
	}
	for _, name := range []string{"TMPDIR", "EVENER_SCRATCH_DIR"} {
		if got, _ := envValue(out, name); got != "/tmp/evener-sandbox-owned" {
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

// Under the overlay strategy a Go cache variable the overlay does not serve (its
// root was dropped, say inside the worktree) would stay persistently writable,
// so it goes to the session scratch like the session-private strategy's. One the
// overlay serves keeps its real path.
func TestEnvFloorRedirectsGoCacheVarsTheOverlayDoesNotServe(t *testing.T) {
	tmp := "/tmp/evener-session-xyz"
	policy := ResolvedPolicy{Mode: ModeWorkspaceWrite, CacheStrategy: CacheOverlay, CacheRoots: []string{"/home/u/go/pkg", "/home/u/.cache"}}
	out := ApplyEnvFloor([]string{"GOMODCACHE=/work/project/.mod", "GOCACHE=/home/u/.cache/go-build"}, policy, tmp)
	if v, _ := envValue(out, "GOMODCACHE"); !strings.HasPrefix(v, tmp+"/") {
		t.Errorf("an unserved GOMODCACHE must move into the session scratch, got %q", v)
	}
	if v, _ := envValue(out, "GOCACHE"); v != "/home/u/.cache/go-build" {
		t.Errorf("a served GOCACHE must keep its real path, got %q", v)
	}
}

// When the go env file exists but could not be read at session start, the
// overlay cannot know where go will write, so the Go caches go to the session
// scratch instead.
func TestEnvFloorRedirectsGoCachesWhenTheGoEnvFileWasUnreadable(t *testing.T) {
	tmp := "/tmp/evener-session-xyz"
	policy := ResolvedPolicy{Mode: ModeWorkspaceWrite, CacheStrategy: CacheOverlay, CacheRoots: []string{"/home/u/go/pkg"}, resolveHost: HostFacts{GoEnvUnreadable: true}}
	out := ApplyEnvFloor(nil, policy, tmp)
	for _, name := range []string{"GOCACHE", "GOMODCACHE", "GOPATH"} {
		if v, ok := envValue(out, name); !ok || !strings.HasPrefix(v, tmp+"/") {
			t.Errorf("%s must move into the session scratch, got %q (ok=%v)", name, v, ok)
		}
	}
}
