package launchconfig

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/BurntSushi/toml"
)

// CanonicalHashTOML returns sha256-hex of the canonicalized TOML — parsed
// into a Layer, re-encoded with sorted keys via the toml library — so
// whitespace edits and key reorderings produce a stable hash but semantic
// changes break it.
func CanonicalHashTOML(data []byte) (string, error) {
	var l Layer
	if _, err := toml.Decode(string(data), &l); err != nil {
		return "", fmt.Errorf("canonical hash: parse: %w", err)
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(l); err != nil {
		return "", fmt.Errorf("canonical hash: encode: %w", err)
	}
	sum := sha256.Sum256(buf.Bytes())
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// ComputeTrustState evaluates the current TrustState from the on-disk
// hash and the recorded Meta.
//
//   - hash == ""  → file is absent
//   - hash != "", Meta.Trust.Hash == ""      → untrusted (first contact)
//   - hash != "", Meta.Trust.Decision rejected and matches → rejected
//   - hash != "", Meta.Trust.Decision trusted and matches  → trusted
//   - hash != "", Meta.Trust.Hash differs                  → changed
func ComputeTrustState(hash string, meta Meta) TrustState {
	if hash == "" {
		return TrustAbsent
	}
	if meta.Trust.Hash == "" {
		return TrustUntrusted
	}
	if meta.Trust.Hash != hash {
		return TrustChanged
	}
	switch meta.Trust.Decision {
	case "trusted":
		return TrustTrusted
	case "rejected":
		return TrustRejected
	default:
		return TrustUntrusted
	}
}
