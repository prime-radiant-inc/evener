package hubcore

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"primeradiant.com/evener/appwire"
)

func TestVoiceJournalPresentationPinnedAtUtteranceStart(t *testing.T) {
	t.Parallel()
	j, root, binding := voiceJournalFixture(t)
	ctx := context.Background()
	none, err := j.CreateTurn(ctx, binding.ID, binding.Epoch, 1)
	if err != nil {
		t.Fatal(err)
	}
	first := voiceJournalDelivery(binding)
	if err := j.CreateSpeechDelivery(ctx, first); err != nil {
		t.Fatal(err)
	}
	pinned, err := j.CreateTurn(ctx, binding.ID, binding.Epoch, 2)
	if err != nil {
		t.Fatal(err)
	}
	later := first
	later.ID, later.CanonicalSourceItemID = "later-delivery", "later-source"
	later.PresentationGeneration++
	if err := j.CreateSpeechDelivery(ctx, later); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewVoiceJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for _, tc := range []struct {
		turn VoiceTurn
		want string
	}{{none, ""}, {pinned, first.ID}} {
		got, err := reopened.CreateTurn(ctx, binding.ID, binding.Epoch, tc.turn.Sequence)
		if err != nil || got.ID != tc.turn.ID {
			t.Fatalf("replaying original utterance identity: %+v %v", got, err)
		}
		var record map[string]json.RawMessage
		raw, err := json.Marshal(got)
		if err != nil || json.Unmarshal(raw, &record) != nil {
			t.Fatal("encoding utterance record")
		}
		var deliveryID string
		if json.Unmarshal(record["presentationDeliveryId"], &deliveryID) != nil || deliveryID != tc.want {
			t.Errorf("utterance %d lost its presentation association: raw=%s want %q", got.Sequence, record["presentationDeliveryId"], tc.want)
		}
	}
}

