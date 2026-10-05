// RailRow is the Tree widget's renderRow implementation for the sidebar:
// given one RailNode (railNodes.ts) and the TreeRowInfo the Tree widget
// computed for it (depth/expanded/hasChildren/toggle/activate), it renders
// one compact line: an outdented status indicator when action or work is
// present, the title, a trailing disclosure chevron, and the age/actions
// slot. Session context that used to make rows taller lives in the title's
// HoverCard. Pure presentation: every
// mutation goes back out through the `actions` prop, which Rail.tsx
// implements against actions.ts + the tree store's refresh().
//
// The rail is a TRIAGE surface. Broken is red, needs-you is yellow, and running
// work is a grey spinner. Broken and blocked attention outrank work; plain
// awaiting and warning yield to work already in flight. Idle and ended rows
// have no indicator. The stable one-line rhythm keeps the
// title list scannable while the HoverCard preserves project, host, branch,
// jobs, subagents, watches, tier, and age without permanent visual noise.
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
  approvalWaiting,
  humanizeState,
  SHUT_DOWN_STATUSES,
  watchCadenceLabel,
  watchDurationLabel,
  watchGloss,
} from "@evener/appwire-client";
import { subagentTallyToShow } from "@evener/appwire-client/state/navigation";
import { memo, type ReactNode, useCallback, useRef } from "react";
import { LOCAL_HOST } from "../../stores/hostRouting";
import {
  relativeAge,
  selectDisplaySources,
  selectPinSectionSummaries,
  selectSources,
} from "../../stores/navigation/selectors";
import { useNavigationStore } from "../../stores/navigation/store";
import { Badge, type CadenceState, Chevron, IconButton } from "../../widgets";
import { HoverCard } from "../../widgets/hovercard";
import { requireClass } from "../../widgets/internal/requireClass";
import { Menu, type MenuItem } from "../../widgets/menu";
import type { TreeRowInfo } from "../../widgets/tree";
import { useActivitySidebarOpenFor } from "../activitybar/activitySidebarStore";
import { navigate } from "../routing";
import { type PinTarget, SessionMenu } from "../sessionMenu/SessionMenu";
import styles from "./RailRow.module.css";
import {
  activeWatchCount,
  displayState,
  type HostRailNode,
  type OverflowRailNode,
  type ProjectRailNode,
  type RailNode,
  type RailProject,
  type RailSession,
  type SessionRailNode,
  watchCountLabel,
} from "./railNodes";
import { useRailNow } from "./railNow";
import { useRailRenderObserver } from "./railRenderObserver";
import { isConfirmedCrashedSession, isTopLevelSession } from "./sessionKind";

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
  sessionTitle: requireClass(styles.sessionTitle, "RailRow.module.css", "sessionTitle"),
  statusDot: requireClass(styles.statusDot, "RailRow.module.css", "statusDot"),
  statusSpinner: requireClass(styles.statusSpinner, "RailRow.module.css", "statusSpinner"),
  contextCard: requireClass(styles.contextCard, "RailRow.module.css", "contextCard"),
  contextHead: requireClass(styles.contextHead, "RailRow.module.css", "contextHead"),
  contextKind: requireClass(styles.contextKind, "RailRow.module.css", "contextKind"),
  contextStatus: requireClass(styles.contextStatus, "RailRow.module.css", "contextStatus"),
  contextSummary: requireClass(styles.contextSummary, "RailRow.module.css", "contextSummary"),
  contextRows: requireClass(styles.contextRows, "RailRow.module.css", "contextRows"),
  contextRow: requireClass(styles.contextRow, "RailRow.module.css", "contextRow"),
  contextKey: requireClass(styles.contextKey, "RailRow.module.css", "contextKey"),
  contextValue: requireClass(styles.contextValue, "RailRow.module.css", "contextValue"),
  contextMono: requireClass(styles.contextMono, "RailRow.module.css", "contextMono"),
  time: requireClass(styles.time, "RailRow.module.css", "time"),
  notStarted: requireClass(styles.notStarted, "RailRow.module.css", "notStarted"),
  host: requireClass(styles.host, "RailRow.module.css", "host"),
  hostOffline: requireClass(styles.hostOffline, "RailRow.module.css", "hostOffline"),
  hostGlyph: requireClass(styles.hostGlyph, "RailRow.module.css", "hostGlyph"),
  star: requireClass(styles.star, "RailRow.module.css", "star"),
  loadingRow: requireClass(styles.loadingRow, "RailRow.module.css", "loadingRow"),
  overflow: requireClass(styles.overflow, "RailRow.module.css", "overflow"),
  srOnly: requireClass(styles.srOnly, "RailRow.module.css", "srOnly"),
};

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
const SIGNAL_STATES: ReadonlySet<CadenceState> = new Set<CadenceState>(["working", "needs-you", "failed"]);

