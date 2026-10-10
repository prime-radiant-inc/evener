package sandbox

import (
	"errors"
	"io/fs"
	"os"
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
//   - redirects the language cache vars into the session tmp so a sandboxed
//     build can never poison a cache a later build consumes: all of them
//     (GOCACHE / GOMODCACHE / GOPATH / npm_config_cache / CARGO_HOME) under the
//     session-private strategy, and under the overlay the Go caches it does not
//     serve (see redirectedCacheVars).
//
// It returns a fresh slice and never reads the process environment; the overlay
// branch inspects the filesystem at the cache paths it judges (overlayServes).
// Called at EVERY spawn site (shell jobs, rg, stdio MCP servers, hook commands)
// so no spawned process escapes the floor.
func ApplyEnvFloor(env []string, policy ResolvedPolicy, sessionScratch string) []string {
	out := make([]string, 0, len(env)+4)
	redirected := redirectedCacheVars(env, policy)
	goEnvOff := spawnGoEnvFileRefused(env, policy.resolveHost.OS)
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
		if name == envvars.GoEnv.Name && goEnvOff {
			continue // re-added below as off
		}
		out = append(out, kv)
	}

	// A go env file that cannot be read now (a FIFO, a device) would block or
	// flood the spawned go, so it is turned off for this spawn; one repaired,
	// or a healthy one the spawn names instead, is read as usual.
	if goEnvOff {
		out = append(out, envvars.GoEnv.Assignment("off"))
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
// overlay, any Go cache whose directory the overlay does not serve
// (overlayGoCacheDirs; its root was dropped, say inside the worktree) or that
// lies in a persistent write root re-bound on top of the overlay, which would
// otherwise stay persistently writable, and all of them when the spawn's
// go env file cannot be judged (unreadable now, relative, or not the probe's),
// since the overlay cannot know where go will write.
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
		dirs, goKnown := overlayGoCacheDirs(env, policy.resolveHost)
		writeRoots := bwrapWriteRoots(policy)
		for _, name := range []string{envvars.GoCache.Name, envvars.GoModCache.Name, envvars.GoPath.Name} {
			if dir := dirs[name]; !goKnown || (filepath.IsAbs(dir) && (!overlayServes(policy.CacheRoots, dir) || insideAnyRoot(dir, writeRoots))) {
				out[name] = true
			}
		}
	}
	return out
}

// overlayServes reports whether the overlay covers writes to dir: dir lies under
// an existing cache root by spelling, can be a directory, and its real destination
// (following any symlink on the way, existing or not yet created) still lies
// under one, so a link from a served root into the worktree does not count.
func overlayServes(roots []string, dir string) bool {
	// Only an existing directory is overlaid at spawn (buildBwrapArgv skips
	// a root removed since the wrapper was built), so only one serves.
	roots = slices.DeleteFunc(slices.Clone(roots), func(r string) bool {
		fi, err := os.Stat(r)
		return err != nil || !fi.IsDir()
	})
	if !isUnderAnyRoot(dir, roots) || !canBeDirectory(dir) {
		return false
	}
	dest := resolveExistingPrefix(dir)
	for _, r := range roots {
		if pathUnder(dest, r) || pathUnder(dest, resolveExistingPrefix(r)) {
			return true
		}
	}
	return false
}

// insideAnyRoot reports whether dir lies at or under one of roots, by spelling
// or resolved location. A persistent write root inside an overlaid cache root
// is bound writable on top of the overlay (buildBwrapArgv), so a cache there
// persists and is not served.
func insideAnyRoot(dir string, roots []string) bool {
	rd := resolveExistingPrefix(dir)
	return slices.ContainsFunc(roots, func(r string) bool {
		return pathUnder(dir, r) || pathUnder(rd, resolveExistingPrefix(r))
	})
}

// canBeDirectory reports whether dir is a directory or could be created as
// one: its nearest existing ancestor (following symlinks) is a directory. An
// existing file, a path below one, or a dangling symlink on the way cannot be,
// and a go pointed at it fails rather than caching.
func canBeDirectory(dir string) bool {
	for p := dir; ; p = filepath.Dir(p) {
		fi, err := os.Stat(p)
		if err == nil {
			return fi.IsDir()
		}
		if _, lerr := os.Lstat(p); lerr == nil || !errors.Is(err, fs.ErrNotExist) {
			return false // a dangling symlink, or a path that cannot be inspected
		}
		if filepath.Dir(p) == p {
			return false
		}
	}
}

