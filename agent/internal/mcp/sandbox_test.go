package mcp

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"primeradiant.com/evener/agent/mcpconfig"
	"primeradiant.com/evener/agent/sandbox"
)

// childEnvChannel decodes the private child-env fd the sandbox attaches to a
// confined launcher: the file is the launcher's only extra fd, and its payload
// is bwrap's NUL-separated --args records. It parses the records itself rather
// than reusing the production encoder, so the test proves the wire format.
func childEnvChannel(t *testing.T, cmd *exec.Cmd) (map[string]string, []string) {
	t.Helper()
	if len(cmd.ExtraFiles) != 1 {
		t.Fatalf("confined launcher must gain exactly one private env fd, got %d", len(cmd.ExtraFiles))
	}
	f := cmd.ExtraFiles[0]
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("seek private env fd: %v", err)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read private env fd: %v", err)
	}
	fields := strings.Split(string(data), "\x00")
	if n := len(fields); n > 0 && fields[n-1] == "" {
		fields = fields[:n-1]
	}
	set := map[string]string{}
	var unset []string
	for i := 0; i < len(fields); {
		switch fields[i] {
		case "--setenv":
			if i+2 >= len(fields) {
				t.Fatalf("truncated --setenv record in child-env payload: %q", fields)
			}
			set[fields[i+1]] = fields[i+2]
			i += 3
		case "--unsetenv":
			if i+1 >= len(fields) {
				t.Fatalf("truncated --unsetenv record in child-env payload: %q", fields)
			}
			unset = append(unset, fields[i+1])
			i += 2
		default:
			t.Fatalf("unexpected child-env payload record %q", fields[i])
		}
	}
	return set, unset
}

// TestMCPStdioServerSecretEnvNeverRidesLauncherArgv is issue #7009's proof.
//
// A configured secret in cfg.Env must reach the confined MCP server without
// appearing in the launcher's argv: host process-list diagnostics sample
// /proc/<pid>/cmdline while bwrap is alive, and bwrap --setenv VALUE (or an
// /usr/bin/env NAME=VALUE prefix) put the plaintext there. The assignments
// therefore ride a private fd the launcher reads and closes, while cmd.Env
// stays the scrubbed launcher environment.
func TestMCPStdioServerSecretEnvNeverRidesLauncherArgv(t *testing.T) {
	const secret = "mcp-fixture-token-9c1d"
	cfg := stdioCfg()
	cfg.Env = map[string]string{"MCP_SERVER_TOKEN": secret}

	dial := productionDial(cfg, testWrapper(t))
	tr, err := dial(context.Background())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	ct, ok := tr.(*mcpsdk.CommandTransport)
	if !ok || ct.Command == nil {
		t.Fatalf("expected a *CommandTransport with a command, got %T", tr)
	}
	t.Cleanup(func() {
		for _, f := range ct.Command.ExtraFiles {
			_ = f.Close()
		}
	})

	// 1. No plaintext on the launcher's command line.
	if joined := strings.Join(ct.Command.Args, "\x00"); strings.Contains(joined, secret) {
		t.Fatalf("configured secret is visible in the launcher argv: %v", ct.Command.Args)
	}
	// ...and none in the launcher environment, whose loader bwrap honours first.
	for _, kv := range ct.Command.Env {
		if strings.Contains(kv, secret) {
			t.Fatalf("configured secret is in the launcher environment: %q", kv)
		}
	}
	// 2. It still reaches the confined server over the private fd, which is the
	// only fd the launcher gains.
	set, unset := childEnvChannel(t, ct.Command)
	if got, ok := set["MCP_SERVER_TOKEN"]; !ok || got != secret {
		t.Fatalf("private child-env channel did not carry MCP_SERVER_TOKEN: set=%v unset=%v", set, unset)
	}
	// 3. The original command still follows the backend separator untouched.
	sep := slices.Index(ct.Command.Args, "--")
	if sep < 0 || !slices.Equal(ct.Command.Args[sep+1:], []string{"my-mcp-server", "--flag"}) {
		t.Fatalf("original MCP command must follow --: %v", ct.Command.Args)
	}
}

// bwrapUnsetenvNames returns the names the private child-env fd REMOVES inside
// the sandbox, via bwrap's own --unsetenv records.
func bwrapUnsetenvNames(t *testing.T, cmd *exec.Cmd) []string {
	t.Helper()
	_, unset := childEnvChannel(t, cmd)
	return unset
}

