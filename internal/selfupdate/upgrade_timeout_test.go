package selfupdate

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestUpgradeStalledDownloadDoesNotHangForever proves the apply path cannot
// hold hubUpdateMu indefinitely when the release server accepts the
// connection and then never answers: Upgrade with no caller-supplied client
// must still bound the download. Fails today because Upgrade falls back to
// http.DefaultClient, which has no timeout.
func TestUpgradeStalledDownloadDoesNotHangForever(t *testing.T) {
	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Accept the request, then never write a byte -- the client must
		// give up on its own. Waiting on the request context (not select{})
		// so the handler exits when the test server closes.
		<-r.Context().Done()
	}))
	t.Cleanup(stalled.Close)

	previous := defaultUpgradeTimeout
	defaultUpgradeTimeout = 5 * time.Second
	t.Cleanup(func() { defaultUpgradeTimeout = previous })

	prefix := t.TempDir()
	done := make(chan error, 1)
	go func() {
		_, err := Upgrade(t.Context(), Options{
			Requested:      "snapshot",
			CurrentChannel: "snapshot",
			Prefix:         prefix,
			GOOS:           "linux",
			GOARCH:         "amd64",
			RepoURL:        stalled.URL,
		})
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Upgrade against a stalled server succeeded, want a timeout error")
		}
	case <-time.After(60 * time.Second):
		t.Fatal("Upgrade hung for 60s against a stalled server: no download deadline")
	}
}

// TestDefaultUpgradeTimeoutIsBounded pins the shipped default: a caller that
// passes no HTTP client must still get a finite download deadline.
func TestDefaultUpgradeTimeoutIsBounded(t *testing.T) {
	if defaultUpgradeTimeout <= 0 {
		t.Fatalf("defaultUpgradeTimeout = %v, want a positive download deadline", defaultUpgradeTimeout)
	}
}
