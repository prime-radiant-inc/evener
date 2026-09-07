import {
  type ChangeEvent,
  type CSSProperties,
  memo,
  useCallback,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
} from "react";
import { sessionPanelPaneType } from "../../panes/sessionPanels";
import { errorText } from "../../protocol/errors";
import type {
  NavigationCatalogs,
  NavigationProjectPage,
  NavigationProjectResource,
  NavigationProjectSummary,
  NavigationSessionSummary,
} from "../../protocol/types.gen";
import { useConnectionStore } from "../../stores/connection";
import {
  nextNavigationOffset,
  relativeAge,
  selectAttentionSummary,
  selectPinSectionSummaries,
  selectPinSections,
  selectRailModel,
} from "../../stores/navigation/selectors";
import { buildShutdownConvergence } from "../../stores/navigation/shutdownConvergence";
import { navigationStore, useNavigationStore } from "../../stores/navigation/store";
import {
  isSettledGone,
  keyID,
  navigationOwnedContainerKey,
  navigationRootContainerKey,
  type ResourceKey,
  type ResourceState,
} from "../../stores/navigation/types";
import { threadsStore } from "../../stores/threads";
import {
  Badge,
  Button,
  Chevron,
  Dialog,
  EmptyState,
  IconButton,
  Input,
  Menu,
  Skeleton,
  Tooltip,
  Tree,
  type TreeRowInfo,
  useToasts,
} from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { useClient } from "../clientContext";
import { closePanesForDeletedSessions } from "../deletedSessionPanes";
import { navigate, paneToURL } from "../routing";
import { workspaceStore } from "../workspace";
import {
  assignSessionPin,
  deletePinSection,
  deleteProject,
  deleteSession,
  type NavigationMutationReceipt,
  renamePinSection,
  setArchived,
  setFavorite,
  unpinSession,
} from "./actions";
import styles from "./Rail.module.css";
import { RAIL_WIDTH_PROPERTY, RailResizeHandle } from "./RailResizeHandle";
import { RailRow, type RailRowActions } from "./RailRow";
import dialogStyles from "./railDialog.module.css";
import { loadExpansion, projectNodeExpansionKey, saveExpansion } from "./railExpansion";
import { GearIcon, SearchIcon, SidebarIcon } from "./railIcons";
import {
  archivedCount,
  archivedProjectNodes,
  archivedSessionGroups,
  catalogOverflowNode,
  type OverflowPage,
  type OverflowRailNode,
  overrideLookup,
  pinSectionDisclosureID,
  pinSectionOverflowNode,
  projectNodeIdForSessionRef,
  projectNodes,
  type RailNode,
  type RailPinSection,
  type RailProject,
  type RailSession,
  sectionOverflowNode,
  sessionNodes,
} from "./railNodes";
import { applyPending, buildPinSourceIndex, type PendingOp, type RailResources } from "./railPending";

const CLASS = {
  rail: requireClass(styles.rail, "Rail.module.css", "rail"),
  header: requireClass(styles.header, "Rail.module.css", "header"),
  brand: requireClass(styles.brand, "Rail.module.css", "brand"),
  brandIdentity: requireClass(styles.brandIdentity, "Rail.module.css", "brandIdentity"),
  newSession: requireClass(styles.newSession, "Rail.module.css", "newSession"),
  body: requireClass(styles.body, "Rail.module.css", "body"),
  parentScrollRail: requireClass(styles.parentScrollRail, "Rail.module.css", "parentScrollRail"),
  parentScrollBody: requireClass(styles.parentScrollBody, "Rail.module.css", "parentScrollBody"),
  section: requireClass(styles.section, "Rail.module.css", "section"),
  sectionTitle: requireClass(styles.sectionTitle, "Rail.module.css", "sectionTitle"),
  sectionDisclosure: requireClass(styles.sectionDisclosure, "Rail.module.css", "sectionDisclosure"),
  sectionHeadingRow: requireClass(styles.sectionHeadingRow, "Rail.module.css", "sectionHeadingRow"),
  sectionHeadingAction: requireClass(styles.sectionHeadingAction, "Rail.module.css", "sectionHeadingAction"),
  dialogField: requireClass(dialogStyles.dialogField, "railDialog.module.css", "dialogField"),
  dialogActions: requireClass(dialogStyles.dialogActions, "railDialog.module.css", "dialogActions"),
  pickerError: requireClass(dialogStyles.pickerError, "railDialog.module.css", "pickerError"),
  srOnly: requireClass(styles.srOnly, "Rail.module.css", "srOnly"),
};

const ARCHIVED_SECTION_KEY = "section:archived";
type CatalogKind = keyof NavigationCatalogs;
const sessionModelCache = new WeakMap<object, Map<string, RailSession>>();
type ProjectPageDependency = Readonly<{
  id: string;
  tier: "current" | "recent" | "archived";
  graphOrData: object | null;
}>;
type ProjectModelCacheEntry = Readonly<{
  mode: "compatibility" | "graph";
  root: object | null;
  pages: readonly ProjectPageDependency[];
  compatibilityError?: string;
  result: RailProject;
}>;
const projectModelCache = new WeakMap<object, ProjectModelCacheEntry>();
const archivedProjectModelCache = new WeakMap<object, RailProject>();

