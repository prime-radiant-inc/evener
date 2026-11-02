package sandbox

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"

	"primeradiant.com/evener/identifier"
)

// Wrapper turns a spawned command's argv into a kernel-wrapped argv that
// confines the command — and every descendant it forks — to a ResolvedPolicy's
// boundary. It is built once per sandboxed session from the resolved policy, the
// probed backend binary, and the per-session tmp dir, then carried on the
// execution environment and threaded to the MCP-stdio and hook spawn sites
// (which do not flow through execenv). A nil *Wrapper means no kernel confinement
// — exactly today's behavior, so a non-sandboxed spawn is byte-identical to
// before.
//
// It drives the bwrap backend on Linux (Wrap prepends the bubblewrap flags) and
// the Seatbelt backend on macOS (Wrap prepends sandbox-exec + the generated
// SBPL); the backend is chosen by the resolver and recorded on the policy.
type Wrapper struct {
	policy     ResolvedPolicy
	binaryPath string // absolute backend binary, resolved outside cwd (PATH-injection defense)
	sessionTmp string // per-session writable tmp; also the child's TMPDIR
}

var wrapSeatbelt = seatbeltWrap

// NewWrapper builds a kernel wrapper for policy using the backend binary at
// binaryPath and the per-session tmp dir sessionTmp. It refuses a backend that
// imposes no containment (BackendNone) and a non-absolute binary path: a
// cwd-relative sandbox binary is a PATH-injection vector — a spawned command
// could drop a fake "bwrap"/"sandbox-exec" beside the worktree and neuter the
// sandbox. The caller resolves the binary via HostFacts.BwrapPath /
// HostFacts.SandboxExecPath (both absolute). The Seatbelt Wrap path additionally
// hard-codes /usr/bin/sandbox-exec regardless of binaryPath, so a bogus stored
// path can never redirect the exec.
func NewWrapper(policy ResolvedPolicy, binaryPath, sessionTmp string) (*Wrapper, error) {
	switch policy.Backend {
	case BackendBwrap, BackendSeatbelt:
	default:
		return nil, fmt.Errorf("sandbox: kernel wrapper requires an enforcing backend (bwrap or seatbelt), got %s", policy.Backend)
	}
	if !filepath.IsAbs(binaryPath) {
		return nil, fmt.Errorf("sandbox: backend binary path %q must be an absolute path (cwd-relative sandbox binaries are a PATH-injection vector)", binaryPath)
	}
	// bubblewrap pins a MISSING protected git surface by mounting over it, which
	// materializes that name on the real filesystem — so the surfaces whose empty
	// residue would break the repo are written with inert content first, once per
	// wrapper, rather than as a side effect of every Wrap. Seatbelt matches path
	// strings and creates nothing, so it needs no preparation.
	if policy.Backend == BackendBwrap {
		if err := prepareGitSurfaces(policy); err != nil {
			return nil, err
		}
		// The session scratch is allocated after Resolve, so a cache root
		// sharing storage with it (scratch kept under ~/.cache) is dropped
		// here, by the same rule Resolve applies to persistent write roots.
		policy.CacheRoots = dropCacheRootsSharingWriteRoots(slices.Clone(policy.CacheRoots), appendNonEmpty(nil, sessionTmp))
		policy = prepareCacheRoots(policy)
	}
	return &Wrapper{policy: policy, binaryPath: binaryPath, sessionTmp: sessionTmp}, nil
}

// prepareCacheRoots creates, empty, each overlaid cache root that does not exist
// yet. Such a root has no lower to overlay and a mount target bwrap cannot create
// in the read-only tree, so it would stay read-only while GOCACHE or GOMODCACHE
// still point at it; the go command would create the same empty directory on its
// first unsandboxed run. An empty directory cannot poison a cache. A root that
// cannot be served yet stays in the policy: each spawn judges it again
// (liveCacheRoots), so one that becomes available is served.
func prepareCacheRoots(rp ResolvedPolicy) ResolvedPolicy {
	if rp.CacheStrategy == CacheOverlay {
		recreateMissingCacheRoots(rp)
		warmOverlayProbes(rp)
	}
	return rp
}

// warmOverlayProbes asks the per-root overlay probe for each cache root that is
// a directory now, so the session's first spawn does not pay the probe's
// subprocess. A root that is not a directory yet is left unwarmed: its verdict
// is judged when it appears (servableCacheRoot, #4220), and warming it now would
// pin an unserved verdict it might outgrow.
func warmOverlayProbes(rp ResolvedPolicy) {
	if rp.resolveHost.OverlayLowerOK == nil {
		return
	}
	for _, c := range rp.CacheRoots {
		if fi, err := os.Stat(c); err == nil && fi.IsDir() {
			overlayLowerOK(rp, c)
		}
	}
}

