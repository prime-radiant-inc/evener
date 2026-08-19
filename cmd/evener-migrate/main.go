// Command evener-migrate migrates user data to the final evener layout. It
// handles three source generations:
//
//   - legacy (pre-rename): ~/.serf, ~/.config/serf, ~/.local/state/serf, and
//     per-project .serf directories.
//   - interim (post-rename, pre-consolidation): ~/.evener held the same
//     files ~/.serf did — providers.toml, credentials.toml, hub.toml,
//     launch.toml, auth-token, index.db, hub.lock, run/, deletions/ — never
//     split into the XDG config/state roots the way ~/.config/evener and
//     ~/.local/state/evener already were.
//   - final: ~/.config/evener holds the user-editable config files;
//     ~/.local/state/evener holds the machine-generated state.
//
// ~/.config/serf and ~/.local/state/serf move wholesale to their evener
// counterparts (their contents were already correctly split, just under the
// old name). ~/.serf and ~/.evener are retired entirely: each file/directory
// they held moves individually into the config or state root — see
// homeRootFiles. Per-project .serf directories also move wholesale to
// .evener.
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

// migration tracks a single source -> destination move (a file or a
// directory; os.Rename handles both identically).
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

// homeRootFile describes one file or directory that used to live directly
// under the legacy (~/.serf) or interim (~/.evener) home root, and which XDG
// root it belongs under in the final layout.
type homeRootFile struct {
	name string // basename under the home root
	xdg  string // "config" or "state"
	kind string // migration report label
}

// homeRootFiles is the complete inventory of what ~/.serf and ~/.evener held:
// providers.toml/credentials.toml/hub.toml/launch.toml are user-editable
// configuration, so they move to the XDG config root; auth-token/index.db/
// hub.lock/run/deletions are machine-generated (or, for hub.lock and run,
// runtime-coordination) state, so they move to the XDG state root — see the
// PR's mapping table for the per-file config/state rationale.
//
// cmdutil.checkLegacyDataDirs (cmdutil/legacymigration.go) uses the identical
// basename list to decide whether either home root still holds unmigrated
// data. It is duplicated here rather than shared because evener-migrate is a
// deliberately dependency-light standalone binary that does not import
// cmdutil.
var homeRootFiles = []homeRootFile{
	{name: "providers.toml", xdg: "config", kind: "provider config"},
	{name: "credentials.toml", xdg: "config", kind: "credentials"},
	{name: "hub.toml", xdg: "config", kind: "hub config"},
	{name: "launch.toml", xdg: "config", kind: "launch config"},
	{name: "auth-token", xdg: "state", kind: "hub auth token"},
	{name: "index.db", xdg: "state", kind: "past-session index"},
	{name: "hub.lock", xdg: "state", kind: "hub lock"},
	{name: "run", xdg: "state", kind: "daemon rendezvous/log directory"},
	{name: "deletions", xdg: "state", kind: "deletion-fence state"},
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
		_, _ = fmt.Fprintln(stderr, "Usage: evener-migrate [--dry-run] [--verbose]")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "evener-migrate: positional arguments are not accepted")
		return 2
	}

	home, err := os.UserHomeDir()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "evener-migrate: resolve home directory: %v\n", err)
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
		_, _ = fmt.Fprintf(stderr, "evener-migrate: get current directory: %v\n", err)
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
		{src: filepath.Join(opts.configBase, "serf"), dst: filepath.Join(opts.configBase, "evener"), kind: "config root"},
		{src: filepath.Join(opts.stateBase, "serf"), dst: filepath.Join(opts.stateBase, "evener"), kind: "XDG state root"},
	}
	migrations = append(migrations, homeRootMigrations(opts)...)
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

	if !opts.dryRun {
		// The retired home roots hold nothing evener-migrate doesn't already
		// know how to place elsewhere — once every known file is out, the
		// directory itself should not linger.
		for _, homeRoot := range []string{filepath.Join(opts.home, ".serf"), filepath.Join(opts.home, ".evener")} {
			removeEmptyHomeRoot(homeRoot, opts.verbose, stdout)
		}
		repairHomeRootFileReferences(opts, stdout, stderr)
	}

	if opts.dryRun {
		_, _ = fmt.Fprintf(stdout, "\ndry-run report: would_move=%d skipped=%d failed=%d\n", rep.moved, rep.skipped, rep.failed)
	} else {
		_, _ = fmt.Fprintf(stdout, "\nmigration report: moved=%d skipped=%d failed=%d\n", rep.moved, rep.skipped, rep.failed)
	}
	if rep.failed > 0 {
		return 1
	}
	return 0
}

