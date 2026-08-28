package launchconfig

import (
	"testing"
)

func checkCanonicalHash_StableAcrossWhitespace(t *testing.T) {
	a := `model = "x"
skills_dirs = ["/a"]
`
	b := `

model = "x"


skills_dirs = ["/a"]
`
	ha, err := CanonicalHashTOML([]byte(a))
	if err != nil {
		t.Fatal(err)
	}
	hb, err := CanonicalHashTOML([]byte(b))
	if err != nil {
		t.Fatal(err)
	}
	if ha != hb {
		t.Errorf("hash should be whitespace-stable: %q vs %q", ha, hb)
	}
}

func checkCanonicalHash_DetectsSemanticChange(t *testing.T) {
	a := `model = "x"`
	b := `model = "y"`
	ha, _ := CanonicalHashTOML([]byte(a))
	hb, _ := CanonicalHashTOML([]byte(b))
	if ha == hb {
		t.Errorf("hashes should differ for different content")
	}
}

func checkCanonicalHash_DistinguishesExplicitEmptyModelFallbacks(t *testing.T) {
	absent := `model = "openai/gpt-5.2"`
	explicitEmpty := `model = "openai/gpt-5.2"
model_fallbacks = []
`
	ha, err := CanonicalHashTOML([]byte(absent))
	if err != nil {
		t.Fatal(err)
	}
	hb, err := CanonicalHashTOML([]byte(explicitEmpty))
	if err != nil {
		t.Fatal(err)
	}
	if ha == hb {
		t.Fatalf("hashes should differ when model_fallbacks is explicitly empty: %q", ha)
	}
}

func checkComputeTrustState(t *testing.T) {
	// File absent.
	if got := ComputeTrustState("", Meta{}); got != TrustAbsent {
		t.Errorf("absent: got %q, want %q", got, TrustAbsent)
	}
	// File present, no recorded decision → untrusted.
	if got := ComputeTrustState("hash-1", Meta{}); got != TrustUntrusted {
		t.Errorf("untrusted: got %q, want %q", got, TrustUntrusted)
	}
	// File present, hash matches trusted record.
	meta := Meta{Trust: MetaTrust{Hashes: []string{"hash-1"}, Decision: "trusted"}}
	if got := ComputeTrustState("hash-1", meta); got != TrustTrusted {
		t.Errorf("trusted: got %q, want %q", got, TrustTrusted)
	}
	// File present, hash differs from recorded hash → changed.
	if got := ComputeTrustState("hash-2", meta); got != TrustChanged {
		t.Errorf("changed: got %q, want %q", got, TrustChanged)
	}
	// File present, explicitly rejected.
	rejected := Meta{Trust: MetaTrust{Hashes: []string{"hash-1"}, Decision: "rejected"}}
	if got := ComputeTrustState("hash-1", rejected); got != TrustRejected {
		t.Errorf("rejected: got %q, want %q", got, TrustRejected)
	}

	// Multi-hash set: hash-1 and hash-2 both trusted.
	multiTrusted := Meta{Trust: MetaTrust{Hashes: []string{"hash-1", "hash-2"}, Decision: "trusted"}}
	if got := ComputeTrustState("hash-1", multiTrusted); got != TrustTrusted {
		t.Errorf("multi trusted hash-1: got %q, want %q", got, TrustTrusted)
	}
	if got := ComputeTrustState("hash-2", multiTrusted); got != TrustTrusted {
		t.Errorf("multi trusted hash-2: got %q, want %q", got, TrustTrusted)
	}
	// Hash not in the set → changed (prompt again).
	if got := ComputeTrustState("hash-3", multiTrusted); got != TrustChanged {
		t.Errorf("multi changed hash-3: got %q, want %q", got, TrustChanged)
	}

	// TrustHashSet and HashInSet helpers.
	trust := MetaTrust{Hashes: []string{"h-1", "h-2"}, Decision: "trusted"}
	set := TrustHashSet(trust)
	if !HashInSet("h-1", set) {
		t.Error("HashInSet: h-1 should be in set")
	}
	if HashInSet("h-unknown", set) {
		t.Error("HashInSet: h-unknown should not be in set")
	}
}
