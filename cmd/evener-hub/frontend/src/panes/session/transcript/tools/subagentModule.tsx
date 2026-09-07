// The delegate descriptor and its Rail × Quote card. Each delegate tool call
// renders exactly one card in its own ToolCallItem body. Only the `delegate`
// spawn materializes a frozen pre-hydration row in the shared store; once the
// owning stable delegate projection exists, it supplies the hydrated state.
import { useEffect } from "react";
import { type ItemModel, SYSTEM_PRELUDE_TURN_ID } from "../../../../protocol/model";
import type { EvenerDelegateInfo } from "../../../../protocol/types.gen";
import { threadsStore, useThreadsStore } from "../../../../stores/threads";
import { Chevron, IconButton, Timestamp } from "../../../../widgets";
import { isDisclosureOpen, toggleDisclosure } from "../../../../widgets/disclosure/disclosureStore";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { formatUsagePair } from "../../chrome/activityFormat";
import { cadenceStateForStatus, useSessionNow } from "../../liveness";
import { formatElapsed, plainQuoteLine } from "../messages/format";
import { statedIntentOf } from "../ToolRow";
import type { ToolRenderProps } from "../toolRenderers";
import { registerToolRenderer } from "../toolRenderers";
import { parseJSONObject, str } from "./helpers";
import {
  classifyJobStatus,
  effectiveRowKind,
  resolveRowKey,
  type SubagentRow,
  type SubagentRowKind,
  turnScopeKey,
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
  quoteText: requireClass(styles.quoteText, "subagentmodule.module.css", "quoteText"),
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
// - intent-less tool calls: a whitespace-only description is ABSENCE, not a
//   step - the same statedIntentOf rule the main transcript's tool row
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
    const intent = statedIntentOf(it);
    if (intent !== undefined) {
      out.push({ id: it.id, text: intent, msg: false, startedAt: it.startedAt, completedAt: it.completedAt });
    }
  }
  return out;
}

