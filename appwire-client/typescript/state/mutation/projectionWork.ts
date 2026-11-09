// Counts a host's own in-flight durable projection reads/writes, so a caller
// can ask "is anything still outstanding" without polling storage itself.
//
// This is the package's whole job: track, forget, and wait. Bounding the wait
// with a stall tripwire and yielding a macrotask between checks are test
// harness policy - statements about vitest, not about mutation projection - so
// they live in each host's own test helper (#1891). Keeping them here cost
// three host ports every future host had to bind.
export interface MutationProjectionWorkTracker {
  // Registers `work` as outstanding until it settles either way.
  track<T>(work: Promise<T>): Promise<T>;
  // Waits until nothing tracked is outstanding, and reports how much work it
  // saw: what was outstanding when it started plus everything that registered
  // while it ran. Zero means the call began and ended quiet. Work whose next
  // step starts only once the step before it settled leaves the tracker empty
  // between steps, so a caller that must outlast a durable chain hops a
  // macrotask and calls whenIdle again.
  whenIdle(): Promise<number>;
  // Forgets every currently tracked promise with no wait at all - a fence
  // reset already knows their result no longer matters to this host.
  clear(): void;
}

// The work tracked between two clear() calls. Counting instead of keeping the
// promises holds the cost of tracking an operation to the one `finally`
// reaction that observes it, with `end` shared by the whole generation. Work
// tracked before a clear settles into its own generation, which no later
// whenIdle watches.
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
      // Nothing outside a test waits on a drain, so a drain with no waiter
      // skips the splice and allocates nothing.
      if (generation.outstanding === 0 && generation.whenDrained.length > 0) {
        for (const wake of generation.whenDrained.splice(0)) wake();
      }
    },
  };
  return generation;
}

export function createMutationProjectionWorkTracker(): MutationProjectionWorkTracker {
  let generation = createWorkGeneration();
  // Every registration so far. A whenIdle compares it across its own wait,
  // which is how it sees work that registered while it ran.
  let registrations = 0;

  function drained(current: WorkGeneration): Promise<void> {
    if (current.outstanding === 0) return Promise.resolve();
    return new Promise<void>((resolve) => {
      current.whenDrained.push(resolve);
    });
  }

  return {
    track(work) {
      registrations += 1;
      generation.outstanding += 1;
      return work.finally(generation.end);
    },

    async whenIdle() {
      const current = generation;
      const outstandingAtStart = current.outstanding;
      const registrationsAtStart = registrations;
      await drained(current);
      return outstandingAtStart + (registrations - registrationsAtStart);
    },

    clear() {
      generation = createWorkGeneration();
    },
  };
}
