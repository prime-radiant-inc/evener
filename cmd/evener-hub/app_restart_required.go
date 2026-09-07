package hub

import (
	"context"
	"errors"
	"fmt"
	"maps"

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
	seen := make(map[string]bool)
	for !seen[threadID] {
		seen[threadID] = true
		if entry, ok := cfg.Roster.Find(threadID); ok && !entry.Crashed {
			return entry, entry.Status == appwire.ThreadStatusRestartRequired
		}
		workspaceRef := localAppRef(threadID)
		for _, entry := range cfg.Roster.List() {
			if !entry.Crashed && localSpawnWorkspaceRef(entry.Entry) == workspaceRef {
				return entry, entry.Status == appwire.ThreadStatusRestartRequired
			}
		}
		// A fork shares ancestry but not daemon ownership. Require the parent's
		// persisted delegate descriptor before following that ownership edge.
		child, ok := pastEntryForRead(cfg, appwire.ThreadReadParams{ThreadID: threadID})
		if !ok || child.Meta.ParentSessionID == "" {
			break
		}
		parent, ok := pastEntryForRead(cfg, appwire.ThreadReadParams{ThreadID: child.Meta.ParentSessionID})
		if !ok {
			break
		}
		delegates, _, err := pastEntryDelegateStatus(context.Background(), parent)
		if err != nil {
			break
		}
		owner := ""
		for _, delegate := range delegates {
			if delegate.ChildSessionID == threadID && delegate.OwnerSessionID == parent.Meta.ID {
				owner = delegate.OwnerSessionID
				break
			}
		}
		if owner == "" {
			break
		}
		threadID = owner
	}

	return hubcore.LiveEntry{}, false
}

// Refresh ownership before a local metadata write or before treating a missing
// daemon as proof that a mutation was not accepted. Successful delivery uses
// the live source directly and does not wait for an unrelated roster probe.
func refreshDaemonRestartRequiredError(cfg hubcore.WebConfig, ref, threadID, mutationID string) error {
	if ref != "" {
		parsed, err := appwire.ParseRef(ref)
		if err != nil {
			return appwire.InvalidParams(err.Error())
		}
		if parsed.SourceID != "local" {
			return nil
		}
	}
	if cfg.Roster != nil {
		hubRosterRefresh(cfg.Roster)
	}
	return daemonRestartRequiredError(cfg, ref, threadID, mutationID)
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
		updated := maps.Clone(data)
		updated["clientMutationId"] = mutationID
		updated["mutationOutcome"] = string(appwire.MutationOutcomeUnknown)
		updated["retryDisposition"] = string(appwire.RetryDispositionBlocked)
		wire.Data = updated
	}
	return wire
}
