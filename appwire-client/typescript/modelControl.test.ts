import { describe, expect, test } from "vitest";
import { WireError } from "./errors";
import type { ThreadModel } from "./model";
import {
  type ModelControlClientState,
  type ModelControlInput,
  type ModelControlRequestFence,
  modelControlAuthority,
  modelControlDisplay,
  modelControlRecoveryInput,
  reduceModelControl,
  validateThreadModelSetResponse,
} from "./modelControl";
import type { ModelControlAuthority, ModelControlSnapshot, ThreadModelSetResponse } from "./types.gen";

const AUTHORITY: ModelControlAuthority = { instanceId: "instance-1", bootGeneration: "boot-1" };
const REPLACEMENT_AUTHORITY: ModelControlAuthority = { instanceId: "instance-2", bootGeneration: "boot-2" };
const CANONICAL = { modelProvider: "legacy-provider", model: "legacy-model" };

function snapshot(
  revision: number,
  options: {
    authority?: ModelControlAuthority;
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

function model(
  state: ModelControlClientState = { kind: "uninitialized", canonicalDisplay: CANONICAL },
  overrides: Partial<ThreadModel> = {},
): ThreadModel {
  return {
    ref: "local:thread-1",
    threadId: "thread-1",
    name: "Thread",
    status: "idle",
    modelProvider: CANONICAL.modelProvider,
    model: CANONICAL.model,
    modelControlState: state,
    visionModel: "",
    askPending: false,
    pendingEscalations: [],
    turns: [],
    queue: null,
    capabilities: {},
    createdAt: "2026-11-17T00:00:00Z",
    updatedAt: "2026-11-17T00:00:00Z",
    cwd: "",
    reasoningEffortLevels: [],
    supportsReasoning: false,
    ...overrides,
  } as ThreadModel;
}

function ready(current = snapshot(1)): ThreadModel {
  return model(
    { kind: "ready", snapshot: current },
    {
      modelProvider: current.active.modelProvider,
      model: current.active.model,
      reasoningEffortLevels: current.active.reasoningEffortLevels ?? [],
      supportsReasoning: current.active.supportsReasoning,
    },
  );
}

function reduceSequence(initial: ThreadModel, inputs: ModelControlInput[]) {
  let current = initial;
  let result = reduceModelControl(current, { kind: "connectionRetired" });
  current = initial;
  for (const input of inputs) {
    result = reduceModelControl(current, input);
    current = result.model;
  }
  return result;
}

function completeDesired(): NonNullable<ModelControlSnapshot["desired"]> {
  return {
    requestSequence: 2,
    clientMutationId: "mutation-2",
    modelProvider: "openai",
    model: "gpt-5.5",
    lifecycle: "pending",
  };
}

function completeResponse(): ThreadModelSetResponse {
  return {
    modelControlVersion: 1,
    disposition: "accepted",
    modelControl: snapshot(2, { desired: completeDesired() }),
  };
}

describe("explicit model-control state", () => {
  test("an accepted matching version-1 read establishes ready authority and projects all active metadata", () => {
    const current = snapshot(4, {
      provider: "openai",
      model: "gpt-5.5",
      levels: ["minimal", "high"],
      supportsReasoning: true,
    });

    const result = reduceModelControl(model(), {
      kind: "readAccepted",
      version: 1,
      snapshot: current,
      authority: AUTHORITY,
    });

    expect(result).toMatchObject({ decision: "applied", replacementReadRequired: false });
    expect(result.model.modelControlState).toEqual({ kind: "ready", snapshot: current });
    expect(result.model).toMatchObject({
      modelProvider: "openai",
      model: "gpt-5.5",
      reasoningEffortLevels: ["minimal", "high"],
      supportsReasoning: true,
    });
    expect(modelControlAuthority(result.model)).toEqual(AUTHORITY);
  });

  test("an accepted read tolerates a null effort ladder from an older encoder", () => {
    const current = snapshot(1);
    current.active.reasoningEffortLevels = null as unknown as string[];

    const result = reduceModelControl(model(), {
      kind: "readAccepted",
      version: 1,
      snapshot: current,
      authority: AUTHORITY,
    });

    expect(result.model.reasoningEffortLevels).toEqual([]);
  });

  test.each([
    ["absent", undefined],
    ["legacy", 0],
    ["unknown", 2],
  ])("an accepted %s-version read enters unsupported and preserves canonical display", (_, version) => {
    const withFreshCanonical = {
      ...ready(snapshot(5)),
      modelProvider: "canonical-provider",
      model: "canonical-model",
    };

    const result = reduceModelControl(withFreshCanonical, {
      kind: "readAccepted",
      version,
      authority: AUTHORITY,
    });

    expect(result).toMatchObject({ decision: "unsupported", replacementReadRequired: false });
    expect(result.model.modelControlState).toEqual({
      kind: "unsupported",
      canonicalDisplay: { modelProvider: "canonical-provider", model: "canonical-model" },
      ...(version === undefined ? {} : { observedVersion: version }),
    });
    expect(modelControlAuthority(result.model)).toBeUndefined();
  });

  test.each([
    ["missing snapshot", undefined, AUTHORITY],
    ["mismatched authority", snapshot(1, { authority: REPLACEMENT_AUTHORITY }), AUTHORITY],
  ])(
    "a malformed version-1 read enters recovery without immediately requesting another read: %s",
    (_, current, authority) => {
      const initial = ready(snapshot(3));

      const result = reduceModelControl(initial, {
        kind: "readAccepted",
        version: 1,
        snapshot: current,
        authority,
      });
      const repeated = reduceModelControl(result.model, {
        kind: "readAccepted",
        version: 1,
        snapshot: current,
        authority,
      });

      expect(result).toMatchObject({ decision: "recovering", replacementReadRequired: false });
      expect(result.model.modelControlState).toEqual({
        kind: "recovering",
        lastSafeDisplay: { modelProvider: "anthropic", model: "claude-sonnet-4-5" },
      });
      expect(repeated).toMatchObject({ decision: "recovering", replacementReadRequired: false });
    },
  );

  test("a connection retirement preserves canonical model fields but removes authority in every prior state", () => {
    const states: ModelControlClientState[] = [
      { kind: "uninitialized", canonicalDisplay: CANONICAL },
      { kind: "ready", snapshot: snapshot(1, { provider: "openai", model: "gpt-5.5" }) },
      { kind: "recovering", lastSafeDisplay: { modelProvider: "deepseek", model: "deepseek-v4" } },
      { kind: "unsupported", canonicalDisplay: CANONICAL, observedVersion: 9 },
    ];

    for (const state of states) {
      const initial = model(state);
      const result = reduceModelControl(initial, { kind: "connectionRetired" });
      expect(result).toMatchObject({ decision: "applied", replacementReadRequired: false });
      expect(result.model.modelControlState).toEqual({ kind: "uninitialized", canonicalDisplay: CANONICAL });
      expect(modelControlAuthority(result.model)).toBeUndefined();
    }
  });

  test("connection retirement drops an old authority's pending intent and preserves only canonical active display", () => {
    const pending = snapshot(2, {
      desired: {
        requestSequence: 2,
        clientMutationId: "old-connection-pick",
        modelProvider: "openai",
        model: "gpt-5.5",
        lifecycle: "pending",
      },
    });
    const initial = ready(pending);

    const result = reduceModelControl(initial, { kind: "connectionRetired" });

    expect(result.model.modelControlState).toEqual({
      kind: "uninitialized",
      canonicalDisplay: { modelProvider: "anthropic", model: "claude-sonnet-4-5" },
    });
    expect(modelControlDisplay(result.model)).toEqual({
      modelProvider: "anthropic",
      model: "claude-sonnet-4-5",
    });
  });

  test("only ready exposes mutation authority", () => {
    const states: ModelControlClientState[] = [
      { kind: "uninitialized", canonicalDisplay: CANONICAL },
      { kind: "recovering", lastSafeDisplay: CANONICAL },
      { kind: "unsupported", canonicalDisplay: CANONICAL, observedVersion: 3 },
    ];

    for (const state of states) expect(modelControlAuthority(model(state))).toBeUndefined();
    expect(modelControlAuthority(ready())).toEqual(AUTHORITY);
  });
});

describe("whole-snapshot reconciliation", () => {
  test("newer carriers replace, lower revisions are ignored, and equal content is duplicate", () => {
    const current = snapshot(3);
    const newer = snapshot(4, { provider: "openai", model: "gpt-5.5" });

    const lowerInitial = ready(current);
    const lower = reduceModelControl(lowerInitial, {
      kind: "snapshot",
      source: "response",
      snapshot: snapshot(2),
    });
    const duplicate = reduceModelControl(ready(current), {
      kind: "snapshot",
      source: "notification",
      snapshot: structuredClone(current),
    });
    const applied = reduceModelControl(ready(current), { kind: "snapshot", source: "response", snapshot: newer });

    expect(lower).toMatchObject({ decision: "ignored", replacementReadRequired: false });
    expect(lower.model).toBe(lowerInitial);
    expect(duplicate).toMatchObject({ decision: "duplicate", replacementReadRequired: false });
    expect(applied).toMatchObject({ decision: "applied", replacementReadRequired: false });
    expect(applied.model.modelControlState).toEqual({ kind: "ready", snapshot: newer });
    expect(applied.model).toMatchObject({ modelProvider: "openai", model: "gpt-5.5" });
  });

  test.each([
    ["equal-revision conflict", snapshot(3, { model: "claude-opus-4-6" })],
    ["authority mismatch", snapshot(1, { authority: REPLACEMENT_AUTHORITY })],
  ])("a live ready inconsistency enters recovery and requests exactly one read: %s", (_, incoming) => {
    const initial = ready(snapshot(3));

    const first = reduceModelControl(initial, { kind: "snapshot", source: "notification", snapshot: incoming });
    const repeated = reduceModelControl(first.model, { kind: "snapshot", source: "notification", snapshot: incoming });

    expect(first).toMatchObject({ decision: "recovering", replacementReadRequired: true });
    expect(first.model.modelControlState).toEqual({
      kind: "recovering",
      lastSafeDisplay: { modelProvider: "anthropic", model: "claude-sonnet-4-5" },
    });
    expect(repeated).toMatchObject({ decision: "ignored", replacementReadRequired: false });
  });

  test.each([
    ["authority-stale refusal", { kind: "authorityStale" } as const],
    ["post-dispatch response loss", { kind: "postDispatchFailure" } as const],
  ])("$0 enters recovery and requests exactly one read", (_, input) => {
    const first = reduceModelControl(ready(snapshot(2)), input);
    const repeated = reduceModelControl(first.model, input);

    expect(first).toMatchObject({ decision: "recovering", replacementReadRequired: true });
    expect(repeated).toMatchObject({ decision: "ignored", replacementReadRequired: false });
  });

  test.each([
    [
      "authority-stale refusal",
      new WireError("stale", -32602, { evenerErrorInfo: "authorityStale", mutationOutcome: "notAccepted" }),
      { kind: "authorityStale" },
    ],
    ["transport failure", new Error("closed"), { kind: "postDispatchFailure" }],
    [
      "unknown mutation outcome",
      new WireError("unknown", -32603, { mutationOutcome: "unknown" }),
      { kind: "postDispatchFailure" },
    ],
    [
      "blocked retry",
      new WireError("blocked", -32603, { mutationOutcome: "notAccepted", retryDisposition: "blocked" }),
      { kind: "postDispatchFailure" },
    ],
    [
      "settled refusal",
      new WireError("refused", -32602, { mutationOutcome: "notAccepted", retryDisposition: "none" }),
      undefined,
    ],
    ["ordinary wire error", new WireError("refused", -32602), undefined],
  ])("classifies model-set recovery after %s", (_, error, expected) => {
    expect(modelControlRecoveryInput(error)).toEqual(expected);
  });

  test("responses and notifications cannot establish or restore authority", () => {
    const carrier = { kind: "snapshot", source: "notification", snapshot: snapshot(2) } as const;
    const states: ModelControlClientState[] = [
      { kind: "uninitialized", canonicalDisplay: CANONICAL },
      { kind: "recovering", lastSafeDisplay: CANONICAL },
      { kind: "unsupported", canonicalDisplay: CANONICAL },
    ];

    for (const state of states) {
      const initial = model(state);
      const result = reduceModelControl(initial, carrier);
      expect(result).toMatchObject({ decision: "ignored", replacementReadRequired: false });
      expect(result.model).toBe(initial);
    }
  });

  test("response then notification and notification then stale response converge on the newest whole snapshot", () => {
    const second = snapshot(2, { model: "claude-opus-4-6" });
    const third = snapshot(3, { provider: "openai", model: "gpt-5.5" });

    const responseFirst = reduceSequence(ready(), [
      { kind: "snapshot", source: "response", snapshot: second },
      { kind: "snapshot", source: "notification", snapshot: third },
    ]);
    const notificationFirst = reduceSequence(ready(), [
      { kind: "snapshot", source: "notification", snapshot: third },
      { kind: "snapshot", source: "response", snapshot: second },
    ]);

    expect(responseFirst.model.modelControlState).toEqual({ kind: "ready", snapshot: third });
    expect(notificationFirst.model.modelControlState).toEqual({ kind: "ready", snapshot: third });
  });

  test("a local pending choice yields atomically to a foreign accepted choice", () => {
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

    const result = reduceModelControl(ready(localPending), {
      kind: "snapshot",
      source: "notification",
      snapshot: foreignApplied,
    });

    expect(result.model.modelControlState).toEqual({ kind: "ready", snapshot: foreignApplied });
    expect(modelControlDisplay(result.model)).toEqual({ modelProvider: "deepseek", model: "deepseek-v4" });
  });

  test("pending, applied, and rejected desired lifecycles project the approved display", () => {
    for (const lifecycle of ["pending", "applied", "rejected"] as const) {
      const current = snapshot(2, {
        desired: {
          requestSequence: 8,
          clientMutationId: "mutation-8",
          modelProvider: "openai",
          model: "gpt-5.5",
          lifecycle,
        },
      });
      expect(modelControlDisplay(ready(current))).toEqual(
        lifecycle === "rejected"
          ? { modelProvider: "anthropic", model: "claude-sonnet-4-5" }
          : { modelProvider: "openai", model: "gpt-5.5" },
      );
    }
  });

  test("a same-model applied snapshot still corrects active profile metadata", () => {
    const corrected = snapshot(2, {
      levels: ["minimal", "low", "medium", "high"],
      supportsReasoning: true,
      desired: {
        requestSequence: 2,
        clientMutationId: "same-model",
        modelProvider: "anthropic",
        model: "claude-sonnet-4-5",
        lifecycle: "applied",
      },
    });

    const result = reduceModelControl(ready(snapshot(1, { levels: [], supportsReasoning: false })), {
      kind: "snapshot",
      source: "notification",
      snapshot: corrected,
    });

    expect(result.model).toMatchObject({
      model: "claude-sonnet-4-5",
      reasoningEffortLevels: ["minimal", "low", "medium", "high"],
      supportsReasoning: true,
    });
  });

  test("a retired response stays outside the reducer through the existing request fence", () => {
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
    const initial = ready();
    const admitted =
      retiredFence.clientGeneration === currentFence.clientGeneration &&
      retiredFence.readGeneration === currentFence.readGeneration &&
      retiredFence.authority.instanceId === currentFence.authority.instanceId &&
      retiredFence.authority.bootGeneration === currentFence.authority.bootGeneration;
    const result = admitted
      ? reduceModelControl(initial, { kind: "snapshot", source: "response", snapshot: snapshot(2) })
      : undefined;

    expect(result).toBeUndefined();
    expect(initial.modelControlState).toEqual({ kind: "ready", snapshot: snapshot(1) });
  });
});

describe("model-set response validation", () => {
  test("returns a complete structurally valid response", () => {
    const response = completeResponse();
    expect(validateThreadModelSetResponse(response, "mutation-2")).toEqual(response);
  });

  test.each([
    ["empty object", {}],
    ["missing version", { disposition: "accepted", modelControl: completeResponse().modelControl }],
    ["unknown version", { ...completeResponse(), modelControlVersion: 2 }],
    ["missing disposition", { modelControlVersion: 1, modelControl: completeResponse().modelControl }],
    ["invalid disposition", { ...completeResponse(), disposition: "invalid" }],
    ["missing snapshot", { modelControlVersion: 1, disposition: "accepted" }],
    ["null snapshot", { ...completeResponse(), modelControl: null }],
    ["missing authority", { ...completeResponse(), modelControl: { revision: 2, active: snapshot(2).active } }],
    [
      "missing instance",
      { ...completeResponse(), modelControl: { ...snapshot(2), authority: { bootGeneration: "boot-1" } } },
    ],
    [
      "missing boot",
      { ...completeResponse(), modelControl: { ...snapshot(2), authority: { instanceId: "instance-1" } } },
    ],
    ["missing revision", { ...completeResponse(), modelControl: { ...snapshot(2), revision: undefined } }],
    ["zero revision", { ...completeResponse(), modelControl: { ...snapshot(2), revision: 0 } }],
    ["missing active", { ...completeResponse(), modelControl: { ...snapshot(2), active: undefined } }],
    [
      "missing active provider",
      { ...completeResponse(), modelControl: { ...snapshot(2), active: { ...snapshot(2).active, modelProvider: "" } } },
    ],
    [
      "missing active model",
      { ...completeResponse(), modelControl: { ...snapshot(2), active: { ...snapshot(2).active, model: "" } } },
    ],
    [
      "missing effort levels",
      {
        ...completeResponse(),
        modelControl: {
          ...snapshot(2),
          active: { modelProvider: "anthropic", model: "claude", supportsReasoning: true },
        },
      },
    ],
    [
      "missing reasoning support",
      {
        ...completeResponse(),
        modelControl: {
          ...snapshot(2),
          active: { modelProvider: "anthropic", model: "claude", reasoningEffortLevels: [] },
        },
      },
    ],
    [
      "zero desired sequence",
      {
        ...completeResponse(),
        modelControl: {
          ...snapshot(2),
          desired: { ...completeDesired(), requestSequence: 0 },
        },
      },
    ],
    [
      "missing desired mutation",
      {
        ...completeResponse(),
        modelControl: {
          ...snapshot(2),
          desired: { ...completeDesired(), clientMutationId: "" },
        },
      },
    ],
    [
      "missing desired provider",
      {
        ...completeResponse(),
        modelControl: {
          ...snapshot(2),
          desired: { ...completeDesired(), modelProvider: "" },
        },
      },
    ],
    [
      "missing desired model",
      {
        ...completeResponse(),
        modelControl: {
          ...snapshot(2),
          desired: { ...completeDesired(), model: "" },
        },
      },
    ],
    [
      "invalid desired lifecycle",
      {
        ...completeResponse(),
        modelControl: {
          ...snapshot(2),
          desired: { ...completeDesired(), lifecycle: "invalid" },
        },
      },
    ],
  ])("rejects malformed successful response: %s", (_, value) => {
    let caught: unknown;
    try {
      validateThreadModelSetResponse(value, "mutation-exact");
    } catch (error) {
      caught = error;
    }

    expect(caught).toBeInstanceOf(WireError);
    expect(caught).toMatchObject({
      code: -32603,
      data: {
        evenerErrorInfo: "mutationOutcomeUnknown",
        clientMutationId: "mutation-exact",
        mutationOutcome: "unknown",
        retryDisposition: "blocked",
      },
    });
  });
});
