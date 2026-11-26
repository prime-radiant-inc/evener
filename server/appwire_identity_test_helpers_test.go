package server

import "testing"

// setAppIdentity prepares and installs an empty-history AppWire identity for
// srv, failing the test if PrepareAppIdentity rejects the inputs.
func setAppIdentity(t testing.TB, srv *Server, sourceID, threadID string) {
	t.Helper()
	prepared, err := PrepareAppIdentity(sourceID, threadID, "")
	if err != nil {
		t.Fatalf("PrepareAppIdentity(%q, %q): %v", sourceID, threadID, err)
	}
	srv.ReplaceAppIdentity(prepared, nil)
}
