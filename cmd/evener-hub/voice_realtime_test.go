package hub

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type realtimePeer struct {
	conn *websocket.Conn
	ctx  context.Context
	t    *testing.T
}

func openRealtimeTest(t *testing.T) (*VoiceRealtime, realtimePeer) {
	t.Helper()
	cfg, peers := realtimeTestServer(t)
	type result struct {
		v   *VoiceRealtime
		err error
	}
	done := make(chan result, 1)
	go func() { v, e := OpenVoiceRealtime(t.Context(), cfg); done <- result{v, e} }()
	p := <-peers
	p.read()
	p.send(`{"type":"session.updated","session":{"audio":{"input":{"format":{"type":"audio/pcm","rate":24000},"turn_detection":null},"output":{"format":{"type":"audio/pcm","rate":24000},"voice":"marin"}},"tools":[],"output_modalities":["text"]}}`)
	r := <-done
	if r.err != nil {
		t.Fatal(r.err)
	}
	t.Cleanup(func() { _ = r.v.Close() })
	return r.v, p
}
func realtimeCommittedInput(t *testing.T, v *VoiceRealtime, p realtimePeer) string {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- v.BeginInput(t.Context()) }()
	if p.read()["type"] != "input_audio_buffer.clear" {
		t.Fatal("missing input clear")
	}
	p.send(`{"type":"input_audio_buffer.cleared"}`)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	pcm := []byte{0, 0, 255, 127, 0, 128}
	if err := v.AppendPCM(t.Context(), pcm); err != nil {
		t.Fatal(err)
	}
	appendEvent := p.read()
	if appendEvent["type"] != "input_audio_buffer.append" || appendEvent["audio"] != base64.StdEncoding.EncodeToString(pcm) {
		t.Fatal("PCM changed on wire")
	}
	type committed struct {
		id  string
		err error
	}
	result := make(chan committed, 1)
	go func() { id, e := v.CommitInput(t.Context()); result <- committed{id, e} }()
	if p.read()["type"] != "input_audio_buffer.commit" {
		t.Fatal("missing commit")
	}
	select {
	case <-result:
		t.Fatal("commit returned without provider item identity")
	case <-time.After(10 * time.Millisecond):
	}
	p.send(`{"type":"input_audio_buffer.committed","previous_item_id":null,"item_id":"input-7"}`)
	r := <-result
	if r.err != nil || r.id != "input-7" {
		t.Fatalf("commit = %q, %v", r.id, r.err)
	}
	return r.id
}

func TestVoiceRealtimeCompletedInterpretation(t *testing.T) {
	for _, tc := range []struct {
		name, status, itemStatus, args string
		extra                          bool
		want                           bool
	}{
		{"completed", "completed", "completed", `{"text":"opaque request"}`, false, true},
		{"cancelled", "cancelled", "completed", `{"text":"opaque request"}`, false, false},
		{"incomplete", "incomplete", "completed", `{"text":"opaque request"}`, false, false},
		{"partial_item", "completed", "in_progress", `{"text":"opaque request"}`, false, false},
		{"malformed", "completed", "completed", `{"text":`, false, false},
		{"unknown_field", "completed", "completed", `{"text":"x","action":"execute"}`, false, false},
		{"duplicate_key", "completed", "completed", `{"text":"x","text":"y"}`, false, false},
		{"empty", "completed", "completed", `{"text":"  "}`, false, false},
		{"additional_call", "completed", "completed", `{"text":"x"}`, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, p := openRealtimeTest(t)
			id := realtimeCommittedInput(t, v, p)
			type result struct {
				value VoiceInterpretation
				err   error
			}
			done := make(chan result, 1)
			go func() { r, e := v.Interpret(t.Context(), "turn-9", id); done <- result{r, e} }()
			request := p.read()
			r := request["response"].(map[string]any)
			if request["type"] != "response.create" || r["conversation"] != "none" {
				t.Fatalf("interpretation was not out-of-band: %#v", request)
			}
			input := r["input"].([]any)
			ref := input[0].(map[string]any)
			tools := r["tools"].([]any)
			modalities := r["output_modalities"].([]any)
			if len(input) != 1 || ref["id"] != "input-7" || ref["type"] != "item_reference" || len(tools) != 1 || tools[0].(map[string]any)["name"] != "submit_utterance" || len(modalities) != 1 || modalities[0] != "text" {
				t.Fatal("interpretation request exceeded authority")
			}
			p.send(`{"type":"response.created","response":{"id":"resp-8","status":"in_progress","metadata":{"turn_id":"turn-9","input_item_id":"input-7"}}}`)
			p.send(`{"type":"response.function_call_arguments.done","response_id":"resp-8","item_id":"call-item-3","call_id":"call-4","name":"submit_utterance","arguments":"{\"text\":\"not yet committed\"}"}`)
			select {
			case <-done:
				t.Fatal("arguments.done admitted an intent")
			case <-time.After(10 * time.Millisecond):
			}
			// A stale terminal response cannot supply the current request's intent.
			p.send(`{"type":"response.done","response":{"id":"old-response","status":"completed","metadata":{"turn_id":"turn-9","input_item_id":"input-7"},"output":[]}}`)
			args, _ := json.Marshal(tc.args)
			item := `{"id":"call-item-3","type":"function_call","status":"` + tc.itemStatus + `","name":"submit_utterance","call_id":"call-4","arguments":` + string(args) + `}`
			output := item
			if tc.extra {
				output += "," + item
			}
			wire := `{"type":"response.done","response":{"id":"resp-8","status":"` + tc.status + `","metadata":{"turn_id":"turn-9","input_item_id":"input-7"},"output":[` + output + `],"usage":{"input_tokens":11,"output_tokens":3,"total_tokens":14}}}`
			p.send(wire)
			got := <-done
			if tc.want {
				if got.err != nil || got.value.Text != "opaque request" || got.value.ResponseID != "resp-8" || got.value.Usage.TotalTokens != 14 {
					t.Fatalf("interpretation=%+v err=%v", got.value, got.err)
				}
			} else if got.err == nil {
				t.Fatalf("unsafe completed response admitted: %+v", got.value)
			}
			// Duplicate completion cannot admit the consumed input a second time.
			p.send(wire)
			if _, e := v.Interpret(t.Context(), "turn-9", id); e == nil {
				t.Fatal("duplicate interpretation admitted")
			}
		})
	}
}

