package hub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/e2ecap"
	"primeradiant.com/evener/test/e2e/fakellm"
)

// The manager, mutations, transcript projection, notifications and journal are
// real. Only external model replies and the provider/device sockets are scripted.
func TestE2E_VoiceTypedCorrectionRetiresPlayingReply(t *testing.T) {
	f := newVoiceCorrectionStack(t)
	const alpha = "CORRECTION-ALPHA-INPUT-79"
	const alphaReply = "CORRECTION-ALPHA-OUTPUT-79"
	const beta = "CORRECTION-BETA-INPUT-83"
	const betaReply = "CORRECTION-BETA-OUTPUT-83"
	f.admit(t, alpha, false)
	requestAlpha := f.next(t)
	if !requestAlpha.Contains(alpha) || requestAlpha.Contains(beta) {
		t.Fatal("Alpha model request has the wrong input")
	}
	requestAlpha.RespondToolCall("communicate", communicateArgs(alphaReply))
	f.waitRead(t, "Alpha committed and manager idle", func(read appwire.ThreadReadResponse) bool {
		return read.Thread.Evener.ActiveTurnID == "" && voiceCorrectionItem(read, alphaReply).ID != ""
	})
	start := voiceJourneyEvent(t, f.device, "speech_start")
	peer := f.openSpeaker(t)
	old := f.speechRequest(t, peer, start, alphaReply)
	const oldResponse, oldItem = "correction-response-alpha", "correction-item-alpha"
	oldPCM := base64.StdEncoding.EncodeToString(make([]byte, 960))
	voiceCorrectionAudio(peer, old, oldResponse, oldItem, oldPCM)
	audio := voiceJourneyEvent(t, f.device, "audio")
	if audio["pcm"] != oldPCM || audio["deliveryId"] != old.ID || audio["generation"] != float64(old.PresentationGeneration) {
		t.Fatal("old canonical delivery was not actually playing on the device")
	}
	// Simulated device rendering, not received/generated samples: play half.
	f.device.send(fmt.Sprintf(`{"type":"played","deliveryId":%q,"generation":%d,"itemId":%q,"samples":240}`, old.ID, old.PresentationGeneration, oldItem))
	f.waitDelivery(t, old.ID, func(d hubcore.SpeechDelivery) bool {
		return len(d.Items) == 1 && d.Items[0].PlayedSamples == 240
	})
	betaID := f.admit(t, beta, false)
	requestBeta := f.next(t) // Hold the actual newer model response throughout the fence.
	if !requestBeta.Contains(beta) || !requestBeta.Contains(alpha) {
		t.Fatal("typed correction lost input or original manager context")
	}
	running := f.read(t)
	if running.Thread.Evener.ActiveTurnID == "" || !slices.Contains(running.Thread.Evener.ConversationProvenance.AdmittedInputs, "client:"+betaID) {
		t.Fatal("typed Beta was not admitted into the active original manager")
	}
	t.Logf("PLAYING reached: committed source=%s delivery=%s generation=%d, PCM received, played=240/480, Beta admission=%s, newer model reply held", old.CanonicalSourceItemID, old.ID, old.PresentationGeneration, betaID)

	// A timeout is a failing tripwire, never evidence of successful suppression.
	stopCtx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	_, raw, err := f.device.conn.Read(stopCtx)
	if err != nil {
		d := f.delivery(t, old.ID)
		after := f.read(t)
		t.Fatalf("typed-admission stop boundary: no device stop before newer output; interrupted=%t managerActive=%t sameInstance=%t: %v; cancel/truncate, late PCM and fresh speech assertions not reached", d.Interrupted, after.Thread.Evener.ActiveTurnID != "", after.Thread.Evener.InstanceID == f.instance, err)
	}
	var stop map[string]any
	if err := json.Unmarshal(raw, &stop); err != nil {
		t.Fatal(err)
	}
	minimum, _ := stop["generation"].(float64)
	if stop["type"] != "stop" || minimum <= float64(old.PresentationGeneration) {
		t.Fatalf("typed admission did not advance device minimum generation: %v", stop)
	}
	f.waitDelivery(t, old.ID, func(d hubcore.SpeechDelivery) bool { return d.Interrupted })
	after := f.read(t)
	if after.Thread.Evener.ActiveTurnID != running.Thread.Evener.ActiveTurnID {
		t.Fatal("speech interruption stopped or replaced manager work")
	}
	select {
	case <-requestBeta.Cancelled():
		t.Fatal("speech interruption cancelled the held manager model request")
	default:
	}

	cancelRequest := peer.read()
	if cancelRequest["type"] != "response.cancel" || cancelRequest["response_id"] != oldResponse {
		t.Fatalf("playing provider response was not cancelled: %v", cancelRequest)
	}
	peer.send(fmt.Sprintf(`{"type":"response.done","response":{"id":%q,"status":"cancelled","metadata":{"delivery_id":%q,"generation":%q},"output":[]}}`, oldResponse, old.ID, strconv.FormatUint(old.PresentationGeneration, 10)))
	truncate := peer.read()
	if truncate["type"] != "conversation.item.truncate" || truncate["item_id"] != oldItem || truncate["audio_end_ms"] != float64(10) || truncate["content_index"] != float64(0) {
		t.Fatalf("truncate did not use actual played watermark: %v", truncate)
	}
	peer.send(`{"type":"conversation.item.truncated","item_id":"` + oldItem + `","content_index":0,"audio_end_ms":10}`)
	// The fresh response and its terminal device event are ordered positive
	// barriers after this stale packet, not a timed assertion of silence.
	voiceCorrectionDelta(peer, oldResponse, oldItem, "//8=")
	requestBeta.RespondToolCall("communicate", communicateArgs(betaReply))
	freshStart := voiceJourneyEvent(t, f.device, "speech_start")
	fresh := f.speechRequest(t, peer, freshStart, betaReply)
	if fresh.PresentationGeneration < uint64(minimum) || fresh.ID == old.ID {
		t.Fatal("fresh speech reused a retired generation")
	}
	voiceCorrectionAudio(peer, fresh, "correction-response-beta", "correction-item-beta", "AQACAA==")
	voiceCorrectionDelta(peer, oldResponse, oldItem, "//8=")
	voiceCorrectionDone(peer, fresh, "correction-response-beta", "correction-item-beta")
	f.expectFinishedAudio(t, fresh, "AQACAA==")
	final := f.waitRead(t, "Beta committed and original manager idle", func(read appwire.ThreadReadResponse) bool {
		return read.Thread.Evener.ActiveTurnID == "" && voiceCorrectionItem(read, betaReply).ID != ""
	})
	if voiceCorrectionItem(final, alphaReply).ID != old.CanonicalSourceItemID {
		t.Fatal("interrupt deleted or replaced the old canonical text")
	}
	t.Log("typed correction stopped old playback without cancelling work; acknowledged cancel/truncate used played samples; late old PCM excluded through fresh delivery completion")
}

