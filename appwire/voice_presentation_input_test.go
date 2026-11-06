package appwire

import "testing"

func TestVoicePresentationInputNormalization(t *testing.T) {
	t.Parallel()
	for _, method := range []string{MethodTurnStart, MethodTurnSteer} {
		t.Run(method, func(t *testing.T) {
			items := voicePresentationInputFixture()
			input, err := NormalizeVoicePresentationInput(items, method, "human", "instance-719")
			if err != nil {
				t.Fatal(err)
			}
			if !input.HasContent() || len(input.Items) != 1 || input.Items[0].Text != items[0].Text {
				t.Fatalf("normalized input lost text: %+v", input)
			}
			items[0].Text = "changed-831"
			items[0].VoicePresentation.Items[0].PlayedSamples = 0
			items[0].VoicePresentation.GeneratedTranscript = "changed-831"
			if input.Items[0].Text == items[0].Text || input.Items[0].VoicePresentation.Items[0].PlayedSamples != 1 || input.Items[0].VoicePresentation.GeneratedTranscript == items[0].VoicePresentation.GeneratedTranscript {
				t.Fatal("normalization retained caller aliases")
			}
			input.Items[0].VoicePresentation.Items[0].QueuedSamples = 3
			if items[0].VoicePresentation.Items[0].QueuedSamples != 2 {
				t.Fatal("normalized evidence mutation changed caller input")
			}
		})
	}
	input, err := NormalizeVoicePresentationInput([]InputItem{{Type: "text", Text: " "}, {Type: "skill", Name: "  skill-719  "}}, "", "", "")
	if err != nil || len(input.Items) != 1 || input.Items[0].Type != "skill" || input.Items[0].Name != "skill-719" || !input.HasContent() {
		t.Fatalf("nonvoice normalization changed: %+v, %v", input, err)
	}
}

func TestVoicePresentationInputNormalizationRejectsInvalid(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		method   string
		actor    string
		instance string
		change   func([]InputItem)
	}{
		{name: "method", method: "turn/queue", actor: "human", instance: "instance-719"},
		{name: "actor", method: MethodTurnStart, actor: "manager", instance: "instance-719"},
		{name: "instance", method: MethodTurnStart, actor: "human", instance: "other-831"},
		{name: "blank", method: MethodTurnStart, actor: "human", instance: "instance-719", change: func(items []InputItem) { items[0].Text = " " }},
		{name: "samples", method: MethodTurnStart, actor: "human", instance: "instance-719", change: func(items []InputItem) { items[0].VoicePresentation.Items[0].PlayedSamples = 4 }},
		{name: "nonvoice-shape", change: func(items []InputItem) { items[0] = InputItem{Type: "unsupported"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := voicePresentationInputFixture()
			if tc.change != nil {
				tc.change(items)
			}
			input, err := NormalizeVoicePresentationInput(items, tc.method, tc.actor, tc.instance)
			if err == nil || input.Items != nil || input.HasContent() {
				t.Fatalf("invalid input not rejected atomically: %+v, %v", input, err)
			}
			if err := ValidateVoicePresentationInput(items, tc.method, tc.actor, tc.instance); err == nil {
				t.Fatal("validation wrapper accepted invalid input")
			}
		})
	}
}

func voicePresentationInputFixture() []InputItem {
	return []InputItem{{Type: "text", Text: "  opaque-719\n", VoicePresentation: &VoicePresentationContext{
		Version: 1, DeliveryID: "delivery-719", BindingID: "binding-719", Epoch: "epoch-719", InstanceID: "instance-719",
		CanonicalSourceItemID: "source-719", OriginatingRequestID: "request-719", FrontierID: "frontier-719",
		GeneratedTranscript: "transcript-719", SampleRateHz: 24000,
		Items: []VoicePresentationItem{{ResponseID: "response-719", ItemID: "item-719", GeneratedSamples: 3, QueuedSamples: 2, PlayedSamples: 1}},
	}}}
}
