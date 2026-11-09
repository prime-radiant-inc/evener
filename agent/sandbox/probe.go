package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/envvars/userdirs"
)

// HostFacts are the backend-relevant capabilities of the host, gathered once at
// session start. They are plain data so the resolver stays a pure function of
// (policy, facts, cwd): the real prober fills them by probing the kernel/binaries;
// unit tests inject a FakeProber and never touch the host. The three fields that
// matter to the fail-closed floor are OS, BwrapCapable, and (macOS)
// SandboxExecPath.
type HostFacts struct {
	// OS is runtime.GOOS: "linux", "darwin", "windows", …. Sandboxing is only
	// expressible on linux (bwrap) and darwin (Seatbelt); every other OS refuses
	// any sandboxed mode.
	OS string

	// Home is the user's home directory (absolute), the anchor the resolver joins
	// the credential denylist against (~/.ssh, ~/.aws, …). It is a host/session
	// fact rather than a capability, but the resolver needs it and threading it
	// through HostFacts keeps Resolve a pure function of its inputs (no env reads).
	Home string

	// BwrapPath is the resolved bubblewrap binary path, or "" if not found.
	BwrapPath string

	// BwrapCapable reports that bwrap is present AND can actually create the
	// namespaces it needs (unprivileged user namespaces work). bwrap serves the
	// full mode matrix and net=off, so this being true is what lets every mode run.
	//
	// RealProber sets this from a real userns-execution probe (M3): it runs a
	// trivial fully namespaced sandbox and reports true only if it exits 0, so a
	// host with apparmor_restrict_unprivileged_userns=1 (which `--version` fine but
	// cannot namespace) reports false and the floor refuses. Tests set it directly
	// via FakeProber.
	BwrapCapable bool

	// OverlaySupported reports that bwrap can mount a read-real/write-private
	// overlay (bwrap ≥ 0.5, kernel overlay support). Only affects cache strategy:
	// when false, cache roots degrade to session-private redirect (never
	// persistent-writable).
	OverlaySupported bool

	// SandboxExecPath is the resolved /usr/bin/sandbox-exec path on darwin
	// (Seatbelt), or "" if unavailable. Seatbelt is deny-capable and serves the
	// full mode matrix (cache always session-private — no overlay on macOS).
	SandboxExecPath string

	// DeveloperToolRoots are the macOS developer-toolchain directories a spawned
	// process must be able to READ to run the developer tools macOS ships as
	// xcrun shims. /usr/bin/git is the load-bearing case: it is a shim that execs
	// the real git out of the ACTIVE developer directory (`xcode-select -p`) or
	// the standalone Command Line Tools, neither of which lives under the system
	// read roots — so without these, restricted mode could not run git at all.
	//
	// Read-only, and only consulted by restricted mode (the other modes' spawned
	// layer already reads everything but the denylist). Empty on non-darwin hosts
	// and on a Mac with no toolchain installed; a missing path is simply absent,
	// never a session-start failure.
	DeveloperToolRoots []string

	// DeveloperToolBinDir is the bin directory of the ACTIVE developer directory
	// (`xcode-select -p` + /usr/bin), where the real git, clang and friends that
	// the /usr/bin shims exec actually live. Empty when no toolchain is active or
	// the directory does not exist.
	//
	// It is named so the env floor can put it on PATH and let a sandboxed `git`
	// BE the real git. The /usr/bin shim otherwise memoizes its tool lookup in a
	// cache file under the per-user temp directory confstr(_CS_DARWIN_USER_TEMP_DIR)
	// reports — which honours no env var, sits in a shared multi-tenant tree no
	// mode makes writable, and so fails on every call, making the shim re-run
	// `xcodebuild -find git` each time: two denial lines on stderr and seconds of
	// latency per invocation.
	//
	// Derived from the same `xcode-select -p` probe as DeveloperToolRoots, and so
	// equally untrusted ($DEVELOPER_DIR steers it): the resolver passes it through
	// RootGuard and requires it to be readable under the mode's own grants before
	// naming it. It adds no grant — the directory is already inside the read-only
	// toolchain grant.
	DeveloperToolBinDir string

	// GitGlobalConfigPaths are the user's GLOBAL git config FILES that exist on
	// this host. git-config(1) FILES names two of them —
	// $XDG_CONFIG_HOME/git/config (with $HOME/.config standing in for an unset or
	// empty XDG_CONFIG_HOME) and ~/.gitconfig — and reads BOTH when both exist.
	// They live under $HOME, which restricted mode grants no read of, so without
	// this every git invocation in a restricted session died with
	// "fatal: unable to access '<home>/.gitconfig': Operation not permitted" on
	// any host whose developer has a global config at all.
	//
	// READ-ONLY and FILE-EXACT (ruled 2026-08-07): each entry is an existing
	// non-directory FILE, never a directory and never the home directory itself,
	// so the grant cannot widen into a home read. File-exactness is enforced HERE,
	// at the probe: RootGuard, which every candidate also passes through, refuses
	// shared trees but admits directories by design (the toolchain and hook/MCP
	// grants are directories), so it is the non-directory test below — not the
	// guard — that keeps this grant to files. It changes no write surface — git
	// config and hook writes stay denied — and loses to the credential denylist,
	// so a credential.helper line becomes readable while ~/.git-credentials stays
	// masked. A path that does not exist contributes nothing and never fails
	// session start. A non-empty $GIT_CONFIG_GLOBAL replaces both FILES; a
	// set-but-empty or unreadable value names no config git reads (git ignores
	// it and the developer's settings are silently absent), so nothing is
	// granted for it.
	GitGlobalConfigPaths []string

	// GitSystemConfigPaths are the SYSTEM-WIDE git config FILES git reads on
	// this host. The built-in location lives under /etc, which restricted mode
	// already grants as a system read root, so this names only an override:
	// $GIT_CONFIG_SYSTEM when it is a non-empty absolute path to an existing
	// non-directory FILE and $GIT_CONFIG_NOSYSTEM does not disable the system
	// config. That override sits anywhere the environment says, typically under
	// $HOME, which restricted mode otherwise grants no read of; an unreadable
	// $GIT_CONFIG_SYSTEM is fatal to git rather than ignored (verified on git
	// 2.43), so without the grant the environment variable could make every git
	// command in a restricted session fail. Like GitGlobalConfigPaths it is
	// READ-ONLY, FILE-EXACT, changes no write surface, and loses to the
	// credential denylist.
	GitSystemConfigPaths []string

	// EvenerContentRoots are the installed plugin files in Evener's plugin store
	// and the user's skills directory at their configured locations.
	// Missing directories are listed anyway, so one created mid-session is
	// covered from its next use.
	EvenerContentRoots []string

	// EvenerManagedContentRoots are the installed-plugin content roots inside
	// EvenerContentRoots (the cache and bundled directories), distinct from the
	// user's own skills directory. EvenerManagedContentClean reports whether the
	// plugin store's last complete audit found no Git metadata there; only then
	// may a backend skip its per-spawn walk for those roots.
	EvenerManagedContentRoots []string
	EvenerManagedContentClean bool

	// EvenerConfigRoot is Evener's config root ($XDG_CONFIG_HOME/evener, else
	// ~/.config/evener). Resolve masks it wherever it lives, since that is where
	// hub configuration and credentials are kept.
	EvenerConfigRoot string

	// GoPath, GoModCache and GoCache are the go command's settings as configured
	// on this host, in the environment or with `go env -w` (see goEnvValue),
	// resolved once at session start. Empty means unset: Go's own default
	// applies, and goPathEntries holds GOPATH's. They grant nothing: they place
	// the overlaid Go cache roots and the ambient GOPATH the env floor keeps
	// behind the session scratch.
	GoPath     string
	GoModCache string
	GoCache    string

	// XDGCacheHome is $XDG_CACHE_HOME, which moves ~/.cache and with it Go's
	// default GOCACHE; empty when unset.
	XDGCacheHome string

	// NpmConfigCache and CargoHome are $npm_config_cache and $CARGO_HOME, which
	// move npm's and cargo's cache directories; empty when unset. Cargo keeps no
	// file for CARGO_HOME. npm also reads a `cache=` setting from its .npmrc
	// files; NpmrcCaches holds every absolute value this host's user and global
	// files set. The project file is read at resolution and per spawn, where the
	// session's package directory and the spawn environment are known, so an
	// overlay serves a cache moved only there (#4542).
	NpmConfigCache string
	CargoHome      string
	NpmrcCaches    []string

	// OverlayLowerOK reports whether bwrap can mount an unprivileged overlay
	// with a given directory as its lower on this host. Whether overlay is
	// supported at all is host-wide (OverlaySupported), but whether one mounts
	// depends on each lower directory's filesystem: a lower on an overlayfs
	// root, or a network mount, is refused with EINVAL, which would abort the
	// whole sandbox at every spawn (#4220). The probe runs a trivial namespaced
	// overlay mount per root, once per session. nil means "every root can be
	// overlaid": the host has no overlay support, or nothing supplied a probe
	// (tests, a hand-built HostFacts).
	OverlayLowerOK func(root string) bool

	// LoginHome is the login user's home directory from the user database
	// (`getpwuid`/`/etc/passwd`), which is what npm and cargo fall back to for
	// "~" when their environment carries no HOME (a clean EnvPolicyNone child).
	// Empty when it cannot be resolved. It can differ from Home, which is this
	// process's own HOME and which a deployment may override.
	LoginHome string

	// GoEnvFile is the go env file the probe read GoPath from ("" when none).
	// The env floor reads it again at each spawn whose go reads the same file,
	// since go reads it at every run.
	GoEnvFile string

	// KernelVersion is the best-effort `uname -r` string, informational only
	// (surfaced in the startup enforcement line, not used for decisions).
	KernelVersion string
}

