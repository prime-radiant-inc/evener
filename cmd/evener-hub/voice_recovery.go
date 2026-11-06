package hub

import (
	"context"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func (b *voiceBridge) recoverIntents(ctx context.Context) error {
	var cursor int64
	for {
		intents, next, err := b.journal.ListIntents(ctx, b.binding.ID, cursor, 200)
		if err != nil {
			return err
		}
		if len(intents) == 0 {
			return nil
		}
		for _, intent := range intents {
			if intent.State == hubcore.VoiceIntentAccepted {
				continue
			}
			if intent.State != hubcore.VoiceIntentRejected {
				read, err := b.client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: b.binding.Ref})
				if err != nil {
					return err
				}
				e := read.Thread.Evener
				if e.Ref != intent.Ref || e.InstanceID != intent.ExpectedInstanceID || !e.Manager || !e.MutationStateAuthoritative {
					return hubcore.ErrVoiceStale
				}
				// A window's absence cannot prove non-delivery. Reconcile against the
				// original instance's durable mutation owner by replaying the exact
				// stored method and bytes, even when its current activity has changed.
				intent, err = b.submitIntent(ctx, intent, true)
				if err != nil {
					return err
				}
			}
			if err := b.send(map[string]any{"type": "intent", "turnId": intent.TurnID, "text": intent.Text, "custody": intent.State}); err != nil {
				return err
			}
		}
		cursor = next
	}
}
