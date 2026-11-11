// The test-redundancy subcommand finds tests a package can lose without losing
// behavior coverage, and assembles the proof a deletion PR carries. The work is
// internal/devtool/testredundancy; this file is its command line.
package dev

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"primeradiant.com/evener/internal/devtool/testredundancy"
)

const testRedundancyUsage = `usage: evener dev test-redundancy COMMAND -pkg DIR [flags]

Finds tests a Go package can lose without losing behavior coverage, and builds
the proof docs/developing-evener/testing.md ("Deleting and Consolidating
Tests") asks a deletion PR to carry. The package's cover test binary is built
once per run; each test's profile is cached under -cache by the binary's
content, so an unchanged package never profiles a test twice. Export
GOFLAGS=-trimpath so worktrees share build-cache entries. Tests are profiled
one at a time; set GOMAXPROCS to bound the load.

Commands:
  profile -tests FILE           run each named test alone and cache its profile
  unique  -tests FILE           for each named test, the statements no other
                                test in the package reaches (runs the package
                                once without the named tests)
  pairs   -tests FILE [-top N]  rank pairs of named tests by coverage
                                containment, then assertion similarity
  cover   -o FILE               profile the whole package (before and after a
                                deletion batch)
  mutate  -spec FILE -o FILE    apply each seeded mutation alone, run the tests
                                it names, restore the file with git restore,
                                and write the results as JSON
  proof   -before FILE -after FILE [-mutations FILE] [-noise N]
                                print the coverage diff and mutation results
                                as Markdown; exit 1 when the batch lost more
                                statements than the noise band

-tests FILE holds one top-level test name per line; blank lines and lines
starting with # are ignored. A mutation spec is a JSON array of
{"name", "file", "old", "new", "run"}: replace the one occurrence of old in
file (relative to -pkg) with new, and run the tests the -test.run pattern run
selects.

Flags common to every command:
  -pkg DIR     the package directory (required)
  -cache DIR   the profile cache (default: the user cache dir's
               evener/test-redundancy)
`

func testRedundancyMain(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return testRedundancyRun(ctx, args, os.Stdout, os.Stderr)
}

func testRedundancyRun(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		_, _ = fmt.Fprint(stderr, testRedundancyUsage)
		return 2
	}
	command := args[0]
	fs := flag.NewFlagSet("evener dev test-redundancy "+command, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { _, _ = fmt.Fprint(stderr, testRedundancyUsage) }
	pkg := fs.String("pkg", "", "package directory")
	cache := fs.String("cache", "", "profile cache directory")
	tests := fs.String("tests", "", "file of test names")
	top := fs.Int("top", 50, "pairs to print")
	out := fs.String("o", "", "output file")
	spec := fs.String("spec", "", "mutation spec")
	before := fs.String("before", "", "whole-package profile before the batch")
	after := fs.String("after", "", "whole-package profile after the batch")
	mutations := fs.String("mutations", "", "mutate's JSON results")
	noise := fs.Int("noise", 4, "statements two runs of the same suite can differ by")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	usageErr := func(msg string) int {
		_, _ = fmt.Fprintf(stderr, "test-redundancy %s: %s\n", command, msg)
		return 2
	}
	if *pkg == "" || fs.NArg() > 0 {
		return usageErr("-pkg is required and no positional arguments are taken")
	}
	if *cache == "" {
		dir, err := os.UserCacheDir()
		if err != nil {
			return usageErr("no -cache and no user cache dir: " + err.Error())
		}
		*cache = filepath.Join(dir, "evener", "test-redundancy")
	}
	r, err := testredundancy.NewRunner(ctx, *pkg, *cache, stderr)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "test-redundancy: %v\n", err)
		return 1
	}

	var run func() error
	switch command {
	case "profile", "unique", "pairs":
		if *tests == "" {
			return usageErr("-tests is required")
		}
		names, err := readTestNames(*tests)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "test-redundancy: %v\n", err)
			return 1
		}
		switch command {
		case "profile":
			run = func() error { return redundancyProfile(ctx, r, names, stdout) }
		case "unique":
			run = func() error { return redundancyUnique(ctx, r, names, stdout) }
		default:
			run = func() error { return redundancyPairs(ctx, r, names, *top, stdout) }
		}
	case "cover":
		if *out == "" {
			return usageErr("-o is required")
		}
		run = func() error { return redundancyCover(ctx, r, *out, stdout) }
	case "mutate":
		if *spec == "" || *out == "" {
			return usageErr("-spec and -o are required")
		}
		run = func() error { return redundancyMutate(ctx, r, *spec, *out) }
	case "proof":
		if *before == "" || *after == "" {
			return usageErr("-before and -after are required")
		}
		within, err := redundancyProof(r, *before, *after, *mutations, *noise, stdout)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "test-redundancy: %v\n", err)
			return 1
		}
		if !within {
			return 1
		}
		return 0
	default:
		return usageErr("unknown command")
	}
	if err := run(); err != nil {
		_, _ = fmt.Fprintf(stderr, "test-redundancy: %v\n", err)
		return 1
	}
	return 0
}

