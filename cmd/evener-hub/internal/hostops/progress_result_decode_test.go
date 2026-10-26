package hostops

import (
	"encoding/json"
	"testing"
)

// TestRecordAndTombstoneDecodeProgressAndResultAlike pins that a record and a
// tombstone read the persisted progress list and terminal result under one rule
// set: the writer's shapes load, and each shape it never produces is refused.
func TestRecordAndTombstoneDecodeProgressAndResultAlike(t *testing.T) {
	const base = `"id":"00000000000000000001","host":"h1","kind":"deploy","state":"complete","generation":7,"hostRemoved":false`
	cases := []struct {
		name    string
		fields  string
		wantErr bool
	}{
		{"neither present", ``, false},
		{"progress and result", `,"progress":[{"ts":"2026-09-26T00:00:00Z","message":"step"}],"result":{"ok":false,"message":"failed"}`, false},
		{"null progress", `,"progress":null`, true},
		{"unparseable progress", `,"progress":{"ts":1}`, true},
		{"progress with an unknown field", `,"progress":[{"ts":"2026-09-26T00:00:00Z","message":"x","extra":1}]`, true},
		{"null result", `,"result":null`, true},
		{"unparseable result", `,"result":[1]`, true},
		{"result with an unknown field", `,"result":{"ok":true,"message":"x","extra":1}`, true},
		{"result without an outcome", `,"result":{"message":"failed"}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var rec recordFile
			if err := json.Unmarshal([]byte(`{`+base+tc.fields+`}`), &rec); err != nil {
				t.Fatalf("unmarshal record: %v", err)
			}
			record, recordErr := rec.record()
			var tomb tombstoneFile
			if err := json.Unmarshal([]byte(`{`+base+tc.fields+`}`), &tomb); err != nil {
				t.Fatalf("unmarshal tombstone: %v", err)
			}
			tombstone, tombstoneErr := tomb.tombstone()
			if (recordErr != nil) != tc.wantErr || (tombstoneErr != nil) != tc.wantErr {
				t.Fatalf("record err = %v, tombstone err = %v, want error: %v", recordErr, tombstoneErr, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if len(record.Progress) != len(tombstone.Progress) || (record.Result == nil) != (tombstone.Result == nil) {
				t.Fatalf("record %+v and tombstone %+v decoded differently", record, tombstone)
			}
			if record.Result != nil && *record.Result != *tombstone.Result {
				t.Fatalf("results differ: %+v vs %+v", record.Result, tombstone.Result)
			}
		})
	}
}
