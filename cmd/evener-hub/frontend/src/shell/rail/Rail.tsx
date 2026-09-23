import type {
  NavigationCatalogs,
  NavigationProjectPage,
  NavigationProjectResource,
  NavigationProjectSummary,
  NavigationSessionSummary,
  Source,
} from "@evener/appwire-client";
import { canReadSharedNotes, errorText } from "@evener/appwire-client";
import {
  isSettledGone,
  keyID,
  navigationOwnedContainerKey,
  navigationRootContainerKey,
  nextNavigationOffset,
  projectNodeExpansionKey,
  type ResourceKey,
  type ResourceState,
} from "@evener/appwire-client/state/navigation";
import {
  type ChangeEvent,
  type CSSProperties,
  memo,
  type ReactNode,
  useCallback,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
} from "react";
import { sessionPanelPaneType } from "../../panes/sessionPanels";
import { useConnectionStore } from "../../stores/connection";
import { LOCAL_HOST } from "../../stores/hostRouting";
import {
  selectAttentionSummary,
  selectDisplaySources,
  selectPinSectionSummaries,
  selectPinSections,
  selectRailModel,
} from "../../stores/navigation/selectors";
import { buildShutdownConvergence } from "../../stores/navigation/shutdownConvergence";
import { navigationStore, useNavigationStore } from "../../stores/navigation/store";
import { type SidebarGroupingPref, usePrefsStore } from "../../stores/prefs";
import { threadsStore } from "../../stores/threads";
import { topNotesStore } from "../../stores/topNotes";
import {
  Badge,
  Button,
  Chevron,
  Dialog,
  EmptyState,
  IconButton,
  Input,
  Popover,
  RadioGroup,
  Skeleton,
  Tooltip,
  useToasts,
} from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { Menu } from "../../widgets/menu";
import { Tree, type TreeProps, type TreeRowInfo } from "../../widgets/tree";
import { useClient } from "../clientContext";
import { closePanesForDeletedSessions } from "../deletedSessionPanes";
import { navigate } from "../routing";
import { openSessionByRef } from "../sessionPlacement";
import { useIsMobile } from "../useIsMobile";
import { workspaceStore } from "../workspace";
import {
  assignSessionPin,
  deletePinSection,
  deleteProject,
  deleteSession,
  type NavigationMutationReceipt,
  partialFanOutNotice,
  projectOwnership,
  renamePinSection,
  setArchived,
  setFavorite,
  unpinSession,
} from "./actions";
import styles from "./Rail.module.css";
import { RAIL_WIDTH_PROPERTY, RailResizeHandle } from "./RailResizeHandle";
import { RailRow, type RailRowActions } from "./RailRow";
import dialogStyles from "./railDialog.module.css";
import { loadExpansion, saveExpansion } from "./railExpansion";
import { GearIcon, SearchIcon, SidebarIcon, TuneIcon } from "./railIcons";
import {
  archivedCount,
  archivedProjectNodes,
  archivedSessionGroups,
  catalogOverflowNode,
  hostProjectNodes,
  type IsExpanded,
  liveNodesGroupedByHost,
  type OverflowPage,
  type OverflowRailNode,
  overrideLookup,
  pinSectionDisclosureID,
  pinSectionOverflowNode,
  projectLoadExpansionKeys,
  projectNodes,
  projectNodesWithHostBranches,
  type RailGroupingMode,
  type RailNode,
  type RailPinSection,
  type RailProject,
  type RailSession,
  revealExpansionIds,
  sectionOverflowNode,
  sessionNodes,
} from "./railNodes";
import { RailTickProvider } from "./railNow";
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
  staticSectionLabel: requireClass(styles.staticSectionLabel, "Rail.module.css", "staticSectionLabel"),
  sectionDisclosure: requireClass(styles.sectionDisclosure, "Rail.module.css", "sectionDisclosure"),
  sectionHeadingRow: requireClass(styles.sectionHeadingRow, "Rail.module.css", "sectionHeadingRow"),
  sectionHeadingAction: requireClass(styles.sectionHeadingAction, "Rail.module.css", "sectionHeadingAction"),
  organizeRow: requireClass(styles.organizeRow, "Rail.module.css", "organizeRow"),
  organizePanel: requireClass(styles.organizePanel, "Rail.module.css", "organizePanel"),
  dialogField: requireClass(dialogStyles.dialogField, "railDialog.module.css", "dialogField"),
  dialogActions: requireClass(dialogStyles.dialogActions, "railDialog.module.css", "dialogActions"),
  pickerError: requireClass(dialogStyles.pickerError, "railDialog.module.css", "pickerError"),
  srOnly: requireClass(styles.srOnly, "Rail.module.css", "srOnly"),
};

