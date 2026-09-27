package agent

import (
	"fmt"
	"strings"

	"primeradiant.com/evener/agent/internal/bucketref"
)

// encodeRef builds an opaque transcript ref using the shared session-ref
// dialect (see agent/internal/bucketref.SessionRefDialect). An empty projectID
// means the current bucket (local:<id>); otherwise proj:<projectID>:<id>.
func encodeRef(projectID, sessionID string) string {
	if projectID == "" {
		return bucketref.LocalScheme + sessionID
	}
	return bucketref.ProjScheme + projectID + ":" + sessionID
}

// decodeRef parses a ref into (projectID, sessionID) using the shared
// session-ref dialect (see agent/internal/bucketref.SessionRefDialect). It is
// the deliberately stricter, model-facing subset: validIDToken rejects
// colons/dots/spaces on top of the separators, so a ref the model emits for a
// non-canonical bucket name is rejected at the agent read boundary rather than
// at the doctor's wider projectTokenOK. projectID is "" for local refs.
func decodeRef(ref string) (projectID, sessionID string, err error) {
	switch {
	case strings.HasPrefix(ref, bucketref.LocalScheme):
		sessionID = strings.TrimPrefix(ref, bucketref.LocalScheme)
		if err := validIDToken(sessionID); err != nil {
			return "", "", fmt.Errorf("transcript ref %q: %w", ref, err)
		}
		return "", sessionID, nil
	case strings.HasPrefix(ref, bucketref.ProjScheme):
		rest := strings.TrimPrefix(ref, bucketref.ProjScheme)
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
