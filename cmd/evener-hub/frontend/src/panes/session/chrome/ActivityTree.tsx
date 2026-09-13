import {
  createContext,
  Fragment,
  forwardRef,
  type KeyboardEvent,
  memo,
  type ReactNode,
  useCallback,
  useContext,
  useEffect,
  useImperativeHandle,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import {
  type ActivityDelegate,
  type ActivitySessionNode,
  type ActivityTree as ActivityTreeData,
  activityNodeID,
} from "../../../protocol/activityData";
import {
  type ActivityDelegateRow,
  type ActivityFoldRow,
  type ActivityJobRow,
  type ActivityRow,
  type ActivityWatchRow,
  activityDelegateState,
  buildActivityRows,
  buildWatchRows,
  jobIsFailed,
  watchMeta,
  watchName,
} from "../../../protocol/activityRows";
import type { NavigationWatchSummary } from "../../../protocol/types.gen";
import { WatchGlyph } from "../../../shell/rail/RailRow";
import { armedWatchCount } from "../../../shell/rail/railNodes";
import { Button, Chevron } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { OpenTranscriptButton } from "../transcript/openTranscript";
import { ActivityRowDetail, ActivityWatchDetail } from "./ActivityRowDetail";
import { formatQuietAge, formatUsagePair, jobStatusDotState, quietAnchorMillis } from "./activityFormat";
import styles from "./activitypanel.module.css";

export interface ActivityTreeProps {
  tree: ActivityTreeData;
  expandedFoldIDs: string[];
  onToggleFold: (foldID: string) => void;
  // The session's live watches, absent-able: an old daemon omits the list, and
  // undefined renders exactly as an empty list does.
  watches?: NavigationWatchSummary[];
  // The panel's ticking clock, used only by watch durations and the timeline.
  now?: number;
  continuationFailures?: Record<string, string | undefined>;
  onContinue?: (targetID: string, continuation: string) => void;
  loadingContinuationID?: string;
  // A root refresh in flight is about to replace this tree, every branch's
  // continuation token included, so no page may be requested against it. A page
  // already loading blocks the others the same way: the panel carries one
  // request at a time, so only the branch that asked first can be answered.
  rootRefreshing?: boolean;
}

export interface ActivityTreeHandle {
  focusRow: (id: string) => void;
}

const CLASS = {
  tree: requireClass(styles.tree, "activitypanel.module.css", "tree"),
  denseRow: requireClass(styles.denseRow, "activitypanel.module.css", "denseRow"),
  denseName: requireClass(styles.denseName, "activitypanel.module.css", "denseName"),
  denseNameLive: requireClass(styles.denseNameLive, "activitypanel.module.css", "denseNameLive"),
  denseKind: requireClass(styles.denseKind, "activitypanel.module.css", "denseKind"),
  kindAlive: requireClass(styles.kindAlive, "activitypanel.module.css", "kindAlive"),
  kindAttention: requireClass(styles.kindAttention, "activitypanel.module.css", "kindAttention"),
  kindDanger: requireClass(styles.kindDanger, "activitypanel.module.css", "kindDanger"),
  denseMeta: requireClass(styles.denseMeta, "activitypanel.module.css", "denseMeta"),
  denseQuiet: requireClass(styles.denseQuiet, "activitypanel.module.css", "denseQuiet"),
  denseFailed: requireClass(styles.denseFailed, "activitypanel.module.css", "denseFailed"),
  foldRow: requireClass(styles.foldRow, "activitypanel.module.css", "foldRow"),
  rowToggle: requireClass(styles.rowToggle, "activitypanel.module.css", "rowToggle"),
  rowActions: requireClass(styles.rowActions, "activitypanel.module.css", "rowActions"),
  rowContinuation: requireClass(styles.rowContinuation, "activitypanel.module.css", "rowContinuation"),
  indentGuide: requireClass(styles.indentGuide, "activitypanel.module.css", "indentGuide"),
  watchGlyph: requireClass(styles.watchGlyph, "activitypanel.module.css", "watchGlyph"),
  srOnly: requireClass(styles.srOnly, "activitypanel.module.css", "srOnly"),
  watchGroup: requireClass(styles.watchGroup, "activitypanel.module.css", "watchGroup"),
  watchGroupTitle: requireClass(styles.watchGroupTitle, "activitypanel.module.css", "watchGroupTitle"),
  watchGroupCount: requireClass(styles.watchGroupCount, "activitypanel.module.css", "watchGroupCount"),
};

// Every row kind that owns an expandable detail strip (a fold row toggles its
// own list instead, through onToggleFold).
type DetailRow = ActivityJobRow | ActivityDelegateRow | ActivityWatchRow;

function delegateStatusText(delegate: ActivityDelegate): string {
  return activityDelegateState(delegate).status;
}

function delegateName(delegate: ActivityDelegate): string {
  return delegate.mandate ?? delegate.task ?? delegate.description ?? delegate.child?.label ?? delegate.childSessionId;
}

// The kind glyph ($/⌘) carries the status hue the StatusDot used to: working
// is alive, failed is danger, needs-you is attention, and idle/ended keep the
// glyph's default low ink. The label preserves the dot's accessible name.
const KIND_STATE_LABEL: Record<string, string> = {
  idle: "Idle",
  working: "Working",
  "needs-you": "Needs you",
  failed: "Failed",
  ended: "Ended",
};

function kindStateClass(state: string): string | undefined {
  switch (state) {
    case "working":
      return CLASS.kindAlive;
    case "needs-you":
      return CLASS.kindAttention;
    case "failed":
      return CLASS.kindDanger;
    default:
      return undefined;
  }
}

// transcriptTarget mirrors OpenTranscriptButton's own gate: the ref is
// trimmed and a row with no ref gets no transcript action at all. There is
// deliberately no `job:<id>` fallback - the backend populates
// transcriptRef.
function transcriptTarget(row: ActivityJobRow | ActivityDelegateRow): string | undefined {
  const ref = row.transcriptRef?.trim();
  return ref ? ref : undefined;
}

interface MetaSegment {
  key: string;
  text: string;
  tone?: "quiet" | "failed";
}

function parseMillis(value: string | undefined): number | undefined {
  if (!value) return undefined;
  const parsed = Date.parse(value);
  return Number.isNaN(parsed) ? undefined : parsed;
}

// terminalSegment renders the duration (endedAt - startedAt, quiet-age
// bucketed) when both endpoints parse, else the status text - colored danger
// when the outcome is failure, so a failed row with no endedAt
// never needs a second "failed" suffix.
function terminalSegment(
  job: { startedAt: string; endedAt?: string } | undefined,
  statusText: string,
  failed: boolean,
): MetaSegment {
  if (job) {
    const start = parseMillis(job.startedAt);
    const end = parseMillis(job.endedAt);
    if (start !== undefined && end !== undefined) {
      return { key: "duration", text: formatQuietAge(end - start) };
    }
  }
  return { key: "status", text: statusText, tone: failed ? "failed" : undefined };
}

function jobMetaSegments(row: ActivityJobRow, now: number): MetaSegment[] {
  const { job } = row;
  if (row.live) {
    return [
      { key: "tokens", text: "—" },
      { key: "quiet", text: formatQuietAge(now - quietAnchorMillis(job)), tone: "quiet" },
    ];
  }
  // No "failed" suffix: the colored kind glyph already carries the outcome.
  return [terminalSegment(job, job.status, jobIsFailed(job))];
}

function delegateMetaSegments(row: ActivityDelegateRow, now: number): MetaSegment[] {
  const { delegate } = row;
  const tokens = formatUsagePair(delegate.usage);
  if (row.live) {
    const segments: MetaSegment[] = [{ key: "tokens", text: tokens ?? "—" }];
    if (!tokens) segments.push({ key: "status", text: delegateStatusText(delegate) });
    // quietForMs arrives frozen at snapshot time, and a quiet delegate emits
    // no frames to refresh the snapshot: derive the displayed age from the
    // server's own quiet anchor (latestActivityAt, else runStartedAt — the
    // same fallback the server computes quietForMs from) and the ticking
    // `now`. The frozen value itself is only a last resort for a snapshot
    // with no parseable anchor.
    const quietAnchorAt = parseMillis(
      delegate.latestActivityAt ?? (delegate.quietForMs != null ? delegate.runStartedAt : undefined),
    );
    const quiet = quietAnchorAt !== undefined ? Math.max(0, now - quietAnchorAt) : (delegate.quietForMs ?? undefined);
    if (quiet !== undefined) segments.push({ key: "quiet", text: formatQuietAge(quiet), tone: "quiet" });
    return segments;
  }
  const statusText = delegateStatusText(delegate);
  const segments: MetaSegment[] = [];
  if (tokens) segments.push({ key: "tokens", text: tokens });
  if (delegate.durationMs !== undefined && delegate.durationMs !== null) {
    segments.push({ key: "duration", text: formatQuietAge(delegate.durationMs) });
  } else {
    segments.push(
      terminalSegment(
        { startedAt: delegate.runStartedAt ?? "", endedAt: delegate.runEndedAt },
        statusText,
        activityDelegateState(delegate).failed,
      ),
    );
  }
  return segments;
}

interface ContinuationStrip {
  targetID: string;
  afterRowID: string;
  token?: string;
  branchError?: string;
}

// subtreeLastRowID finds the last visible row belonging to a delegate's
// subtree: the contiguous block of deeper-level rows right after its row.
function subtreeLastRowID(rows: ActivityRow[], delegateRowID: string): string | undefined {
  const index = rows.findIndex((row) => row.id === delegateRowID);
  const row = rows[index];
  if (!row) return undefined;
  let last = index;
  for (let cursor = index + 1; cursor < rows.length; cursor++) {
    const candidate = rows[cursor];
    if (!candidate || candidate.level <= row.level) break;
    last = cursor;
  }
  return rows[last]?.id;
}

// collectContinuations maps each session/delegate branch continuation to the
// row it renders after: the root's strip follows the whole tree, a delegate's
// strip follows its subtree's last visible row. targetID keeps the old
// component's semantics (session node id for the root, delegate node id for
// delegate branches) so the panel store's continuationFailures keys and
// graftContinuationTree targets keep matching.
function collectContinuations(
  tree: ActivityTreeData,
  rows: ActivityRow[],
  continuationFailures: Record<string, string | undefined>,
): ContinuationStrip[] {
  const strips: ContinuationStrip[] = [];
  const root = tree.root;
  const rootID = activityNodeID(root);
  const lastRowID = rows.at(-1)?.id;
  if ((root.branch.continuation || continuationFailures[rootID] !== undefined) && lastRowID) {
    strips.push({
      targetID: rootID,
      afterRowID: lastRowID,
      token: root.branch.continuation,
      branchError: root.branch.error,
    });
  }

  function visitDelegates(session: ActivitySessionNode): void {
    for (const entry of session.entries) {
      if (entry.kind !== "delegate") continue;
      const delegate = entry.delegate;
      const targetID = activityNodeID(entry);
      const token = delegate.child?.branch.continuation ?? delegate.branch.continuation;
      if (token || continuationFailures[targetID] !== undefined) {
        const afterRowID = subtreeLastRowID(rows, targetID);
        if (afterRowID) {
          strips.push({
            targetID,
            afterRowID,
            token,
            branchError: delegate.child?.branch.error ?? delegate.branch.error,
          });
        }
      }
      if (delegate.child) visitDelegates(delegate.child);
    }
  }
  visitDelegates(root);
  return strips;
}

// TreeNowContext carries the live rows' ticking clock. TreeTickProvider is
// the only setInterval in this file, and only context consumers (the live
// meta cluster and live detail strips) re-render on each tick: memoized rows
// and the tree chrome never subscribe, so a tick touches live leaves only.
const TreeNowContext = createContext<number>(0);

function TreeTickProvider({ live, children }: { live: boolean; children: ReactNode }): ReactNode {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!live) return;
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [live]);
  return <TreeNowContext.Provider value={now}>{children}</TreeNowContext.Provider>;
}

