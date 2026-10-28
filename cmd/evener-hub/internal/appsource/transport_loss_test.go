package appsource

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"testing"
)

// transportLossShapes are the failures the local daemon (websocket) and remote
// hub (SSH stdio) sources both treat as the host being unreachable. Each source
// adds transport-specific shapes on top; this table is the part they share.
func transportLossShapes() map[string]error {
	return map[string]error{
		"ECONNREFUSED":     &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED},
		"ECONNRESET":       &net.OpError{Op: "read", Err: syscall.ECONNRESET},
		"EPIPE":            &net.OpError{Op: "write", Err: syscall.EPIPE},
		"EOF":              io.EOF,
		"unexpected EOF":   fmt.Errorf("recv: %w", io.ErrUnexpectedEOF),
		"deadline":         fmt.Errorf("dial failed: %w", context.DeadlineExceeded),
		"net timeout":      fakeTimeoutError{msg: "timed out"},
		"reset text":       errors.New("read tcp: connection reset by peer"),
		"broken pipe text": errors.New("write tcp: broken pipe"),
		"closed conn text": errors.New("use of closed network connection"),
		"i/o timeout text": errors.New("read tcp: i/o timeout"),
	}
}

func TestLocalAndRemoteSourcesShareTheTransportLossCore(t *testing.T) {
	remote := NewRemoteHubSource("host", nil, nil)
	for name, err := range transportLossShapes() {
		t.Run(name, func(t *testing.T) {
			assertSessionUnavailable(t, localDaemonDialError(err), "local "+name)
			assertSessionUnavailable(t, remote.transportUnavailable(err), "remote "+name)
		})
	}
}

func TestLocalAndRemoteSourcesLeaveApplicationErrorsRaw(t *testing.T) {
	remote := NewRemoteHubSource("host", nil, nil)
	app := errors.New("manifest rejected")
	if got := localDaemonDialError(app); !errors.Is(got, app) {
		t.Fatalf("local mapped an application error: %v", got)
	}
	if got := remote.transportUnavailable(app); !errors.Is(got, app) {
		t.Fatalf("remote mapped an application error: %v", got)
	}
}
