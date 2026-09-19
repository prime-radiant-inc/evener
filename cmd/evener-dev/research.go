package dev

import (
	"flag"
	"fmt"
	"os"

	"primeradiant.com/evener/agent/doctor"
	"primeradiant.com/evener/agent/research"
)

// researchUsageDoc is the usage text for the research subcommand family.
const researchUsageDoc = `usage: evener-dev research <subcommand> [flags]

subcommands:
  oracle    measure harness overhead signals in real session transcripts
  rollout   run paired harness rollouts against research environments
`

// runResearch dispatches the research subcommand family. Subcommands are
// wired as they land: oracle (slice 1), rollout (slice 1), gate/freeze/
// validate (slice 2).
func runResearch(args []string) int {
	if len(args) < 1 {
		_, _ = fmt.Fprint(os.Stderr, researchUsageDoc)
		return 2
	}
	switch args[0] {
	case "oracle":
		return researchOracleCmd(args[1:])
	case "rollout":
		return researchRolloutCmd(args[1:])
	default:
		_, _ = fmt.Fprintf(os.Stderr, "evener-dev research: unknown subcommand %q\n%s", args[0], researchUsageDoc)
		return 2
	}
}

// researchRolloutCmd is wired in by its task.
var researchRolloutCmd = func(args []string) int { _, _ = fmt.Fprint(os.Stderr, researchUsageDoc); return 2 }

// researchOracleCmd implements `evener-dev research oracle`: measure harness
// overhead signals in real session transcripts and print the ranked report.
func researchOracleCmd(args []string) int {
	fs := flag.NewFlagSet("evener-dev research oracle", flag.ContinueOnError)
	stateDir := fs.String("state-dir", "", "state base to walk (default: resolved like evener doctor)")
	limit := fs.Int("limit", 30, "newest N sessions (0 = all)")
	out := fs.String("out", "", "append the report record to this JSONL path")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	base := *stateDir
	if base == "" {
		base = doctor.ResolveStateBase("")
	}
	rep, err := research.RunOracle(research.OracleOptions{
		StateBase: base, Limit: *limit, OutPath: *out, Stdout: os.Stdout,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	_ = rep
	return 0
}
