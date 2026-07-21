// RailRow is the Tree widget's renderRow implementation for the sidebar:
// given one RailNode (railNodes.ts) and the TreeRowInfo the Tree widget
// computed for it (depth/expanded/hasChildren/toggle/activate), it renders
// a chevron (branches only), a Cadence liveness dot, the row's label, a
// favorite star / secondary tier tag / attention Badge as applicable, and
// an actions Menu. Pure presentation: every mutation goes back out through
// the `actions` prop, which Rail.tsx implements against actions.ts + the
// tree store's refresh().
import type { ReactNode } from "react";
import type { TreeNode as ApiTreeNode, TreeProject as ApiTreeProject } from "../../stores/tree";
import { Badge, Cadence, type CadenceState, Menu, type MenuItem, type TreeRowInfo } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import styles from "./Rail.module.css";
import type { ProjectRailNode, RailNode, SessionRailNode } from "./railNodes";

const CLASS = {
  row: requireClass(styles.row, "Rail.module.css", "row"),
  chevron: requireClass(styles.chevron, "Rail.module.css", "chevron"),
  label: requireClass(styles.label, "Rail.module.css", "label"),
  meta: requireClass(styles.meta, "Rail.module.css", "meta"),
  star: requireClass(styles.star, "Rail.module.css", "star"),
  loadingRow: requireClass(styles.loadingRow, "Rail.module.css", "loadingRow"),
  srOnly: requireClass(styles.srOnly, "Rail.module.css", "srOnly"),
};

// frameTimes is always [] here: the REST /api/tree snapshot carries no
// per-frame timestamps, only a point-in-time `state`. Cadence still renders
// correctly with an empty trace (just the state dot, no ticks) - wave-4's
// live-socket enrichment is what will thread real frame arrivals through
// for sessions the rail is currently showing, at which point this becomes
// a real frameTimes array instead of a permanent [].
const NO_FRAME_TIMES: number[] = [];
// Inert with an empty frameTimes (see ticksFor in widgets/cadence): every
// tick is filtered by age-vs-now, and there are no ticks to filter. Fixed
// rather than Date.now() so this component never re-renders for a clock
// tick it has nothing to show for.
const INERT_NOW = 0;

// Maps hubcore's normalized session state (cmd/serf-hub/internal/hubcore/
// tree.go's NormalizeState / the State field's own doc comment: "errored" |
// "awaiting" | "active" | "warning" | "idle" | "ended", plus a "notLoaded"
// fallback) onto Cadence's four-family state space. "awaiting" is exactly
// what makes a row NeedsYou-eligible server-side, so it maps to
// "needs-you"; "warning" has no dedicated Cadence family (attention/alive/
// danger/neutral) and is the next rung down from "active" in
// hubapi.AttentionRank, so it shares "needs-you" rather than downgrading to
// neutral. Exported for direct testing - this mapping is exactly the kind
// of one-to-many judgment call worth pinning down explicitly.
export function cadenceStateFor(wireState: string): CadenceState {
  switch (wireState) {
    case "errored":
      return "failed";
    case "awaiting":
    case "warning":
      return "needs-you";
    case "active":
      return "working";
    case "ended":
      return "ended";
    default: // "idle", "notLoaded", "", and any future/unknown value
      return "idle";
  }
}

export interface RailRowActions {
  onToggleFavorite: (session: ApiTreeNode) => void;
  onToggleArchiveSession: (session: ApiTreeNode) => void;
  onRenameRequest: (session: ApiTreeNode) => void;
  onToggleFavoriteProject: (project: ApiTreeProject) => void;
  onToggleArchiveProject: (project: ApiTreeProject) => void;
  onDeleteProjectRequest: (project: ApiTreeProject) => void;
}

export interface RailRowProps {
  node: RailNode;
  info: TreeRowInfo;
  actions: RailRowActions;
}

function Chevron({ expanded, onToggle }: { expanded: boolean; onToggle: () => void }) {
  return (
    // Decorative mouse shortcut for the same action Left/Right arrow
    // already performs on the treeitem itself (see widgets/tree's own doc
    // comment and dev/gallery-sections/tree.tsx's identical convention) -
    // out of tab order and hidden from assistive tech so it isn't a second,
    // redundant "toggle" announcement.
    <button
      type="button"
      data-testid="rail-chevron"
      className={CLASS.chevron}
      aria-hidden="true"
      tabIndex={-1}
      onClick={onToggle}
    >
      {expanded ? "▾" : "▸"}
    </button>
  );
}

function ActionsMenu({ label, items }: { label: string; items: MenuItem[] }) {
  // No items (e.g. the synthetic "(no project)" bucket - see
  // NO_PROJECT_KEY below) means nothing here is actionable; an empty
  // dropdown button would be worse than no button at all.
  if (items.length === 0) return null;
  return (
    <Menu
      // Same reasoning as Chevron's own tabIndex={-1} above: the row's
      // single outer treeitem is the Tree widget's one roving Tab stop -
      // without this, the trigger becomes a SECOND, always-focusable Tab
      // stop on every row simultaneously, breaking that contract (Tab
      // would reach "Actions for Row B" without ever reaching Row B's own
      // treeitem). Still reachable by click; Menu's own consume-then-stop
      // key handling (widgets/menu/index.tsx) is the other half of this -
      // an ArrowDown/Enter/Space this trigger already gives meaning to
      // must never also bubble into Tree's onKeyDown and move the roving
      // tabindex to a different row out from under an open menu.
      triggerTabIndex={-1}
      trigger={
        <>
          <span aria-hidden="true">{"⋯"}</span>
          <span className={CLASS.srOnly}>{`Actions for ${label}`}</span>
        </>
      }
      items={items}
    />
  );
}

