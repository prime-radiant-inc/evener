import type {
  NavigationDelta,
  NavigationEntityRecord,
  NavigationOrderContainer,
  NavigationReadBase,
  NavigationSnapshot,
} from "../../protocol/types.gen";
import { cloneAndDeepFreezeJSON } from "./immutable";
import {
  NavigationBaseInvalidError,
  navigationOwnedContainerKey,
  navigationRootContainerKey,
  navigationViewScope,
  type ResourceKey,
} from "./types";

export type NavigationPresence = "present" | "gone";
export type NavigationGraphEntity = Readonly<NavigationEntityRecord>;
export type NavigationGraphContainer = Readonly<Omit<NavigationOrderContainer, "owner" | "children">> & {
  readonly owner: Readonly<NavigationOrderContainer["owner"]>;
  readonly children: readonly string[];
};
export interface NavigationGraph {
  readonly metadata: Readonly<Record<string, unknown>>;
  readonly entities: ReadonlyMap<string, NavigationGraphEntity>;
  readonly containers: ReadonlyMap<string, NavigationGraphContainer>;
}
export interface NormalizedResource {
  readonly key: ResourceKey;
  readonly graph: NavigationGraph;
  readonly version: NavigationReadBase;
  readonly presence: NavigationPresence;
}
export type DecodedNavigationResponse =
  | { status: "not_modified"; version: NavigationReadBase }
  | { status: "gone"; version: NavigationReadBase }
  | { status: "snapshot"; version: NavigationReadBase; snapshot: NavigationSnapshot }
  | { status: "delta"; version: NavigationReadBase; base: NavigationReadBase; delta: NavigationDelta };

const isRecord = (value: unknown): value is Record<string, unknown> =>
  !!value && typeof value === "object" && !Array.isArray(value);
const hasOwn = (value: Record<string, unknown>, key: string) => Object.hasOwn(value, key);
const exactKeys = (
  value: unknown,
  required: readonly string[],
  optional: readonly string[] = [],
): value is Record<string, unknown> => {
  if (!isRecord(value) || !required.every((key) => hasOwn(value, key))) return false;
  const allowed = new Set([...required, ...optional]);
  return Object.keys(value).every((key) => allowed.has(key));
};
const safeString = (value: unknown, max = 4096): value is string =>
  typeof value === "string" && value.length > 0 && value.length <= max;
const utf8Length = (value: string) => new TextEncoder().encode(value).length;
const identity = (value: unknown, allowEmpty = false): value is string =>
  typeof value === "string" && (allowEmpty ? value.length >= 0 : value.length > 0) && utf8Length(value) <= 1024;
const boundedString = (value: unknown, max: number): value is string =>
  typeof value === "string" && [...value].length <= max;
const count = (value: unknown): value is number => Number.isSafeInteger(value) && (value as number) >= 0;
const bool = (value: unknown): value is boolean => typeof value === "boolean";
const optional = (value: unknown, check: (candidate: unknown) => boolean) => value === undefined || check(value);
const schemaError = (category: string): Error => new Error(`navigation protocol: invalid ${category}`);
const MAX_NAVIGATION_DEPTH = 32;
const MAX_NAVIGATION_SESSION_ENTITIES = 2000;
const MAX_NAVIGATION_GRAPH_ENTITIES = MAX_NAVIGATION_SESSION_ENTITIES + 1;
const MAX_NAVIGATION_GRAPH_CONTAINERS = MAX_NAVIGATION_SESSION_ENTITIES + 3;

function rfc3339Timestamp(value: unknown): value is string {
  if (typeof value !== "string") return false;
  const match = value.match(/^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d{1,9})?(?:Z|[+-](\d{2}):(\d{2}))$/);
  if (!match || match[0] !== value) return false;
  const [, yearText, monthText, dayText, hourText, minuteText, secondText, zoneHourText, zoneMinuteText] = match;
  const year = Number(yearText);
  const month = Number(monthText);
  const day = Number(dayText);
  const hour = Number(hourText);
  const minute = Number(minuteText);
  const second = Number(secondText);
  const zoneHour = zoneHourText === undefined ? 0 : Number(zoneHourText);
  const zoneMinute = zoneMinuteText === undefined ? 0 : Number(zoneMinuteText);
  if (month < 1 || month > 12 || hour > 23 || minute > 59 || second > 59 || zoneHour > 23 || zoneMinute > 59)
    return false;
  const leap = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0);
  const daysInMonth = [31, leap ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31][month - 1];
  return day >= 1 && daysInMonth !== undefined && day <= daysInMonth;
}