function RowSegments({ segments }: { segments: MetaSegment[] }): ReactNode {
  return (
    <span className={CLASS.denseMeta}>
      {segments.map((segment, index) => (
        <Fragment key={segment.key}>
          {index > 0 ? " · " : null}
          <span
            className={
              segment.tone === "failed" ? CLASS.denseFailed : segment.tone === "quiet" ? CLASS.denseQuiet : undefined
            }
          >
            {segment.text}
          </span>
        </Fragment>
      ))}
    </span>
  );
}

// LiveMetaSegments is the per-row tick subscriber for live rows: it reads the
// clock straight from context so the memoized row above it stays asleep.
function LiveMetaSegments({ row }: { row: ActivityJobRow | ActivityDelegateRow }): ReactNode {
  const now = useContext(TreeNowContext);
  const segments = row.kind === "job" ? jobMetaSegments(row, now) : delegateMetaSegments(row, now);
  return <RowSegments segments={segments} />;
}

// Terminal rows never read the clock - terminalSegment derives the duration
// from startedAt/endedAt, never from `now` - so the static cluster renders
// once per row and holds no subscription. The 0 only fills `now`'s slot.
const StaticMetaSegments = memo(function StaticMetaSegments({
  row,
}: {
  row: ActivityJobRow | ActivityDelegateRow;
}): ReactNode {
  const segments = row.kind === "job" ? jobMetaSegments(row, 0) : delegateMetaSegments(row, 0);
  return <RowSegments segments={segments} />;
});

