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
//	serf-doctor apilog     <selector> [--empty] [--errors] [--cache-spikes [--threshold N]] [--summary]
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
	"strings"

	"primeradiant.com/serf/agent/doctor"
	"primeradiant.com/serf/envvars"
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
	case "apilog":
		return cmdAPILog(rest, stdout, stderr)
	case "watches":
		return cmdWatches(rest, stdout, stderr)
	case "tree":
		return cmdTree(rest, stdout, stderr)
	case "-h", "--help", "help":
		return usage(stdout)
	default:
		if code := writef(stderr, "serf-doctor: unknown subcommand %q\n\n", sub); code != 0 {
			return code
		}
		if code := usage(stderr); code != 0 {
			return code
		}
		return 2
	}
}

func usage(w io.Writer) int {
	return writef(w, `serf-doctor — read-only forensic inspector for serf sessions, jobs, and watches

USAGE:
  serf-doctor <subcommand> <selector> [flags]

SUBCOMMANDS:
  locate      resolve a selector to its on-disk transcript/meta/jobs paths
  transcript  render a session's turns; --count <tool> prints the structural call count
  apilog      API-call diagnostics: per-call tokens/latency, empties, errors, cache spikes
  watches     watch/delivery inspector: distinct deliveries, provenance, self-loop verdict
  tree        parent ↔ delegate/observer session tree across buckets

SELECTOR:
  "" | current  (rejected — name a session)   local:<id>   proj:<hash>:<id>   <id>

COMMON FLAGS:
  --state-dir <path>   state root (default: %s, then %s, then ~/.local/state)
  --json               emit JSON instead of the human summary

Run "serf-doctor <subcommand> -h" for subcommand flags.
`, envvars.SERFStateDir.Name, envvars.XDGStateHome.Name)
}

func writef(w io.Writer, format string, args ...any) int {
	if _, err := fmt.Fprintf(w, format, args...); err != nil {
		return 1
	}
	return 0
}

func writeln(w io.Writer, args ...any) int {
	if _, err := fmt.Fprintln(w, args...); err != nil {
		return 1
	}
	return 0
}

func writeText(w io.Writer, text string) int {
	if _, err := fmt.Fprint(w, text); err != nil {
		return 1
	}
	return 0
}

// stateFlags registers the flags every subcommand shares and returns the set.
func stateFlags(name string, stderr io.Writer) (*flag.FlagSet, *string, *bool) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := fs.String("state-dir", "", fmt.Sprintf("state root (default: %s / %s / ~/.local/state)", envvars.SERFStateDir.Name, envvars.XDGStateHome.Name))
	asJSON := fs.Bool("json", false, "emit JSON instead of the human summary")
	return fs, stateDir, asJSON
}

// parseSelectorAndFlags parses flags while supporting the documented
// `serf-doctor <cmd> <selector> [flags]` form (selector first — the form the
// skill docs and the doctor persona use), as well as `<cmd> [flags] <selector>`.
// Go's flag package stops at the first non-flag arg, so a bare leading selector
// is peeled off before parsing the rest. Returns the selector and a process exit
// code (0 to proceed, 2 on a flag parse error).
func parseSelectorAndFlags(fs *flag.FlagSet, args []string) (string, int) {
	var selector string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		selector = args[0]
		args = args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return "", 2
	}
	if selector == "" && fs.NArg() > 0 {
		selector = fs.Arg(0)
	}
	return selector, 0
}

func emitJSON(w io.Writer, v any) int {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		_ = writeln(w, "serf-doctor: encode json:", err)
		return 1
	}
	return 0
}

func fail(stderr io.Writer, sub string, err error) int {
	if code := writef(stderr, "serf-doctor %s: %v\n", sub, err); code != 0 {
		return code
	}
	return 1
}

func cmdLocate(args []string, stdout, stderr io.Writer) int {
	fs, stateDir, asJSON := stateFlags("locate", stderr)
	sel, code := parseSelectorAndFlags(fs, args)
	if code != 0 {
		return code
	}
	base := doctor.ResolveStateBase(*stateDir)
	paths, err := doctor.Locate(base, sel)
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
	return writef(stdout, "session %s\n  ref:        %s\n  transcript: %s\n  meta:       %s\n  jobs:       %s\n  bucket:     %s\n",
		paths.SessionID, paths.TranscriptRef, paths.TranscriptPath, paths.MetaPath, paths.JobsPath, bucket)
}

