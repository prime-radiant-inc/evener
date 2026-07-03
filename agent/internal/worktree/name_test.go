package worktree

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestValidateNameAccepts(t *testing.T) {
	cases := []string{
		"feature/foo",
		"dlg_01HXYZ",
		"my_feature",
		"a.json/b",
		"a",
		"_leading_underscore",
		"a-b-c",
		"a/b/c",
		"9numeric-start",
		strings.Repeat("a", 100), // exactly at the byte cap
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			if err := ValidateName(name); err != nil {
				t.Fatalf("ValidateName(%q) = %v, want nil", name, err)
			}
		})
	}
}

func TestValidateNameRejects(t *testing.T) {
	cases := []struct {
		name   string
		reason string
	}{
		{"..", "bare double-dot"},
		{"a..b", "double-dot substring"},
		{"a/../b", "double-dot path component"},
		{"-x", "leading dash"},
		{"a/", "trailing slash"},
		{"a@{1}", "reflog syntax"},
		{"a@{-1}", "branch-stack reflog syntax"},
		{"a b", "space"},
		{"", "empty"},
		{".hidden", "leading dot fails first-char class"},
		{strings.Repeat("a", 101), "101 bytes exceeds cap"},
		{"a//b", "empty path component / consecutive slashes"},
		{"a.lock", "component ends in .lock"},
		{"a/b.lock", "non-final component ends in .lock"},
		{"a/.b", "component starts with dot"},
		{"a/./b", "component is a bare dot"},
		{"foo.", "whole name ends with dot"},
		{"a@b", "raw @ is regex-illegal (only @{ is special-cased, but @ itself isn't in the alphabet)"},
		{"a~b", "tilde not in alphabet"},
		{"a:b", "colon not in alphabet"},
		{"a\\b", "backslash not in alphabet"},
	}
	for _, c := range cases {
		t.Run(c.reason, func(t *testing.T) {
			if err := ValidateName(c.name); err == nil {
				t.Fatalf("ValidateName(%q) = nil, want error (%s)", c.name, c.reason)
			}
		})
	}
}

func TestValidateNameNeverPanics(t *testing.T) {
	// A handful of adversarial inputs that must not panic, whatever they
	// return.
	inputs := []string{"", "/", "\x00", strings.Repeat("/", 200), "a\x7f"}
	for _, in := range inputs {
		_ = ValidateName(in)
	}
}

func TestProjectIDDifferentRootsSameBasename(t *testing.T) {
	a := ProjectID("/home/jesse/git/serf")
	b := ProjectID("/home/other/work/serf")
	if a == b {
		t.Fatalf("ProjectID collided for different roots sharing a basename: %q", a)
	}
	if !strings.HasPrefix(a, "serf-") || !strings.HasPrefix(b, "serf-") {
		t.Fatalf("expected both projectids to keep the shared basename prefix, got %q and %q", a, b)
	}
}

func TestProjectIDIsDeterministic(t *testing.T) {
	root := "/home/jesse/git/prime-radiant/serf"
	a := ProjectID(root)
	b := ProjectID(root)
	if a != b {
		t.Fatalf("ProjectID(%q) is not deterministic: %q vs %q", root, a, b)
	}
}

func TestProjectIDMatchesSpecExample(t *testing.T) {
	// Spec §6 worked example.
	got := ProjectID("/home/jesse/git/prime-radiant/serf")
	sum := sha256.Sum256([]byte("/home/jesse/git/prime-radiant/serf"))
	want := "serf-" + hex.EncodeToString(sum[:])[:16]
	if got != want {
		t.Fatalf("ProjectID = %q, want %q", got, want)
	}
}

func TestProjectIDBasenameTruncatedTo48Bytes(t *testing.T) {
	longBase := strings.Repeat("x", 200)
	got := ProjectID("/repos/" + longBase)
	idx := strings.LastIndex(got, "-")
	if idx == -1 {
		t.Fatalf("ProjectID(%q) = %q has no hash separator", longBase, got)
	}
	basenamePart := got[:idx]
	if len(basenamePart) != 48 {
		t.Fatalf("basename part = %q (%d bytes), want 48 bytes", basenamePart, len(basenamePart))
	}
	if basenamePart != strings.Repeat("x", 48) {
		t.Fatalf("basename part = %q, want 48 x's", basenamePart)
	}
}

func TestProjectIDUnsafeCharsSanitized(t *testing.T) {
	got := ProjectID("/repos/my repo!@#$%^&*()+=")
	idx := strings.LastIndex(got, "-")
	if idx == -1 {
		t.Fatalf("ProjectID = %q has no hash separator", got)
	}
	basenamePart := got[:idx]
	const allowed = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._-"
	for _, r := range basenamePart {
		if !strings.ContainsRune(allowed, r) {
			t.Fatalf("basename part %q contains disallowed rune %q", basenamePart, r)
		}
	}
}

