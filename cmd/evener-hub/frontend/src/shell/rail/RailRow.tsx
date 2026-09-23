// RailRow is the Tree widget's renderRow implementation for the sidebar:
// given one RailNode (railNodes.ts) and the TreeRowInfo the Tree widget
// computed for it (depth/expanded/hasChildren/toggle/activate), it renders
// a title line (an outdented signal dot rendered only for the states worth
// spotting - SIGNAL_STATES, no slot is held when there is no dot, see
// Signal - the title, and a trailing expand/collapse chevron on branch
// rows, transcript-style), a gloss second line on signal rows, a favorite
// star / attention Badge as applicable, a right-aligned relative timestamp
// (session rows only, when there's no Badge to show instead), and an
// actions Menu overlaid on that timestamp. Pure presentation: every
// mutation goes back out through the `actions` prop, which Rail.tsx
// implements against actions.ts + the tree store's refresh().
//
// The rail is a TRIAGE surface: who needs me, nothing else. A quiet session
// (idle, ended, notLoaded) is one line - title + age - because the empty signal
// gutter and a grey age already say nothing is happening. Only a signal state
// (working / needs-you / failed) earns the second line, which glosses why. So
// rows change height as sessions change state; see SessionRow's own comment for
// why that trade is deliberate. The one other thing that earns a second line
// regardless of state is a row's project name, on a session shown flat across
// projects (Live/Pinned, depth 0) - see SessionRow's showsProject. And since
// watches: a session whose only pending work is an armed watch earns it too, so
// the count is never invisible on exactly the rows where a watch is the only
// thing happening (see SessionRow's showsActivity).
//
// CLASS.actions (RailRow.module.css) is what makes the "..." trigger (and a
// project row's "+") quiet: transparent/borderless by default, revealed only
// on row hover/focus, matching the design bar (Linear/VS Code-quality
// sidebar - quiet, hover-revealed, zero layout shift) instead of a
// permanently-visible bordered button on every row. It lives in the shared
// .rightSlot grid cell with the row's right-slot occupant (timestamp /
// Badge / "Not started"), so revealing it covers the occupant instead of
// narrowing the title column with a reserved slot of its own - and the
// trailing disclosure chevron, which lives in .textCol left of the slot,
// stays layout-disjoint from the menu at every width. See
// RailRow.module.css's own comment on .rightSlot for the exact selectors
// (row hover, treeitem focus, open-menu, and the <900px touch fallback that
// keeps the actions visible beside the occupant - in flow, not stacked -
// with no hover to reveal them).

import {
  canReadSharedNotes,
  humanizeState,
  isActivityFailure,
  watchArmedLabel,
  watchCadenceLabel,
  watchDurationLabel,
  watchGloss,
  watchTitle,
} from "@evener/appwire-client";
import { memo, type ReactNode } from "react";
import { jobStatusDisplay } from "../../panes/session/chrome/activityFormat";
import type { SessionPanelKind } from "../../panes/sessionPanels";
import { LOCAL_HOST } from "../../stores/hostRouting";
import { relativeAge, selectDisplaySources, selectSources } from "../../stores/navigation/selectors";
import { useNavigationStore } from "../../stores/navigation/store";
import { useThreadsStore } from "../../stores/threads";
import { useTopNotesExpanded } from "../../stores/topNotes";
import { Badge, Cadence, type CadenceState, Chevron, IconButton } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { Menu, type MenuItem } from "../../widgets/menu";
import type { TreeRowInfo } from "../../widgets/tree";
import { navigate } from "../routing";
import { type PinTarget, SessionMenu } from "../sessionMenu/SessionMenu";
import { isPaneOpen, useWorkspaceStore } from "../workspace";
import styles from "./RailRow.module.css";
import {
  activeWatchCount,
  activeWorkSummary,
  type CompletedJobsFoldRailNode,
  displayState,
  type HostRailNode,
  type InactiveFoldRailNode,
  type JobRailNode,
  needsYouDescendantCount,
  type OverflowRailNode,
  type ProjectRailNode,
  type RailNode,
  type RailProject,
  type RailSession,
  type SessionRailNode,
  type WatchRailNode,
  watchCountLabel,
} from "./railNodes";
import { useRailNow } from "./railNow";
import { useRailRenderObserver } from "./railRenderObserver";
import { isTopLevelSession } from "./sessionKind";

export { isTopLevelSession } from "./sessionKind";

const CLASS = {
  railRow: requireClass(styles.railRow, "RailRow.module.css", "railRow"),
  rightSlot: requireClass(styles.rightSlot, "RailRow.module.css", "rightSlot"),
  actions: requireClass(styles.actions, "RailRow.module.css", "actions"),
  chevronButton: requireClass(styles.chevronButton, "RailRow.module.css", "chevronButton"),
  signal: requireClass(styles.signal, "RailRow.module.css", "signal"),
  textCol: requireClass(styles.textCol, "RailRow.module.css", "textCol"),
  titleLine: requireClass(styles.titleLine, "RailRow.module.css", "titleLine"),
  label: requireClass(styles.label, "RailRow.module.css", "label"),
  activity: requireClass(styles.activity, "RailRow.module.css", "activity"),
  activityAlive: requireClass(styles.activityAlive, "RailRow.module.css", "activityAlive"),
  activityAttention: requireClass(styles.activityAttention, "RailRow.module.css", "activityAttention"),
  activityDanger: requireClass(styles.activityDanger, "RailRow.module.css", "activityDanger"),
  time: requireClass(styles.time, "RailRow.module.css", "time"),
  notStarted: requireClass(styles.notStarted, "RailRow.module.css", "notStarted"),
  host: requireClass(styles.host, "RailRow.module.css", "host"),
  hostOffline: requireClass(styles.hostOffline, "RailRow.module.css", "hostOffline"),
  hostGlyph: requireClass(styles.hostGlyph, "RailRow.module.css", "hostGlyph"),
  star: requireClass(styles.star, "RailRow.module.css", "star"),
  loadingRow: requireClass(styles.loadingRow, "RailRow.module.css", "loadingRow"),
  overflow: requireClass(styles.overflow, "RailRow.module.css", "overflow"),
  secondLine: requireClass(styles.secondLine, "RailRow.module.css", "secondLine"),
  watchCount: requireClass(styles.watchCount, "RailRow.module.css", "watchCount"),
  watchGlyph: requireClass(styles.watchGlyph, "RailRow.module.css", "watchGlyph"),
  srOnly: requireClass(styles.srOnly, "RailRow.module.css", "srOnly"),
};

