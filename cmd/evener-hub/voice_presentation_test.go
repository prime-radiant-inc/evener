package hub

import (
	"context"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func TestVoicePresentationRejectsPublicInput(t *testing.T) {
	server := newHubAppServer(hubcore.WebConfig{}, appsource.NewRegistry())
	for _, method := range []string{appwire.MethodThreadStart, appwire.MethodTurnStart, appwire.MethodTurnSteer, appwire.MethodTurnQueue, appwire.MethodTurnDrainAsSteer} {
		t.Run(method, func(t *testing.T) {
			// The missing source makes dispatch past this boundary observable without
			// launching work. These are otherwise valid platform evidence fields.
			params := map[string]any{
				"ref": "missing:thread", "expectedInstanceId": "instance", "clientMutationId": "mutation",
				"input": []any{map[string]any{"type": "text", "text": "REFERENCE-149", "voicePresentation": map[string]any{
					"version": 1, "deliveryId": "delivery", "bindingId": "binding", "epoch": "epoch", "instanceId": "instance",
					"canonicalSourceItemId": "source", "originatingRequestId": "request", "frontierId": "frontier",
					"runGeneration": 1, "presentationGeneration": 1, "generatedTranscript": "SPEECH-151",
					"transcriptTruncated": false, "outputComplete": false, "interrupted": true, "sampleRateHz": 24000,
					"items": []any{map[string]any{"responseId": "response", "itemId": "item", "generatedSamples": 480, "queuedSamples": 480, "playedSamples": 240, "playbackComplete": false}},
				}}},
			}
			_, err := exactDispatch(context.Background(), t, server, method, params)
			wire, ok := wireErrorFromError(err)
			if !ok || wire.Code != appwire.CodeInvalidParams {
				t.Fatalf("public presentation reached session routing: %v", err)
			}
		})
	}
}

func TestVoicePresentationRequiresOriginalManagerCapability(t *testing.T) {
	t.Parallel()
	b := voiceBridge{binding: hubcore.VoiceBinding{Ref: "local:manager", InstanceID: "instance"}}
	read := appwire.ThreadReadResponse{Snapshot: &appwire.SnapshotIdentity{Incarnation: "history"}}
	read.Thread.Evener.Ref = b.binding.Ref
	read.Thread.Evener.InstanceID = b.binding.InstanceID
	read.Thread.Evener.Manager = true
	read.Thread.Evener.MutationStateAuthoritative = true
	read.Thread.Evener.ConversationProvenance = &appwire.ConversationProvenanceState{Version: 1, InstanceID: b.binding.InstanceID, Authoritative: true}
	if b.validSpeechRead(read) {
		t.Fatal("bridge accepted a manager that cannot preserve presentation context")
	}
	read.Thread.Evener.Capabilities.VoicePresentationContextV1 = true
	if !b.validSpeechRead(read) {
		t.Fatal("capable original manager was refused")
	}
	read.Thread.Evener.InstanceID = "replacement"
	if b.validSpeechRead(read) {
		t.Fatal("replacement instance inherited the binding")
	}
}
