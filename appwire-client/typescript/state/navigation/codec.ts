// Navigation snapshot and delta decoding: validates a hub navigation response
// against the resource key that asked for it, drops the value-record keys
// this client does not know, normalizes the entities and order containers
// into a deep-frozen graph, and materializes that graph back into the rows a
// view renders.
import type {
  AttentionSummary,
  NavigationCatalogs,
  NavigationDelta,
  NavigationEntityRecord,
  NavigationFailure,
  NavigationManifest,
  NavigationOrderContainer,
  NavigationPinSectionDescriptor,
  NavigationProjectSummary,
  NavigationQuestion,
  NavigationReadBase,
  NavigationResourceDescriptor,
  NavigationSections,
  NavigationSessionLocation,
  NavigationSessionSummary,
  NavigationSnapshot,
  NavigationSubagentTally,
  NavigationTaskProgress,
  Source,
} from "../../types.gen";
import { cloneAndDeepFreezeJSON } from "./immutable";
import {
  NAVIGATION_CATALOG_LIMIT,
  NAVIGATION_SECTION_LIMIT,
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
// exactKeys is for structure: the response envelope, the snapshot and delta
// records, entity, container and owner records, and paging metadata. A key the
// codec does not know there changes how the rest of the response is read, so
// a change of that kind ships behind a new representationVersion.
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

// A value record's keys: the keys it must carry, every key the codec knows
// (the required keys, then the optional ones), and the keys whose values are
// themselves value records, one record or a list of them.
interface ValueRecordKeys {
  readonly required: readonly string[];
  readonly known: readonly string[];
  readonly nested?: Readonly<Record<string, ValueRecordKeys>>;
}

// A value record's key table, mapped over its generated interface: every key
// of the interface must be listed, and each entry must say whether the
// interface marks that key optional. A key the interface dropped, one added
// without a table entry, or one flipped between required and optional stops
// the table from compiling (#2477).
export type ValueRecordKeyTable<T> = {
  readonly [K in keyof T]-?: Record<never, never> extends Pick<T, K> ? "optional" : "required";
};

// valueRecordKeys turns that table back into the runtime lists the validators
// read: the required keys, every key (in the table's own order, required keys
// written first), and the nested value records on the keys that hold them.
const valueRecordKeys = <T>(table: ValueRecordKeyTable<T>, nested?: ValueRecordKeys["nested"]): ValueRecordKeys => {
  const required: string[] = [];
  const known: string[] = [];
  for (const [key, mark] of Object.entries(table as Record<string, "required" | "optional">)) {
    known.push(key);
    if (mark === "required") required.push(key);
  }
  return { required, known, nested };
};

// knownKeys is exactKeys for a value record: every required key is present,
// and a key the record's keys do not name is allowed. A newer hub adds
// optional keys to value records without a ProtocolVersion bump
// (appwire/types.go), and an app built before a key existed must keep reading
// every page that carries it. The validators still check every key they name;
// decodeNavigationResponse then drops the rest (dropUnknownKeys).
const knownKeys = (value: unknown, keys: ValueRecordKeys): value is Record<string, unknown> =>
  isRecord(value) && keys.required.every((key) => hasOwn(value, key));

// dropUnknownKeys copies a validated value record, keeping only the keys its
// ValueRecordKeys name, nested records included. It runs after validation, so
// every nested value it recurses into is a record or a list of records.
// Dropping keeps unvalidated data out of the graph and the rendered rows, and
// keeps merge's identity check from seeing a change an older app cannot show.
function dropUnknownKeys(value: Record<string, unknown>, keys: ValueRecordKeys): Record<string, unknown> {
  const kept: Record<string, unknown> = {};
  for (const key of keys.known) {
    if (!hasOwn(value, key)) continue;
    const item = value[key];
    const nested = keys.nested?.[key];
    if (nested === undefined) kept[key] = item;
    else if (Array.isArray(item)) kept[key] = item.map((entry) => dropUnknownKeys(entry, nested));
    else kept[key] = dropUnknownKeys(item as Record<string, unknown>, nested);
  }
  return kept;
}

const MAX_NAVIGATION_DEPTH = 32;
const MAX_NAVIGATION_SESSION_ENTITIES = 2000;
const MAX_NAVIGATION_GRAPH_ENTITIES = MAX_NAVIGATION_SESSION_ENTITIES + 1;
const MAX_NAVIGATION_GRAPH_CONTAINERS = MAX_NAVIGATION_SESSION_ENTITIES + 3;
// MAX_NAVIGATION_PROJECT_SOURCES mirrors the projector's
// maxNavigationProjectSources: one entry per configured source plus the
// controller's own, which a merged project spells "local".
const MAX_NAVIGATION_PROJECT_SOURCES = 65;

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

const TASKS_KEYS = valueRecordKeys<NavigationTaskProgress>({
  total: "required",
  done: "required",
  cancelled: "optional",
  current_id: "optional",
  current: "optional",
});
const SUBAGENT_TALLY_KEYS = valueRecordKeys<NavigationSubagentTally>({
  running: "required",
  failed: "required",
  done: "required",
});
const QUESTION_KEYS = valueRecordKeys<NavigationQuestion>({
  text: "required",
  count: "required",
  options: "optional",
});
const FAILURE_KEYS = valueRecordKeys<NavigationFailure>({
  title: "optional",
  cause_kind: "optional",
  provider: "optional",
  status: "optional",
});
const SESSION_KEYS = valueRecordKeys<NavigationSessionSummary>(
  {
    ref: "required",
    host_id: "required",
    session_id: "required",
    title: "required",
    project: "required",
    state: "required",
    kind: "required",
    live: "required",
    children: "required",
    branch: "optional",
    favorite: "optional",
    rename: "optional",
    ask_pending: "optional",
    approval_pending: "optional",
    approval_tool: "optional",
    approval_target: "optional",
    question: "optional",
    failure: "optional",
    last_message: "optional",
    model_name: "optional",
    dormant: "optional",
    offline: "optional",
    updated_at: "optional",
    turn_ended_at: "optional",
    unseen: "optional",
    more_subagents: "optional",
    subagents: "optional",
    omitted_descendants: "optional",
    running_job_count: "optional",
    running_job_command: "optional",
    watch_count: "optional",
    armed_watch_count: "optional",
    tasks: "optional",
  },
  {
    tasks: TASKS_KEYS,
    subagents: SUBAGENT_TALLY_KEYS,
    question: QUESTION_KEYS,
    failure: FAILURE_KEYS,
  },
);
const PROJECT_KEYS = valueRecordKeys<NavigationProjectSummary>({
  key: "required",
  name: "required",
  session_count: "required",
  working_dir: "optional",
  rollup_state: "optional",
  rollup_live: "optional",
  rollup_attn: "optional",
  default_expanded: "optional",
  more_current: "optional",
  more_recent: "optional",
  more_archived: "optional",
  worktrees: "optional",
  is_archived: "optional",
  favorite: "optional",
  sources: "optional",
});
// A project resource's anchor entity carries only key; the rest of the summary
// arrives through the owned containers, so the table is the generated
// summary's key field and nothing more.
const PROJECT_ANCHOR_KEYS = valueRecordKeys<Pick<NavigationProjectSummary, "key">>({ key: "required" });
const PIN_SECTION_KEYS = valueRecordKeys<NavigationPinSectionDescriptor>({
  id: "required",
  name: "required",
  count: "required",
});
const SOURCE_KEYS = valueRecordKeys<Source>({
  id: "required",
  label: "required",
  kind: "required",
  online: "required",
});
const COUNT_KEYS = valueRecordKeys<NavigationResourceDescriptor>({ count: "required" });
const ATTENTION_SUMMARY_KEYS = valueRecordKeys<AttentionSummary>({
  needsYou: "required",
  error: "required",
  working: "required",
});
const SECTIONS_KEYS = valueRecordKeys<NavigationSections>(
  { live: "required", needs_you: "required", pin_sections: "required" },
  { live: COUNT_KEYS, needs_you: COUNT_KEYS, pin_sections: COUNT_KEYS },
);
const CATALOGS_KEYS = valueRecordKeys<NavigationCatalogs>(
  { projects: "required", archived_projects: "required", test_runs: "required" },
  { projects: COUNT_KEYS, archived_projects: COUNT_KEYS, test_runs: COUNT_KEYS },
);
const MANIFEST_KEYS = valueRecordKeys<NavigationManifest>(
  {
    generation_id: "required",
    revision: "required",
    sources: "required",
    attentionSummary: "required",
    sections: "required",
    catalogs: "required",
  },
  { sources: SOURCE_KEYS, attentionSummary: ATTENTION_SUMMARY_KEYS, sections: SECTIONS_KEYS, catalogs: CATALOGS_KEYS },
);
// A location resource's metadata is the generated response type without the
// session the resource carries through its containers; the metadata wire type
// has no generated name of its own.
const LOCATION_KEYS = valueRecordKeys<Omit<NavigationSessionLocation, "session">>({
  generation_id: "required",
  revision: "required",
  ref: "required",
  top_level_ref: "required",
  top_level: "required",
  project_key: "optional",
  tier: "optional",
  pin_section_id: "optional",
});

// Mirrors navigationTaskProgressValid: safe non-negative counts, no more tasks
// done and cancelled than exist (an absent cancelled count is zero), and a
// current task within the label bound.
const tasksValue = (value: unknown): boolean =>
  knownKeys(value, TASKS_KEYS) &&
  count(value.total) &&
  count(value.done) &&
  optional(value.cancelled, count) &&
  optional(value.current_id, count) &&
  optional(value.current, (item) => boundedString(item, 512)) &&
  (value.done as number) + ((value.cancelled as number | undefined) ?? 0) <= (value.total as number);

// Mirrors navigationSubagentTallyValid: every count is a safe non-negative
// integer.
const subagentTallyValue = (value: unknown): boolean =>
  knownKeys(value, SUBAGENT_TALLY_KEYS) && count(value.running) && count(value.failed) && count(value.done);

// Mirrors navigationQuestionValid's bounds: text of 1 to 200 characters, at
// least one question counted, and at most five labels of 1 to 80 characters.
const questionValue = (value: unknown): boolean =>
  knownKeys(value, QUESTION_KEYS) &&
  boundedString(value.text, 200) &&
  value.text !== "" &&
  count(value.count) &&
  (value.count as number) >= 1 &&
  optional(
    value.options,
    (item) =>
      Array.isArray(item) && item.length <= 5 && item.every((label) => boundedString(label, 80) && label !== ""),
  );

// Mirrors navigationFailureValid: a title of 1 to 80 characters or a cause kind
// (at least one), cause identities, and a safe non-negative status.
const failureValue = (value: unknown): boolean =>
  knownKeys(value, FAILURE_KEYS) &&
  (value.title !== undefined || value.cause_kind !== undefined) &&
  optional(value.title, (item) => boundedString(item, 80) && item !== "") &&
  optional(value.cause_kind, (item) => identity(item)) &&
  optional(value.provider, (item) => identity(item)) &&
  optional(value.status, count);

// sessionFieldsValue checks every field of a session summary except its
// children, whose rule depends on where the summary travels.
function sessionFieldsValue(value: unknown): value is Record<string, unknown> & { children: unknown[] } {
  return (
    knownKeys(value, SESSION_KEYS) &&
    identity(value.ref) &&
    identity(value.host_id) &&
    identity(value.session_id) &&
    boundedString(value.title, 200) &&
    identity(value.project, true) &&
    identity(value.state) &&
    identity(value.kind) &&
    bool(value.live) &&
    Array.isArray(value.children) &&
    optional(value.branch, (item) => boundedString(item, 512)) &&
    optional(value.favorite, bool) &&
    optional(value.rename, bool) &&
    optional(value.ask_pending, bool) &&
    optional(value.approval_pending, bool) &&
    optional(value.approval_tool, (item) => identity(item)) &&
    optional(value.approval_target, (item) => boundedString(item, 512)) &&
    optional(value.question, questionValue) &&
    optional(value.failure, failureValue) &&
    optional(value.last_message, (item) => boundedString(item, 200) && item !== "") &&
    optional(value.model_name, (item) => boundedString(item, 512) && item !== "") &&
    optional(value.dormant, bool) &&
    optional(value.offline, bool) &&
    optional(value.updated_at, rfc3339Timestamp) &&
    optional(value.turn_ended_at, rfc3339Timestamp) &&
    optional(value.unseen, bool) &&
    optional(value.more_subagents, count) &&
    optional(value.subagents, subagentTallyValue) &&
    optional(value.omitted_descendants, count) &&
    optional(value.running_job_count, count) &&
    optional(value.running_job_command, (item) => boundedString(item, 512)) &&
    optional(value.watch_count, count) &&
    optional(value.armed_watch_count, count) &&
    ((value.armed_watch_count as number | undefined) ?? 0) <= ((value.watch_count as number | undefined) ?? 0) &&
    optional(value.tasks, tasksValue)
  );
}

// A graph entity's children are edges, so its own children array is empty.
function sessionValue(value: unknown): value is Record<string, unknown> {
  return sessionFieldsValue(value) && value.children.length === 0;
}

// An evener/archived/list row is a plain summary with its children (fork
// originals) nested inline, as the hub projects them. Every row and nested
// child counts against `budget`, which starts at the entity bound a
// navigation session may hold.
function sessionTreeValue(value: unknown, depth: number, budget: { nodes: number }): boolean {
  return (
    depth <= MAX_NAVIGATION_DEPTH &&
    --budget.nodes >= 0 &&
    sessionFieldsValue(value) &&
    value.children.every((child) => sessionTreeValue(child, depth + 1, budget))
  );
}

/** Validates the `sessions` of an evener/archived/list response: at most one
 * page of plain session summaries whose children are nested inline. Throws on
 * any malformed row or an oversized page. */
export function decodeArchivedListSessions(value: unknown): NavigationSessionSummary[] {
  const budget = { nodes: MAX_NAVIGATION_SESSION_ENTITIES };
  if (
    !Array.isArray(value) ||
    value.length > NAVIGATION_SECTION_LIMIT ||
    !value.every((row) => sessionTreeValue(row, 1, budget))
  )
    throw schemaError("archived list");
  return value as NavigationSessionSummary[];
}

function projectValue(value: unknown): value is Record<string, unknown> {
  return (
    knownKeys(value, PROJECT_KEYS) &&
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
    optional(value.favorite, bool) &&
    // The sources that own the project's rows, spelled "local" for this hub's
    // own and a host name for each remote owner. Every entry is a decision
    // key, so an empty one would address no source: each must be a bounded,
    // non-empty identity, and the list is capped at the registry's own bound.
    optional(
      value.sources,
      (item) =>
        Array.isArray(item) &&
        item.length <= MAX_NAVIGATION_PROJECT_SOURCES &&
        item.every((source) => identity(source)),
    )
  );
}

function pinSectionValue(value: unknown): value is Record<string, unknown> {
  return (
    knownKeys(value, PIN_SECTION_KEYS) && identity(value.id) && boundedString(value.name, 512) && count(value.count)
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
    if (!knownKeys(value.value, PROJECT_ANCHOR_KEYS) || value.value.key !== key.projectKey)
      throw schemaError("entity schema");
    return { logical: `project\0${value.value.key as string}`, anchor: true };
  }
  if (value.kind !== "session" || !sessionValue(value.value)) throw schemaError("entity schema");
  return { logical: `session\0${value.value.ref as string}`, anchor: false };
}

// entity checks an entity record's structure only. validateDeltaForResource
// runs the value validator separately (entityIdentityForResource below), so
// this stays a boolean guard rather than a second full validation (#2478).
function entity(value: unknown, key: ResourceKey): value is NavigationEntityRecord {
  return (
    exactKeys(value, ["key", "kind", "value"]) &&
    entityKeyValid(key, value.key) &&
    safeString(value.kind, 128) &&
    isRecord(value.value)
  );
}

// validateEntity runs an entity record's structure check and its value's
// validator once, returning the value's decoded identity. validateGraphForResource
// needs the identity, so a single call replaces the
// entity()+entityIdentityForResource() pair it used to run (#2478).
function validateEntity(value: unknown, key: ResourceKey): { logical: string; anchor: boolean } {
  if (!entity(value, key)) throw schemaError("entity schema");
  return entityIdentityForResource(key, { kind: value.kind, value: value.value });
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
  return knownKeys(value, COUNT_KEYS) && count(value.count);
}

function manifestMetadata(value: Record<string, unknown>): boolean {
  if (
    !knownKeys(value, MANIFEST_KEYS) ||
    !Array.isArray(value.sources) ||
    value.sources.length > 64 ||
    !value.sources.every(
      (source) =>
        knownKeys(source, SOURCE_KEYS) &&
        identity(source.id) &&
        boundedString(source.label, 512) &&
        identity(source.kind) &&
        bool(source.online),
    ) ||
    !knownKeys(value.attentionSummary, ATTENTION_SUMMARY_KEYS) ||
    !count(value.attentionSummary.needsYou) ||
    !count(value.attentionSummary.error) ||
    !count(value.attentionSummary.working) ||
    !knownKeys(value.sections, SECTIONS_KEYS) ||
    !descriptor(value.sections.live) ||
    !descriptor(value.sections.needs_you) ||
    !descriptor(value.sections.pin_sections) ||
    !knownKeys(value.catalogs, CATALOGS_KEYS) ||
    !descriptor(value.catalogs.projects) ||
    !descriptor(value.catalogs.archived_projects) ||
    !descriptor(value.catalogs.test_runs)
  )
    return false;
  return true;
}

function effectiveLimit(key: ResourceKey): number {
  if (!("limit" in key)) return 0;
  const maximum =
    key.kind === "pin_catalog" || key.kind === "catalog" ? NAVIGATION_CATALOG_LIMIT : NAVIGATION_SECTION_LIMIT;
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
      knownKeys(metadata, LOCATION_KEYS) &&
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
    if (mapKey !== item.key) throw schemaError("entity schema");
    const decoded = validateEntity(item, key);
    if (item.kind === "session" && ++sessionEntities > MAX_NAVIGATION_SESSION_ENTITIES)
      throw schemaError("resource graph");
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
      if (ownerEntity.kind === "session" && item.children.length !== 0) throw schemaError("resource graph");
      if (item.children.length > NAVIGATION_SECTION_LIMIT) throw schemaError("resource graph");
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
  // Build the maps decode validates, rejecting a duplicate key outright.
  // validateGraphForResource below runs each entity and container validator
  // exactly once; validating here too made a snapshot read run them twice
  // before merge's third pass (#2478).
  const entities = new Map<string, NavigationGraphEntity>();
  for (const item of snapshot.entities) {
    if (!isRecord(item) || typeof item.key !== "string" || entities.has(item.key)) throw schemaError("entity schema");
    entities.set(item.key, item as NavigationGraphEntity);
  }
  const containers = new Map<string, NavigationGraphContainer>();
  for (const item of snapshot.containers) {
    if (!isRecord(item) || typeof item.key !== "string" || containers.has(item.key))
      throw schemaError("container schema");
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

// The value-record keys of an entity a resource holds; validateEntity already
// refused, through entityIdentityForResource, a kind the resource cannot hold.
function entityValueKeys(key: ResourceKey, kind: string): ValueRecordKeys {
  if (kind === "session") return SESSION_KEYS;
  if (kind === "pin_section") return PIN_SECTION_KEYS;
  return key.kind === "project" ? PROJECT_ANCHOR_KEYS : PROJECT_KEYS;
}

function knownEntity(key: ResourceKey, item: NavigationEntityRecord): NavigationEntityRecord {
  return { ...item, value: dropUnknownKeys(item.value as Record<string, unknown>, entityValueKeys(key, item.kind)) };
}

// Paging metadata is exact (validateResourceMetadata refused any unknown key),
// so only a manifest's or a location's metadata can carry one to drop.
function knownMetadata(key: ResourceKey, metadata: unknown): unknown {
  if (key.kind === "manifest") return dropUnknownKeys(metadata as Record<string, unknown>, MANIFEST_KEYS);
  if (key.kind === "location") return dropUnknownKeys(metadata as Record<string, unknown>, LOCATION_KEYS);
  return metadata;
}

function knownSnapshot(key: ResourceKey, snapshot: NavigationSnapshot): NavigationSnapshot {
  return {
    ...snapshot,
    metadata: knownMetadata(key, snapshot.metadata),
    entities: snapshot.entities.map((item) => knownEntity(key, item)),
  };
}

function knownDelta(key: ResourceKey, delta: NavigationDelta): NavigationDelta {
  return {
    ...delta,
    ...(delta.metadata === undefined ? {} : { metadata: knownMetadata(key, delta.metadata) }),
    upsertedEntities: delta.upsertedEntities.map((item) => knownEntity(key, item)),
  };
}

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
    throw new Error("navigation protocol: invalid v3 response");
  if (wire.representation === "snapshot") {
    if (!exactKeys(wire, RESPONSE_SNAPSHOT_KEYS)) throw new Error("navigation protocol: invalid snapshot");
    const snapshot = wire.data as NavigationSnapshot;
    validateSnapshotForResource(key, current, snapshot);
    return { status: "snapshot", version: current, snapshot: knownSnapshot(key, snapshot) };
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
    return { status: "delta", version: current, base: wire.base, delta: knownDelta(key, wire.data) };
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

/** The normalized resource a decoded snapshot stands for, built by hand at
 * five call sites before this. */
export function snapshotResource(
  key: ResourceKey,
  decoded: Extract<DecodedNavigationResponse, { status: "snapshot" }>,
): NormalizedResource {
  return {
    key,
    graph: normalizedGraphFromSnapshot(decoded.snapshot),
    version: decoded.version,
    presence: "present",
  };
}

/** The rows a decoded snapshot renders as, for a one-shot reader with no
 * previous resource to reconcile against. */
export function materializeSnapshot(
  key: ResourceKey,
  decoded: Extract<DecodedNavigationResponse, { status: "snapshot" }>,
): MaterializedValue {
  return materializeNavigationResource(snapshotResource(key, decoded));
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
