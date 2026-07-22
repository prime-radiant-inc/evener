import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeAll, expect, test } from "vitest";
import { resetTreeStoreForTests } from "../../stores/tree";
import { RailHost } from "./RailHost";

// Node 26 shadows jsdom's real window.localStorage with a non-functional
// global under vitest; Rail reads its collapsed-state key during render, so
// this file needs the same in-memory stand-in every rail/shell test uses
// (see Rail.test.tsx's identical note). Scoped to this file only.
class MemoryStorage {
  private store = new Map<string, string>();
  getItem(key: string): string | null {
    return this.store.has(key) ? (this.store.get(key) ?? null) : null;
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

beforeAll(() => {
  // @ts-expect-error MemoryStorage implements only the Storage methods Rail
  // actually calls - see the comment above.
  globalThis.localStorage = new MemoryStorage();
});

afterEach(() => {
  cleanup();
  resetTreeStoreForTests();
});

test("renders the rail (T1 pass-through to <Rail/>)", () => {
  render(<RailHost />);
  // The expanded rail always renders its "Sessions" header regardless of
  // tree/loading state - proving RailHost mounted a real <Rail/>.
  expect(screen.getByRole("heading", { name: "Sessions" })).toBeTruthy();
});
