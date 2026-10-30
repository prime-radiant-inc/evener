package hubapi

import (
	"encoding/json"
	"testing"
)

// FuzzNavigationRecordDecode drives the strict navigation record decoders over
// arbitrary bytes. The floor is no panic; any record that decodes must encode
// again, since the hub re-serves decoded records.
func FuzzNavigationRecordDecode(f *testing.F) {
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"id":"local:a","unknown":1}`))
	f.Add([]byte(`{"id":"x"} {"id":"y"}`))
	f.Add([]byte(`null`))
	f.Add([]byte(``))

	f.Fuzz(func(t *testing.T, raw []byte) {
		var record NavigationEntityRecord
		if err := json.Unmarshal(raw, &record); err == nil {
			if _, err := json.Marshal(record); err != nil {
				t.Fatalf("decoded entity record does not re-encode: %v", err)
			}
		}
		var container NavigationOrderContainer
		if err := json.Unmarshal(raw, &container); err == nil {
			if _, err := json.Marshal(container); err != nil {
				t.Fatalf("decoded order container does not re-encode: %v", err)
			}
		}
	})
}
