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
	WriteBlocked  bool          // true = only the separately provisioned session scratch is writable
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

	// UnmaskedRoots are read-only roots carved out of MaskedPaths: the installed
	// plugin files and user skills directory (HostFacts.EvenerContentRoots),
	// readable in both layers in every mode. A root is admitted only if it holds
	// no masked path and lies outside the pseudo-fs floor, so the carve-out
	// exposes that content and nothing the credential denylist protects.
	UnmaskedRoots []string

	// Git is the resolved git-surface map: writable metadata, write-protected
	// config/hook surfaces, and outside-worktree read grants. Zero for off.
	Git GitLayout

	// ToolchainBinDir is the macOS developer-toolchain bin directory the env floor
	// puts on a spawned process's PATH, ahead of the system directories, so `git`
	// and friends resolve to the real binaries instead of the /usr/bin xcrun
	// shims. The shims memoize their lookup in a per-user temp file no sandbox
	// mode makes writable, so under a sandbox each shim invocation re-runs
	// `xcodebuild -find`: stderr noise and seconds of latency, on every call.
	//
	// It is NOT a grant and never widens one — the directory sits inside the
	// read-only toolchain grant restricted mode already makes, and this field is
	// populated only when the directory survives RootGuard, is not masked, and is
	// really readable under this policy's spawned grants. Empty for off, on
	// non-darwin hosts, and whenever any of those checks fails (in which case the
	// shim path still works, just loudly).
	ToolchainBinDir string

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

// Enforced reports whether an OS sandbox is in force for this policy. False for
// off. It is the question the kernel-wrapper layer asks — whether to build bwrap
// arguments, print the startup enforcement line, or announce a box in the prompt
// — and is deliberately NOT the question the in-process file tools ask; see
// FileToolConfined.
func (rp ResolvedPolicy) Enforced() bool { return rp.Mode != ModeOff }

// FileToolConfined reports whether the in-process file-tool layer must enforce
// this policy's grants. Every enforced mode confines the file tools, and so does
// a write-blocked OFF policy: that is the read-only delegate scope degraded on a
// host with no sandbox backend, where the file tools are the only enforcement
// available and carry the whole write boundary (the shell stays unconfined, which
// both the delegate and its parent are told). A plain off policy confines
// nothing, so the file tools keep today's byte-identical os path.
func (rp ResolvedPolicy) FileToolConfined() bool { return rp.Mode != ModeOff || rp.WriteBlocked }

// FileToolCanRead reports whether the in-process file-tool layer may read
// the absolute path under this policy — the question a caller naming a path
// to the model must settle first (the pasted-image attachment note promises
// read_file, and the promise must be true). It models the policy-level
// grants: masked paths deny in every mode, ReadAnywhere allows everything
// else, and ReadWorktreeOnly requires the path to fall under a read root.
// The live-filesystem checks securepath layers on top (symlink-component
// refusal, escape detection) are TOCTOU guards around these grants and stay
// there. An unconfined policy reads with plain os and allows any path; a
// relative path cannot be proved inside any root and reads false.
func (rp ResolvedPolicy) FileToolCanRead(path string) bool {
	if !rp.FileToolConfined() {
		return true
	}
	// Containment is lexical (pathUnder) and both sides must be absolute:
	// a relative path cannot be proved inside any root, and a relative
	// root grants nothing.
	if !filepath.IsAbs(path) {
		return false
	}
	if rp.Masks(path) {
		return false
	}
	if rp.FileTool.Read == ReadAnywhere {
		return true
	}
	for _, root := range rp.FileTool.ReadRoots {
		if filepath.IsAbs(root) && pathUnder(path, root) {
			return true
		}
	}
	return false
}

// Masks reports whether path is denied by the masked set: at or beneath a
// masked path and not inside one of the UnmaskedRoots carved out of it. Inside
// a carve-out, any .git stays masked: an installed copy cloned from git keeps
// its remote URL, which may carry a token, in .git/config, and skills and hooks
// never need it. Every layer asks this one question, so the file tools, the
// policy filters and the backends agree on what a mask hides.
func (rp ResolvedPolicy) Masks(path string) bool {
	if !isUnderAnyRoot(path, rp.MaskedPaths) {
		return false
	}
	for _, root := range rp.UnmaskedRoots {
		if pathUnder(path, root) {
			return insideGitDir(path, root)
		}
	}
	return true
}

