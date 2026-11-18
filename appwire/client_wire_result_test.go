package appwire

import (
	"context"
	"encoding/json"
	"errors"
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

// An envelope the direct decode cannot reproduce exactly stays on the ordinary
// decode: every repeated member merges in one pass where the ordinary decoder
// keeps the last, and a null result clears the field instead of reaching the
// target's unmarshaler, which decides whether the null is acceptable.
func TestDecodeFrameBoundFallsBackOnAmbiguousEnvelope(t *testing.T) {
	pad := strings.Repeat("x", boundFrameMinSize)
	repeated := `{"id":1,"result":{"thread":{"id":"first"}},"result":{"thread":{"id":"last"}},"pad":"` + pad + `"}`
	nullable := `{"id":1,"result":null,"pad":"` + pad + `"}`

	var plain Message
	if err := decodeFrame(&plain, []byte(repeated)); err != nil {
		t.Fatalf("ordinary decode of a repeated result: %v", err)
	}
	var storage ThreadReadResponse
	msg, bound, err := decodeFrameBound([]byte(repeated), func([]byte) any { return &storage })
	if err != nil {
		t.Fatalf("bound decode of a repeated result: %v", err)
	}
	if bound != nil {
		t.Fatalf("bound=%v, want the ordinary decode: one pass merges repeated results", bound != nil)
	}
	if got, want := string(msg.Response.Result.(json.RawMessage)), string(plain.Response.Result.(json.RawMessage)); got != want {
		t.Fatalf("result = %s, ordinary decode got %s", got, want)
	}

	// HostPlanResult refuses a result carrying no arm, which a null is. A null
	// handed to the field's interface clears it instead of reaching that
	// unmarshaler, so only the ordinary decode can report the refusal.
	var plan HostPlanResult
	if err := json.Unmarshal([]byte(`null`), &plan); err == nil {
		t.Fatal("HostPlanResult accepted a null result")
	}
	var planStorage HostPlanResult
	if _, bound, _ := decodeFrameBound([]byte(nullable), func([]byte) any { return &planStorage }); bound != nil {
		t.Fatalf("bound=%v, want the ordinary decode to report the refusal", bound != nil)
	}
	if planStorage.HostPlanPlanned != nil || planStorage.HostPlanNoToken != nil {
		t.Fatalf("target = %+v, want it left alone", planStorage)
	}
}

// Size must not decide whether a model-control contract holds. The bound path
// decodes a large response without re-reading the frame, so before this check a
// version-1 thread/read missing supportsReasoning was refused below the bound's
// threshold and accepted above it, fabricating a false the daemon never sent.
func TestDecodeFrameBoundRefusesIncompleteModelControlAtEverySize(t *testing.T) {
	pad := strings.Repeat("x", boundFrameMinSize)
	result := `{"thread":{"id":"t1","evener":{"capabilities":{"modelControlVersion":1},` +
		`"modelControl":{"authority":{"instanceId":"i1","bootGeneration":"b1"},"revision":3,` +
		`"active":{"modelProvider":"openai","model":"gpt-5.4","reasoningEffortLevels":["high"]}}}}}`

	// The ordinary request path refuses the omission, so a larger frame that
	// carries the same result must refuse it too.
	var small ThreadReadResponse
	ordinaryErr := decodeResponseResult(MethodThreadRead, json.RawMessage(result), &small)
	if ordinaryErr == nil {
		t.Fatal("ordinary decode accepted a version-1 envelope with no supportsReasoning")
	}

	frame := []byte(`{"id":1,"result":` + result + `,"pad":"` + pad + `"}`)
	var storage ThreadReadResponse
	msg, bound, err := decodeFrameBound(frame, func([]byte) any { return &storage })
	// The bound fast path declines a result it refuses, so the frame leaves as an
	// ordinary message and the request's own decode reports the refusal. The
	// caller must not be able to tell which size produced the frame.
	if err != nil {
		t.Fatalf("decodeFrameBound reported a receive error for a per-request refusal: %v", err)
	}
	if bound != nil {
		t.Fatalf("refused frame returned bound storage: %+v", bound)
	}
	raw, ok := msg.Response.Result.(json.RawMessage)
	if !ok {
		t.Fatalf("Response.Result = %T, want the wire bytes for the ordinary decode", msg.Response.Result)
	}
	var delivered ThreadReadResponse
	boundErr := decodeResponseResult(MethodThreadRead, raw, &delivered)
	if boundErr == nil {
		t.Fatal("bound decode accepted a version-1 envelope with no supportsReasoning")
	}
	var boundRefusal, ordinaryRefusal responseDecodeError
	if !errors.As(boundErr, &boundRefusal) {
		t.Fatalf("bound refusal = %T %v, want the response decode error class", boundErr, boundErr)
	}
	if !errors.As(ordinaryErr, &ordinaryRefusal) {
		t.Fatalf("ordinary refusal = %T %v, want the response decode error class", ordinaryErr, ordinaryErr)
	}
	if boundRefusal.method != ordinaryRefusal.method {
		t.Fatalf("bound refusal names method %q, ordinary refusal names %q",
			boundRefusal.method, ordinaryRefusal.method)
	}
}

// BenchmarkMessageDecodeLargeResponse measures one large response frame decoded
// into its typed result, shaped like a probe's thread/read answer whose
// diagnostics carry every retained delegate's task text. The frame is built once,
// so the measurement is the decode: the result is bound to its request by id and
// filled by the pass that validates the frame. The bind itself is one map lookup
// under the client's lock, which the routing test exercises.
func BenchmarkMessageDecodeLargeResponse(b *testing.B) {
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
		var storage ThreadReadResponse
		msg, bound, err := decodeFrameBound(frame, func([]byte) any { return &storage })
		if err != nil {
			b.Fatal(err)
		}
		if bound == nil || storage.Thread.ID != "root" {
			b.Fatalf("bound=%v Thread.ID=%q, want the result filled from the frame", bound != nil, storage.Thread.ID)
		}
		if _, ok := msg.Response.Result.(*ThreadReadResponse); !ok {
			b.Fatalf("Response.Result = %T, want the filled target", msg.Response.Result)
		}
	}
}