const version = (value: unknown): value is NavigationReadBase =>
  exactKeys(value, ["generationId", "revision", "etag"]) &&
  safeString(value.generationId, 256) &&
  Number.isSafeInteger(value.revision) &&
  (value.revision as number) >= 0 &&
  safeString(value.etag, 1024);

const SESSION_REQUIRED = ["ref", "host_id", "session_id", "title", "project", "state", "kind", "live", "children"];
const SESSION_OPTIONAL = [
  "branch",
  "cluster_count",
  "favorite",
  "rename",
  "ask_pending",
  "dormant",
  "updated_at",
  "more_subagents",
  "omitted_descendants",
  "running_jobs",
  "completed_jobs",
];
const JOB_REQUIRED = ["job_id", "job_type", "status"];
const JOB_OPTIONAL = ["command", "task", "reason", "intent", "full_command"];
const PROJECT_REQUIRED = ["key", "name", "session_count"];
const PROJECT_OPTIONAL = [
  "working_dir",
  "rollup_state",
  "rollup_live",
  "rollup_attn",
  "default_expanded",
  "more_current",
  "more_recent",
  "more_archived",
  "worktrees",
  "is_archived",
  "favorite",
];

function jobValue(value: unknown): boolean {
  return (
    exactKeys(value, JOB_REQUIRED, JOB_OPTIONAL) &&
    identity(value.job_id) &&
    identity(value.job_type) &&
    identity(value.status) &&
    optional(value.command, (item) => boundedString(item, 512)) &&
    optional(value.task, (item) => boundedString(item, 512)) &&
    optional(value.reason, (item) => boundedString(item, 512)) &&
    optional(value.intent, (item) => boundedString(item, 512)) &&
    optional(value.full_command, (item) => boundedString(item, 4096))
  );
}

function sessionValue(value: unknown): value is Record<string, unknown> {
  return (
    exactKeys(value, SESSION_REQUIRED, SESSION_OPTIONAL) &&
    identity(value.ref) &&
    identity(value.host_id) &&
    identity(value.session_id) &&
    boundedString(value.title, 200) &&
    identity(value.project, true) &&
    identity(value.state) &&
    identity(value.kind) &&
    bool(value.live) &&
    Array.isArray(value.children) &&
    value.children.length === 0 &&
    optional(value.branch, (item) => boundedString(item, 512)) &&
    optional(value.cluster_count, count) &&
    optional(value.favorite, bool) &&
    optional(value.rename, bool) &&
    optional(value.ask_pending, bool) &&
    optional(value.dormant, bool) &&
    optional(value.updated_at, rfc3339Timestamp) &&
    optional(value.more_subagents, count) &&
    optional(value.omitted_descendants, count) &&
    optional(value.running_jobs, (item) => Array.isArray(item) && item.every(jobValue)) &&
    optional(value.completed_jobs, (item) => Array.isArray(item) && item.every(jobValue))
  );
}

function projectValue(value: unknown): value is Record<string, unknown> {
  return (
    exactKeys(value, PROJECT_REQUIRED, PROJECT_OPTIONAL) &&
    identity(value.key) &&
    boundedString(value.name, 512) &&
    count(value.session_count) &&
    optional(value.working_dir, (item) => typeof item === "string" && utf8Length(item) <= 4096) &&
    optional(value.rollup_state, (item) => boundedString(item, 512)) &&
    optional(value.rollup_live, count) &&
    optional(value.rollup_attn, count) &&
    optional(value.default_expanded, bool) &&
    optional(value.more_current, count) &&
    optional(value.more_recent, count) &&
    optional(value.more_archived, count) &&
    optional(value.worktrees, count) &&
    optional(value.is_archived, bool) &&
    optional(value.favorite, bool)
  );
}

function pinSectionValue(value: unknown): value is Record<string, unknown> {
  return (
    exactKeys(value, ["id", "name", "count"]) &&
    identity(value.id) &&
    boundedString(value.name, 512) &&
    count(value.count)
  );
}

