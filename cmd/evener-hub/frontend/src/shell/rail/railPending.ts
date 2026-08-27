// Pure optimistic projection over resource-local rail slices. The authoritative
// navigation store remains untouched; an operation is removed only after the
// caller has reloaded the affected navigation resources.
import type { RailPinSection, RailProject, RailSession } from "./railNodes";

export interface RailPinSummary {
  id: string;
  name: string;
  member_count: number;
}

export type PendingOp =
  | { kind: "hideSession"; ref: string }
  | { kind: "hideProject"; key: string }
  | { kind: "sessionPin"; ref: string; source: RailSession; section: RailPinSummary }
  | { kind: "sessionUnpin"; ref: string }
  | { kind: "pinSectionRename"; id: string; name: string }
  | { kind: "pinSectionDelete"; id: string }
  | { kind: "projectFavorite"; key: string; value: boolean }
  | { kind: "sessionTitle"; ref: string; title: string };

export interface RailResources {
  live: readonly RailSession[];
  needsYou: readonly RailSession[];
  pinSections: readonly RailPinSection[];
  projects: readonly RailProject[];
  archivedProjects: readonly RailProject[];
  testRuns: readonly RailProject[];
  liveOverflow?: { remaining: number; offset: number; limit: number };
  needsYouOverflow?: { remaining: number; offset: number; limit: number };
  catalogOverflow?: Partial<
    Record<"projects" | "archived_projects" | "test_runs", { remaining: number; offset: number; limit: number }>
  >;
}

export interface RenderedProjectRows {
  projectKey: string;
  generationID: string;
  rows: readonly RailSession[];
}

export interface PinSourceIndexOptions {
  generationID: string;
  projectDetails: readonly RenderedProjectRows[];
}

export interface PinSourceIndex {
  rowsByIdentity: ReadonlyMap<string, RailSession>;
}

export interface ApplyPendingOptions {
  pinSources?: PinSourceIndex;
}

const NON_TOP_LEVEL_KINDS = new Set(["fork", "subagent", "cluster"]);
const identity = (node: Pick<RailSession, "row_id" | "ref">) => `${node.row_id}\0${node.ref}`;

function mapNodes(nodes: readonly RailSession[], fn: (node: RailSession) => RailSession | null): RailSession[] {
  const result: RailSession[] = [];
  for (const node of nodes) {
    const mapped = fn(node);
    if (mapped) result.push({ ...mapped, children: mapNodes(mapped.children, fn) });
  }
  return result;
}

function mapSessions(resources: RailResources, fn: (node: RailSession) => RailSession | null): RailResources {
  const projects = (items: readonly RailProject[]) =>
    items.map((project) => ({ ...project, sessions: mapNodes(project.sessions, fn) }));
  return {
    ...resources,
    live: mapNodes(resources.live, fn),
    needsYou: mapNodes(resources.needsYou, fn),
    pinSections: resources.pinSections.map((section) => ({
      ...section,
      sessions: mapNodes(section.sessions ?? [], fn),
    })),
    projects: projects(resources.projects),
    archivedProjects: projects(resources.archivedProjects),
    testRuns: projects(resources.testRuns),
  };
}

export function buildPinSourceIndex(resources: RailResources, options?: PinSourceIndexOptions): PinSourceIndex {
  const rowsByIdentity = new Map<string, RailSession>();
  const add = (rows: readonly RailSession[]) => {
    for (const row of rows) {
      if (!NON_TOP_LEVEL_KINDS.has(row.kind)) rowsByIdentity.set(identity(row), row);
    }
  };
  const addProjects = (projects: readonly RailProject[]) => {
    projects.forEach((project) => {
      add(project.sessions);
    });
  };
  add(resources.live);
  add(resources.needsYou);
  resources.pinSections.forEach((section) => {
    add(section.sessions ?? []);
  });
  addProjects(resources.projects);
  addProjects(resources.archivedProjects);
  addProjects(resources.testRuns);
  if (options) {
    const currentKeys = new Set(
      [...resources.projects, ...resources.archivedProjects, ...resources.testRuns].map((project) => project.key),
    );
    for (const detail of options.projectDetails) {
      if (detail.generationID === options.generationID && currentKeys.has(detail.projectKey)) add(detail.rows);
    }
  }
  return { rowsByIdentity };
}

