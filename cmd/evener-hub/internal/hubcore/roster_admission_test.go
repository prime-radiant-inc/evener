package hubcore

import (
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
)

func TestRestartRequiredRootRef(t *testing.T) {
	owner := LiveEntry{Entry: rendezvous.Entry{PID: 1, SessionID: "current", ThreadID: "current", WorkspaceRef: "local:saved"}, SessionID: "current", Status: appwire.ThreadStatusRestartRequired}
	for _, ref := range []string{"local:current", "local:saved"} {
		roster := NewRosterWithEntries(owner)
		if got, ok := roster.RestartRequiredRootRef(ref); !ok || got != "local:saved" {
			t.Fatalf("%s: ref=%s ok=%v", ref, got, ok)
		}
	}
	for _, scenario := range []string{"missing", "remote", "duplicate", "overlap", "cross-alias", "unconfirmed", "discovery", "compatible", "crashed", "descendant"} {
		t.Run(scenario, func(t *testing.T) {
			owner := owner
			roster := NewRosterWithEntries(owner)
			ref := "local:current"
			switch scenario {
			case "missing":
				ref = "local:missing"
			case "remote":
				ref = "remote:current"
			case "duplicate", "overlap", "cross-alias":
				other := owner
				other.PID = 2
				if scenario == "overlap" {
					other.SessionID = "other"
					other.Entry.SessionID = "other"
					other.ThreadID = "other"
				}
				if scenario == "cross-alias" {
					other.SessionID, other.Entry.SessionID, other.ThreadID = "saved", "saved", "saved"
					other.WorkspaceRef = "local:other"
				}
				roster.byPID[2] = other
			case "unconfirmed":
				roster.unconfirmed = []rendezvous.Entry{{PID: 2}}
			case "discovery":
				roster.ownershipErr = errors.New("unreadable discovery")
			case "compatible":
				owner.Status = appwire.ThreadStatusIdle
				roster = NewRosterWithEntries(owner)
			case "crashed":
				owner.Crashed = true
				roster = NewRosterWithEntries(owner)
			case "descendant":
				owner.RunningSubagentIDs = []string{"child"}
				roster = NewRosterWithEntries(owner)
				ref = "local:child"
			}
			if got, ok := roster.RestartRequiredRootRef(ref); ok {
				t.Fatalf("unexpected root admission %s", got)
			}
		})
	}
}