function entityKeyValid(key: ResourceKey, value: unknown): value is string {
  if (typeof value !== "string") return false;
  const prefix = `${navigationViewScope(key)}/entity/`;
  return value.startsWith(prefix) && /^[0-9a-f]{64}$/.test(value.slice(prefix.length));
}

function entityIdentityForResource(
  key: ResourceKey,
  value: { kind: string; value: unknown },
): { logical: string; anchor: boolean } {
  if (key.kind === "manifest") throw schemaError("entity schema");
  if (key.kind === "pin_catalog") {
    if (value.kind !== "pin_section" || !pinSectionValue(value.value)) throw schemaError("entity schema");
    return { logical: `pin_section\0${value.value.id as string}`, anchor: false };
  }
  if (key.kind === "catalog") {
    if (value.kind !== "project" || !projectValue(value.value)) throw schemaError("entity schema");
    return { logical: `project\0${value.value.key as string}`, anchor: false };
  }
  if (key.kind === "project" && value.kind === "project") {
    if (!exactKeys(value.value, ["key"]) || value.value.key !== key.projectKey) throw schemaError("entity schema");
    return { logical: `project\0${value.value.key as string}`, anchor: true };
  }
  if (value.kind !== "session" || !sessionValue(value.value)) throw schemaError("entity schema");
  return { logical: `session\0${value.value.ref as string}`, anchor: false };
}

function entity(value: unknown, key: ResourceKey): value is NavigationEntityRecord {
  if (
    !exactKeys(value, ["key", "kind", "value"]) ||
    !entityKeyValid(key, value.key) ||
    !safeString(value.kind, 128) ||
    !isRecord(value.value)
  )
    return false;
  const kind = value.kind;
  if (typeof kind !== "string") return false;
  entityIdentityForResource(key, { kind, value: value.value });
  return true;
}

function owner(value: unknown): value is NavigationOrderContainer["owner"] {
  return (
    (exactKeys(value, ["kind", "slot"]) && value.kind === "resource_root" && safeString(value.slot, 128)) ||
    (exactKeys(value, ["kind", "slot", "entityKey"]) &&
      value.kind === "entity" &&
      safeString(value.entityKey, 2048) &&
      safeString(value.slot, 128))
  );
}

function containerKeyValid(key: ResourceKey, value: string): boolean {
  const scope = navigationViewScope(key);
  if (value.startsWith(`${scope}/root/`)) return value.length > `${scope}/root/`.length;
  const match = value.match(/^(.*\/entity\/[0-9a-f]{64})\/([^/]+)$/);
  return !!match && entityKeyValid(key, match[1]);
}

function container(value: unknown, key: ResourceKey): value is NavigationOrderContainer {
  return (
    exactKeys(value, ["key", "owner", "children"]) &&
    safeString(value.key, 2048) &&
    containerKeyValid(key, value.key) &&
    owner(value.owner) &&
    Array.isArray(value.children) &&
    value.children.every((child) => entityKeyValid(key, child))
  );
}

function descriptor(value: unknown): boolean {
  return exactKeys(value, ["count"]) && count(value.count);
}

function manifestMetadata(value: Record<string, unknown>): boolean {
  if (
    !exactKeys(value, ["generation_id", "revision", "sources", "attentionSummary", "sections", "catalogs"]) ||
    !Array.isArray(value.sources) ||
    value.sources.length > 64 ||
    !value.sources.every(
      (source) =>
        exactKeys(source, ["id", "label", "kind", "online"]) &&
        identity(source.id) &&
        boundedString(source.label, 512) &&
        identity(source.kind) &&
        bool(source.online),
    ) ||
    !exactKeys(value.attentionSummary, ["needsYou", "error", "working"]) ||
    !count(value.attentionSummary.needsYou) ||
    !count(value.attentionSummary.error) ||
    !count(value.attentionSummary.working) ||
    !exactKeys(value.sections, ["live", "needs_you", "pin_sections"]) ||
    !descriptor(value.sections.live) ||
    !descriptor(value.sections.needs_you) ||
    !descriptor(value.sections.pin_sections) ||
    !exactKeys(value.catalogs, ["projects", "archived_projects", "test_runs"]) ||
    !descriptor(value.catalogs.projects) ||
    !descriptor(value.catalogs.archived_projects) ||
    !descriptor(value.catalogs.test_runs)
  )
    return false;
  return true;
}

