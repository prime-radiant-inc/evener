package dev

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

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

func researchRolloutCmd(args []string) int {
	fs := flag.NewFlagSet("evener-dev research rollout", flag.ContinueOnError)
	envDir := fs.String("env-dir", "research/environments", "environments directory")
	var envs multiFlag
	fs.Var(&envs, "env", "environment name (repeatable)")
	model := fs.String("model", "", "provider/model for the session")
	binary := fs.String("binary", "evener", "harness binary to run")
	arm := fs.String("arm", "base", "arm label recorded in run rows")
	runs := fs.Int("runs", 1, "repetitions per environment")
	maxRounds := fs.Int("max-rounds", 40, "session round cap")
	runDir := fs.String("run-dir", "", "scratch run directory (required)")
	live := fs.Bool("live", false, "enable a live provider-backed pass")
	maxLiveRuns := fs.Int("max-live-runs", 48, "cap on total planned runs")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *runDir == "" {
		fmt.Fprintln(os.Stderr, "--run-dir is required")
		return 2
	}
	if len(envs) == 0 {
		fmt.Fprintln(os.Stderr, "--env is required (at least one)")
		return 2
	}
	ctx := context.Background()
	err := research.RunRollouts(ctx, research.RolloutOptions{
		EnvDir: *envDir, Envs: envs, Arm: *arm, Model: *model, Binary: *binary,
		Runs: *runs, MaxRounds: *maxRounds, RunDir: *runDir,
		Live: *live, MaxLiveRuns: *maxLiveRuns, Stdout: os.Stdout,
	}, research.ExecExecutor{Binary: *binary, Live: *live})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// multiFlag collects repeated string flag values.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

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