// overlayGoCacheDirs returns, for each Go cache variable, the directory a
// spawned go would write that cache to: the spawn's own setting, else the
// host's resolved one (env or go env -w), else Go's default under the spawn's
// own HOME and XDG_CACHE_HOME. GOPATH maps to its first absolute entry's pkg,
// where the checksum database lives. The host's settings count only when the
// spawn's go reads the same go env file the probe read; a spawn pointing at
// another file (an alternate GOENV) may configure its caches anywhere, which it
// reports as not known.
func overlayGoCacheDirs(env []string, host HostFacts) (map[string]string, bool) {
	vars := envVars(env)
	firstOr := func(values ...string) string {
		for _, v := range values {
			if v != "" {
				return v
			}
		}
		return ""
	}
	// Defaults follow the spawn's own HOME and XDG_CACHE_HOME, never the
	// host's: a child run with its own (or none) writes where its tools decide.
	home := vars[envvars.Home.Name]
	out := map[string]string{}

	// Go settings come from the spawn's environment, then the go env file it
	// reads: that file's own values (never the host process's environment
	// overrides) as they are now, and only when it is the file the probe read.
	file := map[string]string{}
	if spawnFile := goEnvFileFor(vars, host.OS); spawnFile != "" {
		// A relative file resolves against the spawn's own directory, which
		// the floor does not know, so it may not be the probe's file.
		if spawnFile != host.GoEnvFile || !filepath.IsAbs(spawnFile) {
			return out, false
		}
		var ok bool
		if file, ok = currentGoEnvSettings(spawnFile); !ok {
			return out, false
		}
	}
	gopath := ""
	if entries := goPathEntries(HostFacts{Home: home, GoPath: firstOr(vars[envvars.GoPath.Name], file[envvars.GoPath.Name])}); len(entries) > 0 {
		gopath = entries[0]
	}
	cacheHome := vars[envvars.XDGCacheHome.Name]
	if cacheHome == "" && home != "" {
		cacheHome = filepath.Join(home, ".cache")
	}
	out[envvars.GoModCache.Name] = firstOr(vars[envvars.GoModCache.Name], file[envvars.GoModCache.Name])
	out[envvars.GoCache.Name] = firstOr(vars[envvars.GoCache.Name], file[envvars.GoCache.Name])
	if gopath != "" {
		out[envvars.GoPath.Name] = filepath.Join(gopath, "pkg")
		if out[envvars.GoModCache.Name] == "" {
			out[envvars.GoModCache.Name] = filepath.Join(gopath, "pkg", "mod")
		}
	}
	if out[envvars.GoCache.Name] == "" && cacheHome != "" {
		out[envvars.GoCache.Name] = filepath.Join(cacheHome, "go-build")
	}
	return out, true
}

// envVars maps an environment slice to its values by name.
func envVars(env []string) map[string]string {
	vars := map[string]string{}
	for _, kv := range env {
		name, val, _ := strings.Cut(kv, "=")
		vars[name] = val
	}
	return vars
}

// spawnGoEnvFileRefused reports whether the go env file a spawn's go would read
// exists now but cannot be read as a small regular file (a FIFO, a device, an
// oversized file), the way the probe reads it.
func spawnGoEnvFileRefused(env []string, goos string) bool {
	file := goEnvFileFor(envVars(env), goos)
	if !filepath.IsAbs(file) {
		return false
	}
	_, err := readGoEnvFileSettings(hostProbeSystem{}, file)
	return err != nil && !errors.Is(err, fs.ErrNotExist)
}

// currentGoEnvSettings reads the go env file at path as it is now, since the
// spawned go reads it at every run (a `go env -w` mid-session changes what the
// next spawn does). ok is false when the file exists but cannot be read; a
// missing file has no settings.
func currentGoEnvSettings(path string) (map[string]string, bool) {
	settings, err := readGoEnvFileSettings(hostProbeSystem{}, path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, true
	}
	return settings, err == nil
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
			if settings, ok := currentGoEnvSettings(file); ok {
				configured = settings[envvars.GoPath.Name]
			}
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
