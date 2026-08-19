// Command evener-migrate migrates user data from legacy serf paths to evener
// paths. It moves the legacy state root (~/.serf), the XDG config root
// (~/.config/serf), the XDG state root (~/.local/state/serf), and per-project
// .serf directories to their evener counterparts.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"primeradiant.com/evener/envvars"
)

// migration tracks a single source -> destination directory move.
type migration struct {
	src  string
	dst  string
	kind string
}

// options holds resolved paths and flags for a migration run.
type options struct {
	dryRun     bool
	verbose    bool
	home       string
	configBase string
	stateBase  string
	cwd        string
}

type status int

const (
	statusMoved status = iota
	statusSkipped
	statusFailed
)

type report struct {
	moved   int
	skipped int
	failed  int
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("evener-migrate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dryRun := flags.Bool("dry-run", false, "preview changes without moving anything")
	verbose := flags.Bool("verbose", false, "print detailed output including skipped paths")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: evener-migrate [--dry-run] [--verbose]")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "evener-migrate: positional arguments are not accepted")
		return 2
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "evener-migrate: resolve home directory: %v\n", err)
		return 1
	}

	configBase := os.Getenv(envvars.XDGConfigHome.Name)
	if configBase == "" {
		configBase = filepath.Join(home, ".config")
	}

	stateBase := os.Getenv(envvars.XDGStateHome.Name)
	if stateBase == "" {
		stateBase = filepath.Join(home, ".local", "state")
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "evener-migrate: get current directory: %v\n", err)
		return 1
	}

	return execute(options{
		dryRun:     *dryRun,
		verbose:    *verbose,
		home:       home,
		configBase: configBase,
		stateBase:  stateBase,
		cwd:        cwd,
	}, stdout, stderr)
}

func execute(opts options, stdout, stderr io.Writer) int {
	var rep report

	migrations := []migration{
		{src: filepath.Join(opts.home, ".serf"), dst: filepath.Join(opts.home, ".evener"), kind: "legacy state root"},
		{src: filepath.Join(opts.configBase, "serf"), dst: filepath.Join(opts.configBase, "evener"), kind: "config root"},
		{src: filepath.Join(opts.stateBase, "serf"), dst: filepath.Join(opts.stateBase, "evener"), kind: "XDG state root"},
	}
	migrations = append(migrations, discoverProjectMigrations(opts.cwd)...)

	seen := map[string]bool{}
	for _, m := range migrations {
		if seen[m.src] {
			continue
		}
		seen[m.src] = true
		switch migrate(m, opts.dryRun, opts.verbose, stdout, stderr) {
		case statusMoved:
			rep.moved++
		case statusSkipped:
			rep.skipped++
		case statusFailed:
			rep.failed++
		}
	}

	if opts.dryRun {
		fmt.Fprintf(stdout, "\ndry-run report: would_move=%d skipped=%d failed=%d\n", rep.moved, rep.skipped, rep.failed)
	} else {
		fmt.Fprintf(stdout, "\nmigration report: moved=%d skipped=%d failed=%d\n", rep.moved, rep.skipped, rep.failed)
	}
	if rep.failed > 0 {
		return 1
	}
	return 0
}

// migrate moves src to dst. If dst already exists it refuses to overwrite and
// returns statusSkipped. If src does not exist it returns statusSkipped
// silently (or prints a message when verbose). When dryRun is true it reports
// what would happen without moving.
func migrate(m migration, dryRun, verbose bool, stdout, stderr io.Writer) status {
	_, srcErr := os.Lstat(m.src)
	if errors.Is(srcErr, os.ErrNotExist) {
		if verbose {
			fmt.Fprintf(stdout, "skip  %s (source does not exist)\n", m.src)
		}
		return statusSkipped
	}
	if srcErr != nil {
		fmt.Fprintf(stderr, "evener-migrate: stat %s: %v\n", m.src, srcErr)
		return statusFailed
	}

	_, dstErr := os.Lstat(m.dst)
	if dstErr == nil {
		fmt.Fprintf(stdout, "skip  %s -> %s (destination already exists)\n", m.src, m.dst)
		return statusSkipped
	}
	if !errors.Is(dstErr, os.ErrNotExist) {
		fmt.Fprintf(stderr, "evener-migrate: stat %s: %v\n", m.dst, dstErr)
		return statusFailed
	}

	if dryRun {
		fmt.Fprintf(stdout, "would move  %s -> %s\n", m.src, m.dst)
		return statusMoved
	}

	if err := os.Rename(m.src, m.dst); err != nil {
		fmt.Fprintf(stderr, "evener-migrate: move %s -> %s: %v\n", m.src, m.dst, err)
		return statusFailed
	}
	fmt.Fprintf(stdout, "moved  %s -> %s\n", m.src, m.dst)
	return statusMoved
}

// discoverProjectMigrations scans the current directory and ancestor git roots
// for .serf directories that should be migrated to .evener.
func discoverProjectMigrations(cwd string) []migration {
	var migrations []migration
	seen := map[string]bool{}

	add := func(projectDir string) {
		dst := filepath.Join(projectDir, ".evener")
		if seen[dst] {
			return
		}
		seen[dst] = true
		migrations = append(migrations, migration{
			src:  filepath.Join(projectDir, ".serf"),
			dst:  dst,
			kind: "project config",
		})
	}

	add(cwd)

	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			add(dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return migrations
}
