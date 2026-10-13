//go:build linux || darwin

package execenv

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A symlink alias can spell pseudo-filesystem content as an ordinary path —
// /tmp/proc-alias -> /proc makes /tmp/proc-alias/version stat as a small
// regular file — so the routing classifies what the target resolves to as
// well as how it is spelled (#4574 round 3, MEDIUM). The aliased target
// still gets searched, by the native arm, bounded by its read cap; and a
// directory symlink in a walked tree contributes nothing: fs.WalkDir does
// not descend through one, so the walk cannot reach pseudo content behind
// an alias either.
func TestGrepResolvesPseudoRootAliasesWhenRouting(t *testing.T) {
	// Not parallel: stubPseudoFsRoots swaps the package's root list.
	root := t.TempDir()
	for _, dir := range []string{"pseudo", "plain"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "pseudo", "keep.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "plain", "keep2.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(filepath.Join(root, "pseudo"), alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "plain"), filepath.Join(root, "plainlink")); err != nil {
		t.Fatal(err)
	}
	stubPseudoFsRoots(t, filepath.Join(root, "pseudo"))

	aliased := filepath.Join(alias, "keep.txt")
	if !grepTakesNativeArm(aliased) {
		t.Fatalf("grepTakesNativeArm(%q) = false, want the resolved pseudo path to route native", aliased)
	}

	rec := &grepArgvRecorder{}
	env := NewLocalExecutionEnvironment(root)
	defer env.Cleanup()
	env.EnvPolicy = EnvPolicyNone
	env.commandFactory = rec
	env.lookPath = func(string) (string, error) { return "/fixture/rg", nil }

	// Ripgrep is installed and never invoked: the resolved target is
	// pseudo-rooted, and the native arm still reads it through the alias.
	got, err := env.Grep(t.Context(), "needle", aliased, "", false, 100, "content")
	if err != nil {
		t.Fatal(err)
	}
	if rec.argvCalls != 0 {
		t.Fatalf("aliased pseudo target went to ripgrep: argv calls = %d, want 0", rec.argvCalls)
	}
	if want := "1:needle"; got != want {
		t.Fatalf("aliased pseudo target = %q, want %q from the native arm reading through the alias", got, want)
	}

	// A walk over the tree cannot reach pseudo content behind the alias:
	// the stubbed pseudo root is skipped at its real name, and no directory
	// symlink — alias or plainlink — is descended through.
	got, err = env.Grep(t.Context(), "needle", root, "", false, 100, "files_with_matches")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.FromSlash("plain/keep2.txt"); got != want {
		t.Fatalf("walk over symlinked aliases = %q, want only %q", got, want)
	}
}

// The kernel's own /proc behind a symlink, the reviewer's exact fixture.
// Classifying needs no privilege — resolving and stat'ing the alias never
// reads the file, which is what makes kmsg, root-only to read, safe to
// classify.
func TestGrepResolvesAProcAliasWhenRouting(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("/proc exists only on Linux")
	}
	t.Parallel()
	alias := filepath.Join(t.TempDir(), "proc-alias")
	if err := os.Symlink("/proc", alias); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{filepath.Join(alias, "kmsg"), filepath.Join(alias, "version")} {
		if !grepTakesNativeArm(target) {
			t.Errorf("grepTakesNativeArm(%q) = false, want the aliased /proc path to route native", target)
		}
	}
}
