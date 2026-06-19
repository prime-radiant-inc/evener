package doctor

import (
	"fmt"
	"strings"
)

// selector is a parsed session selector. hash is non-empty only for a proj:
// ref; sid is always the bare session id.
type selector struct {
	hash string
	sid  string
}

// parseSelector parses a session selector in the dialect read_session_transcript
// accepts: local:<sid>, proj:<hash>:<sid>, or a bare <sid>. The empty selector
// and "current" are rejected: a standalone forensic tool has no current session,
// so the caller must name one. All tokens are validated to be bare identifiers
// (no path separators or dots) to reject traversal.
func parseSelector(s string) (selector, error) {
	if s == "" || s == "current" {
		return selector{}, fmt.Errorf("no session selector: pass a session id, local:<id>, or proj:<hash>:<id> (a standalone forensic tool has no %q session)", "current")
	}
	if strings.HasPrefix(s, "local:") {
		sid := strings.TrimPrefix(s, "local:")
		if !validToken(sid) {
			return selector{}, fmt.Errorf("invalid session id in selector %q", s)
		}
		return selector{sid: sid}, nil
	}
	if strings.HasPrefix(s, "proj:") {
		rest := strings.TrimPrefix(s, "proj:")
		hash, sid, ok := strings.Cut(rest, ":")
		if !ok {
			return selector{}, fmt.Errorf("malformed proj ref %q (want proj:<hash>:<id>)", s)
		}
		if !validToken(hash) || !validToken(sid) {
			return selector{}, fmt.Errorf("invalid token in selector %q", s)
		}
		return selector{hash: hash, sid: sid}, nil
	}
	if !validToken(s) {
		return selector{}, fmt.Errorf("invalid session id %q", s)
	}
	return selector{sid: s}, nil
}
