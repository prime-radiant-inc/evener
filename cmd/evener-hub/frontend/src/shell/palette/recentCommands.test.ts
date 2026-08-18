import { afterEach, beforeAll, beforeEach, expect, test } from "vitest";
import { RECENT_COMMANDS_KEY, readRecentCommandIds, rememberCommand } from "./recentCommands";

// Recent commands live at localStorage["serf.search.recentCommands"] as a
// JSON array of ids, most-recent-first, capped at 5 (search.js:16-17,
// 619-633). This is a legacy key OUTSIDE the serf.prefs.* namespace - pin it.

// See stores/prefs.test.ts's identical comment: Node 26 shadows jsdom's real
// window.localStorage with its own (non-functional under vitest) global, so
// every test file that touches localStorage needs this same small in-memory
// stand-in. Scoped to this file only.
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
  // @ts-expect-error see MemoryStorage's own comment for why this is needed
  globalThis.localStorage = new MemoryStorage();
});

beforeEach(() => {
  localStorage.clear();
});
afterEach(() => {
  localStorage.clear();
});

test("the storage key is the legacy serf.search.recentCommands", () => {
  expect(RECENT_COMMANDS_KEY).toBe("serf.search.recentCommands");
});

test("readRecentCommandIds returns [] when nothing is stored", () => {
  expect(readRecentCommandIds()).toEqual([]);
});

test("readRecentCommandIds parses a stored id list", () => {
  localStorage.setItem(RECENT_COMMANDS_KEY, JSON.stringify(["model", "steer"]));
  expect(readRecentCommandIds()).toEqual(["model", "steer"]);
});

test("readRecentCommandIds tolerates corrupt / non-array JSON as []", () => {
  localStorage.setItem(RECENT_COMMANDS_KEY, "{not json");
  expect(readRecentCommandIds()).toEqual([]);
  localStorage.setItem(RECENT_COMMANDS_KEY, JSON.stringify({ not: "an array" }));
  expect(readRecentCommandIds()).toEqual([]);
});

test("readRecentCommandIds filters out non-string entries", () => {
  localStorage.setItem(RECENT_COMMANDS_KEY, JSON.stringify(["model", 3, null, "steer"]));
  expect(readRecentCommandIds()).toEqual(["model", "steer"]);
});

test("rememberCommand pushes to the front and de-duplicates", () => {
  rememberCommand("model");
  rememberCommand("steer");
  rememberCommand("model");
  expect(readRecentCommandIds()).toEqual(["model", "steer"]);
});

test("rememberCommand caps the list at 5 entries, dropping the oldest", () => {
  for (const id of ["a", "b", "c", "d", "e", "f"]) rememberCommand(id);
  expect(readRecentCommandIds()).toEqual(["f", "e", "d", "c", "b"]);
});

test("rememberCommand ignores an empty id", () => {
  rememberCommand("");
  expect(readRecentCommandIds()).toEqual([]);
});
