// @vitest-environment node
import { describe, expect, it } from "vitest";
import {
  type ActivityNodeLike,
  type ActivityWatchRow,
  activityNodeID,
  asJsonObject,
  boolField,
  buildEntityView,
  buildWatchRows,
  type ConditionSpec,
  conditionSpec,
  type DelegateEntityView,
  delegateRowFields,
  type EntityIdMatch,
  type EntityKind,
  type EntityView,
  entityKindOf,
  entityOpenTarget,
  findEntityIds,
  foldWatchSummaries,
  humanizeInterval,
  humanizeSeconds,
  indexActivityEntities,
  type JobEntityView,
  type JsonObject,
  jobOwnerSessionId,
  jobRowFields,
  normalizeRow,
  numField,
  type OpenTarget,
  parseConditionText,
  sourceLabel,
  strArrayField,
  strField,
  type WatchDisplayState,
  type WatchEntityView,
  type WatchRow,
  type WatchSummary,
  watchDeliveryInstants,
  watchDisplayState,
  watchFacts,
  watchFoldKey,
  watchIsScheduled,
  watchItems,
  watchMeta,
  watchName,
  watchRowID,
} from "./index";

type EntityLinkPublicTypes =
  | ConditionSpec
  | DelegateEntityView
  | EntityIdMatch
  | EntityKind
  | EntityView
  | JobEntityView
  | JsonObject
  | OpenTarget
  | WatchDisplayState
  | WatchEntityView
  | WatchRow
  | WatchSummary;

// activityNodeID's parameter type is part of the package's public surface: an
// installed consumer must be able to name it. Import it from the package root
// in a type position so a missing re-export fails compilation here.
describe("protocol package root public exports", () => {
  it("re-exports the type that activityNodeID's parameter uses", () => {
    const nodes: ActivityNodeLike[] = [
      { kind: "session", sessionId: "s1" },
      { kind: "delegate", delegateId: "d1" },
      { kind: "shell", jobId: "j1" },
    ];
    expect(nodes.map(activityNodeID)).toEqual(["session:s1", "delegate:d1", "job:j1"]);
  });

  it("re-exports the transcript entity-link surface", () => {
    const typeSurface: EntityLinkPublicTypes | undefined = undefined;
    expect(typeSurface).toBeUndefined();
    expect([
      asJsonObject,
      boolField,
      buildEntityView,
      conditionSpec,
      delegateRowFields,
      entityKindOf,
      entityOpenTarget,
      findEntityIds,
      foldWatchSummaries,
      humanizeInterval,
      humanizeSeconds,
      indexActivityEntities,
      jobOwnerSessionId,
      jobRowFields,
      normalizeRow,
      numField,
      parseConditionText,
      sourceLabel,
      strArrayField,
      strField,
      watchDisplayState,
      watchFoldKey,
      watchItems,
    ]).toSatisfy((values: unknown[]) => values.every((value) => typeof value === "function"));
  });

  // The watch-row API is part of the same row surface as the job/delegate/fold
  // rows. Importing the type and every helper from the package root (not the
  // module) means a future removal from index.ts fails this test at compile
  // time, which is exactly the regression the barrel gap let through.
  it("re-exports ActivityWatchRow and the watch-row helpers from the package root", () => {
    const rows = buildWatchRows([
      { id: "w1", source: "self", deliveries: 0, created_at: "2026-09-12T19:00:00Z", active: true },
      {
        id: "w2",
        source: "timer",
        deliveries: 2,
        created_at: "2026-09-12T18:00:00Z",
        active: false,
        cadence: [{ kind: "every", seconds: 600 }],
        delivery_times: ["2026-09-12T19:00:01Z"],
      },
    ]);
    const watched: ActivityWatchRow = rows[0]!;
    expect(watched).toMatchObject({ kind: "watch", id: watchRowID("w1"), level: 1, defaultDetailOpen: true });
    expect(watched.watch.id).toBe("w1");
    expect(rows.map((row) => row.id)).toEqual([watchRowID("w1"), watchRowID("w2")]);

    const scheduled = rows[1]!.watch;
    expect(watchIsScheduled(scheduled)).toBe(true);
    expect(watchDeliveryInstants(scheduled)).toEqual([Date.parse("2026-09-12T19:00:01Z")]);
    expect(watchName(watched.watch)).toBe("w1");
    expect(typeof watchMeta(scheduled)).toBe("string");
    expect(typeof watchFacts(scheduled, Date.parse("2026-09-12T20:00:00Z"))).toBe("string");
  });
});
