// Command evener-dev is the dev/test infrastructure binary: it dispatches
// the dev and test tooling subcommands that an end-user install never needs
// (agent-shards, covstmt, module-lint, fuzz-harvest, fuzzcov, fuzzregistry,
// internalcheck, tomlcheck, transcript-v2-upgrade).
//
// The end-user binary is `evener`; this binary is built and used by repo
// contributors via `make` targets and `go run ./cmd/evener-dev/bin`.
package main

import (
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	devcmd "primeradiant.com/evener/cmd/evener-dev"
	fuzzharvestcmd "primeradiant.com/evener/cmd/evener-fuzz-harvest"
	fuzzcovcmd "primeradiant.com/evener/cmd/evener-fuzzcov"
	fuzzregistrycmd "primeradiant.com/evener/cmd/evener-fuzzregistry"
	internalcheckcmd "primeradiant.com/evener/cmd/evener-internalcheck"
	tomlcheckcmd "primeradiant.com/evener/cmd/evener-tomlcheck"
	transcriptv2cmd "primeradiant.com/evener/cmd/evener-transcript-v2-upgrade"
)

func main() {
	os.Exit(dispatch(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func dispatch(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}

	switch args[0] {
	case "dev":
		return devcmd.Run(args[1:], stdin, stdout, stderr)
	case "module-lint", "agent-shards":
		return devcmd.Run(args, stdin, stdout, stderr)
	case "fuzz-harvest":
		return fuzzharvestcmd.Run(args[1:], stdin, stdout, stderr)
	case "fuzzcov":
		return fuzzcovcmd.Run(args[1:], stdin, stdout, stderr)
	case "fuzzregistry":
		return fuzzregistrycmd.Run(args[1:], stdin, stdout, stderr)
	case "internalcheck":
		return internalcheckcmd.Run(args[1:], stdin, stdout, stderr)
	case "tomlcheck":
		return tomlcheckcmd.Run(args[1:], stdin, stdout, stderr)
	case "transcript-v2-upgrade":
		return transcriptv2cmd.Run(args[1:], stdin, stdout, stderr)
	case "-h", "--help", "help":
		usage(stdout)
		return 0
	default:
		_, _ = fmt.Fprintf(stderr, "evener-dev: unknown subcommand %q\n", args[0])
		usage(stderr)
		return 2
	}
}

func usage(w io.Writer) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintf(w, "Usage: evener-dev <subcommand> [flags]\n\nSubcommands:\n")
	_, _ = fmt.Fprintf(tw, "  dev\t\t\tDev tooling (agent-shards, covstmt, module-lint)\n")
	_, _ = fmt.Fprintf(tw, "  module-lint\t\tRun golangci-lint across workspace modules in parallel waves\n")
	_, _ = fmt.Fprintf(tw, "  agent-shards\t\tRun agent test shards in parallel\n")
	_, _ = fmt.Fprintf(tw, "  fuzz-harvest\t\tHarvest fuzz seed corpora from recorded traffic\n")
	_, _ = fmt.Fprintf(tw, "  fuzzcov\t\tStatic fuzz gap gate\n")
	_, _ = fmt.Fprintf(tw, "  fuzzregistry\t\tAudit the fuzz target registry\n")
	_, _ = fmt.Fprintf(tw, "  internalcheck\t\tCheck public packages don't leak internal types\n")
	_, _ = fmt.Fprintf(tw, "  tomlcheck\t\tEnforce TOML wire-format naming conventions\n")
	_, _ = fmt.Fprintf(tw, "  transcript-v2-upgrade\tConvert legacy transcript v1 files to v2\n")
	_ = tw.Flush()
}
