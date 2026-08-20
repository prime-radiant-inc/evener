// Command maketargetsdoc reads the "## " annotation blocks that sit above
// each rule in make/*.mk (see ParseFamily, parse.go) and regenerates the
// per-family target-reference table inside the matching
// docs/developing-evener/*.md's marked "## Targets" region (see Render and
// RewriteRegion, render.go). It never touches anything outside that
// region: the rest of each doc is hand-written prose.
//
// Run from the repo root as `go run ./internal/maketargetsdoc`, or reach the
// same directive through `make generate`, which runs it alongside the appwire
// generators. `make lint`'s lint-generated target runs `make generate` and
// diffs the six committed docs, so a doc region that has drifted from its
// annotations fails the required gate the same way a stale
// docs/appwire-protocol.md does.
//
// The -root argument is what makes the directive work: go generate runs a
// command with the cwd set to the package's own directory, so without it the
// generator would look for make/*.mk beneath internal/maketargetsdoc.
//
//go:generate go run . -root ../..
package main
