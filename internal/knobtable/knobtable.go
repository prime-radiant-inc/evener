// Package knobtable builds docs/developing-evener/config-knobs.md: one table
// per configuration surface (knob, default, doc row, consumer), derived from
// the envvars registry and the hub.toml decoder in cmd/evener-hub/config.go.
// A knob with no consumer is a candidate for deletion, so the table is also
// an audit: UnconsumedKnobs lists such knobs, and `make lint-repo-audit` fails
// when it finds one.
//
// Generate it from the repo root with `go run ./internal/knobtable/cmd/knobtable`,
// or through `make generate`; lint-generated diffs the output so the table
// cannot go stale.
package knobtable

import (
	"os"
	"path/filepath"
)

const outputDoc = "docs/developing-evener/config-knobs.md"

// Generate writes the knob tables for the repository at root.
func Generate(root string) error {
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

// UnconsumedKnobs returns the registered environment variables and the
// decoded hub.toml keys in the repository at root that no code reads.
func UnconsumedKnobs(root string) (envVars, hubKeys []string, err error) {
	sources, err := scanGoSources(root)
	if err != nil {
		return nil, nil, err
	}
	env, err := envRows(root, sources)
	if err != nil {
		return nil, nil, err
	}
	for _, r := range env {
		if len(r.Consumers) == 0 {
			envVars = append(envVars, r.Name)
		}
	}
	hub, _, err := hubRows(root, sources)
	if err != nil {
		return nil, nil, err
	}
	for _, r := range hub {
		if len(r.Consumers) == 0 {
			hubKeys = append(hubKeys, r.Key)
		}
	}
	return envVars, hubKeys, nil
}
