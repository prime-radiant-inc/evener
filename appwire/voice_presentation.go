package appwire

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	VoicePresentationMaxBytes           = 64 * 1024
	VoicePresentationMaxTranscriptBytes = 16 * 1024
	VoicePresentationMaxItems           = 32
	VoicePresentationMaxIdentityBytes   = 512
)

// VoicePresentationContext is bounded presentation evidence attached atomically
// to ordinary human text. Generated wording is data, not authority or a receipt.
type VoicePresentationContext struct {
	Version                int                     `json:"version"`
	DeliveryID             string                  `json:"deliveryId"`
	BindingID              string                  `json:"bindingId"`
	Epoch                  string                  `json:"epoch"`
	InstanceID             string                  `json:"instanceId"`
	CanonicalSourceItemID  string                  `json:"canonicalSourceItemId"`
	OriginatingRequestID   string                  `json:"originatingRequestId"`
	FrontierID             string                  `json:"frontierId"`
	RunGeneration          uint64                  `json:"runGeneration"`
	PresentationGeneration uint64                  `json:"presentationGeneration"`
	GeneratedTranscript    string                  `json:"generatedTranscript"`
	TranscriptTruncated    bool                    `json:"transcriptTruncated"`
	OutputComplete         bool                    `json:"outputComplete"`
	Interrupted            bool                    `json:"interrupted"`
	SampleRateHz           int                     `json:"sampleRateHz"`
	Items                  []VoicePresentationItem `json:"items"`
}

// VoicePresentationItem counters refer to an item's audio timeline, not words.
// PlaybackComplete records an explicit device completion report, not attention.
type VoicePresentationItem struct {
	ResponseID       string `json:"responseId"`
	ItemID           string `json:"itemId"`
	GeneratedSamples int64  `json:"generatedSamples"`
	QueuedSamples    int64  `json:"queuedSamples"`
	PlayedSamples    int64  `json:"playedSamples"`
	PlaybackComplete bool   `json:"playbackComplete"`
}

func (context *VoicePresentationContext) UnmarshalJSON(data []byte) error {
	if !utf8.Valid(data) {
		return fmt.Errorf("voice presentation must be valid UTF-8")
	}
	type plain VoicePresentationContext
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*context = VoicePresentationContext(decoded)
	return nil
}

func CloneVoicePresentation(context *VoicePresentationContext) *VoicePresentationContext {
	if context == nil {
		return nil
	}
	cloned := *context
	cloned.Items = slices.Clone(context.Items)
	return &cloned
}

func ValidateVoicePresentation(context *VoicePresentationContext) error {
	if context == nil {
		return nil
	}
	if context.Version != 1 || context.SampleRateHz != 24000 {
		return fmt.Errorf("unsupported voice presentation version or sample rate")
	}
	validIdentity := func(value string) bool {
		return strings.TrimSpace(value) != "" && len(value) <= VoicePresentationMaxIdentityBytes && utf8.ValidString(value)
	}
	for _, value := range []string{context.DeliveryID, context.BindingID, context.Epoch, context.InstanceID, context.CanonicalSourceItemID, context.OriginatingRequestID, context.FrontierID} {
		if !validIdentity(value) {
			return fmt.Errorf("invalid voice presentation identity")
		}
	}
	if len(context.GeneratedTranscript) > VoicePresentationMaxTranscriptBytes || !utf8.ValidString(context.GeneratedTranscript) {
		return fmt.Errorf("voice presentation transcript exceeds UTF-8 bounds")
	}
	if len(context.Items) > VoicePresentationMaxItems {
		return fmt.Errorf("voice presentation has too many items")
	}
	seen := make(map[[2]string]bool, len(context.Items))
	for _, item := range context.Items {
		key := [2]string{item.ResponseID, item.ItemID}
		if !validIdentity(item.ResponseID) || !validIdentity(item.ItemID) || seen[key] {
			return fmt.Errorf("invalid or duplicate voice presentation item identity")
		}
		seen[key] = true
		if item.PlayedSamples < 0 || item.QueuedSamples < item.PlayedSamples || item.GeneratedSamples < item.QueuedSamples {
			return fmt.Errorf("invalid voice presentation sample counters")
		}
	}
	data, err := json.Marshal(context)
	if err != nil {
		return err
	}
	if len(data) > VoicePresentationMaxBytes {
		return fmt.Errorf("voice presentation exceeds serialized bound")
	}
	return nil
}

// ValidateVoicePresentationInput validates the attachment's ingress, author and
// instance. Unsupported ingress must reject evidence, never discard it.
func ValidateVoicePresentationInput(items []InputItem, method, actorKind, instanceID string) error {
	_, err := NormalizeVoicePresentationInput(items, method, actorKind, instanceID)
	return err
}

// NormalizeVoicePresentationInput validates voice ingress before normalizing
// mutation input. The returned input owns its copy of the presentation evidence.
func NormalizeVoicePresentationInput(items []InputItem, method, actorKind, instanceID string) (MutationInput, error) {
	for _, item := range items {
		if item.VoicePresentation == nil {
			continue
		}
		if method != "turn/start" && method != "turn/steer" {
			return MutationInput{}, fmt.Errorf("voice presentation unsupported on %s", method)
		}
		if actorKind != "human" {
			return MutationInput{}, fmt.Errorf("voice presentation requires human input")
		}
		if item.VoicePresentation.InstanceID != instanceID {
			return MutationInput{}, fmt.Errorf("voice presentation instance mismatch")
		}
	}
	return NormalizeMutationInput(items)
}
