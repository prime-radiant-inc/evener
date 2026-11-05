package hub

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"github.com/coder/websocket"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/e2ecap"
	"primeradiant.com/evener/test/e2e/fakellm"
)

// This is a paid, single-turn qualification, not part of the offline suite.
// Only the manager model is scripted. The observer forwards unmodified frames
// between the production adapter and OpenAI, never synthesizing provider events.
// No playback acknowledgements are sent: bytes do not establish audible speech.
func TestE2E_VoiceLiveProductionRelay(t *testing.T) {
	if os.Getenv("EVENER_VOICE_RELAY_E2E") != "1" {
		t.Skip("requires EVENER_VOICE_RELAY_E2E=1, independently of credentials")
	}
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		t.Skip("requires OPENAI_API_KEY")
	}
	if testing.Short() {
		t.Skip("requires real isolated hub and daemon processes")
	}
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	// The key stays in the observer, not child environments or hub fixture files.
	t.Setenv("OPENAI_API_KEY", "")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pcm := voiceLiveFixture(t, ctx)
	model, voice := os.Getenv("EVENER_VOICE_RELAY_MODEL"), os.Getenv("EVENER_VOICE_RELAY_VOICE")
	if model == "" {
		model = "gpt-realtime-2.1-mini"
	}
	if voice == "" {
		voice = "marin"
	}
	voiceLiveCatalog(t, ctx, key, model)
	observer, endpoint := voiceLiveObserver(t, ctx, key, model)
	provider := newFakeLLMProvider(t)
	stack := startHubStackOnProvider(t, fmt.Sprintf(`
default = "fake"
[providers.fake]
base = "openai-compatible"
base_url = %q
api_key = "fakellm-not-a-secret"
[providers.voice-live]
base = "openai-compatible"
base_url = %q
api_key = "observer-not-a-secret"
`, provider.BaseURL(), endpoint+"/v1"), "fake/"+fakellm.ModelID)
	client := stack.dialRPC(ctx, t)
	target, err := clientRequest[appwire.ThreadStartResponse](ctx, client, appwire.MethodThreadStart, appwire.ThreadStartParams{
		Harness: "evener", CWD: stack.workDir, Model: stack.model,
		LaunchOverrides: &appwire.LaunchConfigLayer{Sandbox: "off"},
	})
	if err != nil {
		t.Fatal(err)
	}
	targetRef, targetInstance := target.Thread.Evener.Ref, target.Thread.Evener.InstanceID
	if targetRef == "" || targetInstance == "" || target.Thread.Evener.Manager {
		t.Fatal("inspection target is not an ordinary live session")
	}
	t.Cleanup(func() {
		shutdown, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		if _, err := clientRequest[appwire.EmptyResponse](shutdown, client, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: targetRef}); err != nil {
			t.Errorf("target shutdown: %v", err)
		}
	})
	const opening = "Fixture session Amber is the read-only inspection target. Context marker AMBER-CONTEXT-73."
	managerRef := startManagerThread(ctx, t, client, stack, opening)
	first, err := provider.Next(ctx.Done())
	if err != nil || !first.Contains("AMBER-CONTEXT-73") {
		t.Fatalf("opening context missing: %v", err)
	}
	first.RespondToolCall("communicate", communicateArgs("Ready for the read-only fixture."))
	awaitThread(ctx, t, client, managerRef, "manager opening", func(thread appwire.Thread) bool { return thread.Evener.ActiveTurnID == "" })
	initial, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: managerRef})
	if err != nil {
		t.Fatal(err)
	}
	managerInstance := initial.Thread.Evener.InstanceID
	const deviceID = "live-relay-fixture"
	status, raw := voiceTestPost(t, ctx, stack, "/api/voice/arm", stack.token, map[string]any{
		"ref": managerRef, "expectedInstanceId": managerInstance, "deviceId": deviceID,
		"provider": "voice-live", "model": model, "voice": voice,
	})
	if status != http.StatusOK {
		t.Fatalf("Arm HTTP status=%d", status)
	}
	var armed voiceArmResponse
	if err := json.Unmarshal(raw, &armed); err != nil {
		t.Fatal(err)
	}
	if armed.BindingID == "" || armed.Epoch == "" || armed.Grant == "" || armed.SampleRate != 24000 {
		t.Fatal("Arm missing scoped identity or mono24k format")
	}
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+armed.Grant)
	headers.Set("X-Evener-Voice-Binding", armed.BindingID)
	headers.Set("X-Evener-Voice-Epoch", armed.Epoch)
	headers.Set("X-Evener-Voice-Device", deviceID)
	device := voiceJourneyConnect(t, ctx, "ws://"+stack.addr+armed.SocketPath, headers)
	defer func() { _ = device.CloseNow() }()
	peer := realtimePeer{conn: device, ctx: ctx, t: t}
	ready := voiceJourneyEvent(t, peer, "ready")
	voiceJourneyAcknowledgeReady(t, peer, ready, 0)
	voiceJourneyEvent(t, peer, "stop")
	quiet := observer.snapshot()
	if quiet.Sessions != 0 || quiet.InputBytes != 0 {
		t.Fatal("quiet ready connection contacted the provider before input_start")
	}
	t.Log("quiet ready barrier: zero upstream WebSocket sessions and zero audio bytes")
	peer.send(`{"type":"input_start","sequence":1}`)
	started := voiceJourneyEvent(t, peer, "input_started")
	turnID, _ := started["turnId"].(string)
	journal, err := hubcore.NewVoiceJournal(filepath.Join(stack.home, "state", "evener"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()
	turn, err := journal.Turn(ctx, turnID)
	if err != nil || turn.ID == "" || turn.State != hubcore.VoiceTurnOpen || turn.BindingID != armed.BindingID {
		t.Fatalf("input_started without durable open turn: %v", err)
	}
	// 100 ms mono24k PCM16 frames, paced as speech rather than a bulk upload.
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for offset, frame := 0, 0; offset < len(pcm); offset, frame = offset+4800, frame+1 {
		end := min(offset+4800, len(pcm))
		message, _ := json.Marshal(map[string]any{"type": "audio", "sequence": 1, "frame": frame, "pcm": base64.StdEncoding.EncodeToString(pcm[offset:end])})
		peer.send(string(message))
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("input stream deadline")
		}
	}
	peer.send(`{"type":"input_end","sequence":1}`)
	accepted := voiceJourneyEvent(t, peer, "intent")
	intent, err := journal.Intent(ctx, turnID)
	if err != nil || accepted["turnId"] != turnID || accepted["custody"] != hubcore.VoiceIntentAccepted || intent.State != hubcore.VoiceIntentAccepted || intent.Ref != managerRef || intent.ExpectedInstanceID != managerInstance || intent.Method != appwire.MethodTurnStart || intent.Receipt == nil || intent.Receipt.ClientMutationID != intent.MutationID || intent.Receipt.InstanceID != managerInstance || intent.Receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("missing durable accepted original-instance intent: %v", err)
	}
	t.Logf("interpreted=%q mutation=%s disposition=%s", intent.Text, intent.MutationID, intent.Receipt.Disposition)
	// Independent fixture tokens, not a snapshot of the provider's prose. These
	// are a narrow data-preservation check, not a general semantic judge.
	voiceLiveTokens(t, intent.Text, "amber", "status")
	spoken, err := provider.Next(ctx.Done())
	if err != nil || !spoken.Contains(intent.Text) || !spoken.Contains("AMBER-CONTEXT-73") {
		t.Fatalf("voice input or prior context missing from manager: %v", err)
	}
	spoken.RespondToolCall("job_status", map[string]any{"hub": map[string]any{"ref": targetRef}})
	result, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatal(err)
	}
	inspection := voiceJourneyInspection(t, result, spoken.ToolCallID())
	if inspection.Read == nil || inspection.Read.Thread.Evener.Ref != targetRef || inspection.Read.Thread.Evener.InstanceID != targetInstance || inspection.Actor.ManagerRef != managerRef || inspection.Read.Thread.Evener.ActiveTurnID != "" {
		t.Fatal("job_status did not inspect the original idle fixture session")
	}
	t.Logf("real job_status: manager=%s instance=%s target=%s target_instance=%s active_turn=%q", managerRef, managerInstance, targetRef, targetInstance, inspection.Read.Thread.Evener.ActiveTurnID)
	const canonical = "Amber is idle."
	result.RespondToolCall("communicate", communicateArgs(canonical))
	var deliveryID, generated string
	var outputSamples int64
	var nonzero int
	for {
		event := peer.read()
		switch event["type"] {
		case "error":
			t.Fatalf("production voice socket error code=%v", event["code"])
		case "speech_start":
			deliveryID, _ = event["deliveryId"].(string)
		case "audio":
			encoded, _ := event["pcm"].(string)
			chunk, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil || len(chunk) == 0 || len(chunk)%2 != 0 || event["sampleRate"] != float64(24000) || event["deliveryId"] != deliveryID {
				t.Fatal("invalid generated PCM or delivery identity")
			}
			outputSamples += int64(len(chunk) / 2)
			nonzero += voiceLiveNonzero(chunk)
		case "speech_text":
			text, _ := event["text"].(string)
			generated += text
		case "delivery_end":
			if event["deliveryId"] != deliveryID {
				t.Fatal("completion belongs to a different delivery")
			}
			goto completed
		}
	}
