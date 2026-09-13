//go:build race

package shardfixture

// fixtureRaceEnabled is how a test in this module can tell whether the binary
// it is part of was compiled with -race: the race build tag is set by the
// toolchain and by nothing else.
const fixtureRaceEnabled = true
