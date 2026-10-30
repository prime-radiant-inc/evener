import type { NavigationSessionSummary, Source } from "@evener/appwire-client";
import { errorText, LOCAL_HOST } from "@evener/appwire-client";
import { keyID } from "@evener/appwire-client/state/navigation";
import { type CSSProperties, memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import {
  archivedListKey,
  loadMoreArchivedList,
  refreshArchivedList,
  useArchivedLists,
} from "../../stores/archivedList";
import { useConnectionStore } from "../../stores/connection";
import { selectAttentionSummary, selectDisplaySources } from "../../stores/navigation/selectors";
import { buildShutdownConvergence } from "../../stores/navigation/shutdownConvergence";
import { navigationStore, useNavigationStore } from "../../stores/navigation/store";
import { type SidebarGroupingPref, usePrefsStore } from "../../stores/prefs";
import { threadsStore } from "../../stores/threads";
import {
  Badge,
  Button,
  ConfirmDialog,
  DisclosureHeading,
  EmptyState,
  IconButton,
  Menu,
  Popover,
  RadioGroup,
  Skeleton,
  Tooltip,
  Tree,
  type TreeProps,
  type TreeRowInfo,
  useToasts,
} from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { activitySidebarStore } from "../activitybar/activitySidebarStore";
import { useClient } from "../clientContext";
import { closePanesForDeletedSessions } from "../deletedSessionPanes";
import { navigate } from "../routing";
import { openSessionByRef } from "../sessionPlacement";
import { useIsMobile } from "../useIsMobile";
import { currentSessionRef, useWorkspaceStore, workspaceStore } from "../workspace";
import {
  assignSessionPin,
  deleteProject,
  deleteSession,
  type NavigationMutationReceipt,
  partialFanOutNotice,
  projectOwnership,
  setArchived,
  setFavorite,
  unpinSession,
} from "./actions";
import styles from "./Rail.module.css";
import { RAIL_WIDTH_PROPERTY, RailResizeHandle } from "./RailResizeHandle";
import { RailRow, type RailRowActions } from "./RailRow";
import {
  ARCHIVED_SECTION_KEY,
  LIVE_SECTION_KEY,
  loadExpansion,
  PROJECTS_SECTION_KEY,
  saveExpansion,
  TEST_RUNS_SECTION_KEY,
} from "./railExpansion";
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
  sectionOverflowNode,
  sessionNodes,
} from "./railNodes";
import { RailTickProvider } from "./railNow";
import { applyPending, buildPinSourceIndex, type PendingOp } from "./railPending";
import {
  allRailProjects,
  nonEmpty,
  railResources,
  resourceData,
  resourceState,
  withArchivedLists,
} from "./railResources";
import { usePinSectionDialogs } from "./usePinSectionDialogs";
import { useProjectRootLoads } from "./useProjectRootLoads";
import { useRailReveal } from "./useRailReveal";

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
  organizeRow: requireClass(styles.organizeRow, "Rail.module.css", "organizeRow"),
  organizePanel: requireClass(styles.organizePanel, "Rail.module.css", "organizePanel"),
};

interface RailSectionProps {
  title: string;
  nodes: RailNode[];
  open: boolean;
  onToggleOpen: () => void;
  onToggle: (node: RailNode) => void;
  onActivate: (node: RailNode) => void;
  actions: RailRowActions;
  isRowSelected: (node: RailNode) => boolean | undefined;
  projectRetryCallback: (key: string) => () => void;
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
  // The loading placeholder is the rail's one passive row: an Enter or expand
  // chord on it must not persist an expansion override for a row that cannot
  // expand.
  return node.kind === "loading";
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
 * one-decision doctrine as projectPlacement in useRailReveal). Test runs stay flat -
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
      // Flat rows name their launch host too: the display sources resolve
      // it (a local project to this hub, a remote-owned one to its host),
      // so a stale spawn draft cannot redirect the click and a host the
      // manifest has not named cannot fall back to this hub with the
      // remote working_dir.
      return { title: "Projects", nodes: projectNodes(projects, isExpanded, sources) };
  }
}