interface RailSectionProps {
  title: string;
  nodes: RailNode[];
  onToggle: (node: RailNode) => void;
  onActivate: (node: RailNode) => void;
  actions: RailRowActions;
  projectRetryCallback: (key: string) => () => void;
}
interface NavigationRailRowProps {
  node: RailNode;
  info: TreeRowInfo;
  actions: RailRowActions;
  projectRetryCallback: (key: string) => () => void;
}
function ProjectNavigationRailRow({
  node,
  info,
  actions,
  projectRetryCallback,
}: NavigationRailRowProps & {
  node: Extract<RailNode, { kind: "project" }>;
}) {
  const projectKey = node.project.key;
  const resourceError = useNavigationStore((state) => {
    const error = state.resources.get(keyID({ kind: "project", projectKey }))?.error;
    return error ? errorText(error) : undefined;
  });
  return (
    <RailRow
      node={node}
      info={info}
      actions={actions}
      resourceError={resourceError}
      retry={resourceError ? projectRetryCallback(projectKey) : undefined}
    />
  );
}
const NavigationRailRow = memo(function NavigationRailRow({
  node,
  info,
  actions,
  projectRetryCallback,
}: NavigationRailRowProps) {
  return node.kind === "project" ? (
    <ProjectNavigationRailRow node={node} info={info} actions={actions} projectRetryCallback={projectRetryCallback} />
  ) : (
    <RailRow node={node} info={info} actions={actions} />
  );
});
function renderRailRow(actions: RailRowActions, projectRetryCallback: (key: string) => () => void) {
  return (node: RailNode, info: TreeRowInfo) => (
    <NavigationRailRow node={node} info={info} actions={actions} projectRetryCallback={projectRetryCallback} />
  );
}
function isPassiveRailNode(node: RailNode): boolean {
  return node.kind === "loading" || node.kind === "job";
}
function RailSection({ title, nodes, onToggle, onActivate, actions, projectRetryCallback }: RailSectionProps) {
  const renderRow = useMemo(() => renderRailRow(actions, projectRetryCallback), [actions, projectRetryCallback]);
  if (nodes.length === 0) return null;
  return (
    <section className={CLASS.section}>
      <h3 className={CLASS.sectionTitle}>{title}</h3>
      <Tree nodes={nodes} onToggle={onToggle} onActivate={onActivate} renderRow={renderRow} />
    </section>
  );
}
interface PinnedRailSectionProps extends Omit<RailSectionProps, "title" | "nodes"> {
  section: RailPinSection;
  open: boolean;
  onToggleOpen: () => void;
  onRename: () => void;
  onDelete: () => void;
  isExpanded: ReturnType<typeof overrideLookup>;
  projectRetryCallback: (key: string) => () => void;
}
function PinnedRailSection({
  section,
  open,
  onToggleOpen,
  onRename,
  onDelete,
  isExpanded,
  onToggle,
  onActivate,
  actions,
  projectRetryCallback,
}: PinnedRailSectionProps) {
  const renderRow = useMemo(() => renderRailRow(actions, projectRetryCallback), [actions, projectRetryCallback]);
  return (
    <section className={CLASS.section}>
      <div className={CLASS.sectionHeadingRow}>
        <h3 className={CLASS.sectionTitle} aria-label={section.name}>
          <button type="button" className={CLASS.sectionDisclosure} aria-expanded={open} onClick={onToggleOpen}>
            <Chevron direction={open ? "down" : "right"} /> {section.name}
          </button>
        </h3>
        <div className={CLASS.sectionHeadingAction}>
          <Menu
            variant="quiet"
            trigger={
              <>
                <span aria-hidden="true">⋯</span>
                <span className={CLASS.srOnly}>{`Actions for ${section.name}`}</span>
              </>
            }
            items={[
              { id: "rename", label: "Rename", onSelect: onRename },
              { id: "delete", label: "Delete", onSelect: onDelete },
            ]}
          />
        </div>
      </div>
      {open && (
        <Tree
          nodes={[
            ...sessionNodes(section.sessions ?? [], isExpanded),
            ...pinSectionOverflowNode(
              `pinsection:${section.id}`,
              section.id,
              section.remaining ?? 0,
              section.offset ?? section.sessions.length,
              section.limit ?? 50,
            ),
          ]}
          onToggle={onToggle}
          onActivate={onActivate}
          renderRow={renderRow}
        />
      )}
    </section>
  );
}
interface ArchivedSectionProps extends Omit<RailSectionProps, "title"> {
  count: number;
  open: boolean;
  onToggleOpen: () => void;
  projectRetryCallback: (key: string) => () => void;
}
function ArchivedSection({
  count,
  open,
  onToggleOpen,
  nodes,
  onToggle,
  onActivate,
  actions,
  projectRetryCallback,
}: ArchivedSectionProps) {
  const renderRow = useMemo(() => renderRailRow(actions, projectRetryCallback), [actions, projectRetryCallback]);
  return (
    <section className={CLASS.section}>
      <button type="button" className={CLASS.sectionDisclosure} aria-expanded={open} onClick={onToggleOpen}>
        <Chevron direction={open ? "down" : "right"} /> {`Archived sessions (${count})`}
      </button>
      {open && <Tree nodes={nodes} onToggle={onToggle} onActivate={onActivate} renderRow={renderRow} />}
    </section>
  );
}

export interface RailProps {
  onHide?: () => void;
  width?: number;
  onWidthChange?: (width: number) => void;
  revealTarget?: string | null;
  onRevealConsumed?: () => void;
  /** The normal rail owns its list scrolling. Mobile's Sheet can opt to own
   * the whole rail instead, so the rail grows with its content. */
  scrollOwner?: "rail" | "parent";
}
interface RevealRequestGuard {
  target: string;
  token: symbol;
}