function LiveRowDetail({ row }: { row: ActivityJobRow | ActivityDelegateRow }): ReactNode {
  const now = useContext(TreeNowContext);
  return <ActivityRowDetail row={row} now={now} />;
}

// Static detail strips carry no running age (metaText's terminal line has no
// clock term), so they render once with a dummy instant and never subscribe.
const RowDetail = memo(function RowDetail({ row }: { row: ActivityJobRow | ActivityDelegateRow }): ReactNode {
  if (!row.live) return <ActivityRowDetail row={row} now={0} />;
  return <LiveRowDetail row={row} />;
});

interface FoldRowViewProps {
  row: ActivityFoldRow;
  expanded: boolean;
  tabIndex: number;
  onToggleFold: (foldID: string) => void;
  onFocusRow: (id: string) => void;
  onKeyDown: (event: KeyboardEvent<HTMLDivElement>, row: ActivityRow) => void;
  registerRowRef: (id: string, element: HTMLDivElement | null) => void;
}

// Fold rows carry no clock text, so a memoized view with no subscription
// renders once per (row, expanded, focus) and sleeps through every tick.
const FoldRowView = memo(function FoldRowView({
  row,
  expanded,
  tabIndex,
  onToggleFold,
  onFocusRow,
  onKeyDown,
  registerRowRef,
}: FoldRowViewProps): ReactNode {
  const label = `${row.inactiveCount} inactive`;
  const accessibleLabel = row.failedCount > 0 ? `${label} · ${row.failedCount} failed` : label;
  return (
    <div
      ref={(element) => {
        registerRowRef(row.id, element);
      }}
      role="treeitem"
      aria-label={accessibleLabel}
      aria-level={row.level}
      aria-expanded={expanded}
      tabIndex={tabIndex}
      className={CLASS.foldRow}
      onFocus={() => onFocusRow(row.id)}
      onKeyDown={(event) => onKeyDown(event, row)}
      onClick={() => onToggleFold(row.id)}
    >
      <button
        type="button"
        tabIndex={-1}
        aria-label={`${expanded ? "Collapse" : "Expand"} inactive entries`}
        className={CLASS.rowToggle}
        onClick={(event) => {
          event.stopPropagation();
          onToggleFold(row.id);
        }}
      >
        <Chevron direction={expanded ? "down" : "right"} size={12} />
      </button>
      <span className={CLASS.denseName}>
        {label}
        {row.failedCount > 0 && <span className={CLASS.denseFailed}>{` · ${row.failedCount} failed`}</span>}
      </span>
    </div>
  );
});

