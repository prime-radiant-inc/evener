package worktree

import (
	"path/filepath"
	"strings"
	"testing"
)

// FuzzValidateName drives ValidateName with arbitrary strings. ValidateName is
// deliberately dependency-free, so this target never delegates acceptance to a
// host git binary. Any accepted name must remain lexically contained below a
// private root and must survive the sidecar-name codec fixed point exactly.
func FuzzValidateName(f *testing.F) {
	seeds := []string{
		"feature/foo",
		"dlg_01HXYZ",
		"my_feature",
		"a.json/b",
		"..",
		"-x",
		"a/",
		"a@{1}",
		"a b",
		"",
		".",
		"a//b",
		"a.lock",
		"a/.b",
		"foo.",
		strings.Repeat("a", 101),
		strings.Repeat("a", 100),
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, name string) {
		if ValidateName(name) != nil { // must not panic regardless of outcome
			return
		}

		root := filepath.Join(t.TempDir(), "worktrees")
		candidate := filepath.Join(root, filepath.FromSlash(name))
		rel, err := filepath.Rel(root, candidate)
		if err != nil {
			t.Fatalf("filepath.Rel(%q, %q): %v", root, candidate, err)
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Fatalf("ValidateName accepted path-escaping name %q: root=%q candidate=%q rel=%q", name, root, candidate, rel)
		}

		encoded := EncodeSidecarName(name)
		if strings.Contains(encoded, "/") {
			t.Fatalf("EncodeSidecarName(%q) = %q still contains a slash", name, encoded)
		}
		decoded, ok := DecodeSidecarName(encoded)
		if !ok || decoded != name {
			t.Fatalf("DecodeSidecarName(EncodeSidecarName(%q)) = (%q, %v), want (%q, true)", name, decoded, ok, name)
		}
	})
}

// FuzzSidecarNameRoundtrip checks that, for every name ValidateName accepts,
// EncodeSidecarName produces a "/"-free string and DecodeSidecarName
// reverses it exactly (spec §6 sidecar encoding).
func FuzzSidecarNameRoundtrip(f *testing.F) {
	seeds := []string{
		"feature/foo",
		"dlg_01HXYZ",
		"my_feature",
		"a.json/b",
		"a",
		"a/b/c",
		"a%b",
		"a%2Fb",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, name string) {
		if ValidateName(name) != nil {
			return
		}
		encoded := EncodeSidecarName(name)
		if strings.Contains(encoded, "/") {
			t.Fatalf("EncodeSidecarName(%q) = %q still contains a slash", name, encoded)
		}
		decoded, ok := DecodeSidecarName(encoded)
		if !ok {
			t.Fatalf("DecodeSidecarName(%q) (from %q) ok=false, want true", encoded, name)
		}
		if decoded != name {
			t.Fatalf("round trip mismatch: encode(%q) = %q, decode -> %q", name, encoded, decoded)
		}
	})
}