function summarySession(
  summary: NavigationSessionSummary,
  scope: string,
  tier?: string,
  pinSectionID?: string,
  projectKey?: string,
): RailSession {
  const context = `${scope}\0${tier ?? ""}\0${pinSectionID ?? ""}\0${projectKey ?? ""}`;
  const cached = sessionModelCache.get(summary as object)?.get(context);
  if (cached) return cached;
  const children = summary.children.map((child) => summarySession(child, scope, tier, pinSectionID, projectKey));
  const result = {
    ...summary,
    row_id: `navigation:${scope}:${summary.ref}`,
    tier,
    pin_section_id: pinSectionID,
    project_key: projectKey,
    age: relativeAge(summary.updated_at),
    children,
  };
  let entries = sessionModelCache.get(summary as object);
  if (!entries) {
    entries = new Map();
    sessionModelCache.set(summary as object, entries);
  }
  entries.set(context, result);
  return result;
}
function sessions(
  summaries: readonly NavigationSessionSummary[],
  scope: string,
  tier?: string,
  pinSectionID?: string,
  projectKey?: string,
): RailSession[] {
  return summaries.map((summary) => summarySession(summary, scope, tier, pinSectionID, projectKey));
}
function dedupeSessions(rows: readonly RailSession[]): RailSession[] {
  const seen = new Set<string>();
  return rows.filter((row) => {
    if (seen.has(row.ref)) return false;
    seen.add(row.ref);
    return true;
  });
}
function resourceState(
  state: ReturnType<typeof navigationStore.getState>,
  key: ResourceKey,
): ResourceState | undefined {
  return state.resources.get(keyID(key));
}
function resourceData<T>(state: ReturnType<typeof navigationStore.getState>, key: ResourceKey): T | null {
  const resource = resourceState(state, key);
  if (resource?.normalized?.presence === "gone") return null;
  return (resource?.data as T | undefined) ?? null;
}
function returnedRootRows(resource: ResourceState, slot: string, field: string): number {
  const normalized = resource.normalized;
  if (normalized)
    return normalized.graph.containers.get(navigationRootContainerKey(resource.key, slot))?.children.length ?? 0;
  const data = resource.data as Record<string, unknown> | null;
  const rows = data?.[field];
  return Array.isArray(rows) ? rows.length : 0;
}
const PROJECT_TIERS = ["current", "recent", "archived"] as const;
function projectPageStates(
  pages: ReadonlyMap<string, ResourceState>,
  projectKey: string,
): Array<ResourceState & { key: Extract<ResourceKey, { kind: "project_page" }> }> {
  const tierOrder = { current: 0, recent: 1, archived: 2 } as const;
  return [...pages.values()]
    .filter(
      (state): state is ResourceState & { key: Extract<ResourceKey, { kind: "project_page" }> } =>
        state.key.kind === "project_page" &&
        state.key.projectKey === projectKey &&
        state.data !== null &&
        state.normalized?.presence !== "gone",
    )
    .sort(
      (a, b) =>
        tierOrder[a.key.tier] - tierOrder[b.key.tier] ||
        a.key.offset - b.key.offset ||
        a.key.limit - b.key.limit ||
        keyID(a.key).localeCompare(keyID(b.key)),
    );
}
function projectPageDependencies(states: readonly ResourceState[]): ProjectPageDependency[] {
  return states.flatMap((state) => {
    if (state.key.kind !== "project_page") return [];
    const graphOrData = state.normalized?.graph ?? (typeof state.data === "object" ? state.data : null);
    return [{ id: keyID(state.key), tier: state.key.tier, graphOrData }];
  });
}
function sameProjectPageDependencies(
  left: readonly ProjectPageDependency[],
  right: readonly ProjectPageDependency[],
): boolean {
  return (
    left.length === right.length &&
    left.every(
      (dependency, index) =>
        dependency.id === right[index]?.id &&
        dependency.tier === right[index]?.tier &&
        dependency.graphOrData === right[index]?.graphOrData,
    )
  );
}
function cachedProject(
  summary: NavigationProjectSummary,
  mode: ProjectModelCacheEntry["mode"],
  root: object | null,
  pages: readonly ProjectPageDependency[],
  compatibilityError?: string,
): RailProject | undefined {
  const cached = projectModelCache.get(summary as object);
  return cached?.mode === mode &&
    cached.root === root &&
    sameProjectPageDependencies(cached.pages, pages) &&
    (mode === "graph" || cached.compatibilityError === compatibilityError)
    ? cached.result
    : undefined;
}
function cacheProject(
  summary: NavigationProjectSummary,
  mode: ProjectModelCacheEntry["mode"],
  root: object | null,
  pages: readonly ProjectPageDependency[],
  result: RailProject,
  compatibilityError?: string,
): RailProject {
  projectModelCache.set(summary as object, { mode, root, pages, compatibilityError, result });
  return result;
}
interface LoadedSection {
  sessions: RailSession[];
  remaining: number;
  offset: number;
  limit: number;
}
function loadedSection(
  state: ReturnType<typeof navigationStore.getState>,
  section: "live" | "needs_you",
): LoadedSection {
  const pages = [...state.resources.values()]
    .filter((resource) => resource.key.kind === "section" && resource.key.section === section && resource.data !== null)
    .sort((a, b) =>
      a.key.kind === "section" && b.key.kind === "section"
        ? a.key.offset - b.key.offset || a.key.limit - b.key.limit
        : 0,
    );
  const seen = new Set<string>();
  const rows = pages.flatMap((resource) => {
    const normalized = resource.normalized;
    if (normalized) {
      const model = selectRailModel(normalized);
      const root = normalized.graph.containers.get(navigationRootContainerKey(resource.key, "sessions"));
      return (root?.children ?? []).flatMap((entityKey) => {
        const session = model.sessions.get(entityKey);
        if (!session || seen.has(session.ref)) return [];
        seen.add(session.ref);
        return [session];
      });
    }
    return (resource.data as { sessions: NavigationSessionSummary[] }).sessions.flatMap((summary) => {
      if (seen.has(summary.ref)) return [];
      seen.add(summary.ref);
      return [summarySession(summary, section)];
    });
  });
  const last = pages.at(-1);
  const data = last?.data as { remaining?: number } | null;
  const pageKey = last?.key.kind === "section" ? last.key : { offset: 0, limit: 50 };
  return {
    sessions: rows,
    remaining: data?.remaining ?? 0,
    offset: nextNavigationOffset(pageKey.offset, last ? returnedRootRows(last, "sessions", "sessions") : 0),
    limit: pageKey.limit,
  };
}
function projectFromSummary(
  summary: NavigationProjectSummary,
  root: NavigationProjectResource | null,
  rootError: string | undefined,
  pages: ReadonlyMap<string, ResourceState>,
): RailProject {
  const rootObject = root as object | null;
  const allPageStates = projectPageStates(pages, summary.key);
  const pageDependencies = projectPageDependencies(allPageStates);
  const cached = cachedProject(summary, "compatibility", rootObject, pageDependencies, rootError);
  if (cached) return cached;
  const all: RailSession[] = [];
  const more: Partial<Record<"current" | "recent" | "archived", number>> = {};
  const nextOffsets: Partial<Record<"current" | "recent" | "archived", number>> = {};
  for (const tier of PROJECT_TIERS) {
    const base = root?.[tier];
    const pageStates = allPageStates.filter((state) => state.key.tier === tier);
    const rows = [...(base?.sessions ?? [])];
    let remaining = base?.remaining ?? summary[`more_${tier}`] ?? 0;
    for (const pageState of pageStates) {
      const page = pageState.data as NavigationProjectPage;
      for (const row of page.sessions) if (!rows.some((existing) => existing.ref === row.ref)) rows.push(row);
      remaining = Math.min(remaining, page.remaining);
    }
    const lastPage = pageStates.at(-1);
    nextOffsets[tier] =
      lastPage?.key.kind === "project_page"
        ? nextNavigationOffset(lastPage.key.offset, returnedRootRows(lastPage, "sessions", "sessions"))
        : rows.length;
    all.push(...sessions(rows, `project:${summary.key}:${tier}`, tier, undefined, summary.key));
    more[tier] = remaining;
  }
  const result = {
    ...summary,
    loaded: root !== null,
    resourceError: rootError,
    nextOffsets,
    sessions: all,
    more_current: more.current,
    more_recent: more.recent,
    more_archived: more.archived,
  };
  return cacheProject(summary, "compatibility", rootObject, pageDependencies, result, rootError);
}
function graphSessionsForResource(resource: ResourceState): RailSession[] {
  const normalized = resource.normalized;
  if (!normalized) return [];
  const model = selectRailModel(normalized);
  const root = normalized.graph.containers.get(navigationRootContainerKey(resource.key, "sessions"));
  return (root?.children ?? []).flatMap((entityKey) => {
    const session = model.sessions.get(entityKey);
    return session ? [session] : [];
  });
}
function projectFromGraph(
  summary: NavigationProjectSummary,
  resource: ResourceState,
  pages: ReadonlyMap<string, ResourceState>,
): RailProject | null {
  const normalized = resource.normalized;
  if (!normalized || normalized.presence === "gone") return null;
  const allPageStates = projectPageStates(pages, summary.key);
  const pageDependencies = projectPageDependencies(allPageStates);
  const cached = cachedProject(summary, "graph", normalized.graph as object, pageDependencies);
  if (cached) return cached;
  const projectEntity = [...normalized.graph.entities.values()].find(
    (entity) =>
      entity.kind === "project" &&
      entity.value !== null &&
      typeof entity.value === "object" &&
      (entity.value as Record<string, unknown>).key === summary.key,
  );
  if (!projectEntity) return null;
  const metadata = normalized.graph.metadata;
  const all: RailSession[] = [];
  const more: Partial<Record<"current" | "recent" | "archived", number>> = {};
  const nextOffsets: Partial<Record<"current" | "recent" | "archived", number>> = {};
  for (const tier of PROJECT_TIERS) {
    const container = normalized.graph.containers.get(navigationOwnedContainerKey(projectEntity.key, tier));
    const rootSessions = (container?.children ?? []).flatMap((entityKey) => {
      const session = selectRailModel(normalized).sessions.get(entityKey);
      return session ? [session] : [];
    });
    const pageStates = allPageStates.filter((page) => page.key.tier === tier);
    const seen = new Set(rootSessions.map((session) => session.ref));
    const sessions = [...rootSessions];
    for (const page of pageStates) {
      for (const session of graphSessionsForResource(page)) {
        if (seen.has(session.ref)) continue;
        seen.add(session.ref);
        sessions.push(session);
      }
    }
    all.push(...sessions);
    const lastPage = pageStates.at(-1);
    nextOffsets[tier] =
      lastPage?.key.kind === "project_page"
        ? nextNavigationOffset(lastPage.key.offset, returnedRootRows(lastPage, "sessions", "sessions"))
        : (container?.children.length ?? 0);
    const pageRemaining = pageStates.at(-1)?.data as { remaining?: number } | undefined;
    const metadataRemaining = metadata[`${tier}_remaining`];
    more[tier] = pageRemaining?.remaining ?? (typeof metadataRemaining === "number" ? metadataRemaining : 0);
  }
  return cacheProject(summary, "graph", normalized.graph as object, pageDependencies, {
    ...summary,
    loaded: true,
    sessions: all,
    nextOffsets,
    more_current: more.current,
    more_recent: more.recent,
    more_archived: more.archived,
  });
}
function asArchivedProject(project: RailProject): RailProject {
  if (project.is_archived === true) return project;
  const cached = archivedProjectModelCache.get(project as object);
  if (cached) return cached;
  const archived = { ...project, is_archived: true };
  archivedProjectModelCache.set(project as object, archived);
  return archived;
}
function projectsFor(state: ReturnType<typeof navigationStore.getState>, catalog: CatalogKind): RailProject[] {
  const output: RailProject[] = [];
  const catalogResources = [...state.resources.values()]
    .filter(
      (resource) =>
        resource.key.kind === "catalog" &&
        resource.key.catalog === catalog &&
        resource.data !== null &&
        resource.normalized?.presence !== "gone",
    )
    .sort((a, b) =>
      a.key.kind === "catalog" && b.key.kind === "catalog"
        ? a.key.offset - b.key.offset || a.key.limit - b.key.limit
        : 0,
    );
  for (const resource of catalogResources) {
    const normalizedCatalog = resource.normalized;
    const data = resource.data as { projects: NavigationProjectSummary[] };
    const summaries = normalizedCatalog
      ? (() => {
          const root = normalizedCatalog.graph.containers.get(navigationRootContainerKey(resource.key, "projects"));
          return (root?.children ?? []).flatMap((entityKey) => {
            const entity = normalizedCatalog.graph.entities.get(entityKey);
            if (entity?.kind !== "project" || !entity.value || typeof entity.value !== "object") return [];
            return [entity.value as NavigationProjectSummary];
          });
        })()
      : data.projects;
    for (const summary of summaries) {
      if (output.some((project) => project.key === summary.key)) continue;
      const rootState = state.resources.get(keyID({ kind: "project", projectKey: summary.key }));
      if (normalizedCatalog && rootState) {
        const graphProject = projectFromGraph(summary, rootState, state.resources);
        if (graphProject) {
          output.push(graphProject);
          continue;
        }
      }
      const root =
        rootState?.normalized?.presence === "gone"
          ? null
          : ((rootState?.data as NavigationProjectResource | null | undefined) ?? null);
      output.push(
        projectFromSummary(summary, root, rootState?.error ? errorText(rootState.error) : undefined, state.resources),
      );
    }
  }
  return output;
}
function catalogOverflowFor(
  state: ReturnType<typeof navigationStore.getState>,
  catalog: CatalogKind,
): { remaining: number; offset: number; limit: number } | undefined {
  const pages = [...state.resources.values()]
    .filter(
      (resource) =>
        resource.key.kind === "catalog" &&
        resource.key.catalog === catalog &&
        resource.data !== null &&
        resource.normalized?.presence !== "gone",
    )
    .sort((a, b) =>
      a.key.kind === "catalog" && b.key.kind === "catalog"
        ? a.key.offset - b.key.offset || a.key.limit - b.key.limit
        : 0,
    );
  const last = pages.at(-1);
  if (!last) return undefined;
  const remaining = (last.data as { remaining?: number } | null)?.remaining ?? 0;
  if (remaining <= 0) return undefined;
  const pageKey = last.key.kind === "catalog" ? last.key : { offset: 0, limit: 100 };
  return {
    remaining,
    offset: nextNavigationOffset(pageKey.offset, returnedRootRows(last, "projects", "projects")),
    limit: pageKey.limit,
  };
}
function railResources(state: ReturnType<typeof navigationStore.getState>): RailResources {
  const live = loadedSection(state, "live");
  const needsYou = loadedSection(state, "needs_you");
  const pinCatalog = [...state.resources.values()]
    .filter((resource) => resource.key.kind === "pin_catalog" && resource.data !== null)
    .sort((a, b) => (a.key.kind === "pin_catalog" && b.key.kind === "pin_catalog" ? a.key.offset - b.key.offset : 0))
    .flatMap(
      (resource) =>
        (resource.data as { pin_sections: Array<{ id: string; name: string; count: number }> }).pin_sections,
    );
  const pinCounts = new Map<string, number>();
  for (const descriptor of pinCatalog)
    if (!pinCounts.has(descriptor.id)) pinCounts.set(descriptor.id, descriptor.count);
  const pinSections = selectPinSections(state)
    .map((section) => {
      const pages = [...state.resources.values()]
        .filter(
          (resource) =>
            resource.key.kind === "pin_section" && resource.key.sectionId === section.id && resource.data !== null,
        )
        .sort((a, b) =>
          a.key.kind === "pin_section" && b.key.kind === "pin_section"
            ? a.key.offset - b.key.offset || a.key.limit - b.key.limit
            : 0,
        );
      const last = pages.at(-1);
      const pageKey = last?.key.kind === "pin_section" ? last.key : { offset: 0, limit: 50 };
      const remaining = (last?.data as { remaining?: number } | null)?.remaining ?? 0;
      const normalizedPages = pages.filter((resource) => resource.normalized);
      const graphSessions = normalizedPages.length
        ? dedupeSessions(normalizedPages.flatMap((resource) => graphSessionsForResource(resource)))
        : null;
      return {
        id: section.id,
        name: section.name,
        member_count: pinCounts.get(section.id) ?? section.sessions.length,
        remaining,
        offset: nextNavigationOffset(pageKey.offset, last ? returnedRootRows(last, "sessions", "sessions") : 0),
        limit: pageKey.limit,
        sessions:
          graphSessions ?? dedupeSessions(sessions(section.sessions, `pin:${section.id}`, undefined, section.id)),
      };
    })
    .filter((section) => section.sessions.length > 0);
  return {
    live: live.sessions,
    needsYou: needsYou.sessions,
    liveOverflow: { remaining: live.remaining, offset: live.offset, limit: live.limit },
    needsYouOverflow: { remaining: needsYou.remaining, offset: needsYou.offset, limit: needsYou.limit },
    pinSections,
    projects: projectsFor(state, "projects"),
    archivedProjects: projectsFor(state, "archived_projects").map(asArchivedProject),
    testRuns: projectsFor(state, "test_runs"),
    catalogOverflow: {
      projects: catalogOverflowFor(state, "projects"),
      archived_projects: catalogOverflowFor(state, "archived_projects"),
      test_runs: catalogOverflowFor(state, "test_runs"),
    },
  };
}
export const adaptNavigationResources = railResources;
function nonEmpty(resources: RailResources): boolean {
  return (
    resources.live.length > 0 ||
    resources.needsYou.length > 0 ||
    resources.pinSections.length > 0 ||
    resources.projects.length > 0 ||
    resources.archivedProjects.length > 0 ||
    resources.testRuns.length > 0
  );
}

