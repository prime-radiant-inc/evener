package identifier

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

// TestNewUUIDv7Payload_Error covers the error branch of newUUIDv7Payload
// (uuid.go:77-79) by swapping the uuidNewV7 seam to a function that always
// fails. Without this seam the branch is unreachable: uuid.NewV7 only fails
// when the system random source is unavailable.
func TestNewUUIDv7Payload_Error(t *testing.T) {
	old := uuidNewV7
	uuidNewV7 = func() (uuid.UUID, error) {
		return uuid.UUID{}, errors.New("random source unavailable")
	}
	t.Cleanup(func() { uuidNewV7 = old })

	_, err := newUUIDv7Payload()
	if err == nil {
		t.Fatal("expected error from newUUIDv7Payload when uuidNewV7 fails")
	}
}

// TestNewDomainID_Error covers the error branch of newDomainID
// (domains.go:5-7) by the same seam.
func TestNewDomainID_Error(t *testing.T) {
	old := uuidNewV7
	uuidNewV7 = func() (uuid.UUID, error) {
		return uuid.UUID{}, errors.New("random source unavailable")
	}
	t.Cleanup(func() { uuidNewV7 = old })

	_, err := NewSessionID()
	if err == nil {
		t.Fatal("expected error from NewSessionID when uuidNewV7 fails")
	}
}
