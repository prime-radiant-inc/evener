// The `delegate` descriptor (parity checklist §2's delegateRenderer) and
// the subagent card it feeds - "one aggregated block per turn collecting
// that turn's job_* activity" per the wave-4 plan, redesigned 2026-08-20
// as the Rail × Quote card (see the stylesheet's own header).
//
// Design rationale (see subagentModuleStore.ts's own header for the full
// interface-boundary reasoning): ToolRenderProps is {item, live} only, so
// a single tool call's body cannot see its sibling items to build a
// shared block by itself. Every delegate item computes its OWN row
// (spawned/running/done/failed + duration + result preview, all derived
// from that ONE item's own output/args) and upserts it into
// subagentModuleStore keyed by (turnId, rowKey); job_status/job_stop/
// delegate_send do the same via updateSubagentRowIfExists (jobTools.tsx),
// UPDATING an existing row rather than spawning one, mirroring the legacy
// reconcileSubagent's identical rule. Whichever delegate item is first to
// mount within a turn (VirtualList windows a whole TurnBlock as one row -
// see TurnBlock.tsx's own file - so a turn's items always mount/unmount
// together, making "first to render, in wire/array order" a real,
// deterministic signal, not a race) claims leadership inside a
// useLayoutEffect (symmetric with its own release - see DelegateBody's own
// comment for why claim and release must live in the same effect) and is
// the only one that renders the module chrome; every other delegate item in
// the same turn renders nothing further (its own one-line summary still
// shows independently - that's ToolCallItem's mandatory summary span, owned
// by T1, not this body).
//
// Scope decision: only `delegate` (the spawn) and the three follow-up
// calls above ever touch a row. job_list/job_watch do not - they're
// orientation calls over MANY jobs at once, not a check-in on one
// specific child, and correlating an arbitrary listing back to individual
// rows would need far more inference than the wire actually supports.
import { useEffect, useLayoutEffect, useState } from "react";
import { type ItemModel, SYSTEM_PRELUDE_TURN_ID, type TurnModel } from "../../../../protocol/model";
import { threadsStore, useThreadsStore } from "../../../../stores/threads";
import { Button, Chevron, IconButton } from "../../../../widgets";
import { isDisclosureOpen, toggleDisclosure } from "../../../../widgets/disclosure/disclosureStore";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { formatUsagePair } from "../../chrome/activityFormat";
import { cadenceStateForStatus, NOW_TICK_MS, useNowTick } from "../../liveness";
import { formatClockTimeSeconds, formatElapsed, plainQuoteLine } from "../messages/format";
import { statedPurposeOf } from "../ToolRow";
import type { ToolRenderProps } from "../toolRenderers";
import { registerToolRenderer } from "../toolRenderers";
import { parseArgs, parseJSONObject, str } from "./helpers";
import {
  claimLeader,
  classifyJobStatus,
  effectiveRowKind,
  releaseLeader,
  removeSubagentRow,
  resolveRowKey,
  type SubagentRow,
  type SubagentRowKind,
  setWatchedLiveKind,
  turnScopeKey,
  upsertSubagentRow,
  useLeader,
  useSubagentRows,
} from "./subagentModuleStore";
import styles from "./subagentmodule.module.css";

const DONE_VISIBLE_CAP = 6;
// The expanded quote list shows the most recent quotes, not the full feed -
// "open transcript" exists for the full history (the same reasoning the old
// Activity feed's cap carried). Within the window the order is
// chronological, the live quote last.
const RECENT_QUOTES_CAP = 5;

