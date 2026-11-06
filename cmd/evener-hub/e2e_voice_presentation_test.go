package hub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/rendezvous"
)

func TestE2E_VoicePresentationCompletionFacts(t *testing.T) {
	for _, complete := range []bool{true, false} {
		t.Run(fmt.Sprintf("rendered_complete_%t", complete), func(t *testing.T) {
			f := newVoiceCorrectionStack(t)
			f.admit(t, "PRESENTATION-INPUT-101", false)
			f.next(t).RespondToolCall("communicate", communicateArgs("PRESENTATION-OUTPUT-103"))
			start := voiceJourneyEvent(t, f.device, "speech_start")
			peer := f.openSpeaker(t)
			delivery := f.speechRequest(t, peer, start, "PRESENTATION-OUTPUT-103")
			voiceCorrectionAudio(peer, delivery, "presentation-response", "presentation-item", "AQACAA==")
			voiceCorrectionDone(peer, delivery, "presentation-response", "presentation-item")
			f.expectFinishedAudio(t, delivery, "AQACAA==")
			generated := f.delivery(t, delivery.ID)
			if !generated.OutputComplete || len(generated.Items) != 1 || generated.Items[0].PlaybackComplete || generated.Items[0].PlayedSamples != 0 {
				t.Fatalf("provider completion did not remain distinct from rendering: %+v", generated)
			}
			played := 1
			if complete {
				played = 2
			}
			f.device.send(fmt.Sprintf(`{"type":"played","deliveryId":%q,"generation":%d,"itemId":"presentation-item","samples":%d,"complete":%t}`, delivery.ID, delivery.PresentationGeneration, played, complete))
			f.waitDelivery(t, delivery.ID, func(d hubcore.SpeechDelivery) bool {
				return d.Items[0].PlayedSamples == int64(played)
			})
			acknowledged := f.delivery(t, delivery.ID)
			if acknowledged.Items[0].PlaybackComplete != complete {
				t.Fatal("device completion acknowledgement was not journaled")
			}
			f.device.send(fmt.Sprintf(`{"type":"interrupt","generation":%d,"playedSamples":{"presentation-item":%d}}`, delivery.PresentationGeneration, played))
			voiceJourneyEvent(t, f.device, "stop")
			retired := f.delivery(t, delivery.ID)
			if retired.Interrupted == complete || !retired.OutputComplete || retired.Items[0].PlaybackComplete != complete {
				t.Fatalf("retirement changed the observed rendering facts: %+v", retired)
			}
		})
	}
}

