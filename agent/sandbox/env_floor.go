package sandbox

import (
	"path/filepath"
	"slices"
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
//   - points TMPDIR and SERF_SCRATCH_DIR at the per-session scratch, and
//   - redirects the language cache vars (GOCACHE / GOMODCACHE / npm_config_cache /
//     CARGO_HOME) into the session tmp when the cache strategy is session-private,
//     so a sandboxed build can never poison a cache a later build consumes.
//
// It is a pure function of its inputs and returns a fresh slice; it never reads
// the process environment. Called at EVERY spawn site (shell jobs, rg, stdio MCP
// servers, hook commands) so no spawned process escapes the floor.
func ApplyEnvFloor(env []string, policy ResolvedPolicy, sessionScratch string) []string {
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
		if policy.CacheStrategy == CacheSessionPrivate && isRedirectedCacheVar(name) {
			continue // re-added below, pointing into the session scratch
		}
		out = append(out, kv)
	}

	out = ApplySessionScratchEnv(out, sessionScratch)
	if sessionScratch != "" {
		if policy.CacheStrategy == CacheSessionPrivate {
			out = append(out,
				"GOCACHE="+filepath.Join(sessionScratch, "gocache"),
				envvars.GoModCache.Assignment(filepath.Join(sessionScratch, "gomodcache")),
				"npm_config_cache="+filepath.Join(sessionScratch, "npm"),
				envvars.CargoHome.Assignment(filepath.Join(sessionScratch, "cargo")),
			)
		}
	}
	return out
}

// ApplySessionScratchEnv replaces the two reserved scratch variables together.
// An empty path removes stale values without installing a replacement.
func ApplySessionScratchEnv(env []string, scratchDir string) []string {
	out := make([]string, 0, len(env)+2)
	for _, kv := range env {
		name, _, ok := strings.Cut(kv, "=")
		if ok && (name == envvars.TmpDir.Name || name == envvars.SERFScratchDir.Name) {
			continue
		}
		out = append(out, kv)
	}
	if scratchDir != "" {
		out = append(out,
			envvars.TmpDir.Assignment(scratchDir),
			envvars.SERFScratchDir.Assignment(scratchDir),
		)
	}
	return out
}

// floorDrops reports whether an env var name is removed by the floor.
func floorDrops(name string) bool {
	if slices.Contains(floorExactDrops, name) {
		return true
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
// GOMODCACHE is included alongside GOCACHE: it defaults to $GOPATH/pkg/mod, and
// the granted cache root the resolver computes (cacheRootsFor) is a fixed
// $HOME/go/pkg — it does not track a custom GOPATH, so an ambient GOMODCACHE
// computed from a non-default GOPATH would land outside every granted root.
// Verified 2026-08-06 (see env_floor_test.go).
func isRedirectedCacheVar(name string) bool {
	return name == "GOCACHE" || name == envvars.GoModCache.Name || name == "npm_config_cache" || name == envvars.CargoHome.Name
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
