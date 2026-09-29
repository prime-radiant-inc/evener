import { describe, expect, test } from "vitest";
import {
  createHubWriteGate,
  HUB_WRITE_BUSY,
  HubWriteBusyError,
  isHubWriteBusy,
  runGatedMutation,
} from "./hubWriteGate";

/** A promise plus the resolver for it, to hold a write open. */
function pending(): { promise: Promise<void>; settle: () => void } {
  let settle!: () => void;
  const promise = new Promise<void>((resolve) => {
    settle = () => resolve();
  });
  return { promise, settle };
}

describe("one hub write at a time", () => {
  test("a write started under one store refuses the next while it runs", async () => {
    const gate = createHubWriteGate();
    const install = pending();

    const installing = gate.run(() => install.promise);
    expect(gate.isBusy()).toBe(true);

    const removing = gate.run(async () => {
      throw new Error("the second write ran");
    });
    await expect(removing).resolves.toBe(false);

    install.settle();
    await expect(installing).resolves.toBe(true);
    expect(gate.isBusy()).toBe(false);
  });

  test("the next write runs once the first has finished, failure or not", async () => {
    const gate = createHubWriteGate();
    await expect(gate.run(() => Promise.reject(new Error("install failed")))).rejects.toThrow("install failed");
    expect(gate.isBusy()).toBe(false);

    let ran = false;
    await expect(
      gate.run(async () => {
        ran = true;
      }),
    ).resolves.toBe(true);
    expect(ran).toBe(true);
  });

  test("subscribers hear every transition, so both stores disable together", async () => {
    const gate = createHubWriteGate();
    const seen: boolean[] = [];
    const unsubscribe = gate.subscribe(() => seen.push(gate.isBusy()));

    const first = pending();
    const running = gate.run(() => first.promise);
    await gate.run(async () => undefined); // refused: no transition of its own
    first.settle();
    await running;

    expect(seen).toEqual([true, false]);
    unsubscribe();
    await gate.run(async () => undefined);
    expect(seen).toEqual([true, false]);
  });
});

describe("runGatedMutation reduces a store write to the copy's four outcomes", () => {
  test("a store write resolves to ran", async () => {
    await expect(
      runGatedMutation(
        () => true,
        async () => undefined,
      ),
    ).resolves.toBe("ran");
  });

  test("a HubWriteBusyError resolves to refused, distinct from a failure", async () => {
    await expect(
      runGatedMutation(
        () => true,
        () => Promise.reject(new HubWriteBusyError()),
      ),
    ).resolves.toBe("refused");
  });

  test("a throw resolves to failed and does not propagate its error", async () => {
    await expect(
      runGatedMutation(
        () => true,
        () => Promise.reject(new Error("write failed")),
      ),
    ).resolves.toBe("failed");
  });

  test("not ready is distinct from busy without running the action", async () => {
    let ran = false;
    await expect(
      runGatedMutation(
        () => false,
        async () => {
          ran = true;
        },
      ),
    ).resolves.toBe("not-ready");
    expect(ran).toBe(false);
  });
});

describe("HubWriteBusyError carries the busy copy", () => {
  test("its message is HUB_WRITE_BUSY and isHubWriteBusy recognizes it", () => {
    const error = new HubWriteBusyError();
    expect(error.message).toBe(HUB_WRITE_BUSY);
    expect(isHubWriteBusy(error)).toBe(true);
    expect(isHubWriteBusy(new Error("other"))).toBe(false);
  });
});

describe("reset opens the gate without letting an abandoned write reopen it", () => {
  test("a reset write's late settlement cannot clear a newer write's busy", async () => {
    const gate = createHubWriteGate();
    const abandoned = pending();
    const first = gate.run(() => abandoned.promise);
    expect(gate.isBusy()).toBe(true);

    gate.reset();
    expect(gate.isBusy()).toBe(false);

    const held = pending();
    const second = gate.run(() => held.promise);
    expect(gate.isBusy()).toBe(true);

    // The abandoned write settles after the reset: its finally is stale and
    // must not open the gate under the write that now owns it.
    abandoned.settle();
    await expect(first).resolves.toBe(true);
    expect(gate.isBusy()).toBe(true);

    held.settle();
    await expect(second).resolves.toBe(true);
    expect(gate.isBusy()).toBe(false);
  });
});
