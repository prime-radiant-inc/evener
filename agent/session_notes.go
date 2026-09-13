package agent

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/identifier"
)

// Shared-notes store: note normalization/clamping and the session URL list.
// Every method holds s.mu while touching session state, mirroring setPinnedNote
// (agent/session_self_compact.go); callers persist through the existing
// meta-save path (maybeAutoSave → Meta) after a change.

const (
	sessionNoteMaxRunes = 1000
	sessionURLMax       = 50
	sessionURLMaxLen    = 2048
	sessionLabelMaxLen  = 280
)

// ReadCanonicalHumanNote reads committed human-note authority without writing
// or recovering the journal. present distinguishes an explicit clear from a
// snapshot that does not yet own the note. There is no metadata import fallback.
func ReadCanonicalHumanNote(stateDir, sessionID string) (note string, present bool, err error) {
	if err := schema.ValidateSessionID(sessionID); err != nil {
		return "", false, err
	}
	snapshot, err := loadClientMutationSnapshotFS(afero.NewOsFs(), stateDir, sessionID)
	if err != nil {
		return "", false, err
	}
	if snapshot.HumanNote == nil {
		return "", false, nil
	}
	return *snapshot.HumanNote, true, nil
}

// normalizeNote collapses every run of whitespace (including newlines) to one
// space and clamps to sessionNoteMaxRunes Unicode characters.
func normalizeNote(text string) string {
	collapsed := strings.Join(strings.Fields(text), " ")
	runes := []rune(collapsed)
	if len(runes) > sessionNoteMaxRunes {
		collapsed = string(runes[:sessionNoteMaxRunes])
	}
	return collapsed
}

// setAgentNote normalizes and clamps the agent whiteboard.
func (s *Session) setAgentNote(note string) (stored string, changed bool) {
	normalized := normalizeNote(note)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.agentNote == normalized {
		return s.agentNote, false
	}
	s.agentNote = normalized
	return normalized, true
}

// addSessionURL validates url plus label, canonicalizes the URL, and appends
// it to the session list. A re-add of an existing canonical URL updates the
// label and returns the existing entry (id, addedBy, addedAt unchanged).
// It performs no fetch. The caller persists (maybeAutoSave) and emits
// EventUrlsUpdated after a successful add.
func (s *Session) addSessionURL(rawURL, label string) (schema.SessionURL, error) {
	cwd := s.notesCWD()
	canonical, err := canonicalSessionURL(rawURL, cwd)
	if err != nil {
		return schema.SessionURL{}, err
	}
	clampedLabel := normalizeNote(label)
	if utf8.RuneCountInString(clampedLabel) > sessionLabelMaxLen {
		return schema.SessionURL{}, fmt.Errorf("urls/add: label exceeds %d characters", sessionLabelMaxLen)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.sessionURLs {
		if s.sessionURLs[i].URL == canonical {
			s.sessionURLs[i].Label = clampedLabel
			return s.sessionURLs[i], nil
		}
	}
	if len(s.sessionURLs) >= sessionURLMax {
		return schema.SessionURL{}, fmt.Errorf("urls/add: session URL list is full (%d entries)", sessionURLMax)
	}
	id, err := identifier.NewClientMutationID()
	if err != nil {
		return schema.SessionURL{}, fmt.Errorf("urls/add: mint entry id: %w", err)
	}
	entry := schema.SessionURL{
		ID:      id,
		URL:     canonical,
		Label:   clampedLabel,
		AddedBy: "agent",
		AddedAt: s.sclock().Now().UTC().Unix(),
	}
	s.sessionURLs = append(s.sessionURLs, entry)
	return entry, nil
}

// removeSessionURL deletes the entry with id, reporting whether one was found.
func (s *Session) removeSessionURL(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.sessionURLs {
		if s.sessionURLs[i].ID == id {
			s.sessionURLs = append(s.sessionURLs[:i], s.sessionURLs[i+1:]...)
			return true
		}
	}
	return false
}

// notesCWD returns the session's working directory for bare-path resolution,
// reading s.env under s.mu (callers must not already hold the lock).
func (s *Session) notesCWD() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.env == nil {
		return ""
	}
	return s.env.WorkingDirectory()
}

