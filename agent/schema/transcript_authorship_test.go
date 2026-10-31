package schema

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestTranscriptAuthorshipClone(t *testing.T) {
	t.Parallel()
	var absent *TranscriptAuthorship
	if absent.Clone() != nil {
		t.Fatal("nil clone became known")
	}
	source := &TranscriptAuthorship{Author: TranscriptAuthor{Kind: TranscriptAuthorAgent, SessionID: "session", OwnerSessionID: "owner", DelegateID: "delegate", DelegateName: "Name"}, Inherited: true}
	clone := source.Clone()
	if clone == source || !reflect.DeepEqual(clone, source) {
		t.Fatal("clone not independent and equal")
	}
	clone.Author.DelegateName = "Changed"
	if source.Author.DelegateName != "Name" {
		t.Fatal("clone aliases source")
	}
	b, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	var decoded TranscriptAuthorship
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(&decoded, source) {
		t.Fatal("roundtrip lost metadata")
	}
}
