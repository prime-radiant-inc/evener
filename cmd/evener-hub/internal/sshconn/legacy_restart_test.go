package sshconn

import (
	"context"
	"fmt"
	"strings"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// Historical port-based restart harness, retained only for its existing parser
// and refusal tests. Production replacements all use restartSelectedPending.
// restartBare implements the doc's four-step ad hoc-hub restart: find the pid
// by listening port, recover the exact argv and log destination, stop it, and
// relaunch detached with the recovered argv.
//
// It is the supervisorless restart path: restartHub falls through to it when no
// supervisor owns the hub, or when a detected supervisor's restart command
// cannot be built safely. Its guards reject a listener that does not own the
// configured address before any `kill`, but identification and the signal are
// separate host commands, so a PID-reuse window remains — the ad hoc path
// validates at identification time only, and that residual window is accepted.
func (m *Manager) restartBare(ctx context.Context, host hostreg.Host, expectedVersion, expectedGitSHA string, replaced hubIdentity, binding *effectBinding) error {
	port := hubPort(m.hostAddr(host))
	pid, err := m.findHubPID(ctx, host, port)
	if err != nil {
		return err
	}

	// Validate before killing: the process merely holds the hub's port, which a
	// port collision — or a second hub on a different loopback address — could
	// make something other than the hub this host is configured for. Compare the
	// FULL normalized bind address, not just the port: a hub on 127.0.0.1:9180
	// does not own a host configured for 127.0.0.2:9180 and must not be killed.
	//
	// An invocation with no --addr bound whatever the host's hub.toml or the hub
	// default selects, and the controller cannot read that config. Only the hub
	// default is therefore provably this endpoint; a non-default configured
	// address is refused rather than assumed.
	argv, err := m.recoverHubArgv(ctx, host, pid)
	if err != nil {
		return err
	}
	expectedAddr := loopbackAddr(m.hostAddr(host))
	// Inspect the listener's actual bound address first: it is authoritative,
	// where the recovered --addr (or its absence) is only a proxy. A probe that
	// cannot run — neither lsof nor ss is available, or the tool answered with
	// something unusable — falls back to the recovered address, so such a host is
	// no worse off than before.
	addrs, aerr := m.hubListenerAddrs(ctx, host, pid, port)
	if aerr == nil && len(addrs) > 0 {
		owned := false
		for _, a := range addrs {
			if listenerOwnsAddr(a, expectedAddr) {
				owned = true
				break
			}
		}
		if !owned {
			return fmt.Errorf("%w: host %q pid %s listens on %s but does not own the configured endpoint %s; refusing to restart it",
				ErrRestart, host.Name, pid, strings.Join(addrs, ", "), expectedAddr)
		}
		return m.stopAndRelaunch(ctx, host, port, pid, argv, expectedVersion, expectedGitSHA, replaced, binding)
	}
	recoveredAddr, hasAddr := hubAddrFlag(argv)
	if !hasAddr {
		recoveredAddr = defaultHubAddr
	}
	if _, ok := argvHubPort(recoveredAddr); !ok {
		// An unparsable recovered address must not be defaulted onto :9180:
		// that would agree with a default-port host and let this kill a
		// listener the recovered invocation did not own.
		return fmt.Errorf("%w: host %q pid %s was launched with unparsable --addr %q; refusing to restart it",
			ErrRestart, host.Name, pid, recoveredAddr)
	}
	if norm := loopbackAddr(recoveredAddr); norm != expectedAddr {
		if hasAddr {
			return fmt.Errorf("%w: host %q pid %s was launched with --addr %q (listens on %s) but the configured endpoint is %s; refusing to restart it",
				ErrRestart, host.Name, pid, recoveredAddr, norm, expectedAddr)
		}
		return fmt.Errorf("%w: host %q pid %s was launched with no --addr (hub default %s) but the configured endpoint is %s; refusing to restart it",
			ErrRestart, host.Name, pid, norm, expectedAddr)
	}
	return m.stopAndRelaunch(ctx, host, port, pid, argv, expectedVersion, expectedGitSHA, replaced, binding)
}

// stopAndRelaunch is the half of restartBare that runs once the listener has been
// proved to be the hub this host is configured for: it completes the identity
// read by proving the process runs as the host's effective SSH user (spec 04,
// §"Stop/restart mechanics" checks 1–4), then recovers the log destination,
// stops the old process, and relaunches the recovered argv detached. The owner
// check lives here, immediately before the signal, so no future call path can
// reach `kill` without it.
func (m *Manager) stopAndRelaunch(ctx context.Context, host hostreg.Host, port, pid string, argv []string, expectedVersion, expectedGitSHA string, replaced hubIdentity, binding *effectBinding) error {
	pid, err := numericPID(pid)
	if err != nil {
		return err
	}
	if err := m.checkHubOwner(ctx, host, pid); err != nil {
		return err
	}
	logPath := ""
	if lout, lerr := m.runner.Run(ctx, rawCommandArgv(m.opts, host, "lsof -p "+pid+" -a -d 1,2"), nil); lerr == nil {
		logPath, _ = parseLogPath(lout)
	}

	relaunch := relaunchCommand(argv, logPath)
	// Stop the old hub and wait for its port to clear before starting a new
	// one; relaunching into the gap races hub.lock (docs/evener-hub-remote-
	// operations.md:363-377). The pid is validated, and `kill --` documents at
	// the command itself that the argument can only ever be a pid.
	if err := m.checkEffectBinding(binding); err != nil {
		return err
	}
	kout, kerr := m.runner.Run(ctx, rawCommandArgv(m.opts, host, "kill -- "+pid), nil)
	if kerr != nil {
		return fmt.Errorf("%w: host %q could not stop hub pid %s: %w: %s", ErrRestart, host.Name, pid, kerr, tail(kout))
	}
	// Record the relaunch before waiting for the port to clear: the kill has
	// landed, and waitPortClear can still fail afterwards (a graceful drain past
	// the wait window, a dropped transport, a context timeout). With the relaunch
	// recorded only after it succeeded, that failure left the old hub dead, no
	// listener on the port, and nothing for the next Ensure to recover.
	m.setPendingRestart(host.Name, relaunch, expectedVersion, expectedGitSHA, replaced, binding)
	if err := m.waitPortClear(ctx, host, port); err != nil {
		return err
	}
	// The port clearing is not the same as the old hub being gone: the hub closes
	// its listener at the start of its graceful shutdown and keeps hub.lock while
	// it drains, so the replacement could still race the lock. Wait for the
	// process itself to exit, which is what releases the lock.
	if err := m.waitProcessExit(ctx, host, pid); err != nil {
		return err
	}

	if err := m.checkEffectBinding(binding); err != nil {
		return err
	}
	rout, rerr := m.runner.Run(ctx, rawCommandArgv(m.opts, host, relaunch), nil)
	if rerr != nil {
		return fmt.Errorf("%w: host %q relaunch: %w: %s", ErrRestart, host.Name, rerr, tail(rout))
	}
	return nil
}
