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
  // Waits until nothing tracked is outstanding and a macrotask hop passes with
  // nothing new registered (or the stall tripwire fires, whichever comes
  // first), and reports how much work it saw: what was outstanding when it
  // started plus everything that registered while it ran. A durable chain
  // whose next step starts only once the step before it settled - the
  // dispatcher's next read, a refresh after a commit's notify - leaves the
  // tracker empty between steps, and the hop is what lets that next step
  // register before settle decides the chain is over. Zero means settle
  // began and ended quiet.
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
  // Every registration so far. A settle compares it across each hop, which is
  // how it sees work that registered while it ran.
  let registrations = 0;

  function drained(current: WorkGeneration): Promise<void> {
    if (current.outstanding === 0) return Promise.resolve();
    return new Promise<void>((resolve) => {
      current.whenDrained.push(resolve);
    });
  }

  // Drains, hops, and drains again for as long as a hop lets new work register
  // or leaves work in flight. The tripwire bounds the whole settle, so a chain
  // that keeps going past it fails as loudly as work that never settles; once
  // the settle is over, `abandoned` stops the loop.
  async function drainedAndQuiet(current: WorkGeneration, abandoned: () => boolean): Promise<void> {
    for (;;) {
      await drained(current);
      const registrationsBeforeHop = registrations;
      await ports.yieldMacrotask();
      if (abandoned() || (registrations === registrationsBeforeHop && current.outstanding === 0)) return;
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
      // The executor below runs synchronously, so `timer` is always assigned
      // before the `finally` reads it - TypeScript cannot see that through a
      // closure, hence the assertion.
      let timer!: TimerId;
      const tripwire = new Promise<never>((_resolve, reject) => {
        timer = ports.setTimeout(
          () =>
            reject(
              new Error(
                `projection work stalled: ${current.outstanding} operation(s) still unsettled after ${STALL_TRIPWIRE_MS}ms (${registrations - registrationsAtStart} registered since this settle began) - release whatever storage or transport is holding this work open`,
              ),
            ),
          STALL_TRIPWIRE_MS,
        );
      });
      let over = false;
      try {
        await Promise.race([drainedAndQuiet(current, () => over), tripwire]);
      } finally {
        over = true;
        ports.clearTimeout(timer);
      }
      return outstandingAtStart + (registrations - registrationsAtStart);
    },

    clear() {
      generation = createWorkGeneration();
    },
  };
}
