package agent

import (
	"fmt"
	"strings"
)

// encodeRef builds an opaque transcript ref. An empty bucketHash means the
// current bucket (local:<id>); otherwise proj:<bucketHash>:<id>.
func encodeRef(bucketHash, sessionID string) string {
	if bucketHash == "" {
		return "local:" + sessionID
	}
	return "proj:" + bucketHash + ":" + sessionID
}

// decodeRef parses a ref into (bucketHash, sessionID). bucketHash is "" for
// local refs. Tokens are validated to be bare (no path separators) to reject
// traversal.
func decodeRef(ref string) (bucketHash, sessionID string, err error) {
	switch {
	case strings.HasPrefix(ref, "local:"):
		sessionID = strings.TrimPrefix(ref, "local:")
		if err := validIDToken(sessionID); err != nil {
			return "", "", fmt.Errorf("transcript ref %q: %w", ref, err)
		}
		return "", sessionID, nil
	case strings.HasPrefix(ref, "proj:"):
		rest := strings.TrimPrefix(ref, "proj:")
		bucketHash, sessionID, ok := strings.Cut(rest, ":")
		if !ok {
			return "", "", fmt.Errorf("transcript ref %q: malformed proj ref", ref)
		}
		if err := validIDToken(bucketHash); err != nil {
			return "", "", fmt.Errorf("transcript ref %q: %w", ref, err)
		}
		if err := validIDToken(sessionID); err != nil {
			return "", "", fmt.Errorf("transcript ref %q: %w", ref, err)
		}
		return bucketHash, sessionID, nil
	default:
		return "", "", fmt.Errorf("transcript ref %q: unknown scheme", ref)
	}
}

func validIDToken(s string) error {
	if s == "" || strings.ContainsAny(s, "/\\.: ") {
		return fmt.Errorf("invalid id token %q", s)
	}
	return nil
}
