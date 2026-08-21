// The delegate descriptor and its Rail × Quote card. Each delegate tool call
// renders exactly one card in its own ToolCallItem body; shared store state is
// limited to that row's reactive lifecycle data.
//
// Scope decision: only `delegate` (the spawn) and the three follow-up
// calls above ever touch a row. job_list/job_watch do not - they're
// orientation calls over MANY jobs at once, not a check-in on one
// specific child, and correlating an arbitrary listing back to individual
// rows would need far more inference than the wire actually supports.
import { useEffect, useLayoutEffect } from "react";
import { type ItemModel, SYSTEM_PRELUDE_TURN_ID, type TurnModel } from "../../../../protocol/model";
import { threadsStore, useThreadsStore } from "../../../../stores/threads";
import { Chevron, IconButton } from "../../../../widgets";
import { isDisclosureOpen, toggleDisclosure } from "../../../../widgets/disclosure/disclosureStore";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { formatUsagePair } from "../../chrome/activityFormat";
import { cadenceStateForStatus, useSessionNow } from "../../liveness";
import { formatClockTimeSeconds, formatElapsed, plainQuoteLine } from "../messages/format";
import { statedPurposeOf } from "../ToolRow";
import type { ToolRenderProps } from "../toolRenderers";
import { registerToolRenderer } from "../toolRenderers";
import { parseArgs, parseJSONObject, str } from "./helpers";
import {
  classifyJobStatus,
  effectiveRowKind,
  removeSubagentRow,
  resolveRowKey,
  type SubagentRow,
  type SubagentRowKind,
  setWatchedLiveKind,
  turnScopeKey,
  upsertSubagentRow,
  useSubagentRow,
} from "./subagentModuleStore";
import styles from "./subagentmodule.module.css";

// The expanded quote list shows the most recent quotes, not the full feed -
// "open transcript" exists for the full history (the same reasoning the old
// Activity feed's cap carried). Within the window the order is
// chronological, the live quote last.
const RECENT_QUOTES_CAP = 5;

