package worktree

import (
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