const CLASS = {
  module: requireClass(styles.module, "subagentmodule.module.css", "module"),
  card: requireClass(styles.card, "subagentmodule.module.css", "card"),
  quote: requireClass(styles.quote, "subagentmodule.module.css", "quote"),
  stats: requireClass(styles.stats, "subagentmodule.module.css", "stats"),
  statsSep: requireClass(styles.statsSep, "subagentmodule.module.css", "statsSep"),
  statsSpring: requireClass(styles.statsSpring, "subagentmodule.module.css", "statsSpring"),
  clock: requireClass(styles.clock, "subagentmodule.module.css", "clock"),
  quotes: requireClass(styles.quotes, "subagentmodule.module.css", "quotes"),
  quotesList: requireClass(styles.quotesList, "subagentmodule.module.css", "quotesList"),
  quoteItem: requireClass(styles.quoteItem, "subagentmodule.module.css", "quoteItem"),
  quoteLive: requireClass(styles.quoteLive, "subagentmodule.module.css", "quoteLive"),
  quoteMsg: requireClass(styles.quoteMsg, "subagentmodule.module.css", "quoteMsg"),
  quoteMeta: requireClass(styles.quoteMeta, "subagentmodule.module.css", "quoteMeta"),
  quotesEmpty: requireClass(styles.quotesEmpty, "subagentmodule.module.css", "quotesEmpty"),
  section: requireClass(styles.section, "subagentmodule.module.css", "section"),
  sectionLabel: requireClass(styles.sectionLabel, "subagentmodule.module.css", "sectionLabel"),
  mandate: requireClass(styles.mandate, "subagentmodule.module.css", "mandate"),
};

// classifyJobStatus and resolveRowKey live in subagentModuleStore.ts so stable
// projection and a delegate item's frozen output use identical
// classification/keying. That plain data store is also the layer
// stores/threads.ts can import without transitively pulling in
// Button/CSS modules a core store must never transitively
// bundle. Re-exported here unchanged so every existing import site (this
// file's own uses below, subagentModule.test.tsx) keeps working.
export { classifyJobStatus, resolveRowKey } from "./subagentModuleStore";

// rowKindFromChildStatus maps the child's LIVE thread status onto a row kind
// for the card's data-kind (yd16). model.status.type is the WIRE thread-status
// vocabulary (active/closed/systemError/awaiting/warning/idle/notLoaded), NOT
// the job-status words classifyJobStatus reads - feeding thread-status to
// classifyJobStatus misclassifies ("closed"->"running", "systemError"->
// "running"). So this reuses cadenceStateForStatus (the one canonical wire-
// status interpreter) and adapts its CadenceState to a SubagentRowKind: a
// failed child is "failed", an ended (closed) child is "done", and everything
// still live from the parent's view (working / needs-you / idle) stays
// "running".
//
// g5kf (the honest-clock bug): "notLoaded" must be carved out BEFORE that
// collapse, not after. cadenceStateForStatus deliberately folds notLoaded
// into the same "idle" family a genuinely-idle-but-still-live child gets -
// right for the Cadence dot, which only needs "nothing to animate" either
// way (liveness.ts's own doc comment, cross-checked by its test) - but wrong
// here: notLoaded means the child left the daemon's live roster entirely
// (evicted, orphaned, or lost to a hub restart - cmd/evener-hub/
// app_threadread.go's pastEntryThread stamps it), not "still going". Once
// cadenceStateForStatus has already collapsed it to "idle" this function has
// no way to tell the two apart, and a delegate call frozen at
// status:"running" by a foreground_timeout (agent/job_delegate.go's mainline
// path for any non-trivial delegate, not an edge case) would then read as
// "running" forever with no path back to honesty - this was the only
// remaining liveness check once the child stops reporting. Composer.tsx and
// StatusRow.tsx each already keep their own separate notLoaded check
// alongside cadenceStateForStatus for the identical reason; this follows the
// same, already-established pattern rather than teaching
// cadenceStateForStatus itself a state its other two callers don't want.
export function rowKindFromChildStatus(type: string): SubagentRowKind {
  if (type === "notLoaded") return "unknown";
  const state = cadenceStateForStatus(type);
  if (state === "failed") return "failed";
  if (state === "ended") return "done";
  return "running";
}

// KNOWN_JOB_STATUSES mirrors the status enum job_list's own tool schema
// declares (agent/internal/tool/definitions.go's DefJobList: "running",
// "completed", "failed", "exhausted", "cancelled", "stopped"). Used by
// statusWordFromText below to read a status back out of a plain-text
// footer (job_stop/delegate_send) whose field ORDER isn't fixed - see
// that function's own comment.
const KNOWN_JOB_STATUSES = ["completed", "failed", "cancelled", "stopped", "exhausted", "running"] as const;