interface DenseRowViewProps {
  row: ActivityJobRow | ActivityDelegateRow;
  detailOpen: boolean;
  tabIndex: number;
  onSetDetailOpen: (row: DetailRow, open: boolean) => void;
  onFocusRow: (id: string) => void;
  onKeyDown: (event: KeyboardEvent<HTMLDivElement>, row: ActivityRow) => void;
  registerRowRef: (id: string, element: HTMLDivElement | null) => void;
}

// The row chrome (name, glyph, transcript button, focus, disclosure) is all
// snapshot data, so the memoized view re-renders only when its own props
// change; the ticking pieces live one level down in LiveMetaSegments (the
// quiet-age cluster) and RowDetail (the open strip), which subscribe alone.
const DenseRowView = memo(function DenseRowView({
  row,
  detailOpen,
  tabIndex,
  onSetDetailOpen,
  onFocusRow,
  onKeyDown,
  registerRowRef,
}: DenseRowViewProps): ReactNode {
  const name = row.kind === "job" ? row.job.description : delegateName(row.delegate);
  const statusText = row.kind === "job" ? row.job.status : delegateStatusText(row.delegate);
  const target = transcriptTarget(row);
  const statusState = jobStatusDotState(statusText, true);
  const failed = row.kind === "job" ? jobIsFailed(row.job) : activityDelegateState(row.delegate).failed;
  // Work that has ended says so through its outcome, the verdict the fold and
  // the badge already count; only live work still reads its status.
  const liveState = statusState !== "needs-you" ? "working" : statusState;
  const kindState = failed ? "failed" : row.live ? liveState : "ended";
  const kindClass = kindStateClass(kindState);
  return (
    <Fragment>
      <div
        ref={(element) => {
          registerRowRef(row.id, element);
        }}
        role="treeitem"
        aria-label={name}
        aria-level={row.level}
        aria-expanded={detailOpen}
        tabIndex={tabIndex}
        className={CLASS.denseRow}
        onFocus={() => onFocusRow(row.id)}
        onKeyDown={(event) => onKeyDown(event, row)}
        // Clicking the title toggles the disclosure, same as the chevron;
        // the transcript opens only from the row's open button.
        onClick={() => onSetDetailOpen(row, !detailOpen)}
      >
        <button
          type="button"
          tabIndex={-1}
          aria-label={`${detailOpen ? "Hide" : "Show"} details for ${name}`}
          aria-expanded={detailOpen}
          className={CLASS.rowToggle}
          onClick={(event) => {
            event.stopPropagation();
            onSetDetailOpen(row, !detailOpen);
          }}
        >
          <Chevron direction={detailOpen ? "down" : "right"} size={12} />
        </button>
        <span
          role="img"
          aria-label={KIND_STATE_LABEL[kindState] ?? kindState}
          className={kindClass ? `${CLASS.denseKind} ${kindClass}` : CLASS.denseKind}
        >
          {row.kind === "delegate" ? "⌘" : "$"}
        </span>
        <span className={row.live ? `${CLASS.denseName} ${CLASS.denseNameLive}` : CLASS.denseName}>{name}</span>
        {target && <OpenTranscriptButton transcriptRef={target} parentRef={row.parentRef} tabIndex={-1} />}
        {row.live ? <LiveMetaSegments row={row} /> : <StaticMetaSegments row={row} />}
      </div>
      {detailOpen && <RowDetail row={row} />}
    </Fragment>
  );
});

