package hubendpoint

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestEndpointLocalTargetLiteralAddresses(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{"http://127.0.0.1:9180", true},
		{"http://127.0.0.2:9180", true},
		{"http://[::1]:9180", true},
		{"http://[::ffff:127.0.0.2]:9180", true},
		{"http://192.0.2.1:9180", false},
		{"http://[2001:db8::1]:9180", false},
		{"http://localhost:9180", false},
		{"http://unresolved.invalid:9180", false},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			origin, err := ParseOrigin(tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			if got := LocalTarget(origin); got != tc.want {
				t.Fatalf("local=%v want=%v", got, tc.want)
			}
		})
	}
	for _, addr := range []netip.Addr{{}, netip.MustParseAddr("::1%lo"), netip.MustParseAddr("fe80::1%lo")} {
		if localAddress(addr) {
			t.Fatalf("accepted invalid or zoned address %v", addr)
		}
	}
}

func TestEndpointVerifyLocalConnReusableForAuthenticatedRequest(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	record := testRecord(root)
	record.BoundAddr = ln.Addr().String()
	record.LocalURL = "http://" + record.BoundAddr
	auth := make(chan string, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/health":
			if r.Header.Get("Authorization") != "" {
				t.Error("probe sent credential")
			}
			_ = json.NewEncoder(w).Encode(VerifiedHealth{BootID: record.BootID, HubAddr: record.BoundAddr, StartedAt: record.StartedAt})
		case "/api/next":
			auth <- r.Header.Get("Authorization")
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})}
	done := make(chan error, 1)
	go func() { done <- server.Serve(ln) }()
	defer func() { _ = server.Close(); <-done }()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	origin, err := ParseOrigin("http://localhost:" + record.BoundAddr[len("127.0.0.1:"):])
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyLocalConn(context.Background(), conn, origin, record); err != nil {
		t.Fatalf("same connection verification: %v", err)
	}
	req, err := http.NewRequest(http.MethodGet, origin.String()+"/api/next", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer test-sentinel")
	if err := req.Write(conn); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status %d", resp.StatusCode)
	}
	select {
	case got := <-auth:
		if got != "Bearer test-sentinel" {
			t.Fatalf("wrong second request header %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("subsequent request not received")
	}
}

func TestEndpointVerifyRejectsRedirectAndOversizeHealth(t *testing.T) {
	for _, name := range []string{"redirect", "oversize-header", "oversize-body", "started_at", "hub_addr"} {
		t.Run(name, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			record := testRecord(t.TempDir())
			record.BoundAddr = ln.Addr().String()
			record.LocalURL = "http://" + record.BoundAddr
			seen := make(chan http.Header, 1)
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen <- r.Header.Clone()
				switch name {
				case "redirect":
					w.Header().Set("Location", "http://127.0.0.1:1/")
					w.WriteHeader(http.StatusFound)
				case "oversize-header":
					w.Header().Set("X-Large", strings.Repeat("x", maxEndpointBytes+1))
					_ = json.NewEncoder(w).Encode(VerifiedHealth{BootID: record.BootID, HubAddr: record.BoundAddr, StartedAt: record.StartedAt})
				case "oversize-body":
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(strings.Repeat("x", maxEndpointBytes+1)))
				case "started_at", "hub_addr":
					health := VerifiedHealth{BootID: record.BootID, HubAddr: record.BoundAddr, StartedAt: record.StartedAt}
					if name == "started_at" {
						health.StartedAt = health.StartedAt.Add(time.Second)
					} else {
						health.HubAddr = "127.0.0.1:1"
					}
					_ = json.NewEncoder(w).Encode(health)
				}
			})}
			done := make(chan error, 1)
			go func() { done <- server.Serve(ln) }()
			defer func() { _ = server.Close(); <-done }()
			conn, err := net.Dial("tcp", ln.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			origin, err := ParseOrigin(record.LocalURL)
			if err != nil {
				t.Fatal(err)
			}
			if err := VerifyLocalConn(context.Background(), conn, origin, record); err == nil {
				t.Fatal("untrusted health accepted")
			} else if (name == "started_at" || name == "hub_addr") && !errors.Is(err, ErrIdentityMismatch) {
				t.Fatalf("identity mismatch returned %T: %v", err, err)
			}
			select {
			case headers := <-seen:
				if headers.Get("Authorization") != "" {
					t.Fatal("probe disclosed token")
				}
			case <-time.After(time.Second):
				t.Fatal("probe did not reach listener")
			}
		})
	}
}

type deadlineObservedConn struct {
	net.Conn
	deadlines chan time.Time
}

func (c *deadlineObservedConn) SetDeadline(deadline time.Time) error {
	c.deadlines <- deadline
	return c.Conn.SetDeadline(deadline)
}

func TestEndpointVerifyCancellationDuringRequest(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	record := testRecord(t.TempDir())
	record.BoundAddr = ln.Addr().String()
	record.LocalURL = "http://" + record.BoundAddr
	requested := make(chan http.Header, 1)
	disconnected := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested <- r.Header.Clone()
		<-r.Context().Done() // No response, only cancellation can end the client read.
		close(disconnected)
	})}
	done := make(chan error, 1)
	go func() { done <- server.Serve(ln) }()
	defer func() { _ = server.Close(); <-done }()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	observed := &deadlineObservedConn{Conn: conn, deadlines: make(chan time.Time, 4)}
	origin, err := ParseOrigin(record.LocalURL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- VerifyLocalConn(ctx, observed, origin, record) }()
	select {
	case headers := <-requested:
		if headers.Get("Authorization") != "" {
			t.Fatal("verification sent a token")
		}
	case <-time.After(time.Second):
		t.Fatal("verification request not observed")
	}
	<-observed.deadlines // Initial bounded network deadline.
	cancel()
	select {
	case deadline := <-observed.deadlines:
		if deadline.After(time.Now()) {
			t.Fatal("cancellation did not interrupt the real socket")
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not change the socket deadline")
	}
	select {
	case err := <-result:
		var network net.Error
		if !errors.As(err, &network) || !network.Timeout() || !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatalf("cancelled read=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled verification still blocked")
	}
	_ = conn.Close() // Failure contract requires the caller to close, not reuse.
	select {
	case <-disconnected:
	case <-time.After(time.Second):
		t.Fatal("server did not observe connection closure")
	}
}
