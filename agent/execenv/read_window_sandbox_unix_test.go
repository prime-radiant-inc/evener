//go:build linux || darwin

package execenv

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/sandbox"
)

// TestSandboxReadFileWindowMatchesTheOffArm is read_file's offset/limit read on
// the confined arm: the entry a model selects just by passing offset or limit.
// It runs a file above the windowed read's whole-file threshold through both
// arms and requires the same rows, so the sandboxed window is not a second
// implementation with its own answers.
func TestSandboxReadFileWindowMatchesTheOffArm(t *testing.T) {
	t.Parallel()
	env, _, worktree := sandboxedEnv(t, sandbox.ModeRestricted)
	// A separate, unconfined environment over the same directory is the off arm.
	// It reads through the plain filesystem, so the comparison is arm against
	// arm and not a re-run of the same code.
	off := NewLocalExecutionEnvironment(worktree)
	t.Cleanup(off.Cleanup)

	const row = "line of representative text content here\n"
	body := strings.Repeat(row, readWindowScanFloor/len(row))
	path := filepath.Join(worktree, "large.txt")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write large.txt: %v", err)
	}
	if int64(len(body)) <= readWindowWholeBelow {
		t.Fatalf("fixture is %d bytes, want past the %d-byte scan threshold", len(body), readWindowWholeBelow)
	}

	for _, w := range []struct{ off, lim int }{
		{1, 2},
		{1, 20},
		{7, 3},
	} {
		confined, err := env.ReadFileCtx(t.Context(), path, &w.off, &w.lim)
		if err != nil {
			t.Fatalf("confined ReadFileCtx(offset=%d, limit=%d): %v", w.off, w.lim, err)
		}
		plain, err := off.ReadFileCtx(t.Context(), path, &w.off, &w.lim)
		if err != nil {
			t.Fatalf("off-arm ReadFileCtx(offset=%d, limit=%d): %v", w.off, w.lim, err)
		}
		if confined != plain {
			t.Fatalf("confined window (offset=%d, limit=%d) = %q, off arm = %q", w.off, w.lim, confined, plain)
		}
		// The rows must be real content from the requested line, so a read that
		// agreed with the off arm by returning nothing could not pass.
		if want := fmt.Sprintf("%4d\tline of representative", w.off); !strings.HasPrefix(confined, want) {
			t.Fatalf("confined window (offset=%d, limit=%d) = %q, want rows from line %d", w.off, w.lim, confined, w.off)
		}
	}
}

// TestSandboxReadFileWindowDeniesLikeTheWholeFileRead pins the confined window's
// admission: a target the sandbox refuses must be refused through offset/limit
// exactly as through a nil, nil read, so passing a window cannot route around
// the confined resolver. Both shapes are checked — outside the roots, and a
// symlink out of them — because they fail at different points in the open.
func TestSandboxReadFileWindowDeniesLikeTheWholeFileRead(t *testing.T) {
	t.Parallel()
	env, home, worktree := sandboxedEnv(t, sandbox.ModeRestricted)

	outside := filepath.Join(home, "outside.txt")
	if err := os.WriteFile(outside, []byte("outside the roots\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(worktree, "link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	offset, limit := 1, 2
	for _, tc := range []struct {
		name string
		path string
	}{
		{"outside the roots", outside},
		{"through a symlink out", link},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, wholeErr := env.ReadFileCtx(t.Context(), tc.path, nil, nil)
			mustDenied(t, wholeErr, "whole-file read of %s", tc.name)

			_, winErr := env.ReadFileCtx(t.Context(), tc.path, &offset, &limit)
			mustDenied(t, winErr, "windowed read of %s", tc.name)

			// The denial must be the same refusal, not merely some error: a
			// windowed read that reached a different gate would be a hole.
			var wholeDenied, winDenied *sandbox.DeniedError
			wholeOK := errors.As(wholeErr, &wholeDenied)
			winOK := errors.As(winErr, &winDenied)
			if !wholeOK || !winOK {
				t.Fatalf("denial types: whole-file %T, windowed %T, want *sandbox.DeniedError for both", wholeErr, winErr)
			}
		})
	}
}
