// Rail is the workspace shell's sidebar: a brand row (with search and hide
// icon buttons) over a "+ New session" button as its header,
// session tree over stores/tree.ts, and the pinned identity/settings footer.
// It renders the SAME full chrome everywhere it appears — docked on desktop
// (always; collapsed mode was removed 2026-07-24 at Jesse's direction) and
// inside StackHost's mobile TreeDrawer sheet (which passes it as children).
// Rail owns per-branch expand state, the reveal (railController /project),
// and the rename/delete-project confirmation dialogs. Every mutation goes
// through actions.ts, showing optimistically (railPending) while the request
// is in flight, refetching the tree on success and toasting on failure.
import { type ChangeEvent, type CSSProperties, useCallback, useEffect, useId, useRef, useState } from "react";
import { errorText } from "../../protocol/errors";
import { useConnectionStore } from "../../stores/connection";
import {
  type TreeNode as ApiTreeNode,
  type TreeProject as ApiTreeProject,
  type PinSectionSummary,
  type PinSectionTree,
  type TreeResponse,
  treeStore,
  useTreeStore,
} from "../../stores/tree";
import {
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
import { navigate, paneToURL, urlToPane } from "../routing";
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
import { PinSectionPicker } from "./PinSectionPicker";
import styles from "./Rail.module.css";
import { RAIL_WIDTH_PROPERTY, RailResizeHandle } from "./RailResizeHandle";
import { RailRow, type RailRowActions } from "./RailRow";
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
  sessionNodes,
} from "./railNodes";
import { applyPending, buildPinSourceIndex, type PendingOp } from "./railPending";

const CLASS = {
  rail: requireClass(styles.rail, "Rail.module.css", "rail"),
  header: requireClass(styles.header, "Rail.module.css", "header"),
  brand: requireClass(styles.brand, "Rail.module.css", "brand"),
  brandName: requireClass(styles.brandName, "Rail.module.css", "brandName"),
  newSession: requireClass(styles.newSession, "Rail.module.css", "newSession"),
  footer: requireClass(styles.footer, "Rail.module.css", "footer"),
  footerIdentity: requireClass(styles.footerIdentity, "Rail.module.css", "footerIdentity"),
  body: requireClass(styles.body, "Rail.module.css", "body"),
  section: requireClass(styles.section, "Rail.module.css", "section"),
  sectionTitle: requireClass(styles.sectionTitle, "Rail.module.css", "sectionTitle"),
  sectionDisclosure: requireClass(styles.sectionDisclosure, "Rail.module.css", "sectionDisclosure"),
  sectionHeadingRow: requireClass(styles.sectionHeadingRow, "Rail.module.css", "sectionHeadingRow"),
  sectionHeadingAction: requireClass(styles.sectionHeadingAction, "Rail.module.css", "sectionHeadingAction"),
  dialogField: requireClass(styles.dialogField, "Rail.module.css", "dialogField"),
  dialogActions: requireClass(styles.dialogActions, "Rail.module.css", "dialogActions"),
  pickerError: requireClass(styles.pickerError, "Rail.module.css", "pickerError"),
  srOnly: requireClass(styles.srOnly, "Rail.module.css", "srOnly"),
};

// The Archived section's key in the same expand-override map every row uses.
// Namespaced apart from the id schemes railNodes assigns (row_ids, and its
// own "projectnode:"/"archivedgroup:"/"inactive:" prefixes) so it can never
// collide with a real row.
const ARCHIVED_SECTION_KEY = "section:archived";

function isEmptyTree(tree: TreeResponse): boolean {
  return (
    tree.needs_you.length === 0 &&
    tree.live.length === 0 &&
    tree.pin_sections.length === 0 &&
    tree.projects.length === 0 &&
    tree.archived_projects.length === 0 &&
    tree.test_runs.length === 0
  );
}

