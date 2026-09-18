// Tracks a host's own set of in-flight durable projection reads/writes, so a
// caller can ask "is anything still outstanding" without polling storage
// itself, and bounds that wait with a tripwire: work whose completion never
// lands (a storage transaction or transport the caller itself is holding
// open) must fail loudly rather than hang forever.
//
// `setTimeout`/`clearTimeout` and the macrotask yield are host ports: no
// browser or Node global is named here, and the timer handle is a type
// parameter (`outbox.ts`'s `setInterval`/`clearInterval` pair is the same
// convention) so a host's own handle type - a `number` on the web, a
// `NodeJS.Timeout` under Node - flows through with no cast at the binding
// site. The web binds the yield to a MessageChannel hop, which drains every
// pending microtask so that work chained onto an operation that just
// finished has already registered itself by the time a caller looks again.
export interface MutationProjectionWorkPorts<TimerId = unknown> {
  setTimeout(callback: () => void, milliseconds: number): TimerId;
  clearTimeout(timerId: TimerId): void;
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

const STALL_TRIPWIRE_MS = 4_000;

export function createMutationProjectionWorkTracker<TimerId = unknown>(
  ports: MutationProjectionWorkPorts<TimerId>,
): MutationProjectionWorkTracker {
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
      // Nothing to race a tripwire against: an empty `allSettled` already
      // wins in the same microtask, so building the timer at all would only
      // suggest a stall this call could never observe.
      if (outstanding.length === 0) {
        await ports.yieldMacrotask();
        return 0;
      }
      // The executor below runs synchronously, so `timer` is always assigned
      // before the `finally` reads it - TypeScript cannot see that through a
      // closure, hence the assertion.
      let timer!: TimerId;
      const tripwire = new Promise<never>((_resolve, reject) => {
        timer = ports.setTimeout(
          () =>
            reject(
              new Error(
                `projection work stalled: ${outstanding.length} operation(s) still unsettled after ${STALL_TRIPWIRE_MS}ms - release whatever storage or transport is holding this work open`,
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
