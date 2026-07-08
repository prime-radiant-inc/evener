package sandbox

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// HostFacts are the backend-relevant capabilities of the host, gathered once at
// session start. They are plain data so the resolver stays a pure function of
// (policy, facts, cwd): the real prober fills them by probing the kernel/binaries;
// unit tests inject a FakeProber and never touch the host. The four fields that
// matter to the fail-closed floor are OS, BwrapCapable, LandlockABI, and (macOS)
// SandboxExecPath.
type HostFacts struct {
	// OS is runtime.GOOS: "linux", "darwin", "windows", …. Sandboxing is only
	// expressible on linux (bwrap/Landlock) and darwin (Seatbelt); every other
	// OS refuses any sandboxed mode.
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

	// LandlockABI is the Landlock LSM ABI version available (0 = unavailable).
	// Landlock is allowlist-only: it is probed and reported here but the resolver
	// never SELECTS it (it cannot subtract a path within a granted root, so it
	// cannot enforce our contract in any mode). bwrap is required on Linux; a
	// Landlock-only host gets only --sandbox off. See resolve.go's chooseBackend.
	LandlockABI int

	// SandboxExecPath is the resolved /usr/bin/sandbox-exec path on darwin
	// (Seatbelt), or "" if unavailable. Seatbelt is deny-capable and serves the
	// full mode matrix (cache always session-private — no overlay on macOS).
	SandboxExecPath string

	// KernelVersion is the best-effort `uname -r` string, informational only
	// (surfaced in the startup enforcement line, not used for decisions).
	KernelVersion string
}

// LandlockAvailable reports whether the Landlock LSM is usable on this host.
func (h HostFacts) LandlockAvailable() bool { return h.LandlockABI > 0 }

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
// path, so no unit test ever shells out to bwrap or issues a landlock syscall.
type FakeProber struct {
	Facts HostFacts
}

// Probe returns the canned facts.
func (f FakeProber) Probe() HostFacts { return f.Facts }

// RealProber probes the live host. It is used only behind explicit opt-in (the
// gated TestRealProberOptIn / eventual production wiring in M3+): the unit path
// never constructs it, so unit runs stay hermetic.
type RealProber struct{}

// probeCommandTimeout bounds each capability-probe subprocess.
var probeCommandTimeout = 3 * time.Second

// Probe gathers host capabilities. The bwrap capability and overlay probes are
// intentionally conservative here (presence + version); M3 hardens BwrapCapable
// into a real unprivileged-userns execution probe and adds true overlay
// detection. The Landlock ABI probe is exact (a direct landlock_create_ruleset
// version query, no side effects) and the Seatbelt/OS facts are exact.
func (RealProber) Probe() HostFacts {
	facts := HostFacts{
		OS:            runtime.GOOS,
		LandlockABI:   probeLandlockABI(),
		KernelVersion: probeKernelVersion(),
	}
	if home, err := os.UserHomeDir(); err == nil {
		facts.Home = home
	}

	if path, err := exec.LookPath("bwrap"); err == nil {
		facts.BwrapPath = path
		facts.BwrapCapable = bwrapUsernsWorks(path)
		facts.OverlaySupported = bwrapSupportsOverlay(path)
	}

	if runtime.GOOS == "darwin" {
		const seatbelt = "/usr/bin/sandbox-exec"
		if st, err := os.Stat(seatbelt); err == nil && !st.IsDir() {
			facts.SandboxExecPath = seatbelt
		}
	}

	return facts
}

// bwrapUsernsWorks reports whether the resolved bwrap binary can actually create
// the namespaces our confinement needs on THIS host. Presence + `--version` is
// not enough: a host with apparmor_restrict_unprivileged_userns=1 (Ubuntu 24.04)
// ships a runnable bwrap that cannot create an unprivileged user namespace, so it
// would `--version` fine yet fail every real sandbox. This runs a trivial fully
// namespaced sandbox (`true`) and reports success only if it exits 0 — the exact
// capability the kernel wrapper relies on. Fail-closed: any error is "not
// capable" so the floor refuses rather than half-enforcing.
func bwrapUsernsWorks(path string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), probeCommandTimeout)
	defer cancel()
	args := bwrapProbeArgs(path)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	return cmd.Run() == nil
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
func bwrapSupportsOverlay(path string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), probeCommandTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--help").CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "--overlay-src")
}

// probeKernelVersion returns `uname -r` best-effort, or "" if unavailable.
func probeKernelVersion() string {
	ctx, cancel := context.WithTimeout(context.Background(), probeCommandTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "uname", "-r").Output()
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
