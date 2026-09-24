package appwire

import (
	"encoding/json"
	"fmt"
	"strings"
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

// Taking the result from the probe must not loosen the id: a response naming
// a null id is still refused, and one with no id at all still decodes.
func TestMessageDecodeResponseIDRules(t *testing.T) {
	var m Message
	if err := json.Unmarshal([]byte(`{"id":null,"result":1}`), &m); err == nil {
		t.Fatalf("a null response id decoded: %+v", m.Response)
	}
	if err := json.Unmarshal([]byte(`{"result":1}`), &m); err != nil || m.Kind() != MessageResponse || m.IDString() != "" {
		t.Fatalf("an id-less response: err=%v kind=%d id=%q", err, m.Kind(), m.IDString())
	}
	if err := json.Unmarshal([]byte(`{"id":"a","result":1}`), &m); err != nil || m.IDString() != "a" {
		t.Fatalf("a string id: err=%v id=%q", err, m.IDString())
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

// BenchmarkWSTransportRecvLargeResponse measures what reading one large
// response costs the receive loop: the hub's roster probe reads thread/list and
// thread/read answers whose diagnostics carry every retained delegate's task
// text, megabytes per daemon every few seconds.
func BenchmarkWSTransportRecvLargeResponse(b *testing.B) {
	delegates := make([]EvenerDelegateInfo, 200)
	for i := range delegates {
		task := strings.Repeat("Jesse asks: \"study the \\\"hub\\\" <daemon> lifecycle\"\n", 60)
		delegates[i] = EvenerDelegateInfo{ChildSessionID: fmt.Sprintf("child-%d", i), Lifecycle: "idle", Task: task, Description: task}
	}
	result := ThreadReadResponse{Thread: Thread{ID: "root", SessionID: "root", Evener: EvenerThread{Diagnostics: &EvenerDiagnostics{Delegates: delegates}}}}
	frame, err := json.Marshal(ResponseMessage(NewIntID(1), result))
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(frame)))
	b.ReportAllocs()
	for b.Loop() {
		var msg Message
		if err := unmarshalWSMessage(&msg, frame); err != nil {
			b.Fatal(err)
		}
		var out ThreadReadResponse
		if err := json.Unmarshal(msg.Response.Result.(json.RawMessage), &out); err != nil {
			b.Fatal(err)
		}
	}
}
