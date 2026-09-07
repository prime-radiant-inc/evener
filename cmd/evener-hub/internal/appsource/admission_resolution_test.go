package appsource

import (
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
)

func TestPairedRelayAdmissionSingleInventorySnapshot(t *testing.T) {
	for _, child := range []bool{false, true} {
		reads := 0
		entry := rendezvous.Entry{SourceID: "local", ThreadID: "stable", SessionID: "current", WorkspaceRef: "local:stable", Endpoint: "http://127.0.0.1", Protocol: appwire.ProtocolVersion}
		source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
			reads++
			if reads > 1 {
				return nil
			}
			return []LocalDaemonEntry{{Entry: entry}, {Entry: entry, SessionID: "child", OwnerSessionID: "current", ReadOnlyAlias: true}}
		}, nil)
		target, wantOwner := "current", "local:stable"
		if child {
			target, wantOwner = "child", "local:child"
		}
		canonical, owner, err := source.ResolveRelaySessionWithAdmission(appwire.ThreadReadParams{Ref: "local:" + target})
		if err != nil || canonical.String() != "local:stable" || owner != wantOwner || reads != 1 {
			t.Fatalf("paired canonical=%s owner=%s reads=%d err=%v", canonical.String(), owner, reads, err)
		}
	}
}

func TestCanonicalRelayRejectsReassociatedCurrentAlias(t *testing.T) {
	entry := rendezvous.Entry{SourceID: "local", ThreadID: "stable", SessionID: "current", WorkspaceRef: "local:stable", Endpoint: "http://127.0.0.1", Protocol: appwire.ProtocolVersion}
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry { return []LocalDaemonEntry{{Entry: entry}} }, nil)
	canonical, _, err := source.ResolveRelaySessionWithAdmission(appwire.ThreadReadParams{Ref: "local:current"})
	if err != nil {
		t.Fatal(err)
	}
	// The old canonical string now happens to be a current-ID alias for a
	// different workspace. It must not redirect an already resolved request.
	entry.ThreadID = "other"
	entry.SessionID = "stable"
	entry.WorkspaceRef = "local:other"
	if lease, err := source.AcquireRelaySession(canonical); err == nil {
		lease.Close()
		t.Fatal("canonical lookup changed semantic owner")
	}
	entry.ThreadID = "stable"
	entry.SessionID = "new-current"
	entry.WorkspaceRef = "local:stable"
	lease, err := source.AcquireRelaySession(canonical)
	if err != nil {
		t.Fatalf("same-owner refresh rejected: %v", err)
	}
	lease.Close()
}