interface WatchRowViewProps {
  row: ActivityWatchRow;
  detailOpen: boolean;
  tabIndex: number;
  now?: number;
  onSetDetailOpen: (row: DetailRow, open: boolean) => void;
  onFocusRow: (id: string) => void;
  onKeyDown: (event: KeyboardEvent<HTMLDivElement>, row: ActivityRow) => void;
  registerRowRef: (id: string, element: HTMLDivElement | null) => void;
}

// A watch row shares the dense row grammar (toggle, name, right-hand meta) but
// has no live clock cluster: its meta is cadence plus delivery count or armed
// state, all snapshot data. Its detail is the one place the panel's `now` is
// read, so only an open watch detail re-renders on a tick.
function WatchRowView({
  row,
  detailOpen,
  tabIndex,
  now,
  onSetDetailOpen,
  onFocusRow,
  onKeyDown,
  registerRowRef,
}: WatchRowViewProps): ReactNode {
  const name = `Watch: ${watchName(row.watch)}`;
  // A caller with its own ticking clock (the pane chrome) passes `now`; the
  // standalone pane passes nothing and shares the tree's own live tick, so no
  // second clock is installed on a surface that already has none.
  const contextNow = useContext(TreeNowContext);
  const effectiveNow = now ?? contextNow;
  return (
    <Fragment>
      <div
        ref={(element) => {
          registerRowRef(row.id, element);
        }}
        role="treeitem"
        aria-label={name}
        aria-level={row.level}
        aria-expanded={detailOpen}
        tabIndex={tabIndex}
        className={CLASS.denseRow}
        onFocus={() => onFocusRow(row.id)}
        onKeyDown={(event) => onKeyDown(event, row)}
        onClick={() => onSetDetailOpen(row, !detailOpen)}
      >
        <button
          type="button"
          tabIndex={-1}
          aria-label={`${detailOpen ? "Hide" : "Show"} details for ${name}`}
          aria-expanded={detailOpen}
          className={CLASS.rowToggle}
          onClick={(event) => {
            event.stopPropagation();
            onSetDetailOpen(row, !detailOpen);
          }}
        >
          <Chevron direction={detailOpen ? "down" : "right"} size={12} />
        </button>
        <WatchGlyph className={CLASS.watchGlyph} testId="watch-glyph" />
        <span className={CLASS.srOnly}>Watch:</span>
        <span className={CLASS.denseName}>{watchName(row.watch)}</span>
        <span className={CLASS.denseMeta}>{watchMeta(row.watch)}</span>
      </div>
      {detailOpen && <ActivityWatchDetail row={row} now={effectiveNow} />}
    </Fragment>
  );
}

// The Watches group title that leads the panel body, with the count of armed
// watches on its right. No "needs a look" count: the projection tracks no
// drops, so there is no abnormal count to report.
function WatchGroupHeader({ armed }: { armed: number }): ReactNode {
  return (
    <div className={CLASS.watchGroup} data-testid="watch-group">
      <span className={CLASS.watchGroupTitle}>Watches</span>
      <span className={CLASS.watchGroupCount}>{`${armed} armed`}</span>
    </div>
  );
}

