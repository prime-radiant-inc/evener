package agent

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

// newTestNotesSession builds a minimal Session with the given working
// directory and empty notes state.
func newTestNotesSession(t *testing.T, cwd string) *Session {
	t.Helper()
	return &Session{env: execenv.NewLocalExecutionEnvironment(cwd)}
}

// sessionURLsForTest returns a copy of the session's URL list.
func (s *Session) sessionURLsForTest() []schema.SessionURL {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]schema.SessionURL(nil), s.sessionURLs...)
}

func TestNormalizeNoteCollapsesWhitespaceAndClamps(t *testing.T) {
	in := "  hello\n\n  world\t\tfoo  "
	if got := normalizeNote(in); got != "hello world foo" {
		t.Fatalf("normalizeNote(%q) = %q", in, got)
	}
	long := strings.Repeat("a", 2000)
	if got := normalizeNote(long); len([]rune(got)) != 1000 {
		t.Fatalf("clamped length = %d, want 1000", len([]rune(got)))
	}
}

func TestAddSessionURLDedupsCanonically(t *testing.T) {
	s := newTestNotesSession(t, "/tmp/proj")
	a, err := s.addSessionURL("docs/x.md", "first")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	b, err := s.addSessionURL("./docs/x.md", "second")
	if err != nil {
		t.Fatalf("re-add: %v", err)
	}
	if a.ID != b.ID || b.Label != "second" {
		t.Fatalf("dedup = %+v vs %+v, want same id with updated label", a, b)
	}
	if n := len(s.sessionURLsForTest()); n != 1 {
		t.Fatalf("list length = %d, want 1", n)
	}
}

func TestAddSessionURLRejectsOverCapAndBadScheme(t *testing.T) {
	s := newTestNotesSession(t, "/tmp/proj")
	if _, err := s.addSessionURL("gopher://x.test/y", ""); err == nil {
		t.Fatalf("bad scheme accepted")
	}
	for i := range 50 {
		if _, err := s.addSessionURL(fmt.Sprintf("https://x.test/%d", i), ""); err != nil {
			t.Fatalf("fill %d: %v", i, err)
		}
	}
	if _, err := s.addSessionURL("https://x.test/overflow", ""); err == nil {
		t.Fatalf("51st URL accepted")
	}
}

// TestCanonicalSessionURLRejectsSchemalessNonWebSchemes covers the scheme
// bypass: inputs without "://" that still name a URI scheme
// (javascript:, data:, mailto:) must be rejected, not stored as file://
// entries.
func TestCanonicalSessionURLRejectsSchemalessNonWebSchemes(t *testing.T) {
	for _, raw := range []string{
		"javascript:alert(1)",
		"JaVaScRiPt:alert(1)",
		"data:text/plain,hi",
		"mailto:foo@bar",
		"ftp:example.com/file",
	} {
		if got, err := canonicalSessionURL(raw, "/tmp/proj"); err == nil {
			t.Fatalf("canonicalSessionURL(%q) = %q, want rejection", raw, got)
		}
	}
}

// TestCanonicalSessionURLRejectsUserinfo covers credential-bearing http(s)
// URLs: userinfo must be rejected, never canonicalized with credentials
// intact into the persisted list.
func TestCanonicalSessionURLRejectsUserinfo(t *testing.T) {
	for _, raw := range []string{
		"https://user:secret@example.com/",
		"https://user@example.com/y",
		"http://user:pw@x.test:8080/y",
	} {
		if got, err := canonicalSessionURL(raw, "/tmp/proj"); err == nil {
			t.Fatalf("canonicalSessionURL(%q) = %q, want rejection", raw, got)
		}
	}
}

// TestCanonicalSessionURLKeepsColonPathsWithoutSchemes pins the other side
// of the scheme gate: inputs whose pre-colon segment is not a valid scheme
// (a slash, a leading digit, an empty segment) still resolve as bare paths.
func TestCanonicalSessionURLKeepsColonPathsWithoutSchemes(t *testing.T) {
	for _, raw := range []string{"docs/a:b.md", "10:30 note.md", "/tmp/proj/a:b.md"} {
		if _, err := canonicalSessionURL(raw, "/tmp/proj"); err != nil {
			t.Fatalf("canonicalSessionURL(%q): %v", raw, err)
		}
	}
}