completed:
	delivery, err := journal.SpeechDelivery(ctx, deliveryID)
	if err != nil || !delivery.OutputComplete || delivery.Interrupted || delivery.InstanceID != managerInstance || delivery.CanonicalText != canonical || delivery.GeneratedTranscript != generated || delivery.CanonicalSourceItemID == "" || delivery.OriginatingRequestID == "" || delivery.FrontierID == "" || outputSamples == 0 || nonzero == 0 {
		t.Fatalf("missing committed source, transcript, nonempty audio or provider completion: %v", err)
	}
	var durableSamples int64
	for _, item := range delivery.Items {
		if item.PlayedSamples != 0 || item.PlaybackComplete || item.GeneratedSamples != item.QueuedSamples {
			t.Fatal("generated audio was confused with playback")
		}
		durableSamples += item.GeneratedSamples
	}
	if durableSamples != outputSamples {
		t.Fatal("device audio and durable generated samples disagree")
	}
	t.Logf("canonical=%q generated=%q output_samples=%d nonzero_samples=%d provider_complete=%t played_samples=0", canonical, generated, outputSamples, nonzero, delivery.OutputComplete)
	voiceLiveTokens(t, generated, "amber", "idle")
	awaitThread(ctx, t, client, managerRef, "voice turn completion", func(thread appwire.Thread) bool { return thread.Evener.ActiveTurnID == "" })
	settled, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: managerRef})
	if err != nil {
		t.Fatal(err)
	}
	if settled.Thread.Evener.InstanceID != managerInstance || !settled.Thread.Evener.Manager {
		t.Fatal("voice replaced the manager")
	}
	turns, err := readTranscriptTurns(ctx, client, managerRef, "")
	if err != nil {
		t.Fatal(err)
	}
	inputs, replies := 0, 0
	for _, history := range turns {
		for _, item := range history.Items {
			if item.Type == "userMessage" && item.Text == intent.Text {
				inputs++
			}
			if item.Type == "agentMessage" && item.ID == delivery.CanonicalSourceItemID && item.Text == canonical && item.ConversationProvenance != nil {
				replies++
			}
		}
	}
	if inputs != 1 || replies != 1 {
		t.Fatalf("durable history: inputs=%d canonical replies=%d", inputs, replies)
	}
	wire := observer.snapshot()
	if wire.Sessions != 1 || wire.InputBytes != len(pcm) || wire.Commits != 1 || wire.Model != model || len(wire.Requests) != 2 || len(wire.Responses) != 2 {
		t.Fatalf("unexpected upstream accounting: sessions=%d input_bytes=%d commits=%d model=%q requests=%d responses=%d", wire.Sessions, wire.InputBytes, wire.Commits, wire.Model, len(wire.Requests), len(wire.Responses))
	}
	interpret, speech := wire.Requests[0], wire.Requests[1]
	tools, ok := speech["tools"].([]any)
	input, _ := speech["input"].([]any)
	if !ok || len(tools) != 0 || speech["tool_choice"] != "none" || len(input) != 1 {
		t.Fatal("production presentation was not tool-free single-source input")
	}
	message, _ := input[0].(map[string]any)
	content, _ := message["content"].([]any)
	if len(content) != 1 {
		t.Fatal("presentation has extra content")
	}
	text, _ := content[0].(map[string]any)
	if text["text"] != canonical || text["type"] != "input_text" || message["role"] != "user" {
		t.Fatal("presentation input differs from committed canonical source")
	}
	interpretTools, _ := interpret["tools"].([]any)
	if len(interpretTools) != 1 || interpret["conversation"] != "none" {
		t.Fatal("interpretation exposed extra tools or conversation")
	}
	tool, _ := interpretTools[0].(map[string]any)
	if tool["name"] != "submit_utterance" {
		t.Fatal("interpretation exposed a tool other than submit_utterance")
	}
	for _, response := range wire.Responses {
		if response.Status != "completed" {
			t.Fatalf("upstream response status=%s", response.Status)
		}
		t.Logf("upstream response=%s status=%s usage=%+v", response.ID, response.Status, response.Usage)
	}
	t.Logf("one admitted voice input, one committed reply, unchanged manager, model=%s voice=%s; fixture-token checks only, semantic review still required", wire.Model, voice)
}

