package mcp

import (
	"context"
	"slices"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"primeradiant.com/serf/agent/mcpconfig"
	"primeradiant.com/serf/agent/sandbox"
)

func testWrapper(t *testing.T) *sandbox.Wrapper {
	t.Helper()
	home := t.TempDir()
	cwd := sandbox.MaterializeWorkspace(t, sandbox.MainCheckout)
	net := true
	rp, err := sandbox.Resolve(
		sandbox.SandboxPolicy{Mode: sandbox.ModeWorkspaceWrite, Network: &net},
		sandbox.HostFacts{OS: "linux", Home: home, BwrapPath: "/usr/bin/bwrap", BwrapCapable: true},
		cwd)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	w, err := sandbox.NewWrapper(rp, "/usr/bin/bwrap", t.TempDir())
	if err != nil {
		t.Fatalf("NewWrapper: %v", err)
	}
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

// A confined stdio MCP server must not inherit serf's own ambient credentials
// (its provider API key et al.) from the parent environment, but a secret the
// server config sets explicitly is deliberate configuration and must survive.
func TestMCPStdioServerScrubsAmbientSecrets(t *testing.T) {
	t.Setenv("SERF_AMBIENT_API_KEY", "sk-ambient-leak")
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

	if strings.Contains(joined, "SERF_AMBIENT_API_KEY=") {
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
	home := t.TempDir()
	cwd := sandbox.MaterializeWorkspace(t, sandbox.MainCheckout)
	off := false
	rp, err := sandbox.Resolve(
		sandbox.SandboxPolicy{Mode: sandbox.ModeWorkspaceWrite, Network: &off},
		sandbox.HostFacts{OS: "linux", Home: home, BwrapPath: "/usr/bin/bwrap", BwrapCapable: true},
		cwd)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	w, err := sandbox.NewWrapper(rp, "/usr/bin/bwrap", t.TempDir())
	if err != nil {
		t.Fatalf("NewWrapper: %v", err)
	}
	return w
}

func TestRemoteMCPRefusedUnderNetOff(t *testing.T) {
	w := testWrapperNetOff(t)
	for _, typ := range []string{"sse", "http"} {
		dial := productionDial(mcpconfig.ServerConfig{Name: "remote", Type: typ, URL: "https://example.com/mcp"}, w)
		if _, err := dial(context.Background()); err == nil {
			t.Errorf("%s MCP server must be refused under net=off", typ)
		} else if !strings.Contains(err.Error(), "network egress is disabled") || strings.Contains(err.Error(), "--sandbox-net") {
			t.Errorf("%s refusal must be legible (flag-free) about disabled egress, got %v", typ, err)
		}
	}
	// A stdio server stays available under net=off (its network is severed by
	// --unshare-net; it is not tool-plane egress).
	dial := productionDial(stdioCfg(), w)
	if _, err := dial(context.Background()); err != nil {
		t.Errorf("stdio MCP server must remain available under net=off, got %v", err)
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
