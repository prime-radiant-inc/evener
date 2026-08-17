package identifier

import (
	"bytes"
	"errors"
	"testing"
)

func TestJobIDCarriesCompleteOwnerAndRandomSuffix(t *testing.T) {
	const owner = "02wMz5TxvEMoJEDTDGOTil"
	id, err := newJobID(owner, bytes.NewReader(make([]byte, 64)))
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

// exhaustedReader stands in for an entropy source that has stopped answering.
type exhaustedReader struct{}

func (exhaustedReader) Read([]byte) (int, error) { return 0, errors.New("entropy exhausted") }

func TestNewJobIDFailsRatherThanMintFromExhaustedEntropy(t *testing.T) {
	// The suffix is what keeps two jobs from the same session distinct. If a
	// failing entropy source were ignored, the loop would emit whatever the
	// zero-filled buffer encodes to — a well-formed ID that is not unique, and
	// that collides silently with every other ID minted the same way.
	id, err := newJobID("02wMz5TxvEMoJEDTDGOTil", exhaustedReader{})
	if err == nil {
		t.Fatalf("newJobID with a failing reader = %q, want an error", id)
	}
	if id != "" {
		t.Fatalf("newJobID returned %q alongside error %v, want empty", id, err)
	}
}

func TestJobOwnerSessionIDRejectsAnInvalidJobID(t *testing.T) {
	// The owner is sliced out of the ID by fixed offsets, so validation is the
	// only thing standing between a malformed ID and a silently wrong owner.
	for _, jobID := range []string{
		"",
		"not-a-job-id",
		"job_02wMz5TxvEMoJEDTDGOTil-000000000001", // '-' where the separator must be '_'
		"xxx_02wMz5TxvEMoJEDTDGOTil_000000000001", // wrong domain prefix
	} {
		owner, err := JobOwnerSessionID(jobID)
		if err == nil {
			t.Errorf("JobOwnerSessionID(%q) = %q, want an error", jobID, owner)
		}
		if owner != "" {
			t.Errorf("JobOwnerSessionID(%q) returned owner %q alongside error %v, want empty", jobID, owner, err)
		}
	}
}

func TestAbbreviateJobIDReturnsTheIDWhenNoPrefixSurvives(t *testing.T) {
	const owner = "02wMz5TxvEMoJEDTDGOTil"
	jobID := "job_" + owner + "_000000000001"
	// The suffix alone is jobIDSuffixSize bytes plus an ellipsis, so any budget
	// that cannot seat at least one prefix byte has nothing to abbreviate toward.
	// Truncating anyway would emit a string that is neither the ID nor a usable
	// abbreviation of it.
	for _, maxLength := range []int{0, 1, jobIDSuffixSize, jobIDSuffixSize + 1} {
		if got := AbbreviateJobID(jobID, maxLength); got != jobID {
			t.Errorf("AbbreviateJobID(%q, %d) = %q, want the ID unchanged", jobID, maxLength, got)
		}
	}
}

func TestMustNewJobIDPanicsRatherThanReturnARejectedID(t *testing.T) {
	const owner = "02wMz5TxvEMoJEDTDGOTil"
	id := MustNewJobID(owner)
	if err := ValidateJobID(id); err != nil {
		t.Fatalf("MustNewJobID(%q) = %q, which ValidateJobID rejects: %v", owner, id, err)
	}
	if got, err := JobOwnerSessionID(id); err != nil || got != owner {
		t.Fatalf("owner of %q = %q (err=%v), want %q", id, got, err, owner)
	}

	// Panicking is the only behaviour that distinguishes this from NewJobID, so
	// it is the half worth proving: a caller who cannot supply a valid owner must
	// fail loudly here rather than receive an ID every validator would reject.
	defer func() {
		if recover() == nil {
			t.Fatal("MustNewJobID returned normally for an owner NewJobID rejects")
		}
	}()
	MustNewJobID("not-a-session-id")
}

func TestAbbreviateJobIDPreservesCompleteRandomSuffix(t *testing.T) {
	const owner = "02wMz5TxvEMoJEDTDGOTil"
	first := "job_" + owner + "_000000000001"
	second := "job_" + owner + "_000000000002"

	if got := AbbreviateJobID("short", 26); got != "short" {
		t.Fatalf("short abbreviation = %q, want unchanged", got)
	}
	for _, jobID := range []string{first, second} {
		if got := AbbreviateJobID(jobID, 26); !bytes.Contains([]byte(got), []byte(jobID[len(jobID)-12:])) {
			t.Fatalf("abbreviation %q omitted suffix from %q", got, jobID)
		}
	}
	if AbbreviateJobID(first, 26) == AbbreviateJobID(second, 26) {
		t.Fatal("same-owner job abbreviations must remain distinct")
	}
}
