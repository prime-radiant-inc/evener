package sandbox

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

var resolveAbs = filepath.Abs

// Backend names the concrete enforcement mechanism the resolver chose for a
// session on this host. It is recorded on the ResolvedPolicy so M3/M6 know which
// backend to drive; M1 selects it but nothing enforces.
type Backend int

const (
	// BackendNone: off mode — no backend, no containment.
	BackendNone Backend = iota
	// BackendBwrap: Linux bubblewrap. Serves the full mode matrix and net=off.
	BackendBwrap
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
// chosen Backend that every backend (bwrap, Seatbelt) must satisfy.
// It is produced by Resolve, immutable thereafter, and carried inert on the
// execution environment until a backend consumes it (M2/M3/M6). A nil
// *ResolvedPolicy (or Mode == ModeOff) is exactly today's behavior.
type ResolvedPolicy struct {
	Mode          Mode
	Network       bool          // true = egress allowed (--sandbox-net on)
	Backend       Backend       // enforcing backend chosen for this host
	CacheStrategy CacheStrategy // how cache roots are served (never persistent-writable)
	SessionTmp    bool          // a per-session writable tmp (TMPDIR) is provisioned

	// CacheRoots are the absolute language cache directories (~/.cache, ~/go/pkg,
	// ~/.npm, ~/.cargo) served under the cache strategy: overlaid read-real/
	// write-private when CacheStrategy is CacheOverlay, or redirected via env to
	// the session tmp when CacheSessionPrivate. Empty when no cache handling is
	// needed (off, read-only). Populated for the writable modes.
	CacheRoots []string

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

	// resolveInputs and resolveHost are the request this policy was resolved
	// FROM, retained so ReRoot / ControlPolicy can re-run the root + gitdir
	// resolution against a DIFFERENT worktree (a child delegate lane, a managed
	// worktree, the manage_worktree main-repo control env) using the same
	// mode/net/denylist-deltas/extra-roots and host facts — never by copying this
	// policy's worktree-anchored roots, which would confine the child to the
	// parent's lane (a containment hole). Set by Resolve on every success path; a
	// hand-built literal leaves them zero and is not re-rootable (ReRoot passes
	// such a policy through unchanged — the M1 inert-carrier semantics).
	resolveInputs SandboxPolicy
	resolveHost   HostFacts
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

// sharedTempRoots are the multi-tenant scratch roots (in both macOS spellings) a
// host-derived grant root must never sit at or above: they hold every other
// session's scratch. They anchor the RootGuard alongside the home directory and
// the session's own worktree.
var sharedTempRoots = []string{"/tmp", "/private/tmp", "/var/folders", "/private/var/folders"}

// guardedHostRoots filters host-derived grant candidates (the developer-toolchain
// directories) through RootGuard, refusing any that sits at or above the home
// directory, the worktree, or a temp root. The candidates come from a host probe
// that honours configuration (`xcode-select -p` follows $DEVELOPER_DIR), so they
// are treated as untrusted input like every other configured root.
func guardedHostRoots(candidates []string, home, worktree string) []string {
	if len(candidates) == 0 {
		return nil
	}
	guard := NewRootGuard(slices.Concat([]string{home, worktree}, sharedTempRoots)...)
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if root := guard.Permit(c); root != "" {
			out = append(out, root)
		}
	}
	return out
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
		return ResolvedPolicy{Mode: ModeOff, Network: true, Backend: BackendNone, resolveInputs: policy, resolveHost: host}, nil
	}

	// Network defaults ON when sandboxed: a nil policy.Network is the unset default,
	// a non-nil value an explicit choice. Collapse to a concrete bool exactly once
	// here so the zero value can never silently disable egress.
	netOn := policy.Network == nil || *policy.Network

	// A sandboxed session needs an absolute home to anchor the credential denylist.
	// Without one (e.g. the home-directory env var is unset in a bare service),
	// joining home-relative secrets yields RELATIVE paths the enforcement layers
	// never match — a silent unmask of ~/.ssh, ~/.aws, etc. Fail closed rather than
	// resolve a leaky policy.
	if !filepath.IsAbs(host.Home) {
		return ResolvedPolicy{}, &RefusalError{
			Mode:   policy.Mode,
			Net:    netOn,
			Reason: "cannot anchor the credential denylist: the session's home directory is not an absolute path; sandboxing without a resolvable home would silently unmask credential directories",
		}
	}

	// A relative cwd would flow through into relative grant roots, but
	// ResolvedPolicy documents absolute roots and the enforcement layers compare
	// absolute paths — a relative root would silently never match. Absolutize
	// fail-closed (filepath.Abs only errors if the process working directory is
	// unresolvable).
	absCwd, err := resolveAbs(cwd)
	if err != nil {
		return ResolvedPolicy{}, &RefusalError{
			Mode:   policy.Mode,
			Net:    netOn,
			Reason: fmt.Sprintf("could not resolve an absolute path for the session working directory %q: %v", cwd, err),
		}
	}
	cwd = absCwd

	// Extra roots are folded verbatim into the resolved grants, so a relative
	// entry would emit a relative grant root (same non-matching hazard as a
	// relative cwd). Refuse rather than resolve a leaky policy.
	for _, r := range slices.Concat(policy.ExtraReadRoots, policy.ExtraWritableRoots, policy.InfraReadRoots) {
		if strings.TrimSpace(r) == "" {
			continue
		}
		if !filepath.IsAbs(r) {
			return ResolvedPolicy{}, &RefusalError{
				Mode:   policy.Mode,
				Net:    netOn,
				Reason: fmt.Sprintf("extra sandbox root %q must be an absolute path", r),
			}
		}
	}

	layout, err := ClassifyWorkspace(cwd)
	if err != nil {
		return ResolvedPolicy{}, &RefusalError{
			Mode:   policy.Mode,
			Net:    netOn,
			Reason: fmt.Sprintf("could not resolve the git layout of %s: %v", cwd, err),
		}
	}

	backend, refusal := chooseBackend(policy, host, netOn)
	if refusal != nil {
		return ResolvedPolicy{}, refusal
	}

	masked := policy.EffectiveDenylist(host.Home)
	worktree := layout.WorktreeRoot

	rp := ResolvedPolicy{
		Mode:          policy.Mode,
		Network:       netOn,
		Backend:       backend,
		CacheStrategy: cacheStrategyFor(policy.Mode, backend, host),
		CacheRoots:    cacheRootsFor(policy.Mode, host.Home),
		SessionTmp:    true,
		MaskedPaths:   masked,
		Git:           layout,
		resolveInputs: policy,
		resolveHost:   host,
	}
	rp.FileTool, rp.Spawned = scopesFor(policy, host, layout, worktree)

	// Fail-closed invariant: never grant a root that is at or under a masked path.
	rp.FileTool.ReadRoots = filterMasked(rp.FileTool.ReadRoots, masked)
	rp.FileTool.WriteRoots = filterMasked(rp.FileTool.WriteRoots, masked)
	rp.Spawned.ReadRoots = filterMasked(rp.Spawned.ReadRoots, masked)
	rp.Spawned.WriteRoots = filterMasked(rp.Spawned.WriteRoots, masked)
	return rp, nil
}