func voiceLiveTokens(t *testing.T, text string, required ...string) {
	t.Helper()
	tokens := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) })
	set := make(map[string]bool)
	for _, token := range tokens {
		set[token] = true
	}
	for _, token := range required {
		if !set[token] {
			t.Fatalf("independent fixture token %q missing from %q", token, text)
		}
	}
}

func voiceLiveNonzero(pcm []byte) int {
	n := 0
	for i := 0; i < len(pcm); i += 2 {
		if binary.LittleEndian.Uint16(pcm[i:]) != 0 {
			n++
		}
	}
	return n
}

func voiceLiveFixture(t *testing.T, ctx context.Context) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "amber.wav")
	const filter = "flite=text='What is the status of Amber?':voice=slt"
	cmd := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", filter, "-ac", "1", "-ar", "24000", "-c:a", "pcm_s16le", path)
	cmd.WaitDelay = time.Second
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("local FFmpeg with Flite required: %v: %s", err, out)
	}
	wave, err := os.ReadFile(path)
	if err != nil || len(wave) < 12 || string(wave[:4]) != "RIFF" || string(wave[8:12]) != "WAVE" || int(binary.LittleEndian.Uint32(wave[4:8]))+8 != len(wave) {
		t.Fatalf("invalid fixture WAVE: %v", err)
	}
	var pcm []byte
	formatOK := false
	for offset := 12; offset+8 <= len(wave); {
		size := int(binary.LittleEndian.Uint32(wave[offset+4 : offset+8]))
		start := offset + 8
		if size > len(wave)-start {
			t.Fatal("truncated WAVE chunk")
		}
		chunk := wave[start : start+size]
		switch string(wave[offset : offset+4]) {
		case "fmt ":
			formatOK = len(chunk) >= 16 && binary.LittleEndian.Uint16(chunk) == 1 && binary.LittleEndian.Uint16(chunk[2:]) == 1 && binary.LittleEndian.Uint32(chunk[4:]) == 24000 && binary.LittleEndian.Uint32(chunk[8:]) == 48000 && binary.LittleEndian.Uint16(chunk[12:]) == 2 && binary.LittleEndian.Uint16(chunk[14:]) == 16
		case "data":
			pcm = chunk
		}
		offset = start + size + size%2
	}
	if !formatOK || len(pcm) < 4800 || len(pcm) > 10*48000 || len(pcm)%2 != 0 || voiceLiveNonzero(pcm) == 0 {
		t.Fatal("fixture must be non-silent, bounded mono24k signed16le speech")
	}
	t.Logf("Flite slt fixture: words=%q WAVE_SHA256=%x PCM_SHA256=%x samples=%d duration_seconds=%.4f nonzero_samples=%d", "What is the status of Amber?", sha256.Sum256(wave), sha256.Sum256(pcm), len(pcm)/2, float64(len(pcm))/48000, voiceLiveNonzero(pcm))
	return pcm
}

