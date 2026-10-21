package main

import (
	"bytes"
	"context"
	"strings"
)

// changedFiles returns the union of `git diff BASE...HEAD`, `git diff HEAD`,
// and the untracked-but-not-ignored files -- the committed delta from the
// base, the uncommitted worktree changes, and files a new change has added
// without staging them yet. A new package is untracked until it is staged, so
// omitting that third set made `make test-changed` report nothing to run for
// exactly the change it was written to cover. Paths are repo-relative and
// deduplicated in first-seen order.
func changedFiles(root, base string) ([]string, error) {
	var files []string
	seen := map[string]bool{}
	collect := func(spec ...string) error {
		out, err := gitOutput(root, spec...)
		if err != nil {
			return err
		}
		for path := range strings.SplitSeq(out, "\x00") {
			if path == "" || seen[path] {
				continue
			}
			seen[path] = true
			files = append(files, path)
		}
		return nil
	}
	// --no-renames makes a rename two records, a deletion of the old path and
	// an addition of the new one. `--name-only` reports only the new path of a
	// rename, so the package that lost the file was never selected even though
	// its own tests stop compiling. -z keeps git from C-quoting a non-ASCII
	// path, which would otherwise be read as a literal name nobody has.
	for _, spec := range [][]string{
		{"diff", "--no-renames", "--name-only", "-z", base + "...HEAD"},
		{"diff", "--no-renames", "--name-only", "-z", "HEAD"},
		{"ls-files", "--others", "--exclude-standard", "-z"},
	} {
		if err := collect(spec...); err != nil {
			return nil, err
		}
	}
	return files, nil
}

func gitOutput(root string, args ...string) (string, error) {
	cmd := command(context.Background(), "git", args...)
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", &commandError{args: args, err: err, stderr: msg}
		}
		return "", err
	}
	return string(out), nil
}

type commandError struct {
	args   []string
	err    error
	stderr string
}

func (e *commandError) Error() string {
	return "git " + strings.Join(e.args, " ") + ": " + e.stderr
}
func (e *commandError) Unwrap() error { return e.err }
