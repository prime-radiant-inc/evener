/**
 * MemoryStorage is an in-memory Storage for tests. Node's own experimental
 * Web Storage global is disabled in every worker (vite.config.ts passes
 * --no-experimental-webstorage), so jsdom's localStorage works; a test file
 * installs a MemoryStorage anyway so each file starts from empty, isolated
 * storage it controls, and so it can subclass or spy on it (FailingStorage,
 * the prefs tests). Install it with installLocalStorage below.
 */
export class MemoryStorage implements Storage {
  private store = new Map<string, string>();
  get length(): number {
    return this.store.size;
  }
  key(index: number): string | null {
    return Array.from(this.store.keys())[index] ?? null;
  }
  getItem(key: string): string | null {
    return this.store.get(key) ?? null;
  }
  setItem(key: string, value: string): void {
    this.store.set(key, String(value));
  }
  removeItem(key: string): void {
    this.store.delete(key);
  }
  clear(): void {
    this.store.clear();
  }
}

/**
 * installLocalStorage makes `storage` the global localStorage for the rest of
 * the file. A plain `globalThis.localStorage = storage` is not enough under
 * Vitest's vmThreads pool: there the global object is the jsdom window itself,
 * whose localStorage is a getter-only accessor, so the assignment throws.
 */
export function installLocalStorage(storage: Storage): void {
  Object.defineProperty(globalThis, "localStorage", { value: storage, configurable: true, writable: true });
}
