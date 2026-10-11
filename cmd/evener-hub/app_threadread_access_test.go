package hub

import (
	"context"
	"testing"

	"primeradiant.com/evener/appwire"
)

// A past session's read carries the sandbox mode and network setting its meta
// persisted (S15), so the Session sheet's Access section reads the same after
// the session ends as while it ran.
func TestPastThreadReadCarriesThePersistedAccess(t *testing.T) {
	cfg, sessionID, _ := seedPastSessionWithTasks(t, nil)
	entry, ok := cfg.Past.Find(sessionID)
	if !ok {
		t.Fatal("past entry not found")
	}
	networkOff := false
	entry.Meta.Config.Sandbox = "restricted"
	entry.Meta.Config.SandboxNet = &networkOff

	thread, err := pastEntryThread(context.Background(), cfg, entry, false)
	if err != nil {
		t.Fatalf("pastEntryThread: %v", err)
	}
	want := appwire.ThreadAccess{Sandbox: "restricted", Network: false}
	if got := thread.Evener.Access; got == nil || *got != want {
		t.Fatalf("past access = %+v, want %+v", got, want)
	}

	entry.Meta.Config.Sandbox = ""
	entry.Meta.Config.SandboxNet = nil
	thread, err = pastEntryThread(context.Background(), cfg, entry, false)
	if err != nil {
		t.Fatalf("pastEntryThread: %v", err)
	}
	if got := thread.Evener.Access; got == nil || *got != (appwire.ThreadAccess{Sandbox: "off", Network: true}) {
		t.Fatalf("unsandboxed past access = %+v, want off with the network on", got)
	}
}
