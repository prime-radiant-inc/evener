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
import { type ChangeEvent, type CSSProperties, useCallback, useEffect, useRef, useState } from "react";
import { errorText } from "../../protocol/errors";
import { useConnectionStore } from "../../stores/connection";
import {
  type TreeNode as ApiTreeNode,
  type TreeProject as ApiTreeProject,
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
  Skeleton,
  Tooltip,
  Tree,
  type TreeRowInfo,
  useToasts,
} from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { navigate } from "../routing";
import { workspaceStore } from "../workspace";
import { deleteProject, renameSession, setArchived, setFavorite } from "./actions";
import styles from "./Rail.module.css";
import { RAIL_WIDTH_PROPERTY, RailResizeHandle } from "./RailResizeHandle";
import { isTopLevelSession, RailRow, type RailRowActions } from "./RailRow";
import { loadExpansion, saveExpansion } from "./railExpansion";
import {
  archivedCount,
  archivedProjectNodes,
  archivedSessionGroups,
  overrideLookup,
  projectNodeIdForSessionRef,
  projectNodes,
  type RailNode,
  sessionNodes,
  topLevelAncestorRef,
} from "./railNodes";
import { applyPending, type PendingOp } from "./railPending";

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
  dialogField: requireClass(styles.dialogField, "Rail.module.css", "dialogField"),
  dialogActions: requireClass(styles.dialogActions, "Rail.module.css", "dialogActions"),
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
    tree.favorites.length === 0 &&
    tree.projects.length === 0 &&
    tree.archived_projects.length === 0 &&
    tree.test_runs.length === 0
  );
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
  const projectDetails = useTreeStore((s) => s.projectDetails);
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
  const [deleteTarget, setDeleteTarget] = useState<ApiTreeProject | null>(null);
  // Mutations currently in flight. Everything below renders from the tree with
  // these projected on (railPending), so a click shows before its round trip
  // resolves; runAction adds and removes them.
  const [pending, setPending] = useState<readonly PendingOp[]>([]);
  const bodyRef = useRef<HTMLDivElement>(null);
  const railRef = useRef<HTMLDivElement>(null);

  // applyPending returns the same object when nothing is pending, so the
  // common case allocates nothing and every downstream memo stays stable.
  const tree = fetchedTree === null ? null : applyPending(fetchedTree, pending);

  useEffect(() => {
    void treeStore.getState().refresh();
  }, []);

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
    // in-flight is naturally deduped by projectDetails already having the
    // key, so a second expand never re-fetches.
    if (
      willExpand &&
      node.kind === "project" &&
      node.project.is_archived === true &&
      !projectDetails.has(node.project.key)
    ) {
      void treeStore.getState().loadProjectDetail(node.project.key);
    }
  }

  // Opens the session a row stands for. A NESTED session (a subagent, or a
  // fork's snapshotted original) opens beside the top-level session that owns
  // its task tree rather than as the main pane - see docs/web-ui/specs/
  // 2026-07-26-subagent-opens-beside-main.md. A top-level session opens
  // normally and lands in main when main is free.
  function openSession(session: ApiTreeNode) {
    const workspace = workspaceStore.getState();
    if (isTopLevelSession(session)) {
      workspace.openPane("session", { ref: session.ref });
      return;
    }
    // A layout saved before this rule existed can have this very pane stamped
    // slot:"main" permanently (slot is assign-once and persisted). Closing it
    // first is what lets the reopen below place it correctly, so an existing
    // workspace repairs itself on use instead of every saved layout being
    // discarded to fix the few that are wrong.
    const stuckInMain = workspace.panes.find(
      (p) => p.slot === "main" && p.type === "session" && (p.params as { ref?: string }).ref === session.ref,
    );
    if (stuckInMain) workspace.closePane(stuckInMain.id);

    // Give the subagent something to sit beside, but only when the main slot
    // is genuinely empty: replacing whatever you were already reading would be
    // worse than the misplacement this fixes. mainPane() reads live state -
    // closePane above may just have emptied it.
    const main = workspaceStore.getState().mainPane();
    if (main === null || main.type === "welcome") {
      const ancestor = tree ? topLevelAncestorRef([...tree.projects, ...tree.test_runs], session.ref) : null;
      // keepExistingFocus: the row you clicked is the subagent, so the pane
      // opened on your behalf must not steal focus from it.
      if (ancestor !== null && ancestor !== session.ref) {
        workspaceStore.getState().openPane("session", { ref: ancestor }, { keepExistingFocus: true });
      }
    }
    workspaceStore.getState().openPane("session", { ref: session.ref }, { slot: "secondary" });
  }

  function handleActivate(node: RailNode) {
    if (node.kind === "loading" || node.kind === "overflow") return;
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
  async function runAction(fn: () => Promise<unknown>, failureMessage: string, optimistic?: PendingOp): Promise<void> {
    if (optimistic) setPending((ops) => [...ops, optimistic]);
    try {
      await fn();
      await treeStore.getState().refresh();
    } catch (err) {
      toasts.push("error", `${failureMessage}: ${errorText(err)}`);
    } finally {
      if (optimistic) setPending((ops) => ops.filter((op) => op !== optimistic));
    }
  }

  const rowActions: RailRowActions = {
    onToggleFavorite: (session) => {
      const value = !session.favorite;
      void runAction(() => setFavorite("session", session.ref, value), "Couldn't update favorite", {
        kind: "sessionFavorite",
        ref: session.ref,
        value,
      });
    },
    onToggleArchiveSession: (session) => {
      const archiving = session.tier !== "archived";
      void runAction(
        () => setArchived("session", session.ref, archiving),
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
    try {
      const result = await deleteProject(target.key, target.working_dir ?? "");
      if (result.skipped.length > 0) {
        toasts.push(
          "warning",
          `Deleted ${result.deleted.length} session(s); ${result.skipped.length} could not be removed`,
        );
      }
      void treeStore.getState().refresh();
    } catch (err) {
      toasts.push("error", `Couldn't delete project: ${errorText(err)}`);
    }
  }

  const isExpanded = overrideLookup(expandedOverrides);
  // The Archived section is a disclosure like any other, so it lives in the
  // same override map rather than in a useState of its own - one expand
  // mechanism for the whole rail, and it persists for free.
  const archivedOpen = isExpanded(ARCHIVED_SECTION_KEY, false);

  // Everything the bottom Archived-sessions disclosure holds: whole archived
  // projects (stubs until their own first expand), then one sub-branch per
  // still-active project for the archived sessions projectNodes diverted out
  // of it. Test runs are treated like any other project here - the htmx UI
  // special-cased them out of this divert, and one rule for every project
  // beats carrying that split forward.
  const unarchivedProjects = tree ? [...tree.projects, ...tree.test_runs] : [];
  const archivedNodes = tree
    ? [
        ...archivedProjectNodes(tree.archived_projects, projectDetails, isExpanded),
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
                Live/Pinned/Projects/Archived/Test runs are all retained
                as-is, including Live's own residual overlap with Projects. */}
            <RailSection
              title="Live"
              nodes={sessionNodes(tree.live, isExpanded)}
              onToggle={handleToggle}
              onActivate={handleActivate}
              actions={rowActions}
            />
            <RailSection
              title="Pinned"
              nodes={sessionNodes(tree.favorites, isExpanded)}
              onToggle={handleToggle}
              onActivate={handleActivate}
              actions={rowActions}
            />
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
    </div>
  );
}
