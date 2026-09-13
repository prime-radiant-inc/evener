package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode"
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

// notesHumanNoteFieldKey is the persisted snapshot's JSON key for the canonical
// human note, as a quoted byte literal so the reader's absence pre-filter looks
// for the field rather than for the word appearing inside some string value.
const notesHumanNoteFieldKey = `"human_note"`

// ReadPersistedHumanNote extracts the top-level human_note value from a
// session's persisted mutation snapshot without decoding the journal or
// validating the snapshot. It exists for the hub's read-only past-session
// roster, which projects one note per entry and must not pay a full
// snapshot decode and validation per entry. ReadCanonicalHumanNote remains the
// strict authority for every caller that can act on a note; the roster only
// displays it.
//
// The tolerated rejections are deliberate and bounded to display: the document
// may carry unknown fields, a version or session_id the strict validator
// refuses, a journal or later fields that do not decode, or trailing bytes
// after the top-level object, and the first top-level human_note wins even if
// the strict decoder would take a later duplicate. Nothing after the value it
// finds is examined — that is what keeps the read cheap on journal-sized
// documents — so a note this reader returns is a projection, never authority.
//
// An absent snapshot file, a document that never spells the key, or a
// null human_note all return ("", false, nil): each of those cannot carry a
// note, so the roster reads them the same way. A document that does carry the
// key but fails to parse at or before the value, or whose human_note is not a
// string or null, returns an error.
func ReadPersistedHumanNote(stateDir, sessionID string) (note string, present bool, err error) {
	if err := schema.ValidateSessionID(sessionID); err != nil {
		return "", false, err
	}
	if stateDir == "" {
		return "", false, nil
	}
	data, err := afero.ReadFile(afero.NewOsFs(), clientMutationFilePath(stateDir, sessionID))
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read client mutation snapshot: %w", err)
	}
	// Absence is decided by the key's bytes rather than by parsing: a document
	// that never spells the top-level key cannot carry a note, so the roster
	// answers "no canonical note" from one scan of the bytes instead of a walk
	// over every value after the key's position. The roster pays this read once
	// per past entry and the absent case is the common one, which is why the
	// walk was worth removing. A false positive (the spelling inside some string
	// value) costs only the walk this used to do unconditionally.
	if !bytes.Contains(data, []byte(notesHumanNoteFieldKey)) {
		return "", false, nil
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	open, err := decoder.Token()
	if err != nil {
		return "", false, fmt.Errorf("decode client mutation snapshot: %w", err)
	}
	if delim, ok := open.(json.Delim); !ok || delim != '{' {
		return "", false, errors.New("decode client mutation snapshot: top-level value is not an object")
	}
	// skipValue consumes one complete JSON value without materializing it. It is
	// a closure rather than a package helper so no other caller can skip a value
	// that needs the strict decode.
	skipValue := func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		if delim != '{' && delim != '[' {
			return fmt.Errorf("unexpected delimiter %v", delim)
		}
		depth := 1
		for depth > 0 {
			token, err := decoder.Token()
			if err != nil {
				return err
			}
			if delim, ok := token.(json.Delim); ok {
				switch delim {
				case '{', '[':
					depth++
				case '}', ']':
					depth--
				}
			}
		}
		return nil
	}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return "", false, fmt.Errorf("decode client mutation snapshot: %w", err)
		}
		if key, _ := keyToken.(string); key == "human_note" {
			var value *string
			if err := decoder.Decode(&value); err != nil {
				return "", false, fmt.Errorf("decode client mutation snapshot: %w", err)
			}
			if value == nil {
				return "", false, nil
			}
			// The roster renders what this returns and never goes through a
			// Session, so the load-path strip has to happen here too.
			return stripNoteControls(*value), true, nil
		}
		if err := skipValue(); err != nil {
			return "", false, fmt.Errorf("decode client mutation snapshot: %w", err)
		}
	}
	// Consume the closing brace: a document truncated before it must read as an
	// error, not as an absence, and More() cannot tell the two apart.
	closing, err := decoder.Token()
	if err != nil {
		return "", false, fmt.Errorf("decode client mutation snapshot: %w", err)
	}
	if delim, ok := closing.(json.Delim); !ok || delim != '}' {
		return "", false, errors.New("decode client mutation snapshot: top-level object does not close")
	}
	return "", false, nil
}

// normalizeNote collapses every run of whitespace (including newlines) to one
// space, strips terminal control characters, and clamps to sessionNoteMaxRunes
// Unicode characters.
//
// The strip runs before the collapse and leaves the whitespace controls for it:
// stripping those first would join words ("a\nb" would store "ab"), and
// stripping after the collapse would leave a double space wherever a control sat
// between two spaces ("a \x1b b" would store "a  b"). What the strip removes is
// what the collapse cannot consume — ESC, DEL, the C1 introducers — which carry
// no meaning as note content while stored notes, labels, and URLs are printed by
// terminals (the TUI details drawer, the transcript's human-note echo, the notes
// tool output).
func normalizeNote(text string) string {
	text = stripNoteControls(text)
	collapsed := strings.Join(strings.Fields(text), " ")
	runes := []rune(collapsed)
	if len(runes) > sessionNoteMaxRunes {
		collapsed = string(runes[:sessionNoteMaxRunes])
	}
	return collapsed
}