// n15j's safety contract for any delete that actually happened: "if the
// deleted session is open in the WebUI, navigate to a surviving
// workspace/session rather than leaving a dead route." Closing every pane
// still showing a session whose files are gone is the whole of what this
// layer owes - workspace.ts's own invariant (DockHost's relaunchWelcome)
// refills an emptied main slot from there, so this never picks a
// replacement. Shared by BOTH delete paths: one deleted session and a whole
// deleted project leave the workspace in the same dead-route state, so they
// clean it up the same way.
//
// Both endpoints report what they actually removed as bare thread ids
// (web_api_project_delete.go's result.Deleted carries target.ThreadID;
// web_api_session_delete.go ships the same shape for one target), and both
// only ever delete LOCAL sessions - so a bare id names the "local:<id>" ref
// a pane carries. An id that already carries a source prefix passes through
// unchanged, the same both-forms tolerance stores/tree.ts's sessionIDMatches
// applies to this very field.
function closePanesForDeletedSessions(deletedIDs: string[]): void {
  const goneRefs = new Set(deletedIDs.map((id) => (id.includes(":") ? id : `local:${id}`)));
  const workspace = workspaceStore.getState();
  for (const pane of workspace.panes) {
    const paneRef = (pane.params as { ref?: unknown }).ref;
    if (typeof paneRef === "string" && goneRefs.has(paneRef)) workspace.closePane(pane.id);
  }
  leaveDeadRoute(goneRefs);
}

// Closing the pane is not enough for the pane the ADDRESS BAR names (kata
// 1hdc): AppShell re-applies the current route on every workspace change, and
// a URL naming a session re-opens a pane for it whether or not the tree still
// has that session - so closing the routed pane just makes the shell open it
// again, on "Loading transcript…" forever. Landing on welcome removes the dead
// route the re-application would keep acting on, and is where the emptied main
// slot already goes on its own (DockHost's relaunch-welcome invariant).
//
// Only a URL naming a ref we JUST deleted is rewritten. Every other route -
// another session, settings, anything - is left exactly where it is: deleting
// one session from the rail is not a reason to move the user off whatever they
// were looking at.
function leaveDeadRoute(goneRefs: ReadonlySet<string>): void {
  const route = urlToPane(window.location.pathname);
  if (route?.type !== "session") return;
  const routedRef = (route.params as { ref?: unknown }).ref;
  if (typeof routedRef !== "string" || !goneRefs.has(routedRef)) return;
  const welcome = paneToURL("welcome", {});
  if (welcome !== null) navigate(welcome);
}

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
  section: PinSectionTree;
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
          nodes={sessionNodes(section.sessions, isExpanded)}
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

// The one bottom disclosure holding everything archived hub-wide
// (parity-m3-sidebar-tree.md §8): whole archived projects, plus a sub-branch
// per active/test-run project for the archived sessions diverted out of it.
// Collapsed by default - it is the least likely thing you opened the rail for,
// which is also why it sits last.
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
  // Renders the header's « "Hide sidebar" button when provided (the desktop
  // case; RailHost wires it to the persisted sidebarHidden boolean). The
  // mobile drawer instance passes none — the drawer is its own show/hide.
  onHide?: () => void;
  // Renders the right-edge drag-to-resize handle at this width when provided
  // (the desktop case; RailHost passes the persisted serf.prefs.sidebarWidth).
  // The mobile drawer instance passes none: the Rail fills the sheet there
  // (Rail.module.css's <=899px block) and has no resizable edge.
  width?: number;
  onWidthChange?: (width: number) => void;
  // The session ref the palette's /project command wants revealed. Rail expands
  // its project section and scrolls its row into view, then calls
  // onRevealConsumed so the caller can clear it. See railController (PIN-A).
  revealTarget?: string | null;
  onRevealConsumed?: () => void;
}

