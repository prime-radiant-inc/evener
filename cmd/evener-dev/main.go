// evener-dev is the home of the dev tooling that outgrew shell (see
// docs/superpowers/specs/2026-08-17-dev-tooling-in-go-design.md): one
// subcommand per retired script, invoked from the Makefile and the remaining
// scripts as `go run ./cmd/evener-dev <subcommand> ...`. Subcommand env and
// output contracts are the retired scripts' contracts; their Go tests are
// what those scripts used to fake with PATH stubs.
package dev

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

var subcommands = map[string]func(args []string) int{
	"agent-shards":       runAgentShards,
	"check-gate-flags":   checkGateFlagsMain,
	"cli-shards":         runCLIShards,
	"covstmt":            covstmtMain,
	"hub-shards":         runHubShards,
	"list-build-flags":   listBuildFlagsMain,
	"module-lint":        lintMain,
	"root-test-flags":    rootTestFlagsMain,
	"web-browser-guards": runWebBrowserGuards,
}

func Run(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		usage(stderr)
		return 2
	}
	run, ok := subcommands[args[0]]
	if !ok {
		_, _ = fmt.Fprintf(stderr, "evener dev: unknown subcommand %q\n", args[0])
		usage(stderr)
		return 2
	}
	return run(args[1:])
}

// Subcommands is every `evener dev` subcommand, sorted: the one list both
// usage texts print, so neither can drift from what Run dispatches.
func Subcommands() []string {
	names := make([]string, 0, len(subcommands))
	for name := range subcommands {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func usage(w io.Writer) {
	_, _ = fmt.Fprintf(w, "usage: evener dev <subcommand> [args]\nsubcommands: %s\n", strings.Join(Subcommands(), " "))
}
