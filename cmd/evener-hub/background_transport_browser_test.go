//go:build browserguard

package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
)

const backgroundTransportControl = "/_background-transcript-transport"

// This fixture owns only /rpc admission and the lifetime of real HTTP handlers.
// In particular, cancellation alone cannot acknowledge a pre-entered upgrade.
type backgroundTransportGate struct {
	mu                        sync.Mutex
	next                      http.Handler
	active                    map[*backgroundTransportRequest]struct{}
	closed, drained, terminal bool
	stopped                   chan struct{}
	admitted, rejected        int
	record                    func(backgroundTransportReceipt) error
}
type backgroundTransportRequest struct {
	cancel context.CancelFunc
	done   chan struct{}
}
type backgroundTransportReceipt struct {
	Event       string `json:"event"`
	Active      int    `json:"active"`
	Admitted    int    `json:"admitted"`
	Rejected    int    `json:"rejected"`
	TraceBytes  int64  `json:"trace_bytes"`
	HelperBytes int64  `json:"helper_bytes"`
}

func newBackgroundTransportGate(next http.Handler, record func(backgroundTransportReceipt) error) *backgroundTransportGate {
	return &backgroundTransportGate{next: next, active: make(map[*backgroundTransportRequest]struct{}), stopped: make(chan struct{}), record: record}
}
func (g *backgroundTransportGate) receipt(event string) error {
	if g.record == nil {
		return nil
	}
	return g.record(backgroundTransportReceipt{Event: event, Active: len(g.active), Admitted: g.admitted, Rejected: g.rejected})
}
func (g *backgroundTransportGate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == backgroundTransportControl {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		var command struct {
			Action string `json:"action"`
		}
		if err := json.NewDecoder(r.Body).Decode(&command); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var err error
		switch command.Action {
		case "close":
			err = g.close(r.Context(), false)
		case "open":
			err = g.open()
		default:
			err = errors.New("unknown transport action")
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
		return
	}
	if r.URL.Path != "/rpc" {
		g.next.ServeHTTP(w, r)
		return
	}
	g.mu.Lock()
	if g.closed {
		g.rejected++
		g.mu.Unlock()
		http.Error(w, "fixture transport closed", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	request := &backgroundTransportRequest{cancel: cancel, done: make(chan struct{})}
	g.active[request] = struct{}{}
	g.admitted++
	g.mu.Unlock()
	defer func() { cancel(); g.mu.Lock(); delete(g.active, request); close(request.done); g.mu.Unlock() }()
	g.next.ServeHTTP(w, r.WithContext(ctx))
}
func (g *backgroundTransportGate) close(ctx context.Context, terminal bool) error {
	g.mu.Lock()
	if terminal && g.terminal && g.drained && len(g.active) == 0 {
		g.mu.Unlock()
		return nil
	}
	if terminal && !g.terminal {
		g.terminal = true
		close(g.stopped)
	}
	if !terminal && g.terminal {
		g.mu.Unlock()
		return errors.New("transport terminal")
	}
	g.closed = true
	g.drained = false
	requests := make([]*backgroundTransportRequest, 0, len(g.active))
	for request := range g.active {
		requests = append(requests, request)
	}
	g.mu.Unlock()
	for _, request := range requests {
		request.cancel()
	}
	var stopped <-chan struct{}
	if !terminal {
		stopped = g.stopped
	}
	for _, request := range requests {
		select {
		case <-request.done:
		case <-ctx.Done():
			return ctx.Err()
		case <-stopped:
			return errors.New("transport terminal")
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if !terminal && g.terminal {
		return errors.New("transport terminal")
	}
	event := "drained"
	if terminal {
		event = "terminal"
	}
	if err := g.receipt(event); err != nil {
		return err
	}
	g.drained = true
	return nil
}
func (g *backgroundTransportGate) open() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.terminal || !g.closed || !g.drained || len(g.active) != 0 {
		return errors.New("transport not drained or terminal")
	}
	// Offsets are recorded before any new handler can enter.
	if err := g.receipt("open"); err != nil {
		return err
	}
	g.closed = false
	g.drained = false
	return nil
}
func backgroundTransportRecorder(fixture *skillGuardFixture) func(backgroundTransportReceipt) error {
	return func(receipt backgroundTransportReceipt) error {
		for path, offset := range map[string]*int64{filepath.Join(fixture.artifact, "appwire-trace.jsonl"): &receipt.TraceBytes, fixture.requestLog[1]: &receipt.HelperBytes} {
			info, err := os.Stat(path)
			if err != nil {
				return err
			}
			*offset = info.Size()
		}
		return appendFileLine(filepath.Join(fixture.artifact, "transport-gate.jsonl"), receipt)
	}
}
func assertBackgroundTransportInterval(t *testing.T, fixture *skillGuardFixture, ref string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixture.artifact, "transport-gate.jsonl"))
	if err != nil {
		t.Errorf("read transport gate receipts: %v", err)
		return
	}
	var drained, opened *backgroundTransportReceipt
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		var receipt backgroundTransportReceipt
		if err := json.Unmarshal(line, &receipt); err != nil {
			t.Fatalf("decode transport receipt: %v", err)
		}
		switch receipt.Event {
		case "drained":
			if drained != nil {
				t.Fatal("multiple drained intervals")
			}
			drained = &receipt
		case "open":
			if opened != nil {
				t.Fatal("multiple transport releases")
			}
			opened = &receipt
		}
	}
	if drained == nil || opened == nil {
		t.Error("missing drained transport interval or release")
		return
	}
	if drained.Active != 0 || opened.Active != 0 || drained.Admitted != opened.Admitted {
		t.Errorf("transport admitted work in closed interval: %+v -> %+v", drained, opened)
	}
	trace, err := os.ReadFile(filepath.Join(fixture.artifact, "appwire-trace.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range bytes.Split(bytes.TrimSpace(traceWindowBytes(t, trace, drained.TraceBytes, opened.TraceBytes)), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var record struct {
			Event string `json:"event"`
		}
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		if record.Event == "open" || record.Event == "frame" {
			t.Errorf("real transport activity inside drained interval: %s", line)
		}
	}
	helper, err := os.ReadFile(fixture.requestLog[1])
	if err != nil {
		t.Fatal(err)
	}
	producer, err := os.ReadFile(filepath.Join(fixture.artifact, "target-producer-transcript.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	durableID, err := backgroundGapTurnID(string(producer))
	if err != nil {
		t.Fatal(err)
	}
	completions := 0
	for _, line := range bytes.Split(bytes.TrimSpace(traceWindowBytes(t, helper, drained.HelperBytes, opened.HelperBytes)), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var record struct {
			Event, Prompt, Ref, Status string
			OK                         bool   `json:"ok"`
			TurnID                     string `json:"turnId"`
		}
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		if record.Event == "turn-complete" && record.Prompt == backgroundGapTurn && record.Ref == ref {
			completions++
			if !record.OK || record.Status != "completed" || record.TurnID == "" || record.TurnID != durableID {
				t.Errorf("invalid real gap completion inside drained interval: %+v", record)
			}
		}
	}
	if completions != 1 {
		t.Errorf("drained interval contains %d real gap completions, want one", completions)
	}
	t.Logf("transport interval: trace bytes %d..%d, helper bytes %d..%d, admissions %d -> %d, rejected %d -> %d", drained.TraceBytes, opened.TraceBytes, drained.HelperBytes, opened.HelperBytes, drained.Admitted, opened.Admitted, drained.Rejected, opened.Rejected)
}

func backgroundGateTestServer(t *testing.T, wrap func(http.Handler) http.Handler) (*backgroundTransportGate, *httptest.Server) {
	t.Helper()
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "background-gate", Version: "test", SourceID: "local"})
	var next http.Handler = http.HandlerFunc(server.ServeWebSocket)
	if wrap != nil {
		next = wrap(next)
	}
	gate := newBackgroundTransportGate(next, nil)
	httpServer := httptest.NewServer(gate)
	t.Cleanup(func() {
		ctx := context.Background()
		if deadline, ok := t.Deadline(); ok {
			var cancel context.CancelFunc
			ctx, cancel = context.WithDeadline(ctx, deadline)
			defer cancel()
		}
		if err := gate.close(ctx, true); err != nil {
			t.Error(err)
		}
		httpServer.Close()
		if err := server.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	return gate, httpServer
}
func backgroundGateDial(t *testing.T, server *httptest.Server) *appwire.WSTransport {
	t.Helper()
	transport, err := appwire.DialWebSocket(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http")+"/rpc", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	return transport
}
func TestBackgroundTransportGateEstablishedDrain(t *testing.T) {
	gate, server := backgroundGateTestServer(t, nil)
	transport := backgroundGateDial(t, server)
	if err := gate.close(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	gate.mu.Lock()
	active := len(gate.active)
	gate.mu.Unlock()
	if active != 0 {
		t.Fatalf("close acknowledged %d active handlers", active)
	}
	if _, err := transport.Recv(t.Context()); err == nil {
		t.Fatal("drained real socket still receives")
	}
}
func TestBackgroundTransportGateClosedRejectOpen(t *testing.T) {
	gate, server := backgroundGateTestServer(t, nil)
	if err := gate.close(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	transport, err := appwire.DialWebSocket(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http")+"/rpc", server.Client())
	if err == nil {
		_ = transport.Close()
		t.Fatal("closed gate admitted real socket")
	}
	gate.mu.Lock()
	rejected, admitted := gate.rejected, gate.admitted
	gate.mu.Unlock()
	if rejected != 1 || admitted != 0 {
		t.Fatalf("rejection counts: %d/%d", rejected, admitted)
	}
	if err := gate.open(); err != nil {
		t.Fatal(err)
	}
	backgroundGateDial(t, server)
}

// The real upgrade is entered before close, then held past request cancellation.
// Releasing it after cancellation exercises websocket.Accept's late upgrade.
func backgroundGatePreentered(t *testing.T) (*backgroundTransportGate, <-chan struct{}, func(), <-chan struct{}) {
	t.Helper()
	entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	gate, server := backgroundGateTestServer(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(entered)
			<-r.Context().Done()
			close(cancelled)
			<-release
			next.ServeHTTP(w, r)
		})
	})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	dialDone := make(chan struct{})
	go func() {
		transport, _ := appwire.DialWebSocket(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http")+"/rpc", server.Client())
		if transport != nil {
			_ = transport.Close()
		}
		close(dialDone)
	}()
	t.Cleanup(func() {
		unblock()
		ctx := context.Background()
		if deadline, ok := t.Deadline(); ok {
			var cancel context.CancelFunc
			ctx, cancel = context.WithDeadline(ctx, deadline)
			defer cancel()
		}
		if err := gate.close(ctx, true); err != nil {
			t.Error(err)
		}
		<-dialDone
	})
	<-entered
	return gate, cancelled, unblock, dialDone
}
func TestBackgroundTransportGatePreenteredUpgrade(t *testing.T) {
	gate, cancelled, release, dialDone := backgroundGatePreentered(t)
	done := make(chan error, 1)
	go func() { done <- gate.close(t.Context(), false) }()
	select {
	case <-cancelled:
	case err := <-done:
		t.Fatalf("close returned before pre-entered cancellation/drain: %v", err)
	}
	select {
	case err := <-done:
		t.Fatalf("close acknowledged blocked upgrade: %v", err)
	default:
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	<-dialDone
}
func TestBackgroundTransportGateCancelledClose(t *testing.T) {
	gate, cancelled, release, dialDone := backgroundGatePreentered(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- gate.close(ctx, false) }()
	<-cancelled
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled close: %v", err)
	}
	if err := gate.open(); err == nil {
		t.Fatal("cancelled close allowed reopen")
	}
	release()
	<-dialDone
}
func TestBackgroundTransportGateTerminalCleanup(t *testing.T) {
	gate, cancelled, release, dialDone := backgroundGatePreentered(t)
	ordinary := make(chan error, 1)
	go func() { ordinary <- gate.close(t.Context(), false) }()
	<-cancelled
	terminal := make(chan error, 1)
	go func() { terminal <- gate.close(t.Context(), true) }()
	if err := <-ordinary; err == nil {
		t.Fatal("terminal cleanup did not interrupt ordinary close")
	}
	if err := gate.open(); err == nil {
		t.Fatal("terminal gate reopened")
	}
	release()
	if err := <-terminal; err != nil {
		t.Fatal(err)
	}
	<-dialDone
	if err := gate.open(); err == nil {
		t.Fatal("drained terminal gate reopened")
	}
}
