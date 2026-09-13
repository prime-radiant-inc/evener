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