// SeatbeltAvailable reports whether the macOS Seatbelt backend is usable: it
// requires darwin AND a resolved sandbox-exec binary. Presence of sandbox-exec
// on a non-darwin OS never counts.
func (h HostFacts) SeatbeltAvailable() bool {
	return h.OS == "darwin" && h.SandboxExecPath != ""
}

// Prober produces HostFacts. Injecting it keeps the resolver's tests hermetic:
// FakeProber returns canned facts, RealProber probes the live host (opt-in).
type Prober interface {
	Probe() HostFacts
}

// FakeProber returns fixed HostFacts. It is the only prober used on the unit
// path, so no unit test ever shells out to bwrap.
type FakeProber struct {
	Facts HostFacts
}

// Probe returns the canned facts.
func (f FakeProber) Probe() HostFacts { return f.Facts }

// RealProber probes the live host. It is used only behind explicit opt-in (the
// gated TestRealProberOptIn / eventual production wiring in M3+): the unit path
// never constructs it, so unit runs stay hermetic. The private system field is
// an explicit command/filesystem boundary for deterministic package tests;
// production constructs the zero value and receives hostProbeSystem.
type RealProber struct {
	system probeSystem
}

// probeSystem is the narrow host boundary needed for capability discovery. It
// intentionally owns every external read and command invocation, so the policy
// resolver and its deterministic tests never need a process-wide seam or a real
// bwrap installation.
type probeSystem interface {
	goos() string
	getenv(string) string
	environ() []string
	userHomeDir() (string, error)
	loginHomeDir() (string, error)
	lookPath(string) (string, error)
	nonDirectoryFile(string) bool
	userConfigDir() (string, error)
	readFile(string) ([]byte, error)
	run(context.Context, string, ...string) error
	combinedOutput(context.Context, string, ...string) ([]byte, error)
	output(context.Context, string, ...string) ([]byte, error)
}