// ResolveNamed resolves a session policy from a mode NAME (the persisted/flag form)
// plus the network decision, host facts, and cwd. It is the single glue the live
// flag path (cmd/serf) and the resume path (agent.RestoreSessionFromMetaWithConfig)
// share so both build a policy identically. An empty or off name returns
// (nil, nil) — byte-identical to today, no containment; an unknown name is a typed
// parse error; otherwise it returns the Resolve result (a *RefusalError when the
// host cannot enforce the mode, surfaced as a start-time refusal by the caller).
//
// The caller supplies already-probed host facts so an OFF session can skip the
// probe entirely (see ModeIsOff); ResolveNamed never probes. infraReadRoots are
// the session's hook and MCP-server paths (SandboxPolicy.InfraReadRoots) — session
// infrastructure that must run in every mode, resolved by the caller from the
// session's actual hook/MCP config.
func ResolveNamed(modeName string, net *bool, host HostFacts, cwd string, infraReadRoots []string) (*ResolvedPolicy, error) {
	if ModeIsOff(modeName) {
		return nil, nil
	}
	mode, err := ParseMode(modeName)
	if err != nil {
		return nil, err
	}
	rp, err := Resolve(SandboxPolicy{Mode: mode, Network: net, InfraReadRoots: infraReadRoots}, host, cwd)
	if err != nil {
		return nil, err
	}
	return &rp, nil
}