// frameTimes is always [] here: navigation summaries carry no
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

// Maps hubcore's normalized session state (cmd/evener-hub/internal/hubcore/
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
    case "restartRequired":
      return "needs-you";
    case "active":
      return "working";
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

// RowGutter is the wrapper the row's signal dot renders inside. The dot is
// conditionally rendered (see Signal) and OUTDENTED by stylesheet: .signal's
// negative margin pulls it left of the title line's text, into the leading
// padding .railRow reserves for it, so every row's text starts at the same x
// whether or not a dot hangs beside it.
function RowGutter({ className, testId, children }: { className: string; testId: string; children?: ReactNode }) {
  return (
    <span data-testid={testId} className={className}>
      {children}
    </span>
  );
}

// The title line's leading signal dot, shared by session and project rows.
// Renders ONLY for a signal state - working / needs-you / failed - and holds
// no space otherwise. It sits INSIDE the title line as its first item,
// outdented by .signal's negative margin (Rail.module.css has the
// arithmetic): the dot hangs in the row's leading padding, left of the text,
// so a dotted row's title and a quiet row's title start at exactly the same
// x instead of state moving the title's position.
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
export function activityGloss(session: RailSession, activity = activeWorkSummary(session)): string {
  const workingCount = activity.workingSubagents;
  const jobCount = activity.runningJobs;
  const parts: string[] = [];
  if (session.state === "restartRequired") parts.push(humanizeState(session.state, session.ask_pending === true));
  if (workingCount > 0) {
    parts.push(`${workingCount} subagent${workingCount === 1 ? "" : "s"} working`);
  } else if (session.state !== "restartRequired" && (jobCount === 0 || session.state === "active")) {
    parts.push(humanizeState(session.state, session.ask_pending === true));
  }
  if (jobCount > 0) parts.push(`${jobCount} job${jobCount === 1 ? "" : "s"} running`);
  if (session.branch !== undefined && session.branch !== "") parts.push(session.branch);
  return parts.join(" · ");
}

export { watchArmedLabel, watchCadenceLabel, watchDurationLabel, watchGloss, watchTitle };

// secondLine is the row's second line in full: activityGloss above, joined
// with the session's project when the row needs one (kata hxjn). A session
// row only needs its project named when it is rendered FLAT, mixed in with
// other projects' sessions - the Live and Pinned tiers, where a row's own
// nesting depth is 0 (see below). A session nested under its own ProjectRow
// (Projects/Test runs/Archived) is depth >= 1 there and never needs this:
// the project it belongs to is the row it is indented under. Project leads
// the line (state is what's happening, project is where) the same way
// activityGloss already leads with state before branch.
function secondLine(
  session: RailSession,
  showsGloss: boolean,
  showsProject: boolean,
  activity?: ReturnType<typeof activeWorkSummary>,
): string {
  const parts: string[] = [];
  // An empty project name has nothing to join, so it must not contribute a
  // leading " · " separator with no text before it (UX fix).
  if (showsProject && session.project !== "") parts.push(session.project);
  if (showsGloss) parts.push(activityGloss(session, activity));
  return parts.join(" · ");
}

export interface RailRowActions {
  onOpenSessionPane(session: RailSession, pane: SessionPanelKind | "notes"): void;
  onRenameSession(session: RailSession, name: string): Promise<void>;
  onShutdownSession(session: RailSession): Promise<void>;
  onForceStopSession(session: RailSession): Promise<void>;
  onPinSession(
    session: RailSession,
    target: PinTarget,
    section?: { id: string; name: string; member_count: number },
  ): Promise<void>;
  // Unpin/archive/delete return the mutation's promise so a rejection
  // reaches SessionMenu's confirm helper (the failure convention in
  // SessionMenu.tsx's header comment): Rail's runAction already toasts,
  // and the propagated rejection keeps the menu's dialog open.
  onUnpinRequest(session: RailSession): Promise<void>;
  onToggleArchiveSession(session: RailSession): Promise<void>;
  onDeleteSession(session: RailSession): Promise<void>;
  onToggleFavoriteProject(project: RailProject): void;
  onToggleArchiveProject(project: RailProject): void;
  onDeleteProjectRequest(project: RailProject): void;
}

export interface RailRowProps {
  node: RailNode;
  info: TreeRowInfo;
  actions: RailRowActions;
  resourceError?: string;
  retry?: () => void;
}

