package appsource

import (
	"context"
	"errors"
	"io"
	"net"
	"syscall"
	"testing"

	"primeradiant.com/evener/appwire"
)

// TestLocalDaemonDialErrorNil covers the nil error path (line 629-630).
func TestLocalDaemonDialErrorNil(t *testing.T) {
	if err := localDaemonDialError(nil); err != nil {
		t.Fatalf("localDaemonDialError(nil) should return nil, got %v", err)
	}
}

// TestLocalDaemonDialErrorConnRefused covers the ECONNREFUSED path.
func TestLocalDaemonDialErrorConnRefused(t *testing.T) {
	err := localDaemonDialError(syscall.ECONNREFUSED)
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
		t.Fatalf("ECONNREFUSED should map to SessionUnavailable, got %v", err)
	}
}

// TestLocalDaemonDialErrorConnReset covers the ECONNRESET path.
func TestLocalDaemonDialErrorConnReset(t *testing.T) {
	err := localDaemonDialError(syscall.ECONNRESET)
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
		t.Fatalf("ECONNRESET should map to SessionUnavailable, got %v", err)
	}
}

// TestLocalDaemonDialErrorEPIPE covers the EPIPE path.
func TestLocalDaemonDialErrorEPIPE(t *testing.T) {
	err := localDaemonDialError(syscall.EPIPE)
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
		t.Fatalf("EPIPE should map to SessionUnavailable, got %v", err)
	}
}

// TestLocalDaemonDialErrorEOF covers the io.EOF path.
func TestLocalDaemonDialErrorEOF(t *testing.T) {
	err := localDaemonDialError(io.EOF)
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
		t.Fatalf("EOF should map to SessionUnavailable, got %v", err)
	}
}

// TestLocalDaemonDialErrorUnexpectedEOF covers the io.ErrUnexpectedEOF path.
func TestLocalDaemonDialErrorUnexpectedEOF(t *testing.T) {
	err := localDaemonDialError(io.ErrUnexpectedEOF)
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
		t.Fatalf("ErrUnexpectedEOF should map to SessionUnavailable, got %v", err)
	}
}

// TestLocalDaemonDialErrorDeadlineExceeded covers the context.DeadlineExceeded path.
func TestLocalDaemonDialErrorDeadlineExceeded(t *testing.T) {
	err := localDaemonDialError(context.DeadlineExceeded)
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
		t.Fatalf("DeadlineExceeded should map to SessionUnavailable, got %v", err)
	}
}

// TestLocalDaemonDialErrorNetTimeout covers the net.Error.Timeout() path.
func TestLocalDaemonDialErrorNetTimeout(t *testing.T) {
	err := localDaemonDialError(fakeTimeoutError{msg: "i/o timeout"})
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
		t.Fatalf("net timeout should map to SessionUnavailable, got %v", err)
	}
}

// TestLocalDaemonDialErrorStringMatchConnectionReset covers the string match
// fallback for "connection reset".
func TestLocalDaemonDialErrorStringMatchConnectionReset(t *testing.T) {
	err := localDaemonDialError(errors.New("connection reset by peer"))
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
		t.Fatalf("string 'connection reset' should map to SessionUnavailable, got %v", err)
	}
}

// TestLocalDaemonDialErrorStringMatchBrokenPipe covers the string match for
// "broken pipe".
func TestLocalDaemonDialErrorStringMatchBrokenPipe(t *testing.T) {
	err := localDaemonDialError(errors.New("broken pipe"))
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
		t.Fatalf("string 'broken pipe' should map to SessionUnavailable, got %v", err)
	}
}

// TestLocalDaemonDialErrorStringMatchClosedNetwork covers the string match
// for "use of closed network connection".
func TestLocalDaemonDialErrorStringMatchClosedNetwork(t *testing.T) {
	err := localDaemonDialError(errors.New("use of closed network connection"))
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
		t.Fatalf("string 'use of closed network connection' should map to SessionUnavailable, got %v", err)
	}
}

// TestLocalDaemonDialErrorStringMatchIOTimeout covers the string match for
// "i/o timeout".
func TestLocalDaemonDialErrorStringMatchIOTimeout(t *testing.T) {
	err := localDaemonDialError(errors.New("i/o timeout"))
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
		t.Fatalf("string 'i/o timeout' should map to SessionUnavailable, got %v", err)
	}
}

// TestLocalDaemonDialErrorPassThrough covers the fallback path where the
// error doesn't match any known pattern and is returned as-is (line 679).
func TestLocalDaemonDialErrorPassThrough(t *testing.T) {
	original := errors.New("some unknown error")
	err := localDaemonDialError(original)
	if !errors.Is(err, original) {
		t.Fatalf("unknown error should pass through, got %v", err)
	}
}