const CADENCE_LABEL: Record<CadenceState, string> = {
  working: "Running",
  "needs-you": "Needs you",
  failed: "Broken",
  ended: "Ended",
  idle: "Idle",
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
      <span
        role="img"
        aria-label={CADENCE_LABEL[state]}
        data-testid={state === "working" ? "rail-status-spinner" : "rail-status-dot"}
        data-status={state === "working" ? undefined : state}
        className={state === "working" ? CLASS.statusSpinner : CLASS.statusDot}
      />
    </RowGutter>
  );
}

// The rail no longer renders watch rows of its own (the activity sidebar's
// Watches tab owns them), but its tests and the sidebar's watch rows share
// the wire's wording through these re-exports.
export { watchCadenceLabel, watchDurationLabel, watchGloss };

export interface RailRowActions {
  onOpenOverview(session: RailSession): void;
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
// directory. A "Host, then project" copy passes the host it nests under,
// and a "Project, then host" project row passes the first owning host in
// rail order - this hub included in both cases, since the same project's
// rows share one working_dir and the draft's last-chosen host must not
// survive a launch from another row. Falls back to a bare /new only when
// the project has neither a
// working_dir nor a host to name (shouldn't happen for a real project, but
// degrades gracefully rather than silently doing nothing) - NO_PROJECT_KEY
// itself is excluded before this is ever called, same as every other
// project-scoped action here.
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

// saysNotStarted decides whether a row leads with "this has never run".
//
// Dormancy is a fact about a session's HISTORY; the state is a fact about what
// it is doing now. When those two compete for one slot the state wins: a
// dormant session handed a prompt a second ago is genuinely working, and a row
// still calling it "Not started" would be flatly wrong. So this is only ever
// true on a row that is otherwise quiet - which is exactly the row that had
// nothing to say before.
function saysNotStarted(session: RailSession, hasSignal: boolean): boolean {
  return session.dormant === true && !hasSignal;
}

// The rail-row use of the shared session menu: same component the session
// pane's chrome renders, fed from the RailSession instead of a ThreadModel.
// Overview's explicit session scope drives the ✓ marker; triggerTabIndex
// -1 keeps the Tree widget's single-roving-Tab-stop contract (see
// ActionsMenu's own comment, which this replaces for session rows).
function SessionMenuRow({ session, actions }: { session: RailSession; actions: RailRowActions }) {
  const ref = session.ref;
  // The Overview ✓ marks the shared sidebar's scope. A leftover
  // sessionActivity pane is not an opener and is intentionally not marked.
  const activitySidebarOpen = useActivitySidebarOpenFor(ref);
  return (
    <SessionMenu
      sessionRef={ref}
      title={session.title}
      triggerLabel={`Actions for ${session.title}`}
      canRename={session.rename === true}
      canShutdown={session.live && session.state !== "restartRequired"}
      stopped={SHUT_DOWN_STATUSES.has(session.state) || isConfirmedCrashedSession(session)}
      treeNode={session}
      overviewOpen={activitySidebarOpen}
      actions={{
        onOpenOverview: () => actions.onOpenOverview(session),
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

// RailAge is the row's live "last update" stamp.
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

function effectiveSessionState(session: RailSession): string {
  const presented = displayState(session);
  const tally = isTopLevelSession(session) ? subagentTallyToShow(session) : null;
  if (presented === "errored") return "errored";
  // Only blocked attention outranks work. Plain awaiting ("Your move") and a
  // warning share the amber dot family, but running jobs remain what is happening.
  const attentionOutranksWork =
    session.state === "restartRequired" ||
    ((session.state === "awaiting" || session.state === "warning") && session.ask_pending === true) ||
    approvalWaiting(session.state, session.approval_pending === true);
  if (attentionOutranksWork) return presented;
  if (session.state === "active" || (session.running_job_count ?? 0) > 0 || (tally?.running ?? 0) > 0) {
    return "active";
  }
  return presented;
}

function sessionStatusLabel(session: RailSession, effectiveState: string, notStarted: boolean): string {
  const state = cadenceStateFor(effectiveState);
  if (notStarted) return "Not started";
  if (state === "failed") return CADENCE_LABEL.failed;
  if (state === "working") return CADENCE_LABEL.working;
  const label = humanizeState(session.state, session.ask_pending === true, session.approval_pending === true);
  return label.charAt(0).toUpperCase() + label.slice(1);
}

function ContextRow({ label, value, mono = false }: { label: string; value?: ReactNode; mono?: boolean }) {
  if (value === undefined || value === null || value === "") return null;
  return (
    <div className={CLASS.contextRow}>
      <dt className={CLASS.contextKey}>{label}</dt>
      <dd className={`${CLASS.contextValue}${mono ? ` ${CLASS.contextMono}` : ""}`}>{value}</dd>
    </div>
  );
}

function subagentContext(session: RailSession): string | undefined {
  const tally = session.subagents;
  if (tally === undefined) return undefined;
  const parts: string[] = [];
  if (tally.running > 0) parts.push(`${tally.running} running`);
  if (tally.failed > 0) parts.push(`${tally.failed} failed`);
  if (tally.done > 0) parts.push(`${tally.done} done`);
  return parts.length > 0 ? parts.join(", ") : undefined;
}

function ContextAgeRow({ updatedAt }: { updatedAt?: string }) {
  const now = useRailNow();
  return <ContextRow label="Age" value={relativeAge(updatedAt, now)} />;
}

function SessionContextCard({
  session,
  effectiveState,
  notStarted,
}: {
  session: RailSession;
  effectiveState: string;
  notStarted: boolean;
}) {
  const hostOnline = useHostOnline(session.host_id);
  const pinSection = useNavigationStore((state) => {
    if (session.pin_section_id === undefined || !isTopLevelSession(session)) return undefined;
    return (
      selectPinSectionSummaries(state).find((section) => section.id === session.pin_section_id)?.name ??
      session.pin_section_id
    );
  });
  const jobs = session.running_job_count ?? 0;
  const watches = session.watch_count ?? 0;
  const armedWatches = activeWatchCount(session);
  const status = sessionStatusLabel(session, effectiveState, notStarted);
  return (
    <div className={CLASS.contextCard} data-state={cadenceStateFor(effectiveState)}>
      <div className={CLASS.contextHead}>
        <strong className={CLASS.contextKind}>Session</strong>
        <span className={CLASS.contextStatus}>{status}</span>
      </div>
      <div className={CLASS.contextSummary}>{session.title}</div>
      <dl className={CLASS.contextRows}>
        <ContextRow label="Project" value={session.project} />
        <ContextRow
          label="Host"
          value={session.host_id && !hostOnline ? `${session.host_id} (offline)` : session.host_id}
          mono
        />
        <ContextRow label="Branch" value={session.branch} mono />
        <ContextRow label="Jobs" value={jobs > 0 ? `${jobs} running` : undefined} />
        <ContextRow label="Subagents" value={subagentContext(session)} />
        <ContextRow label="Watches" value={watches > 0 ? watchCountLabel(armedWatches, watches) : undefined} />
        <ContextRow label="Pinned" value={pinSection} />
        <ContextRow
          label="Tier"
          value={session.tier !== undefined && session.tier !== "current" ? session.tier : undefined}
        />
        <ContextAgeRow updatedAt={session.updated_at} />
      </dl>
    </div>
  );
}

function SessionTitle({
  session,
  effectiveState,
  notStarted,
  focusTarget,
}: {
  session: RailSession;
  effectiveState: string;
  notStarted: boolean;
  focusTarget: () => HTMLElement | null;
}) {
  const sideAnchor = useCallback(() => {
    const row = focusTarget();
    const rail = row?.closest<HTMLElement>("[data-sidebar-rail]");
    if (!row || !rail) return null;
    return { rowRect: row.getBoundingClientRect(), sideRight: rail.getBoundingClientRect().right };
  }, [focusTarget]);
  return (
    <span className={CLASS.sessionTitle}>
      <HoverCard
        label={<SessionContextCard session={session} effectiveState={effectiveState} notStarted={notStarted} />}
        focusTarget={focusTarget}
        sideAnchor={sideAnchor}
        longPressEnabled
      >
        {({ describedBy }) => (
          <button
            type="button"
            tabIndex={-1}
            data-testid="rail-row-title"
            className={CLASS.label}
            aria-describedby={describedBy}
            onMouseDown={(event) => {
              if (event.button !== 0) return;
              event.preventDefault();
              const target = focusTarget();
              if (target === document.activeElement) return;
              target?.focus();
            }}
          >
            {session.title}
          </button>
        )}
      </HoverCard>
    </span>
  );
}

function SessionRow({ node, info, actions }: { node: SessionRailNode; info: TreeRowInfo; actions: RailRowActions }) {
  const { session } = node;
  const effectiveState = effectiveSessionState(session);
  const hasSignal = SIGNAL_STATES.has(cadenceStateFor(effectiveState));
  const notStarted = saysNotStarted(session, hasSignal);
  const rowRef = useRef<HTMLSpanElement>(null);
  const focusTarget = useCallback(() => rowRef.current?.closest<HTMLElement>('[role="treeitem"]') ?? null, []);
  return (
    // data-session-ref is the scroll target Rail's reveal effect (the palette's
    // /project command via railController) queries to bring a session's row
    // into view - the ref is stable and unique per session, unlike the label.
    <span ref={rowRef} className={CLASS.railRow} data-session-ref={session.ref}>
      {/* biome-ignore lint/a11y/noStaticElementInteractions: redundant with the row's own Enter handling, see below */}
      {/* biome-ignore lint/a11y/useKeyWithClickEvents: redundant with the row's own Enter handling, see below */}
      <span className={CLASS.textCol} onClick={info.activate}>
        {/* Mouse-only shortcut for the same activation Enter already performs
            on the owning treeitem - can't use aria-hidden the way Chevron
            does, since this text IS the treeitem's accessible name (no
            separate aria-label on the row). */}
        <span className={CLASS.titleLine}>
          <Signal wireState={effectiveState} />
          <SessionTitle
            session={session}
            effectiveState={effectiveState}
            notStarted={notStarted}
            focusTarget={focusTarget}
          />
          <TrailingChevron info={info} />
        </span>
      </span>
      {/* The timestamp shares its slot with hover actions. */}
      <span className={CLASS.rightSlot}>
        {notStarted ? (
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
  // The project-wide rollup is an aggregate fact, so it reads ONCE: on the
  // rows that own the project's aggregate facts - every single-copy tier's
  // canonical row (flat, test runs, archived) or a hostless row (the
  // no-sources builder contract) - or on the one aggregate row the grouped
  // modes mark (the project-first project row, or the canonical host-first
  // copy - the first host in rail order with loaded rows, the same copy
  // that renders the project's overflow). Every other copy claims nothing
  // - an honest per-host count would need wire support the manifest does
  // not carry, the same line the host group row itself draws.
  const showsRollup = node.spawnHost === undefined || node.canonicalCopy === true;
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

// project.sources is the project's ownership (project-level mutations are
// keyed by (source, project ID), and the row's menu closes over the project
// object), so the memo comparator must compare it by contents: an ownership
// change (a host attached or detached) re-renders the row and its action
// closures, while an unchanged list reuses the memoized row.
function projectSourcesEqual(a: readonly string[] | undefined, b: readonly string[] | undefined): boolean {
  if (a === b) return true;
  if (a === undefined || b === undefined) return false;
  return a.length === b.length && a.every((value, index) => value === b[index]);
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

// The clock a watch row leads with, DRAWN rather than typed: the app's font
// ranges stop at U+2215, so a "◷" (or any other clock code point) falls back
// to a system font - and the rail's accessible name is name-from-content, so a
// typed glyph would be announced as a stray character rather than as "watch"
// aria-hidden because the visually-hidden "Watch:"
// beside it is the word assistive tech should read.
//
// Exported for the activity sidebar's watch rows: the geometry lives here
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

// The "+N older" note for rows the server capped away (hubcore's
// maxSidebarSessionsPerTier). Its text starts at the same x as every other
// row's, with no dot or chevron of its own. Project overflow rows activate a
// bounded fetch for the capped-away tier rows.
function OverflowRow({ node, info }: { node: OverflowRailNode; info: TreeRowInfo }) {
  return (
    <span className={CLASS.railRow}>
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
    previous.node.spawnHost === next.node.spawnHost &&
    previous.node.canonicalCopy === next.node.canonicalCopy &&
    previous.node.displayName === next.node.displayName &&
    previous.node.resourceError === next.node.resourceError &&
    previous.node.retry === next.node.retry &&
    previousProject.key === nextProject.key &&
    previousProject.name === nextProject.name &&
    previousProject.working_dir === nextProject.working_dir &&
    projectSourcesEqual(previousProject.sources, nextProject.sources) &&
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
