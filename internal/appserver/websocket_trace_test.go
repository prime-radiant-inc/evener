package appserver

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
	"testing"
	"time"

	"github.com/coder/websocket"
	"primeradiant.com/evener/appwire"
)

type decodedWebSocketTraceRecord struct {
	Timestamp  string  `json:"timestamp"`
	Connection string  `json:"connection"`
	Event      string  `json:"event"`
	Direction  string  `json:"direction"`
	Bytes      *int    `json:"bytes"`
	Frame      *string `json:"frame"`
	Error      string  `json:"error"`
}

type failingTraceWriteCloser struct {
	err error
}

func (w failingTraceWriteCloser) Write([]byte) (int, error) { return 0, w.err }
func (w failingTraceWriteCloser) Close() error              { return nil }

func TestNewWebSocketTraceCreatesPrivateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	trace, err := NewWebSocketTrace(path)
	if err != nil {
		t.Fatalf("NewWebSocketTrace: %v", err)
	}
	if err := trace.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat trace: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("trace permissions = %04o, want 0600", got)
	}
}

func TestNewWebSocketTraceRefusesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	if err := os.WriteFile(path, []byte("keep me"), 0o600); err != nil {
		t.Fatalf("seed existing file: %v", err)
	}

	trace, err := NewWebSocketTrace(path)
	if err == nil {
		trace.Close() //nolint:errcheck // failure cleanup
		t.Fatal("NewWebSocketTrace succeeded for an existing file")
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read existing file: %v", readErr)
	}
	if string(got) != "keep me" {
		t.Fatalf("existing file = %q, want unchanged content", got)
	}
}

func TestWebSocketTraceRecordsPerConnectionFrameDetails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	trace, err := NewWebSocketTrace(path)
	if err != nil {
		t.Fatalf("NewWebSocketTrace: %v", err)
	}
	trace.now = func() time.Time {
		return time.Date(2026, time.August, 31, 21, 0, 0, 123456789, time.UTC)
	}

	observer := trace.Observer("conn-9")
	observer.RecordRecv([]byte(`{ "method": "ping" }`))
	observer.RecordSend([]byte(`{"id":9,"result":{}}`))
	if err := trace.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	records := readWebSocketTraceRecords(t, path)
	if len(records) != 2 {
		t.Fatalf("record count = %d, want 2: %+v", len(records), records)
	}
	wantTimestamp := "2026-08-31T21:00:00.123456789Z"
	wantFrames := []struct {
		direction string
		bytes     int
		frame     string
	}{
		{direction: "browser_to_hub", bytes: 20, frame: `{ "method": "ping" }`},
		{direction: "hub_to_browser", bytes: 20, frame: `{"id":9,"result":{}}`},
	}
	for i, want := range wantFrames {
		got := records[i]
		if got.Timestamp != wantTimestamp || got.Connection != "conn-9" || got.Event != "frame" || got.Direction != want.direction {
			t.Errorf("record %d identity = %+v, want timestamp=%q connection=conn-9 event=frame direction=%q", i, got, wantTimestamp, want.direction)
		}
		if got.Bytes == nil || *got.Bytes != want.bytes {
			t.Errorf("record %d bytes = %v, want %d", i, got.Bytes, want.bytes)
		}
		if got.Frame == nil || *got.Frame != want.frame {
			t.Errorf("record %d frame = %v, want %q", i, got.Frame, want.frame)
		}
	}
}

func TestWebSocketTraceRecordsConnectionLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	trace, err := NewWebSocketTrace(path)
	if err != nil {
		t.Fatalf("NewWebSocketTrace: %v", err)
	}
	trace.now = func() time.Time {
		return time.Date(2026, time.August, 31, 21, 5, 0, 0, time.UTC)
	}

	trace.ConnectionOpened("conn-4")
	trace.ConnectionClosed("conn-4", errors.New("peer vanished"))
	if err := trace.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	records := readWebSocketTraceRecords(t, path)
	if len(records) != 2 {
		t.Fatalf("record count = %d, want 2: %+v", len(records), records)
	}
	if got := records[0]; got.Connection != "conn-4" || got.Event != "open" || got.Direction != "" || got.Bytes != nil || got.Frame != nil || got.Error != "" {
		t.Errorf("open record = %+v, want lifecycle-only conn-4 open", got)
	}
	if got := records[1]; got.Connection != "conn-4" || got.Event != "close" || got.Error != "peer vanished" || got.Direction != "" || got.Bytes != nil || got.Frame != nil {
		t.Errorf("close record = %+v, want conn-4 close with error", got)
	}
}

func TestWebSocketTraceCloseReportsWriteFailure(t *testing.T) {
	want := errors.New("disk full")
	trace := &WebSocketTrace{
		file: failingTraceWriteCloser{err: want},
		now:  time.Now,
	}

	trace.ConnectionOpened("conn-1")
	if err := trace.Close(); !errors.Is(err, want) {
		t.Fatalf("Close error = %v, want write failure %v", err, want)
	}
}

func TestServeWebSocketTraceSeparatesConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	trace, err := NewWebSocketTrace(path)
	if err != nil {
		t.Fatalf("NewWebSocketTrace: %v", err)
	}
	server := NewServer(ServerConfig{
		ServerName:     "test-server",
		Version:        "test",
		SourceID:       "local",
		WebSocketTrace: trace,
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))

	ctx := context.Background()
	url := "ws" + strings.TrimPrefix(httpServer.URL, "http")
	first := dialTraceWebSocket(ctx, t, url, httpServer.Client())
	second := dialTraceWebSocket(ctx, t, url, httpServer.Client())
	requests := []string{
		`{"id":41,"method":"initialize","params":{"protocolVersion":"evener-appwire-v4"}}`,
		`{"id":42,"method":"initialize","params":{"protocolVersion":"evener-appwire-v4"}}`,
	}
	for i, conn := range []*websocket.Conn{first, second} {
		if err := conn.Write(ctx, websocket.MessageText, []byte(requests[i])); err != nil {
			t.Fatalf("connection %d write: %v", i, err)
		}
		if _, _, err := conn.Read(ctx); err != nil {
			t.Fatalf("connection %d read: %v", i, err)
		}
	}
	if err := first.Close(websocket.StatusNormalClosure, ""); err != nil {
		t.Fatalf("close first: %v", err)
	}
	if err := second.Close(websocket.StatusNormalClosure, ""); err != nil {
		t.Fatalf("close second: %v", err)
	}
	// ServeWebSocket writes its close record from a defer that only runs once
	// the handler goroutine finishes teardown. That goroutine's completion is
	// independent of the client-side Close calls above: websocket.Accept
	// hijacks the connection immediately, so httptest.Server's own shutdown
	// bookkeeping stops tracking it long before the handler actually returns,
	// and httpServer.Close below won't wait for it either. Wait for every
	// connection's close record to land before stopping the trace, or
	// trace.Close can race the still-running handler and silently drop it
	// (WebSocketTrace.record discards writes once closed).
	var records []decodedWebSocketTraceRecord
	waitUntil(t, "both connections' open, outbound frame, and close records to land in the trace", func() bool {
		records = readWebSocketTraceRecords(t, path)
		byConnection, _ := groupWebSocketTraceRecordsByConnection(records)
		if len(byConnection) != 2 {
			return false
		}
		for _, conn := range byConnection {
			if !conn.sawOpen || !conn.sawClose || !conn.sawOutbound {
				return false
			}
		}
		return true
	})
	httpServer.Close()
	if err := trace.Close(); err != nil {
		t.Fatalf("close trace: %v", err)
	}

	records = readWebSocketTraceRecords(t, path)
	byConnection, seenRequests := groupWebSocketTraceRecordsByConnection(records)
	if len(byConnection) != 2 {
		t.Fatalf("traced connections = %d, want 2: %+v", len(byConnection), records)
	}
	for connection, conn := range byConnection {
		if !conn.sawOpen || !conn.sawClose || !conn.sawOutbound {
			t.Errorf("connection %q records = %+v, want open, outbound frame, and close", connection, conn.records)
		}
	}
	for _, request := range requests {
		if !seenRequests[request] {
			t.Errorf("missing exact inbound request %q in trace", request)
		}
	}
}

// webSocketTraceConnection is one connection's trace records plus whether
// its lifecycle events (open, an outbound frame, close) were all seen.
type webSocketTraceConnection struct {
	records                        []decodedWebSocketTraceRecord
	sawOpen, sawClose, sawOutbound bool
}

// groupWebSocketTraceRecordsByConnection groups trace records by connection
// and classifies each connection's lifecycle coverage, alongside the exact
// inbound request payloads seen across all connections.
func groupWebSocketTraceRecordsByConnection(records []decodedWebSocketTraceRecord) (byConnection map[string]*webSocketTraceConnection, seenRequests map[string]bool) {
	byConnection = make(map[string]*webSocketTraceConnection)
	seenRequests = make(map[string]bool)
	for _, record := range records {
		conn := byConnection[record.Connection]
		if conn == nil {
			conn = &webSocketTraceConnection{}
			byConnection[record.Connection] = conn
		}
		conn.records = append(conn.records, record)
		switch {
		case record.Event == "open":
			conn.sawOpen = true
		case record.Event == "close":
			conn.sawClose = true
		case record.Event == "frame" && record.Direction == "hub_to_browser":
			conn.sawOutbound = true
		case record.Event == "frame" && record.Direction == "browser_to_hub" && record.Frame != nil:
			seenRequests[*record.Frame] = true
		}
	}
	return byConnection, seenRequests
}

func TestServerShutdownDrainsOpenTracedWebSocket(t *testing.T) {
	testServerShutdownDrainsOpenTracedWebSocket(t, false)
}

func TestServerShutdownBeforeTracedWebSocketReceive(t *testing.T) {
	testServerShutdownDrainsOpenTracedWebSocket(t, true)
}

func TestWebSocketReceiveCloseModes(t *testing.T) {
	exerciseReceiveLoops(t)
}

type shutdownQueueTransport struct {
	webSocketTransport
	overflowReceived chan struct{}
}

