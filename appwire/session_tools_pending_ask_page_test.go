package appwire

import (
	"encoding/json"
	"testing"
)

func TestSessionOperationPendingAskPageValidation(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		`{"read":{"ref":"local:root","unexpectedField":true}}`,
		`{"read":{"ref":"local:root","offset":0}}`,
		`{"read":{"ref":"local:root","expectedAskRevision":0}}`,
		`{"read":{"expectedAskRevision":0,"expectedAskBootGeneration":"1"}}`,
		`{"read":{"ref":"local:root","expectedAskRevision":0,"expectedAskBootGeneration":"1","heldSnapshot":{"incarnation":"x","length":1}}}`,
		`{"read":{"ref":"local:root","expectedAskRevision":0,"expectedAskBootGeneration":"1","answerContinuation":{"ancestorTurnId":"a","candidateTurnId":"b"}}}`,
	} {
		var request SessionOperationRequest
		if err := json.Unmarshal([]byte(raw), &request); err == nil {
			t.Errorf("Manager admitted malformed page: %s", raw)
		}
	}
	for _, raw := range []string{
		`{"read":{"ref":"local:root"}}`,
		`{"read":{"ref":"local:root","expectedAskRevision":0,"expectedAskBootGeneration":"1","offset":0,"limit":0}}`,
		`{"read":{"ref":"local:root","expectedAskRevision":0,"expectedAskBootGeneration":"1","offset":-1,"limit":-1}}`,
	} {
		var request SessionOperationRequest
		if err := json.Unmarshal([]byte(raw), &request); err != nil {
			t.Errorf("valid Manager read refused: %v", err)
		}
	}
}
