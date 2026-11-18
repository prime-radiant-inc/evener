package agent

import "primeradiant.com/evener/agent/execenv"

// Install the canonical path rule as the root resolver for every local
// execution environment.
//
// The environment's root is what a relative file operation is joined to, and its
// WorkingDirectory is what the snapshot and the prompt report. Those two must
// name the same directory, and they only do if the root was resolved the way the
// kernel resolves it: `link/..` pops the physical parent of what link points at,
// not the lexical parent of the link. canonicalStateDir is this package's one
// implementation of that walk, and it is installed here rather than called from
// execenv because execenv cannot import agent.
//
// This is a package-level assignment at init, not a per-call option: an
// environment built anywhere must resolve its root the same way, and a caller
// that had to remember to pass a resolver would eventually not.
func init() {
	execenv.SetLaunchRootResolver(canonicalStateDir)
}
