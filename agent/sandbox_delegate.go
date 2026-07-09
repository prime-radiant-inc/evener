package agent

import (
	"fmt"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/sandbox"
)

// provisionRestoredSandbox engages enforcement on a RESUMED root session's env from
// its PERSISTED mode, the resume-path counterpart to cmd/serf.provisionSandbox for a
// fresh session. Off/empty skips the host probe and restores byte-identically. A
// non-off mode re-resolves the persisted request against freshly-probed host facts
// and the restored cwd (never replaying stored roots), then builds the enforced env
// via EnableSandbox; a host that can no longer enforce the mode surfaces the
// resolver's *sandbox.RefusalError so the resume fails closed rather than running
// unconfined. A non-local env carrying a non-off mode is a misconfiguration — only
// the local env can sandbox. Tests inject a FakeProber via cfg.testOnly so the
// resume path stays hermetic.
func provisionRestoredSandbox(cfg SessionConfig, env execenv.ExecutionEnvironment) error {
	if sandbox.ModeIsOff(cfg.Sandbox) {
		return nil
	}
	le, ok := env.(*execenv.LocalExecutionEnvironment)
	if !ok {
		return fmt.Errorf("restore session sandbox: execution environment does not support sandboxing (mode %q)", cfg.Sandbox)
	}
	prober := sandbox.Prober(sandbox.RealProber{})
	if cfg.testOnly.sandboxProber != nil {
		prober = cfg.testOnly.sandboxProber
	}
	rp, err := sandbox.ResolveNamed(cfg.Sandbox, cfg.SandboxNet, prober.Probe(), env.WorkingDirectory())
	if err != nil {
		return fmt.Errorf("restore session sandbox: %w", err)
	}
	if err := le.EnableSandbox(rp); err != nil {
		return fmt.Errorf("restore session sandbox: %w", err)
	}
	return nil
}

// sandboxSnapshotFromEnv captures the durable sandbox policy INPUTS from a
// (possibly re-rooted) execution environment, mirroring how localEnvPolicyName
// captures the env var policy. It returns nil for an unsandboxed env (off, a nil
// policy, or a non-local env), so an off delegate's descriptor stays byte-
// identical to today. The captured value is the lane-INDEPENDENT policy request
// (mode/net/denylist-deltas/extra-roots), so a resumed delegate re-resolves it
// against its own lane rather than replaying a parent's worktree-anchored roots.
func sandboxSnapshotFromEnv(env execenv.ExecutionEnvironment) *jobstore.SandboxSnapshot {
	le, ok := env.(*execenv.LocalExecutionEnvironment)
	if !ok || le.Sandbox == nil || !le.Sandbox.Enforced() {
		return nil
	}
	return sandboxSnapshotFromInputs(le.Sandbox.Inputs())
}

// sandboxSnapshotFromInputs converts a live SandboxPolicy request into its durable
// snapshot, decoupling the persisted schema from the live type. An off request
// persists nothing.
func sandboxSnapshotFromInputs(in sandbox.SandboxPolicy) *jobstore.SandboxSnapshot {
	if in.Mode == sandbox.ModeOff {
		return nil
	}
	snap := &jobstore.SandboxSnapshot{
		Mode:               in.Mode.String(),
		DenylistAdd:        append([]string(nil), in.DenylistAdd...),
		DenylistRemove:     append([]string(nil), in.DenylistRemove...),
		ExtraWritableRoots: append([]string(nil), in.ExtraWritableRoots...),
		ExtraReadRoots:     append([]string(nil), in.ExtraReadRoots...),
	}
	if in.Network != nil {
		n := *in.Network
		snap.Network = &n
	}
	return snap
}

// cloneSandboxSnapshot deep-copies a persisted snapshot so a resumed-turn
// descriptor does not alias the previous descriptor's slices (mirroring the other
// resume-path clones in resumedDelegateRestoreDescriptor). nil stays nil.
func cloneSandboxSnapshot(in *jobstore.SandboxSnapshot) *jobstore.SandboxSnapshot {
	if in == nil {
		return nil
	}
	out := &jobstore.SandboxSnapshot{
		Mode:               in.Mode,
		DenylistAdd:        append([]string(nil), in.DenylistAdd...),
		DenylistRemove:     append([]string(nil), in.DenylistRemove...),
		ExtraWritableRoots: append([]string(nil), in.ExtraWritableRoots...),
		ExtraReadRoots:     append([]string(nil), in.ExtraReadRoots...),
	}
	if in.Network != nil {
		n := *in.Network
		out.Network = &n
	}
	return out
}

// sandboxPolicyFromSnapshot rebuilds the backend-independent policy REQUEST from a
// persisted snapshot for re-resolution on restore. ok is false when the mode name
// is unparseable (a corrupt/hand-edited descriptor), which the restore path treats
// as not-resumable rather than resuming with a guessed policy.
func sandboxPolicyFromSnapshot(snap *jobstore.SandboxSnapshot) (sandbox.SandboxPolicy, bool) {
	if snap == nil {
		return sandbox.SandboxPolicy{}, false
	}
	mode, err := sandbox.ParseMode(snap.Mode)
	if err != nil {
		return sandbox.SandboxPolicy{}, false
	}
	pol := sandbox.SandboxPolicy{
		Mode:               mode,
		DenylistAdd:        append([]string(nil), snap.DenylistAdd...),
		DenylistRemove:     append([]string(nil), snap.DenylistRemove...),
		ExtraWritableRoots: append([]string(nil), snap.ExtraWritableRoots...),
		ExtraReadRoots:     append([]string(nil), snap.ExtraReadRoots...),
	}
	if snap.Network != nil {
		n := *snap.Network
		pol.Network = &n
	}
	return pol, true
}
