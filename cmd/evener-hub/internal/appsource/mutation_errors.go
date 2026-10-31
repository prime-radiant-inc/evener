package appsource

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"syscall"

	"primeradiant.com/evener/appwire"
)

// mutationOutcomeUnknown is the internal-error refusal for a mutation whose
// response was lost: it may have applied, and disposition says whether the
// caller replays it automatically or waits for receipt reconciliation.
// clientMutationID is empty for a mutation that carries none.
func mutationOutcomeUnknown(message, clientMutationID string, disposition appwire.RetryDisposition) appwire.WireError {
	return appwire.WireError{
		Code:    appwire.CodeInternalError,
		Message: message,
		Data: appwire.ErrorData{
			EvenerErrorInfo:  appwire.ErrorMutationOutcomeUnknown,
			ClientMutationID: clientMutationID,
			MutationOutcome:  appwire.MutationOutcomeUnknown,
			RetryDisposition: disposition,
		},
	}
}

// remoteHubUnavailable is the host-unavailable refusal for the remote hub
// registered as id. detail says why, and is left out when empty.
func remoteHubUnavailable(id, detail string) appwire.WireError {
	message := "remote hub unavailable: " + id
	if detail != "" {
		message += ": " + detail
	}
	return appwire.SessionUnavailable(message)
}

// isTransportLoss reports the failure shapes every source's transport shares:
// the peer is gone (refused, reset, broken pipe, EOF), this end's write side is
// already closed (pipe, descriptor, network connection or appwire stream), a
// deadline passed, or a library wrapped one of those without exposing the
// sentinel. A closed write says the connection failed mid-call, not that the
// request was never sent, so a mutation that hits one is outcome-unknown. Each
// source adds the shapes only its own transport produces.
func isTransportLoss(err error) bool {
	if errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, os.ErrClosed) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, appwire.ErrStreamClosed) ||
		errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return isTransportLossText(strings.ToLower(err.Error()))
}

// isTransportLossText is the message-text fallback for transport errors that a
// library wraps without exposing a typed sentinel.
func isTransportLossText(lower string) bool {
	return strings.Contains(lower, "connection reset") ||
		strings.Contains(lower, "broken pipe") ||
		strings.Contains(lower, "use of closed network connection") ||
		strings.Contains(lower, "i/o timeout")
}
