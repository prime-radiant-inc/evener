import { describe, expect, test, vi } from "vitest";
import { createReadyGenerationFence } from "./readyGenerationFence";
import {
  createSettingsHubGeneration,
  retireSettingsHubPayload,
  settleUnsettleableWrite,
} from "./settingsHubGeneration";

/** A fence over a hub whose support the test drives directly, the same
 * fixture readyGenerationFence.test.ts uses. */
function fenceOver(supported = true) {
  let support = supported;
  const fence = createReadyGenerationFence(() => support);
  return { fence, drop: () => (support = false), restore: () => (support = true) };
}

interface Fields {
  loaded: boolean;
  saving: boolean;
  hubLoading: boolean;
  writeUncertain: boolean;
  revision: number;
}

function fieldsStore(initial: Fields) {
  let state = initial;
  return {
    getState: () => state,
    setState: (partial: Partial<Fields>) => {
      state = { ...state, ...partial };
    },
  };
}

describe("retireSettingsHubPayload", () => {
  test("clears loaded/saving/hubLoading and marks writeUncertain when a write was in flight", () => {
    const { fence } = fenceOver();
    const generation = fence.begin();
    const read = fence.claimRead();
    const store = fieldsStore({ loaded: true, saving: true, hubLoading: true, writeUncertain: false, revision: 3 });

    retireSettingsHubPayload(fence, store.getState, store.setState);

    expect(store.getState()).toMatchObject({ loaded: false, saving: false, hubLoading: false, writeUncertain: true });
    // fence.supersede() ran: the read claimed before retirement is no longer
    // this generation's own to land.
    expect(fence.readStillMine(generation, read)).toBe(false);
  });

  test("leaves writeUncertain false when nothing was saving and it was not already set", () => {
    const { fence } = fenceOver();
    fence.begin();
    const store = fieldsStore({ loaded: true, saving: false, hubLoading: true, writeUncertain: false, revision: 3 });

    retireSettingsHubPayload(fence, store.getState, store.setState);

    expect(store.getState().writeUncertain).toBe(false);
  });

  test("preserves an already-uncertain write across a retirement with nothing newly saving", () => {
    const { fence } = fenceOver();
    fence.begin();
    const store = fieldsStore({ loaded: true, saving: false, hubLoading: false, writeUncertain: true, revision: 3 });

    retireSettingsHubPayload(fence, store.getState, store.setState);

    expect(store.getState().writeUncertain).toBe(true);
  });

  test("extra lands in the same publish, alongside the generic reset", () => {
    const { fence } = fenceOver();
    fence.begin();
    const store = fieldsStore({ loaded: true, saving: false, hubLoading: false, writeUncertain: false, revision: 3 });

    retireSettingsHubPayload(fence, store.getState, store.setState, { revision: 0 });

    expect(store.getState()).toMatchObject({ loaded: false, revision: 0 });
  });
});

describe("settleUnsettleableWrite", () => {
  test("a lost-hub write (still claimed, generation still current) clears saving and marks writeUncertain", () => {
    const { fence, drop } = fenceOver();
    const generation = fence.begin();
    const token = fence.claimWrite();
    const store = fieldsStore({ loaded: true, saving: true, hubLoading: false, writeUncertain: false, revision: 3 });

    // Support drops (the unknown window): the write's own claim is intact,
    // only support went away, so nothing else will ever settle it.
    drop();
    settleUnsettleableWrite(fence, generation, token === fence.writeToken, store.getState, store.setState);

    expect(store.getState()).toMatchObject({ saving: false, writeUncertain: true });
  });

  test("a superseded write (a later write claimed the token) publishes nothing - its successor owns the flags", () => {
    const { fence } = fenceOver();
    const generation = fence.begin();
    const token = fence.claimWrite();
    fence.claimWrite(); // a later write supersedes this one's token
    const store = fieldsStore({ loaded: true, saving: true, hubLoading: false, writeUncertain: false, revision: 3 });

    settleUnsettleableWrite(fence, generation, token === fence.writeToken, store.getState, store.setState);

    expect(store.getState()).toMatchObject({ saving: true, writeUncertain: false });
  });

  test("a lost-hub write with nothing saving is a no-op", () => {
    const { fence, drop } = fenceOver();
    const generation = fence.begin();
    const token = fence.claimWrite();
    const store = fieldsStore({ loaded: true, saving: false, hubLoading: false, writeUncertain: false, revision: 3 });

    drop();
    settleUnsettleableWrite(fence, generation, token === fence.writeToken, store.getState, store.setState);

    expect(store.getState()).toMatchObject({ saving: false, writeUncertain: false });
  });

  test("extra lands in the same publish, alongside the generic settle - keybindingsStore's own draftConflict", () => {
    const { fence, drop } = fenceOver();
    const generation = fence.begin();
    const token = fence.claimWrite();
    const store = fieldsStore({ loaded: true, saving: true, hubLoading: false, writeUncertain: false, revision: 3 });

    drop();
    settleUnsettleableWrite(fence, generation, token === fence.writeToken, store.getState, store.setState, {
      revision: 9,
    });

    expect(store.getState()).toMatchObject({ saving: false, writeUncertain: true, revision: 9 });
  });

  test("extra is not published when the write was superseded", () => {
    const { fence } = fenceOver();
    const generation = fence.begin();
    const token = fence.claimWrite();
    fence.claimWrite();
    const store = fieldsStore({ loaded: true, saving: true, hubLoading: false, writeUncertain: false, revision: 3 });

    settleUnsettleableWrite(fence, generation, token === fence.writeToken, store.getState, store.setState, {
      revision: 9,
    });

    expect(store.getState().revision).toBe(3);
  });
});

