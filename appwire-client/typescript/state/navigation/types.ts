// The navigation resource vocabulary both apps share: resource keys and their
// view scopes, container key builders, page offsets, the per-resource state
// and response shapes, and the classifiers for a hub that has no navigation
// to offer or a base the hub no longer recognizes.
import { WireError } from "../../errors";
import type { NavigationReadBase, NavigationReadParams } from "../../types.gen";
import type { DecodedNavigationResponse, NormalizedResource } from "./codec";

const NAVIGATION_UNAVAILABLE_CODE = -32014;

export class NavigationBaseInvalidError extends Error {
  constructor(cause?: unknown) {
    super("navigation protocol: invalid installed base", { cause });
  }
}

export function isNavigationUnavailable(error: unknown): boolean {
  return (
    error instanceof WireError &&
    error.code === NAVIGATION_UNAVAILABLE_CODE &&
    error.evenerErrorInfo === "actionUnavailable"
  );
}

export type ResourceKey =
  | { kind: "manifest" }
  | { kind: "section"; section: "live" | "needs_you"; offset: number; limit: number }
  | { kind: "pin_catalog"; offset: number; limit: number }
  | { kind: "pin_section"; sectionId: string; offset: number; limit: number }
  | { kind: "catalog"; catalog: "projects" | "archived_projects" | "test_runs"; offset: number; limit: number }
  | { kind: "project"; projectKey: string }
  | { kind: "project_page"; projectKey: string; tier: "current" | "recent" | "archived"; offset: number; limit: number }
  | { kind: "location"; ref: string };

function rawBase64URL(value: string): string {
  let binary = "";
  for (const byte of new TextEncoder().encode(value)) binary += String.fromCharCode(byte);
  return btoa(binary).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/, "");
}

/** The hub's page maxima, one per resource family. A read that omits `limit`
 * is served the maximum for its kind rather than some smaller default:
 * `navigationReadPage` in `cmd/evener-hub/app_navigation.go` starts from the
 * maximum and only narrows it when the caller names one, and the two maxima
 * are `maxNavigationSectionRows` and `maxNavigationCatalogRows` in
 * `cmd/evener-hub/navigation_projection.go`. A client that assumes a
 * different default builds a resource key for a page the hub never served. */
export const NAVIGATION_SECTION_LIMIT = 50;
export const NAVIGATION_CATALOG_LIMIT = 100;

function canonicalNavigationLimit(limit: number, maximum: number): number {
  return limit === 0 || limit > maximum ? maximum : limit;
}

export function canonicalResourceKey(key: ResourceKey): ResourceKey {
  if (key.kind !== "location" || key.ref.includes(":")) return key;
  return { kind: "location", ref: `local:${key.ref}` };
}

/** The resource a read's parameters identify. The inverse of the params a
 * store builds from a key, and the one place a client turns a request it is
 * about to send - or a target it has resolved to a read - into the key the
 * codec validates the response against. An omitted `offset` is the first
 * page and an omitted `limit` is the hub's maximum for that kind. */
export function navigationParamsToResourceKey(params: NavigationReadParams): ResourceKey {
  const offset = params.offset ?? 0;
  const paged = (maximum: number) => ({ offset, limit: params.limit ?? maximum });
  // The wire type generates `resource: string`, wider than ResourceKey["kind"]:
  // the cast plus the never-typed default below is what makes an unhandled
  // kind (a real protocol addition, or a typo) a build failure here instead
  // of a silent fallback to manifest.
  const resource = params.resource as ResourceKey["kind"];
  switch (resource) {
    case "manifest":
      return { kind: "manifest" };
    case "section":
      return { kind: "section", section: params.section as "live" | "needs_you", ...paged(NAVIGATION_SECTION_LIMIT) };
    case "pin_catalog":
      return { kind: "pin_catalog", ...paged(NAVIGATION_CATALOG_LIMIT) };
    case "pin_section":
      return { kind: "pin_section", sectionId: params.sectionId as string, ...paged(NAVIGATION_SECTION_LIMIT) };
    case "catalog":
      return {
        kind: "catalog",
        catalog: params.catalog as "projects" | "archived_projects" | "test_runs",
        ...paged(NAVIGATION_CATALOG_LIMIT),
      };
    case "project":
      return { kind: "project", projectKey: params.projectKey as string };
    case "project_page":
      return {
        kind: "project_page",
        projectKey: params.projectKey as string,
        tier: params.tier as "current" | "recent" | "archived",
        ...paged(NAVIGATION_SECTION_LIMIT),
      };
    case "location":
      return { kind: "location", ref: params.ref as string };
    default: {
      const exhaustive: never = resource;
      throw new Error(`unknown navigation resource: ${String(exhaustive)}`);
    }
  }
}