export function Rail({ onHide, width, onWidthChange, revealTarget, onRevealConsumed }: RailProps = {}) {
  const fetchedTree = useTreeStore((s) => s.tree);
  const loading = useTreeStore((s) => s.loading);
  const error = useTreeStore((s) => s.error);
  const treeGeneration = useTreeStore((s) => s.treeGeneration);
  const projectDetails = useTreeStore((s) => s.projectDetails);
  const projectDetailGenerations = useTreeStore((s) => s.projectDetailGenerations);
  // Footer identity: the connected daemon's own name (never a fabricated user
  // handle). Falls back to the "serf" brand string before a handshake has
  // populated serverInfo.
  const serverInfo = useConnectionStore((s) => s.serverInfo);
  const toasts = useToasts();

  // Seeded from localStorage on first render (railExpansion), so a rail you
  // arranged comes back arranged. The lazy initializer means the read happens
  // once per mount, not on every render.
  const [expandedOverrides, setExpandedOverrides] = useState<ReadonlyMap<string, boolean>>(loadExpansion);
  const [renameTarget, setRenameTarget] = useState<ApiTreeNode | null>(null);
  const [renameValue, setRenameValue] = useState("");
  const [pickerTarget, setPickerTarget] = useState<{
    session: ApiTreeNode;
    mode: "pin" | "move";
    token: number;
  } | null>(null);
  const pickerToken = useRef(0);
  const [sectionRenameTarget, setSectionRenameTarget] = useState<PinSectionTree | null>(null);
  const [sectionRenameValue, setSectionRenameValue] = useState("");
  const [sectionRenameError, setSectionRenameError] = useState("");
  const [sectionRenameSubmitting, setSectionRenameSubmitting] = useState(false);
  const sectionRenameInputID = useId();
  const sectionRenameErrorID = useId();
  // State updates do not become visible until React renders. This ref is the
  // synchronous lock that rejects a second click/Enter in that gap; the token
  // also prevents an obsolete async completion from changing a later dialog.
  const sectionRenameSubmission = useRef<{ token: number; sectionID: string } | null>(null);
  const sectionRenameToken = useRef(0);
  const sectionDeleteRequestToken = useRef(0);
  const [sectionDeleteTarget, setSectionDeleteTarget] = useState<{
    section: PinSectionTree;
    memberCount: number;
  } | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<ApiTreeProject | null>(null);
  const [deleteSessionTarget, setDeleteSessionTarget] = useState<ApiTreeNode | null>(null);
  // Mutations currently in flight. Everything below renders from the tree with
  // these projected on (railPending), so a click shows before its round trip
  // resolves; runAction adds and removes them.
  const [pending, setPending] = useState<readonly PendingOp[]>([]);
  const bodyRef = useRef<HTMLDivElement>(null);
  const railRef = useRef<HTMLDivElement>(null);
  const overflowPagesInFlight = useRef(new Set<string>());

  // applyPending returns the same object when nothing is pending, so the
  // common case allocates nothing and every downstream memo stays stable.
  const currentProjectDetails = new Map(
    Array.from(projectDetails).filter(([key]) => projectDetailGenerations.get(key) === treeGeneration),
  );
  const tree =
    fetchedTree === null
      ? null
      : applyPending(fetchedTree, pending, {
          pinSources: buildPinSourceIndex(fetchedTree, {
            treeGeneration,
            projectDetails: Array.from(currentProjectDetails, ([projectKey, detail]) => ({
              projectKey,
              treeGeneration,
              rows: detail.sessions,
            })),
          }),
        });

  // ensureLoaded, not refresh: the duty here is "the rail has data", and the
  // tree it renders is kept current by serf/tree/changed pushes, so a mount
  // with one already loaded has nothing to go and get. Sharing the store's
  // in-flight request is what collapses a desktop boot's two identical GET
  // /api/tree - this and initNotifications()'s baseline, fired milliseconds
  // apart - into one, and stops every mobile drawer OPEN (which remounts this
  // component, since TreeDrawer's sheet renders null while closed) from
  // re-fetching (kata p5w9).
  useEffect(() => {
    void treeStore.getState().ensureLoaded();
  }, []);

  useEffect(
    () => () => {
      sectionRenameToken.current += 1;
      sectionRenameSubmission.current = null;
      pickerToken.current += 1;
      sectionDeleteRequestToken.current += 1;
    },
    [],
  );

  // Every disclosure in the rail funnels through here - project rows, subagent
  // folds, the Archived section - so persisting on this one path covers all of
  // them, row by row. The new map is built OUTSIDE the state updater: a
  // setState updater must stay pure (React runs it twice under StrictMode),
  // and a localStorage write is not. Reading expandedOverrides from the
  // closure is safe because every caller is a discrete user action - one
  // toggle per tick.
  //
  // useCallback, and declared above the reveal effect below, because that
  // effect calls it and so must list it as a dependency: it changes identity
  // exactly when expandedOverrides does, which that effect already depends on,
  // so this adds no extra runs of its own.
  const setExpanded = useCallback(
    (id: string, value: boolean) => {
      const next = new Map(expandedOverrides);
      next.set(id, value);
      setExpandedOverrides(next);
      saveExpansion(next);
    },
    [expandedOverrides],
  );

  // A successful tree refresh invalidates every archived detail's generation.
  // Rehydrate projects which are still visibly expanded without requiring a
  // collapse/re-expand gesture. loadProjectDetail dedupes by project+generation
  // and rejects stale responses at the store authority boundary.
  useEffect(() => {
    if (!fetchedTree || expandedOverrides.get(ARCHIVED_SECTION_KEY) !== true) return;
    for (const project of fetchedTree.archived_projects) {
      if (
        expandedOverrides.get(`projectnode:${project.key}`) === true &&
        projectDetailGenerations.get(project.key) !== treeGeneration
      ) {
        void treeStore.getState().loadProjectDetail(project.key);
      }
    }
  }, [fetchedTree, treeGeneration, projectDetailGenerations, expandedOverrides]);

  // Reveal a session's row for the palette's /project command (railController).
  // If the row is already rendered, scroll it into view (block:"center"). If
  // it's hidden inside a collapsed project, un-collapse that project instead
  // and return: setting the override changes expandedOverrides, which re-runs
  // this effect, and the now-rendered row scrolls on that pass. The Tree
  // renders every expanded row (no virtualization), so a project-bearing ref
  // always resolves after its one expand; the `!== true` guard makes the
  // expand happen at most once, so an unknown ref just falls through to consume
  // rather than looping. Consuming (onRevealConsumed) lets the caller clear the
  // target.
  useEffect(() => {
    if (!revealTarget) return;
    const rows = bodyRef.current?.querySelectorAll<HTMLElement>("[data-session-ref]");
    const row = rows ? Array.from(rows).find((el) => el.dataset.sessionRef === revealTarget) : undefined;
    if (row) {
      row.scrollIntoView({ block: "center", behavior: "smooth" });
      onRevealConsumed?.();
      return;
    }
    if (!tree) return; // tree still loading - wait for it (tree is in deps), don't give up early
    const projectId = projectNodeIdForSessionRef([...tree.projects, ...tree.test_runs], revealTarget);
    if (projectId && expandedOverrides.get(projectId) !== true) {
      // Through setExpanded, so a project the reveal opened is remembered the
      // same way one you opened by hand is - it is the same expand state.
      setExpanded(projectId, true);
      return;
    }
    onRevealConsumed?.();
  }, [revealTarget, tree, expandedOverrides, onRevealConsumed, setExpanded]);

  function handleToggle(node: RailNode) {
    if (node.kind === "loading") return;
    const willExpand = !node.expanded;
    setExpanded(node.id, willExpand);
    // Archived projects ship as session_count-only stubs (see
    // cmd/serf-hub/web_api_tree.go's apiTreeProject doc comment); the first
    // expand is what triggers the lazy load. Already-loaded / already
    // in-flight/current-generation detail is naturally deduped by its
    // provenance marker. A retained cache entry from an older tree generation
    // is deliberately re-fetched: it is neither rendered nor pin-eligible.
    if (
      willExpand &&
      node.kind === "project" &&
      node.project.is_archived === true &&
      projectDetailGenerations.get(node.project.key) !== treeGeneration
    ) {
      void treeStore.getState().loadProjectDetail(node.project.key);
    }
  }

  function openSession(session: ApiTreeNode): void {
    const url = paneToURL("session", { ref: session.ref });
    if (url !== null) navigate(url);
  }

  function handleActivate(node: RailNode) {
    if (node.kind === "loading") return;
    if (node.kind === "overflow") {
      void revealOverflow(node);
      return;
    }
    if (node.kind === "session") {
      // A cluster row is a repeated-title FOLD, not a session: its ref is a
      // synthetic "cluster:<hex>" naming no session, so opening a pane for it
      // loads a transcript that never arrives. It toggles, like every other
      // disclosure in the rail.
      if (node.session.kind === "cluster") {
        handleToggle(node);
        return;
      }
      openSession(node.session);
      return;
    }
    if (node.kind === "inactiveFold") {
      handleToggle(node);
      return;
    }
    handleToggle(node); // a project row has nowhere to "open" - Enter/click toggles it, same as its chevron
  }

  async function revealOverflow(node: OverflowRailNode): Promise<void> {
    const pages = node.pages.filter((page) => {
      const key = `${page.projectKey}:${page.tier}:${page.offset}:${page.limit}`;
      if (overflowPagesInFlight.current.has(key)) return false;
      overflowPagesInFlight.current.add(key);
      return true;
    });
    if (pages.length === 0) return;
    try {
      await Promise.all(
        pages.map((page) => treeStore.getState().loadProjectPage(page.projectKey, page.tier, page.offset, page.limit)),
      );
    } catch (err) {
      toasts.push("error", `Couldn't load older sessions: ${errorText(err)}`);
    } finally {
      for (const page of pages) {
        overflowPagesInFlight.current.delete(`${page.projectKey}:${page.tier}:${page.offset}:${page.limit}`);
      }
    }
  }

  // runAction is the one path every rail mutation takes. `optimistic` is what
  // the row should look like while the request is in flight; it is projected
  // onto the rendered tree (railPending) from before the POST until the
  // follow-up refresh has settled, then dropped. On failure it is dropped too,
  // so a rejected mutation restores exactly what was on screen before - the
  // overlay is a guess, and a refused guess must not outlive its request.
  //
  // The refresh is AWAITED (it never rejects - see stores/tree.ts) rather than
  // fired and forgotten, because its completion is precisely the moment the
  // real tree carries the change and the overlay stops being needed. Dropping
  // the op any earlier would flash the pre-mutation row back for one render.
  async function runAction<T>(
    fn: () => Promise<T>,
    failureMessage: string,
    optimistic?: PendingOp | ((result: T) => PendingOp),
    propagateFailure = false,
  ): Promise<void> {
    let installed = typeof optimistic === "object" ? optimistic : undefined;
    if (installed) {
      const initial = installed;
      setPending((ops) => [...ops, initial]);
    }
    try {
      const result = await fn();
      if (typeof optimistic === "function") {
        const next = optimistic(result);
        installed = next;
        setPending((ops) => [...ops, next]);
      }
      await treeStore.getState().refresh();
    } catch (err) {
      toasts.push("error", `${failureMessage}: ${errorText(err)}`);
      if (propagateFailure) throw err;
    } finally {
      if (installed) setPending((ops) => ops.filter((op) => op !== installed));
    }
  }

  const rowActions: RailRowActions = {
    onPinSectionRequest: (session) => {
      const token = pickerToken.current + 1;
      pickerToken.current = token;
      setPickerTarget({ session, mode: "pin", token });
    },
    onMovePinSectionRequest: (session) => {
      const token = pickerToken.current + 1;
      pickerToken.current = token;
      setPickerTarget({ session, mode: "move", token });
    },
    onToggleArchiveSession: (session) => {
      const archiving = session.tier !== "archived";
      void runAction(
        () => setArchived("session", session.session_id, archiving),
        "Couldn't update archive state",
        // Only the archiving direction hides anything: unarchiving lands the
        // row in whichever tier the server classifies it into, which this
        // layer cannot predict (railPending's own comment).
        archiving ? { kind: "hideSession", ref: session.ref } : undefined,
      );
    },
    onRenameRequest: (session) => {
      setRenameTarget(session);
      setRenameValue(session.title);
    },
    onDeleteSessionRequest: (session) => {
      setDeleteSessionTarget(session);
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
      const archiving = !(project.is_archived ?? false);
      void runAction(
        () => setArchived("project", project.key, archiving, project.working_dir),
        "Couldn't update archive state",
        archiving ? { kind: "hideProject", key: project.key } : undefined,
      );
    },
    onDeleteProjectRequest: (project) => {
      setDeleteTarget(project);
    },
  };

  function closeRenameDialog() {
    setRenameTarget(null);
    setRenameValue("");
  }

  async function confirmRename() {
    const target = renameTarget;
    const name = renameValue.trim();
    if (!target || !name) return;
    closeRenameDialog();
    await runAction(() => renameSession(target.ref, name), "Couldn't rename session", {
      kind: "sessionTitle",
      ref: target.ref,
      title: name,
    });
  }

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
      // The response may contain both deleted and skipped sessions when a
      // session resumes during the destructive pass. Awaiting this refresh
      // while the optimistic project hide is still active makes the next
      // render authoritative: deleted rows stay gone, while skipped live
      // rows/projects return from the rebuilt index honestly.
      await treeStore.getState().refresh();
      // Refresh is best effort. The delete response is authoritative for the
      // identities it processed, so reconcile it even when the follow-up GET
      // failed and would otherwise leave hydrated stale detail visible.
      treeStore.getState().reconcileProjectDelete(
        target.key,
        result.deleted,
        result.skipped.map((session) => session.id),
      );
      // The rail row is gone; the panes routed at its sessions have to go too.
      closePanesForDeletedSessions(result.deleted);
      if (result.skipped.length > 0) {
        toasts.push(
          "warning",
          `Deleted ${result.deleted.length} session(s); ${result.skipped.length} could not be removed`,
        );
      }
    } catch (err) {
      toasts.push("error", `Couldn't delete project: ${errorText(err)}`);
    } finally {
      setPending((ops) => ops.filter((op) => op !== optimistic));
    }
  }

  function closeDeleteSessionDialog() {
    setDeleteSessionTarget(null);
  }

  async function confirmDeleteSession() {
    const target = deleteSessionTarget;
    if (!target) return;
    closeDeleteSessionDialog();
    const optimistic: PendingOp = { kind: "hideSession", ref: target.ref };
    setPending((ops) => [...ops, optimistic]);
    try {
      const result = await deleteSession(target.ref);
      await treeStore.getState().refresh();
      // The session is actually gone: close every open pane still showing it
      // instead of leaving a phantom tab open (see
      // closePanesForDeletedSessions).
      closePanesForDeletedSessions(result.deleted);
      if (result.skipped.length > 0) {
        const reason = result.skipped[0]?.reason ?? "still in use";
        toasts.push("warning", `Couldn't delete "${target.title}": ${reason}`);
      }
    } catch (err) {
      toasts.push("error", `Couldn't delete "${target.title}": ${errorText(err)}`);
    } finally {
      setPending((ops) => ops.filter((op) => op !== optimistic));
    }
  }

  async function assignPickerTarget(
    target: { section_id: string } | { section_name: string },
    selectedSection?: PinSectionSummary,
  ): Promise<void> {
    const picker = pickerTarget;
    if (!picker) return;
    const optimistic = selectedSection
      ? ({ kind: "sessionPin", ref: picker.session.ref, source: picker.session, section: selectedSection } as const)
      : (result: Awaited<ReturnType<typeof assignSessionPin>>): PendingOp => ({
          kind: "sessionPin",
          ref: picker.session.ref,
          source: picker.session,
          section: result.assignment.section,
        });
    await runAction(
      () => assignSessionPin(picker.session.ref, target),
      "Couldn't assign pinned session",
      optimistic,
      true,
    );
    if (pickerToken.current === picker.token) setPickerTarget(null);
  }

  async function unpinPickerTarget(): Promise<void> {
    const picker = pickerTarget;
    if (!picker) return;
    await runAction(
      () => unpinSession(picker.session.ref),
      "Couldn't unpin session",
      { kind: "sessionUnpin", ref: picker.session.ref },
      true,
    );
    if (pickerToken.current === picker.token) setPickerTarget(null);
  }

  function openSectionRename(section: PinSectionTree): void {
    sectionRenameToken.current += 1;
    sectionRenameSubmission.current = null;
    setSectionRenameTarget(section);
    setSectionRenameValue(section.name);
    setSectionRenameError("");
    setSectionRenameSubmitting(false);
  }

  function closeSectionRename(): void {
    if (sectionRenameSubmission.current) return;
    sectionRenameToken.current += 1;
    setSectionRenameTarget(null);
    setSectionRenameValue("");
    setSectionRenameError("");
    setSectionRenameSubmitting(false);
  }

  async function confirmSectionRename(): Promise<void> {
    if (sectionRenameSubmission.current) return;
    const target = sectionRenameTarget;
    const name = sectionRenameValue.trim();
    if (!target) return;
    const count = Array.from(name).length;
    if (count === 0) {
      setSectionRenameError("Section name is required");
      return;
    }
    if (count > 80) {
      setSectionRenameError("Section names must be 80 characters or fewer");
      return;
    }
    setSectionRenameError("");
    const submission = { token: sectionRenameToken.current + 1, sectionID: target.id };
    sectionRenameToken.current = submission.token;
    sectionRenameSubmission.current = submission;
    setSectionRenameSubmitting(true);
    try {
      await runAction(
        () => renamePinSection(target.id, name),
        "Couldn't rename pin section",
        (section): PendingOp => ({ kind: "pinSectionRename", id: target.id, name: section.name }),
        true,
      );
      if (sectionRenameSubmission.current !== submission || sectionRenameToken.current !== submission.token) return;
      sectionRenameSubmission.current = null;
      setSectionRenameTarget(null);
      setSectionRenameValue("");
      setSectionRenameError("");
      setSectionRenameSubmitting(false);
    } catch (err) {
      if (sectionRenameSubmission.current !== submission || sectionRenameToken.current !== submission.token) return;
      sectionRenameSubmission.current = null;
      setSectionRenameError(errorText(err));
      setSectionRenameSubmitting(false);
    }
  }

  async function requestSectionDelete(section: PinSectionTree): Promise<void> {
    const requestToken = sectionDeleteRequestToken.current + 1;
    sectionDeleteRequestToken.current = requestToken;
    try {
      const summaries = await listPinSections();
      if (sectionDeleteRequestToken.current !== requestToken) return;
      const durable = summaries.find((summary) => summary.id === section.id);
      if (!durable) throw new Error("pin section not found");
      setSectionDeleteTarget({ section, memberCount: durable.member_count });
    } catch (err) {
      if (sectionDeleteRequestToken.current !== requestToken) return;
      toasts.push("error", `Couldn't load pin section details: ${errorText(err)}`);
    }
  }

  async function confirmSectionDelete(): Promise<void> {
    const target = sectionDeleteTarget;
    if (!target) return;
    setSectionDeleteTarget(null);
    await runAction(() => deletePinSection(target.section.id), "Couldn't delete pin section", {
      kind: "pinSectionDelete",
      id: target.section.id,
    });
  }

  const isExpanded = overrideLookup(expandedOverrides);
  // The Archived section is a disclosure like any other, so it lives in the
  // same override map rather than in a useState of its own - one expand
  // mechanism for the whole rail, and it persists for free.
  const archivedOpen = isExpanded(ARCHIVED_SECTION_KEY, false);
  const pinSections = tree
    ? [...tree.pin_sections].sort(
        (a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: "base" }) || a.id.localeCompare(b.id),
      )
    : [];

  // Everything the bottom Archived-sessions disclosure holds: whole archived
  // projects (stubs until their own first expand), then one sub-branch per
  // still-active project for the archived sessions projectNodes diverted out
  // of it. Test runs are treated like any other project here - the htmx UI
  // special-cased them out of this divert, and one rule for every project
  // beats carrying that split forward.
  const unarchivedProjects = tree ? [...tree.projects, ...tree.test_runs] : [];
  const archivedNodes = tree
    ? [
        ...archivedProjectNodes(tree.archived_projects, currentProjectDetails, isExpanded),
        ...archivedSessionGroups(unarchivedProjects, isExpanded),
      ]
    : [];

  return (
    // --rail-width is what Rail.module.css's own `.rail` width resolves; the
    // resizable (desktop) instance sets it from the persisted pref, the mobile
    // drawer instance sets nothing and takes the stylesheet's 100% instead.
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
          <span className={CLASS.brandName}>serf</span>
          {/* Search is a palette opener, never a text input, so it rides in
              the brand row as an icon button instead of claiming a row of the
              sidebar's vertical space for itself. data-search-trigger is what
              AppShell's global click handler listens for; the ⌘K / Ctrl-K
              chord that also opens it is taught by the palette's own help
              rows. The tooltip says what the palette actually searches, since
              the icon and the accessible name can only afford one word each:
              widgets/tooltip shifts its bubble away from the viewport edge and
              portals it clear of the rail, so a label this long no longer
              clips at the right edge of a 280px rail or inside the mobile
              drawer (measured both). */}
          <Tooltip label="Search sessions and commands">
            <IconButton
              data-testid="rail-search"
              data-search-trigger="true"
              label="Search"
              icon={<span aria-hidden="true">{"⌕"}</span>}
              variant="quiet"
              size="sm"
            />
          </Tooltip>
          {onHide && (
            <IconButton
              label="Hide sidebar"
              icon={<span aria-hidden="true">{"«"}</span>}
              variant="quiet"
              size="sm"
              onClick={onHide}
            />
          )}
        </div>
        {/* Grid wrapper stretches the primary Button to the rail's full
            width without reaching into the widget's own computed className. */}
        <div className={CLASS.newSession}>
          <Button variant="primary" onClick={() => navigate("/new")}>
            + New session
          </Button>
        </div>
      </div>
      <div className={CLASS.body} ref={bodyRef}>
        {loading && !tree && <Skeleton lines={6} />}
        {!loading && !tree && error && (
          <EmptyState
            title="Couldn't load sessions"
            hint={error}
            action={
              <Button size="sm" onClick={() => void treeStore.getState().refresh()}>
                Retry
              </Button>
            }
          />
        )}
        {tree && isEmptyTree(tree) && (
          <EmptyState title="No sessions yet" hint="Start a session from the command line to see it here." />
        )}
        {tree && !isEmptyTree(tree) && (
          <>
            {/* The auto-grouped "Needs you" tier is deliberately NOT rendered
                here (vbh8, §2.2): it duplicated a session already listed
                under its own project/tier. Attention now surfaces inline -
                the session's own Cadence dot (cadenceStateFor already maps
                awaiting/warning to "needs-you") plus RailRow's derived
                needs-you-descendant Badge - rather than as a second listing.
                tree.needs_you itself is untouched (RailHost's ☰ chip badge
                and attentionSummary.needsYou still read the underlying tiers)
                - only this one RailSection is gone. Per Jesse's decision,
                Live/named pin sections/Projects/Archived/Test runs are all
                retained, including Live's own residual overlap with Projects. */}
            <RailSection
              title="Live"
              nodes={sessionNodes(tree.live, isExpanded)}
              onToggle={handleToggle}
              onActivate={handleActivate}
              actions={rowActions}
            />
            {pinSections.map((section) => {
              const disclosureID = pinSectionDisclosureID(section.id);
              const open = isExpanded(disclosureID, true);
              return (
                <PinnedRailSection
                  key={section.id}
                  section={section}
                  open={open}
                  onToggleOpen={() => setExpanded(disclosureID, !open)}
                  onRename={() => openSectionRename(section)}
                  onDelete={() => void requestSectionDelete(section)}
                  isExpanded={isExpanded}
                  onToggle={handleToggle}
                  onActivate={handleActivate}
                  actions={rowActions}
                />
              );
            })}
            <RailSection
              title="Projects"
              nodes={projectNodes(tree.projects, isExpanded)}
              onToggle={handleToggle}
              onActivate={handleActivate}
              actions={rowActions}
            />
            <RailSection
              title="Test runs"
              nodes={projectNodes(tree.test_runs, isExpanded)}
              onToggle={handleToggle}
              onActivate={handleActivate}
              actions={rowActions}
            />
            {archivedNodes.length > 0 && (
              <ArchivedSection
                count={archivedCount(tree.archived_projects, unarchivedProjects)}
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
        <span className={CLASS.footerIdentity}>{serverInfo?.name ?? "serf"}</span>
        <IconButton
          data-testid="rail-settings"
          label="Settings"
          icon={<span aria-hidden="true">{"⚙"}</span>}
          variant="quiet"
          size="sm"
          onClick={() => navigate("/settings")}
        />
      </div>

      {renameTarget && (
        <Dialog
          open
          onClose={closeRenameDialog}
          title="Rename session"
          footer={
            <div className={CLASS.dialogActions}>
              <Button variant="quiet" onClick={closeRenameDialog}>
                Cancel
              </Button>
              <Button onClick={() => void confirmRename()} disabled={renameValue.trim() === ""}>
                Rename
              </Button>
            </div>
          }
        >
          <label className={CLASS.dialogField}>
            Name
            <Input
              value={renameValue}
              onChange={(e: ChangeEvent<HTMLInputElement>) => setRenameValue(e.target.value)}
            />
          </label>
        </Dialog>
      )}

      {pickerTarget && (
        <PinSectionPicker
          session={pickerTarget.session}
          currentSectionId={pickerTarget.session.pin_section_id}
          mode={pickerTarget.mode}
          onAssign={assignPickerTarget}
          onUnpin={pickerTarget.mode === "move" ? unpinPickerTarget : undefined}
          onClose={() => {
            pickerToken.current += 1;
            setPickerTarget(null);
          }}
        />
      )}

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
          <p>
            Permanently delete every session in "{deleteTarget.name}"? This removes their transcripts and cannot be
            undone.
          </p>
        </Dialog>
      )}

      {deleteSessionTarget && (
        <Dialog
          open
          onClose={closeDeleteSessionDialog}
          title="Delete session?"
          footer={
            <div className={CLASS.dialogActions}>
              <Button variant="quiet" onClick={closeDeleteSessionDialog}>
                Cancel
              </Button>
              <Button variant="danger" onClick={() => void confirmDeleteSession()}>
                Delete
              </Button>
            </div>
          }
        >
          <p>Permanently delete "{deleteSessionTarget.title}"? This removes its transcript and cannot be undone.</p>
        </Dialog>
      )}
    </div>
  );
}
