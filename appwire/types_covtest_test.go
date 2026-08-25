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

// TestCovWSTransportPing pins the WebSocket boundary: Ping must pass its own
// connection and the caller's context through unchanged, and return the
// boundary error unchanged.
func TestCovWSTransportPing(t *testing.T) {
	oldPing := pingWebSocket
	t.Cleanup(func() { pingWebSocket = oldPing })

	conn := &websocket.Conn{}
	successCtx := context.WithValue(t.Context(), struct{}{}, "success")
	errorCtx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	wantErr := errors.New("ping failed")
	wantContexts := []context.Context{successCtx, errorCtx}
	calls := 0
	pingWebSocket = func(gotConn *websocket.Conn, gotCtx context.Context) error {
		if gotConn != conn {
			t.Fatalf("ping receiver = %p, want %p", gotConn, conn)
		}
		if calls >= len(wantContexts) {
			t.Fatalf("unexpected ping call %d", calls+1)
		}
		if gotCtx != wantContexts[calls] {
			t.Fatalf("ping context on call %d was replaced", calls+1)
		}
		calls++
		if gotCtx == errorCtx {
			return wantErr
		}
		return nil
	}

	tr := &WSTransport{conn: conn}
	if err := tr.Ping(successCtx); err != nil {
		t.Fatalf("Ping success: unexpected error: %v", err)
	}
	if err := tr.Ping(errorCtx); !errors.Is(err, wantErr) {
		t.Fatalf("Ping error = %v, want %v", err, wantErr)
	}
	if calls != 2 {
		t.Fatalf("ping calls = %d, want 2", calls)
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
