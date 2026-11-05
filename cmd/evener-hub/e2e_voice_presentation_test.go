package hub

import (
	"fmt"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
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