const CLASS = {
  card: requireClass(styles.card, "subagentmodule.module.css", "card"),
  statusGlyph: requireClass(styles.statusGlyph, "subagentmodule.module.css", "statusGlyph"),
  srOnly: requireClass(styles.srOnly, "subagentmodule.module.css", "srOnly"),
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

// Keep stable projections and frozen tool output on the same classification.
export { classifyJobStatus, resolveRowKey } from "./subagentModuleStore";

// notLoaded means the child left the live roster; cadence intentionally folds
// it into idle, so preserve that distinction before mapping cadence states.
export function rowKindFromChildStatus(type: string): SubagentRowKind {
  if (type === "notLoaded") return "unknown";
  const state = cadenceStateForStatus(type);
  if (state === "failed") return "failed";
  if (state === "ended") return "done";
  return "running";
}

const KNOWN_JOB_STATUSES = ["completed", "failed", "cancelled", "stopped", "exhausted", "running"] as const;

// Footer fields are optional, so status cannot be read by position.
export function statusWordFromText(text: string): string | undefined {
  for (const status of KNOWN_JOB_STATUSES) {
    if (new RegExp(`\\b${status}\\b`).test(text)) return status;
  }
  return undefined;
}

// A Quote is one child-authored line. Folded hero quotes are always italic;
// `msg` preserves source-specific italics only in the expanded activity list.
interface Quote {
  id: string;
  text: string;
  msg: boolean;
  startedAt?: string;
  completedAt?: string;
}

const STATUS_GLYPH: Record<SubagentRowKind | "attention", string> = {
  running: "●",
  done: "✓",
  stopped: "■",
  failed: "×",
  unknown: "?",
  attention: "◆",
};

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

// Fallback run window for historical children without a stable projection.
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

// Prefer the child's stable run window, then its transcript, then the spawn
// item. Only running rows consume the shared `now` value.
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

// Stable exhaustion evidence belongs in the expanded region.
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

// One headless card for one delegate. Its full child watch drives quote,
// counts, status, and the expanded recent-activity region.
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

  // Needs-you is an attention overlay on a still-running child.
  const displayKind = effectiveRowKind(row);
  const attention = model !== undefined && cadenceStateForStatus(model.status.type) === "needs-you";
  const childRunning = model ? rowKindFromChildStatus(model.status.type) === "running" : displayKind === "running";

  const items = model ? model.turns.flatMap((t) => t.items) : [];
  const quotes = deriveQuotes(items);
  const reason = row.liveReason ?? row.resultPreview;
  // Failures lead with their reason; other cards use the child's latest words.
  const latestQuote = quotes.at(-1)?.text;
  const quoteText = displayKind === "failed" && reason ? `✕ ${reason}` : (latestQuote ?? (reason || undefined));

  // Missing snapshots omit counts rather than fabricating zeroes.
  const realTurns = model ? model.turns.filter((t) => t.id !== SYSTEM_PRELUDE_TURN_ID) : undefined;
  const turnCount = realTurns?.length;
  const callCount = realTurns
    ? realTurns.flatMap((t) => t.items).filter((it) => it.type === "commandExecution").length
    : undefined;
  const stableUsage = row.stable?.usage;
  // A half-present usage pair would be a guess, so omit it.
  const usage =
    stableUsage?.inputTokens !== undefined && stableUsage.outputTokens !== undefined
      ? formatUsagePair({ inputTokens: stableUsage.inputTokens, outputTokens: stableUsage.outputTokens })
      : null;
  const statsSegments: string[] = [];
  if (turnCount !== undefined) statsSegments.push(`${turnCount} ${turnCount === 1 ? "turn" : "turns"}`);
  if (callCount !== undefined) statsSegments.push(`${callCount} ${callCount === 1 ? "call" : "calls"}`);
  if (usage) statsSegments.push(usage);

  const nowMs = useSessionNow();
  const clock = cardClock(row, displayKind, nowMs, realTurns ? childRunWindow(realTurns) : undefined);

  // Deterministic scoping preserves disclosure state across virtualization.
  const disclosureId = `subagent-quotes-${encodeURIComponent(scopeKey)}-${encodeURIComponent(row.rowKey)}`;
  const open = isDisclosureOpen(disclosureId, false);
  const effectiveStatus = attention ? "needs attention" : displayKind;

  // Keep true feed ordinals when slicing the recent chronological window.
  const windowStart = Math.max(0, quotes.length - RECENT_QUOTES_CAP);
  const recentQuotes = quotes.slice(windowStart).map((q, i) => ({ ...q, ordinal: windowStart + i + 1 }));

  return (
    <div
      className={CLASS.card}
      data-testid="subagent-row"
      data-kind={displayKind}
      data-attention={attention ? "true" : undefined}
    >
      <span className={CLASS.srOnly}>{`Delegate ${row.delegateId ?? row.rowKey.replace(/^[^:]+:/, "")}`}</span>
      <span className={CLASS.srOnly}>{`Status: ${effectiveStatus}`}</span>
      {quoteText && (
        <em className={CLASS.quote} data-testid="subagent-quote">
          {quoteText}
        </em>
      )}
      <div className={CLASS.stats} data-testid="subagent-stats">
        <span className={CLASS.statusGlyph} data-testid="subagent-status-glyph" aria-hidden="true">
          {STATUS_GLYPH[attention ? "attention" : displayKind]}
        </span>
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
          aria-expanded={open}
          aria-controls={disclosureId}
          onClick={(event) => {
            event.stopPropagation();
            toggleDisclosure(disclosureId, false);
          }}
        />
      </div>
      {open && (
        <section
          id={disclosureId}
          aria-label={`Recent activity for ${row.delegateId ?? "delegate"}`}
          className={CLASS.quotes}
          data-testid="subagent-quotes"
        >
          {recentQuotes.length > 0 ? (
            <ol className={CLASS.quotesList}>
              {recentQuotes.map((q) => {
                // The chronologically last quote is live while the child runs.
                const live = childRunning && q.ordinal === quotes.length;
                // In-flight stamped work gets an ellipsis; missing stamps omit time.
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
                    {q.msg ? <em className={CLASS.quoteMsg}>{q.text}</em> : <span>{q.text}</span>}
                    {meta && <span className={CLASS.quoteMeta}>{meta}</span>}
                  </li>
                );
              })}
            </ol>
          ) : (
            <div className={CLASS.quotesEmpty}>No activity yet</div>
          )}
          <JobDetailSection row={row} />
        </section>
      )}
    </div>
  );
}

export function rowFromDelegateItem(item: ItemModel): {
  rowKey: string;
  migrateFromRowKey?: string;
  row: Omit<SubagentRow, "rowKey">;
} | null {
  const args = parseArgs(item.argumentsJSON);
  // Keep the full task in row state even though ToolCallItem's visible purpose
  // preview is clipped.
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
  const projected = rowFromDelegateItem(item);
  const storedRow = useSubagentRow(scopeKey, projected?.rowKey ?? "");

  useLayoutEffect(() => {
    const next = rowFromDelegateItem(item);
    if (!next) {
      removeSubagentRow(scopeKey, resolveRowKey(undefined, undefined, item.callId ?? item.id));
      return;
    }
    const { rowKey, migrateFromRowKey, row } = next;
    upsertSubagentRow(scopeKey, { rowKey, ...row }, migrateFromRowKey);
  }, [scopeKey, item]);

  if (!projected) return null;
  return (
    <SubagentCard
      row={storedRow ?? { rowKey: projected.rowKey, ...projected.row }}
      turnId={item.turnId}
      sessionRef={sessionRef}
    />
  );
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
