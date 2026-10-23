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
	"errors"
	"fmt"
	"slices"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// effectBinding outlives the worker that authorized an unfinished effect. The
// validator owns copied hub-entry facts, not an attempt context. An explicit
// deployment also retains its journal ID for a later correlated supersession.
// Automatic recovery retains this immutable value, even under a new record.
type effectBinding struct {
	registration hostreg.Host
	validate     func() error
	operationID  string
}

var errEntryBinding = errors.New("sshconn: pending effect entry validation refused")

func newEffectBinding(host hostreg.Host, validate func() error) *effectBinding {
	if validate == nil {
		return nil // Ordinary automatic operations have no explicit entry binding.
	}
	host.Roots = slices.Clone(host.Roots)
	return &effectBinding{registration: host, validate: validate}
}

func (m *Manager) checkEffectBinding(binding *effectBinding) error {
	if binding == nil {
		return nil
	}
	if m.reg == nil || !m.reg.SameRegistration(binding.registration.Name, binding.registration) {
		return fmt.Errorf("%w: %q", ErrHostNotFound, binding.registration.Name)
	}
	if err := binding.validate(); err != nil {
		return fmt.Errorf("%w: %w", errEntryBinding, err)
	}
	return nil
}

// Only a fresh admitted Restart reaches this producer. Validate its new entry
// before renewing, and never carry old work into a different registration.
// A nil validator does not renew an existing binding.
func (m *Manager) renewRestartBinding(host hostreg.Host, validate func() error) (*effectBinding, error) {
	pending := m.pendingRestart(host.Name)
	if pending.binding != nil && !hostreg.SameRegistration(pending.binding.registration, host) {
		return nil, fmt.Errorf("%w: %q", ErrHostNotFound, host.Name)
	}
	binding := newEffectBinding(host, validate)
	if binding == nil {
		return pending.binding, nil
	}
	if err := m.checkEffectBinding(binding); err != nil {
		return nil, err
	}
	if pending.binding != nil {
		binding.operationID = pending.binding.operationID
	}
	m.mu.Lock()
	if pending.active() {
		pending.binding = binding
		m.pendingRestarts[host.Name] = pending
	}
	m.mu.Unlock()
	return binding, nil
}

// DeploymentJournal identifies an explicit deployment's existing progress
// journal. Supersede must durably correlate the prior operation before the
// validated new intent replaces it at the write boundary. Ordinary callers
// omit this optional journal and keep the existing recovery behavior.
type DeploymentJournal struct {
	OperationID string
	Supersede   func(priorOperationID string) error
}

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
func (m *Manager) DeployForOperation(ctx context.Context, host hostreg.Host, facts Preflight, restart bool, beforeMutation func() error, journals ...DeploymentJournal) (string, Preflight, error) {
	intent := m.deploymentIntent(restart, hubIdentity{})
	intent.binding = newEffectBinding(host, beforeMutation)
	if len(journals) != 0 && intent.binding != nil {
		journal := journals[0]
		intent.binding.operationID = journal.OperationID
		prior := m.pendingRestart(host.Name)
		if prior.active() && prior.binding != nil && prior.binding.operationID != "" && prior.binding.operationID != journal.OperationID && journal.Supersede != nil {
			if !hostreg.SameRegistration(prior.binding.registration, host) {
				return "", facts, fmt.Errorf("%w: %q", ErrHostNotFound, host.Name)
			}
			if err := m.checkEffectBinding(intent.binding); err != nil {
				return "", facts, err
			}
			if facts.ExecutableMissing {
				return "", facts, endpointMigration(host)
			}
			return m.deployForOperationWithSupersession(ctx, host, facts, intent, func() error {
				if err := m.checkEffectBinding(intent.binding); err != nil {
					return err
				}
				return journal.Supersede(prior.binding.operationID)
			})
		}
	}
	return m.deployForOperation(ctx, host, facts, intent)
}

