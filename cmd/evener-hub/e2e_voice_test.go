package hub

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/e2ecap"
	"primeradiant.com/evener/test/e2e/fakellm"
)

// Only the external model and device are scripted. The hub, daemon, manager
// tools and durable journal are real. Capability remains an endcap assertion
// while the approved Arm response supplies the concrete socket route.
func TestE2E_VoiceReadOnlyManagerJourney(t *testing.T) {
	voiceManagerJourney(t, true)
}

func TestE2E_VoiceIntakeExistingManager(t *testing.T) {
	voiceManagerJourney(t, false)
}

func voiceManagerJourney(t *testing.T, withPlayback bool) {
	t.Helper()
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	if testing.Short() {
		t.Skip("live-stack e2e: builds binaries and runs a hub + daemon")
	}
	provider := newFakeLLMProvider(t)
	voiceConfig, peers := realtimeTestServer(t)
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
`, provider.BaseURL(), "http"+strings.TrimPrefix(voiceConfig.Endpoint, "ws")+"/v1"), "fake/"+fakellm.ModelID)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	client := stack.dialRPC(ctx, t)

	// Features is the peer's initialize response, cached by the real client.
	// Decode it rather than requiring a production Voice field before it exists.
	encoded, err := json.Marshal(client.Features())
	if err != nil {
		t.Fatal(err)
	}
	var features map[string]bool
	if err := json.Unmarshal(encoded, &features); err != nil {
		t.Fatal(err)
	}
	if !features["turnStart"] {
		t.Fatal("initialize did not advertise the existing text input capability")
	}
	t.Logf("initialize discovery: turnStart=%t voice=%t", features["turnStart"], features["voice"])

	// An ordinary independently started session is the read-only inspection
	// target. No fixture supplies its status or writes a synthetic transcript.
	target, err := clientRequest[appwire.ThreadStartResponse](ctx, client, appwire.MethodThreadStart, appwire.ThreadStartParams{
		Harness: "evener", CWD: stack.workDir, Model: stack.model,
		LaunchOverrides: &appwire.LaunchConfigLayer{Sandbox: "off"},
	})
	if err != nil {
		t.Fatalf("start inspection target: %v", err)
	}
	targetRef, targetInstance := target.Thread.Evener.Ref, target.Thread.Evener.InstanceID
	if targetRef == "" || targetInstance == "" || target.Thread.Evener.Manager {
		t.Fatalf("invalid ordinary inspection target: %+v", target.Thread.Evener)
	}
	t.Cleanup(func() {
		shutdownCtx, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		if _, err := clientRequest[appwire.EmptyResponse](shutdownCtx, client, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: targetRef}); err != nil {
			t.Errorf("inspection target shutdown: %v", err)
		}
	})

	const opening = "EVENER-VOICE-TEXT-OPENING-6C"
	const openingReply = "EVENER-VOICE-TEXT-READY-6C"
	const textRequest = "EVENER-VOICE-TEXT-INSPECT-6C"
	const textReply = "EVENER-VOICE-TEXT-RESULT-6C"
	managerRef := startManagerThread(ctx, t, client, stack, opening)
	initial, err := clientRequest[appwire.ThreadReadResponse](ctx, client, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: managerRef})
	if err != nil {
		t.Fatal(err)
	}
	managerInstance := initial.Thread.Evener.InstanceID
	if managerInstance == "" || !initial.Thread.Evener.Manager || managerRef == targetRef {
		t.Fatalf("invalid manager identity: %+v", initial.Thread.Evener)
	}
	first, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatalf("manager opening model request: %v", err)
	}
	if !first.Contains(opening) {
		t.Fatal("manager did not consume its text opening")
	}
	first.RespondToolCall("communicate", communicateArgs(openingReply))
	awaitThread(ctx, t, client, managerRef, "manager opening to finish", func(thread appwire.Thread) bool {
		return thread.Evener.ActiveTurnID == ""
	})

	// Voice discovery must not disturb ordinary text admission. Use the same
	// manager's immutable instance, then drive its real read-only manager tool.
	receipt, err := clientRequest[appwire.TurnStartResponse](ctx, client, appwire.MethodTurnStart, appwire.TurnStartParams{
		Ref: managerRef, ClientMutationID: newMutationID(t), ExpectedInstanceID: managerInstance,
		Input: []appwire.InputItem{{Type: "text", Text: textRequest}},
	})
	if err != nil {
		t.Fatalf("text fallback: %v", err)
	}
	if receipt.Receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("text fallback disposition=%q", receipt.Receipt.Disposition)
	}
	inspection, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatalf("text fallback model request: %v", err)
	}
	if !inspection.Contains(textRequest) || !inspection.Contains(opening) {
		t.Fatal("text fallback lost the new input or existing manager context")
	}
	inspection.RespondToolCall("job_status", map[string]any{"hub": map[string]any{"ref": targetRef}})
	result, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatalf("manager job_status result: %v", err)
	}
	status := voiceJourneyInspection(t, result, inspection.ToolCallID())
	if status.Read == nil {
		t.Fatalf("manager inspection returned no structured session read: %+v", status)
	}
	if status.Read.Thread.Evener.Ref != targetRef || status.Read.Thread.Evener.InstanceID != targetInstance {
		t.Fatalf("manager inspected a different session: %+v", status.Read.Thread.Evener)
	}
	result.RespondToolCall("communicate", communicateArgs(textReply))
	awaitThread(ctx, t, client, managerRef, "text fallback to finish", func(thread appwire.Thread) bool {
		return thread.Evener.ActiveTurnID == ""
	})
	settled, err := clientRequest[appwire.ThreadReadResponse](ctx, client, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: managerRef})
	if err != nil {
		t.Fatal(err)
	}
	if settled.Thread.Evener.Ref != managerRef || settled.Thread.Evener.InstanceID != managerInstance || !settled.Thread.Evener.Manager {
		t.Fatalf("text fallback replaced the manager: %+v", settled.Thread.Evener)
	}
	turns, err := readTranscriptTurns(ctx, client, managerRef, "")
	if err != nil {
		t.Fatal(err)
	}
	var inputCount, replyCount int
	for _, turn := range turns {
		for _, item := range turn.Items {
			if item.Type == "userMessage" && item.Text == textRequest {
				inputCount++
			}
			if item.Type == "agentMessage" && item.Text == textReply && item.ID != "" {
				replyCount++
			}
		}
	}
	if inputCount != 1 || replyCount != 1 {
		t.Fatalf("text fallback history: inputs=%d committed replies=%d, want one each", inputCount, replyCount)
	}
	t.Log("text fallback: same manager instance, real job_status target, one admitted input and one committed communicate reply")

	const deviceID = "journey-device-6c"
	armStatus, raw := voiceTestPost(ctx, t, stack, "/api/voice/arm", stack.token, map[string]any{
		"ref": managerRef, "expectedInstanceId": managerInstance, "deviceId": deviceID,
		"provider": "voice-fixture", "model": "gpt-realtime-2.1-mini", "voice": "marin",
	})
	if armStatus != http.StatusOK {
		t.Fatalf("voice Arm status=%d, want 200", armStatus)
	}
	var armed voiceArmResponse
	if err := json.Unmarshal(raw, &armed); err != nil {
		t.Fatal(err)
	}
	if armed.BindingID == "" || armed.Epoch == "" || armed.Grant == "" || armed.SocketPath != "/api/voice/socket" || armed.SampleRate != 24000 {
		t.Fatal("Arm omitted binding, epoch, scoped grant, socket route or mono24k format")
	}
	journal, err := hubcore.NewVoiceJournal(filepath.Join(stack.home, "state", "evener"))
	if err != nil {
		t.Fatal(err)
	}
	binding, err := journal.Binding(ctx, armed.BindingID)
	closeErr := journal.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("reopen durable Arm: read=%v close=%v", err, closeErr)
	}
	grantDigest := sha256.Sum256([]byte(armed.Grant))
	if binding.Ref != managerRef || binding.InstanceID != managerInstance || binding.DeviceID != deviceID || binding.Epoch != armed.Epoch || binding.GrantHash != hex.EncodeToString(grantDigest[:]) || !binding.Active {
		t.Fatal("durable Arm disagrees with selected manager instance or scoped device grant")
	}
	t.Log("voice Arm: same manager instance, active device binding and hashed scoped grant survived journal reopen")
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+armed.Grant)
	headers.Set("X-Evener-Voice-Binding", armed.BindingID)
	headers.Set("X-Evener-Voice-Epoch", armed.Epoch)
	headers.Set("X-Evener-Voice-Device", deviceID)
	device := voiceJourneyConnect(ctx, t, "ws://"+stack.addr+armed.SocketPath, headers)
	t.Cleanup(func() { _ = device.CloseNow() })
	devicePeer := realtimePeer{conn: device, ctx: ctx, t: t}
	ready := voiceJourneyEvent(t, devicePeer, "ready")
	voiceJourneyAcknowledgeReady(t, devicePeer, ready, 0)
	voiceJourneyEvent(t, devicePeer, "stop")
	select {
	case <-peers:
		t.Fatal("quiet ready socket opened a provider before Talk or eligible output")
	default:
	}
	journal, err = hubcore.NewVoiceJournal(filepath.Join(stack.home, "state", "evener"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })

	// Talk creates a durable app turn before any microphone bytes leave the device.
	devicePeer.send(`{"type":"input_start","sequence":1}`)
	peer, started := voiceJourneyTalk(ctx, t, device, peers)
	turnID, _ := started["turnId"].(string)
	turn, err := journal.Turn(ctx, turnID)
	if err != nil || turn.ID == "" || turn.BindingID != armed.BindingID || turn.Epoch != armed.Epoch || turn.Sequence != 1 || turn.State != hubcore.VoiceTurnOpen || turn.ProviderInputItemID != "" {
		t.Fatalf("durable turn missing before microphone input: %v", err)
	}
	const pcm = "AAABAP//AH8AgA=="
	devicePeer.send(`{"type":"audio","sequence":1,"frame":0,"pcm":"` + pcm + `"}`)
	devicePeer.send(`{"type":"input_end","sequence":1}`)
	appended := peer.read()
	if appended["type"] != "input_audio_buffer.append" || appended["audio"] != pcm {
		t.Fatal("microphone PCM was lost or altered at the provider boundary")
	}
	if peer.read()["type"] != "input_audio_buffer.commit" {
		t.Fatal("input_end did not commit the external provider buffer")
	}
	peer.send(`{"type":"input_audio_buffer.committed","previous_item_id":null,"item_id":"journey-input-1"}`)
	interpret := peer.read()
	r, _ := interpret["response"].(map[string]any)
	metadata, _ := r["metadata"].(map[string]any)
	input, _ := r["input"].([]any)
	tools, _ := r["tools"].([]any)
	if interpret["type"] != "response.create" || r["conversation"] != "none" || metadata["turn_id"] != turnID || metadata["input_item_id"] != "journey-input-1" || len(input) != 1 || len(tools) != 1 {
		t.Fatal("interpretation was not scoped to the durable input identity")
	}
	reference, _ := input[0].(map[string]any)
	tool, _ := tools[0].(map[string]any)
	if reference["type"] != "item_reference" || reference["id"] != "journey-input-1" || tool["name"] != "submit_utterance" {
		t.Fatal("interpretation received extra input or a tool other than submit_utterance")
	}
	turn, err = journal.Turn(ctx, turnID)
	if err != nil || turn.State != hubcore.VoiceTurnCommitted || turn.ProviderInputItemID != "journey-input-1" {
		t.Fatalf("provider input mapping was not durable before interpretation: %v", err)
	}
	const utterance = "EVENER-VOICE-SPOKEN-INSPECT-6C"
	const canonical = "EVENER-VOICE-CANONICAL-RESULT-6C"
	const generated = "Generated reading differs from the canonical result."
	metaJSON, _ := json.Marshal(metadata)
	peer.send(`{"type":"response.created","response":{"id":"journey-interpret-1","status":"in_progress","metadata":` + string(metaJSON) + `}}`)
	peer.send(`{"type":"response.function_call_arguments.done","response_id":"journey-interpret-1","item_id":"journey-call-item","call_id":"journey-call","name":"submit_utterance","arguments":"{\"text\":\"` + utterance + `\"}"}`)
	// Fresh live read is the observation barrier, not a sleep or cached UI state.
	beforeDone, err := clientRequest[appwire.ThreadReadResponse](ctx, client, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: managerRef, IncludeTurns: true})
	if err != nil || !beforeDone.Thread.Evener.MutationStateAuthoritative || beforeDone.Thread.Evener.InstanceID != managerInstance || beforeDone.Thread.Evener.ActiveTurnID != "" {
		t.Fatalf("arguments.done changed live manager admission: %v", err)
	}
	if _, err := journal.Intent(ctx, turnID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("arguments.done created a durable intent: %v", err)
	}
	beforeTurns, err := readTranscriptTurns(ctx, client, managerRef, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, historyTurn := range beforeTurns {
		for _, item := range historyTurn.Items {
			if item.Type == "userMessage" && item.Text == utterance {
				t.Fatal("arguments.done admitted speech before completed response.done")
			}
		}
	}
	peer.send(`{"type":"response.done","response":{"id":"journey-interpret-1","status":"completed","metadata":` + string(metaJSON) + `,"output":[{"id":"journey-call-item","type":"function_call","status":"completed","name":"submit_utterance","call_id":"journey-call","arguments":"{\"text\":\"` + utterance + `\"}"}],"usage":{"input_tokens":8,"output_tokens":3,"total_tokens":11}}}`)
	spokenRequest, err := provider.Next(ctx.Done())
	if err != nil || !spokenRequest.Contains(utterance) || !spokenRequest.Contains(opening) {
		t.Fatalf("voice did not reach the existing manager context: %v", err)
	}
	spokenRequest.RespondToolCall("job_status", map[string]any{"hub": map[string]any{"ref": targetRef}})
	spokenResult, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatal(err)
	}
	status = voiceJourneyInspection(t, spokenResult, spokenRequest.ToolCallID())
	if status.Read == nil || status.Read.Thread.Evener.Ref != targetRef || status.Read.Thread.Evener.InstanceID != targetInstance || status.Actor.ManagerRef != managerRef {
		t.Fatalf("voice-triggered job_status did not inspect the real target instance: status=%+v", status)
	}
	accepted := voiceJourneyEvent(t, devicePeer, "intent")
	if accepted["turnId"] != turnID || accepted["text"] != utterance || accepted["custody"] != hubcore.VoiceIntentAccepted {
		t.Fatal("device did not receive the durable voice mutation receipt")
	}
	intent, err := journal.Intent(ctx, turnID)
	if err != nil || intent.State != hubcore.VoiceIntentAccepted || intent.Text != utterance || intent.Ref != managerRef || intent.ExpectedInstanceID != managerInstance || intent.Method != appwire.MethodTurnStart || intent.Receipt == nil || intent.Receipt.ClientMutationID != intent.MutationID || intent.Receipt.InstanceID != managerInstance {
		t.Fatalf("voice intent lost immutable text, instance or accepted mutation custody: %v", err)
	}
	t.Log("voice intake: accepted immutable intent and real manager inspection")
	spokenResult.RespondToolCall("communicate", communicateArgs(canonical))
	awaitThread(ctx, t, client, managerRef, "voice manager turn to finish", func(thread appwire.Thread) bool { return thread.Evener.ActiveTurnID == "" })
	if !withPlayback {
		return
	}
	committed, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: managerRef, IncludeTurns: true})
	if err != nil {
		t.Fatal(err)
	}
	var source *appwire.ThreadItem
	for _, historyTurn := range committed.Thread.Turns {
		for _, item := range historyTurn.Items {
			if item.Text == canonical && item.ConversationProvenance != nil {
				source = &item
			}
		}
	}
	if source == nil {
		t.Fatal("manager completed without committing a provenance-bearing canonical reply")
	}
	speech := peer.read()
	speechResponse, _ := speech["response"].(map[string]any)
	speechInput, _ := speechResponse["input"].([]any)
	speechTools, toolsPresent := speechResponse["tools"].([]any)
	speechMeta, _ := speechResponse["metadata"].(map[string]any)
	deliveryID, _ := speechMeta["delivery_id"].(string)
	generationString, _ := speechMeta["generation"].(string)
	generation, err := strconv.ParseUint(generationString, 10, 64)
	if err != nil || deliveryID == "" || speech["type"] != "response.create" || !toolsPresent || len(speechTools) != 0 || speechResponse["tool_choice"] != "none" || len(speechInput) != 1 {
		t.Fatal("speaker request was not a generation-tagged, tool-free delivery")
	}
	message, _ := speechInput[0].(map[string]any)
	content, _ := message["content"].([]any)
	if len(content) != 1 {
		t.Fatal("speaker received extra conversation context")
	}
	text, _ := content[0].(map[string]any)
	if message["type"] != "message" || message["role"] != "user" || text["type"] != "input_text" || text["text"] != canonical {
		t.Fatal("speaker input was not solely the committed canonical communicate text")
	}
	delivery, err := journal.SpeechDelivery(ctx, deliveryID)
	if err != nil || delivery.CanonicalText != canonical || delivery.InstanceID != managerInstance || delivery.OriginatingRequestID == "" || delivery.FrontierID == "" || delivery.PresentationGeneration != generation || delivery.GeneratedTranscript != "" {
		t.Fatalf("speech source was not durably selected before generation: %v", err)
	}
	voiceTurns, err := readTranscriptTurns(ctx, client, managerRef, "")
	if err != nil {
		t.Fatal(err)
	}
	inputCount, replyCount = 0, 0
	for _, historyTurn := range voiceTurns {
		for _, item := range historyTurn.Items {
			if item.Type == "userMessage" && item.Text == utterance {
				inputCount++
			}
			if item.Type == "agentMessage" && item.Text == canonical && item.ID == delivery.CanonicalSourceItemID {
				replyCount++
			}
		}
	}
	if inputCount != 1 || replyCount != 1 {
		t.Fatalf("voice history: inputs=%d canonical source replies=%d, want one each", inputCount, replyCount)
	}
	speechMetadata, _ := json.Marshal(speechMeta)
	peer.send(`{"type":"response.created","response":{"id":"journey-speech-1","status":"in_progress","metadata":` + string(speechMetadata) + `}}`)
	peer.send(`{"type":"response.output_item.added","response_id":"journey-speech-1","output_index":0,"item":{"id":"journey-audio-1","type":"message","role":"assistant","status":"in_progress","content":[]}}`)
	peer.send(`{"type":"response.output_audio.delta","response_id":"journey-speech-1","item_id":"journey-audio-1","content_index":0,"delta":"` + pcm + `"}`)
	peer.send(`{"type":"response.output_audio_transcript.done","response_id":"journey-speech-1","item_id":"journey-audio-1","content_index":0,"transcript":"` + generated + `"}`)
	peer.send(`{"type":"response.output_audio.done","response_id":"journey-speech-1","item_id":"journey-audio-1","content_index":0}`)
	peer.send(`{"type":"response.done","response":{"id":"journey-speech-1","status":"completed","metadata":` + string(speechMetadata) + `,"output":[{"id":"journey-audio-1","type":"message","status":"completed"}],"usage":{"input_tokens":2,"output_tokens":5,"total_tokens":7}}}`)
	for _, kind := range []string{"speech_start", "audio", "speech_text", "speech_end", "delivery_end"} {
		event := voiceJourneyEvent(t, devicePeer, kind)
		if event["deliveryId"] != deliveryID || event["generation"] != float64(generation) {
			t.Fatalf("%s lost the selected delivery generation", kind)
		}
		if kind != "delivery_end" && kind != "speech_start" && event["itemId"] != "journey-audio-1" {
			t.Fatalf("%s lost the provider item identity", kind)
		}
		if kind == "audio" && event["pcm"] != pcm {
			t.Fatal("speaker PCM changed on the device socket")
		}
		if kind == "speech_text" && event["text"] != generated {
			t.Fatal("generated transcript was replaced with canonical source text")
		}
	}
	delivery, err = journal.SpeechDelivery(ctx, deliveryID)
	pcmBytes, decodeErr := base64.StdEncoding.DecodeString(pcm)
	samples := int64(len(pcmBytes) / 2)
	if err != nil || decodeErr != nil || len(delivery.Items) != 1 || delivery.Items[0].GeneratedSamples != samples || delivery.Items[0].QueuedSamples != samples || delivery.Items[0].PlayedSamples != 0 || delivery.GeneratedTranscript != generated || delivery.CanonicalText != canonical {
		t.Fatalf("generation or queueing was conflated with playback or canonical text: %v", err)
	}
	played, _ := json.Marshal(map[string]any{"type": "played", "deliveryId": deliveryID, "generation": generation, "itemId": "journey-audio-1", "samples": samples, "complete": true})
	devicePeer.send(string(played))
	// Poll only positive durable evidence, never absence of a prohibited action.
	playbackCtx, playbackCancel := context.WithTimeout(ctx, 5*time.Second)
	defer playbackCancel()
	for {
		reopened, openErr := hubcore.NewVoiceJournal(filepath.Join(stack.home, "state", "evener"))
		if openErr != nil {
			t.Fatal(openErr)
		}
		delivery, err = reopened.SpeechDelivery(playbackCtx, deliveryID)
		closeErr = reopened.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("reopen playback: read=%v close=%v", err, closeErr)
		}
		if len(delivery.Items) == 1 && delivery.Items[0].PlayedSamples == samples {
			break
		}
		select {
		case <-playbackCtx.Done():
			t.Fatal("device playback receipt was not persisted")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if delivery.CanonicalText != canonical || delivery.GeneratedTranscript != generated || delivery.Items[0].ResponseID != "journey-speech-1" {
		t.Fatal("reopened playback journal lost canonical/generated/provider identities")
	}
	t.Log("voice journey: real PCM, completed-only relay, same-manager job_status, committed canonical speech and persisted device playback")

	// Keep a real model request running while disconnecting and Disarming. These
	// operations retire voice presentation, never Stop the manager's work.
	awaitThread(ctx, t, client, managerRef, "voice manager turn to finish", func(thread appwire.Thread) bool { return thread.Evener.ActiveTurnID == "" })
	_, err = clientRequest[appwire.TurnStartResponse](ctx, client, appwire.MethodTurnStart, appwire.TurnStartParams{
		Ref: managerRef, ClientMutationID: newMutationID(t), ExpectedInstanceID: managerInstance,
		Input: []appwire.InputItem{{Type: "text", Text: "EVENER-VOICE-DISARM-KEEPS-WORK-6C"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	held, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatal(err)
	}
	running, err := clientRequest[appwire.ThreadReadResponse](ctx, client, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: managerRef})
	if err != nil || running.Thread.Evener.ActiveTurnID == "" {
		t.Fatalf("manager work was not active before voice teardown: %v", err)
	}
	_ = device.CloseNow()
	disarmStatus, _ := voiceTestPost(ctx, t, stack, "/api/voice/disarm", stack.token, map[string]any{"bindingId": armed.BindingID, "epoch": armed.Epoch})
	if disarmStatus != http.StatusOK {
		t.Fatalf("Disarm status=%d, want 200", disarmStatus)
	}
	afterDisarm, err := clientRequest[appwire.ThreadReadResponse](ctx, client, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: managerRef})
	if err != nil || !afterDisarm.Thread.Evener.MutationStateAuthoritative || afterDisarm.Thread.Evener.InstanceID != managerInstance || afterDisarm.Thread.Evener.ActiveTurnID != running.Thread.Evener.ActiveTurnID {
		t.Fatalf("voice EOF/Disarm stopped or replaced the working manager: %v", err)
	}
	binding, err = journal.Binding(ctx, armed.BindingID)
	if err != nil || binding.Active {
		t.Fatalf("Disarm failed to retire the durable voice binding: %v", err)
	}
	held.RespondToolCall("communicate", communicateArgs("EVENER-VOICE-WORK-SURVIVED-6C"))
	awaitThread(ctx, t, client, managerRef, "manager work after Disarm", func(thread appwire.Thread) bool { return thread.Evener.ActiveTurnID == "" })
	// Discovery must eventually advertise the complete path, not just Arm.
	if !features["voice"] {
		t.Fatal("initialize features.voice is absent or false")
	}
}

func voiceJourneyInspection(t *testing.T, call *fakellm.Call, toolCallID string) appwire.SessionOperationResponse {
	t.Helper()
	messages, _ := call.Body["messages"].([]any)
	for _, raw := range messages {
		message, _ := raw.(map[string]any)
		if message["role"] != "tool" || message["tool_call_id"] != toolCallID {
			continue
		}
		body, _ := message["content"].(string)
		var result appwire.SessionOperationResponse
		// Loop protection may append a warning after the operation's JSON.
		if err := json.NewDecoder(strings.NewReader(body)).Decode(&result); err != nil {
			t.Fatalf("decode correlated manager inspection: %v", err)
		}
		return result
	}
	t.Fatal("manager request has no result for the current inspection call")
	return appwire.SessionOperationResponse{}
}

// A quiet socket is only a control connection. Provider startup belongs to Talk.
func voiceJourneyConnect(ctx context.Context, t *testing.T, endpoint string, headers http.Header) *websocket.Conn {
	t.Helper()
	device, response, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		status := 0
		if response != nil {
			status = response.StatusCode
			if response.Body != nil {
				_ = response.Body.Close()
			}
		}
		t.Fatalf("voice socket boundary: durable Arm passed, scoped-grant WebSocket upgrade returned HTTP %d instead of 101; Talk/PCM and later phases were not exercised", status)
	}
	return device
}

func voiceJourneyTalk(ctx context.Context, t *testing.T, device *websocket.Conn, peers <-chan realtimePeer) (realtimePeer, map[string]any) {
	t.Helper()
	type deviceResult struct {
		event map[string]any
		err   error
	}
	started := make(chan deviceResult, 1)
	go func() {
		for {
			_, raw, err := device.Read(ctx)
			var event map[string]any
			if err == nil {
				err = json.Unmarshal(raw, &event)
			}
			if err != nil || event["type"] == "error" || event["type"] == "input_started" {
				started <- deviceResult{event, err}
				return
			}
		}
	}()
	var peer realtimePeer
	var inputStarted map[string]any
	for peer.conn == nil || inputStarted == nil {
		select {
		case result := <-started:
			if result.err != nil || result.event["type"] != "input_started" {
				t.Fatalf("voice Talk boundary: durable Arm and quiet socket ready passed, input_start returned type=%v code=%v readError=%v instead of input_started; PCM and later phases were not exercised", result.event["type"], result.event["code"], result.err)
			}
			inputStarted = result.event
		case peer = <-peers:
			peer.ctx = ctx
			request := peer.read()
			session, _ := request["session"].(map[string]any)
			audio, _ := session["audio"].(map[string]any)
			input, _ := audio["input"].(map[string]any)
			format, _ := input["format"].(map[string]any)
			output, _ := audio["output"].(map[string]any)
			turnDetection, present := input["turn_detection"]
			if request["type"] != "session.update" || !present || turnDetection != nil || format["type"] != "audio/pcm" || format["rate"] != float64(24000) || output["voice"] != "marin" {
				t.Fatal("provider session did not use app-owned endpointing and configured mono24k voice")
			}
			peer.send(`{"type":"session.updated","session":{"type":"realtime","audio":{"input":{"format":{"type":"audio/pcm","rate":24000},"turn_detection":null},"output":{"format":{"type":"audio/pcm","rate":24000},"voice":"marin"}},"tools":[],"output_modalities":["text"]}}`)
			if peer.read()["type"] != "input_audio_buffer.clear" {
				t.Fatal("Talk did not clear the external provider input buffer")
			}
			peer.send(`{"type":"input_audio_buffer.cleared"}`)
		case <-ctx.Done():
			t.Fatal("voice Talk provider bootstrap did not finish")
		}
	}
	return peer, inputStarted
}

// Match native ready handling: every connection reports its retained playback
// floor, even a fresh Arm whose minimum is still zero.
func voiceJourneyAcknowledgeReady(t *testing.T, device realtimePeer, ready map[string]any, minimum uint64) {
	t.Helper()
	generation, ok := ready["generation"].(float64)
	if !ok || generation < 1 || generation >= voiceMaxGeneration || generation != float64(uint64(generation)) {
		t.Fatal("ready did not advertise a representable positive generation")
	}
	device.send(fmt.Sprintf(`{"type":"interrupt","generation":%d,"playedSamples":{}}`, max(uint64(generation), minimum)-1))
}

func voiceJourneyEvent(t *testing.T, device realtimePeer, want string) map[string]any {
	t.Helper()
	for {
		event := device.read()
		kind, _ := event["type"].(string)
		if kind == want {
			return event
		}
		switch kind {
		case "error", "speech_start", "audio", "speech_text", "speech_end", "delivery_end":
			t.Fatalf("device event %q (code=%v) before expected %q", kind, event["code"], want)
		}
	}
}
