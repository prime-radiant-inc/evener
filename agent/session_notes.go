package agent

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strings"

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

// setHumanNote normalizes and clamps note, stores it on change, and reports
// the stored value with whether it changed (clamp-then-compare: re-saving an
// identical over-length note is a no-op).
func (s *Session) setHumanNote(note string) (stored string, changed bool) {
	normalized := normalizeNote(note)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.humanNote == normalized {
		return s.humanNote, false
	}
	s.humanNote = normalized
	return s.humanNote, true
}

// setAgentNote normalizes and clamps note, stores it on change, and reports
// the stored value with whether it changed.
func (s *Session) setAgentNote(note string) (stored string, changed bool) {
	normalized := normalizeNote(note)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.agentNote == normalized {
		return s.agentNote, false
	}
	s.agentNote = normalized
	return s.agentNote, true
}

// addSessionURL validates url plus label, canonicalizes the URL, and appends
// it to the session list. A re-add of an existing canonical URL updates the
// label and returns the existing entry (id, addedBy, addedAt unchanged).
// It performs no fetch. The caller persists (maybeAutoSave) and emits
// EventUrlsUpdated after a successful add.
func (s *Session) addSessionURL(rawURL, label string) (schema.SessionURL, error) {
	if len([]rune(label)) > sessionLabelMaxLen {
		return schema.SessionURL{}, fmt.Errorf("urls/add: label exceeds %d characters", sessionLabelMaxLen)
	}
	cwd := s.notesCWD()
	canonical, err := canonicalSessionURL(rawURL, cwd)
	if err != nil {
		return schema.SessionURL{}, err
	}
	clampedLabel := normalizeNote(label)
	if len([]rune(clampedLabel)) > sessionLabelMaxLen {
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
		return "", fmt.Errorf("urls/add: empty URL")
	}
	if len([]rune(trimmed)) > sessionURLMaxLen {
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
		return canonicalFilePath(parsed.Path, cwd, raw)
	default:
		if strings.Contains(trimmed, "://") {
			scheme := trimmed
			if i := strings.Index(scheme, "://"); i >= 0 {
				scheme = scheme[:i]
			}
			return "", fmt.Errorf("urls/add: unsupported URL scheme %q", scheme)
		}
		return canonicalFilePath(trimmed, cwd, raw)
	}
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
	if len([]rune(out)) > sessionURLMaxLen {
		return "", fmt.Errorf("urls/add: URL exceeds %d characters", sessionURLMaxLen)
	}
	return out, nil
}

// canonicalFilePath resolves a bare path or file-URL path against cwd and
// returns its file:/// absolute form, rejecting out-of-scope paths via the
// execenv.RootBoundary precedent (the same symlink-aware escape check the
// shell tool applies to a model-chosen cwd).
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
	}
	abs = filepath.Clean(abs)
	if rb, ok := notesRootBoundary(cwd); ok {
		if err := rb.EnsureUnderRoot(abs); err != nil {
			return "", fmt.Errorf("urls/add: path %q is outside the session scope: %w", raw, err)
		}
	} else if cwd != "" && !pathWithinNotesScope(abs, cwd) {
		return "", fmt.Errorf("urls/add: path %q is outside the session scope %q", raw, cwd)
	}
	out := "file://" + abs
	if len([]rune(out)) > sessionURLMaxLen {
		return "", fmt.Errorf("urls/add: URL exceeds %d characters", sessionURLMaxLen)
	}
	return out, nil
}

// notesRootBoundary returns the RootBoundary view of a local execution
// environment rooted at cwd, matching the securepath-backed check the shell
// tool uses for cwd validation.
func notesRootBoundary(cwd string) (execenv.RootBoundary, bool) {
	env := execenv.NewLocalExecutionEnvironment(cwd)
	rb, ok := interface{}(env).(execenv.RootBoundary)
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