async function convergeMutation(result: unknown): Promise<void> {
  if (!isNavigationMutationReceipt(result)) return;
  await navigationStore.getState().applyNavigationMutation(result.navigation);
}
function isNavigationMutationReceipt(result: unknown): result is NavigationMutationReceipt {
  return (
    !!result &&
    typeof result === "object" &&
    "navigation" in result &&
    !!result.navigation &&
    typeof result.navigation === "object" &&
    "generation_id" in result.navigation &&
    "targets" in result.navigation
  );
}

function NavigationRail({
  onHide,
  width,
  onWidthChange,
  revealTarget,
  onRevealConsumed,
  scrollOwner = "rail",
}: RailProps = {}) {
  const client = useClient();
  const navigationMode = useNavigationStore((state) => state.mode);
  const manifest = useNavigationStore((state) => state.manifest);
  const resourcesState = useNavigationStore((state) => state.resources);
  const expanded = useNavigationStore((state) => state.expanded);
  const attention = useNavigationStore((state) => selectAttentionSummary(state));
  const serverInfo = useConnectionStore((state) => state.serverInfo);
  const toasts = useToasts();
  const [expandedOverrides, setExpandedOverrides] = useState<ReadonlyMap<string, boolean>>(loadExpansion);
  const [sectionRenameTarget, setSectionRenameTarget] = useState<RailPinSection | null>(null);
  const [sectionRenameValue, setSectionRenameValue] = useState("");
  const [sectionRenameError, setSectionRenameError] = useState("");
  const [sectionRenameSubmitting, setSectionRenameSubmitting] = useState(false);
  const sectionRenameInputID = useId();
  const sectionRenameErrorID = useId();
  const sectionRenameSubmission = useRef<{ token: number; sectionID: string } | null>(null);
  const sectionRenameToken = useRef(0);
  const sectionDeleteRequestToken = useRef(0);
  const [sectionDeleteTarget, setSectionDeleteTarget] = useState<{
    section: RailPinSection;
    memberCount: number;
  } | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<RailProject | null>(null);
  const [pending, setPending] = useState<readonly PendingOp[]>([]);
  const bodyRef = useRef<HTMLDivElement>(null);
  const railRef = useRef<HTMLDivElement>(null);
  const overflowPagesInFlight = useRef(new Set<string>());
  const state = { ...navigationStore.getState(), resources: resourcesState, expanded };
  const base = useMemo(
    () => railResources({ ...navigationStore.getState(), resources: resourcesState }),
    [resourcesState],
  );
  const resources = useMemo(
    () => applyPending(base, pending, { pinSources: buildPinSourceIndex(base) }),
    [base, pending],
  );
  const isExpanded = useMemo(() => overrideLookup(expandedOverrides), [expandedOverrides]);
  const revealLookupInFlight = useRef<RevealRequestGuard | null>(null);
  const revealResourceRequests = useRef(new Map<string, RevealRequestGuard>());
  const revealCompletedTarget = useRef<string | null>(null);
  const revealRequestTarget = useRef<string | null>(null);

  useEffect(() => {
    if (revealRequestTarget.current === revealTarget) return;
    revealRequestTarget.current = revealTarget ?? null;
    revealLookupInFlight.current = null;
    revealResourceRequests.current.clear();
    revealCompletedTarget.current = null;
  }, [revealTarget]);

  const requestRevealResource = useCallback(
    (target: string, key: string, request: () => Promise<unknown> | undefined): void => {
      if (revealRequestTarget.current !== target || revealResourceRequests.current.has(key)) return;
      const guard: RevealRequestGuard = { target, token: Symbol(key) };
      revealResourceRequests.current.set(key, guard);
      let result: Promise<unknown> | undefined;
      try {
        result = request();
      } catch (error) {
        result = Promise.reject(error);
      }
      if (!result) {
        if (revealResourceRequests.current.get(key) === guard) revealResourceRequests.current.delete(key);
        return;
      }
      void result.catch(() => {
        if (revealRequestTarget.current !== target || revealResourceRequests.current.get(key) !== guard) return;
        revealResourceRequests.current.delete(key);
      });
    },
    [],
  );
  const consumeReveal = useCallback(() => {
    if (!revealTarget || revealCompletedTarget.current === revealTarget) return;
    revealCompletedTarget.current = revealTarget;
    onRevealConsumed?.();
  }, [revealTarget, onRevealConsumed]);

  useEffect(() => {
    if (navigationMode !== "v2") return;
    if (!manifest)
      void navigationStore
        .getState()
        .loadManifest()
        .catch(() => undefined);
  }, [navigationMode, manifest]);
  useEffect(
    () => () => {
      sectionRenameToken.current += 1;
      sectionRenameSubmission.current = null;
      sectionDeleteRequestToken.current += 1;
    },
    [],
  );

  const setExpanded = useCallback(
    (id: string, value: boolean) => {
      const next = new Map(expandedOverrides);
      next.set(id, value);
      setExpandedOverrides(next);
      saveExpansion(next);
    },
    [expandedOverrides],
  );
  const rootLoadsInFlight = useRef(new Set<string>());
  const rootGeneration = useRef("");
  const loadProjectRoot = useCallback((key: string) => {
    if (rootLoadsInFlight.current.has(key)) return;
    rootLoadsInFlight.current.add(key);
    void Promise.resolve(navigationStore.getState().loadProject(key))
      .catch(() => undefined)
      .finally(() => rootLoadsInFlight.current.delete(key));
  }, []);
  const currentLoadProjectRoot = useRef(loadProjectRoot);
  const projectRetryCallbacks = useRef(new Map<string, () => void>());
  const projectRetryCallback = useCallback((key: string): (() => void) => {
    const cached = projectRetryCallbacks.current.get(key);
    if (cached) return cached;
    const retry = () => {
      rootLoadsInFlight.current.delete(key);
      currentLoadProjectRoot.current(key);
    };
    projectRetryCallbacks.current.set(key, retry);
    return retry;
  }, []);
  useEffect(() => {
    currentLoadProjectRoot.current = loadProjectRoot;
    const ownedKeys = new Set(
      [...resources.projects, ...resources.archivedProjects, ...resources.testRuns].map((project) => project.key),
    );
    for (const key of projectRetryCallbacks.current.keys()) {
      if (!ownedKeys.has(key)) projectRetryCallbacks.current.delete(key);
    }
  }, [loadProjectRoot, resources]);
  useEffect(() => {
    if (navigationMode !== "v2") return;
    const generation = navigationStore.getState().clientGenerationID;
    if (generation !== rootGeneration.current) {
      rootLoadsInFlight.current.clear();
      rootGeneration.current = generation;
    }
    for (const project of [...resources.projects, ...resources.archivedProjects, ...resources.testRuns]) {
      const expanded = isExpanded(projectNodeExpansionKey(project.key), project.default_expanded ?? false);
      if (
        !expanded ||
        project.loaded === true ||
        project.resourceError !== undefined ||
        (project.session_count ?? 0) === 0 ||
        resourceState(navigationStore.getState(), { kind: "project", projectKey: project.key })?.normalized
          ?.presence === "gone" ||
        rootLoadsInFlight.current.has(project.key)
      )
        continue;
      loadProjectRoot(project.key);
    }
  }, [navigationMode, resources, isExpanded, loadProjectRoot]);
  useEffect(() => {
    if (!revealTarget) return;
    const row = Array.from(bodyRef.current?.querySelectorAll<HTMLElement>("[data-session-ref]") ?? []).find(
      (element) => element.dataset.sessionRef === revealTarget,
    );
    if (row && revealCompletedTarget.current !== revealTarget) {
      row.scrollIntoView({ block: "center", behavior: "smooth" });
      consumeReveal();
      return;
    }
    const projectID = projectNodeIdForSessionRef(
      [...resources.projects, ...resources.testRuns, ...resources.archivedProjects],
      revealTarget,
    );
    if (projectID && expandedOverrides.get(projectID) !== true) {
      setExpanded(projectID, true);
      return;
    }
    const currentState = navigationStore.getState();
    const locationKey = { kind: "location", ref: revealTarget } as const;
    // Only a settled tombstone consumes the reveal: a stale one retained
    // across a generation reset may precede the fresh response showing the
    // session present again, and consuming here is irreversible.
    if (isSettledGone(resourceState(currentState, locationKey))) {
      consumeReveal();
      return;
    }
    const location = resourceData<{ project_key?: string; tier?: string; pin_section_id?: string; session?: unknown }>(
      currentState,
      locationKey,
    );
    if (!location) {
      if (revealLookupInFlight.current?.target !== revealTarget) {
        const target = revealTarget;
        const guard: RevealRequestGuard = { target, token: Symbol(`location:${target}`) };
        revealLookupInFlight.current = guard;
        let request: Promise<unknown>;
        try {
          request = Promise.resolve(navigationStore.getState().lookupLocation(target));
        } catch (error) {
          request = Promise.reject(error);
        }
        void request
          .catch(() => undefined)
          .finally(() => {
            if (revealRequestTarget.current === target && revealLookupInFlight.current === guard)
              revealLookupInFlight.current = null;
          });
      }
      return;
    }
    if (!location.session) {
      consumeReveal();
      return;
    }
    if (location.project_key) {
      const projectState = resourceState(currentState, { kind: "project", projectKey: location.project_key });
      if (isSettledGone(projectState)) {
        consumeReveal();
        return;
      }
      const projectID = projectNodeExpansionKey(location.project_key);
      if (expandedOverrides.get(projectID) !== true) {
        setExpanded(projectID, true);
        return;
      }
      const catalog = location.tier === "archived" ? "archived_projects" : "projects";
      requestRevealResource(revealTarget, `catalog:${catalog}`, () => navigationStore.getState().loadCatalog(catalog));
      requestRevealResource(revealTarget, `project:${location.project_key}`, () =>
        navigationStore.getState().loadProject(location.project_key as string),
      );
      return;
    }
    if (location.pin_section_id) {
      requestRevealResource(revealTarget, "pin_catalog", () => navigationStore.getState().loadPinCatalog());
      requestRevealResource(revealTarget, `pin:${location.pin_section_id}`, () =>
        navigationStore.getState().loadPinSection(location.pin_section_id as string),
      );
      return;
    }
    const section = location.tier === "needs_you" ? "needs_you" : "live";
    requestRevealResource(revealTarget, `section:${section}`, () => navigationStore.getState().loadSection(section));
  }, [revealTarget, resources, expandedOverrides, consumeReveal, setExpanded, requestRevealResource]);

  function handleToggle(node: RailNode) {
    if (isPassiveRailNode(node)) return;
    const value = !node.expanded;
    setExpanded(node.id, value);
    if (!value && node.kind === "project") rootLoadsInFlight.current.delete(node.project.key);
    if (
      value &&
      node.kind === "project" &&
      resourceState(state, { kind: "project", projectKey: node.project.key })?.normalized?.presence !== "gone" &&
      !resourceData(state, { kind: "project", projectKey: node.project.key }) &&
      !rootLoadsInFlight.current.has(node.project.key)
    ) {
      loadProjectRoot(node.project.key);
    }
  }
  function openSession(session: RailSession) {
    const url = paneToURL("session", { ref: session.ref });
    if (url) navigate(url);
  }
  function handleActivate(node: RailNode) {
    if (isPassiveRailNode(node)) return;
    if (node.kind === "overflow") {
      void revealOverflow(node);
      return;
    }
    if (node.kind === "session") {
      if (node.session.kind === "cluster") handleToggle(node);
      else openSession(node.session);
      return;
    }
    handleToggle(node);
  }
  async function loadOverflowPage(page: OverflowPage): Promise<void> {
    if (page.projectKey && page.tier) {
      await navigationStore.getState().loadProjectPage(page.projectKey, page.tier, page.offset, page.limit);
      return;
    }
    if (page.section) {
      await navigationStore.getState().loadSection(page.section, page.offset, page.limit);
      return;
    }
    if (page.sectionId) {
      await navigationStore.getState().loadPinSection(page.sectionId, page.offset, page.limit);
      return;
    }
    if (page.catalog) await navigationStore.getState().loadCatalog(page.catalog, page.offset, page.limit);
  }
  async function revealOverflow(node: OverflowRailNode) {
    const pages = node.pages.slice(0, 1).filter((page) => {
      const key = JSON.stringify(page);
      if (overflowPagesInFlight.current.has(key)) return false;
      overflowPagesInFlight.current.add(key);
      return true;
    });
    try {
      await Promise.all(pages.map(loadOverflowPage));
    } catch (error) {
      toasts.push("error", `Couldn't load older sessions: ${errorText(error)}`);
    } finally {
      pages.forEach((page) => {
        overflowPagesInFlight.current.delete(JSON.stringify(page));
      });
    }
  }
  const runAction = useCallback(
    async function runAction<T>(
      fn: () => Promise<T>,
      failure: string,
      optimistic?: PendingOp | ((result: T) => PendingOp),
      propagate = false,
    ) {
      let installed = typeof optimistic === "object" ? optimistic : undefined;
      let mutationCompleted = false;
      let converged = false;
      if (installed) setPending((ops) => [...ops, installed as PendingOp]);
      try {
        const result = await fn();
        mutationCompleted = true;
        if (typeof optimistic === "function") {
          installed = optimistic(result);
          setPending((ops) => [...ops, installed as PendingOp]);
        }
        await convergeMutation(result);
        converged = true;
      } catch (error) {
        toasts.push("error", `${failure}: ${errorText(error)}`);
        if (propagate) throw error;
      } finally {
        if (installed && (!mutationCompleted || converged)) setPending((ops) => ops.filter((op) => op !== installed));
      }
    },
    [toasts.push],
  );
  const rowActions = useMemo<RailRowActions>(
    () => ({
      onOpenSessionPane: (session, pane) => {
        const workspace = workspaceStore.getState();
        workspace.openPane("session", { ref: session.ref });
        workspace.openPane(sessionPanelPaneType(pane), { ref: session.ref });
      },
      onRenameSession: (session, name) =>
        runAction(
          () => threadsStore.getState().rename(session.ref, name),
          "Couldn't rename session",
          { kind: "sessionTitle", ref: session.ref, title: name },
          true,
        ),
      onShutdownSession: async (session) => {
        const convergence = buildShutdownConvergence(session.ref, {
          pinSectionId: session.pin_section_id,
          projectKey: session.project_key,
        });
        const invalidation = convergence.arm();
        try {
          await runAction(
            () => threadsStore.getState().shutdown(session.ref),
            "Couldn't shut down session",
            undefined,
            true,
          );
          await convergence.converge(invalidation);
        } catch (error) {
          invalidation.cancel();
          throw error;
        }
      },
      onPinSession: (session, target, section) =>
        runAction(
          () => assignSessionPin(client, session.ref, target),
          "Couldn't assign pinned session",
          (result) => {
            const assignedSection = section ?? {
              id: result.assignment.section.id,
              name: result.assignment.section.name,
              member_count: result.assignment.section.memberCount,
            };
            navigationStore.getState().trackPinSection(assignedSection.id);
            return {
              kind: "sessionPin",
              ref: session.ref,
              source: session,
              section: { ...assignedSection },
            };
          },
          true,
        ),
      onUnpinRequest: (session) =>
        runAction(
          () => unpinSession(client, session.ref),
          "Couldn't unpin session",
          { kind: "sessionUnpin", ref: session.ref },
          true,
        ),
      onToggleArchiveSession: (session) => {
        const archiving = session.tier !== "archived";
        return runAction(
          () => setArchived("session", session.session_id, archiving),
          "Couldn't update archive state",
          archiving ? { kind: "hideSession", ref: session.ref } : undefined,
          true,
        );
      },
      onDeleteSession: async (session) => {
        const optimistic: PendingOp = { kind: "hideSession", ref: session.ref };
        let mutationCompleted = false;
        let converged = false;
        setPending((ops) => [...ops, optimistic]);
        try {
          const result = await deleteSession(client, session.ref);
          mutationCompleted = true;
          await convergeMutation(result);
          converged = true;
          closePanesForDeletedSessions(result.deleted);
          if (result.skipped.length)
            toasts.push(
              "warning",
              `Couldn't delete "${session.title}": ${result.skipped[0]?.reason ?? "still in use"}`,
            );
        } catch (error) {
          toasts.push("error", `Couldn't delete "${session.title}": ${errorText(error)}`);
          throw error;
        } finally {
          if (!mutationCompleted || converged) setPending((ops) => ops.filter((op) => op !== optimistic));
        }
      },
      onToggleFavoriteProject: (project) => {
        const value = !project.favorite;
        void runAction(() => setFavorite(client, "project", project.key, value), "Couldn't update favorite", {
          kind: "projectFavorite",
          key: project.key,
          value,
        });
      },
      onToggleArchiveProject: (project) => {
        const value = !(project.is_archived ?? false);
        void runAction(
          () => setArchived("project", project.key, value, project.working_dir),
          "Couldn't update archive state",
          value ? { kind: "hideProject", key: project.key } : undefined,
        );
      },
      onDeleteProjectRequest: (project) => setDeleteTarget(project),
    }),
    [client, runAction, toasts.push],
  );
  function closeDeleteDialog() {
    setDeleteTarget(null);
  }
  async function confirmDelete() {
    const target = deleteTarget;
    if (!target) return;
    closeDeleteDialog();
    const optimistic: PendingOp = { kind: "hideProject", key: target.key };
    let mutationCompleted = false;
    let converged = false;
    setPending((ops) => [...ops, optimistic]);
    try {
      const result = await deleteProject(target.key, target.working_dir ?? "");
      mutationCompleted = true;
      await convergeMutation(result);
      converged = true;
      closePanesForDeletedSessions(result.deleted);
      if (result.skipped.length)
        toasts.push(
          "warning",
          `Deleted ${result.deleted.length} session(s); ${result.skipped.length} could not be removed`,
        );
    } catch (error) {
      toasts.push("error", `Couldn't delete project: ${errorText(error)}`);
    } finally {
      if (!mutationCompleted || converged) setPending((ops) => ops.filter((op) => op !== optimistic));
    }
  }
  function openSectionRename(section: RailPinSection) {
    sectionRenameToken.current += 1;
    sectionRenameSubmission.current = null;
    setSectionRenameTarget(section);
    setSectionRenameValue(section.name);
    setSectionRenameError("");
    setSectionRenameSubmitting(false);
  }
  function closeSectionRename() {
    if (sectionRenameSubmission.current) return;
    sectionRenameToken.current += 1;
    setSectionRenameTarget(null);
    setSectionRenameValue("");
    setSectionRenameError("");
    setSectionRenameSubmitting(false);
  }
  async function confirmSectionRename() {
    if (sectionRenameSubmission.current) return;
    const target = sectionRenameTarget;
    const name = sectionRenameValue.trim();
    if (!target) return;
    if (!name) {
      setSectionRenameError("Section name is required");
      return;
    }
    if ([...name].length > 80) {
      setSectionRenameError("Section names must be 80 characters or fewer");
      return;
    }
    const submission = { token: sectionRenameToken.current + 1, sectionID: target.id };
    sectionRenameToken.current = submission.token;
    sectionRenameSubmission.current = submission;
    setSectionRenameSubmitting(true);
    try {
      await runAction(
        () => renamePinSection(client, target.id, name),
        "Couldn't rename pin section",
        (section) => ({ kind: "pinSectionRename", id: target.id, name: section.section.name }),
        true,
      );
      if (sectionRenameSubmission.current !== submission) return;
      sectionRenameSubmission.current = null;
      setSectionRenameTarget(null);
      setSectionRenameValue("");
      setSectionRenameSubmitting(false);
    } catch (error) {
      if (sectionRenameSubmission.current !== submission) return;
      sectionRenameSubmission.current = null;
      setSectionRenameError(errorText(error));
      setSectionRenameSubmitting(false);
    }
  }
  async function requestSectionDelete(section: RailPinSection) {
    const token = ++sectionDeleteRequestToken.current;
    try {
      await navigationStore.getState().loadPinCatalogPages(true);
      if (token !== sectionDeleteRequestToken.current) return;
      const summaries = selectPinSectionSummaries(navigationStore.getState());
      const durable = summaries.find((candidate) => candidate.id === section.id);
      if (!durable) throw new Error("pin section not found");
      setSectionDeleteTarget({ section, memberCount: durable.member_count });
    } catch (error) {
      if (token === sectionDeleteRequestToken.current)
        toasts.push("error", `Couldn't load pin section details: ${errorText(error)}`);
    }
  }
  async function confirmSectionDelete() {
    const target = sectionDeleteTarget;
    if (!target) return;
    setSectionDeleteTarget(null);
    await runAction(() => deletePinSection(client, target.section.id), "Couldn't delete pin section", {
      kind: "pinSectionDelete",
      id: target.section.id,
    });
  }

  const archivedOpen = isExpanded(ARCHIVED_SECTION_KEY, false);
  const pinSections = [...resources.pinSections].sort(
    (a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: "base" }) || a.id.localeCompare(b.id),
  );
  const unarchived = [...resources.projects, ...resources.testRuns];
  const archivedNodes: RailNode[] = [
    ...archivedProjectNodes(
      resources.archivedProjects,
      new Map(
        resources.archivedProjects
          .filter((p) => resourceData(state, { kind: "project", projectKey: p.key }))
          .map((p) => [p.key, p]),
      ),
      isExpanded,
    ),
    ...archivedSessionGroups(unarchived, isExpanded),
  ];
  if (resources.catalogOverflow?.archived_projects) {
    const ov = resources.catalogOverflow.archived_projects;
    archivedNodes.push(
      ...catalogOverflowNode("catalog:archived_projects", "archived_projects", ov.remaining, ov.offset, ov.limit),
    );
  }
  const projectRailNodes = (
    projects: readonly RailProject[],
    overflow?: { remaining: number; offset: number; limit: number },
    overflowId?: string,
    overflowCatalog?: "projects" | "archived_projects" | "test_runs",
  ): RailNode[] => {
    const nodes: RailNode[] = projectNodes(projects, isExpanded);
    if (overflow && overflowId && overflowCatalog && overflow.remaining > 0) {
      nodes.push(
        ...catalogOverflowNode(overflowId, overflowCatalog, overflow.remaining, overflow.offset, overflow.limit),
      );
    }
    return nodes;
  };
  const liveNodes = [
    ...sessionNodes(resources.live, isExpanded),
    ...sectionOverflowNode(
      "section:live",
      "live",
      resources.liveOverflow?.remaining ?? 0,
      resources.liveOverflow?.offset ?? 0,
      resources.liveOverflow?.limit ?? 50,
    ),
  ];
  const resourceLoading = [...resourcesState.values()].some((resource) => resource.loading);
  const loading =
    navigationMode === "unknown" || (navigationMode === "v2" && (!manifest || manifest.loading || resourceLoading));
  const manifestError = manifest?.error ? errorText(manifest.error) : null;
  const resourceError = [...resourcesState.values()].find((resource) => resource.error)?.error;
  const loadError =
    manifestError ??
    (resourceError ? errorText(resourceError) : null) ??
    (navigationMode === "error" ? "Navigation resources are unavailable" : null);
  const displayed = nonEmpty(resources);
  const needsYou = attention?.needsYou ?? manifest?.data?.attentionSummary.needsYou ?? 0;
  const parentOwnsScroll = scrollOwner === "parent";
  return (
    <div
      className={parentOwnsScroll ? `${CLASS.rail} ${CLASS.parentScrollRail}` : CLASS.rail}
      ref={railRef}
      style={width === undefined ? undefined : ({ [RAIL_WIDTH_PROPERTY]: `${width}px` } as CSSProperties)}
    >
      {width !== undefined && onWidthChange && (
        <RailResizeHandle width={width} onCommit={onWidthChange} railRef={railRef} />
      )}
      <div className={CLASS.header}>
        <div data-testid="rail-brand" className={CLASS.brand}>
          <span className={CLASS.brandIdentity}>{serverInfo?.name ?? "evener"}</span>
          {needsYou > 0 && <Badge count={needsYou} tone="attention" />}
          <IconButton
            data-testid="rail-settings"
            label="Settings"
            icon={<GearIcon />}
            variant="quiet"
            size="md"
            onClick={() => navigate("/settings")}
          />
          <Tooltip label="Search sessions and commands">
            <IconButton
              data-testid="rail-search"
              data-search-trigger="true"
              label="Search"
              icon={<SearchIcon />}
              variant="quiet"
              size="md"
            />
          </Tooltip>
          {onHide && (
            <IconButton
              data-rail-toggle=""
              label="Hide sidebar"
              icon={<SidebarIcon />}
              variant="quiet"
              size="md"
              onClick={onHide}
            />
          )}
        </div>
        <div className={CLASS.newSession}>
          <Button variant="primary" onClick={() => navigate("/new")}>
            + New session
          </Button>
        </div>
      </div>
      <div className={parentOwnsScroll ? `${CLASS.body} ${CLASS.parentScrollBody}` : CLASS.body} ref={bodyRef}>
        {loading && !displayed && <Skeleton lines={6} />}
        {!loading && !displayed && loadError && (
          <EmptyState
            title="Couldn't load sessions"
            hint={loadError}
            action={
              <Button size="sm" onClick={() => void navigationStore.getState().loadManifest()}>
                Retry
              </Button>
            }
          />
        )}
        {!loading && !displayed && !loadError && manifest && (
          <EmptyState title="No sessions yet" hint="Start one with the button above." />
        )}
        {displayed && (
          <>
            <RailSection
              title="Live"
              nodes={liveNodes}
              onToggle={handleToggle}
              onActivate={handleActivate}
              actions={rowActions}
              projectRetryCallback={projectRetryCallback}
            />
            {pinSections.map((section) => (
              <PinnedRailSection
                key={section.id}
                section={section}
                open={isExpanded(pinSectionDisclosureID(section.id), true)}
                onToggleOpen={() =>
                  setExpanded(pinSectionDisclosureID(section.id), !isExpanded(pinSectionDisclosureID(section.id), true))
                }
                onRename={() => openSectionRename(section)}
                onDelete={() => void requestSectionDelete(section)}
                isExpanded={isExpanded}
                onToggle={handleToggle}
                onActivate={handleActivate}
                actions={rowActions}
                projectRetryCallback={projectRetryCallback}
              />
            ))}
            <RailSection
              title="Projects"
              nodes={projectRailNodes(
                resources.projects,
                resources.catalogOverflow?.projects,
                "catalog:projects",
                "projects",
              )}
              onToggle={handleToggle}
              onActivate={handleActivate}
              actions={rowActions}
              projectRetryCallback={projectRetryCallback}
            />
            <RailSection
              title="Test runs"
              nodes={projectRailNodes(
                resources.testRuns,
                resources.catalogOverflow?.test_runs,
                "catalog:test_runs",
                "test_runs",
              )}
              onToggle={handleToggle}
              onActivate={handleActivate}
              actions={rowActions}
              projectRetryCallback={projectRetryCallback}
            />
            {archivedNodes.length > 0 && (
              <ArchivedSection
                count={archivedCount(resources.archivedProjects, unarchived)}
                open={archivedOpen}
                onToggleOpen={() => setExpanded(ARCHIVED_SECTION_KEY, !archivedOpen)}
                nodes={archivedNodes}
                onToggle={handleToggle}
                onActivate={handleActivate}
                actions={rowActions}
                projectRetryCallback={projectRetryCallback}
              />
            )}
          </>
        )}
      </div>
      {sectionRenameTarget && (
        <Dialog
          open
          onClose={closeSectionRename}
          title="Rename pin section"
          footer={
            <div className={CLASS.dialogActions}>
              <Button variant="quiet" onClick={closeSectionRename} disabled={sectionRenameSubmitting}>
                Cancel
              </Button>
              <Button onClick={() => void confirmSectionRename()} disabled={sectionRenameSubmitting}>
                Rename section
              </Button>
            </div>
          }
        >
          <label className={CLASS.dialogField} htmlFor={sectionRenameInputID}>
            Section name
            <Input
              id={sectionRenameInputID}
              value={sectionRenameValue}
              onChange={(event: ChangeEvent<HTMLInputElement>) => {
                setSectionRenameValue(event.target.value);
                setSectionRenameError("");
              }}
              disabled={sectionRenameSubmitting}
              aria-describedby={sectionRenameError ? sectionRenameErrorID : undefined}
            />
          </label>
          {sectionRenameError && (
            <p id={sectionRenameErrorID} className={CLASS.pickerError} role="alert">
              {sectionRenameError}
            </p>
          )}
        </Dialog>
      )}
      {sectionDeleteTarget && (
        <Dialog
          open
          onClose={() => setSectionDeleteTarget(null)}
          title="Delete pin section?"
          footer={
            <div className={CLASS.dialogActions}>
              <Button variant="quiet" onClick={() => setSectionDeleteTarget(null)}>
                Cancel
              </Button>
              <Button variant="danger" onClick={() => void confirmSectionDelete()}>
                Delete section
              </Button>
            </div>
          }
        >
          <p>{`Delete “${sectionDeleteTarget.section.name}”? This will unpin ${sectionDeleteTarget.memberCount} session${sectionDeleteTarget.memberCount === 1 ? "" : "s"}.`}</p>
        </Dialog>
      )}
      {deleteTarget && (
        <Dialog
          open
          onClose={closeDeleteDialog}
          title="Delete project?"
          footer={
            <div className={CLASS.dialogActions}>
              <Button variant="quiet" onClick={closeDeleteDialog}>
                Cancel
              </Button>
              <Button variant="danger" onClick={() => void confirmDelete()}>
                Delete
              </Button>
            </div>
          }
        >
          <p>{`Permanently delete every session in "${deleteTarget.name}"? This removes their transcripts and cannot be undone.`}</p>
        </Dialog>
      )}
    </div>
  );
}

export function Rail(props: RailProps = {}) {
  return <NavigationRail {...props} />;
}
