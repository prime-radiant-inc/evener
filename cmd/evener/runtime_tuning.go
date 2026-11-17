package main

// The collector's target for evener's processes, measured rather than taken
// from a default. A 200-round scripted-provider run (test/e2e/turncpu, the
// harness that found issue #6408) reclaims almost nothing: a cycle ends with
// about 17 MiB of live heap behind a 35 MiB goal, so the 18 MiB it reclaimed
// is the request, event and transcript garbage of one stretch of rounds.
// Raising the target trades RSS for collector cycles and the marking CPU those
// cycles cost:
//
//	GOGC    cycles   GC cpu     peak RSS
//	100        69    ~740 ms    ~89 MiB
//	200        31    ~340 ms   ~110 MiB
//	400        15    ~170 ms   ~151 MiB
//
// The cycles and GC cpu are from gctrace on that harness; peak RSS is the
// daemon's VmHWM. 200 is the stopping point: it removes 55% of the cycles and
// 405 ms of the daemon's 2.85 s mean process CPU for 19 MiB, where 400 spends a
// further 43 MiB for another 139 ms. The hub runs many sessions in one
// process and its memory is the risk the audit named, so the cheaper of the two
// measured steps wins.
const (
	// gcDefaultPercent is Go's startup target when GOGC names nothing, so a
	// previous value equal to it means the process was not tuned.
	gcDefaultPercent = 100
	// gcTargetPercent is the target evener's processes run with.
	gcTargetPercent = 200
)

// tuneGCTarget raises the collector's target for this process unless
// something else already set one. The collector reports its previous target,
// which is the authority on that: GOGC's value is already in effect by the
// time main runs, so a number other than Go's default means an operator
// named it — GOGC=off comes back as a negative percentage — and overriding it
// would grow the RSS of exactly the memory-constrained host that set it.
//
// set is debug.SetGCPercent in production and a recorder in tests.
func tuneGCTarget(set func(percent int) int) {
	if previous := set(gcTargetPercent); previous != gcDefaultPercent {
		set(previous)
	}
}