// readTestNames reads one test name per line, skipping blanks and # comments.
func readTestNames(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var names []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			names = append(names, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, errors.New(path + " names no tests")
	}
	return names, nil
}

func writeUnprofiled(w io.Writer, runs []testredundancy.TestRun) {
	for _, run := range runs {
		_, _ = fmt.Fprintf(w, "%s\t%s\n", run.Status, run.Name)
	}
}

func redundancyProfile(ctx context.Context, r *testredundancy.Runner, names []string, w io.Writer) error {
	runs, err := r.ProfileTests(ctx, names)
	if err != nil {
		return err
	}
	counts := map[testredundancy.Status]int{}
	var unprofiled []testredundancy.TestRun
	for _, run := range runs {
		counts[run.Status]++
		if run.Status == testredundancy.StatusFailed || run.Status == testredundancy.StatusMissing {
			unprofiled = append(unprofiled, run)
		}
	}
	_, _ = fmt.Fprintf(w, "%d ran, %d cached, %d failed, %d missing\n",
		counts[testredundancy.StatusRan], counts[testredundancy.StatusCached],
		counts[testredundancy.StatusFailed], counts[testredundancy.StatusMissing])
	writeUnprofiled(w, unprofiled)
	return nil
}

func redundancyUnique(ctx context.Context, r *testredundancy.Runner, names []string, w io.Writer) error {
	report, err := r.Unique(ctx, names)
	if err != nil {
		return err
	}
	asserts, err := testredundancy.Assertions(r.PkgDir)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(w, "STMTS\tASSERTS\tTEST\tUNIQUE BLOCKS")
	zero := 0
	for _, u := range report.Unique {
		if u.Stmts == 0 {
			zero++
		}
		blocks := make([]string, len(u.Blocks))
		for i, b := range u.Blocks {
			b.File = r.DisplayFile(b.File)
			blocks[i] = b.String()
		}
		_, _ = fmt.Fprintf(w, "%d\t%d\t%s\t%s\n", u.Stmts, len(asserts[u.Name]), u.Name, strings.Join(blocks, " "))
	}
	_, _ = fmt.Fprintf(w, "\n%d candidates profiled, %d with no unique statements, %d not profiled\n", len(report.Unique), zero, len(report.Failed))
	writeUnprofiled(w, report.Failed)
	if len(report.Rest.Failed) > 0 {
		_, _ = fmt.Fprintf(w, "failed in the package run without the candidates: %s\n", strings.Join(report.Rest.Failed, " "))
	}
	return nil
}

func redundancyPairs(ctx context.Context, r *testredundancy.Runner, names []string, top int, w io.Writer) error {
	pairs, unprofiled, err := r.Pairs(ctx, names)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(w, "CONTAIN\tASSERTSIM\tREMOVABLE\tKEEPER")
	for i, p := range pairs {
		if i >= top {
			break
		}
		_, _ = fmt.Fprintf(w, "%.2f\t%.2f\t%s\t%s\n", p.Containment, p.AssertSimilarity, p.Removable, p.Keeper)
	}
	_, _ = fmt.Fprintf(w, "\nshowing %d of %d overlapping pairs\n", min(top, len(pairs)), len(pairs))
	writeUnprofiled(w, unprofiled)
	return nil
}

func redundancyCover(ctx context.Context, r *testredundancy.Runner, out string, w io.Writer) error {
	rest, err := r.ProfileRest(ctx, nil)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(rest.Profile)
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, data, 0o644); err != nil {
		return err
	}
	p, err := testredundancy.LoadProfile(out)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, "%d of %d statements covered\n", p.Covered(), p.Total())
	if len(rest.Failed) > 0 {
		_, _ = fmt.Fprintf(w, "failed: %s\n", strings.Join(rest.Failed, " "))
	}
	return nil
}

func redundancyMutate(ctx context.Context, r *testredundancy.Runner, spec, out string) error {
	muts, err := testredundancy.LoadMutations(spec)
	if err != nil {
		return err
	}
	results, runErr := r.Mutate(ctx, muts)
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return errors.Join(runErr, err)
	}
	return errors.Join(runErr, os.WriteFile(out, append(data, '\n'), 0o644))
}

func redundancyProof(r *testredundancy.Runner, beforePath, afterPath, mutationsPath string, noise int, w io.Writer) (bool, error) {
	before, err := testredundancy.LoadProfile(beforePath)
	if err != nil {
		return false, err
	}
	after, err := testredundancy.LoadProfile(afterPath)
	if err != nil {
		return false, err
	}
	var results []testredundancy.MutationResult
	if mutationsPath != "" {
		data, err := os.ReadFile(mutationsPath)
		if err != nil {
			return false, err
		}
		if err := json.Unmarshal(data, &results); err != nil {
			return false, fmt.Errorf("%s: %w", mutationsPath, err)
		}
	}
	diff := testredundancy.DiffCoverage(before, after, noise)
	testredundancy.WriteProof(w, diff, results, r.DisplayFile)
	return diff.WithinNoise(), nil
}
