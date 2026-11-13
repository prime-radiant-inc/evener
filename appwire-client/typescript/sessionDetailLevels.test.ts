// @vitest-environment node

import { describe, expect, test, vi } from "vitest";
import { configAtLevel, type DetailLevelStorage, SessionDetailLevels } from "./sessionDetailLevels";
import { makeTranscriptDisplayConfig, shippedConfig } from "./transcriptDisplayConfig";

class MemoryStorage implements DetailLevelStorage {
  readonly items = new Map<string, string>();
  getItemSync(key: string): string | null {
    return this.items.get(key) ?? null;
  }
  setItemSync(key: string, value: string): void {
    this.items.set(key, value);
  }
}

const KEY = "test.detail-levels";

describe("SessionDetailLevels", () => {
  test("a choice survives a re-read from storage", () => {
    const storage = new MemoryStorage();
    new SessionDetailLevels(storage, KEY).set("s1", "tools");
    const reread = new SessionDetailLevels(storage, KEY);
    expect(reread.get("s1")).toBe("tools");
    expect(reread.get("other")).toBeNull();
  });

  test("keeps the newest 500 choices and evicts the oldest", () => {
    const storage = new MemoryStorage();
    const levels = new SessionDetailLevels(storage, KEY);
    for (let i = 0; i <= 500; i += 1) levels.set(`s${i}`, "full");
    expect(levels.get("s0")).toBeNull();
    expect(levels.get("s1")).toBe("full");
    expect(levels.get("s500")).toBe("full");
    const reread = new SessionDetailLevels(storage, KEY);
    expect(reread.get("s0")).toBeNull();
    expect(reread.get("s500")).toBe("full");
  });

  test("choosing again moves a session to the front", () => {
    const levels = new SessionDetailLevels(new MemoryStorage(), KEY);
    levels.set("keep", "chat");
    for (let i = 0; i < 499; i += 1) levels.set(`s${i}`, "full");
    levels.set("keep", "intent");
    levels.set("newest", "full");
    expect(levels.get("keep")).toBe("intent");
    expect(levels.get("s0")).toBeNull();
  });

  test.each([
    ["garbage", "not json{"],
    ["a non-array", '{"a":"chat"}'],
  ])("unreadable storage (%s) reads as nothing chosen", (_name, raw) => {
    const storage = new MemoryStorage();
    storage.items.set(KEY, raw);
    expect(new SessionDetailLevels(storage, KEY).get("a")).toBeNull();
  });

  test("a storage that throws on read reads as nothing chosen", () => {
    const storage: DetailLevelStorage = {
      getItemSync: () => {
        throw new Error("unavailable");
      },
      setItemSync: () => {},
    };
    expect(new SessionDetailLevels(storage, KEY).get("a")).toBeNull();
  });

  test("malformed entries and unknown levels are dropped", () => {
    const storage = new MemoryStorage();
    storage.items.set(
      KEY,
      JSON.stringify([["a", "tools"], ["b", "bogus"], ["c"], [1, "chat"], ["d", 3], ["e", "chat", "x"], ["f", "full"]]),
    );
    const levels = new SessionDetailLevels(storage, KEY);
    expect(levels.get("a")).toBe("tools");
    expect(levels.get("f")).toBe("full");
    for (const ref of ["b", "c", "d", "e"]) expect(levels.get(ref)).toBeNull();
  });

  test("a failed write still serves the in-memory choice", () => {
    const storage: DetailLevelStorage = {
      getItemSync: () => null,
      setItemSync: () => {
        throw new Error("quota");
      },
    };
    const levels = new SessionDetailLevels(storage, KEY);
    const before = levels.getRevision();
    levels.set("a", "activity");
    expect(levels.get("a")).toBe("activity");
    expect(levels.getRevision()).toBe(before + 1);
  });

  test("set notifies subscribers and advances the revision; unsubscribe stops it", () => {
    const levels = new SessionDetailLevels(new MemoryStorage(), KEY);
    const listener = vi.fn();
    const unsubscribe = levels.subscribe(listener);
    const before = levels.getRevision();
    levels.set("a", "chat");
    expect(listener).toHaveBeenCalledTimes(1);
    expect(levels.getRevision()).toBe(before + 1);
    unsubscribe();
    levels.set("a", "full");
    expect(listener).toHaveBeenCalledTimes(1);
  });

  test("clear removes a choice, persists it and notifies once", () => {
    const storage = new MemoryStorage();
    const levels = new SessionDetailLevels(storage, KEY);
    levels.set("a", "tools");
    levels.set("b", "full");
    const listener = vi.fn();
    levels.subscribe(listener);
    const before = levels.getRevision();
    levels.clear("a");
    expect(levels.get("a")).toBeNull();
    expect(levels.get("b")).toBe("full");
    expect(listener).toHaveBeenCalledTimes(1);
    expect(levels.getRevision()).toBe(before + 1);
    const reread = new SessionDetailLevels(storage, KEY);
    expect(reread.get("a")).toBeNull();
    expect(reread.get("b")).toBe("full");
  });

  test("clear of a session with no choice changes nothing", () => {
    const levels = new SessionDetailLevels(new MemoryStorage(), KEY);
    levels.set("a", "tools");
    const listener = vi.fn();
    levels.subscribe(listener);
    const before = levels.getRevision();
    levels.clear("missing");
    expect(listener).not.toHaveBeenCalled();
    expect(levels.getRevision()).toBe(before);
    expect(levels.get("a")).toBe("tools");
  });

  test("two storage keys are independent", () => {
    const storage = new MemoryStorage();
    new SessionDetailLevels(storage, "one").set("a", "chat");
    const two = new SessionDetailLevels(storage, "two");
    expect(two.get("a")).toBeNull();
    two.set("a", "full");
    expect(new SessionDetailLevels(storage, "one").get("a")).toBe("chat");
    expect(new SessionDetailLevels(storage, "two").get("a")).toBe("full");
  });
});

describe("configAtLevel", () => {
  test("shows the level's content with the base's advanced settings", () => {
    const base = makeTranscriptDisplayConfig(
      { kind: "preset", level: "chat" },
      { tokenCounts: true, roundTimings: true },
    );
    const config = configAtLevel("tools", base);
    expect(config.content).toEqual({ kind: "preset", level: "tools" });
    expect(config.advanced).toEqual(base.advanced);
    expect(config).toEqual(makeTranscriptDisplayConfig({ kind: "preset", level: "tools" }, base.advanced));
  });

  test("replaces a custom base's content", () => {
    const base = shippedConfig("desktop");
    expect(configAtLevel("full", base).content).toEqual({ kind: "preset", level: "full" });
  });
});
