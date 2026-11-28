// RailRow is the Tree widget's renderRow implementation for the sidebar:
// given one RailNode (railNodes.ts) and the TreeRowInfo the Tree widget
// computed for it (depth/expanded/hasChildren/toggle/activate), it renders
// one compact line: an outdented status indicator when action or work is
// present, the title, a trailing disclosure chevron, and the age/actions
// slot. Session context that used to make rows taller lives in the title's
// HoverCard; a row in one of Live's bands may add one second line saying why
// it is there (RailWhy). Pure presentation: every
// mutation goes back out through the `actions` prop, which Rail.tsx
// implements against actions.ts + the tree store's refresh().
//
// The rail is a TRIAGE surface. Broken is red, needs-you is yellow, and running
// work is a grey spinner. Broken attention and anything waiting on a person
// (a question or an approval) outrank work; an awaiting frame from an older
// daemon with no pending question is a finished turn and reads idle. A warning
// yields to work already in flight. Idle and ended rows have no indicator. The
// stable one-line rhythm keeps the
// title list scannable while the HoverCard preserves project, host, branch,
// jobs, subagents, watches, tier, and age without permanent visual noise.
//
// CLASS.actions (RailRow.module.css) is what makes the "..." trigger (and a
// project row's "+") quiet: transparent/borderless by default, revealed only
// on row hover/focus, matching the design bar (Linear/VS Code-quality
// sidebar - quiet, hover-revealed, zero layout shift) instead of a
// permanently-visible bordered button on every row. It lives in the shared
// .rightSlot grid cell with the row's right-slot occupant (timestamp /
// "Not started"), so revealing it covers the occupant instead of
// narrowing the title column with a reserved slot of its own - and the
// trailing disclosure chevron, which lives in .textCol left of the slot,
// stays layout-disjoint from the menu at every width. See
// RailRow.module.css's own comment on .rightSlot for the exact selectors
// (row hover, treeitem focus, open-menu, and the <900px touch fallback that
// keeps the actions visible beside the occupant - in flow, not stacked -
// with no hover to reveal them).

