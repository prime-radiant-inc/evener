package agent

import (
	"errors"
	"fmt"
	"strings"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/sandbox"
)

type delegatePreparedEnvironment struct {
	env              execenv.ExecutionEnvironment
	ownsFresh        bool
	stableController bool
}

type delegatePreparedEnvironmentContextKey struct{}

func (s *Session) prepareSubagentEnvironment(workingDir string, requested *sandbox.SandboxPolicy) (execenv.ExecutionEnvironment, bool, error) {
	subEnv := s.currentEnv()
	workingDir = strings.TrimSpace(workingDir)
	if workingDir != "" {
		local, ok := subEnv.(*execenv.LocalExecutionEnvironment)
		if s.subagentPrepareFault("working_dir_env") != nil || !ok {
			return nil, false, errors.New("execution environment does not support working_dir override")
		}
		rerooted := local.WithWorkingDirectory(workingDir)
		rerootErr := rerooted.SandboxReRootError()
		if fault := s.subagentPrepareFault("sandbox_reroot"); fault != nil {
			rerootErr = fault
		}
		if rerootErr != nil {
			return nil, false, fmt.Errorf("sandbox cannot confine the subagent to %s: %w", workingDir, rerootErr)
		}
		subEnv = rerooted
	}
	if requested != nil {
		local, ok := subEnv.(*execenv.LocalExecutionEnvironment)
		if s.subagentPrepareFault("sandbox_env") != nil || !ok {
			return nil, false, errors.New("execution environment does not support a per-delegate sandbox")
		}
		if workingDir == "" {
			local = local.WithWorkingDirectory(local.WorkingDirectory())
		}
		var resolved sandbox.ResolvedPolicy
		var err error
		if fault := s.subagentPrepareFault("sandbox_resolve"); fault != nil {
			err = fault
		} else {
			policy := *requested
			policy.InfraReadRoots = SessionInfraRoots(s.cfg, local)
			resolved, err = sandbox.Resolve(policy, s.sandboxHostFacts(), local.WorkingDirectory())
		}
		if err != nil {
			local.DisposeSandboxScratch()
			return nil, false, fmt.Errorf("per-delegate sandbox: %w", err)
		}
		if fault := s.subagentPrepareFault("sandbox_enable"); fault != nil {
			err = fault
		} else {
			err = local.EnableSandbox(&resolved)
		}
		if err != nil {
			local.DisposeSandboxScratch()
			return nil, false, fmt.Errorf("per-delegate sandbox: %w", err)
		}
		subEnv = local
	}
	return subEnv, workingDir != "" || requested != nil, nil
}

// parentSandboxModeNet reports the session's effective sandbox (mode, network) —
// the parent side of the per-delegate no-escalation floor. An unsandboxed session
// (nil/non-enforced policy, or a non-local env) is off with unrestricted network,
// so a delegate under an off parent may request any box.
func (s *Session) parentSandboxModeNet() (sandbox.Mode, bool) {
	mode, network, _ := s.parentSandboxFloor()
	return mode, network
}

// parentSandboxFloor returns every enforced parent axis that a child must not
// relax. WriteBlocked is separate from Mode because a restricted read-only role
// keeps ModeRestricted's narrow reads while removing its ordinary workspace
// writes.
func (s *Session) parentSandboxFloor() (sandbox.Mode, bool, bool) {
	if le, ok := s.currentEnv().(*execenv.LocalExecutionEnvironment); ok && le.Sandbox != nil && le.Sandbox.Enforced() {
		return le.Sandbox.Mode, le.Sandbox.Network, le.Sandbox.WriteBlocked
	}
	return sandbox.ModeOff, true, false
}

// readOnlyDelegateSandbox returns the enforced box for a structured read-only
// delegate scope. A restricted parent already has a narrower read surface than
// ModeReadOnly, so the two modes are incomparable; in that case the child keeps
// ModeRestricted and removes every persistent write root instead of widening its
// reads. The ordinary off/workspace-write/read-only parent cases can safely
// tighten to ModeReadOnly.
func (s *Session) readOnlyDelegateSandbox() (*sandbox.SandboxPolicy, error) {
	parentMode, parentNetwork := s.parentSandboxModeNet()
	if parentMode == sandbox.ModeRestricted {
		return &sandbox.SandboxPolicy{
			Mode:         sandbox.ModeRestricted,
			WriteBlocked: true,
			Network:      &parentNetwork,
		}, nil
	}
	return resolveDelegateSandboxRequest(sandbox.ModeReadOnly.String(), nil, parentMode, parentNetwork)
}

