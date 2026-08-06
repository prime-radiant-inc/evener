import {
  Fragment,
  forwardRef,
  type KeyboardEvent,
  type ReactNode,
  useCallback,
  useEffect,
  useImperativeHandle,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { Button, Chevron, StatusDot } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { openTranscript } from "../transcript/openTranscript";
import { ActivityRowDetail } from "./ActivityRowDetail";
import {
  type ActivityDelegate,
  type ActivitySessionNode,
  type ActivityTree as ActivityTreeData,
  activityNodeID,
} from "./activityData";
import {
  formatQuietAge,
  formatUsagePair,
  isFailedStatus,
  jobStatusDotState,
  quietAnchorMillis,
} from "./activityFormat";
import styles from "./activitypanel.module.css";
import {
  type ActivityDelegateRow,
  type ActivityFoldRow,
  type ActivityJobRow,
  type ActivityRow,
  buildActivityRows,
} from "./activityRows";

export interface ActivityTreeProps {
  tree: ActivityTreeData;
  expandedFoldIDs: string[];
  onToggleFold: (foldID: string) => void;
  continuationFailures?: Record<string, string | undefined>;
  onContinue?: (targetID: string, continuation: string) => void;
  loadingContinuationID?: string;
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
  denseMeta: requireClass(styles.denseMeta, "activitypanel.module.css", "denseMeta"),
  denseQuiet: requireClass(styles.denseQuiet, "activitypanel.module.css", "denseQuiet"),
  denseFailed: requireClass(styles.denseFailed, "activitypanel.module.css", "denseFailed"),
  foldRow: requireClass(styles.foldRow, "activitypanel.module.css", "foldRow"),
  rowToggle: requireClass(styles.rowToggle, "activitypanel.module.css", "rowToggle"),
  rowActions: requireClass(styles.rowActions, "activitypanel.module.css", "rowActions"),
  rowContinuation: requireClass(styles.rowContinuation, "activitypanel.module.css", "rowContinuation"),
  indentGuide: requireClass(styles.indentGuide, "activitypanel.module.css", "indentGuide"),
};

function delegateStatusText(delegate: ActivityDelegate): string {
  const latest = delegate.turns.at(-1);
  if (latest) return latest.status;
  return delegate.child?.aggregate ?? "unknown";
}

function delegateName(delegate: ActivityDelegate): string {
  return delegate.mandate ?? delegate.child?.label ?? delegate.childSessionId;
}

// transcriptTarget mirrors ActivityTranscriptAction exactly: the ref is
// trimmed and a row with no ref gets no transcript action at all (that
// component renders null in the same situation). There is deliberately no
// `job:<id>` fallback - the backend populates transcriptRef, and
// ActivityTranscriptAction has no fallback either.
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
// when the status itself is the failure, so a failed row with no endedAt
// never needs a second "failed" suffix.
function terminalSegment(job: { startedAt: string; endedAt?: string } | undefined, statusText: string): MetaSegment {
  if (job) {
    const start = parseMillis(job.startedAt);
    const end = parseMillis(job.endedAt);
    if (start !== undefined && end !== undefined) {
      return { key: "duration", text: formatQuietAge(end - start) };
    }
  }
  return { key: "status", text: statusText, tone: isFailedStatus(statusText) ? "failed" : undefined };
}

function failedSuffix(segments: MetaSegment[], statusText: string): MetaSegment[] {
  if (!isFailedStatus(statusText)) return segments;
  const last = segments.at(-1);
  if (last?.key !== "duration") return segments;
  return [...segments, { key: "failed", text: "failed", tone: "failed" }];
}

function jobMetaSegments(row: ActivityJobRow, now: number): MetaSegment[] {
  const { job } = row;
  if (row.live) {
    return [
      { key: "tokens", text: "—" },
      { key: "quiet", text: formatQuietAge(now - quietAnchorMillis(job)), tone: "quiet" },
    ];
  }
  return failedSuffix([terminalSegment(job, job.status)], job.status);
}

function delegateMetaSegments(row: ActivityDelegateRow, now: number): MetaSegment[] {
  const { delegate } = row;
  const tokens = formatUsagePair(delegate.usage);
  const lastTurn = delegate.turns.at(-1);
  if (row.live) {
    const segments: MetaSegment[] = [{ key: "tokens", text: tokens ?? "—" }];
    // The delegate's own startedAt is unknown; with no turns there is no
    // anchor to measure quiet from, so the quiet segment is hidden entirely.
    if (lastTurn) {
      segments.push({ key: "quiet", text: formatQuietAge(now - quietAnchorMillis(lastTurn)), tone: "quiet" });
    }
    return segments;
  }
  const statusText = delegateStatusText(delegate);
  const segments: MetaSegment[] = [];
  if (tokens) segments.push({ key: "tokens", text: tokens });
  segments.push(terminalSegment(lastTurn, statusText));
  return failedSuffix(segments, statusText);
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

export const ActivityTree = forwardRef<ActivityTreeHandle, ActivityTreeProps>(function ActivityTree(
  { tree, expandedFoldIDs, onToggleFold, continuationFailures = {}, onContinue, loadingContinuationID },
  ref,
) {
  // Detail strips are per-row, not an accordion: top-level rows start
  // expanded so the command/mandate is visible without a click, nested rows
  // start collapsed, and every chevron/arrow toggle overrides its row's
  // default independently. Overrides keyed by vanished rows stay inert - they
  // are only ever read for rows the current tree actually renders.
  const [detailOverrides, setDetailOverrides] = useState<ReadonlyMap<string, boolean>>(new Map());
  const [now, setNow] = useState(() => Date.now());
  const rows = useMemo(() => buildActivityRows(tree, new Set(expandedFoldIDs)), [tree, expandedFoldIDs]);

  function isDetailOpen(row: ActivityJobRow | ActivityDelegateRow): boolean {
    return detailOverrides.get(row.id) ?? row.level === 1;
  }

  function setDetailOpen(row: ActivityJobRow | ActivityDelegateRow, open: boolean): void {
    setDetailOverrides((current) => {
      const next = new Map(current);
      next.set(row.id, open);
      return next;
    });
  }
  const expandedFolds = useMemo(() => new Set(expandedFoldIDs), [expandedFoldIDs]);
  const hasLive = rows.some((row) => row.kind !== "fold" && row.live);
  useEffect(() => {
    if (!hasLive) return;
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [hasLive]);

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

  function activateRow(row: ActivityRow): void {
    if (row.kind === "fold") {
      onToggleFold(row.id);
      return;
    }
    const target = transcriptTarget(row);
    if (target) openTranscript(target, row.parentRef);
  }

  function handleKeyDown(event: KeyboardEvent<HTMLDivElement>, row: ActivityRow): void {
    const index = indexByID.get(row.id);
    if (index === undefined) return;
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
  }

  function renderSegments(segments: MetaSegment[]): ReactNode {
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

  function renderContinuationStrip(strip: ContinuationStrip): ReactNode {
    if (!onContinue) return null;
    const failure = continuationFailures[strip.targetID];
    return (
      <div className={CLASS.rowActions} key={`${strip.targetID}-continuation`}>
        <span className={CLASS.rowContinuation}>
          {failure ?? strip.branchError ?? "This branch is partially retained."}
        </span>
        {strip.token && (
          <Button
            variant="quiet"
            size="xs"
            tabIndex={-1}
            disabled={loadingContinuationID === strip.targetID}
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
  }

  function renderFoldRow(row: ActivityFoldRow): ReactNode {
    const expanded = expandedFolds.has(row.id);
    const label = `${row.inactiveCount} inactive`;
    const accessibleLabel = row.failedCount > 0 ? `${label} · ${row.failedCount} failed` : label;
    return (
      <div
        key={row.id}
        ref={(element) => {
          if (element) rowRefs.current.set(row.id, element);
          else rowRefs.current.delete(row.id);
        }}
        role="treeitem"
        aria-label={accessibleLabel}
        aria-level={row.level}
        aria-expanded={expanded}
        tabIndex={row.id === effectiveFocusedID ? 0 : -1}
        className={CLASS.foldRow}
        onFocus={() => setFocusedID(row.id)}
        onKeyDown={(event) => handleKeyDown(event, row)}
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
  }

  function renderDenseRow(row: ActivityJobRow | ActivityDelegateRow): ReactNode {
    const name = row.kind === "job" ? row.job.description : delegateName(row.delegate);
    const statusText = row.kind === "job" ? row.job.status : delegateStatusText(row.delegate);
    const target = transcriptTarget(row);
    const detailOpen = isDetailOpen(row);
    const segments = row.kind === "job" ? jobMetaSegments(row, now) : delegateMetaSegments(row, now);
    return (
      <Fragment key={row.id}>
        <div
          ref={(element) => {
            if (element) rowRefs.current.set(row.id, element);
            else rowRefs.current.delete(row.id);
          }}
          role="treeitem"
          aria-label={name}
          aria-level={row.level}
          tabIndex={row.id === effectiveFocusedID ? 0 : -1}
          className={CLASS.denseRow}
          onFocus={() => setFocusedID(row.id)}
          onKeyDown={(event) => handleKeyDown(event, row)}
          onClick={target ? () => openTranscript(target, row.parentRef) : undefined}
        >
          <button
            type="button"
            tabIndex={-1}
            aria-label={`${detailOpen ? "Hide" : "Show"} details for ${name}`}
            aria-expanded={detailOpen}
            className={CLASS.rowToggle}
            onClick={(event) => {
              event.stopPropagation();
              setDetailOpen(row, !detailOpen);
            }}
          >
            <Chevron direction={detailOpen ? "down" : "right"} size={12} />
          </button>
          <StatusDot state={jobStatusDotState(statusText, row.live ? undefined : true)} />
          <span className={CLASS.denseKind} aria-hidden="true">
            {row.kind === "delegate" ? "⌘" : "$"}
          </span>
          <span className={row.live ? `${CLASS.denseName} ${CLASS.denseNameLive}` : CLASS.denseName}>{name}</span>
          {renderSegments(segments)}
        </div>
        {detailOpen && <ActivityRowDetail row={row} now={now} />}
      </Fragment>
    );
  }

  // Rows arrive flat with levels; a delegate's child-session rows are the
  // contiguous deeper-level block right after it and nest one group deeper.
  function renderRowBlock(slice: ActivityRow[]): ReactNode[] {
    const out: ReactNode[] = [];
    let cursor = 0;
    while (cursor < slice.length) {
      const row = slice[cursor];
      if (!row) break;
      out.push(row.kind === "fold" ? renderFoldRow(row) : renderDenseRow(row));
      for (const strip of stripsByAfterRowID.get(row.id) ?? []) {
        out.push(renderContinuationStrip(strip));
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
            {renderRowBlock(slice.slice(cursor + 1, end))}
          </div>,
        );
      }
      cursor = end;
    }
    return out;
  }

  return (
    <div ref={treeRef} role="tree" className={CLASS.tree}>
      {renderRowBlock(rows)}
    </div>
  );
});
