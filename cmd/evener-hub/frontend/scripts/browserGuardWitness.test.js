// Shared failure composition, exercised through real CDP evaluate with only
// its transport replaced. Keep guard-specific page-expression tests separate.
import { afterEach, describe, expect, test, vi } from "vitest";

import { witnessFailure } from "./browserGuardWitness.mjs";

function witnessOf(failure, original) {
  expect(failure).toBeInstanceOf(Error);
  expect(failure.cause).toBe(original);
  const prefix = `${original instanceof Error ? original.message : String(original)}; witness `;
  expect(failure.message.startsWith(prefix)).toBe(true);
  return JSON.parse(failure.message.slice(prefix.length));
}

const response = (value) => ({ result: { result: { value } } });

afterEach(() => {
  vi.useRealTimers();
});

describe("shared browser-guard witness failure contract", () => {
  test("appends evidence from the supplied expression and preserves the original cause", async () => {
    const original = new Error("a67c5e; witness 38bd2a");
    const driver = {
      send: async (_method, { expression }) => response(new Function(`return (${expression});`)()),
    };
    const failure = await witnessFailure(driver, '({ unreadable: [], paneId: "p42" })', original);

    expect(witnessOf(failure, original)).toEqual({ unreadable: [], paneId: "p42" });
  });

  test.each([false, true])("collection rejection is a note with partial policy %s", async (partial) => {
    const original = new Error("8f34d2");
    const transportError = new Error("7c926b");
    const send = async () => { throw transportError; };
    const failure = await witnessFailure({ send }, "null", original, { partial, context: { stage: "s4" } });

    expect(witnessOf(failure, original)).toEqual({
      stage: "s4",
      ...(partial ? { partial: true } : {}),
      unreadable: ["witness"],
      error: transportError.message,
    });
  });

  test("a synchronous transport throw cannot replace the original failure", async () => {
    const original = new Error("27b5e4");
    const detail = "00f32c";
    const send = () => { throw detail; };
    const failure = await witnessFailure({ send }, "null", original);

    expect(witnessOf(failure, original)).toEqual({ unreadable: ["witness"], error: detail });
  });

  test("a non-Error original failure retains its value as cause", async () => {
    const original = "b453ea";
    const failure = await witnessFailure({ send: async () => response({ unreadable: [] }) }, "null", original);

    expect(witnessOf(failure, original)).toEqual({ unreadable: [] });
  });

  test.each([false, true])("serialization failure retains host context with partial policy %s", async (partial) => {
    const original = new Error("e3ac26");
    const send = async () => response({ unreadable: [], value: 1n });
    const failure = await witnessFailure({ send }, "null", original, { context: { fixture: "f2", openPaneMs: 14 }, partial });
    const witness = witnessOf(failure, original);

    expect(witness).toMatchObject({ fixture: "f2", openPaneMs: 14, unreadable: ["payload"] });
    expect(witness.partial).toBe(partial ? true : undefined);
  });

  test.each(["resolve", "reject"])("an early %s clears both evaluation and witness timers", async (outcome) => {
    vi.useFakeTimers();
    const original = new Error("c9d723");
    let complete;
    const send = () => new Promise((resolve, reject) => {
      complete = outcome === "resolve" ? () => resolve(response({ unreadable: [] })) : () => reject("df326b");
    });
    const pending = witnessFailure({ send }, "null", original, { readMs: 2000 });
    expect(vi.getTimerCount()).toBe(2);
    complete();
    const witness = witnessOf(await pending, original);

    expect(witness.unreadable).toEqual(outcome === "resolve" ? [] : ["witness"]);
    expect(vi.getTimerCount()).toBe(0);
  });

  test("omitting the short bound preserves the caller's existing CDP wait", async () => {
    vi.useFakeTimers();
    const original = new Error("ab725f");
    let complete;
    const send = () => new Promise((resolve) => { complete = () => resolve(response({ unreadable: [] })); });
    const pending = witnessFailure({ send }, "null", original);
    const settled = vi.fn();
    void pending.then(settled, settled);
    await vi.advanceTimersByTimeAsync(2000);
    expect(settled).not.toHaveBeenCalled();
    complete();
    expect(witnessOf(await pending, original).unreadable).toEqual([]);
    expect(vi.getTimerCount()).toBe(0);
  });
});
