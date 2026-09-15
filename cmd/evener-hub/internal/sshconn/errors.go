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

	// ErrSSHAuth names a non-interactive authentication refusal (BatchMode
	// refused a password prompt, host key not trusted, no key/agent).
	//
	// It is NOT terminal, and in production the manager classifies no failure as
	// ErrSSHAuth. ssh forwards a remote command's stderr onto the same stream as
	// its own diagnostics and forwards the remote command's exit status unchanged
	// — including 255, the status ssh(1) uses for its own failures — so neither
	// signal can prove that a refusal is ssh's own. An authentication-shaped
	// failure is therefore reported as the retryable ErrSSHStart and retried under
	// backoff rather than ending the reconnect loop for a host that may merely be
	// unreachable. The sentinel is retained for spec 04's error taxonomy and for
	// callers that match it; the one case the classifier attributes to ssh itself
	// is a failed Start, which never spawned a remote command and, in practice,
	// carries no diagnostic to match.
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

	// ErrPreflightDecode marks preflight output (launch-check JSON or the
	// environment probe) that could not be parsed. Treated as an incompatible
	// host: terminal.
	ErrPreflightDecode = errors.New("sshconn: preflight output unparseable")

	// ErrManagerClosed marks an Ensure on a Manager whose Close has already run.
	// Close is terminal for the Manager: the base context is canceled for good,
	// so no channel created after it would ever be supervised.
	ErrManagerClosed = errors.New("sshconn: manager closed")
)
