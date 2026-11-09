package mcp

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"primeradiant.com/evener/agent/mcpconfig"
	"primeradiant.com/evener/agent/sandbox"
)

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
	// Env floor: the ssh-agent handle is dropped, ordinary config env survives.
	for _, kv := range ct.Command.Env {
		if strings.HasPrefix(kv, "SSH_AUTH_SOCK=") {
			t.Errorf("env floor must drop SSH_AUTH_SOCK from a sandboxed MCP server: %v", ct.Command.Env)
		}
	}
	if !slices.Contains(ct.Command.Env, "EXTRA=1") {
		t.Errorf("configured MCP env must survive the floor: %v", ct.Command.Env)
	}
	if ct.Command.ExtraFiles != nil {
		t.Errorf("fd hygiene: a sandboxed MCP server must inherit no extra fds")
	}
}

// A confined stdio MCP server must not inherit evener's own ambient credentials
// (its provider API key et al.) from the parent environment, but a secret the
// server config sets explicitly is deliberate configuration and must survive.
func TestMCPStdioServerScrubsAmbientSecrets(t *testing.T) {
	t.Setenv("EVENER_AMBIENT_API_KEY", "sk-ambient-leak")
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
	// ambient leak, so it must survive the scrub.
	if !slices.Contains(ct.Command.Env, "MCP_SERVER_TOKEN=configured-keep") {
		t.Errorf("a configured cfg.Env secret must survive the scrub: %v", ct.Command.Env)
	}
	if !slices.Contains(ct.Command.Env, "EXTRA=1") {
		t.Errorf("ordinary configured MCP env must survive: %v", ct.Command.Env)
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