// TestCanonicalSessionURLRejectsEmptyHostname covers URLs whose Host is
// non-empty but whose hostname is empty ("http://:80"): the Host check
// passes, but the canonical form builds from Hostname() and would persist
// an unusable link.
func TestCanonicalSessionURLRejectsEmptyHostname(t *testing.T) {
	for _, raw := range []string{"http://:80", "https://:443/y", "http://@/y"} {
		if got, err := canonicalSessionURL(raw, "/tmp/proj"); err == nil {
			t.Fatalf("canonicalSessionURL(%q) = %q, want rejection", raw, got)
		}
	}
}

// TestCanonicalSessionURLRejectsFileQueryFragment covers file URLs carrying
// a query or fragment: they are path metadata, not path content, and must
// be rejected rather than silently collapsed onto the bare path.
func TestCanonicalSessionURLRejectsFileQueryFragment(t *testing.T) {
	for _, raw := range []string{"file:///tmp/proj/a.md?bar", "file:///tmp/proj/a.md#frag"} {
		if got, err := canonicalSessionURL(raw, "/tmp/proj"); err == nil {
			t.Fatalf("canonicalSessionURL(%q) = %q, want rejection", raw, got)
		}
	}
}

// TestCanonicalSessionURLFileEscapedSeparators covers encoded separators in
// file URLs: an encoded "%2F" must not change path hierarchy before the
// scope check — the escaped form scopes, then unescapes for storage.
func TestCanonicalSessionURLFileEscapedSeparators(t *testing.T) {
	got, err := canonicalSessionURL("file:///tmp/proj/docs%2Fa.md", "/tmp/proj")
	if err != nil {
		t.Fatalf("canonicalSessionURL escaped slash: %v", err)
	}
	if got != "file:///tmp/proj/docs/a.md" {
		t.Fatalf("canonicalSessionURL escaped slash = %q, want decoded storage form", got)
	}
	if _, err := canonicalSessionURL("file:///tmp%2F..%2Fetc%2Fpasswd", "/tmp/proj"); err == nil {
		t.Fatalf("canonicalSessionURL escaped traversal accepted, want rejection")
	}
}

// TestCanonicalSessionURLRejectsAbsoluteWithoutCWD covers absolute file URLs
// with no session working directory (nil env): with no scope to check
// against, they must be rejected fail-closed, not accepted unchecked.
func TestCanonicalSessionURLRejectsAbsoluteWithoutCWD(t *testing.T) {
	for _, raw := range []string{"file:///etc/passwd", "file:///tmp/proj/a.md"} {
		if got, err := canonicalSessionURL(raw, ""); err == nil {
			t.Fatalf("canonicalSessionURL(%q) without cwd = %q, want rejection", raw, got)
		}
	}
}

func TestSetHumanNoteClampThenCompares(t *testing.T) {
	s := newNotesToolSession(t)
	long := strings.Repeat("界", 2000)
	for i, tc := range []struct {
		input, want string
		projection  appwire.MutationProjectionState
	}{
		{"  hello   world  ", "hello world", appwire.MutationProjectionPending},
		{"hello world", "hello world", appwire.MutationProjectionRemoved},
		{long, strings.Repeat("界", 1000), appwire.MutationProjectionPending},
		{long, strings.Repeat("界", 1000), appwire.MutationProjectionRemoved},
		{"", "", appwire.MutationProjectionPending},
	} {
		response, err := s.SetHumanNote(fmt.Sprintf("save-%d", i), tc.input)
		if err != nil {
			t.Fatal(err)
		}
		if response.Note != tc.want || response.Receipt.ProjectionState != tc.projection {
			t.Fatalf("save %d = %+v", i, response)
		}
	}
}

func TestSetAgentNoteStoresSeparately(t *testing.T) {
	s := newTestNotesSession(t, "/tmp/proj")
	if _, changed := s.setAgentNote("agent work"); !changed {
		t.Fatalf("agent set not reported as change")
	}
	human, agent := s.notesSnapshot()
	if human != "" || agent != "agent work" {
		t.Fatalf("notes = %q, %q; want empty human, agent stored", human, agent)
	}
}

func TestRemoveSessionURLByID(t *testing.T) {
	s := newTestNotesSession(t, "/tmp/proj")
	a, err := s.addSessionURL("https://x.test/y", "")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if !s.removeSessionURL(a.ID) {
		t.Fatalf("remove of %q returned false", a.ID)
	}
	if len(s.sessionURLsForTest()) != 0 {
		t.Fatalf("list not empty after remove")
	}
	if s.removeSessionURL(a.ID) {
		t.Fatalf("second remove returned true")
	}
	if s.removeSessionURL("nonexistent") {
		t.Fatalf("remove of unknown id returned true")
	}
}

