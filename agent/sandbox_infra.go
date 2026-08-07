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
// multi-tenant locations — see its doc for the exact rule.
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

// infraGuard decides whether a configured path may become a read/exec root. It
// holds the session's shared, multi-tenant anchors in canonical (symlink-resolved)
// form so the checks below compare like with like.
type infraGuard struct {
	// shared are locations that hold MANY tenants' data: the user's home (every
	// other project, every credential the denylist does not name, every other
	// session's transcripts), the session's own worktree (whose PARENT typically
	// holds every other lane), and the temp roots (every other session's scratch).
	// A root at or ABOVE any of these is refused.
	shared []string
}

func newInfraGuard(env execenv.ExecutionEnvironment) infraGuard {
	var shared []string
	addShared := func(p string) {
		if c := canonicalPath(p); c != "" {
			shared = append(shared, c)
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		addShared(home)
	}
	if env != nil {
		addShared(env.WorkingDirectory())
	}
	addShared(os.TempDir())
	addShared("/tmp")
	return infraGuard{shared: shared}
}

// directoryRoot resolves a configured plugin directory to the root to grant. The
// path must exist and be a DIRECTORY; a plugin dir that is a file is malformed and
// contributes nothing.
func (g infraGuard) directoryRoot(path string) string {
	resolved, fi := statCanonical(path)
	if resolved == "" || !fi.IsDir() {
		return ""
	}
	return g.permit(resolved)
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
	return g.permit(filepath.Dir(resolved))
}

// permit returns root if it is safe to grant, else "".
//
// A root is refused when it is at or ABOVE any shared anchor (home, the worktree,
// a temp root), or when it has fewer than two path components. Both shapes hand a
// spawned process a whole multi-tenant tree: "/Users" and "/home" are ANCESTORS of
// a home directory rather than equal to one, so an equality check misses them, and
// sandbox.Resolve's filterMasked cannot help either — it drops roots at or BENEATH
// a masked path, and an ancestor is above them. The result would be read/exec of
// every home on the machine (and every other worktree lane) minus only the nine
// named credential directories.
//
// A misconfigured hook or server that names such a path stays broken under a
// sandbox — the same outcome as before this grant existed — rather than silently
// gutting the mode. Everything a real installation needs is unaffected: a plugin
// under the registry root, a plugin under ~/.claude or ~/.config, an MCP program in
// /opt/<vendor>/... or inside the worktree are all at or below their anchors, not
// above them.
func (g infraGuard) permit(root string) string {
	if root == "" || !filepath.IsAbs(root) {
		return ""
	}
	if pathDepth(root) < 2 {
		return "" // "/", "/Users", "/home", "/private", "/var", "/opt", "/Volumes", …
	}
	for _, anchor := range g.shared {
		if dirContainsOrEquals(root, anchor) {
			return ""
		}
	}
	return root
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
	return stripDataVolumeAlias(resolved), fi
}

// dataVolumePrefix is macOS's second real spelling for every data-volume path:
// /System/Volumes/Data/Users/x and /Users/x are the same file. Firmlinks are not
// symlinks, so EvalSymlinks does NOT collapse them (the sandbox backend denies
// both spellings for exactly this reason). Left uncollapsed here, the alias would
// walk straight past the guard: "/System/Volumes/Data/Users" is four components
// deep and is not an ancestor of the canonical "/Users/<user>" home, so it would
// pass both checks while granting every home on the machine.
const dataVolumePrefix = "/System/Volumes/Data"

// stripDataVolumeAlias reduces a path to its plain spelling, so the alias and the
// direct path compare equal in every guard check below.
func stripDataVolumeAlias(p string) string {
	c := filepath.Clean(p)
	if c == dataVolumePrefix {
		return "/"
	}
	if rest, ok := strings.CutPrefix(c, dataVolumePrefix+"/"); ok {
		return filepath.Clean("/" + rest)
	}
	return c
}

// canonicalPath is statCanonical's path half, for anchors that must be comparable
// even if they cannot be stat'd. An unresolvable path is cleaned, never dropped —
// a dropped anchor would silently weaken the guard.
func canonicalPath(p string) string {
	if strings.TrimSpace(p) == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return stripDataVolumeAlias(resolved)
	}
	return stripDataVolumeAlias(abs)
}

// pathDepth counts a cleaned absolute path's components ("/" is 0, "/Users" is 1,
// "/Users/jesse" is 2).
func pathDepth(p string) int {
	trimmed := strings.Trim(filepath.ToSlash(filepath.Clean(p)), "/")
	if trimmed == "" {
		return 0
	}
	return strings.Count(trimmed, "/") + 1
}

// dirContainsOrEquals reports whether parent is at or above child.
func dirContainsOrEquals(parent, child string) bool {
	if parent == "" || child == "" {
		return false
	}
	if parent == child {
		return true
	}
	return strings.HasPrefix(child, parent+string(filepath.Separator))
}
