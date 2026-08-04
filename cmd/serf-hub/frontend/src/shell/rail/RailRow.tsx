// RailRow is the Tree widget's renderRow implementation for the sidebar:
// given one RailNode (railNodes.ts) and the TreeRowInfo the Tree widget
// computed for it (depth/expanded/hasChildren/toggle/activate), it renders
// a reserved chevron gutter (filled only on a branch row), a signal dot
// rendered only for the states worth spotting (SIGNAL_STATES - no slot is
// held when there is no dot, see Signal), a text column, a favorite star /
// attention Badge as applicable, a right-aligned relative timestamp (session
// rows only, when there's no Badge to show instead), and an actions Menu
// overlaid on that timestamp. Pure presentation: every mutation goes back
// out through the `actions` prop, which Rail.tsx implements against
// actions.ts + the tree store's refresh().
//
// The rail is a TRIAGE surface: who needs me, nothing else. A quiet session
// (idle, ended, notLoaded) is one line - title + age - because the empty signal
// gutter and a grey age already say nothing is happening. Only a signal state
// (working / needs-you / failed) earns the second line, which glosses why. So
// rows change height as sessions change state; see SessionRow's own comment for
// why that trade is deliberate. The one other thing that earns a second line
// regardless of state is a row's project name, on a session shown flat across
// projects (Live/Pinned, depth 0) - see SessionRow's showsProject.
//
// CLASS.actions (Rail.module.css) is what makes the "..." trigger (and a
// project row's "+") quiet: transparent/borderless by default, revealed only
// on row hover/focus, matching the design bar (Linear/VS Code-quality
// sidebar - quiet, hover-revealed, zero layout shift) instead of a
// permanently-visible bordered button on every row. They overlay the row's
// right edge (over the timestamp/Badge) rather than occupying a slot beside
// it, so revealing them costs this row's own content zero width. See
// Rail.module.css's own comment on .actions for the exact selectors (row
// hover, treeitem focus, open-menu, and the <900px touch fallback that
// keeps it visible - in flow, not overlaid - with no hover to reveal it).
import type { ReactNode } from "react";
import type { TreeNode as ApiTreeNode, TreeProject as ApiTreeProject } from "../../stores/tree";
import {
  Badge,
  Cadence,
  type CadenceState,
  Chevron,
  IconButton,
  Menu,
  type MenuItem,
  type TreeRowInfo,
} from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { navigate } from "../routing";
import styles from "./Rail.module.css";
import {
  type InactiveFoldRailNode,
  needsYouDescendantCount,
  type OverflowRailNode,
  type ProjectRailNode,
  type RailNode,
  type SessionRailNode,
  workingDescendantCount,
} from "./railNodes";

