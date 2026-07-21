// Rail is the workspace shell's sidebar: session tree over stores/tree.ts,
// mounted by AppShell as a plain sibling of DockHost (see this task's
// report for the exact mount contract - AppShell.tsx is out of this task's
// scope to edit). Owns its own collapse/expand chrome, per-branch expand
// state, and the rename/delete-project confirmation dialogs; every mutation
// goes through actions.ts, refetching the tree on success and toasting on
// failure (no optimistic UI - out of this task's scope).
import { type ChangeEvent, useEffect, useState } from "react";
import {
  type TreeNode as ApiTreeNode,
  type TreeProject as ApiTreeProject,
  type TreeResponse,
  treeStore,
  useTreeStore,
} from "../../stores/tree";
import {
  Badge,
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
import { archivedProjectNodes, overrideLookup, projectNodes, type RailNode, sessionNodes } from "./railNodes";

const CLASS = {
  rail: requireClass(styles.rail, "Rail.module.css", "rail"),
  header: requireClass(styles.header, "Rail.module.css", "header"),
  title: requireClass(styles.title, "Rail.module.css", "title"),
  body: requireClass(styles.body, "Rail.module.css", "body"),
  section: requireClass(styles.section, "Rail.module.css", "section"),
  sectionTitle: requireClass(styles.sectionTitle, "Rail.module.css", "sectionTitle"),
  sectionDisclosure: requireClass(styles.sectionDisclosure, "Rail.module.css", "sectionDisclosure"),
  railCollapsed: requireClass(styles.railCollapsed, "Rail.module.css", "railCollapsed"),
  reopen: requireClass(styles.reopen, "Rail.module.css", "reopen"),
  dialogField: requireClass(styles.dialogField, "Rail.module.css", "dialogField"),
  dialogActions: requireClass(styles.dialogActions, "Rail.module.css", "dialogActions"),
};

const COLLAPSED_STORAGE_KEY = "serf.rail.collapsed.v1";

function readCollapsed(): boolean {
  try {
    return localStorage.getItem(COLLAPSED_STORAGE_KEY) === "1";
  } catch {
    return false; // localStorage unavailable (Safari private mode, etc.): default open
  }
}

function persistCollapsed(collapsed: boolean): void {
  try {
    localStorage.setItem(COLLAPSED_STORAGE_KEY, collapsed ? "1" : "0");
  } catch {
    // Best-effort, mirrors DockHost.tsx's own persistLayout precedent - a
    // full quota must never be fatal to the rail itself.
  }
}

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

export function Rail() {
  const tree = useTreeStore((s) => s.tree);
  const loading = useTreeStore((s) => s.loading);
  const error = useTreeStore((s) => s.error);
  const projectDetails = useTreeStore((s) => s.projectDetails);
  const toasts = useToasts();

  const [collapsed, setCollapsed] = useState(readCollapsed);
  const [expandedOverrides, setExpandedOverrides] = useState<ReadonlyMap<string, boolean>>(new Map());
  const [archivedOpen, setArchivedOpen] = useState(false);
  const [renameTarget, setRenameTarget] = useState<ApiTreeNode | null>(null);
  const [renameValue, setRenameValue] = useState("");
  const [deleteTarget, setDeleteTarget] = useState<ApiTreeProject | null>(null);

  useEffect(() => {
    void treeStore.getState().refresh();
  }, []);

  function toggleCollapsed() {
    setCollapsed((prev) => {
      const next = !prev;
      persistCollapsed(next);
      return next;
    });
  }

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

  if (collapsed) {
    const needsYouCount = tree?.attentionSummary.needsYou ?? 0;
    const label = needsYouCount > 0 ? `Show sidebar (${needsYouCount} need attention)` : "Show sidebar";
    return (
      <div className={CLASS.railCollapsed}>
        <button type="button" className={CLASS.reopen} aria-label={label} onClick={toggleCollapsed}>
          {needsYouCount > 0 ? <Badge count={needsYouCount} tone="attention" /> : <span aria-hidden="true">{"»"}</span>}
        </button>
      </div>
    );
  }

  const isExpanded = overrideLookup(expandedOverrides);

  return (
    <div className={CLASS.rail}>
      <div className={CLASS.header}>
        <h2 className={CLASS.title}>Sessions</h2>
        <IconButton
          label="Hide sidebar"
          icon={<span aria-hidden="true">{"«"}</span>}
          variant="quiet"
          size="sm"
          onClick={toggleCollapsed}
        />
      </div>
      <div className={CLASS.body}>
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
