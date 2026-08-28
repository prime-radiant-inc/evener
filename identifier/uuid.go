package identifier

import (
	"errors"
	"math/big"

	"github.com/google/uuid"
)

const (
	base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	base62Width    = 22
)

var (
	errInvalidUUIDPayload = errors.New("invalid UUID payload")
	errInvalidUUIDv7      = errors.New("UUID payload is not UUIDv7")
)

func EncodeUUID(value uuid.UUID) string {
	n := new(big.Int).SetBytes(value[:])
	base := big.NewInt(62)
	result := make([]byte, base62Width)
	for i := range result {
		result[i] = '0'
	}
	for i := base62Width - 1; n.Sign() > 0; i-- {
		var remainder big.Int
		n.QuoRem(n, base, &remainder)
		result[i] = base62Alphabet[remainder.Int64()]
	}
	return string(result)
}

func DecodeUUID(payload string) (uuid.UUID, error) {
	var value uuid.UUID
	if len(payload) != base62Width {
		return value, errInvalidUUIDPayload
	}
	n := new(big.Int)
	base := big.NewInt(62)
	for i := range len(payload) {
		index := int64(-1)
		for j := range int64(len(base62Alphabet)) {
			if payload[i] == base62Alphabet[j] {
				index = j
				break
			}
		}
		if index < 0 {
			return value, errInvalidUUIDPayload
		}
		n.Mul(n, base)
		n.Add(n, big.NewInt(index))
	}
	if n.BitLen() > 128 {
		return value, errInvalidUUIDPayload
	}
	bytes := n.Bytes()
	copy(value[16-len(bytes):], bytes)
	return value, nil
}

func ValidateUUIDv7Payload(payload string) error {
	value, err := DecodeUUID(payload)
	if err != nil {
		return err
	}
	if value.Version() != 7 || value.Variant() != uuid.RFC4122 {
		return errInvalidUUIDv7
	}
	return nil
}

func newUUIDv7Payload() (string, error) {
	value, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	return EncodeUUID(value), nil
}
