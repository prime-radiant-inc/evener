// Command knobtable writes docs/developing-evener/config-knobs.md through
// internal/knobtable.
//
// Run from the repo root as `go run ./internal/knobtable/cmd/knobtable`, or
// through `make generate`.
//
//go:generate go run . -root ../../../..
package main

import (
	"flag"
	"fmt"
	"os"

	"primeradiant.com/evener/internal/knobtable"
)

func main() {
	root := flag.String("root", ".", "repository root")
	flag.Parse()
	if err := knobtable.Generate(*root); err != nil {
		fmt.Fprintln(os.Stderr, "knobtable:", err)
		os.Exit(1)
	}
}
