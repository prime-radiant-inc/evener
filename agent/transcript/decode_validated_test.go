package transcript

import (
	"reflect"
	"testing"

	"primeradiant.com/evener/llm"
)

// TestDecodeValidatedEntryMatchesDecodeEntry: for bytes DecodeEntry accepts,
// the validated decode yields the same entry, pre-flag machinery inference
// included.
func TestDecodeValidatedEntryMatchesDecodeEntry(t *testing.T) {
	for _, entry := range []Entry{
		{Kind: "entry", Seq: 1, MachineryFlagged: true, Turn: entryTurn(llm.ContentPart{Kind: llm.ContentText, Text: machineryBlock})},
		{Kind: "entry", Seq: 2, Turn: entryTurn(llm.ContentPart{Kind: llm.ContentText, Text: machineryBlock})},
		{Kind: "entry", Seq: 3, MachineryFlagged: true, Turn: entryTurn(llm.ContentPart{Kind: llm.ContentText, Text: "plain", Machinery: true})},
	} {
		line := marshalEntryLine(t, entry)
		strict, err := DecodeEntry(line)
		if err != nil {
			t.Fatalf("DecodeEntry: %v", err)
		}
		validated, err := DecodeValidatedEntry(line)
		if err != nil {
			t.Fatalf("DecodeValidatedEntry: %v", err)
		}
		if !reflect.DeepEqual(validated, strict) {
			t.Fatalf("seq %d: validated decode %+v, strict decode %+v", entry.Seq, validated, strict)
		}
	}
	if _, err := DecodeValidatedEntry([]byte("{not json")); err == nil {
		t.Fatal("DecodeValidatedEntry accepted malformed JSON")
	}
}
