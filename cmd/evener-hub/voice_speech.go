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

func validVoiceProvenance(state *appwire.ConversationProvenanceState, instanceID string) bool {
	return state != nil && state.Version == 1 && state.Authoritative && instanceID != "" && state.InstanceID == instanceID
}

func (b *voiceBridge) validSpeechRead(read appwire.ThreadReadResponse) bool {
	evener := read.Thread.Evener
	return evener.Ref == b.binding.Ref && evener.InstanceID == b.binding.InstanceID && evener.Manager && evener.MutationStateAuthoritative && evener.Capabilities.VoicePresentationContextV1 && validVoiceProvenance(evener.ConversationProvenance, b.binding.InstanceID) && read.Snapshot != nil
}

func (b *voiceBridge) captureSpeechBaseline(read appwire.ThreadReadResponse) {
	b.history = *read.Snapshot
	b.historyThreadID, b.historyBoot, b.historyEpoch = read.Thread.ID, read.BootGeneration, read.Epoch
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
	read, err := b.client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: b.binding.Ref, IncludeTurns: true, ItemLimit: appwire.TranscriptItemPageLimit, LiveOnly: true})
	if err != nil || !b.validSpeechRead(read) || read.Snapshot.Incarnation != history.Incarnation {
		return read, err
	}
	read, err = b.backfillSpeechHistory(ctx, read, cursor)
	if err != nil {
		return read, err
	}
	return b.resolveSpeechQuestions(ctx, read, cursor)
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

