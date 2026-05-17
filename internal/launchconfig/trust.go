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

// trustHashes returns the effective set of recorded hashes from a MetaTrust.
// It merges the deprecated singular Hash field into the Hashes slice so that
// old single-hash entries are handled transparently.
func trustHashes(t MetaTrust) []string {
	seen := map[string]bool{}
	var out []string
	for _, h := range t.Hashes {
		if h != "" && !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	// Migrate the deprecated singular Hash field.
	if t.Hash != "" && !seen[t.Hash] {
		out = append(out, t.Hash)
	}
	return out
}

// TrustHashSet returns the deduplicated set of hashes from a MetaTrust,
// including migration of the deprecated singular Hash field.
// This is the canonical way for callers to read the hash set before appending.
func TrustHashSet(t MetaTrust) []string {
	return trustHashes(t)
}

// HashInSet reports whether hash appears in the given set.
func HashInSet(hash string, set []string) bool {
	for _, h := range set {
		if h == hash {
			return true
		}
	}
	return false
}

// ComputeTrustState evaluates the current TrustState from the on-disk
// hash and the recorded Meta.
//
//   - hash == ""                              → file is absent
//   - hash != "", no recorded hashes          → untrusted (first contact)
//   - hash != "", hash in set, decision trusted  → trusted
//   - hash != "", hash in set, decision rejected → rejected
//   - hash != "", hash not in set             → changed (new content)
func ComputeTrustState(hash string, meta Meta) TrustState {
	if hash == "" {
		return TrustAbsent
	}
	hashes := trustHashes(meta.Trust)
	if len(hashes) == 0 {
		return TrustUntrusted
	}
	inSet := false
	for _, h := range hashes {
		if h == hash {
			inSet = true
			break
		}
	}
	if !inSet {
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
