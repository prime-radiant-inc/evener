// Rail is the workspace shell's sidebar content: session tree over
// stores/tree.ts. RailHost decides its VISIBILITY (inline, or hidden behind the
// ☰ overlay drawer per sidebarMode) and mounts it; Rail itself just renders the
// tree, owns per-branch expand state, the reveal (railController /project), and
// the rename/delete-project confirmation dialogs. Every mutation goes through
// actions.ts, refetching the tree on success and toasting on failure (no
// optimistic UI - out of this task's scope). The header's "Hide sidebar" button
// is shown only when RailHost passes onHide (the inline desktop case).
//
// hostedInSheet (default false) is how Rail avoids nesting a "Sessions" panel
// inside a "Sessions" panel: RailHost's collapsed ☰ overlay drawer and the
// mobile TreeDrawer both wrap Rail in a widgets/sheet Sheet that already
// renders its own "Sessions" title and close (X) button and owns the panel's
// outer frame. Without this, Rail's OWN header duplicated that exact chrome -
// two stacked "Sessions" titles, Rail's fixed 280px-wide bordered box floating
// inside the Sheet's wider (420px) panel like a second, narrower panel nested
// in the first. hostedInSheet suppresses Rail's own header entirely and fills
// the Sheet's width instead of imposing the inline-desktop fixed width (see
// Rail.module.css's .hosted). It is purely a rendering concern - no persisted
// state ever encodes "nested"; the previous bug reproduced identically on
// every fresh open with no history required and never compounded across
// repeated open/close cycles, so there is no saved layout to migrate or
// normalize - a plain reload with this code already shows exactly one
// "Sessions" panel.
import { type ChangeEvent, useEffect, useRef, useState } from "react";
import {
  type TreeNode as ApiTreeNode,
  type TreeProject as ApiTreeProject,
  type TreeResponse,
  treeStore,
  useTreeStore,
} from "../../stores/tree";
import {
  Button,
  Dialog,
  EmptyState,
  IconButton,
  Input,
  Skeleton,
  Tree,
  type TreeRowInfo,
  useToasts,
} from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { workspaceStore } from "../workspace";
import { deleteProject, renameSession, setArchived, setFavorite } from "./actions";
import styles from "./Rail.module.css";
import { RailRow, type RailRowActions } from "./RailRow";
import {
  archivedProjectNodes,
  overrideLookup,
  projectNodeIdForSessionRef,
  projectNodes,
  type RailNode,
  sessionNodes,
} from "./railNodes";

const CLASS = {
  rail: requireClass(styles.rail, "Rail.module.css", "rail"),
  hosted: requireClass(styles.hosted, "Rail.module.css", "hosted"),
  header: requireClass(styles.header, "Rail.module.css", "header"),
  title: requireClass(styles.title, "Rail.module.css", "title"),
  body: requireClass(styles.body, "Rail.module.css", "body"),
  section: requireClass(styles.section, "Rail.module.css", "section"),
  sectionTitle: requireClass(styles.sectionTitle, "Rail.module.css", "sectionTitle"),
  sectionDisclosure: requireClass(styles.sectionDisclosure, "Rail.module.css", "sectionDisclosure"),
  dialogField: requireClass(styles.dialogField, "Rail.module.css", "dialogField"),
  dialogActions: requireClass(styles.dialogActions, "Rail.module.css", "dialogActions"),
};

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

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
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
  open: boolean;
  onToggleOpen: () => void;
}

function ArchivedSection({ open, onToggleOpen, nodes, onToggle, onActivate, actions }: ArchivedSectionProps) {
  return (
    <section className={CLASS.section}>
      <button type="button" className={CLASS.sectionDisclosure} aria-expanded={open} onClick={onToggleOpen}>
        <span aria-hidden="true">{open ? "▾" : "▸"}</span> Archived
      </button>
      {open && <Tree nodes={nodes} onToggle={onToggle} onActivate={onActivate} renderRow={renderRailRow(actions)} />}
    </section>
  );
}

export interface RailProps {
  // Shown as the header "Hide sidebar" button when provided (the inline desktop
  // case). RailHost wires it to collapse the sidebar (setSidebarMode("rail"));
  // the mobile-drawer and overlay-drawer instances pass none (the drawer has
  // its own close) and show no button.
  onHide?: () => void;
  // The session ref the palette's /project command wants revealed. Rail expands
  // its project section and scrolls its row into view, then calls
  // onRevealConsumed so the caller can clear it. See railController (PIN-A).
  revealTarget?: string | null;
  onRevealConsumed?: () => void;
  // True when a Sheet-based overlay drawer already renders this instance's
  // title/close chrome and owns its outer frame (RailHost's collapsed ☰
  // drawer; the mobile TreeDrawer) - see this file's own top comment.
  // Defaults to false: the inline desktop case, where Rail has no enclosing
  // Sheet and must frame and title itself.
  hostedInSheet?: boolean;
}

