package shellquote

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestLiteralAndRemoteWord(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		literal string
		remote  string
	}{
		{name: "empty", in: "", literal: "''", remote: "''"},
		{name: "plain word", in: "evener", literal: "evener", remote: "evener"},
		{name: "safe punctuation", in: "a.b-c_d/e:f,g@h%i+j=k", literal: "a.b-c_d/e:f,g@h%i+j=k", remote: "a.b-c_d/e:f,g@h%i+j=k"},
		{name: "space", in: "a b", literal: "'a b'", remote: "'a b'"},
		{name: "leading tilde", in: "~/bin/evener", literal: "'~/bin/evener'", remote: "~/bin/evener"},
		{name: "bare tilde", in: "~", literal: "'~'", remote: "~"},
		{name: "embedded tilde", in: "a~b", literal: "'a~b'", remote: "a~b"},
		{name: "tilde with space", in: "~/My Dir", literal: "'~/My Dir'", remote: "'~/My Dir'"},
		{name: "embedded single quote", in: "it's", literal: `'it'\''s'`, remote: `'it'\''s'`},
		{name: "only single quote", in: "'", literal: `''\'''`, remote: `''\'''`},
		{name: "unicode with space", in: "héllo wörld", literal: "'héllo wörld'", remote: "'héllo wörld'"},
		{name: "unicode no space", in: "café", literal: "café", remote: "café"},
		// An accented path with no metacharacter stays bare: high bytes are not
		// POSIX metacharacters, so quoting them buys sh no safety, and leaving
		// them bare keeps the pre-consolidation rendering byte-for-byte. This
		// pins the pre-consolidation deny-list behavior.
		{name: "accented path", in: "/tmp/café/menu", literal: "/tmp/café/menu", remote: "/tmp/café/menu"},
		{name: "accented with metacharacter", in: "café;rm", literal: "'café;rm'", remote: "'café;rm'"},
		// A caret is not a POSIX metacharacter and no prior deny-list named it,
		// so it stays bare: quoting it would only change the bytes the
		// pre-consolidation rendering produced, and no remaining caller renders
		// these words for a shell where a caret is syntax (see the package doc).
		{name: "caret", in: "^HEAD", literal: "^HEAD", remote: "^HEAD"},
		{name: "caret with metacharacter", in: "^a b", literal: "'^a b'", remote: "'^a b'"},
		{name: "substitution", in: "$(id)", literal: "'$(id)'", remote: "'$(id)'"},
		{name: "backticks", in: "`id`", literal: "'`id`'", remote: "'`id`'"},
		{name: "newline", in: "a\nb", literal: "'a\nb'", remote: "'a\nb'"},
		{name: "backslash", in: `a\b`, literal: `'a\b'`, remote: `'a\b'`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Literal(tc.in); got != tc.literal {
				t.Errorf("Literal(%q) = %q, want %q", tc.in, got, tc.literal)
			}
			if got := RemoteWord(tc.in); got != tc.remote {
				t.Errorf("RemoteWord(%q) = %q, want %q", tc.in, got, tc.remote)
			}
		})
	}
}

// TestMetacharactersAreQuoted walks every byte the two implementations used to
// enumerate as special. Each embedded in an otherwise plain word must come back
// quoted (so the shell treats it literally), except a tilde, which is the one
// deliberate divergence: Literal quotes it, RemoteWord leaves it bare.
func TestMetacharactersAreQuoted(t *testing.T) {
	metachars := []string{
		" ", "\t", "\n", `"`, "'", `\`, "$", "`", "!", "(", ")", ";", "|", "&",
		"<", ">", "*", "?", "[", "]", "{", "}", "~", "#",
	}
	for _, m := range metachars {
		in := "a" + m + "b"
		t.Run("literal/"+m, func(t *testing.T) {
			got := Literal(in)
			if got == in {
				t.Fatalf("Literal(%q) left a metacharacter bare", in)
			}
			if !strings.HasPrefix(got, "'") || !strings.HasSuffix(got, "'") {
				t.Fatalf("Literal(%q) = %q, want a single-quoted word", in, got)
			}
		})
		t.Run("remote/"+m, func(t *testing.T) {
			got := RemoteWord(in)
			if m == "~" {
				if got != in {
					t.Fatalf("RemoteWord(%q) = %q, want the bare word so the tilde can expand", in, got)
				}
				return
			}
			if got == in {
				t.Fatalf("RemoteWord(%q) left a metacharacter bare", in)
			}
			if !strings.HasPrefix(got, "'") || !strings.HasSuffix(got, "'") {
				t.Fatalf("RemoteWord(%q) = %q, want a single-quoted word", in, got)
			}
		})
	}
	// An embedded single quote uses the POSIX close-escape-reopen splice.
	if got := Literal("a'b"); !strings.Contains(got, `'\''`) {
		t.Fatalf("Literal(%q) = %q, want the '\\'' splice", "a'b", got)
	}
}

