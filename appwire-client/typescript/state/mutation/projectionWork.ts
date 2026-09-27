// Counts a host's own in-flight durable projection reads/writes, so a caller
// can ask "is anything still outstanding" without polling storage itself, and
// bounds that wait with a tripwire: work whose completion never lands (a
// storage transaction or transport the caller itself is holding open) must
// fail loudly rather than hang forever.
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
  // Waits until nothing tracked is outstanding (or the stall tripwire fires,
  // whichever comes first), yields one macrotask, and reports how much work
  // the round saw: what was outstanding when it started plus what registered
  // while it ran. Work chained a few microtasks behind an operation that just
  // finished registers during the round, so zero is the only answer that
  // means the round began and ended quiet.
  settle(): Promise<number>;
  // Forgets every currently tracked promise with no wait at all - a fence
  // reset already knows their result no longer matters to this host.
  clear(): void;
}

const STALL_TRIPWIRE_MS = 4_000;

// The work tracked between two clear() calls. Counting instead of keeping the
// promises holds the cost of tracking an operation to the one `finally`
// reaction that observes it, with `end` shared by the whole generation. Work
// tracked before a clear settles into its own generation, which no later
// settle watches.
interface WorkGeneration {
  outstanding: number;
  whenDrained: (() => void)[];
  end: () => void;
}

function createWorkGeneration(): WorkGeneration {
  const generation: WorkGeneration = {
    outstanding: 0,
    whenDrained: [],
    end: () => {
      generation.outstanding -= 1;
      if (generation.outstanding === 0) for (const wake of generation.whenDrained.splice(0)) wake();
    },
  };
  return generation;
}

export function createMutationProjectionWorkTracker<TimerId = unknown>(
  ports: MutationProjectionWorkPorts<TimerId>,
): MutationProjectionWorkTracker {
  let generation = createWorkGeneration();
  // Every registration so far. A round compares it before and after, which is
  // how it sees work that registered while it ran.
  let registrations = 0;

  async function drainedOrStalled(current: WorkGeneration): Promise<void> {
    // The executor below runs synchronously, so `timer` is always assigned
    // before the `finally` reads it - TypeScript cannot see that through a
    // closure, hence the assertion.
    let timer!: TimerId;
    const tripwire = new Promise<never>((_resolve, reject) => {
      timer = ports.setTimeout(
        () =>
          reject(
            new Error(
              `projection work stalled: ${current.outstanding} operation(s) still unsettled after ${STALL_TRIPWIRE_MS}ms - release whatever storage or transport is holding this work open`,
            ),
          ),
        STALL_TRIPWIRE_MS,
      );
    });
    const drained = new Promise<void>((resolve) => {
      current.whenDrained.push(resolve);
    });
    try {
      await Promise.race([drained, tripwire]);
    } finally {
      ports.clearTimeout(timer);
    }
  }

  return {
    track(work) {
      registrations += 1;
      generation.outstanding += 1;
      return work.finally(generation.end);
    },

    async settle() {
      const current = generation;
      const outstandingAtStart = current.outstanding;
      const registrationsAtStart = registrations;
      // With nothing outstanding there is no stall to race a tripwire
      // against, so no timer is built.
      if (outstandingAtStart > 0) await drainedOrStalled(current);
      await ports.yieldMacrotask();
      return outstandingAtStart + (registrations - registrationsAtStart);
    },

    clear() {
      generation = createWorkGeneration();
    },
  };
}