func voiceLiveCatalog(t *testing.T, ctx context.Context, key, model string) {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.openai.com/v1/models/"+url.PathEscape(model), nil)
	if err != nil {
		t.Fatal("invalid model catalog request")
	}
	request.Header.Set("Authorization", "Bearer "+key)
	client := &http.Client{Timeout: 15 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("model catalog transport failed")
	}
	defer func() { _ = response.Body.Close() }()
	var entry struct {
		ID string `json:"id"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&entry) != nil || entry.ID != model {
		t.Fatalf("model catalog did not confirm selected model, HTTP status=%d", response.StatusCode)
	}
	t.Logf("catalog confirmed model=%s, inference access remains to be tested", entry.ID)
}

type voiceLiveWire struct {
	Sessions, InputBytes, Commits int
	Model                         string
	Requests                      []map[string]any
	Responses                     []voiceWireResponse
}

type voiceLiveWireObserver struct {
	mu   sync.Mutex
	wire voiceLiveWire
}

func (o *voiceLiveWireObserver) snapshot() voiceLiveWire {
	o.mu.Lock()
	defer o.mu.Unlock()
	copy := o.wire
	copy.Requests = append([]map[string]any(nil), copy.Requests...)
	copy.Responses = append([]voiceWireResponse(nil), copy.Responses...)
	return copy
}

// One upstream session maximum, no retry or fallback. Authorization is added
// only on the TLS upstream connection. Headers, PCM and provider error prose
// are never logged. Both directions otherwise forward byte-identical messages.
func voiceLiveObserver(t *testing.T, ctx context.Context, key, model string) (*voiceLiveWireObserver, string) {
	t.Helper()
	o := &voiceLiveWireObserver{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/realtime" || r.URL.Query().Get("model") != model {
			http.Error(w, "unsupported route", http.StatusNotFound)
			return
		}
		o.mu.Lock()
		o.wire.Sessions++
		allowed := o.wire.Sessions == 1
		o.mu.Unlock()
		if !allowed {
			t.Error("upstream session budget exceeded, no request forwarded")
			http.Error(w, "budget", http.StatusTooManyRequests)
			return
		}
		headers := http.Header{"Authorization": {"Bearer " + key}}
		upstream, response, err := websocket.Dial(ctx, "wss://api.openai.com/v1/realtime?model="+url.QueryEscape(model), &websocket.DialOptions{HTTPHeader: headers})
		if err != nil {
			status := 0
			if response != nil {
				status = response.StatusCode
				if response.Body != nil {
					_ = response.Body.Close()
				}
			}
			t.Logf("upstream handshake failure HTTP status=%d", status)
			http.Error(w, "upstream handshake", http.StatusBadGateway)
			return
		}
		defer func() { _ = upstream.CloseNow() }()
		downstream, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = downstream.CloseNow() }()
		upstream.SetReadLimit(1 << 20)
		downstream.SetReadLimit(1 << 20)
		pumpCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer cancel()
			for {
				kind, raw, err := upstream.Read(pumpCtx)
				if err != nil {
					return
				}
				var event struct {
					Type    string `json:"type"`
					Session struct {
						Model string `json:"model"`
					} `json:"session"`
					Response voiceWireResponse                  `json:"response"`
					Error    struct{ Type, Code, Param string } `json:"error"`
				}
				if json.Unmarshal(raw, &event) != nil {
					return
				}
				o.mu.Lock()
				if event.Type == "session.created" {
					o.wire.Model = event.Session.Model
				}
				if event.Type == "response.done" {
					o.wire.Responses = append(o.wire.Responses, event.Response)
				}
				o.mu.Unlock()
				if event.Type == "error" {
					t.Logf("upstream error type=%q code=%q param=%q (prose suppressed)", event.Error.Type, event.Error.Code, event.Error.Param)
				}
				if downstream.Write(pumpCtx, kind, raw) != nil {
					return
				}
			}
		}()
		defer func() { cancel(); <-done }()
		for {
			kind, raw, err := downstream.Read(pumpCtx)
			if err != nil {
				return
			}
			var event map[string]any
			if json.Unmarshal(raw, &event) != nil {
				return
			}
			o.mu.Lock()
			switch event["type"] {
			case "input_audio_buffer.append":
				encoded, _ := event["audio"].(string)
				pcm, err := base64.StdEncoding.DecodeString(encoded)
				if err == nil {
					o.wire.InputBytes += len(pcm)
				}
			case "input_audio_buffer.commit":
				o.wire.Commits++
			case "response.create":
				response, _ := event["response"].(map[string]any)
				o.wire.Requests = append(o.wire.Requests, response)
			}
			withinBudget := o.wire.Commits <= 1 && len(o.wire.Requests) <= 2
			o.mu.Unlock()
			if !withinBudget {
				t.Error("upstream turn budget exceeded, frame not forwarded")
				return
			}
			if upstream.Write(pumpCtx, kind, raw) != nil {
				return
			}
		}
	}))
	t.Cleanup(func() {
		server.Close()
		wire := o.snapshot()
		t.Logf("upstream accounting: sessions=%d commits=%d input_bytes=%d response_create=%d response_done=%d model=%q", wire.Sessions, wire.Commits, wire.InputBytes, len(wire.Requests), len(wire.Responses), wire.Model)
	})
	return o, server.URL
}
