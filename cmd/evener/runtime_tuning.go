package main

import (
	"errors"
	"fmt"
	"io"
	"strconv"

	"primeradiant.com/evener/envvars"
)

// The collector's target for evener's processes, measured rather than taken
// from a default. A 200-round scripted-provider run (test/e2e/turncpu, the
// harness that found issue #6408) allocates almost nothing that survives: a
// cycle ends with about 17 MiB of live heap behind a 35 MiB goal, so the 18 MiB
// it reclaimed is the request, event and transcript garbage of one stretch of
// rounds. Short-lived garbage is most of the heap, which is why collecting
// less often costs so little memory. Raising the target trades RSS for
// collector cycles and the marking CPU those cycles cost:
//
//	GOGC    cycles   GC cpu     peak RSS
//	100        69    ~740 ms    ~89 MiB
//	200        31    ~340 ms   ~110 MiB
//	400        15    ~170 ms   ~151 MiB
//
// The cycles and GC cpu are from gctrace on that harness; peak RSS is the
// daemon's VmHWM. 200 is the stopping point: it removes 55% of the cycles and
// 405 ms of the daemon's 2.85 s mean process CPU for 19 MiB, where 400 spends a
// further 43 MiB for another 139 ms. The hub spawns one `evener serve` process
// per session, so this cost is paid per live daemon and multiplies with the
// session count; that multiplication is why the cheaper of the two measured
// steps wins.
const (
	// gcDefaultPercent is Go's own startup target, which the tests use to start
	// from a known state.
	gcDefaultPercent = 100
	// gcTargetPercent is the target evener's processes run with.
	gcTargetPercent = 200
)

// errInvalidGOGC reports a GOGC value the collector would not honor. It is a
// sentinel so a test matches the class with errors.Is rather than the wording.
var errInvalidGOGC = errors.New("invalid " + envvars.GoGC.Name)

// tuneGCTarget raises the collector's target for this process unless the
// operator named one. Presence is the test, not the value: GOGC=100 is how an
// operator asks for Go's own default back on a memory-tight host, and it is
// indistinguishable from an unset GOGC by the collector's reported target.
// Overriding a named target would grow the RSS of exactly the host that set it.
//
// The value is validated here, at the one place evener starts, because the
// alternative is silent: the runtime's own read of GOGC replaces anything it
// cannot parse with 100, so a typo would leave the process at a target nobody
// asked for — neither the operator's value nor evener's 200. parseGCPercent is
// the runtime's rule, so a value it accepts is the value the collector runs.
//
// set is debug.SetGCPercent in production and a recorder in tests.
func tuneGCTarget(set func(percent int) int) error {
	value, operatorSet := envvars.GoGC.LookupEnv()
	if operatorSet {
		if _, err := parseGCPercent(value); err != nil {
			return err
		}
		return nil
	}
	set(gcTargetPercent)
	return nil
}

// parseGCPercent validates a GOGC value the way the collector does at startup:
// the word "off", or a base-10 integer the runtime's 32-bit parse accepts. A
// negative number is allowed and disables collection, exactly as Go documents.
func parseGCPercent(value string) (int, error) {
	if value == "off" {
		return -1, nil
	}
	percent, err := strconv.ParseInt(value, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("%w: %q must be \"off\" or an integer percentage (for example 100 or 200)", errInvalidGOGC, value)
	}
	return int(percent), nil
}

// applyGCTuning is what main runs before any work: tuneGCTarget's policy, and
// a refusal through the process's own exit so a configuration mistake stops
// evener at startup instead of leaving it at a target nobody asked for. set,
// stderr and exit are seams for tests, mirroring mainDeps.
func applyGCTuning(set func(percent int) int, stderr io.Writer, exit func(int)) {
	if err := tuneGCTarget(set); err != nil {
		_, _ = fmt.Fprintf(stderr, "evener: %v\n", err)
		exit(1)
	}
}
