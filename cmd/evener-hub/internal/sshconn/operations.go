package sshconn

// The 04b deploy/restart entry points the deploy pipeline's operation workers
// call (deploy pipeline 08b §6). The hub's `deploy` and `restart` handlers own
// admission, the token, dedup and the durable record; once the record exists
// its worker runs these. They are deliberately the same code paths `Ensure`
// drives — the same guards, the same refusals, the same resolved-target
// persistence — with the caller holding the host's per-host gate instead of the
// attach ladder (Manager.TryAcquire is the gate).
//
// Neither entry point acquires the gate: the caller already holds it (the gate
// is the same non-reentrant per-host lock), and re-acquiring it would deadlock.

import (
	"context"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// DeployForOperation runs the 04b deploy path against host under the caller's
// held gate and returns the resolved install target plus the launch-contract
// facts re-read after the install. It performs exactly what ensureOnce's deploy
// step plus its immediate guards perform:
//
//   - `m.deploy` itself: the terminal dirty-controller refusal, source and
//     revision verification, the push's atomic temp-plus-rename install with
//     its byte-count integrity check (or the pinned installer fallback), and
//     the resolved-target return;
//   - the resolved target is recorded as host.EvenerPath and remembered across
//     attempts (setResolvedTarget), so the restart, health verification and
//     later attaches address the binary that was actually installed;
//   - the launch contract is re-read on disk and judged (deployedBuildNotStamped):
//     an artifact that is not this controller's build is the terminal
//     unstamped-build refusal, exactly as in the attach ladder.
//
// The phase is bounded by the deploy limit, and the caller's context (the
// operation worker's controller-lifetime context) cancels it.
func (m *Manager) DeployForOperation(ctx context.Context, host hostreg.Host, facts Preflight) (string, Preflight, error) {
	deployCtx, cancel := context.WithTimeout(ctx, m.opts.deployLimit())
	defer cancel()
	target, err := m.deploy(deployCtx, host, facts)
	if err != nil {
		return "", facts, err
	}
	if target != "" {
		host.EvenerPath = target
		m.setResolvedTarget(host.Name, target)
	}
	// The controller's own build is installed now; the dev-identity question is
	// settled for this Manager's lifetime (the same mark ensureOnce sets).
	m.markDevDeployed(host.Name)
	after, err := m.reReadLaunchContract(deployCtx, host, facts)
	if err != nil {
		return "", facts, err
	}
	if err := m.deployedBuildNotStamped(host.Name, after, m.opts.controllerVersion()); err != nil {
		return "", facts, err
	}
	return target, after, nil
}

// RestartForOperation runs the 04b restart path against host under the caller's
// held gate: the same user-versus-system unit decision and proven-replacement
// wait `ensureOnce`'s restart step runs (restartHub), including the recorded
// restart a failed attempt leaves for the next Ensure. The phase is bounded by
// the deploy limit.
//
// The hub's restart worker owns the post-restart reattach and re-probe
// (deploy pipeline §6's post-operation refresh); this call ends once the
// replacement is proven healthy.
func (m *Manager) RestartForOperation(ctx context.Context, host hostreg.Host, facts Preflight) error {
	restartCtx, cancel := context.WithTimeout(ctx, m.opts.deployLimit())
	defer cancel()
	running, _ := m.probeRunningHub(restartCtx, host)
	return m.restartHub(restartCtx, host, facts, running)
}
