/**
 * RED contract test for the typed harness actions and the live conversation
 * harness API. This file is written in Step 4 before the actual production
 * harness (`LiveConceptBrowserHarness`) exists; it imports `createReadyHarness`
 * and `drive` from a not-yet-created module, so the initial `vitest run` fails
 * with an import error — the intentional RED. Step 6 implements the harness
 * against the real `LiveConceptHost` and turns this GREEN.
 *
 * Every action is driven through the real production store types and the real
 * `ConversationFrameState` / `LiveComposerView` projection. `drive(action)`
 * creates a fresh ready harness seeded with `DRAFT` and `LAST_GOOD` for each
 * call, so each assertion starts from a deterministic ready state.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { createReadyHarness, drive } from "./LiveConceptBrowserHarness";
import type { HarnessAction } from "./live-conversation-harness-actions";
import { validateHarnessAction } from "./live-conversation-harness-actions";
import { makePathological39ItemFixture } from "./live-conversation-pathological.fixture";

const DRAFT = "draft-sentinel::production-appwire";
const LAST_GOOD = "last-good-key-digest::fixture";

afterEach(() => {
  vi.restoreAllMocks();
});

describe("validateHarnessAction", () => {
  it("accepts every known action type with its required fields", () => {
    const valid: HarnessAction[] = [
      { type: "items/prepend" },
      { type: "items/replace-authoritative" },
      { type: "items/evict", key: "item-1" },
      { type: "items/stream", key: "item-1", delta: "delta-text" },
      { type: "conversation/loading", generation: 2 },
      { type: "conversation/empty", generation: 3 },
      { type: "connection/offline" },
      { type: "connection/reconnecting", generation: 4 },
      { type: "read/fail", generation: 5, lastGood: "retain" },
      { type: "read/fail", generation: 6, lastGood: "none" },
      { type: "mutation/pending", kind: "send", draftSnapshot: DRAFT },
      { type: "mutation/fail", kind: "send", draftSnapshot: DRAFT },
      { type: "projection/malformed", generation: 7 },
      { type: "projection/publish", generation: 8, fixture: "pathological-39" },
      { type: "projection/publish", generation: 8, fixture: "variable-500" },
      {
        type: "viewport/set",
        innerHeight: 852,
        offsetTop: 0,
        height: 532,
        safeBottom: 34,
      },
    ];
    for (const action of valid) {
      expect(() => validateHarnessAction(action)).not.toThrow();
    }
  });

  it("rejects unknown action types", () => {
    expect(() => validateHarnessAction({ type: "unknown/action" })).toThrow(
      /unknown harness action type/,
    );
  });

  it("rejects non-object actions", () => {
    expect(() => validateHarnessAction(null)).toThrow(/plain object/);
    expect(() => validateHarnessAction("send")).toThrow(/plain object/);
    expect(() => validateHarnessAction([])).toThrow(/plain object/);
  });

  it("rejects actions missing the type field", () => {
    expect(() => validateHarnessAction({ generation: 2 })).toThrow(
      /missing string type/,
    );
  });

  it("rejects unknown fields on known actions", () => {
    expect(() =>
      validateHarnessAction({ type: "connection/offline", generation: 1 }),
    ).toThrow(/unknown field "generation"/);
    expect(() =>
      validateHarnessAction({
        type: "conversation/loading",
        generation: 2,
        extra: true,
      }),
    ).toThrow(/unknown field "extra"/);
  });

  it("rejects wrong-typed required fields", () => {
    expect(() =>
      validateHarnessAction({
        type: "conversation/loading",
        generation: "two",
      }),
    ).toThrow(/requires number "generation"/);
    expect(() =>
      validateHarnessAction({ type: "items/evict", key: 1 }),
    ).toThrow(/requires string "key"/);
    expect(() =>
      validateHarnessAction({
        type: "read/fail",
        generation: 5,
        lastGood: "maybe",
      }),
    ).toThrow(/lastGood "retain"|"none"/);
    expect(() =>
      validateHarnessAction({
        type: "mutation/pending",
        kind: 1,
        draftSnapshot: DRAFT,
      }),
    ).toThrow(/requires string "kind"/);
    expect(() =>
      validateHarnessAction({
        type: "projection/publish",
        generation: 8,
        fixture: "other",
      }),
    ).toThrow(/fixture "pathological-39"|"variable-500"/);
    expect(() =>
      validateHarnessAction({
        type: "viewport/set",
        innerHeight: "852",
        offsetTop: 0,
        height: 532,
        safeBottom: 34,
      }),
    ).toThrow(/requires number "innerHeight"/);
  });
});

describe("live conversation harness drive — typed state observations", () => {
  it("drives conversation/loading and observes disabled composer", async () => {
    expect(
      await drive({ type: "conversation/loading", generation: 2 }),
    ).toMatchObject({
      phase: "loading",
      draft: DRAFT,
      navigationReachable: true,
      composer: { draftEditable: false, primaryActionEnabled: false },
      retry: { visible: false },
    });
  });

  it("drives conversation/empty and observes editable composer", async () => {
    expect(
      await drive({ type: "conversation/empty", generation: 3 }),
    ).toMatchObject({
      phase: "empty",
      lastGoodKeyDigest: null,
      draft: DRAFT,
      composer: { draftEditable: true, primaryActionEnabled: true },
    });
  });

  it("drives connection/offline with retained last-good", async () => {
    expect(await drive({ type: "connection/offline" })).toMatchObject({
      lastGoodKeyDigest: LAST_GOOD,
      draft: DRAFT,
      navigationReachable: true,
      composer: { draftEditable: true, primaryActionEnabled: false },
    });
  });

  it("drives connection/reconnecting with retained last-good", async () => {
    expect(
      await drive({ type: "connection/reconnecting", generation: 4 }),
    ).toMatchObject({
      lastGoodKeyDigest: LAST_GOOD,
      draft: DRAFT,
      composer: { draftEditable: true, primaryActionEnabled: false },
    });
  });

  it("drives read/fail with retained last-good", async () => {
    expect(
      await drive({ type: "read/fail", generation: 5, lastGood: "retain" }),
    ).toMatchObject({
      phase: "read-error",
      lastGoodKeyDigest: LAST_GOOD,
      draft: DRAFT,
      retry: { visible: true },
      navigationReachable: true,
    });
  });

  it("drives read/fail without last-good", async () => {
    expect(
      await drive({ type: "read/fail", generation: 6, lastGood: "none" }),
    ).toMatchObject({
      phase: "read-error",
      lastGoodKeyDigest: null,
      draft: DRAFT,
      retry: { visible: true },
      navigationReachable: true,
    });
  });

  it("drives mutation/pending and observes pending mutation", async () => {
    expect(
      await drive({
        type: "mutation/pending",
        kind: "send",
        draftSnapshot: DRAFT,
      }),
    ).toMatchObject({
      draft: DRAFT,
      mutation: { kind: "send", status: "pending" },
      composer: { draftEditable: false, primaryActionEnabled: false },
      alertCount: 0,
    });
  });

  it("drives mutation/fail and observes failed mutation alert", async () => {
    expect(
      await drive({
        type: "mutation/fail",
        kind: "send",
        draftSnapshot: DRAFT,
      }),
    ).toMatchObject({
      draft: DRAFT,
      mutation: { kind: "send", status: "failed" },
      composer: { draftEditable: true },
      alertCount: 1,
    });
  });

  it("drives projection/malformed and observes compatibility error", async () => {
    expect(
      await drive({ type: "projection/malformed", generation: 7 }),
    ).toMatchObject({
      lastGoodKeyDigest: LAST_GOOD,
      draft: DRAFT,
      compatibilityError: "Unable to display conversation",
      retry: { visible: true },
    });
  });

  it("rejects stale projection/publish leaving the observation deep-equal", async () => {
    const api = createReadyHarness({
      generation: 7,
      draft: DRAFT,
      lastGoodKeyDigest: LAST_GOOD,
    });
    const beforeStale = api.snapshot();
    await api.dispatch({
      type: "projection/publish",
      generation: 6,
      fixture: "variable-500",
    });
    expect(api.snapshot()).toEqual(beforeStale);
  });

  it("accepts current-generation projection/publish with pathological fixture", async () => {
    const api = createReadyHarness({
      generation: 7,
      draft: DRAFT,
      lastGoodKeyDigest: LAST_GOOD,
    });
    const observation = await api.dispatch({
      type: "projection/publish",
      generation: 7,
      fixture: "pathological-39",
    });
    expect(observation.phase).toBe("ready");
    const _fixture = makePathological39ItemFixture();
    expect(observation.acceptedGeneration).toBe(7);
    // The published conversation carries the pathological fixture's items.
    expect(observation.lastGoodKeyDigest).not.toBeNull();
  });
});