interface ContinuationStripViewProps {
  strip: ContinuationStrip;
  failure: string | undefined;
  loadingContinuationID?: string;
  rootRefreshing?: boolean;
  onContinue: (targetID: string, continuation: string) => void;
}

const ContinuationStripView = memo(function ContinuationStripView({
  strip,
  failure,
  loadingContinuationID,
  rootRefreshing,
  onContinue,
}: ContinuationStripViewProps): ReactNode {
  return (
    <div className={CLASS.rowActions}>
      <span className={CLASS.rowContinuation}>
        {failure ?? strip.branchError ?? "This branch is partially retained."}
      </span>
      {strip.token && (
        <Button
          variant="quiet"
          size="xs"
          tabIndex={-1}
          disabled={rootRefreshing || loadingContinuationID !== undefined}
          onClick={(event) => {
            event.stopPropagation();
            onContinue(strip.targetID, strip.token ?? "");
          }}
        >
          {loadingContinuationID === strip.targetID ? "Loading…" : "Load more"}
        </Button>
      )}
    </div>
  );
});

interface RowBlockProps {
  slice: ActivityRow[];
  stripsByAfterRowID: Map<string, ContinuationStrip[]>;
  expandedFolds: Set<string>;
  effectiveFocusedID: string | null;
  now?: number;
  isDetailOpen: (row: DetailRow) => boolean;
  onToggleFold: (foldID: string) => void;
  onSetDetailOpen: (row: DetailRow, open: boolean) => void;
  onFocusRow: (id: string) => void;
  onKeyDown: (event: KeyboardEvent<HTMLDivElement>, row: ActivityRow) => void;
  registerRowRef: (id: string, element: HTMLDivElement | null) => void;
  continuationFailures: Record<string, string | undefined>;
  loadingContinuationID?: string;
  rootRefreshing?: boolean;
  onContinue?: (targetID: string, continuation: string) => void;
}

// Rows arrive flat with levels; a delegate's child-session rows are the
// contiguous deeper-level block right after it and nest one group deeper. A
// plain (unmemoized) component: it holds no clock itself, and the tree only
// re-renders it on real data/focus/detail changes - never on a tick.
function RowBlock({
  slice,
  stripsByAfterRowID,
  expandedFolds,
  effectiveFocusedID,
  now,
  isDetailOpen,
  onToggleFold,
  onSetDetailOpen,
  onFocusRow,
  onKeyDown,
  registerRowRef,
  continuationFailures,
  loadingContinuationID,
  rootRefreshing,
  onContinue,
}: RowBlockProps): ReactNode[] {
  const out: ReactNode[] = [];
  let cursor = 0;
  while (cursor < slice.length) {
    const row = slice[cursor];
    if (!row) break;
    const tabIndex = row.id === effectiveFocusedID ? 0 : -1;
    if (row.kind === "fold") {
      out.push(
        <FoldRowView
          key={row.id}
          row={row}
          expanded={expandedFolds.has(row.id)}
          tabIndex={tabIndex}
          onToggleFold={onToggleFold}
          onFocusRow={onFocusRow}
          onKeyDown={onKeyDown}
          registerRowRef={registerRowRef}
        />,
      );
    } else if (row.kind === "watch") {
      out.push(
        <WatchRowView
          key={row.id}
          row={row}
          detailOpen={isDetailOpen(row)}
          tabIndex={tabIndex}
          now={now}
          onSetDetailOpen={onSetDetailOpen}
          onFocusRow={onFocusRow}
          onKeyDown={onKeyDown}
          registerRowRef={registerRowRef}
        />,
      );
    } else {
      out.push(
        <DenseRowView
          key={row.id}
          row={row}
          detailOpen={isDetailOpen(row)}
          tabIndex={tabIndex}
          onSetDetailOpen={onSetDetailOpen}
          onFocusRow={onFocusRow}
          onKeyDown={onKeyDown}
          registerRowRef={registerRowRef}
        />,
      );
    }
    for (const strip of stripsByAfterRowID.get(row.id) ?? []) {
      out.push(
        onContinue ? (
          <ContinuationStripView
            key={`${strip.targetID}-continuation`}
            strip={strip}
            failure={continuationFailures[strip.targetID]}
            loadingContinuationID={loadingContinuationID}
            rootRefreshing={rootRefreshing}
            onContinue={onContinue}
          />
        ) : null,
      );
    }
    let end = cursor + 1;
    while (end < slice.length) {
      const candidate = slice[end];
      if (!candidate || candidate.level <= row.level) break;
      end++;
    }
    if (end > cursor + 1) {
      out.push(
        // role="group" is the WAI-ARIA treeview pattern's nested-children container.
        // biome-ignore lint/a11y/useSemanticElements: role="group" is deliberate tree semantics
        <div role="group" className={CLASS.indentGuide} key={`${row.id}-group`}>
          <RowBlock
            slice={slice.slice(cursor + 1, end)}
            stripsByAfterRowID={stripsByAfterRowID}
            expandedFolds={expandedFolds}
            effectiveFocusedID={effectiveFocusedID}
            now={now}
            isDetailOpen={isDetailOpen}
            onToggleFold={onToggleFold}
            onSetDetailOpen={onSetDetailOpen}
            onFocusRow={onFocusRow}
            onKeyDown={onKeyDown}
            registerRowRef={registerRowRef}
            continuationFailures={continuationFailures}
            loadingContinuationID={loadingContinuationID}
            rootRefreshing={rootRefreshing}
            onContinue={onContinue}
          />
        </div>,
      );
    }
    cursor = end;
  }
  return out;
}

