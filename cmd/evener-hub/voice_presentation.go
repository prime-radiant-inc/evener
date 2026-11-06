package hub

import (
	"context"

	"primeradiant.com/evener/appwire"
)

// Only the journal-backed voice bridge may attach platform presentation facts.
// Public request fields cannot create this in-process transport identity.
type voicePresentationContextKey struct{}

func validateHubVoicePresentation(ctx context.Context, items []appwire.InputItem, method, instanceID string) error {
	for _, item := range items {
		if item.VoicePresentation == nil {
			continue
		}
		if trusted, _ := ctx.Value(voicePresentationContextKey{}).(bool); !trusted {
			return appwire.InvalidParams("voice presentation evidence is owned by the voice bridge")
		}
		if err := appwire.ValidateVoicePresentationInput(items, method, "human", instanceID); err != nil {
			return appwire.InvalidParams(err.Error())
		}
		break
	}
	return nil
}