// resolveReadOnlyDelegateSandboxRequest applies the structured read-only role's
// write-confinement floor to the caller's explicit per-delegate request. A
// mode that grants persistent writes is not silently tightened: explicit
// sandbox requests are part of the caller-visible contract, so an incompatible
// request is refused. A net-only request is compatible because it changes only
// the network axis; apply it to the role floor rather than the parent's
// potentially write-capable mode.
func (s *Session) resolveReadOnlyDelegateSandboxRequest(sandboxMode string, sandboxNet *bool) (*sandbox.SandboxPolicy, error) {
	parentMode, parentNetwork := s.parentSandboxModeNet()
	floor, err := s.readOnlyDelegateSandbox()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(sandboxMode) == "" {
		if sandboxNet == nil {
			return floor, nil
		}
		requested, err := buildDelegateSandboxPolicy(floor.Mode.String(), sandboxNet, parentMode, parentNetwork)
		if err != nil {
			return nil, err
		}
		requested.WriteBlocked = floor.WriteBlocked
		return requested, nil
	}
	requested, err := resolveDelegateSandboxRequest(sandboxMode, sandboxNet, parentMode, parentNetwork)
	if err != nil {
		return nil, err
	}
	if !delegateSandboxBlocksPersistentWrites(requested) {
		return nil, fmt.Errorf("invalid_request: sandbox %q permits persistent workspace writes, but this delegate's structured tool scope requires a write-blocked sandbox; use sandbox=%q or omit the sandbox arguments", strings.TrimSpace(sandboxMode), sandbox.ModeReadOnly)
	}
	return requested, nil
}

func delegateSandboxBlocksPersistentWrites(policy *sandbox.SandboxPolicy) bool {
	if policy == nil || policy.Mode == sandbox.ModeOff {
		return false
	}
	return policy.Mode == sandbox.ModeReadOnly || policy.WriteBlocked
}

// applyParentWriteBlockedFloor prevents an explicitly sandboxed child from
// dropping a parent's delegate-only WriteBlocked axis. A net-only request does
// not ask to change filesystem policy, so it inherits the write block; an
// explicit write-capable mode is contradictory and fails before admission.
func (s *Session) applyParentWriteBlockedFloor(sandboxMode string, policy *sandbox.SandboxPolicy) (*sandbox.SandboxPolicy, error) {
	_, _, parentWriteBlocked := s.parentSandboxFloor()
	if !parentWriteBlocked {
		return policy, nil
	}
	if strings.TrimSpace(sandboxMode) == "" {
		if policy == nil {
			return nil, errors.New("invalid_request: sandbox_net could not preserve the parent's write-blocked sandbox")
		}
		policy.WriteBlocked = true
		return policy, nil
	}
	if !delegateSandboxBlocksPersistentWrites(policy) {
		return nil, fmt.Errorf("invalid_request: sandbox %q permits persistent workspace writes that the parent sandbox forbids", strings.TrimSpace(sandboxMode))
	}
	return policy, nil
}