type hostProbeSystem struct{}

var probeUserHomeDir = os.UserHomeDir

func (hostProbeSystem) goos() string { return runtime.GOOS }

func (hostProbeSystem) getenv(name string) string { return os.Getenv(name) }

func (hostProbeSystem) environ() []string { return os.Environ() }

func (hostProbeSystem) userHomeDir() (string, error) { return probeUserHomeDir() }

func (hostProbeSystem) loginHomeDir() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	return u.HomeDir, nil
}

func (hostProbeSystem) lookPath(name string) (string, error) { return exec.LookPath(name) }

func (hostProbeSystem) nonDirectoryFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func (hostProbeSystem) userConfigDir() (string, error) { return os.UserConfigDir() }

// goEnvFileLimit bounds the go env file the probe reads. `go env -w` writes a
// few lines; anything near this size is not one.
const goEnvFileLimit = 64 << 10

// readFile reads a small regular file. GOENV can name anything, and the probe
// runs before any sandbox exists, so a FIFO (which would block session start),
// a device such as /dev/zero (which never ends) or an oversized file is refused.
func (hostProbeSystem) readFile(path string) ([]byte, error) {
	f, err := openGoEnvFile(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return readGoEnvFile(f, path)
}

// openGoEnvFile opens a go env file without blocking (a FIFO's open waits for
// a writer); readGoEnvFile then judges the descriptor itself, so a file
// swapped in after a path check cannot slip by.
func openGoEnvFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|openNonblock, 0)
}

// readGoEnvFile reads an opened go env file: a regular file of at most
// goEnvFileLimit bytes.
func readGoEnvFile(f *os.File, path string) ([]byte, error) {
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > goEnvFileLimit {
		return nil, fmt.Errorf("%s is not a regular file of at most %d bytes", path, goEnvFileLimit)
	}
	data, err := io.ReadAll(io.LimitReader(f, goEnvFileLimit+1))
	if err == nil && len(data) > goEnvFileLimit {
		err = fmt.Errorf("%s is larger than %d bytes", path, goEnvFileLimit)
	}
	return data, err
}

func (hostProbeSystem) run(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}

func (hostProbeSystem) combinedOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func (hostProbeSystem) output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// probeCommandTimeout bounds each capability-probe subprocess.
var probeCommandTimeout = 3 * time.Second

// Probe gathers host capabilities. The bwrap capability and overlay probes are
// intentionally conservative here (presence + version); M3 hardens BwrapCapable
// into a real unprivileged-userns execution probe and adds true overlay
// detection. The Seatbelt/OS facts are exact.
func (p RealProber) Probe() HostFacts {
	system := p.system
	if system == nil {
		system = hostProbeSystem{}
	}
	return probeHost(system)
}

func probeHost(system probeSystem) HostFacts {
	facts := HostFacts{
		OS:            system.goos(),
		KernelVersion: probeKernelVersion(system),
	}
	if home, err := system.userHomeDir(); err == nil {
		facts.Home = home
	}
	if login, err := system.loginHomeDir(); err == nil {
		facts.LoginHome = login
	}
	facts.GitGlobalConfigPaths = probeGitGlobalConfigPaths(system)
	facts.GitSystemConfigPaths = probeGitSystemConfigPaths(system)
	facts.EvenerConfigRoot = probeEvenerConfigRoot(system)
	facts.EvenerContentRoots = evenerContentRoots(facts.EvenerConfigRoot, system.getenv(envvars.EVENERNoUserSkills.Name) == "1")
	storeRoot := userdirs.PluginStore(facts.EvenerConfigRoot)
	facts.EvenerManagedContentRoots = userdirs.PluginContentDirs(storeRoot)
	facts.EvenerManagedContentClean = managedContentCleanAt(system, storeRoot)
	facts.GoPath = goEnvValue(system, envvars.GoPath.Name)
	facts.GoEnvFile = goEnvFile(system)
	facts.GoModCache = goEnvValue(system, envvars.GoModCache.Name)
	facts.GoCache = goEnvValue(system, envvars.GoCache.Name)
	facts.XDGCacheHome = system.getenv(envvars.XDGCacheHome.Name)
	facts.NpmConfigCache = envValueFold(system.environ(), npmConfigCacheEnv)
	facts.CargoHome = system.getenv(envvars.CargoHome.Name)
	facts.NpmrcCaches = probeNpmrcCaches(system, facts.Home)

	if path, err := system.lookPath("bwrap"); err == nil {
		facts.BwrapPath = path
		facts.BwrapCapable = bwrapUsernsWorks(system, path)
		facts.OverlaySupported = bwrapSupportsOverlay(system, path)
		if facts.OverlaySupported {
			facts.OverlayLowerOK = memoizedOverlayLower(system, path)
		}
	}

	if facts.OS == "darwin" {
		const seatbelt = "/usr/bin/sandbox-exec"
		if system.nonDirectoryFile(seatbelt) {
			facts.SandboxExecPath = seatbelt
		}
		facts.DeveloperToolRoots = probeDeveloperToolRoots(system)
		facts.DeveloperToolBinDir = probeDeveloperToolBinDir(system)
	}

	return facts
}

// envValueFold returns the value of key in environ, matched case-insensitively:
// npm reads its environment configuration through a lowercased key, so
// NPM_CONFIG_CACHE and npm_config_cache name the same setting. A later entry
// wins, as npm's own last-assignment-wins map does.
func envValueFold(environ []string, key string) string {
	value := ""
	for _, kv := range environ {
		if name, v, ok := strings.Cut(kv, "="); ok && strings.EqualFold(name, key) {
			value = v
		}
	}
	return value
}