function effectiveLimit(key: ResourceKey): number {
  if (!("limit" in key)) return 0;
  const maximum = key.kind === "pin_catalog" || key.kind === "catalog" ? 100 : 50;
  return key.limit === 0 || key.limit > maximum ? maximum : key.limit;
}

function resourceKeyValid(key: ResourceKey): boolean {
  const selector = (value: number) => Number.isSafeInteger(value) && value >= 0 && value <= 4_294_967_295;
  if (key.kind === "manifest") return true;
  if (key.kind === "section")
    return (key.section === "live" || key.section === "needs_you") && selector(key.offset) && selector(key.limit);
  if (key.kind === "pin_catalog" || key.kind === "catalog") return selector(key.offset) && selector(key.limit);
  if (key.kind === "pin_section") return identity(key.sectionId) && selector(key.offset) && selector(key.limit);
  if (key.kind === "project") return identity(key.projectKey);
  if (key.kind === "project_page")
    return (
      identity(key.projectKey) &&
      ["current", "recent", "archived"].includes(key.tier) &&
      selector(key.offset) &&
      selector(key.limit)
    );
  return identity(key.ref);
}

function validateResourceMetadata(metadata: unknown, key: ResourceKey, versionValue: NavigationReadBase): void {
  if (
    !resourceKeyValid(key) ||
    !identity(versionValue.generationId) ||
    !Number.isSafeInteger(versionValue.revision) ||
    versionValue.revision <= 0 ||
    !isRecord(metadata) ||
    metadata.generation_id !== versionValue.generationId ||
    metadata.revision !== versionValue.revision
  )
    throw schemaError("resource metadata");
  let valid = false;
  if (key.kind === "manifest") valid = manifestMetadata(metadata);
  else if (key.kind === "section" || key.kind === "pin_section")
    valid =
      exactKeys(metadata, ["generation_id", "revision", "offset", "limit", "remaining", "truncated"]) &&
      metadata.offset === key.offset &&
      metadata.limit === effectiveLimit(key) &&
      count(metadata.remaining) &&
      bool(metadata.truncated);
  else if (key.kind === "pin_catalog" || key.kind === "catalog")
    valid =
      exactKeys(metadata, ["generation_id", "revision", "offset", "limit", "remaining"]) &&
      metadata.offset === key.offset &&
      metadata.limit === effectiveLimit(key) &&
      count(metadata.remaining);
  else if (key.kind === "project")
    valid =
      exactKeys(metadata, [
        "generation_id",
        "revision",
        "key",
        "current_remaining",
        "recent_remaining",
        "archived_remaining",
        "truncated",
      ]) &&
      metadata.key === key.projectKey &&
      count(metadata.current_remaining) &&
      count(metadata.recent_remaining) &&
      count(metadata.archived_remaining) &&
      bool(metadata.truncated);
  else if (key.kind === "project_page")
    valid =
      exactKeys(metadata, ["generation_id", "revision", "key", "tier", "offset", "limit", "remaining", "truncated"]) &&
      metadata.key === key.projectKey &&
      metadata.tier === key.tier &&
      metadata.offset === key.offset &&
      metadata.limit === effectiveLimit(key) &&
      count(metadata.remaining) &&
      bool(metadata.truncated);
  else
    valid =
      exactKeys(
        metadata,
        ["generation_id", "revision", "ref", "top_level_ref", "top_level"],
        ["project_key", "tier", "pin_section_id"],
      ) &&
      metadata.ref === key.ref &&
      identity(metadata.top_level_ref) &&
      bool(metadata.top_level) &&
      optional(metadata.project_key, (item) => identity(item)) &&
      optional(metadata.tier, (item) => identity(item)) &&
      optional(metadata.pin_section_id, (item) => identity(item));
  if (!valid) throw schemaError("resource metadata");
}

function expectedRootSlot(key: ResourceKey): string | undefined {
  if (key.kind === "manifest") return "manifest";
  if (key.kind === "section" || key.kind === "pin_section" || key.kind === "project_page") return "sessions";
  if (key.kind === "pin_catalog") return "pin_sections";
  if (key.kind === "catalog") return "projects";
  if (key.kind === "location") return "session";
  return undefined;
}

