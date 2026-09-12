import { expect, test } from "vitest";
import { storageHasSecret } from "./storageHasSecret";

// A Storage-shaped fake whose entries live outside the object's own enumerable
// properties - which is exactly why the guard cannot be built on
// JSON.stringify(localStorage): that serializes to "{}" and can never see a
// leaked credential.
function storageWith(entries: Record<string, string>): Storage {
  const keys = Object.keys(entries);
  return {
    get length(): number {
      return keys.length;
    },
    key: (index: number): string | null => keys[index] ?? null,
    getItem: (key: string): string | null => entries[key] ?? null,
    setItem: (key: string, value: string): void => {
      if (!(key in entries)) keys.push(key);
      entries[key] = value;
    },
    removeItem: (key: string): void => {
      const index = keys.indexOf(key);
      if (index >= 0) keys.splice(index, 1);
      delete entries[key];
    },
    clear: (): void => {
      keys.length = 0;
      for (const key of Object.keys(entries)) delete entries[key];
    },
  } as Storage;
}

test("sees a credential stored under an innocuous key, where stringifying storage cannot", () => {
  const storage = storageWith({ "evener-hub.spawn-defaults": '{"prompt":"fixture-not-a-real-key"}' });

  // The blind spot this helper exists for: Storage entries are not own
  // enumerable properties, so the serialized form carries none of them.
  expect(JSON.stringify(storage)).not.toContain("fixture-not-a-real-key");
  expect(storageHasSecret(storage, "fixture-not-a-real-key")).toBe(true);
});

test("reports no secret when storage holds none", () => {
  const storage = storageWith({ "evener-hub.spawn-defaults": '{"prompt":"ordinary draft"}' });
  expect(storageHasSecret(storage, "fixture-not-a-real-key")).toBe(false);
});

test("handles empty storage and a key whose value is missing", () => {
  expect(storageHasSecret(storageWith({}), "fixture-not-a-real-key")).toBe(false);
  const storage = storageWith({ present: "value" });
  const keys = ["present", "absent"];
  const sparse = {
    get length(): number {
      return keys.length;
    },
    key: (index: number): string | null => keys[index] ?? null,
    getItem: (key: string): string | null => (key === "present" ? storage.getItem(key) : null),
  } as Storage;
  expect(storageHasSecret(sparse, "fixture-not-a-real-key")).toBe(false);
});
