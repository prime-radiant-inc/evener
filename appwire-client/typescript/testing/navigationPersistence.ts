// memoryNavigationPersistence is an in-memory NavigationPersistence for the
// navigation store's tests: one expansion map, copied on the way in and out
// the way a storage-backed port would, with the knobs a test needs to see
// which side of the seam a read or write landed on - a read count, the
// writes as plain records, and an onWrite hook that runs INSIDE
// writeExpansion, so a test can observe what the store had already published
// by the time it handed the map over. In-repo test support, not shipped.

import type { NavigationPersistence } from "../state/navigation/store";

export interface MemoryNavigationPersistence extends NavigationPersistence {
  /** How many times the store has read the port. */
  readonly reads: number;
  /** Every map the store has written, oldest first. */
  readonly writes: ReadonlyArray<Record<string, boolean>>;
  /** What the port holds right now. */
  stored(): Map<string, boolean>;
}

export function memoryNavigationPersistence(
  seed: Iterable<readonly [string, boolean]> = [],
  onWrite: (expansion: ReadonlyMap<string, boolean>) => void = () => undefined,
): MemoryNavigationPersistence {
  let stored = new Map(seed);
  const port = {
    reads: 0,
    writes: [] as Array<Record<string, boolean>>,
    readExpansion(): Map<string, boolean> {
      port.reads++;
      return new Map(stored);
    },
    writeExpansion(expansion: ReadonlyMap<string, boolean>): void {
      stored = new Map(expansion);
      port.writes.push(Object.fromEntries(expansion));
      onWrite(expansion);
    },
    stored: () => new Map(stored),
  };
  return port;
}
