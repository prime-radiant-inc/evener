/**
 * installLocalStorage makes `storage` the global localStorage for the rest of
 * the file. A plain `globalThis.localStorage = storage` is not enough under
 * Vitest's vmThreads pool: there the global object is the jsdom window itself,
 * whose localStorage is a getter-only accessor, so the assignment throws.
 */
export function installLocalStorage(storage: Storage): void {
  Object.defineProperty(globalThis, "localStorage", { value: storage, configurable: true, writable: true });
}
