package sandbox

import "path/filepath"

// ReRoot re-resolves this policy against a different working directory, returning
// a fresh *ResolvedPolicy whose grants are anchored at cwd — the single
// re-rooting primitive every env re-root funnels through (subagent/delegate lane
// spawn, delegate resume, managed-worktree switch). It re-runs the M1 root +
// gitdir resolution against cwd from the policy's RETAINED inputs
// (mode/net/denylist-deltas/extra-roots + host facts), so a child delegate in its
// own worktree is confined to THAT worktree with fresh gitdir resolution — never
// to the parent's roots, which a plain pointer-copy would leak (a containment
// hole). It fails closed: a target the host cannot enforce (e.g. a mode+net the
// backend cannot serve) returns the same typed *RefusalError Resolve does, which
// the caller surfaces (a spawn errors, a resume is marked not-resumable, a
// worktree switch is refused) rather than silently downgrading.
//
//   - nil receiver → nil, nil (off is a no-op; the env carries no policy).
//   - an ENFORCED policy that retained no inputs (a hand-built literal, only
//     reachable in tests) → a typed *RefusalError. Re-rooting is a containment
//     boundary, so an un-re-rootable enforced policy fails CLOSED rather than
//     silently passing the source's worktree-anchored roots through to the child.
//     A Resolve-produced enforced policy always retains its inputs, so this never
//     fires in production.
func (rp *ResolvedPolicy) ReRoot(cwd string) (*ResolvedPolicy, error) {
	if rp == nil {
		return nil, nil
	}
	if err := rp.reRootableInputs(); err != nil {
		return nil, err
	}
	out, err := Resolve(rp.resolveInputs, rp.resolveHost, cwd)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// reRootableInputs fails closed when an enforced policy carries no retained
// resolve inputs (not produced by Resolve). Off policies re-resolve trivially, so
// they are exempt.
func (rp *ResolvedPolicy) reRootableInputs() error {
	if rp.Mode != ModeOff && rp.resolveInputs.Mode == ModeOff {
		return &RefusalError{
			Mode:   rp.Mode,
			Reason: "cannot re-root a sandbox policy that retained no resolve inputs (it was not produced by Resolve); refusing rather than leaking the source worktree's roots",
		}
	}
	return nil
}

// ControlPolicy returns the manage_worktree main-repo control variant of this
// policy: re-resolved at mainRepoRoot (so its grants anchor at the MAIN repo, not
// the current linked-worktree tool root) with the worktree registry
// (<main>/.git/worktrees) confirmed writable so the lifecycle git commands
// (worktree add/remove/lock/prune) can write registry entries. .git/config and
// hooks stay in Git.ProtectedPaths — write-denied by the enforcement layers (the
// bwrap backend re-binds ProtectedPaths read-only AFTER the writable binds; the
// in-process file-tool layer refuses a write to any protected surface), even
// though they sit under the writable main-repo root, exactly as a normal
// workspace-write worktree's own .git/config does today.
//
// Only writable modes manage worktrees: a read-only session's control env stays
// read-only (it can list/status worktrees but never create/remove one), so the
// registry is granted ONLY when the re-resolved policy already grants writes.
// Re-resolving workspace-write/restricted at mainRepoRoot already makes the main
// repo the writable worktree — which contains .git/worktrees — so the explicit
// registry grant is a belt-and-suspenders confirmation, not a widening past the
// mode's floor.
//
//   - nil receiver → nil, nil.
//   - an enforced policy with no retained inputs → typed *RefusalError (fail closed).
//   - a host that cannot enforce the mode at mainRepoRoot → typed *RefusalError.
//   - off / read-only / a non-enforced result → returned re-rooted, un-widened.
func (rp *ResolvedPolicy) ControlPolicy(mainRepoRoot string) (*ResolvedPolicy, error) {
	if rp == nil {
		return nil, nil
	}
	if err := rp.reRootableInputs(); err != nil {
		return nil, err
	}
	out, err := Resolve(rp.resolveInputs, rp.resolveHost, mainRepoRoot)
	if err != nil {
		return nil, err
	}
	// No writes to widen: off, read-only, or a git dir we could not classify.
	if !out.Enforced() || out.Git.CommonDir == "" || len(out.Spawned.WriteRoots) == 0 {
		return &out, nil
	}
	registry := filepath.Join(out.Git.CommonDir, "worktrees")
	out.FileTool.WriteRoots = dedupeRoots(append(append([]string{}, out.FileTool.WriteRoots...), registry))
	out.Spawned.WriteRoots = dedupeRoots(append(append([]string{}, out.Spawned.WriteRoots...), registry))
	// Uphold the fail-closed invariant: never grant a masked path.
	out.FileTool.WriteRoots = filterMasked(out.FileTool.WriteRoots, out.MaskedPaths)
	out.Spawned.WriteRoots = filterMasked(out.Spawned.WriteRoots, out.MaskedPaths)
	return &out, nil
}

// WithSessionScratch returns a copy of rp with dir folded into the file-tool
// layer's grants: a write root always (every mode's file-tool "temp only"/"+
// temp" carve-out per docs/sandboxing.md IS this grant — without it read-only
// has no writable file-tool root at all), and a read root too when the mode is
// worktree-confined (restricted), whose ReadRoots would otherwise exclude a
// directory the session just wrote into.
//
// Resolve cannot grant dir directly: EnableSandbox creates the concrete scratch
// directory only AFTER resolving the policy (the directory does not exist yet at
// resolve time), so this is the one late-bound file-tool grant — the execenv
// package calls it when it builds the file-tool enforcement layer, passing the
// concrete sandbox.SessionScratch.Dir / Wrapper.SessionTmp() path. The
// kernel-wrapped spawned-process layer needs no equivalent: it already reaches
// the scratch dir through its own sessionTmp parameter at Wrap-build time
// (buildBwrapArgv / SeatbeltPolicy both take sessionTmp separately from
// rp.Spawned), so Spawned's roots are left untouched here.
//
// A blank dir or an unenforced (off) policy returns rp unchanged. The result
// still upholds the "never grant a masked path" invariant (filterMasked), though
// a freshly created session-private directory is never itself denylisted.
func (rp ResolvedPolicy) WithSessionScratch(dir string) ResolvedPolicy {
	if dir == "" || !rp.Enforced() {
		return rp
	}
	rp.FileTool.WriteRoots = filterMasked(
		dedupeRoots(append(append([]string{}, rp.FileTool.WriteRoots...), dir)), rp.MaskedPaths)
	if rp.FileTool.Read == ReadWorktreeOnly {
		rp.FileTool.ReadRoots = filterMasked(
			dedupeRoots(append(append([]string{}, rp.FileTool.ReadRoots...), dir)), rp.MaskedPaths)
	}
	return rp
}

// Inputs returns the backend-independent policy REQUEST this policy was resolved
// from (mode, network, denylist deltas, extra roots). It is what a delegate
// descriptor persists so a resumed delegate can RE-RESOLVE against its lane plus
// freshly-probed host facts — never the worktree-anchored resolved roots, which a
// config that loosened between serf runs must not be able to widen. Zero for a
// hand-built literal (not Resolve-produced).
func (rp ResolvedPolicy) Inputs() SandboxPolicy { return rp.resolveInputs }

// HostBwrapPath returns the probed bubblewrap binary path the policy was resolved
// against (empty when none / off / non-Linux), so the env layer can build the
// kernel wrapper from a resolved policy without separately threading host facts.
func (rp ResolvedPolicy) HostBwrapPath() string { return rp.resolveHost.BwrapPath }

// HostBinaryPath returns the probed backend-binary path the policy was resolved
// against, matching its Backend: bubblewrap on Linux, /usr/bin/sandbox-exec on
// macOS. Empty for off / a non-enforcing policy or a backend with no binary, so
// the env layer can provision the wrapper for whichever backend resolved without
// re-threading host facts.
func (rp ResolvedPolicy) HostBinaryPath() string {
	switch rp.Backend {
	case BackendBwrap:
		return rp.resolveHost.BwrapPath
	case BackendSeatbelt:
		return rp.resolveHost.SandboxExecPath
	default:
		return ""
	}
}

// WithPolicy returns a copy of the wrapper enforcing rp instead of its current
// policy, keeping the same probed bwrap binary and per-session tmp. It is how the
// manage_worktree control env swaps in the ControlPolicy variant without
// re-provisioning the session tmp. A nil receiver returns nil.
func (w *Wrapper) WithPolicy(rp ResolvedPolicy) (*Wrapper, error) {
	if w == nil {
		return nil, nil
	}
	return NewWrapper(rp, w.binaryPath, w.sessionTmp)
}

// ReRoot re-roots the kernel wrapper to cwd: it re-resolves the wrapper's policy
// against cwd (fresh gitdir resolution for the target lane) and rebuilds the
// bwrap invocation from that, keeping the same probed bwrap binary and per-
// session tmp. A delegate in its own worktree thus spawns processes confined to
// THAT worktree, not the parent's. A nil receiver returns nil (no confinement);
// a target the host cannot enforce returns the typed *RefusalError from the
// resolve, which the caller surfaces.
func (w *Wrapper) ReRoot(cwd string) (*Wrapper, error) {
	if w == nil {
		return nil, nil
	}
	rerooted, err := w.policy.ReRoot(cwd)
	if err != nil {
		return nil, err
	}
	return NewWrapper(*rerooted, w.binaryPath, w.sessionTmp)
}