// TestLocalDaemonCallErrorWireErrorNonInternal covers the path where a
// WireError with non-InternalError code is returned as-is (line 684-685).
func TestLocalDaemonCallErrorWireErrorNonInternal(t *testing.T) {
	original := appwire.InvalidParams("bad params")
	err := localDaemonCallError(original)
	if !errors.Is(err, original) {
		t.Fatalf("non-InternalError WireError should pass through, got %v", err)
	}
}

// TestLocalDaemonCallErrorContextCanceled covers the context.Canceled path
// (line 688-689).
func TestLocalDaemonCallErrorContextCanceled(t *testing.T) {
	err := localDaemonCallError(context.Canceled)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("context.Canceled should pass through, got %v", err)
	}
}

// TestLocalDaemonCallErrorContextDeadlineExceeded covers the
// context.DeadlineExceeded path (line 688-689).
func TestLocalDaemonCallErrorContextDeadlineExceeded(t *testing.T) {
	err := localDaemonCallError(context.DeadlineExceeded)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("context.DeadlineExceeded should pass through, got %v", err)
	}
}

// TestLocalDaemonCallErrorNonWireNonContext covers the path where a non-wire,
// non-context error falls through to localDaemonDialError (line 691).
func TestLocalDaemonCallErrorNonWireNonContext(t *testing.T) {
	err := localDaemonCallError(syscall.ECONNREFUSED)
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
		t.Fatalf("ECONNREFUSED should be mapped to SessionUnavailable via localDaemonDialError, got %v", err)
	}
}

// TestLocalDaemonCallErrorWireInternalFailedToGetReader covers the string
// match for "failed to get reader" in a wire error (line 694-701).
func TestLocalDaemonCallErrorWireInternalFailedToGetReader(t *testing.T) {
	original := appwire.WireError{
		Code:    appwire.CodeInternalError,
		Message: "failed to get reader: connection closed",
	}
	err := localDaemonCallError(original)
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
		t.Fatalf("wire error with 'failed to get reader' should map to SessionUnavailable, got %v", err)
	}
}

// TestLocalDaemonCallErrorWireInternalWebsocket covers the string match for
// "websocket" in a wire error.
func TestLocalDaemonCallErrorWireInternalWebsocket(t *testing.T) {
	original := appwire.WireError{
		Code:    appwire.CodeInternalError,
		Message: "websocket: connection closed",
	}
	err := localDaemonCallError(original)
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
		t.Fatalf("wire error with 'websocket' should map to SessionUnavailable, got %v", err)
	}
}

// TestLocalDaemonCallErrorWireInternalEOF covers the string match for "eof".
func TestLocalDaemonCallErrorWireInternalEOF(t *testing.T) {
	original := appwire.WireError{
		Code:    appwire.CodeInternalError,
		Message: "unexpected eof during read",
	}
	err := localDaemonCallError(original)
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
		t.Fatalf("wire error with 'eof' should map to SessionUnavailable, got %v", err)
	}
}

// TestLocalDaemonCallErrorWireInternalConnectionReset covers the string
// match for "connection reset" in a wire error.
func TestLocalDaemonCallErrorWireInternalConnectionReset(t *testing.T) {
	original := appwire.WireError{
		Code:    appwire.CodeInternalError,
		Message: "connection reset by peer",
	}
	err := localDaemonCallError(original)
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
		t.Fatalf("wire error with 'connection reset' should map to SessionUnavailable, got %v", err)
	}
}

// TestLocalDaemonCallErrorWireInternalBrokenPipe covers the string match for
// "broken pipe" in a wire error.
func TestLocalDaemonCallErrorWireInternalBrokenPipe(t *testing.T) {
	original := appwire.WireError{
		Code:    appwire.CodeInternalError,
		Message: "broken pipe",
	}
	err := localDaemonCallError(original)
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
		t.Fatalf("wire error with 'broken pipe' should map to SessionUnavailable, got %v", err)
	}
}

// TestLocalDaemonCallErrorWireInternalClosedNetwork covers the string match
// for "use of closed network connection" in a wire error.
func TestLocalDaemonCallErrorWireInternalClosedNetwork(t *testing.T) {
	original := appwire.WireError{
		Code:    appwire.CodeInternalError,
		Message: "use of closed network connection",
	}
	err := localDaemonCallError(original)
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
		t.Fatalf("wire error with 'use of closed network connection' should map to SessionUnavailable, got %v", err)
	}
}