export function navigationViewScope(key: ResourceKey): string {
  let kind: string = key.kind;
  let id = "";
  let sectionID = "";
  let projectKey = "";
  let tier = "";
  let offset = 0;
  let limit = 0;
  switch (key.kind) {
    case "section":
      kind = key.section;
      offset = key.offset;
      limit = canonicalNavigationLimit(key.limit, NAVIGATION_SECTION_LIMIT);
      break;
    case "pin_catalog":
      offset = key.offset;
      limit = canonicalNavigationLimit(key.limit, NAVIGATION_CATALOG_LIMIT);
      break;
    case "pin_section":
      sectionID = key.sectionId;
      offset = key.offset;
      limit = canonicalNavigationLimit(key.limit, NAVIGATION_SECTION_LIMIT);
      break;
    case "catalog":
      kind = key.catalog;
      offset = key.offset;
      limit = canonicalNavigationLimit(key.limit, NAVIGATION_CATALOG_LIMIT);
      break;
    case "project":
      projectKey = key.projectKey;
      break;
    case "project_page":
      projectKey = key.projectKey;
      tier = key.tier;
      offset = key.offset;
      limit = canonicalNavigationLimit(key.limit, NAVIGATION_SECTION_LIMIT);
      break;
    case "location":
      id = key.ref;
      break;
  }
  return `nav2/${kind}/${rawBase64URL(id)}/${rawBase64URL(sectionID)}/${rawBase64URL(projectKey)}/${rawBase64URL(tier)}/${offset}/${limit}`;
}

export function navigationRootContainerKey(key: ResourceKey, slot: string): string {
  return `${navigationViewScope(key)}/root/${slot}`;
}

export function navigationOwnedContainerKey(entityKey: string, slot: string): string {
  return `${entityKey}/${slot}`;
}

export function nextNavigationOffset(offset: number, returnedTopLevelRows: number): number {
  return offset + returnedTopLevelRows;
}

export interface ResourceState<T = unknown> {
  readonly key: ResourceKey;
  readonly data: T | null;
  readonly loadedRevision: number | null;
  readonly targetRevision: number | null;
  readonly forceToken: number;
  readonly etag: string | null;
  readonly loading: boolean;
  readonly stale: boolean;
  readonly error: unknown | null;
  readonly generationID: string;
  readonly version?: NavigationReadBase;
  readonly normalized?: NormalizedResource;
}

export interface NavigationResponse<T = unknown> {
  status: number;
  generationID: string;
  revision: number;
  etag: string;
  data?: T | null;
  v2?: DecodedNavigationResponse;
  normalized?: NormalizedResource;
}
export type NavigationRequest<T = unknown> = (
  signal: AbortSignal,
  base?: NavigationReadBase,
) => Promise<NavigationResponse<T>>;
export type ResourceListener = (state: ResourceState) => void;
export function keyID(key: ResourceKey): string {
  const fields: Record<string, unknown> = { kind: key.kind };
  for (const name of ["catalog", "limit", "offset", "projectKey", "ref", "section", "sectionId", "tier"]) {
    if (name in key) fields[name] = (key as Record<string, unknown>)[name];
  }
  return JSON.stringify(fields);
}
export function isProjectResource(key: ResourceKey): boolean {
  return key.kind === "project" || key.kind === "project_page";
}
/** True only when a resource carries settled (non-stale) normalized presence.
 * Generation resets retain the old normalized payload (including a `gone`
 * tombstone) while marking it stale and re-requesting for the new epoch, so
 * consumers must not treat a stale tombstone as authoritative: during
 * reconnect a previous-generation `gone` can precede the fresh response that
 * shows the resource present again. */
export function settledPresence(resource: ResourceState | null | undefined): string | undefined {
  if (!resource || resource.stale) return undefined;
  return resource.normalized?.presence;
}
/** True only when the resource is a settled (non-stale) `gone` tombstone. */
export function isSettledGone(resource: ResourceState | null | undefined): boolean {
  return settledPresence(resource) === "gone";
}
