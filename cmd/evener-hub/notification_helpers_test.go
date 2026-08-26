package hub

import (
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
)

// waitForNotification blocks until the client's next notification arrives (or
// t.Fatal on timeout) and returns its method name.
func waitForNotification(t *testing.T, client *appwire.Client) string {
	t.Helper()
	select {
	case got := <-client.Notifications():
		return got.Method
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for a notification")
		return ""
	}
}

// assertSingleNotification waits for the client's next notification and
// asserts its method is wantMethod, then proves no SECOND notification
// follows before an ordering sentinel broadcast right after: seeing the
// sentinel next (rather than a repeat of wantMethod) rules out a
// double-broadcast without a race-prone sleep-based check.
func assertSingleNotification(t *testing.T, client *appwire.Client, server *appserver.Server, wantMethod string) {
	t.Helper()
	if got := waitForNotification(t, client); got != wantMethod {
		t.Fatalf("method=%q, want %q", got, wantMethod)
	}
	server.BroadcastAll("test/sentinel", nil)
	if got := waitForNotification(t, client); got != "test/sentinel" {
		t.Fatalf("got a second notification %q before the sentinel; want exactly one %q", got, wantMethod)
	}
}

// assertNoNotification proves nothing has reached client yet: an ordering
// sentinel is broadcast immediately, so receiving anything else first would
// mean forbiddenMethod (or something else) was already pending.
func assertNoNotification(t *testing.T, client *appwire.Client, server *appserver.Server, forbiddenMethod string) {
	t.Helper()
	server.BroadcastAll("test/sentinel", nil)
	if got := waitForNotification(t, client); got != "test/sentinel" {
		t.Fatalf("got notification %q before the sentinel; must not have broadcast %q here", got, forbiddenMethod)
	}
}