// One shared Tree wrapper for the rail's sections: the rail renders on
// desktop (RailHost) AND mobile (StackHost's rail slot), but the global Alt
// chords are desktop-only - so only the desktop instance releases
// Alt-held arrows/Home/End from the tree. On mobile the chords are inert,
// and released Alt+ArrowLeft/Right would fall through to the browser's
// history navigation, so the tree keeps them tree-owned.
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
  isRowSelected,
  projectRetryCallback,
}: RailSectionProps) {
  const renderRow = useMemo(() => renderRailRow(actions, projectRetryCallback), [actions, projectRetryCallback]);
  if (nodes.length === 0) return null;
  return (
    <section className={CLASS.section}>
      <DisclosureHeading level={3} label={title} eyebrow open={open} onOpenChange={onToggleOpen} />
      {open && (
        <RailTree
          nodes={nodes}
          onToggle={onToggle}
          onActivate={onActivate}
          renderRow={renderRow}
          isSelected={isRowSelected}
        />
      )}
    </section>
  );
}
interface PinnedRailSectionProps extends Omit<RailSectionProps, "title" | "nodes"> {
  section: RailPinSection;
  onRename: () => void;
  onDelete: () => void;
  projectRetryCallback: (key: string) => () => void;
}
function PinnedRailSection({
  section,
  open,
  onToggleOpen,
  onRename,
  onDelete,
  onToggle,
  onActivate,
  actions,
  isRowSelected,
  projectRetryCallback,
}: PinnedRailSectionProps) {
  const renderRow = useMemo(() => renderRailRow(actions, projectRetryCallback), [actions, projectRetryCallback]);
  return (
    <section className={CLASS.section}>
      <DisclosureHeading
        level={3}
        label={section.name}
        open={open}
        onOpenChange={onToggleOpen}
        action={
          <Menu
            variant="quiet"
            icon="⋯"
            label={`Actions for ${section.name}`}
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
            ...sessionNodes(section.sessions ?? []),
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
          isSelected={isRowSelected}
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
  isRowSelected,
  projectRetryCallback,
}: ArchivedSectionProps) {
  const renderRow = useMemo(() => renderRailRow(actions, projectRetryCallback), [actions, projectRetryCallback]);
  const label = `Archived sessions (${count})`;
  return (
    <section className={CLASS.section}>
      <DisclosureHeading level={3} label={label} eyebrow open={open} onOpenChange={onToggleOpen} />
      {open && (
        <RailTree
          nodes={nodes}
          onToggle={onToggle}
          onActivate={onActivate}
          renderRow={renderRow}
          isSelected={isRowSelected}
        />
      )}
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
// (navigation_service.go's Capture), with no alias expansion - unlike favorites, which pass through
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
  // The session the workspace is showing, if any, marks its row as the selected
  // item. One subscription here, not one inside every section's Tree: a rail
  // renders several Trees, and each would re-render its whole subtree on every
  // pane open/focus/close. Only session rows are selectable - a project or
  // host row returns undefined, leaving it without a selected state rather
  // than asserting it is a non-selection.
  const selectedSessionRef = useWorkspaceStore(currentSessionRef);
  const isRowSelected = (node: RailNode): boolean | undefined =>
    node.kind === "session" ? node.session.ref === selectedSessionRef : undefined;
  const [expandedOverrides, setExpandedOverrides] = useState<ReadonlyMap<string, boolean>>(loadExpansion);
  const [deleteTarget, setDeleteTarget] = useState<RailProject | null>(null);
  const [pending, setPending] = useState<readonly PendingOp[]>([]);
  const bodyRef = useRef<HTMLDivElement>(null);
  const railRef = useRef<HTMLDivElement>(null);
  const overflowPagesInFlight = useRef(new Set<string>());
  const state = { ...navigationStore.getState(), resources: resourcesState, expanded };
  const archivedLists = useArchivedLists();
  const navigationBase = useMemo(
    () => railResources({ ...navigationStore.getState(), resources: resourcesState }),
    [resourcesState],
  );
  const base = useMemo(() => withArchivedLists(navigationBase, archivedLists), [navigationBase, archivedLists]);
  const resources = useMemo(
    () => applyPending(base, pending, { pinSources: buildPinSourceIndex(base) }),
    [base, pending],
  );
  const isExpanded = useMemo(() => overrideLookup(expandedOverrides), [expandedOverrides]);
  useEffect(() => {
    if (navigationMode !== "v3") return;
    if (!manifest)
      void navigationStore
        .getState()
        .loadManifest()
        .catch(() => undefined);
  }, [navigationMode, manifest]);

  const setExpanded = useCallback(
    (id: string, value: boolean) => {
      const next = new Map(expandedOverrides);
      next.set(id, value);
      setExpandedOverrides(next);
      saveExpansion(next);
    },
    [expandedOverrides],
  );
  const { rootLoadsInFlight, loadProjectRoot, projectRetryCallback } = useProjectRootLoads(
    resources,
    navigationMode,
    isExpanded,
    groupingMode,
  );
  // A hydrated project with archived sessions loads its archived list. A
  // loaded list refetches when the navigation count moves to a total the list
  // does not hold: rows were archived or unarchived elsewhere (another client,
  // the CLI, age). Reacting only to a move keeps two counts that disagree from
  // refetching without end, and a list an action already refreshed holds the
  // new total, so it is not fetched twice.
  const archivedCountsSeen = useRef(new Map<string, number>());
  useEffect(() => {
    for (const project of allRailProjects(resources)) {
      if (!project.loaded || !project.catalog) continue;
      const total = project.archived_total ?? 0;
      const key = archivedListKey(project.catalog, project.key);
      const list = archivedLists[key];
      // A move seen while the list loads waits for the answer, which may carry
      // the old total.
      if (list?.loading) continue;
      const countMoved = archivedCountsSeen.current.get(key) !== total;
      archivedCountsSeen.current.set(key, total);
      if (!list) {
        if (total > 0) void refreshArchivedList(project.catalog, project.key);
      } else if (countMoved && (!list.loaded || list.total !== total)) {
        void refreshArchivedList(project.catalog, project.key);
      }
    }
  }, [resources, archivedLists]);
  useRailReveal({
    revealTarget,
    onRevealConsumed,
    resources,
    archivedLists,
    expandedOverrides,
    isExpanded,
    setExpanded,
    groupingMode,
    bodyRef,
  });

  function handleToggle(node: RailNode) {
    if (isPassiveRailNode(node)) return;
    const value = !node.expanded;
    setExpanded(node.id, value);
    if (!value && node.kind === "project") {
      // The in-flight guard is project-wide but one project renders several
      // nodes (host-first copies): clear it only when no other copy stays
      // expanded, or the load effect re-fires a duplicate concurrent load.
      // node.id is excluded because the memoized lookup still reads the
      // pre-toggle map, where this copy counts as expanded.
      const stillExpanded = projectLoadExpansionKeys(node.project, groupingMode).some(
        (id) => id !== node.id && isExpanded(id, node.project.default_expanded ?? false),
      );
      if (!stillExpanded) rootLoadsInFlight.current.delete(node.project.key);
    }
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
      openSession(node.session);
      return;
    }
    handleToggle(node);
  }
  async function loadOverflowPage(page: OverflowPage): Promise<void> {
    if (page.projectKey && page.tier === "archived" && page.catalog) {
      await loadMoreArchivedList(page.catalog, page.projectKey);
      return;
    }
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
      onOpenOverview: (session) => {
        workspaceStore.getState().openPane("session", { ref: session.ref });
        activitySidebarStore.getState().openFor(session.ref);
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
          "Couldn't force shutdown session",
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
  const pinSectionDialogs = usePinSectionDialogs(runAction);
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
      displaySources,
    ),
    ...archivedSessionGroups(unarchived, isExpanded, displaySources),
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
  // A host-grouping fold born mid-session opens itself: a project-first
  // host branch, or a host-first copy that just gained rows. Left closed,
  // the branch hides rows that were visible a render ago (the project's
  // flat children) and the copy hides rows the reveal just brought -
  // either reads as data loss. Only folds born after the first render:
  // the first render keeps the collapsed per-host shape the grouping
  // ships with, and a fold that leaves and returns still respects an
  // explicit collapse from its earlier life. A layout effect so the rows
  // never paint a frame behind a closed fold.
  const seenHostFolds = useRef<ReadonlySet<string> | null>(null);
  const hostFoldIds = useMemo(() => {
    const ids = new Set<string>();
    for (const node of projectsSectionNodes) {
      // Project-first: the host branches inside a project row. A branch
      // renders only once it holds loaded rows, so its id arrives together
      // with the rows it must not hide.
      if (node.kind === "project") {
        for (const child of node.children) if (child.kind === "host") ids.add(child.id);
        continue;
      }
      // Host-first: the project copies inside a host group. A copy renders
      // as soon as the project claims the host, empty or not, so gate on
      // the rows: a copy with no session rows folds nothing away, while
      // one that just gained them must not stay closed.
      if (node.kind === "host") {
        for (const copy of node.children) {
          if (copy.kind === "project" && copy.children.some((row) => row.kind === "session")) {
            ids.add(copy.id);
          }
        }
      }
    }
    return ids;
  }, [projectsSectionNodes]);
  useLayoutEffect(() => {
    const handled = seenHostFolds.current;
    if (handled === null) {
      // The first DATA-BEARING render is the at-rest shape: nothing
      // auto-opens. The rail mounts before navigation data arrives (the
      // store starts with no manifest and no resources), so seeding on
      // the empty first commit would mark every fold that lands with
      // the data as a birth and auto-open the whole section on every
      // fresh load.
      if (projectsSectionNodes.length === 0) return;
      seenHostFolds.current = new Set(hostFoldIds);
      return;
    }
    // One fold per pass, the reveal idiom: setExpanded closes over this
    // render's override map, so a second call in one pass would clobber the
    // first. The override change re-runs this effect and opens the rest,
    // all before paint.
    const next = [...hostFoldIds].find((id) => !handled.has(id) && expandedOverrides.get(id) !== false);
    if (next) {
      seenHostFolds.current = new Set([...handled, next]);
      setExpanded(next, true);
      return;
    }
    // Nothing pending: every current fold is handled, and ids that left
    // the tree drop out so a fold that returns re-opens (its rows came
    // back).
    seenHostFolds.current = new Set(hostFoldIds);
  }, [projectsSectionNodes, hostFoldIds, expandedOverrides, setExpanded]);
  const liveNodes = [
    // Live answers "which machine" the same way in either mode: rows group
    // under host subheaders exactly while they span more than one host
    // (liveNodesGroupedByHost keeps a single-host list flat, byte for byte).
    ...(groupingMode !== "flat"
      ? liveNodesGroupedByHost(sessionNodes(resources.live), displaySources, isExpanded)
      : sessionNodes(resources.live)),
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
    navigationMode === "unknown" || (navigationMode === "v3" && (!manifest || manifest.loading || resourceLoading));
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
      data-testid="rail"
      data-sidebar-rail
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
                isRowSelected={isRowSelected}
                projectRetryCallback={projectRetryCallback}
              />
              {pinSections.map((section) => (
                <PinnedRailSection
                  key={section.id}
                  section={section}
                  open={isExpanded(pinSectionDisclosureID(section.id), true)}
                  onToggleOpen={() => toggleSection(pinSectionDisclosureID(section.id), true)}
                  onRename={() => pinSectionDialogs.openRename(section)}
                  onDelete={() => void pinSectionDialogs.requestDelete(section)}
                  onToggle={handleToggle}
                  onActivate={handleActivate}
                  actions={rowActions}
                  isRowSelected={isRowSelected}
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
                isRowSelected={isRowSelected}
                projectRetryCallback={projectRetryCallback}
              />
              <RailSection
                title="Test runs"
                nodes={withCatalogOverflow(
                  projectNodes(resources.testRuns, isExpanded, displaySources),
                  resources.catalogOverflow?.test_runs,
                  "catalog:test_runs",
                  "test_runs",
                )}
                open={isExpanded(TEST_RUNS_SECTION_KEY, true)}
                onToggleOpen={() => toggleSection(TEST_RUNS_SECTION_KEY, true)}
                onToggle={handleToggle}
                onActivate={handleActivate}
                actions={rowActions}
                isRowSelected={isRowSelected}
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
                  isRowSelected={isRowSelected}
                  projectRetryCallback={projectRetryCallback}
                />
              )}
            </>
          )}
        </div>
      </RailTickProvider>
      {pinSectionDialogs.dialogs}
      <ConfirmDialog
        open={deleteTarget !== null}
        title="Delete project?"
        confirmLabel="Delete"
        onConfirm={() => void confirmDelete()}
        onCancel={closeDeleteDialog}
      >
        {deleteTarget &&
          `Permanently delete every session in "${deleteTarget.name}", including their subagents? This removes their transcripts and cannot be undone.`}
      </ConfirmDialog>
    </div>
  );
}

export function Rail(props: RailProps = {}) {
  return <NavigationRail {...props} />;
}
