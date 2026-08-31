package appserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"primeradiant.com/evener/appwire"
)

// WebSocketTrace writes hub-side WebSocket diagnostic events to a JSONL file.
type WebSocketTrace struct {
	mu     sync.Mutex
	file   io.WriteCloser
	now    func() time.Time
	err    error
	closed bool
}

type webSocketTraceRecord struct {
	Timestamp  string  `json:"timestamp"`
	Connection string  `json:"connection"`
	Event      string  `json:"event"`
	Direction  string  `json:"direction,omitempty"`
	Bytes      *int    `json:"bytes,omitempty"`
	Frame      *string `json:"frame,omitempty"`
	Error      string  `json:"error,omitempty"`
}

type webSocketTraceObserver struct {
	trace      *WebSocketTrace
	connection string
}

// NewWebSocketTrace creates a private WebSocket diagnostic trace at path.
func NewWebSocketTrace(path string) (*WebSocketTrace, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &WebSocketTrace{file: file, now: time.Now}, nil
}

// Observer returns a raw frame observer scoped to one hub-side connection.
func (t *WebSocketTrace) Observer(connection string) appwire.FrameObserver {
	return webSocketTraceObserver{trace: t, connection: connection}
}

func (o webSocketTraceObserver) RecordRecv(data []byte) {
	o.trace.recordFrame(o.connection, "browser_to_hub", data)
}

func (o webSocketTraceObserver) RecordSend(data []byte) {
	o.trace.recordFrame(o.connection, "hub_to_browser", data)
}

// ConnectionOpened records the start of one hub-side WebSocket connection.
func (t *WebSocketTrace) ConnectionOpened(connection string) {
	t.record(webSocketTraceRecord{
		Timestamp:  t.now().UTC().Format(time.RFC3339Nano),
		Connection: connection,
		Event:      "open",
	})
}

// ConnectionClosed records the end of one hub-side WebSocket connection.
func (t *WebSocketTrace) ConnectionClosed(connection string, err error) {
	record := webSocketTraceRecord{
		Timestamp:  t.now().UTC().Format(time.RFC3339Nano),
		Connection: connection,
		Event:      "close",
	}
	if err != nil {
		record.Error = err.Error()
	}
	t.record(record)
}

func (t *WebSocketTrace) recordFrame(connection, direction string, data []byte) {
	bytes := len(data)
	frame := string(data)
	t.record(webSocketTraceRecord{
		Timestamp:  t.now().UTC().Format(time.RFC3339Nano),
		Connection: connection,
		Event:      "frame",
		Direction:  direction,
		Bytes:      &bytes,
		Frame:      &frame,
	})
}

func (t *WebSocketTrace) record(record webSocketTraceRecord) {
	line, err := json.Marshal(record)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	if err != nil {
		t.retainError(fmt.Errorf("marshal websocket trace record: %w", err))
		return
	}
	line = append(line, '\n')
	n, writeErr := t.file.Write(line)
	if writeErr == nil && n != len(line) {
		writeErr = io.ErrShortWrite
	}
	if writeErr != nil {
		t.retainError(fmt.Errorf("write websocket trace: %w", writeErr))
	}
}

func (t *WebSocketTrace) retainError(err error) {
	if t.err == nil {
		t.err = err
	}
}

// Close closes the trace file and reports any earlier write failure.
func (t *WebSocketTrace) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.closed {
		t.closed = true
		t.err = errors.Join(t.err, t.file.Close())
	}
	return t.err
}