func TestCanonicalSessionURLNormalizesHTTP(t *testing.T) {
	cases := map[string]string{
		"HTTPS://X.TEST/":             "https://x.test",
		"https://x.test":              "https://x.test",
		"https://x.test/#frag":        "https://x.test",
		"http://x.test:80/a":          "http://x.test/a",
		"https://x.test:443/a":        "https://x.test/a",
		"https://x.test:8443/a":       "https://x.test:8443/a",
		"HTTP://X.TEST:80/a?b=c#frag": "http://x.test/a?b=c",
	}
	for raw, want := range cases {
		got, err := canonicalSessionURL(raw, "/tmp/proj")
		if err != nil {
			t.Fatalf("canonicalSessionURL(%q): %v", raw, err)
		}
		if got != want {
			t.Fatalf("canonicalSessionURL(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestCanonicalSessionURLFileScope(t *testing.T) {
	got, err := canonicalSessionURL("docs/x.md", "/tmp/proj")
	if err != nil {
		t.Fatalf("bare path: %v", err)
	}
	if got != "file:///tmp/proj/docs/x.md" {
		t.Fatalf("bare path canonical = %q", got)
	}
	abs, err := canonicalSessionURL("file:///tmp/proj/docs/x.md", "/tmp/proj")
	if err != nil {
		t.Fatalf("file URL: %v", err)
	}
	if abs != got {
		t.Fatalf("bare %q vs file URL %q do not collide", got, abs)
	}
	if _, err := canonicalSessionURL("../escape.md", "/tmp/proj"); err == nil {
		t.Fatalf("out-of-scope bare path accepted")
	}
	if _, err := canonicalSessionURL("file:///etc/passwd", "/tmp/proj"); err == nil {
		t.Fatalf("out-of-scope file URL accepted")
	}
}

func TestAddSessionURLRejectsOverLength(t *testing.T) {
	s := newTestNotesSession(t, "/tmp/proj")
	longURL := "https://x.test/" + strings.Repeat("a", 2048)
	if _, err := s.addSessionURL(longURL, ""); err == nil {
		t.Fatalf("over-length URL accepted")
	}
	if _, err := s.addSessionURL("https://x.test/ok", strings.Repeat("l", 281)); err == nil {
		t.Fatalf("over-length label accepted")
	}
}

func TestAddSessionURLDedupKeepsIdentity(t *testing.T) {
	s := newTestNotesSession(t, "/tmp/proj")
	a, err := s.addSessionURL("https://x.test/y", "first")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if a.ID == "" || a.AddedBy != "agent" || a.AddedAt == 0 {
		t.Fatalf("entry missing server fields: %+v", a)
	}
	b, err := s.addSessionURL("https://x.test/y#other", "second")
	if err != nil {
		t.Fatalf("re-add: %v", err)
	}
	if b.ID != a.ID || b.AddedBy != a.AddedBy || b.AddedAt != a.AddedAt {
		t.Fatalf("re-add changed identity: %+v vs %+v", a, b)
	}
}

// TestCanonicalFilePathEscapesDelimiters verifies the M6 contract: filenames
// containing URL delimiters (#, ?, %) serialize escaped through net/url, and
// a bare path and its canonical file:/// URL dedup to the same entry (the
// pre-fix "file://" + abs concat let docs/a#b.md round-trip as path docs/a
// with fragment b.md).
func TestCanonicalFilePathEscapesDelimiters(t *testing.T) {
	for _, name := range []string{"docs/a#b.md", "docs/a?b.md", "docs/a%b.md"} {
		got, err := canonicalSessionURL(name, "/tmp/proj")
		if err != nil {
			t.Fatalf("canonicalSessionURL(%q): %v", name, err)
		}
		parsed, err := url.Parse(got)
		if err != nil {
			t.Fatalf("parse canonical %q: %v", got, err)
		}
		if parsed.Fragment != "" || parsed.RawQuery != "" {
			t.Fatalf("canonical %q split into fragment/query: path=%q frag=%q query=%q", got, parsed.Path, parsed.Fragment, parsed.RawQuery)
		}
		wantPath := "/tmp/proj/" + name
		if parsed.Path != wantPath {
			t.Fatalf("canonical %q decodes to path %q, want %q", got, parsed.Path, wantPath)
		}
		// The canonical file:/// URL re-adds to the same entry (dedup).
		s := newTestNotesSession(t, "/tmp/proj")
		a, err := s.addSessionURL(name, "")
		if err != nil {
			t.Fatalf("add %q: %v", name, err)
		}
		b, err := s.addSessionURL(got, "")
		if err != nil {
			t.Fatalf("re-add %q: %v", got, err)
		}
		if a.ID != b.ID || a.URL != b.URL {
			t.Fatalf("dedup mismatch: bare %q -> %+v vs canonical %q -> %+v", name, a, got, b)
		}
	}
}

// TestSetHumanNoteStoresAndSteers verifies the daemon human-set path: the
// note stores, one EventNotesUpdated emits, and one human-note steer lands in
// the durable steering queue under the derived inner id.
func TestSetHumanNoteStoresAndSteers(t *testing.T) {
	s := newNotesToolSession(t)
	defer s.Close()
	storedResponse, err := s.SetHumanNote("outer-1", "hello world")
	stored := storedResponse.Note
	if err != nil {
		t.Fatalf("SetHumanNote: %v", err)
	}
	if stored != "hello world" {
		t.Fatalf("stored = %q, want %q", stored, "hello world")
	}
	data, ok := nextNotesEvent(t, s, events.EventNotesUpdated).(events.NotesUpdatedData)
	if !ok {
		t.Fatalf("NOTES_UPDATED payload = %T", nextNotesEvent(t, s, events.EventNotesUpdated))
	}
	if data.HumanNote != "hello world" {
		t.Fatalf("NOTES_UPDATED human note = %q, want %q", data.HumanNote, "hello world")
	}
	s.mu.Lock()
	queue := append([]steeringMessage(nil), s.steeringQueue...)
	s.mu.Unlock()
	if len(queue) != 1 {
		t.Fatalf("steering queue length = %d, want 1", len(queue))
	}
	if queue[0].ClientMutationID != "outer-1" {
		t.Fatalf("inner steer id = %q, want %q", queue[0].ClientMutationID, "outer-1")
	}
	if queue[0].Kind != events.SteeringKindHumanNote {
		t.Fatalf("inner steer kind = %q, want %q", queue[0].Kind, events.SteeringKindHumanNote)
	}
	if !strings.Contains(queue[0].Text, "hello world") {
		t.Fatalf("inner steer text = %q", queue[0].Text)
	}
}

// TestSetHumanNoteRetryOfOneOuterIDSteersOnce verifies the no-double-interrupt
// contract: retries replay the same atomic save without duplicating notification.
func TestSetHumanNoteRetryOfOneOuterIDSteersOnce(t *testing.T) {
	s := newNotesToolSession(t)
	defer s.Close()
	if _, err := s.SetHumanNote("outer-9", "same text"); err != nil {
		t.Fatalf("SetHumanNote: %v", err)
	}
	if _, err := s.SetHumanNote("outer-9", "same text"); err != nil {
		t.Fatalf("outer retry: %v", err)
	}
	resp, err := s.SetHumanNote("outer-9", "same text")
	if err != nil {
		t.Fatalf("inner retry: %v", err)
	}
	if resp.Receipt.Disposition != appwire.MutationDispositionReplayed {
		t.Fatalf("inner retry disposition = %q, want %q", resp.Receipt.Disposition, appwire.MutationDispositionReplayed)
	}
	s.mu.Lock()
	n := len(s.steeringQueue)
	s.mu.Unlock()
	if n != 1 {
		t.Fatalf("steering queue length = %d, want exactly 1", n)
	}
}

// TestSetHumanNoteNoOpOnEqualText verifies equal-text saves (including an
// empty save on an already-empty note) return the current value with no event
// and no steer.
func TestSetHumanNoteNoOpOnEqualText(t *testing.T) {
	s := newNotesToolSession(t)
	defer s.Close()
	if stored, err := s.SetHumanNote("outer-empty", ""); err != nil || stored.Note != "" {
		t.Fatalf("empty save on empty note = %q, %v; want empty, nil", stored, err)
	}
	s.mu.Lock()
	n := len(s.steeringQueue)
	s.mu.Unlock()
	if n != 0 {
		t.Fatalf("steering queue length = %d, want 0 after no-op", n)
	}
}

// TestSetHumanNoteClearUsesClearedMarker verifies the non-empty→empty
// transition notifies with the cleared marker instead of empty inline text.
func TestSetHumanNoteClearUsesClearedMarker(t *testing.T) {
	s := newNotesToolSession(t)
	defer s.Close()
	if _, err := s.SetHumanNote("outer-1", "something"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if stored, err := s.SetHumanNote("outer-2", ""); err != nil || stored.Note != "" {
		t.Fatalf("clear = %q, %v; want empty, nil", stored, err)
	}
	s.mu.Lock()
	queue := append([]steeringMessage(nil), s.steeringQueue...)
	s.mu.Unlock()
	if len(queue) != 2 {
		t.Fatalf("steering queue length = %d, want 2", len(queue))
	}
	if queue[1].Text != "human updated their whiteboard: (whiteboard cleared)" {
		t.Fatalf("clear steer text = %q, want cleared marker", queue[1].Text)
	}
}

// TestRemoveSessionURLDaemonPath verifies the daemon URL-remove path removes
// by id and emits EventUrlsUpdated with the same shape the agent tools emit.
func TestRemoveSessionURLDaemonPath(t *testing.T) {
	s := newNotesToolSession(t)
	defer s.Close()
	a, err := s.addSessionURL("https://x.test/y", "")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	removed, err := s.RemoveSessionURL("outer-rm-1", a.ID)
	if err != nil || !removed {
		t.Fatalf("remove = %v, %v; want true, nil", removed, err)
	}
	if removed, _ := s.RemoveSessionURL("outer-unknown", "nonexistent"); removed {
		t.Fatalf("remove of unknown id returned true")
	}
	if _, ok := nextNotesEvent(t, s, events.EventUrlsUpdated).(events.UrlsUpdatedData); !ok {
		t.Fatal("URLS_UPDATED payload has wrong type")
	}
	if got := s.sessionURLsForTest(); len(got) != 0 {
		t.Fatalf("url list after remove = %+v, want empty", got)
	}
}

// TestNotesContextBlockContainsNotesAndURLs verifies the agent context
// injection renders the current notes plus URL list, and renders nothing when
// the store is empty.
func TestNotesContextBlockContainsNotesAndURLs(t *testing.T) {
	s := newNotesToolSession(t)
	defer s.Close()
	if got := s.notesContextBlock(); got != "" {
		t.Fatalf("empty block = %q, want empty", got)
	}
	if _, err := s.SetHumanNote("fixture", "human hello"); err != nil {
		t.Fatal(err)
	}
	if _, changed := s.setAgentNote("agent hello"); !changed {
		t.Fatal("agent set not reported as change")
	}
	if _, err := s.addSessionURL("https://x.test/y", "why"); err != nil {
		t.Fatalf("add: %v", err)
	}
	block := s.notesContextBlock()
	for _, want := range []string{"human hello", "agent hello", "https://x.test/y", "why"} {
		if !strings.Contains(block, want) {
			t.Fatalf("context block = %q, want it to contain %q", block, want)
		}
	}
	s.maybeAppendNotesContext()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.history) != 1 || s.history[0].Kind != schema.TurnNotesContext {
		t.Fatalf("history kinds = %+v, want one NOTES_CONTEXT turn", s.history)
	}
}

// TestNotesProjectionAppendsOnceWhenUnchanged verifies the M4 change gate:
// two consecutive projections with no notes change append exactly one
// NOTES_CONTEXT turn, and a later change appends again.
func TestNotesProjectionAppendsOnceWhenUnchanged(t *testing.T) {
	s := newNotesToolSession(t)
	defer s.Close()
	if _, err := s.SetHumanNote("fixture", "human hello"); err != nil {
		t.Fatal(err)
	}
	s.maybeAppendNotesContext()
	s.maybeAppendNotesContext()
	s.mu.Lock()
	n := len(s.history)
	s.mu.Unlock()
	if n != 1 {
		t.Fatalf("history length after two unchanged projections = %d, want 1", n)
	}
	if _, changed := s.setAgentNote("agent hello"); !changed {
		t.Fatal("agent set not reported as change")
	}
	s.maybeAppendNotesContext()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.history) != 2 {
		t.Fatalf("history length after changed projection = %d, want 2", len(s.history))
	}
}

// TestNotesProjectionStillProjectsAfterCompaction verifies the resume/
// compaction guarantee survives the M4 change gate: after compaction folds
// history away, the next projection re-emits the current state the model can
// no longer see instead of staying silent.
func TestNotesProjectionStillProjectsAfterCompaction(t *testing.T) {
	s := newNotesToolSession(t)
	defer s.Close()
	if _, err := s.SetHumanNote("fixture", "human hello"); err != nil {
		t.Fatal(err)
	}
	s.maybeAppendNotesContext()
	s.resetNotesProjectionAfterCompaction()
	s.maybeAppendNotesContext()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.history) != 2 {
		t.Fatalf("history length after compaction reset = %d, want 2 (re-projection)", len(s.history))
	}
	if s.history[1].Message.Text() != s.history[0].Message.Text() {
		t.Fatalf("re-projected block = %q, want %q", s.history[1].Message.Text(), s.history[0].Message.Text())
	}
}

// TestNotesRemoveAllProjectsEmptySnapshot verifies the M3 contract:
// fill→remove-all→next projection appends the explicit empty snapshot (not
// silence), so the next model request reflects the cleared list instead of
// the stale pre-removal rows.
func TestNotesRemoveAllProjectsEmptySnapshot(t *testing.T) {
	s := newNotesToolSession(t)
	defer s.Close()
	entry, err := s.addSessionURL("https://x.test/y", "why")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	s.maybeAppendNotesContext()
	if !s.removeSessionURL(entry.ID) {
		t.Fatalf("remove of %q returned false", entry.ID)
	}
	s.maybeAppendNotesContext()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.history) != 2 {
		t.Fatalf("history length after fill→remove-all = %d, want 2 (snapshot + explicit empty)", len(s.history))
	}
	last := s.history[1].Message.Text()
	if !strings.Contains(last, "empty") {
		t.Fatalf("empty snapshot = %q, want the explicit cleared marker", last)
	}
	if strings.Contains(last, "https://x.test/y") {
		t.Fatalf("empty snapshot = %q, want no stale rows", last)
	}
}

