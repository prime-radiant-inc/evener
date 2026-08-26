import { type ChangeEvent, type CSSProperties, useCallback, useEffect, useId, useRef, useState } from "react";
import { sessionPanelPaneType } from "../../panes/sessionPanels";
import { errorText } from "../../protocol/errors";
import type {
  NavigationProjectPage,
  NavigationProjectResource,
  NavigationProjectSummary,
  NavigationSessionSummary,
} from "../../protocol/types.gen";
import { useConnectionStore } from "../../stores/connection";
import { selectAttentionSummary, selectPinSections } from "../../stores/navigation/selectors";
import { navigationStore, useNavigationStore } from "../../stores/navigation/store";
import { keyID, type ResourceKey, type ResourceState } from "../../stores/navigation/types";
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
import { closePanesForDeletedSessions } from "../deletedSessionPanes";
import { navigate, paneToURL } from "../routing";
import { workspaceStore } from "../workspace";
import {
  assignSessionPin,
  deletePinSection,
  deleteProject,
  deleteSession,
  listPinSections,
  renamePinSection,
  renameSession,
  setArchived,
  setFavorite,
  unpinSession,
} from "./actions";
import styles from "./Rail.module.css";
import { RAIL_WIDTH_PROPERTY, RailResizeHandle } from "./RailResizeHandle";
import { RailRow, type RailRowActions } from "./RailRow";
import dialogStyles from "./railDialog.module.css";
import { loadExpansion, saveExpansion } from "./railExpansion";
import {
  archivedCount,
  archivedProjectNodes,
  archivedSessionGroups,
  type OverflowRailNode,
  overrideLookup,
  pinSectionDisclosureID,
  projectNodeIdForSessionRef,
  projectNodes,
  type RailNode,
  type RailPinSection,
  type RailProject,
  type RailSession,
  sessionNodes,
} from "./railNodes";
import { applyPending, buildPinSourceIndex, type PendingOp, type RailResources } from "./railPending";

const CLASS = {
  rail: requireClass(styles.rail, "Rail.module.css", "rail"),
  header: requireClass(styles.header, "Rail.module.css", "header"),
  brand: requireClass(styles.brand, "Rail.module.css", "brand"),
  brandSpacer: requireClass(styles.brandSpacer, "Rail.module.css", "brandSpacer"),
  newSession: requireClass(styles.newSession, "Rail.module.css", "newSession"),
  footer: requireClass(styles.footer, "Rail.module.css", "footer"),
  footerIdentity: requireClass(styles.footerIdentity, "Rail.module.css", "footerIdentity"),
  body: requireClass(styles.body, "Rail.module.css", "body"),
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
const catalogKinds = ["projects", "archived_projects", "test_runs"] as const;
type CatalogKind = (typeof catalogKinds)[number];

interface RailSectionProps {
  title: string;
  nodes: RailNode[];
  onToggle: (node: RailNode) => void;
  onActivate: (node: RailNode) => void;
  actions: RailRowActions;
}
function renderRailRow(actions: RailRowActions) {
  return (node: RailNode, info: TreeRowInfo) => <RailRow node={node} info={info} actions={actions} />;
}
function RailSection({ title, nodes, onToggle, onActivate, actions }: RailSectionProps) {
  if (nodes.length === 0) return null;
  return (
    <section className={CLASS.section}>
      <h3 className={CLASS.sectionTitle}>{title}</h3>
      <Tree nodes={nodes} onToggle={onToggle} onActivate={onActivate} renderRow={renderRailRow(actions)} />
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
}: PinnedRailSectionProps) {
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
          nodes={sessionNodes(section.sessions ?? [], isExpanded)}
          onToggle={onToggle}
          onActivate={onActivate}
          renderRow={renderRailRow(actions)}
        />
      )}
    </section>
  );
}
interface ArchivedSectionProps extends Omit<RailSectionProps, "title"> {
  count: number;
  open: boolean;
  onToggleOpen: () => void;
}
function ArchivedSection({ count, open, onToggleOpen, nodes, onToggle, onActivate, actions }: ArchivedSectionProps) {
  return (
    <section className={CLASS.section}>
      <button type="button" className={CLASS.sectionDisclosure} aria-expanded={open} onClick={onToggleOpen}>
        <Chevron direction={open ? "down" : "right"} /> {`Archived sessions (${count})`}
      </button>
      {open && <Tree nodes={nodes} onToggle={onToggle} onActivate={onActivate} renderRow={renderRailRow(actions)} />}
    </section>
  );
}