func TestVoiceRealtimeInputClearLostAckAndEOF(t *testing.T) {
	for _, eof := range []bool{false, true} {
		t.Run(map[bool]string{false: "lost_ack", true: "eof"}[eof], func(t *testing.T) {
			v, p := openRealtimeTest(t)
			done := make(chan error, 1)
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			go func() { done <- v.BeginInput(ctx) }()
			p.read()
			if eof {
				_ = p.conn.CloseNow()
			}
			err := <-done
			var typed *VoiceRealtimeError
			if !errors.As(err, &typed) || !typed.ReplaceConnection {
				t.Fatalf("missing replacement requirement: %v", err)
			}
		})
	}
}

func (p realtimePeer) read() map[string]any {
	p.t.Helper()
	_, b, err := p.conn.Read(p.ctx)
	if err != nil {
		p.t.Fatalf("provider read: %v", err)
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		p.t.Fatal(err)
	}
	return v
}
func (p realtimePeer) send(v string) {
	p.t.Helper()
	if err := p.conn.Write(p.ctx, websocket.MessageText, []byte(v)); err != nil {
		p.t.Fatalf("provider write: %v", err)
	}
}
func realtimeTestServer(t *testing.T) (VoiceRealtimeConfig, <-chan realtimePeer) {
	t.Helper()
	accepted := make(chan realtimePeer, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		c.SetReadLimit(2 << 20)
		accepted <- realtimePeer{c, t.Context(), t}
		<-t.Context().Done()
		_ = c.CloseNow()
	}))
	t.Cleanup(server.Close)
	return VoiceRealtimeConfig{Endpoint: "ws" + strings.TrimPrefix(server.URL, "http"), Model: "gpt-realtime-2.1-mini", Voice: "marin", HTTPClient: server.Client()}, accepted
}
func TestVoiceRealtimeSessionConfiguration(t *testing.T) {
	cfg, peers := realtimeTestServer(t)
	type outcome struct {
		client *VoiceRealtime
		err    error
	}
	opened := make(chan outcome, 1)
	go func() { c, e := OpenVoiceRealtime(t.Context(), cfg); opened <- outcome{c, e} }()
	peer := <-peers
	request := peer.read()
	peer.send(`{"type":"session.updated","session":{"type":"realtime","audio":{"input":{"format":{"type":"audio/pcm","rate":24000},"turn_detection":null},"output":{"format":{"type":"audio/pcm","rate":24000},"voice":"marin"}},"tools":[],"output_modalities":["text"]}}`)
	result := <-opened
	if result.err != nil {
		t.Fatal(result.err)
	}
	t.Cleanup(func() { _ = result.client.Close() })
	session, _ := request["session"].(map[string]any)
	audio, _ := session["audio"].(map[string]any)
	input, _ := audio["input"].(map[string]any)
	output, _ := audio["output"].(map[string]any)
	format, _ := input["format"].(map[string]any)
	turn, hasTurn := input["turn_detection"]
	if request["type"] != "session.update" || session["type"] != "realtime" || !hasTurn || turn != nil || format["type"] != "audio/pcm" || format["rate"] != float64(24000) || output["voice"] != "marin" {
		t.Fatalf("session missing app-owned endpointing or configured PCM/voice: %#v", request)
	}
	if result.client.Capabilities().ExactReading || result.client.Capabilities().ConnectionLimit != 60*time.Minute {
		t.Fatal("incorrect adapter capability declaration")
	}
}

