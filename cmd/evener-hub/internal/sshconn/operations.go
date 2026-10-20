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
	"fmt"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
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
func (m *Manager) DeployForOperation(ctx context.Context, host hostreg.Host, facts Preflight, restart bool) (string, Preflight, error) {
	return m.deployForOperation(ctx, host, facts, m.deploymentIntent(restart, hubIdentity{}))
}

func (m *Manager) deployForOperation(ctx context.Context, host hostreg.Host, facts Preflight, intent pendingRestartState) (string, Preflight, error) {
	deployCtx, cancel := context.WithTimeout(ctx, m.opts.deployLimit())
	defer cancel()
	beforeWrite := func(target string) {
		if intent.restart && intent.replaced.startedAt.IsZero() && intent.command == "" {
			intent.replaced, _ = m.probeRunningHub(deployCtx, host)
		}
		m.deploymentRecorder(host.Name, intent)(target)
	}
	target, err := m.deploy(deployCtx, host, facts, beforeWrite)
	if err != nil {
		return "", facts, err
	}
	m.acknowledgeDeployment(host.Name)
	if target != "" {
		host.EvenerPath = target
		m.setResolvedTarget(host.Name, target)
	}
	after, err := m.reReadLaunchContract(deployCtx, host, facts)
	if err != nil {
		return "", facts, err
	}
	if err := m.deployedBuildNotStamped(host.Name, after, intent.expectedVersion); err != nil {
		return "", facts, err
	}
	if !intent.restart {
		m.clearPendingRestart(host.Name)
	}
	return target, after, nil
}

// RestartResult preserves the selected build and original predecessor across
// recovery, so a worker reattaching afterward does not restart the replacement
// again or compare it against itself. Zero ReplacedStartedAt is unknown, not
// proof of replacement.
type RestartResult struct {
	TargetVersion     string
	ReplacedStartedAt time.Time
	// StartedAt is the verified identity of an owned free-port startup, not a
	// substitute predecessor. Ordinary replacements leave it zero.
	StartedAt time.Time
}

// RecoverDeploymentForRestart completes only an already-authorized deployment
// or recorded startup under the Restart worker's hold. Attachment cannot precede
// this repair: its launch-contract guard can refuse the unfinished installation.
// A nil result leaves ordinary standalone Restart on its attach-first,
// non-deployment path.
func (m *Manager) RecoverDeploymentForRestart(ctx context.Context, host hostreg.Host, holder hostops.Holder, beforeMutation func() error) (*RestartResult, error) {
	if !m.hostGateHeldBy(host.Name, holder) {
		return nil, fmt.Errorf("%w: host %q; deployment recovery requires the caller's hold", hostops.ErrGateNotHeld, host.Name)
	}
	if m.reg == nil || !m.reg.SameRegistration(host.Name, host) {
		return nil, fmt.Errorf("%w: %q", ErrHostNotFound, host.Name)
	}
	if pending := m.pendingRestart(host.Name); !pending.deployment && !pending.start {
		return nil, nil
	}
	if !m.beginEnsure() {
		return nil, ErrManagerClosed
	}
	defer m.ensureWG.Done()
	recoveryCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopClose := context.AfterFunc(m.baseCtx, cancel)
	defer stopClose()
	probeCtx, stopProbe := context.WithTimeout(recoveryCtx, m.opts.attemptLimit())
	resolved := host
	m.applyResolvedTarget(&resolved)
	preflight, err := m.collectPreflight(probeCtx, resolved)
	stopProbe()
	if err != nil {
		return nil, err
	}
	// Preserve host/environment refusals above, but let the authorized deployment
	// repair preflight.launchErr. The restart path re-reads and validates the
	// selected executable and retains the original build pin and predecessor.
	result, err := m.restartForOperation(recoveryCtx, host, preflight.facts, beforeMutation)
	return &result, err
}