// confinedNpmFixture is a writable directory OUTSIDE /tmp, because bwrap gives
// every spawn a private /tmp: an npm config file reached through one there is
// hidden from the confined child, which would make a "config names an unserved
// cache" fixture actually test an unreadable config instead.
func confinedNpmFixture(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/var/tmp", "evener-mcp-delta-")
	if err != nil {
		t.Skipf("needs a writable directory outside /tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// TestMCPStdioServerDeliversTheFlooredConfigDelta is Medium #2's proof at the
// spawn site.
//
// A sandboxed stdio MCP server configured with npm_config_userconfig pointing at
// a config whose cache the session's overlays do not serve gets an
// npm_config_cache redirect to the session scratch — but the floor DERIVES that
// assignment from the environment it judges, so cfg.Env never names it. A child
// environment built by copying cfg.Env's own keys therefore dropped it, and the
// confined server (an npx-based server is exactly this shape) wrote its cache
// into read-only storage.
//
// The delta is by NAME over the floored launcher environment and the floored
// configured one, so a derived assignment rides it like a surviving cfg.Env
// entry, and a name the floor REMOVES from the configured environment (an
// worktree-external KUBECONFIG here) is emitted as --unsetenv rather than
// inherited from the launcher. cfg.Env itself still never joins cmd.Env.
func TestMCPStdioServerDeliversTheFlooredConfigDelta(t *testing.T) {
	worktree := sandbox.MaterializeWorkspace(t, sandbox.MainCheckout)
	sessionTmp := t.TempDir()
	fixture := confinedNpmFixture(t)
	// HOME lives beside the fixture, outside /tmp: a home under /tmp would have
	// its ~/.npm cache root hidden by bwrap's private mount, so the launcher's
	// own floor would already redirect npm_config_cache and the assignment under
	// test would not be the DERIVED one.
	home := filepath.Join(fixture, "home")
	if err := os.MkdirAll(filepath.Join(home, ".npm"), 0o755); err != nil {
		t.Fatal(err)
	}
	unservedCache := filepath.Join(fixture, "custom-npm-cache")
	if err := os.MkdirAll(unservedCache, 0o755); err != nil {
		t.Fatal(err)
	}
	userConfig := filepath.Join(fixture, "spawn-user-npmrc")
	if err := os.WriteFile(userConfig, []byte("cache="+unservedCache+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	internalKubeconfig := filepath.Join(worktree, "kubeconfig")
	externalKubeconfig := filepath.Join(fixture, "external-kubeconfig")

	rp, w := resolveWrapper(t, sandbox.HostFacts{
		OS: "linux", Home: home, BwrapPath: "/usr/bin/bwrap", BwrapCapable: true,
		OverlaySupported: true,
	}, worktree, sessionTmp, true)
	if rp.CacheStrategy != sandbox.CacheOverlay {
		t.Fatalf("test premise: cache strategy = %v, want overlay", rp.CacheStrategy)
	}
	// The premise that makes the redirect DERIVED: the launcher's own
	// environment resolves npm's cache to the served ~/.npm, so nothing asks the
	// floor to redirect it until cfg.Env's userconfig moves npm's effective
	// cache to the unserved directory.
	launcherFloor := sandbox.ApplyEnvFloorInDir([]string{"HOME=" + home}, rp, sessionTmp, worktree)
	for _, kv := range launcherFloor {
		if strings.HasPrefix(kv, "npm_config_cache=") {
			t.Fatalf("test premise: the launcher's floor already redirects npm_config_cache, so the assignment under test is not derived from cfg.Env: %v", launcherFloor)
		}
	}

	cfg := stdioCfg()
	cfg.Env = map[string]string{
		"npm_config_userconfig": userConfig,
		"KUBECONFIG":            externalKubeconfig,
	}
	dial := productionDialWithEnv(cfg, w, func() []string {
		return []string{"HOME=" + home, "KUBECONFIG=" + internalKubeconfig}
	})
	tr, err := dial(context.Background())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	ct, ok := tr.(*mcpsdk.CommandTransport)
	if !ok || ct.Command == nil {
		t.Fatalf("expected a *CommandTransport with a command, got %T", tr)
	}

	// The defect: the derived redirect must reach the CONFINED server, because
	// the launcher's own value points elsewhere and the child inherits it unless
	// the argv says otherwise.
	if got, ok := bwrapSetenvValue(t, ct.Command, "npm_config_cache"); !ok || got != filepath.Join(sessionTmp, "npm") {
		t.Errorf("npm_config_cache reached the confined server as %q (present=%v), want the derived session-scratch redirect %q", got, ok, filepath.Join(sessionTmp, "npm"))
	}
	// A surviving cfg.Env entry still rides the same channel.
	if got, ok := bwrapSetenvValue(t, ct.Command, "npm_config_userconfig"); !ok || got != userConfig {
		t.Errorf("child-env npm_config_userconfig = %q (present=%v), want the configured path %q", got, ok, userConfig)
	}
	// The floor refuses the external KUBECONFIG the config asked for, so the
	// child must not inherit the launcher's in-worktree one either: the floored
	// configured environment has none.
	if got := bwrapUnsetenvNames(t, ct.Command); !slices.Contains(got, "KUBECONFIG") {
		t.Errorf("child-env unsets = %v, want KUBECONFIG: the floor removed it from the configured environment, so the child must not inherit the launcher's value", got)
	}
	// The whole point of the child-env channel: cfg.Env never rides cmd.Env. The
	// launcher keeps the PARENT's KUBECONFIG (the parent environment is its own),
	// but not the configured one, and not the configured userconfig at all.
	for _, kv := range ct.Command.Env {
		name, value, _ := strings.Cut(kv, "=")
		if name == "npm_config_userconfig" || (name == "KUBECONFIG" && value == externalKubeconfig) {
			t.Errorf("cfg.Env entry %q is in the sandbox LAUNCHER's environment: %v", kv, ct.Command.Env)
		}
	}
}

// resolveWrapper resolves a workspace-write policy at cwd and wraps it, so a
// test can inspect both the policy the env floor judges and the command the
// wrapper confines.
func resolveWrapper(t *testing.T, facts sandbox.HostFacts, cwd, sessionTmp string, netOn bool) (sandbox.ResolvedPolicy, *sandbox.Wrapper) {
	t.Helper()
	net := netOn
	rp, err := sandbox.Resolve(
		sandbox.SandboxPolicy{Mode: sandbox.ModeWorkspaceWrite, Network: &net},
		facts,
		cwd)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	w, err := sandbox.NewWrapper(rp, facts.BwrapPath, sessionTmp)
	if err != nil {
		t.Fatalf("NewWrapper: %v", err)
	}
	return rp, w
}

func testWrapper(t *testing.T) *sandbox.Wrapper {
	t.Helper()
	cwd := sandbox.MaterializeWorkspace(t, sandbox.MainCheckout)
	_, w := resolveWrapper(t, sandbox.HostFacts{
		OS: "linux", Home: t.TempDir(), BwrapPath: "/usr/bin/bwrap", BwrapCapable: true,
	}, cwd, t.TempDir(), true)
	return w
}

func stdioCfg() mcpconfig.ServerConfig {
	return mcpconfig.ServerConfig{
		Name:    "s",
		Type:    "stdio",
		Command: "my-mcp-server",
		Args:    []string{"--flag"},
		Env:     map[string]string{"SSH_AUTH_SOCK": "/run/agent.sock", "EXTRA": "1"},
	}
}

func TestMCPStdioServerConfined(t *testing.T) {
	dial := productionDial(stdioCfg(), testWrapper(t))
	tr, err := dial(context.Background())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	ct, ok := tr.(*mcpsdk.CommandTransport)
	if !ok || ct.Command == nil {
		t.Fatalf("expected a *CommandTransport with a command, got %T", tr)
	}

	if ct.Command.Args[0] != "/usr/bin/bwrap" || ct.Command.Path != "/usr/bin/bwrap" {
		t.Errorf("stdio MCP server must be spawned under bwrap: args[0]=%q path=%q", ct.Command.Args[0], ct.Command.Path)
	}
	if !slices.Contains(ct.Command.Args, "--unshare-pid") {
		t.Errorf("wrapped MCP argv missing confinement flags: %v", ct.Command.Args)
	}
	// The original command survives after the -- separator.
	sep := slices.Index(ct.Command.Args, "--")
	if sep < 0 || !slices.Equal(ct.Command.Args[sep+1:], []string{"my-mcp-server", "--flag"}) {
		t.Errorf("original MCP command must follow --: %v", ct.Command.Args)
	}
	// Env floor: the ssh-agent handle is dropped from BOTH the launcher and the
	// confined child, and ordinary config env still reaches the child — through
	// the private child-env fd, never through the launcher's own environment.
	for _, kv := range ct.Command.Env {
		if strings.HasPrefix(kv, "SSH_AUTH_SOCK=") {
			t.Errorf("env floor must drop SSH_AUTH_SOCK from a sandboxed MCP server: %v", ct.Command.Env)
		}
	}
	if _, ok := bwrapSetenvValue(t, ct.Command, "SSH_AUTH_SOCK"); ok {
		t.Errorf("env floor must drop SSH_AUTH_SOCK from the confined server too")
	}
	if got, ok := bwrapSetenvValue(t, ct.Command, "EXTRA"); !ok || got != "1" {
		t.Errorf("configured MCP env must survive the floor and reach the confined server")
	}
}

// bwrapSetenvValue returns the value the private child-env fd sets name to
// INSIDE the sandbox, via bwrap's own --setenv records.
func bwrapSetenvValue(t *testing.T, cmd *exec.Cmd, name string) (string, bool) {
	t.Helper()
	set, _ := childEnvChannel(t, cmd)
	value, ok := set[name]
	return value, ok
}

// TestMCPStdioServerAppliesConfigEnvInsideTheSandbox is High finding #1's proof.
//
// A stdio server's cfg.Env is repository- or user-controlled configuration. The
// launcher's environment is bwrap's OWN dynamic-loader environment, so a
// cfg.Env LD_PRELOAD (or LD_LIBRARY_PATH, DYLD_INSERT_LIBRARIES, ...) would run
// repository-controlled code before the namespaces and the filesystem
// confinement exist. cmd.Env must therefore carry the scrubbed base plus the
// sandbox floor and NEVER cfg.Env, while cfg.Env still reaches the confined
// server itself — bwrap's --setenv applies a variable to the child only.
//
// Both confinement decisions are covered: the trusted-infrastructure carve-out
// (DenyNetworkException false) and the ordinary confinement an unauthorized
// project server gets (true). The env rule is the same either way.
func TestMCPStdioServerAppliesConfigEnvInsideTheSandbox(t *testing.T) {
	for _, tc := range []struct {
		name string
		deny bool
	}{
		{name: "trusted infra", deny: false},
		{name: "deny network exception", deny: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := stdioCfg()
			cfg.DenyNetworkException = tc.deny
			cfg.Env = map[string]string{"LD_PRELOAD": "/tmp/evil.so", "MY_SERVER_FLAG": "1"}

			dial := productionDial(cfg, testWrapper(t))
			tr, err := dial(context.Background())
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			ct, ok := tr.(*mcpsdk.CommandTransport)
			if !ok || ct.Command == nil {
				t.Fatalf("expected a *CommandTransport with a command, got %T", tr)
			}

			// 1. The launcher's own environment never holds repository-controlled
			// config: bwrap inherits cmd.Env, and its loader reads it before any
			// confinement exists.
			for _, kv := range ct.Command.Env {
				name, _, ok := strings.Cut(kv, "=")
				if ok && (name == "LD_PRELOAD" || name == "MY_SERVER_FLAG") {
					t.Errorf("cfg.Env entry %q is in the sandbox LAUNCHER's environment, where the loader honours it before confinement: %v", kv, ct.Command.Env)
				}
			}
			// 2. The confined server still receives it: the private child-env fd
			// applies to the child, after the namespaces and mounts are in place.
			for name, want := range cfg.Env {
				got, ok := bwrapSetenvValue(t, ct.Command, name)
				if !ok {
					t.Errorf("cfg.Env entry %s=%s is not delivered to the confined server over the private fd", name, want)
					continue
				}
				if got != want {
					t.Errorf("child-env %s = %q, want %q", name, got, want)
				}
			}
		})
	}
}

// A confined stdio MCP server must not inherit evener's own ambient credentials
// (its provider API key et al.) from the parent environment, but a secret the
// server config sets explicitly is deliberate configuration and must survive.
func TestMCPStdioServerScrubsAmbientSecrets(t *testing.T) {
	t.Setenv("EVENER_AMBIENT_API_KEY", "ambient-fixture-key-leak")
	cfg := mcpconfig.ServerConfig{
		Name:    "s",
		Type:    "stdio",
		Command: "my-mcp-server",
		Env:     map[string]string{"MCP_SERVER_TOKEN": "configured-keep", "EXTRA": "1"},
	}
	dial := productionDial(cfg, testWrapper(t))
	tr, err := dial(context.Background())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	ct := tr.(*mcpsdk.CommandTransport)
	joined := strings.Join(ct.Command.Env, "\n")

	if strings.Contains(joined, "EVENER_AMBIENT_API_KEY=") {
		t.Errorf("an ambient API key must be scrubbed from a confined MCP server: %v", ct.Command.Env)
	}
	// A cfg.Env var whose NAME looks secret is explicit configuration, not an
	// ambient leak, so it must survive the scrub and reach the confined server —
	// through the child-env channel, which is the only place cfg.Env may appear.
	if got, ok := bwrapSetenvValue(t, ct.Command, "MCP_SERVER_TOKEN"); !ok || got != "configured-keep" {
		t.Errorf("a configured cfg.Env secret must reach the confined server")
	}
	if got, ok := bwrapSetenvValue(t, ct.Command, "EXTRA"); !ok || got != "1" {
		t.Errorf("ordinary configured MCP env must reach the confined server")
	}
}

func testWrapperNetOff(t *testing.T) *sandbox.Wrapper {
	t.Helper()
	cwd := sandbox.MaterializeWorkspace(t, sandbox.MainCheckout)
	_, w := resolveWrapper(t, sandbox.HostFacts{
		OS: "linux", Home: t.TempDir(), BwrapPath: "/usr/bin/bwrap", BwrapCapable: true,
	}, cwd, t.TempDir(), false)
	return w
}

// TestRemoteMCPAllowedUnderNetOff pins kata 83pm: MCP servers (stdio and
// remote alike) are trusted infrastructure, launched only from config layers
// the model cannot write, so net=off does not sever them — unlike
// model-directed egress (web_fetch/web_search), which remains disabled.
func TestRemoteMCPAllowedUnderNetOff(t *testing.T) {
	w := testWrapperNetOff(t)
	for _, typ := range []string{"sse", "http"} {
		dial := productionDial(mcpconfig.ServerConfig{Name: "remote", Type: typ, URL: "https://example.com/mcp"}, w)
		if _, err := dial(context.Background()); err != nil {
			t.Errorf("%s MCP server must remain available under net=off (trusted infrastructure), got %v", typ, err)
		}
	}
	// A stdio server also stays available under net=off.
	dial := productionDial(stdioCfg(), w)
	if _, err := dial(context.Background()); err != nil {
		t.Errorf("stdio MCP server must remain available under net=off, got %v", err)
	}
}

// TestMCPStdioServerKeepsNetworkUnderNetOff pins the other half of kata 83pm:
// a stdio MCP server's confined exec.Cmd argv must NOT carry --unshare-net
// under a net=off session policy, even though it keeps every other kernel
// confinement (the bwrap prefix, --unshare-pid, the env floor, fd hygiene —
// all already covered by TestMCPStdioServerConfined).
func TestMCPStdioServerKeepsNetworkUnderNetOff(t *testing.T) {
	w := testWrapperNetOff(t)
	dial := productionDial(stdioCfg(), w)
	tr, err := dial(context.Background())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	ct, ok := tr.(*mcpsdk.CommandTransport)
	if !ok || ct.Command == nil {
		t.Fatalf("expected a *CommandTransport with a command, got %T", tr)
	}
	if slices.Contains(ct.Command.Args, "--unshare-net") {
		t.Errorf("stdio MCP server must keep network under net=off (trusted infrastructure): %v", ct.Command.Args)
	}
	if !slices.Contains(ct.Command.Args, "--unshare-pid") {
		t.Errorf("stdio MCP server must still be kernel-confined under net=off: %v", ct.Command.Args)
	}
}

func TestMCPStdioServerUnsandboxedUnchanged(t *testing.T) {
	dial := productionDial(stdioCfg(), nil)
	tr, err := dial(context.Background())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	ct := tr.(*mcpsdk.CommandTransport)
	if ct.Command.Args[0] != "my-mcp-server" {
		t.Errorf("an unsandboxed MCP server must be spawned directly, got %v", ct.Command.Args)
	}
}

// TestMCPStdioServerDeniesNetworkExceptionUnderNetOff is Finding 1's proof at
// the spawn site.
//
// ServerConfig.DenyNetworkException marks a stdio server that discovery admitted
// without launch authorization under a network-disabled sandbox. Such a server
// must be confined with w.Confine — the same confinement a model-authored spawn
// gets — so net=off severs its egress with --unshare-net. Every other stdio
// server keeps the trusted-infrastructure carve-out
// (TestMCPStdioServerKeepsNetworkUnderNetOff pins that half).
func TestMCPStdioServerDeniesNetworkExceptionUnderNetOff(t *testing.T) {
	w := testWrapperNetOff(t)
	cfg := stdioCfg()
	cfg.DenyNetworkException = true

	dial := productionDial(cfg, w)
	tr, err := dial(context.Background())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	ct, ok := tr.(*mcpsdk.CommandTransport)
	if !ok || ct.Command == nil {
		t.Fatalf("expected a *CommandTransport with a command, got %T", tr)
	}
	// The denial is exactly "confine it like any other model-influenced spawn":
	// net=off must reach the argv.
	if !slices.Contains(ct.Command.Args, "--unshare-net") {
		t.Errorf("an unauthorized stdio project server under net=off must have its network severed (--unshare-net): %v", ct.Command.Args)
	}
	// It is still an ordinary sandboxed spawn: every other confinement applies.
	if ct.Command.Args[0] != "/usr/bin/bwrap" || ct.Command.Path != "/usr/bin/bwrap" {
		t.Errorf("a denied stdio server must still be spawned under bwrap: args[0]=%q path=%q", ct.Command.Args[0], ct.Command.Path)
	}
	if !slices.Contains(ct.Command.Args, "--unshare-pid") {
		t.Errorf("a denied stdio server must still be kernel-confined: %v", ct.Command.Args)
	}
	// fd hygiene: the only extra fd is the private child-env channel, which the
	// launcher consumes at startup.
	if _, ok := bwrapSetenvValue(t, ct.Command, "EXTRA"); !ok {
		t.Errorf("the confined server must still receive its configured env over the private fd")
	}

	// The same config under a network-ENABLED sandbox is unaffected: the field
	// only withholds an exception, and there is nothing to withhold when egress
	// is allowed anyway.
	tr, err = productionDial(cfg, testWrapper(t))(context.Background())
	if err != nil {
		t.Fatalf("dial under net=on: %v", err)
	}
	ct = tr.(*mcpsdk.CommandTransport)
	if slices.Contains(ct.Command.Args, "--unshare-net") {
		t.Errorf("net=on must not emit --unshare-net at all: %v", ct.Command.Args)
	}
}

// A session started in a nested package launches its stdio MCP server at the
// Git worktree root, so the env floor must judge npm's project config there. A
// root .npmrc naming an unserved external cache has to fall back to the session
// scratch, or an npx-based server writes into read-only storage and can fail to
// start (#4542 review).
func TestMCPStdioServerFloorUsesTheConfinedWorktreeDir(t *testing.T) {
	worktree := sandbox.MaterializeWorkspace(t, sandbox.MainCheckout)
	packageDir := filepath.Join(worktree, "packages", "foo")
	packageCache := filepath.Join(t.TempDir(), "package-npm")
	rootCache := filepath.Join(t.TempDir(), "root-npm")
	for _, dir := range []string{packageDir, packageCache, rootCache} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(filepath.Join(packageDir, "package.json"), "{}\n")
	writeFile(filepath.Join(packageDir, ".npmrc"), "cache="+packageCache+"\n")
	writeFile(filepath.Join(worktree, ".npmrc"), "cache="+rootCache+"\n")

	home := t.TempDir()
	sessionTmp := t.TempDir()
	rp, w := resolveWrapper(t, sandbox.HostFacts{
		OS: "linux", Home: home, BwrapPath: "/usr/bin/bwrap", BwrapCapable: true,
		OverlaySupported: true,
	}, packageDir, sessionTmp, true)
	if rp.CacheStrategy != sandbox.CacheOverlay {
		t.Fatalf("test premise: cache strategy = %v, want overlay", rp.CacheStrategy)
	}
	if rp.SessionDir != packageDir || rp.Git.WorktreeRoot != worktree {
		t.Fatalf("test premise: session dir = %q, worktree root = %q", rp.SessionDir, rp.Git.WorktreeRoot)
	}
	if !slices.Contains(rp.CacheRoots, packageCache) || slices.Contains(rp.CacheRoots, rootCache) {
		t.Fatalf("test premise: cache roots = %v, want the package cache only", rp.CacheRoots)
	}

	dial := productionDialWithEnv(stdioCfg(), w, func() []string { return []string{"HOME=" + home} })
	tr, err := dial(context.Background())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	ct, ok := tr.(*mcpsdk.CommandTransport)
	if !ok || ct.Command == nil {
		t.Fatalf("expected a *CommandTransport with a command, got %T", tr)
	}
	workdir := ""
	for i := 0; i+1 < len(ct.Command.Args); i++ {
		if ct.Command.Args[i] == "--chdir" {
			workdir = ct.Command.Args[i+1]
			break
		}
	}
	if workdir != worktree {
		t.Fatalf("MCP server workdir = %q, want the worktree root %q", workdir, worktree)
	}
	got := ""
	for _, kv := range ct.Command.Env {
		if name, val, found := strings.Cut(kv, "="); found && name == "npm_config_cache" {
			got = val
		}
	}
	if want := filepath.Join(sessionTmp, "npm"); got != want {
		t.Fatalf("npm_config_cache = %q, want the session scratch %q", got, want)
	}
}

// childEnvCommand builds a stdio transport whose command carries a private
// child-env fd, the way confinement leaves it. The command never speaks MCP, so
// Connect fails — the fd must be released either way, and a failure is the
// cheapest deterministic path to a started-then-failed launcher.
func childEnvCommand(t *testing.T) (*mcpsdk.CommandTransport, *os.File) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "child-env-*")
	if err != nil {
		t.Fatalf("create private child-env fd: %v", err)
	}
	if _, err := f.WriteString("+MCP_TOKEN=fixture-token\n"); err != nil {
		t.Fatalf("write private child-env fd: %v", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("rewind private child-env fd: %v", err)
	}
	cmd := exec.Command("/bin/sh", "-c", "exit 0") //nolint:noctx // test-only cmd, starts and exits without speaking MCP
	cmd.ExtraFiles = []*os.File{f}
	return &mcpsdk.CommandTransport{Command: cmd}, f
}

// assertChildEnvFdReleased fails when the parent still holds the private
// child-env fd after a connect. Close on an already-closed *os.File returns an
// error, which is how "released" is observed without reaching into fd tables.
func assertChildEnvFdReleased(t *testing.T, ct *mcpsdk.CommandTransport, f *os.File) {
	t.Helper()
	if ct.Command.ExtraFiles != nil {
		t.Errorf("connect left ExtraFiles populated: %v", ct.Command.ExtraFiles)
	}
	if err := f.Close(); err == nil {
		t.Error("connect left the parent's copy of the private child-env fd open")
	}
}

// TestInitialConnectReleasesParentChildEnvFd covers the first connect: the
// launcher owns its own copy of the fd from the exec on, so the parent's copy
// must not stay open for the session's lifetime.
func TestInitialConnectReleasesParentChildEnvFd(t *testing.T) {
	ctx := context.Background()
	ct, f := childEnvCommand(t)
	mgr, outcomes := NewManager(ctx, []mcpconfig.ServerConfig{{Name: "s", Type: "stdio"}},
		[]func(context.Context) (mcpsdk.Transport, error){staticDial(ct)})
	defer mgr.Close()
	if len(outcomes) != 1 || outcomes[0].Stage != "connect" {
		t.Fatalf("NewManager: %+v", outcomes)
	}
	assertChildEnvFdReleased(t, ct, f)
}

// TestReconnectReleasesParentChildEnvFd covers the lazy redial: reconnect
// builds a fresh confined command with its own private child-env fd, and its
// parent copy was previously never closed — one leaked fd per redial.
func TestReconnectReleasesParentChildEnvFd(t *testing.T) {
	ctx := context.Background()
	_, live := newReconnectTestServer(t, "s", "probe", "reply")
	mgr, outcomes := NewManager(ctx, []mcpconfig.ServerConfig{{Name: "s", Type: "stdio"}},
		[]func(context.Context) (mcpsdk.Transport, error){staticDial(live)})
	defer mgr.Close()
	if len(outcomes) != 0 {
		t.Fatalf("NewManager: %+v", outcomes)
	}

	ct, f := childEnvCommand(t)
	c := &mgr.conns[0]
	c.mu.Lock()
	c.dial = staticDial(ct)
	c.mu.Unlock()
	if _, ok := c.reconnect(ctx); ok {
		t.Fatal("a command that never speaks MCP must not connect")
	}
	assertChildEnvFdReleased(t, ct, f)
}
