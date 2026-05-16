package launchconfig

import (
	"testing"
)

func TestCanonicalHash_StableAcrossWhitespace(t *testing.T) {
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

func TestCanonicalHash_DetectsSemanticChange(t *testing.T) {
	a := `model = "x"`
	b := `model = "y"`
	ha, _ := CanonicalHashTOML([]byte(a))
	hb, _ := CanonicalHashTOML([]byte(b))
	if ha == hb {
		t.Errorf("hashes should differ for different content")
	}
}

func TestComputeTrustState(t *testing.T) {
	// File absent.
	if got := ComputeTrustState("", Meta{}); got != TrustAbsent {
		t.Errorf("absent: got %q, want %q", got, TrustAbsent)
	}
	// File present, no recorded decision → untrusted.
	if got := ComputeTrustState("hash-1", Meta{}); got != TrustUntrusted {
		t.Errorf("untrusted: got %q, want %q", got, TrustUntrusted)
	}
	// File present, hash matches trusted record.
	meta := Meta{Trust: MetaTrust{Hash: "hash-1", Decision: "trusted"}}
	if got := ComputeTrustState("hash-1", meta); got != TrustTrusted {
		t.Errorf("trusted: got %q, want %q", got, TrustTrusted)
	}
	// File present, hash differs → changed.
	if got := ComputeTrustState("hash-2", meta); got != TrustChanged {
		t.Errorf("changed: got %q, want %q", got, TrustChanged)
	}
	// File present, explicitly rejected.
	rejected := Meta{Trust: MetaTrust{Hash: "hash-1", Decision: "rejected"}}
	if got := ComputeTrustState("hash-1", rejected); got != TrustRejected {
		t.Errorf("rejected: got %q, want %q", got, TrustRejected)
	}
}
