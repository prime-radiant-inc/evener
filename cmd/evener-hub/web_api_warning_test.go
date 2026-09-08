package hub

import (
	"encoding/json"
	"testing"
)

// TestWarningPayloadRecoversMessageAndHintDespiteBogusCause pins the
// agreement between the hub's warningPayload and the TUI's own warning
// reader on the same wire frame: a cause that does not decode into
// appwire.DiagnosticCause must not cost the message or hint that json did
// successfully populate.
func TestWarningPayloadRecoversMessageAndHintDespiteBogusCause(t *testing.T) {
	raw := json.RawMessage(`{"message":"real message","cause":"bogus-not-an-object","hint":"retry"}`)

	payload := warningPayload(raw)

	if got, _ := payload["message"].(string); got != "real message" {
		t.Fatalf("payload[message] = %v, want %q", payload["message"], "real message")
	}
	if got, _ := payload["hint"].(string); got != "retry" {
		t.Fatalf("payload[hint] = %v, want %q", payload["hint"], "retry")
	}
}