func TestE2E_VoiceLateReplyKeepsOriginatingRequestFrontier(t *testing.T) {
	f := newVoiceCorrectionStack(t)
	const alpha = "LATE-ALPHA-INPUT-91"
	const beta = "LATE-BETA-INPUT-97"
	const alphaReply = "LATE-ALPHA-OUTPUT-91"
	const betaReply = "LATE-BETA-OUTPUT-97"
	alphaID := f.admit(t, alpha, false)
	requestAlpha := f.next(t)
	if !requestAlpha.Contains(alpha) || requestAlpha.Contains(beta) {
		t.Fatal("held actual Alpha request did not exclude Beta")
	}
	before := f.read(t)
	// Held-request frontiers are published only with the model response. The
	// independent expected set is opening + Alpha, both observed in its request.
	oldInputs := make(map[string]bool)
	for _, id := range before.Thread.Evener.ConversationProvenance.AdmittedInputs {
		oldInputs[id] = true
	}
	if len(oldInputs) != 2 || !oldInputs["client:"+alphaID] || !requestAlpha.Contains("CORRECTION-OPENING-61") {
		t.Fatal("held Alpha request lacks its observed admission identity")
	}
	betaID := f.admit(t, beta, true)
	admitted := f.waitRead(t, "Beta admitted while Alpha response held", func(read appwire.ThreadReadResponse) bool {
		return slices.Contains(read.Thread.Evener.ConversationProvenance.AdmittedInputs, "client:"+betaID)
	})
	if admitted.Thread.Evener.ActiveTurnID != before.Thread.Evener.ActiveTurnID {
		t.Fatal("Beta steer replaced Alpha's active manager turn")
	}
	args := communicateArgs(alphaReply)
	args["end_turn"] = false
	requestAlpha.RespondToolCall("communicate", args)
	requestBeta := f.next(t)
	if !requestBeta.Contains(beta) || !requestBeta.Contains(alpha) || requestBeta.PreviousToolCallID() != requestAlpha.ToolCallID() {
		t.Fatal("actual next request did not consume Beta after Alpha communicate")
	}
	incorporated := f.waitRead(t, "late Alpha canonical and Beta incorporated", func(read appwire.ThreadReadResponse) bool {
		return voiceCorrectionItem(read, alphaReply).ID != ""
	})
	alphaItem := voiceCorrectionItem(incorporated, alphaReply)
	p := alphaItem.ConversationProvenance
	if p == nil || p.RequestID == "" || p.FrontierID == "" || p.SourceItemID != alphaItem.ID || !maps.Equal(voiceCorrectionFrontier(t, incorporated.Thread.Evener.ConversationProvenance, p.FrontierID), oldInputs) {
		t.Fatalf("late Alpha output was relabelled with newer state: source=%s provenance=%+v expectedInputs=%v", alphaItem.ID, p, oldInputs)
	}
	newInputs := maps.Clone(oldInputs)
	newInputs["client:"+betaID] = true
	t.Logf("late-request boundary reached: Alpha committed=%s oldRequest=%s oldFrontier=%s, Beta admission=%s observed in actual next request", alphaItem.ID, p.RequestID, p.FrontierID, betaID)
	requestBeta.RespondToolCall("communicate", communicateArgs(betaReply))
	final := f.waitRead(t, "Beta canonical committed", func(read appwire.ThreadReadResponse) bool { return voiceCorrectionItem(read, betaReply).ID != "" })
	betaItem := voiceCorrectionItem(final, betaReply)
	q := betaItem.ConversationProvenance
	if q == nil || q.RequestID == p.RequestID || q.RequestID == "" || q.FrontierID == p.FrontierID || q.SourceItemID != betaItem.ID || !maps.Equal(voiceCorrectionFrontier(t, final.Thread.Evener.ConversationProvenance, q.FrontierID), newInputs) {
		t.Fatalf("Beta output lost its actual distinct request frontier: %+v", q)
	}
	t.Logf("Beta canonical committed=%s request=%s frontier=%s, exact input set includes Beta and old output excludes it", betaItem.ID, q.RequestID, q.FrontierID)
	// Any queued Alpha speech_start is read first and fails. Naturally absent
	// Alpha output is allowed, but its real durable text was proven above.
	start := voiceJourneyEvent(t, f.device, "speech_start")
	if start["canonicalText"] != betaReply {
		t.Fatalf("first speech source after typed Beta is obsolete: source=%v", start["sourceItemId"])
	}
	peer := f.openSpeaker(t)
	delivery := f.speechRequest(t, peer, start, betaReply)
	if delivery.CanonicalSourceItemID != betaItem.ID || delivery.OriginatingRequestID != q.RequestID || delivery.FrontierID != q.FrontierID || delivery.RunGeneration != q.RunGeneration {
		t.Fatalf("speech journal selected provenance other than actual projected Beta source: %+v", delivery)
	}
	voiceCorrectionAudio(peer, delivery, "late-response-beta", "late-item-beta", "AwAEAA==")
	voiceCorrectionDone(peer, delivery, "late-response-beta", "late-item-beta")
	f.expectFinishedAudio(t, delivery, "AwAEAA==")
	if voiceCorrectionItem(final, alphaReply).ID != alphaItem.ID {
		t.Fatal("suppression removed old canonical output")
	}
	t.Log("late Alpha stayed canonical with its original request frontier; only the actual Beta-consuming request became generated speech")
}

