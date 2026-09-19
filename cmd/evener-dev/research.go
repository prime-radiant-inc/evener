package dev

import (
	"fmt"
	"os"
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

// researchOracleCmd and researchRolloutCmd are wired in by their tasks.
var (
	researchOracleCmd  = func(args []string) int { _, _ = fmt.Fprint(os.Stderr, researchUsageDoc); return 2 }
	researchRolloutCmd = func(args []string) int { _, _ = fmt.Fprint(os.Stderr, researchUsageDoc); return 2 }
)