// restoreDelegateSandboxFloor validates and, for a legacy descriptor with no
// sandbox snapshot, derives every mandatory parent/role floor before any cold
// runtime is constructed. The descriptor's structured tool ceiling is
// authoritative; role prose is deliberately irrelevant. Persisted write-capable
// policies fail closed instead of being silently rewritten.
func (s *Session) restoreDelegateSandboxFloor(descriptor *delegatestore.Descriptor) (*sandbox.SandboxPolicy, error) {
	if descriptor == nil {
		return nil, errors.New("invalid_request: committed delegate descriptor is unavailable")
	}
	policy := sandboxPolicyFromStableSnapshot(descriptor.Sandbox)
	if descriptor.Sandbox != nil && policy == nil {
		return nil, fmt.Errorf("invalid_request: committed delegate sandbox mode %q is invalid", descriptor.Sandbox.Mode)
	}
	readOnlyScope := subagentToolScopeIsReadOnly(false, descriptor.ToolNameCeiling)
	parentMode, parentNetwork, parentWriteBlocked := s.parentSandboxFloor()
	if policy == nil && readOnlyScope {
		var err error
		policy, err = s.readOnlyDelegateSandbox()
		if err != nil {
			return nil, fmt.Errorf("read-only delegate sandbox: %w", err)
		}
	}
	if policy == nil && parentWriteBlocked {
		local, ok := s.currentEnv().(*execenv.LocalExecutionEnvironment)
		if !ok || local.Sandbox == nil || !local.Sandbox.Enforced() {
			return nil, errors.New("invalid_request: current parent write-blocked sandbox is unavailable during delegate restore")
		}
		inherited := local.Sandbox.Inputs()
		policy = &inherited
	}
	if policy != nil && descriptor.Sandbox == nil {
		descriptor.Sandbox = stableDelegateSandboxSnapshot(policy)
		descriptor.Config.Sandbox = descriptor.Sandbox.Mode
		descriptor.Config.SandboxNet = nil
		if descriptor.Sandbox.Network != nil {
			network := *descriptor.Sandbox.Network
			descriptor.Config.SandboxNet = &network
		}
	}
	if readOnlyScope && !delegateSandboxBlocksPersistentWrites(policy) {
		return nil, fmt.Errorf("invalid_request: committed sandbox %q permits persistent workspace writes for a delegate whose structured tool scope requires a write-blocked sandbox", policy.Mode)
	}
	if parentWriteBlocked && !delegateSandboxBlocksPersistentWrites(policy) {
		return nil, fmt.Errorf("invalid_request: committed sandbox %q permits persistent workspace writes that the current parent sandbox forbids", policy.Mode)
	}
	if policy == nil {
		return nil, nil
	}
	if !policy.Mode.AtLeastAsConfining(parentMode) {
		return nil, fmt.Errorf("invalid_request: committed sandbox %q is not at least as confining as the current parent sandbox %q", policy.Mode, parentMode)
	}
	network := true
	if policy.Network != nil {
		network = *policy.Network
	}
	if network && !parentNetwork {
		return nil, errors.New("invalid_request: committed delegate sandbox grants network access that the current parent sandbox forbids")
	}
	return policy, nil
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

// allowedDelegateModes lists, in AllModes order, the sandbox modes a delegate may
// request under a parent in the given mode — those at least as confining as the
// parent on BOTH axes — as a comma-joined string for the floor error's recoverable-
// set hint. Pure.
func allowedDelegateModes(parentMode sandbox.Mode) string {
	var names []string
	for _, m := range sandbox.AllModes() {
		if m.AtLeastAsConfining(parentMode) {
			names = append(names, m.String())
		}
	}
	return strings.Join(names, ", ")
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
	// The modes are a PARTIAL order (read-only and restricted are incomparable), so a
	// refusal must not read as "looser" — a read-only parent refusing a restricted
	// child is really a WRITE-axis mismatch, not "restricted grants more access".
	// Name the confinement failure and list the recoverable set, mirroring the
	// delegation-allowance error's "valid grants" house style.
	if !requested.AtLeastAsConfining(parentMode) {
		return nil, fmt.Errorf("invalid_request: sandbox %q allows access on an axis your %s sandbox forbids (it is not at least as confining on both reads and writes); modes allowed under your %s sandbox: %s", requested, parentMode, parentMode, allowedDelegateModes(parentMode))
	}
	// off applies NO network confinement — Resolve hard-codes net on for ModeOff and
	// EnableSandbox treats a non-enforced policy as a no-op — so an explicit
	// sandbox_net alongside sandbox="off" would silently run with full network while
	// the caller believes egress is off. Refuse the contradiction (BEFORE the
	// inherit short-circuit below) rather than drop the flag.
	if requested == sandbox.ModeOff && sandboxNet != nil {
		return nil, errors.New(`invalid_request: sandbox_net has no effect with sandbox="off" (off applies no network confinement); pass a non-off sandbox mode or omit sandbox_net`)
	}
	// An explicit sandbox="off" that passes the floor (only under an off parent) is
	// exactly the inherit path: returning nil lets createDelegate skip the clone +
	// EnableSandbox(off) round-trip and just inherit the (off) parent env.
	if requested == sandbox.ModeOff {
		return nil, nil
	}
	net := parentNet
	if sandboxNet != nil {
		net = *sandboxNet
	}
	if net && !parentNet {
		return nil, errors.New("invalid_request: sandbox_net on grants more network access than your own sandbox (network off); a delegate cannot be less restricted than you; omit sandbox_net or pass sandbox_net=false")
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
// its PERSISTED mode, the resume-path counterpart to cmd/evener.provisionSandbox for a
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
	rp, err := sandbox.ResolveNamed(cfg.Sandbox, cfg.SandboxNet, prober.Probe(), env.WorkingDirectory(), SessionInfraRoots(cfg, env))
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
func sandboxSnapshotFromEnv(env execenv.ExecutionEnvironment) *delegatestore.SandboxSnapshot {
	le, ok := env.(*execenv.LocalExecutionEnvironment)
	if !ok || le.Sandbox == nil || !le.Sandbox.Enforced() {
		return nil
	}
	return sandboxSnapshotFromInputs(le.Sandbox.Inputs())
}

// sandboxSnapshotFromInputs converts a live SandboxPolicy request into its durable
// snapshot, decoupling the persisted schema from the live type. An off request
// persists nothing.
func sandboxSnapshotFromInputs(in sandbox.SandboxPolicy) *delegatestore.SandboxSnapshot {
	if in.Mode == sandbox.ModeOff {
		return nil
	}
	snap := &delegatestore.SandboxSnapshot{
		Mode:               in.Mode.String(),
		WriteBlocked:       in.WriteBlocked,
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
//
//nolint:unused // retained for the tagged sandbox descriptor round-trip fuzz owner.
func cloneSandboxSnapshot(in *delegatestore.SandboxSnapshot) *delegatestore.SandboxSnapshot {
	if in == nil {
		return nil
	}
	out := &delegatestore.SandboxSnapshot{
		Mode:               in.Mode,
		WriteBlocked:       in.WriteBlocked,
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
//
//nolint:unused // retained for the tagged sandbox descriptor round-trip fuzz owner.
func sandboxPolicyFromSnapshot(snap *delegatestore.SandboxSnapshot) (sandbox.SandboxPolicy, bool) {
	if snap == nil {
		return sandbox.SandboxPolicy{}, false
	}
	mode, err := sandbox.ParseMode(snap.Mode)
	if err != nil {
		return sandbox.SandboxPolicy{}, false
	}
	pol := sandbox.SandboxPolicy{
		Mode:               mode,
		WriteBlocked:       snap.WriteBlocked,
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
