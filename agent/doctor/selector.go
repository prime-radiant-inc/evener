package doctor

import (
	"fmt"
	"strings"

	"primeradiant.com/evener/identifier"
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
// so the caller must name one. The sid is a strict identifier; the project id
// only has to be a traversal-safe directory name (see projectTokenOK) — bucket
// identity is whatever a directory under evener/projects is named, and Locate
// emits refs from actual names, so anything stricter would make a located
// legacy-named bucket impossible to address again. A proj: ref cuts at the
// LAST colon: session ids never contain one (pinned by
// TestSessionIDGrammarCarriesNoColon), so the final colon is always the sid
// boundary and a project id may itself contain colons.
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
		cut := strings.LastIndexByte(rest, ':')
		if cut < 0 {
			return selector{}, fmt.Errorf("malformed proj ref %q (want proj:<project-id>:<id>)", s)
		}
		projectID, sid := rest[:cut], rest[cut+1:]
		if !projectTokenOK(projectID) || identifier.ValidateSessionID(sid) != nil {
			return selector{}, fmt.Errorf("invalid token in selector %q", s)
		}
		return selector{projectID: projectID, sid: sid}, nil
	}
	if err := identifier.ValidateSessionID(s); err != nil {
		return selector{}, fmt.Errorf("invalid session id %q", s)
	}
	return selector{sid: s}, nil
}

// projectTokenOK reports whether a proj: selector's project-id token is safe
// to treat as a bucket-directory name. Rejected: empty, the dot components,
// path separators, and NUL — everything that could turn the token into a
// path-component escape. Everything else is accepted, including colons —
// colons are legal in directory names and the ref parser cuts at the last
// one — matching the sweep (globBuckets enumerates every directory and
// emits refs from actual names), so a legacy- or foreign-named bucket stays
// addressable by the very refs Locate returns for its sessions. The token
// is only ever compared against enumerated names, never joined into a
// path, so this check is defense-in-depth on top of that.
func projectTokenOK(projectID string) bool {
	if projectID == "" || projectID == "." || projectID == ".." {
		return false
	}
	return !strings.ContainsAny(projectID, "/\\\x00")
}