func (m *Manager) deployForOperation(ctx context.Context, host hostreg.Host, facts Preflight, intent pendingRestartState) (string, Preflight, error) {
	return m.deployForOperationWithSupersession(ctx, host, facts, intent, nil)
}

func (m *Manager) deployForOperationWithSupersession(ctx context.Context, host hostreg.Host, facts Preflight, intent pendingRestartState, supersede func() error) (string, Preflight, error) {
	deployCtx, cancel := context.WithTimeout(ctx, m.opts.deployLimit())
	defer cancel()
	beforeWrite := func(target string) error {
		if intent.restart && intent.replaced.startedAt.IsZero() && intent.command == "" {
			intent.replaced, _ = m.probeRunningHub(deployCtx, host, facts)
		}
		if supersede != nil {
			// The supersession callback already validated the binding and appended
			// the journal note. Publication below cannot fail after that note.
			m.recordDeployment(host.Name, intent, target)
			return nil
		}
		return m.deploymentRecorder(host.Name, intent)(target)
	}
	retained := m.pendingRestart(host.Name)
	target, err := m.deploySelectedWithSupersession(deployCtx, host, facts, supersede == nil && retained.deployment && retained.selected != nil, supersede, beforeWrite)
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
	// StartedAt identifies the verified successor for an owned startup or a
	// replacement whose predecessor start time is unknown. It is not a predecessor.
	StartedAt time.Time
}

