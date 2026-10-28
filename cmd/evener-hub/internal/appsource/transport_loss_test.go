package appsource

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"syscall"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
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

// closedWriteShapes are the write-side closed-connection failures every
// transport reports on the first write after its stream is gone.
func closedWriteShapes() map[string]error {
	return map[string]error{
		"io.ErrClosedPipe":        io.ErrClosedPipe,
		"os.ErrClosed":            os.ErrClosed,
		"wrapped os.ErrClosed":    &fs.PathError{Op: "write", Path: "|1", Err: os.ErrClosed},
		"net.ErrClosed":           &net.OpError{Op: "write", Err: net.ErrClosed},
		"appwire.ErrStreamClosed": fmt.Errorf("send: %w", appwire.ErrStreamClosed),
	}
}

// failingWriteTransport completes the initialize handshake, then fails every
// later send with sendErr.
func failingWriteTransport(sendErr error) *scriptedAppwireTransport {
	var transport *scriptedAppwireTransport
	transport = newScriptedAppwireTransport(func(_ context.Context, msg appwire.Message) error {
		if msg.Request == nil {
			return nil
		}
		if msg.Request.Method == appwire.MethodInitialize {
			transport.recv <- appwire.ResponseMessage(msg.Request.ID, appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion})
			return nil
		}
		return sendErr
	})
	return transport
}

func newClosedWriteLocalSource(sendErr error) *LocalDaemonSource {
	entry := rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://daemon", SourceID: "local", ThreadID: "th_1", SessionID: "sess_1"}
	local := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry { return []LocalDaemonEntry{{Entry: entry}} }, nil)
	local.dial = dialTransport(failingWriteTransport(sendErr))
	return local
}

func TestLocalDaemonTreatsClosedWritesAsTransportLoss(t *testing.T) {
	for name, sendErr := range closedWriteShapes() {
		t.Run(name, func(t *testing.T) {
			remote := NewRemoteHubSource("host", nil, nil)
			assertSessionUnavailable(t, remote.transportUnavailable(sendErr), "remote")

			_, err := newClosedWriteLocalSource(sendErr).StartTurn(context.Background(), appwire.TurnStartParams{
				ClientMutationID: "closed-write-id", ExpectedInstanceID: "sess_1", Ref: "local:th_1",
				Input: []appwire.InputItem{{Type: "text", Text: "hi"}},
			})
			assertMutationOutcomeUnknownAutomatic(t, err, "closed-write-id")

			_, err = newClosedWriteLocalSource(sendErr).ReadThread(context.Background(), appwire.ThreadReadParams{Ref: "local:th_1"})
			assertSessionUnavailable(t, err, "local read")
		})
	}
}

func TestLocalDaemonLeavesCallerCancellationRawMidMutation(t *testing.T) {
	_, err := newClosedWriteLocalSource(context.Canceled).StartTurn(context.Background(), appwire.TurnStartParams{
		ClientMutationID: "cancel-id", ExpectedInstanceID: "sess_1", Ref: "local:th_1",
		Input: []appwire.InputItem{{Type: "text", Text: "hi"}},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("StartTurn error = %v, want context.Canceled kept raw", err)
	}
}
