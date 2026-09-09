package agent

import (
	"fmt"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
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
	for i := 0; i < 50; i++ {
		if _, err := s.addSessionURL(fmt.Sprintf("https://x.test/%d", i), ""); err != nil {
			t.Fatalf("fill %d: %v", i, err)
		}
	}
	if _, err := s.addSessionURL("https://x.test/overflow", ""); err == nil {
		t.Fatalf("51st URL accepted")
	}
}

func TestSetHumanNoteClampThenCompares(t *testing.T) {
	s := newTestNotesSession(t, "/tmp/proj")
	stored, changed := s.setHumanNote("  hello   world  ")
	if stored != "hello world" || !changed {
		t.Fatalf("set = %q, %v; want %q, true", stored, changed, "hello world")
	}
	if stored, changed := s.setHumanNote("hello world"); stored != "hello world" || changed {
		t.Fatalf("re-set = %q, %v; want no-op", stored, changed)
	}
	long := strings.Repeat("b", 2000)
	stored, changed = s.setHumanNote(long)
	if len([]rune(stored)) != 1000 || !changed {
		t.Fatalf("clamped set = len %d, %v; want len 1000, true", len([]rune(stored)), changed)
	}
	if stored, changed := s.setHumanNote(long); len([]rune(stored)) != 1000 || changed {
		t.Fatalf("over-length re-set = len %d, %v; want no-op", len([]rune(stored)), changed)
	}
	if stored, changed := s.setHumanNote(""); stored != "" || !changed {
		t.Fatalf("clear = %q, %v; want empty, true", stored, changed)
	}
}

func TestSetAgentNoteStoresSeparately(t *testing.T) {
	s := newTestNotesSession(t, "/tmp/proj")
	if _, changed := s.setAgentNote("agent work"); !changed {
		t.Fatalf("agent set not reported as change")
	}
	s.mu.Lock()
	human, agent := s.humanNote, s.agentNote
	s.mu.Unlock()
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