type voiceCorrectionStack struct {
	ctx           context.Context
	client        *appwire.Client
	provider      *fakellm.Server
	peers         <-chan realtimePeer
	device        realtimePeer
	journal       *hubcore.VoiceJournal
	ref, instance string
	stack         hubStack
	armed         voiceArmResponse
	headers       http.Header
}

func newVoiceCorrectionStack(t *testing.T) *voiceCorrectionStack {
	t.Helper()
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	if testing.Short() {
		t.Skip("live-stack e2e: real hub and manager daemon")
	}
	provider := newFakeLLMProvider(t)
	config, peers := realtimeTestServer(t)
	stack := startHubStackOnProvider(t, fmt.Sprintf(`
default = "fake"
[providers.fake]
base = "openai-compatible"
base_url = %q
api_key = "fakellm-not-a-secret"
[providers.voice-fixture]
base = "openai-compatible"
base_url = %q
api_key = "voice-fixture-not-a-secret"
`, provider.BaseURL(), "http"+strings.TrimPrefix(config.Endpoint, "ws")+"/v1"), "fake/"+fakellm.ModelID)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	client := stack.dialRPC(ctx, t)
	ref := startManagerThread(ctx, t, client, stack, "CORRECTION-OPENING-61")
	opening, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatal(err)
	}
	opening.RespondToolCall("communicate", communicateArgs("CORRECTION-READY-61"))
	f := &voiceCorrectionStack{ctx: ctx, client: client, provider: provider, peers: peers, ref: ref, stack: stack}
	read, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: ref, Subscribe: true, IncludeTurns: true})
	if err != nil {
		t.Fatal(err)
	}
	f.instance = read.Thread.Evener.InstanceID
	f.waitRead(t, "opening settled", func(read appwire.ThreadReadResponse) bool { return read.Thread.Evener.ActiveTurnID == "" })
	status, raw := voiceTestPost(t, ctx, stack, "/api/voice/arm", stack.token, map[string]any{
		"ref": ref, "expectedInstanceId": f.instance, "deviceId": "correction-device", "provider": "voice-fixture", "model": "gpt-realtime-2.1-mini", "voice": "marin",
	})
	if status != http.StatusOK {
		t.Fatalf("correction Arm status=%d", status)
	}
	var armed voiceArmResponse
	if err := json.Unmarshal(raw, &armed); err != nil {
		t.Fatal(err)
	}
	if armed.BindingID == "" || armed.Epoch == "" || armed.Grant == "" || armed.SocketPath != "/api/voice/socket" {
		t.Fatal("Arm lacks concrete scoped transport")
	}
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+armed.Grant)
	headers.Set("X-Evener-Voice-Binding", armed.BindingID)
	headers.Set("X-Evener-Voice-Epoch", armed.Epoch)
	headers.Set("X-Evener-Voice-Device", "correction-device")
	f.armed, f.headers = armed, headers
	f.connect(t)
	f.journal, err = hubcore.NewVoiceJournal(filepath.Join(stack.home, "state", "evener"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.journal.Close() })
	return f
}

