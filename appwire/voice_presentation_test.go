package appwire

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func TestVoicePresentationNormalizationRetainsEvidence(t *testing.T) {
	raw := []byte(`[{"type":"text","text":"  second one\n","voicePresentation":{"version":1,"deliveryId":"delivery","bindingId":"binding","epoch":"epoch","instanceId":"instance","canonicalSourceItemId":"source","originatingRequestId":"request","frontierId":"frontier","runGeneration":1,"presentationGeneration":1,"generatedTranscript":"second then first","sampleRateHz":24000,"items":[{"responseId":"response","itemId":"item","generatedSamples":24,"queuedSamples":12,"playedSamples":0}]}}]`)
	var items []InputItem
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatal(err)
	}
	normalized, err := NormalizeMutationInput(items)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(normalized.Items)
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]json.RawMessage
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0]["voicePresentation"] == nil {
		t.Fatalf("evidence dropped: %s", data)
	}
	if normalized.Items[0].Text != "  second one\n" {
		t.Fatal("text changed")
	}
}

func TestVoicePresentationBoundsAndIsolation(t *testing.T) {
	valid := func() *VoicePresentationContext {
		return &VoicePresentationContext{Version: 1, DeliveryID: "delivery", BindingID: "binding", Epoch: "epoch", InstanceID: "instance", CanonicalSourceItemID: "source", OriginatingRequestID: "request", FrontierID: "frontier", SampleRateHz: 24000, Items: []VoicePresentationItem{{ResponseID: "response", ItemID: "item", GeneratedSamples: 3, QueuedSamples: 2, PlayedSamples: 1}}}
	}
	cases := map[string]func(*VoicePresentationContext){
		"version":       func(v *VoicePresentationContext) { v.Version = 2 },
		"rate":          func(v *VoicePresentationContext) { v.SampleRateHz = 16000 },
		"identity":      func(v *VoicePresentationContext) { v.DeliveryID = strings.Repeat("a", 513) },
		"identity-utf8": func(v *VoicePresentationContext) { v.Epoch = string([]byte{255}) },
		"transcript": func(v *VoicePresentationContext) {
			v.GeneratedTranscript = strings.Repeat("a", 16385)
			v.TranscriptTruncated = true
		},
		"transcript-utf8": func(v *VoicePresentationContext) { v.GeneratedTranscript = string([]byte{255}) },
		"negative":        func(v *VoicePresentationContext) { v.Items[0].PlayedSamples = -1 },
		"played":          func(v *VoicePresentationContext) { v.Items[0].PlayedSamples = 3 },
		"queued":          func(v *VoicePresentationContext) { v.Items[0].QueuedSamples = 4 },
		"duplicate":       func(v *VoicePresentationContext) { v.Items = append(v.Items, v.Items[0]) },
		"items": func(v *VoicePresentationContext) {
			for i := range 32 {
				v.Items = append(v.Items, VoicePresentationItem{ResponseID: "r", ItemID: strconv.Itoa(i)})
			}
		},
		"serialized": func(v *VoicePresentationContext) { v.GeneratedTranscript = strings.Repeat("\x00", 16384) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			v := valid()
			change(v)
			if err := ValidateVoicePresentation(v); err == nil {
				t.Fatal("accepted invalid evidence")
			}
		})
	}
	v := valid()
	v.GeneratedTranscript = strings.Repeat("é", 8192)
	v.TranscriptTruncated = true
	items := []InputItem{{Type: "text", Text: "opaque-719", VoicePresentation: v}}
	normalized, err := NormalizeMutationInput(items)
	if err != nil {
		t.Fatal(err)
	}
	v.Items[0].PlayedSamples = 0
	v.GeneratedTranscript = "changed"
	if normalized.Items[0].VoicePresentation.Items[0].PlayedSamples != 1 || len(normalized.Items[0].VoicePresentation.GeneratedTranscript) != 16384 {
		t.Fatal("normalization retained caller aliases")
	}
	for _, shape := range [][]InputItem{
		{{Type: "text", Text: " ", VoicePresentation: valid()}},
		{{Type: "image", VoicePresentation: valid()}},
		{{Type: "skill", Name: "x", VoicePresentation: valid()}},
		{{Type: "text", Text: "opaque-719", VoicePresentation: valid()}, {Type: "text", Text: "opaque-831"}},
	} {
		if _, err := NormalizeMutationInput(shape); err == nil {
			t.Fatal("accepted invalid attachment shape")
		}
	}
	for _, method := range []string{"turn/queue", "turn/drainAsSteer", "thread/start", ""} {
		if err := ValidateVoicePresentationInput(normalized.Items, method, "human", "instance"); err == nil {
			t.Fatalf("unsupported ingress accepted %s", method)
		}
	}
	for _, actor := range []string{"manager", ""} {
		if err := ValidateVoicePresentationInput(normalized.Items, "turn/start", actor, "instance"); err == nil {
			t.Fatal("nonhuman evidence accepted")
		}
	}
	if err := ValidateVoicePresentationInput(normalized.Items, "turn/start", "human", "other"); err == nil {
		t.Fatal("wrong instance accepted")
	}
}

func TestVoicePresentationRejectsInvalidWireUTF8(t *testing.T) {
	data := append([]byte(`{"type":"text","text":"opaque-719","voicePresentation":{"generatedTranscript":"`), 255)
	data = append(data, []byte(`"}}`)...)
	var item InputItem
	if err := json.Unmarshal(data, &item); err == nil {
		t.Fatal("invalid UTF-8 evidence was replaced during wire decoding")
	}
}
