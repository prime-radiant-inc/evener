package sandbox

import (
	"os"
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
	goEnv := writeGoEnvFile(t, "GOPATH=/from/go/env\n")
	host := HostFacts{OS: "linux", Home: "/home/u", GoPath: "/from/go/env", GoEnvFile: goEnv}
	restricted := ResolvedPolicy{Mode: ModeRestricted, CacheStrategy: CacheSessionPrivate, Spawned: AccessScope{Read: ReadWorktreeOnly}, resolveHost: host}
	for _, tc := range []struct {
		name   string
		policy ResolvedPolicy
		in     []string
		want   string
	}{
		{"spawn env", readAnywhere(host), []string{"GOPATH=/custom/a" + sep + "relative" + sep + "/custom/b"}, scratchGoPath + sep + "/custom/a" + sep + "/custom/b"},
		{"go env -w", readAnywhere(host), []string{"HOME=/home/u", "GOENV=" + goEnv}, scratchGoPath + sep + "/from/go/env"},
		// A command run with its own HOME or GOENV would read a different env
		// file, so it gets Go's default for its own HOME, not the host's setting.
		{"overridden HOME", readAnywhere(host), []string{"HOME=/tmp/isolated"}, scratchGoPath + sep + "/tmp/isolated/go"},
		{"overridden GOENV", readAnywhere(host), []string{"HOME=/home/u", "GOENV=/other/env"}, scratchGoPath + sep + "/home/u/go"},
		{"default", readAnywhere(HostFacts{OS: "linux", Home: "/home/u", GoEnvFile: "/home/u/.config/go/env"}), []string{"HOME=/home/u"}, scratchGoPath + sep + "/home/u/go"},
		// A clean environment (EnvPolicyNone) carries neither the host's GOPATH
		// nor anything a go command could find its env file or default with, so
		// the host's settings stay out of it too.
		{"clean env", readAnywhere(host), nil, scratchGoPath},
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
	home := servedHome(t)
	// With an overlay cache strategy the real cache paths the overlay serves stay
	// (bwrap overlays them read-real/write-private); the floor must not redirect.
	in := []string{"GOCACHE=" + home + "/.cache/go-build"}
	out := ApplyEnvFloor(in, ResolvedPolicy{Mode: ModeWorkspaceWrite, CacheStrategy: CacheOverlay, CacheRoots: []string{home + "/.cache"}}, "/tmp/s")
	if v, _ := envValue(out, "GOCACHE"); v != home+"/.cache/go-build" {
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
	home := servedHome(t)
	tmp := "/tmp/evener-session-xyz"
	policy := ResolvedPolicy{Mode: ModeWorkspaceWrite, CacheStrategy: CacheOverlay, CacheRoots: []string{home + "/go/pkg", home + "/.cache"}}
	out := ApplyEnvFloor([]string{"GOMODCACHE=/work/project/.mod", "GOCACHE=" + home + "/.cache/go-build"}, policy, tmp)
	if v, _ := envValue(out, "GOMODCACHE"); !strings.HasPrefix(v, tmp+"/") {
		t.Errorf("an unserved GOMODCACHE must move into the session scratch, got %q", v)
	}
	if v, _ := envValue(out, "GOCACHE"); v != home+"/.cache/go-build" {
		t.Errorf("a served GOCACHE must keep its real path, got %q", v)
	}
}

// Under the overlay, a Go cache go resolves from the host's settings rather
// than the spawn's environment (a go env -w GOMODCACHE, or the default under a
// GOPATH) is redirected too when the overlay does not serve it, such as one
// dropped for lying inside the worktree.
func TestEnvFloorRedirectsResolvedGoCachesTheOverlayDoesNotServe(t *testing.T) {
	home := servedHome(t)
	tmp := "/tmp/evener-session-xyz"
	goEnv := writeGoEnvFile(t, "GOMODCACHE=/work/project/.mod\nGOPATH=/work/project/gopath\n")
	host := HostFacts{OS: "linux", Home: home, GoModCache: "/work/project/.mod", GoPath: "/work/project/gopath", GoEnvFile: goEnv}
	policy := ResolvedPolicy{Mode: ModeWorkspaceWrite, CacheStrategy: CacheOverlay, CacheRoots: []string{home + "/.cache", home + "/go/pkg"}, Spawned: AccessScope{Read: ReadAnywhere}, resolveHost: host}
	out := ApplyEnvFloor([]string{"HOME=" + home, "GOENV=" + goEnv}, policy, tmp)
	if v, _ := envValue(out, "GOMODCACHE"); !strings.HasPrefix(v, tmp+"/") {
		t.Errorf("an unserved go env -w GOMODCACHE must move into the session scratch, got %q", v)
	}
	if v, _ := envValue(out, "GOPATH"); !strings.HasPrefix(v, tmp+"/") {
		t.Errorf("an unserved GOPATH must get the session scratch first, got %q", v)
	}
	if _, ok := envValue(out, "GOCACHE"); ok {
		t.Errorf("a GOCACHE the overlay serves (~/.cache/go-build) must be left alone: %v", out)
	}
}

// A spawn that points GOENV at a different file may configure its caches
// anywhere, so the host's resolved settings say nothing about it: under the
// overlay its Go caches go to the session scratch.
func TestEnvFloorRedirectsGoCachesForAnAlternateGoEnvFile(t *testing.T) {
	home := servedHome(t)
	tmp := "/tmp/evener-session-xyz"
	host := HostFacts{OS: "linux", Home: home, GoEnvFile: home + "/.config/go/env"}
	policy := ResolvedPolicy{Mode: ModeWorkspaceWrite, CacheStrategy: CacheOverlay, CacheRoots: []string{home + "/.cache", home + "/go/pkg"}, Spawned: AccessScope{Read: ReadAnywhere}, resolveHost: host}
	out := ApplyEnvFloor([]string{"HOME=" + home, "GOENV=/work/project/go.env"}, policy, tmp)
	for _, name := range []string{"GOCACHE", "GOMODCACHE", "GOPATH"} {
		if v, _ := envValue(out, name); !strings.HasPrefix(v, tmp+"/") {
			t.Errorf("%s must move into the session scratch for an alternate GOENV, got %q", name, v)
		}
	}
	// The same file the probe read keeps the host's served settings.
	same := ApplyEnvFloor([]string{"HOME=" + home}, policy, tmp)
	if _, ok := envValue(same, "GOCACHE"); ok {
		t.Errorf("the host's own go env file must not redirect a served GOCACHE: %v", same)
	}
}

// The caches a spawned command writes follow its own environment: a child with
// its own XDG_CACHE_HOME (here inside the worktree, which the overlay does not
// serve) gets its GOCACHE redirected rather than judged by the host's value,
// and a GOPATH list is read past a leading relative entry.
func TestEnvFloorJudgesTheChildsOwnCacheLocations(t *testing.T) {
	home := servedHome(t)
	tmp := "/tmp/evener-session-xyz"
	host := HostFacts{OS: "linux", Home: home, XDGCacheHome: home + "/.cache", GoEnvFile: home + "/.config/go/env"}
	policy := ResolvedPolicy{Mode: ModeWorkspaceWrite, CacheStrategy: CacheOverlay, CacheRoots: []string{home + "/.cache", home + "/go/pkg", home + "/.npm", home + "/.cargo"}, Spawned: AccessScope{Read: ReadAnywhere}, resolveHost: host}

	out := ApplyEnvFloor([]string{"HOME=" + home, "XDG_CACHE_HOME=/work/project/.cache"}, policy, tmp)
	if v, _ := envValue(out, "GOCACHE"); !strings.HasPrefix(v, tmp+"/") {
		t.Errorf("a child's own unserved XDG_CACHE_HOME must send GOCACHE to the scratch, got %q", v)
	}

	// A child with its own HOME and no XDG_CACHE_HOME uses that HOME's .cache,
	// not the host's XDG_CACHE_HOME.
	out = ApplyEnvFloor([]string{"HOME=/work/project/home"}, policy, tmp)
	if v, _ := envValue(out, "GOCACHE"); !strings.HasPrefix(v, tmp+"/") {
		t.Errorf("a child's own HOME decides its default GOCACHE; an unserved one must go to the scratch, got %q", v)
	}

	sep := string(filepath.ListSeparator)
	out = ApplyEnvFloor([]string{"HOME=" + home, "GOPATH=relative" + sep + "/work/project/gopath"}, policy, tmp)
	if v, _ := envValue(out, "GOMODCACHE"); !strings.HasPrefix(v, tmp+"/") {
		t.Errorf("the first absolute GOPATH entry decides GOMODCACHE; an unserved one must go to the scratch, got %q", v)
	}

	out = ApplyEnvFloor([]string{"HOME=" + home}, policy, tmp)
	for _, name := range []string{"GOCACHE", "GOMODCACHE", "GOPATH"} {
		if _, ok := envValue(out, name); ok {
			t.Errorf("a served default %s must be left alone: %v", name, out)
		}
	}
}

// A child reads the go env file's values, not the host process's environment
// overrides: a GOMODCACHE the host overrode in its environment, but which the
// shared env file sets inside the worktree, is redirected for a child that
// does not carry the override.
func TestEnvFloorJudgesAChildByTheEnvFileNotTheHostsOverrides(t *testing.T) {
	tmp := "/tmp/evener-session-xyz"
	goEnv := writeGoEnvFile(t, "GOMODCACHE=/work/project/.mod\n")
	host := HostFacts{OS: "linux", Home: "/home/u", GoEnvFile: goEnv, GoModCache: "/home/u/go/pkg/mod"}
	policy := ResolvedPolicy{Mode: ModeWorkspaceWrite, CacheStrategy: CacheOverlay, CacheRoots: []string{"/home/u/.cache", "/home/u/go/pkg"}, Spawned: AccessScope{Read: ReadAnywhere}, resolveHost: host}
	out := ApplyEnvFloor([]string{"HOME=/home/u", "GOENV=" + goEnv}, policy, tmp)
	if v, _ := envValue(out, "GOMODCACHE"); !strings.HasPrefix(v, tmp+"/") {
		t.Errorf("the env file's worktree GOMODCACHE must go to the scratch, got %q", v)
	}
}

// A cache directory under a served root that is a symlink into the worktree is
// not served: the overlay covers the root, but writes follow the link into the
// persistent workspace.
func TestEnvFloorRedirectsACacheSymlinkedOutOfItsServedRoot(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	cacheHome := filepath.Join(home, ".cache")
	if err := os.MkdirAll(cacheHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(worktree, filepath.Join(cacheHome, "go-build")); err != nil {
		t.Fatal(err)
	}
	tmp := "/tmp/evener-session-xyz"
	policy := ResolvedPolicy{Mode: ModeWorkspaceWrite, CacheStrategy: CacheOverlay, CacheRoots: []string{cacheHome}, Spawned: AccessScope{Read: ReadAnywhere}, resolveHost: HostFacts{OS: "linux", Home: home, GoEnvFile: filepath.Join(home, ".config", "go", "env")}}
	out := ApplyEnvFloor([]string{"HOME=" + home}, policy, tmp)
	if v, _ := envValue(out, "GOCACHE"); !strings.HasPrefix(v, tmp+"/") {
		t.Errorf("a GOCACHE symlinked out of its served root must go to the scratch, got %q", v)
	}
}

// A cache root the wrapper cannot create is not served, so the floor falls back
// to the scratch for what names it.
func TestNewWrapperDropsACacheRootItCannotCreate(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	unmakeable := filepath.Join(blocker, "gocache")
	facts := HostFacts{OS: "linux", Home: t.TempDir(), BwrapPath: "/usr/bin/bwrap", BwrapCapable: true, OverlaySupported: true, GoCache: unmakeable}
	cwd := MaterializeWorkspace(t, MainCheckout)
	rp, err := Resolve(SandboxPolicy{Mode: ModeWorkspaceWrite, Network: new(true)}, facts, cwd)
	if err != nil {
		t.Fatal(err)
	}
	w, err := NewWrapper(rp, facts.BwrapPath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(liveCacheRoots(w.Policy()), unmakeable) {
		t.Errorf("a cache root that could not be created must not be served: %v", w.Policy().CacheRoots)
	}
}

// A Go cache that cannot be a directory (an existing file, or one below a
// file) is not served by an overlaid ancestor: go would fail on it, so it
// goes to the scratch instead.
func TestEnvFloorRedirectsAGoCacheThatCannotBeADirectory(t *testing.T) {
	home := overlayFixtureDir(t)
	cacheHome := filepath.Join(home, ".cache")
	if err := os.MkdirAll(cacheHome, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(cacheHome, "go-build")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	tmp := "/tmp/evener-session-xyz"
	policy := ResolvedPolicy{Mode: ModeWorkspaceWrite, CacheStrategy: CacheOverlay, CacheRoots: []string{cacheHome}, Spawned: AccessScope{Read: ReadAnywhere}, resolveHost: HostFacts{OS: "linux", Home: home, GoEnvFile: filepath.Join(home, ".config", "go", "env")}}
	for _, gocache := range []string{file, filepath.Join(file, "below")} {
		out := ApplyEnvFloor([]string{"HOME=" + home, "GOCACHE=" + gocache}, policy, tmp)
		if v, _ := envValue(out, "GOCACHE"); !strings.HasPrefix(v, tmp+"/") {
			t.Errorf("GOCACHE=%s cannot be a directory and must go to the scratch, got %q", gocache, v)
		}
	}
	out := ApplyEnvFloor([]string{"HOME=" + home, "GOCACHE=" + filepath.Join(cacheHome, "not-yet")}, policy, tmp)
	if v, _ := envValue(out, "GOCACHE"); v != filepath.Join(cacheHome, "not-yet") {
		t.Errorf("a served GOCACHE not created yet must be left alone, got %q", v)
	}
}

// The floor judges a spawn by the go env file as it is at that spawn, which is
// what the spawned go reads: a `go env -w` that moves GOMODCACHE out of the
// overlay between spawns is redirected from the next spawn on.
func TestEnvFloorJudgesTheGoEnvFileAsItIsAtEachSpawn(t *testing.T) {
	home := servedHome(t)
	envFile := filepath.Join(home, ".config", "go", "env")
	if err := os.MkdirAll(filepath.Dir(envFile), 0o755); err != nil {
		t.Fatal(err)
	}
	served := filepath.Join(home, "go", "pkg", "mod")
	if err := os.WriteFile(envFile, []byte("GOMODCACHE="+served+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tmp := "/tmp/evener-session-xyz"
	policy := ResolvedPolicy{Mode: ModeWorkspaceWrite, CacheStrategy: CacheOverlay, CacheRoots: []string{filepath.Join(home, ".cache"), filepath.Join(home, "go", "pkg")}, Spawned: AccessScope{Read: ReadAnywhere}, resolveHost: HostFacts{OS: "linux", Home: home, GoEnvFile: envFile}}
	if _, ok := envValue(ApplyEnvFloor([]string{"HOME=" + home}, policy, tmp), "GOMODCACHE"); ok {
		t.Fatalf("a served GOMODCACHE from the go env file must be left alone")
	}
	if err := os.WriteFile(envFile, []byte("GOMODCACHE=/work/project/.mod\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if v, _ := envValue(ApplyEnvFloor([]string{"HOME=" + home}, policy, tmp), "GOMODCACHE"); !strings.HasPrefix(v, tmp+"/") {
		t.Errorf("a GOMODCACHE moved out of the overlay by go env -w must go to the scratch, got %q", v)
	}
}

// writeGoEnvFile writes a go env file with body and returns its path, for a
// spawn to name with GOENV.
func writeGoEnvFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(overlayFixtureDir(t), "go.env")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// A relative GOENV resolves against the spawn's own directory, which the floor
// does not know, so the Go caches go to the scratch even when the spelling
// matches the probe's file.
func TestEnvFloorRedirectsGoCachesForARelativeGoEnvFile(t *testing.T) {
	tmp := "/tmp/evener-session-xyz"
	policy := ResolvedPolicy{Mode: ModeWorkspaceWrite, CacheStrategy: CacheOverlay, CacheRoots: []string{"/home/u/.cache", "/home/u/go/pkg"}, Spawned: AccessScope{Read: ReadAnywhere}, resolveHost: HostFacts{OS: "linux", Home: "/home/u", GoEnvFile: "go.env"}}
	out := ApplyEnvFloor([]string{"HOME=/home/u", "GOENV=go.env"}, policy, tmp)
	for _, name := range []string{"GOCACHE", "GOMODCACHE", "GOPATH"} {
		if v, _ := envValue(out, name); !strings.HasPrefix(v, tmp+"/") {
			t.Errorf("%s must go to the scratch for a relative GOENV, got %q", name, v)
		}
	}
}

// A cache root removed after the wrapper was built is not overlaid at the next
// spawn, so a Go cache under it is no longer served and goes to the scratch.
func TestEnvFloorRedirectsACacheWhoseRootWasRemoved(t *testing.T) {
	home := t.TempDir()
	gocache := filepath.Join(t.TempDir(), "gocache")
	if err := os.MkdirAll(gocache, 0o755); err != nil {
		t.Fatal(err)
	}
	tmp := "/tmp/evener-session-xyz"
	policy := ResolvedPolicy{Mode: ModeWorkspaceWrite, CacheStrategy: CacheOverlay, CacheRoots: []string{gocache}, Spawned: AccessScope{Read: ReadAnywhere}, resolveHost: HostFacts{OS: "linux", Home: home}}
	env := []string{"HOME=" + home, "GOENV=off", "GOCACHE=" + gocache}
	if v, _ := envValue(ApplyEnvFloor(env, policy, tmp), "GOCACHE"); v != gocache {
		t.Fatalf("a served GOCACHE must be left alone, got %q", v)
	}
	if err := os.RemoveAll(gocache); err != nil {
		t.Fatal(err)
	}
	if v, _ := envValue(ApplyEnvFloor(env, policy, tmp), "GOCACHE"); !strings.HasPrefix(v, tmp+"/") {
		t.Errorf("a GOCACHE whose root was removed must go to the scratch, got %q", v)
	}
}

// servedHome returns a home directory holding the default cache roots, which
// the overlay serves only while they exist.
func servedHome(t *testing.T) string {
	t.Helper()
	home := overlayFixtureDir(t)
	for _, rel := range []string{".cache", "go/pkg", ".npm", ".cargo"} {
		if err := os.MkdirAll(filepath.Join(home, rel), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

// A cache root holding a persistent write root is not overlaid, so a Go cache
// anywhere in it (inside the write root, or elsewhere in that cache) is not
// served and goes to the scratch.
func TestEnvFloorRedirectsGoCachesInACacheHoldingAWritableRoot(t *testing.T) {
	home := servedHome(t)
	library := filepath.Join(home, ".cache", "library")
	if err := os.MkdirAll(library, 0o755); err != nil {
		t.Fatal(err)
	}
	facts := HostFacts{OS: "linux", Home: home, BwrapPath: "/usr/bin/bwrap", BwrapCapable: true, OverlaySupported: true}
	cwd := MaterializeWorkspace(t, MainCheckout)
	rp, err := Resolve(SandboxPolicy{Mode: ModeWorkspaceWrite, Network: new(true), ExtraWritableRoots: []string{library}}, facts, cwd)
	if err != nil {
		t.Fatal(err)
	}
	tmp := "/tmp/evener-session-xyz"
	out := ApplyEnvFloor([]string{"HOME=" + home, "GOENV=off", "GOCACHE=" + filepath.Join(library, "go-build")}, rp, tmp)
	if v, _ := envValue(out, "GOCACHE"); !strings.HasPrefix(v, tmp+"/") {
		t.Errorf("a GOCACHE inside a writable root must go to the scratch, got %q", v)
	}
	out = ApplyEnvFloor([]string{"HOME=" + home, "GOENV=off", "GOCACHE=" + filepath.Join(home, ".cache", "go-build")}, rp, tmp)
	if _, ok := envValue(out, "GOCACHE"); !ok {
		t.Fatalf("GOCACHE missing: %v", out)
	}
	if v, _ := envValue(out, "GOCACHE"); !strings.HasPrefix(v, tmp+"/") {
		t.Errorf("a GOCACHE elsewhere in a cache that is not overlaid must go to the scratch, got %q", v)
	}
}

// A Go cache whose root's symlinked parent was retargeted into the worktree
// after resolution is no longer served (bwrap skips its overlay), so the floor
// sends it to the scratch at that spawn.
func TestEnvFloorRedirectsACacheRootRetargetedIntoTheWorktree(t *testing.T) {
	base := overlayFixtureDir(t)
	realDir := filepath.Join(base, "real")
	if err := os.MkdirAll(filepath.Join(realDir, "cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}
	gocache := filepath.Join(link, "cache")
	facts := HostFacts{OS: "linux", Home: servedHome(t), BwrapPath: "/usr/bin/bwrap", BwrapCapable: true, OverlaySupported: true, GoCache: gocache}
	cwd := MaterializeWorkspace(t, MainCheckout)
	rp, err := Resolve(SandboxPolicy{Mode: ModeWorkspaceWrite, Network: new(true)}, facts, cwd)
	if err != nil {
		t.Fatal(err)
	}
	tmp := "/tmp/evener-session-xyz"
	env := []string{"HOME=" + facts.Home, "GOENV=off", "GOCACHE=" + gocache}
	if _, ok := envValue(ApplyEnvFloor(env, rp, tmp), "GOCACHE"); !ok {
		t.Fatal("GOCACHE missing")
	}
	if v, _ := envValue(ApplyEnvFloor(env, rp, tmp), "GOCACHE"); strings.HasPrefix(v, tmp+"/") {
		t.Fatalf("fixture: the cache must be served at first, got %q", v)
	}
	if err := os.MkdirAll(filepath.Join(cwd, "sub", "cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(cwd, "sub"), link); err != nil {
		t.Fatal(err)
	}
	if v, _ := envValue(ApplyEnvFloor(env, rp, tmp), "GOCACHE"); !strings.HasPrefix(v, tmp+"/") {
		t.Errorf("a cache retargeted into the worktree must go to the scratch, got %q", v)
	}
}

// A persistent write root inside a Go cache directory (a writable
// $HOME/go/pkg/sumdb) keeps $HOME/go/pkg from being overlaid, so GOPATH and
// the module cache under it go to the scratch.
func TestEnvFloorRedirectsAGoCacheHoldingAWritableRoot(t *testing.T) {
	home := servedHome(t)
	sumdb := filepath.Join(home, "go", "pkg", "sumdb")
	if err := os.MkdirAll(sumdb, 0o755); err != nil {
		t.Fatal(err)
	}
	facts := HostFacts{OS: "linux", Home: home, BwrapPath: "/usr/bin/bwrap", BwrapCapable: true, OverlaySupported: true}
	cwd := MaterializeWorkspace(t, MainCheckout)
	rp, err := Resolve(SandboxPolicy{Mode: ModeWorkspaceWrite, Network: new(true), ExtraWritableRoots: []string{sumdb}}, facts, cwd)
	if err != nil {
		t.Fatal(err)
	}
	tmp := "/tmp/evener-session-xyz"
	out := ApplyEnvFloor([]string{"HOME=" + home, "GOENV=off"}, rp, tmp)
	if v, _ := envValue(out, "GOPATH"); !strings.HasPrefix(v, tmp+"/") {
		t.Errorf("a GOPATH whose pkg holds a writable root must get the scratch first, got %q", v)
	}
	if v, ok := envValue(out, "GOMODCACHE"); ok && !strings.HasPrefix(v, tmp+"/") {
		t.Errorf("a module cache in a cache that is not overlaid must not stay there, got %q", v)
	}
}

// A Go cache spelled through a different path to the same storage as an
// overlaid root (GOMODCACHE=/ssd/go/pkg/mod with $HOME/go linked to /ssd/go)
// is served by that overlay and left alone.
func TestEnvFloorServesACacheSpelledThroughAnAlias(t *testing.T) {
	home := servedHome(t)
	ssd := filepath.Join(overlayFixtureDir(t), "go")
	if err := os.MkdirAll(filepath.Join(ssd, "pkg", "mod"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(home, "go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(ssd, filepath.Join(home, "go")); err != nil {
		t.Fatal(err)
	}
	modcache := filepath.Join(ssd, "pkg", "mod")
	facts := HostFacts{OS: "linux", Home: home, BwrapPath: "/usr/bin/bwrap", BwrapCapable: true, OverlaySupported: true}
	rp, err := Resolve(SandboxPolicy{Mode: ModeWorkspaceWrite, Network: new(true)}, facts, MaterializeWorkspace(t, MainCheckout))
	if err != nil {
		t.Fatal(err)
	}
	tmp := "/tmp/evener-session-xyz"
	out := ApplyEnvFloor([]string{"HOME=" + home, "GOENV=off", "GOMODCACHE=" + modcache}, rp, tmp)
	if v, _ := envValue(out, "GOMODCACHE"); v != modcache {
		t.Errorf("a GOMODCACHE the overlay serves under another spelling must be left alone, got %q", v)
	}
}

// A relative GOENV resolves against the spawn's own directory, which the floor
// does not know, so its settings are unknown and the Go caches go to the
// scratch, but the spawn keeps reading it: turning it off would drop its
// other settings (GOPROXY, GOPRIVATE) and could send private module paths to
// public services. A FIFO there would be the session's own doing.
func TestEnvFloorKeepsARelativeGoEnvForTheSpawn(t *testing.T) {
	policy := ResolvedPolicy{Mode: ModeWorkspaceWrite, CacheStrategy: CacheOverlay, CacheRoots: []string{"/home/u/.cache"}, Spawned: AccessScope{Read: ReadAnywhere}, resolveHost: HostFacts{OS: "linux", Home: "/home/u"}}
	out := ApplyEnvFloor([]string{"HOME=/home/u", "GOENV=go.env"}, policy, "/tmp/s")
	if v, _ := envValue(out, "GOENV"); v != "go.env" {
		t.Errorf("a relative GOENV must be left for the spawn, got %q", v)
	}
	if v, _ := envValue(out, "GOCACHE"); !strings.HasPrefix(v, "/tmp/s/") {
		t.Errorf("a relative GOENV leaves the caches unknown, so GOCACHE must go to the scratch, got %q", v)
	}
}

// bwrap gives the spawn a private /tmp, so a go env file under /tmp (outside
// the session scratch) is not the file the spawn's go reads: its settings are
// unknown, and the Go caches go to the scratch.
func TestEnvFloorRedirectsGoCachesForAGoEnvFileInTmp(t *testing.T) {
	home := servedHome(t)
	dir, err := os.MkdirTemp("/tmp", "evener-goenv-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	envFile := filepath.Join(dir, "go.env")
	if err := os.WriteFile(envFile, []byte("GOCACHE="+filepath.Join(home, ".cache", "go-build")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tmp := "/tmp/evener-session-xyz"
	policy := ResolvedPolicy{Mode: ModeWorkspaceWrite, CacheStrategy: CacheOverlay, CacheRoots: []string{filepath.Join(home, ".cache"), filepath.Join(home, "go", "pkg")}, Spawned: AccessScope{Read: ReadAnywhere}, resolveHost: HostFacts{OS: "linux", Home: home, GoEnvFile: envFile}}
	out := ApplyEnvFloor([]string{"HOME=" + home, "GOENV=" + envFile}, policy, tmp)
	if v, _ := envValue(out, "GOCACHE"); !strings.HasPrefix(v, tmp+"/") {
		t.Errorf("a go env file in the host /tmp must not decide the spawn's caches, got GOCACHE=%q", v)
	}
}

// An explicit empty GOMODCACHE is unset to go, so it is pinned to the served
// module cache like an absent one when GOPATH moves to the scratch: here a
// writable $HOME/go/pkg/sumdb keeps $HOME/go/pkg from being overlaid, while
// the module cache inside it, configured on the host as its own cache root,
// shares no storage with that write root and is still served.
func TestEnvFloorPinsAnEmptyGoModCacheWhenGoPathMoves(t *testing.T) {
	home := servedHome(t)
	sumdb := filepath.Join(home, "go", "pkg", "sumdb")
	modcache := filepath.Join(home, "go", "pkg", "mod")
	for _, d := range []string{sumdb, modcache} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	facts := HostFacts{OS: "linux", Home: home, BwrapPath: "/usr/bin/bwrap", BwrapCapable: true, OverlaySupported: true, GoModCache: modcache}
	rp, err := Resolve(SandboxPolicy{Mode: ModeWorkspaceWrite, Network: new(true), ExtraWritableRoots: []string{sumdb}}, facts, MaterializeWorkspace(t, MainCheckout))
	if err != nil {
		t.Fatal(err)
	}
	out := ApplyEnvFloor([]string{"HOME=" + home, "GOENV=off", "GOMODCACHE="}, rp, "/tmp/evener-session-xyz")
	if v, _ := envValue(out, "GOPATH"); !strings.HasPrefix(v, "/tmp/evener-session-xyz/") {
		t.Fatalf("fixture: GOPATH must move to the scratch, got %q", v)
	}
	vals := []string{}
	for _, kv := range out {
		if v, ok := strings.CutPrefix(kv, "GOMODCACHE="); ok {
			vals = append(vals, v)
		}
	}
	if len(vals) != 1 || vals[0] != modcache {
		t.Errorf("an empty GOMODCACHE must be replaced by the served module cache, got %v", vals)
	}
}

// A cache root reached through a link under bwrap's private /tmp is not
// overlaid, so a spawn's GOCACHE naming the storage behind that link is not
// served and goes to the scratch.
func TestEnvFloorRedirectsACacheBehindALinkInTmp(t *testing.T) {
	home := servedHome(t)
	store := overlayFixtureDir(t)
	if err := os.MkdirAll(filepath.Join(store, "go-build"), 0o755); err != nil {
		t.Fatal(err)
	}
	linkDir, err := os.MkdirTemp("/tmp", "evener-cache-link-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(linkDir) })
	link := filepath.Join(linkDir, "cache")
	if err := os.Symlink(store, link); err != nil {
		t.Fatal(err)
	}
	facts := HostFacts{OS: "linux", Home: home, BwrapPath: "/usr/bin/bwrap", BwrapCapable: true, OverlaySupported: true, XDGCacheHome: link}
	rp, err := Resolve(SandboxPolicy{Mode: ModeWorkspaceWrite, Network: new(true)}, facts, MaterializeWorkspace(t, MainCheckout))
	if err != nil {
		t.Fatal(err)
	}
	tmp := "/tmp/evener-session-xyz"
	out := ApplyEnvFloor([]string{"HOME=" + home, "GOENV=off", "GOCACHE=" + filepath.Join(store, "go-build")}, rp, tmp)
	if v, _ := envValue(out, "GOCACHE"); !strings.HasPrefix(v, tmp+"/") {
		t.Errorf("a GOCACHE behind a /tmp link root must go to the scratch, got %q", v)
	}
}

// A go env file named outside /tmp but linked into it is hidden from the spawn
// by bwrap's private /tmp all the same: its settings are unknown.
func TestEnvFloorRedirectsGoCachesForAGoEnvFileLinkedIntoTmp(t *testing.T) {
	home := servedHome(t)
	dir, err := os.MkdirTemp("/tmp", "evener-goenv-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if !pathUnder(resolveExistingPrefix(dir), "/tmp") {
		t.Skip("/tmp is itself a link on this host, which never uses the overlay")
	}
	target := filepath.Join(dir, "go.env")
	if err := os.WriteFile(target, []byte("GOCACHE="+filepath.Join(home, ".cache", "go-build")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	envFile := filepath.Join(home, ".config", "go", "env")
	if err := os.MkdirAll(filepath.Dir(envFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, envFile); err != nil {
		t.Fatal(err)
	}
	tmp := "/tmp/evener-session-xyz"
	policy := ResolvedPolicy{Mode: ModeWorkspaceWrite, CacheStrategy: CacheOverlay, CacheRoots: []string{filepath.Join(home, ".cache"), filepath.Join(home, "go", "pkg")}, Spawned: AccessScope{Read: ReadAnywhere}, resolveHost: HostFacts{OS: "linux", Home: home, GoEnvFile: envFile}}
	out := ApplyEnvFloor([]string{"HOME=" + home, "XDG_CACHE_HOME=/work/project/.cache"}, policy, tmp)
	if v, _ := envValue(out, "GOCACHE"); !strings.HasPrefix(v, tmp+"/") {
		t.Errorf("a go env file linked into the host /tmp must not decide the spawn's caches, got GOCACHE=%q", v)
	}
}

// A go env file whose link chain passes through a link in /tmp, even one that
// leads back out (env -> /tmp/hop -> /var/tmp/real/go.env), is hidden from the
// spawn by bwrap's private /tmp too: its settings are unknown.
func TestEnvFloorRedirectsGoCachesForAGoEnvFileLinkedThroughTmp(t *testing.T) {
	home := servedHome(t)
	dir, err := os.MkdirTemp("/tmp", "evener-goenv-hop-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if !pathUnder(resolveExistingPrefix(dir), "/tmp") {
		t.Skip("/tmp is itself a link on this host, which never uses the overlay")
	}
	realDir, err := os.MkdirTemp("/var/tmp", "evener-goenv-real-")
	if err != nil {
		t.Skipf("no writable /var/tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(realDir) })
	target := filepath.Join(realDir, "go.env")
	if err := os.WriteFile(target, []byte("GOCACHE="+filepath.Join(home, ".cache", "go-build")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hop := filepath.Join(dir, "hop")
	if err := os.Symlink(target, hop); err != nil {
		t.Fatal(err)
	}
	envFile := filepath.Join(home, ".config", "go", "env")
	if err := os.MkdirAll(filepath.Dir(envFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(hop, envFile); err != nil {
		t.Fatal(err)
	}
	tmp := "/tmp/evener-session-xyz"
	policy := ResolvedPolicy{Mode: ModeWorkspaceWrite, CacheStrategy: CacheOverlay, CacheRoots: []string{filepath.Join(home, ".cache"), filepath.Join(home, "go", "pkg")}, Spawned: AccessScope{Read: ReadAnywhere}, resolveHost: HostFacts{OS: "linux", Home: home, GoEnvFile: envFile}}
	out := ApplyEnvFloor([]string{"HOME=" + home, "XDG_CACHE_HOME=/work/project/.cache"}, policy, tmp)
	if v, _ := envValue(out, "GOCACHE"); !strings.HasPrefix(v, tmp+"/") {
		t.Errorf("a go env file linked through the host /tmp must not decide the spawn's caches, got GOCACHE=%q", v)
	}
}

// A go env file the user masked (a denylist entry over ~/.config/go) is
// hidden from the spawn, whose go then falls back to its defaults, so the
// floor must not trust the file's settings: its caches go to the scratch.
func TestEnvFloorRedirectsGoCachesForAMaskedGoEnvFile(t *testing.T) {
	home := servedHome(t)
	envFile := filepath.Join(home, ".config", "go", "env")
	if err := os.MkdirAll(filepath.Dir(envFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envFile, []byte("GOCACHE="+filepath.Join(home, ".cache", "go-build")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tmp := "/tmp/evener-session-xyz"
	policy := ResolvedPolicy{Mode: ModeWorkspaceWrite, CacheStrategy: CacheOverlay, CacheRoots: []string{filepath.Join(home, ".cache"), filepath.Join(home, "go", "pkg")}, MaskedPaths: []string{filepath.Dir(envFile)}, Spawned: AccessScope{Read: ReadAnywhere}, resolveHost: HostFacts{OS: "linux", Home: home, GoEnvFile: envFile}}
	out := ApplyEnvFloor([]string{"HOME=" + home, "XDG_CACHE_HOME=/work/project/.cache"}, policy, tmp)
	if v, _ := envValue(out, "GOCACHE"); !strings.HasPrefix(v, tmp+"/") {
		t.Errorf("a masked go env file must not decide the spawn's caches, got GOCACHE=%q", v)
	}
}

// Settings the spawn's environment gives explicitly override its go env file,
// so they are known even when a go env file exists: served ones are left
// alone, and only the settings left to the file are unknown.
func TestEnvFloorKnowsExplicitGoSettingsDespiteAGoEnvFile(t *testing.T) {
	home := servedHome(t)
	envFile := filepath.Join(home, ".config", "go", "env")
	if err := os.MkdirAll(filepath.Dir(envFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envFile, []byte("GOPROXY=direct\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	facts := HostFacts{OS: "linux", Home: home, BwrapPath: "/usr/bin/bwrap", BwrapCapable: true, OverlaySupported: true, GoEnvFile: envFile}
	rp, err := Resolve(SandboxPolicy{Mode: ModeWorkspaceWrite, Network: new(true)}, facts, MaterializeWorkspace(t, MainCheckout))
	if err != nil {
		t.Fatal(err)
	}
	tmp := "/tmp/evener-session-xyz"
	explicit := map[string]string{
		"GOCACHE":    filepath.Join(home, ".cache", "go-build"),
		"GOMODCACHE": filepath.Join(home, "go", "pkg", "mod"),
		"GOPATH":     filepath.Join(home, "go"),
	}
	env := []string{"HOME=" + home}
	for name, v := range explicit {
		env = append(env, name+"="+v)
	}
	out := ApplyEnvFloor(env, rp, tmp)
	for name, want := range explicit {
		if v, _ := envValue(out, name); v != want {
			t.Errorf("an explicit, served %s must be left alone despite a go env file, got %q", name, v)
		}
	}
	// A go env file the floor cannot judge (here another file than the
	// probe's) leaves unknown only what the environment does not set.
	other := filepath.Join(overlayFixtureDir(t), "other.env")
	if err := os.WriteFile(other, []byte("GOPROXY=direct\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out = ApplyEnvFloor([]string{"HOME=" + home, "GOENV=" + other, "GOCACHE=" + explicit["GOCACHE"]}, rp, tmp)
	if v, _ := envValue(out, "GOCACHE"); v != explicit["GOCACHE"] {
		t.Errorf("an explicit GOCACHE must stay known, got %q", v)
	}
	if v, _ := envValue(out, "GOMODCACHE"); !strings.HasPrefix(v, tmp+"/") {
		t.Errorf("a GOMODCACHE left to an unjudged go env file is unknown and must go to the scratch, got %q", v)
	}
}

// goEnvLeakFixture builds, outside /tmp (bwrap's private mount would hide it
// on its own), a masked directory holding a go env file whose GOPATH must
// never reach a spawn, an ordinary one, and a go env file path in the
// workspace for a test to link at either.
func goEnvLeakFixture(t *testing.T) (secret, plain, envFile string, policyFor func(masked string) ResolvedPolicy) {
	t.Helper()
	base, err := os.MkdirTemp("/var/tmp", "evener-goenv-mask-")
	if err != nil {
		t.Skipf("no writable /var/tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	for name, body := range map[string]string{"secret/go.env": "GOPATH=/leaked/value\n", "plain/go.env": "GOPATH=/plain/value\n"} {
		p := filepath.Join(base, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	envFile = filepath.Join(base, "work", "go.env")
	if err := os.MkdirAll(filepath.Dir(envFile), 0o755); err != nil {
		t.Fatal(err)
	}
	policyFor = func(masked string) ResolvedPolicy {
		return ResolvedPolicy{Mode: ModeWorkspaceWrite, CacheStrategy: CacheSessionPrivate, MaskedPaths: []string{masked}, Spawned: AccessScope{Read: ReadAnywhere}, resolveHost: HostFacts{OS: "linux", Home: "/home/u", GoEnvFile: envFile}}
	}
	return filepath.Join(base, "secret", "go.env"), filepath.Join(base, "plain", "go.env"), envFile, policyFor
}

func flooredGoPath(policy ResolvedPolicy, envFile string) string {
	v, _ := envValue(ApplyEnvFloor([]string{"HOME=/home/u", "GOENV=" + envFile}, policy, "/tmp/evener-session-xyz"), "GOPATH")
	return v
}

// The floor reads the go env file outside the sandbox, so it never follows one
// the session retargeted at a masked file, by its final target or through a
// masked directory on the way: that file's settings would reach the spawn's
// environment. An ordinary file's GOPATH still follows the scratch.
func TestEnvFloorNeverReadsAGoEnvFileLinkedIntoAMask(t *testing.T) {
	secret, plain, envFile, policyFor := goEnvLeakFixture(t)
	secretDir := filepath.Dir(secret)
	if err := os.Symlink(plain, envFile); err != nil {
		t.Fatal(err)
	}
	if v := flooredGoPath(policyFor(secretDir), envFile); !strings.Contains(v, "/plain/value") {
		t.Fatalf("fixture: an ordinary go env file's GOPATH must follow the scratch, got %q", v)
	}
	if err := os.Remove(envFile); err != nil {
		t.Fatal(err)
	}
	// Through the masked directory and back out with "..".
	// filepath.Join would clean the ".." away, so the target is spelled raw.
	if err := os.Symlink(secretDir+"/../plain/go.env", envFile); err != nil {
		t.Fatal(err)
	}
	if v := flooredGoPath(policyFor(secretDir), envFile); strings.Contains(v, "/plain/value") {
		t.Errorf("a go env file reached through a masked directory must not be read, got GOPATH=%q", v)
	}
	if err := os.Remove(envFile); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, envFile); err != nil {
		t.Fatal(err)
	}
	for _, masked := range []string{secretDir, envFile} {
		if v := flooredGoPath(policyFor(masked), envFile); strings.Contains(v, "/leaked/value") {
			t.Errorf("mask %s: a masked go env file's settings must not reach the spawn, got GOPATH=%q", masked, v)
		}
	}
}

// The judgement holds for the file actually opened: a link swapped to a masked
// file between the check and the open (a sandboxed background process racing
// the floor) is caught on the opened descriptor.
func TestEnvFloorJudgesTheGoEnvFileItOpened(t *testing.T) {
	secret, plain, envFile, policyFor := goEnvLeakFixture(t)
	if err := os.Symlink(plain, envFile); err != nil {
		t.Fatal(err)
	}
	beforeGoEnvFileOpen = func(string) {
		_ = os.Remove(envFile)
		_ = os.Symlink(secret, envFile)
	}
	t.Cleanup(func() { beforeGoEnvFileOpen = func(string) {} })
	if v := flooredGoPath(policyFor(filepath.Dir(secret)), envFile); strings.Contains(v, "/leaked/value") {
		t.Errorf("a go env file swapped to a masked target before the open must not be read, got GOPATH=%q", v)
	}
}

// A relative GOENV resolves against the spawn's own directory, which the floor
// does not know, so the floor never reads it from its own: a file there with
// the same relative name must not lend the spawn its GOPATH.
func TestEnvFloorNeverReadsARelativeGoEnvFileFromItsOwnDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.env"), []byte("GOPATH=/unrelated/value\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	policy := ResolvedPolicy{Mode: ModeWorkspaceWrite, CacheStrategy: CacheSessionPrivate, Spawned: AccessScope{Read: ReadAnywhere}, resolveHost: HostFacts{OS: "linux", Home: "/home/u", GoEnvFile: "go.env"}}
	if v := flooredGoPath(policy, "go.env"); strings.Contains(v, "/unrelated/value") {
		t.Errorf("a relative go env file must not be read from the floor's own directory, got GOPATH=%q", v)
	}
}
