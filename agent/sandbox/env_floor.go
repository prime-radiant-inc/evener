package sandbox

import (
	"path/filepath"
	"slices"
	"strings"

	"primeradiant.com/evener/envvars"
)

// floorExactDrops are environment variables removed from every spawned process in
// a sandboxed session, in addition to evener's existing *KEY*/*SECRET*/*TOKEN*/…
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
// env slice (the output of evener's EnvPolicy filtering). It:
//   - drops the ssh-agent handle and cloud credential vars (floorExactDrops /
//     floorPrefixDrops),
//   - drops a worktree-external KUBECONFIG (an absolute path outside every granted
//     root points at a cluster config the sandboxed session should not reach),
//   - puts the resolved developer-toolchain bin directory on PATH ahead of the
//     system directories, so a spawned `git` is the real git and not the
//     /usr/bin xcrun shim (which is loud and slow under a sandbox),
//   - points TMPDIR and EVENER_SCRATCH_DIR at the per-session scratch, and
//   - redirects the language cache vars (GOCACHE / GOMODCACHE / GOPATH /
//     npm_config_cache / CARGO_HOME) into the session tmp when the cache strategy
//     is session-private, so a sandboxed build can never poison a cache a later
//     build consumes.
//
// It is a pure function of its inputs and returns a fresh slice; it never reads
// the process environment. Called at EVERY spawn site (shell jobs, rg, stdio MCP
// servers, hook commands) so no spawned process escapes the floor.
func ApplyEnvFloor(env []string, policy ResolvedPolicy, sessionScratch string) []string {
	out := make([]string, 0, len(env)+4)
	redirected := redirectedCacheVars(env, policy)
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
		if redirected[name] {
			continue // re-added below, pointing into the session scratch
		}
		out = append(out, kv)
	}

	out = applyToolchainPath(out, policy.ToolchainBinDir)
	out = ApplySessionScratchEnv(out, sessionScratch)
	if sessionScratch != "" {
		for _, r := range []struct{ name, value string }{
			{envvars.GoCache.Name, filepath.Join(sessionScratch, goCacheDirName)},
			{envvars.GoModCache.Name, filepath.Join(sessionScratch, goModCacheDirName)},
			{envvars.GoPath.Name, sessionGoPath(env, policy, sessionScratch)},
			{"npm_config_cache", filepath.Join(sessionScratch, npmCacheDirName)},
			{envvars.CargoHome.Name, filepath.Join(sessionScratch, cargoHomeDirName)},
		} {
			if redirected[r.name] {
				out = append(out, r.name+"="+r.value)
			}
		}
	}
	return out
}

// redirectedCacheVars names the cache variables the floor points into the
// session scratch: every one under the session-private strategy. Under the
// overlay, the Go caches when the go env file was unreadable at session start
// (the overlay cannot know where go will write), and a GOCACHE or GOMODCACHE
// the overlay does not serve (its root was dropped, say inside the worktree),
// which would otherwise stay persistently writable.
//
// GOMODCACHE goes alongside GOCACHE because an ambient value can name any
// directory. GOPATH goes because Go writes the checksum database's tree heads to
// its first entry's pkg/sumdb whatever GOMODCACHE says (see sessionGoPath), yet
// GOMODCACHE stays set explicitly: an environment value overrides one written
// with `go env -w`, which deriving it from GOPATH would not. PATH is left alone,
// so `go install` output in the scratch GOPATH's bin runs by its path: a
// session-writable directory on PATH would let anything the model writes there
// shadow the commands every spawn site runs, hooks included.
func redirectedCacheVars(env []string, policy ResolvedPolicy) map[string]bool {
	out := map[string]bool{}
	switch policy.CacheStrategy {
	case CacheSessionPrivate:
		for _, name := range []string{envvars.GoCache.Name, envvars.GoModCache.Name, envvars.GoPath.Name, "npm_config_cache", envvars.CargoHome.Name} {
			out[name] = true
		}
	case CacheOverlay:
		if policy.resolveHost.GoEnvUnreadable {
			out[envvars.GoCache.Name], out[envvars.GoModCache.Name], out[envvars.GoPath.Name] = true, true, true
		}
		for _, kv := range env {
			name, val, _ := strings.Cut(kv, "=")
			if (name == envvars.GoCache.Name || name == envvars.GoModCache.Name) && val != "" && !isUnderAnyRoot(val, policy.CacheRoots) {
				out[name] = true
			}
		}
	}
	return out
}