export interface RailProps {
  onHide?: () => void;
  width?: number;
  onWidthChange?: (width: number) => void;
  revealTarget?: string | null;
  onRevealConsumed?: () => void;
}

function relativeAge(updatedAt?: string): string | undefined {
  if (!updatedAt) return undefined;
  const timestamp = Date.parse(updatedAt);
  if (!Number.isFinite(timestamp)) return undefined;
  const seconds = Math.max(0, Math.floor((Date.now() - timestamp) / 1000));
  if (seconds < 60) return "now";
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h`;
  return `${Math.floor(seconds / 86400)}d`;
}
function summarySession(summary: NavigationSessionSummary, tier?: string, pinSectionID?: string): RailSession {
  const children = summary.children.map((child) => summarySession(child, tier, pinSectionID));
  return {
    ...summary,
    row_id: `navigation:${summary.ref}`,
    tier,
    pin_section_id: pinSectionID,
    age: relativeAge(summary.updated_at),
    children,
  };
}
function sessions(summaries: readonly NavigationSessionSummary[], tier?: string, pinSectionID?: string): RailSession[] {
  return summaries.map((summary) => summarySession(summary, tier, pinSectionID));
}
function resourceData<T>(state: ReturnType<typeof navigationStore.getState>, key: ResourceKey): T | null {
  return (state.resources.get(keyID(key))?.data as T | undefined) ?? null;
}
function loadedSection(
  state: ReturnType<typeof navigationStore.getState>,
  section: "live" | "needs_you",
): RailSession[] {
  return [...state.resources.values()]
    .filter((resource) => resource.key.kind === "section" && resource.key.section === section && resource.data !== null)
    .sort((a, b) => (a.key.kind === "section" && b.key.kind === "section" ? a.key.offset - b.key.offset : 0))
    .flatMap((resource) => sessions((resource.data as { sessions: NavigationSessionSummary[] }).sessions));
}
function projectFromSummary(
  summary: NavigationProjectSummary,
  root: NavigationProjectResource | null,
  pages: ReadonlyMap<string, ResourceState>,
): RailProject {
  const all: RailSession[] = [];
  const more: Partial<Record<"current" | "recent" | "archived", number>> = {};
  for (const tier of ["current", "recent", "archived"] as const) {
    const base = root?.[tier];
    const pageStates = [...pages.values()].filter(
      (state) =>
        state.key.kind === "project_page" &&
        state.key.projectKey === summary.key &&
        state.key.tier === tier &&
        state.data !== null,
    );
    const rows = [...(base?.sessions ?? [])];
    let remaining = base?.remaining ?? summary[`more_${tier}`] ?? 0;
    for (const pageState of pageStates) {
      const page = pageState.data as NavigationProjectPage;
      for (const row of page.sessions) if (!rows.some((existing) => existing.ref === row.ref)) rows.push(row);
      remaining = Math.min(remaining, page.remaining);
    }
    all.push(...sessions(rows, tier));
    more[tier] = remaining;
  }
  return {
    ...summary,
    loaded: root !== null,
    sessions: all,
    more_current: more.current,
    more_recent: more.recent,
    more_archived: more.archived,
  };
}
function projectsFor(state: ReturnType<typeof navigationStore.getState>, catalog: CatalogKind): RailProject[] {
  const output: RailProject[] = [];
  for (const resource of state.resources.values()) {
    if (resource.key.kind !== "catalog" || resource.key.catalog !== catalog || resource.data === null) continue;
    const data = resource.data as { projects: NavigationProjectSummary[] };
    for (const summary of data.projects) {
      if (output.some((project) => project.key === summary.key)) continue;
      const root = resourceData<NavigationProjectResource>(state, { kind: "project", projectKey: summary.key });
      output.push(projectFromSummary(summary, root, state.resources));
    }
  }
  return output;
}
function railResources(state: ReturnType<typeof navigationStore.getState>): RailResources {
  const pinSections = selectPinSections(state).map((section) => ({
    id: section.id,
    name: section.name,
    member_count: 0,
    sessions: sessions(section.sessions, undefined, section.id),
  }));
  return {
    live: loadedSection(state, "live"),
    needsYou: loadedSection(state, "needs_you"),
    pinSections,
    projects: projectsFor(state, "projects"),
    archivedProjects: projectsFor(state, "archived_projects").map((project) => ({ ...project, is_archived: true })),
    testRuns: projectsFor(state, "test_runs"),
  };
}
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

async function loadKey(key: ResourceKey): Promise<unknown> {
  const state = navigationStore.getState();
  switch (key.kind) {
    case "manifest":
      return state.loadManifest();
    case "section":
      return state.loadSection(key.section, key.offset, key.limit);
    case "pin_catalog":
      return state.loadPinCatalog(key.offset, key.limit);
    case "pin_section":
      return state.loadPinSection(key.sectionId, key.offset, key.limit);
    case "catalog":
      return state.loadCatalog(key.catalog, key.offset, key.limit);
    case "project":
      return state.loadProject(key.projectKey);
    case "project_page":
      return state.loadProjectPage(key.projectKey, key.tier, key.offset, key.limit);
    case "location":
      return undefined;
  }
}
/** Task 10 exposes loaders and invalidation, but no target-satisfaction API.
 * Reload only already-loaded non-location resources as the typed adapter
 * boundary; do not recreate revalidator state or issue a whole-tree request. */
async function reloadLoadedNavigation(): Promise<void> {
  const keys = [...navigationStore.getState().resources.values()]
    .map((resource) => resource.key)
    .filter((key) => key.kind !== "location");
  await Promise.all(keys.map((key) => loadKey(key).catch(() => undefined)));
}

export function Rail({ onHide, width, onWidthChange, revealTarget, onRevealConsumed }: RailProps = {}) {
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
  const base = railResources(state);
  const resources = applyPending(base, pending, { pinSources: buildPinSourceIndex(base) });
  const isExpanded = overrideLookup(expandedOverrides);

  useEffect(() => {
    if (navigationMode !== "v1") return;
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
  useEffect(() => {
    if (navigationMode !== "v1") return;
    for (const project of [...resources.projects, ...resources.archivedProjects, ...resources.testRuns]) {
      const expanded = isExpanded(`projectnode:${project.key}`, project.default_expanded ?? false);
      if (
        !expanded ||
        project.loaded === true ||
        (project.session_count ?? 0) === 0 ||
        rootLoadsInFlight.current.has(project.key)
      )
        continue;
      rootLoadsInFlight.current.add(project.key);
      void Promise.resolve(navigationStore.getState().loadProject(project.key)).catch(() => undefined);
    }
  }, [navigationMode, resources, isExpanded]);
  useEffect(() => {
    if (!revealTarget) return;
    const row = Array.from(bodyRef.current?.querySelectorAll<HTMLElement>("[data-session-ref]") ?? []).find(
      (element) => element.dataset.sessionRef === revealTarget,
    );
    if (row) {
      row.scrollIntoView({ block: "center", behavior: "smooth" });
      onRevealConsumed?.();
      return;
    }
    if (!nonEmpty(resources)) return;
    const projectID = projectNodeIdForSessionRef(
      [...resources.projects, ...resources.testRuns, ...resources.archivedProjects],
      revealTarget,
    );
    if (projectID && expandedOverrides.get(projectID) !== true) {
      setExpanded(projectID, true);
      return;
    }
    const location = resourceData<{ project_key?: string }>(navigationStore.getState(), {
      kind: "location",
      ref: revealTarget,
    });
    if (!location) {
      void Promise.resolve(navigationStore.getState().lookupLocation(revealTarget)).catch(() => undefined);
      return;
    }
    if (location.project_key) {
      const projectID = `projectnode:${location.project_key}`;
      if (expandedOverrides.get(projectID) !== true) {
        setExpanded(projectID, true);
        return;
      }
      void Promise.resolve(navigationStore.getState().loadProject(location.project_key)).catch(() => undefined);
      return;
    }
    onRevealConsumed?.();
  }, [revealTarget, resources, expandedOverrides, onRevealConsumed, setExpanded]);

  function handleToggle(node: RailNode) {
    if (node.kind === "loading") return;
    const value = !node.expanded;
    setExpanded(node.id, value);
    if (
      value &&
      node.kind === "project" &&
      !resourceData(state, { kind: "project", projectKey: node.project.key }) &&
      !rootLoadsInFlight.current.has(node.project.key)
    ) {
      rootLoadsInFlight.current.add(node.project.key);
      void Promise.resolve(navigationStore.getState().loadProject(node.project.key)).catch(() => undefined);
    }
  }
  function openSession(session: RailSession) {
    const url = paneToURL("session", { ref: session.ref });
    if (url) navigate(url);
  }
  function handleActivate(node: RailNode) {
    if (node.kind === "loading") return;
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
  async function revealOverflow(node: OverflowRailNode) {
    const pages = node.pages.filter((page) => {
      const key = `${page.projectKey}:${page.tier}:${page.offset}:${page.limit}`;
      if (overflowPagesInFlight.current.has(key)) return false;
      overflowPagesInFlight.current.add(key);
      return true;
    });
    try {
      await Promise.all(
        pages.map((page) =>
          navigationStore.getState().loadProjectPage(page.projectKey, page.tier, page.offset, page.limit),
        ),
      );
    } catch (error) {
      toasts.push("error", `Couldn't load older sessions: ${errorText(error)}`);
    } finally {
      pages.forEach((page) => {
        overflowPagesInFlight.current.delete(`${page.projectKey}:${page.tier}:${page.offset}:${page.limit}`);
      });
    }
  }
  async function runAction<T>(
    fn: () => Promise<T>,
    failure: string,
    optimistic?: PendingOp | ((result: T) => PendingOp),
    propagate = false,
  ) {
    let installed = typeof optimistic === "object" ? optimistic : undefined;
    if (installed) setPending((ops) => [...ops, installed as PendingOp]);
    try {
      const result = await fn();
      if (typeof optimistic === "function") {
        installed = optimistic(result);
        setPending((ops) => [...ops, installed as PendingOp]);
      }
      await reloadLoadedNavigation();
    } catch (error) {
      toasts.push("error", `${failure}: ${errorText(error)}`);
      if (propagate) throw error;
    } finally {
      if (installed) setPending((ops) => ops.filter((op) => op !== installed));
    }
  }
  const rowActions: RailRowActions = {
    onOpenSessionPane: (session, pane) => {
      const workspace = workspaceStore.getState();
      workspace.openPane("session", { ref: session.ref });
      workspace.openPane(sessionPanelPaneType(pane), { ref: session.ref });
    },
    onRenameSession: (session, name) =>
      runAction(
        () => renameSession(session.ref, name),
        "Couldn't rename session",
        { kind: "sessionTitle", ref: session.ref, title: name },
        true,
      ),
    onShutdownSession: (session) =>
      runAction(() => threadsStore.getState().shutdown(session.ref), "Couldn't shut down session", undefined, true),
    onPinSession: (session, target, section) =>
      runAction(
        () => assignSessionPin(session.ref, target),
        "Couldn't assign pinned session",
        section
          ? {
              kind: "sessionPin",
              ref: session.ref,
              source: session,
              section: { ...section, member_count: section.member_count },
            }
          : (result) => ({
              kind: "sessionPin",
              ref: session.ref,
              source: session,
              section: { ...result.assignment.section },
            }),
        true,
      ),
    onUnpinRequest: (session) =>
      runAction(
        () => unpinSession(session.ref),
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
      setPending((ops) => [...ops, optimistic]);
      try {
        const result = await deleteSession(session.ref);
        await reloadLoadedNavigation();
        closePanesForDeletedSessions(result.deleted);
        if (result.skipped.length)
          toasts.push("warning", `Couldn't delete "${session.title}": ${result.skipped[0]?.reason ?? "still in use"}`);
      } catch (error) {
        toasts.push("error", `Couldn't delete "${session.title}": ${errorText(error)}`);
        throw error;
      } finally {
        setPending((ops) => ops.filter((op) => op !== optimistic));
      }
    },
    onToggleFavoriteProject: (project) => {
      const value = !project.favorite;
      void runAction(() => setFavorite("project", project.key, value), "Couldn't update favorite", {
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
  };
  function closeDeleteDialog() {
    setDeleteTarget(null);
  }
  async function confirmDelete() {
    const target = deleteTarget;
    if (!target) return;
    closeDeleteDialog();
    const optimistic: PendingOp = { kind: "hideProject", key: target.key };
    setPending((ops) => [...ops, optimistic]);
    try {
      const result = await deleteProject(target.key, target.working_dir ?? "");
      await reloadLoadedNavigation();
      closePanesForDeletedSessions(result.deleted);
      if (result.skipped.length)
        toasts.push(
          "warning",
          `Deleted ${result.deleted.length} session(s); ${result.skipped.length} could not be removed`,
        );
    } catch (error) {
      toasts.push("error", `Couldn't delete project: ${errorText(error)}`);
    } finally {
      setPending((ops) => ops.filter((op) => op !== optimistic));
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
        () => renamePinSection(target.id, name),
        "Couldn't rename pin section",
        (section) => ({ kind: "pinSectionRename", id: target.id, name: section.name }),
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
      const summaries = await listPinSections();
      if (token !== sectionDeleteRequestToken.current) return;
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
    await runAction(() => deletePinSection(target.section.id), "Couldn't delete pin section", {
      kind: "pinSectionDelete",
      id: target.section.id,
    });
  }

  const archivedOpen = isExpanded(ARCHIVED_SECTION_KEY, false);
  const pinSections = [...resources.pinSections].sort(
    (a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: "base" }) || a.id.localeCompare(b.id),
  );
  const unarchived = [...resources.projects, ...resources.testRuns];
  const archivedNodes = [
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
  const resourceLoading = [...resourcesState.values()].some((resource) => resource.loading);
  const loading = navigationMode === "v1" && (!manifest || manifest.loading || resourceLoading);
  const manifestError = manifest?.error ? errorText(manifest.error) : null;
  const resourceError = [...resourcesState.values()].find((resource) => resource.error)?.error;
  const loadError = manifestError ?? (resourceError ? errorText(resourceError) : null);
  const displayed = nonEmpty(resources);
  const needsYou = attention?.needsYou ?? manifest?.data?.attentionSummary.needsYou ?? 0;
  return (
    <div
      className={CLASS.rail}
      ref={railRef}
      style={width === undefined ? undefined : ({ [RAIL_WIDTH_PROPERTY]: `${width}px` } as CSSProperties)}
    >
      {width !== undefined && onWidthChange && (
        <RailResizeHandle width={width} onCommit={onWidthChange} railRef={railRef} />
      )}
      <div className={CLASS.header}>
        <div data-testid="rail-brand" className={CLASS.brand}>
          {needsYou > 0 && <Badge count={needsYou} tone="attention" />}
          <span className={CLASS.brandSpacer} />
          <Tooltip label="Search sessions and commands">
            <IconButton
              data-testid="rail-search"
              data-search-trigger="true"
              label="Search"
              icon={<span aria-hidden="true">⌕</span>}
              variant="quiet"
              size="md"
            />
          </Tooltip>
          {onHide && (
            <IconButton
              label="Hide sidebar"
              icon={<span aria-hidden="true">☰</span>}
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
      <div className={CLASS.body} ref={bodyRef}>
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
          <EmptyState title="No sessions yet" hint="Start a session from the command line to see it here." />
        )}
        {displayed && (
          <>
            <RailSection
              title="Live"
              nodes={sessionNodes(resources.live, isExpanded)}
              onToggle={handleToggle}
              onActivate={handleActivate}
              actions={rowActions}
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
              />
            ))}
            <RailSection
              title="Projects"
              nodes={projectNodes(resources.projects, isExpanded)}
              onToggle={handleToggle}
              onActivate={handleActivate}
              actions={rowActions}
            />
            <RailSection
              title="Test runs"
              nodes={projectNodes(resources.testRuns, isExpanded)}
              onToggle={handleToggle}
              onActivate={handleActivate}
              actions={rowActions}
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
              />
            )}
          </>
        )}
      </div>
      <div className={CLASS.footer}>
        <span className={CLASS.footerIdentity}>{serverInfo?.name ?? "evener"}</span>
        <IconButton
          data-testid="rail-settings"
          label="Settings"
          icon={<span aria-hidden="true">⚙</span>}
          variant="quiet"
          size="md"
          onClick={() => navigate("/settings")}
        />
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