// TestHighBytesAreLeftBare pins the allow-list's one deliberate liberty: every
// byte in 0x80-0xFF is safe to leave bare, because no high byte is a POSIX
// metacharacter or word separator. Quoting them would be shell-equivalent for sh
// while changing bytes the pre-consolidation rendering already produced (see the
// package doc). A high byte mixed with a real metacharacter is still quoted
// whole.
func TestHighBytesAreLeftBare(t *testing.T) {
	words := []string{"café", "naïve", "Straße", "日本語", "/tmp/Ünïcode/path"}
	for _, w := range words {
		if got := Literal(w); got != w {
			t.Errorf("Literal(%q) = %q, want the bare word for pre-consolidation byte-compatibility", w, got)
		}
		if got := RemoteWord(w); got != w {
			t.Errorf("RemoteWord(%q) = %q, want the bare word for pre-consolidation byte-compatibility", w, got)
		}
	}

	// Control bytes are still unsafe: only the high half of the byte range is
	// trusted bare, not the ASCII control characters.
	for _, c := range []byte{0x01, 0x1f, 0x7f} {
		in := "a" + string(c) + "b"
		if got := Literal(in); got == in {
			t.Errorf("Literal(%q) left a control byte bare", in)
		}
	}
}

// TestCaretStaysBareAndDELIsQuoted pins the two bytes where the old deny-lists
// and the new allow-list disagree, together because the finding named them
// together. The caret is restored to the bare form: it is not a POSIX
// metacharacter, no prior deny-list named it, and keeping it bare preserves the
// pre-consolidation rendering byte-for-byte on the surviving POSIX paths (see
// the package doc). DEL (0x7F) is the same drift and stays quoted on purpose: no
// remaining consumer relies on a bare control byte, and a non-printing byte is
// better surfaced than reproduced.
func TestCaretStaysBareAndDELIsQuoted(t *testing.T) {
	for _, w := range []string{"^", "^HEAD", "a^b", "^v1.2.3", "a^b_c.d"} {
		if got := Literal(w); got != w {
			t.Errorf("Literal(%q) = %q, want the bare word for pre-consolidation byte-compatibility", w, got)
		}
		if got := RemoteWord(w); got != w {
			t.Errorf("RemoteWord(%q) = %q, want the bare word for pre-consolidation byte-compatibility", w, got)
		}
	}

	// A caret mixed with a real metacharacter is still quoted whole.
	if got, want := Literal("^a b"), "'^a b'"; got != want {
		t.Errorf("Literal(%q) = %q, want %q", "^a b", got, want)
	}

	// DEL is the other deny-list-to-allow-list drift, and stays quoted by design.
	del := "a" + string(rune(0x7f)) + "b"
	if got := Literal(del); got == del {
		t.Errorf("Literal(%q) left DEL bare, want it quoted", del)
	}
	if got := RemoteWord(del); got == del {
		t.Errorf("RemoteWord(%q) left DEL bare, want it quoted", del)
	}
}

func TestArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "none", args: nil, want: ""},
		{name: "simple", args: []string{"echo", "hello"}, want: "echo hello"},
		{name: "empty argument", args: []string{""}, want: "''"},
		{name: "space", args: []string{"echo", "hello world"}, want: "echo 'hello world'"},
		{name: "single quote", args: []string{"it's"}, want: `'it'\''s'`},
		{name: "shell syntax", args: []string{"$(rm -rf /)"}, want: "'$(rm -rf /)'"},
		{name: "tilde is literal", args: []string{"~/bin"}, want: "'~/bin'"},
		{name: "multiple", args: []string{"git", "commit", "-m", "fix: issue #42"}, want: "git commit -m 'fix: issue #42'"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Args(tc.args...); got != tc.want {
				t.Fatalf("Args(%q) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}

// TestShellRoundTrip proves the rendered words mean what the package claims by
// handing them to a real POSIX shell: a Literal word arrives byte-identical,
// and a RemoteWord's leading tilde expands while a Literal's does not.
func TestShellRoundTrip(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no POSIX sh on PATH: %v", err)
	}
	words := []string{
		"", "plain", "a b", "it's", "$(id)", "`id`", "a;b", "a|b", "a&b",
		"a\nb", `a\b`, "héllo wörld", "*", "?", "[x]", "{x,y}", "#c", `"d"`,
		"café", "/tmp/café/menu", "^HEAD",
	}
	for _, w := range words {
		out, err := exec.Command(sh, "-c", "printf '%s' "+Literal(w)).Output()
		if err != nil {
			t.Fatalf("sh rejected Literal(%q): %v", w, err)
		}
		if string(out) != w {
			t.Errorf("Literal(%q) round-tripped to %q", w, out)
		}
	}

	// The divergence: a leading tilde expands only for RemoteWord.
	cmd := exec.Command(sh, "-c", "printf '%s' "+RemoteWord("~/x"))
	cmd.Env = append(withoutEnv(os.Environ(), "HOME"), "HOME=/tmp/shellquote-home")
	expanded, err := cmd.Output()
	if err != nil {
		t.Fatalf("sh rejected RemoteWord(~/x): %v", err)
	}
	if string(expanded) != "/tmp/shellquote-home/x" {
		t.Errorf("RemoteWord(~/x) expanded to %q, want %q", expanded, "/tmp/shellquote-home/x")
	}

	literal, err := exec.Command(sh, "-c", "printf '%s' "+Literal("~/x")).Output()
	if err != nil {
		t.Fatalf("sh rejected Literal(~/x): %v", err)
	}
	if string(literal) != "~/x" {
		t.Errorf("Literal(~/x) = %q, want the untouched %q", literal, "~/x")
	}
}

// withoutEnv removes every "NAME=..." entry for name.
func withoutEnv(env []string, name string) []string {
	prefix := name + "="
	out := env[:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, prefix) {
			out = append(out, kv)
		}
	}
	return out
}
