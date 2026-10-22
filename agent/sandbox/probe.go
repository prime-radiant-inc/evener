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
	// session start.
	GitGlobalConfigPaths []string

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
	// move npm's and cargo's cache directories; empty when unset. They are read
	// from the environment alone: cargo keeps no file for CARGO_HOME, and npm's
	// .npmrc `cache=` is not consulted (a cache moved only there stays unserved).
	NpmConfigCache string
	CargoHome      string

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

// probeGitGlobalConfigPaths returns the user's global git config files that
// exist on this host, in git's own precedence order (git-config(1), FILES):
// $XDG_CONFIG_HOME/git/config first, then ~/.gitconfig. git reads BOTH when both
// exist, so both are probed rather than the first hit.
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
