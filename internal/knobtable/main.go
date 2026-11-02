// Command knobtable writes docs/developing-evener/config-knobs.md: one table
// per configuration surface (knob, default, doc row, consumer), derived from
// the envvars registry and the hub.toml decoder in cmd/evener-hub/config.go.
// A knob with no consumer is a candidate for deletion, so the table is also
// the audit: TestRepoKnobsHaveConsumers fails when one appears.
//
// Run from the repo root as `go run ./internal/knobtable`, or through
// `make generate`; lint-generated diffs the output so the table cannot go
// stale.
//
//go:generate go run . -root ../..
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

const outputDoc = "docs/developing-evener/config-knobs.md"

func main() {
	root := flag.String("root", ".", "repository root")
	flag.Parse()
	if err := generate(*root); err != nil {
		fmt.Fprintln(os.Stderr, "knobtable:", err)
		os.Exit(1)
	}
}

func generate(root string) error {
	scan, err := scanGoSources(root)
	if err != nil {
		return err
	}
	env, err := envRows(root, scan)
	if err != nil {
		return err
	}
	direct, err := directEnvRows(root, scan)
	if err != nil {
		return err
	}
	hub, removed, err := hubRows(root, scan)
	if err != nil {
		return err
	}
	out := render(env, direct, hub, removed)
	return os.WriteFile(filepath.Join(root, outputDoc), []byte(out), 0o644)
}
