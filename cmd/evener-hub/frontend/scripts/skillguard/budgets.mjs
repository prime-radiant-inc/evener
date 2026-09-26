// Load-aware wall-clock budgets for the skillguard driver's waits.
//
// Every wait the driver runs is RELEASED by the app's own next event (a turn
// boundary, a rendered reply, a cleared composer). The budget around it is a
// HANG TRIPWIRE, not the pacing mechanism: it only decides how long a silent
// page is treated as wedged rather than merely slow. A FIXED budget therefore
// trips a correct-but-slow reaction whenever the machine is loaded, and this
// scenario is the loaded case by construction -- it drives a real hub and two
// real `evener serve` daemons through a real browser, so CPU contention
// stretches every step (CI run 35486854632: the 30s continuation wait tripped
// against a correct daemon).
//
// The fix is the one the Go daemon-retirement watchdogs took (#2255): size the
// tripwire from the slowest reaction THIS RUN has already observed instead of a
// fixed number. A wait that demonstrably completed slowly says the machine is
// slow; the waits that follow it get a proportionally wider tripwire. The proof
// each wait enforces is untouched -- only how long a silent wait is tolerated
// before it is a named failure.
//
// Only a COMPLETED wait is an observation. A wait's wall-clock duration is the
// time the app demonstrably took to reach the awaited state under this run's
// load; a wait still in progress has observed nothing and cannot inflate its
// own budget.

// Tripwire = growth x the slowest observed reaction, floored at the wait's own
// budget and capped at ceilingFactor x that floor. The growth factor makes the
// widening proportional: a run whose slowest step was 5s does not get the same
// tripwire as one whose slowest step was 30s.
export const BUDGET_GROWTH = 10;
export const BUDGET_CEILING_FACTOR = 4;

export function scaledDeadlineMs(
  floorMs,
  observedMs,
  { growth = BUDGET_GROWTH, ceilingFactor = BUDGET_CEILING_FACTOR } = {},
) {
  const floor = Number.isFinite(floorMs) && floorMs > 0 ? floorMs : 1;
  const ceiling = floor * ceilingFactor;
  const observed = Number.isFinite(observedMs) && observedMs > 0 ? observedMs : 0;
  return Math.round(Math.min(ceiling, Math.max(floor, observed * growth)));
}

// The driver's own observation of how slow this machine is. `observe` is fed
// the duration of every wait that COMPLETED; `deadline` sizes the next wait's
// tripwire from the slowest of them.
export class ReactionBudget {
  constructor() {
    this.slowestMs = 0;
  }

  observe(elapsedMs) {
    if (Number.isFinite(elapsedMs) && elapsedMs > this.slowestMs) this.slowestMs = elapsedMs;
  }

  deadline(floorMs, options) {
    return scaledDeadlineMs(floorMs, this.slowestMs, options);
  }
}
