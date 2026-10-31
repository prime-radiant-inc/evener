package appwire

import (
	"encoding/json"
	"testing"
)

func TestSessionToolsRemotePeerIdentityNotManagerOperation(t *testing.T) {
	var operation SessionOperationRequest
	for _, encoded := range []string{
		`{"identify":{"hubId":"asserted-hub"}}`,
		`{"read":{"ref":"local:worker"},"identify":{"hubId":"asserted-hub"}}`,
	} {
		if err := json.Unmarshal([]byte(encoded), &operation); err == nil {
			t.Fatalf("Manager tool input established peer identity: %s", encoded)
		}
	}
}