// gitConfigGlobalEnv, gitConfigSystemEnv and gitConfigNoSystemEnv are git's own
// environment controls over which configuration files git reads
// (git-config(1), ENVIRONMENT). They are not Evener variables, so they live
// here rather than in envvars.
const (
	gitConfigGlobalEnv   = "GIT_CONFIG_GLOBAL"
	gitConfigSystemEnv   = "GIT_CONFIG_SYSTEM"
	gitConfigNoSystemEnv = "GIT_CONFIG_NOSYSTEM"
)

// probeNpmrcCaches returns the absolute `cache=` values npm resolves from the
// .npmrc files whose locations do not depend on a project: the user config
// ($npm_config_userconfig, else ~/.npmrc) and the global config
// ($npm_config_globalconfig, else $npm_config_prefix/etc/npmrc). npm reads a
// `cache=` setting in either, so a
// host that moves its cache only there would otherwise leave it outside every
// overlay root, read-only (#4542).
//
// npm's default global config lives beside its Node installation, which the
// probe cannot locate without running npm, so it is consulted only when the
// environment names it. An absent file contributes nothing; a relative
// filename cannot be located as npm will (it resolves against npm's own
// working directory), so it contributes nothing here and the spawn floor
// treats it as unknown. The cache value is judged absolute later.
func probeNpmrcCaches(system probeSystem, home string) []string {
	environ := system.environ()
	user := envValueFold(environ, npmConfigUserConfigEnv)
	if user == "" && home != "" {
		user = filepath.Join(home, ".npmrc")
	}
	global := envValueFold(environ, npmConfigGlobalConfigEnv)
	if global == "" {
		if prefix := envValueFold(environ, npmConfigPrefixEnv); prefix != "" {
			global = filepath.Join(prefix, "etc", "npmrc")
		}
	}
	var roots []string
	for _, path := range []string{user, global} {
		roots = append(roots, readNpmrcCacheRoots(system, path, home)...)
	}
	return roots
}

// readNpmrcCacheRoots reads the absolute `cache=` values in the .npmrc at
// path. The probe's bounded, non-blocking read refuses a FIFO, a device or an
// oversized file, so a hostile file costs only its setting (#4542).
func readNpmrcCacheRoots(system probeSystem, path, home string) []string {
	lookup := npmrcEnvLookup(system.environ())
	resolved, readable, _ := npmConfigFilePath(path, lookup, home)
	if !readable {
		return nil
	}
	data, err := system.readFile(resolved)
	if err != nil {
		return nil
	}
	roots, _ := parseNpmrcCacheRoots(data, lookup, home)
	return roots
}

// readProjectNpmrcCacheRoots reads the worktree's .npmrc. It is untrusted
// repository content, so a final-component symlink is refused (O_NOFOLLOW) and
// a cache it names is served only where it is already a directory: resolution
// must never create a host directory on repository say-so.
func readProjectNpmrcCacheRoots(path string, lookup func(string) string, home string) []string {
	var roots []string
	values, _ := readProjectNpmrc(path, lookup, home)
	for _, root := range values {
		if fi, err := os.Stat(root); err == nil && fi.IsDir() {
			roots = append(roots, root)
		}
	}
	return roots
}

// readProjectNpmrcCacheValue returns the last cache value from the project
// file, whether or not that directory exists.
func readProjectNpmrcCacheValue(path string, lookup func(string) string, home string) (string, bool) {
	values, ok := readProjectNpmrc(path, lookup, home)
	if !ok {
		return "", false
	}
	if len(values) == 0 {
		return "", true
	}
	return values[len(values)-1], true
}

// readProjectNpmrc parses the cache values of the project .npmrc at path. An
// absent file is (nil, true): npm falls through to its user/global config. A
// file that exists but cannot be read safely — a final-component symlink, a
// device, an unreadable or oversized file — or whose path cannot be located as
// npm will is (nil, false): npm may still read a cache from it, so the caller
// must not treat it as absent.
func readProjectNpmrc(path string, lookup func(string) string, home string) ([]string, bool) {
	readable, absent := projectNpmrcFileState(path)
	if !readable {
		return nil, absent
	}
	f, err := os.OpenFile(path, os.O_RDONLY|openNonblock|openNoFollow, 0)
	if err != nil {
		return nil, os.IsNotExist(err)
	}
	defer func() { _ = f.Close() }()
	data, err := readGoEnvFile(f, path)
	if err != nil {
		return nil, false
	}
	return parseNpmrcCacheRoots(data, lookup, home)
}

// npmrcHostLookup expands a project .npmrc value from host facts, for
// resolution, which reads no process environment.
func npmrcHostLookup(host HostFacts) func(string) string {
	return func(name string) string {
		switch name {
		case envvars.Home.Name:
			return host.Home
		case envvars.XDGCacheHome.Name:
			return host.XDGCacheHome
		}
		return ""
	}
}

// readNpmrcCacheRootsEnv reads a user or global .npmrc for a spawn, expanding
// ${NAME} from the spawn's own environment and ~ from home. Unlike a project
// file it is host configuration, so symlinks are followed and the bounded
// non-blocking reader still refuses a FIFO, device, or oversized file. ok is
// false when the effective cache cannot be resolved safely: a refusal, a
// nonempty relative filename (npm resolves it against its own working
// directory), an unresolvable ~, or a file the spawn cannot see (a mask, or
// bwrap's private /tmp and /dev). npm may still read a cache from the file, so
// it is unknown, not absent.
func readNpmrcCacheRootsEnv(path string, lookup func(string) string, home string, policy ResolvedPolicy) ([]string, bool) {
	resolved, readable, absent := npmConfigFilePath(path, lookup, home)
	if !readable {
		return nil, absent
	}
	if npmConfigFileHidden(policy, resolved) {
		return nil, false
	}
	f, err := openGoEnvFile(resolved)
	if err != nil {
		return nil, os.IsNotExist(err)
	}
	defer func() { _ = f.Close() }()
	data, err := readGoEnvFile(f, resolved)
	if err != nil {
		return nil, false
	}
	return parseNpmrcCacheRoots(data, lookup, home)
}