func TestVoiceJournalPresentationBoundsAndBinding(t *testing.T) {
	t.Parallel()
	j, _, binding := voiceJournalFixture(t)
	ctx := context.Background()
	source := voiceJournalDelivery(binding)
	if err := j.CreateSpeechDelivery(ctx, source); err != nil {
		t.Fatal(err)
	}
	transcript := strings.Repeat("x", appwire.VoicePresentationMaxTranscriptBytes-1) + "€TAIL-193"
	if _, err := j.UpdateSpeechDelivery(ctx, source.ID, SpeechDeliveryUpdate{Epoch: binding.Epoch, PresentationGeneration: source.PresentationGeneration, GeneratedTranscript: &transcript}); err != nil {
		t.Fatal(err)
	}
	other := binding
	other.ID, other.Ref, other.Epoch = "other-binding", "local:other-session", "other-epoch"
	if err := j.CreateBinding(ctx, other, false); err != nil {
		t.Fatal(err)
	}
	foreign := voiceJournalDelivery(other)
	foreign.ID = "foreign-delivery"
	if err := j.CreateSpeechDelivery(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	turn, err := j.CreateTurn(ctx, binding.ID, binding.Epoch, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.CommitTurnInput(ctx, turn.ID, "input-item"); err != nil {
		t.Fatal(err)
	}
	intent, err := j.PrepareIntent(ctx, turn.ID, appwire.MethodTurnStart, "REFERENCE-197", "mutation-bounded")
	if err != nil {
		t.Fatal(err)
	}
	var params appwire.TurnStartParams
	if json.Unmarshal(intent.Params, &params) != nil || len(params.Input) != 1 || params.Input[0].VoicePresentation == nil {
		t.Fatal("intent lost bounded evidence")
	}
	presentation := params.Input[0].VoicePresentation
	if presentation.DeliveryID != source.ID || presentation.BindingID != binding.ID || presentation.Epoch != binding.Epoch {
		t.Fatal("another binding's later speech replaced this utterance's presentation")
	}
	if !presentation.TranscriptTruncated || !utf8.ValidString(presentation.GeneratedTranscript) || presentation.GeneratedTranscript != strings.Repeat("x", appwire.VoicePresentationMaxTranscriptBytes-1) {
		t.Fatal("transcript truncation lost its uncertainty or split UTF-8")
	}
}

func TestVoiceJournalPresentationFrozenIntoIntent(t *testing.T) {
	t.Parallel()
	for _, method := range []string{appwire.MethodTurnStart, appwire.MethodTurnSteer} {
		t.Run(method, func(t *testing.T) {
			j, root, binding := voiceJournalFixture(t)
			ctx := context.Background()
			none, err := j.CreateTurn(ctx, binding.ID, binding.Epoch, 1)
			if err != nil {
				t.Fatal(err)
			}
			source := voiceJournalDelivery(binding)
			if err := j.CreateSpeechDelivery(ctx, source); err != nil {
				t.Fatal(err)
			}
			turn, err := j.CreateTurn(ctx, binding.ID, binding.Epoch, 2)
			if err != nil {
				t.Fatal(err)
			}
			later := source
			later.ID, later.CanonicalSourceItemID = "newer-delivery", "newer-source"
			later.PresentationGeneration++
			if err := j.CreateSpeechDelivery(ctx, later); err != nil {
				t.Fatal(err)
			}
			transcript := "PRESENTED-BETA-127 PRESENTED-ALPHA-131"
			item := SpeechDeliveryItem{ResponseID: "response", ItemID: "item", GeneratedSamples: 480, QueuedSamples: 480, PlayedSamples: 240}
			update := SpeechDeliveryUpdate{Epoch: binding.Epoch, PresentationGeneration: source.PresentationGeneration, Item: &item, GeneratedTranscript: &transcript, OutputComplete: true, Interrupted: true}
			if _, err := j.UpdateSpeechDelivery(ctx, source.ID, update); err != nil {
				t.Fatal(err)
			}
			if _, err := j.CommitTurnInput(ctx, turn.ID, "input-item"); err != nil {
				t.Fatal(err)
			}
			text := "\n  REFERENCE-INPUT-137\t"
			intent, err := j.PrepareIntent(ctx, turn.ID, method, text, "voice-mutation")
			if err != nil {
				t.Fatal(err)
			}
			var params struct {
				Input []struct {
					Type              string          `json:"type"`
					Text              string          `json:"text"`
					VoicePresentation json.RawMessage `json:"voicePresentation"`
				} `json:"input"`
			}
			if err := json.Unmarshal(intent.Params, &params); err != nil || len(params.Input) != 1 || params.Input[0].Text != text || params.Input[0].Type != "text" {
				t.Fatalf("presentation changed ordinary text admission: %s %v", intent.Params, err)
			}
			want := map[string]any{
				"version": 1, "deliveryId": source.ID, "bindingId": binding.ID, "epoch": binding.Epoch, "instanceId": binding.InstanceID,
				"canonicalSourceItemId": source.CanonicalSourceItemID, "originatingRequestId": source.OriginatingRequestID, "frontierId": source.FrontierID,
				"runGeneration": source.RunGeneration, "presentationGeneration": source.PresentationGeneration,
				"generatedTranscript": transcript, "transcriptTruncated": false, "outputComplete": true, "interrupted": true, "sampleRateHz": 24000,
				"items": []any{map[string]any{"responseId": item.ResponseID, "itemId": item.ItemID, "generatedSamples": 480, "queuedSamples": 480, "playedSamples": 240, "playbackComplete": false}},
			}
			wantJSON, err := json.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}
			var actual, expected any
			if json.Unmarshal(wantJSON, &expected) != nil || json.Unmarshal(params.Input[0].VoicePresentation, &actual) != nil || !reflect.DeepEqual(actual, expected) {
				t.Fatalf("intent lost pinned presentation evidence: got %s want %s", params.Input[0].VoicePresentation, wantJSON)
			}
			item.PlayedSamples, item.PlaybackComplete = 480, true
			if _, err := j.UpdateSpeechDelivery(ctx, source.ID, update); err != nil {
				t.Fatal(err)
			}
			if _, err := j.CommitTurnInput(ctx, none.ID, "no-presentation-input"); err != nil {
				t.Fatal(err)
			}
			noPresentation, err := j.PrepareIntent(ctx, none.ID, method, text, "no-presentation-mutation")
			if err != nil {
				t.Fatal(err)
			}
			params.Input = nil
			if json.Unmarshal(noPresentation.Params, &params) != nil || len(params.Input) != 1 || params.Input[0].VoicePresentation != nil {
				t.Fatalf("later speech replaced pinned absence: %s", noPresentation.Params)
			}
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := NewVoiceJournal(root)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			replayed, err := reopened.PrepareIntent(ctx, turn.ID, method, text, intent.MutationID)
			if err != nil || !reflect.DeepEqual(replayed, intent) {
				t.Fatalf("recovery refreshed frozen presentation: %+v %v", replayed, err)
			}
		})
	}
}