// statusWordFromText finds a known status word anywhere in `text` (a
// job_stop/delegate_send footer), rather than splitting by position: both
// formatJobStop's and formatDelegateSend's footer fields are each
// conditionally present (agent/session_tools_jobs.go - verified directly,
// e.g. delegate_send's footer omits `status` entirely when the field was
// empty), so a fixed field index would silently read the wrong segment
// whenever an earlier field is missing. A word-boundary search is robust
// to that reordering/omission.
export function statusWordFromText(text: string): string | undefined {
  for (const status of KNOWN_JOB_STATUSES) {
    if (new RegExp(`\\b${status}\\b`).test(text)) return status;
  }
  return undefined;
}

// A Quote is one child-authored line: a tool call's purpose field (plain) or
// an agent message (msg - rendered italic). Everything the card shows as the
// child's "own words" comes from this one derivation, folded card and
// expanded list alike.
interface Quote {
  id: string;
  text: string;
  msg: boolean;
  startedAt?: string;
  completedAt?: string;
}

// deriveQuotes flattens the child's turns into its authored lines. Two
// exclusions, both deliberate:
// - round_timings items: a timing annotation is not an action, and a chatty
//   child's feed otherwise drowns real steps in them (every round produces
//   one). Excluded by eventKind - the stable typed discriminator, not by
//   matching the "Round timings" description text.
// - purpose-less tool calls: a whitespace-only description is ABSENCE, not a
//   step - the same statedPurposeOf rule the main transcript's tool row
//   applies, so a line is a quote on one surface iff it is on the other.
function deriveQuotes(items: ItemModel[]): Quote[] {
  const out: Quote[] = [];
  for (const it of items) {
    if (it.eventKind === "round_timings") continue;
    if (it.type === "agentMessage") {
      // Messages quote as plain text: a final report's markdown structure
      // ("## Summary", "**Fixed**") is noise on a one-line glance.
      const text = plainQuoteLine(it.text);
      if (text !== "") out.push({ id: it.id, text, msg: true, startedAt: it.startedAt, completedAt: it.completedAt });
      continue;
    }
    const purpose = statedPurposeOf(it);
    if (purpose !== undefined) {
      out.push({ id: it.id, text: purpose, msg: false, startedAt: it.startedAt, completedAt: it.completedAt });
    }
  }
  return out;
}

// childRunWindow is the child transcript's own first-turn-start →
// last-turn-end span. The card already holds the full-turns watch, so this
// costs nothing; historical sessions (read back, no delegate notifications)
// never get a stable projection, and this is the only honest run window left.
function childRunWindow(turns: TurnModel[]): { startMs?: number; endMs?: number } | undefined {
  let startMs: number | undefined;
  let endMs: number | undefined;
  for (const t of turns) {
    const s = Date.parse(t.startedAt ?? "");
    if (!Number.isNaN(s)) startMs = startMs === undefined ? s : Math.min(startMs, s);
    const e = Date.parse(t.completedAt ?? "");
    if (!Number.isNaN(e)) endMs = endMs === undefined ? e : Math.max(endMs, e);
  }
  if (startMs === undefined && endMs === undefined) return undefined;
  return { startMs, endMs };
}

