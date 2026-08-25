// Module primeradiant.com/evener/fuzz holds the portable, evener-agnostic core of
// the fuzzing toolkit: the failure→regression promoter and (later) the
// schema→generator. NOTHING here may import any primeradiant.com/evener package —
// this go.mod has no evener dependency, so the module simply will not build if
// that boundary is violated. That structural guarantee IS the portability test.
module primeradiant.com/evener/fuzz

go 1.27.0

require (
	github.com/spf13/afero v1.15.0
	pgregory.net/rapid v1.3.0
)

require golang.org/x/text v0.36.0 // indirect
