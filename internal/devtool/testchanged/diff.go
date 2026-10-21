package main

import (
	"bytes"
	"os/exec"
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
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || seen[line] {
				continue
			}
			seen[line] = true
			files = append(files, line)
		}
		return nil
	}
	for _, spec := range [][]string{
		{"diff", "--name-only", base + "...HEAD"},
		{"diff", "--name-only", "HEAD"},
		{"ls-files", "--others", "--exclude-standard"},
	} {
		if err := collect(spec...); err != nil {
			return nil, err
		}
	}
	return files, nil
}

func gitOutput(root string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
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
