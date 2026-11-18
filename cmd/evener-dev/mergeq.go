package dev

// merge-enqueue is the decision half of scripts/ops/merge-enqueue.sh: the
// launcher parses the script's own arguments and keeps its contract, and this
// subcommand owns the queue's decisions (internal/devtool/mergeq). It was a
// shell script whose only test seam was a fake `gh` on PATH, which
// docs/developing-evener/testing.md bans; the decisions moved here under
// `go test` instead, which is that rule's port-on-touch remedy.
//
// The flags mirror the script's, and so do the exit codes: 0 for queued or
// merged, 1 for a drop or a timeout, 2 for a usage error or a failed enqueue
// call.
//
// A `gh` this subcommand did not find is reported like any other failed read:
// the merge queue is GitHub's, so there is no second client to substitute for
// it. The client and the wait's clock are parameters for the same reason the
// decisions are a package: a test supplies GitHub's answers and a clock, and
// nothing stands in for the tool.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"primeradiant.com/evener/internal/devtool/mergeq"
)

func mergeEnqueueMain(args []string) int {
	return runMergeEnqueue(args, mergeq.GHClient{}, mergeq.SystemClock, os.Stdout, os.Stderr)
}

func runMergeEnqueue(args []string, c mergeq.Client, clock mergeq.Clock, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("evener dev merge-enqueue", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", "prime-radiant-inc/evener", "repository, OWNER/NAME")
	pr := fs.String("pr", "", "pull request number")
	// A string, not an Int: Go's flag package reads a leading zero as octal, so
	// "--wait 08" would be a parse error, and the script this replaces has
	// always read whole seconds as decimal (issue #5304). Parsing here keeps one
	// meaning for the flag whichever entry point the caller uses.
	waitSeconds := fs.String("wait", "0", "seconds to poll after enqueueing; 0 reports the position and stops")
	jump := fs.Bool("jump", false, "put the PR at the front of the queue (admins only); refuses an already queued PR")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: evener dev merge-enqueue --pr N [--wait SECONDS] [--jump] [--repo OWNER/NAME]")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	wait, err := strconv.Atoi(*waitSeconds)
	if err != nil || wait < 0 {
		_, _ = fmt.Fprintf(stderr, "evener dev merge-enqueue: --wait takes whole seconds, got %q\n", *waitSeconds)
		fs.Usage()
		return 2
	}
	if *pr == "" || len(fs.Args()) > 0 {
		fs.Usage()
		return 2
	}
	opts := mergeq.Options{
		Repo: *repo,
		PR:   *pr,
		Wait: time.Duration(wait) * time.Second,
		Jump: *jump,
	}
	// A wait must end when the operator interrupts it, not leave a poll
	// running behind a gone parent.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return mergeq.Run(ctx, opts, c, clock, stdout, stderr)
}
