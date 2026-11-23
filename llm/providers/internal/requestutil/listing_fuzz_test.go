package requestutil

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"testing"
)

// FuzzListedReasoningDecode pins the tolerant decode contract of the listed
// facts: for any valid JSON value neither unmarshaler errors, a boolean
// verdict is the fact it spells and carries nothing else, an object's facts
// decode exactly as encoding/json would decode them natively, and a levels
// list accepts plain strings and effort objects while any other shape states
// nothing. Syntax errors are the envelope's business, not the field's.
func FuzzListedReasoningDecode(f *testing.F) {
	f.Add([]byte(`{"mandatory":true,"supported_efforts":["low","high"]}`))
	f.Add([]byte(`{"default_enabled":false}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`true`))
	f.Add([]byte(`false`))
	f.Add([]byte(`{"mandatory":"yes"}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`"always"`))
	f.Add([]byte(`5`))
	f.Add([]byte(`[]`))
	f.Add([]byte(`["low",{"effort":"high"},5,null]`))
	f.Add([]byte(`{"effort":"high"}`))

	f.Fuzz(func(t *testing.T, b []byte) {
		if !json.Valid(b) {
			return
		}
		var r ListedReasoning
		if err := json.Unmarshal(b, &r); err != nil {
			t.Fatalf("the reasoning field must swallow any shape: %v", err)
		}
		// json.Unmarshal(null, &bool) succeeds as a no-op, so a null is
		// not a boolean verdict even though it decodes into one — the same
		// trap the unmarshaler's own null guard exists for.
		var verdict bool
		if string(bytes.TrimSpace(b)) != "null" && json.Unmarshal(b, &verdict) == nil {
			if r.Verdict == nil || *r.Verdict != verdict {
				t.Fatalf("verdict %v must read the boolean %s", r.Verdict, b)
			}
			if r.Mandatory != nil || r.DefaultEnabled != nil || r.SupportedEfforts != nil {
				t.Fatalf("a boolean verdict carries no object facts: %+v", r)
			}
		} else {
			if r.Verdict != nil {
				t.Fatalf("a non-boolean shape must set no verdict: %+v", r)
			}
			var plain struct {
				Mandatory        *bool    `json:"mandatory"`
				DefaultEnabled   *bool    `json:"default_enabled"`
				SupportedEfforts []string `json:"supported_efforts"`
			}
			if json.Unmarshal(b, &plain) == nil &&
				(!reflect.DeepEqual(r.Mandatory, plain.Mandatory) ||
					!reflect.DeepEqual(r.DefaultEnabled, plain.DefaultEnabled) ||
					!slices.Equal(r.SupportedEfforts, plain.SupportedEfforts)) {
				t.Fatalf("object facts must decode as encoding/json would: %+v vs %+v", r, plain)
			}
		}
		var levels ListedReasoningLevels
		if err := json.Unmarshal(b, &levels); err != nil {
			t.Fatalf("the levels list must swallow any shape: %v", err)
		}
		var plainStrings []string
		if json.Unmarshal(b, &plainStrings) == nil && !slices.Equal(levels, plainStrings) {
			t.Fatalf("levels = %v, want the plain strings %v", levels, plainStrings)
		}
		var plainObjs []struct {
			Effort string `json:"effort"`
		}
		if json.Unmarshal(b, &plainObjs) == nil {
			want := make([]string, len(plainObjs))
			for i, o := range plainObjs {
				want[i] = o.Effort
			}
			if !slices.Equal(levels, want) {
				t.Fatalf("levels = %v, want the effort objects %v", levels, want)
			}
		}
		rows, firstErr := DecodeListingRows[struct {
			ID string `json:"id"`
		}]([]json.RawMessage{b})
		if firstErr != nil && len(rows) != 0 {
			t.Fatalf("an undecodable row must yield no row: %v", firstErr)
		}
	})
}
