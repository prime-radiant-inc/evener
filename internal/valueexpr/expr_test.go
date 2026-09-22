package valueexpr

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// The union grammar, one parser for every config surface that expands
// $ expressions (providers.toml and MCP config):
//
//	$NAME            bare reference
//	${NAME}          braced reference
//	${NAME:-default} reference with a default used when NAME is unset
//	$(command)       command expression; stdout is the value
//	$$               a literal $
//	$ followed by any other byte is a literal $
//
// Interior of $(command) is opaque: the shell owns its syntax at run time.
// An unterminated ${ or $(, an empty command, and an invalid variable name
// are syntax errors. The error for an invalid ${...} name never echoes the
// content, which may be a misplaced secret.

func TestScan(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		refs     []string
		commands []string
		literal  string
	}{
		{"empty", "", nil, nil, ""},
		{"plain text", "hello world", nil, nil, "hello world"},
		{"bare ref", "$FOO", []string{"FOO"}, nil, ""},
		{"bare ref embedded", "a $FOO b", []string{"FOO"}, nil, "a  b"},
		{"bare ref runs to first non-name byte", "$FOO-BAR", []string{"FOO"}, nil, "-BAR"},
		{"braced ref", "${FOO}", []string{"FOO"}, nil, ""},
		{"ref with default", "${FOO:-x}", []string{"FOO"}, nil, ""},
		{"ref with empty default", "${FOO:-}", []string{"FOO"}, nil, ""},
		{"default is not literal material", "Bearer ${FOO:-x}", []string{"FOO"}, nil, "Bearer "},
		{"several refs", "$FOO ${BAR} $BAZ", []string{"FOO", "BAR", "BAZ"}, nil, "  "},
		{"dollar-dollar escapes a literal dollar", "$$FOO", nil, nil, "$FOO"},
		{"dollar at end is literal", "a$", nil, nil, "a$"},
		{"dollar before a non-name byte is literal", "a$!b", nil, nil, "a$!b"},
		{"command expression", "$(cat /tmp/thing)", nil, []string{"cat /tmp/thing"}, ""},
		{"command keeps inner parens", "$(echo (x) y)", nil, []string{"echo (x) y"}, ""},
		{"command interior is opaque", "$(echo $HOME ${NOT_EXPANDED})", nil, []string{"echo $HOME ${NOT_EXPANDED}"}, ""},
		{"commands adjacent", "$(a)$(b)", nil, []string{"a", "b"}, ""},
		{"command beside literal and ref", "Bearer $(cat /tmp/t) $FOO", []string{"FOO"}, []string{"cat /tmp/t"}, "Bearer  "},
		{"escaped parens stay literal", "$$(echo hi)", nil, nil, "$(echo hi)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Scan(tt.value)
			if err != nil {
				t.Fatalf("Scan(%q): %v", tt.value, err)
			}
			if !slices.Equal(refNames(got.Refs), tt.refs) {
				t.Errorf("Scan(%q).Refs = %v; want %v", tt.value, refNames(got.Refs), tt.refs)
			}
			if !slices.Equal(got.Commands, tt.commands) {
				t.Errorf("Scan(%q).Commands = %v; want %v", tt.value, got.Commands, tt.commands)
			}
			if got.Literal != tt.literal {
				t.Errorf("Scan(%q).Literal = %q; want %q", tt.value, got.Literal, tt.literal)
			}
		})
	}
}

func TestScanErrors(t *testing.T) {
	tests := []struct {
		name  string
		value string
		// wantSubstring must not appear in the error: a value may hold a
		// secret, and the error must never echo it back.
		secret string
	}{
		{"unterminated braced ref", "a ${FOO", ""},
		{"unterminated command", "$(cat /tmp/thing", ""},
		{"bare paren with no command", "$(", ""},
		{"empty command", "$()", ""},
		{"invalid braced name", "${sk-live-abc123}", "sk-live-abc123"},
		{"braced name may not start with a digit", "${9FOO}", "9FOO"},
		{"invalid braced name with default", "${sk-live-abc123:-x}", "sk-live-abc123"},
		{"empty braced name", "${}", ""},
		{"empty braced name with default", "${:-x}", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Scan(tt.value)
			if err == nil {
				t.Fatalf("Scan(%q) succeeded; want a syntax error", tt.value)
			}
			if tt.secret != "" && strings.Contains(err.Error(), tt.secret) {
				t.Errorf("Scan(%q) error echoes the value: %v", tt.value, err)
			}
		})
	}
}