// The row's trailing chevron: a toggle rendered INLINE, right after the
// title text (before any star/Badge/timestamp), on branch rows only - the
// same trailing position the transcript's disclosure rows use. A leaf row
// renders nothing here: the chevron trails the text, so its absence leaves
// no hole to reserve (unlike the old leading gutter, whose conditional fill
// moved every title after it). stopPropagation keeps the click from also
// reaching the text column's activate handler it sits inside.
function TrailingChevron({ info }: { info: TreeRowInfo }) {
  if (!info.hasChildren) return null;
  return (
    // Decorative mouse shortcut for the same action Left/Right arrow
    // already performs on the treeitem itself (see widgets/tree's own doc
    // comment and dev/gallery-sections/tree.tsx's identical convention) -
    // out of tab order and hidden from assistive tech so it isn't a second,
    // redundant "toggle" announcement.
    //
    // A <span>, not a <button>: the chevron is a mouse-only affordance, and a
    // <button> receives focus on click - a focused aria-hidden element is the
    // exact violation Chrome's a11y console warns about ("blocked aria-hidden
    // on an element because its descendant retained focus"). A non-focusable
    // span can't hold focus, so aria-hidden is safe here. The owning treeitem
    // is the Tree widget's one roving Tab stop; keyboard users toggle it with
    // Left/Right arrow there, never via this glyph.
    <span
      data-testid="rail-chevron"
      className={CLASS.chevronButton}
      aria-hidden="true"
      onClick={(event) => {
        event.stopPropagation();
        info.toggle();
      }}
    >
      <Chevron direction={info.expanded ? "down" : "right"} size={12} />
    </span>
  );
}

