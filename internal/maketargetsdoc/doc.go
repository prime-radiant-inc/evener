// Command maketargetsdoc reads the "## " annotation blocks that sit above
// each rule in make/*.mk (see ParseFamily, parse.go) and regenerates the
// per-family target-reference table inside the matching
// docs/developing-evener/*.md's marked "## Targets" region (see Render and
// RewriteRegion, render.go). It never touches anything outside that
// region: the rest of each doc is hand-written prose.
//
// Run from the repo root as `go run ./internal/maketargetsdoc`. Wiring this
// into `make generate` (so the committed docs are gated for staleness the
// same way docs/appwire-protocol.md already is) is a later change.
//
//go:generate go run .
package main
