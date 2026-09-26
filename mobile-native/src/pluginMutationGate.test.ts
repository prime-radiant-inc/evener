import { describe, expect, test } from "vitest";
import { createPluginMutationGate, runGatedMutation } from "./pluginMutationGate";

/** A promise plus the resolver for it, to hold a mutation open. */
function pending(): { promise: Promise<void>; settle: () => void } {
  let settle!: () => void;
  const promise = new Promise<void>((resolve) => {
    settle = () => resolve();
  });
  return { promise, settle };
}

describe("one plugin mutation at a time", () => {
  // The plugins screen renders the browser and the installed list as a
  // ternary, so switching tabs unmounts whichever one started a mutation -
  // and with it any flag that lived inside it. The mutation itself keeps
  // running, so the gate the two share is what refuses the second one.
  test("a mutation started under one surface refuses the next while it runs", async () => {
    const gate = createPluginMutationGate();
    const install = pending();

    const installing = gate.run(() => install.promise);
    expect(gate.isBusy()).toBe(true);

    const removing = gate.run(async () => {
      throw new Error("the second mutation ran");
    });
    await expect(removing).resolves.toBe(false);

    install.settle();
    await expect(installing).resolves.toBe(true);
    expect(gate.isBusy()).toBe(false);
  });

  // The screens tell the two outcomes apart to say different things: a refusal
  // is "one is already running", a rejection is "this one may not have
  // landed". A refusal that resolved like a success would be silent, which is
  // what the deleted InstalledPlugins model surfaced as busy copy.
  test("a refusal is a distinct outcome with copy of its own, not a failure", async () => {
    const gate = createPluginMutationGate();
    const first = pending();
    const running = gate.run(() => first.promise);

    const refused = gate.run(async () => undefined);
    await expect(refused).resolves.toBe(false);

    first.settle();
    await expect(running).resolves.toBe(true);
  });

  test("the next mutation runs once the first has finished, failure or not", async () => {
    const gate = createPluginMutationGate();
    await expect(
      gate.run(() => Promise.reject(new Error("install failed"))),
    ).rejects.toThrow("install failed");
    expect(gate.isBusy()).toBe(false);

    let ran = false;
    await expect(
      gate.run(async () => {
        ran = true;
      }),
    ).resolves.toBe(true);
    expect(ran).toBe(true);
  });

  test("subscribers hear every transition, so both surfaces disable together", async () => {
    const gate = createPluginMutationGate();
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

describe("one call reduces a gated mutation to the copy's four outcomes", () => {
  test("a run resolves to ran", async () => {
    const gate = createPluginMutationGate();
    await expect(
      runGatedMutation(gate, () => true, async () => undefined),
    ).resolves.toBe("ran");
  });

  test("a refusal resolves to refused, distinct from a failure", async () => {
    const gate = createPluginMutationGate();
    const first = pending();
    void gate.run(() => first.promise);

    await expect(
      runGatedMutation(gate, () => true, async () => undefined),
    ).resolves.toBe("refused");
  });

  test("a throw resolves to failed and does not propagate its error", async () => {
    const gate = createPluginMutationGate();
    await expect(
      runGatedMutation(gate, () => true, () => Promise.reject(new Error("write failed"))),
    ).resolves.toBe("failed");
    expect(gate.isBusy()).toBe(false);
  });

  // The one place every screen's mutations funnel readiness through: a
  // caller that forgets a `disabled`/`whenReady` check, or fires one
  // programmatically, still cannot reach the wire while disconnected. This
  // outcome must stay distinct from a busy refusal so screens do not show
  // "another change" when the connection is the reason nothing ran.
  test("not ready is distinct from busy without running the action or taking the gate's lock", async () => {
    const gate = createPluginMutationGate();
    let ran = false;
    await expect(
      runGatedMutation(gate, () => false, async () => {
        ran = true;
      }),
    ).resolves.toBe("not-ready");
    expect(ran).toBe(false);
    expect(gate.isBusy()).toBe(false);
  });
});
