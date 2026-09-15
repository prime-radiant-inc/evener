// @vitest-environment node
import { describe, expect, it } from "vitest";
import {
  type ActivityNodeLike,
  activityNodeID,
  asJsonObject,
  boolField,
  buildEntityView,
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
  watchDisplayState,
  watchFoldKey,
  watchItems,
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
});
