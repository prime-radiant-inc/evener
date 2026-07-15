package agent

import (
	"fmt"
	"strings"
)

// encodeRef builds an opaque transcript ref. An empty projectID means the
// current bucket (local:<id>); otherwise proj:<projectID>:<id>.
func encodeRef(projectID, sessionID string) string {
	if projectID == "" {
		return "local:" + sessionID
	}
	return "proj:" + projectID + ":" + sessionID
}

// decodeRef parses a ref into (projectID, sessionID). projectID is "" for
// local refs. Tokens are validated to be bare (no path separators) to reject
// traversal.
func decodeRef(ref string) (projectID, sessionID string, err error) {
	switch {
	case strings.HasPrefix(ref, "local:"):
		sessionID = strings.TrimPrefix(ref, "local:")
		if err := validIDToken(sessionID); err != nil {
			return "", "", fmt.Errorf("transcript ref %q: %w", ref, err)
		}
		return "", sessionID, nil
	case strings.HasPrefix(ref, "proj:"):
		rest := strings.TrimPrefix(ref, "proj:")
		projectID, sessionID, ok := strings.Cut(rest, ":")
		if !ok {
			return "", "", fmt.Errorf("transcript ref %q: malformed proj ref", ref)
		}
		if err := validIDToken(projectID); err != nil {
			return "", "", fmt.Errorf("transcript ref %q: %w", ref, err)
		}
		if err := validIDToken(sessionID); err != nil {
			return "", "", fmt.Errorf("transcript ref %q: %w", ref, err)
		}
		return projectID, sessionID, nil
	default:
		return "", "", fmt.Errorf("transcript ref %q: unknown scheme", ref)
	}
}

// validIDToken only parses an opaque internal ref token. Local filesystem
// boundaries apply the domain validators after parsing; keeping parsing and
// validation separate preserves opaque refs used by non-local providers.
func validIDToken(s string) error {
	if s == "" || strings.ContainsAny(s, "/\\.: ") {
		return fmt.Errorf("invalid id token %q", s)
	}
	return nil
}
