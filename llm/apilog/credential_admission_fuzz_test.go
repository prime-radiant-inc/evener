//go:build evenerfuzz

package apilog

import (
	"strings"
	"testing"
)

// FuzzAPILogCredentialAdmission drives MarshalRecord's last line of defence: a
// record whose provider-derived fields still contain credential material must
// not reach disk. api.jsonl is kept as forensic evidence and read by humans and
// tools alike, so a credential written into it is a leak that outlives the
// session.
//
// The existing unit tests pin two hand-picked placements. A fuzzer covers the
// cross product instead: any secret, in any of the fields validateProviderEvidence
// inspects. That matters because the list of inspected fields is maintained by
// hand — a field added to the record and forgotten here is exactly the gap this
// finds.
//
// The oracle is careful not to pass for the wrong reason. An arbitrary string
// dropped into a field can make the record invalid on unrelated grounds, and
// MarshalRecord would then refuse for that reason instead. So each case first
// proves the SAME record marshals cleanly with no forbidden binding, and then
// requires the refusal to name credential admission specifically.
func FuzzAPILogCredentialAdmission(f *testing.F) {
	f.Add("sk-secret-value", uint8(0))
	f.Add("1", uint8(1))
	f.Add("token", uint8(4))
	f.Add("https://leak", uint8(3))

	f.Fuzz(func(t *testing.T, secret string, field uint8) {
		// An empty or whitespace secret matches everything or nothing; neither
		// says anything about the admission rule.
		if strings.TrimSpace(secret) == "" || len(secret) > 512 {
			t.Skip()
		}

		place := func(r APIAttemptRecord) (APIAttemptRecord, string) {
			switch field % 8 {
			case 0:
				r.ProviderInstance = secret
				return r, "provider instance"
			case 1:
				r.RequestModel = secret
				return r, "request model"
			case 2:
				r.Request.Endpoint = secret
				return r, "request endpoint"
			case 3:
				r.Request.Model = secret
				return r, "request body model"
			case 4:
				r.Request.HistoryMode = secret
				return r, "request history mode"
			case 5:
				r.Request.EndpointFamily = secret
				return r, "request endpoint family"
			case 6:
				r.ErrorClass = secret
				return r, "error class"
			default:
				r.ErrorMessage = secret
				return r, "error message"
			}
		}

		clean, where := place(validAPIAttemptRecord(t))
		if _, err := MarshalRecord(clean); err != nil {
			// This input makes the record invalid for reasons unrelated to
			// credentials, so a refusal below would prove nothing.
			t.Skip()
		}

		bound, _ := place(validAPIAttemptRecord(t))
		bound = bound.WithForbiddenProviderEvidence([]string{secret}, nil)
		_, err := MarshalRecord(bound)
		if err == nil {
			t.Fatalf("MarshalRecord wrote a record with the forbidden value %q in its %s", secret, where)
		}
		if !strings.Contains(err.Error(), "credential admission") {
			t.Fatalf("MarshalRecord refused %q in %s for the wrong reason: %v", secret, where, err)
		}
	})
}
