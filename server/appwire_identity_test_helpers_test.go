package server

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

// setAppIdentity prepares and installs an empty-history AppWire identity for
// srv, failing the test if PrepareAppIdentityForRef rejects the inputs.
func setAppIdentity(t testing.TB, srv *Server, sourceID, threadID string) {
	t.Helper()
	if sourceID == "" {
		sourceID = appwire.LocalSourceID
	}
	ref := appwire.Ref{SourceID: sourceID, ThreadID: threadID}.String()
	prepared, err := PrepareAppIdentityForRef(sourceID, threadID, ref, "")
	if err != nil {
		t.Fatalf("PrepareAppIdentityForRef(%q, %q, %q): %v", sourceID, threadID, ref, err)
	}
	srv.ReplaceAppIdentity(prepared, nil)
}