// recreateMissingCacheRoots creates, empty, each cache root that has gone
// missing, at wrapper creation and again at every spawn (go clean -modcache on
// the host removes one), unless creating it would land inside a persistent
// write root or the worktree.
func recreateMissingCacheRoots(rp ResolvedPolicy) {
	for _, c := range rp.CacheRoots {
		if _, err := os.Lstat(c); errors.Is(err, fs.ErrNotExist) && !landsInPersistentRoot(rp, c) {
			_ = os.MkdirAll(c, 0o755)
		}
	}
}

// servableCacheRoot reports whether the overlay can serve cache root c now: it
// is a directory, it shares no storage with a persistent write root or the
// worktree (dropCacheRootsSharingWriteRoots, landsInPersistentRoot), and
// resolving it follows no link in or into bwrap's private /tmp or /dev. It is
// judged at each spawn, since a symlinked parent retargeted after resolution
// would otherwise move the overlay, and its discarded upper layer, into the
// workspace.
//
// A root whose filesystem this host refuses as an unprivileged overlay lower is
// never servable (overlayLowerOK): emitting it would abort every spawn with
// EINVAL (#4220). The verdict is judged here, at each spawn, so a root that only
// becomes a directory later is judged when it appears.
//
// The session scratch is checked once, when the wrapper is built (NewWrapper):
// a cache link retargeted onto the scratch mid-session would at worst send the
// session's own scratch writes into the discarded layer, which the threat
// model leaves to the session (docs/sandboxing.md#threat-model).
func servableCacheRoot(rp ResolvedPolicy, c string) bool {
	fi, err := os.Stat(c)
	if err != nil || !fi.IsDir() || landsInPersistentRoot(rp, c) || linksThroughPrivateMount(c) ||
		len(dropCacheRootsSharingWriteRoots([]string{c}, bwrapWriteRoots(rp))) == 0 {
		return false
	}
	// The shared-tree guard Resolve applied, applied again to where the root
	// resolves now: a retargeted parent must not make it a temp root or /.
	return len(guardedHostRoots([]string{resolveExistingPrefix(c)}, rp.resolveHost.Home, rp.Git.WorktreeRoot)) == 1 &&
		overlayLowerOK(rp, c)
}

// overlayLowerOK reports whether bwrap can mount an unprivileged overlay with c
// as its lower on this host (HostFacts.OverlayLowerOK, #4220). A nil probe means
// yes: no overlay support, or a hand-built policy in a test. The probe memoizes
// per root, so asking at every spawn costs one probe per root per session.
func overlayLowerOK(rp ResolvedPolicy, c string) bool {
	if rp.resolveHost.OverlayLowerOK == nil {
		return true
	}
	return rp.resolveHost.OverlayLowerOK(c)
}

// liveCacheRoots returns the cache roots bwrap overlays at this spawn: the
// servable ones (servableCacheRoot), minus any whose real location lies inside
// another's, which that outer overlay serves (a nested overlay would be a
// mountpoint go clean -modcache cannot remove). A root merely spelled under
// another but linked elsewhere keeps its own overlay. Of two spellings of one
// directory, the first is kept.
func liveCacheRoots(rp ResolvedPolicy) []string {
	var servable, resolved []string
	for _, c := range rp.CacheRoots {
		if servableCacheRoot(rp, c) {
			servable = append(servable, c)
			resolved = append(resolved, resolveExistingPrefix(c))
		}
	}
	var live []string
	for i, c := range servable {
		nested := false
		for j := range servable {
			if j == i {
				continue
			}
			// A root whose real location lies inside another's is covered by
			// that overlay, provided the sandbox reaches it there: spelled
			// inside the outer root, or the outer root not spelled under a
			// private mount (bwrap's own /tmp and /dev hide host symlinks, so
			// such an overlay sits elsewhere in the sandbox).
			inside := resolved[i] != resolved[j] && identifier.PathUnder(resolved[i], resolved[j]) || resolved[i] == resolved[j] && j < i
			reached := identifier.PathUnder(servable[i], servable[j]) || !inPrivateMount(servable[j])
			if inside && reached {
				nested = true
			}
		}
		if !nested {
			live = append(live, c)
		}
	}
	return live
}

// landsInPersistentRoot reports whether c's real location (or, not created
// yet, where creating it would land) is at or inside where a persistent write
// root or the worktree lands, dangling links included (physicalLanding).
func landsInPersistentRoot(rp ResolvedPolicy, c string) bool {
	rc := physicalLanding(c)
	return slices.ContainsFunc(appendNonEmpty(slices.Clone(bwrapWriteRoots(rp)), rp.Git.WorktreeRoot), func(w string) bool {
		return identifier.PathUnder(rc, physicalLanding(w))
	})
}