// homeRootMigrations builds the per-file migrations for both the legacy
// (~/.serf) and interim (~/.evener) home roots into the final config/state
// roots. Both candidate sources are always included: a machine that never
// went through the "serf" era simply finds nothing under ~/.serf, and one
// that already consolidated finds nothing under either. Legacy entries are
// listed first, so that if both a legacy and an interim copy of the same
// file somehow exist, the legacy one wins the move — migrate()'s
// refuse-don't-clobber semantics then skip the interim leftover rather than
// merging or overwriting.
func homeRootMigrations(opts options) []migration {
	homeRoots := []struct {
		path string
		kind string
	}{
		{path: filepath.Join(opts.home, ".serf"), kind: "legacy"},
		{path: filepath.Join(opts.home, ".evener"), kind: "interim"},
	}
	migrations := make([]migration, 0, len(homeRoots)*len(homeRootFiles))
	for _, homeRoot := range homeRoots {
		for _, hf := range homeRootFiles {
			migrations = append(migrations, migration{
				src:  filepath.Join(homeRoot.path, hf.name),
				dst:  homeRootFileDest(opts, hf),
				kind: homeRoot.kind + " " + hf.kind,
			})
		}
	}
	return migrations
}

// removeEmptyHomeRoot removes dir if it exists and is now empty — the state
// after every known homeRootFiles entry it held has been moved out. It is
// best-effort and silent: a missing dir (nothing to remove) and a non-empty
// one (unrecognized content evener-migrate deliberately leaves alone,
// consistent with refuse-don't-clobber) both simply return.
func removeEmptyHomeRoot(dir string, verbose bool, stdout io.Writer) {
	if err := os.Remove(dir); err != nil {
		return
	}
	if verbose {
		_, _ = fmt.Fprintf(stdout, "removed empty %s\n", dir)
	}
}

// homeRootFileDest resolves a homeRootFile to its final XDG path.
func homeRootFileDest(opts options, hf homeRootFile) string {
	base := opts.configBase
	if hf.xdg == "state" {
		base = opts.stateBase
	}
	return filepath.Join(base, "evener", hf.name)
}