function exactSlots(actual: ReadonlySet<string>, expected: readonly string[]): boolean {
  return actual.size === expected.length && expected.every((slot) => actual.has(slot));
}

export function validateGraphForResource(
  key: ResourceKey,
  versionValue: NavigationReadBase,
  graph: NavigationGraph,
): void {
  validateResourceMetadata(graph.metadata, key, versionValue);
  if (graph.entities.size > MAX_NAVIGATION_GRAPH_ENTITIES || graph.containers.size > MAX_NAVIGATION_GRAPH_CONTAINERS)
    throw schemaError("resource graph");
  const logical = new Set<string>();
  let anchorKey: string | undefined;
  let sessionEntities = 0;
  for (const [mapKey, item] of graph.entities) {
    if (mapKey !== item.key || !entity(item, key)) throw schemaError("entity schema");
    if (item.kind === "session" && ++sessionEntities > MAX_NAVIGATION_SESSION_ENTITIES)
      throw schemaError("resource graph");
    const decoded = entityIdentityForResource(key, item);
    if (logical.has(decoded.logical)) throw schemaError("logical identity");
    logical.add(decoded.logical);
    if (decoded.anchor) {
      if (anchorKey) throw schemaError("resource graph");
      anchorKey = item.key;
    }
  }
  if (key.kind === "manifest" && graph.entities.size !== 0) throw schemaError("entity schema");
  if (key.kind === "project" && !anchorKey) throw schemaError("resource graph");
  if (key.kind === "location" && graph.entities.size > 1) throw schemaError("resource graph");

  const rootSlot = expectedRootSlot(key);
  let roots = 0;
  const rootChildren: string[] = [];
  const parents = new Map<string, string>();
  const slots = new Map<string, Set<string>>();
  const edges = new Map<string, string[]>();
  for (const [mapKey, item] of graph.containers) {
    if (mapKey !== item.key || !container(item, key)) throw schemaError("container schema");
    if (item.owner.kind === "resource_root") {
      roots++;
      rootChildren.push(...item.children);
      if (!rootSlot || item.owner.slot !== rootSlot || item.key !== navigationRootContainerKey(key, rootSlot))
        throw schemaError("resource graph");
      const maximum = key.kind === "location" ? 1 : effectiveLimit(key);
      if (maximum === 0 ? item.children.length !== 0 : item.children.length > maximum)
        throw schemaError("resource graph");
    } else {
      const ownerEntityKey = item.owner.entityKey;
      const ownerSlot = item.owner.slot;
      if (!ownerEntityKey || !ownerSlot) throw schemaError("resource graph");
      const ownerEntity = graph.entities.get(ownerEntityKey);
      if (!ownerEntity || item.key !== navigationOwnedContainerKey(ownerEntityKey, ownerSlot))
        throw schemaError("resource graph");
      const allowed =
        ownerEntity.kind === "session"
          ? ownerSlot === "children"
          : key.kind === "project" &&
            ownerEntity.key === anchorKey &&
            ["current", "recent", "archived"].includes(ownerSlot);
      if (!allowed) throw schemaError("resource graph");
      if (item.children.length > 50) throw schemaError("resource graph");
      const owned = slots.get(ownerEntity.key) ?? new Set<string>();
      owned.add(ownerSlot);
      slots.set(ownerEntity.key, owned);
      const children = edges.get(ownerEntity.key) ?? [];
      children.push(...item.children);
      edges.set(ownerEntity.key, children);
    }
    for (const child of item.children) {
      if (!graph.entities.has(child) || parents.has(child)) throw schemaError("resource graph");
      parents.set(child, item.key);
    }
  }
  if (rootSlot ? roots !== 1 : roots !== 0) throw schemaError("resource graph");
  for (const item of graph.entities.values()) {
    if (item.key === anchorKey) {
      if (parents.has(item.key) || !exactSlots(slots.get(item.key) ?? new Set(), ["current", "recent", "archived"]))
        throw schemaError("resource graph");
    } else if (!parents.has(item.key)) throw schemaError("resource graph");
    if (item.kind === "session" && !exactSlots(slots.get(item.key) ?? new Set(), ["children"]))
      throw schemaError("resource graph");
    if (item.kind !== "session" && item.key !== anchorKey && (slots.get(item.key)?.size ?? 0) !== 0)
      throw schemaError("resource graph");
  }
  const visiting = new Set<string>();
  const visited = new Set<string>();
  const visit = (entityKey: string, depth: number): void => {
    if (visiting.has(entityKey)) throw schemaError("resource graph");
    if (visited.has(entityKey)) return;
    if (depth > MAX_NAVIGATION_DEPTH) throw schemaError("resource graph");
    visiting.add(entityKey);
    for (const child of edges.get(entityKey) ?? []) visit(child, depth + 1);
    visiting.delete(entityKey);
    visited.add(entityKey);
  };
  for (const entityKey of rootChildren) visit(entityKey, 1);
  if (anchorKey) visit(anchorKey, 0);
  for (const entityKey of graph.entities.keys()) visit(entityKey, 1);
}