// npmConfigFilePath resolves a configured npm config filename the way npm
// does: ${NAME} from lookup and a leading ~ from home. Empty is absent; a
// nonempty path that remains relative after expansion, or a ~ it cannot
// resolve, is unknown.
func npmConfigFilePath(path string, lookup func(string) string, home string) (resolved string, readable, absent bool) {
	if path == "" {
		return "", false, true
	}
	path = expandNpmrcVars(path, lookup)
	path, ok := expandNpmCacheHome(path, home)
	if !ok || !filepath.IsAbs(path) {
		return "", false, false
	}
	return path, true, false
}

// projectNpmrcFileState classifies the project config path, which the floor
// constructs from the working directory rather than reading from npm's
// configuration. Empty is absent; a nonempty relative name is unknown.
func projectNpmrcFileState(path string) (readable, absent bool) {
	switch {
	case path == "":
		return false, true
	case !filepath.IsAbs(path):
		return false, false
	default:
		return true, false
	}
}

// npmrcEnvLookup expands npm's ${NAME} references from the probe's environment.
func npmrcEnvLookup(environ []string) func(string) string {
	return func(name string) string { return envValueFold(environ, name) }
}

// parseNpmrcCacheRoots returns the absolute values of `cache=` lines in an npm
// config file, with npm's ${NAME} references expanded by lookup and a leading ~
// expanded from home (falling back to lookup's HOME). npm's file format is
// INI-like: blank lines, `#`/`;` comments and `[section]` headers carry no
// setting, and a quoted value is unquoted. Only an absolute value names a
// stable directory; a relative one resolves against npm's own working
// directory, which is not the overlay's. ok is false when the last value is
// relative or a ~ value cannot be resolved to an absolute home: npm may still
// write the cache it names, so the effective cache is unknown.
func parseNpmrcCacheRoots(data []byte, lookup func(string) string, home string) ([]string, bool) {
	var roots []string
	known := true
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "[") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "cache") {
			continue
		}
		value = expandNpmrcVars(stripNpmrcComment(value), lookup)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		// npm expands a leading ~ from its own home; a value it would resolve to
		// an absolute cache must be judged there, not dropped as relative. Only
		// the last value in the file is effective, so knownness rides that line.
		tildeHome := home
		if tildeHome == "" {
			tildeHome = lookup(envvars.Home.Name)
		}
		expanded, resolvable := expandNpmCacheHome(value, tildeHome)
		if !resolvable {
			known = false
			continue
		}
		value = expanded
		known = filepath.IsAbs(value)
		if known {
			roots = append(roots, value)
		}
	}
	return roots, known
}

// expandNpmCacheHome expands npm's leading ~ in a cache value from home. ok is
// false when the value is ~-rooted and home is not absolute: npm resolves it
// from a home the floor cannot locate, so the effective cache is unknown.
func expandNpmCacheHome(value, home string) (string, bool) {
	if value != "~" && !strings.HasPrefix(value, "~/") {
		return value, true
	}
	if !filepath.IsAbs(home) {
		return "", false
	}
	if value == "~" {
		return home, true
	}
	return filepath.Join(home, strings.TrimPrefix(value, "~/")), true
}

// stripNpmrcComment removes npm's INI inline comment from value: an unquoted
// '#' or ';' at the start or after whitespace ends the value. A quoted value
// runs to its closing quote, so a '#' inside it is data.
func stripNpmrcComment(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return value
	}
	if quote := value[0]; quote == '"' || quote == '\'' {
		for i := 1; i < len(value); i++ {
			if value[i] == '\\' {
				i++
				continue
			}
			if value[i] == quote {
				return value[:i+1]
			}
		}
		return value
	}
	for i := 0; i < len(value); i++ {
		if (value[i] == '#' || value[i] == ';') && (i == 0 || value[i-1] == ' ' || value[i-1] == '\t') {
			return strings.TrimSpace(value[:i])
		}
	}
	return value
}

// expandNpmrcVars replaces npm's ${NAME} references with lookup(NAME). An
// unknown name expands to nothing, so the value is then relative or empty and
// contributes no root.
func expandNpmrcVars(value string, lookup func(string) string) string {
	if !strings.Contains(value, "${") {
		return value
	}
	var out strings.Builder
	for {
		start := strings.Index(value, "${")
		if start < 0 {
			out.WriteString(value)
			return out.String()
		}
		out.WriteString(value[:start])
		rest := value[start+2:]
		name, after, ok := strings.Cut(rest, "}")
		if !ok {
			out.WriteString(value[start:])
			return out.String()
		}
		out.WriteString(lookup(name))
		value = after
	}
}