// RecoverDeploymentForRestart completes an already-authorized deployment or
// recorded command under a fresh Restart worker's hold. It accepts that request's
// validated entry before attachment could replay the old binding. Attachment
// cannot precede deployment repair either, as its contract guard may refuse it.
// A nil result leaves ordinary standalone Restart on its attach-first,
// non-deployment path.
func (m *Manager) RecoverDeploymentForRestart(ctx context.Context, host hostreg.Host, holder hostops.Holder, beforeMutation func() error) (*RestartResult, error) {
	if !m.hostGateHeldBy(host.Name, holder) {
		return nil, fmt.Errorf("%w: host %q; deployment recovery requires the caller's hold", hostops.ErrGateNotHeld, host.Name)
	}
	if m.reg == nil || !m.reg.SameRegistration(host.Name, host) {
		return nil, fmt.Errorf("%w: %q", ErrHostNotFound, host.Name)
	}
	if pending := m.pendingRestart(host.Name); !pending.active() {
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
	binding, err := m.renewRestartBinding(host, beforeMutation)
	if err != nil {
		return RestartResult{}, err
	}
	restartCtx, cancel := context.WithTimeout(ctx, m.opts.deployLimit())
	defer cancel()
	registration := host
	m.applyResolvedTarget(&host)
	if pending := m.pendingRestart(host.Name); pending.selected != nil {
		if err := checkSelectedRestartRegistration(host, pending.selected.host); err != nil {
			return RestartResult{}, err
		}
	}
	// Refresh is read-only. A refusal may not suppress independent proof of an
	// already-completed effect, but still guards every execution path below.
	facts, err = m.reReadLaunchContract(restartCtx, host, facts)
	if pending := m.pendingRestart(host.Name); err != nil && pending.selected != nil && !pending.deployment {
		if successor, complete, err := m.proveSelectedRestartComplete(restartCtx, host, facts, pending); err != nil {
			return RestartResult{}, err
		} else if complete {
			result := RestartResult{TargetVersion: pending.expectedVersion, ReplacedStartedAt: pending.replaced.startedAt}
			if pending.start || result.ReplacedStartedAt.IsZero() {
				result.StartedAt = successor.report.Running.StartedAt
			}
			m.clearPendingRestart(host.Name)
			if ch := m.currentChannel(host.Name); ch != nil && ch.MatchesRegistration(registration) {
				ch.markLost()
			}
			return result, nil
		}
	}
	// Attachment proves the running hub's protocol, not the executable that
	// will replace it. Cached facts cannot settle a pending installation either.
	if facts.LaunchCheckKnown && appwire.NewerProtocolVersion(facts.Protocol) {
		return RestartResult{}, fmt.Errorf("%w: host %q's installed executable speaks %q, newer than this controller's %q; update the controller instead of downgrading the host",
			ErrHostProtocolNewer, host.Name, facts.Protocol, appwire.ProtocolVersion)
	}
	pending := m.authorizeDeploymentRestart(host.Name)
	if pending.deployment && (err != nil || pending.needsDeployment(facts)) {
		// This explicit Restart authorizes replacement even when the unfinished
		// deployment was deploy-only. Finish its write before replacing its marker.
		// An unusable contract must not block this already-authorized repair;
		// deployForOperation validates both the installed contract and exact build.
		cancel()
		_, facts, err = m.deployForOperation(ctx, host, facts, pending)
		m.applyResolvedTarget(&host)
		// Deployment and the ensuing restart retain their separate phase budgets.
		restartCtx, cancel = context.WithTimeout(ctx, m.opts.deployLimit())
		defer cancel()
	}
	if err != nil && (pending.deployment || pending.command == "") {
		return RestartResult{}, err
	}
	pending = m.pendingRestart(host.Name)
	version, pin := expectedServedBuild(facts, m.opts.controllerVersion())
	if pending.active() {
		version, pin = pending.expectedVersion, pending.expectedGitSHA
	}
	result := RestartResult{TargetVersion: version, ReplacedStartedAt: pending.replaced.startedAt}
	if err != nil {
		return result, err
	}
	if pending.selected == nil {
		if err := pending.validateExecutable(host.Name, facts); err != nil {
			return result, err
		}
	}
	var started bool
	if pending.selected != nil {
		started, err = m.recoverSelectedRestartWithStart(restartCtx, host, facts, &pending, true)
	} else {
		if err := m.checkEffectBinding(binding); err != nil {
			return result, err
		}
		owner, resolveErr := m.resolveOwnedEndpoint(restartCtx, host, facts)
		if resolveErr != nil {
			return result, fmt.Errorf("%w: %w", ErrRestart, resolveErr)
		}
		running, _ := identityFromReport(owner.report)
		if !pending.active() {
			result.ReplacedStartedAt = running.startedAt
		}
		previous := pending
		pending, err = m.prepareSelectedRestart(restartCtx, host, facts, owner, version, pin)
		if err != nil {
			return result, err
		}
		pending.binding = binding
		pending.replaced = previous.restartPredecessor(running)
		pending.start = owner.report.State == "stopped"
		started = pending.start
		m.mu.Lock()
		m.pendingRestarts[host.Name] = pending
		m.mu.Unlock()
		if err = m.checkEffectBinding(binding); err != nil {
			m.mu.Lock()
			m.pendingRestarts[host.Name] = previous
			m.mu.Unlock()
			return result, err
		}
		err = m.restartPreparedSelectedPending(restartCtx, host, facts, &pending, owner.pid != "0", previous)
	}
	if err != nil {
		return result, err
	}
	if started || result.ReplacedStartedAt.IsZero() {
		final, finalErr := m.resolveOwnedEndpoint(restartCtx, host, facts)
		actual, known := identityFromReport(final.report)
		if finalErr != nil || !known || actual.startedAt.IsZero() || !selectedSuccessor(final, *pending.selected, pending.nextEnv, version, pin) {
			pending.start, pending.deployment = true, false
			m.mu.Lock()
			m.pendingRestarts[host.Name] = pending
			m.mu.Unlock()
			return result, fmt.Errorf("%w: host %q startup identity could not be verified", ErrRestart, host.Name)
		}
		result.StartedAt = actual.startedAt
	}
	// Health has proved the replacement. The predecessor's SSH reader may not
	// have observed EOF yet, so make reattach replace its channel immediately.
	if ch := m.currentChannel(host.Name); ch != nil && ch.MatchesRegistration(registration) {
		ch.markLost()
	}
	return result, nil
}
