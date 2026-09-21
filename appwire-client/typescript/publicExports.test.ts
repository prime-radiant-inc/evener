// @vitest-environment node
import { describe, expect, it } from "vitest";
import * as packageRoot from "./index";
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
  mergeTurnHistory,
  normalizeRow,
  numField,
  type OpenTarget,
  parseConditionText,
  sourceLabel,
  strArrayField,
  strField,
  type TranscriptDisplayAdvancedV1,
  type TurnHistoryMergeResult,
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
import * as transcriptDisplayConfigModule from "./transcriptDisplayConfig";

// #1431 also retired two transcriptDisplayConfig type aliases from the root.
// A type export is erased at runtime, so the value `in` checks below cannot see
// one, and the root's type surface needs its own compile-time lock. Only these
// two are asserted: TranscriptDisplayConfig is deliberately excluded because
// the generated wire type of that name owns the root spelling, so it is no
// longer this module's alias. The union keeps both references live for biome's
// noUnusedVariables; each directive is reported unused if an alias returns.
// @ts-expect-error -- deleted zero-consumer alias (#1431)
type RootRetiredTranscriptHookExitDetail = import("./index").TranscriptHookExitDetail;
// @ts-expect-error -- deleted zero-consumer alias (#1431)
type RootRetiredTranscriptLevel = import("./index").TranscriptLevel;
type RootRetiredTranscriptAliases = RootRetiredTranscriptHookExitDetail | RootRetiredTranscriptLevel;
void (undefined as RootRetiredTranscriptAliases | undefined);

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
  it("re-exports mergeTurnHistory and its result type", () => {
    const result: TurnHistoryMergeResult = mergeTurnHistory([], []);
    expect(result.turns).toEqual([]);
    expect(result.olderCoverage).toBe(false);
    expect(result.transcriptOverlap).toBe(false);
  });

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

  // The local advanced block shares its base name with the wire type, so the
  // V1 suffix is what lets a consumer name it from the package root without
  // shadowing the wire export. Importing it here in a type position fails
  // compilation if index.ts stops publishing it.
  it("re-exports TranscriptDisplayAdvancedV1 from the package root", () => {
    const advanced: TranscriptDisplayAdvancedV1 = {
      roundTimings: false,
      tokenCounts: false,
      estimatedCost: false,
      systemEvents: false,
      promptEvents: false,
      hookExits: "none",
    };
    expect(advanced.hookExits).toBe("none");
  });

  // #1431: transcriptDisplayConfig advertised up to three spellings per
  // operation (encodeLocalConfig / encodeConfig / encodeLocal) and C4 mirrored
  // the module 1:1 into this barrel, so every alias had become package API.
  // Assert the removal at both surfaces a consumer can reach: a root that
  // still re-published one would advertise a second spelling, and a module
  // that still exported one would leave it importable by module path.
  it("does not publish the retired transcriptDisplayConfig aliases", () => {
    const retiredValueAliases = [
      "encodeConfig",
      "encodeLocal",
      "decodeConfig",
      "decodeLocal",
      "parseLocalConfig",
      "wireToConfig",
      "configToWire",
      "fromWireTranscriptDisplayConfig",
      "toWireTranscriptDisplayConfig",
      "wireToDefault",
      "defaultToWire",
      "wireToDefaults",
      "defaultsToWire",
      "fingerprintConfig",
      "categoryInventory",
      "migrateLegacyConfig",
      "configFromLegacyPrefs",
      "legacyPrefsFromConfig",
      "SHIPPED_DESKTOP_CONFIG",
      "SHIPPED_MOBILE_CONFIG",
      "SHIPPED_DEFAULTS",
    ];
    for (const name of retiredValueAliases) {
      expect(name in packageRoot).toBe(false);
      expect(name in transcriptDisplayConfigModule).toBe(false);
    }
  });

  // The check above can only prove a name is absent, so the canonical
  // functions it must not sweep up need the opposite assertion. toWireDefault
  // and fromWireDefault are the symmetric default-codec pair and both stay
  // exported (#1431 removed aliases, not the canonical spellings).
  it("keeps the transcript display default codec pair on the package root", () => {
    expect(typeof packageRoot.fromWireDefault).toBe("function");
    expect(typeof packageRoot.toWireDefault).toBe("function");
  });
});