function annotateSessions(resources: RailResources, ref: string, sectionID: string | undefined): RailResources {
  return mapSessions(resources, (node) => {
    if (node.ref !== ref) return node;
    if (sectionID === undefined) {
      const { pin_section_id: _pinSectionID, ...unpinned } = node;
      return unpinned;
    }
    return { ...node, pin_section_id: sectionID };
  });
}

function applySessionPin(
  resources: RailResources,
  op: Extract<PendingOp, { kind: "sessionPin" }>,
  pinSources: PinSourceIndex,
): RailResources {
  const source =
    op.source.ref === op.ref && !NON_TOP_LEVEL_KINDS.has(op.source.kind)
      ? pinSources.rowsByIdentity.get(identity(op.source))
      : undefined;
  if (!source) return resources;
  const annotated = annotateSessions(resources, op.ref, op.section.id);
  let found = false;
  const sections = annotated.pinSections.flatMap((section) => {
    const without = (section.sessions ?? []).filter((session) => session.ref !== op.ref);
    if (section.id !== op.section.id) return without.length ? [{ ...section, sessions: without }] : [];
    found = true;
    const index = (section.sessions ?? []).findIndex((session) => session.ref === op.ref);
    const sessions = [...without];
    sessions.splice(index < 0 ? sessions.length : Math.min(index, sessions.length), 0, {
      ...source,
      pin_section_id: op.section.id,
    });
    return [{ ...section, name: op.section.name, sessions }];
  });
  if (!found) sections.push({ ...op.section, sessions: [{ ...source, pin_section_id: op.section.id }] });
  return { ...annotated, pinSections: sections };
}

function applyOne(resources: RailResources, op: PendingOp, pinSources: PinSourceIndex): RailResources {
  switch (op.kind) {
    case "hideSession":
      return mapSessions(resources, (node) => (node.ref === op.ref ? null : node));
    case "hideProject": {
      const keep = (projects: readonly RailProject[]) => projects.filter((project) => project.key !== op.key);
      return {
        ...resources,
        projects: keep(resources.projects),
        archivedProjects: keep(resources.archivedProjects),
        testRuns: keep(resources.testRuns),
      };
    }
    case "sessionPin":
      return applySessionPin(resources, op, pinSources);
    case "sessionUnpin": {
      const annotated = annotateSessions(resources, op.ref, undefined);
      return {
        ...annotated,
        pinSections: annotated.pinSections
          .map((section) => ({ ...section, sessions: (section.sessions ?? []).filter((node) => node.ref !== op.ref) }))
          .filter((section) => (section.sessions ?? []).length > 0),
      };
    }
    case "pinSectionRename":
      return {
        ...resources,
        pinSections: resources.pinSections.map((s) => (s.id === op.id ? { ...s, name: op.name } : s)),
      };
    case "pinSectionDelete": {
      const deleted = resources.pinSections.find((section) => section.id === op.id);
      if (!deleted) return resources;
      const refs = new Set<string>();
      const collect = (rows: readonly RailSession[]) =>
        rows.forEach((row) => {
          refs.add(row.ref);
          collect(row.children);
        });
      collect(deleted.sessions ?? []);
      const remaining = { ...resources, pinSections: resources.pinSections.filter((section) => section.id !== op.id) };
      return mapSessions(remaining, (node) =>
        refs.has(node.ref)
          ? (() => {
              const { pin_section_id: _, ...clean } = node;
              return clean;
            })()
          : node,
      );
    }
    case "sessionTitle":
      return mapSessions(resources, (node) => (node.ref === op.ref ? { ...node, title: op.title } : node));
    case "projectFavorite": {
      const update = (projects: readonly RailProject[]) =>
        projects.map((project) => (project.key === op.key ? { ...project, favorite: op.value } : project));
      return {
        ...resources,
        projects: update(resources.projects),
        archivedProjects: update(resources.archivedProjects),
        testRuns: update(resources.testRuns),
      };
    }
  }
}

export function applyPending(
  resources: RailResources,
  operations: readonly PendingOp[],
  options: ApplyPendingOptions = {},
): RailResources {
  if (operations.length === 0) return resources;
  const pinSources = options.pinSources ?? buildPinSourceIndex(resources);
  return operations.reduce((current, operation) => applyOne(current, operation, pinSources), resources);
}