// TestNotesNeverPopulatedProjectsNothing verifies the M3 boundary: a fresh
// session whose store was never non-empty projects nothing, keeping its
// history byte-identical.
func TestNotesNeverPopulatedProjectsNothing(t *testing.T) {
	s := newNotesToolSession(t)
	defer s.Close()
	if got := s.notesContextBlock(); got != "" {
		t.Fatalf("empty block = %q, want empty", got)
	}
	s.maybeAppendNotesContext()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.history) != 0 {
		t.Fatalf("history length = %d, want 0 (never-populated projects nothing)", len(s.history))
	}
}

// TestHumanNoteSteerKindSurvivesDurableReconstruction verifies the M5
// contract: the human-note steer's kind is stamped on the durable journal
// record (not just the reflected in-memory entry), so rebuilding the runtime
// queue from the snapshot — the restart path — restores the kind the divider
// renders from. Plain user steering keeps its empty kind.
func TestHumanNoteSteerKindSurvivesDurableReconstruction(t *testing.T) {
	s := newNotesToolSession(t)
	defer s.Close()
	if _, err := s.SetHumanNote("outer-kind-1", "hello world"); err != nil {
		t.Fatalf("SetHumanNote: %v", err)
	}
	s.mu.Lock()
	live := append([]steeringMessage(nil), s.steeringQueue...)
	s.mu.Unlock()
	if len(live) != 1 || live[0].Kind != events.SteeringKindHumanNote {
		t.Fatalf("live steer kind = %+v, want one human-note entry", live)
	}
	rebuilt := clientSteeringFromSnapshot(s.clientMutations.snapshot())
	if len(rebuilt) != 1 {
		t.Fatalf("rebuilt steering length = %d, want 1", len(rebuilt))
	}
	if rebuilt[0].Kind != events.SteeringKindHumanNote {
		t.Fatalf("rebuilt steer kind = %q, want %q", rebuilt[0].Kind, events.SteeringKindHumanNote)
	}
	if rebuilt[0].Source != events.SteeringSourceUser {
		t.Fatalf("rebuilt steer source = %q, want %q (user provenance retained)", rebuilt[0].Source, events.SteeringSourceUser)
	}
	// The rebuilt entry must drain to a steering turn carrying the kind, so
	// the projection (and its SteeringInjected event) labels the divider.
	s.injectDrainedSteering()
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for _, turn := range s.history {
		if turn.Kind == schema.TurnSteering && turn.SteeringKind == events.SteeringKindHumanNote {
			found = true
		}
	}
	if !found {
		t.Fatalf("no TurnSteering with human-note kind in history %+v", s.history)
	}
}
