package sandbox

import (
	"fmt"
	"path/filepath"
	"slices"
)

// Backend names the concrete enforcement mechanism the resolver chose for a
// session on this host. It is recorded on the ResolvedPolicy so M3/M6 know which
// backend to drive; M1 selects it but nothing enforces.
type Backend int

const (
	// BackendNone: off mode — no backend, no containment.
	BackendNone Backend = iota
	// BackendBwrap: Linux bubblewrap. Serves the full mode matrix and net=off.
	BackendBwrap
	// BackendLandlock: Linux Landlock (allowlist-only). Serves exactly restricted
	// in a linked worktree with net=on.
	BackendLandlock
	// BackendSeatbelt: macOS sandbox-exec. Deny-capable — serves the full mode
	// matrix; cache is always session-private (no overlay on macOS).
	BackendSeatbelt
)

// String returns the backend's name.
func (b Backend) String() string {
	switch b {
	case BackendNone:
		return "none"
	case BackendBwrap:
		return "bwrap"
	case BackendLandlock:
		return "landlock"
	case BackendSeatbelt:
		return "seatbelt"
	default:
		return fmt.Sprintf("Backend(%d)", int(b))
	}
}

// CacheStrategy is how cache roots (~/.cache, ~/go/pkg, ~/.npm, ~/.cargo, …) are
// served so a sandboxed session can never poison a cache a later build consumes.
type CacheStrategy int

const (
	// CacheNone: no cache write strategy needed (off and read-only never write).
	CacheNone CacheStrategy = iota
	// CacheOverlay: read-real lower + private-upper overlay (warm reads, writes
	// discarded at session end). workspace-write on a host with overlay support.
	CacheOverlay
	// CacheSessionPrivate: redirect GOCACHE/npm_config_cache/CARGO_HOME to session
	// tmp (cold, never persistent-writable). The security floor when overlay is
	// unavailable, and always for restricted and Seatbelt.
	CacheSessionPrivate
)

// String returns the cache strategy's name.
func (c CacheStrategy) String() string {
	switch c {
	case CacheNone:
		return "none"
	case CacheOverlay:
		return "overlay"
	case CacheSessionPrivate:
		return "session-private"
	default:
		return fmt.Sprintf("CacheStrategy(%d)", int(c))
	}
}

// ReadScope distinguishes the two read models a layer can have.
type ReadScope int

const (
	// ReadAnywhere: reads are allowed anywhere EXCEPT the masked paths.
	ReadAnywhere ReadScope = iota
	// ReadWorktreeOnly: reads are allowed only within the layer's ReadRoots.
	ReadWorktreeOnly
)

// String returns the read scope's name.
func (r ReadScope) String() string {
	switch r {
	case ReadAnywhere:
		return "anywhere"
	case ReadWorktreeOnly:
		return "roots-only"
	default:
		return fmt.Sprintf("ReadScope(%d)", int(r))
	}
}

// AccessScope is one enforcement layer's filesystem grants. The two layers of a
// ResolvedPolicy differ deliberately: in restricted mode the FILE-TOOL layer may
// only browse the worktree, while the SPAWNED-process layer additionally reads
// system roots a process needs to run (tools = what the model may browse; kernel
// = what a process needs to execute).
type AccessScope struct {
	// Read is the read model. ReadAnywhere = anywhere minus MaskedPaths;
	// ReadWorktreeOnly = only within ReadRoots.
	Read ReadScope
	// ReadRoots are the absolute roots readable when Read == ReadWorktreeOnly
	// (the worktree, plus system read roots for the restricted spawned layer).
	ReadRoots []string
	// WriteRoots are the absolute roots a write may land beneath. Empty means no
	// writes are granted (session tmp, tracked separately, is the only scratch).
	WriteRoots []string
}

