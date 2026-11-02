//go:build unix

package execenv

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/sandbox"
)

// The unsandboxed native grep walk (grepNativeSkipping) and the sandboxed one
// (sandboxFS.grepNative) are two bodies of the same search. This pins that
// they agree on one tree: dot entries, gitignored entries, binary and
// oversized files, symlinks, a named file, a skip predicate and the directory
// entry budget. A difference between them must be a deliberate, named one.

func writeGrepParityTree(t *testing.T, root string) {
	t.Helper()
	files := map[string]string{
		"a.txt":             "needle in a\n",
		"sub/b.txt":         "needle in b\nother\n",
		"sub/deep/c.go":     "package c // needle\n",
		".hidden/d.txt":     "needle hidden\n",
		".dotfile":          "needle dot\n",
		".gitignore":        "ignored/\n*.log\n",
		"ignored/e.txt":     "needle ignored\n",
		"skip.log":          "needle log\n",
		"binary.bin":        "needle\x00bin\n",
		"nomatch.txt":       "nothing here\n",
		"also/needle_f.txt": "needle again\nneedle twice\n",
	}
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, "emptydir"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Oversize files hold text: a window search skips a NUL-filled sparse hole as binary.
	filler := bytes.Repeat([]byte("filler line\n"), 1<<16)
	for _, big := range []string{"big.txt", "also/big2.txt"} {
		f, err := os.Create(filepath.Join(root, big))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString("needle in the head of a huge file\n"); err != nil {
			t.Fatal(err)
		}
		for written := 0; written <= grepMaxFileBytes; written += len(filler) {
			if _, err := f.Write(filler); err != nil {
				t.Fatal(err)
			}
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func grepErrClass(err error) string {
	if err == nil {
		return "none"
	}
	if _, ok := errors.AsType[*globBudgetError](err); ok {
		return "budget"
	}
	return "other"
}

func TestGrepNativeArmsAgreeOnOneTree(t *testing.T) {
	sandboxed, _, root := sandboxedEnv(t, sandbox.ModeReadOnly)
	writeGrepParityTree(t, root)
	plain := NewLocalExecutionEnvironment(root)
	cases := []struct {
		name    string
		path    string
		glob    string
		mode    string
		skip    func(rel string) bool
		budget  int
		wantErr string
		matches bool // the search finds something, so equal outputs are not both empty
	}{
		{name: "content from the root", matches: true, path: ".", mode: "content"},
		{name: "files with matches", matches: true, path: ".", mode: "files_with_matches"},
		{name: "count", matches: true, path: ".", mode: "count"},
		{name: "glob filter", matches: true, path: ".", glob: "*.go", mode: "content"},
		{name: "subdirectory", matches: true, path: "sub", mode: "content"},
		{name: "directory with only an oversize file and a match", path: "also", mode: "content"},
		{name: "skip predicate", path: ".", mode: "files_with_matches", skip: func(rel string) bool { return rel == "a.txt" }},
		{name: "named file", matches: true, path: "a.txt", mode: "content"},
		{name: "named file, count", matches: true, path: "a.txt", mode: "count"},
		{name: "named file with a glob that excludes it", path: "a.txt", glob: "*.go", mode: "content"},
		{name: "named file the skip predicate names", path: "a.txt", mode: "content", skip: func(rel string) bool { return true }},
		{name: "named dotfile", path: ".dotfile", mode: "content"},
		{name: "named gitignored file", matches: true, path: "ignored/e.txt", mode: "content"},
		{name: "named gitignored by pattern", matches: true, path: "skip.log", mode: "content"},
		{name: "named binary file", path: "binary.bin", mode: "content"},
		{name: "named oversize file", matches: true, path: "big.txt", mode: "content"},
		{name: "named hidden directory", path: ".hidden", mode: "content"},
		{name: "directory entry budget", path: ".", mode: "content", budget: 3, wantErr: "budget"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.budget > 0 {
				stubMaxGlobDirEntries(t, tc.budget)
			} else {
				t.Parallel()
			}
			target := filepath.Join(root, tc.path)

			wantOut, wantErr := plain.grepNativeSkipping(context.Background(), "needle", target, tc.glob, false, 100, tc.mode, 0, tc.skip)
			gotOut, gotErr := sandboxed.GrepSkipping(context.Background(), "needle", target, tc.glob, false, 100, tc.mode, 0, tc.skip)
			if want := cmp.Or(tc.wantErr, "none"); grepErrClass(wantErr) != want {
				t.Fatalf("unsandboxed err class = %s (%v), want %s", grepErrClass(wantErr), wantErr, want)
			}
			if grepErrClass(gotErr) != grepErrClass(wantErr) {
				t.Fatalf("sandboxed err = %v, unsandboxed err = %v", gotErr, wantErr)
			}
			if tc.matches && wantOut == "" {
				t.Fatal("unsandboxed search found nothing")
			}
			if gotOut != wantOut {
				t.Fatalf("sandboxed output differs from unsandboxed:\nsandboxed:\n%s\nunsandboxed:\n%s", gotOut, wantOut)
			}
		})
	}
}

// The arms differ on purpose in three places, all because the sandboxed walk
// opens beneath a policy-checked root without following links: a symlink
// named as the target is refused rather than followed, a symlinked file inside
// a searched directory is left out of the walk, and a missing target is an
// error rather than an empty result.
func TestGrepNativeArmsDifferOnSymlinksAndMissingTargets(t *testing.T) {
	t.Parallel()
	sandboxed, _, root := sandboxedEnv(t, sandbox.ModeReadOnly)
	writeGrepParityTree(t, root)
	if err := os.Symlink("a.txt", filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("sub", filepath.Join(root, "linkdir")); err != nil {
		t.Fatal(err)
	}
	plain := NewLocalExecutionEnvironment(root)
	ctx := context.Background()
	search := func(path string) (plainOut string, plainErr error, sandboxedOut string, sandboxedErr error) {
		target := filepath.Join(root, path)
		plainOut, plainErr = plain.grepNativeSkipping(ctx, "needle", target, "", false, 100, "files_with_matches", 0, nil)
		sandboxedOut, sandboxedErr = sandboxed.GrepSkipping(ctx, "needle", target, "", false, 100, "files_with_matches", 0, nil)
		return
	}

	for _, named := range []string{"link.txt", "linkdir"} {
		_, plainErr, _, sandboxedErr := search(named)
		if plainErr != nil {
			t.Fatalf("unsandboxed search of symlink %s: %v", named, plainErr)
		}
		if _, denied := errors.AsType[*sandbox.DeniedError](sandboxedErr); !denied {
			t.Fatalf("sandboxed search of symlink %s: %v, want a typed denial", named, sandboxedErr)
		}
	}

	plainOut, plainErr, sandboxedOut, sandboxedErr := search(".")
	if plainErr != nil || sandboxedErr != nil {
		t.Fatalf("walk errors: unsandboxed %v, sandboxed %v", plainErr, sandboxedErr)
	}
	hasLink := func(out string) bool { return strings.Contains(out, "link.txt") }
	if !hasLink(plainOut) || hasLink(sandboxedOut) {
		t.Fatalf("symlinked file in a walk: unsandboxed found=%t, sandboxed found=%t, want true and false", hasLink(plainOut), hasLink(sandboxedOut))
	}

	plainOut, plainErr, _, sandboxedErr = search("missing.txt")
	if plainErr != nil || plainOut != "" {
		t.Fatalf("unsandboxed search of a missing path = %q, %v, want an empty result", plainOut, plainErr)
	}
	if sandboxedErr == nil {
		t.Fatal("sandboxed search of a missing path succeeded, want an error")
	}
}
