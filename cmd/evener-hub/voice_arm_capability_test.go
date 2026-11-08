package hub

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// The hub owns voiceArm, so a thread's read and its status frames answer the
// same, and an older hub's silence is preserved as absent rather than false.
func TestVoiceArmCapabilityFollowsTheHubAnswer(t *testing.T) {
	frame := func(capabilities string) appwire.Notification {
		return appwire.Notification{
			Method: appwire.NotifyThreadStatusChanged,
			Params: json.RawMessage(`{"threadId":"t1","status":{"type":"idle"},"capabilities":` + capabilities + `}`),
		}
	}
	decode := func(n appwire.Notification) map[string]json.RawMessage {
		var params struct {
			Capabilities map[string]json.RawMessage `json:"capabilities"`
		}
		if err := json.Unmarshal(n.Params, &params); err != nil {
			t.Fatal(err)
		}
		return params.Capabilities
	}
	if caps := decode(stampVoiceArmCapability(frame(`{"send":true}`), true)); string(caps["voiceArm"]) != "true" || string(caps["send"]) != "true" {
		t.Fatalf("stamped capabilities = %v, want voiceArm beside the daemon's send", caps)
	}
	if caps := decode(stampVoiceArmCapability(frame(`{"send":true,"voiceArm":true}`), false)); string(caps["voiceArm"]) != "false" {
		t.Fatalf("stamped capabilities = %v, want a stale voiceArm replaced with false", caps)
	}
	other := appwire.Notification{Method: appwire.NotifyThreadNameChanged, Params: json.RawMessage(`{"threadId":"t1"}`)}
	if got := stampVoiceArmCapability(other, true); string(got.Params) != string(other.Params) {
		t.Fatalf("a non-status frame changed: %s", got.Params)
	}
}

// A session that cannot arm reads false, and a hub without an OpenAI voice
// instance reports false for every session.
func TestHubVoiceArmReportsSessionAndProvider(t *testing.T) {
	armable := appwire.EvenerThread{
		InstanceID: "i1", Manager: true, MutationStateAuthoritative: true,
		Capabilities:           appwire.ThreadCapabilities{VoicePresentationContextV1: true},
		ConversationProvenance: &appwire.ConversationProvenanceState{Version: 1, Authoritative: true, InstanceID: "i1"},
	}
	// A hub with no provider registry cannot run voice.
	if got := hubVoiceArm(hubcore.WebConfig{}, appwire.Thread{Evener: armable}); got {
		t.Fatalf("voiceArm = true without a voice instance")
	}
	notLive := armable
	notLive.MutationStateAuthoritative = false
	if hubVoiceArm(hubcore.WebConfig{}, appwire.Thread{Evener: notLive}) {
		t.Fatalf("voiceArm = true for a session with no live manager")
	}
	oldProvenance := armable
	oldProvenance.ConversationProvenance = &appwire.ConversationProvenanceState{Version: 1, InstanceID: "i1"}
	if hubVoiceArm(hubcore.WebConfig{}, appwire.Thread{Evener: oldProvenance}) {
		t.Fatalf("voiceArm = true for a non-authoritative provenance")
	}
}