// canonicalSessionURL resolves raw against cwd (bare paths) and normalizes:
// http(s) lowercases scheme+host, drops default ports, collapses a trailing
// slash on an empty path, and drops the fragment. Bare paths and file:/// URLs
// resolve to file:/// absolute forms checked against the session scope via the
// execenv.RootBoundary precedent (resolveShellWorkingDir). It rejects
// non-http(s)/file schemes and over-length url/label values.
func canonicalSessionURL(raw, cwd string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("urls/add: empty URL")
	}
	if utf8.RuneCountInString(trimmed) > sessionURLMaxLen {
		return "", fmt.Errorf("urls/add: URL exceeds %d characters", sessionURLMaxLen)
	}
	lowered := strings.ToLower(trimmed)
	switch {
	case strings.HasPrefix(lowered, "http://") || strings.HasPrefix(lowered, "https://"):
		return canonicalHTTPURL(trimmed)
	case strings.HasPrefix(lowered, "file://"):
		parsed, err := url.Parse(trimmed)
		if err != nil {
			return "", fmt.Errorf("urls/add: invalid file URL %q: %w", raw, err)
		}
		if parsed.Host != "" && parsed.Host != "localhost" {
			return "", fmt.Errorf("urls/add: file URL host %q is not supported", parsed.Host)
		}
		// Query and fragment are not path content: without this gate
		// "file:///tmp/foo?bar" and "file:///tmp/foo#frag" would collapse to
		// "file:///tmp/foo", silently dropping the caller's input.
		if parsed.RawQuery != "" || parsed.Fragment != "" {
			return "", fmt.Errorf("urls/add: file URL %q must not carry a query or fragment", raw)
		}
		// Decode URL syntax before the scope check; bare paths stay literal.
		return canonicalFilePath(parsed.Path, cwd, raw)
	default:
		// Any explicit scheme that is not http(s) or file is rejected here,
		// BEFORE the bare-path fallback: inputs like "javascript:alert(1)",
		// "data:text/plain,hi" or "mailto:foo@bar" carry no "://" but must
		// never be stored as file:// entries. A bare path can share the shape
		// ("report:2024.md"), so the error names the "./" form that resolves as
		// a path rather than only the scheme that was read.
		if scheme, _, ok := strings.Cut(trimmed, ":"); ok && isURLScheme(scheme) {
			return "", fmt.Errorf("urls/add: %q is read as the unsupported URL scheme %q; to add a path whose first segment contains a colon, prefix it with ./ (e.g. %q)", trimmed, scheme, "./"+trimmed)
		}
		if strings.Contains(trimmed, "://") {
			scheme, _, _ := strings.Cut(trimmed, "://")
			return "", fmt.Errorf("urls/add: unsupported URL scheme %q", scheme)
		}
		return canonicalFilePath(trimmed, cwd, raw)
	}
}

// isURLScheme reports whether s is a URI scheme per RFC 3986 §3.1
// (ALPHA *( ALPHA / DIGIT / "+" / "-" / "." )): the gate canonicalSessionURL
// uses to tell "this input names a scheme" from "this input is a bare path"
// (a Windows drive letter like "C:" also matches, and is rejected as a
// scheme rather than resolved as a path — this daemon never runs on
// Windows, and a drive-lettered path is meaningless in its scope model).
func isURLScheme(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' {
			continue
		}
		if i > 0 && (c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.') {
			continue
		}
		return false
	}
	return true
}