// RestartForOperation runs the 04b restart path against host under the caller's
// held gate: the same user-versus-system unit decision and proven-replacement
// wait `ensureOnce`'s restart step runs (restartHub), including the recorded
// restart a failed attempt leaves for the next Ensure. The phase is bounded by
// the deploy limit.
//
// The hub's restart worker owns the post-restart reattach and re-probe
// (deploy pipeline §6's post-operation refresh); this call ends once the
// replacement is proven healthy. It returns the selected target version for
// that refresh, which may differ from the predecessor's version when this
// restart completes a pending deployment. The target is captured before the
// restart, never inferred from the replacement's health response.
// beforeMutation revalidates the worker's bound entry before each execution
// phase, including replacement after an installation repair within this call.
func (m *Manager) RestartForOperation(ctx context.Context, host hostreg.Host, facts Preflight, beforeMutation func() error) (string, error) {
	result, err := m.restartForOperation(ctx, host, facts, beforeMutation)
	return result.TargetVersion, err
}

func (m *Manager) restartForOperation(ctx context.Context, host hostreg.Host, facts Preflight, beforeMutation func() error) (RestartResult, error) {
	restartCtx, cancel := context.WithTimeout(ctx, m.opts.deployLimit())
	defer cancel()
	registration := host
	m.applyResolvedTarget(&host)
	// Attachment proves the running hub's protocol, not the executable that
	// will replace it. Cached facts cannot settle a pending installation either.
	facts, err := m.reReadLaunchContract(restartCtx, host, facts)
	pending := m.authorizeDeploymentRestart(host.Name)
	if pending.deployment && (err != nil || pending.needsDeployment(facts)) {
		// This explicit Restart authorizes replacement even when the unfinished
		// deployment was deploy-only. Finish its write before replacing its marker.
		// An unusable contract must not block this already-authorized repair;
		// deployForOperation validates both the installed contract and exact build.
		if beforeMutation != nil {
			if err := beforeMutation(); err != nil {
				return RestartResult{}, err
			}
		}
		cancel()
		_, facts, err = m.deployForOperation(ctx, host, facts, pending)
		m.applyResolvedTarget(&host)
		// Deployment and the ensuing restart retain their separate phase budgets.
		restartCtx, cancel = context.WithTimeout(ctx, m.opts.deployLimit())
		defer cancel()
	}
	if err != nil {
		return RestartResult{}, err
	}
	running, runningKnown := m.probeRunningHub(restartCtx, host)
	version, pin := expectedServedBuild(facts, m.opts.controllerVersion())
	pending = m.pendingRestart(host.Name)
	completed := runningKnown && pending.completedBy(running, facts)
	if pending.active() {
		version, pin = pending.expectedVersion, pending.expectedGitSHA
		running = pending.restartPredecessor(running)
	}
	result := RestartResult{TargetVersion: version, ReplacedStartedAt: running.startedAt}
	if !completed {
		if err := pending.validateExecutable(host.Name, facts); err != nil {
			return result, err
		}
	}
	if beforeMutation != nil {
		if err := beforeMutation(); err != nil {
			return result, err
		}
	}
	started := pending.start
	if pending.start {
		// A failed owned startup is not a new replacement request. If it is
		// already serving, verify it below without issuing another command.
		if !runningKnown {
			cleared, probeErr := m.portCleared(restartCtx, host, hubPort(m.hostAddr(host)))
			if probeErr != nil {
				return result, probeErr
			}
			if !cleared {
				return result, fmt.Errorf("%w: host %q startup recovery requires a free port", ErrRestart, host.Name)
			}
			err = m.recoverRestartPending(restartCtx, host, pending)
		}
	} else if completed {
		m.clearPendingRestart(host.Name)
	} else {
		started, err = m.restartHubWithStartProof(restartCtx, host, facts, version, pin, running)
	}
	if err != nil {
		return result, err
	}
	if started {
		// This mode is reached only through a recorded start or the actual
		// free-port startup branch. Capture the real process with the same
		// build/pin checks, for an independent epoch-bearing AppWire comparison.
		actual, known := m.probeRunningHub(restartCtx, host)
		if !known || actual.startedAt.IsZero() || actual.version != version || !hubBuildSHAMatches(actual.gitSHA, pin) {
			return result, fmt.Errorf("%w: host %q startup identity could not be verified", ErrRestart, host.Name)
		}
		result.StartedAt = actual.startedAt
		m.clearPendingRestart(host.Name)
	}
	// Health has proved the replacement. The predecessor's SSH reader may not
	// have observed EOF yet, so make reattach replace its channel immediately.
	if ch := m.currentChannel(host.Name); ch != nil && ch.MatchesRegistration(registration) {
		ch.markLost()
	}
	return result, nil
}