// sessionGoPath returns the GOPATH a session-private spawn gets: the scratch
// first, because Go writes the checksum database and `go install` output only to
// the first entry. Where the spawned layer reads anywhere, the ambient GOPATH
// follows, so GOPATH-mode builds still find the packages already there,
// read-only: the spawn's own GOPATH when it has one, else what its go would
// resolve: the host's `go env -w` setting when it reads the same env file,
// otherwise Go's default for its HOME.
// Restricted mode cannot read it, so it gets the scratch alone. Entries inside
// the scratch are dropped from the ambient value, so flooring an already-floored
// env does not repeat them.
func sessionGoPath(env []string, policy ResolvedPolicy, sessionScratch string) string {
	scratchGoPath := filepath.Join(sessionScratch, goPathDirName)
	if policy.Spawned.Read != ReadAnywhere {
		return scratchGoPath
	}
	var ambient []string
	vars := map[string]string{}
	for _, kv := range env {
		name, val, _ := strings.Cut(kv, "=")
		if name == envvars.GoPath.Name && val != "" {
			ambient = filepath.SplitList(val)
		}
		vars[name] = val
	}
	if ambient == nil {
		// What the spawned go would use for GOPATH: the host's configured value
		// when it reads the same go env file the probe read, else Go's default
		// for its own HOME. A clean environment (EnvPolicyNone) has neither.
		host := policy.resolveHost
		configured := ""
		if file := goEnvFileFor(vars, host.OS); file != "" && file == host.GoEnvFile {
			configured = host.GoPath
		}
		ambient = goPathEntries(HostFacts{Home: vars[envvars.Home.Name], GoPath: configured})
	}
	entries := []string{scratchGoPath}
	for _, entry := range ambient {
		// The go command refuses relative entries, as goPathEntries does.
		if filepath.IsAbs(entry) && !isUnderAnyRoot(entry, []string{sessionScratch}) {
			entries = append(entries, entry)
		}
	}
	return strings.Join(entries, string(filepath.ListSeparator))
}

// goEnvFileFor returns the go env file a go command run with vars would read,
// mirroring goEnvFile and os.UserConfigDir for goos: $GOENV, else
// $HOME/Library/Application Support/go/env on darwin, else
// ${XDG_CONFIG_HOME:-$HOME/.config}/go/env; "" when GOENV=off or nothing locates
// it.
func goEnvFileFor(vars map[string]string, goos string) string {
	if file := vars[envvars.GoEnv.Name]; file != "" {
		if file == "off" {
			return ""
		}
		return file
	}
	home := vars[envvars.Home.Name]
	switch {
	case goos == "darwin" && home != "":
		return filepath.Join(home, "Library", "Application Support", "go", "env")
	case goos != "darwin" && vars[envvars.XDGConfigHome.Name] != "":
		return filepath.Join(vars[envvars.XDGConfigHome.Name], "go", "env")
	case goos != "darwin" && home != "":
		return filepath.Join(home, ".config", "go", "env")
	}
	return ""
}

// systemBinDirs are the PATH entries the macOS developer-tool shims live in.
// The toolchain directory is inserted immediately BEFORE the first of them.
var systemBinDirs = []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin"}

// applyToolchainPath inserts the developer-toolchain bin directory into PATH so
// a spawned `git` is the real git rather than the /usr/bin xcrun shim, whose
// per-call lookup a sandbox makes both loud and slow (see
// ResolvedPolicy.ToolchainBinDir).
//
// It goes in just ahead of the SYSTEM directories rather than at the front, so
// it shadows the shims and nothing else: a developer who put their own git — a
// Homebrew build, a version manager — ahead of /usr/bin still gets it. With no
// system directory on PATH there is no shim to shadow, so the toolchain goes
// last, where it can only help a lookup that would otherwise fail.
//
// An empty binDir, an absent PATH, an explicitly EMPTY PATH, or a PATH that
// already names the directory are all left exactly as they were: the floor never
// invents a search path, and an empty PATH is a deliberate "find nothing".
func applyToolchainPath(env []string, binDir string) []string {
	if binDir == "" {
		return env
	}
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, val, ok := strings.Cut(kv, "=")
		if !ok || name != envvars.Path.Name {
			out = append(out, kv)
			continue
		}
		out = append(out, envvars.Path.Assignment(insertToolchainDir(val, binDir)))
	}
	return out
}

// insertToolchainDir returns path with binDir placed before its first system
// directory (or appended when it has none), unchanged if it already lists it or
// is empty. Entries are compared CLEANED, so a trailing slash or a doubled
// separator names the same directory it would name to the kernel and does not
// earn a second copy.
func insertToolchainDir(path, binDir string) string {
	if path == "" {
		return path
	}
	entries := filepath.SplitList(path)
	clean := make([]string, len(entries))
	for i, e := range entries {
		clean[i] = filepath.Clean(e)
	}
	// The already-present check runs over the WHOLE list before any insertion:
	// a system directory listed BEFORE the toolchain is still an insertion point
	// the toolchain does not need, and inserting there would duplicate it.
	if slices.Contains(clean, filepath.Clean(binDir)) {
		return path
	}
	for i, c := range clean {
		if slices.Contains(systemBinDirs, c) {
			return strings.Join(slices.Insert(slices.Clone(entries), i, binDir), string(filepath.ListSeparator))
		}
	}
	return strings.Join(append(slices.Clone(entries), binDir), string(filepath.ListSeparator))
}

// ApplySessionScratchEnv replaces the two reserved scratch variables together.
// An empty path removes stale values without installing a replacement.
func ApplySessionScratchEnv(env []string, scratchDir string) []string {
	out := make([]string, 0, len(env)+2)
	for _, kv := range env {
		name, _, ok := strings.Cut(kv, "=")
		if ok && (name == envvars.TmpDir.Name || name == envvars.EVENERScratchDir.Name) {
			continue
		}
		out = append(out, kv)
	}
	if scratchDir != "" {
		out = append(out,
			envvars.TmpDir.Assignment(scratchDir),
			envvars.EVENERScratchDir.Assignment(scratchDir),
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
