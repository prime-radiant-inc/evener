package identifier

import (
	"bytes"
	"testing"
)

func TestJobIDCarriesCompleteOwnerAndRandomSuffix(t *testing.T) {
	const owner = "02wMz5TxvEMoJEDTDGOTil"
	id, err := newJobID(owner, bytes.NewReader(bytes.Repeat([]byte{0}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	if len(id) != 39 {
		t.Fatalf("len(%q) = %d, want 39", id, len(id))
	}
	if got, err := JobOwnerSessionID(id); err != nil || got != owner {
		t.Fatalf("owner = %q, err=%v", got, err)
	}
	if err := ValidateJobID(id); err != nil {
		t.Fatalf("ValidateJobID(%q): %v", id, err)
	}
}

func TestJobIDRejectsMalformedShapes(t *testing.T) {
	const owner = "02wMz5TxvEMoJEDTDGOTil"
	tests := map[string]string{
		"old job UUID payload": "job_02wMz5TxvEMoJEDTDGOTil",
		"truncated owner":      "job_02wMz5TxvEMoJEDTDGO_Til000000000000",
		"invalid owner":        "job_02wMz5TxvEMoJEDTDGOTi!_000000000000",
		"missing separator":    "job_" + owner + "000000000000",
		"short suffix":         "job_" + owner + "_00000000000",
		"long suffix":          "job_" + owner + "_0000000000000",
		"non-base62 suffix":    "job_" + owner + "_00000000000_",
	}
	for name, id := range tests {
		t.Run(name, func(t *testing.T) {
			if err := ValidateJobID(id); err == nil {
				t.Fatalf("ValidateJobID(%q) succeeded", id)
			}
		})
	}
}
