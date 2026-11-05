package hub

import (
	"context"
	"encoding/base64"
	"strings"

	"github.com/google/uuid"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appitempaging"
)

func (b *voiceBridge) validSpeechRead(read appwire.ThreadReadResponse) bool {
	evener := read.Thread.Evener
	state := evener.ConversationProvenance
	return evener.Ref == b.binding.Ref && evener.InstanceID == b.binding.InstanceID && evener.Manager && evener.MutationStateAuthoritative && evener.Capabilities.VoicePresentationContextV1 && state != nil && state.Authoritative && state.InstanceID == b.binding.InstanceID && read.Snapshot != nil
}

func (b *voiceBridge) captureSpeechBaseline(read appwire.ThreadReadResponse) {
	b.history = *read.Snapshot
	b.speechCursor = nil
	for _, turn := range read.Thread.Turns {
		for _, item := range turn.Items {
			if item.Position != nil && (b.speechCursor == nil || appitempaging.ComparePosition(*item.Position, *b.speechCursor) > 0) {
				b.speechCursor = appwire.ClonePointer(item.Position)
			}
		}
	}
}

// Walk back to the last selected source before choosing the next one. Pages
// from another snapshot cannot prove there was no earlier unsaid output.
func (b *voiceBridge) readSpeechHistory(ctx context.Context, history appwire.SnapshotIdentity, cursor *appwire.ThreadItemPosition) (appwire.ThreadReadResponse, error) {
	read, err := b.client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: b.binding.Ref, IncludeTurns: true, ItemLimit: appwire.TranscriptItemPageLimit})
	if err != nil || !b.validSpeechRead(read) || read.Snapshot.Incarnation != history.Incarnation {
		return read, err
	}
	return b.backfillSpeechHistory(ctx, read, cursor)
}

func (b *voiceBridge) backfillSpeechHistory(ctx context.Context, read appwire.ThreadReadResponse, cursor *appwire.ThreadItemPosition) (appwire.ThreadReadResponse, error) {
	page := read.Thread.Turns
	for older := read.OlderCursor; older != "" && !voicePageReachesCursor(page, cursor); {
		next, err := b.client.ThreadTurnsList(ctx, appwire.ThreadTurnsListParams{Ref: b.binding.Ref, Cursor: older, ItemLimit: appwire.TranscriptItemPageLimit})
		if err != nil {
			return appwire.ThreadReadResponse{}, err
		}
		// Authoritative controls replacement of a returned history range, not
		// the identity or freshness of the snapshot it was projected from.
		if next.Snapshot == nil || *next.Snapshot != *read.Snapshot || next.BootGeneration != read.BootGeneration || next.Epoch != read.Epoch {
			return appwire.ThreadReadResponse{}, appwire.TranscriptItemCursorStale()
		}
		page, older = next.Data, next.NextCursor
		read.Thread.Turns = append(page, read.Thread.Turns...)
	}
	return read, nil
}

func voicePageReachesCursor(turns []appwire.Turn, cursor *appwire.ThreadItemPosition) bool {
	if cursor != nil {
		for _, turn := range turns {
			for _, item := range turn.Items {
				if item.Position != nil && appitempaging.ComparePosition(*item.Position, *cursor) <= 0 {
					return true
				}
			}
		}
	}
	return false
}

// Only recorded public output items carry request provenance. The initial
// item position is the speech cursor, so attaching never reads old history aloud.
// Snapshot length counts bytes, not the entry ordinals in item positions.
func (b *voiceBridge) selectSpeech(read appwire.ThreadReadResponse) error {
	if !b.validSpeechRead(read) {
		return hubcore.ErrVoiceStale
	}
	if err := b.applyProvenance(read.Thread.Evener.ConversationProvenance); err != nil {
		return err
	}
	if read.Snapshot.Incarnation != b.history.Incarnation {
		b.captureSpeechBaseline(read)
		return nil
	}
	if b.turn != nil || b.retiring != nil || (b.delivery != nil && !b.playbackFinished()) {
		return nil
	}
	for _, turn := range read.Thread.Turns {
		for _, item := range turn.Items {
			provenance := item.ConversationProvenance
			if item.Position == nil || (b.speechCursor != nil && appitempaging.ComparePosition(*item.Position, *b.speechCursor) <= 0) || provenance == nil || provenance.SourceItemID != item.ID || !appwire.ConversationOutputEligible(b.provenance, provenance) {
				continue
			}
			// A batched communicate can commit before ask_user's result. The
			// completed question keeps its opener position, so do not pass it.
			if item.Type == "commandExecution" && item.ToolName == "ask_user" && item.Status == appwire.TurnStatusInProgress {
				return nil
			}
			item.Text = voiceSpeechText(item, read.Thread.Evener.PendingAsk, b.binding.InstanceID)
			if strings.TrimSpace(item.Text) == "" {
				continue
			}
			b.speechCursor = appwire.ClonePointer(item.Position)
			return b.startSpeech(item)
		}
	}
	return nil
}