// ResolvedPolicy is the fully-resolved, backend-independent enforcement contract
// for a session: the exact grants (per-layer read/write roots), denials
// (MaskedPaths, git ProtectedPaths), network decision, cache strategy, and the
// chosen Backend that every backend (bwrap, Landlock, Seatbelt) must satisfy.
// It is produced by Resolve, immutable thereafter, and carried inert on the
// execution environment until a backend consumes it (M2/M3/M6). A nil
// *ResolvedPolicy (or Mode == ModeOff) is exactly today's behavior.
type ResolvedPolicy struct {
	Mode          Mode
	Network       bool          // true = egress allowed (--sandbox-net on)
	Backend       Backend       // enforcing backend chosen for this host
	CacheStrategy CacheStrategy // how cache roots are served (never persistent-writable)
	SessionTmp    bool          // a per-session writable tmp (TMPDIR) is provisioned

	// FileTool is the in-process file-tool layer's grants (M2 satisfies).
	FileTool AccessScope
	// Spawned is the kernel-wrapped process layer's grants (M3/M6 satisfy).
	Spawned AccessScope

	// MaskedPaths are absolute paths denied in BOTH layers (the secrets +
	// pseudo-fs denylist). Enforcement treats each as a subtree prefix (masking
	// /proc masks /proc/<pid>/environ; masking ~/.ssh masks its whole tree).
	MaskedPaths []string

	// Git is the resolved git-surface map: writable metadata, write-protected
	// config/hook surfaces, and outside-worktree read grants. Zero for off.
	Git GitLayout
}

// Enforced reports whether this policy imposes any containment. False for off.
func (rp ResolvedPolicy) Enforced() bool { return rp.Mode != ModeOff }

// RefusalError is the typed fail-closed refusal returned when the host cannot
// enforce the requested (mode, network) — the floor's "full contract or refuse"
// rule. It is distinct from an ordinary error so the flag/session layer can
// present it as a start-time refusal.
type RefusalError struct {
	Mode Mode
	Net  bool
	// Reason is the human-legible explanation surfaced to the user.
	Reason string
	// RequiredBackend names the backend that WOULD satisfy the request
	// ("bwrap", "sandbox-exec"), or "" when no backend on this OS could.
	RequiredBackend string
}

// Error implements error.
func (e *RefusalError) Error() string {
	return fmt.Sprintf("cannot enforce --sandbox %s (network=%v): %s", e.Mode, e.Net, e.Reason)
}

// defaultSystemReadRoots are the read-only system roots a RESTRICTED session's
// spawned processes need to execute (a process needs its interpreter, libraries,
// and config to run). File tools do not get these. Excludes /proc (masked).
var defaultSystemReadRoots = []string{
	"/usr", "/bin", "/sbin", "/lib", "/lib64", "/etc", "/opt", "/nix/store",
}

// Resolve turns a policy request plus host facts and the session's cwd into a
// ResolvedPolicy, or a typed *RefusalError when the host cannot enforce the
// request (the fail-closed floor). It is a pure function of its inputs (reads no
// process environment): the credential denylist anchors on host.Home, and the
// git layout is resolved structurally from cwd's on-disk .git entries.
//
// Off short-circuits before any host check, so off resolves on every host
// (including Windows) — it is today's behavior with no containment.
func Resolve(policy SandboxPolicy, host HostFacts, cwd string) (ResolvedPolicy, error) {
	if policy.Mode == ModeOff {
		return ResolvedPolicy{Mode: ModeOff, Network: true, Backend: BackendNone}, nil
	}

	// A sandboxed session needs an absolute home to anchor the credential denylist.
	// Without one (e.g. the home-directory env var is unset in a bare service),
	// joining home-relative secrets yields RELATIVE paths the enforcement layers
	// never match — a silent unmask of ~/.ssh, ~/.aws, etc. Fail closed rather than
	// resolve a leaky policy.
	if !filepath.IsAbs(host.Home) {
		return ResolvedPolicy{}, &RefusalError{
			Mode:   policy.Mode,
			Net:    policy.Network,
			Reason: "cannot anchor the credential denylist: the session's home directory is not an absolute path; sandboxing without a resolvable home would silently unmask credential directories",
		}
	}

	layout, err := ClassifyWorkspace(cwd)
	if err != nil {
		return ResolvedPolicy{}, &RefusalError{
			Mode:   policy.Mode,
			Net:    policy.Network,
			Reason: fmt.Sprintf("could not resolve the git layout of %s: %v", cwd, err),
		}
	}

	backend, refusal := chooseBackend(policy, host, layout.Kind)
	if refusal != nil {
		return ResolvedPolicy{}, refusal
	}

	masked := policy.EffectiveDenylist(host.Home)
	worktree := layout.WorktreeRoot

	rp := ResolvedPolicy{
		Mode:          policy.Mode,
		Network:       policy.Network,
		Backend:       backend,
		CacheStrategy: cacheStrategyFor(policy.Mode, backend, host),
		SessionTmp:    true,
		MaskedPaths:   masked,
		Git:           layout,
	}
	rp.FileTool, rp.Spawned = scopesFor(policy, layout, worktree)

	// Fail-closed invariant: never grant a root that is at or under a masked path.
	rp.FileTool.ReadRoots = filterMasked(rp.FileTool.ReadRoots, masked)
	rp.FileTool.WriteRoots = filterMasked(rp.FileTool.WriteRoots, masked)
	rp.Spawned.ReadRoots = filterMasked(rp.Spawned.ReadRoots, masked)
	rp.Spawned.WriteRoots = filterMasked(rp.Spawned.WriteRoots, masked)
	return rp, nil
}

