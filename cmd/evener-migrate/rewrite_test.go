package migrate

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRewriteLegacyPaths_RewritesTextFile(t *testing.T) {
	dst := t.TempDir()
	old := filepath.Join(t.TempDir(), "legacy-root") // any distinct absolute string works as "old"
	nw := filepath.Join(t.TempDir(), "new-root")

	target := filepath.Join(dst, "known_marketplaces.json")
	body := `{"acme":{"installLocation":"` + old + `/marketplaces/acme"}}`
	if err := os.WriteFile(target, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	if err := rewriteLegacyPaths(dst, old, nw, &stdout); err != nil {
		t.Fatalf("rewriteLegacyPaths: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"acme":{"installLocation":"` + nw + `/marketplaces/acme"}}`
	if string(got) != want {
		t.Fatalf("rewritten content = %q, want %q", got, want)
	}
	if !strings.Contains(stdout.String(), "rewrote 1 path reference(s) in "+target) {
		t.Fatalf("stdout = %q, want a log line naming the file and count", stdout.String())
	}
}

func TestRewriteLegacyPaths_LeavesBinaryFileUntouched(t *testing.T) {
	dst := t.TempDir()
	old := filepath.Join(t.TempDir(), "legacy-root")
	nw := filepath.Join(t.TempDir(), "new-root")

	target := filepath.Join(dst, "index.db")
	// A NUL byte anywhere in the sniff window marks this as binary.
	body := append([]byte(old+"/some/path\x00binary-tail"), 0, 1, 2, 3)
	if err := os.WriteFile(target, body, 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	if err := rewriteLegacyPaths(dst, old, nw, &stdout); err != nil {
		t.Fatalf("rewriteLegacyPaths: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("binary file was modified: got %q, want unchanged %q", got, body)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want no rewrite logged for a binary file", stdout.String())
	}
}

func TestRewriteLegacyPaths_SkipsGitDirectories(t *testing.T) {
	dst := t.TempDir()
	old := filepath.Join(t.TempDir(), "legacy-root")
	nw := filepath.Join(t.TempDir(), "new-root")

	repo := filepath.Join(dst, "marketplaces", "acme")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A tracked-looking file with a legacy path reference: must not be
	// touched, since it lives inside a git-managed working tree.
	tracked := filepath.Join(repo, "marketplace.json")
	body := `{"note":"` + old + `/somewhere"}`
	if err := os.WriteFile(tracked, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	// Something living inside .git itself, in case the walk ever reaches it.
	gitInternal := filepath.Join(repo, ".git", "config")
	if err := os.WriteFile(gitInternal, []byte(old+"/somewhere"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	if err := rewriteLegacyPaths(dst, old, nw, &stdout); err != nil {
		t.Fatalf("rewriteLegacyPaths: %v", err)
	}

	got, err := os.ReadFile(tracked)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Fatalf("file inside git-managed tree was modified: got %q, want unchanged %q", got, body)
	}
	gitGot, err := os.ReadFile(gitInternal)
	if err != nil {
		t.Fatal(err)
	}
	if string(gitGot) != old+"/somewhere" {
		t.Fatalf(".git internal file was modified: got %q", gitGot)
	}
}

func TestRewriteLegacyPaths_SkipsFilesOverSizeCap(t *testing.T) {
	dst := t.TempDir()
	old := filepath.Join(t.TempDir(), "legacy-root")
	nw := filepath.Join(t.TempDir(), "new-root")

	target := filepath.Join(dst, "huge.jsonl")
	body := old + "/x " + strings.Repeat("a", maxRewriteFileSize+1)
	if err := os.WriteFile(target, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	if err := rewriteLegacyPaths(dst, old, nw, &stdout); err != nil {
		t.Fatalf("rewriteLegacyPaths: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Fatalf("oversized file was modified")
	}
}

func TestRewriteLegacyPaths_IdempotentSecondRun(t *testing.T) {
	dst := t.TempDir()
	old := filepath.Join(t.TempDir(), "legacy-root")
	nw := filepath.Join(t.TempDir(), "new-root")

	target := filepath.Join(dst, "meta.json")
	body := `{"cwd":"` + old + `/proj"}`
	if err := os.WriteFile(target, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	var first bytes.Buffer
	if err := rewriteLegacyPaths(dst, old, nw, &first); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if !strings.Contains(first.String(), "rewrote 1 path reference(s)") {
		t.Fatalf("first pass stdout = %q, want a rewrite logged", first.String())
	}

	var second bytes.Buffer
	if err := rewriteLegacyPaths(dst, old, nw, &second); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if second.Len() != 0 {
		t.Fatalf("second pass stdout = %q, want no further rewrites (idempotent)", second.String())
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"cwd":"` + nw + `/proj"}`
	if string(got) != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestRewriteLegacyPaths_SkipsSymlinks(t *testing.T) {
	dst := t.TempDir()
	old := filepath.Join(t.TempDir(), "legacy-root")
	nw := filepath.Join(t.TempDir(), "new-root")

	outside := filepath.Join(t.TempDir(), "outside.txt")
	body := old + "/somewhere"
	if err := os.WriteFile(outside, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dst, "link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	var stdout bytes.Buffer
	if err := rewriteLegacyPaths(dst, old, nw, &stdout); err != nil {
		t.Fatalf("rewriteLegacyPaths: %v", err)
	}

	got, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Fatalf("symlink target was modified: got %q, want unchanged %q", got, body)
	}
}
