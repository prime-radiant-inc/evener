package agent

import (
	"strings"
	"testing"
	"unicode"
)

// Stored notes text is rendered by terminals: the TUI details drawer prints the
// human note, the agent note, and every URL line, the transcript echoes the
// human-note steering message, and the notes tool prints its own output.
// Anywhere the text carries the control characters a terminal executes, an OSC
// or CSI sequence in a note, label, or URL could retitle, recolor, or reposition
// the terminal that displays it.
func TestNormalizeNoteStripsTerminalControlSequences(t *testing.T) {
	t.Parallel()
	cases := map[string]struct{ in, want string }{
		"CSI color":            {"safe \x1b[31mred\x1b[0m text", "safe [31mred[0m text"},
		"OSC title with BEL":   {"\x1b]0;owned\x07after", "]0;ownedafter"},
		"bare BEL":             {"ding\x07dong", "dingdong"},
		"C1 CSI":               {"c1 \u009b31m red", "c1 31m red"},
		"DEL":                  {"del\x7feted", "deleted"},
		"backspaces":           {"a\x08\x08\x08b", "ab"},
		"NUL":                  {"a\x00b", "ab"},
		"control plus newline": {"line1\nline2\x1btail", "line1 line2tail"},
		"controls only":        {"\x1b\x07\x7f", ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := normalizeNote(tc.in); got != tc.want {
				t.Fatalf("normalizeNote(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The strip has to hold at every surface that stores note text, not only inside
// normalizeNote: the human whiteboard (whose stored value is echoed into the
// steering message the transcript renders), the agent whiteboard, and the URL
// list's label and URL. A test that only checked normalizeNote would still pass
// if a surface bypassed it.
func TestStoredNoteSurfacesCarryNoTerminalControls(t *testing.T) {
	t.Parallel()
	const payload = "\x1b]0;owned\x07note\u009b31m\x7f"
	assertNoControls := func(t *testing.T, surface, text string) {
		t.Helper()
		for _, r := range text {
			if unicode.IsControl(r) {
				t.Fatalf("%s = %q carries control rune %U", surface, text, r)
			}
		}
	}

	t.Run("human note", func(t *testing.T) {
		s := newNotesToolSession(t)
		defer s.Close()
		response, err := s.SetHumanNote("outer-controls", payload)
		if err != nil {
			t.Fatalf("SetHumanNote: %v", err)
		}
		assertNoControls(t, "stored human note", response.Note)
		canonical, _ := s.notesSnapshot()
		assertNoControls(t, "canonical human note", canonical)
		s.mu.Lock()
		queue := append([]steeringMessage(nil), s.steeringQueue...)
		s.mu.Unlock()
		if len(queue) != 1 {
			t.Fatalf("steering queue length = %d, want 1", len(queue))
		}
		assertNoControls(t, "human-note steering text", queue[0].Text)
		if !strings.Contains(response.Note, "note") {
			t.Fatalf("stored human note %q lost the printable part of the input", response.Note)
		}
	})

	t.Run("agent note", func(t *testing.T) {
		s := newTestNotesSession(t, "/tmp/proj")
		stored, changed := s.setAgentNote(payload)
		if !changed {
			t.Fatal("setAgentNote reported no change")
		}
		assertNoControls(t, "stored agent note", stored)
		if !strings.Contains(stored, "note") {
			t.Fatalf("stored agent note %q lost the printable part of the input", stored)
		}
	})

	t.Run("URL label", func(t *testing.T) {
		s := newTestNotesSession(t, "/tmp/proj")
		entry, err := s.addSessionURL("https://x.test/y", payload)
		if err != nil {
			t.Fatalf("addSessionURL: %v", err)
		}
		assertNoControls(t, "stored URL label", entry.Label)
		if !strings.Contains(entry.Label, "note") {
			t.Fatalf("stored URL label %q lost the printable part of the input", entry.Label)
		}
	})

	t.Run("web URL", func(t *testing.T) {
		s := newTestNotesSession(t, "/tmp/proj")
		entry, err := s.addSessionURL("https://x.test/\x1b]0;owned\x07", "")
		if err != nil {
			// Rejecting the URL outright is an equally safe outcome; what must
			// never happen is a stored value that carries the sequence.
			return
		}
		assertNoControls(t, "stored web URL", entry.URL)
	})

	t.Run("file path", func(t *testing.T) {
		s := newTestNotesSession(t, "/tmp/proj")
		entry, err := s.addSessionURL("a\x1bb.md", "")
		if err != nil {
			return
		}
		assertNoControls(t, "stored file URL", entry.URL)
	})
}

// The unknown-id error echoes the caller's id back through the RPC, and the TUI
// renders RPC errors in the transcript, so the echo must not carry control
// sequences either.
func TestUnknownURLRemoveIDErrorCannotDriveATerminal(t *testing.T) {
	t.Parallel()
	s := newNotesToolSession(t)
	defer s.Close()
	removed, err := s.RemoveSessionURL("outer-control-id", "\x1b]0;owned\x07")
	if removed || err == nil {
		t.Fatalf("unknown remove = %v, %v; want false, error", removed, err)
	}
	if !strings.Contains(err.Error(), "no URL entry with id") {
		t.Fatalf("error %q does not name the unknown id", err.Error())
	}
	for _, r := range err.Error() {
		if unicode.IsControl(r) {
			t.Fatalf("unknown-id error = %q carries control rune %U", err.Error(), r)
		}
	}
}

// A URL is printed by terminals (the TUI details drawer and the notes tool
// output) and sent to the model, so the stored URL must not be able to carry a
// control sequence either. url.Parse only rejects ASCII controls; a C1 control
// such as U+009B (CSI) is a multi-byte rune it accepts and keeps in RawQuery, so
// the raw input has to be scanned before parsing rather than trusted to the
// parser.
func TestCanonicalSessionURLRejectsControlCharacters(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"C1 CSI in query": "https://x.test/y?q=\u009b31m",
		"C1 CSI in path":  "https://x.test/\u009b31m",
		"C1 OSC in query": "https://x.test/y?q=\u009d0;owned",
		"BEL in query":    "https://x.test/y?q=\x07",
		"DEL in path":     "https://x.test/y\x7f",
		"escape in path":  "https://x.test/y\x1b[31m",
		"bare file path":  "a\u009bb.md",
		"file URL":        "file:///tmp/a\u009bb.md",
		"control in host": "https://x.te\u009bst/",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := canonicalSessionURL(in, "/tmp/proj")
			if err == nil {
				t.Fatalf("canonicalSessionURL(%q) = %q, want a control-character rejection", in, got)
			}
			for _, r := range err.Error() {
				if unicode.IsControl(r) {
					t.Fatalf("rejection error %q carries control rune %U", err.Error(), r)
				}
			}
		})
	}

	// Percent-encoded bytes are ordinary text by the time they are stored, so a
	// caller that means to name such a URL still can.
	got, err := canonicalSessionURL("https://x.test/y?q=%C2%9B", "/tmp/proj")
	if err != nil {
		t.Fatalf("percent-encoded URL rejected: %v", err)
	}
	for _, r := range got {
		if unicode.IsControl(r) {
			t.Fatalf("canonicalSessionURL stored %q with control rune %U", got, r)
		}
	}
}

// Stripping controls must not break the collapse's single-space invariant: a
// control sitting between two spaces disappears, and the caller sees one space.
// Stripping after the collapse (as this fix first did) turns "a \x1b b" into
// "a  b", which is text the user never wrote.
func TestNormalizeNoteKeepsSingleSpacesWhenStrippingControls(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"control between spaces":   "a \x1b b",
		"controls between spaces":  "a \x1b\x07 b",
		"C1 between spaces":        "a \u009b b",
		"control at the end":       "a \x1b",
		"control at the start":     "\x1b a",
		"control between newlines": "a\n\x1b\nb",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			got := normalizeNote(in)
			for _, r := range got {
				if unicode.IsControl(r) {
					t.Fatalf("normalizeNote(%q) = %q carries control rune %U", in, got, r)
				}
			}
			if strings.Contains(got, "  ") {
				t.Fatalf("normalizeNote(%q) = %q left a double space", in, got)
			}
		})
	}
	if got := normalizeNote("a \x1b b"); got != "a b" {
		t.Fatalf("normalizeNote(%q) = %q, want %q", "a \x1b b", got, "a b")
	}
	// The collapse still owns the whitespace controls: newlines become spaces.
	if got := normalizeNote("a\nb"); got != "a b" {
		t.Fatalf("normalizeNote(%q) = %q, want %q", "a\nb", got, "a b")
	}
}