// cardClock is the stats line's trailing clock. Window source precedence:
//  1. the stable projection's runStartedAt/runEndedAt - the child's actual
//     run. A foreground_timeout delegate's own ITEM timestamps bracket the
//     spawn round-trip (seconds), not the child's work (minutes); when the
//     projection owns the start, only its end counts (a settled spawn item
//     must not freeze the clock at the spawn's seconds).
//  2. the child transcript's turn span (its end counts only once the row is
//     no longer running - a mid-run "end" would freeze a live clock).
//  3. the delegate item's own stamps, the only source left when the child is
//     unwatched (no transcriptRef).
// A row with no start shows no clock; a non-running row with no end shows
// none. The clock never guesses.
function cardClock(
  row: SubagentRow,
  displayKind: SubagentRowKind,
  nowMs: number,
  childWindow?: { startMs?: number; endMs?: number },
): string | undefined {
  let startMs: number | undefined;
  let endMs: number | undefined;
  const stableStart = Date.parse(row.stable?.runStartedAt ?? "");
  if (!Number.isNaN(stableStart)) {
    startMs = stableStart;
    const stableEnd = Date.parse(row.stable?.runEndedAt ?? "");
    if (!Number.isNaN(stableEnd)) endMs = stableEnd;
  } else if (childWindow?.startMs !== undefined) {
    startMs = childWindow.startMs;
    if (displayKind !== "running" && childWindow.endMs !== undefined) endMs = childWindow.endMs;
  } else {
    const itemStart = Date.parse(row.startedAt ?? "");
    if (Number.isNaN(itemStart)) return undefined;
    startMs = itemStart;
    const itemEnd = Date.parse(row.completedAt ?? "");
    if (!Number.isNaN(itemEnd)) endMs = itemEnd;
  }
  if (endMs !== undefined && endMs >= startMs) return formatElapsed(endMs - startMs);
  if (displayKind !== "running") return undefined;
  return formatElapsed(nowMs - startMs);
}

// JobDetailSection surfaces stable exhaustion/resumable evidence, inside the
// expanded region. Reason is already visible in the folded quote via
// row.resultPreview/liveReason, so it isn't repeated here. Renders nothing
// once neither field is present.
function JobDetailSection({ row }: { row: SubagentRow }) {
  if (row.resumable === undefined && row.exhaustionBudget === undefined && row.exhaustionLimit === undefined) {
    return null;
  }
  const exhaustion =
    row.exhaustionBudget !== undefined || row.exhaustionLimit !== undefined
      ? `${row.exhaustionBudget ?? "?"} of ${row.exhaustionLimit ?? "?"}`
      : undefined;
  return (
    <section className={CLASS.section} data-testid="subagent-job-detail">
      <div className={CLASS.sectionLabel}>Job</div>
      <div className={CLASS.mandate}>
        {exhaustion && <div>Exhaustion budget: {exhaustion}</div>}
        {row.resumable !== undefined && <div>{row.resumable ? "Resumable" : "Not resumable"}</div>}
      </div>
    </section>
  );
}

