import "@testing-library/jest-dom/vitest";
import { afterEach } from "vitest";

class MemoryStorage implements Storage {
  readonly #items = new Map<string, string>();

  get length() {
    return this.#items.size;
  }

  clear() {
    this.#items.clear();
  }

  getItem(key: string) {
    return this.#items.get(key) ?? null;
  }

  key(index: number) {
    return [...this.#items.keys()][index] ?? null;
  }

  removeItem(key: string) {
    this.#items.delete(key);
  }

  setItem(key: string, value: string) {
    this.#items.set(key, value);
  }
}

Object.defineProperties(window, {
  localStorage: { configurable: true, value: new MemoryStorage() },
  sessionStorage: { configurable: true, value: new MemoryStorage() },
});

afterEach(() => {
  document.body.replaceChildren();
  window.localStorage.clear();
  window.sessionStorage.clear();
});
