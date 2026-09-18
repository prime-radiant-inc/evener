package interactiveartifacts

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStrictJSON(t *testing.T) {
	tests := []struct {
		name, input, canonical string
		valid                  bool
	}{
		{"ordered objects", ` {"z":[2,1],"a":{"y":1.0,"x":-0}} `, `{"a":{"x":-0,"y":1.0},"z":[2,1]}`, true},
		{"nested duplicate", `{"state":{"x":1,"x":2}}`, "", false},
		{"escaped duplicate", `{"x":1,"\u0078":2}`, "", false},
		{"array duplicate", `[{"x":1,"x":2}]`, "", false},
		{"trailing document", `{} {}`, "", false},
		{"malformed number", `{"x":01}`, "", false},
		{"invalid UTF8", string([]byte{'"', 0xff, '"'}), "", false},
		{"64 containers", strings.Repeat("[", 64) + "0" + strings.Repeat("]", 64), strings.Repeat("[", 64) + "0" + strings.Repeat("]", 64), true},
		{"65 containers", strings.Repeat("[", 65) + "0" + strings.Repeat("]", 65), "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, err := ParseJSON([]byte(tt.input), MaxRequestBytes)
			if !tt.valid {
				if err == nil {
					t.Fatal("accepted invalid JSON")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := CanonicalJSON(value)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.canonical {
				t.Fatalf("canonical = %s, want %s", got, tt.canonical)
			}
		})
	}
	if _, err := ParseJSON([]byte(`{"x":0}`), 6); err == nil {
		t.Fatal("accepted oversized JSON")
	}
}

func TestStateNumericDomain(t *testing.T) {
	for _, tt := range []struct {
		token string
		valid bool
	}{
		{"0", true}, {"-0", true}, {"1.0", true}, {"1e0", true}, {"0.1", true}, {"9007199254740991", true}, {"-9007199254740991", true}, {"5e-324", true},
		{"1e400", false}, {"-1e400", false}, {"1e-400", false}, {"9007199254740993", false}, {"-9007199254740993", false}, {"9007199254740992.0", false}, {"9.007199254740993e15", false}, {"9007199254740991.1", true},
	} {
		t.Run(tt.token, func(t *testing.T) {
			got, err := ValidateState([]byte(`{"n":` + tt.token + `}`))
			if (err == nil) != tt.valid {
				t.Fatalf("valid=%v, error=%v", tt.valid, err)
			}
			if tt.valid {
				var state map[string]json.RawMessage
				if err := json.Unmarshal(got, &state); err != nil {
					t.Fatal(err)
				}
				if string(state["n"]) != tt.token {
					t.Fatalf("numeric representation changed to %s", state["n"])
				}
			}
		})
	}
	for _, raw := range []string{`[]`, `null`, `{"x":{"n":1e400}}`, `{"x":[9007199254740993]}`, `{"x":"` + strings.Repeat("a", MaxStateBytes) + `"}`} {
		if _, err := ValidateState([]byte(raw)); err == nil {
			t.Fatal("accepted invalid state")
		}
	}
}

func TestFingerprintSemanticIdentity(t *testing.T) {
	base := `{"mutationId":"M1","state":{"x":1,"y":["a","c"]},"expectedSourceRevision":1,"expectedStateVersion":1}`
	want, err := Fingerprint("N", []byte(base))
	if err != nil {
		t.Fatal(err)
	}
	same := `{"expectedStateVersion":1,"state":{"y":["a","c"],"x":1},"mutationId":"M2","expectedSourceRevision":1}`
	got, err := Fingerprint("N", []byte(same))
	if err != nil || got != want {
		t.Fatalf("object order/mutationId changed fingerprint: %v", err)
	}
	for _, raw := range []string{
		strings.Replace(base, `["a","c"]`, `["c","a"]`, 1),
		strings.Replace(base, `"x":1`, `"x":1.0`, 1),
		strings.Replace(base, `"x":1`, `"x":1e0`, 1),
		strings.Replace(base, `"x":1`, `"x":-0`, 1),
		strings.Replace(base, `"expectedStateVersion":1`, `"expectedStateVersion":2`, 1),
		strings.Replace(base, `"state":`, `"optional":null,"state":`, 1),
	} {
		got, err := Fingerprint("N", []byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if got == want {
			t.Fatalf("semantic change reused fingerprint for %s", raw)
		}
	}
	other, err := Fingerprint("other", []byte(base))
	if err != nil || other == want {
		t.Fatal("namespace omitted from fingerprint")
	}
	a, _ := Fingerprint("N", []byte(`{"html":"<h1>é</h1>\r\n"}`))
	b, _ := Fingerprint("N", []byte(`{"html":"<h1>\u00e9</h1>\r\n"}`))
	c, _ := Fingerprint("N", []byte(`{"html":"<h1>é</h1>\n"}`))
	if a != b || a == c {
		t.Fatal("authored decoded UTF8 byte identity lost")
	}
	zero, _ := Fingerprint("N", []byte(`{"state":{"n":0}}`))
	negZero, _ := Fingerprint("N", []byte(`{"state":{"n":-0}}`))
	if zero == negZero {
		t.Fatal("negative zero normalized")
	}
	if _, err := Fingerprint("N", []byte(`{"state":{"x":1,"x":2}}`)); err == nil {
		t.Fatal("fingerprinted duplicate keys")
	}
}

func TestNumericValidationBoundsExponentWork(t *testing.T) {
	for _, token := range []string{"0e-1000000000", "0e1000000000", "-0e-1000000000", "0e100000000000000000000"} {
		state, err := ValidateState([]byte(`{"n":` + token + `}`))
		if err != nil {
			t.Fatalf("zero token %s rejected: %v", token, err)
		}
		if !strings.Contains(string(state), token) {
			t.Fatal("zero token spelling lost")
		}
	}
	for _, token := range []string{"0e-1000000000", "1e-1000000000", "1.0000000000000000000001", "9007199254740993"} {
		var version Version
		if err := json.Unmarshal([]byte(token), &version); err == nil {
			t.Fatalf("invalid version %s accepted", token)
		}
	}
	parsed, err := ParseRequest("artifact_get_view", []byte(`{"artifactId":"A","knownStateVersion":0e-1000000000}`), false)
	if err == nil {
		t.Fatalf("zero known version accepted: %v", parsed)
	}
}