// SubagentCard is one subagent, one card: head (tag + open), the child's
// newest own words as the quote, and the stats line. Expansion (the stats
// line's chevron) lists the recent quotes with their runtimes and
// timestamps, plus the Job detail when populated.
//
// The card subscribes to the child's FULL event stream on mount
// (watchThread with includeTurns:true), so the folded card's quote, counts,
// and attention state are live rather than frozen at the delegate call's own
// output. This is the same subscription the old design opened from its
// expanded body - the module auto-expands (autoExpand below), so the
// subscription was de-facto always-on already; the redesign just admits it.
function SubagentCard({
  row,
  turnId,
  sessionRef,
}: {
  row: SubagentRow;
  turnId: string;
  sessionRef: string | undefined;
}) {
  const scopeKey = turnScopeKey(sessionRef, turnId);
  // Captured once so the effect closures below reference this narrowed local,
  // not row.transcriptRef re-read through a closure TS can't narrow.
  const transcriptRef = row.transcriptRef;

  useEffect(() => {
    if (transcriptRef === undefined) return;
    threadsStore
      .getState()
      .watchThread(transcriptRef, { includeTurns: true })
      .catch(() => {});
    return () => threadsStore.getState().releaseWatchedThread(transcriptRef);
  }, [transcriptRef]);

  const model = useThreadsStore((s) => (transcriptRef !== undefined ? s.watchedThreads.get(transcriptRef) : undefined));
  const liveKind = model ? rowKindFromChildStatus(model.status.type) : undefined;
  useEffect(() => {
    if (liveKind && transcriptRef) {
      setWatchedLiveKind(scopeKey, row.rowKey, liveKind);
    }
  }, [scopeKey, row.rowKey, liveKind, transcriptRef]);

  // The card's own kind prefers the live-status overlay / stable projection
  // over the frozen tool-output kind (effectiveRowKind is the shared rule,
  // so rendering and sorting never disagree). Needs-you is a separate,
  // thinner overlay: the child is still kind=running, but its cadence state
  // is needs-you, which the rail shows in attention.
  const displayKind = effectiveRowKind(row);
  const attention = model !== undefined && cadenceStateForStatus(model.status.type) === "needs-you";
  const childRunning = model ? rowKindFromChildStatus(model.status.type) === "running" : displayKind === "running";

  const items = model ? model.turns.flatMap((t) => t.items) : [];
  const quotes = deriveQuotes(items);
  const reason = row.liveReason ?? row.resultPreview;
  // The folded quote: a failure quotes its reason verbatim (✕-marked - the
  // exception earns the explanation without a click); anything else quotes
  // the child's newest own line, falling back to the reason when the watch
  // hasn't produced one yet.
  const latestQuote = quotes.at(-1)?.text;
  const quoteText = displayKind === "failed" && reason ? `✕ ${reason}` : (latestQuote ?? (reason || undefined));

  // Counts come from the same full-turns model; until the first snapshot
  // lands there is no stats segment rather than a fabricated 0. The system
  // prelude is not a turn the child took.
  const realTurns = model ? model.turns.filter((t) => t.id !== SYSTEM_PRELUDE_TURN_ID) : undefined;
  const turnCount = realTurns?.length;
  const callCount = realTurns
    ? realTurns.flatMap((t) => t.items).filter((it) => it.type === "commandExecution").length
    : undefined;
  // Tokens ride the stable delegate projection (applyEvenerDelegateUpdated);
  // no usage data means no segment, never a misleading ↑0 ↓0 (the thread
  // model's own usage-null precedent, via formatUsagePair).
  const stableUsage = row.stable?.usage;
  // EvenerUsage's fields are individually optional; the pair only renders
  // when both directions are present (a half-pair would be a guess).
  const usage =
    stableUsage?.inputTokens !== undefined && stableUsage.outputTokens !== undefined
      ? formatUsagePair({ inputTokens: stableUsage.inputTokens, outputTokens: stableUsage.outputTokens })
      : null;
  const statsSegments: string[] = [];
  if (turnCount !== undefined) statsSegments.push(`${turnCount} ${turnCount === 1 ? "turn" : "turns"}`);
  if (callCount !== undefined) statsSegments.push(`${callCount} ${callCount === 1 ? "call" : "calls"}`);
  if (usage) statsSegments.push(usage);

  const nowMs = useNowTick(NOW_TICK_MS);
  const clock = cardClock(row, displayKind, nowMs, realTurns ? childRunWindow(realTurns) : undefined);

  // Per-card expansion, session-and-turn-scoped AND rowKey-scoped so it is
  // stable across the VirtualList/dockview remount (yt2q) and collision-free
  // across turns AND sessions (78nj) - the same scoping every other store key
  // in this file already uses (kata 8525).
  const disclosureId = `subagent-quotes-${scopeKey}-${row.rowKey}`;
  const open = isDisclosureOpen(disclosureId, false);

  // The RECENT_QUOTES_CAP most recent quotes in chronological order (most
  // recent LAST), each keeping its TRUE 1-based ordinal into the full feed -
  // list-style:decimal reads e.g. "16." through "20." rather than relabeling
  // the 16th quote "1." merely because it renders first, which would
  // understate how much the child has actually done.
  const windowStart = Math.max(0, quotes.length - RECENT_QUOTES_CAP);
  const recentQuotes = quotes.slice(windowStart).map((q, i) => ({ ...q, ordinal: windowStart + i + 1 }));

  return (
    <div
      className={CLASS.card}
      data-testid="subagent-row"
      data-kind={displayKind}
      data-attention={attention ? "true" : undefined}
    >
      {quoteText && (
        <div className={CLASS.quote} data-testid="subagent-quote">
          {quoteText}
        </div>
      )}
      <div className={CLASS.stats} data-testid="subagent-stats">
        {/* Segments join with a separator BETWEEN them - never a dangling
            "·" advertising a segment that has no data. */}
        {statsSegments.flatMap((segment, i) =>
          i === 0
            ? [<span key={segment}>{segment}</span>]
            : [
                <span key={`sep-${segment}`} className={CLASS.statsSep}>
                  ·
                </span>,
                <span key={segment}>{segment}</span>,
              ],
        )}
        <span className={CLASS.statsSpring} />
        {clock && <span className={CLASS.clock}>{clock}</span>}
        <IconButton
          label={open ? "Hide recent activity" : "Show recent activity"}
          title={open ? "Hide recent activity" : "Show recent activity"}
          icon={<Chevron direction={open ? "down" : "right"} />}
          variant="quiet"
          size="xs"
          onClick={(event) => {
            event.stopPropagation();
            toggleDisclosure(disclosureId, false);
          }}
        />
      </div>
      {open && (
        <div className={CLASS.quotes} data-testid="subagent-quotes">
          {recentQuotes.length > 0 ? (
            <ol className={CLASS.quotesList}>
              {recentQuotes.map((q) => {
                // "live" is the chronologically LAST quote while the child is
                // still running - the card's pulse lands at the natural
                // reading end.
                const live = childRunning && q.ordinal === quotes.length;
                // Runtime only when both stamps exist; the live in-flight
                // quote shows "…" instead. A quote with no start stamp shows
                // no time at all rather than a guess.
                const runtime =
                  q.startedAt !== undefined && q.completedAt !== undefined
                    ? formatElapsed(Date.parse(q.completedAt) - Date.parse(q.startedAt))
                    : live && q.startedAt !== undefined
                      ? "…"
                      : undefined;
                const stamp = formatClockTimeSeconds(q.startedAt);
                const meta = [runtime, stamp].filter((s) => s !== undefined).join(" · ");
                return (
                  <li
                    key={q.id}
                    value={q.ordinal}
                    className={live ? `${CLASS.quoteItem} ${CLASS.quoteLive}` : CLASS.quoteItem}
                  >
                    <span className={q.msg ? CLASS.quoteMsg : undefined}>{q.text}</span>
                    {meta && <span className={CLASS.quoteMeta}>{meta}</span>}
                  </li>
                );
              })}
            </ol>
          ) : (
            <div className={CLASS.quotesEmpty}>No activity yet</div>
          )}
          <JobDetailSection row={row} />
        </div>
      )}
    </div>
  );
}

