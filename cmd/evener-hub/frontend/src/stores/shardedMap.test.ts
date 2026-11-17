import { describe, expect, test } from "vitest";
import { ShardedMap, shardedMap } from "./shardedMap";

describe("ShardedMap", () => {
  test("reads back exactly what was written, across shard boundaries", () => {
    const refs = Array.from({ length: 200 }, (_, index) => `ref_${index}`);
    let map = ShardedMap.empty<number>();
    refs.forEach((ref, index) => {
      map = map.with(ref, index);
    });
    expect(map.size).toBe(refs.length);
    refs.forEach((ref, index) => {
      expect(map.get(ref)).toBe(index);
      expect(map.has(ref)).toBe(true);
    });
    expect(map.get("absent")).toBeUndefined();
    expect(map.has("absent")).toBe(false);
  });

  test("iteration yields every entry exactly once, in any order", () => {
    const refs = Array.from({ length: 100 }, (_, index) => `ref_${index}`);
    const map = ShardedMap.from(refs.map((ref, index) => [ref, index] as const));
    const fromKeys = new Set(map.keys());
    const fromValues = new Set(map.values());
    const fromEntries = new Map(map.entries());
    const fromForEach = new Map<string, number>();
    map.forEach((value, key, owner) => {
      expect(owner).toBe(map);
      fromForEach.set(key, value);
    });
    const fromSpread = new Map(map);
    expect(fromKeys).toEqual(new Set(refs));
    expect(fromValues.size).toBe(refs.length);
    const collected: Array<[string, Map<string, number>]> = [
      ["entries", fromEntries],
      ["forEach", fromForEach],
      ["spread", fromSpread],
    ];
    for (const [name, entries] of collected) {
      expect(entries.size, name).toBe(refs.length);
      for (const [index, ref] of refs.entries()) expect(entries.get(ref), name).toBe(index);
    }
  });

  test("a write shares every untouched shard and keeps the read view intact", () => {
    const refs = Array.from({ length: 64 }, (_, index) => `ref_${index}`);
    const before = ShardedMap.from(refs.map((ref) => [ref, ref.length] as const));
    const first = refs[0] as string;
    const after = before.with(first, -1);
    expect(after.get(first)).toBe(-1);
    expect(before.get(first)).toBe(first.length);
    for (const ref of refs.slice(1)) expect(after.get(ref)).toBe(before.get(ref));
    expect(after.size).toBe(before.size);
    // Adding a new key counts it, replacing one does not.
    expect(after.with("fresh", 1).size).toBe(before.size + 1);
    expect(after.without("absent")).toBe(after);
    expect(after.without(first).size).toBe(before.size - 1);
    expect(after.without(first).has(first)).toBe(false);
  });

  test("shardedMap keeps an already-sharded map and converts a plain map once", () => {
    const sharded = ShardedMap.from([["a", 1]] as const);
    expect(shardedMap(sharded)).toBe(sharded);
    const plain = new Map<string, number>([
      ["a", 1],
      ["b", 2],
    ]);
    const converted = shardedMap(plain);
    expect(converted.size).toBe(2);
    expect(converted.get("b")).toBe(2);
  });
});
