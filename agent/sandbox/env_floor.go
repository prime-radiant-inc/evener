package sandbox

import (
	"path/filepath"
	"strings"

	"primeradiant.com/serf/envvars"
)

// floorExactDrops are environment variables removed from every spawned process in
// a sandboxed session, in addition to serf's existing *KEY*/*SECRET*/*TOKEN*/…
// scrub. A live ssh-agent socket is sign-anything/exfil even with ~/.ssh masked,
// so its handle must not survive into a spawned command.
var floorExactDrops = []string{
	"SSH_AUTH_SOCK",
	// GNUPGHOME would redirect gpg to a home outside the masked ~/.gnupg.
	"GNUPGHOME",
	// DOCKER_HOST points the docker client at a daemon endpoint (pairs with the
	// masked docker sockets): a set DOCKER_HOST could name a reachable daemon.
	"DOCKER_HOST",
}

// floorPrefixDrops are environment-variable name prefixes removed from every
// spawned process: cloud credential-agent and session vars whose secrets a masked
// ~/.aws / ~/.config/gcloud would otherwise still be reachable through.
var floorPrefixDrops = []string{
	"AWS_",
	"GOOGLE_",
	"GCLOUD_",
	"VAULT_",
}

// ApplyEnvFloor raises the sandbox environment floor on top of an already-scrubbed
// env slice (the output of serf's EnvPolicy filtering). It:
//   - drops the ssh-agent handle and cloud credential vars (floorExactDrops /
//     floorPrefixDrops),
//   - drops a worktree-external KUBECONFIG (an absolute path outside every granted
//     root points at a cluster config the sandboxed session should not reach),
//   - points TMPDIR at the per-session tmp, and
//   - redirects the language cache vars (GOCACHE / npm_config_cache / CARGO_HOME)
//     into the session tmp when the cache strategy is session-private, so a
//     sandboxed build can never poison a cache a later build consumes.
//
// It is a pure function of its inputs and returns a fresh slice; it never reads
// the process environment. Called at EVERY spawn site (shell jobs, rg, stdio MCP
// servers, hook commands) so no spawned process escapes the floor.
func ApplyEnvFloor(env []string, policy ResolvedPolicy, sessionTmp string) []string {
	out := make([]string, 0, len(env)+4)
	for _, kv := range env {
		name, val, ok := strings.Cut(kv, "=")
		if !ok {
			out = append(out, kv)
			continue
		}
		if floorDrops(name) {
			continue
		}
		if name == "KUBECONFIG" && kubeconfigIsExternal(val, policy) {
			continue
		}
		if sessionTmp != "" && name == envvars.TmpDir.Name {
			continue // re-added below, pointing at the session tmp
		}
		if policy.CacheStrategy == CacheSessionPrivate && isRedirectedCacheVar(name) {
			continue // re-added below, pointing into the session tmp
		}
		out = append(out, kv)
	}

	if sessionTmp != "" {
		out = append(out, envvars.TmpDir.Assignment(sessionTmp))
		if policy.CacheStrategy == CacheSessionPrivate {
			out = append(out,
				"GOCACHE="+filepath.Join(sessionTmp, "gocache"),
				"npm_config_cache="+filepath.Join(sessionTmp, "npm"),
				envvars.CargoHome.Assignment(filepath.Join(sessionTmp, "cargo")),
			)
		}
	}
	return out
}

// floorDrops reports whether an env var name is removed by the floor.
func floorDrops(name string) bool {
	for _, d := range floorExactDrops {
		if name == d {
			return true
		}
	}
	for _, p := range floorPrefixDrops {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// isRedirectedCacheVar reports whether name is a language cache var the floor
// redirects into the session tmp under a session-private cache strategy.
func isRedirectedCacheVar(name string) bool {
	return name == "GOCACHE" || name == "npm_config_cache" || name == envvars.CargoHome.Name
}

// kubeconfigIsExternal reports whether a KUBECONFIG value points outside every
// granted root (the worktree, its read/write roots). KUBECONFIG is a
// ListSeparator-joined list that kubectl merges entry-by-entry, so it is split
// and the var is treated as external when ANY absolute entry lands outside the
// granted roots — otherwise an in-worktree entry could smuggle an out-of-tree
// cluster config through alongside it. Empty and relative entries are ignored: a
// relative kubeconfig resolves within the worktree cwd, not an external cluster.
func kubeconfigIsExternal(val string, policy ResolvedPolicy) bool {
	roots := make([]string, 0, 8)
	roots = append(roots, policy.Git.WorktreeRoot)
	roots = append(roots, policy.FileTool.ReadRoots...)
	roots = append(roots, policy.FileTool.WriteRoots...)
	roots = append(roots, policy.Spawned.ReadRoots...)
	roots = append(roots, policy.Spawned.WriteRoots...)
	for _, entry := range filepath.SplitList(val) {
		entry = strings.TrimSpace(entry)
		if entry == "" || !filepath.IsAbs(entry) {
			continue
		}
		if !isUnderAnyRoot(entry, roots) {
			return true
		}
	}
	return false
}