import {
  humanizeState,
  LOCAL_HOST,
  SHUT_DOWN_STATUSES,
  watchCadenceLabel,
  watchDurationLabel,
  watchGloss,
} from "@evener/appwire-client";
import {
  type Band,
  type BoardState,
  boardState,
  passedQuotaResetText,
  quotaWaitState,
  waitingForQuotaText,
  whyLine,
} from "@evener/appwire-client/state/navigation";
import {
  createContext,
  type HTMLAttributes,
  memo,
  type ReactNode,
  type Ref,
  useCallback,
  useContext,
  useRef,
} from "react";
import { settingsURLWithQuery } from "../../panes/settings/settingsHost";
import {
  relativeAge,
  selectDisplaySources,
  selectPinSectionSummaries,
  selectSources,
} from "../../stores/navigation/selectors";
import { useNavigationStore } from "../../stores/navigation/store";
import {
  Button,
  type CadenceState,
  Chevron,
  Eyebrow,
  HoverCard,
  IconButton,
  Menu,
  type MenuItem,
  PropertyList,
  type PropertyListRow,
  Skeleton,
  STATE_LABELS,
  StatusDot,
  type TreeRowInfo,
} from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { useActivitySidebarOpenFor } from "../activitybar/activitySidebarStore";
import { navigate } from "../routing";
import { type PinTarget, SessionMenu } from "../sessionMenu/SessionMenu";
import { cadenceStateFor, SIGNAL_STATES } from "../sessionStatus";
import { useIsMobile } from "../useIsMobile";
import styles from "./RailRow.module.css";
import { type RailActivityRead, type RailStatus, useSessionActivityRead, workingStatus } from "./railActivity";
import { HostGlyph, ICON, MoreIcon, PlusIcon, STROKE } from "./railIcons";
import {
  activeWatchCount,
  type BandRailNode,
  effectiveSessionState,
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

export const RailFlatContext = createContext(false);

const CLASS = {
  railRow: requireClass(styles.railRow, "RailRow.module.css", "railRow"),
  stackedRow: requireClass(styles.stackedRow, "RailRow.module.css", "stackedRow"),
  belowRow: requireClass(styles.belowRow, "RailRow.module.css", "belowRow"),
  flatSession: requireClass(styles.flatSession, "RailRow.module.css", "flatSession"),
  flatStatus: requireClass(styles.flatStatus, "RailRow.module.css", "flatStatus"),
  flatMeta: requireClass(styles.flatMeta, "RailRow.module.css", "flatMeta"),
  metaText: requireClass(styles.metaText, "RailRow.module.css", "metaText"),
  rightSlot: requireClass(styles.rightSlot, "RailRow.module.css", "rightSlot"),
  actions: requireClass(styles.actions, "RailRow.module.css", "actions"),
  chevronButton: requireClass(styles.chevronButton, "RailRow.module.css", "chevronButton"),
  signal: requireClass(styles.signal, "RailRow.module.css", "signal"),
  unreadDot: requireClass(styles.unreadDot, "RailRow.module.css", "unreadDot"),
  textCol: requireClass(styles.textCol, "RailRow.module.css", "textCol"),
  titleLine: requireClass(styles.titleLine, "RailRow.module.css", "titleLine"),
  meta: requireClass(styles.meta, "RailRow.module.css", "meta"),
  label: requireClass(styles.label, "RailRow.module.css", "label"),
  sessionTitleButton: requireClass(styles.sessionTitleButton, "RailRow.module.css", "sessionTitleButton"),
  contextCard: requireClass(styles.contextCard, "RailRow.module.css", "contextCard"),
  contextHead: requireClass(styles.contextHead, "RailRow.module.css", "contextHead"),
  contextStatus: requireClass(styles.contextStatus, "RailRow.module.css", "contextStatus"),
  contextSummary: requireClass(styles.contextSummary, "RailRow.module.css", "contextSummary"),
  quotaWait: requireClass(styles.quotaWait, "RailRow.module.css", "quotaWait"),
  why: requireClass(styles.why, "RailRow.module.css", "why"),
  whyReason: requireClass(styles.whyReason, "RailRow.module.css", "whyReason"),
  bandLabel: requireClass(styles.bandLabel, "RailRow.module.css", "bandLabel"),
  railCount: requireClass(styles.railCount, "RailRow.module.css", "railCount"),
  countNum: requireClass(styles.countNum, "RailRow.module.css", "countNum"),
  time: requireClass(styles.time, "RailRow.module.css", "time"),
  notStarted: requireClass(styles.notStarted, "RailRow.module.css", "notStarted"),
  host: requireClass(styles.host, "RailRow.module.css", "host"),
  hostOffline: requireClass(styles.hostOffline, "RailRow.module.css", "hostOffline"),
  hostGlyph: requireClass(styles.hostGlyph, "RailRow.module.css", "hostGlyph"),
  star: requireClass(styles.star, "RailRow.module.css", "star"),
  loadingRow: requireClass(styles.loadingRow, "RailRow.module.css", "loadingRow"),
  overflow: requireClass(styles.overflow, "RailRow.module.css", "overflow"),
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
function Signal({ state }: { state: CadenceState }) {
  if (!SIGNAL_STATES.has(state)) return null;
  return (
    <RowGutter className={CLASS.signal} testId="rail-row-signal">
      <StatusDot state={state} />
    </RowGutter>
  );
}

// Replies the person has not read since they last opened this session, drawn
// as the phone's Board draws them: a small accent dot beside the age. Separate
// from the status dot by design - needs-you says a person is needed, this says
// there is something new to read - so it carries its own name.
function UnreadMarker() {
  return <span role="img" aria-label="Unread" data-testid="rail-row-unread" className={CLASS.unreadDot} />;
}

// The rail no longer renders watch rows of its own (the activity sidebar's
// Watches tab owns them), but its tests and the sidebar's watch rows share
// the wire's wording through these re-exports.
export { watchCadenceLabel, watchDurationLabel, watchGloss };

export interface RailRowActions {
  onOpenOverview(session: RailSession): void;
  /** A row's menu opened (true) or closed (false) one of its dialogs. */
  onRowDialogChange(open: boolean): void;
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
// title text (and any inline heading count, before any star/timestamp), on branch rows only - the
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
      icon={<MoreIcon />}
      label={`Actions for ${label}`}
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
  const cwd = project.working_dir;
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
    ...(cwd
      ? [
          {
            id: "project-settings",
            label: "Project settings",
            onSelect: () =>
              navigate(settingsURLWithQuery("project", spawnHost ?? "local", new URLSearchParams({ cwd }).toString()), {
                carrySettingsHost: false,
              }),
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
      onDialogOpenChange={actions.onRowDialogChange}
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
// the model precomputed. Sitting BELOW the memoized SessionRow is what keeps a
// tick from re-rendering every row that carries a stamp.
function RailAge({ updatedAt, project }: { updatedAt?: string; project?: string }): ReactNode {
  const now = useRailNow();
  const label = relativeAge(updatedAt, now);
  return (
    <>
      {label && (
        <span data-testid="rail-row-time" className={CLASS.time}>
          {label}
        </span>
      )}
      {label && project && " · "}
      {project && <span>{project}</span>}
    </>
  );
}

// RailQuotaWait is the row's second line while the session waits out a
// provider usage limit: working, but paused until a known resume time. It is
// a read-sampled surface - the navigation failure summary the daemon
// refreshes into reads - so it may trail the pane by one refresh; it renders
// the wait's state from the same shared tri-state rule the pane's line reads,
// so both surfaces agree whenever each observes it. Without it the row reads
// as a stuck spinner. It is its own leaf so the countdown ticks without
// re-rendering the memoized row, and it sits inside the treeitem so the wait
// is part of the row's accessible name.
function RailQuotaWait({ session }: { session: RailSession }): ReactNode {
  const now = useRailNow();
  const quota = quotaWaitState(session.failure?.resumes_at, now);
  if (quota === null) return null;
  // Past its instant the line says the retry is pending (the daemon keeps
  // serving the wait's summary while it holds it) rather than going silent,
  // until a refreshed summary shows no wait at all.
  const text = quota.phase === "pending" ? passedQuotaResetText() : waitingForQuotaText(quota.remainingMs);
  return (
    <span
      data-testid="rail-row-quota-wait"
      // The wait's phase as a fact, matching the pane's line's marker.
      data-passed-reset={quota.phase === "pending" ? "true" : undefined}
      // The computed runway as a fact beside the sentence: tests assert
      // this, never the copy.
      data-remaining-ms={quota.phase === "counting" ? String(quota.remainingMs) : undefined}
      className={CLASS.quotaWait}
    >
      {text}
    </span>
  );
}

// RailWhy is a Live row's second line: why the row sits in its band, then its
// host where the band has to name it (liveBandNodes). It prints only what the
// band header and the dot do not already say: which kind of attention a
// Needs-you row wants, and what a working row is waiting on or running. A row
// that is simply working or idle keeps its one line. Only a row waiting out a
// usage limit counts down, so only that row reads the rail's clock
// (RailWhyTicking): every other line is a function of the summary alone and
// stays asleep through a tick.

// A host's plain name, plus the rail's one "(offline)" suffix where the
// manifest says the host is unreachable.
function offlineHostLabel(name: string, online: boolean): string {
  return online ? name : `${name} (offline)`;
}

function RailHostCue({ label, online }: NonNullable<SessionRailNode["liveHost"]>) {
  return (
    <span data-testid="rail-row-why-host" data-host-online={String(online)}>
      {offlineHostLabel(label, online)}
    </span>
  );
}

interface RailWhyProps {
  session: RailSession;
  state: BoardState;
  host?: { label: string; online: boolean };
  showQuestionText: boolean;
}

function RailWhy(props: RailWhyProps): ReactNode {
  return props.session.failure?.resumes_at ? <RailWhyTicking {...props} /> : <RailWhyLine {...props} now={0} />;
}

function RailWhyTicking(props: RailWhyProps): ReactNode {
  return <RailWhyLine {...props} now={useRailNow()} />;
}

function RailWhyLine({ session, state, host, showQuestionText, now }: RailWhyProps & { now: number }): ReactNode {
  const line = whyLine({ row: session, state }, undefined, 0, now);
  // A pending question's own words are the row's useful second line on the
  // flat layout and the compact phone/tablet rail. The question reads in
  // ordinary ink; the dot names the state. With no usable text, the shared
  // state word is the available attention cue.
  const asked = showQuestionText && state === "question" ? session.question?.text.trim() || undefined : undefined;
  // A reason line's word names the attention; the sentence after it only
  // tells the reader to open the session, which the row already invites. The
  // bare state word repeats the band's own name.
  const said =
    line === null || line.kind === "state"
      ? undefined
      : line.kind === "reason"
        ? { text: asked ?? line.word, hue: asked === undefined ? line.hue : undefined, kind: line.kind }
        : { text: line.text, hue: undefined, kind: line.kind };
  if (said === undefined && host === undefined) return null;
  return (
    <span data-testid="rail-row-why" className={CLASS.why}>
      {said !== undefined && (
        <span
          data-testid="rail-row-why-text"
          data-why-kind={said.kind}
          data-hue={said.hue}
          className={said.hue === undefined ? undefined : CLASS.whyReason}
        >
          {said.text}
        </span>
      )}
      {said !== undefined && host !== undefined && " · "}
      {host && <RailHostCue {...host} />}
    </span>
  );
}

function sessionStatusLabel(
  session: RailSession,
  state: CadenceState,
  notStarted: boolean,
  read: RailActivityRead | null,
  now: number,
): RailStatus {
  if (notStarted) return { kind: "not-started", text: "Not started" };
  const quota = quotaWaitState(session.failure?.resumes_at, now);
  // The wait's phase from the same shared tri-state rule the row's own line
  // and the pane's line read, until a refreshed summary shows no wait.
  if (quota !== null) {
    return {
      kind: "quota",
      text: quota.phase === "pending" ? passedQuotaResetText() : waitingForQuotaText(quota.remainingMs),
    };
  }
  if (state === "failed") return { kind: "failed", text: STATE_LABELS.failed };
  if (state === "working") return workingStatus(session, read);
  const label = humanizeState(session.state, session.ask_pending === true, session.approval_pending === true);
  return { kind: "state", text: label.charAt(0).toUpperCase() + label.slice(1) };
}

// Every fact wraps: the card is where the reader confirms which session
// this is, so an ellipsis would hide the part that tells two apart. A fact
// the session doesn't have is left out rather than shown blank.
function contextRow(label: string, value: string | undefined, mono = false): PropertyListRow[] {
  return value === undefined || value === "" ? [] : [{ label, value, mono, wrap: true }];
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
  const state = cadenceStateFor(effectiveState, {
    askPending: session.ask_pending === true,
    approvalPending: session.approval_pending === true,
  });
  // Only a working row asks the hub what the session is doing: every other
  // state word is already settled by the row's own fields.
  const read = useSessionActivityRead(session.ref, state === "working");
  const now = useRailNow();
  const status = sessionStatusLabel(session, state, notStarted, read, now);
  return (
    <div className={CLASS.contextCard} data-state={state}>
      <div className={CLASS.contextHead}>
        <Eyebrow as="span">Session</Eyebrow>
        <span
          className={CLASS.contextStatus}
          data-testid="rail-context-status"
          data-status-kind={status.kind}
          data-hue={state === "needs-you" ? "attention" : state === "failed" ? "danger" : undefined}
        >
          {status.text}
        </span>
      </div>
      <div className={CLASS.contextSummary}>{session.title}</div>
      <PropertyList
        rows={[
          ...contextRow("Project", session.project),
          ...contextRow("Host", session.host_id ? offlineHostLabel(session.host_id, hostOnline) : undefined, true),
          ...contextRow("Branch", session.branch, true),
          ...contextRow("Jobs", jobs > 0 ? `${jobs} running` : undefined),
          ...contextRow("Subagents", subagentContext(session)),
          ...contextRow("Watches", watches > 0 ? watchCountLabel(armedWatches, watches) : undefined),
          ...contextRow("Pinned", pinSection),
          ...contextRow("Tier", session.tier !== undefined && session.tier !== "current" ? session.tier : undefined),
          ...contextRow("Age", relativeAge(session.updated_at, now)),
        ]}
      />
    </div>
  );
}

function SessionTitle({
  session,
  effectiveState,
  notStarted,
  focusTarget,
  stacked,
  detail,
  metadata,
}: {
  session: RailSession;
  effectiveState: string;
  notStarted: boolean;
  focusTarget: () => HTMLElement | null;
  stacked: boolean;
  detail?: ReactNode;
  metadata?: ReactNode;
}) {
  const sideAnchor = useCallback(() => {
    const row = focusTarget();
    const rail = row?.closest<HTMLElement>("[data-sidebar-rail]");
    if (!row || !rail) return null;
    return { rowRect: row.getBoundingClientRect(), sideRight: rail.getBoundingClientRect().right };
  }, [focusTarget]);
  return (
    <HoverCard
      label={<SessionContextCard session={session} effectiveState={effectiveState} notStarted={notStarted} />}
      focusTarget={focusTarget}
      sideAnchor={sideAnchor}
      longPressEnabled
      block
    >
      {({ describedBy }) => (
        <button
          type="button"
          tabIndex={-1}
          data-testid="rail-row-title"
          className={`${CLASS.label} ${CLASS.sessionTitleButton}`}
          aria-describedby={describedBy}
          onMouseDown={(event) => {
            if (event.button !== 0) return;
            event.preventDefault();
            const target = focusTarget();
            if (target === document.activeElement) return;
            target?.focus();
          }}
        >
          {stacked ? <span className={CLASS.label}>{session.title}</span> : session.title}
          {detail}
          {metadata}
        </button>
      )}
    </HoverCard>
  );
}

// The chrome every rail row shares: the row's box, its text column and title
// line, the trailing chevron, and the optional leading glyph, second line and
// right slot. A kind supplies its own label node - the session's title trigger
// is a button of its own, while the branch kinds' labels carry their own click
// - plus the attributes that identify the row (the session scroll target, a
// host group's title, a band's testid and band).
function RailRowShell({
  info,
  rowRef,
  rowClassName,
  rowProps,
  leading,
  label,
  afterLabel,
  below,
  right,
  activateOnTextCol,
}: {
  info: TreeRowInfo;
  rowRef?: Ref<HTMLSpanElement>;
  rowClassName?: string;
  rowProps?: Omit<HTMLAttributes<HTMLSpanElement>, "className" | "children"> &
    Record<`data-${string}`, string | undefined>;
  leading?: ReactNode;
  label: ReactNode;
  afterLabel?: ReactNode;
  below?: ReactNode;
  right?: ReactNode;
  activateOnTextCol?: boolean;
}) {
  const className = [CLASS.railRow, below && CLASS.stackedRow, rowClassName].filter(Boolean).join(" ");
  return (
    <span ref={rowRef} className={className} {...rowProps}>
      {/* biome-ignore lint/a11y/noStaticElementInteractions: mouse shortcut only - the treeitem owns keyboard activation and the accessible name */}
      {/* biome-ignore lint/a11y/useKeyWithClickEvents: mouse shortcut only - the treeitem owns keyboard activation and the accessible name */}
      <span className={CLASS.textCol} onClick={activateOnTextCol ? info.activate : undefined}>
        <span className={CLASS.titleLine}>
          {leading}
          {label}
          {afterLabel}
          <TrailingChevron info={info} />
        </span>
      </span>
      {right}
      {below && (
        // biome-ignore lint/a11y/noStaticElementInteractions: mouse shortcut only - the treeitem owns keyboard activation and the accessible name
        // biome-ignore lint/a11y/useKeyWithClickEvents: mouse shortcut only - the treeitem owns keyboard activation and the accessible name
        <span className={CLASS.belowRow} onClick={activateOnTextCol ? info.activate : undefined}>
          {below}
        </span>
      )}
    </span>
  );
}

function SessionRow({ node, info, actions }: { node: SessionRailNode; info: TreeRowInfo; actions: RailRowActions }) {
  const { session } = node;
  const effectiveState = effectiveSessionState(session);
  const signalState = cadenceStateFor(effectiveState, {
    askPending: session.ask_pending === true,
    approvalPending: session.approval_pending === true,
  });
  const hasSignal = SIGNAL_STATES.has(signalState);
  const notStarted = saysNotStarted(session, hasSignal);
  const isMobile = useIsMobile();
  const flat = useContext(RailFlatContext);
  const stacked = flat || isMobile;
  const rowRef = useRef<HTMLSpanElement>(null);
  const focusTarget = useCallback(() => rowRef.current?.closest<HTMLElement>('[role="treeitem"]') ?? null, []);
  const host = node.liveHost;
  const state = node.live ?? (flat ? boardState({ ...session, state: effectiveState }, false) : undefined);
  const detail =
    state !== undefined ? (
      <RailWhy session={session} state={state} host={flat ? undefined : host} showQuestionText={stacked} />
    ) : cadenceStateFor(effectiveState) === "working" ? (
      <RailQuotaWait session={session} />
    ) : null;
  // A newly created session has no elapsed work to report.
  const dormant = notStarted ? (
    <span data-testid="rail-row-not-started" className={CLASS.notStarted}>
      Not started
    </span>
  ) : null;
  const unread = !notStarted && node.unseen === true ? <UnreadMarker /> : null;
  const metadata = flat ? (
    <span className={`${CLASS.meta} ${CLASS.flatMeta}`}>
      {unread}
      <span className={CLASS.metaText}>
        <RailAge updatedAt={notStarted ? undefined : session.updated_at} project={session.project?.trim()} />
      </span>
      {host && <RailHostCue {...host} />}
    </span>
  ) : null;
  return (
    // data-session-ref is the scroll target Rail's reveal effect (the palette's
    // /project command via railController) queries to bring a session's row
    // into view - the ref is stable and unique per session, unlike the label.
    // data-row-focused is the treeitem's own focus, from Tree: it reveals the
    // row's actions.
    <RailRowShell
      info={info}
      rowRef={rowRef}
      rowClassName={flat ? CLASS.flatSession : undefined}
      rowProps={{
        "data-session-ref": session.ref,
        "data-row-focused": info.focused ? "true" : undefined,
      }}
      activateOnTextCol
      leading={<Signal state={signalState} />}
      label={
        <SessionTitle
          session={session}
          effectiveState={effectiveState}
          notStarted={notStarted}
          focusTarget={focusTarget}
          stacked={stacked}
          detail={
            flat ? (
              <span data-testid="rail-row-status" className={CLASS.flatStatus}>
                {dormant ?? detail}
              </span>
            ) : isMobile ? (
              detail
            ) : undefined
          }
          metadata={metadata}
        />
      }
      below={stacked ? null : detail}
      // The timestamp shares its slot with hover actions.
      right={
        <span className={CLASS.rightSlot}>
          {!flat && (
            <span className={CLASS.meta}>
              {unread}
              {dormant ?? <RailAge updatedAt={session.updated_at} />}
            </span>
          )}
          <span className={CLASS.actions}>
            <SessionMenuRow session={session} actions={actions} />
          </span>
        </span>
      }
    />
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
    // Same title-line anatomy as SessionRow: outdented signal dot, name,
    // trailing chevron on a branch row.
    <RailRowShell
      info={info}
      rowProps={{ "data-row-focused": info.focused ? "true" : undefined }}
      activateOnTextCol
      leading={
        <Signal
          state={cadenceStateFor(showsRollup ? (project.rollup_state ?? "idle") : "idle", {
            askPending: showsRollup && project.rollup_state === "awaiting",
          })}
        />
      }
      // Same reasoning as SessionRow's own label above. displayName is the
      // UX-fix decoration railNodes.ts's projectDisplayLabels stamps on when
      // this project's name collides with a sibling's in the same list; it
      // falls back to the bare name otherwise (and for a hand-built test
      // double that omits it).
      label={<span className={CLASS.label}>{node.displayName ?? project.name}</span>}
      afterLabel={
        <>
          {showsRollup && attentionCount > 0 && <RailCount count={attentionCount} />}
          {resourceError && retry && (
            <Button
              variant="quiet"
              size="xs"
              onClick={(event) => {
                event.stopPropagation();
                retry();
              }}
            >
              Retry
            </Button>
          )}
        </>
      }
      right={
        <>
          {project.favorite === true && (
            <span data-testid="favorite-star" aria-hidden="true" className={CLASS.star}>
              {"★"}
            </span>
          )}
          <span className={CLASS.rightSlot}>
            <span className={CLASS.actions}>
              {project.key !== NO_PROJECT_KEY && canSpawn && (
                <IconButton
                  label={`New session in ${project.name}`}
                  icon={<PlusIcon />}
                  variant="quiet"
                  size="sm"
                  tabIndex={-1}
                  onClick={() => spawnInProject(project, node.spawnHost)}
                />
              )}
              <ActionsMenu label={project.name} items={projectMenuItems(project, actions, node.spawnHost, canSpawn)} />
            </span>
          </span>
        </>
      }
    />
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
// italic, dimmed, "(offline)" in the caption ink. No rollup count and no
// actions: the manifest carries no per-host attention count, and the rows
// under the group keep their own signals and menus.
function HostRow({ node, info }: { node: HostRailNode; info: TreeRowInfo }) {
  const { host } = node;
  return (
    <RailRowShell
      info={info}
      rowProps={{
        "data-testid": "rail-row-host-group",
        title: host.online ? `Host ${host.label}` : `Host ${host.label} is offline`,
      }}
      leading={<HostGlyph className={CLASS.hostGlyph} testId="rail-row-host-glyph" />}
      label={
        // biome-ignore lint/a11y/noStaticElementInteractions: redundant with the row's own Enter handling, see SessionRow
        // biome-ignore lint/a11y/useKeyWithClickEvents: redundant with the row's own Enter handling, see SessionRow
        <span className={host.online ? CLASS.label : `${CLASS.label} ${CLASS.hostOffline}`} onClick={info.activate}>
          {host.label}
        </span>
      }
      afterLabel={
        !host.online && (
          <span data-testid="rail-row-host-group-offline" className={CLASS.host}>
            {" (offline)"}
          </span>
        )
      }
    />
  );
}

function RailCount({ count, testId }: { count: number; testId?: string }) {
  return (
    <span className={CLASS.railCount}>
      <span aria-hidden="true">{"·"}</span>
      <span data-testid={testId} className={CLASS.countNum}>
        {count}
      </span>
    </span>
  );
}

// A collapsed band still says how many loaded sessions it hides.
// The rail's band words are the cadence state words, so they read one table.
const bandLabel = (band: Band): string => STATE_LABELS[band === "needsYou" ? "needs-you" : band];

function BandRow({ node, info }: { node: BandRailNode; info: TreeRowInfo }) {
  return (
    <RailRowShell
      info={info}
      rowProps={{ "data-testid": "rail-row-band", "data-band": node.band }}
      activateOnTextCol
      label={<span className={`${CLASS.label} ${CLASS.bandLabel}`}>{bandLabel(node.band)}</span>}
      afterLabel={
        /* The count is sessions in the band. A band's own "+N older" row
           stands for rows beyond it, so it is not one of them. */
        <RailCount
          count={node.children.filter((child) => child.kind === "session").length}
          testId="rail-row-band-count"
        />
      }
    />
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
    <svg data-testid={testId} className={className} {...ICON} aria-hidden="true" focusable="false">
      <circle cx="8" cy="8" r="6.25" {...STROKE} />
      <path d="M8 4.5V8l2.5 1.5" {...STROKE} />
    </svg>
  );
}

// The "+N older" note for rows the server capped away (hubcore's
// maxSidebarSessionsPerTier). Its text starts at the same x as every other
// row's, with no dot or chevron of its own. Project overflow rows activate a
// bounded fetch for the capped-away tier rows.
//
// A row that renders away from the list it pages (node.scope set) names that
// list too: it can end up beside another capped list's row with the same
// count, and the two controls must not read the same. Inside a fold the
// surrounding rows already say which list it speaks for, so it stays compact.
// The name is the row's content, so this is both what a reader sees and what
// assistive tech announces.
function OverflowRow({ node, info }: { node: OverflowRailNode; info: TreeRowInfo }) {
  return (
    <span className={CLASS.railRow}>
      {/* The treeitem's Enter handler is the keyboard path; this click makes
          the visible affordance usable with a mouse as well. */}
      {/* biome-ignore lint/a11y/noStaticElementInteractions: treeitem owns keyboard activation and accessible semantics */}
      {/* biome-ignore lint/a11y/useKeyWithClickEvents: treeitem owns keyboard activation */}
      <span data-testid="rail-row-overflow" className={CLASS.overflow} onClick={info.activate}>
        {node.scope ? `${bandLabel(node.scope)} · +${node.count} older` : `+${node.count} older`}
      </span>
    </span>
  );
}

function LoadingRow(): ReactNode {
  return (
    <div className={CLASS.loadingRow}>
      <Skeleton lines={1} />
    </div>
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
    case "band":
      return <BandRow node={node} info={info} />;
    case "session":
      return <SessionRow node={node} info={info} actions={actions} />;
  }
}, railRowPropsEqual);
