package sshconn

import (
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// EnsureRestartHook records one restart-only Ensure attempt as a durable
// operation (deploy pipeline 08b §6: "a reconnect with no durable record
// performs no mutating SSH command"). It mirrors EnsureDeployHook: called with
// the host's per-host gate already held and before the restart leg's first
// remote command, it persists the record carrying the attempt's fencing epoch
// and returns the finish the leg's outcome is recorded through. A non-nil
// error means no durable record could be persisted, so nothing may be
// restarted; with no hook wired at all, the attempt is refused for the same
// reason.
type EnsureRestartHook func(host hostreg.Host) (finish func(err error), err error)

// SetEnsureRestartHook wires the deploy pipeline's restart recorder into this
// Manager's Ensure path. It is a setter for the same reason
// SetEnsureDeployHook is: the hub's host surface is constructed after the
// Manager. A nil hook clears it; an unwired recorder refuses a restart-only
// Ensure attempt before any mutating command (§6's durable-record rule), so
// production always wires it (newHubHostManager wires both recorders in one
// block, and that is the only production construction).
func (m *Manager) SetEnsureRestartHook(hook EnsureRestartHook) {
	if hook == nil {
		m.ensureRestart.Store(nil)
		return
	}
	m.ensureRestart.Store(&hook)
}

// ensureRestartHook returns the wired hook, if any.
func (m *Manager) ensureRestartHook() EnsureRestartHook {
	if hook := m.ensureRestart.Load(); hook != nil {
		return *hook
	}
	return nil
}

// EnsureDeployHook records one Ensure-triggered deploy as a durable operation
// (deploy pipeline 08b §6). It is called with the host's per-host gate already
// held and before any remote write of the deploy step; it mints and persists
// the operation record carrying the deploy's fencing epoch and returns the
// finish the step's outcome is recorded through. A non-nil error means no
// durable record could be persisted, so nothing may be launched; with no hook
// wired at all, the deploy is refused for the same reason.
type EnsureDeployHook func(host hostreg.Host) (finish func(err error), err error)

// SetEnsureDeployHook wires the deploy pipeline's recorder into this Manager's
// Ensure path. It is a setter rather than an Options field because the hub's
// host surface (which owns the operation store and the gate's holder
// publication) is constructed after the Manager; the wiring runs before the
// Manager serves any request. A nil hook clears it; an unwired recorder
// refuses an Ensure-triggered deploy before any mutating command (§6's
// durable-record rule), so production always wires it.
func (m *Manager) SetEnsureDeployHook(hook EnsureDeployHook) {
	if hook == nil {
		m.ensureDeploy.Store(nil)
		return
	}
	m.ensureDeploy.Store(&hook)
}

// ensureDeployHook returns the wired hook, if any.
func (m *Manager) ensureDeployHook() EnsureDeployHook {
	if hook := m.ensureDeploy.Load(); hook != nil {
		return *hook
	}
	return nil
}
