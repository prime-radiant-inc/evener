package appwire

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

func TestEnvRecordEnabled(t *testing.T) {
	cases := map[string]bool{
		"1": true, "true": true, "TRUE": true, "yes": true, "on": true, " on ": true,
		"0": false, "false": false, "": false, "off": false, "nope": false,
	}
	for in, want := range cases {
		if got := envRecordEnabled(in); got != want {
			t.Errorf("envRecordEnabled(%q)=%v, want %v", in, got, want)
		}
	}
}

func TestFrameRecorderWritesJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "appwire-frames.jsonl")
	rec, err := NewFrameRecorder(path)
	if err != nil {
		t.Fatalf("NewFrameRecorder: %v", err)
	}
	rec.RecordRecv([]byte(`{"id":1,"method":"ping"}`))
	rec.RecordSend([]byte(`{"id":1,"result":{}}`))
	if err := rec.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	frames := readFrames(t, path)
	if len(frames) != 2 {
		t.Fatalf("got %d frames, want 2: %+v", len(frames), frames)
	}
	if frames[0].Dir != "recv" || frames[0].Frame != `{"id":1,"method":"ping"}` {
		t.Errorf("recv frame mismatch: %+v", frames[0])
	}
	if frames[1].Dir != "send" || frames[1].Frame != `{"id":1,"result":{}}` {
		t.Errorf("send frame mismatch: %+v", frames[1])
	}
}

// A nil *FrameRecorder must be a safe no-op so the default-off path costs nothing.
func TestNilFrameRecorderIsNoOp(t *testing.T) {
	var rec *FrameRecorder
	rec.RecordRecv([]byte("x"))
	rec.RecordSend([]byte("y"))
	if err := rec.Close(); err != nil {
		t.Fatalf("nil Close: %v", err)
	}
}

// With the recorder attached, a live transport round-trip records both the
// inbound (recv) and outbound (send) frames byte-for-byte.
func TestWSTransportRecordsFrames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "appwire-frames.jsonl")
	rec, err := NewFrameRecorder(path)
	if err != nil {
		t.Fatalf("NewFrameRecorder: %v", err)
	}
	defer rec.Close() //nolint:errcheck

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		_, data, err := conn.Read(r.Context())
		if err != nil {
			t.Errorf("read: %v", err)
			return
		}
		var msg Message
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Errorf("decode: %v", err)
			return
		}
		out, _ := json.Marshal(ResponseMessage(msg.Request.ID, ThreadListResponse{})) //nolint:errcheck
		if err := conn.Write(r.Context(), websocket.MessageText, out); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	defer server.Close()

	ctx := context.Background()
	transport, err := DialWebSocket(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), server.Client())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer transport.Close() //nolint:errcheck
	transport.rec = rec

	if err := transport.Send(ctx, RequestMessage(NewIntID(1), MethodThreadList, ThreadListParams{})); err != nil {
		t.Fatalf("send: %v", err)
	}
	if _, err := transport.Recv(ctx); err != nil {
		t.Fatalf("recv: %v", err)
	}
	if err := transport.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	frames := readFrames(t, path)
	var sawSend, sawRecv bool
	for _, fr := range frames {
		switch fr.Dir {
		case "send":
			sawSend = true
			if !strings.Contains(fr.Frame, MethodThreadList) {
				t.Errorf("send frame missing method: %q", fr.Frame)
			}
		case "recv":
			sawRecv = true
		}
	}
	if !sawSend || !sawRecv {
		t.Fatalf("expected both send and recv frames, got %+v", frames)
	}
}

func readFrames(t *testing.T, path string) []recordedFrame {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open recording: %v", err)
	}
	defer f.Close() //nolint:errcheck
	var out []recordedFrame
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var fr recordedFrame
		if err := json.Unmarshal([]byte(line), &fr); err != nil {
			t.Fatalf("decode frame line %q: %v", line, err)
		}
		out = append(out, fr)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return out
}
