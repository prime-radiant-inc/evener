package appwire

import (
	"context"
	"errors"
	"testing"

	"github.com/coder/websocket"

	"primeradiant.com/evener/envvars"
)

// TestClientMutationTurnID covers ClientMutationTurnID (types.go:696), a pure
// format function with no existing test in the default build.
func TestCovClientMutationTurnID(t *testing.T) {
	cases := []struct {
		seq  uint64
		want string
	}{
		{0, "turn_m0"},
		{1, "turn_m1"},
		{42, "turn_m42"},
		{18446744073709551615, "turn_m18446744073709551615"},
	}
	for _, c := range cases {
		if got := ClientMutationTurnID(c.seq); got != c.want {
			t.Errorf("ClientMutationTurnID(%d) = %q, want %q", c.seq, got, c.want)
		}
	}
}

// TestCovWSTransportPing covers WSTransport.Ping (ws_transport.go:71). Ping
// delegates to the package-level pingWebSocket var, which is normally
// (*websocket.Conn).Ping. We substitute a fake to avoid a real WebSocket.
func TestCovWSTransportPing(t *testing.T) {
	// Success path.
	oldPing := pingWebSocket
	t.Cleanup(func() { pingWebSocket = oldPing })
	pingWebSocket = func(_ *websocket.Conn, _ context.Context) error {
		return nil
	}
	tr := &WSTransport{}
	if err := tr.Ping(context.Background()); err != nil {
		t.Fatalf("Ping success: unexpected error: %v", err)
	}

	// Error path.
	pingWebSocket = func(_ *websocket.Conn, _ context.Context) error {
		return errors.New("ping failed")
	}
	if err := tr.Ping(context.Background()); err == nil {
		t.Fatalf("Ping error: expected error, got nil")
	}
}

// TestCovRecorderStateRootFallback covers the third branch of
// recorderStateRoot (frame_recorder.go:94) where XDG_STATE_HOME is empty and
// frameRecorderHomeDir returns an error, falling through to the
// ./.local/state/evener relative path.
func TestCovRecorderStateRootFallback(t *testing.T) {
	t.Setenv(envvars.XDGStateHome.Name, "")
	oldHome := frameRecorderHomeDir
	t.Cleanup(func() { frameRecorderHomeDir = oldHome })
	frameRecorderHomeDir = func() (string, error) { return "", errors.New("no home") }

	got := recorderStateRoot()
	want := ".local/state/evener"
	if got != want {
		t.Fatalf("recorderStateRoot error fallback = %q, want %q", got, want)
	}
}