func cmdTranscript(args []string, stdout, stderr io.Writer) int {
	fs, stateDir, asJSON := stateFlags("transcript", stderr)
	count := fs.String("count", "", "print the structural invocation count of this tool name and exit")
	format := fs.String("format", "markdown", "render format: outline | markdown")
	rangeArg := fs.String("range", "", "turn window: last:N | start:N | A-B")
	sel, code := parseSelectorAndFlags(fs, args)
	if code != 0 {
		return code
	}
	base := doctor.ResolveStateBase(*stateDir)

	if *count != "" {
		res, err := doctor.Count(base, sel, *count)
		if err != nil {
			return fail(stderr, "transcript", err)
		}
		if *asJSON {
			return emitJSON(stdout, res)
		}
		return writeln(stdout, doctor.RenderCount(res))
	}

	res, err := doctor.Transcript(base, sel, doctor.TranscriptOpts{Format: *format, Range: *rangeArg})
	if err != nil {
		return fail(stderr, "transcript", err)
	}
	if *asJSON {
		return emitJSON(stdout, res)
	}
	return writeText(stdout, doctor.RenderTranscript(res, *format))
}

func cmdAPILog(args []string, stdout, stderr io.Writer) int {
	fs, stateDir, asJSON := stateFlags("apilog", stderr)
	empty := fs.Bool("empty", false, "only empty responses (no text, no tool calls)")
	errorsOnly := fs.Bool("errors", false, "only failed calls")
	cacheSpikes := fs.Bool("cache-spikes", false, "only calls whose uncached input >= --threshold")
	threshold := fs.Int("threshold", 0, "uncached-input-token floor for --cache-spikes (default 50000)")
	summary := fs.Bool("summary", false, "render only the per-session aggregate")
	sel, code := parseSelectorAndFlags(fs, args)
	if code != 0 {
		return code
	}
	base := doctor.ResolveStateBase(*stateDir)
	opts := doctor.APILogOpts{
		EmptyOnly:      *empty,
		ErrorsOnly:     *errorsOnly,
		CacheSpikes:    *cacheSpikes,
		SpikeThreshold: *threshold,
		SummaryOnly:    *summary,
	}
	res, err := doctor.APILog(base, sel, opts)
	if err != nil {
		return fail(stderr, "apilog", err)
	}
	if *asJSON {
		return emitJSON(stdout, res)
	}
	return writeText(stdout, doctor.RenderAPILog(res, opts))
}

func cmdWatches(args []string, stdout, stderr io.Writer) int {
	fs, stateDir, asJSON := stateFlags("watches", stderr)
	watchID := fs.String("watch", "", "scope to one watch_id")
	selfLoops := fs.Bool("self-loops", false, "only watches with a self-loop verdict")
	sel, code := parseSelectorAndFlags(fs, args)
	if code != 0 {
		return code
	}
	base := doctor.ResolveStateBase(*stateDir)
	res, err := doctor.Watches(base, sel, doctor.WatchOpts{WatchID: *watchID, SelfLoopsOnly: *selfLoops})
	if err != nil {
		return fail(stderr, "watches", err)
	}
	if *asJSON {
		return emitJSON(stdout, res)
	}
	return writeText(stdout, doctor.RenderWatches(res))
}

func cmdTree(args []string, stdout, stderr io.Writer) int {
	fs, stateDir, asJSON := stateFlags("tree", stderr)
	depth := fs.Int("depth", 0, "max depth (0 = unlimited)")
	observers := fs.Bool("observers", false, "include observer edges, not just delegate edges")
	sel, code := parseSelectorAndFlags(fs, args)
	if code != 0 {
		return code
	}
	base := doctor.ResolveStateBase(*stateDir)
	res, err := doctor.Tree(base, sel, doctor.TreeOpts{Depth: *depth, Observers: *observers})
	if err != nil {
		return fail(stderr, "tree", err)
	}
	if *asJSON {
		return emitJSON(stdout, res)
	}
	return writeText(stdout, doctor.RenderTree(res))
}
