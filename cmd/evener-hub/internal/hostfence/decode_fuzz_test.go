package hostfence

import (
	"encoding/json"
	"errors"
	"testing"
)

// FuzzHostfenceDecode drives the trust-boundary decoders with arbitrary bytes:
// the helper's responses arrive over a remote link, so a value outside the
// schema must be refused, never half-understood into an authorization. Every
// accepted value must satisfy the same validation the verifiers apply.
func FuzzHostfenceDecode(f *testing.F) {
	seeds := []string{
		`{"version":1,"guardEpoch":4,"epoch":{"bootId":"b1","opSeq":3},"fence":null,"superseded":null,"holder":{"bootId":"b1","opSeq":3},"entries":1}`,
		`{"version":1,"guardEpoch":5,"epoch":{"bootId":"b1","opSeq":3},"fence":{"epoch":{"bootId":"b1","opSeq":4},"superseded":{"bootId":"b1","opSeq":3},"guardEpoch":5},"superseded":null,"holder":{"bootId":"b1","opSeq":4},"entries":1}`,
		`{"version":1,"entries":[{"id":"n1","command":"deploy --now","registeredAt":"2026-09-28T10:00:00Z","ownership":{"pid":41,"pidStartTime":"777"},"state":"running"}]}`,
		`{"version":1,"entries":[{"id":"n2","command":"restart","registeredAt":"2026-09-28T09:00:00Z","ownership":{"nonce":"n2"},"state":"exited","exit":0,"exitedAt":"2026-09-28T09:00:05Z"}]}`,
		`{"version":1,"id":"n1","live":true,"state":"running","ownership":{"pid":41,"pidStartTime":"777"}}`,
		`{"version":1,"id":"n1","signaled":true,"live":false,"state":"killed"}`,
		`{"version":1,"id":"n2","signaled":false,"live":false,"state":""}`,
		`{"version":1,"id":"n3","signaled":true,"live":true,"state":"running","remaining":[{"pid":41,"startToken":"777"}]}`,
		`{"version":1,"refused":true,"error":"stale-epoch","detail":"older"}`,
		`{"bootId":"b1","opSeq":3}`,
		`null`, ``, `[]`, `{`, `{"version":1}`,
	}
	for _, seed := range seeds {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if epoch, ok := EpochFromRaw(json.RawMessage(raw)); ok {
			if err := epoch.Validate(); err != nil {
				t.Fatalf("accepted epoch fails its own validation: %+v: %v", epoch, err)
			}
		}
		if status, err := DecodeStatus(raw); err == nil {
			if err := status.Guard().Validate(); err != nil {
				t.Fatalf("accepted status fails guard validation: %+v: %v", status, err)
			}
		}
		if entries, err := DecodeEntries(raw); err == nil {
			for _, entry := range entries {
				if err := entry.Validate(); err != nil {
					t.Fatalf("accepted entry fails validation: %+v: %v", entry, err)
				}
			}
		}
		if recheck, err := DecodeRecheck(raw); err == nil && recheck.ID == "" {
			t.Fatal("accepted recheck answer carries no id")
		}
		if report, err := DecodeKillReport(raw); err == nil {
			if report.ID == "" {
				t.Fatal("accepted kill answer carries no id")
			}
			if report.Live && report.State != LeaseRegistering && report.State != LeaseRunning {
				t.Fatalf("accepted kill answer reports state %q live", report.State)
			}
			for _, remaining := range report.Remaining {
				if err := remaining.Validate(); err != nil {
					t.Fatalf("accepted kill answer carries an invalid member: %+v: %v", remaining, err)
				}
			}
		}
		if err := DecodeRefusal(raw); err != nil {
			var refusal *HelperRefusalError
			if errors.As(err, &refusal) && refusal.Unwrap() == nil {
				t.Fatalf("accepted refusal carries no known reason: %+v", refusal)
			}
		}
	})
}
