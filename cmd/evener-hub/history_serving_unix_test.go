//go:build unix

package hub

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/internal/transcriptindex"
	"primeradiant.com/evener/llm"
)

func TestHistoryServingLockedFallbackIsTypedFailure(t *testing.T) {
	cfg, entry, path := seedIndexedPastSession(t, execution("saved", schema.NewTurn(schema.TurnUserInput, llm.User("saved-only"))))
	lock, err := os.OpenFile(filepath.Join(transcriptindex.DirFor(path), "lock"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	sources := appsource.NewRegistry()
	source := &pastFallbackRelaySource{canceled: make(chan struct{}), readErr: appwire.SessionUnavailable("fixture")}
	sources.Add(source)
	app := newHubAppServer(cfg, sources)
	hub := httptest.NewServer(http.HandlerFunc(app.ServeWebSocket))
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	params := appwire.ThreadReadParams{Ref: "local:" + entry.Meta.ID, IncludeTurns: true}
	var response appwire.ThreadHistoryReadResponse
	err = client.Request(t.Context(), appwire.MethodThreadHistoryRead, params, &response)
	wire, ok := wireErrorFromError(err)
	if !ok || !wireErrorInfoIs(wire.Data, appwire.ErrorTranscriptIndexLocked) {
		t.Fatalf("locked index returned %T %v", err, err)
	}
	if response.ServedFrom != "" || response.AsOf != "" || len(response.Thread.Turns) != 0 {
		t.Fatalf("failed read invented a serving view: %+v", response)
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if err := client.Request(t.Context(), appwire.MethodThreadHistoryRead, params, &response); err != nil {
		t.Fatal(err)
	}
	if response.ServedFrom != "saved-index" || response.FallbackReason != "live-read-failed" || len(response.Thread.Turns) != 1 {
		t.Fatalf("unlocked retry=%+v", response)
	}
}