func (w *shutdownQueueTransport) Recv(ctx context.Context) (appwire.Message, error) {
	msg, err := w.webSocketTransport.Recv(ctx)
	if msg.Request != nil && msg.Request.Method == "test/overflow" {
		close(w.overflowReceived)
	}
	return msg, err
}

func TestServerShutdownWithFullWebSocketRequestQueue(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test-server", SourceID: "local"})
	server.requestQueueCapacity = 1
	entered, release := make(chan struct{}), make(chan struct{})
	server.Router().Handle("test/block", func(context.Context, json.RawMessage) (any, error) {
		close(entered)
		<-release
		return nil, nil
	})
	overflowReceived := make(chan struct{})
	server.wrapWebSocketTransport = func(inner webSocketTransport) webSocketTransport {
		return &shutdownQueueTransport{webSocketTransport: inner, overflowReceived: overflowReceived}
	}
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	conn := dialTraceWebSocket(ctx, t, "ws"+strings.TrimPrefix(httpServer.URL, "http"), httpServer.Client())
	defer conn.CloseNow()
	defer close(release)
	write := func(frame string) {
		t.Helper()
		if err := conn.Write(ctx, websocket.MessageText, []byte(frame)); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"id":1,"method":"initialize","params":{"protocolVersion":"evener-appwire-v4"}}`)
	if _, _, err := conn.Read(ctx); err != nil {
		t.Fatal(err)
	}
	write(`{"id":2,"method":"test/block"}`)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("worker did not enter blocking request")
	}
	write(`{"id":3,"method":"test/queued"}`)
	write(`{"id":4,"method":"test/overflow"}`)
	select {
	case <-overflowReceived:
	case <-ctx.Done():
		t.Fatal("receive loop did not reach full queue")
	}
	// The serial worker remains blocked and the preceding request fills its
	// only queue slot. Cancellation must end enqueueRequest without another Recv.
	shutdownCtx, shutdownCancel := context.WithTimeout(ctx, 2*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if _, _, err := conn.Read(shutdownCtx); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("peer read after shutdown = %v, want closed socket", err)
	}
}

// A canceled receive may return before entering the socket read (for example,
// while acquiring coder/websocket's read lock), leaving the socket open. Keep
// the real initialization exchange, then deterministically select that boundary.
type shutdownBeforeReceiveTransport struct {
	webSocketTransport
	initialized bool
}

func (w *shutdownBeforeReceiveTransport) Recv(ctx context.Context) (appwire.Message, error) {
	if !w.initialized {
		w.initialized = true
		return w.webSocketTransport.Recv(ctx)
	}
	<-ctx.Done()
	return appwire.Message{}, ctx.Err()
}

func testServerShutdownDrainsOpenTracedWebSocket(t *testing.T, beforeReceive bool) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	trace, err := NewWebSocketTrace(path)
	if err != nil {
		t.Fatalf("NewWebSocketTrace: %v", err)
	}
	server := NewServer(ServerConfig{
		ServerName:     "test-server",
		Version:        "test",
		SourceID:       "local",
		WebSocketTrace: trace,
	})
	if beforeReceive {
		server.wrapWebSocketTransport = func(inner webSocketTransport) webSocketTransport {
			return &shutdownBeforeReceiveTransport{webSocketTransport: inner}
		}
	}
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()

	ctx := context.Background()
	conn := dialTraceWebSocket(ctx, t, "ws"+strings.TrimPrefix(httpServer.URL, "http"), httpServer.Client())
	defer conn.CloseNow()
	request := []byte(`{"id":51,"method":"initialize","params":{"protocolVersion":"evener-appwire-v4"}}`)
	if err := conn.Write(ctx, websocket.MessageText, request); err != nil {
		t.Fatalf("write initialize: %v", err)
	}
	if _, _, err := conn.Read(ctx); err != nil {
		t.Fatalf("read initialize response: %v", err)
	}

	shutdownCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if _, _, err := conn.Read(shutdownCtx); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("peer read after shutdown = %v, want closed socket", err)
	}
	conn.CloseNow()
	httpServer.Close()
	if err := trace.Close(); err != nil {
		t.Fatalf("close trace: %v", err)
	}

	records := readWebSocketTraceRecords(t, path)
	if len(records) < 4 {
		t.Fatalf("records = %+v, want open, inbound, outbound, and close", records)
	}
	if got := records[len(records)-1]; got.Event != "close" {
		t.Fatalf("final record = %+v, want close", got)
	}
}

func dialTraceWebSocket(ctx context.Context, t *testing.T, url string, client *http.Client) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPClient: client})
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	return conn
}

func readWebSocketTraceRecords(t *testing.T, path string) []decodedWebSocketTraceRecord {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}
	var records []decodedWebSocketTraceRecord
	for _, line := range bytesLines(data) {
		var record decodedWebSocketTraceRecord
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("decode trace line %q: %v", line, err)
		}
		records = append(records, record)
	}
	return records
}

func bytesLines(data []byte) [][]byte {
	return bytes.FieldsFunc(data, func(r rune) bool { return r == '\n' })
}
