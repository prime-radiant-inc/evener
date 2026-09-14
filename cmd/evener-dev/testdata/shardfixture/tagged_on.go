//go:build shardfixturetag

package shardfixture

// fixtureBuiltWithTag is how a test in this module can tell whether the binary
// it is part of was compiled with the caller's -tags: the tag selects this
// file over its counterpart, and nothing else can.
const fixtureBuiltWithTag = true