// probeGitGlobalConfigPaths returns the user's global git config files that
// exist on this host, in git's own precedence order (git-config(1), FILES):
// $XDG_CONFIG_HOME/git/config first, then ~/.gitconfig. git reads BOTH when both
// exist, so both are probed rather than the first hit.
//
// A $GIT_CONFIG_GLOBAL that is SET replaces both FILES, matching git's own
// precedence. Only an absolute path to an existing non-directory file is
// admitted; an empty value, a relative path, or a path that names nothing git
// can read names no config git reads (git resolves a relative value against the
// process's working directory and ignores an unreadable global file), so
// granting the default files instead would over-grant files git never opens.
//
// $XDG_CONFIG_HOME defaults to the home directory's .config when unset or empty. A NON-ABSOLUTE
// XDG_CONFIG_HOME is ignored entirely: git would resolve it against the process's
// working directory, which is not a stable file to grant, and guessing would emit
// a root for a file git never reads.
//
// A candidate is admitted only when it names an existing non-DIRECTORY file (the
// stat follows symlinks, so a dotfile-managed config that is a link to a real
// file counts and a broken link does not) — a host with no global config
// contributes nothing and still starts a session.
//
// The paths are kept in the LITERAL spelling git uses, not symlink-resolved: the
// backends grant a root under both its literal and its canonical name, and the
// literal one is the name git actually opens.
func probeGitGlobalConfigPaths(system probeSystem) []string {
	if override, set := environValue(system.environ(), gitConfigGlobalEnv); set {
		return gitConfigFileOverride(override, system)
	}
	home, err := system.userHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return nil
	}
	var candidates []string
	xdg := strings.TrimSpace(system.getenv(envvars.XDGConfigHome.Name))
	if xdg == "" {
		xdg = filepath.Join(home, ".config")
	}
	if filepath.IsAbs(xdg) {
		candidates = append(candidates, filepath.Join(xdg, "git", "config"))
	}
	candidates = append(candidates, filepath.Join(home, ".gitconfig"))

	var out []string
	for _, c := range candidates {
		if system.nonDirectoryFile(c) && !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	return out
}

// probeGitSystemConfigPaths returns the system git config file git reads when
// $GIT_CONFIG_SYSTEM overrides the built-in /etc/gitconfig location. The
// built-in location already lies under /etc, a restricted-mode system read
// root, so only an override needs a grant. $GIT_CONFIG_NOSYSTEM disables the
// system config entirely; see gitConfigNoSystem for its spellings.
func probeGitSystemConfigPaths(system probeSystem) []string {
	files := gitConfigFileOverride(system.getenv(gitConfigSystemEnv), system)
	if len(files) == 0 || gitConfigNoSystem(system.getenv(gitConfigNoSystemEnv)) {
		return nil
	}
	return files
}

// gitConfigFileOverride returns the one config file an environment override
// names, or nil when the value names nothing git reads as a config file. The
// file admitted is the file granted: the value must be absolute, free of ".."
// components (a ".." after a symlink resolves to a different object than
// filepath.Clean reports, and the cleaned spelling is the grant), and name an
// existing non-directory file.
func gitConfigFileOverride(value string, system probeSystem) []string {
	if value == "" {
		return nil
	}
	cleaned := filepath.Clean(value)
	if !filepath.IsAbs(cleaned) || hasParentComponent(value) || !system.nonDirectoryFile(cleaned) {
		return nil
	}
	return []string{cleaned}
}

