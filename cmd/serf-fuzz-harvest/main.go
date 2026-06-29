// Command serf-fuzz-harvest turns recorded serf traffic — provider SSE bodies
// (api-raw.jsonl), conversation transcripts, the opt-in AppWire/HTTP recorder
// logs, and jobs.jsonl — into Go fuzz seed corpora under each target's native
// testdata/fuzz/<FuzzName>/.
//
// By default every string/number leaf is shape-scrubbed to a synthetic
// placeholder (structure, framing, and a small set of structural enum values
// survive), so committed seeds carry no PII or secrets by construction. An
// always-on abort gate (known-secret regexes + entropy quarantine) drops any
// seed in which a secret survived and fails the run. --keep-values (gated to a
// designated capture box, ignored for a personal ~/.serf) preserves real values
// for local-only campaigns and is never committed.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/doctor"
	"primeradiant.com/serf/cmdutil"
	"primeradiant.com/serf/envvars"
)

type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ",") }
func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

const defaultMaxSeedBytes = 32768

var allSurfaces = []string{"sse", "toolargs", "appwire", "http", "jobs"}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serf-fuzz-harvest", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var stateDirs stringSlice
	fs.Var(&stateDirs, "state-dir", "state directory to harvest (repeatable; default: the serf state root)")
	outRoot := fs.String("out-root", ".", "repo root under which seeds land in each target's testdata/fuzz/<Name>/")
	surfaceList := fs.String("surface", strings.Join(allSurfaces, ","), "comma-separated surfaces to harvest")
	keepValues := fs.Bool("keep-values", false, "skip shape-scrub, keep real values (GATED; local-only, never committed)")
	maxSeedBytes := fs.Int("max-seed-bytes", defaultMaxSeedBytes, "drop seeds whose data exceeds this many bytes")
	dryRun := fs.Bool("dry-run", false, "report counts and would-write paths; write nothing")
	logPath := fs.String("log", "", "write per-seed provenance to this file")
	noGitleaks := fs.Bool("no-gitleaks", false, "skip the post-write gitleaks barrier")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	surfaces, err := parseSurfaces(*surfaceList)
	if err != nil {
		fmt.Fprintln(stderr, err) //nolint:errcheck
		return 2
	}

	if *keepValues && envvars.SERFFuzzCaptureEnv.Getenv() == "" {
		fmt.Fprintf(stderr, "refusing --keep-values: %s is not set (run only on a designated capture box)\n", envvars.SERFFuzzCaptureEnv.Name) //nolint:errcheck
		return 2
	}

	if len(stateDirs) == 0 {
		stateDirs = defaultStateDirs()
	}

	var logw io.Writer
	if *logPath != "" {
		f, err := os.Create(*logPath)
		if err != nil {
			fmt.Fprintf(stderr, "open log: %v\n", err) //nolint:errcheck
			return 2
		}
		defer f.Close() //nolint:errcheck
		logw = f
	}

	var coreNames []string
	if surfaces["toolargs"] {
		coreNames, err = agent.CoreToolNames()
		if err != nil {
			fmt.Fprintf(stderr, "resolve core tool names: %v\n", err) //nolint:errcheck
			return 1
		}
	}

	emit := NewEmitter(*dryRun, *maxSeedBytes)
	r := newRunner(*outRoot, emit, logw)

	for _, sd := range stateDirs {
		personal := isPersonalStateDir(sd)
		san := &Sanitizer{keepValues: *keepValues && !personal}
		if *keepValues && personal {
			fmt.Fprintf(stderr, "note: --keep-values ignored for personal state dir %s (shape-scrub forced)\n", sd) //nolint:errcheck
		}
		src, err := discoverSources(sd)
		if err != nil {
			fmt.Fprintf(stderr, "discover %s: %v\n", sd, err) //nolint:errcheck
			continue
		}
		if surfaces["sse"] {
			harvestSSE(r, san, src.sse)
		}
		if surfaces["toolargs"] {
			harvestToolArgs(r, san, src.transcripts, coreNames)
		}
		if surfaces["appwire"] {
			harvestAppwire(r, san, src.appwire)
		}
		if surfaces["http"] {
			harvestHTTP(r, src.http)
		}
		if surfaces["jobs"] {
			harvestJobs(r, san, src.jobs)
		}
	}

	fmt.Fprint(stdout, r.summary()) //nolint:errcheck

	exit := 0
	if r.leaks > 0 {
		fmt.Fprintf(stderr, "FAIL: %d seed(s) dropped because a secret survived sanitization\n", r.leaks) //nolint:errcheck
		exit = 1
	}

	if !*dryRun && !*noGitleaks {
		switch clean, available := gitleaksScan(r.dir("."), stderr); {
		case !available:
			fmt.Fprintln(stderr, "note: gitleaks not found; skipped the post-write secret-scan barrier") //nolint:errcheck
		case !clean:
			fmt.Fprintln(stderr, "FAIL: gitleaks found a secret in the written corpus") //nolint:errcheck
			exit = 1
		}
	}
	return exit
}

// defaultStateDirs returns the roots to harvest when no --state-dir is given.
// serf splits its on-disk state across two roots: provider config and the raw
// API log live under cmdutil.DefaultStateRoot (~/.serf), while session
// transcripts and jobs.jsonl live under the XDG session base
// (doctor.ResolveStateBase, ~/.local/state/serf). Both are walked, deduped, and
// scoped to the serf/ subtree where one exists.
func defaultStateDirs() stringSlice {
	seen := map[string]bool{}
	var out stringSlice
	add := func(dir string) {
		abs, err := filepath.Abs(dir)
		if err != nil {
			abs = dir
		}
		if seen[abs] {
			return
		}
		seen[abs] = true
		out = append(out, dir)
	}
	add(cmdutil.DefaultStateRoot())
	base := doctor.ResolveStateBase("")
	if sub := filepath.Join(base, "serf"); isDir(sub) {
		add(sub)
	} else {
		add(base)
	}
	return out
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func parseSurfaces(csv string) (map[string]bool, error) {
	known := map[string]bool{}
	for _, s := range allSurfaces {
		known[s] = true
	}
	out := map[string]bool{}
	for _, part := range strings.Split(csv, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !known[part] {
			return nil, fmt.Errorf("unknown surface %q (known: %s)", part, strings.Join(allSurfaces, ","))
		}
		out[part] = true
	}
	if len(out) == 0 {
		return nil, errors.New("no surfaces selected")
	}
	return out, nil
}
