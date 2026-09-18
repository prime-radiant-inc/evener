// Tracks a host's own set of in-flight durable projection reads/writes, so a
// caller can ask "is anything still outstanding" without polling storage
// itself, and bounds that wait with a tripwire: work whose completion never
// lands (a stalled test double, a storage transaction the test itself is
// holding open) must fail loudly rather than hang the caller forever. A
// round waits on real durable work, so its wall time scales with machine
// load - but no amount of load turns work that has no completion left into
// work that finishes. This is what turns a stalled flush into one named
// failure in the test that caused it, rather than an abandoned wait that
// leaves every later test in the file failing too (issue #1187).
//
// `setTimeout`/`clearTimeout` and the macrotask yield are host ports: no
// browser or Node global is named here. The web binds the yield to a
// MessageChannel hop, which drains every pending microtask so that work
// chained onto an operation that just finished has already registered
// itself by the time a caller looks again.
export interface MutationProjectionWorkPorts {
  setTimeout(callback: () => void, milliseconds: number): unknown;
  clearTimeout(timerId: unknown): void;
  yieldMacrotask(): Promise<void>;
}

export interface MutationProjectionWorkTracker {
  // Registers `work` as outstanding until it settles either way.
  track<T>(work: Promise<T>): Promise<T>;
  // Awaits every promise tracked right now (or the stall tripwire, whichever
  // comes first), yields one macrotask, and reports how many were
  // outstanding when this call started.
  settle(): Promise<number>;
  // Forgets every currently tracked promise with no wait at all - a fence
  // reset already knows their result no longer matters to this host.
  clear(): void;
}

// The slowest round measured across the whole web suite (10373 tests) under
// 32-way CPU contention was 165ms; this is a tripwire for a stall, never
// pacing.
const STALL_TRIPWIRE_MS = 4_000;

export function createMutationProjectionWorkTracker(ports: MutationProjectionWorkPorts): MutationProjectionWorkTracker {
  const inFlight = new Set<Promise<unknown>>();

  return {
    track(work) {
      inFlight.add(work);
      return work.finally(() => {
        inFlight.delete(work);
      });
    },

    async settle() {
      const outstanding = [...inFlight];
      let timer: unknown;
      const tripwire = new Promise<never>((_resolve, reject) => {
        timer = ports.setTimeout(
          () =>
            reject(
              new Error(
                `pending-turns projection work stalled: ${outstanding.length} operation(s) still unsettled after ${STALL_TRIPWIRE_MS}ms - release whatever storage or transport this test is holding before flushing`,
              ),
            ),
          STALL_TRIPWIRE_MS,
        );
      });
      try {
        await Promise.race([Promise.allSettled(outstanding), tripwire]);
      } finally {
        ports.clearTimeout(timer);
      }
      await ports.yieldMacrotask();
      return outstanding.length;
    },

    clear() {
      inFlight.clear();
    },
  };
}