function sessionMenuItems(session: ApiTreeNode, actions: RailRowActions): MenuItem[] {
  const items: MenuItem[] = [
    {
      id: "favorite",
      label: session.favorite ? "Remove from pinned" : "Add to pinned",
      onSelect: () => actions.onToggleFavorite(session),
    },
  ];
  if (session.rename) {
    items.push({ id: "rename", label: "Rename", onSelect: () => actions.onRenameRequest(session) });
  }
  items.push({
    id: "archive",
    // The wire has no direct "is this session archived" boolean - tier is
    // the closest available signal, and is itself decision-driven when an
    // explicit archive decision exists (see hubcore.classifySession).
    label: session.tier === "archived" ? "Unarchive" : "Archive",
    onSelect: () => actions.onToggleArchiveSession(session),
  });
  return items;
}

// "no-project" is a synthetic bucket handleAPITree synthesizes for orphan
// live sessions with no resolvable project (cmd/serf-hub/web_api_tree.go) -
// it can appear in the wire's `projects` array like any other TreeProject,
// but the server rejects both archive and delete for this exact key
// ("no-project is not a local project" - web_api_archive.go/
// web_api_project_delete.go). Offering menu items that are guaranteed to
// fail server-side would be worse than offering none - kept as an
// all-or-nothing exclusion (favorite included) rather than special-casing
// per action, since POST /api/favorite's own project-kind validation is a
// separate, disclosed gap (unrelated to this row's own scope) that this
// component has no reliable way to distinguish from "would actually work".
const NO_PROJECT_KEY = "no-project";

function projectMenuItems(project: ApiTreeProject, actions: RailRowActions): MenuItem[] {
  if (project.key === NO_PROJECT_KEY) return [];
  return [
    {
      id: "favorite",
      label: project.favorite ? "Remove from pinned" : "Add to pinned",
      onSelect: () => actions.onToggleFavoriteProject(project),
    },
    {
      id: "archive",
      label: project.is_archived ? "Unarchive project" : "Archive project",
      onSelect: () => actions.onToggleArchiveProject(project),
    },
    {
      id: "delete",
      label: "Delete project…",
      onSelect: () => actions.onDeleteProjectRequest(project),
    },
  ];
}

function SessionRow({ node, info, actions }: { node: SessionRailNode; info: TreeRowInfo; actions: RailRowActions }) {
  const { session } = node;
  return (
    <span className={CLASS.row}>
      {info.hasChildren && <Chevron expanded={info.expanded} onToggle={info.toggle} />}
      <Cadence state={cadenceStateFor(session.state)} frameTimes={NO_FRAME_TIMES} now={INERT_NOW} />
      <span className={CLASS.label} onClick={info.activate}>
        {session.title}
      </span>
      {session.favorite === true && (
        <span data-testid="favorite-star" aria-hidden="true" className={CLASS.star}>
          {"★"}
        </span>
      )}
      {session.branch !== undefined && session.branch !== "" && <span className={CLASS.meta}>{session.branch}</span>}
      {session.tier !== undefined && session.tier !== "current" && session.tier !== "" && (
        <span className={CLASS.meta}>{session.tier}</span>
      )}
      <ActionsMenu label={session.title} items={sessionMenuItems(session, actions)} />
    </span>
  );
}

function ProjectRow({ node, info, actions }: { node: ProjectRailNode; info: TreeRowInfo; actions: RailRowActions }) {
  const { project } = node;
  const attentionCount = project.rollup_attn ?? 0;
  return (
    <span className={CLASS.row}>
      {info.hasChildren && <Chevron expanded={info.expanded} onToggle={info.toggle} />}
      <Cadence state={cadenceStateFor(project.rollup_state ?? "idle")} frameTimes={NO_FRAME_TIMES} now={INERT_NOW} />
      <span className={CLASS.label} onClick={info.activate}>
        {project.name}
      </span>
      {project.favorite === true && (
        <span data-testid="favorite-star" aria-hidden="true" className={CLASS.star}>
          {"★"}
        </span>
      )}
      {attentionCount > 0 && <Badge count={attentionCount} tone="attention" />}
      <ActionsMenu label={project.name} items={projectMenuItems(project, actions)} />
    </span>
  );
}

function LoadingRow(): ReactNode {
  // role="status" so this is announced the same way the top-level Skeleton
  // (widgets/skeleton) is - the visible "Loading…" text is its own
  // accessible name via name-from-content, no separate aria-label needed.
  return (
    <span role="status" className={CLASS.loadingRow}>
      Loading…
    </span>
  );
}

export function RailRow({ node, info, actions }: RailRowProps) {
  switch (node.kind) {
    case "loading":
      return LoadingRow();
    case "project":
      return <ProjectRow node={node} info={info} actions={actions} />;
    case "session":
      return <SessionRow node={node} info={info} actions={actions} />;
  }
}
