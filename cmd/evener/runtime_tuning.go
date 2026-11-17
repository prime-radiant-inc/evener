package main

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

// tuneGCTarget raises the collector's target for this process unless the
// operator named one. Presence is the test, not the value: GOGC=100 is how an
// operator asks for Go's own default back on a memory-tight host, and it is
// indistinguishable from an unset GOGC by the collector's reported target.
// Overriding a named target would grow the RSS of exactly the host that set it.
//
// set is debug.SetGCPercent in production and a recorder in tests.
func tuneGCTarget(operatorSet bool, set func(percent int) int) {
	if operatorSet {
		return
	}
	set(gcTargetPercent)
}