export function validateSnapshotForResource(
  key: ResourceKey,
  versionValue: NavigationReadBase,
  snapshot: NavigationSnapshot,
): void {
  if (
    !resourceKeyValid(key) ||
    !exactKeys(snapshot, ["metadata", "entities", "containers"]) ||
    !Array.isArray(snapshot.entities) ||
    !Array.isArray(snapshot.containers)
  )
    throw schemaError("snapshot");
  const entities = new Map<string, NavigationGraphEntity>();
  for (const item of snapshot.entities) {
    if (!entity(item, key) || entities.has(item.key)) throw schemaError("entity schema");
    entities.set(item.key, item);
  }
  const containers = new Map<string, NavigationGraphContainer>();
  for (const item of snapshot.containers) {
    if (!container(item, key) || containers.has(item.key)) throw schemaError("container schema");
    containers.set(item.key, item as NavigationGraphContainer);
  }
  validateGraphForResource(key, versionValue, {
    metadata: snapshot.metadata as Readonly<Record<string, unknown>>,
    entities,
    containers,
  });
}

function validateDeltaForResource(
  key: ResourceKey,
  versionValue: NavigationReadBase,
  value: unknown,
): value is NavigationDelta {
  if (
    !resourceKeyValid(key) ||
    !identity(versionValue.generationId) ||
    !Number.isSafeInteger(versionValue.revision) ||
    versionValue.revision <= 0 ||
    !exactKeys(
      value,
      ["upsertedEntities", "removedEntityKeys", "upsertedContainers", "removedContainerKeys"],
      ["metadata"],
    ) ||
    !Array.isArray(value.upsertedEntities) ||
    !Array.isArray(value.removedEntityKeys) ||
    !Array.isArray(value.upsertedContainers) ||
    !Array.isArray(value.removedContainerKeys)
  )
    return false;
  if (value.metadata !== undefined) validateResourceMetadata(value.metadata, key, versionValue);
  if (
    !value.upsertedEntities.every((item) => entity(item, key)) ||
    !value.upsertedContainers.every((item) => container(item, key)) ||
    !value.removedEntityKeys.every((item) => entityKeyValid(key, item)) ||
    !value.removedContainerKeys.every((item) => typeof item === "string" && containerKeyValid(key, item))
  )
    return false;
  const entities = [...value.upsertedEntities.map((item) => item.key), ...value.removedEntityKeys];
  const containers = [...value.upsertedContainers.map((item) => item.key), ...value.removedContainerKeys];
  if (new Set(entities).size !== entities.length || new Set(containers).size !== containers.length) return false;
  const logical = value.upsertedEntities.map((item) => entityIdentityForResource(key, item).logical);
  return new Set(logical).size === logical.length;
}

const RESPONSE_COMMON_KEYS = ["status", "generationId", "revision", "etag"] as const;
const RESPONSE_SNAPSHOT_KEYS = [...RESPONSE_COMMON_KEYS, "representation", "data"] as const;
const RESPONSE_DELTA_KEYS = [...RESPONSE_COMMON_KEYS, "representation", "base", "data"] as const;