func TestProjectIDEmptyBasenameFallsBackToRepo(t *testing.T) {
	// A basename made entirely of leading-trim characters (dots) sanitizes
	// fine but trims away to nothing.
	got := ProjectID("/repos/...")
	if !strings.HasPrefix(got, "repo-") {
		t.Fatalf("ProjectID(%q) = %q, want repo-<hash> fallback", "/repos/...", got)
	}
}

func TestProjectIDFixedLengthHashSuffix(t *testing.T) {
	roots := []string{
		"/a",
		"/home/jesse/git/prime-radiant/serf",
		"/repos/" + strings.Repeat("y", 300),
		"/repos/...",
	}
	for _, root := range roots {
		got := ProjectID(root)
		idx := strings.LastIndex(got, "-")
		if idx == -1 {
			t.Fatalf("ProjectID(%q) = %q has no hash separator", root, got)
		}
		hashPart := got[idx+1:]
		if len(hashPart) != 16 {
			t.Fatalf("ProjectID(%q) hash suffix = %q (%d bytes), want 16", root, hashPart, len(hashPart))
		}
		for _, r := range hashPart {
			if !strings.ContainsRune("0123456789abcdef", r) {
				t.Fatalf("ProjectID(%q) hash suffix %q contains non-hex rune %q", root, hashPart, r)
			}
		}
	}
}

func TestEncodeDecodeSidecarNameRoundtrip(t *testing.T) {
	names := []string{
		"feature/foo",
		"dlg_01HXYZ",
		"my_feature",
		"a.json/b",
		"a",
		"a/b/c",
		"deeply/nested/name/here",
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			if err := ValidateName(name); err != nil {
				t.Fatalf("test bug: %q is not a legal name: %v", name, err)
			}
			encoded := EncodeSidecarName(name)
			if strings.Contains(encoded, "/") {
				t.Fatalf("EncodeSidecarName(%q) = %q still contains a slash", name, encoded)
			}
			decoded, ok := DecodeSidecarName(encoded)
			if !ok {
				t.Fatalf("DecodeSidecarName(%q) = ok=false, want true", encoded)
			}
			if decoded != name {
				t.Fatalf("round trip mismatch: encode(%q) = %q, decode -> %q", name, encoded, decoded)
			}
		})
	}
}

// TestEncodeSidecarNameFlatNamespaceNoCollision guards the rev-6 finding
// (spec §6): under mirrored nesting, the legal name pair "a" and "a.json/b"
// would collide as file .meta/a.json vs directory .meta/a.json/. The flat,
// "/"->"%2F" encoding must keep their sidecar file names distinct AND must
// not let one become a path-style prefix of the other (no literal "/" ever
// appears in an encoded name, so neither file name can be mistaken for a
// directory containing the other).
func TestEncodeSidecarNameFlatNamespaceNoCollision(t *testing.T) {
	nameA := "a"
	nameB := "a.json/b"
	encA := EncodeSidecarName(nameA)
	encB := EncodeSidecarName(nameB)

	fileA := encA + ".json"
	fileB := encB + ".json"

	if fileA == fileB {
		t.Fatalf("sidecar file names collide: %q", fileA)
	}
	if strings.Contains(fileA, "/") || strings.Contains(fileB, "/") {
		t.Fatalf("sidecar file names must never contain a slash: %q, %q", fileA, fileB)
	}
	// The historical mirrored-nesting collision was fileA (e.g. "a.json")
	// being exactly the directory-prefix of fileB (e.g. "a.json/b.json").
	// With flat encoding that can't happen because there is no "/" left in
	// either name to form a directory boundary.
	if strings.HasPrefix(fileB, fileA+"/") {
		t.Fatalf("fileB %q is nested under fileA %q as a directory", fileB, fileA)
	}
}

func TestDecodeSidecarNameRejectsStrayPercent(t *testing.T) {
	cases := []string{
		"a%2",
		"a%",
		"a%2Gb",
		"a%20b",
		"100%done",
	}
	for _, encoded := range cases {
		t.Run(encoded, func(t *testing.T) {
			if _, ok := DecodeSidecarName(encoded); ok {
				t.Fatalf("DecodeSidecarName(%q) = ok=true, want false (stray %%)", encoded)
			}
		})
	}
}

func TestDecodeSidecarNameAcceptsLiteralEncoding(t *testing.T) {
	got, ok := DecodeSidecarName("feature%2Ffoo")
	if !ok {
		t.Fatalf("DecodeSidecarName(feature%%2Ffoo) ok=false, want true")
	}
	if got != "feature/foo" {
		t.Fatalf("DecodeSidecarName(feature%%2Ffoo) = %q, want feature/foo", got)
	}
}