func (b *voiceBridge) applyProvenance(state *appwire.ConversationProvenanceState) error {
	if state == nil || !state.Authoritative || state.InstanceID != b.binding.InstanceID {
		return hubcore.ErrVoiceStale
	}
	if b.provenance != nil && state.Revision <= b.provenance.Revision {
		return nil
	}
	b.provenance = state
	if b.delivery == nil || b.playbackFinished() {
		return nil
	}
	delivery := b.delivery
	output := &appwire.ConversationOutputProvenance{
		Version: 1, InstanceID: delivery.InstanceID, SourceItemID: delivery.CanonicalSourceItemID,
		RequestID: delivery.OriginatingRequestID, FrontierID: delivery.FrontierID, RunGeneration: delivery.RunGeneration,
	}
	if appwire.ConversationOutputEligible(state, output) {
		return nil
	}
	return b.retireSpeech()
}

// Retire presentation before waiting for provider acknowledgement. The manager
// is untouched, and its recorded reply remains available as canonical text.
func (b *voiceBridge) retireSpeech() error {
	if err := b.updateDelivery(hubcore.SpeechDeliveryUpdate{Interrupted: !b.playbackFinished()}); err != nil {
		return err
	}
	b.retiring, b.delivery = b.delivery, nil
	b.refresh = true
	if err := b.advanceGeneration(); err != nil {
		return err
	}
	if b.workKind == "speech" {
		b.workCancel()
	}
	return b.send(map[string]any{"type": "stop", "generation": b.generation})
}

func (b *voiceBridge) interruptSpeech(control voiceControl) error {
	if control.Generation >= voiceMaxGeneration {
		return hubcore.ErrVoiceInvalid
	}
	if b.awaitingFence {
		// Every ready is acknowledged, including fresh Arm. Reconcile the phone's
		// retained fence without lowering generations already used by this binding.
		b.generation = max(b.generation, control.Generation+1)
		b.awaitingFence = false
		return b.send(map[string]any{"type": "stop", "generation": b.generation})
	}
	if control.Generation < b.generation {
		return nil
	}
	if b.delivery != nil && control.Generation == b.delivery.PresentationGeneration {
		for item, samples := range control.PlayedSamples {
			if err := b.recordPlayed(voiceControl{DeliveryID: b.delivery.ID, Generation: control.Generation, ItemID: item, Samples: samples}); err != nil {
				return err
			}
		}
	}
	b.generation = control.Generation
	if b.delivery != nil {
		return b.retireSpeech()
	}
	if err := b.advanceGeneration(); err != nil {
		return err
	}
	return b.send(map[string]any{"type": "stop", "generation": b.generation})
}

// Native and JavaScript clients must represent the same generation exactly.
const voiceMaxGeneration = 1<<53 - 1

func (b *voiceBridge) advanceGeneration() error {
	if b.generation >= voiceMaxGeneration {
		return hubcore.ErrVoiceInvalid
	}
	b.generation++
	return nil
}

func (b *voiceBridge) startSpeech(source appwire.ThreadItem) error {
	previous := b.delivery
	if err := b.advanceGeneration(); err != nil {
		return err
	}
	provenance := source.ConversationProvenance
	delivery := hubcore.SpeechDelivery{
		ID: uuid.NewString(), BindingID: b.binding.ID, Epoch: b.binding.Epoch, InstanceID: b.binding.InstanceID,
		CanonicalSourceItemID: source.ID, CanonicalText: source.Text,
		OriginatingRequestID: provenance.RequestID, FrontierID: provenance.FrontierID,
		RunGeneration: provenance.RunGeneration, PresentationGeneration: b.generation,
	}
	if err := b.journal.CreateSpeechDelivery(b.ctx, delivery); err != nil {
		return err
	}
	b.delivery = &delivery
	b.transcripts = make(map[string]string)
	if err := b.send(map[string]any{"type": "speech_start", "deliveryId": delivery.ID, "generation": delivery.PresentationGeneration, "sourceItemId": source.ID, "canonicalText": source.Text}); err != nil {
		return err
	}
	provider := b.provider
	b.beginWork("speech", func(ctx context.Context) voiceWorkResult {
		result := voiceWorkResult{}
		if provider == nil {
			provider, result.err = b.openProvider(ctx)
			result.provider = provider
		}
		if result.err == nil && previous != nil {
			played := make(map[string]int64, len(previous.Items))
			for _, item := range previous.Items {
				played[item.ItemID] = item.PlayedSamples
			}
			result.err = provider.Interrupt(ctx, previous.PresentationGeneration, played)
		}
		if result.err == nil {
			result.err = provider.Speak(ctx, delivery.ID, delivery.PresentationGeneration, delivery.CanonicalText)
		}
		return result
	})
	return nil
}