export function Rail({ onHide, revealTarget, onRevealConsumed, hostedInSheet = false }: RailProps = {}) {
  const tree = useTreeStore((s) => s.tree);
  const loading = useTreeStore((s) => s.loading);
  const error = useTreeStore((s) => s.error);
  const projectDetails = useTreeStore((s) => s.projectDetails);
  const toasts = useToasts();

  const [expandedOverrides, setExpandedOverrides] = useState<ReadonlyMap<string, boolean>>(new Map());
  const [archivedOpen, setArchivedOpen] = useState(false);
  const [renameTarget, setRenameTarget] = useState<ApiTreeNode | null>(null);
  const [renameValue, setRenameValue] = useState("");
  const [deleteTarget, setDeleteTarget] = useState<ApiTreeProject | null>(null);
  const bodyRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    void treeStore.getState().refresh();
  }, []);

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
      setExpandedOverrides((prev) => {
        const next = new Map(prev);
        next.set(projectId, true);
        return next;
      });
      return;
    }
    onRevealConsumed?.();
  }, [revealTarget, tree, expandedOverrides, onRevealConsumed]);

  function setExpanded(id: string, value: boolean) {
    setExpandedOverrides((prev) => {
      const next = new Map(prev);
      next.set(id, value);
      return next;
    });
  }

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

  function handleActivate(node: RailNode) {
    if (node.kind === "loading") return;
    if (node.kind === "session") {
      workspaceStore.getState().openPane("session", { ref: node.session.ref });
      return;
    }
    handleToggle(node); // a project row has nowhere to "open" - Enter/click toggles it, same as its chevron
  }

  async function runAction(fn: () => Promise<unknown>, failureMessage: string): Promise<void> {
    try {
      await fn();
      void treeStore.getState().refresh();
    } catch (err) {
      toasts.push("error", `${failureMessage}: ${errorMessage(err)}`);
    }
  }

  const rowActions: RailRowActions = {
    onToggleFavorite: (session) => {
      void runAction(() => setFavorite("session", session.ref, !session.favorite), "Couldn't update favorite");
    },
    onToggleArchiveSession: (session) => {
      void runAction(
        () => setArchived("session", session.ref, session.tier !== "archived"),
        "Couldn't update archive state",
      );
    },
    onRenameRequest: (session) => {
      setRenameTarget(session);
      setRenameValue(session.title);
    },
    onToggleFavoriteProject: (project) => {
      void runAction(() => setFavorite("project", project.key, !project.favorite), "Couldn't update favorite");
    },
    onToggleArchiveProject: (project) => {
      void runAction(
        () => setArchived("project", project.key, !(project.is_archived ?? false), project.working_dir),
        "Couldn't update archive state",
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
    await runAction(() => renameSession(target.ref, name), "Couldn't rename session");
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
      toasts.push("error", `Couldn't delete project: ${errorMessage(err)}`);
    }
  }

  const isExpanded = overrideLookup(expandedOverrides);

  return (
    <div className={hostedInSheet ? `${CLASS.rail} ${CLASS.hosted}` : CLASS.rail}>
      {!hostedInSheet && (
        <div className={CLASS.header}>
          <h2 className={CLASS.title}>Sessions</h2>
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
      )}
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
            <RailSection
              title="Needs you"
              nodes={sessionNodes(tree.needs_you, isExpanded)}
              onToggle={handleToggle}
              onActivate={handleActivate}
              actions={rowActions}
            />
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
            {tree.archived_projects.length > 0 && (
              <ArchivedSection
                open={archivedOpen}
                onToggleOpen={() => setArchivedOpen((v) => !v)}
                nodes={archivedProjectNodes(tree.archived_projects, projectDetails, isExpanded)}
                onToggle={handleToggle}
                onActivate={handleActivate}
                actions={rowActions}
              />
            )}
            <RailSection
              title="Test runs"
              nodes={projectNodes(tree.test_runs, isExpanded)}
              onToggle={handleToggle}
              onActivate={handleActivate}
              actions={rowActions}
            />
          </>
        )}
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
