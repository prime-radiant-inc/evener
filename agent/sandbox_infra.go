package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/mcpconfig"
)

// SessionInfraRoots returns the session's hook and MCP-server paths: the read/
// exec roots a sandboxed session needs so its SessionStart (and every other) hook
// script and its stdio MCP-server programs can actually run. Ruled 2026-08-06:
// hooks and MCP servers are session INFRASTRUCTURE and must work in every sandbox
// mode. Before that, a hook script installed in the plugin cache — outside
// restricted mode's worktree-only read/exec surface — died with exit 126,
// "Operation not permitted".
//
// Every root is derived from the session's ACTUAL configuration, never from a
// home-directory glob such as ~/.claude/plugins/*:
//
//   - Each configured plugin directory (cfg.PluginDirs — already resolved by
//     internal/plugins.Manager.EnabledPluginDirs to the explicit --plugin-dir
//     values plus every installed+enabled registry install path). A plugin's
//     directory is the unit its hook scripts and bundled MCP servers live in, and
//     a hook's command is an arbitrary shell string that cannot be parsed for the
//     script path it will exec.
//   - For each configured stdio MCP server (the same global/project/CLI layers
//     mcpconfig.Discover feeds to the MCP manager), the directory holding its
//     program, plus the directory of any absolute path argument that exists — a
//     script-interpreter server (`node /srv/mcp/server.js`) needs its script and
//     that script's neighbours, not just the interpreter.
//
// A glob would be wrong twice over: it would grant plugin caches this session does
// not load, and it would miss a --plugin-dir or MCP server configured anywhere
// else.
//
// The result is a read/exec grant only — SandboxPolicy.InfraReadRoots reaches the
// spawned layer and never a write root — and the credential denylist still wins
// over it (sandbox.Resolve drops any root at or under a masked path, and both
// backends deny masked paths after every allow).
//
// It is fail-soft: a config layer that cannot be read contributes no roots rather
// than failing session start, because the same config is loaded again (with proper
// diagnostics) by initMCP and initPlugins.
//
// Callers invoke it only for a sandboxed session, so an unsandboxed run never
// pays for the config reads.
func SessionInfraRoots(cfg SessionConfig, env execenv.ExecutionEnvironment) []string {
	var roots []string
	add := func(p string) {
		if r := infraRoot(p); r != "" && !slices.Contains(roots, r) {
			roots = append(roots, r)
		}
	}

	for _, dir := range cfg.PluginDirs {
		add(dir)
	}

	// Errors are deliberately dropped: initMCP re-runs Discover and reports them.
	servers, _, _ := mcpconfig.Discover(env, cfg.MCPConfigFiles, cfg.MCPInline)
	for _, srv := range servers {
		if srv.Type != "" && srv.Type != "stdio" {
			continue // remote servers spawn no local program
		}
		add(srv.Command)
		for _, arg := range srv.Args {
			if filepath.IsAbs(arg) {
				add(arg)
			}
		}
	}
	return roots
}

// infraRoot turns one configured hook/MCP path into the absolute read/exec root
// to grant, or "" when there is nothing safe or useful to grant. A directory is
// granted as itself; anything else (a program or script file) is granted as its
// containing directory, because an executable generally needs its neighbours —
// dynamic libraries, a node_modules tree, a plugin's hooks/ subtree — to run.
//
// A path that does not exist yields "": there is nothing to grant, and a
// non-existent root would only add noise to the policy.
//
// A root that would resolve to the filesystem root or to the user's home
// directory is REFUSED. Restricted mode exists to hold spawned processes to the
// worktree; a single MCP server configured as `$HOME/run-server.sh` would
// otherwise hand the whole home directory to every shell command the model runs.
// Such a server stays broken under a sandbox — the same outcome as before this
// grant existed — rather than silently gutting the mode.
func infraRoot(path string) string {
	p := strings.TrimSpace(path)
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return ""
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return ""
	}
	root := abs
	if !fi.IsDir() {
		root = filepath.Dir(abs)
	}
	if root == filepath.Dir(root) || root == userHomeDirOrEmpty() {
		return ""
	}
	return root
}

// userHomeDirOrEmpty returns the user's home directory, or "" when it cannot be
// determined (in which case the home guard in infraRoot simply does not fire).
func userHomeDirOrEmpty() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Clean(home)
}