func (f *voiceCorrectionStack) connect(t *testing.T) map[string]any {
	t.Helper()
	device := voiceJourneyConnect(t, f.ctx, "ws://"+f.stack.addr+f.armed.SocketPath, f.headers)
	t.Cleanup(func() { _ = device.CloseNow() })
	f.device = realtimePeer{conn: device, ctx: f.ctx, t: t}
	return voiceJourneyEvent(t, f.device, "ready")
}

func (f *voiceCorrectionStack) read(t *testing.T) appwire.ThreadReadResponse {
	t.Helper()
	read, err := f.client.ThreadRead(f.ctx, appwire.ThreadReadParams{Ref: f.ref, IncludeTurns: true, ItemLimit: appwire.TranscriptItemPageLimit})
	if err != nil {
		t.Fatal(err)
	}
	e := read.Thread.Evener
	if e.InstanceID != f.instance || !e.Manager || !e.MutationStateAuthoritative || e.ConversationProvenance == nil || !e.ConversationProvenance.Authoritative {
		t.Fatal("read lost authoritative original manager identity/provenance")
	}
	return read
}

func (f *voiceCorrectionStack) waitRead(t *testing.T, boundary string, ready func(appwire.ThreadReadResponse) bool) appwire.ThreadReadResponse {
	t.Helper()
	for {
		read := f.read(t)
		if ready(read) {
			return read
		}
		select {
		case _, ok := <-f.client.Notifications():
			if !ok {
				t.Fatalf("notifications ended before %s", boundary)
			}
		case <-f.ctx.Done():
			t.Fatalf("no positive completion for %s: %v", boundary, f.ctx.Err())
		}
	}
}