// The row is a bounded prefix. Resolve omitted candidates before selection,
// keeping network reads in the history worker and its observed pending-set pair.
func (b *voiceBridge) resolveSpeechQuestions(ctx context.Context, read appwire.ThreadReadResponse, cursor *appwire.ThreadItemPosition) (appwire.ThreadReadResponse, error) {
	pending := read.Thread.Evener.PendingAsk
	if pending == nil || !pending.CallsTruncated {
		return read, nil
	}
	known := make(map[appwire.PendingAskCall]bool, len(pending.Calls))
	for _, call := range pending.Calls {
		known[call] = true
	}
	wanted := make(map[appwire.PendingAskCall]bool)
	for _, turn := range read.Thread.Turns {
		for _, item := range turn.Items {
			provenance := item.ConversationProvenance
			if item.Type != "commandExecution" || item.ToolName != "ask_user" || item.Status != appwire.TurnStatusCompleted || item.Position == nil || cursor != nil && appitempaging.ComparePosition(*item.Position, *cursor) <= 0 || provenance == nil || provenance.SourceItemID != item.ID || !appwire.ConversationOutputEligible(read.Thread.Evener.ConversationProvenance, provenance) {
				continue
			}
			call := appwire.PendingAskCall{TurnID: item.TurnID, CallID: item.CallID}
			if !known[call] {
				wanted[call] = true
			}
		}
	}
	if len(wanted) == 0 {
		return read, nil
	}
	pending = appwire.ClonePendingAsk(pending)
	for offset := 0; ; {
		page, err := b.client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: b.binding.Ref, ExpectedAskRevision: &pending.Revision, ExpectedAskBootGeneration: pending.BootGeneration, Offset: &offset, LiveOnly: true})
		if err != nil {
			return appwire.ThreadReadResponse{}, err
		}
		state := page.Thread.Evener.PendingAsk
		if state == nil || state.InstanceID != pending.InstanceID || state.BootGeneration != pending.BootGeneration || state.Revision != pending.Revision || page.PendingAskPage == nil {
			return appwire.ThreadReadResponse{}, appwire.TranscriptItemCursorStale()
		}
		for _, call := range page.PendingAskPage.Composites {
			if !known[call] {
				known[call] = true
				pending.Calls = append(pending.Calls, call)
			}
			delete(wanted, call)
		}
		if len(wanted) == 0 || page.PendingAskPage.NextOffset == nil {
			break
		}
		if *page.PendingAskPage.NextOffset <= offset {
			return appwire.ThreadReadResponse{}, appwire.TranscriptItemCursorStale()
		}
		offset = *page.PendingAskPage.NextOffset
	}
	read.Thread.Evener.PendingAsk = pending
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
	if err := b.applySpeechAuthority(read); err != nil {
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
		if b.interruptedTurns[turn.ID] {
			continue
		}
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

func (b *voiceBridge) applySpeechAuthority(read appwire.ThreadReadResponse) error {
	if !b.validSpeechRead(read) {
		return hubcore.ErrVoiceStale
	}
	if err := b.applyProvenance(read.Thread.Evener.ConversationProvenance); err != nil {
		return err
	}
	return b.applyInterruptedTurns(read.Thread.Turns)
}

// Resync is a read demand, never evidence that any execution was interrupted.
// Relay recovery can name only the ref, without a history epoch or thread ID.
func (b *voiceBridge) applySpeechResync(changed appwire.ThreadResyncParams) {
	if changed.Ref == b.binding.Ref && (changed.ThreadID == "" || changed.ThreadID == b.historyThreadID) {
		b.reconcileNow()
	}
}

// An authenticated terminal notification can retire setup immediately, but
// only under the established history identity. Recovery identities demand an
// authoritative read without waiting for that setup to finish.
func (b *voiceBridge) applySpeechHistoryUpdate(changed appwire.HistoryUpdatedParams) error {
	if changed.Ref != b.binding.Ref || changed.ThreadID != b.historyThreadID {
		return nil
	}
	if changed.BootGeneration != b.historyBoot || changed.Epoch != b.historyEpoch || changed.Snapshot.Incarnation != b.history.Incarnation {
		b.reconcileNow()
		return nil
	}
	return b.applyInterruptedTurns(changed.Turns)
}

// Interruption is terminal within this bound manager instance. Keep the evidence
// across older in-flight reads and paged windows that omit the stopped execution.
// Execution IDs are not provenance run IDs or audio presentation generations.
func (b *voiceBridge) applyInterruptedTurns(turns []appwire.Turn) error {
	for _, turn := range turns {
		if turn.ID != "" && turn.Status == appwire.TurnStatusInterrupted {
			if b.interruptedTurns == nil {
				b.interruptedTurns = make(map[string]bool)
			}
			b.interruptedTurns[turn.ID] = true
		}
	}
	if b.delivery != nil && !b.playbackFinished() && b.interruptedTurns[b.deliveryTurnID] {
		return b.retireSpeech()
	}
	return nil
}

func (b *voiceBridge) applyProvenance(state *appwire.ConversationProvenanceState) error {
	if !validVoiceProvenance(state, b.binding.InstanceID) {
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
	if err := b.updateDelivery(b.ctx, hubcore.SpeechDeliveryUpdate{Interrupted: !b.playbackFinished()}); err != nil {
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
	b.deliveryTurnID = source.TurnID
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

// Chunk counters stay in memory. A milestone flushes every pending item before
// marking completion or interruption, which fence further generated samples.
func (b *voiceBridge) updateDelivery(ctx context.Context, update hubcore.SpeechDeliveryUpdate) error {
	update.Epoch, update.PresentationGeneration = b.binding.Epoch, b.delivery.PresentationGeneration
	update.GeneratedTranscript = &b.delivery.GeneratedTranscript
	for _, item := range b.delivery.Items {
		if update.Item != nil && update.Item.ItemID == item.ItemID {
			continue
		}
		if _, err := b.journal.UpdateSpeechDelivery(ctx, b.delivery.ID, hubcore.SpeechDeliveryUpdate{
			Epoch: update.Epoch, PresentationGeneration: update.PresentationGeneration, Item: &item,
		}); err != nil {
			return err
		}
	}
	delivery, err := b.journal.UpdateSpeechDelivery(ctx, b.delivery.ID, update)
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
		index := -1
		for i, existing := range b.delivery.Items {
			if existing.ItemID == event.ItemID {
				index = i
				break
			}
		}
		newItem := index < 0
		if newItem {
			index = len(b.delivery.Items)
			b.delivery.Items = append(b.delivery.Items, hubcore.SpeechDeliveryItem{ResponseID: event.ResponseID, ItemID: event.ItemID})
		}
		item := &b.delivery.Items[index]
		item.GeneratedSamples += int64(len(event.PCM) / 2)
		item.QueuedSamples = item.GeneratedSamples
		if newItem {
			if err := b.updateDelivery(b.ctx, hubcore.SpeechDeliveryUpdate{}); err != nil {
				return err
			}
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
		b.delivery.GeneratedTranscript += text
		message["type"], message["text"] = "speech_text", text
	case "output_done":
		if event.Status != "completed" {
			return hubcore.ErrVoiceConflict
		}
		if err := b.updateDelivery(b.ctx, hubcore.SpeechDeliveryUpdate{OutputComplete: true}); err != nil {
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
	var delivery hubcore.SpeechDelivery
	current := b.delivery != nil && b.delivery.ID == control.DeliveryID
	if current {
		// Device progress may be ahead of the last durable milestone.
		delivery = *b.delivery
	} else {
		var err error
		delivery, err = b.journal.SpeechDelivery(b.ctx, control.DeliveryID)
		if err != nil {
			return err
		}
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
		update := hubcore.SpeechDeliveryUpdate{Epoch: delivery.Epoch, PresentationGeneration: control.Generation, Item: &item}
		if current {
			wasFinished := b.playbackFinished()
			if err := b.updateDelivery(b.ctx, update); err != nil {
				return err
			}
			if !wasFinished && b.playbackFinished() {
				b.refresh = true
			}
		} else if _, err := b.journal.UpdateSpeechDelivery(b.ctx, delivery.ID, update); err != nil {
			return err
		}
		return nil
	}
	return hubcore.ErrVoiceInvalid
}
