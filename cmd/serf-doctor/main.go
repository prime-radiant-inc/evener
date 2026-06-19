// Command serf-doctor is the read-only forensic data plane of serf's doctoring
// system: a thin main over the agent/doctor package. It resolves a session
// selector and inspects settled on-disk state — transcript, meta, jobs.jsonl —
// with the same folds and types the serf runtime uses, so a schema change either
// flows through automatically or fails to compile.
//
// Usage:
//
//	serf-doctor locate     <selector> [--all-buckets]
//	serf-doctor transcript <selector> [--count <tool>] [--format outline|markdown] [--range last:N|start:N|A-B]
//	serf-doctor watches    <selector> [--watch <id>] [--self-loops]
//	serf-doctor tree       <selector> [--depth N] [--observers]
//
// A selector is "", local:<id>, proj:<hash>:<id>, or a bare <id>. Common flags:
// --state-dir <path> (overrides SERF_STATE_DIR / XDG default) and --json.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"primeradiant.com/serf/agent/doctor"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "locate":
		return cmdLocate(rest, stdout, stderr)
	case "transcript":
		return cmdTranscript(rest, stdout, stderr)
	case "watches":
		return cmdWatches(rest, stdout, stderr)
	case "tree":
		return cmdTree(rest, stdout, stderr)
	case "-h", "--help", "help":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "serf-doctor: unknown subcommand %q\n\n", sub)
		usage(stderr)
		return 2
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `serf-doctor — read-only forensic inspector for serf sessions, jobs, and watches

USAGE:
  serf-doctor <subcommand> <selector> [flags]

SUBCOMMANDS:
  locate      resolve a selector to its on-disk transcript/meta/jobs paths
  transcript  render a session's turns; --count <tool> prints the structural call count
  watches     watch/delivery inspector: distinct deliveries, provenance, self-loop verdict
  tree        parent ↔ delegate/observer session tree across buckets

SELECTOR:
  "" | current  (rejected — name a session)   local:<id>   proj:<hash>:<id>   <id>

COMMON FLAGS:
  --state-dir <path>   state root (default: SERF_STATE_DIR, then XDG_STATE_HOME, then ~/.local/state)
  --json               emit JSON instead of the human summary

Run "serf-doctor <subcommand> -h" for subcommand flags.
`)
}

// stateFlags registers the flags every subcommand shares and returns the set.
func stateFlags(name string, stderr io.Writer) (*flag.FlagSet, *string, *bool) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := fs.String("state-dir", "", "state root (default: SERF_STATE_DIR / XDG_STATE_HOME / ~/.local/state)")
	asJSON := fs.Bool("json", false, "emit JSON instead of the human summary")
	return fs, stateDir, asJSON
}

func selectorArg(fs *flag.FlagSet) string {
	if fs.NArg() == 0 {
		return ""
	}
	return fs.Arg(0)
}

func emitJSON(w io.Writer, v any) int {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintln(w, "serf-doctor: encode json:", err)
		return 1
	}
	return 0
}

func fail(stderr io.Writer, sub string, err error) int {
	fmt.Fprintf(stderr, "serf-doctor %s: %v\n", sub, err)
	return 1
}

func cmdLocate(args []string, stdout, stderr io.Writer) int {
	fs, stateDir, asJSON := stateFlags("locate", stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	base := doctor.ResolveStateBase(*stateDir)
	paths, err := doctor.Locate(base, selectorArg(fs))
	if err != nil {
		return fail(stderr, "locate", err)
	}
	if *asJSON {
		return emitJSON(stdout, paths)
	}
	bucket := paths.BucketHash
	if bucket == "" {
		bucket = "(override root)"
	}
	fmt.Fprintf(stdout, "session %s\n  ref:        %s\n  transcript: %s\n  meta:       %s\n  jobs:       %s\n  bucket:     %s\n",
		paths.SessionID, paths.TranscriptRef, paths.TranscriptPath, paths.MetaPath, paths.JobsPath, bucket)
	return 0
}

func cmdTranscript(args []string, stdout, stderr io.Writer) int {
	fs, stateDir, asJSON := stateFlags("transcript", stderr)
	count := fs.String("count", "", "print the structural invocation count of this tool name and exit")
	format := fs.String("format", "markdown", "render format: outline | markdown")
	rangeArg := fs.String("range", "", "turn window: last:N | start:N | A-B")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	base := doctor.ResolveStateBase(*stateDir)
	sel := selectorArg(fs)

	if *count != "" {
		res, err := doctor.Count(base, sel, *count)
		if err != nil {
			return fail(stderr, "transcript", err)
		}
		if *asJSON {
			return emitJSON(stdout, res)
		}
		fmt.Fprintln(stdout, doctor.RenderCount(res))
		return 0
	}

	res, err := doctor.Transcript(base, sel, doctor.TranscriptOpts{Format: *format, Range: *rangeArg})
	if err != nil {
		return fail(stderr, "transcript", err)
	}
	if *asJSON {
		return emitJSON(stdout, res)
	}
	fmt.Fprint(stdout, doctor.RenderTranscript(res, *format))
	return 0
}

func cmdWatches(args []string, stdout, stderr io.Writer) int {
	fs, stateDir, asJSON := stateFlags("watches", stderr)
	watchID := fs.String("watch", "", "scope to one watch_id")
	selfLoops := fs.Bool("self-loops", false, "only watches with a self-loop verdict")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	base := doctor.ResolveStateBase(*stateDir)
	res, err := doctor.Watches(base, selectorArg(fs), doctor.WatchOpts{WatchID: *watchID, SelfLoopsOnly: *selfLoops})
	if err != nil {
		return fail(stderr, "watches", err)
	}
	if *asJSON {
		return emitJSON(stdout, res)
	}
	fmt.Fprint(stdout, doctor.RenderWatches(res))
	return 0
}

func cmdTree(args []string, stdout, stderr io.Writer) int {
	fs, stateDir, asJSON := stateFlags("tree", stderr)
	depth := fs.Int("depth", 0, "max depth (0 = unlimited)")
	observers := fs.Bool("observers", false, "include observer edges, not just delegate edges")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	base := doctor.ResolveStateBase(*stateDir)
	res, err := doctor.Tree(base, selectorArg(fs), doctor.TreeOpts{Depth: *depth, Observers: *observers})
	if err != nil {
		return fail(stderr, "tree", err)
	}
	if *asJSON {
		return emitJSON(stdout, res)
	}
	fmt.Fprint(stdout, doctor.RenderTree(res))
	return 0
}
