// The repo-audit subcommand runs internal/devtool/repoaudit's checks over the
// git checkout around the working directory: the rules `make lint` holds the
// repository's own source, scripts and Makefile to.
package dev

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"primeradiant.com/evener/internal/devtool/repoaudit"
)

func repoAuditMain(args []string) int {
	return repoAuditRun(args, os.Stdout, os.Stderr)
}

func repoAuditRun(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		_, _ = fmt.Fprintln(stderr, "usage: evener dev repo-audit")
		return 2
	}
	// Every check reads repo-relative paths, so it runs from the checkout's root.
	out, err := exec.CommandContext(context.Background(), "git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "repo-audit: not in a git checkout: %v\n", err)
		return 2
	}
	if err := os.Chdir(strings.TrimSpace(string(out))); err != nil {
		_, _ = fmt.Fprintf(stderr, "repo-audit: %v\n", err)
		return 2
	}
	return repoaudit.Run(stdout)
}
