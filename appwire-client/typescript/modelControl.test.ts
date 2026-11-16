import { describe, expect, test } from "vitest";
import type { ThreadModel } from "./model";
import {
  type ModelControlCarrier,
  type ModelControlDecision,
  type ModelControlRequestFence,
  modelControlAuthority,
  modelControlDisplay,
  reduceModelControl,
} from "./modelControl";
import type { ModelControlSnapshot } from "./types.gen";

const AUTHORITY = { instanceId: "instance-1", bootGeneration: "boot-1" };
const REPLACEMENT_AUTHORITY = { instanceId: "instance-2", bootGeneration: "boot-2" };

function snapshot(
  revision: number,
  options: {
    authority?: typeof AUTHORITY;
    provider?: string;
    model?: string;
    levels?: string[];
    supportsReasoning?: boolean;
    desired?: ModelControlSnapshot["desired"];
  } = {},
): ModelControlSnapshot {
  return {
    authority: options.authority ?? AUTHORITY,
    revision,
    active: {
      modelProvider: options.provider ?? "anthropic",
      model: options.model ?? "claude-sonnet-4-5",
      reasoningEffortLevels: options.levels ?? ["low", "medium", "high"],
      supportsReasoning: options.supportsReasoning ?? true,
    },
    ...(options.desired ? { desired: options.desired } : {}),
  };
}

function legacyModel(overrides: Partial<ThreadModel> = {}): ThreadModel {
  return {
    modelProvider: "legacy-provider",
    model: "legacy-model",
    reasoningEffortLevels: [],
    supportsReasoning: false,
    ...overrides,
  } as ThreadModel;
}

function reduceSequence(
  initial: ThreadModel,
  carriers: ModelControlCarrier[],
): { model: ThreadModel; decision: ModelControlDecision } {
  let result: { model: ThreadModel; decision: ModelControlDecision } = { model: initial, decision: "ignored" };
  for (const carrier of carriers) result = reduceModelControl(result.model, carrier);
  return result;
}