// Section-fold keys. Archived is the only one that starts collapsed; every
// other tier is where you expect to find your work.
const LIVE_SECTION_KEY = "section:live";
const PROJECTS_SECTION_KEY = "section:projects";
const TEST_RUNS_SECTION_KEY = "section:test_runs";
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
  open: boolean;
  onToggleOpen: () => void;
  onToggle: (node: RailNode) => void;
  onActivate: (node: RailNode) => void;
  actions: RailRowActions;
  projectRetryCallback: (key: string) => () => void;
}

// The rail's one section heading: an <h3> wrapping a disclosure button, so
// every tier folds the same way and keeps its heading role for assistive tech.
// The heading takes its name from the button's text (the chevron is
// aria-hidden), so there is no second aria-label to keep in step. The chevron
// TRAILS the title (matching RailRow's own trailing row chevrons) rather than
// leading it, and an optional action slot hosts a heading-level menu (pin
// sections).
interface SectionHeadingProps {
  label: string;
  open: boolean;
  onToggleOpen: () => void;
  staticLabel: boolean;
  action?: ReactNode;
}
function SectionHeading({ label, open, onToggleOpen, staticLabel, action }: SectionHeadingProps) {
  return (
    <div className={CLASS.sectionHeadingRow}>
      <h3 className={staticLabel ? `${CLASS.sectionTitle} ${CLASS.staticSectionLabel}` : CLASS.sectionTitle}>
        <button type="button" className={CLASS.sectionDisclosure} aria-expanded={open} onClick={onToggleOpen}>
          {label}
          <Chevron direction={open ? "down" : "right"} />
        </button>
      </h3>
      {action !== undefined && <div className={CLASS.sectionHeadingAction}>{action}</div>}
    </div>
  );
}

// The rail's organize-by switcher: an icon-only quiet trigger opening the
// RadioGroup popover. The Tooltip carries the name the icon drops, and the
// popover's checked option plus the re-titled section below state the
// current mode, so nothing else on the row needs to. Rendered hard right in
// its own row between the Live section and the Hosts/Projects section it
// controls, so it reads as a toolbar for the section below and never as a
// Live affordance.
function OrganizeByControl({
  mode,
  onChange,
}: {
  mode: SidebarGroupingPref;
  onChange: (mode: SidebarGroupingPref) => void;
}) {
  const [open, setOpen] = useState(false);
  return (
    <Popover
      open={open}
      onClose={() => setOpen(false)}
      trigger={
        <Tooltip label="Organize by">
          <IconButton
            label="Organize by"
            icon={<TuneIcon />}
            variant="quiet"
            size="sm"
            onClick={() => setOpen((current) => !current)}
          />
        </Tooltip>
      }
    >
      <div className={CLASS.organizePanel}>
        <RadioGroup
          label="Organize by"
          value={mode}
          onChange={(value) => onChange(value as SidebarGroupingPref)}
          options={[
            { value: "host-project", label: "Host, then project" },
            { value: "project-host", label: "Project, then host" },
          ]}
        />
      </div>
    </Popover>
  );
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
  return (
    node.kind === "loading" ||
    node.kind === "job" ||
    // A watch row is a leaf with no disclosure of its own: the wire row
    // (active, cadence, note) is the whole truth, exactly like a job row. An
    // Enter must not persist an expansion override for a row that cannot
    // expand.
    node.kind === "watch" ||
    (node.kind === "overflow" && node.passive === true)
  );
}

/** The catalogs' "+N more projects" row, appended to whatever the section's
 * own nodes are - flat project rows today, host groups under the organize-by
 * setting. */
function withCatalogOverflow(
  nodes: RailNode[],
  overflow?: { remaining: number; offset: number; limit: number },
  overflowId?: string,
  overflowCatalog?: "projects" | "archived_projects" | "test_runs",
): RailNode[] {
  if (overflow && overflowId && overflowCatalog && overflow.remaining > 0) {
    return [
      ...nodes,
      ...catalogOverflowNode(overflowId, overflowCatalog, overflow.remaining, overflow.offset, overflow.limit),
    ];
  }
  return nodes;
}

