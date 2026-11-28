package identifier

import (
	"crypto/rand"
	"io"
)

const (
	jobIDPrefix     = "job_"
	jobIDSuffixSize = 12
	jobIDSize       = len(jobIDPrefix) + base62Width + 1 + jobIDSuffixSize
)

func NewJobID(ownerSessionID string) (string, error) {
	return newJobID(ownerSessionID, rand.Reader)
}

func newJobID(ownerSessionID string, random io.Reader) (string, error) {
	if err := ValidateSessionID(ownerSessionID); err != nil {
		return "", err
	}
	suffix := make([]byte, jobIDSuffixSize)
	var sample [1]byte
	for i := range suffix {
		for {
			if _, err := io.ReadFull(random, sample[:]); err != nil {
				return "", err
			}
			if int(sample[0]) < 256-256%len(base62Alphabet) {
				suffix[i] = base62Alphabet[int(sample[0])%len(base62Alphabet)]
				break
			}
		}
	}
	return jobIDPrefix + ownerSessionID + "_" + string(suffix), nil
}

func ValidateJobID(jobID string) error {
	if len(jobID) != jobIDSize || jobID[:len(jobIDPrefix)] != jobIDPrefix || jobID[len(jobIDPrefix)+base62Width] != '_' {
		return errInvalidUUIDPayload
	}
	if err := ValidateSessionID(jobID[len(jobIDPrefix) : len(jobIDPrefix)+base62Width]); err != nil {
		return err
	}
	for _, value := range []byte(jobID[len(jobIDPrefix)+base62Width+1:]) {
		if !isBase62(value) {
			return errInvalidUUIDPayload
		}
	}
	return nil
}

func JobOwnerSessionID(jobID string) (string, error) {
	if err := ValidateJobID(jobID); err != nil {
		return "", err
	}
	return jobID[len(jobIDPrefix) : len(jobIDPrefix)+base62Width], nil
}

func MustNewJobID(ownerSessionID string) string {
	value, err := NewJobID(ownerSessionID)
	if err != nil {
		panic(err)
	}
	return value
}