func (f *voiceCorrectionStack) admit(t *testing.T, text string, steer bool) string {
	t.Helper()
	id := newMutationID(t)
	var receipt appwire.MutationReceipt
	if steer {
		result, err := clientRequest[appwire.TurnSteerResponse](f.ctx, f.client, appwire.MethodTurnSteer, appwire.TurnSteerParams{Ref: f.ref, ExpectedInstanceID: f.instance, ClientMutationID: id, Input: []appwire.InputItem{{Type: "text", Text: text}}})
		if err != nil {
			t.Fatal(err)
		}
		receipt = result.Receipt
	} else {
		result, err := clientRequest[appwire.TurnStartResponse](f.ctx, f.client, appwire.MethodTurnStart, appwire.TurnStartParams{Ref: f.ref, ExpectedInstanceID: f.instance, ClientMutationID: id, Input: []appwire.InputItem{{Type: "text", Text: text}}})
		if err != nil {
			t.Fatal(err)
		}
		receipt = result.Receipt
	}
	if receipt.Disposition != appwire.MutationDispositionApplied || receipt.InstanceID != f.instance || receipt.ClientMutationID != id {
		t.Fatalf("typed input lacks original-instance admission receipt: %+v", receipt)
	}
	return id
}

func (f *voiceCorrectionStack) next(t *testing.T) *fakellm.Call {
	t.Helper()
	call, err := f.provider.Next(f.ctx.Done())
	if err != nil {
		t.Fatal(err)
	}
	return call
}

func (f *voiceCorrectionStack) openSpeaker(t *testing.T) realtimePeer {
	t.Helper()
	var peer realtimePeer
	select {
	case peer = <-f.peers:
	case <-f.ctx.Done():
		t.Fatal("speech provider never connected")
	}
	peer.ctx = f.ctx
	if peer.read()["type"] != "session.update" {
		t.Fatal("missing real provider session setup")
	}
	peer.send(`{"type":"session.updated","session":{"type":"realtime","audio":{"input":{"format":{"type":"audio/pcm","rate":24000},"turn_detection":null},"output":{"format":{"type":"audio/pcm","rate":24000},"voice":"marin"}},"tools":[],"output_modalities":["text"]}}`)
	return peer
}

func (f *voiceCorrectionStack) speechRequest(t *testing.T, peer realtimePeer, start map[string]any, canonical string) hubcore.SpeechDelivery {
	t.Helper()
	request := peer.read()
	response, _ := request["response"].(map[string]any)
	meta, _ := response["metadata"].(map[string]any)
	id, _ := meta["delivery_id"].(string)
	generation, _ := meta["generation"].(string)
	g, err := strconv.ParseUint(generation, 10, 64)
	tools, present := response["tools"].([]any)
	input, _ := response["input"].([]any)
	if err != nil || request["type"] != "response.create" || id == "" || !present || len(tools) != 0 || response["tool_choice"] != "none" || len(input) != 1 {
		t.Fatal("speech request lacks tool-free generation-scoped canonical source")
	}
	message, _ := input[0].(map[string]any)
	content, _ := message["content"].([]any)
	if len(content) != 1 {
		t.Fatal("speaker received extra context")
	}
	text, _ := content[0].(map[string]any)
	if text["text"] != canonical || text["type"] != "input_text" || message["role"] != "user" {
		t.Fatal("speaker did not receive only canonical text")
	}
	d := f.delivery(t, id)
	if d.InstanceID != f.instance || d.CanonicalText != canonical || d.PresentationGeneration != g || start["deliveryId"] != id || start["generation"] != float64(g) || start["sourceItemId"] != d.CanonicalSourceItemID || start["canonicalText"] != canonical {
		t.Fatal("device, provider and durable selected source disagree")
	}
	return d
}