// TestLocalDaemonCallErrorWireInternalIOTimeout covers the string match for
// "i/o timeout" in a wire error.
func TestLocalDaemonCallErrorWireInternalIOTimeout(t *testing.T) {
	original := appwire.WireError{
		Code:    appwire.CodeInternalError,
		Message: "i/o timeout",
	}
	err := localDaemonCallError(original)
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
		t.Fatalf("wire error with 'i/o timeout' should map to SessionUnavailable, got %v", err)
	}
}

// TestLocalDaemonCallErrorWireInternalNoMatch covers the path where a wire
// error with InternalError code doesn't match any string patterns (line 703).
func TestLocalDaemonCallErrorWireInternalNoMatch(t *testing.T) {
	original := appwire.WireError{
		Code:    appwire.CodeInternalError,
		Message: "some other internal error",
	}
	err := localDaemonCallError(original)
	if !errors.Is(err, original) {
		t.Fatalf("non-matching wire error should pass through, got %v", err)
	}
}

// TestLocalDaemonInitializeErrorWireNonInternal covers the path where the
// mapped error is a WireError with non-InternalError code (lines 731-732).
func TestLocalDaemonInitializeErrorWireNonInternal(t *testing.T) {
	original := appwire.InvalidParams("bad params")
	err := localDaemonInitializeError(original)
	if !errors.Is(err, original) {
		t.Fatalf("non-InternalError should pass through, got %v", err)
	}
}

// TestLocalDaemonInitializeErrorNonWire covers the path where the mapped
// error is not a WireError, falling through to localDaemonDialError (line 734).
func TestLocalDaemonInitializeErrorNonWire(t *testing.T) {
	err := localDaemonInitializeError(syscall.ECONNREFUSED)
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
		t.Fatalf("non-wire error should be mapped via localDaemonDialError, got %v", err)
	}
}

// TestLocalDaemonSubscribeReadErrorWireNonInternal covers the path where the
// mapped error is a WireError with non-InternalError code (lines 740-741).
func TestLocalDaemonSubscribeReadErrorWireNonInternal(t *testing.T) {
	original := appwire.InvalidParams("bad params")
	err := localDaemonSubscribeReadError(original)
	if !errors.Is(err, original) {
		t.Fatalf("non-InternalError should pass through, got %v", err)
	}
}

// TestLocalDaemonSubscribeReadErrorNonWire covers the path where the mapped
// error is not a WireError, falling through to localDaemonDialError (line 743).
func TestLocalDaemonSubscribeReadErrorNonWire(t *testing.T) {
	err := localDaemonSubscribeReadError(syscall.ECONNREFUSED)
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
		t.Fatalf("non-wire error should be mapped via localDaemonDialError, got %v", err)
	}
}

// TestLocalThreadLessUpdatedAtDifferent covers the updatedAt comparison
// path.
func TestLocalThreadLessUpdatedAtDifferent(t *testing.T) {
	a := appwire.Thread{ID: "a", UpdatedAt: 200, CreatedAt: 100}
	b := appwire.Thread{ID: "b", UpdatedAt: 100, CreatedAt: 50}
	if !localThreadLess(a, b) {
		t.Fatal("a with higher UpdatedAt should sort before b")
	}
	if localThreadLess(b, a) {
		t.Fatal("b with lower UpdatedAt should not sort before a")
	}
}

// TestLocalThreadLessCreatedAtDifferent covers the createdAt comparison path
// (when updatedAt is equal).
func TestLocalThreadLessCreatedAtDifferent(t *testing.T) {
	a := appwire.Thread{ID: "a", UpdatedAt: 100, CreatedAt: 200}
	b := appwire.Thread{ID: "b", UpdatedAt: 100, CreatedAt: 100}
	if !localThreadLess(a, b) {
		t.Fatal("a with higher CreatedAt should sort before b when UpdatedAt is equal")
	}
}

// TestLocalThreadLessTitleDifferent covers the title comparison path (when
// updatedAt and createdAt are equal).
func TestLocalThreadLessTitleDifferent(t *testing.T) {
	a := appwire.Thread{ID: "a", Name: "Alpha", UpdatedAt: 100, CreatedAt: 100}
	b := appwire.Thread{ID: "b", Name: "Beta", UpdatedAt: 100, CreatedAt: 100}
	if !localThreadLess(a, b) {
		t.Fatal("a with name 'Alpha' should sort before b with name 'Beta'")
	}
}

// TestLocalThreadLessIDFallback covers the ID fallback comparison path.
func TestLocalThreadLessIDFallback(t *testing.T) {
	a := appwire.Thread{ID: "aaa", UpdatedAt: 100, CreatedAt: 100}
	b := appwire.Thread{ID: "zzz", UpdatedAt: 100, CreatedAt: 100}
	if !localThreadLess(a, b) {
		t.Fatal("a with ID 'aaa' should sort before b with ID 'zzz'")
	}
}