// hasParentComponent reports whether p has a ".." path component.
func hasParentComponent(p string) bool {
	for part := range strings.SplitSeq(filepath.ToSlash(p), "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

// environValue returns the last value assigned to name and whether it is present
// at all, so an explicitly empty variable is distinguishable from an unset one.
func environValue(environ []string, name string) (string, bool) {
	value, set := "", false
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok && k == name {
			value, set = v, true
		}
	}
	return value, set
}

// gitConfigNoSystem reports whether $GIT_CONFIG_NOSYSTEM tells git to skip the
// system config. It follows git's boolean spelling: true/yes/on in any case and
// a base-prefixed integer (0 and the empty value are false). A value git
// rejects as a boolean is treated as true here because git refuses to run and
// never reads the system file, so a grant for it would be dead weight.
func gitConfigNoSystem(v string) bool {
	switch strings.ToLower(v) {
	case "true", "yes", "on":
		return true
	case "false", "no", "off", "":
		return false
	}
	if n, err := strconv.ParseInt(strings.TrimLeft(v, " \t\r\n"), 0, 64); err == nil {
		return n != 0
	}
	return true
}

// evenerContentRoots returns the plugin store's installed plugin files and the
// user skills directory under Evener's config root, or nothing when the root is
// unresolved. The store's metadata, which records marketplace URLs that may
// carry tokens, is left out.
//
// noUserSkills (EVENER_NO_USER_SKILLS=1) leaves the user skills directory out,
// since such a session loads none. Installed plugins stay: their hooks still
// run, and the variable is the eval harness's way to keep the skill catalog
// independent of the operator, not an access boundary (envvars marks it
// Tooling); a readable plugin file adds nothing to the catalog.
func evenerContentRoots(root string, noUserSkills bool) []string {
	if root == "" {
		return nil
	}
	roots := userdirs.PluginContentDirs(userdirs.PluginStore(root))
	if !noUserSkills {
		roots = append(roots, userdirs.UserSkills(root))
	}
	return roots
}

// managedContentStoreFormat is the store-level completion assertion written by
// the plugin store. Its file name and version are part of the sandbox contract
// so a policy can retire the managed walk without importing the store package.
const (
	storeFormatFileName  = "store-format.json"
	storeFormatVersion   = 1
	storeFormatCleanWord = "clean"
)

func managedContentCleanAt(system probeSystem, storeRoot string) bool {
	if storeRoot == "" {
		return false
	}
	data, err := system.readFile(filepath.Join(storeRoot, storeFormatFileName))
	if err != nil {
		return false
	}
	var state struct {
		Version        int    `json:"version"`
		ManagedContent string `json:"managed_content"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return false
	}
	return state.Version == storeFormatVersion && state.ManagedContent == storeFormatCleanWord
}

// goEnvValue returns a go command setting as configured on this host, with the
// go command's own precedence: the environment, then the user's go env file,
// which is what `go env -w` writes. $GOENV names that file, GOENV=off disables
// it, and otherwise it is <user config dir>/go/env; like the go command, a later
// line overrides an earlier one. It returns "" for an unset setting.
//
// The line handling deliberately matches cmd/go/internal/cfg.readEnvFile: a
// line is split at its first "=" with no trimming, quote or "\r" stripping, and
// comment or blank lines never match a name. Normalizing more than go does
// would make the sandbox disagree with the go command it serves.
//
// It reads the file rather than running `go env`: host facts feed Resolve, so a
// `go env` here would run before any sandbox exists, executing whichever go is
// first on PATH, and any toolchain it switches to and downloads, unconfined.
func goEnvValue(system probeSystem, name string) string {
	if value := system.getenv(name); value != "" {
		return value
	}
	return goEnvFileSettings(system)[name]
}

// goEnvFileSettings returns the GOPATH, GOMODCACHE and GOCACHE lines of the go
// env file alone, parsed as goEnvValue describes (nil when there is no file).
func goEnvFileSettings(system probeSystem) map[string]string {
	file := goEnvFile(system)
	if file == "" {
		return nil
	}
	settings, err := readGoEnvFileSettings(system, file)
	if err != nil {
		return nil
	}
	return settings
}

// readGoEnvFileSettings reads the GOPATH, GOMODCACHE and GOCACHE lines of the go
// env file at path, through the probe's bounded, non-blocking read.
func readGoEnvFileSettings(system probeSystem, path string) (map[string]string, error) {
	data, err := system.readFile(path)
	if err != nil {
		return nil, err
	}
	return parseGoEnvSettings(data), nil
}

// parseGoEnvSettings returns the Go cache settings a go env file's contents
// set.
func parseGoEnvSettings(data []byte) map[string]string {
	settings := map[string]string{}
	for line := range strings.SplitSeq(string(data), "\n") {
		key, val, ok := strings.Cut(line, "=")
		if ok && (key == envvars.GoPath.Name || key == envvars.GoModCache.Name || key == envvars.GoCache.Name) {
			settings[key] = val
		}
	}
	return settings
}

// goEnvFile returns the go env file the go command reads: $GOENV, or
// <user config dir>/go/env, or "" when GOENV=off or the directory is unknown.
func goEnvFile(system probeSystem) string {
	file := system.getenv(envvars.GoEnv.Name)
	switch file {
	case "off":
		return ""
	case "":
		dir, err := system.userConfigDir()
		if err != nil || dir == "" {
			return ""
		}
		file = filepath.Join(dir, "go", "env")
	}
	return file
}

// goPathEntries returns the GOPATH entries the go command uses on this host: the
// absolute entries of the configured GOPATH (it refuses relative ones), else
// Go's $HOME/go default, or none without an absolute home.
func goPathEntries(host HostFacts) []string {
	var entries []string
	for _, entry := range filepath.SplitList(host.GoPath) {
		if filepath.IsAbs(entry) {
			entries = append(entries, entry)
		}
	}
	if len(entries) == 0 && filepath.IsAbs(host.Home) {
		entries = []string{filepath.Join(host.Home, "go")}
	}
	return entries
}

// probeEvenerConfigRoot returns Evener's config root as Evener resolves it:
// $XDG_CONFIG_HOME/evener, else ~/.config/evener. A relative XDG_CONFIG_HOME is
// resolved against the working directory, as Evener's own process does, so the
// mask covers the directory it really uses. "" when it cannot be resolved.
func probeEvenerConfigRoot(system probeSystem) string {
	root := userdirs.ConfigRoot(system.getenv(envvars.XDGConfigHome.Name), system.userHomeDir)
	if root == "" || filepath.IsAbs(root) {
		return root
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return ""
	}
	return abs
}

// commandLineToolsRoot is the fixed location the standalone Xcode Command Line
// Tools install to. It is probed in ADDITION to the active developer directory:
// a Mac can have both, `xcode-select -p` names only one, and the shims fall back
// to the other.
const commandLineToolsRoot = "/Library/Developer/CommandLineTools"

// xcodeSelectPath is the absolute path of the tool that reports the active
// developer directory. It is spelled absolutely on purpose — resolving it
// through PATH would let a hijacked PATH entry choose which directory the
// sandbox grants.
const xcodeSelectPath = "/usr/bin/xcode-select"

// probeDeveloperToolRoots returns the developer-toolchain directories present on
// this Mac: the active developer directory plus the standalone Command Line
// Tools root. Each is admitted only if it resolves to an existing DIRECTORY, so
// a host with neither installed (or with xcode-select unconfigured) contributes
// nothing and still starts a session.
func probeDeveloperToolRoots(system probeSystem) []string {
	var out []string
	add := func(p string) {
		if d := CanonicalDir(p); d != "" && !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	add(enclosingAppBundle(activeDeveloperDir(system)))
	add(commandLineToolsRoot)
	return out
}

// probeDeveloperToolBinDir returns the active developer directory's bin
// directory — where the real tools the /usr/bin shims exec actually live — or ""
// when no toolchain is active or the directory is absent. It deliberately does
// NOT widen to the enclosing app bundle the way the read grant does: this value
// goes on PATH, and only the directory holding the executables belongs there.
func probeDeveloperToolBinDir(system probeSystem) string {
	dev := activeDeveloperDir(system)
	if dev == "" {
		return ""
	}
	return CanonicalDir(filepath.Join(dev, "usr", "bin"))
}

// enclosingAppBundle widens a developer directory inside an application bundle
// to the BUNDLE root, and returns any other path unchanged.
//
// The active developer directory of an Xcode install is
// /Applications/Xcode.app/Contents/Developer, but its tools reach outside that
// subtree into sibling bundle directories: xcodebuild dyld-loads
// Contents/SharedFrameworks/*.framework and xcrun stats Contents/Info.plist.
// Granted the developer directory alone, `git --version` still dies with
// "Library not loaded: @rpath/DVTSystemPrerequisites.framework" and "couldn't
// stat Xcode's Info.plist". The bundle is the unit the toolchain is installed
// and versioned as, and is what the 2026-08-06 ruling names.
func enclosingAppBundle(dir string) string {
	for p := filepath.Clean(dir); p != "/" && p != "." && p != ""; p = filepath.Dir(p) {
		if strings.HasSuffix(p, ".app") {
			return p
		}
	}
	return dir
}

// activeDeveloperDir returns `xcode-select -p`, or "" when the tool is missing,
// fails, or reports nothing (no toolchain selected).
func activeDeveloperDir(system probeSystem) string {
	ctx, cancel := context.WithTimeout(context.Background(), probeCommandTimeout)
	defer cancel()
	out, err := system.output(ctx, xcodeSelectPath, "-p")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(trimTrailingNewline(out)))
}

// bwrapUsernsWorks reports whether the resolved bwrap binary can actually create
// the namespaces our confinement needs on THIS host. Presence + `--version` is
// not enough: a host with apparmor_restrict_unprivileged_userns=1 (Ubuntu 24.04)
// ships a runnable bwrap that cannot create an unprivileged user namespace, so it
// would `--version` fine yet fail every real sandbox. This runs a trivial fully
// namespaced sandbox (`true`) and reports success only if it exits 0 — the exact
// capability the kernel wrapper relies on. Fail-closed: any error is "not
// capable" so the floor refuses rather than half-enforcing.
func bwrapUsernsWorks(system probeSystem, path string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), probeCommandTimeout)
	defer cancel()
	args := bwrapProbeArgs(path)
	return system.run(ctx, args[0], args[1:]...) == nil
}

// bwrapProbeArgs is the full argv (binary included) the capability probe runs. It
// mirrors the exact flag set Wrap emits — including the version-gated --new-session
// (hardening base) and --argv0 (bwrap 0.9.0+) — so a host whose bwrap predates
// those flags fails the probe here rather than passing it and then aborting every
// real spawn with "unknown option". --argv0 takes an argument, placed (like Wrap)
// immediately before the "--" that precedes the real command.
func bwrapProbeArgs(path string) []string {
	return []string{
		path,
		"--unshare-user", "--unshare-pid", "--die-with-parent", "--new-session",
		"--ro-bind", "/", "/", "--proc", "/proc", "--dev", "/dev",
		"--argv0", "true",
		"--", "true",
	}
}

// bwrapSupportsOverlay reports whether this bwrap build was compiled with overlay
// support (`--overlay-src`/`--tmp-overlay`). Only affects cache strategy: when
// false, cache roots degrade to the session-private redirect (never persistent-
// writable). bubblewrap 0.9.0 built without overlay omits the option from --help.
func bwrapSupportsOverlay(system probeSystem, path string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), probeCommandTimeout)
	defer cancel()
	out, err := system.combinedOutput(ctx, path, "--help")
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "--overlay-src")
}

// bwrapOverlayLowerWorks reports whether bwrap can mount an unprivileged overlay
// whose lower is root on THIS host, by running a trivial namespaced sandbox with
// the exact flag pair buildBwrapArgv emits and reporting success only if it exits
// 0. The lower's filesystem, not the bwrap build, decides: an unprivileged
// overlay over a lower on an overlayfs root (the reported container case) or a
// network mount fails EINVAL. Fail-closed: any error is "cannot overlay", so the
// root degrades rather than aborting every spawn.
func bwrapOverlayLowerWorks(system probeSystem, path, root string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), probeCommandTimeout)
	defer cancel()
	args := bwrapOverlayProbeArgs(path, root)
	return system.run(ctx, args[0], args[1:]...) == nil
}

// memoizedOverlayLower returns bwrapOverlayLowerWorks, remembering each root's
// verdict so the probe runs once per root per session. A cache root can be
// retargeted under a session (a symlinked parent moved onto another filesystem),
// so a verdict is keyed by the root's RESOLVED location, not its spelling: a
// retarget is a new key and is probed again.
//
// A root that is not a directory is never remembered: it is not overlaid now,
// and remembering that would keep it unserved for good once its storage appears
// (its caller judges it afresh, servableCacheRoot).
//
// Accepted residual, and inherent to the issue's once-per-session shape: a
// DIFFERENT filesystem mounted over the same path (not through a link) keeps the
// key, so its verdict stands for the session. A user who remounts a cache root's
// storage mid-session gets the next session's judgment.
//
// The probe is asked from more than one spawn site, so the memo is
// mutex-guarded; the lock is released while the subprocess runs, so concurrent
// spawns do not queue behind it (a root probed twice concurrently stores the
// same answer). The closure is shared by every copy of the HostFacts it was
// built into.
func memoizedOverlayLower(system probeSystem, path string) func(root string) bool {
	var mu sync.Mutex
	seen := map[string]bool{}
	return func(root string) bool {
		if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
			return bwrapOverlayLowerWorks(system, path, root)
		}
		key := resolveExistingPrefix(root)
		mu.Lock()
		over, found := seen[key]
		mu.Unlock()
		if found {
			return over
		}
		over = bwrapOverlayLowerWorks(system, path, root)
		mu.Lock()
		seen[key] = over
		mu.Unlock()
		return over
	}
}

// bwrapOverlayProbeArgs is the argv the per-root overlay probe runs: the same
// namespaced base as the capability probe (bwrapProbeArgs — --unshare-user, a
// --ro-bind / / that makes the mount target exist, --proc, --dev, …), plus the
// overlay flag pair buildBwrapArgv emits, placed immediately before the "--".
func bwrapOverlayProbeArgs(path, root string) []string {
	args := bwrapProbeArgs(path)
	return slices.Insert(args, slices.Index(args, "--"), overlayFlags(root)...)
}

// probeKernelVersion returns `uname -r` best-effort, or "" if unavailable.
func probeKernelVersion(system probeSystem) string {
	ctx, cancel := context.WithTimeout(context.Background(), probeCommandTimeout)
	defer cancel()
	out, err := system.output(ctx, "uname", "-r")
	if err != nil {
		return ""
	}
	return string(trimTrailingNewline(out))
}

func trimTrailingNewline(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}
