package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyPatch_AddUpdateMoveDelete(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\nthree\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "d.txt"), []byte("delete me\n"), 0o644)

	patch := `*** Begin Patch
*** Add File: b.txt
+hello
+world
*** Update File: a.txt
@@
 one
-two
+TWO
 three
*** Update File: b.txt
*** Move to: c.txt
@@
 hello
 world
*** Delete File: d.txt
*** End Patch
`
	out, err := ApplyPatch(dir, patch)
	if err != nil {
		t.Fatalf("ApplyPatch: %v", err)
	}
	if !strings.Contains(out, "b.txt") || !strings.Contains(out, "a.txt") || !strings.Contains(out, "c.txt") {
		t.Fatalf("output: %q", out)
	}

	// a.txt updated
	ab, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil {
		t.Fatalf("read a.txt: %v", err)
	}
	if strings.TrimSpace(string(ab)) != "one\nTWO\nthree" {
		t.Fatalf("a.txt: %q", string(ab))
	}
	// b.txt moved to c.txt
	if _, err := os.Stat(filepath.Join(dir, "b.txt")); err == nil {
		t.Fatalf("expected b.txt to be moved away")
	}
	b, err := os.ReadFile(filepath.Join(dir, "c.txt"))
	if err != nil {
		t.Fatalf("read c.txt: %v", err)
	}
	if strings.TrimSpace(string(b)) != "hello\nworld" {
		t.Fatalf("c.txt: %q", string(b))
	}

	// d.txt deleted
	if _, err := os.Stat(filepath.Join(dir, "d.txt")); err == nil {
		t.Fatalf("expected d.txt to be deleted")
	}
}

func TestApplyPatch_RejectsPathTraversalAndAbsolutePaths(t *testing.T) {
	dir := t.TempDir()
	cases := []string{
		`*** Begin Patch
*** Add File: ../x.txt
+nope
*** End Patch
`,
		`*** Begin Patch
*** Add File: /abs.txt
+nope
*** End Patch
`,
	}
	for _, p := range cases {
		if _, err := ApplyPatch(dir, p); err == nil {
			t.Fatalf("expected error for patch:\n%s", p)
		}
	}
}