const CLASS = {
  row: requireClass(styles.row, "Rail.module.css", "row"),
  actions: requireClass(styles.actions, "Rail.module.css", "actions"),
  chevron: requireClass(styles.chevron, "Rail.module.css", "chevron"),
  chevronButton: requireClass(styles.chevronButton, "Rail.module.css", "chevronButton"),
  signal: requireClass(styles.signal, "Rail.module.css", "signal"),
  textCol: requireClass(styles.textCol, "Rail.module.css", "textCol"),
  label: requireClass(styles.label, "Rail.module.css", "label"),
  activity: requireClass(styles.activity, "Rail.module.css", "activity"),
  activityAlive: requireClass(styles.activityAlive, "Rail.module.css", "activityAlive"),
  activityAttention: requireClass(styles.activityAttention, "Rail.module.css", "activityAttention"),
  activityDanger: requireClass(styles.activityDanger, "Rail.module.css", "activityDanger"),
  time: requireClass(styles.time, "Rail.module.css", "time"),
  notStarted: requireClass(styles.notStarted, "Rail.module.css", "notStarted"),
  star: requireClass(styles.star, "Rail.module.css", "star"),
  loadingRow: requireClass(styles.loadingRow, "Rail.module.css", "loadingRow"),
  overflow: requireClass(styles.overflow, "Rail.module.css", "overflow"),
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

// The humanized wire state a row's second line leads with (§2.3) - the same
// wire state vocabulary cadenceStateFor reads, worded for a person rather
// than mapped to a Cadence family.
//
// "awaiting" itself splits on askPending: hubapi.StateWord (hubapi/
// attention.go, Track A §2 ask-tiering) already draws this same line for the
// TUI and the older web surface - "Question waiting" when the agent is
// genuinely blocked on an answer, "Your move" when a turn simply ended with
// nothing further queued - because those are different urgencies wearing the
// identical amber dot. This rail's own row never read askPending before,
// so every "awaiting" row rendered as the same generic "waiting on you" -
// a person scanning the list for the one session that's actually blocked on
// them had to open every amber row to find out which. Lowercased to match
// this line's existing casing ("working"/"failed"/"idle"), not the Go
// vocabulary's sentence case verbatim.
//
// "warning" gets its own word for the same reason (kata 59mx): StateWord
// already gives it a dedicated "Warning", distinct from either awaiting
// band, so a warning row reading as generic "waiting on you" was this
// gloss never having read that vocabulary for this state either - the same
// gap ask_pending closed for "awaiting" above. Sharing Cadence's "needs-you"
// dot family (cadenceStateFor) is still correct: that comment's own text
// says only the dot family is shared by design, never the word.
function humanizeState(wireState: string, askPending: boolean): string {
  switch (wireState) {
    case "active":
      return "working";
    case "awaiting":
      return askPending ? "question waiting" : "your move";
    case "warning":
      return "warning";
    case "errored":
      return "failed";
    case "ended":
      return "ended";
    default: // "idle", "notLoaded", "", and any future/unknown value
      return "idle";
  }
}

// The Cadence states worth spending a dot on: a row is working, a human is
// needed, or something failed. idle/ended are deliberately absent - a sidebar
// full of identical grey dots trains the eye to ignore the one dot that
// matters, and an EMPTY gutter beside a grey age already reads as "nothing
// happening here" without a glyph asserting it. This is the RAIL asking for
// less, not the widget changing: every other Cadence surface still renders all
// five states.
//
// This set is also what decides whether a row gets its gloss line at all (see
// SessionRow): the dot and the second line answer the same question, so they
// appear and disappear together.
const SIGNAL_STATES: ReadonlySet<CadenceState> = new Set<CadenceState>(["working", "needs-you", "failed"]);

// kata zq7g: the gloss line's own text color, one family per SIGNAL_STATES
// member - mirrors Cadence's private STATE_FAMILY table (cadence/index.tsx)
// exactly, duplicated locally rather than shared, matching the precedent
// StatusDot's own copy already set (that widget's doc comment explains why
// Cadence's mapping stays unexported: its directory is out of scope for
// callers that want the same state->family judgment elsewhere). idle/ended
// never reach this - they never render a gloss line at all (see
// SessionRow's showsGloss) - so there is no "neutral" entry to carry.
const ACTIVITY_FAMILY_CLASS: Partial<Record<CadenceState, string>> = {
  working: CLASS.activityAlive,
  "needs-you": CLASS.activityAttention,
  failed: CLASS.activityDanger,
};

// RowGutter is a leading slot on a row. The CHEVRON gutter is always
// rendered, empty whenever this row has nothing to put in it: it is
// conditionally FILLED, and a slot that disappears when empty moves every
// title after it. Reserving the chevron width unconditionally is what makes
// one x-position hold for every title in the list, at every nesting depth,
// regardless of whether a row has children. (The signal slot is different -
// it renders only when it has a dot, by explicit request: see Signal.)
function RowGutter({ className, testId, children }: { className: string; testId: string; children?: ReactNode }) {
  return (
    <span data-testid={testId} className={className}>
      {children}
    </span>
  );
}

// The row's leading signal dot, shared by session and project rows. Renders
// ONLY for a signal state - working / needs-you / failed - and holds no
// space otherwise: the 2026-07-31 sidebar-density pass ended the old
// always-reserved 6px slot, so a quiet row's title now starts one slot
// further left than a signal row's (state already moves the row's height via
// the gloss line; letting it move the title's x too is the density trade the
// reserved slot used to prevent, made deliberately).
function Signal({ wireState }: { wireState: string }) {
  const state = cadenceStateFor(wireState);
  if (!SIGNAL_STATES.has(state)) return null;
  return (
    <RowGutter className={CLASS.signal} testId="rail-row-signal">
      <Cadence state={state} frameTimes={NO_FRAME_TIMES} now={INERT_NOW} />
    </RowGutter>
  );
}

// The gloss a SIGNAL row gets: the state in words, plus the branch when the
// session carries one. Rendered only for the states worth spotting from across
// the list (SIGNAL_STATES), which is what earns it the second line.
//
// The model is deliberately NOT here. It is a property of the session, not a
// reason to look at it, and the session pane's own status strip reports it the
// moment you open the row - so on a rail whose whole job is triage it was three
// facts of noise. Tier is likewise gone from the visible line: it survives in
// the row's title tooltip (see SessionRow), where a fact a title cannot carry
// stays reachable without spending a line on it.
//
// Branch stays because it distinguishes SIBLINGS in the case that matters - two
// working sessions in the same project, on different branches - and it is on
// the second line rather than beside the title because as a fixed-width sibling
// on the main line it charged its width to the title at the rail's default
// 280px. Exported for direct testing of the join, which the rendered line can
// only assert on as one flat string.
export function activityGloss(session: ApiTreeNode): string {
  const workingCount = workingDescendantCount(session);
  const parts = [
    workingCount === 0
      ? humanizeState(session.state, session.ask_pending === true)
      : `${workingCount} subagent${workingCount === 1 ? "" : "s"} working`,
  ];
  if (session.branch !== undefined && session.branch !== "") parts.push(session.branch);
  return parts.join(" · ");
}

// secondLine is the row's second line in full: activityGloss above, joined
// with the session's project when the row needs one (kata hxjn). A session
// row only needs its project named when it is rendered FLAT, mixed in with
// other projects' sessions - the Live and Pinned tiers, where a row's own
// nesting depth is 0 (see below). A session nested under its own ProjectRow
// (Projects/Test runs/Archived) is depth >= 1 there and never needs this:
// the project it belongs to is the row it is indented under. Project leads
// the line (state is what's happening, project is where) the same way
// activityGloss already leads with state before branch.
function secondLine(session: ApiTreeNode, showsGloss: boolean, showsProject: boolean): string {
  const parts: string[] = [];
  if (showsProject) parts.push(session.project);
  if (showsGloss) parts.push(activityGloss(session));
  return parts.join(" · ");
}

export interface RailRowActions {
  onPinSectionRequest: (session: ApiTreeNode) => void;
  onUnpinRequest: (session: ApiTreeNode) => void;
  onToggleArchiveSession: (session: ApiTreeNode) => void;
  onRenameRequest: (session: ApiTreeNode) => void;
  onDeleteSessionRequest: (session: ApiTreeNode) => void;
  onToggleFavoriteProject: (project: ApiTreeProject) => void;
  onToggleArchiveProject: (project: ApiTreeProject) => void;
  onDeleteProjectRequest: (project: ApiTreeProject) => void;
}

export interface RailRowProps {
  node: RailNode;
  info: TreeRowInfo;
  actions: RailRowActions;
}

// The row's leading chevron slot: a toggle on a branch row, an empty reserved
// gutter on a leaf. The GUTTER is unconditional and the BUTTON is not, for the
// same reason Signal's dot is conditional inside an unconditional slot - a leaf
// row that rendered nothing at all here gained no chevron width, so its title
// started a chevron further left than its branch siblings' and the list read as
// a ragged extra indent on whichever rows happened to have children.
function ChevronGutter({ info }: { info: TreeRowInfo }) {
  return (
    <RowGutter className={CLASS.chevron} testId="rail-row-chevron-gutter">
      {info.hasChildren && (
        // Decorative mouse shortcut for the same action Left/Right arrow
        // already performs on the treeitem itself (see widgets/tree's own doc
        // comment and dev/gallery-sections/tree.tsx's identical convention) -
        // out of tab order and hidden from assistive tech so it isn't a second,
        // redundant "toggle" announcement.
        <button
          type="button"
          data-testid="rail-chevron"
          className={CLASS.chevronButton}
          aria-hidden="true"
          tabIndex={-1}
          onClick={info.toggle}
        >
          <Chevron direction={info.expanded ? "down" : "right"} />
        </button>
      )}
    </RowGutter>
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
      variant="quiet"
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

// Archive is a decision about a TOP-LEVEL row, so only a top-level row
// offers it. hubcore's nodeKind (internal/hubcore/tree.go) names the kinds
// that are never top-level: "subagent" (nested under its parent) and "fork"
// (a snapshotted original nested under the branch that superseded it) - the
// same two nestedSessionIDs computes - plus the synthetic "cluster" fold row,
// which stands for a group of sessions rather than being one. Everything else
// is a real top-level session. Written as an exclusion list rather than
// `=== "session"` so an unrecognized future kind still gets the action rather
// than silently losing it.
const NESTED_KINDS: ReadonlySet<string> = new Set(["subagent", "fork", "cluster"]);

export function isTopLevelSession(session: ApiTreeNode): boolean {
  return !NESTED_KINDS.has(session.kind);
}

function sessionMenuItems(session: ApiTreeNode, actions: RailRowActions): MenuItem[] {
  const items: MenuItem[] = [];
  // Pinning, like archiving, is a decision about a top-level row. The Pinned
  // tier is built only from a project's top-level Current+Recent sessions
  // (web_api_tree.go), so pinning a nested row writes a decision that never
  // surfaces anywhere; pinning a synthetic cluster row writes one keyed to an
  // id that names no session at all. Both confirmed against a live hub.
  //
  // Rename needs no check here: the server already withholds its `rename`
  // flag from every nested and synthetic node, and the item below is gated on
  // it - the request 404s for those ids, and the menu never offers it.
  if (isTopLevelSession(session)) {
    // A pinned session gets a direct Unpin, not a "move" flow: moving between
    // sections is unpin + pin, and the one-gesture action is what the row
    // menu owes. Only an unpinned session opens the section picker.
    if (session.pin_section_id) {
      items.push({
        id: "unpin",
        label: "Unpin",
        onSelect: () => actions.onUnpinRequest(session),
      });
    } else {
      items.push({
        id: "pin-section",
        label: "Pin this session…",
        onSelect: () => actions.onPinSectionRequest(session),
      });
    }
  }
  if (session.rename) {
    items.push({ id: "rename", label: "Rename", onSelect: () => actions.onRenameRequest(session) });
  }
  if (isTopLevelSession(session)) {
    items.push({
      id: "archive",
      // The wire has no direct "is this session archived" boolean - tier is
      // the closest available signal, and is itself decision-driven when an
      // explicit archive decision exists (see hubcore.classifySession).
      label: session.tier === "archived" ? "Unarchive" : "Archive",
      onSelect: () => actions.onToggleArchiveSession(session),
    });
  }
  // Delete (kata n15j) targets a stable LOCAL session ref
  // (identifier.ValidateSessionID via web_api_session_delete.go) - offering
  // it for a remote-source session would only ever 400. No client-side
  // liveness check: a live session is offered the same as an ended one, and
  // the server refuses via the same skipped/toast path deleteProject already
  // uses for a session that raced back to live, rather than this menu
  // duplicating the server's own crash-vs-live predicate (kata 8at6).
  if (isTopLevelSession(session) && session.host_id === "local") {
    items.push({ id: "delete", label: "Delete…", onSelect: () => actions.onDeleteSessionRequest(session) });
  }
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

// Opens a fresh spawn targeted at this project's working directory, via the
// same /new?dir= URL prefill the palette's "Spawn with prompt" command
// already uses for /new?prompt= (shell/palette/commands.ts): Spawn.tsx reads
// both off window.location.search (panes/spawn/urlPrefill.ts), never pane
// params - the spawn pane's own params type is deliberately empty (see
// panes/spawn/Spawn.tsx), so a URL prefill is the only way to hand it a
// directory. Falls back to a bare /new when a project has no working_dir
// (shouldn't happen for a real project, but degrades gracefully rather than
// silently doing nothing) - NO_PROJECT_KEY itself is excluded before this is
// ever called, same as every other project-scoped action here.
function spawnInProject(project: ApiTreeProject): void {
  navigate(project.working_dir ? `/new?dir=${encodeURIComponent(project.working_dir)}` : "/new");
}

function projectMenuItems(project: ApiTreeProject, actions: RailRowActions): MenuItem[] {
  if (project.key === NO_PROJECT_KEY) return [];
  return [
    {
      id: "new-session",
      label: "New session",
      onSelect: () => spawnInProject(project),
    },
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

// rowTooltip is the title on a row's own label: the session title, plus the
// facts the visible row no longer spends space on. A quiet row's dropped state
// word and every row's tier land here - real information a title cannot carry,
// reachable on hover without costing the list a line. The title always leads, so
// a truncated title is still recoverable from it (the case this tooltip
// originally existed for).
function rowTooltip(session: ApiTreeNode, showsGloss: boolean, saysNotStarted: boolean): string {
  const parts = [session.title];
  // A signal row already prints its state; a quiet one doesn't, so only the
  // quiet case needs the word here. A row that has never run reports THAT
  // instead: "idle" is true of it but tells the reader nothing they don't
  // already believe, and it is the very confusion this line exists to end.
  if (saysNotStarted) parts.push("not started");
  else if (!showsGloss) parts.push(humanizeState(session.state, session.ask_pending === true));
  // "current" is the unremarkable default state of a session - the same
  // exclusion the visible line used to make.
  if (session.tier !== undefined && session.tier !== "" && session.tier !== "current") parts.push(session.tier);
  // A dormant row spends its right slot on "Not started" instead of the age,
  // so the age lands here - the same contract every other fact this row gives
  // up is held to.
  if (saysNotStarted && session.age !== undefined && session.age !== "") parts.push(session.age);
  return parts.join(" · ");
}

// saysNotStarted decides whether a row leads with "this has never run".
//
// Dormancy is a fact about a session's HISTORY; the state is a fact about what
// it is doing now. When those two compete for one slot the state wins: a
// dormant session handed a prompt a second ago is genuinely working, and a row
// still calling it "Not started" would be flatly wrong. So this is only ever
// true on a row that is otherwise quiet - which is exactly the row that had
// nothing to say before.
function saysNotStarted(session: ApiTreeNode, showsGloss: boolean): boolean {
  return session.dormant === true && !showsGloss;
}

function SessionRow({ node, info, actions }: { node: SessionRailNode; info: TreeRowInfo; actions: RailRowActions }) {
  const { session } = node;
  const needsYouCount = needsYouDescendantCount(session);
  // A quiet row (idle, ended, notLoaded, unknown) is title + age, one line: the
  // empty signal gutter and a grey age already say "nothing is happening here",
  // so a second line restating "idle" in words was the state living at two
  // altitudes on the row whose whole job is triage. A signal row keeps its
  // gloss, and with it its second line - which makes signal rows physically
  // taller than quiet ones. That is the point: the rows worth finding are bigger
  // than the rows that aren't, and the list's evenness is worth less than that.
  const showsGloss = SIGNAL_STATES.has(cadenceStateFor(session.state));
  const hasWorkingDescendants = workingDescendantCount(session) > 0;
  // kata hxjn: a row at depth 0 is a top-level entry in a flat, cross-project
  // tier (Live/Pinned - see toSessionNode/sessionNodes; a Projects/Test-runs/
  // Archived session is always nested under its own ProjectRow, never a depth-0
  // SessionRow). Cross-referencing which project a Live row belongs to used to
  // mean leaving the rail entirely, so those rows get a second line even when
  // otherwise quiet - the one exception to the "quiet row is one line" rule
  // above, made for exactly the fact that rule can't otherwise carry.
  const showsProject = info.depth === 0;
  const notStarted = saysNotStarted(session, showsGloss);
  const showsActivity = showsGloss || hasWorkingDescendants;
  const gloss = secondLine(session, showsActivity, showsProject);
  const showsSecondLine = showsActivity || showsProject;
  // Only a genuine signal row (showsGloss) carries a state to tint - the
  // depth-0-only "just the project name" line (showsProject with no signal)
  // has no state family to color, so it stays the plain --ink-low default.
  const activityClass = showsGloss
    ? `${CLASS.activity} ${ACTIVITY_FAMILY_CLASS[cadenceStateFor(session.state)] ?? ""}`.trim()
    : CLASS.activity;
  return (
    // data-session-ref is the scroll target Rail's reveal effect (the palette's
    // /project command via railController) queries to bring a session's row
    // into view - the ref is stable and unique per session, unlike the label.
    <span className={CLASS.row} data-session-ref={session.ref}>
      <ChevronGutter info={info} />
      <Signal wireState={session.state} />
      {/* The text column: the title, and - on a signal row only - a second line
          glossing why it wants attention. Row anatomy for the subagent tree's
          already-existing recursion (toSessionNode/Tree), not new recursion of
          its own. */}
      {/* biome-ignore lint/a11y/noStaticElementInteractions: redundant with the row's own Enter handling, see below */}
      {/* biome-ignore lint/a11y/useKeyWithClickEvents: redundant with the row's own Enter handling, see below */}
      <span className={CLASS.textCol} onClick={info.activate}>
        {/* Mouse-only shortcut for the same activation Enter already performs
            on the owning treeitem - can't use aria-hidden the way Chevron
            does, since this text IS the treeitem's accessible name (no
            separate aria-label on the row). */}
        {/* Both lines ellipsize, so both carry their own full text as a
            native tooltip - nothing a narrow rail cuts off becomes
            unreachable. The title's tooltip also carries what the visible row
            drops (rowTooltip). */}
        <span className={CLASS.label} title={rowTooltip(session, showsGloss, notStarted)}>
          {session.title}
        </span>
        {showsSecondLine && (
          <span data-testid="rail-row-activity" className={activityClass} title={gloss}>
            {gloss}
          </span>
        )}
      </span>
      {/* Gated on the same rule as the pin action: the wire can still carry
          favorite:true on a nested or synthetic node (a decision written
          before pinning was scoped, or a direct API call), and a star on a row
          whose menu offers no way to remove it is a dead end. Depth 0 rows -
          the flat Live and named-pin-section tiers - never carry it at all:
          being listed in those sections already says the session is pinned,
          so the star there is redundancy, not information. */}
      {session.pin_section_id !== undefined && isTopLevelSession(session) && info.depth > 0 && (
        <span data-testid="favorite-star" aria-hidden="true" className={CLASS.star}>
          {"★"}
        </span>
      )}
      {/* Right slot: either the Task-7 needs-you-descendant Badge, or (when
          there's nothing to flag) a relative timestamp - never both, so the
          slot never widens the row by more than one small element (vbh8
          new capability, §2.3). The session's OWN needs-you already shows
          via its amber Cadence dot above (cadenceStateFor maps
          awaiting/warning to "needs-you"), so a leaf needs-you session with
          no needs-you descendants correctly shows its timestamp here, not a
          redundant "0"/"1" badge. */}
      {needsYouCount > 0 ? (
        <Badge count={needsYouCount} tone="attention" />
      ) : notStarted ? (
        // Words, not a number: a session that has never run has no elapsed
        // work to report, and the age this slot would otherwise show is
        // counting from the moment it was created - which reads as activity
        // and is the single most misleading thing on the row. Saying so also
        // gives the row an accessible name that answers the question a
        // returning user actually has ("did I already ask it something?"),
        // which an empty signal gutter never could.
        <span data-testid="rail-row-not-started" className={CLASS.notStarted}>
          Not started
        </span>
      ) : (
        session.age !== undefined &&
        session.age !== "" && (
          <span data-testid="rail-row-time" className={CLASS.time}>
            {session.age}
          </span>
        )
      )}
      <span className={CLASS.actions}>
        <ActionsMenu label={session.title} items={sessionMenuItems(session, actions)} />
      </span>
    </span>
  );
}

function ProjectRow({ node, info, actions }: { node: ProjectRailNode; info: TreeRowInfo; actions: RailRowActions }) {
  const { project } = node;
  const attentionCount = project.rollup_attn ?? 0;
  return (
    <span className={CLASS.row}>
      <ChevronGutter info={info} />
      <Signal wireState={project.rollup_state ?? "idle"} />
      {/* Same reasoning as SessionRow's own label above. */}
      {/* biome-ignore lint/a11y/noStaticElementInteractions: redundant with the row's own Enter handling, see SessionRow */}
      {/* biome-ignore lint/a11y/useKeyWithClickEvents: redundant with the row's own Enter handling, see SessionRow */}
      <span className={CLASS.label} onClick={info.activate}>
        {project.name}
      </span>
      {project.favorite === true && (
        <span data-testid="favorite-star" aria-hidden="true" className={CLASS.star}>
          {"★"}
        </span>
      )}
      {attentionCount > 0 && <Badge count={attentionCount} tone="attention" />}
      <span className={CLASS.actions}>
        {project.key !== NO_PROJECT_KEY && (
          <IconButton
            label={`New session in ${project.name}`}
            icon={<span aria-hidden="true">{"+"}</span>}
            variant="quiet"
            size="sm"
            tabIndex={-1}
            onClick={() => spawnInProject(project)}
          />
        )}
        <ActionsMenu label={project.name} items={projectMenuItems(project, actions)} />
      </span>
    </span>
  );
}

// The "Inactive subagents (N)" disclosure (parity-m3-sidebar-tree.md §3).
// Built from the same chevron gutter every other row uses, so its title sits
// where a childless row's title sits - and no signal slot at all, matching
// Signal's own render-only-when-dotted contract (a group of finished
// sessions has no state to report). No actions menu either: it stands for
// rows rather than being one, and the rows it hides carry their own.
function InactiveFoldRow({ node, info }: { node: InactiveFoldRailNode; info: TreeRowInfo }) {
  const label = `${node.count === 1 ? "Inactive subagent" : "Inactive subagents"} (${node.count})`;
  return (
    <span className={CLASS.row}>
      <ChevronGutter info={info} />
      {/* Same mouse-only shortcut for the toggle the chevron already offers,
          and the same a11y reasoning as SessionRow's own label: this text is
          the treeitem's accessible name, so it can't be aria-hidden. */}
      {/* biome-ignore lint/a11y/noStaticElementInteractions: redundant with the row's own Enter handling, see SessionRow */}
      {/* biome-ignore lint/a11y/useKeyWithClickEvents: redundant with the row's own Enter handling, see SessionRow */}
      <span className={CLASS.label} onClick={info.toggle}>
        {label}
      </span>
    </span>
  );
}

// The "+N older" note for rows the server capped away (hubcore's
// maxSidebarSessionsPerTier). Reserves the chevron gutter like every other
// row so it lines up with the list it belongs to; no signal slot (Signal's
// contract: no dot, no space). Project overflow rows activate a bounded
// fetch for the capped-away tier rows; synthetic child overflow remains an
// honest non-actionable count.
function OverflowRow({ node, info }: { node: OverflowRailNode; info: TreeRowInfo }) {
  return (
    <span className={CLASS.row}>
      <RowGutter className={CLASS.chevron} testId="rail-row-chevron-gutter" />
      {/* The treeitem's Enter handler is the keyboard path; this click makes
          the visible affordance usable with a mouse as well. */}
      {/* biome-ignore lint/a11y/noStaticElementInteractions: treeitem owns keyboard activation and accessible semantics */}
      {/* biome-ignore lint/a11y/useKeyWithClickEvents: treeitem owns keyboard activation */}
      <span
        data-testid="rail-row-overflow"
        className={CLASS.overflow}
        onClick={info.activate}
      >{`+${node.count} older`}</span>
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
    case "inactiveFold":
      return <InactiveFoldRow node={node} info={info} />;
    case "overflow":
      return <OverflowRow node={node} info={info} />;
    case "project":
      return <ProjectRow node={node} info={info} actions={actions} />;
    case "session":
      return <SessionRow node={node} info={info} actions={actions} />;
  }
}
