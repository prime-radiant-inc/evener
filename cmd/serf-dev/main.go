// serf-dev is the home of the dev tooling that outgrew shell (see
// docs/superpowers/specs/2026-08-17-dev-tooling-in-go-design.md): one
// subcommand per retired script, invoked from the Makefile and the remaining
// scripts as `go run ./cmd/serf-dev <subcommand> ...`. Subcommand env and
// output contracts are the retired scripts' contracts; their Go tests are the
// selftests those scripts used to fake with PATH stubs.
package main

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

var subcommands = map[string]func(args []string) int{
	"agent-shards": runAgentShards,
	"module-lint":  lintMain,
}

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	run, ok := subcommands[os.Args[1]]
	if !ok {
		_, _ = fmt.Fprintf(os.Stderr, "serf-dev: unknown subcommand %q\n", os.Args[1])
		usage(os.Stderr)
		os.Exit(2)
	}
	os.Exit(run(os.Args[2:]))
}

func usage(w io.Writer) {
	names := make([]string, 0, len(subcommands))
	for name := range subcommands {
		names = append(names, name)
	}
	sort.Strings(names)
	_, _ = fmt.Fprintf(w, "usage: serf-dev <subcommand> [args]\nsubcommands: %s\n", strings.Join(names, " "))
}