// stripNoteControls removes every non-whitespace control character from text
// bound for stored notes state or a terminal, leaving the whitespace controls
// to the caller's collapse. It is the write-path rule and the load-path rule in
// one place: values persisted before the strip existed are normalized when they
// are read back again (the restore path, the mutation-snapshot load, and the
// roster's own reader), so a legacy note cannot reach a terminal.
func stripNoteControls(text string) string {
	if !strings.ContainsFunc(text, isNoteControl) {
		return text
	}
	return strings.Map(func(r rune) rune {
		if isNoteControl(r) {
			return -1
		}
		return r
	}, text)
}

// sanitizeRestoredURLs normalizes a persisted URL list on load. Labels are note
// text and are normalized like one, and a URL's own control characters are
// stripped: canonicalSessionURL refuses such input today, so only rows written
// before that check can carry any.
func sanitizeRestoredURLs(urls []schema.SessionURL) []schema.SessionURL {
	if len(urls) == 0 {
		return nil
	}
	sanitized := make([]schema.SessionURL, 0, len(urls))
	for _, entry := range urls {
		entry.URL = stripNoteControls(entry.URL)
		entry.Label = normalizeNote(entry.Label)
		sanitized = append(sanitized, entry)
	}
	return sanitized
}

// isNoteControl reports whether r is a control character the whitespace collapse
// cannot consume: C0 apart from the whitespace controls, DEL, and C1 apart from
// the C1 whitespace (NEL), which strings.Fields collapses like any other space.
func isNoteControl(r rune) bool {
	return unicode.IsControl(r) && !unicode.IsSpace(r)
}

// setAgentNote normalizes and clamps the agent whiteboard and publishes the
// resulting committed notes cut. It is the direct-commit entry point; the
// serialized notes mutators write through stageAgentNote instead, because their
// value is not committed until their metadata save lands.
func (s *Session) setAgentNote(note string) (stored string, changed bool) {
	stored, changed = s.stageAgentNote(note)
	s.publishStandaloneNotesCommit()
	return stored, changed
}

// stageAgentNote is setAgentNote's live-store write alone: no publication, for
// a mutator that owns the commit point and already holds notesUpdateMu.
func (s *Session) stageAgentNote(note string) (stored string, changed bool) {
	normalized := normalizeNote(note)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.agentNote == normalized {
		return s.agentNote, false
	}
	s.agentNote = normalized
	return normalized, true
}

// addSessionURL validates url plus label, canonicalizes the URL, appends it to
// the session list, and publishes the resulting committed notes cut. It is the
// direct-commit entry point (AddSessionURLForTest and the notes tests); the
// serialized notes mutators stage through stageSessionURLAdd and publish only
// after their metadata save lands.
func (s *Session) addSessionURL(rawURL, label string) (schema.SessionURL, error) {
	entry, err := s.stageSessionURLAdd(rawURL, label)
	if err != nil {
		return schema.SessionURL{}, err
	}
	s.publishStandaloneNotesCommit()
	return entry, nil
}

// stageSessionURLAdd is addSessionURL's live-store write alone: no
// publication, for a mutator that owns the commit point. A re-add of an
// existing canonical URL updates the label and returns the existing entry (id,
// addedBy, addedAt unchanged). It performs no fetch. The mutator persists
// (maybeAutoSave) and emits EventUrlsUpdated after a successful add.
func (s *Session) stageSessionURLAdd(rawURL, label string) (schema.SessionURL, error) {
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

// removeSessionURL deletes the entry with id, reporting whether one was found,
// and publishes the resulting committed notes cut when one was removed. It is
// the direct-commit entry point; the serialized notes mutators stage through
// stageSessionURLRemove instead.
func (s *Session) removeSessionURL(id string) bool {
	removed := s.stageSessionURLRemove(id)
	if removed {
		s.publishStandaloneNotesCommit()
	}
	return removed
}

// stageSessionURLRemove is removeSessionURL's live-store write alone: no
// publication, for a mutator that owns the commit point and already holds
// notesUpdateMu.
func (s *Session) stageSessionURLRemove(id string) bool {
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

// publishStandaloneNotesCommit publishes the live store after a direct-commit
// write (setAgentNote, addSessionURL, removeSessionURL — the store helpers the
// tests and AddSessionURLForTest call). The serialized notes mutators stage
// their write and hold notesUpdateMu until their metadata save has published,
// so taking the lock here serializes this publish with them and never publishes
// a staged value.
func (s *Session) publishStandaloneNotesCommit() {
	s.notesUpdateMu.Lock()
	defer s.notesUpdateMu.Unlock()
	s.publishCommittedNotesLocked()
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
	// A stored URL is printed by terminals (the TUI details drawer, the notes
	// tool output) and sent to the model, so a control character in the raw
	// input is refused rather than carried: url.Parse rejects ASCII controls but
	// accepts a C1 control, and it keeps one in RawQuery verbatim, which is how
	// a CSI introducer could reach a terminal from a stored link.
	if idx := strings.IndexFunc(trimmed, unicode.IsControl); idx >= 0 {
		control, _ := utf8.DecodeRuneInString(trimmed[idx:])
		return "", fmt.Errorf("urls/add: URL contains the control character %q", control)
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
