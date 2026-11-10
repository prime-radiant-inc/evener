// Package cilanes decides which CI lanes a pull request can skip. It reads
// the change between a base revision and HEAD, the go.work modules' import
// graph, and the paths Go source names, and prints the skippable jobs and
// matrix cells as GitHub step outputs for .github/workflows/ci.yml.
//
// A lane is skipped only when the change provably cannot reach it; every
// path the rules cannot place runs every lane. CI runs it on pull requests
// alone: merge groups and pushes to main run every lane. Its main lives in
// ./bin, apart from cmd/evener-dev/bin, because the CI job that runs it gates
// every lane and must build in seconds.
//
// Run it locally to preview a branch's plan:
//
//	go run ./cmd/evener-cilanes/bin --base origin/main
//
// docs/developing-evener/testing.md ("Pull Requests Skip Lanes a Change
// Cannot Reach") describes the rules.
package cilanes

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Run diffs --base against HEAD in --repo, writes the step outputs to
// stdout and the per-lane report to stderr. On any failure it writes no
// outputs, so the workflow runs every lane.
func Run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("evener-cilanes", flag.ContinueOnError)
	flags.SetOutput(stderr)
	repo := flags.String("repo", ".", "repository root")
	base := flags.String("base", "", "base revision; the change is base...HEAD (required)")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "usage: evener-cilanes --base REV [--repo DIR]")
		fmt.Fprintln(stderr, "Prints the CI lanes the change base...HEAD can skip, as GitHub step outputs.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *base == "" || flags.NArg() > 0 {
		flags.Usage()
		return 2
	}
	var outputs bytes.Buffer
	if err := classify(*repo, *base, &outputs, stderr); err != nil {
		fmt.Fprintf(stderr, "evener-cilanes: %v\n", err)
		return 1
	}
	if _, err := stdout.Write(outputs.Bytes()); err != nil {
		fmt.Fprintf(stderr, "evener-cilanes: %v\n", err)
		return 1
	}
	return 0
}

func classify(repo, base string, outputs, report io.Writer) error {
	tracked, err := gitPaths(repo, "ls-files", "-z")
	if err != nil {
		return err
	}
	// --no-renames lists a moved file under both names, so the package it
	// left counts as changed too.
	changed, err := gitPaths(repo, "diff", "--name-only", "-z", "--no-renames", base+"...HEAD")
	if err != nil {
		return err
	}
	ws, err := loadWorkspace(repo, tracked)
	if err != nil {
		return err
	}
	p := planLanes(ws, changed)
	if err := p.writeReport(report); err != nil {
		return err
	}
	return p.writeOutputs(outputs)
}

func gitPaths(repo string, args ...string) ([]string, error) {
	out, err := git(repo, args...)
	if err != nil {
		return nil, err
	}
	var paths []string
	for p := range strings.SplitSeq(out, "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// git runs git in dir and returns its standard output.
func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}