// insideGitDir reports whether path has a .git component below root. The name
// is compared case-insensitively: on a case-insensitive filesystem (macOS by
// default) .GIT names the same directory.
func insideGitDir(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(strings.Split(rel, string(filepath.Separator)), isGitName)
}

// isGitName reports whether a path component names git metadata (.git, in any
// letter case).
func isGitName(name string) bool { return strings.EqualFold(name, ".git") }

// FileToolEnforceable reports whether this OS has an in-process file-tool
// enforcement implementation. Its race-safe primitives (openat2 /
// RESOLVE_NO_SYMLINKS on Linux, the O_NOFOLLOW tail walk on darwin) exist only
// there; every other platform's stand-ins fail closed on EVERY operation, reads
// included. Linux callers must separately prove the live process may use openat2:
// an older kernel or seccomp policy can reject the syscall even though the OS has
// the implementation. The delegate fallback combines this platform check with
// execenv's runtime probe before deriving a wrapperless policy.
func FileToolEnforceable(host HostFacts) bool {
	return host.OS == "linux" || host.OS == "darwin"
}

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
// directories, the global git config files) through RootGuard, refusing any that
// sits at or above the home directory, the worktree, or a temp root. The
// candidates come from a host probe that honours configuration (`xcode-select -p`
// follows $DEVELOPER_DIR, the global config location follows $XDG_CONFIG_HOME),
// so they are treated as untrusted input like every other configured root.
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
// PLAIN off short-circuits before any host check, so it resolves on every host
// (including Windows) — it is today's behavior with no containment. A WRITE-BLOCKED
// off policy is not that: it is the read-only delegate scope degraded on a host
// with no backend, and it confines the file tools, so it takes the ordinary path
// through the denylist, the git surface and the scope builder (skipping only the
// backend choice, which is what it has none of).
func Resolve(policy SandboxPolicy, host HostFacts, cwd string) (ResolvedPolicy, error) {
	if policy.Mode == ModeOff && !policy.WriteBlocked {
		return ResolvedPolicy{Mode: ModeOff, Network: true, Backend: BackendNone, resolveInputs: policy, resolveHost: host}, nil
	}

	// The write-blocked box has no kernel wrapper behind it, so the in-process file
	// tools ARE its enforcement. Where their race-safe primitives do not exist, every
	// file operation fails closed instead — a delegate with broken file tools rather
	// than a confined one — so refuse here, at the resolver, rather than leave it to
	// whichever caller derived the request.
	if policy.Mode == ModeOff && !FileToolEnforceable(host) {
		return ResolvedPolicy{}, &RefusalError{
			Mode: policy.Mode, Net: true,
			Reason: fmt.Sprintf("a write-blocked sandbox is enforced by the in-process file tools, which have no race-safe primitives on %s; only an unconfined --sandbox off is available there", host.OS),
		}
	}

	// Network defaults ON when sandboxed: a nil policy.Network is the unset default,
	// a non-nil value an explicit choice. Collapse to a concrete bool exactly once
	// here so the zero value can never silently disable egress.
	netOn := policy.Network == nil || *policy.Network
	if policy.Mode == ModeOff {
		// off applies no network confinement whatever the request said — there is no
		// wrapper to unshare a namespace — so reporting anything but "on" would
		// overstate, exactly as the plain-off short-circuit above hard-codes it.
		netOn = true
	}

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

	// A write-blocked off policy chooses no backend — it exists precisely because
	// none is available, and asking would reproduce the refusal it replaces.
	backend := BackendNone
	if policy.Mode != ModeOff {
		chosen, refusal := chooseBackend(policy, host, netOn)
		if refusal != nil {
			return ResolvedPolicy{}, refusal
		}
		backend = chosen
	}

	masked, configMasks := withEvenerConfigMask(policy.EffectiveDenylist(host.Home), host)
	worktree := layout.WorktreeRoot

	rp := ResolvedPolicy{
		Mode:          policy.Mode,
		WriteBlocked:  policy.WriteBlocked,
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
	rp.UnmaskedRoots = unmaskedContentRoots(host.EvenerContentRoots, masked, configMasks,
		slices.Concat(rp.FileTool.WriteRoots, rp.Spawned.WriteRoots), host.Home, worktree)

	// Fail-closed invariant: never grant a root that is at or under a masked path.
	rp.FileTool.ReadRoots = filterMasked(rp.FileTool.ReadRoots, rp)
	rp.FileTool.WriteRoots = filterMasked(rp.FileTool.WriteRoots, rp)
	rp.Spawned.ReadRoots = filterMasked(rp.Spawned.ReadRoots, rp)
	rp.Spawned.WriteRoots = filterMasked(rp.Spawned.WriteRoots, rp)

	// Read roots in both layers: restricted needs the grant, and the
	// read-anywhere modes' file tools use it as an open anchor.
	rp.FileTool.ReadRoots = dedupeRoots(slices.Concat(rp.FileTool.ReadRoots, rp.UnmaskedRoots))
	rp.Spawned.ReadRoots = dedupeRoots(slices.Concat(rp.Spawned.ReadRoots, rp.UnmaskedRoots))

	rp.ToolchainBinDir = toolchainBinDir(host, rp, worktree)
	return rp, nil
}

// toolchainBinDir decides whether the probed developer-toolchain bin directory
// may go on a spawned process's PATH. It grants nothing: the directory is
// already inside restricted mode's read-only toolchain grant, and the looser
// modes read everywhere but the denylist. The checks exist so PATH never names a
// directory the process cannot actually read — an unreadable PATH entry would
// turn a noisy `git` into a missing one, which is strictly worse.
//
//   - RootGuard, because the value derives from `xcode-select -p`, which honours
//     $DEVELOPER_DIR: it is untrusted input exactly like the read grant.
//   - Not at or beneath a masked path, which is unreadable in every mode.
//   - Under a granted spawned read root whenever the spawned layer is
//     root-limited; the read-anywhere modes need no such check.
func toolchainBinDir(host HostFacts, rp ResolvedPolicy, worktree string) string {
	bin := host.DeveloperToolBinDir
	if bin == "" {
		return ""
	}
	if len(guardedHostRoots([]string{bin}, host.Home, worktree)) == 0 {
		return ""
	}
	if len(filterMasked([]string{bin}, rp)) == 0 {
		return ""
	}
	if rp.Spawned.Read == ReadWorktreeOnly && !isUnderAnyRoot(bin, rp.Spawned.ReadRoots) {
		return ""
	}
	return bin
}

// ResolveNamed resolves a session policy from a mode NAME (the persisted/flag form)
// plus the network decision, host facts, and cwd. It is the single glue the live
// flag path (cmd/evener) and the resume path (agent.RestoreSessionFromMetaWithConfig)
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
	case ModeOff, ModeReadOnly:
		// Reads anywhere (minus masked); no writes (session tmp is the only scratch).
		// ModeOff reaches here only as the write-blocked degrade — plain off never
		// calls scopesFor — and takes read-only's scope by construction, because
		// read-only is the box it stands in for.
		//
		// The worktree is a READ root even though reads are already allowed anywhere.
		// It is an ANCHOR, not a limit: openRead resolves an in-root target beneath
		// this root's fd, which tolerates a symlinked ANCESTOR of the worktree while
		// still refusing a symlink component INSIDE it. Without it every read takes
		// the "/"-anchored no-symlinks path, and a workspace reached through a
		// symlinked ancestor (a bind-mounted link, a symlinked checkout, macOS
		// /tmp) is unreadable — a workspace shape, not an attack. The other modes
		// need no equivalent: their worktree is already a WRITE root, which openRead
		// anchors at first.
		anchor := dedupeRoots([]string{worktree})
		fileTool = AccessScope{Read: ReadAnywhere, ReadRoots: anchor}
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
		// developer directory — ruled 2026-08-06), the user's global git config
		// FILES (git fatals on a present-but-unreadable ~/.gitconfig, so without
		// them git was unusable in this mode on any host with a global config —
		// read-only and file-exact, ruled 2026-08-07), the session's hook/MCP-server
		// paths (session infrastructure, which must run in every mode — ruled
		// 2026-08-06), and write the git metadata subset. Those extra grants stay OUT
		// of readFile: a hook living in the plugin cache must be executable and the
		// git shim must reach its toolchain, but the model must not gain a file-tool
		// browse grant over either along with it.
		//
		// The other modes need no equivalent line: their spawned layer already reads
		// anywhere-minus-the-denylist, which covers any hook/MCP/toolchain path that
		// is not masked — and a masked one must stay masked in every mode.
		devRoots := guardedHostRoots(host.DeveloperToolRoots, host.Home, worktree)
		gitConfig := guardedHostRoots(host.GitGlobalConfigPaths, host.Home, worktree)
		readSpawn := dedupeRoots(slices.Concat(readFile, layout.ReadGrantPaths, defaultSystemReadRoots, devRoots, gitConfig, policy.InfraReadRoots))
		writeSpawn := dedupeRoots(slices.Concat(writeFile, layout.WritablePaths))
		fileTool = AccessScope{Read: ReadWorktreeOnly, ReadRoots: readFile, WriteRoots: writeFile}
		spawned = AccessScope{Read: ReadWorktreeOnly, ReadRoots: readSpawn, WriteRoots: writeSpawn}
	}
	if policy.WriteBlocked {
		fileTool.WriteRoots = nil
		spawned.WriteRoots = nil
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

// filterMasked drops any root the policy masks (ResolvedPolicy.Masks), upholding
// the invariant that a resolved policy never grants a denylisted/pseudo-fs path.
func filterMasked(roots []string, rp ResolvedPolicy) []string {
	if len(roots) == 0 {
		return roots
	}
	out := roots[:0:0]
	for _, r := range roots {
		if !rp.Masks(r) {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// withEvenerConfigMask returns the masked set with Evener's own config root in
// it, plus the masks that stand for that root: ~/.config/evener while the
// denylist holds it, and the host's configured root (HostFacts.EvenerConfigRoot)
// when XDG_CONFIG_HOME moved it, since the credentials live wherever the root
// is. A user who removed ~/.config/evener from the denylist removed both.
func withEvenerConfigMask(masked []string, host HostFacts) (all, configMasks []string) {
	def := filepath.Join(host.Home, ".config", "evener")
	if !slices.Contains(masked, def) {
		return masked, nil
	}
	configMasks = []string{def}
	if cr := filepath.Clean(host.EvenerConfigRoot); filepath.IsAbs(host.EvenerConfigRoot) && cr != def {
		masked = append(slices.Clone(masked), cr)
		configMasks = append(configMasks, cr)
	}
	return masked, configMasks
}

// unmaskedContentRoots admits the host's Evener content roots as read-only
// roots carved out of Evener's config mask. A candidate is refused when it
// fails the shared-tree guard (relative, at or above home, the worktree or a
// temp root), lies at or beneath the non-removable pseudo-fs floor, sits under
// any mask other than Evener's config mask (a user's own denylist entry above
// it wins), holds a masked path (carving it out would expose that path),
// overlaps a write root (it would not stay read-only), or resolves anywhere but
// its own place under the mask (carveOutEscapes). guardedHostRoots only cleans
// a candidate, without resolving symlinks, so these checks see its literal
// spelling: a cache -> plugins symlink is refused
// (TestEvenerContentRootsRefuseASymlinkWithinTheConfigMask).
func unmaskedContentRoots(candidates, masked, configMasks, writeRoots []string, home, worktree string) []string {
	var out []string
	for _, root := range guardedHostRoots(candidates, home, worktree) {
		switch {
		case isUnderAnyRoot(root, defaultPseudoFSPaths),
			slices.ContainsFunc(masked, func(m string) bool { return pathUnder(m, root) }),
			slices.ContainsFunc(masked, func(m string) bool { return pathUnder(root, m) && !slices.Contains(configMasks, m) }),
			slices.ContainsFunc(writeRoots, func(w string) bool { return pathUnder(w, root) || pathUnder(root, w) }):
			continue
		}
		if resolved, err := filepath.EvalSymlinks(root); err == nil && carveOutEscapes(resolved, root, masked) {
			continue
		}
		out = append(out, root)
	}
	return dedupeRoots(out)
}

// carveOutEscapes reports whether resolved, the symlink-resolved location of the
// carve-out root, is anywhere but the root's own place inside the mask it is
// carved from: under another mask, under the pseudo-fs floor, or elsewhere in
// its own mask through a symlink at or below that mask.
// The backends re-grant a carve-out at its real path, so such a root would
// re-expose that other mask. Masks are compared both as written and resolved.
func carveOutEscapes(resolved, root string, masked []string) bool {
	if isUnderAnyRoot(resolved, defaultPseudoFSPaths) {
		return true
	}
	for _, m := range masked {
		if pathUnder(root, m) {
			// The mask the root is carved from: the root must resolve to its own
			// place inside it, so a symlink at or below the mask cannot redirect
			// the re-grant to the config root or the store's metadata.
			rm, err := filepath.EvalSymlinks(m)
			if err != nil {
				rm = m
			}
			rel, err := filepath.Rel(m, root)
			if err != nil || resolved != filepath.Join(rm, rel) {
				return true
			}
			continue
		}
		if pathUnder(resolved, m) {
			return true
		}
		if rm, err := filepath.EvalSymlinks(m); err == nil && pathUnder(resolved, rm) {
			return true
		}
	}
	return false
}