func (b *voiceBridge) playbackFinished() bool {
	if b.delivery == nil || !b.delivery.OutputComplete {
		return false
	}
	for _, item := range b.delivery.Items {
		if !item.PlaybackComplete || item.PlayedSamples != item.QueuedSamples {
			return false
		}
	}
	return true
}

func (b *voiceBridge) updateDelivery(update hubcore.SpeechDeliveryUpdate) error {
	update.Epoch, update.PresentationGeneration = b.binding.Epoch, b.delivery.PresentationGeneration
	delivery, err := b.journal.UpdateSpeechDelivery(b.ctx, b.delivery.ID, update)
	if err == nil {
		b.delivery = &delivery
	}
	return err
}

func (b *voiceBridge) providerEvent(event VoiceRealtimeEvent) error {
	if event.Kind == "error" {
		return event.Err
	}
	if b.delivery == nil || event.DeliveryID != b.delivery.ID || event.Generation != b.delivery.PresentationGeneration || b.delivery.Interrupted {
		return nil
	}
	message := map[string]any{"deliveryId": event.DeliveryID, "generation": event.Generation, "responseId": event.ResponseID, "itemId": event.ItemID}
	switch event.Kind {
	case "audio":
		item := hubcore.SpeechDeliveryItem{ResponseID: event.ResponseID, ItemID: event.ItemID}
		for _, existing := range b.delivery.Items {
			if existing.ItemID == event.ItemID {
				item = existing
			}
		}
		item.GeneratedSamples += int64(len(event.PCM) / 2)
		item.QueuedSamples = item.GeneratedSamples
		if err := b.updateDelivery(hubcore.SpeechDeliveryUpdate{Item: &item}); err != nil {
			return err
		}
		message["type"], message["pcm"], message["sampleRate"] = "audio", base64.StdEncoding.EncodeToString(event.PCM), 24000
	case "transcript_delta", "transcript_done":
		text := event.Transcript
		if event.Kind == "transcript_done" {
			previous := b.transcripts[event.ItemID]
			if !strings.HasPrefix(text, previous) {
				return hubcore.ErrVoiceConflict
			}
			text = strings.TrimPrefix(text, previous)
		}
		if text == "" {
			return nil
		}
		b.transcripts[event.ItemID] += text
		transcript := b.delivery.GeneratedTranscript + text
		if err := b.updateDelivery(hubcore.SpeechDeliveryUpdate{GeneratedTranscript: &transcript}); err != nil {
			return err
		}
		message["type"], message["text"] = "speech_text", text
	case "output_done":
		if event.Status != "completed" {
			return hubcore.ErrVoiceConflict
		}
		if err := b.updateDelivery(hubcore.SpeechDeliveryUpdate{OutputComplete: true}); err != nil {
			return err
		}
		for _, item := range b.delivery.Items {
			if err := b.send(map[string]any{"type": "speech_end", "deliveryId": event.DeliveryID, "generation": event.Generation, "responseId": item.ResponseID, "itemId": item.ItemID}); err != nil {
				return err
			}
		}
		message["type"] = "delivery_end"
		b.refresh = true
	default:
		return nil
	}
	return b.send(message)
}

func (b *voiceBridge) recordPlayed(control voiceControl) error {
	delivery, err := b.journal.SpeechDelivery(b.ctx, control.DeliveryID)
	if err != nil {
		return err
	}
	if delivery.BindingID != b.binding.ID || delivery.Epoch != b.binding.Epoch || delivery.PresentationGeneration != control.Generation {
		return hubcore.ErrVoiceStale
	}
	for _, item := range delivery.Items {
		if item.ItemID != control.ItemID {
			continue
		}
		item.PlayedSamples = control.Samples
		item.PlaybackComplete = item.PlaybackComplete || control.Complete
		updated, err := b.journal.UpdateSpeechDelivery(b.ctx, delivery.ID, hubcore.SpeechDeliveryUpdate{Epoch: delivery.Epoch, PresentationGeneration: control.Generation, Item: &item})
		if err != nil {
			return err
		}
		if b.delivery != nil && b.delivery.ID == delivery.ID {
			wasFinished := b.playbackFinished()
			b.delivery = &updated
			if !wasFinished && b.playbackFinished() {
				b.refresh = true
			}
		}
		return nil
	}
	return hubcore.ErrVoiceInvalid
}