// chooseBackend applies the fail-closed floor: it returns the backend that will
// enforce this (mode, net) on this host, or a *RefusalError naming the backend
// that would.
func chooseBackend(policy SandboxPolicy, host HostFacts, net bool) (Backend, *RefusalError) {
	switch host.OS {
	case "linux":
		// bwrap is the only Linux backend: it serves the full mode matrix and
		// net on/off. A Linux host that is not bwrap-capable has no way to enforce
		// any sandboxed mode, so it refuses; only --sandbox off works there.
		if host.BwrapCapable {
			return BackendBwrap, nil
		}
		return 0, &RefusalError{
			Mode: policy.Mode, Net: net, RequiredBackend: "",
			Reason: "no sandbox backend is available (bubblewrap is required on Linux but is not usable on this host); only --sandbox off is supported",
		}
	case "darwin":
		if host.SeatbeltAvailable() {
			return BackendSeatbelt, nil // deny-capable: full matrix + net on/off
		}
		return 0, &RefusalError{
			Mode: policy.Mode, Net: net, RequiredBackend: "sandbox-exec",
			Reason: "macOS sandboxing requires /usr/bin/sandbox-exec, which was not found",
		}
	default:
		return 0, &RefusalError{
			Mode: policy.Mode, Net: net, RequiredBackend: "",
			Reason: fmt.Sprintf("sandboxing is not supported on %s; only --sandbox off is available", host.OS),
		}
	}
}

// defaultCacheRoots are the language cache directories served under the cache
// strategy, expressed relative to $HOME.
var defaultCacheRoots = []string{".cache", "go/pkg", ".npm", ".cargo"}

// cacheRootsFor returns the absolute cache roots for a mode: the writable modes
// serve caches (overlaid or redirected), off/read-only need none.
func cacheRootsFor(mode Mode, home string) []string {
	switch mode {
	case ModeWorkspaceWrite, ModeRestricted:
		out := make([]string, 0, len(defaultCacheRoots))
		for _, rel := range defaultCacheRoots {
			out = append(out, filepath.Join(home, rel))
		}
		return out
	default:
		return nil
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
func scopesFor(policy SandboxPolicy, host HostFacts, layout GitLayout, worktree string) (fileTool, spawned AccessScope) {
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
		// execute), the host's developer-toolchain directories (macOS ships git and
		// friends as xcrun shims that exec the real binary out of the active
		// developer directory — ruled 2026-08-06), the session's hook/MCP-server
		// paths (session infrastructure, which must run in every mode — ruled
		// 2026-08-06), and write the git metadata subset. Both extra grants stay OUT
		// of readFile: a hook living in the plugin cache must be executable and the
		// git shim must reach its toolchain, but the model must not gain a file-tool
		// browse grant over either along with it.
		//
		// The other modes need no equivalent line: their spawned layer already reads
		// anywhere-minus-the-denylist, which covers any hook/MCP/toolchain path that
		// is not masked — and a masked one must stay masked in every mode.
		devRoots := guardedHostRoots(host.DeveloperToolRoots, host.Home, worktree)
		readSpawn := dedupeRoots(slices.Concat(readFile, layout.ReadGrantPaths, defaultSystemReadRoots, devRoots, policy.InfraReadRoots))
		writeSpawn := dedupeRoots(slices.Concat(writeFile, layout.WritablePaths))
		fileTool = AccessScope{Read: ReadWorktreeOnly, ReadRoots: readFile, WriteRoots: writeFile}
		spawned = AccessScope{Read: ReadWorktreeOnly, ReadRoots: readSpawn, WriteRoots: writeSpawn}
	}
	return fileTool, spawned
}

// dedupeRoots cleans, drops empty/whitespace-only entries, and de-duplicates a
// root list, preserving first-seen order. A whitespace-only entry ("   ") is
// dropped rather than emitted: filepath.Clean does not trim whitespace, so an
// un-dropped "   " would become a relative grant root the absolute-path
// enforcement layers never match (a silent grant). The extra-root validation
// already refuses a non-absolute non-empty entry; a whitespace-only entry is
// treated as empty here.
func dedupeRoots(roots []string) []string {
	out := make([]string, 0, len(roots))
	for _, r := range roots {
		if strings.TrimSpace(r) == "" {
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