func TestE2E_VoicePresentationReachesManager(t *testing.T) {
	f := newVoiceCorrectionStack(t)
	const canonical = "CANONICAL-ALPHA-157 CANONICAL-BETA-163"
	const presented = "PRESENTED-BETA-167 PRESENTED-ALPHA-173"
	const reference = "REFERENCE-179"
	f.admit(t, "PRESENTATION-INPUT-181", false)
	f.next(t).RespondToolCall("communicate", communicateArgs(canonical))
	start := voiceJourneyEvent(t, f.device, "speech_start")
	peer := f.openSpeaker(t)
	delivery := f.speechRequest(t, peer, start, canonical)
	voiceCorrectionAudio(peer, delivery, "reference-response", "reference-item", "AQACAA==")
	peer.send(fmt.Sprintf(`{"type":"response.output_audio_transcript.done","response_id":"reference-response","item_id":"reference-item","content_index":0,"transcript":%q}`, presented))
	voiceCorrectionDone(peer, delivery, "reference-response", "reference-item")
	for _, kind := range []string{"audio", "speech_text", "speech_end", "delivery_end"} {
		event := voiceJourneyEvent(t, f.device, kind)
		if event["deliveryId"] != delivery.ID || event["generation"] != float64(delivery.PresentationGeneration) || (kind == "audio" && event["pcm"] != "AQACAA==") || (kind == "speech_text" && event["text"] != presented) {
			t.Fatalf("presentation event lost its source or content: %v", event)
		}
	}
	f.device.send(fmt.Sprintf(`{"type":"played","deliveryId":%q,"generation":%d,"itemId":"reference-item","samples":1}`, delivery.ID, delivery.PresentationGeneration))
	f.waitDelivery(t, delivery.ID, func(d hubcore.SpeechDelivery) bool {
		return d.GeneratedTranscript == presented && d.Items[0].PlayedSamples == 1
	})
	f.device.send(fmt.Sprintf(`{"type":"interrupt","generation":%d,"playedSamples":{"reference-item":1}}`, delivery.PresentationGeneration))
	f.device.send(`{"type":"input_start","sequence":1}`)
	voiceJourneyEvent(t, f.device, "stop")
	truncate := peer.read()
	if truncate["type"] != "conversation.item.truncate" || truncate["item_id"] != "reference-item" || truncate["audio_end_ms"] != float64(0) {
		t.Fatalf("partial presentation was not truncated at its watermark: %v", truncate)
	}
	peer.send(`{"type":"conversation.item.truncated","item_id":"reference-item","content_index":0,"audio_end_ms":0}`)
	if peer.read()["type"] != "input_audio_buffer.clear" {
		t.Fatal("reference did not begin ordinary voice input")
	}
	peer.send(`{"type":"input_audio_buffer.cleared"}`)
	turnID := voiceJourneyEvent(t, f.device, "input_started")["turnId"].(string)
	voiceBargeInterpret(t, peer, f.device, turnID, reference)
	voiceJourneyEvent(t, f.device, "intent")
	intent, err := f.journal.Intent(f.ctx, turnID)
	if err != nil || intent.State != hubcore.VoiceIntentAccepted || intent.Text != reference {
		t.Fatalf("reference admission failed: %+v %v", intent, err)
	}
	want := appwire.VoicePresentationContext{
		Version: 1, DeliveryID: delivery.ID, BindingID: delivery.BindingID, Epoch: delivery.Epoch, InstanceID: f.instance,
		CanonicalSourceItemID: delivery.CanonicalSourceItemID, OriginatingRequestID: delivery.OriginatingRequestID, FrontierID: delivery.FrontierID,
		RunGeneration: delivery.RunGeneration, PresentationGeneration: delivery.PresentationGeneration,
		GeneratedTranscript: presented, OutputComplete: true, Interrupted: true, SampleRateHz: 24000,
		Items: []appwire.VoicePresentationItem{{ResponseID: "reference-response", ItemID: "reference-item", GeneratedSamples: 2, QueuedSamples: 2, PlayedSamples: 1}},
	}
	request := f.next(t)
	messages, _ := request.Body["messages"].([]any)
	found := 0
	for _, raw := range messages {
		message, _ := raw.(map[string]any)
		var texts []string
		switch content := message["content"].(type) {
		case string:
			texts = append(texts, content)
		case []any:
			for _, rawPart := range content {
				part, _ := rawPart.(map[string]any)
				if text, ok := part["text"].(string); ok {
					texts = append(texts, text)
				}
			}
		}
		for _, text := range texts {
			if !strings.Contains(text, presented) {
				continue
			}
			if message["role"] != "user" {
				t.Fatalf("presentation elevated to role %v", message["role"])
			}
			index := strings.Index(text, `{"inputId":`)
			var evidence struct {
				InputID      string                           `json:"inputId"`
				Historical   bool                             `json:"historical"`
				Presentation appwire.VoicePresentationContext `json:"presentation"`
			}
			if index < 0 || json.NewDecoder(strings.NewReader(text[index:])).Decode(&evidence) != nil || evidence.InputID != "client:"+intent.MutationID || evidence.Historical || !reflect.DeepEqual(evidence.Presentation, want) {
				t.Fatalf("actual model request lost exact pinned speech facts: %+v", evidence)
			}
			found++
		}
	}
	if found != 1 || !request.Contains(reference) {
		t.Fatalf("actual model request contains %d presentation envelopes", found)
	}
	voicePresentationRetry(t, f, intent)
	read := f.read(t)
	inputs := 0
	for _, turn := range read.Thread.Turns {
		for _, item := range turn.Items {
			if item.Type == "userMessage" && item.ClientMutationID == intent.MutationID {
				inputs++
				if item.Text != reference || item.Actor == nil || item.Actor.Kind != "human" {
					t.Fatalf("presentation rewrote user text or attribution: %+v", item)
				}
			}
		}
	}
	if inputs != 1 || voiceCorrectionItem(read, canonical).ID != delivery.CanonicalSourceItemID {
		t.Fatal("retry duplicated user input or replaced the canonical source")
	}
	request.RespondToolCall("communicate", communicateArgs("REFERENCE-SETTLED-191"))
}

func voicePresentationRetry(t *testing.T, f *voiceCorrectionStack, intent hubcore.VoiceIntent) {
	t.Helper()
	// The bridge owns platform evidence. Retry its exact immutable payload at
	// the original local daemon, not through the untrusted public hub ingress.
	entries, err := rendezvous.ListStrict(filepath.Join(f.stack.home, "state", "evener", "run"))
	if err != nil {
		t.Fatal(err)
	}
	var address string
	header := http.Header{}
	for _, entry := range entries {
		if entry.InstanceID == f.instance && entry.WorkspaceRef == f.ref {
			address = entry.Address
			header.Set("Authorization", "Bearer "+entry.HubToken)
		}
	}
	if address == "" {
		t.Fatal("original daemon rendezvous missing")
	}
	transport, err := appwire.DialWebSocketWithHeaders(f.ctx, "ws://"+address+"/rpc", nil, header)
	if err != nil {
		t.Fatal(err)
	}
	client := appwire.NewClient(transport)
	defer client.Close()
	client.Start(f.ctx)
	if _, err := client.Initialize(f.ctx, appwire.InitializeParams{ClientInfo: appwire.ClientInfo{Name: "voice-presentation-retry", Version: "test"}}); err != nil {
		t.Fatal(err)
	}
	var replay appwire.TurnStartResponse
	if err := client.Request(f.ctx, intent.Method, intent.Params, &replay); err != nil || replay.Receipt.Disposition != appwire.MutationDispositionReplayed {
		t.Fatalf("ordinary answer retry was not idempotent: %+v %v", replay.Receipt, err)
	}
}
