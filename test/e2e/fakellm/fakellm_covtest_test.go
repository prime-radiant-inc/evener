package fakellm

import (
	"encoding/json"
	"net"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/evener/internal/e2ecap"
)

// TestCovMintToolCallID covers mintToolCallID (fakellm.go:90), which mints
// a unique tool-call id from an atomic counter.
func TestCovMintToolCallID(t *testing.T) {
	id1 := mintToolCallID()
	id2 := mintToolCallID()
	const prefix = "call_fakellm_"
	if !strings.HasPrefix(id1, prefix) || !strings.HasPrefix(id2, prefix) {
		t.Fatalf("tool call IDs = %q, %q, want %q prefix", id1, id2, prefix)
	}
	n1, err := strconv.ParseUint(strings.TrimPrefix(id1, prefix), 10, 64)
	if err != nil {
		t.Fatalf("parse first tool call ID %q: %v", id1, err)
	}
	n2, err := strconv.ParseUint(strings.TrimPrefix(id2, prefix), 10, 64)
	if err != nil {
		t.Fatalf("parse second tool call ID %q: %v", id2, err)
	}
	if n2 != n1+1 {
		t.Fatalf("tool call sequences = %d then %d, want consecutive values", n1, n2)
	}
}

// TestCovNew covers New (fakellm.go:93), which starts a fake provider on
// a kernel-assigned loopback port. NewOn is already 100% covered; New is
// the convenience wrapper.
func TestCovNew(t *testing.T) {
	e2ecap.RequireLoopbackBind(t)
	srv, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()
	host, portText, err := net.SplitHostPort(srv.Addr())
	if err != nil {
		t.Fatalf("split Addr %q: %v", srv.Addr(), err)
	}
	if host != "127.0.0.1" {
		t.Fatalf("New listen host = %q, want IPv4 loopback", host)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 {
		t.Fatalf("New listen port = %q, want kernel-assigned positive port (error %v)", portText, err)
	}
	if got, want := srv.BaseURL(), "http://"+srv.Addr()+"/v1"; got != want {
		t.Fatalf("BaseURL = %q, want %q", got, want)
	}

	resp, err := http.Get(srv.BaseURL() + "/models") //nolint:noctx // local test server request
	if err != nil {
		t.Fatalf("GET live fake provider: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /models status = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Object string `json:"object"`
		Data   []struct {
			ID     string `json:"id"`
			Object string `json:"object"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode GET /models: %v", err)
	}
	if body.Object != "list" || len(body.Data) != 1 || body.Data[0].ID != ModelID || body.Data[0].Object != "model" {
		t.Fatalf("GET /models body = %#v, want one %q model", body, ModelID)
	}
}

// TestCovCallAccessors covers Cancelled, AffinityHeader, and ToolCallID
// (fakellm.go:69, 74, 167), which are simple field accessors on Call.
func TestCovCallAccessors(t *testing.T) {
	cancelled := make(chan struct{})
	close(cancelled)
	c := &Call{
		toolCallID:     "call_test_123",
		affinityHeader: "affinity-abc",
		cancelled:      cancelled,
		reply:          make(chan reply, 1),
	}

	if got := c.Cancelled(); got != cancelled {
		t.Fatal("Cancelled() did not return the call's cancellation channel")
	}
	select {
	case <-c.Cancelled():
		// expected: already closed
	default:
		t.Fatal("Cancelled() channel should be closed")
	}

	if got := c.AffinityHeader(); got != "affinity-abc" {
		t.Fatalf("AffinityHeader() = %q, want affinity-abc", got)
	}

	if got := c.ToolCallID(); got != "call_test_123" {
		t.Fatalf("ToolCallID() = %q, want call_test_123", got)
	}
}

// TestCovRespondText covers RespondText (fakellm.go:153), which finishes
// a round with an assistant message and finish_reason "stop".
func TestCovRespondText(t *testing.T) {
	c := &Call{reply: make(chan reply, 1)}
	c.RespondText("hello world")

	select {
	case r := <-c.reply:
		if r.text != "hello world" {
			t.Fatalf("reply.text = %q, want hello world", r.text)
		}
		if r.toolName != "" {
			t.Fatalf("reply.toolName = %q, want empty", r.toolName)
		}
	default:
		t.Fatal("RespondText should send on reply channel")
	}
}

// TestCovRespondToolCall covers RespondToolCall (fakellm.go:160), which
// finishes a round with a single tool call.
func TestCovRespondToolCall(t *testing.T) {
	c := &Call{reply: make(chan reply, 1), toolCallID: "call_tc_1"}
	args := map[string]any{"file_path": "test.txt"}
	c.RespondToolCall("read_file", args)

	select {
	case r := <-c.reply:
		if r.toolName != "read_file" {
			t.Fatalf("reply.toolName = %q, want read_file", r.toolName)
		}
		if r.toolArgs["file_path"] != "test.txt" {
			t.Fatalf("reply.toolArgs = %v, want file_path=test.txt", r.toolArgs)
		}
		if r.toolID != "call_tc_1" {
			t.Fatalf("reply.toolID = %q, want call_tc_1", r.toolID)
		}
		if r.text != "" {
			t.Fatalf("reply.text = %q, want empty", r.text)
		}
	default:
		t.Fatal("RespondToolCall should send on reply channel")
	}
}

// TestCovRespondOnce covers the sync.Once guard in respond (fakellm.go:169):
// a second call is a no-op and does not block.
func TestCovRespondOnce(t *testing.T) {
	c := &Call{reply: make(chan reply, 1)}
	c.RespondText("first")
	first, ok := <-c.reply
	wantFirst := reply{text: "first"}
	if !ok || !reflect.DeepEqual(first, wantFirst) {
		t.Fatalf("first reply = %#v, %v; want %#v, true", first, ok, wantFirst)
	}
	c.RespondText("second")
	if second, ok := <-c.reply; ok {
		t.Fatalf("second RespondText published another reply: %#v", second)
	}
}