// refusingFrameTransport feeds canned frames through the bound decode path, so a
// test can drive a real Client over the path that decodes a large result while
// the frame is read.
type refusingFrameTransport struct {
	reads  chan []byte
	writes chan Message
}

func (b *refusingFrameTransport) Send(_ context.Context, msg Message) error {
	b.writes <- msg
	return nil
}

func (b *refusingFrameTransport) Recv(ctx context.Context) (Message, error) {
	msg, _, err := b.recvBound(ctx, nil)
	return msg, err
}

func (b *refusingFrameTransport) recvBound(ctx context.Context, bind frameBinder) (Message, any, error) {
	select {
	case frame := <-b.reads:
		return decodeFrameBound(frame, bind)
	case <-ctx.Done():
		return Message{}, nil, ctx.Err()
	}
}

func (b *refusingFrameTransport) Close() error { return nil }

// A refused result belongs to the request that asked for it. The bound decode
// path reads frames for the whole connection, so reporting its refusal as a
// receive error would latch the connection closed and fail every other pending
// call, for a frame the ordinary path reports to one caller alone.
func TestClientKeepsTheConnectionWhenABoundResultIsRefused(t *testing.T) {
	pad := strings.Repeat("x", boundFrameMinSize)
	// A version-1 thread/read missing supportsReasoning, refused at any size.
	refusedResult := `{"thread":{"id":"t1","evener":{"capabilities":{"modelControlVersion":1},` +
		`"modelControl":{"authority":{"instanceId":"i1","bootGeneration":"b1"},"revision":3,` +
		`"active":{"modelProvider":"openai","model":"gpt-5.4","reasoningEffortLevels":["high"]}}}}}`

	transport := &refusingFrameTransport{reads: make(chan []byte, 8), writes: make(chan Message, 8)}
	client := NewClient(transport)
	client.Start(t.Context())

	// Two requests are in flight on one connection; the first one's result is
	// the refused envelope.
	refused := make(chan error, 1)
	go func() {
		_, err := client.ThreadRead(t.Context(), ThreadReadParams{Ref: "local:t1"})
		refused <- err
	}()
	first := <-transport.writes

	other := make(chan error, 1)
	go func() {
		_, err := client.ThreadList(t.Context(), ThreadListParams{Limit: 1})
		other <- err
	}()
	second := <-transport.writes

	firstID, err := json.Marshal(first.Request.ID)
	if err != nil {
		t.Fatalf("marshal first id: %v", err)
	}
	transport.reads <- []byte(`{"id":` + string(firstID) + `,"result":` + refusedResult + `,"pad":"` + pad + `"}`)
	if err := <-refused; err == nil {
		t.Fatal("the refused result was delivered as a success")
	}

	// A notification must still arrive, and the other pending call must still
	// be answered on the same connection.
	transport.reads <- []byte(`{"method":"` + NotifyThreadStatusChanged + `","params":{"seq":1}}`)
	select {
	case notification, ok := <-client.Notifications():
		if !ok {
			t.Fatal("notifications stopped after one refused result")
		}
		if notification.Method != NotifyThreadStatusChanged {
			t.Fatalf("notification = %+v", notification)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no notification arrived after a refused result; the connection was latched closed")
	}
	secondID, err := json.Marshal(second.Request.ID)
	if err != nil {
		t.Fatalf("marshal second id: %v", err)
	}
	transport.reads <- []byte(`{"id":` + string(secondID) + `,"result":{"data":[]}}`)
	if err := <-other; err != nil {
		t.Fatalf("an unrelated pending call failed after a refused result: %v", err)
	}
}