/** The Projects tier under the organize-by setting: ONE decision pairs the
 * section's title with its node shape, so they cannot drift apart (the same
 * one-decision doctrine as projectPlacement below). Test runs stay flat -
 * that tier is a catalog, not the work surface the setting addresses. */
function projectsTierFor(
  mode: RailGroupingMode,
  projects: readonly RailProject[],
  sources: readonly Source[],
  isExpanded: IsExpanded,
): { title: string; nodes: RailNode[] } {
  switch (mode) {
    case "host-project":
      return { title: "Hosts", nodes: hostProjectNodes(projects, sources, isExpanded) };
    case "project-host":
      return { title: "Projects", nodes: projectNodesWithHostBranches(projects, sources, isExpanded) };
    default:
      return { title: "Projects", nodes: projectNodes(projects, isExpanded) };
  }
}

// One shared Tree wrapper for the rail's sections: the rail renders on
// desktop (RailHost) AND mobile (StackHost's rail slot), but the global Alt
// chords are desktop-only - so only the desktop instance releases
// Alt-held arrows/Home/End from the tree. On mobile the chords are inert,
// and released Alt+ArrowLeft/Right would fall through to the browser's
// history navigation, so the tree keeps them tree-owned (roborev PR #1044
// round-9 medium 1).
function RailTree(props: Omit<TreeProps<RailNode>, "releaseModifierKeys">) {
  const isMobile = useIsMobile();
  return <Tree {...props} releaseModifierKeys={!isMobile} />;
}