// chooseBackend applies the fail-closed floor: it returns the backend that will
// enforce this (mode, net) on this host, or a *RefusalError naming the backend
// that would.
func chooseBackend(policy SandboxPolicy, host HostFacts, kind WorkspaceKind) (Backend, *RefusalError) {
	switch host.OS {
	case "linux":
		switch {
		case host.BwrapCapable:
			return BackendBwrap, nil // full matrix + net on/off
		case host.LandlockAvailable():
			// Landlock is allowlist-only: it can grant a root but cannot SUBTRACT a
			// path within it. A linked worktree's in-worktree ".git" pointer file
			// sits inside the granted worktree root and must be write-denied, but
			// Landlock cannot carve it out — the model could repoint gitdir into the
			// writable worktree, plant hooks, and a later unsandboxed git fires them.
			// So Landlock cannot fully enforce our contract in ANY mode and always
			// refuses; a Landlock-only host gets only --sandbox off. (A future
			// milestone could revisit with a per-entry grant model.)
			return 0, &RefusalError{
				Mode: policy.Mode, Net: policy.Network, RequiredBackend: "bwrap",
				Reason: landlockRefusalReason(policy, kind),
			}
		default:
			return 0, &RefusalError{
				Mode: policy.Mode, Net: policy.Network, RequiredBackend: "",
				Reason: "no sandbox backend is available (neither bubblewrap nor Landlock); only --sandbox off is supported on this host",
			}
		}
	case "darwin":
		if host.SeatbeltAvailable() {
			return BackendSeatbelt, nil // deny-capable: full matrix + net on/off
		}
		return 0, &RefusalError{
			Mode: policy.Mode, Net: policy.Network, RequiredBackend: "sandbox-exec",
			Reason: "macOS sandboxing requires /usr/bin/sandbox-exec, which was not found",
		}
	default:
		return 0, &RefusalError{
			Mode: policy.Mode, Net: policy.Network, RequiredBackend: "",
			Reason: fmt.Sprintf("sandboxing is not supported on %s; only --sandbox off is available", host.OS),
		}
	}
}

// landlockRefusalReason explains precisely why the Landlock-only host cannot
// serve this request. Landlock is allowlist-only and can serve NO sandboxed mode
// (see chooseBackend): net=off needs UDP/DNS isolation, the subtractive modes
// need a denylist, and even restricted in a linked worktree needs the in-worktree
// .git pointer carved out of an allowlisted root — none of which Landlock can do.
func landlockRefusalReason(policy SandboxPolicy, kind WorkspaceKind) string {
	switch {
	case !policy.Network:
		return "network isolation (--sandbox-net off) requires bubblewrap; this host has only Landlock, which cannot isolate UDP/DNS"
	case policy.Mode != ModeRestricted:
		return fmt.Sprintf("--sandbox %s requires bubblewrap; this host has only Landlock, which is allowlist-only and cannot express a subtractive denylist", policy.Mode)
	case kind == LinkedWorktree:
		return "--sandbox restricted requires bubblewrap; Landlock is allowlist-only and cannot protect the in-worktree .git pointer inside an allowlisted worktree root (a future milestone could revisit with a per-entry grant model)"
	default: // restricted on a main checkout (or non-git cwd)
		return "--sandbox restricted requires bubblewrap here; this host has only Landlock, which cannot subtract a main checkout's .git config/hook surfaces from an allowlisted root"
	}
}