// SubagentModule is the leader's own rendered chrome: the stack of cards,
// folding done-kind cards beyond DONE_VISIBLE_CAP behind a "+N more" toggle -
// running/failed/unknown cards are ALWAYS visible regardless of fold state
// (a live or broken child must never be hidden by count, parity §12). The
// module itself is chromeless: no tally header, no box - the cards carry
// their own state.
function SubagentModule({ turnId, sessionRef }: { turnId: string; sessionRef: string | undefined }) {
  const rows = useSubagentRows(turnScopeKey(sessionRef, turnId));
  const [expanded, setExpanded] = useState(false);

  const doneRows = rows.filter((r) => effectiveRowKind(r) === "done");
  const foldedCount = expanded ? 0 : Math.max(0, doneRows.length - DONE_VISIBLE_CAP);
  const hiddenKeys = new Set(expanded ? [] : doneRows.slice(DONE_VISIBLE_CAP).map((r) => r.rowKey));
  const visibleRows = rows.filter((r) => !hiddenKeys.has(r.rowKey));

  if (rows.length === 0) return null;

  return (
    <div className={CLASS.module} data-testid="subagent-module">
      {visibleRows.map((row) => (
        <SubagentCard key={row.rowKey} row={row} turnId={turnId} sessionRef={sessionRef} />
      ))}
      {foldedCount > 0 && (
        <Button variant="quiet" size="sm" onClick={() => setExpanded(true)}>
          +{foldedCount} more
        </Button>
      )}
      {expanded && doneRows.length > DONE_VISIBLE_CAP && (
        <Button variant="quiet" size="sm" onClick={() => setExpanded(false)}>
          Collapse
        </Button>
      )}
    </div>
  );
}

