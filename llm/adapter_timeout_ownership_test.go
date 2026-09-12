package llm

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"primeradiant.com/evener/llm/apilog"
)

func TestClientWithAdapterTimeout_HeaderTimeoutOwnership(t *testing.T) {
	for _, tc := range []struct {
		name    string
		caller  time.Duration
		adapter *AdapterTimeout
		owned   bool
	}{
		{"shorter caller", time.Millisecond, &AdapterTimeout{StreamRead: time.Hour}, false},
		{"equal caller", time.Millisecond, &AdapterTimeout{StreamRead: time.Millisecond}, false},
		{"connect only", time.Millisecond, &AdapterTimeout{Connect: time.Hour}, false},
		{"absent caller", 0, &AdapterTimeout{StreamRead: time.Millisecond}, true},
		{"tightened caller", time.Hour, &AdapterTimeout{StreamRead: time.Millisecond}, true},
		{"request equal caller", time.Millisecond, &AdapterTimeout{Request: time.Millisecond}, false},
		{"request tightens caller", time.Hour, &AdapterTimeout{Request: time.Millisecond}, true},
		{"disabled", time.Millisecond, &AdapterTimeout{}, false},
		{"negative", time.Millisecond, &AdapterTimeout{Request: -1, StreamRead: -1, Connect: -1}, false},
		{"nil", time.Millisecond, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Never send headers: the transport timer is the only completion signal,
			// not a race against a server sleep or a caller deadline.
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				<-release
			}))
			defer server.Close()
			defer close(release)
			transport := &http.Transport{ResponseHeaderTimeout: tc.caller}
			defer transport.CloseIdleConnections()
			client := ClientWithAdapterTimeout(&http.Client{Transport: transport}, tc.adapter)
			defer client.CloseIdleConnections()
			ctx := context.Background()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := client.Do(req)
			if resp != nil {
				resp.Body.Close()
			}
			var timeout net.Error
			if !errors.As(errors.Unwrap(err), &timeout) || !timeout.Timeout() {
				t.Fatalf("expected actual header timeout, got %v", err)
			}
			wantSource := APITimeoutNone
			wantOutcome := apilog.AttemptTransportFail
			if tc.owned {
				wantSource = APITimeoutResponseHeader
				wantOutcome = apilog.AttemptProviderTimeout
			}
			source := APITimeoutSourceForTransport(ctx, ctx, err)
			if source != wantSource {
				t.Errorf("timeout source = %q, want %q (error: %v)", source, wantSource, err)
			}
			outcome := ClassifyAPIAttemptOutcome(APIAttemptContextOwnership{Parent: ctx, Attempt: ctx, TimeoutSource: source}, 0, err, nil, err)
			if outcome != wantOutcome {
				t.Errorf("attempt outcome = %q, want %q", outcome, wantOutcome)
			}
			if transport.ResponseHeaderTimeout != tc.caller {
				t.Errorf("caller transport header policy mutated")
			}
		})
	}
}