export const ActivityTree = forwardRef<ActivityTreeHandle, ActivityTreeProps>(function ActivityTree(
  {
    tree,
    expandedFoldIDs,
    onToggleFold,
    watches,
    now,
    continuationFailures = {},
    onContinue,
    loadingContinuationID,
    rootRefreshing,
  },
  ref,
) {
  // Detail strips are per-row, not an accordion: each row carries its own
  // default (buildActivityRows' defaultDetailOpen - top-level rows open,
  // nested and fold-revealed rows collapsed), and every chevron/arrow toggle
  // overrides its row's default independently. Overrides keyed by vanished
  // rows stay inert - they are only ever read for rows the current tree
  // actually renders.
  const [detailOverrides, setDetailOverrides] = useState<ReadonlyMap<string, boolean>>(new Map());
  const activityRows = useMemo(() => buildActivityRows(tree, new Set(expandedFoldIDs)), [tree, expandedFoldIDs]);
  const watchRows = useMemo(() => buildWatchRows(watches), [watches]);
  const rows = useMemo(() => [...watchRows, ...activityRows], [watchRows, activityRows]);
  const armedWatches = useMemo(() => armedWatchCount(watches), [watches]);

  // Stable callbacks so the memoized row views below only re-render when
  // their own row's data, disclosure, or focus actually changes.
  const isDetailOpen = useCallback(
    (row: DetailRow): boolean => detailOverrides.get(row.id) ?? row.defaultDetailOpen,
    [detailOverrides],
  );

  const setDetailOpen = useCallback((row: DetailRow, open: boolean): void => {
    setDetailOverrides((current) => {
      const next = new Map(current);
      next.set(row.id, open);
      return next;
    });
  }, []);
  const expandedFolds = useMemo(() => new Set(expandedFoldIDs), [expandedFoldIDs]);
  // The ticking clock lives in TreeTickProvider below, gated on this same
  // flag: no live rows, no interval - the old effect's contract, minus the
  // tree-wide setNow that re-rendered every row each second.
  const hasLive = activityRows.some((row) => "live" in row && row.live);

  const strips = useMemo(
    () => collectContinuations(tree, rows, continuationFailures),
    [tree, rows, continuationFailures],
  );
  const stripsByAfterRowID = useMemo(() => {
    const map = new Map<string, ContinuationStrip[]>();
    for (const strip of strips) {
      const list = map.get(strip.afterRowID);
      if (list) list.push(strip);
      else map.set(strip.afterRowID, [strip]);
    }
    return map;
  }, [strips]);

  const indexByID = useMemo(() => new Map(rows.map((row, index) => [row.id, index])), [rows]);
  const treeRef = useRef<HTMLDivElement>(null);
  const rowRefs = useRef(new Map<string, HTMLDivElement>());
  const pendingRefocusIDRef = useRef<string | null>(null);
  const [focusedID, setFocusedID] = useState<string | null>(null);
  const effectiveFocusedID = focusedID && indexByID.has(focusedID) ? focusedID : rows[0] ? rows[0].id : null;

  const focusRow = useCallback((id: string) => {
    rowRefs.current.get(id)?.focus();
  }, []);

  useImperativeHandle(ref, () => ({ focusRow }), [focusRow]);

  if (
    focusedID !== null &&
    !indexByID.has(focusedID) &&
    effectiveFocusedID !== null &&
    treeRef.current?.contains(document.activeElement)
  ) {
    pendingRefocusIDRef.current = effectiveFocusedID;
  }

  useLayoutEffect(() => {
    if (pendingRefocusIDRef.current === null) return;
    const id = pendingRefocusIDRef.current;
    pendingRefocusIDRef.current = null;
    rowRefs.current.get(id)?.focus();
  });

  // Stable identities so memoized rows only re-render when their own row's
  // props (data, disclosure, focus) actually change - never on a tick.
  const registerRowRef = useCallback((id: string, element: HTMLDivElement | null): void => {
    if (element) rowRefs.current.set(id, element);
    else rowRefs.current.delete(id);
  }, []);

  const focusRowByID = useCallback((id: string): void => {
    setFocusedID(id);
  }, []);

  const activateRow = useCallback(
    (row: ActivityRow): void => {
      if (row.kind === "fold") {
        onToggleFold(row.id);
        return;
      }
      // Row activation is the disclosure, same as the chevron: the transcript
      // opens only from the row's own open button, never from the title.
      setDetailOpen(row, !isDetailOpen(row));
    },
    [isDetailOpen, onToggleFold, setDetailOpen],
  );

  const handleKeyDown = useCallback(
    (event: KeyboardEvent<HTMLDivElement>, row: ActivityRow): void => {
      const index = indexByID.get(row.id);
      if (index === undefined) return;
      // Alt/Ctrl/Meta mean the key belongs elsewhere: Alt+ArrowUp/Down and
      // Alt+Home/End are the global transcript scroll/jump chords, and the
      // dispatcher's editable test does not shield them from these row divs -
      // a blanket preventDefault here would swallow them (roborev PR #884
      // round 3). Shift stays: coarse-step and range conventions aside, the
      // tree's own behavior is unchanged by it.
      if (event.altKey || event.ctrlKey || event.metaKey) return;
      switch (event.key) {
        case "ArrowDown": {
          event.preventDefault();
          const next = rows[index + 1];
          if (next) focusRow(next.id);
          break;
        }
        case "ArrowUp": {
          event.preventDefault();
          const previous = rows[index - 1];
          if (previous) focusRow(previous.id);
          break;
        }
        case "ArrowRight": {
          event.preventDefault();
          if (row.kind === "fold") {
            if (!expandedFolds.has(row.id)) onToggleFold(row.id);
          } else {
            setDetailOpen(row, true);
          }
          break;
        }
        case "ArrowLeft": {
          event.preventDefault();
          if (row.kind === "fold") {
            if (expandedFolds.has(row.id)) onToggleFold(row.id);
          } else if (isDetailOpen(row)) {
            setDetailOpen(row, false);
          } else if (row.parentID && indexByID.has(row.parentID)) {
            // parentID is the delegate ROW's id for child-session rows (Task 5
            // contract), so it resolves through the row index directly; root
            // rows' parentID is a session node id, which is never a row.
            focusRow(row.parentID);
          }
          break;
        }
        case "Enter":
        case " ":
        case "Spacebar":
        case "Space": {
          // Enter/Space from a nested control (the chevron button, the Load
          // more button) is that control's own activation; the row must not
          // fire a second activation for it. Arrows, by contrast, always mean
          // row navigation even when focus sits on a nested control (Firefox
          // and Safari focus buttons on click).
          if (event.target !== event.currentTarget) return;
          event.preventDefault();
          activateRow(row);
          break;
        }
        default:
          break;
      }
    },
    [activateRow, expandedFolds, focusRow, indexByID, isDetailOpen, onToggleFold, rows, setDetailOpen],
  );

  return (
    <TreeTickProvider live={hasLive}>
      {watchRows.length > 0 && <WatchGroupHeader armed={armedWatches} />}
      <div ref={treeRef} role="tree" className={CLASS.tree}>
        <RowBlock
          slice={rows}
          stripsByAfterRowID={stripsByAfterRowID}
          expandedFolds={expandedFolds}
          effectiveFocusedID={effectiveFocusedID}
          now={now}
          isDetailOpen={isDetailOpen}
          onToggleFold={onToggleFold}
          onSetDetailOpen={setDetailOpen}
          onFocusRow={focusRowByID}
          onKeyDown={handleKeyDown}
          registerRowRef={registerRowRef}
          continuationFailures={continuationFailures}
          loadingContinuationID={loadingContinuationID}
          rootRefreshing={rootRefreshing}
          onContinue={onContinue}
        />
      </div>
    </TreeTickProvider>
  );
});
