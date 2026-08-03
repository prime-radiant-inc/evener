package doctor

import (
	"fmt"
	"strings"

	"primeradiant.com/serf/identifier"
)

// selector is a parsed session selector. projectID is non-empty only for a proj:
// ref; sid is always the bare session id.
type selector struct {
	projectID string
	sid       string
}

// parseSelector parses a session selector in the dialect read_transcript
// accepts: local:<sid>, proj:<project-id>:<sid>, or a bare <sid>. The empty selector
// and "current" are rejected: a standalone forensic tool has no current session,
// so the caller must name one. All tokens are validated to be bare identifiers
// (no path separators or dots) to reject traversal.
func parseSelector(s string) (selector, error) {
	if s == "" || s == "current" {
		return selector{}, fmt.Errorf("no session selector: pass a session id, local:<id>, or proj:<project-id>:<id> (a standalone forensic tool has no %q session)", "current")
	}
	if sid, ok := strings.CutPrefix(s, "local:"); ok {
		if err := identifier.ValidateSessionID(sid); err != nil {
			return selector{}, fmt.Errorf("invalid session id in selector %q", s)
		}
		return selector{sid: sid}, nil
	}
	if rest, ok := strings.CutPrefix(s, "proj:"); ok {
		projectID, sid, ok := strings.Cut(rest, ":")
		if !ok {
			return selector{}, fmt.Errorf("malformed proj ref %q (want proj:<project-id>:<id>)", s)
		}
		if identifier.ValidateProjectID(projectID) != nil || identifier.ValidateSessionID(sid) != nil {
			return selector{}, fmt.Errorf("invalid token in selector %q", s)
		}
		return selector{projectID: projectID, sid: sid}, nil
	}
	if err := identifier.ValidateSessionID(s); err != nil {
		return selector{}, fmt.Errorf("invalid session id %q", s)
	}
	return selector{sid: s}, nil
}
