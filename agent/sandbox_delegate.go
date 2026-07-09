package agent

import (
	"errors"
	"fmt"
	"strings"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/sandbox"
)

// parentSandboxModeNet reports the session's effective sandbox (mode, network) —
// the parent side of the per-delegate no-escalation floor. An unsandboxed session
// (nil/non-enforced policy, or a non-local env) is off with unrestricted network,
// so a delegate under an off parent may request any box.
func (s *Session) parentSandboxModeNet() (sandbox.Mode, bool) {
	if le, ok := s.currentEnv().(*execenv.LocalExecutionEnvironment); ok && le.Sandbox != nil && le.Sandbox.Enforced() {
		return le.Sandbox.Mode, le.Sandbox.Network
	}
	return sandbox.ModeOff, true
}

// resolveDelegateSandboxRequest turns a delegate's (sandbox, sandbox_net) request
// plus the parent's effective (mode, network) into the policy to enforce, or an
// invalid_request error. It returns (nil, nil) when NEITHER arg is set — the
// inherit path. An explicit sandbox_net WITHOUT a mode inherits the parent's mode
// (a net-only tightening): under a sandboxed parent that yields the parent's mode
// with the requested network; under an unsandboxed (off) parent it is an error,
// because network confinement is meaningless without a sandbox and silently
// dropping the flag would be a surprising no-op. The floor is then applied by
// buildDelegateSandboxPolicy. Pure, so the whole decision is table-testable.
func resolveDelegateSandboxRequest(sandboxMode string, sandboxNet *bool, parentMode sandbox.Mode, parentNet bool) (*sandbox.SandboxPolicy, error) {
	mode := strings.TrimSpace(sandboxMode)
	if mode == "" && sandboxNet == nil {
		return nil, nil
	}
	if mode == "" {
		if parentMode == sandbox.ModeOff {
			return nil, errors.New("invalid_request: sandbox_net requires a sandbox mode; your session is not sandboxed, so pass sandbox=... as well")
		}
		mode = parentMode.String()
	}
	return buildDelegateSandboxPolicy(mode, sandboxNet, parentMode, parentNet)
}

// buildDelegateSandboxPolicy validates an explicit per-delegate sandbox request
// against the parent session's effective (mode, network) and returns the policy to
// enforce for the delegate, or an invalid_request error when the request would
// grant MORE access than the parent has — the no-escalation floor (security
// invariant). The mode must be at least as confining as the parent's on both axes;
// the network may be turned off (tighter) but never on. An omitted sandbox_net
// inherits the parent's effective network, so a delegate under a net-off parent
// stays net-off. The parent's effective network is passed by the caller (off/
// unsandboxed parents are net-on, i.e. unrestricted). It is pure so the floor is
// exhaustively table-testable without minting a delegate.
func buildDelegateSandboxPolicy(sandboxMode string, sandboxNet *bool, parentMode sandbox.Mode, parentNet bool) (*sandbox.SandboxPolicy, error) {
	requested, err := sandbox.ParseMode(sandboxMode)
	if err != nil {
		return nil, fmt.Errorf("invalid_request: %w", err)
	}
	if !requested.AtLeastAsConfining(parentMode) {
		return nil, fmt.Errorf("invalid_request: sandbox %q grants more access than your own sandbox (%s); a delegate cannot be less restricted than you", requested, parentMode)
	}
	// off applies NO network confinement — Resolve hard-codes net on for ModeOff and
	// EnableSandbox treats a non-enforced policy as a no-op — so an explicit
	// sandbox_net alongside sandbox="off" would silently run with full network while
	// the caller believes egress is off. Refuse the contradiction rather than drop
	// the flag.
	if requested == sandbox.ModeOff && sandboxNet != nil {
		return nil, errors.New(`invalid_request: sandbox_net has no effect with sandbox="off" (off applies no network confinement); pass a non-off sandbox mode or omit sandbox_net`)
	}
	net := parentNet
	if sandboxNet != nil {
		net = *sandboxNet
	}
	if net && !parentNet {
		return nil, errors.New("invalid_request: sandbox_net on grants more network access than your own sandbox (network off); a delegate cannot be less restricted than you")
	}
	// The floor compares MODE and NETWORK only, and the returned policy carries only
	// those two axes — deliberately. SandboxPolicy also has denylist deltas
	// (DenylistAdd/DenylistRemove) and extra roots (ExtraRead/WritableRoots), but NO
	// production path originates a non-empty value for them today: they are only ever
	// round-tripped through snapshots (sandboxSnapshotFromInputs / cloneSandboxSnapshot
	// / sandboxPolicyFromSnapshot below), and both origination surfaces — the CLI
	// (sandbox.ResolveNamed) and the launch-config path — are mode+net only. So a
	// same-mode delegate cannot be looser on those axes than its parent: parity holds
	// trivially because the parent's values are empty too.
	//
	// IF a config surface for those axes is ever added (e.g. a launch-config
	// `denylist_add`, or per-session extra read roots), THIS FLOOR MUST BE EXTENDED to
	// carry the parent's TIGHTENING axes (DenylistAdd, and the read/write root
	// scoping) into the child's policy — otherwise a same-mode delegate could read a
	// path its parent masks (a DenylistRemove or an ExtraReadRoot the parent lacks),
	// re-opening the escalation this floor exists to prevent. Do not add speculative
	// propagation now; add it WITH the surface that first needs it.
	return &sandbox.SandboxPolicy{Mode: requested, Network: &net}, nil
}

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