func TestVoiceRealtimeWaitsForSessionUpdated(t *testing.T) {
	cfg, peers := realtimeTestServer(t)
	opened := make(chan *VoiceRealtime, 1)
	go func() { c, _ := OpenVoiceRealtime(t.Context(), cfg); opened <- c }()
	peer := <-peers
	peer.read()
	peer.send(`{"type":"session.created","session":{"id":"session-initial"}}`)
	select {
	case c := <-opened:
		if c != nil {
			_ = c.Close()
		}
		t.Fatal("opened before provider acknowledged session.update")
	case <-time.After(30 * time.Millisecond):
	}
	peer.send(`{"type":"session.updated","session":{"audio":{"input":{"format":{"type":"audio/pcm","rate":24000},"turn_detection":null},"output":{"format":{"type":"audio/pcm","rate":24000},"voice":"marin"}},"tools":[],"output_modalities":["text"]}}`)
	c := <-opened
	if c == nil {
		t.Fatal("acknowledged setup failed")
	}
	_ = c.Close()
}

func realtimeSpeak(t *testing.T, v *VoiceRealtime, p realtimePeer, delivery, generation, response string) {
	t.Helper()
	n, err := strconv.ParseUint(generation, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- v.Speak(t.Context(), delivery, n, "canonical sentinel") }()
	r := p.read()["response"].(map[string]any)
	input := r["input"].([]any)
	message := input[0].(map[string]any)
	content := message["content"].([]any)
	if r["conversation"] != "auto" || r["tool_choice"] != "none" || len(r["tools"].([]any)) != 0 || len(input) != 1 || len(content) != 1 || content[0].(map[string]any)["text"] != "canonical sentinel" {
		t.Fatal("speech included authority or noncanonical context")
	}
	p.send(`{"type":"response.created","response":{"id":"` + response + `","status":"in_progress","metadata":{"delivery_id":"` + delivery + `","generation":"` + generation + `"}}}`)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func realtimeNextEvent(t *testing.T, v *VoiceRealtime) VoiceRealtimeEvent {
	t.Helper()
	select {
	case e, ok := <-v.Events():
		if !ok {
			t.Fatal("event stream closed")
		}
		return e
	case <-time.After(time.Second):
		t.Fatal("provider output never reached consumer")
		return VoiceRealtimeEvent{}
	}
}
func TestVoiceRealtimeSpeechOutputNotPlayback(t *testing.T) {
	v, p := openRealtimeTest(t)
	realtimeSpeak(t, v, p, "delivery-1", "1", "speech-1")
	p.send(`{"type":"response.output_item.added","response_id":"speech-1","output_index":0,"item":{"id":"audio-1","type":"message","role":"assistant","status":"in_progress","content":[]}}`)
	p.send(`{"type":"response.output_audio.delta","response_id":"speech-1","item_id":"audio-1","content_index":0,"delta":"AAABAA=="}`)
	e := realtimeNextEvent(t, v)
	if e.Kind != "audio" || e.Generation != 1 || e.DeliveryID != "delivery-1" || e.ResponseID != "speech-1" || e.ItemID != "audio-1" || !bytes.Equal(e.PCM, []byte{0, 0, 1, 0}) {
		t.Fatalf("PCM event=%+v", e)
	}
	p.send(`{"type":"response.output_audio_transcript.done","response_id":"speech-1","item_id":"audio-1","content_index":0,"transcript":"generated text, not playback"}`)
	e = realtimeNextEvent(t, v)
	if e.Kind != "transcript_done" || e.Transcript != "generated text, not playback" {
		t.Fatalf("transcript=%+v", e)
	}
	p.send(`{"type":"response.output_audio.done","response_id":"speech-1","item_id":"audio-1","content_index":0}`)
	p.send(`{"type":"response.done","response":{"id":"speech-1","status":"completed","metadata":{"delivery_id":"delivery-1","generation":"1"},"output":[{"id":"audio-1","type":"message","status":"completed"}],"usage":{"input_tokens":2,"output_tokens":5,"total_tokens":7}}}`)
	e = realtimeNextEvent(t, v)
	if e.Kind != "output_done" || e.Status != "completed" {
		t.Fatalf("completion=%+v", e)
	}
	e = realtimeNextEvent(t, v)
	if e.Kind != "usage" || e.Usage.TotalTokens != 7 {
		t.Fatalf("usage=%+v", e)
	}
	select {
	case e := <-v.Events():
		t.Fatalf("extra event, playback must remain external: %+v", e)
	case <-time.After(10 * time.Millisecond):
	}
}

func TestVoiceRealtimeInterruptAcknowledgesPlayedTimeline(t *testing.T) {
	v, p := openRealtimeTest(t)
	realtimeSpeak(t, v, p, "delivery-1", "1", "speech-1")
	p.send(`{"type":"response.output_item.added","response_id":"speech-1","item":{"id":"audio-1","type":"message"}}`)
	pcm := base64.StdEncoding.EncodeToString(make([]byte, 200))
	p.send(`{"type":"response.output_audio.delta","response_id":"speech-1","item_id":"audio-1","content_index":0,"delta":"` + pcm + `"}`)
	if e := realtimeNextEvent(t, v); e.Kind != "audio" {
		t.Fatalf("expected generated audio: %+v", e)
	}
	done := make(chan error, 1)
	go func() { done <- v.Interrupt(t.Context(), 1, map[string]int64{"audio-1": 49}) }()
	cancel := p.read()
	if cancel["type"] != "response.cancel" || cancel["response_id"] != "speech-1" {
		t.Fatalf("cancel=%#v", cancel)
	}
	select {
	case <-done:
		t.Fatal("interrupt succeeded without cancel acknowledgement")
	case <-time.After(10 * time.Millisecond):
	}
	// This item was generated but never played, including after the local fence.
	p.send(`{"type":"response.output_item.added","response_id":"speech-1","item":{"id":"audio-2","type":"message"}}`)
	p.send(`{"type":"response.output_audio.delta","response_id":"speech-1","item_id":"audio-2","content_index":0,"delta":"AAABAA=="}`)
	p.send(`{"type":"response.done","response":{"id":"speech-1","status":"cancelled","metadata":{"delivery_id":"delivery-1","generation":"1"},"output":[{"id":"audio-1","type":"message"},{"id":"audio-2","type":"message"}]}}`)
	for _, item := range []struct {
		id string
		ms int
	}{{"audio-1", 2}, {"audio-2", 0}} {
		trunc := p.read()
		if trunc["type"] != "conversation.item.truncate" || trunc["item_id"] != item.id || trunc["content_index"] != float64(0) || trunc["audio_end_ms"] != float64(item.ms) {
			t.Fatalf("truncate must use played floor milliseconds: %#v", trunc)
		}
		select {
		case <-done:
			t.Fatal("interrupt succeeded without truncate acknowledgement")
		case <-time.After(10 * time.Millisecond):
		}
		p.send(`{"type":"conversation.item.truncated","item_id":"` + item.id + `","content_index":0,"audio_end_ms":` + strconv.Itoa(item.ms) + `}`)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-v.Events():
		t.Fatalf("fenced output leaked: %+v", e)
	case <-time.After(10 * time.Millisecond):
	}
	realtimeSpeak(t, v, p, "delivery-2", "2", "speech-2")
	p.send(`{"type":"response.output_audio.delta","response_id":"speech-1","item_id":"audio-1","content_index":0,"delta":"AAABAA=="}`)
	p.send(`{"type":"response.output_item.added","response_id":"speech-2","item":{"id":"audio-3","type":"message"}}`)
	p.send(`{"type":"response.output_audio.delta","response_id":"speech-2","item_id":"audio-3","content_index":0,"delta":"AgADAA=="}`)
	e := realtimeNextEvent(t, v)
	if e.Kind != "audio" || e.Generation != 2 || e.ItemID != "audio-3" {
		t.Fatalf("stale generation leaked: %+v", e)
	}
}

func TestVoiceRealtimeTruncateRequiresReplacement(t *testing.T) {
	for _, mode := range []string{"lost_ack", "wrong_timeline", "refused", "eof"} {
		t.Run(mode, func(t *testing.T) {
			v, p := openRealtimeTest(t)
			realtimeSpeak(t, v, p, "delivery-1", "1", "speech-1")
			p.send(`{"type":"response.output_item.added","response_id":"speech-1","item":{"id":"audio-1","type":"message"}}`)
			p.send(`{"type":"response.output_audio.delta","response_id":"speech-1","item_id":"audio-1","content_index":0,"delta":"AAABAA=="}`)
			realtimeNextEvent(t, v)
			// Generated completion is not proof of playback. It still needs trim to 0.
			p.send(`{"type":"response.done","response":{"id":"speech-1","status":"completed","metadata":{"delivery_id":"delivery-1","generation":"1"},"output":[{"id":"audio-1","type":"message"}]}}`)
			if e := realtimeNextEvent(t, v); e.Kind != "output_done" {
				t.Fatalf("done=%+v", e)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- v.Interrupt(ctx, 1, nil) }()
			trunc := p.read()
			if trunc["type"] != "conversation.item.truncate" || trunc["audio_end_ms"] != float64(0) {
				t.Fatalf("completed unheard audio not truncated: %#v", trunc)
			}
			switch mode {
			case "wrong_timeline":
				p.send(`{"type":"conversation.item.truncated","item_id":"audio-1","content_index":0,"audio_end_ms":1}`)
			case "refused":
				p.send(`{"type":"error","error":{"type":"invalid_request_error","code":"unsupported","message":"PROVIDER-PRIVATE-SENTINEL"}}`)
			case "eof":
				_ = p.conn.CloseNow()
			}
			err := <-done
			var typed *VoiceRealtimeError
			if !errors.As(err, &typed) || !typed.ReplaceConnection || strings.Contains(err.Error(), "PROVIDER-PRIVATE-SENTINEL") {
				t.Fatalf("unsafe reconciliation error: %v", err)
			}
			if e := v.Speak(t.Context(), "delivery-2", 2, "must not reuse"); !errors.As(e, &typed) || !typed.ReplaceConnection {
				t.Fatalf("unreconciled connection hid replacement requirement: %v", e)
			}
		})
	}
}

func TestVoiceRealtimeQueuePressure(t *testing.T) {
	t.Run("microphone", func(t *testing.T) {
		v, p := openRealtimeTest(t)
		done := make(chan error, 1)
		go func() { done <- v.BeginInput(t.Context()) }()
		p.read()
		p.send(`{"type":"input_audio_buffer.cleared"}`)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		err := v.AppendPCM(t.Context(), make([]byte, (64<<10)+2))
		var typed *VoiceRealtimeError
		if !errors.As(err, &typed) || !typed.ReplaceConnection {
			t.Fatalf("oversized microphone frame not aborted: %v", err)
		}
		if _, err := v.CommitInput(t.Context()); err == nil {
			t.Fatal("partial microphone turn committed")
		}
	})
	t.Run("speaker", func(t *testing.T) {
		v, p := openRealtimeTest(t)
		realtimeSpeak(t, v, p, "delivery-1", "1", "speech-1")
		p.send(`{"type":"response.output_item.added","response_id":"speech-1","item":{"id":"audio-1","type":"message"}}`)
		// Do not consume Events. Exactly 33 chunks exceed the documented 32 slots.
		for i := 0; i < 33; i++ {
			p.send(`{"type":"response.output_audio.delta","response_id":"speech-1","item_id":"audio-1","content_index":0,"delta":"AAABAA=="}`)
		}
		select {
		case <-v.readerDone:
		case <-time.After(time.Second):
			t.Fatal("slow consumer did not abort")
		}
		e := realtimeNextEvent(t, v)
		if e.Kind != "error" || e.Err.Reason != "output_overflow" || !e.Err.ReplaceConnection {
			t.Fatalf("pressure result=%+v", e)
		}
		if _, ok := <-v.Events(); ok {
			t.Fatal("overflow retained playable partial output")
		}
	})
}

func TestVoiceRealtimeMalformedEventClosesReader(t *testing.T) {
	v, p := openRealtimeTest(t)
	p.send(`{"type":`)
	e := realtimeNextEvent(t, v)
	if e.Kind != "error" || e.Err.Reason != "malformed_event" {
		t.Fatalf("malformed event=%+v", e)
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := <-v.Events(); ok {
		t.Fatal("closed reader retained events")
	}
}

func TestVoiceRealtimeRejectsUnsafeSessionAcknowledgement(t *testing.T) {
	cfg, peers := realtimeTestServer(t)
	done := make(chan error, 1)
	go func() {
		v, err := OpenVoiceRealtime(t.Context(), cfg)
		if v != nil {
			_ = v.Close()
		}
		done <- err
	}()
	p := <-peers
	p.read()
	p.send(`{"type":"session.updated","session":{"audio":{"input":{"format":{"type":"audio/pcm","rate":24000},"turn_detection":{"type":"server_vad","create_response":true}},"output":{"format":{"type":"audio/pcm","rate":24000},"voice":"marin"}},"tools":[],"output_modalities":["text"]}}`)
	if err := <-done; err == nil {
		t.Fatal("provider enabled automatic responses but adapter opened")
	}
}

func TestVoiceRealtimeControlDoesNotWaitForProvider(t *testing.T) {
	v, p := openRealtimeTest(t)
	speak := make(chan error, 1)
	go func() { speak <- v.Speak(t.Context(), "delivery-1", 1, "canonical sentinel") }()
	if p.read()["type"] != "response.create" {
		t.Fatal("speech request never reached provider")
	}
	// The provider withholds response.created. Interrupt must fence and close,
	// not wait behind Speak's acknowledgement deadline or invent a response ID.
	interrupted := make(chan error, 1)
	go func() { interrupted <- v.Interrupt(t.Context(), 1, nil) }()
	select {
	case err := <-interrupted:
		var typed *VoiceRealtimeError
		if !errors.As(err, &typed) || !typed.ReplaceConnection || typed.Reason != "control_busy" {
			t.Fatalf("control=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("control starved behind pending speech")
	}
	if err := <-speak; err == nil {
		t.Fatal("unacknowledged speech succeeded")
	}
}

func TestVoiceRealtimeConcurrentMicrophoneAbortsPartialTurn(t *testing.T) {
	v, p := openRealtimeTest(t)
	done := make(chan error, 1)
	go func() { done <- v.BeginInput(t.Context()) }()
	p.read()
	p.send(`{"type":"input_audio_buffer.cleared"}`)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := v.AppendPCM(t.Context(), []byte{0, 0}); err != nil {
		t.Fatal(err)
	}
	p.read()
	go func() { _, err := v.CommitInput(t.Context()); done <- err }()
	if p.read()["type"] != "input_audio_buffer.commit" {
		t.Fatal("commit not pending")
	}
	err := v.AppendPCM(t.Context(), []byte{1, 0})
	var typed *VoiceRealtimeError
	if !errors.As(err, &typed) || typed.Reason != "input_backpressure" || !typed.ReplaceConnection {
		t.Fatalf("concurrent PCM not aborted: %v", err)
	}
	if err := <-done; err == nil {
		t.Fatal("aborted partial turn committed")
	}
}

func TestVoiceRealtimeLifetimeCancellationClosesReader(t *testing.T) {
	cfg, peers := realtimeTestServer(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	type result struct {
		v   *VoiceRealtime
		err error
	}
	done := make(chan result, 1)
	go func() { v, err := OpenVoiceRealtime(ctx, cfg); done <- result{v, err} }()
	p := <-peers
	p.read()
	p.send(`{"type":"session.updated","session":{"audio":{"input":{"format":{"type":"audio/pcm","rate":24000},"turn_detection":null},"output":{"format":{"type":"audio/pcm","rate":24000},"voice":"marin"}},"tools":[],"output_modalities":["text"]}}`)
	r := <-done
	if r.err != nil {
		t.Fatal(r.err)
	}
	cancel()
	select {
	case <-r.v.readerDone:
	case <-time.After(time.Second):
		t.Fatal("cancelled reader leaked")
	}
	if err := r.v.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestVoiceRealtimeBoundsResponseIdentity(t *testing.T) {
	v, p := openRealtimeTest(t)
	done := make(chan error, 1)
	go func() { done <- v.Speak(t.Context(), "delivery-1", 1, "canonical sentinel") }()
	p.read()
	p.send(`{"type":"response.created","response":{"id":"` + strings.Repeat("x", 257) + `","status":"in_progress","metadata":{"delivery_id":"delivery-1","generation":"1"}}}`)
	err := <-done
	var typed *VoiceRealtimeError
	if !errors.As(err, &typed) || !typed.ReplaceConnection {
		t.Fatalf("unbounded response identity admitted: %v", err)
	}
}
