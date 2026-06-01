package hubapi

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var refPattern = regexp.MustCompile(`^[A-Za-z0-9._~:-]+$`)

// Ref is an opaque hub session reference. Local refs are formatted as
// "local:<session_id>"; callers should not parse them outside this package.
type Ref struct {
	HostID    string
	SessionID string
}

// LocalRef returns the canonical ref for a local session id.
func LocalRef(sessionID string) Ref {
	return Ref{HostID: "local", SessionID: sessionID}
}

func (r Ref) String() string {
	if r.HostID == "" || r.SessionID == "" {
		return ""
	}
	return r.HostID + ":" + r.SessionID
}

// PathEscaped returns the ref escaped for use as one URL path segment.
func (r Ref) PathEscaped() string {
	return url.PathEscape(r.String())
}

// ParseRef validates and parses a hub session ref.
func ParseRef(raw string) (Ref, error) {
	if raw == "" {
		return Ref{}, errors.New("empty ref")
	}
	if !refPattern.MatchString(raw) {
		return Ref{}, fmt.Errorf("invalid ref %q", raw)
	}
	host, sessionID, ok := strings.Cut(raw, ":")
	if !ok || host == "" || sessionID == "" {
		return Ref{}, fmt.Errorf("invalid ref %q", raw)
	}
	if strings.Contains(sessionID, "..") {
		return Ref{}, fmt.Errorf("invalid ref %q", raw)
	}
	return Ref{HostID: host, SessionID: sessionID}, nil
}
