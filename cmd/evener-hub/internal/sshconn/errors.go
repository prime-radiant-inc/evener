package sshconn

import "errors"

// Named sentinels for the failure classes spec 04's "Error handling" section
// enumerates. Callers match them with errors.Is; each returned error wraps the
// sentinel and adds the host, argv, or stderr tail that identifies the case.
var (
	// ErrHostNotFound marks an Ensure for a name the registry does not hold.
	ErrHostNotFound = errors.New("sshconn: host not found")

	// ErrSSHStart marks a failure to spawn or initialize the ssh bridge, or a
	// preflight command that could not run. It is retried under backoff when
	// the manager supervises a channel.
	ErrSSHStart = errors.New("sshconn: ssh channel start failed")

	// ErrSSHAuth marks a non-interactive authentication failure (BatchMode
	// refused a password prompt, host key not trusted, no key/agent). It is
	// terminal: the reconnect loop never spams ssh against a host that cannot
	// authenticate.
	ErrSSHAuth = errors.New("sshconn: ssh authentication failed")

	// ErrProtocolIncompatible marks a host whose appwire protocol is not the
	// controller's. Terminal, and refused before any bridge process starts.
	ErrProtocolIncompatible = errors.New("sshconn: host protocol incompatible")

	// ErrUnsupportedHost marks a host whose os/arch has no evener build.
	// Terminal; no deploy is attempted.
	ErrUnsupportedHost = errors.New("sshconn: unsupported host")

	// ErrLaunchContract marks a host binary that does not advertise the
	// launch flags the hub requires (today: api-log). In 04a this is surfaced;
	// the version-match deploy that fixes it is 04b.
	ErrLaunchContract = errors.New("sshconn: host launch contract not satisfied")

	// ErrVersionMismatch marks a host whose on-disk build is not the build this
	// controller requires, on a Manager with no build source to install instead.
	// Version auto-match exists to make the attached runtime match the
	// controller: with nothing to deploy there is no way to resolve the
	// difference, so attaching would silently serve the wrong build. Terminal,
	// like the other contract refusals.
	ErrVersionMismatch = errors.New("sshconn: host version does not match the controller")

	// ErrPreflightDecode marks preflight output (launch-check JSON or the
	// environment probe) that could not be parsed. Treated as an incompatible
	// host: terminal.
	ErrPreflightDecode = errors.New("sshconn: preflight output unparseable")

	// ErrDeploy marks a failed cross-compile, push, or deploy-target
	// resolution. The existing host binary is left untouched: the push writes a
	// temp name and mv's it into place, so an interrupted deploy never leaves a
	// truncated evener.
	ErrDeploy = errors.New("sshconn: deploy failed")

	// ErrRestart marks a failed hub restart. The manager stays disconnected and
	// the next Ensure retries; a hub that fails to start leaves hub.lock free.
	ErrRestart = errors.New("sshconn: hub restart failed")

	// ErrHostAddr marks a host whose configured hub address the controller
	// cannot use: malformed, not a loopback or wildcard bind, or a config_path
	// with no address at all. Probing or killing through such an address could
	// hit the wrong service, so it is refused before any ssh command runs.
	// Terminal: no retry can make a configuration error go away.
	ErrHostAddr = errors.New("sshconn: host hub address unusable")

	// ErrManagerClosed marks an Ensure on a Manager whose Close has already run.
	// Close is terminal for the Manager: the base context is canceled for good,
	// so no channel created after it would ever be supervised.
	ErrManagerClosed = errors.New("sshconn: manager closed")
)