// migrate moves src to dst. If dst already exists it refuses to overwrite and
// returns statusSkipped. If src does not exist it returns statusSkipped
// silently (or prints a message when verbose). When dryRun is true it reports
// what would happen without moving.
//
// Whenever dst ends up existing after this call — whether because it was
// just moved into place, it already existed, or a prior run already moved it
// — migrate rewrites any leftover occurrences of src (the legacy absolute
// path) inside dst's files to dst (see rewriteLegacyPaths). This is what
// makes a re-run repair a machine that migrated before this rewrite existed:
// the source is long gone, but stale absolute paths recorded inside the
// already-migrated tree (e.g. a plugin marketplace registry's
// installLocation) still get fixed.
func migrate(m migration, dryRun, verbose bool, stdout, stderr io.Writer) status {
	_, srcErr := os.Lstat(m.src)
	srcMissing := errors.Is(srcErr, os.ErrNotExist)
	if srcErr != nil && !srcMissing {
		_, _ = fmt.Fprintf(stderr, "evener-migrate: stat %s: %v\n", m.src, srcErr)
		return statusFailed
	}

	_, dstErr := os.Lstat(m.dst)
	dstExists := dstErr == nil
	if dstErr != nil && !errors.Is(dstErr, os.ErrNotExist) {
		_, _ = fmt.Fprintf(stderr, "evener-migrate: stat %s: %v\n", m.dst, dstErr)
		return statusFailed
	}

	if srcMissing {
		if verbose {
			_, _ = fmt.Fprintf(stdout, "skip  %s (source does not exist)\n", m.src)
		}
		if dstExists && !dryRun {
			repairLegacyPaths(m, stdout, stderr)
		}
		return statusSkipped
	}

	if dstExists {
		_, _ = fmt.Fprintf(stdout, "skip  %s -> %s (destination already exists)\n", m.src, m.dst)
		if !dryRun {
			repairLegacyPaths(m, stdout, stderr)
		}
		return statusSkipped
	}

	if dryRun {
		_, _ = fmt.Fprintf(stdout, "would move  %s -> %s\n", m.src, m.dst)
		return statusMoved
	}

	// The destination's parent may not exist yet: the whole-directory pairs
	// (config/state XDG roots) always land beside an existing home/XDG base,
	// but a homeRootFile destination (e.g. ~/.config/evener/providers.toml)
	// can be the very first thing to land in a config/state root that
	// EnsureUserConfigDirs hasn't created yet.
	if err := os.MkdirAll(filepath.Dir(m.dst), 0o700); err != nil {
		_, _ = fmt.Fprintf(stderr, "evener-migrate: create %s: %v\n", filepath.Dir(m.dst), err)
		return statusFailed
	}

	if err := os.Rename(m.src, m.dst); err != nil {
		_, _ = fmt.Fprintf(stderr, "evener-migrate: move %s -> %s: %v\n", m.src, m.dst, err)
		return statusFailed
	}
	_, _ = fmt.Fprintf(stdout, "moved  %s -> %s\n", m.src, m.dst)
	repairLegacyPaths(m, stdout, stderr)
	return statusMoved
}

// repairLegacyPaths rewrites any leftover references to m.src inside m.dst's
// files. Errors are reported but non-fatal: the move itself already
// succeeded, and a content-rewrite failure (e.g. a permission error on one
// file) shouldn't be reported as a failed migration.
func repairLegacyPaths(m migration, stdout, stderr io.Writer) {
	if err := rewriteLegacyPaths(m.dst, m.src, m.dst, stdout); err != nil {
		_, _ = fmt.Fprintf(stderr, "evener-migrate: rewriting legacy paths under %s: %v\n", m.dst, err)
	}
}

// repairHomeRootFileReferences rewrites leftover absolute references to a
// retired home-root file (~/.serf/X or ~/.evener/X) that a DIFFERENT
// migrated file still embeds — e.g. a hand-edited hub.toml's own
// hub_state_root/run_dir/past_index_db keys, which name sibling files under
// the same old home root, not hub.toml's own old path (that self-reference
// is already covered by migrate()'s per-migration repair above). It runs
// once per invocation, after every home-root file migration has been
// attempted, over both new roots — a stored reference could in principle
// appear in either.
func repairHomeRootFileReferences(opts options, stdout, stderr io.Writer) {
	newConfigRoot := filepath.Join(opts.configBase, "evener")
	newStateRoot := filepath.Join(opts.stateBase, "evener")
	for _, homeRoot := range []string{filepath.Join(opts.home, ".serf"), filepath.Join(opts.home, ".evener")} {
		for _, hf := range homeRootFiles {
			oldPath := filepath.Join(homeRoot, hf.name)
			newPath := homeRootFileDest(opts, hf)
			for _, tree := range []string{newConfigRoot, newStateRoot} {
				if _, err := os.Lstat(tree); err != nil {
					continue // nothing migrated there yet
				}
				if err := rewriteLegacyPaths(tree, oldPath, newPath, stdout); err != nil {
					_, _ = fmt.Fprintf(stderr, "evener-migrate: rewriting legacy paths under %s: %v\n", tree, err)
				}
			}
		}
	}
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