export function decodeNavigationResponse(
  key: ResourceKey,
  sentBase: NavigationReadBase | undefined,
  wire: unknown,
): DecodedNavigationResponse {
  if (
    !isRecord(wire) ||
    !safeString(wire.generationId, 256) ||
    !Number.isSafeInteger(wire.revision) ||
    (wire.revision as number) < 0 ||
    !safeString(wire.etag, 1024) ||
    typeof wire.status !== "string"
  )
    throw new Error("navigation protocol: invalid response envelope");
  const current: NavigationReadBase = {
    generationId: wire.generationId as string,
    revision: wire.revision as number,
    etag: wire.etag as string,
  };
  if (wire.status === "not_modified") {
    if (
      !exactKeys(wire, RESPONSE_COMMON_KEYS) ||
      !sentBase ||
      sentBase.generationId !== current.generationId ||
      sentBase.revision !== current.revision ||
      sentBase.etag !== current.etag
    )
      throw new Error("navigation protocol: invalid not_modified response");
    return { status: "not_modified", version: current };
  }
  if (wire.status === "gone") {
    if (!exactKeys(wire, RESPONSE_COMMON_KEYS)) throw new Error("navigation protocol: invalid gone response");
    return { status: "gone", version: current };
  }
  if (
    wire.status !== "ok" ||
    (wire.representation !== "snapshot" && wire.representation !== "delta") ||
    !("data" in wire)
  )
    throw new Error("navigation protocol: invalid v2 response");
  if (wire.representation === "snapshot") {
    if (!exactKeys(wire, RESPONSE_SNAPSHOT_KEYS)) throw new Error("navigation protocol: invalid snapshot");
    validateSnapshotForResource(key, current, wire.data as NavigationSnapshot);
    return { status: "snapshot", version: current, snapshot: wire.data as NavigationSnapshot };
  }
  if (!exactKeys(wire, RESPONSE_DELTA_KEYS)) throw new Error("navigation protocol: invalid delta response");
  try {
    if (
      !version(wire.base) ||
      !sentBase ||
      wire.base.generationId !== sentBase.generationId ||
      wire.base.revision !== sentBase.revision ||
      wire.base.etag !== sentBase.etag ||
      !validateDeltaForResource(key, current, wire.data)
    )
      throw schemaError("delta");
    return { status: "delta", version: current, base: wire.base, delta: wire.data };
  } catch (cause) {
    throw new NavigationBaseInvalidError(cause);
  }
}

export function normalizedGraphFromSnapshot(snapshot: NavigationSnapshot): NavigationGraph {
  return Object.freeze({
    metadata: cloneAndDeepFreezeJSON((isRecord(snapshot.metadata) ? snapshot.metadata : {}) as Record<string, unknown>),
    entities: new Map(
      snapshot.entities.map((item) => [item.key, cloneAndDeepFreezeJSON(item) as NavigationGraphEntity]),
    ),
    containers: new Map(
      snapshot.containers.map((item) => [item.key, cloneAndDeepFreezeJSON(item) as NavigationGraphContainer]),
    ),
  });
}

type MaterializedValue = Readonly<Record<string, unknown>>;
type MaterializedEntityCacheEntry = Readonly<{
  childContainer: NavigationGraphContainer | undefined;
  children: readonly MaterializedValue[];
  value: MaterializedValue;
}>;
type MaterializedContainerCacheEntry = Readonly<{
  children: readonly MaterializedValue[];
}>;
type MaterializedResourceCacheEntry = Readonly<{
  scope: string;
  dependencies: readonly unknown[];
  value: MaterializedValue;
}>;

const materializedEntityCache = new WeakMap<object, MaterializedEntityCacheEntry>();
const materializedContainerCache = new WeakMap<object, MaterializedContainerCacheEntry>();
const materializedResourceCache = new WeakMap<object, MaterializedResourceCacheEntry>();
const emptyMaterializedChildren = Object.freeze([]) as readonly MaterializedValue[];

function sameIdentities(left: readonly unknown[], right: readonly unknown[]): boolean {
  return left.length === right.length && left.every((item, index) => item === right[index]);
}

/** Convert the normalized graph back to the resource-shaped view consumed by
 * the existing rail and hydration code. Entity/container identity remains in
 * the NormalizedResource; this projection only provides the compatibility
 * read model while callers migrate selectors to graph-native inputs. */
