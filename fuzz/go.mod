// Module primeradiant.com/serf/fuzz holds the portable, serf-agnostic core of
// the fuzzing toolkit: the failure→regression promoter and (later) the
// schema→generator. NOTHING here may import any primeradiant.com/serf package —
// this go.mod has no serf dependency, so the module simply will not build if
// that boundary is violated. That structural guarantee IS the portability test.
module primeradiant.com/serf/fuzz

go 1.25.0

require pgregory.net/rapid v1.3.0
