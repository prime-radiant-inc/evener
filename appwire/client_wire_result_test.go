package appwire

import (
	"encoding/json"
	"testing"
	"time"
)

// A response frame read off the wire keeps its result as the bytes the peer
// sent. Decoding it into a generic tree first (maps and float64 numbers) and
// re-encoding that tree for the caller's typed decode costs two extra passes
// over every result — for the hub's roster probes, megabytes of thread
// diagnostics per daemon every few seconds — and rounds integers past 2^53.
func TestMessageDecodeKeepsWireResultBytes(t *testing.T) {
	const result = `{"z":1,"a":[9007199254740993,{"k":"v"}]}`
	var m Message
	if err := json.Unmarshal([]byte(`{"id":7,"result":`+result+`}`), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	raw, ok := m.Response.Result.(json.RawMessage)
	if !ok {
		t.Fatalf("Result = %T, want json.RawMessage", m.Response.Result)
	}
	if string(raw) != result {
		t.Fatalf("Result = %s, want %s", raw, result)
	}
}

func TestClientRequestDecodesWireResultWithoutRounding(t *testing.T) {
	transport := newMemoryTransport()
	client := NewClient(transport)
	ctx := t.Context()
	client.Start(ctx)

	type counter struct {
		N int64 `json:"n"`
	}
	done := make(chan counter, 1)
	errs := make(chan error, 1)
	go func() {
		var out counter
		errs <- client.Request(ctx, "test/counter", nil, &out)
		done <- out
	}()

	var written Message
	select {
	case written = <-transport.writes:
	case <-time.After(time.Second):
		t.Fatal("request was not written")
	}
	var wire Message
	if err := json.Unmarshal([]byte(`{"id":`+written.Request.ID.String()+`,"result":{"n":9007199254740993}}`), &wire); err != nil {
		t.Fatalf("decode wire frame: %v", err)
	}
	transport.reads <- wire

	select {
	case err := <-errs:
		if err != nil {
			t.Fatalf("request: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("request did not complete")
	}
	if got := <-done; got.N != 9007199254740993 {
		t.Fatalf("N = %d, want 9007199254740993", got.N)
	}
}