function ActionsMenu({ label, items }: { label: string; items: MenuItem[] }) {
  // No items (e.g. the synthetic "(no project)" bucket - see
  // NO_PROJECT_KEY below) means nothing here is actionable; an empty
  // dropdown button would be worse than no button at all.
  if (items.length === 0) return null;
  return (
    <Menu
      // The row's single outer treeitem is the Tree widget's one roving Tab
      // stop - without triggerTabIndex={-1}, this trigger becomes a SECOND,
      // always-focusable Tab stop on every row simultaneously, breaking that
      // contract (Tab would reach "Actions for Row B" without ever reaching
      // Row B's own treeitem). Still reachable by click; Menu's own
      // consume-then-stop key handling (widgets/menu/index.tsx) is the other
      // half of this - an ArrowDown/Enter/Space this trigger already gives
      // meaning to must never also bubble into Tree's onKeyDown and move the
      // roving tabindex to a different row out from under an open menu. (The
      // chevron above sidesteps this differently: it is a non-focusable span,
      // so it is simply never a tab stop at all.)
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

// "no-project" is a synthetic project bucket for orphan live sessions whose
// project cannot be resolved (cmd/evener-hub/navigation_projection.go). It can
// appear in the wire's `projects` array like any other TreeProject,
// but the server rejects both archive and delete for this exact key
// ("no-project is not a local project" - app_archive.go/project_delete.go).
// Offering menu items that are guaranteed to
// fail server-side would be worse than offering none - kept as an
// all-or-nothing exclusion (favorite included) rather than special-casing
// per action, since evener/favorite/set's own project-kind validation is a
// separate, disclosed gap (unrelated to this row's own scope) that this
// component has no reliable way to distinguish from "would actually work".
const NO_PROJECT_KEY = "no-project";

// Opens a fresh spawn targeted at this project's working directory, via the
// same /new URL prefill the palette's "Start with prompt" command already
// uses for /new?prompt= (shell/palette/commands.ts): Spawn.tsx reads dir,
// prompt, and host off window.location.search (panes/spawn/urlPrefill.ts),
// never pane params - the spawn pane's own params type is deliberately empty
// (see panes/spawn/Spawn.tsx), so a URL prefill is the only way to hand it a
// directory. A "Host, then project" copy passes the host it nests under -
// this hub included, since the same project's copies share one working_dir
// and the draft's last-chosen host must not survive a launch from another
// copy. Falls back to a bare /new when a project has no working_dir
// (shouldn't happen for a real project, but degrades gracefully rather than
// silently doing nothing) - NO_PROJECT_KEY itself is excluded before this is
// ever called, same as every other project-scoped action here.
function spawnInProject(project: RailProject, host?: string): void {
  const params = new URLSearchParams();
  if (project.working_dir) params.set("dir", project.working_dir);
  if (host) params.set("host", host);
  const query = params.toString();
  navigate(query ? `/new?${query}` : "/new");
}

// The project menu offers delete unconditionally: the request is local-only,
// and the Rail owns that judgement - onDeleteProjectRequest refuses a project
// a remote host also owns, with a toast that names the hosts (Rail.test.tsx
// pins it), so the person gets an explanation instead of an item that is
// silently missing. The row keeps no ownership verdict of its own.
function projectMenuItems(
  project: RailProject,
  actions: RailRowActions,
  spawnHost?: string,
  canSpawn = true,
): MenuItem[] {
  if (project.key === NO_PROJECT_KEY) return [];
  return [
    // An offline host copy offers no launch at all: the request would
    // silently fall back to this hub with the remote working_dir.
    ...(canSpawn
      ? [
          {
            id: "new-session",
            label: "New session",
            onSelect: () => spawnInProject(project, spawnHost),
          },
        ]
      : []),
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
function rowTooltip(session: RailSession, showsGloss: boolean, saysNotStarted: boolean, age?: string): string {
  const parts = [session.title];
  // A signal row already prints its state; a quiet one doesn't, so only the
  // quiet case needs the word here. A row that has never run reports THAT
  // instead: "idle" is true of it but tells the reader nothing they don't
  // already believe, and it is the very confusion this line exists to end.
  if (saysNotStarted) parts.push("not started");
  else if (!showsGloss) parts.push(humanizeState(displayState(session), session.ask_pending === true));
  // "current" is the unremarkable default state of a session - the same
  // exclusion the visible line used to make.
  if (session.tier !== undefined && session.tier !== "" && session.tier !== "current") parts.push(session.tier);
  // A dormant row spends its right slot on "Not started" instead of the age,
  // so the age lands here - the same contract every other fact this row gives
  // up is held to. The caller supplies it from the summary's anchor, because a
  // clock-derived value cannot be a field the model froze.
  if (saysNotStarted && age !== undefined && age !== "") parts.push(age);
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
function saysNotStarted(session: RailSession, showsGloss: boolean): boolean {
  return session.dormant === true && !showsGloss;
}

// The rail-row use of the shared session menu: same component the session
// pane's chrome renders, fed from the RailSession instead of a ThreadModel.
// panesOpen drives the ✓ markers via the workspace store; triggerTabIndex
// -1 keeps the Tree widget's single-roving-Tab-stop contract (see
// ActionsMenu's own comment, which this replaces for session rows).
function SessionMenuRow({ session, actions }: { session: RailSession; actions: RailRowActions }) {
  const ref = session.ref;
  // Four separate boolean selectors, NOT one object-literal selector: a
  // fresh { details, tasks, activity, notes } object every call would fail the
  // store's reference-equality check and re-render the row on every
  // workspace change (SessionChrome selects the same four booleans the
  // same way).
  const detailsOpen = useWorkspaceStore((s) => isPaneOpen(s, "sessionDetails", { ref }));
  const tasksOpen = useWorkspaceStore((s) => isPaneOpen(s, "sessionTasks", { ref }));
  const activityOpen = useWorkspaceStore((s) => isPaneOpen(s, "sessionActivity", { ref }));
  const notesOpen = useTopNotesExpanded(ref);
  // Navigation summaries do not carry notes capability. Observe only an
  // already-hydrated snapshot; opening the session owns any needed fetch.
  const canReadNotes = useThreadsStore((s) => canReadSharedNotes(s.threads.get(ref)));
  return (
    <SessionMenu
      sessionRef={ref}
      title={session.title}
      triggerLabel={`Actions for ${session.title}`}
      canRename={session.rename === true}
      canShutdown={session.live && session.state !== "restartRequired"}
      canReadNotes={canReadNotes}
      treeNode={session}
      panesOpen={{ details: detailsOpen, tasks: tasksOpen, activity: activityOpen, notes: notesOpen }}
      actions={{
        onOpenPane: (pane) => actions.onOpenSessionPane(session, pane),
        onRename: (name) => actions.onRenameSession(session, name),
        onShutdown: () => actions.onShutdownSession(session),
        onForceStop:
          ref.startsWith("local:") &&
          session.host_id === "local" &&
          session.kind === "session" &&
          session.state !== "notLoaded" &&
          session.state !== "closed"
            ? () => actions.onForceStopSession(session)
            : undefined,
        onPin: (target, section) => actions.onPinSession(session, target, section),
        onUnpin: () => actions.onUnpinRequest(session),
        onToggleArchive: () => actions.onToggleArchiveSession(session),
        onDelete: () => actions.onDeleteSession(session),
      }}
      triggerTabIndex={-1}
    />
  );
}

// A row's host reachability, derived from the manifest's sources (Component
// 06a) keyed by the row's own host_id. This is deliberately NOT part of the
// row schema and does NOT reuse Dormant: Jesse's standing decision keeps
// Dormant meaning "never run". Unknown hosts - including the many RailRow
// tests that render a row with no manifest store state at all - read as
// ONLINE, so the badge is purely additive and existing rows keep their
// meaning. The selector returns a primitive, so a fresh inline closure each
// render is safe for the store's reference-equality check.
//
// The DISPLAY view, not the settled one: a badge reports what the rail knows
// about the host, and a manifest re-read (loading/stale) must not blank the
// last reading - that flipped every offline badge back to online for the length
// of the refresh (round nine). A host the fresh manifest has dropped still
// leaves its rows reading online once the NEW manifest lands, which is the
// unchanged "unknown host" contract below.
function useHostOnline(hostId: string | undefined): boolean {
  return useNavigationStore((state) => {
    // No host to consult (a flat row asking whether its spawn needs a
    // gate): reads as online, the same default an unknown host gets below.
    if (hostId === undefined) return true;
    const source = selectDisplaySources(state).find((candidate) => candidate.id === hostId);
    return source ? source.online : true;
  });
}

// Launchability is a different question from the display flag above: a
// host can take a launch only while the SETTLED manifest names it and it
// reads online - the same judgment Spawn's own hostChoice makes. A host
// the manifest has removed keeps its rows and chips (the display contract)
// but must stop offering launches: the picker would refuse the prefilled
// host and silently start the session on this hub, with the remote
// working_dir.
function useHostLaunchable(hostId: string | undefined): boolean {
  return useNavigationStore((state) => {
    // This hub is always launchable, whatever the manifest's flight state:
    // Spawn's own fallback IS local, so an in-flight read can never take
    // this machine away.
    if (hostId === undefined || hostId === LOCAL_HOST) return true;
    const source = selectSources(state).find((candidate) => candidate.id === hostId);
    return source ? source.online : false;
  });
}

// The row's label span: the treeitem's accessible name (there is no separate
// aria-label) and the holder of the title tooltip.
function RailLabelSpan({ session, tooltip }: { session: RailSession; tooltip: string }): ReactNode {
  return (
    <span className={CLASS.label} title={tooltip}>
      {session.title}
    </span>
  );
}

// A dormant row's tooltip carries the age its right slot gave up to "Not
// started", and that age is a clock like the visible stamp - so THIS leaf, not
// the memoized SessionRow, is the rail clock's other subscriber. Every other
// row's tooltip is clock-free and renders through RailLabelSpan with no
// subscription at all.
function DormantLabel({ session, showsGloss }: { session: RailSession; showsGloss: boolean }): ReactNode {
  const now = useRailNow();
  return (
    <RailLabelSpan
      session={session}
      tooltip={rowTooltip(session, showsGloss, true, relativeAge(session.updated_at, now))}
    />
  );
}

// RailAge is the row's live "last update" stamp: one of the rail clock's two
// leaf subscribers (the other is a dormant row's label - see DormantLabel).
// railNow.tsx owns why the label comes from `updated_at` rather than a field
// the model precomputed. Sitting BELOW the memoized SessionRow - the boundary
// ActivityTree.tsx draws with LiveMetaSegments - is what keeps a tick from
// re-rendering every row that carries a stamp.
function RailAge({ updatedAt }: { updatedAt?: string }): ReactNode {
  const now = useRailNow();
  const label = relativeAge(updatedAt, now);
  if (label === undefined || label === "") return null;
  return (
    <span data-testid="rail-row-time" className={CLASS.time}>
      {label}
    </span>
  );
}

function SessionRow({ node, info, actions }: { node: SessionRailNode; info: TreeRowInfo; actions: RailRowActions }) {
  const { session } = node;
  // A non-local row names its host on the title line (a LABEL, not a tree
  // re-layout); reachability comes from the manifest's sources, not from the
  // row. Dormant keeps its own "never run" meaning - see useHostOnline.
  const hostId = session.host_id;
  const showsHost = hostId !== "" && hostId !== "local";
  const hostOnline = useHostOnline(hostId);
  const needsYouCount = needsYouDescendantCount(session);
  // The state this row PRESENTS (railNodes' displayState): a turn-ended
  // subagent presents as idle, not "your move", because its next input comes
  // from its parent session, never from the user. Dot, gloss, tint, and
  // tooltip all read this one value so they can never disagree about a row.
  const presented = displayState(session);
  // A quiet row (idle, ended, notLoaded, unknown) is title + age, one line: the
  // empty signal gutter and a grey age already say "nothing is happening here",
  // so a second line restating "idle" in words was the state living at two
  // altitudes on the row whose whole job is triage. A signal row keeps its
  // gloss, and with it its second line - which makes signal rows physically
  // taller than quiet ones. That is the point: the rows worth finding are bigger
  // than the rows that aren't, and the list's evenness is worth less than that.
  const activity = activeWorkSummary(session);
  const hasWorkingDescendants = activity.workingSubagents > 0;
  const hasRunningJobs = activity.runningJobs > 0;
  const hasActiveWork = session.state === "active" || hasWorkingDescendants || hasRunningJobs;
  // Descendant/job activity is a working signal for the owning session. A
  // failed session still wins over that rollup so an error cannot disappear
  // behind a green child.
  let effectiveState = presented;
  if (effectiveState !== "errored" && effectiveState !== "restartRequired" && hasActiveWork) effectiveState = "active";
  const showsGloss = SIGNAL_STATES.has(cadenceStateFor(effectiveState));
  // kata hxjn: a row at depth 0 is a top-level entry in a flat, cross-project
  // tier (Live/Pinned - see toSessionNode/sessionNodes; a Projects/Test-runs/
  // Archived session is always nested under its own ProjectRow, never a depth-0
  // SessionRow). Cross-referencing which project a Live row belongs to used to
  // mean leaving the rail entirely, so those rows get a second line even when
  // otherwise quiet - the one exception to the "quiet row is one line" rule
  // above, made for exactly the fact that rule can't otherwise carry.
  const showsProject = info.depth === 0;
  const notStarted = saysNotStarted(session, showsGloss);
  // The session's own armed watches. Not a subtree rollup: the hub keeps each
  // watch on its receiver's summary, so this is every watch the fold-out below
  // this row will show - see railNodes' activeWatchCount.
  const watchCount = activeWatchCount(session);
  const omittedWatchCount = session.omitted_watches ?? 0;
  // The retained total the fold-out lists. The summary line reports this
  // (with the armed count beside it when they differ) so it matches the rows
  // hanging under the row even when a retained watch is inactive.
  const retainedWatchCount = (session.watches ?? []).length;
  // Omitted rows alone still mean the session holds watches the row does not
  // list, so the line must appear (and say "+N more") even with none retained.
  const hasWatches = retainedWatchCount > 0 || omittedWatchCount > 0;
  // A watch is pending work, and it is the one kind that can be the ONLY thing
  // a session has left to do - so it earns the second line on its own. That is
  // a deliberate amendment to "a quiet row is one line" (the rule at the top of
  // this file): without it the count would vanish on exactly the session where
  // a watch is the only thing happening, which is the case this feature exists
  // for.
  const showsActivity = showsGloss || hasWorkingDescendants || hasRunningJobs || hasWatches;
  // The tinted gloss itself still belongs to a signal row (or to a depth-0
  // row naming its project). A watch-only quiet row's second line is just its
  // watch count; glossing "idle" beside the count would be noise, not a gloss.
  const gloss = secondLine(session, showsGloss, showsProject, activity);
  const showsSecondLine = showsActivity || showsProject;
  // Only a genuine signal row (showsGloss) carries a state to tint - the
  // depth-0-only "just the project name" line (showsProject with no signal)
  // has no state family to color, so it stays the plain --ink-low default.
  const activityClass = showsGloss
    ? `${CLASS.activity} ${ACTIVITY_FAMILY_CLASS[cadenceStateFor(effectiveState)] ?? ""}`.trim()
    : CLASS.activity;
  return (
    // data-session-ref is the scroll target Rail's reveal effect (the palette's
    // /project command via railController) queries to bring a session's row
    // into view - the ref is stable and unique per session, unlike the label.
    <span className={CLASS.railRow} data-session-ref={session.ref}>
      {/* The text column: the title line (outdented signal dot, title,
          trailing chevron on branch rows), and - on a signal row only - a
          second line glossing why it wants attention. Row anatomy for the
          subagent tree's already-existing recursion (toSessionNode/Tree),
          not new recursion of its own. */}
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
        <span className={CLASS.titleLine}>
          <Signal wireState={effectiveState} />
          {notStarted ? (
            <DormantLabel session={session} showsGloss={showsGloss} />
          ) : (
            <RailLabelSpan session={session} tooltip={rowTooltip(session, showsGloss, false)} />
          )}
          <TrailingChevron info={info} />
          {/* Host label after the chevron (which hugs the title text), so a
              remote row says where it lives without pushing the title. The
              offline marker is visible text, not a color or an aria-only
              state, and the title carries the same fact for hover. */}
          {showsHost && (
            <span
              data-testid="rail-row-host"
              className={hostOnline ? CLASS.host : `${CLASS.host} ${CLASS.hostOffline}`}
              title={hostOnline ? `Host ${hostId}` : `Host ${hostId} is offline`}
            >
              {hostId}
              {!hostOnline && <span data-testid="rail-row-host-offline">{" (offline)"}</span>}
            </span>
          )}
        </span>
        {showsSecondLine && (
          // The second line: the watch count and/or the tinted activity gloss,
          // as siblings. The count is its OWN element rather than text inside
          // the gloss so it keeps neutral ink - the gloss's activityClass
          // (alive/attention/danger) must not tint a watch, which is pending
          // work and not one of the four attention hues. It also leads the
          // line, so it precedes the branch that tails the gloss (the
          // deliberate ellipsis sacrifice) and can never be what ellipsis eats.
          <span className={CLASS.secondLine}>
            {hasWatches && (
              <span data-testid="rail-row-watches" className={CLASS.watchCount}>
                {/* The gloss shares the line's separator convention: the count
                    carries it only when something follows, so a watch-only
                    line ends with the word, not a dangling "·". */}
                {`${watchCountLabel(watchCount, retainedWatchCount, omittedWatchCount)}${gloss !== "" ? " ·" : ""}`}
              </span>
            )}
            {gloss !== "" && (
              <span data-testid="rail-row-activity" className={activityClass} title={gloss}>
                {gloss}
              </span>
            )}
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
      {/* Right slot: ONE shared grid cell (RailRow.module.css's .rightSlot)
          holding the occupant - the Task-7 needs-you-descendant Badge, or
          (when there's nothing to flag) a relative timestamp / "Not
          started", never more than one - plus the hover-revealed actions
          menu, which borrows the occupant's space instead of reserving its
          own beside it and covers the occupant while revealed. The session's
          OWN needs-you already shows via its amber Cadence dot above
          (cadenceStateFor maps awaiting/warning to "needs-you"), so a leaf
          needs-you session with no needs-you descendants correctly shows its
          timestamp here, not a redundant "0"/"1" badge. */}
      <span className={CLASS.rightSlot}>
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
          <RailAge updatedAt={session.updated_at} />
        )}
        <span className={CLASS.actions}>
          <SessionMenuRow session={session} actions={actions} />
        </span>
      </span>
    </span>
  );
}

function ProjectRow({
  node,
  info,
  actions,
  resourceError,
  retry,
}: {
  node: ProjectRailNode;
  info: TreeRowInfo;
  actions: RailRowActions;
  resourceError?: string;
  retry?: () => void;
}) {
  const { project } = node;
  const attentionCount = project.rollup_attn ?? 0;
  // A host-first copy claims no aggregate rollup: rollup_state and
  // rollup_attn are project-wide wire facts, and an honest per-host count
  // would need wire support the manifest does not carry - the same line the
  // host group row itself draws. The attention rows still surface in
  // Needs-you.
  const showsRollup = node.spawnHost === undefined;
  // The Spawn picker refuses an offline or unknown-to-the-manifest host; a
  // copy nested under one must not offer a launch that would silently fall
  // back to this hub (with the remote working_dir).
  const canSpawn = useHostLaunchable(node.spawnHost);
  return (
    <span className={CLASS.railRow}>
      {/* Same title-line anatomy as SessionRow: outdented signal dot, name,
          trailing chevron on a branch row. */}
      <span className={CLASS.textCol}>
        <span className={CLASS.titleLine}>
          <Signal wireState={showsRollup ? (project.rollup_state ?? "idle") : "idle"} />
          {/* Same reasoning as SessionRow's own label above. displayName is
              the UX-fix decoration railNodes.ts's projectDisplayLabels
              stamps on when this project's name collides with a sibling's
              in the same list; falls back to the bare name otherwise (and
              for a hand-built test double that omits it). */}
          {/* biome-ignore lint/a11y/noStaticElementInteractions: redundant with the row's own Enter handling, see SessionRow */}
          {/* biome-ignore lint/a11y/useKeyWithClickEvents: redundant with the row's own Enter handling, see SessionRow */}
          <span className={CLASS.label} onClick={info.activate}>
            {node.displayName ?? project.name}
          </span>
          {resourceError && retry && (
            <button
              type="button"
              onClick={(event) => {
                event.stopPropagation();
                retry();
              }}
            >
              Retry
            </button>
          )}
          <TrailingChevron info={info} />
        </span>
      </span>
      {project.favorite === true && (
        <span data-testid="favorite-star" aria-hidden="true" className={CLASS.star}>
          {"★"}
        </span>
      )}
      {/* Same shared right slot as SessionRow: the rollup Badge (when the
          project has needs-you descendants) and the hover-revealed actions
          pair share one cell - the menu covers the badge while revealed
          instead of reserving width beside it. */}
      <span className={CLASS.rightSlot}>
        {showsRollup && attentionCount > 0 && <Badge count={attentionCount} tone="attention" />}
        <span className={CLASS.actions}>
          {project.key !== NO_PROJECT_KEY && canSpawn && (
            <IconButton
              label={`New session in ${project.name}`}
              icon={<span aria-hidden="true">{"+"}</span>}
              variant="quiet"
              size="sm"
              tabIndex={-1}
              onClick={() => spawnInProject(project, node.spawnHost)}
            />
          )}
          <ActionsMenu label={project.name} items={projectMenuItems(project, actions, node.spawnHost, canSpawn)} />
        </span>
      </span>
    </span>
  );
}

// The rail's organize-by host group row - a configured host as a synthetic
// branch, in whichever shape the grouping puts it in ("Host, then project"
// top group, "Project, then host" branch inside a project, or a Live-section
// subheader). Anatomy is the project row's: a leading glyph instead of a
// signal dot (a host is infrastructure, not triage), the label, a trailing
// chevron. Offline follows the session rows' own host-label convention:
// italic, dimmed, "(offline)" in the caption ink. No rollup Badge and no
// actions: the manifest carries no per-host attention count, and the rows
// under the group keep their own signals and menus.
function HostRow({ node, info }: { node: HostRailNode; info: TreeRowInfo }) {
  const { host } = node;
  return (
    <span
      className={CLASS.railRow}
      data-testid="rail-row-host-group"
      title={host.online ? `Host ${host.label}` : `Host ${host.label} is offline`}
    >
      <span className={CLASS.textCol}>
        <span className={CLASS.titleLine}>
          <HostGlyph className={CLASS.hostGlyph} testId="rail-row-host-glyph" />
          {/* biome-ignore lint/a11y/noStaticElementInteractions: redundant with the row's own Enter handling, see SessionRow */}
          {/* biome-ignore lint/a11y/useKeyWithClickEvents: redundant with the row's own Enter handling, see SessionRow */}
          <span className={host.online ? CLASS.label : `${CLASS.label} ${CLASS.hostOffline}`} onClick={info.activate}>
            {host.label}
          </span>
          {!host.online && (
            <span data-testid="rail-row-host-group-offline" className={CLASS.host}>
              {" (offline)"}
            </span>
          )}
          <TrailingChevron info={info} />
        </span>
      </span>
    </span>
  );
}

// The "Inactive subagents (N)" disclosure (parity-m3-sidebar-tree.md §3).
// No signal slot at all, matching Signal's own render-only-when-dotted
// contract (a group of finished sessions has no state to report). No actions
// menu either: it stands for rows rather than being one, and the rows it
// hides carry their own. Its label sits at the same x as every other row at
// its nesting depth - the trailing chevron after the label is its toggle,
// the same inline affordance session and project rows use.
function DisclosureFoldRow({ label, testId, info }: { label: string; testId: string; info: TreeRowInfo }) {
  return (
    <span className={CLASS.railRow} data-testid={testId}>
      <span className={CLASS.textCol}>
        <span className={CLASS.titleLine}>
          {/* The label is the accessible activation target; the chevron is a
              decorative shortcut for the same treeitem toggle. */}
          {/* biome-ignore lint/a11y/noStaticElementInteractions: redundant with the row's own Enter handling */}
          {/* biome-ignore lint/a11y/useKeyWithClickEvents: redundant with the row's own Enter handling */}
          <span className={CLASS.label} onClick={info.toggle}>
            {label}
          </span>
          <TrailingChevron info={info} />
        </span>
      </span>
    </span>
  );
}

function InactiveFoldRow({ node, info }: { node: InactiveFoldRailNode; info: TreeRowInfo }) {
  const label = `${node.count === 1 ? "Inactive subagent" : "Inactive subagents"} (${node.count})`;
  return <DisclosureFoldRow label={label} testId="rail-row-inactive-fold" info={info} />;
}

function jobLabel(job: JobRailNode["job"]): string {
  return job.command?.trim() || job.task?.trim() || job.job_type?.trim() || job.job_id;
}

// jobTitle is the job row's hover tooltip: the full command that actually ran
// (full_command, untruncated, when the label's command was cut by the wire's
// label bound) plus the tool call's `intent` — why the model said it is
// running the command. Status rides along the same way the old single-line
// title carried it, so a hover still answers "what is this doing" end to end.
function jobTitle(job: JobRailNode["job"], status: string): string {
  const command =
    job.full_command?.trim() || job.command?.trim() || job.task?.trim() || job.job_type?.trim() || job.job_id;
  const intent = job.intent?.trim();
  if (intent) {
    return `${command} · ${intent} · ${status}`;
  }
  return `${command} · ${status}`;
}

function JobRow({ node }: { node: JobRailNode }) {
  const active = node.active;
  const status = node.job.status.trim() || (active ? "running" : "completed");
  const displayStatus = jobStatusDisplay(status, node.job.reason);
  return (
    <span className={CLASS.railRow} data-testid="rail-row-job" data-job-id={node.job.job_id}>
      <span className={CLASS.textCol}>
        <span className={CLASS.titleLine}>
          <Signal wireState={active ? "active" : isActivityFailure(undefined, status) ? "errored" : "ended"} />
          <span className={CLASS.label} title={jobTitle(node.job, displayStatus)}>
            {jobLabel(node.job)}
          </span>
        </span>
        <span
          data-testid="rail-row-job-status"
          className={active ? `${CLASS.activity} ${CLASS.activityAlive}` : CLASS.activity}
        >
          {displayStatus}
        </span>
      </span>
    </span>
  );
}

// The clock a watch row leads with, DRAWN rather than typed: the app's font
// ranges stop at U+2215, so a "◷" (or any other clock code point) falls back
// to a system font - and the rail's accessible name is name-from-content, so a
// typed glyph would be announced as a stray character rather than as "watch"
// (see review synthesis §7). aria-hidden because the visually-hidden "Watch:"
// beside it is the word assistive tech should read.
//
// Exported for the session panel's watch rows too: the geometry lives here
// once, and each caller passes its own module's class and its own test id.
export function WatchGlyph({ className, testId }: { className: string; testId: string }) {
  return (
    <svg data-testid={testId} className={className} viewBox="0 0 16 16" aria-hidden="true" focusable="false">
      <circle cx="8" cy="8" r="6.25" fill="none" stroke="currentColor" strokeWidth="1.5" />
      <path d="M8 4.5V8l2.5 1.5" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
    </svg>
  );
}

// The host glyph a host group row leads with: DRAWN on the rail's 16x16 icon
// grammar (railIcons.tsx) and sized/inked like the watch row's clock above -
// two stacked units and their drive dots, so the row reads as "a machine"
// rather than a text glyph falling back to a system font. aria-hidden like
// WatchGlyph: the label beside it is the row's accessible name.
function HostGlyph({ className, testId }: { className: string; testId: string }) {
  return (
    <svg data-testid={testId} className={className} viewBox="0 0 16 16" aria-hidden="true" focusable="false">
      <rect x="2" y="2.75" width="12" height="4.5" rx="1.25" fill="none" stroke="currentColor" strokeWidth="1.5" />
      <rect x="2" y="8.75" width="12" height="4.5" rx="1.25" fill="none" stroke="currentColor" strokeWidth="1.5" />
      <path d="M4.75 5h.01M4.75 11h.01" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" />
    </svg>
  );
}

// A live watch in its receiver session's fold-out. Quieter than a job row by
// design: pending work is inventory, not attention - neutral ink, no signal
// dot, no actions menu, nothing to click. The row's accessible name comes from
// its content, so the drawn clock is aria-hidden and the visually-hidden
// "Watch:" supplies the word.
function WatchRow({ node }: { node: WatchRailNode }) {
  const { watch } = node;
  return (
    <span className={CLASS.railRow} data-testid="rail-row-watch" data-watch-id={watch.id}>
      <span className={CLASS.textCol}>
        <span className={CLASS.titleLine}>
          <WatchGlyph className={CLASS.watchGlyph} testId="rail-row-watch-glyph" />
          <span className={CLASS.srOnly}>Watch:</span>
          {/* The note ellipsizes, so it carries its own full text as a title
              tooltip - the same contract every other truncating line here
              keeps. */}
          <span className={CLASS.label} title={watchTitle(watch)}>
            {watchTitle(watch)}
          </span>
        </span>
        <span data-testid="rail-row-watch-status" className={CLASS.activity}>
          {watchGloss(watch)}
        </span>
      </span>
    </span>
  );
}

function CompletedJobsFoldRow({ node, info }: { node: CompletedJobsFoldRailNode; info: TreeRowInfo }) {
  return (
    <DisclosureFoldRow label={`Completed jobs (${node.count})`} testId="rail-row-completed-jobs-fold" info={info} />
  );
}

// The "+N older" note for rows the server capped away (hubcore's
// maxSidebarSessionsPerTier). Its text starts at the same x as every other
// row's, with no dot or chevron of its own. Project overflow rows activate a
// bounded fetch for the capped-away tier rows; a passive row (node.passive,
// used by the local watch cap whose rows the wire already carried) is an
// honest count with nothing to fetch, so it gets no click handler and the
// Tree's own activation is a no-op for it (see Rail's isPassiveRailNode).
function OverflowRow({ node, info }: { node: OverflowRailNode; info: TreeRowInfo }) {
  const interactive = node.passive !== true;
  return (
    <span className={CLASS.railRow}>
      {/* The treeitem's Enter handler is the keyboard path; this click makes
          the visible affordance usable with a mouse as well. */}
      {/* biome-ignore lint/a11y/noStaticElementInteractions: treeitem owns keyboard activation and accessible semantics */}
      {/* biome-ignore lint/a11y/useKeyWithClickEvents: treeitem owns keyboard activation */}
      <span
        data-testid="rail-row-overflow"
        className={CLASS.overflow}
        onClick={interactive ? info.activate : undefined}
      >{`+${node.count} ${node.suffix ?? "older"}`}</span>
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

function railRowPropsEqual(previous: RailRowProps, next: RailRowProps): boolean {
  if (
    previous.info !== next.info ||
    previous.actions !== next.actions ||
    previous.resourceError !== next.resourceError ||
    previous.retry !== next.retry
  )
    return false;
  if (previous.node === next.node) return true;
  if (previous.node.kind !== "project" || next.node.kind !== "project") return false;
  const previousProject = previous.node.project;
  const nextProject = next.node.project;
  // Keep this list in lockstep with ProjectRow, projectMenuItems, and
  // spawnInProject. Descendant/page fields are Tree recursion inputs, not row
  // presentation or action inputs, so they deliberately do not cross this memo
  // boundary.
  return (
    previous.node.id === next.node.id &&
    previous.node.displayName === next.node.displayName &&
    previous.node.resourceError === next.node.resourceError &&
    previous.node.retry === next.node.retry &&
    previousProject.key === nextProject.key &&
    previousProject.name === nextProject.name &&
    previousProject.working_dir === nextProject.working_dir &&
    previousProject.rollup_state === nextProject.rollup_state &&
    previousProject.rollup_attn === nextProject.rollup_attn &&
    previousProject.favorite === nextProject.favorite &&
    previousProject.is_archived === nextProject.is_archived
  );
}

export const RailRow = memo(function RailRow({ node, info, actions, resourceError, retry }: RailRowProps) {
  useRailRenderObserver()?.(node.id);
  switch (node.kind) {
    case "loading":
      return LoadingRow();
    case "job":
      return <JobRow node={node} />;
    case "watch":
      return <WatchRow node={node} />;
    case "inactiveFold":
      return <InactiveFoldRow node={node} info={info} />;
    case "completedJobsFold":
      return <CompletedJobsFoldRow node={node} info={info} />;
    case "overflow":
      return <OverflowRow node={node} info={info} />;
    case "project":
      return (
        <ProjectRow
          node={node}
          info={info}
          actions={actions}
          resourceError={resourceError ?? node.resourceError}
          retry={retry ?? node.retry}
        />
      );
    case "host":
      return <HostRow node={node} info={info} />;
    case "session":
      return <SessionRow node={node} info={info} actions={actions} />;
  }
}, railRowPropsEqual);