// cacheStrategyFor picks the cache strategy: workspace-write overlays only on a
// bwrap host that supports overlay, else session-private; restricted is always
// session-private; read-only/off need none.
func cacheStrategyFor(mode Mode, backend Backend, host HostFacts) CacheStrategy {
	switch mode {
	case ModeWorkspaceWrite:
		if backend == BackendBwrap && host.OverlaySupported {
			return CacheOverlay
		}
		return CacheSessionPrivate
	case ModeRestricted:
		return CacheSessionPrivate
	default: // read-only, off
		return CacheNone
	}
}

// scopesFor builds the file-tool and spawned-process access scopes for a mode.
func scopesFor(policy SandboxPolicy, layout GitLayout, worktree string) (fileTool, spawned AccessScope) {
	switch policy.Mode {
	case ModeReadOnly:
		// Reads anywhere (minus masked); no writes (session tmp is the only scratch).
		fileTool = AccessScope{Read: ReadAnywhere}
		spawned = AccessScope{Read: ReadAnywhere}

	case ModeWorkspaceWrite:
		writeFile := dedupeRoots(append([]string{worktree}, policy.ExtraWritableRoots...))
		// Spawned writes additionally reach the git metadata subset (objects/refs/
		// index/logs/packed-refs); config/hooks stay in Git.ProtectedPaths.
		writeSpawn := dedupeRoots(slices.Concat(writeFile, layout.WritablePaths))
		fileTool = AccessScope{Read: ReadAnywhere, WriteRoots: writeFile}
		spawned = AccessScope{Read: ReadAnywhere, WriteRoots: writeSpawn}

	case ModeRestricted:
		// File tools stay WORKTREE-ONLY (tools = what the model may browse). The
		// common-.git read grant is a SPAWNED need (the git subprocess must read
		// common config), so it belongs to the spawned layer only — not the file
		// tools, which would otherwise let the model browse the whole main .git.
		readFile := dedupeRoots(append([]string{worktree}, policy.ExtraReadRoots...))
		writeFile := dedupeRoots(append([]string{worktree}, policy.ExtraWritableRoots...))
		// Spawned procs additionally read the common git dir + system roots (to
		// execute) and write the git metadata subset.
		readSpawn := dedupeRoots(slices.Concat(readFile, layout.ReadGrantPaths, defaultSystemReadRoots))
		writeSpawn := dedupeRoots(slices.Concat(writeFile, layout.WritablePaths))
		fileTool = AccessScope{Read: ReadWorktreeOnly, ReadRoots: readFile, WriteRoots: writeFile}
		spawned = AccessScope{Read: ReadWorktreeOnly, ReadRoots: readSpawn, WriteRoots: writeSpawn}
	}
	return fileTool, spawned
}

// dedupeRoots cleans, drops empties, and de-duplicates a root list, preserving
// first-seen order.
func dedupeRoots(roots []string) []string {
	out := make([]string, 0, len(roots))
	for _, r := range roots {
		if r == "" {
			continue
		}
		c := filepath.Clean(r)
		if !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// filterMasked drops any root that is at or beneath a masked path, upholding the
// invariant that a resolved policy never grants a denylisted/pseudo-fs path.
func filterMasked(roots, masked []string) []string {
	if len(roots) == 0 {
		return roots
	}
	out := roots[:0:0]
	for _, r := range roots {
		blocked := false
		for _, m := range masked {
			if r == m || pathUnder(r, m) {
				blocked = true
				break
			}
		}
		if !blocked {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
