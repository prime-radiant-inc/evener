package hub

import (
	"errors"
	"fmt"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// restartRequiredDaemon only uses an authenticated probe's mismatch verdict.
// A rendezvous file or a live PID alone cannot establish daemon identity.
func restartRequiredDaemon(cfg hubcore.WebConfig, ref, threadID string) (hubcore.LiveEntry, bool) {
	if ref != "" {
		parsed, err := appwire.ParseRef(ref)
		if err != nil || parsed.SourceID != "local" {
			return hubcore.LiveEntry{}, false
		}
		threadID = parsed.ThreadID
	}
	if cfg.Roster == nil {
		return hubcore.LiveEntry{}, false
	}
	entry, ok := cfg.Roster.Find(threadID)
	return entry, ok && !entry.Crashed && entry.Status == appwire.ThreadStatusRestartRequired
}

func daemonRestartRequiredError(cfg hubcore.WebConfig, ref, threadID, mutationID string) error {
	entry, ok := restartRequiredDaemon(cfg, ref, threadID)
	if !ok {
		return nil
	}
	wire := appwire.WireError{Code: appwire.CodeConflict, Message: fmt.Sprintf("Session restart required: daemon pid %d speaks %s; this hub requires %s. Stop the daemon, then resume this session. Stopping interrupts active work.", entry.PID, entry.Protocol, appwire.ProtocolVersion), Data: appwire.ErrorData{EvenerErrorInfo: appwire.ErrorConflict, Cause: "daemonRestartRequired"}}
	if mutationID != "" {
		return restartRequiredMutationError(wire, mutationID)
	}
	return wire
}

func isDaemonRestartRequiredError(err error) bool {
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		return false
	}
	switch data := wire.Data.(type) {
	case appwire.ErrorData:
		return data.Cause == "daemonRestartRequired"
	case map[string]any:
		return data["cause"] == "daemonRestartRequired"
	default:
		return false
	}
}

// A retry may name a mutation accepted before the protocol upgrade. Without
// its daemon's receipt history, the hub cannot claim that ID was rejected.
func restartRequiredMutationError(err error, mutationID string) error {
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		return err
	}
	switch data := wire.Data.(type) {
	case appwire.ErrorData:
		data.ClientMutationID = mutationID
		data.MutationOutcome = appwire.MutationOutcomeUnknown
		data.RetryDisposition = appwire.RetryDispositionBlocked
		wire.Data = data
	case map[string]any:
		copy := make(map[string]any, len(data)+3)
		for key, value := range data {
			copy[key] = value
		}
		copy["clientMutationId"] = mutationID
		copy["mutationOutcome"] = string(appwire.MutationOutcomeUnknown)
		copy["retryDisposition"] = string(appwire.RetryDispositionBlocked)
		wire.Data = copy
	}
	return wire
}