// canonicalHTTPURL normalizes an http(s) URL: lowercase scheme+host, drop
// default ports, collapse a trailing slash on an empty path, drop the fragment.
func canonicalHTTPURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("urls/add: invalid URL %q: %w", raw, err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("urls/add: unsupported URL scheme %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("urls/add: URL %q has no host", raw)
	}
	// An empty hostname with a non-empty Host (e.g. "http://:80") passes the
	// Host check above but normalizes to an unusable link: Hostname() is what
	// the canonical form is actually built from, so gate on it.
	if parsed.Hostname() == "" {
		return "", fmt.Errorf("urls/add: URL %q has no hostname", raw)
	}
	// Userinfo is never valid here: credentials would be persisted in the
	// URL list, displayed, and used as link href. Reject rather than strip,
	// so a mistyped "user@host" path is not silently rewritten.
	if parsed.User != nil {
		return "", fmt.Errorf("urls/add: URL %q must not contain credentials", raw)
	}
	host := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	switch {
	case port != "":
		parsed.Host = net.JoinHostPort(host, port)
	case strings.Contains(host, ":"):
		// IPv6 literal without a port keeps its brackets; JoinHostPort
		// with an empty port would drop them and emit a malformed host.
		parsed.Host = "[" + host + "]"
	default:
		parsed.Host = host
	}
	parsed.Scheme = scheme
	parsed.Fragment = ""
	// Collapse a trailing slash on an empty path: "https://host/" → "https://host".
	if parsed.EscapedPath() == "/" {
		parsed.Path = ""
		parsed.RawPath = ""
	}
	out := parsed.String()
	if utf8.RuneCountInString(out) > sessionURLMaxLen {
		return "", fmt.Errorf("urls/add: URL exceeds %d characters", sessionURLMaxLen)
	}
	return out, nil
}

// canonicalFilePath resolves a bare path or file-URL path against cwd and
// returns its file:/// absolute form, rejecting out-of-scope paths via the
// execenv.RootBoundary precedent (the same symlink-aware escape check the
// shell tool applies to a model-chosen cwd). file-URL callers pass the
// decoded path, so encoded traversal is checked against the scoped hierarchy.
// Bare-path callers pass literal filenames, including any percent signs.
func canonicalFilePath(path, cwd, raw string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("urls/add: empty file path in %q", raw)
	}
	abs := path
	if !filepath.IsAbs(abs) {
		if cwd == "" {
			return "", fmt.Errorf("urls/add: relative path %q has no session working directory", raw)
		}
		abs = filepath.Join(cwd, abs)
	} else if cwd == "" {
		// No scope to check against: an absolute path with no session
		// working directory (nil env) would otherwise be accepted unchecked
		// (the branches below both gate on cwd). Reject fail-closed.
		return "", fmt.Errorf("urls/add: absolute path %q has no session working directory", raw)
	}
	abs = filepath.Clean(abs)
	if rb, ok := notesRootBoundary(cwd); ok {
		if err := rb.EnsureUnderRoot(abs); err != nil {
			return "", fmt.Errorf("urls/add: path %q is outside the session scope: %w", raw, err)
		}
	} else if cwd != "" && !pathWithinNotesScope(abs, cwd) {
		return "", fmt.Errorf("urls/add: path %q is outside the session scope %q", raw, cwd)
	}
	// Serialize through net/url so path delimiters in filenames (#, ?, %)
	// escape instead of parsing back as a fragment, query, or escape:
	// "file://" + abs would let docs/a#b.md round-trip as path docs/a with
	// fragment b.md.
	fileURL := url.URL{Scheme: "file", Path: abs}
	out := fileURL.String()
	if utf8.RuneCountInString(out) > sessionURLMaxLen {
		return "", fmt.Errorf("urls/add: URL exceeds %d characters", sessionURLMaxLen)
	}
	return out, nil
}

// notesRootBoundary returns the RootBoundary view of a local execution
// environment rooted at cwd, matching the securepath-backed check the shell
// tool uses for cwd validation.
func notesRootBoundary(cwd string) (execenv.RootBoundary, bool) {
	env := execenv.NewLocalExecutionEnvironment(cwd)
	rb, ok := any(env).(execenv.RootBoundary)
	return rb, ok
}

// pathWithinNotesScope is the lexical fallback when no RootBoundary applies:
// abs must equal cwd or sit beneath it.
func pathWithinNotesScope(abs, cwd string) bool {
	cleanRoot := filepath.Clean(cwd)
	if abs == cleanRoot {
		return true
	}
	return strings.HasPrefix(abs, cleanRoot+string(filepath.Separator))
}