// The stable projection owns timing after hydration. Frozen tool output is the
// pre-hydration fallback. Only running rows consume the shared `now` value.
function cardClock(
  row: SubagentRow,
  stable: EvenerDelegateInfo | undefined,
  displayKind: SubagentRowKind,
  nowMs: number,
): string | undefined {
  let startMs: number | undefined;
  let endMs: number | undefined;
  if (stable !== undefined) {
    const stableStart = Date.parse(stable.runStartedAt ?? "");
    if (Number.isNaN(stableStart)) return undefined;
    startMs = stableStart;
    const stableEnd = Date.parse(stable.runEndedAt ?? "");
    if (!Number.isNaN(stableEnd)) endMs = stableEnd;
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
function JobDetailSection({ row, stable }: { row: SubagentRow; stable: EvenerDelegateInfo | undefined }) {
  const resumable = stable ? (stable.exhaustionResumable ?? stable.resumable) : row.resumable;
  const exhaustionBudget = stable ? stable.exhaustionBudget : row.exhaustionBudget;
  const exhaustionLimit = stable ? stable.exhaustionLimit : row.exhaustionLimit;
  if (resumable === undefined && exhaustionBudget === undefined && exhaustionLimit === undefined) {
    return null;
  }
  const exhaustion =
    exhaustionBudget !== undefined || exhaustionLimit !== undefined
      ? `${exhaustionBudget ?? "?"} of ${exhaustionLimit ?? "?"}`
      : undefined;
  return (
    <section className={CLASS.section} data-testid="subagent-job-detail">
      <div className={CLASS.sectionLabel}>Job</div>
      <div className={CLASS.mandate}>
        {exhaustion && <div>Exhaustion budget: {exhaustion}</div>}
        {resumable !== undefined && <div>{resumable ? "Resumable" : "Not resumable"}</div>}
      </div>
    </section>
  );
}

// One headless card for one delegate. Its full child watch drives quote,
// counts, and the expanded recent-activity region.
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
  const stable = useThreadsStore((s) => {
    if (sessionRef === undefined || row.delegateId === undefined) return undefined;
    const owner = s.threads.get(sessionRef) ?? s.watchedThreads.get(sessionRef);
    return owner?.delegates?.find((delegate) => delegate.delegateId === row.delegateId);
  });
  const displayKind = effectiveRowKind(row, stable);
  const attention = stable?.needsAttention ?? false;
  const childRunning = model ? cadenceStateForStatus(model.status.type) === "working" : displayKind === "running";

  const items = model ? model.turns.flatMap((t) => t.items) : [];
  const quotes = deriveQuotes(items);
  const reason = stable ? stable.reason : row.resultPreview;
  // Failures lead with their reason; other cards use the child's latest words.
  const latestQuote = quotes.at(-1)?.text;
  const quoteText = displayKind === "failed" && reason ? `✕ ${reason}` : (latestQuote ?? (reason || undefined));

  // Missing snapshots omit counts rather than fabricating zeroes.
  const realTurns = model ? model.turns.filter((t) => t.id !== SYSTEM_PRELUDE_TURN_ID) : undefined;
  const turnCount = realTurns?.length;
  const callCount = realTurns
    ? realTurns.flatMap((t) => t.items).filter((it) => it.type === "commandExecution").length
    : undefined;
  const stableUsage = stable?.usage;
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
  const clock = cardClock(row, stable, displayKind, nowMs);

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
                // Relative start time (absolute on hover) replaces the
                // always-visible wall clock; an unparseable start omits it.
                const startMs = q.startedAt !== undefined ? Date.parse(q.startedAt) : Number.NaN;
                const hasStart = !Number.isNaN(startMs);
                return (
                  <li key={q.id} className={live ? `${CLASS.quoteItem} ${CLASS.quoteLive}` : CLASS.quoteItem}>
                    <span className={CLASS.quoteText}>
                      {q.msg ? <em className={CLASS.quoteMsg}>{q.text}</em> : q.text}
                    </span>
                    {(runtime !== undefined || hasStart) && (
                      <span className={CLASS.quoteMeta}>
                        {runtime !== undefined && <span>{runtime}</span>}
                        {runtime !== undefined && hasStart && <span className={CLASS.statsSep}>·</span>}
                        {hasStart && <Timestamp value={startMs} now={nowMs} />}
                      </span>
                    )}
                  </li>
                );
              })}
            </ol>
          ) : (
            <div className={CLASS.quotesEmpty}>No activity yet</div>
          )}
          <JobDetailSection row={row} stable={stable} />
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
  const parsed = parseJSONObject(item.output);
  const status = parsed ? str(parsed, "status") : undefined;
  const delegateId = parsed ? str(parsed, "delegate_id") : undefined;
  // A settled activation-only result is historical data, not a stable
  // delegate control identity. Keep an in-flight call-keyed placeholder, but
  // never turn job_id into a delegate row.
  if (parsed && !delegateId) return null;
  const transcriptRef = parsed ? str(parsed, "transcript_ref") : undefined;
  const reason = parsed ? str(parsed, "reason") : undefined;
  const resumable = parsed && typeof parsed.resumable === "boolean" ? parsed.resumable : undefined;
  const exhaustionBudget = parsed ? str(parsed, "exhaustion_budget") : undefined;
  const exhaustionLimit = parsed && typeof parsed.exhaustion_limit === "number" ? parsed.exhaustion_limit : undefined;
  const fallbackRowKey = resolveRowKey(undefined, undefined, item.callId ?? item.id);
  const rowKey = resolveRowKey(delegateId, undefined, item.callId ?? item.id);
  return {
    rowKey,
    migrateFromRowKey: rowKey === fallbackRowKey ? undefined : fallbackRowKey,
    row: {
      kind: classifyJobStatus(status),
      delegateId,
      transcriptRef,
      startedAt: item.startedAt,
      completedAt: item.completedAt,
      resultPreview: reason ?? "",
      resumable,
      exhaustionBudget,
      exhaustionLimit,
    },
  };
}

function DelegateBody({ item, sessionRef }: ToolRenderProps) {
  const scopeKey = turnScopeKey(sessionRef, item.turnId);
  const projected = rowFromDelegateItem(item);
  const storedRow = useSubagentRow(scopeKey, projected?.rowKey ?? "");

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
  fold: "never", // a delegate card is never folded away into a run
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
  // reasoning as task_list's own `autoExpand: () => true`. Child watching
  // exists only while the body is expanded, for quotes and stats; collapsed
  // lifecycle and attention come from the stable owner projection. Opening at
  // settle makes the card visible without a click; a manual collapse afterward
  // still sticks (ToolCallItem's own autoDefault vs. store-backed toggle).
  autoExpand: () => true,
});