func (f *voiceCorrectionStack) delivery(t *testing.T, id string) hubcore.SpeechDelivery {
	t.Helper()
	d, err := f.journal.SpeechDelivery(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func (f *voiceCorrectionStack) waitDelivery(t *testing.T, id string, ready func(hubcore.SpeechDelivery) bool) {
	t.Helper()
	// The journal exposes no subscription. Poll positive durable state only.
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if ready(f.delivery(t, id)) {
			return
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			t.Fatal("expected durable playback transition never committed")
		}
	}
}

func voiceCorrectionItem(read appwire.ThreadReadResponse, text string) appwire.ThreadItem {
	for _, turn := range read.Thread.Turns {
		for _, item := range turn.Items {
			if item.Type == "agentMessage" && item.Text == text {
				return item
			}
		}
	}
	return appwire.ThreadItem{}
}

// Resolve the returned immutable graph independently, not through the production
// eligibility/resolution helpers. The expected sets come from actual admissions
// and the captured old/new requests, never from current-state relabelling.
func voiceCorrectionFrontier(t *testing.T, state *appwire.ConversationProvenanceState, id string) map[string]bool {
	t.Helper()
	nodes := make(map[string]appwire.ConversationFrontier)
	for _, node := range state.Frontiers {
		nodes[node.ID] = node
	}
	seen := make(map[string]bool)
	var chain []appwire.ConversationFrontier
	for id != "" {
		node, ok := nodes[id]
		if !ok || seen[id] {
			t.Fatalf("unresolvable actual frontier %q", id)
		}
		seen[id] = true
		chain = append(chain, node)
		id = node.BaseID
	}
	result := make(map[string]bool)
	for i := len(chain) - 1; i >= 0; i-- {
		for _, input := range chain[i].AddedInputs {
			result[input] = true
		}
		for _, input := range chain[i].RemovedInputs {
			delete(result, input)
		}
	}
	return result
}

func voiceCorrectionAudio(peer realtimePeer, d hubcore.SpeechDelivery, response, item, pcm string) {
	peer.send(fmt.Sprintf(`{"type":"response.created","response":{"id":%q,"status":"in_progress","metadata":{"delivery_id":%q,"generation":%q}}}`, response, d.ID, strconv.FormatUint(d.PresentationGeneration, 10)))
	peer.send(fmt.Sprintf(`{"type":"response.output_item.added","response_id":%q,"output_index":0,"item":{"id":%q,"type":"message","role":"assistant","status":"in_progress","content":[]}}`, response, item))
	voiceCorrectionDelta(peer, response, item, pcm)
}
func voiceCorrectionDelta(peer realtimePeer, response, item, pcm string) {
	peer.send(fmt.Sprintf(`{"type":"response.output_audio.delta","response_id":%q,"item_id":%q,"content_index":0,"delta":%q}`, response, item, pcm))
}
func voiceCorrectionDone(peer realtimePeer, d hubcore.SpeechDelivery, response, item string) {
	peer.send(fmt.Sprintf(`{"type":"response.done","response":{"id":%q,"status":"completed","metadata":{"delivery_id":%q,"generation":%q},"output":[{"id":%q,"type":"message","status":"completed"}]}}`, response, d.ID, strconv.FormatUint(d.PresentationGeneration, 10), item))
}
func (f *voiceCorrectionStack) expectFinishedAudio(t *testing.T, d hubcore.SpeechDelivery, pcm string) {
	t.Helper()
	for _, kind := range []string{"audio", "speech_end", "delivery_end"} {
		event := voiceJourneyEvent(t, f.device, kind)
		if event["deliveryId"] != d.ID || event["generation"] != float64(d.PresentationGeneration) || (kind == "audio" && event["pcm"] != pcm) {
			t.Fatalf("stale audio crossed ordered fresh %s barrier: %v", kind, event)
		}
	}
}
