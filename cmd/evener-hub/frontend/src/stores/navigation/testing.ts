import type {
  NavigationCapability,
  NavigationManifest,
  NavigationReadParams,
  NavigationReadResponse,
  NavigationSessionSummary,
} from "../../protocol/types.gen";
import type { NavigationResponse, ResourceKey } from "./types";
import { navigationOwnedContainerKey, navigationRootContainerKey, navigationViewScope } from "./types";
export const capability = (generationId = "generation_test", version = 1): NavigationCapability => ({
  version,
  generationId,
  sequence: 0,
  readVersions: [2],
});
export const response = <T>(
  data: T,
  generationID = "generation_test",
  revision = 1,
  etag = '"test"',
): NavigationResponse<T> => ({ status: 200, generationID, revision, etag, data });
export const manifest = (overrides: Partial<NavigationManifest> = {}): NavigationManifest => ({
  generation_id: "generation_test",
  revision: 1,
  sources: [],
  attentionSummary: { needsYou: 0, error: 0, working: 0 },
  sections: { live: { count: 0 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
  catalogs: { projects: { count: 0 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
  ...overrides,
});
export const key = (_kind: ResourceKey["kind"]): ResourceKey => ({ kind: "manifest" }) as ResourceKey;

/** Complete a session fixture with the defaults the store validators require. */
export const completeSession = (value: Record<string, unknown>): Record<string, unknown> => ({
  host_id: "local",
  session_id: String(value.ref ?? "session"),
  title: String(value.ref ?? "Session"),
  project: "",
  state: "idle",
  kind: "session",
  live: false,
  ...value,
  children: Array.isArray(value.children)
    ? value.children.map((child) => completeSession(child as Record<string, unknown>))
    : [],
});

/** Complete a v1-shaped body with generation metadata and session defaults. */
export const completeBody = (data: unknown, revision: number, gen: string): Record<string, unknown> => {
  if (!data || typeof data !== "object" || Array.isArray(data)) return data as Record<string, unknown>;
  const body: Record<string, unknown> = { ...(data as Record<string, unknown>), generation_id: gen, revision };
  if (Array.isArray(body.sessions)) {
    body.sessions = body.sessions.map((item) => completeSession(item as Record<string, unknown>));
    body.truncated ??= false;
  }
  if (body.session && typeof body.session === "object") {
    body.session = completeSession(body.session as Record<string, unknown>);
    body.ref ??= (body.session as Record<string, unknown>).ref;
    body.top_level_ref ??= body.ref;
    body.top_level ??= true;
  }
  if (Array.isArray(body.pin_sections))
    body.pin_sections = body.pin_sections.map((item) => ({ name: String(item.id), ...item }));
  if (Array.isArray(body.projects))
    body.projects = body.projects.map((item) => ({ name: String(item.key), session_count: 0, ...item }));
  for (const tier of ["current", "recent", "archived"] as const) {
    const value = body[tier];
    if (!value || typeof value !== "object" || Array.isArray(value)) continue;
    const typed = value as Record<string, unknown>;
    body[tier] = {
      ...typed,
      sessions: Array.isArray(typed.sessions)
        ? typed.sessions.map((item) => completeSession(item as Record<string, unknown>))
        : [],
    };
    body.truncated ??= false;
  }
  return body;
};

// Test mirrors of the store's page-limit defaults (store.ts PAGE_LIMIT and
// CATALOG_LIMIT): fixtures must agree with the codec's effective limits.
const SECTION_LIMIT = 50;
const CATALOG_LIMIT = 100;

const paged = (params: NavigationReadParams, limit: number): { offset: number; limit: number } => ({
  offset: params.offset ?? 0,
  limit: params.limit ?? limit,
});

const ROOT_SLOT: Record<ResourceKey["kind"], string | undefined> = {
  manifest: "manifest",
  section: "sessions",
  pin_catalog: "pin_sections",
  pin_section: "sessions",
  catalog: "projects",
  project: undefined,
  project_page: "sessions",
  location: "session",
};

export const paramsToResourceKey = (params: NavigationReadParams): ResourceKey => {
  if (params.resource === "manifest") return { kind: "manifest" };
  if (params.resource === "section")
    return {
      kind: "section",
      section: params.section as "live" | "needs_you",
      ...paged(params, SECTION_LIMIT),
    };
  if (params.resource === "pin_catalog") return { kind: "pin_catalog", ...paged(params, CATALOG_LIMIT) };
  if (params.resource === "pin_section")
    return {
      kind: "pin_section",
      sectionId: params.sectionId as string,
      ...paged(params, SECTION_LIMIT),
    };
  if (params.resource === "catalog")
    return {
      kind: "catalog",
      catalog: params.catalog as "projects" | "archived_projects" | "test_runs",
      ...paged(params, CATALOG_LIMIT),
    };
  if (params.resource === "project") return { kind: "project", projectKey: params.projectKey as string };
  if (params.resource === "project_page")
    return {
      kind: "project_page",
      projectKey: params.projectKey as string,
      tier: params.tier as "current" | "recent" | "archived",
      ...paged(params, SECTION_LIMIT),
    };
  return { kind: "location", ref: params.ref as string };
};

// wireV2 converts a v1-shaped body into a valid v2 snapshot response for the
// requesting key, mirroring the server's normalization: nested session trees
// are flattened into entities plus owned children containers, and resource
// metadata carries exactly the keys the codec validates.
export const wireV2 = (
  params: NavigationReadParams,
  data: unknown,
  etag = '"one"',
  revision = 1,
  gen = "generation_test",
): NavigationReadResponse => {
  const key = paramsToResourceKey(params);
  const body = completeBody(data, revision, gen);
  let counter = 0;
  const entityKey = () => `${navigationViewScope(key)}/entity/${String(++counter).padStart(64, "0")}`;
  const entities: Array<{ key: string; kind: string; value: unknown }> = [];
  const containers: Array<{ key: string; owner: unknown; children: string[] }> = [];
  const addSessionTree = (session: Record<string, unknown>): string => {
    const key = entityKey();
    const { children, row_id: _rowId, ...value } = session;
    entities.push({ key, kind: "session", value: { ...value, children: [] } });
    const childKeys = Array.isArray(children)
      ? (children as Record<string, unknown>[]).map((child) => addSessionTree(child))
      : [];
    containers.push({
      key: navigationOwnedContainerKey(key, "children"),
      owner: { kind: "entity", entityKey: key, slot: "children" },
      children: childKeys,
    });
    return key;
  };
  const offset = typeof params.offset === "number" ? params.offset : 0;
  const limit =
    typeof params.limit === "number"
      ? params.limit
      : key.kind === "catalog" || key.kind === "pin_catalog"
        ? CATALOG_LIMIT
        : SECTION_LIMIT;
  let metadata: Record<string, unknown>;
  const rootChildren: string[] = [];
  const rootSlot = ROOT_SLOT[key.kind];
  if (key.kind === "manifest") {
    metadata = body;
  } else if (key.kind === "section" || key.kind === "pin_section" || key.kind === "project_page") {
    const sessions = Array.isArray(body.sessions) ? (body.sessions as Record<string, unknown>[]) : [];
    rootChildren.push(...sessions.map((session) => addSessionTree(session)));
    metadata = {
      generation_id: gen,
      revision,
      ...(key.kind === "project_page" ? { key: params.projectKey, tier: params.tier } : {}),
      offset,
      limit,
      remaining: typeof body.remaining === "number" ? body.remaining : 0,
      truncated: body.truncated === true,
    };
  } else if (key.kind === "pin_catalog") {
    const sections = Array.isArray(body.pin_sections) ? (body.pin_sections as Record<string, unknown>[]) : [];
    for (const section of sections) {
      const k = entityKey();
      entities.push({
        key: k,
        kind: "pin_section",
        value: { id: section.id, name: section.name, count: section.count },
      });
      rootChildren.push(k);
    }
    metadata = {
      generation_id: gen,
      revision,
      offset,
      limit,
      remaining: typeof body.remaining === "number" ? body.remaining : 0,
    };
  } else if (key.kind === "catalog") {
    const projects = Array.isArray(body.projects) ? (body.projects as Record<string, unknown>[]) : [];
    for (const project of projects) {
      const k = entityKey();
      const { children: _ignored, ...value } = project;
      entities.push({ key: k, kind: "project", value });
      rootChildren.push(k);
    }
    metadata = {
      generation_id: gen,
      revision,
      offset,
      limit,
      remaining: typeof body.remaining === "number" ? body.remaining : 0,
    };
  } else if (key.kind === "project") {
    const anchor = entityKey();
    entities.push({ key: anchor, kind: "project", value: { key: params.projectKey } });
    for (const tier of ["current", "recent", "archived"] as const) {
      const tierBody = body[tier] as { sessions?: Record<string, unknown>[] } | undefined;
      const tierSessions = Array.isArray(tierBody?.sessions) ? tierBody.sessions : [];
      const childKeys = tierSessions.map((session) => addSessionTree(session));
      containers.push({
        key: navigationOwnedContainerKey(anchor, tier),
        owner: { kind: "entity", entityKey: anchor, slot: tier },
        children: childKeys,
      });
    }
    metadata = {
      generation_id: gen,
      revision,
      key: params.projectKey,
      current_remaining: 0,
      recent_remaining: 0,
      archived_remaining: 0,
      truncated: body.truncated === true,
    };
  } else {
    const session = body.session as Record<string, unknown> | undefined;
    if (session) rootChildren.push(addSessionTree(session));
    metadata = {
      generation_id: gen,
      revision,
      ref: params.ref,
      top_level_ref: (body.top_level_ref as string) ?? (params.ref as string),
      top_level: true,
    };
  }
  if (rootSlot) {
    containers.push({
      key: navigationRootContainerKey(key, rootSlot),
      owner: { kind: "resource_root", slot: rootSlot },
      children: rootChildren,
    });
  }
  return {
    status: "ok",
    representation: "snapshot",
    generationId: gen,
    revision,
    etag,
    data: { metadata, entities, containers },
  } as NavigationReadResponse;
};

/** Build a NavigationSessionSummary fixture. */
export function sessionSummary(overrides: Partial<NavigationSessionSummary> = {}): NavigationSessionSummary {
  return {
    ref: "local:test",
    host_id: "local",
    session_id: "test",
    title: "test session",
    project: "test",
    state: "idle",
    kind: "session",
    live: false,
    children: [],
    ...overrides,
  };
}

/** JSON response helper for fetch mocks. */
export function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), {
    status,
    headers: { "content-type": "application/json", etag: '"test"' },
  });
}
