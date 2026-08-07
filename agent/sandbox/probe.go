package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"primeradiant.com/serf/envvars"
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
	// so the grant cannot widen into a home read. It changes no write surface — git
	// config and hook writes stay denied — and loses to the credential denylist,
	// so a credential.helper line becomes readable while ~/.git-credentials stays
	// masked. A path that does not exist contributes nothing and never fails
	// session start.
	GitGlobalConfigPaths []string

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
	userHomeDir() (string, error)
	lookPath(string) (string, error)
	nonDirectoryFile(string) bool
	run(context.Context, string, ...string) error
	combinedOutput(context.Context, string, ...string) ([]byte, error)
	output(context.Context, string, ...string) ([]byte, error)
}

type hostProbeSystem struct{}

var probeUserHomeDir = os.UserHomeDir

func (hostProbeSystem) goos() string { return runtime.GOOS }

func (hostProbeSystem) getenv(name string) string { return os.Getenv(name) }

func (hostProbeSystem) userHomeDir() (string, error) { return probeUserHomeDir() }

func (hostProbeSystem) lookPath(name string) (string, error) { return exec.LookPath(name) }

func (hostProbeSystem) nonDirectoryFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
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
	facts.GitGlobalConfigPaths = probeGitGlobalConfigPaths(system)

	if path, err := system.lookPath("bwrap"); err == nil {
		facts.BwrapPath = path
		facts.BwrapCapable = bwrapUsernsWorks(system, path)
		facts.OverlaySupported = bwrapSupportsOverlay(system, path)
	}

	if facts.OS == "darwin" {
		const seatbelt = "/usr/bin/sandbox-exec"
		if system.nonDirectoryFile(seatbelt) {
			facts.SandboxExecPath = seatbelt
		}
		facts.DeveloperToolRoots = probeDeveloperToolRoots(system)
	}

	return facts
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
