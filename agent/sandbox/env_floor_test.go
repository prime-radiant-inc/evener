package sandbox

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/identifier"
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

// npm can reach a cache only through .npmrc. If the overlay cannot serve it
// (a project cache that does not exist yet, or one whose overlay probe fails),
// the per-spawn floor must redirect npm_config_cache into the session scratch;
// otherwise npm writes into read-only storage (#4542 review).
func TestEnvFloorRedirectsAnUnservedNpmrcCache(t *testing.T) {
	scratch := "/tmp/evener-session-xyz"
	home := overlayFixtureDir(t)
	if err := os.MkdirAll(filepath.Join(home, ".npm"), 0o755); err != nil {
		t.Fatal(err)
	}
	worktree := overlayFixtureDir(t)

	t.Run("missing project cache", func(t *testing.T) {
		missing := filepath.Join(overlayFixtureDir(t), "missing-npm")
		if err := os.WriteFile(filepath.Join(worktree, ".npmrc"), []byte("cache="+missing+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		policy := ResolvedPolicy{
			Mode:          ModeWorkspaceWrite,
			CacheStrategy: CacheOverlay,
			CacheRoots:    []string{filepath.Join(home, ".npm")},
			resolveHost:   HostFacts{Home: home},
			Git:           GitLayout{WorktreeRoot: worktree},
			SessionDir:    worktree,
		}
		assertNpmCacheRedirected(t, policy, []string{"HOME=" + home}, scratch)
	})

	t.Run("existing cache whose overlay probe fails", func(t *testing.T) {
		cacheDir := filepath.Join(overlayFixtureDir(t), "npm-cache")
		if err := os.MkdirAll(cacheDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(worktree, ".npmrc"), []byte("cache="+cacheDir+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		policy := ResolvedPolicy{
			Mode:          ModeWorkspaceWrite,
			CacheStrategy: CacheOverlay,
			CacheRoots:    []string{filepath.Join(home, ".npm"), cacheDir},
			resolveHost: HostFacts{
				Home:           home,
				OverlayLowerOK: func(root string) bool { return root != cacheDir },
			},
			Git:        GitLayout{WorktreeRoot: worktree},
			SessionDir: worktree,
		}
		assertNpmCacheRedirected(t, policy, []string{"HOME=" + home}, scratch)
	})
}

// npm follows a project .npmrc symlink, but the bounded project reader refuses
// to follow it. A refusal must not read as an absent file: the floor cannot
// know the cache npm will use, so it has to redirect npm's cache into the
// session scratch instead of judging a user/global cache npm will not read
// (#4542 review).
func TestEnvFloorRedirectsASymlinkedProjectNpmrcCache(t *testing.T) {
	scratch := "/tmp/evener-session-xyz"
	home := servedHome(t)
	worktree := overlayFixtureDir(t)
	external := filepath.Join(overlayFixtureDir(t), "external-npm")
	configDir := filepath.Join(worktree, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "npmrc"), []byte("cache="+external+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("config", "npmrc"), filepath.Join(worktree, ".npmrc")); err != nil {
		t.Fatal(err)
	}
	policy := ResolvedPolicy{
		Mode:          ModeWorkspaceWrite,
		CacheStrategy: CacheOverlay,
		CacheRoots:    []string{filepath.Join(home, ".npm")},
		resolveHost:   HostFacts{Home: home},
		Git:           GitLayout{WorktreeRoot: worktree},
		SessionDir:    worktree,
	}
	assertNpmCacheRedirected(t, policy, []string{"HOME=" + home}, scratch)
}

// A user .npmrc the bounded reader refuses (oversized) is not absent either:
// npm may read a cache from it, so the floor must redirect rather than judge
// the served default.
func TestEnvFloorRedirectsARefusedUserNpmrcCache(t *testing.T) {
	scratch := "/tmp/evener-session-xyz"
	home := servedHome(t)
	external := filepath.Join(overlayFixtureDir(t), "external-npm")
	body := "cache=" + external + "\n" + strings.Repeat("# pad\n", goEnvFileLimit/6+1)
	if err := os.WriteFile(filepath.Join(home, ".npmrc"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	worktree := overlayFixtureDir(t)
	policy := ResolvedPolicy{
		Mode:          ModeWorkspaceWrite,
		CacheStrategy: CacheOverlay,
		CacheRoots:    []string{filepath.Join(home, ".npm")},
		resolveHost:   HostFacts{Home: home},
		Git:           GitLayout{WorktreeRoot: worktree},
		SessionDir:    worktree,
	}
	assertNpmCacheRedirected(t, policy, []string{"HOME=" + home}, scratch)
}

// npm expands a leading ~ in a cache value from HOME. A ~-rooted project cache
// must be judged at that absolute location, not dropped as relative and
// replaced by the served default.
func TestEnvFloorRedirectsATildeProjectNpmrcCache(t *testing.T) {
	scratch := "/tmp/evener-session-xyz"
	home := servedHome(t)
	worktree := overlayFixtureDir(t)
	projectDir := filepath.Join(worktree, "pkg")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "package.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".npmrc"), []byte("cache=~/tilde-npm\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	policy := ResolvedPolicy{
		Mode:          ModeWorkspaceWrite,
		CacheStrategy: CacheOverlay,
		CacheRoots:    []string{filepath.Join(home, ".npm")},
		resolveHost:   HostFacts{Home: home},
		Git:           GitLayout{WorktreeRoot: worktree},
		SessionDir:    projectDir,
	}
	assertNpmCacheRedirected(t, policy, []string{"HOME=" + home}, scratch)
}

// npm resolves a relative npm_config_userconfig against its own confined
// working directory, which the floor does not resolve. The effective cache is
// therefore unknown, not absent: with ~/.npm served, judging the served default
// would leave npm writing to whatever cache that relative file names
// (#4542 review).
func TestEnvFloorRedirectsARelativeUserNpmrcCache(t *testing.T) {
	scratch := "/tmp/evener-session-xyz"
	home := servedHome(t)
	worktree := overlayFixtureDir(t)
	external := filepath.Join(overlayFixtureDir(t), "relative-npm")
	if err := os.WriteFile(filepath.Join(worktree, "ci.npmrc"), []byte("cache="+external+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	policy := ResolvedPolicy{
		Mode:          ModeWorkspaceWrite,
		CacheStrategy: CacheOverlay,
		CacheRoots:    []string{filepath.Join(home, ".npm")},
		resolveHost:   HostFacts{Home: home},
		Git:           GitLayout{WorktreeRoot: worktree},
		SessionDir:    worktree,
	}
	env := []string{"HOME=" + home, "npm_config_userconfig=./ci.npmrc"}
	assertNpmCacheRedirectedInDir(t, policy, env, worktree, scratch)
}

// A user .npmrc hidden behind bwrap's private /tmp is not a served cache: npm
// never reads the host file, so it falls back to its default ~/.npm, which this
// overlay does not serve. The floor must treat the hidden config as unknown and
// redirect to the session scratch.
func TestEnvFloorRedirectsAHiddenUserNpmrcCache(t *testing.T) {
	scratch := "/tmp/evener-session-xyz"
	home := servedHome(t)
	served := filepath.Join(overlayFixtureDir(t), "served-npm")
	if err := os.MkdirAll(served, 0o755); err != nil {
		t.Fatal(err)
	}
	configDir, err := os.MkdirTemp("/tmp", "evener-hidden-npmrc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(configDir) })
	config := filepath.Join(configDir, "ci.npmrc")
	if err := os.WriteFile(config, []byte("cache="+served+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	worktree := overlayFixtureDir(t)
	policy := ResolvedPolicy{
		Backend:       BackendBwrap,
		Mode:          ModeWorkspaceWrite,
		CacheStrategy: CacheOverlay,
		CacheRoots:    []string{served},
		resolveHost:   HostFacts{Home: home},
		Git:           GitLayout{WorktreeRoot: worktree},
		SessionDir:    worktree,
	}
	env := []string{"HOME=" + home, "npm_config_userconfig=" + config}
	assertNpmCacheRedirectedInDir(t, policy, env, worktree, scratch)
}

// A project .npmrc replaced by a DenylistAdd mask is not host truth: bubblewrap
// shows the spawn an empty file, so npm falls through to its default ~/.npm,
// which this overlay does not serve. The floor must redirect to the session
// scratch rather than trust the masked host value.
func TestEnvFloorRedirectsAMaskedProjectNpmrcCache(t *testing.T) {
	scratch := "/tmp/evener-session-xyz"
	home := servedHome(t)
	served := filepath.Join(overlayFixtureDir(t), "served-npm")
	if err := os.MkdirAll(served, 0o755); err != nil {
		t.Fatal(err)
	}
	worktree := overlayFixtureDir(t)
	projectConfig := filepath.Join(worktree, ".npmrc")
	if err := os.WriteFile(projectConfig, []byte("cache="+served+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	policy := ResolvedPolicy{
		Mode:          ModeWorkspaceWrite,
		CacheStrategy: CacheOverlay,
		CacheRoots:    []string{served},
		MaskedPaths:   []string{projectConfig},
		resolveHost:   HostFacts{Home: home},
		Git:           GitLayout{WorktreeRoot: worktree},
		SessionDir:    worktree,
	}
	assertNpmCacheRedirectedInDir(t, policy, []string{"HOME=" + home}, worktree, scratch)
}

// npm expands ${NAME} and a leading ~ in a configured user/global config
// filename before reading it. The floor must follow the same path: a literal
// ~/.npmrc naming a served cache is not unknown, and an absolute filename whose
// ${NAME} segment resolves to an unserved cache must still redirect.
func TestEnvFloorResolvesConfiguredNpmrcPaths(t *testing.T) {
	scratch := "/tmp/evener-session-xyz"
	home := servedHome(t)
	served := filepath.Join(home, ".npm")
	worktree := overlayFixtureDir(t)
	policy := ResolvedPolicy{
		Mode:          ModeWorkspaceWrite,
		CacheStrategy: CacheOverlay,
		CacheRoots:    []string{served},
		resolveHost:   HostFacts{Home: home},
		Git:           GitLayout{WorktreeRoot: worktree},
		SessionDir:    worktree,
	}

	t.Run("tilde filename naming a served cache", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(home, ".npmrc"), []byte("cache="+served+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		env := []string{"HOME=" + home, "npm_config_userconfig=~/.npmrc"}
		out := ApplyEnvFloorInDir(env, policy, scratch, worktree)
		if v, ok := envValue(out, npmConfigCacheEnv); ok {
			t.Fatalf("a served cache through an expanded filename was redirected: %q", v)
		}
	})

	t.Run("variable filename naming an unserved cache", func(t *testing.T) {
		base := overlayFixtureDir(t)
		external := filepath.Join(overlayFixtureDir(t), "var-npm")
		if err := os.MkdirAll(filepath.Join(base, "cfg"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(base, "cfg", "npmrc"), []byte("cache="+external+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		env := []string{
			"HOME=" + home,
			"NPM_CFG=cfg",
			"npm_config_userconfig=" + filepath.Join(base, "${NPM_CFG}", "npmrc"),
		}
		assertNpmCacheRedirectedInDir(t, policy, env, worktree, scratch)
	})
}

// A spawn with no HOME in its environment still expands ~ from the login
// user's home, the same home the floor already uses for npm's default cache. A
// ~ cache that is not served must be judged at that absolute location, not
// dropped as a relative value and replaced by the served default (#4542
// review).
func TestEnvFloorRedirectsATildeProjectNpmrcCacheWithNoHome(t *testing.T) {
	scratch := "/tmp/evener-session-xyz"
	login := servedHome(t)
	worktree := overlayFixtureDir(t)
	projectDir := filepath.Join(worktree, "pkg")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "package.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".npmrc"), []byte("cache=~/no-such-npm\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	policy := ResolvedPolicy{
		Mode:          ModeWorkspaceWrite,
		CacheStrategy: CacheOverlay,
		CacheRoots:    []string{filepath.Join(login, ".npm")},
		resolveHost:   HostFacts{Home: login, LoginHome: login},
		Git:           GitLayout{WorktreeRoot: worktree},
		SessionDir:    projectDir,
	}
	assertNpmCacheRedirectedInDir(t, policy, nil, projectDir, scratch)
}

// A spawn can name its own cache instead of a config file. With no HOME, npm
// still expands a leading ~ from the login user's home, so an unserved
// ~-rooted npm_config_cache must be redirected like any other unknown effective
// cache (#4542 review).
func TestEnvFloorRedirectsATildeNpmConfigCacheWithNoHome(t *testing.T) {
	scratch := "/tmp/evener-session-xyz"
	login := servedHome(t)
	worktree := overlayFixtureDir(t)
	policy := ResolvedPolicy{
		Mode:          ModeWorkspaceWrite,
		CacheStrategy: CacheOverlay,
		CacheRoots:    []string{filepath.Join(login, ".npm")},
		resolveHost:   HostFacts{Home: login, LoginHome: login},
		Git:           GitLayout{WorktreeRoot: worktree},
		SessionDir:    worktree,
	}
	assertNpmCacheRedirectedInDir(t, policy, []string{"npm_config_cache=~/no-such-npm"}, worktree, scratch)
}

// npm expands ${NAME} in an environment-provided cache value too. An unserved
// cache named that way must be judged at its expanded path, not left as a
// relative-looking value the floor cannot place (#4542 review).
func TestEnvFloorResolvesANpmConfigCacheValue(t *testing.T) {
	scratch := "/tmp/evener-session-xyz"
	home := servedHome(t)
	worktree := overlayFixtureDir(t)
	external := filepath.Join(overlayFixtureDir(t), "env-var-npm")
	policy := ResolvedPolicy{
		Mode:          ModeWorkspaceWrite,
		CacheStrategy: CacheOverlay,
		CacheRoots:    []string{filepath.Join(home, ".npm")},
		resolveHost:   HostFacts{Home: home},
		Git:           GitLayout{WorktreeRoot: worktree},
		SessionDir:    worktree,
	}
	env := []string{"HOME=" + home, "NPM_CACHE=" + external, "npm_config_cache=${NPM_CACHE}"}
	assertNpmCacheRedirectedInDir(t, policy, env, worktree, scratch)
}

// npm's last cache setting wins. An earlier unresolvable ~ value must not mark
// the whole file unknown when a later value names a served cache.
func TestEnvFloorJudgesTheLastNpmrcCacheValue(t *testing.T) {
	scratch := "/tmp/evener-session-xyz"
	home := servedHome(t)
	served := filepath.Join(home, ".npm")
	config := filepath.Join(overlayFixtureDir(t), "multi-cache-npmrc")
	if err := os.WriteFile(config, []byte("cache=~\ncache="+served+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	worktree := overlayFixtureDir(t)
	policy := ResolvedPolicy{
		Mode:          ModeWorkspaceWrite,
		CacheStrategy: CacheOverlay,
		CacheRoots:    []string{served},
		resolveHost:   HostFacts{},
		Git:           GitLayout{WorktreeRoot: worktree},
		SessionDir:    worktree,
	}
	out := ApplyEnvFloor([]string{"npm_config_userconfig=" + config}, policy, scratch)
	if v, ok := envValue(out, npmConfigCacheEnv); ok {
		t.Fatalf("the last cache value is served, got redirect %q", v)
	}
}

// A final relative cache value is npm's effective setting even when an earlier
// line names a served absolute cache. It resolves against npm's own working
// directory, not the overlay's, so the effective cache is unknown and must be
// redirected rather than judged served.
func TestEnvFloorRedirectsALastRelativeNpmrcCache(t *testing.T) {
	scratch := "/tmp/evener-session-xyz"
	home := servedHome(t)
	served := filepath.Join(home, ".npm")
	config := filepath.Join(overlayFixtureDir(t), "relative-last-npmrc")
	if err := os.WriteFile(config, []byte("cache="+served+"\ncache=relative/npm\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	worktree := overlayFixtureDir(t)
	policy := ResolvedPolicy{
		Mode:          ModeWorkspaceWrite,
		CacheStrategy: CacheOverlay,
		CacheRoots:    []string{served},
		resolveHost:   HostFacts{},
		Git:           GitLayout{WorktreeRoot: worktree},
		SessionDir:    worktree,
	}
	out := ApplyEnvFloor([]string{"npm_config_userconfig=" + config}, policy, scratch)
	if got, _ := envValue(out, npmConfigCacheEnv); got != filepath.Join(scratch, "npm") {
		t.Fatalf("npm_config_cache = %q, want the session scratch", got)
	}
}

// A global .npmrc the bounded reader refuses is unknown, not absent: npm may
// read a cache from it, so the floor must redirect rather than judge the
// served default.
func TestEnvFloorRedirectsARefusedGlobalNpmrcCache(t *testing.T) {
	scratch := "/tmp/evener-session-xyz"
	home := servedHome(t)
	external := filepath.Join(overlayFixtureDir(t), "external-npm")
	global := filepath.Join(overlayFixtureDir(t), "global-npmrc")
	body := "cache=" + external + "\n" + strings.Repeat("# pad\n", goEnvFileLimit/6+1)
	if err := os.WriteFile(global, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	worktree := overlayFixtureDir(t)
	policy := ResolvedPolicy{
		Mode:          ModeWorkspaceWrite,
		CacheStrategy: CacheOverlay,
		CacheRoots:    []string{filepath.Join(home, ".npm")},
		resolveHost:   HostFacts{Home: home},
		Git:           GitLayout{WorktreeRoot: worktree},
		SessionDir:    worktree,
	}
	assertNpmCacheRedirected(t, policy, []string{"HOME=" + home, "npm_config_globalconfig=" + global}, scratch)
}

func assertNpmCacheRedirected(t *testing.T, policy ResolvedPolicy, env []string, scratch string) {
	t.Helper()
	out := ApplyEnvFloor(env, policy, scratch)
	if got, _ := envValue(out, npmConfigCacheEnv); got != filepath.Join(scratch, "npm") {
		t.Fatalf("npm_config_cache = %q, want the session scratch", got)
	}
}

// npm expands a .npmrc value from the environment of the process that reads it,
// and a spawn can name its own userconfig. The per-spawn judge must use that
// environment, not the host probe's facts (#4542 review).
func TestEnvFloorUsesTheSpawnNpmrcEnvironment(t *testing.T) {
	scratch := "/tmp/evener-session-xyz"
	home := overlayFixtureDir(t)
	if err := os.MkdirAll(filepath.Join(home, ".npm"), 0o755); err != nil {
		t.Fatal(err)
	}
	worktree := overlayFixtureDir(t)
	policy := ResolvedPolicy{
		Mode:          ModeWorkspaceWrite,
		CacheStrategy: CacheOverlay,
		CacheRoots:    []string{filepath.Join(home, ".npm")},
		resolveHost:   HostFacts{Home: home},
		Git:           GitLayout{WorktreeRoot: worktree},
		SessionDir:    worktree,
	}

	t.Run("custom project variable", func(t *testing.T) {
		base := filepath.Join(overlayFixtureDir(t), "npm-base")
		if err := os.WriteFile(filepath.Join(worktree, ".npmrc"), []byte("cache=${NPM_CACHE_DIR}/npm\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		env := []string{"HOME=" + home, "NPM_CACHE_DIR=" + base}
		if dirs, _ := overlayCacheDirs(env, policy, worktree); dirs[npmConfigCacheEnv] != filepath.Join(base, "npm") {
			t.Fatalf("judged npm cache = %q, want the spawn-expanded project value", dirs[npmConfigCacheEnv])
		}
		assertNpmCacheRedirectedInDir(t, policy, env, worktree, scratch)
	})

	t.Run("spawn userconfig override", func(t *testing.T) {
		// A project .npmrc outranks the user file, so this case needs a
		// worktree with no project config of its own.
		clean := overlayFixtureDir(t)
		userPolicy := policy
		userPolicy.Git = GitLayout{WorktreeRoot: clean}
		userPolicy.SessionDir = clean
		configFile := filepath.Join(overlayFixtureDir(t), "spawn-user-npmrc")
		spawnCache := filepath.Join(overlayFixtureDir(t), "spawn-npm")
		if err := os.WriteFile(configFile, []byte("cache=${SPAWN_CACHE}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		env := []string{"HOME=" + home, "npm_config_userconfig=" + configFile, "SPAWN_CACHE=" + spawnCache}
		if dirs, _ := overlayCacheDirs(env, userPolicy, clean); dirs[npmConfigCacheEnv] != spawnCache {
			t.Fatalf("judged npm cache = %q, want the spawn userconfig value", dirs[npmConfigCacheEnv])
		}
		assertNpmCacheRedirectedInDir(t, userPolicy, env, clean, scratch)
	})
}

// A command's own working directory can be an independent nested package even
// when the session started at the repository root; the floor must locate that
// package's config (#4542 review).
func TestEnvFloorUsesTheSpawnWorkingDirectory(t *testing.T) {
	scratch := "/tmp/evener-session-xyz"
	home := overlayFixtureDir(t)
	worktree := t.TempDir()
	rootCache := filepath.Join(overlayFixtureDir(t), "root-npm")
	packageCache := filepath.Join(overlayFixtureDir(t), "package-npm")
	for _, dir := range []string{rootCache, packageCache} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(worktree, ".npmrc"), []byte("cache="+rootCache+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	packageDir := filepath.Join(worktree, "packages", "foo")
	if err := os.MkdirAll(packageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageDir, "package.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageDir, ".npmrc"), []byte("cache="+packageCache+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	policy := ResolvedPolicy{
		Mode:          ModeWorkspaceWrite,
		CacheStrategy: CacheOverlay,
		CacheRoots:    []string{rootCache},
		resolveHost:   HostFacts{Home: home},
		Git:           GitLayout{WorktreeRoot: worktree},
		SessionDir:    worktree,
	}
	env := []string{"HOME=" + home}
	if dirs, _ := overlayCacheDirs(env, policy, packageDir); dirs[npmConfigCacheEnv] != packageCache {
		t.Fatalf("judged npm cache = %q, want the nested package value", dirs[npmConfigCacheEnv])
	}
	assertNpmCacheRedirectedInDir(t, policy, env, packageDir, scratch)
}

func assertNpmCacheRedirectedInDir(t *testing.T, policy ResolvedPolicy, env []string, workingDir, scratch string) {
	t.Helper()
	out := ApplyEnvFloorInDir(env, policy, scratch, workingDir)
	if got, _ := envValue(out, npmConfigCacheEnv); got != filepath.Join(scratch, "npm") {
		t.Fatalf("npm_config_cache = %q, want the session scratch", got)
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

// One cache root on a filesystem the overlay cannot mount no longer fails the
// spawn: it is not overlaid, and a cache variable naming it goes to the session
// scratch — the session-private redirect, cold but working (#4220). The unset
// case matters most: it is the default under the spawn's own HOME that names the
// degraded root, so no environment setting points at it.
func TestEnvFloorRedirectsNpmAndCargoCachesUnderADegradedRoot(t *testing.T) {
	home := servedHome(t)
	tmp := "/tmp/evener-session-xyz"
	roots := []string{home + "/.cache", home + "/.npm", home + "/.cargo", home + "/go/pkg"}
	mk := func(refused ...string) ResolvedPolicy {
		bad := map[string]bool{}
		for _, r := range refused {
			bad[r] = true
		}
		return ResolvedPolicy{
			Mode: ModeWorkspaceWrite, CacheStrategy: CacheOverlay, CacheRoots: roots,
			Spawned: AccessScope{Read: ReadAnywhere},
			resolveHost: HostFacts{
				OS: "linux", Home: home, GoEnvFile: filepath.Join(home, ".config", "go", "env"),
				OverlayLowerOK: func(r string) bool { return !bad[r] },
			},
		}
	}
	out := ApplyEnvFloor([]string{"HOME=" + home}, mk(home+"/.npm", home+"/.cargo"), tmp)
	for name, want := range map[string]string{"npm_config_cache": tmp + "/npm", "CARGO_HOME": tmp + "/cargo"} {
		if v, _ := envValue(out, name); v != want {
			t.Errorf("a %s under a degraded root must go to the session scratch %q, got %q", name, want, v)
		}
	}

	// The uppercase spelling of npm's variable names the same setting: it points
	// at a degraded root the HOME default does not, and must not survive.
	out = ApplyEnvFloor([]string{"HOME=" + home, "NPM_CONFIG_CACHE=" + home + "/.cargo"}, mk(home+"/.cargo"), tmp)
	if v, _ := envValue(out, "npm_config_cache"); v != tmp+"/npm" {
		t.Errorf("an uppercase NPM_CONFIG_CACHE under a degraded root must go to the scratch %q, got %q", tmp+"/npm", v)
	}
	if _, ok := envValue(out, "NPM_CONFIG_CACHE"); ok {
		t.Errorf("the redirected uppercase spelling must not survive: %v", out)
	}

	// Control: with every root overlaid, the defaults are left alone.
	out = ApplyEnvFloor([]string{"HOME=" + home}, mk(), tmp)
	for _, name := range []string{"npm_config_cache", "CARGO_HOME"} {
		if v, ok := envValue(out, name); ok {
			t.Errorf("a %s the overlay serves must be left alone, got %q", name, v)
		}
	}
}

// A cache inside a directory the user made writable on purpose is left alone,
// even when its root is also one the overlay cannot mount: those writes land
// where the user granted, and redirecting them would override that (#4220).
func TestEnvFloorLeavesNpmCachesInAPersistentWriteRootAlone(t *testing.T) {
	home := servedHome(t)
	tmp := "/tmp/evener-session-xyz"
	dir := home + "/.npm"
	policy := ResolvedPolicy{
		Mode: ModeWorkspaceWrite, CacheStrategy: CacheOverlay, CacheRoots: []string{dir},
		Spawned:     AccessScope{Read: ReadAnywhere, WriteRoots: []string{dir}},
		resolveHost: HostFacts{OS: "linux", Home: home, OverlayLowerOK: func(string) bool { return false }},
	}
	out := ApplyEnvFloor([]string{"HOME=" + home}, policy, tmp)
	if v, ok := envValue(out, "npm_config_cache"); ok {
		t.Errorf("an npm cache the user made writable on purpose must be left alone, got %q", v)
	}
}

// A spawn with no HOME in its environment (a clean EnvPolicyNone) still gets
// npm's and cargo's default caches judged where the tools will really write:
// they fall back to the login user's home in the user database, NOT to this
// process's HOME, which a deployment may override (#4220).
func TestEnvFloorRedirectsNpmAndCargoDefaultsWithNoHome(t *testing.T) {
	overridden := servedHome(t)
	login := servedHome(t)
	tmp := "/tmp/evener-session-xyz"
	policy := ResolvedPolicy{
		Mode: ModeWorkspaceWrite, CacheStrategy: CacheOverlay,
		CacheRoots: []string{overridden + "/.npm", overridden + "/.cargo"},
		Spawned:    AccessScope{Read: ReadAnywhere},
		resolveHost: HostFacts{
			OS: "linux", Home: overridden, LoginHome: login,
			// The overridden home's roots are healthy; the login home's default
			// caches are outside every root and must be redirected.
			OverlayLowerOK: func(string) bool { return true },
		},
	}
	out := ApplyEnvFloor(nil, policy, tmp)
	for name, want := range map[string]string{"npm_config_cache": tmp + "/npm", "CARGO_HOME": tmp + "/cargo"} {
		if v, _ := envValue(out, name); v != want {
			t.Errorf("with no HOME, a %s under the login home's unserved default must go to the session scratch %q, got %q", name, want, v)
		}
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
	if !identifier.PathUnder(resolveExistingPrefix(dir), "/tmp") {
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
	if !identifier.PathUnder(resolveExistingPrefix(dir), "/tmp") {
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