// Policy returns the resolved policy this wrapper enforces.
func (w *Wrapper) Policy() ResolvedPolicy { return w.policy }

// SessionTmp returns the per-session writable tmp directory (the child's TMPDIR).
func (w *Wrapper) SessionTmp() string { return w.sessionTmp }

// Confine rewrites cmd to run under the wrapper's backend confinement: it prepends
// the backend invocation to cmd.Args (updating cmd.Path via Wrap) and, for the
// Seatbelt backend, sets cmd.Dir to dir. sandbox-exec has no chdir flag (unlike
// bwrap's --chdir, which Wrap encodes in the argv), so without this the confined
// child would inherit evener's process cwd instead of the worktree. It is the single
// spawn-site helper every kernel-wrapped command routes through (execenv, hooks,
// mcp) so the Seatbelt cwd handling lives in one place rather than being duplicated
// at each site. A nil wrapper leaves cmd unchanged (byte-identical to an
// unsandboxed spawn); an empty dir leaves cmd.Dir as the caller set it.
func (w *Wrapper) Confine(cmd *exec.Cmd, dir string) {
	if w == nil {
		return
	}
	if w.policy.Backend == BackendSeatbelt && dir != "" {
		cmd.Dir = dir
	}
	argv := w.Wrap(cmd.Args, dir)
	cmd.Path = argv[0]
	cmd.Args = argv
}

// ConfineTrustedInfra confines cmd exactly like Confine — same filesystem
// binds, exec floor, fd hygiene — EXCEPT the network is never severed, even
// under a net=off session policy. It exists for trusted-infrastructure spawns:
// MCP server subprocesses, launched only from config layers the model cannot
// write (docs/sandboxing.md), get a network carve-out because the config that
// started them is trusted, not model-directed. Model-authored spawned
// processes (the shell, hooks, rg) must go through Confine, which stays
// network-severed under net=off — this method must never be used for those.
func (w *Wrapper) ConfineTrustedInfra(cmd *exec.Cmd, dir string) {
	w.withNetworkAllowed().Confine(cmd, dir)
}

// withNetworkAllowed returns a Wrapper identical to w except its policy's
// Network is forced true, so Wrap/Confine on the copy never emit a network
// denial regardless of the session's real net=off/on setting. A nil receiver
// stays nil, so ConfineTrustedInfra on an unsandboxed (nil) wrapper is still a
// no-op, matching Confine's nil behavior.
func (w *Wrapper) withNetworkAllowed() *Wrapper {
	if w == nil {
		return nil
	}
	dup := *w
	dup.policy.Network = true
	return &dup
}

// Wrap prepends the backend invocation to argv so the command runs confined to
// the wrapper's policy. For bwrap the returned slice is
// [bwrap, <flags...>, --argv0, argv[0], --, argv...] and cwd becomes the sandbox
// working directory via a --chdir flag. For Seatbelt it is
// [/usr/bin/sandbox-exec, -p, <policy>, -DKEY=path..., --, argv...] and cwd is
// IGNORED — sandbox-exec has no chdir flag, so the caller must set the spawned
// command's cmd.Dir itself (use Confine, which does this). Either is ready to hand
// to exec.Command / syscall.Exec. A nil wrapper returns argv unchanged so callers
// can wrap unconditionally.
func (w *Wrapper) Wrap(argv []string, cwd string) []string {
	if w == nil {
		return argv
	}
	if w.policy.Backend == BackendSeatbelt {
		wrapped, err := wrapSeatbelt(argv, w.policy, w.sessionTmp, cwd)
		if err != nil {
			// Unreachable in a real deployment: the resolver selects the seatbelt
			// backend only on darwin, where seatbeltWrap always succeeds. A seatbelt
			// policy reaching the non-darwin stub means the fail-closed floor was
			// bypassed; panic (fail closed, loudly) rather than return argv unwrapped
			// and run the command UNCONFINED.
			panic(fmt.Sprintf("sandbox: seatbelt backend selected on a host that cannot enforce it: %v", err))
		}
		return wrapped
	}
	if w.policy.CacheStrategy == CacheOverlay {
		recreateMissingCacheRoots(w.policy)
	}
	flags := buildBwrapArgv(w.policy, w.sessionTmp, cwd)
	out := make([]string, 0, 1+len(flags)+3+len(argv))
	out = append(out, w.binaryPath)
	out = append(out, flags...)
	// --argv0 preserves the command's own argv[0] inside the sandbox (bwrap 0.9.0+).
	if len(argv) > 0 {
		out = append(out, "--argv0", argv[0])
	}
	out = append(out, "--")
	out = append(out, argv...)
	return out
}