export function rowFromDelegateItem(item: ItemModel): {
  rowKey: string;
  migrateFromRowKey?: string;
  row: Omit<SubagentRow, "spawnIndex" | "rowKey">;
} | null {
  const args = parseArgs(item.argumentsJSON);
  // Full, unclipped task text - it is the entire specification of what the
  // delegate was asked to do (7f7c). The card's tag clips visually
  // (text-overflow: ellipsis); clipping here would silently drop the rest
  // from the DOM entirely.
  const task = str(args, "task") ?? "";
  const parsed = parseJSONObject(item.output);
  const status = parsed ? str(parsed, "status") : undefined;
  const delegateId = parsed ? str(parsed, "delegate_id") : undefined;
  // A settled activation-only result is historical data, not a stable
  // delegate control identity. Keep an in-flight call-keyed placeholder, but
  // never turn job_id into a delegate row.
  if (parsed && !delegateId) return null;
  const transcriptRef = parsed ? str(parsed, "transcript_ref") : undefined;
  const reason = parsed ? str(parsed, "reason") : undefined;
  const fallbackRowKey = resolveRowKey(undefined, undefined, item.callId ?? item.id);
  const rowKey = resolveRowKey(delegateId, undefined, item.callId ?? item.id);
  return {
    rowKey,
    migrateFromRowKey: rowKey === fallbackRowKey ? undefined : fallbackRowKey,
    row: {
      kind: classifyJobStatus(status),
      task,
      delegateId,
      transcriptRef,
      startedAt: item.startedAt,
      completedAt: item.completedAt,
      resultPreview: reason ?? "",
    },
  };
}

function DelegateBody({ item, sessionRef }: ToolRenderProps) {
  const scopeKey = turnScopeKey(sessionRef, item.turnId);
  const leaderId = useLeader(scopeKey);
  const isLeader = leaderId === item.id;

  useLayoutEffect(() => {
    const projected = rowFromDelegateItem(item);
    if (!projected) {
      removeSubagentRow(scopeKey, resolveRowKey(undefined, undefined, item.callId ?? item.id));
      return;
    }
    const { rowKey, migrateFromRowKey, row } = projected;
    upsertSubagentRow(scopeKey, { rowKey, ...row }, migrateFromRowKey);
  }, [scopeKey, item]);

  // The reactive leader read lets a mounted follower retry after the current
  // leader unmounts. The effect only claims an empty slot and releases it
  // from the elected component's cleanup, keeping StrictMode's mount/cleanup
  // cycle symmetric without making followers poll or render nested details.
  useLayoutEffect(() => {
    if (leaderId === undefined) claimLeader(scopeKey, item.id);
  }, [scopeKey, item.id, leaderId]);
  useLayoutEffect(() => () => releaseLeader(scopeKey, item.id), [scopeKey, item.id]);

  return isLeader ? <SubagentModule turnId={item.turnId} sessionRef={sessionRef} /> : null;
}

registerToolRenderer({
  match: "delegate",
  icon: "delegate",
  summary(item: ItemModel) {
    return item.description ?? "";
  },
  // open ⤢ rides the delegate row's trailing slot (visible folded or not) -
  // ToolCallItem owns the control; the descriptor declares WHAT it targets.
  openTranscriptRef(item: ItemModel) {
    const parsed = parseJSONObject(item.output);
    // Same stable-identity gate as rowFromDelegateItem: an activation-only
    // job_id result has no child transcript worth opening from here.
    if (!parsed || !str(parsed, "delegate_id")) return undefined;
    return str(parsed, "transcript_ref");
  },
  body: DelegateBody,
  // A delegate call is a status card, not a fold-to-open tool row - the same
  // reasoning as task_list's own `autoExpand: () => true`. Left collapsed by
  // default, the card stack and its per-child event-stream watches never
  // mount until opened: the ToolCallItem-owned lean watch still keeps the
  // top-level dot current while collapsed (evch). Opening it at settle makes
  // the cards' state/quote/stats visible without a click; a manual collapse
  // afterward still sticks (ToolCallItem's own autoDefault vs. store-backed
  // toggle).
  autoExpand: () => true,
});