export function materializeNavigationResource(resource: NormalizedResource): MaterializedValue {
  const { key } = resource;
  const { graph } = resource;
  function entityValue(entityKey: string): MaterializedValue | undefined {
    const entity = graph.entities.get(entityKey);
    if (!entity) return undefined;
    const childContainer = graph.containers.get(navigationOwnedContainerKey(entityKey, "children"));
    const children = childContainer
      ? materializeContainer(navigationOwnedContainerKey(entityKey, "children"))
      : emptyMaterializedChildren;
    const cached = materializedEntityCache.get(entity as object);
    if (cached && cached.childContainer === childContainer && sameIdentities(cached.children, children))
      return cached.value;
    const result = Object.freeze({
      ...(isRecord(entity.value) ? entity.value : {}),
      ...(childContainer ? { children } : {}),
    });
    materializedEntityCache.set(entity as object, Object.freeze({ childContainer, children, value: result }));
    return result;
  }
  function materializeContainer(containerKey: string): readonly MaterializedValue[] {
    const container = graph.containers.get(containerKey);
    if (!container) return emptyMaterializedChildren;
    const children = container.children.flatMap((child) => {
      const value = entityValue(child);
      return value ? [value] : [];
    });
    const cached = materializedContainerCache.get(container as object);
    if (cached && sameIdentities(cached.children, children)) return cached.children;
    const frozenChildren = Object.freeze(children);
    materializedContainerCache.set(container as object, Object.freeze({ children: frozenChildren }));
    return frozenChildren;
  }
  const root = (slot: string) => {
    const containerKey = navigationRootContainerKey(key, slot);
    return {
      container: graph.containers.get(containerKey),
      children: materializeContainer(containerKey),
    };
  };
  const owned = (entityKey: string | undefined, slot: string) => {
    if (!entityKey) return { container: undefined, children: emptyMaterializedChildren };
    const containerKey = navigationOwnedContainerKey(entityKey, slot);
    return {
      container: graph.containers.get(containerKey),
      children: materializeContainer(containerKey),
    };
  };
  const cacheResource = (dependencies: readonly unknown[], value: () => MaterializedValue): MaterializedValue => {
    const scope = navigationViewScope(key);
    const cached = materializedResourceCache.get(graph.metadata as object);
    if (cached && cached.scope === scope && sameIdentities(cached.dependencies, dependencies)) return cached.value;
    const materialized = value();
    materializedResourceCache.set(
      graph.metadata as object,
      Object.freeze({ scope, dependencies: Object.freeze([...dependencies]), value: materialized }),
    );
    return materialized;
  };
  switch (key.kind) {
    case "manifest": {
      const manifest = root("manifest");
      return cacheResource([manifest.container, manifest.children], () => Object.freeze({ ...graph.metadata }));
    }
    case "section":
    case "pin_section":
    case "project_page": {
      const sessions = root("sessions");
      return cacheResource([sessions.container, sessions.children], () =>
        Object.freeze({ ...graph.metadata, sessions: sessions.children }),
      );
    }
    case "pin_catalog": {
      const pinSections = root("pin_sections");
      return cacheResource([pinSections.container, pinSections.children], () =>
        Object.freeze({ ...graph.metadata, pin_sections: pinSections.children }),
      );
    }
    case "catalog": {
      const projects = root("projects");
      return cacheResource([projects.container, projects.children], () =>
        Object.freeze({ ...graph.metadata, projects: projects.children }),
      );
    }
    case "project": {
      const metadata = graph.metadata as Record<string, unknown>;
      const projectEntity = [...graph.entities.values()].find(
        (entity) => entity.kind === "project" && isRecord(entity.value) && entity.value.key === key.projectKey,
      );
      const remaining = (slot: "current" | "recent" | "archived") => {
        const value = metadata[`${slot}_remaining`];
        return typeof value === "number" && Number.isSafeInteger(value) && value >= 0 ? value : 0;
      };
      const current = owned(projectEntity?.key, "current");
      const recent = owned(projectEntity?.key, "recent");
      const archived = owned(projectEntity?.key, "archived");
      return cacheResource(
        [current.container, current.children, recent.container, recent.children, archived.container, archived.children],
        () =>
          Object.freeze({
            ...metadata,
            key: key.projectKey,
            current: Object.freeze({ sessions: current.children, remaining: remaining("current") }),
            recent: Object.freeze({ sessions: recent.children, remaining: remaining("recent") }),
            archived: Object.freeze({ sessions: archived.children, remaining: remaining("archived") }),
          }),
      );
    }
    case "location": {
      const session = root("session");
      return cacheResource([session.container, session.children], () =>
        Object.freeze({ ...graph.metadata, session: session.children[0] }),
      );
    }
  }
}
