package openai

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
)

// FuzzResponsesRequestFingerprint drives requestFingerprintForResponsesBody, the
// pure hash that stamps a Responses continuation so a later turn can detect that
// the request shape changed underneath a previous_response_id. Its contract: the
// fingerprint must be STABLE across fields that legitimately vary between
// continuation turns (input/previous_response_id/conversation, plus store on the
// public endpoint) yet reflect every other field.
//
// Oracles beyond no-panic:
//   - format: a successful fingerprint is always "cont-req-v1:" + valid RawURL
//     base64;
//   - determinism: identical bodies fingerprint identically;
//   - exclusion invariance: mutating ONLY an excluded field must not change the
//     fingerprint (a regression that lets input/conversation leak in would break
//     continuation reuse on every turn);
//   - sensitivity: changing a NON-excluded field DOES change the fingerprint.
func FuzzResponsesRequestFingerprint(f *testing.F) {
	f.Add([]byte(`{"model":"gpt-5","input":[{"role":"user"}],"temperature":0.5}`), uint8(0))
	f.Add([]byte(`{"store":true,"previous_response_id":"resp_1","tools":[]}`), uint8(1))
	f.Add([]byte(`{}`), uint8(0))
	f.Add([]byte(`not json`), uint8(1))
	f.Add([]byte(`{"conversation":"c1","model":"m"}`), uint8(0))

	families := []llm.ResponsesEndpointFamily{
		llm.ResponsesEndpointFamilyOpenAIPublic,
		llm.ResponsesEndpointFamilyOpenAICodex,
	}

	f.Fuzz(func(t *testing.T, bodyBytes []byte, famSel uint8) {
		var body map[string]any
		if err := json.Unmarshal(bodyBytes, &body); err != nil || body == nil {
			return // the helper only ever receives an already-decoded body map.
		}
		family := families[int(famSel)%len(families)]

		fp, err := requestFingerprintForResponsesBody(family, body)
		if err != nil {
			return // an unmarshalable filtered body is an honest structured error.
		}

		const prefix = "cont-req-v1:"
		if !strings.HasPrefix(fp, prefix) {
			t.Fatalf("fingerprint %q missing prefix %q", fp, prefix)
		}
		if _, derr := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(fp, prefix)); derr != nil {
			t.Fatalf("fingerprint suffix is not RawURL base64: %v (fp=%q)", derr, fp)
		}

		if again, _ := requestFingerprintForResponsesBody(family, body); again != fp {
			t.Fatalf("fingerprint not deterministic: %q vs %q", fp, again)
		}

		// Exclusion invariance: overwrite each excluded field with a sentinel and
		// confirm the fingerprint is unchanged.
		excluded := responsesRequestFingerprintExcludedFields(family)
		mutated := cloneStringMap(body)
		for k := range excluded {
			mutated[k] = "SENTINEL_EXCLUDED_VALUE"
		}
		if got, gerr := requestFingerprintForResponsesBody(family, mutated); gerr == nil && got != fp {
			t.Fatalf("excluded-field mutation changed fingerprint: %q -> %q (excluded=%v)", fp, got, excluded)
		}

		// Sensitivity: a non-excluded field that we add must change the fingerprint.
		sensitive := cloneStringMap(body)
		key := "fuzz_probe_field"
		for excluded[key] { // never collide with an excluded key
			key += "_x"
		}
		sensitive[key] = "probe-value-12345"
		if got, gerr := requestFingerprintForResponsesBody(family, sensitive); gerr == nil && got == fp {
			t.Fatalf("adding non-excluded field %q did not change fingerprint %q", key, fp)
		}
	})
}

func cloneStringMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