func TestExpandRefs(t *testing.T) {
	ResetForTest()
	lookup := func(name string) (string, bool) {
		switch name {
		case "SET":
			return "val", true
		case "EMPTY":
			// Set but empty: counts as missing, because an empty credential
			// must never resolve as a present one.
			return "", true
		default:
			return "", false
		}
	}
	tests := []struct {
		name       string
		value      string
		want       string
		wantUnsets []string
	}{
		{"plain", "abc", "abc", nil},
		{"resolved", "x$SET y", "xval y", nil},
		{"missing substitutes empty", "x$MISSING y", "x y", []string{"MISSING"}},
		{"empty-set counts as missing", "x$EMPTY y", "x y", []string{"EMPTY"}},
		{"default fills unset", "${MISSING:-def}", "def", nil},
		{"empty default fills unset", "${MISSING:-}", "", nil},
		{"default fills empty-set too, POSIX style", "${EMPTY:-def}", "def", nil},
		{"set ignores default", "${SET:-def}", "val", nil},
		{"default is literal text, never re-expanded", "${MISSING:-$NOT_A_REF}", "$NOT_A_REF", nil},
		{"first colon-dash splits", "${MISSING:-a:-b}", "a:-b", nil},
		{"escape", "$$SET", "$SET", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, unresolved, err := Expand(tt.value, lookup)
			if err != nil {
				t.Fatalf("Expand(%q): %v", tt.value, err)
			}
			if got != tt.want {
				t.Errorf("Expand(%q) = %q; want %q", tt.value, got, tt.want)
			}
			if !equalUnresolvedNames(unresolved, tt.wantUnsets) {
				t.Errorf("Expand(%q) unresolved = %v; want %v", tt.value, unresolvedNames(unresolved), tt.wantUnsets)
			}
		})
	}
}

func TestPieces(t *testing.T) {
	pieces, err := Pieces("Bearer ${A:-x} $(cat /tmp/thing) tail")
	if err != nil {
		t.Fatalf("Pieces: %v", err)
	}
	want := []Piece{
		{Kind: PieceLit, Lit: "Bearer "},
		{Kind: PieceRef, Ref: Ref{Name: "A", Default: "x", HasDefault: true}},
		{Kind: PieceLit, Lit: " "},
		{Kind: PieceCommand, Command: "cat /tmp/thing"},
		{Kind: PieceLit, Lit: " tail"},
	}
	if len(pieces) != len(want) {
		t.Fatalf("Pieces = %+v; want %d pieces", pieces, len(want))
	}
	for i := range want {
		if pieces[i] != want[i] {
			t.Errorf("piece %d = %+v; want %+v", i, pieces[i], want[i])
		}
	}
	if _, err := Pieces("${unterminated"); err == nil {
		t.Fatal("Pieces must report the syntax error")
	}
}

func TestExpandCommand(t *testing.T) {
	ResetForTest()
	calls := 0
	RunCommand = func(command string) (string, error) {
		calls++
		switch command {
		case "ok":
			return "  minted-token \n", nil
		case "fail":
			return "", errors.New("command exited with status 1: boom")
		default:
			return "", errors.New("unexpected command in test: " + command)
		}
	}
	t.Cleanup(func() { RunCommand = realRunCommand })

	got, unresolved, err := Expand("Bearer $(ok)", func(string) (string, bool) { return "", false })
	if err != nil || len(unresolved) != 0 {
		t.Fatalf("Expand command success: got %q, unresolved %v, err %v", got, unresolved, err)
	}
	if got != "Bearer minted-token" {
		t.Errorf("output not trimmed verbatim: %q", got)
	}
	if calls != 1 {
		t.Errorf("executor ran %d times; want 1 (cache)", calls)
	}

	// A failing command behaves like an unset reference: empty value plus
	// one unresolved entry, so hosts can map it to their own convention.
	got, unresolved, err = Expand("Bearer $(fail)", func(string) (string, bool) { return "", false })
	if err != nil || got != "Bearer " {
		t.Fatalf("Expand command failure: got %q, err %v", got, err)
	}
	if len(unresolved) != 1 || unresolved[0].Command != "fail" || unresolved[0].Err == nil {
		t.Errorf("unresolved = %+v; want one failed command entry", unresolved)
	}
}

func TestExpandSyntaxError(t *testing.T) {
	ResetForTest()
	got, unresolved, err := Expand("$(unterminated", func(string) (string, bool) { return "", false })
	if err == nil {
		t.Fatalf("Expand succeeded on a syntax error; got %q, unresolved %v", got, unresolved)
	}
	if got != "" || unresolved != nil {
		t.Errorf("syntax error returned leftovers: %q %v", got, unresolved)
	}
}

func refNames(refs []Ref) []string {
	var names []string
	for _, r := range refs {
		names = append(names, r.Name)
	}
	return names
}

func unresolvedNames(unresolved []Unresolved) []string {
	var names []string
	for _, u := range unresolved {
		if u.Name != "" {
			names = append(names, u.Name)
		}
	}
	return names
}

func equalUnresolvedNames(unresolved []Unresolved, want []string) bool {
	return slices.Equal(unresolvedNames(unresolved), want)
}
