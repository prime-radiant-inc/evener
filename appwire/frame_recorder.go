package appwire

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"primeradiant.com/serf/envvars"
)

// FrameRecorder appends every AppWire WebSocket frame that crosses a transport
// to a JSONL file, one frame per line. It exists solely to harvest real wire
// traffic into fuzz seed corpora (serf-fuzz-harvest); it is opt-in via
// SERF_RECORD_APPWIRE and default-off, so normal operation never touches it.
//
// It is side-effect-only: a failed write is swallowed, never propagated, so the
// recorder can never alter a frame, a response, or the transport's error path.
// Scrubbing is NOT done here — the recorder writes raw bytes and the harvester
// is the single sanitization chokepoint. The file it writes is never committed.
type FrameRecorder struct {
	mu sync.Mutex
	f  *os.File
}

// recordedFrame is one JSONL line in appwire-frames.jsonl. Frame holds the raw
// frame bytes as a JSON string (AppWire frames are UTF-8 JSON-RPC messages).
type recordedFrame struct {
	Dir   string `json:"dir"` // "recv" (inbound) or "send" (outbound)
	Frame string `json:"frame"`
}

// NewFrameRecorder opens (creating, append-only) the given JSONL file.
func NewFrameRecorder(path string) (*FrameRecorder, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &FrameRecorder{f: f}, nil
}

// RecordRecv records an inbound frame. A nil receiver is a no-op.
func (r *FrameRecorder) RecordRecv(data []byte) { r.record("recv", data) }

// RecordSend records an outbound frame. A nil receiver is a no-op.
func (r *FrameRecorder) RecordSend(data []byte) { r.record("send", data) }

func (r *FrameRecorder) record(dir string, data []byte) {
	if r == nil || r.f == nil {
		return
	}
	line, err := json.Marshal(recordedFrame{Dir: dir, Frame: string(data)})
	if err != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.f.Write(append(line, '\n')) //nolint:errcheck // best-effort; recording never affects the transport
}

// Close closes the underlying file. A nil receiver is a no-op.
func (r *FrameRecorder) Close() error {
	if r == nil || r.f == nil {
		return nil
	}
	return r.f.Close()
}

// appwireFrameRecorder is the process-wide recorder attached to every transport.
// It is nil (default-off) unless SERF_RECORD_APPWIRE selects recording at
// startup, so the hot path costs a single nil check per frame.
var appwireFrameRecorder = newEnvFrameRecorder()

func newEnvFrameRecorder() *FrameRecorder {
	if !envvars.RecorderEnabled(envvars.SERFRecordAppwire) {
		return nil
	}
	rec, err := NewFrameRecorder(filepath.Join(recorderStateRoot(), "appwire-frames.jsonl"))
	if err != nil {
		return nil // recording is best-effort; never block startup on it
	}
	return rec
}

// recorderStateRoot mirrors cmdutil.DefaultStateRoot (SERF_STATE_DIR, else
// ~/.serf, else ./.serf). It is duplicated here only to keep appwire — a
// low-level wire-codec package — free of a dependency on the cmd helper layer.
func recorderStateRoot() string {
	if dir := envvars.SERFStateDir.Getenv(); dir != "" {
		return dir
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".serf")
	}
	return ".serf"
}
