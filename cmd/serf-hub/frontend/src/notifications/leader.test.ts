import { afterEach, beforeEach, describe, expect, test } from "vitest";
import { electLeader, isLeader, resetLeaderForTests } from "./leader";

// A minimal Web Locks stand-in. `grant` decides whether the ifAvailable
// request hands the callback a lock (this tab wins) or null (someone else
// holds it). Mirrors the real contract: the callback runs, and the lock is
// held until the promise it returns settles.
function fakeLocks(grant: boolean): Lock {
  return {
    request(_name: string, _opts: unknown, cb: (lock: unknown) => Promise<unknown>): Promise<unknown> {
      return Promise.resolve(cb(grant ? { name: _name } : null));
    },
  } as unknown as Lock;
}

interface Lock {
  request(name: string, opts: unknown, cb: (lock: unknown) => Promise<unknown>): Promise<unknown>;
}

function setLocks(value: unknown): void {
  Object.defineProperty(navigator, "locks", { value, configurable: true });
}

beforeEach(() => {
  resetLeaderForTests();
});
afterEach(() => {
  setLocks(undefined);
  resetLeaderForTests();
});

describe("electLeader (Web Locks only)", () => {
  test("acquiring the lock makes this tab the leader", async () => {
    setLocks(fakeLocks(true));
    electLeader();
    await Promise.resolve();
    expect(isLeader()).toBe(true);
  });

  test("a lock already held elsewhere makes this tab a follower", async () => {
    setLocks(fakeLocks(false));
    electLeader();
    await Promise.resolve();
    expect(isLeader()).toBe(false);
  });

  test("no Web Locks API at all ⇒ every tab is leader (duplicate beats silent)", () => {
    setLocks(undefined);
    electLeader();
    expect(isLeader()).toBe(true);
  });

  test("a request that rejects falls back to leader", async () => {
    setLocks({ request: () => Promise.reject(new Error("denied")) });
    electLeader();
    await Promise.resolve();
    await Promise.resolve();
    expect(isLeader()).toBe(true);
  });

  test("a request that throws synchronously falls back to leader", () => {
    setLocks({
      request: () => {
        throw new Error("boom");
      },
    });
    electLeader();
    expect(isLeader()).toBe(true);
  });

  test("starts as a follower before election runs", () => {
    expect(isLeader()).toBe(false);
  });
});