describe("whole model-control snapshot reconciliation", () => {
  test("an accepted version-1 read installs the whole snapshot and projects active metadata", () => {
    const current = snapshot(4, {
      provider: "openai",
      model: "gpt-5.5",
      levels: ["minimal", "high"],
      supportsReasoning: true,
    });

    const result = reduceModelControl(legacyModel(), { kind: "read", version: 1, snapshot: current });

    expect(result.decision).toBe("applied");
    expect(result.model.modelControlVersion).toBe(1);
    expect(result.model.modelControl).toEqual(current);
    expect(result.model).toMatchObject({
      modelProvider: "openai",
      model: "gpt-5.5",
      reasoningEffortLevels: ["minimal", "high"],
      supportsReasoning: true,
    });
    expect(modelControlAuthority(result.model)).toEqual(AUTHORITY);
  });

  test("an accepted read tolerates an empty effort ladder encoded as null", () => {
    const current = snapshot(1);
    current.active.reasoningEffortLevels = null as unknown as string[];

    const result = reduceModelControl(legacyModel(), { kind: "read", version: 1, snapshot: current });

    expect(result.decision).toBe("applied");
    expect(result.model.reasoningEffortLevels).toEqual([]);
  });

  test("an accepted unsupported read atomically clears capability and snapshot before exposing legacy metadata", () => {
    const capable = reduceModelControl(legacyModel(), { kind: "read", version: 1, snapshot: snapshot(2) }).model;
    const legacyRead = {
      ...capable,
      modelProvider: "legacy-provider-2",
      model: "legacy-model-2",
      reasoningEffortLevels: ["low"],
      supportsReasoning: true,
    };

    const result = reduceModelControl(legacyRead, { kind: "read", version: 0 });

    expect(result.decision).toBe("applied");
    expect(result.model).not.toHaveProperty("modelControlVersion");
    expect(result.model).not.toHaveProperty("modelControl");
    expect(modelControlDisplay(result.model)).toEqual({
      modelProvider: "legacy-provider-2",
      model: "legacy-model-2",
    });
  });

  test.each([
    {
      name: "response then notification",
      carriers: [
        { kind: "response", snapshot: snapshot(2, { model: "claude-opus-4-6" }) },
        { kind: "notification", snapshot: snapshot(3, { provider: "openai", model: "gpt-5.5" }) },
      ] satisfies ModelControlCarrier[],
      revision: 3,
      model: "gpt-5.5",
    },
    {
      name: "notification then stale response",
      carriers: [
        { kind: "notification", snapshot: snapshot(3, { provider: "openai", model: "gpt-5.5" }) },
        { kind: "response", snapshot: snapshot(2, { model: "claude-opus-4-6" }) },
      ] satisfies ModelControlCarrier[],
      revision: 3,
      model: "gpt-5.5",
    },
  ])("keeps the newest whole snapshot when $name arrives", ({ carriers, revision, model }) => {
    const initial = reduceModelControl(legacyModel(), { kind: "read", version: 1, snapshot: snapshot(1) }).model;
    const result = reduceSequence(initial, carriers);

    expect(result.model.modelControl?.revision).toBe(revision);
    expect(result.model.model).toBe(model);
  });

  test("ignores a lower revision inside the current authority", () => {
    const initial = reduceModelControl(legacyModel(), { kind: "read", version: 1, snapshot: snapshot(3) }).model;
    const result = reduceModelControl(initial, {
      kind: "notification",
      snapshot: snapshot(2, { model: "claude-opus-4-6" }),
    });

    expect(result.decision).toBe("ignored");
    expect(result.model).toBe(initial);
  });

  test("accepts an exact equal-revision duplicate without replacing state", () => {
    const current = snapshot(3);
    const initial = reduceModelControl(legacyModel(), { kind: "read", version: 1, snapshot: current }).model;
    const result = reduceModelControl(initial, { kind: "response", snapshot: structuredClone(current) });

    expect(result.decision).toBe("duplicate");
    expect(result.model).toBe(initial);
  });

  test("invalidates equal-revision content conflicts instead of choosing a value", () => {
    const initial = reduceModelControl(legacyModel(), { kind: "read", version: 1, snapshot: snapshot(3) }).model;
    const result = reduceModelControl(initial, {
      kind: "notification",
      snapshot: snapshot(3, { model: "claude-opus-4-6" }),
    });

    expect(result.decision).toBe("invalidate");
    expect(result.model.modelControlVersion).toBe(1);
    expect(result.model.modelControl).toBeUndefined();
  });

  test.each([
    ["pending", "openai", "gpt-5.5"],
    ["applied", "openai", "gpt-5.5"],
    ["rejected", "anthropic", "claude-sonnet-4-5"],
  ])("projects the %s desired lifecycle without merging records", (lifecycle, provider, model) => {
    const current = snapshot(2, {
      desired: {
        requestSequence: 8,
        clientMutationId: "mutation-8",
        modelProvider: "openai",
        model: "gpt-5.5",
        lifecycle,
      },
    });
    const state = reduceModelControl(legacyModel(), { kind: "read", version: 1, snapshot: current }).model;

    expect(modelControlDisplay(state)).toEqual({ modelProvider: provider, model });
  });

  test("an accepted pick survives a refused later call and then yields to its deferred rejection", () => {
    const pending = snapshot(2, {
      desired: {
        requestSequence: 7,
        clientMutationId: "accepted-pick",
        modelProvider: "openai",
        model: "gpt-5.5",
        lifecycle: "pending",
      },
    });
    let state = reduceModelControl(legacyModel(), { kind: "read", version: 1, snapshot: snapshot(1) }).model;
    state = reduceModelControl(state, { kind: "response", snapshot: pending }).model;

    // A direct refusal is a per-call outcome and never enters the shared reducer.
    expect(modelControlDisplay(state)).toEqual({ modelProvider: "openai", model: "gpt-5.5" });

    state = reduceModelControl(state, {
      kind: "notification",
      snapshot: {
        ...pending,
        revision: 3,
        desired: { ...pending.desired!, lifecycle: "rejected" },
      },
    }).model;
    expect(modelControlDisplay(state)).toEqual({ modelProvider: "anthropic", model: "claude-sonnet-4-5" });
  });

  test("a foreign accepted pick replaces the local pending choice as one whole snapshot", () => {
    const localPending = snapshot(2, {
      desired: {
        requestSequence: 7,
        clientMutationId: "local-pick",
        modelProvider: "openai",
        model: "gpt-5.5",
        lifecycle: "pending",
      },
    });
    const foreignApplied = snapshot(3, {
      provider: "deepseek",
      model: "deepseek-v4",
      desired: {
        requestSequence: 8,
        clientMutationId: "foreign-pick",
        modelProvider: "deepseek",
        model: "deepseek-v4",
        lifecycle: "applied",
      },
    });
    const initial = reduceModelControl(legacyModel(), { kind: "read", version: 1, snapshot: localPending }).model;

    const result = reduceModelControl(initial, { kind: "notification", snapshot: foreignApplied });

    expect(result.decision).toBe("applied");
    expect(result.model.modelControl).toEqual(foreignApplied);
    expect(modelControlDisplay(result.model)).toEqual({ modelProvider: "deepseek", model: "deepseek-v4" });
  });

  test("a same-model applied snapshot corrects stale profile metadata", () => {
    const initial = reduceModelControl(legacyModel(), {
      kind: "read",
      version: 1,
      snapshot: snapshot(1, { levels: [], supportsReasoning: false }),
    }).model;
    const corrected = snapshot(2, {
      levels: ["minimal", "low", "medium", "high"],
      supportsReasoning: true,
      desired: {
        requestSequence: 2,
        modelProvider: "anthropic",
        model: "claude-sonnet-4-5",
        lifecycle: "applied",
      },
    });

    const result = reduceModelControl(initial, { kind: "notification", snapshot: corrected });

    expect(result.model).toMatchObject({
      model: "claude-sonnet-4-5",
      reasoningEffortLevels: ["minimal", "low", "medium", "high"],
      supportsReasoning: true,
    });
  });

  test("a version-1 legacy accepted snapshot uses the same whole-state transition", () => {
    const applied = snapshot(2, {
      provider: "openai",
      model: "gpt-5.5",
      desired: {
        requestSequence: 4,
        modelProvider: "openai",
        model: "gpt-5.5",
        lifecycle: "applied",
      },
    });
    const initial = reduceModelControl(legacyModel(), { kind: "read", version: 1, snapshot: snapshot(1) }).model;

    const result = reduceModelControl(initial, { kind: "response", snapshot: applied });

    expect(result.decision).toBe("applied");
    expect(result.model.modelControl).toEqual(applied);
  });

  test("a lower-revision same-authority read cannot replace newer held control", () => {
    const initial = reduceModelControl(legacyModel(), { kind: "read", version: 1, snapshot: snapshot(9) }).model;
    const recovered = snapshot(2, { provider: "openai", model: "gpt-5.5" });

    const result = reduceModelControl(initial, { kind: "read", version: 1, snapshot: recovered });

    expect(result.decision).toBe("ignored");
    expect(result.model).toBe(initial);
  });

  test("an equal-revision conflicting read invalidates held control", () => {
    const initial = reduceModelControl(legacyModel(), { kind: "read", version: 1, snapshot: snapshot(3) }).model;

    const result = reduceModelControl(initial, {
      kind: "read",
      version: 1,
      snapshot: snapshot(3, { provider: "openai", model: "gpt-5.5" }),
    });

    expect(result.decision).toBe("invalidate");
    expect(result.model.modelControl).toBeUndefined();
  });

  test("an accepted replacement-authority read resets the comparison domain", () => {
    const initial = reduceModelControl(legacyModel(), { kind: "read", version: 1, snapshot: snapshot(9) }).model;
    const replacement = snapshot(1, { authority: REPLACEMENT_AUTHORITY, provider: "openai", model: "gpt-5.5" });

    const result = reduceModelControl(initial, { kind: "read", version: 1, snapshot: replacement });

    expect(result.decision).toBe("applied");
    expect(modelControlAuthority(result.model)).toEqual(REPLACEMENT_AUTHORITY);
    expect(result.model.modelControl?.revision).toBe(1);
  });

  test("a current-generation notification from another authority invalidates before comparing revisions", () => {
    const initial = reduceModelControl(legacyModel(), { kind: "read", version: 1, snapshot: snapshot(9) }).model;

    const result = reduceModelControl(initial, {
      kind: "notification",
      snapshot: snapshot(1, { authority: REPLACEMENT_AUTHORITY }),
    });

    expect(result.decision).toBe("invalidate");
    expect(result.model.modelControl).toBeUndefined();
  });

  test("clients can fence a retired response before it reaches the pure reducer", () => {
    const initial = reduceModelControl(legacyModel(), { kind: "read", version: 1, snapshot: snapshot(1) }).model;
    const retiredFence: ModelControlRequestFence = {
      authority: AUTHORITY,
      clientGeneration: 1,
      readGeneration: 4,
    };
    const currentFence: ModelControlRequestFence = {
      authority: REPLACEMENT_AUTHORITY,
      clientGeneration: 2,
      readGeneration: 5,
    };

    const response = snapshot(2);
    const admitted =
      retiredFence.clientGeneration === currentFence.clientGeneration &&
      retiredFence.readGeneration === currentFence.readGeneration &&
      retiredFence.authority.instanceId === currentFence.authority.instanceId &&
      retiredFence.authority.bootGeneration === currentFence.authority.bootGeneration;
    const result = admitted ? reduceModelControl(initial, { kind: "response", snapshot: response }) : undefined;

    expect(result).toBeUndefined();
    expect(initial.modelControl?.revision).toBe(1);
  });

  test("a quarantined newer notification replays after a capable read establishes authority", () => {
    const quarantined = snapshot(2, { provider: "openai", model: "gpt-5.5" });
    let state = legacyModel();

    state = reduceModelControl(state, { kind: "read", version: 1, snapshot: snapshot(1) }).model;
    const replay = reduceModelControl(state, { kind: "notification", snapshot: quarantined });

    expect(replay.decision).toBe("applied");
    expect(replay.model.modelControl).toEqual(quarantined);
  });

  test("an unsupported read lets the client discard quarantined version-1 carriers", () => {
    const quarantined = snapshot(2, { provider: "openai", model: "gpt-5.5" });
    const read = reduceModelControl(legacyModel(), { kind: "read", version: 0 });

    const replay =
      read.model.modelControlVersion === 1
        ? reduceModelControl(read.model, { kind: "notification", snapshot: quarantined })
        : undefined;

    expect(replay).toBeUndefined();
    expect(modelControlDisplay(read.model)).toEqual({ modelProvider: "legacy-provider", model: "legacy-model" });
  });
});