describe("createSettingsHubGeneration", () => {
  test("beginReadyGeneration wires notifications only once the fence actually starts a generation", () => {
    const { fence } = fenceOver();
    const wireNotifications = vi.fn(() => vi.fn());
    const retirePayload = vi.fn();
    const generationCore = createSettingsHubGeneration({ fence, wireNotifications, retirePayload });

    generationCore.beginReadyGeneration();

    expect(wireNotifications).toHaveBeenCalledTimes(1);
    expect(wireNotifications).toHaveBeenCalledWith(fence.generation);
  });

  test("beginReadyGeneration does not wire notifications for a disposed fence", () => {
    const { fence } = fenceOver();
    fence.dispose();
    const wireNotifications = vi.fn(() => vi.fn());
    const retirePayload = vi.fn();
    const generationCore = createSettingsHubGeneration({ fence, wireNotifications, retirePayload });

    generationCore.beginReadyGeneration();

    expect(wireNotifications).not.toHaveBeenCalled();
  });

  test("beginReadyGeneration unwires the previous generation's notifications before wiring the new one", () => {
    const { fence } = fenceOver();
    const firstUnwire = vi.fn();
    const secondUnwire = vi.fn();
    const wireNotifications = vi.fn().mockReturnValueOnce(firstUnwire).mockReturnValueOnce(secondUnwire);
    const retirePayload = vi.fn();
    const generationCore = createSettingsHubGeneration({ fence, wireNotifications, retirePayload });

    generationCore.beginReadyGeneration();
    fence.end();
    generationCore.beginReadyGeneration();

    expect(firstUnwire).toHaveBeenCalledTimes(1);
    expect(secondUnwire).not.toHaveBeenCalled();
  });

  test("endReadyGeneration ends the fence, unwires notifications and retires the payload, in that order", () => {
    const { fence } = fenceOver();
    const order: string[] = [];
    const end = fence.end;
    fence.end = () => {
      order.push("end");
      end();
    };
    const unwire = vi.fn(() => order.push("unwire"));
    const wireNotifications = vi.fn(() => unwire);
    const retirePayload = vi.fn(() => order.push("retirePayload"));
    const generationCore = createSettingsHubGeneration({ fence, wireNotifications, retirePayload });
    generationCore.beginReadyGeneration();

    generationCore.endReadyGeneration();

    expect(fence.generation).toBe(-1);
    expect(unwire).toHaveBeenCalledTimes(1);
    expect(retirePayload).toHaveBeenCalledTimes(1);
    expect(order).toEqual(["end", "unwire", "retirePayload"]);
  });

  test("endReadyGeneration before any generation began still ends the fence and retires, without unwiring anything", () => {
    const { fence } = fenceOver();
    const wireNotifications = vi.fn(() => vi.fn());
    const retirePayload = vi.fn();
    const generationCore = createSettingsHubGeneration({ fence, wireNotifications, retirePayload });

    generationCore.endReadyGeneration();

    expect(fence.generation).toBe(-1);
    expect(retirePayload).toHaveBeenCalledTimes(1);
  });
});
