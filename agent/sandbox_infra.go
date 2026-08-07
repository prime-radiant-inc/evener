package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/mcpconfig"
	"primeradiant.com/serf/agent/sandbox"
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
//   - Each configured plugin DIRECTORY (cfg.PluginDirs — already resolved by
//     internal/plugins.Manager.EnabledPluginDirs to the explicit --plugin-dir
//     values plus every installed+enabled registry install path). A plugin's
//     directory is the unit its hook scripts and bundled MCP servers live in, and
//     a hook's command is an arbitrary shell string that cannot be parsed for the
//     script path it will exec.
//   - For each configured stdio MCP server, the directory holding its program,
//     plus the directory of any absolute REGULAR-FILE argument — a script-
//     interpreter server (`node /srv/mcp/server.js`) needs its script and that
//     script's neighbours, not just the interpreter.
//
// A glob would be wrong twice over: it would grant plugin caches this session does
// not load, and it would miss a --plugin-dir or MCP server configured anywhere
// else.
//
// # Only inputs the model cannot write may feed this
//
// MCP servers come from mcpconfig.DiscoverTrusted, NOT Discover: the per-project
// layer (<git root>/.serf/mcp.json) sits inside the model's own write surface, and
// a sandbox grant derived from a model-writable file would break SandboxPolicy's
// core promise that nothing the model does mid-session can widen its own box. The
// concrete escalation: plant .serf/mcp.json naming a path, spawn a delegate with
// sandbox="restricted" (which re-derives these roots live), and read whatever you
// named. A project-declared MCP server still CONNECTS normally — it just cannot
// hand itself filesystem roots. Its program must already be reachable (inside the
// worktree, or under a system root).
//
// Every candidate is then filtered by infraGuard, which refuses shared,
// multi-tenant locations — see sandbox.RootGuard for the exact rule.
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
	guard := newInfraGuard(env)
	var roots []string
	add := func(root string) {
		if root != "" && !slices.Contains(roots, root) {
			roots = append(roots, root)
		}
	}

	for _, dir := range cfg.PluginDirs {
		add(guard.directoryRoot(dir))
	}

	// Errors are deliberately dropped: initMCP re-runs discovery and reports them.
	servers, _, _ := mcpconfig.DiscoverTrusted(cfg.MCPConfigFiles, cfg.MCPInline)
	for _, srv := range servers {
		if srv.Type != "" && srv.Type != "stdio" {
			continue // remote servers spawn no local program
		}
		add(guard.programRoot(srv.Command))
		for _, arg := range srv.Args {
			if filepath.IsAbs(arg) {
				add(guard.programRoot(arg))
			}
		}
	}
	return roots
}

// infraGuard decides whether a configured path may become a read/exec root. The
// safety rule itself lives in sandbox.RootGuard — the single implementation the
// developer-toolchain grant shares — and this type only resolves a configured
// plugin dir or MCP program to the candidate root to hand it.
type infraGuard struct {
	sandbox.RootGuard
}

// newInfraGuard anchors the guard on the session's shared, multi-tenant
// locations: the user's home, the session's own worktree, and the temp roots.
func newInfraGuard(env execenv.ExecutionEnvironment) infraGuard {
	anchors := []string{os.TempDir(), "/tmp"}
	if home, err := os.UserHomeDir(); err == nil {
		anchors = append(anchors, home)
	}
	if env != nil {
		// Unlike the home and temp anchors, this one is NOT pinned at session start:
		// it is whatever the environment reports at derivation time, and the delegate
		// path re-derives the roots per delegate. That is deliberate — a delegate
		// re-rooted into its own lane must be guarded against ITS lane's parent, not
		// the parent session's — and it is safe today because the value comes from
		// the execution environment, which no tool call can set. If a model-settable
		// working directory is ever introduced, this anchor stops being trustworthy
		// and must be pinned at session start instead.
		anchors = append(anchors, env.WorkingDirectory())
	}
	return infraGuard{RootGuard: sandbox.NewRootGuard(anchors...)}
}

// directoryRoot resolves a configured plugin directory to the root to grant. The
// path must exist and be a DIRECTORY; a plugin dir that is a file is malformed and
// contributes nothing.
func (g infraGuard) directoryRoot(path string) string {
	resolved, fi := statCanonical(path)
	if resolved == "" || !fi.IsDir() {
		return ""
	}
	return g.Permit(resolved)
}

// programRoot resolves a configured MCP command or argument to the root to grant:
// the directory CONTAINING it, because an executable generally needs its
// neighbours — dynamic libraries, a node_modules tree — to run.
//
// The path must be a REGULAR FILE. Requiring that is load-bearing, not tidiness:
// when any existing path qualified, a directory-valued argument (`args: ["/Users"]`)
// was granted verbatim, which is a grant of that whole tree rather than of some
// program's parent. The stated rationale — an interpreter resolving a script's
// neighbours — only ever justified the directory of a script FILE.
func (g infraGuard) programRoot(path string) string {
	resolved, fi := statCanonical(path)
	if resolved == "" || !fi.Mode().IsRegular() {
		return ""
	}
	return g.Permit(filepath.Dir(resolved))
}

// statCanonical resolves path to its absolute, SYMLINK-RESOLVED form and stats it,
// returning ("", nil) when it does not exist or cannot be resolved.
//
// Resolving symlinks before the safety checks is load-bearing: filepath.Abs does
// not follow links and os.Stat follows the link while leaving the textual path
// alone, so a link named in config (`plugin -> /Users/jesse`) would sail past a
// check on its own spelling while granting the target's contents. It also puts
// macOS paths in the canonical form the anchors use (/tmp -> /private/tmp,
// /var -> /private/var), so an ancestor check cannot miss on spelling alone.
func statCanonical(path string) (string, os.FileInfo) {
	p := strings.TrimSpace(path)
	if p == "" {
		return "", nil
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", nil
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", nil // missing, or a broken link: nothing to grant
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		return "", nil
	}
	return sandbox.StripDataVolumeAlias(resolved), fi
}
