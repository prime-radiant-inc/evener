package transcriptindex

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

// TestParseItemKeyRoundTripsItemKey pins ParseItemKey as ItemKey's inverse for
// both position shapes: an entry item (Entry >= 1, keyed by the turn id and
// entry ordinal) and a header prelude item (Entry == 0, keyed by "prelude:
// header" alone, no turn id).
func TestParseItemKeyRoundTripsItemKey(t *testing.T) {
	entryPos := appwire.ThreadItemPosition{Entry: 5, Item: 2}
	key := ItemKey("turn_4", entryPos)
	turnID, pos, err := ParseItemKey(key)
	if err != nil {
		t.Fatalf("ParseItemKey(%q): %v", key, err)
	}
	if turnID != "turn_4" || pos != entryPos {
		t.Fatalf("ParseItemKey(%q) = (%q, %+v), want (%q, %+v)", key, turnID, pos, "turn_4", entryPos)
	}

	headerPos := appwire.ThreadItemPosition{Entry: 0, Item: 3}
	headerKey := ItemKey("turn_4", headerPos)
	turnID, pos, err = ParseItemKey(headerKey)
	if err != nil {
		t.Fatalf("ParseItemKey(%q): %v", headerKey, err)
	}
	if turnID != "" || pos != headerPos {
		t.Fatalf("ParseItemKey(%q) = (%q, %+v), want (%q, %+v)", headerKey, turnID, pos, "", headerPos)
	}
}

// TestParseItemKeyRejectsMalformedKeys pins the parser's refusals: anything
// that isn't exactly the shape ItemKey produces, including a wrong version
// marker, missing segments, and non-numeric ordinals/parts.
func TestParseItemKeyRejectsMalformedKeys(t *testing.T) {
	for _, key := range []string{
		"",
		"not-a-key-at-all",
		"apptranscript-item-v1:turn_4:4:0",  // wrong version
		"apptranscript-item-v2:turn_4:4",    // missing part
		"apptranscript-item-v2:turn_4",      // missing ordinal and part
		"apptranscript-item-v2::4:0",        // empty turn id
		"apptranscript-item-v2:turn_4:x:0",  // non-numeric ordinal
		"apptranscript-item-v2:turn_4:4:x",  // non-numeric part
		"apptranscript-item-v2:turn_4:-1:0", // negative ordinal
		"apptranscript-item-v2:prelude:header:x",
		"apptranscript-item-v2:prelude:header:-1",
		"apptranscript-item-v2:turn_4:007:0",                 // leading zero, non-canonical
		"apptranscript-item-v2:turn_4:4:007",                 // leading zero, non-canonical
		"apptranscript-item-v2:prelude:header:007",           // leading zero, non-canonical
		"apptranscript-item-v2:turn_4:4:4294967296",          // part exceeds MaxUint32
		"apptranscript-item-v2:prelude:header:4294967296",    // header part exceeds MaxUint32
		"apptranscript-item-v2:turn_4:9223372036854775808:0", // ordinal at 2^63 would wrap Entry (ordinal+1) past uint64
	} {
		if _, _, err := ParseItemKey(key); err == nil {
			t.Fatalf("ParseItemKey(%q) succeeded, want a malformed-key error", key)
		}
	}
}
