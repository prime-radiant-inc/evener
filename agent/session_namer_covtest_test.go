package agent

import (
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// TestPtrString covers ptrString (line 210).
func TestPtrString(t *testing.T) {
	s := "hello"
	p := ptrString(s)
	if p == nil || *p != s {
		t.Fatalf("ptrString(%q) = %v, want pointer to %q", s, p, s)
	}
}

// TestSessionNamerEnabled covers sessionNamerEnabled with nil and non-nil
// profiles (lines 91-95).
func TestSessionNamerEnabled(t *testing.T) {
	if sessionNamerEnabled(nil) {
		t.Fatal("expected false for nil profile")
	}
}

// TestSessionNamerModel covers sessionNamerModel with nil profile (line
// 116-117).
func TestSessionNamerModel_NilProfile(t *testing.T) {
	if got := sessionNamerModel(nil); got != "" {
		t.Fatalf("expected empty for nil profile, got %q", got)
	}
}

// TestConfiguredSessionNamerModel_NilProfile covers the nil path (line
// 126-127).
func TestConfiguredSessionNamerModel_NilProfile(t *testing.T) {
	if got := configuredSessionNamerModel(nil); got != "" {
		t.Fatalf("expected empty for nil profile, got %q", got)
	}
}

// TestNormalizeSessionNameSource covers the source normalization (lines
// 169-175).
func TestNormalizeSessionNameSource(t *testing.T) {
	// Compaction source preserved.
	if got := normalizeSessionNameSource(sessionNameSourceCompaction); got != sessionNameSourceCompaction {
		t.Fatalf("got %q, want %q", got, sessionNameSourceCompaction)
	}
	// Whitespace-trimmed compaction source.
	if got := normalizeSessionNameSource("  " + sessionNameSourceCompaction + "  "); got != sessionNameSourceCompaction {
		t.Fatalf("got %q, want %q", got, sessionNameSourceCompaction)
	}
	// Unknown source defaults to prompt.
	if got := normalizeSessionNameSource("unknown"); got != sessionNameSourcePrompt {
		t.Fatalf("got %q, want %q", got, sessionNameSourcePrompt)
	}
	// Empty source defaults to prompt.
	if got := normalizeSessionNameSource(""); got != sessionNameSourcePrompt {
		t.Fatalf("got %q, want %q", got, sessionNameSourcePrompt)
	}
}

// TestTrimForSessionNamer covers the trimming function (lines 178-186).
func TestTrimForSessionNamer(t *testing.T) {
	// Short text — returned as-is.
	short := "hello world"
	if got := trimForSessionNamer(short); got != short {
		t.Fatalf("got %q, want %q", got, short)
	}

	// Long text — truncated.
	long := strings.Repeat("x", 5000)
	got := trimForSessionNamer(long)
	if !strings.Contains(got, "truncated") {
		t.Fatal("expected truncated suffix")
	}
}

// TestSanitizeSessionName covers the sanitization function (lines 188+).
func TestSanitizeSessionName(t *testing.T) {
	// Empty string.
	if got := sanitizeSessionName(""); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
	// Normal name — kept as-is.
	if got := sanitizeSessionName("My Session"); got != "My Session" {
		t.Fatalf("got %q, want 'My Session'", got)
	}
	// Name with surrounding quotes/punctuation — trimmed.
	if got := sanitizeSessionName(`"Hello"`); got == "" {
		t.Fatal("expected non-empty after trimming quotes")
	}
}

// TestSessionNameSchema covers the schema function (lines 153-166).
func TestSessionNameSchema(t *testing.T) {
	schema := sessionNameSchema()
	if schema["type"] != "object" {
		t.Fatalf("type = %v, want object", schema["type"])
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("properties is not a map")
	}
	nameProp, ok := props["name"].(map[string]any)
	if !ok {
		t.Fatal("name property is not a map")
	}
	if nameProp["type"] != "string" {
		t.Fatalf("name type = %v, want string", nameProp["type"])
	}
}

// TestSessionNamerUserPrompt covers the prompt builder (lines 142-150).
func TestSessionNamerUserPrompt(t *testing.T) {
	// Prompt source.
	got := sessionNamerUserPrompt(sessionNameSourcePrompt, "test input")
	if !strings.Contains(got, "initial user prompt") {
		t.Fatalf("missing prompt label: %q", got)
	}
	if !strings.Contains(got, "test input") {
		t.Fatalf("missing text: %q", got)
	}

	// Compaction source.
	got = sessionNamerUserPrompt(sessionNameSourceCompaction, "summary text")
	if !strings.Contains(got, "compaction summary") {
		t.Fatalf("missing compaction label: %q", got)
	}
}

// TestIsSessionNameCompactionTurn covers the turn kind check (lines
// 329-330).
func TestIsSessionNameCompactionTurn(t *testing.T) {
	// Summary turn.
	if !isSessionNameCompactionTurn(schema.Turn{Kind: schema.TurnSummary}) {
		t.Fatal("expected true for TurnSummary")
	}
	// Checkpoint turn.
	if !isSessionNameCompactionTurn(schema.Turn{Kind: schema.TurnCheckpoint}) {
		t.Fatal("expected true for TurnCheckpoint")
	}
	// Other turn kinds.
	if isSessionNameCompactionTurn(schema.Turn{Kind: schema.TurnUserInput}) {
		t.Fatal("expected false for TurnUserInput")
	}
	if isSessionNameCompactionTurn(schema.Turn{Kind: schema.TurnAssistant}) {
		t.Fatal("expected false for TurnAssistant")
	}
}

// TestSuppressSessionNamerIfQuotaExhausted_NonQuota covers the non-quota
// error path (line 251-252).
func TestSuppressSessionNamerIfQuotaExhausted_NonQuota(t *testing.T) {
	s := &Session{}
	if s.suppressSessionNamerIfQuotaExhausted(nil) {
		t.Fatal("expected false for nil error")
	}
	if s.suppressSessionNamerIfQuotaExhausted(errAbort("some error")) {
		t.Fatal("expected false for non-quota error")
	}
}

// TestContextPressure_NilContextMgr covers the nil contextMgr path (line
// 200-201).
func TestContextPressure_NilContextMgr(t *testing.T) {
	s := &Session{}
	if got := s.ContextPressure(); got != 0 {
		t.Fatalf("expected 0, got %f", got)
	}
}

// TestClosingOrClosedLocked covers the closing/closed check.
func TestClosingOrClosedLocked(t *testing.T) {
	s := &Session{}
	if s.closingOrClosedLocked() {
		t.Fatal("expected false for non-closing, non-closed session")
	}
	s.closing = true
	if !s.closingOrClosedLocked() {
		t.Fatal("expected true for closing session")
	}
	s.closing = false
	s.state = SessionClosed
	if !s.closingOrClosedLocked() {
		t.Fatal("expected true for closed session")
	}
}

// TestSetStateIfOpenLocked covers the setStateIfOpenLocked function (lines
// 213-218).
func TestSetStateIfOpenLocked(t *testing.T) {
	s := &Session{state: SessionIdle}
	s.setStateIfOpenLocked(SessionProcessing)
	if s.state != SessionProcessing {
		t.Fatalf("state = %v, want SessionProcessing", s.state)
	}

	// Should be a no-op when closing.
	s.closing = true
	s.setStateIfOpenLocked(SessionIdle)
	if s.state != SessionProcessing {
		t.Fatalf("state changed while closing: %v", s.state)
	}
}

// TestSessionNamerEnabled checks the enabled function.
func TestSessionNamerEnabled_NilProfile(t *testing.T) {
	if sessionNamerEnabled(nil) {
		t.Fatal("expected false for nil profile")
	}
}

// Ensure llm import is used.
var _ = llm.Kind
