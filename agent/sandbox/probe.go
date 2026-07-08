package sandbox

import (
	"context"
	"os"
	"os/exec"
	"runtime"
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

	// BwrapPath is the resolved bubblewrap binary path, or "" if not found.
	BwrapPath string

	// BwrapCapable reports that bwrap is present AND can actually create the
	// namespaces it needs (unprivileged user namespaces work). bwrap serves the
	// full mode matrix and net=off, so this being true is what lets every mode run.
	//
	// M1 caveat: RealProber currently sets this from a bwrap-presence + `--version`
	// check only — it does NOT yet confirm unprivileged userns actually works (a
	// host with apparmor_restrict_unprivileged_userns would still `--version` fine).
	// M3 upgrades RealProber to a real userns-execution probe before any enforcement
	// path consumes this field. Until then, treat a true value as "bwrap present",
	// not "userns proven". Tests set it directly via FakeProber.
	BwrapCapable bool

	// OverlaySupported reports that bwrap can mount a read-real/write-private
	// overlay (bwrap ≥ 0.5, kernel overlay support). Only affects cache strategy:
	// when false, cache roots degrade to session-private redirect (never
	// persistent-writable).
	OverlaySupported bool

	// LandlockABI is the Landlock LSM ABI version available (0 = unavailable).
	// Landlock is the allowlist-only fallback that serves exactly restricted in a
	// linked worktree with net=on.
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

	if path, err := exec.LookPath("bwrap"); err == nil {
		facts.BwrapPath = path
		facts.BwrapCapable = bwrapExecutable(path)
	}

	if runtime.GOOS == "darwin" {
		const seatbelt = "/usr/bin/sandbox-exec"
		if st, err := os.Stat(seatbelt); err == nil && !st.IsDir() {
			facts.SandboxExecPath = seatbelt
		}
	}

	return facts
}

// bwrapExecutable reports whether the resolved bwrap binary runs at all
// (`bwrap --version` exits 0). This is a presence-and-executability check, not a
// namespace-capability check — M3 replaces it with a trivial sandboxed execution
// that proves unprivileged user namespaces actually work on this host.
func bwrapExecutable(path string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), probeCommandTimeout)
	defer cancel()
	return exec.CommandContext(ctx, path, "--version").Run() == nil
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