function RailSection({
  title,
  nodes,
  open,
  onToggleOpen,
  onToggle,
  onActivate,
  actions,
  projectRetryCallback,
}: RailSectionProps) {
  const renderRow = useMemo(() => renderRailRow(actions, projectRetryCallback), [actions, projectRetryCallback]);
  if (nodes.length === 0) return null;
  return (
    <section className={CLASS.section}>
      <SectionHeading label={title} open={open} onToggleOpen={onToggleOpen} staticLabel />
      {open && <RailTree nodes={nodes} onToggle={onToggle} onActivate={onActivate} renderRow={renderRow} />}
    </section>
  );
}
interface PinnedRailSectionProps extends Omit<RailSectionProps, "title" | "nodes"> {
  section: RailPinSection;
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
      <SectionHeading
        label={section.name}
        open={open}
        onToggleOpen={onToggleOpen}
        staticLabel={false}
        action={
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
        }
      />
      {open && (
        <RailTree
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
  const label = `Archived sessions (${count})`;
  return (
    <section className={CLASS.section}>
      <SectionHeading label={label} open={open} onToggleOpen={onToggleOpen} staticLabel />
      {open && <RailTree nodes={nodes} onToggle={onToggle} onActivate={onActivate} renderRow={renderRow} />}
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

// archiveSessionIdentity is the identity a session's archive decision is
// stored and read back under, and it is not the same string for every row
// (round eleven's high finding). A REMOTE row's decision is consulted under its
// host-qualified ref: web_api_tree.go's appThreadTreeEntries stamps a remote
// thread's meta.ID and LiveEntry.SessionID with ref.String() ("buildbox:t1"),
// and hubcore's decisionFor (tree.go) looks the stored key up verbatim - both
// for the tree rows that call it and for tierEligible (attention.go), which is
// handed the bare LiveEntry.SessionID. A LOCAL row's two ids are the bare
// session ID instead (tree.go builds local nodes with ID: m.ID; the local
// entries in web_api_tree.go carry SessionID: past.Meta.ID), and archive
// decisions reach the read model through archiveDecisions() verbatim
// (web_api_tree.go's memoTreeWithAuthority and navigation_service.go's
// Capture), with no alias expansion - unlike favorites, which pass through
// ClassifyFavoriteDecisions.
//
// The write side normalizes, so those two spellings are ONE stored key rather
// than a consulted one and an inert one: app_archive.go runs the wire id
// through hubcore.NormalizeDecisionSessionID (internal/hubcore/archive.go),
// which collapses a "local:<id>" ref to exactly the bare session ID the read
// path above consults and keeps a host-qualified ref as sent. A "local:<id>"
// archive therefore neither lands on a second key nor goes unread; sending the
// bare id is this side matching the reader's own identity, not a correction of
// a key the server would otherwise store verbatim.
//
// Round ten sent the wire ref for every row: the optimistic hideSession overlay
// matches on that ref (railPending.ts), while the mutation's identity is the
// reader's key, which is why only the mutation identity changes here.
export function archiveSessionIdentity(session: NavigationSessionSummary): string {
  return session.host_id === "local" ? session.session_id : session.ref;
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
  // The rail's organize-by setting. Host grouping turns on only when the
  // manifest has named a remote source, and the decision reads the DISPLAY
  // list (last-known): grouping is a layout fact, and the settled list
  // (selectSources) empties for the length of any revalidation, which would
  // blink the whole rail flat and back. Launchability stays on the settled
  // list where it belongs - the spawn picker reads it - so a host the fresh
  // manifest removed is still never launchable; the rail's SHAPE merely
  // keeps the last-known hosts until the read settles. The builders read
  // the display list too: a host group's online flag and label are display
  // facts, and a revalidation must not flip groups offline for the length
  // of the refresh (the same sticky contract the session rows' host chips
  // read). A hub with no configured hosts keeps today's rail exactly.
  const grouping = usePrefsStore((state) => state.sidebarGrouping);
  const setGrouping = usePrefsStore((state) => state.setSidebarGrouping);
  const displaySources = useNavigationStore(selectDisplaySources);
  const hostGrouping = displaySources.some((source) => source.id !== LOCAL_HOST);
  const groupingMode: RailGroupingMode = hostGrouping ? grouping : "flat";
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
  // A reveal lands on a row that may be sitting inside a section fold the
  // reader closed (or that starts closed, like Archived). Open it and report
  // that the caller should re-run rather than scroll to a hidden row.
  const openRevealSection = useCallback(
    (key: string, defaultOpen: boolean) => {
      if (isExpanded(key, defaultOpen)) return true;
      setExpanded(key, true);
      return false;
    },
    [isExpanded, setExpanded],
  );
  // Where a revealed project's row lives: the fold it is rendered in, whether
  // that fold starts open, and the catalog that carries it. Archived is the
  // tier that folds under "Archived sessions" and starts closed; a test-run
  // project folds - and is catalogued - under "Test runs".
  const projectPlacement = useCallback(
    (location: { project_key?: string; tier?: string }) => {
      // A whole-archived project's sessions keep their own tier, so the
      // catalog is the reliable membership test there and the tier is not.
      const archived =
        location.tier === "archived" ||
        resources.archivedProjects.some((project) => project.key === location.project_key);
      if (archived)
        return { sectionKey: ARCHIVED_SECTION_KEY, defaultOpen: false, catalog: "archived_projects" as const };
      if (resources.testRuns.some((project) => project.key === location.project_key))
        return { sectionKey: TEST_RUNS_SECTION_KEY, defaultOpen: true, catalog: "test_runs" as const };
      return { sectionKey: PROJECTS_SECTION_KEY, defaultOpen: true, catalog: "projects" as const };
    },
    [resources],
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
      const expanded = projectLoadExpansionKeys(project, groupingMode).some((id) =>
        isExpanded(id, project.default_expanded ?? false),
      );
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
  }, [navigationMode, resources, isExpanded, loadProjectRoot, groupingMode]);
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
    // The chain must name folds that actually render: only the Projects
    // section honors the grouping, so grouped ids apply to it alone while
    // the always-flat tiers keep flat ids whatever the mode. A test run's
    // archived rows still route to the archived-group fold; a whole-archived
    // project renders every row under its own node instead.
    const chain = [
      ...revealExpansionIds(resources.projects, resources.live, revealTarget, groupingMode),
      ...revealExpansionIds(resources.testRuns, [], revealTarget, "flat"),
      ...revealExpansionIds(resources.archivedProjects, [], revealTarget, "flat", { rowsUnderProjectNode: true }),
    ];
    const nextFold = chain.find((id) => expandedOverrides.get(id) !== true);
    if (nextFold) {
      setExpanded(nextFold, true);
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
      // One decision covers both which fold must open and which catalog holds
      // the row; deriving them separately is how a test run ends up opening
      // one fold while loading another catalog's rows.
      const placement = projectPlacement(location);
      if (!openRevealSection(placement.sectionKey, placement.defaultOpen)) return;
      requestRevealResource(revealTarget, `catalog:${placement.catalog}`, () =>
        navigationStore.getState().loadCatalog(placement.catalog),
      );
      requestRevealResource(revealTarget, `project:${location.project_key}`, () =>
        navigationStore.getState().loadProject(location.project_key as string),
      );
      return;
    }
    if (location.pin_section_id) {
      if (!openRevealSection(pinSectionDisclosureID(location.pin_section_id), true)) return;
      requestRevealResource(revealTarget, "pin_catalog", () => navigationStore.getState().loadPinCatalog());
      requestRevealResource(revealTarget, `pin:${location.pin_section_id}`, () =>
        navigationStore.getState().loadPinSection(location.pin_section_id as string),
      );
      return;
    }
    const section = location.tier === "needs_you" ? "needs_you" : "live";
    // needs_you has no fold of its own, and the rail renders none of its rows
    // today, so Live is the only section that can hide this one.
    if (section === "live" && !openRevealSection(LIVE_SECTION_KEY, true)) return;
    requestRevealResource(revealTarget, `section:${section}`, () => navigationStore.getState().loadSection(section));
  }, [
    revealTarget,
    resources,
    expandedOverrides,
    groupingMode,
    consumeReveal,
    setExpanded,
    requestRevealResource,
    openRevealSection,
    projectPlacement,
  ]);

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
  function toggleSection(key: string, defaultOpen: boolean) {
    setExpanded(key, !isExpanded(key, defaultOpen));
  }
  function openSession(session: RailSession) {
    openSessionByRef(session.ref);
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
        // A menu rendered while the session still had the notes capability
        // can be clicked before React processes the revocation, so the notes
        // action rechecks the capability - the same guard SessionChrome's
        // own Notes entry applies. Without it a stale click leaves expanded
        // and focus state for a panel that cannot render.
        if (pane === "notes" && !canReadSharedNotes(threadsStore.getState().threads.get(session.ref))) return;
        const workspace = workspaceStore.getState();
        workspace.openPane("session", { ref: session.ref });
        if (pane === "notes") {
          // Idempotent open, like the sibling branches below: the rail
          // navigates, it does not toggle - closing notes belongs to the
          // panel's own header and the palette's Toggle command.
          topNotesStore.getState().openAndFocus(session.ref);
        } else {
          workspace.openPane(sessionPanelPaneType(pane), { ref: session.ref });
        }
      },
      onRenameSession: (session, name) =>
        runAction(
          () => threadsStore.getState().rename(session.ref, name),
          "Couldn't rename session",
          { kind: "sessionTitle", ref: session.ref, title: name },
          true,
        ),
      onForceStopSession: async (session) => {
        await runAction(
          () => threadsStore.getState().forceStop(session.ref),
          "Couldn't force stop session",
          undefined,
          true,
        );
        await runAction(
          () => threadsStore.getState().refreshThread(session.ref),
          "Session stopped; couldn't refresh its view",
        );
      },
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
          // The identity the decision is read back under, host for host - see
          // archiveSessionIdentity. Component 06a's round six was right that a
          // remote row's host-qualified ref is that identity; a local row sends
          // its bare session_id, the identity the read model consults, rather
          // than the wire ref (round eleven). The server would normalize either
          // spelling to that same key (app_archive.go runs the id through
          // hubcore.NormalizeDecisionSessionID), so this is the reader's own
          // identity being sent, not a workaround for an inert key.
          () => setArchived("session", archiveSessionIdentity(session), archiving),
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
        void runAction(
          async () => {
            // A merged project's favorite is one decision per owning source.
            // The fan-out settles every owner, so a partial result is a commit
            // for the owners that answered: present the value the settled set
            // yields (the read side shows a favorite when any owner holds one)
            // and name the owners still holding the old decision. The row's own
            // favorite is what the settled set is derived from: it says whether
            // any owner held one, so a clear that missed an owner of a project
            // nobody had favorited cannot present the row as favorited.
            const result = await setFavorite(client, "project", project.key, value, project.sources, {
              favoritedBefore: project.favorite ?? false,
            });
            const notice = partialFanOutNotice(result.failedSources);
            if (notice) toasts.push("warning", `Favorite not updated everywhere: ${notice}`);
            return result;
          },
          "Couldn't update favorite",
          (result) => ({ kind: "projectFavorite", key: project.key, value: result.favorite }),
        );
      },
      onToggleArchiveProject: (project) => {
        const value = !(project.is_archived ?? false);
        void runAction(
          async () => {
            const result = await setArchived("project", project.key, value, project.working_dir, project.sources);
            const notice = partialFanOutNotice(result.failedSources);
            if (notice) toasts.push("warning", `Archive state not updated everywhere: ${notice}`);
            return result;
          },
          "Couldn't update archive state",
          value ? { kind: "hideProject", key: project.key } : undefined,
        );
      },
      onDeleteProjectRequest: (project) => {
        // Deletion is local-only (the hub refuses any other source), and a
        // merged project — this hub's own rows plus a remote host's under the
        // same canonical ID and path — must never be answered with a delete of
        // the local project. Refuse before the confirmation dialog opens.
        const { hosts } = projectOwnership(project.sources);
        if (hosts.length > 0) {
          toasts.push(
            "error",
            `Couldn't delete "${project.name}": it also has sessions on ${hosts.join(", ")}, and deletion is local-only`,
          );
          return;
        }
        setDeleteTarget(project);
      },
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
      const result = await deleteProject(target.key, target.working_dir ?? "", target.sources);
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
  const projectsTier = projectsTierFor(groupingMode, resources.projects, displaySources, isExpanded);
  const projectsSectionNodes = withCatalogOverflow(
    projectsTier.nodes,
    resources.catalogOverflow?.projects,
    "catalog:projects",
    "projects",
  );
  const liveNodes = [
    // Live answers "which machine" the same way in either mode: rows group
    // under host subheaders exactly while they span more than one host
    // (liveNodesGroupedByHost keeps a single-host list flat, byte for byte).
    ...(groupingMode !== "flat"
      ? liveNodesGroupedByHost(sessionNodes(resources.live, isExpanded), displaySources, isExpanded)
      : sessionNodes(resources.live, isExpanded)),
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
      {/* The rail's clock (railNow.tsx): rows derive their relative stamps from
          it. It wraps the tree only - the header and the dialogs render no
          clock-derived value - so clock-derived chrome added later has to move
          inside this boundary to tick. */}
      <RailTickProvider>
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
                open={isExpanded(LIVE_SECTION_KEY, true)}
                onToggleOpen={() => toggleSection(LIVE_SECTION_KEY, true)}
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
                  onToggleOpen={() => toggleSection(pinSectionDisclosureID(section.id), true)}
                  onRename={() => openSectionRename(section)}
                  onDelete={() => void requestSectionDelete(section)}
                  isExpanded={isExpanded}
                  onToggle={handleToggle}
                  onActivate={handleActivate}
                  actions={rowActions}
                  projectRetryCallback={projectRetryCallback}
                />
              ))}
              {hostGrouping && (
                <div className={CLASS.organizeRow}>
                  <OrganizeByControl mode={grouping} onChange={setGrouping} />
                </div>
              )}
              <RailSection
                title={projectsTier.title}
                nodes={projectsSectionNodes}
                open={isExpanded(PROJECTS_SECTION_KEY, true)}
                onToggleOpen={() => toggleSection(PROJECTS_SECTION_KEY, true)}
                onToggle={handleToggle}
                onActivate={handleActivate}
                actions={rowActions}
                projectRetryCallback={projectRetryCallback}
              />
              <RailSection
                title="Test runs"
                nodes={withCatalogOverflow(
                  projectNodes(resources.testRuns, isExpanded),
                  resources.catalogOverflow?.test_runs,
                  "catalog:test_runs",
                  "test_runs",
                )}
                open={isExpanded(TEST_RUNS_SECTION_KEY, true)}
                onToggleOpen={() => toggleSection(TEST_RUNS_SECTION_KEY, true)}
                onToggle={handleToggle}
                onActivate={handleActivate}
                actions={rowActions}
                projectRetryCallback={projectRetryCallback}
              />
              {archivedNodes.length > 0 && (
                <ArchivedSection
                  count={archivedCount(resources.archivedProjects, unarchived)}
                  open={archivedOpen}
                  onToggleOpen={() => toggleSection(ARCHIVED_SECTION_KEY, false)}
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
      </RailTickProvider>
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
