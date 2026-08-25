package llm

import (
	"context"
	"net"
	"net/url"
	"testing"

	apilog "primeradiant.com/evener/llm/apilog"
)

// TestCovAPITimeoutSourceForTransportNetOpError covers the net.OpError unwrap
// path (adapter_timeout.go line 60-61). When the transport error wraps a
// net.OpError whose inner error is context.DeadlineExceeded, the function
// should return APITimeoutTransport.
func TestCovAPITimeoutSourceForTransportNetOpError(t *testing.T) {
	// Construct a *net.OpError whose Err is context.DeadlineExceeded.
	opErr := &net.OpError{
		Op:  "read",
		Net: "tcp",
		Err: context.DeadlineExceeded,
	}
	// Wrap in a *url.Error as the standard transport does.
	urlErr := &url.Error{
		Op:  "Post",
		URL: "https://provider.test/api",
		Err: opErr,
	}
	got := APITimeoutSourceForTransport(context.Background(), context.Background(), urlErr)
	if got != APITimeoutTransport {
		t.Fatalf("APITimeoutSourceForTransport(net.OpError→DeadlineExceeded) = %q, want %q", got, APITimeoutTransport)
	}
}

// TestCovClassifyAPIAttemptOutcomeCallerCancel covers the caller-cancel
// path, ensuring the outcome classification is correct.
func TestCovClassifyAPIAttemptOutcomeCallerCancel(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	owner := APIAttemptContextOwnership{Parent: parent}
	got := ClassifyAPIAttemptOutcome(owner, 0, nil, nil, nil)
	if got != apilog.AttemptCallerCancel {
		t.Fatalf("ClassifyAPIAttemptOutcome(canceled parent) = %q, want %q", got, apilog.AttemptCallerCancel)
	}
}
