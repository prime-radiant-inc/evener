/**
 * A keyed set of callbacks with one lifecycle: add or remove a waiter, and
 * take a key's whole set when the waited event settles, so an emptied key never
 * lingers. The hydration, Stop-drain, and ready-generation registries in
 * threads.ts and wiredClient.ts share this shape; their wake rules and any
 * per-waiter exception stay with the caller.
 */
export class KeyedWaiters<K, V> {
  private readonly byKey = new Map<K, Set<V>>();

  add(key: K, value: V): void {
    let waiters = this.byKey.get(key);
    if (waiters === undefined) {
      waiters = new Set();
      this.byKey.set(key, waiters);
    }
    waiters.add(value);
  }

  remove(key: K, value: V): void {
    const waiters = this.byKey.get(key);
    if (waiters === undefined) return;
    waiters.delete(value);
    if (waiters.size === 0) this.byKey.delete(key);
  }

  take(key: K): Set<V> | undefined {
    const waiters = this.byKey.get(key);
    this.byKey.delete(key);
    return waiters;
  }
}
