// The covstmt subcommand counts statements in Go coverage profiles, so the
// shell coverage runners (coverage-floor.sh) invoke the same Go primitive the
// repo's tests pin instead of a drifted Python duplicate. Orchestration stays
// in shell; only the counting moved.
package dev

import (
	"flag"
	"fmt"
	"io"
	"os"

	"primeradiant.com/evener/internal/devtool/covstmt"
)

// covstmtMain implements `evener dev covstmt PROFILE...`: one line per profile,
// "covered total" — the shape coverage-floor.sh reads with
// `read -r covered total` for the test, fuzz, and union tracks at once.
func covstmtMain(args []string) int {
	return covstmtRun(args, os.Stdout, os.Stderr)
}

func covstmtRun(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("evener dev covstmt", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: evener dev covstmt PROFILE [PROFILE ...]")
		_, _ = fmt.Fprintln(stderr, "  prints \"covered total\" per profile, one line each")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	profiles := fs.Args()
	if len(profiles) == 0 {
		fs.Usage()
		return 2
	}
	for _, p := range profiles {
		covered, total, err := covstmt.StmtCounts(p)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "evener dev covstmt: %s: %v\n", p, err)
			return 1
		}
		_, _ = fmt.Fprintf(stdout, "%d %d\n", covered, total)
	}
	return 0
}