// TestLocalThreadLessSessionIDFallback covers the SessionID fallback when ID
// is empty.
func TestLocalThreadLessSessionIDFallback(t *testing.T) {
	a := appwire.Thread{SessionID: "aaa", UpdatedAt: 100, CreatedAt: 100}
	b := appwire.Thread{SessionID: "zzz", UpdatedAt: 100, CreatedAt: 100}
	if !localThreadLess(a, b) {
		t.Fatal("a with SessionID 'aaa' should sort before b with SessionID 'zzz'")
	}
}

// TestLocalThreadUpdatedAt covers the localThreadUpdatedAt helper.
func TestLocalThreadUpdatedAt(t *testing.T) {
	if got := localThreadUpdatedAt(appwire.Thread{}); got != 0 {
		t.Fatalf("empty thread UpdatedAt should be 0, got %d", got)
	}
	if got := localThreadUpdatedAt(appwire.Thread{UpdatedAt: 500}); got != 500 {
		t.Fatalf("UpdatedAt=500 should return 500, got %d", got)
	}
	// When UpdatedAt is 0 but CreatedAt is set, use CreatedAt
	if got := localThreadUpdatedAt(appwire.Thread{CreatedAt: 300}); got != 300 {
		t.Fatalf("UpdatedAt=0, CreatedAt=300 should return 300, got %d", got)
	}
}

// TestLocalThreadCreatedAt covers the localThreadCreatedAt helper.
func TestLocalThreadCreatedAt(t *testing.T) {
	if got := localThreadCreatedAt(appwire.Thread{}); got != 0 {
		t.Fatalf("empty thread CreatedAt should be 0, got %d", got)
	}
	if got := localThreadCreatedAt(appwire.Thread{CreatedAt: 500}); got != 500 {
		t.Fatalf("CreatedAt=500 should return 500, got %d", got)
	}
	// When CreatedAt is 0 but UpdatedAt is set, use UpdatedAt
	if got := localThreadCreatedAt(appwire.Thread{UpdatedAt: 300}); got != 300 {
		t.Fatalf("CreatedAt=0, UpdatedAt=300 should return 300, got %d", got)
	}
}

// TestLocalThreadTitle covers the localThreadTitle helper.
func TestLocalThreadTitle(t *testing.T) {
	if got := localThreadTitle(appwire.Thread{}); got != "" {
		t.Fatalf("empty thread title should be empty, got %q", got)
	}
	if got := localThreadTitle(appwire.Thread{Name: "My Thread"}); got != "My Thread" {
		t.Fatalf("Name='My Thread' should return 'My Thread', got %q", got)
	}
}

// TestFirstLocalNonEmpty covers the firstLocalNonEmpty helper.
func TestFirstLocalNonEmpty(t *testing.T) {
	if got := firstLocalNonEmpty("", ""); got != "" {
		t.Fatalf("both empty should return empty, got %q", got)
	}
	if got := firstLocalNonEmpty("first", ""); got != "first" {
		t.Fatalf("first non-empty should return 'first', got %q", got)
	}
	if got := firstLocalNonEmpty("", "second"); got != "second" {
		t.Fatalf("second when first is empty should return 'second', got %q", got)
	}
	if got := firstLocalNonEmpty("first", "second"); got != "first" {
		t.Fatalf("both non-empty should return first, got %q", got)
	}
}

// TestCompareLocalOrderText covers the compareLocalOrderText helper.
func TestCompareLocalOrderText(t *testing.T) {
	if got := compareLocalOrderText("", ""); got != 0 {
		t.Fatalf("both empty should return 0, got %d", got)
	}
	// Empty string sorts before non-empty
	if got := compareLocalOrderText("a", ""); got <= 0 {
		t.Fatalf("non-empty vs empty should return > 0, got %d", got)
	}
	if got := compareLocalOrderText("", "b"); got >= 0 {
		t.Fatalf("empty vs non-empty should return < 0, got %d", got)
	}
	if got := compareLocalOrderText("abc", "abc"); got != 0 {
		t.Fatalf("equal strings should return 0, got %d", got)
	}
	if got := compareLocalOrderText("abc", "abd"); got >= 0 {
		t.Fatalf("abc < abd should return < 0, got %d", got)
	}
	// Case-insensitive comparison first, then case-sensitive
	if got := compareLocalOrderText("ABC", "abc"); got == 0 {
		t.Fatal("ABC vs abc: case-insensitive equal, then case-sensitive should differ")
	}
}

// Ensure net.Error is used for the fakeTimeoutError
var _ net.Error = fakeTimeoutError{}
