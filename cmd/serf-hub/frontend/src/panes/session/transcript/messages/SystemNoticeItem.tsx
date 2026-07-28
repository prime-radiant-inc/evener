// The systemMessage item renderer: quiet lifecycle/skill notices (model
// switch, skill activation, plugin loads, hook completions, round timings,
// ...) plus the scaffolding blocks (the session system prompt and compaction
// summaries). Scaffolding is classified off the wire's typed
// ThreadItem.eventKind discriminator (carried onto ItemModel by reducer.ts's
// wireItemToModel) and rendered as a collapsed-by-default disclosure. One more
// kind earns an identity of its own: a persisted turn failure ("error"), which
// is the one system item a reader actively hunts for rather than scrolls past
// - see FailureLine below. Every other kind gets the SAME honest, quiet
// one-line/grouped treatment, without a separate visual identity per sub-kind.
// That already satisfies "system/skill notices: collapsed-by-default quiet
// groups" literally (a skill activation IS a systemMessage item, so it gets
// the quiet/grouped treatment). See the wave-4 T2 report for the uniform
// non-scaffold treatment as a deliberate scope simplification.
//
// Grouping (systemGrouping.ts's systemRunFor/shouldGroup) is recomputed
// fresh from turn.items on every render: a run of 3+ consecutive
// systemMessage items collapses into one disclosure, rendered only by the
// run's FIRST member (every other member of a grouped run renders nothing -
// its content already appears inside the first member's group). A run
// under 3 renders each item as its own standalone line, matching parity
// (contracts-transcript-scroll-liveness.md #12). A failure joins no run at
// all, so no disclosure can hold it behind a summary describing a different
// item.

import type { ItemModel, TurnModel } from "../../../../protocol/model";
import { FailureGlyph, Markdown } from "../../../../widgets";
import { isDisclosureOpen, toggleDisclosure } from "../../../../widgets/disclosure/disclosureStore";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { itemScopeKey } from "../tools/subagentModuleStore";
import { SYSTEM_PROMPT_ITEM_ID } from "../transcriptVisibility";
import { asTurnError } from "../turnFailure";
import { type ItemRenderProps, registerItemRenderer } from "../types";
import { firstLine, formatCharCount, formatDurationMs } from "./format";
import { roundTimingsSummary } from "./roundTimingsView";
import { isTurnFailureItem, type SystemRun, shouldGroup, systemRunFor } from "./systemGrouping";
import styles from "./systemnoticeitem.module.css";

const CLASS = {
  line: requireClass(styles.line, "systemnoticeitem.module.css", "line"),
  failure: requireClass(styles.failure, "systemnoticeitem.module.css", "failure"),
  failureText: requireClass(styles.failureText, "systemnoticeitem.module.css", "failureText"),
  group: requireClass(styles.group, "systemnoticeitem.module.css", "group"),
  summary: requireClass(styles.summary, "systemnoticeitem.module.css", "summary"),
  groupBody: requireClass(styles.groupBody, "systemnoticeitem.module.css", "groupBody"),
  scaffold: requireClass(styles.scaffold, "systemnoticeitem.module.css", "scaffold"),
  scaffoldSummary: requireClass(styles.scaffoldSummary, "systemnoticeitem.module.css", "scaffoldSummary"),
  scaffoldBody: requireClass(styles.scaffoldBody, "systemnoticeitem.module.css", "scaffoldBody"),
};

// SYSTEM_PROMPT_ITEM_ID (imported above) is the narrow fallback signal for a
// system prompt projected by an older daemon that predates the typed
// system_prompt eventKind - see its definition in transcriptVisibility.ts,
// which classifies the same item for the "Prompt loaded" setting.

// The systemMessage eventKinds (appwire.ThreadItemEventKind*) whose text is a
// scaffolding block a reader collapses by default rather than a quiet
// one-liner: the session system prompt, and a compaction summary/checkpoint
// (apptranscript.go's ProjectTurn "Context summary"/"Context checkpoint",
// each a wall of markdown). Every other kind (model switch, skill activation,
// the short live context_compaction stats line, ...) stays a plain quiet
// line. Classification is by this typed wire field, never the item's own char
// count (kata ckgw).
const SCAFFOLD_EVENT_KINDS = new Set(["system_prompt", "compaction"]);

function isScaffoldItem(item: ItemModel): boolean {
  if (item.eventKind !== undefined && item.eventKind !== "") return SCAFFOLD_EVENT_KINDS.has(item.eventKind);
  return item.id === SYSTEM_PROMPT_ITEM_ID;
}

// FALLBACK_LABEL covers a systemMessage item with no text at all (e.g. a
// plugin-loaded event with no resolved plugin name) - a category label
// beats an invisible row, without fabricating specifics the wire didn't
// provide.
const FALLBACK_LABEL = "System event";

function noticeText(item: ItemModel): string {
  return item.text || FALLBACK_LABEL;
}

// scaffoldLabel names the disclosure's collapsed summary: the system
// prompt gets its own exact, honest label (never a snippet of its own
// content, which routinely opens with something less recognizable than
// "System prompt") - keyed off the typed system_prompt eventKind, or the
// stable id for an older daemon that predates it; any other scaffolding item
// falls back to the same first-line preview SystemGroup already uses for its
// own summary.
function scaffoldLabel(item: ItemModel): string {
  if (item.eventKind === "system_prompt" || item.id === SYSTEM_PROMPT_ITEM_ID) return "System prompt";
  return firstLine(noticeText(item), 60) || FALLBACK_LABEL;
}

// ScaffoldDisclosure is the collapsed-by-default treatment for the system
// prompt and any other long system-injected text (webui-ux-transcript C1):
// collapsed to one quiet line ("System prompt · 8.2k chars") by default;
// expanding renders the FULL text through the same Markdown pipeline every
// other message body uses, since the wire's own text is markdown (## headers
// etc.) that would otherwise show as literal, unformatted characters.
function ScaffoldDisclosure({ item, sessionRef }: { item: ItemModel; sessionRef?: string }) {
  // Open/closed state lives in the shared disclosureStore keyed by session ref
  // plus item id, so an expanded scaffold survives a remount without colliding
  // with the same item id in another session. Collapsed by default.
  const disclosureKey = itemScopeKey(sessionRef, item.id);
  const open = isDisclosureOpen(disclosureKey, false);
  return (
    <details className={CLASS.scaffold} data-testid="system-notice-scaffold" open={open}>
      {/* biome-ignore lint/a11y/noStaticElementInteractions: <summary> is natively keyboard-operable; controlled to keep the store the single source of truth (see ToolCallItem.tsx) */}
      <summary
        className={CLASS.scaffoldSummary}
        onClick={(e) => {
          e.preventDefault();
          toggleDisclosure(disclosureKey, false);
        }}
      >
        {scaffoldLabel(item)} · {formatCharCount(item.text.length)}
      </summary>
      <div className={CLASS.scaffoldBody} data-testid="system-notice-scaffold-body">
        <Markdown source={item.text} />
      </div>
    </details>
  );
}

// ROUND_TIMINGS_EVENT_KIND matches transcriptVisibility.ts's own constant of
// the same name/value (the visibility gate and this render-time
// classification read the same typed field independently, same pattern as
// SCAFFOLD_EVENT_KINDS above).
const ROUND_TIMINGS_EVENT_KIND = "round_timings";

function isRoundTimingsItem(item: ItemModel): boolean {
  return item.eventKind === ROUND_TIMINGS_EVENT_KIND;
}

// pctLabel renders a phase's share of the round: whole percents plain,
// "<1%" for a real nonzero share too small to round to 1 - "0%" would read
// as "took no time", which a phase that reached the >=1ms floor never did.
function pctLabel(pct: number): string {
  return pct > 0 ? `${pct}%` : "<1%";
}

// RoundTimingsLine is the round_timings systemMessage item's redesigned
// display (kata 7zkv): the raw dump ("Round 0 total=6.411312958s
// llm=4.935822084s context=8.625µs ...") answered "where did this round go"
// only if a reader did the arithmetic themselves. This reads the real
// per-phase numbers (roundTimingsSummary, from item.raw) and leads with the
// one phase that actually explains the round's length, at a precision a
// reader can act on (whole ms, not ns) - negligible (<1ms) phases dropped
// rather than rounded into a false "1ms".
//
// It is ONE quiet line with no disclosure (Jesse's review call on the
// tiered-density follow-up: the expanded breakdown - LLM/Tools/Overhead
// rows - was more furniture than the opt-in diagnostic is worth). The full
// per-phase breakdown stays reachable on the line's hover title; nothing
// else is lost, since the raw numbers remain on the wire item itself.
//
// Falls back to the plain prose line when raw is absent or malformed - a
// heterogeneous-version relay from a daemon predating this field still shows
// something rather than nothing.
function RoundTimingsLine({ item }: { item: ItemModel }) {
  const summary = roundTimingsSummary(item.raw);
  if (!summary) {
    return (
      <div className={CLASS.line} data-testid="system-notice-line">
        {noticeText(item)}
      </div>
    );
  }
  const total = formatDurationMs(summary.totalMs);
  if (!summary.dominant) {
    // Every tracked phase rounded under 1ms - nothing to break down further.
    return (
      <div className={CLASS.line} data-testid="system-notice-line">
        Round {summary.round} · {total}
      </div>
    );
  }
  const headline = `Round ${summary.round} · ${total} — ${summary.dominant.label} ${formatDurationMs(summary.dominant.ms)} (${pctLabel(summary.dominant.pct)})`;
  const breakdown = [
    ...summary.phases.map((phase) => `${phase.label} ${formatDurationMs(phase.ms)} (${pctLabel(phase.pct)})`),
    ...(summary.omittedCount > 0
      ? [`+ ${summary.omittedCount} phase${summary.omittedCount === 1 ? "" : "s"} under 1ms`]
      : []),
  ].join("\n");
  return (
    <div className={CLASS.line} data-testid="system-notice-line" title={breakdown}>
      {headline}
    </div>
  );
}

// FAILURE_FALLBACK_LABEL names a failure the wire described with neither a
// message nor a description. It is the same category-label-over-invisible-row
// rule FALLBACK_LABEL follows, worded for the one event where the generic
// "System event" would be worst: a row that says a failure happened beats one
// that files it under nothing in particular.
const FAILURE_FALLBACK_LABEL = "Turn failed";

// What the failure row says. The end cap that closes a failed turn (TurnBlock
// renders it on exactly this condition) already states the message with its
// taxonomy chip, hint and recovery action, so with a cap the row names the
// event and lets the cap carry the detail - saying the same sentence twice, ten
// pixels apart, is what a reloaded failure did before. With no cap (an item
// that reached a client without a turn-level error) the row leads with the
// message, since nothing else will carry it: a failure is never left unstated.
function failureText(item: ItemModel, turn: TurnModel): string {
  const named = item.description?.trim();
  const message = item.text.trim();
  const preferred = asTurnError(turn.error) ? [named, message] : [message, named];
  return preferred.find((candidate) => candidate) ?? FAILURE_FALLBACK_LABEL;
}

// FailureLine is a turn failure: the one systemMessage a reader is hunting for
// (three research personas named a failure first, unprompted), so it wears the
// same row grammar a failed tool call does - the --danger FailureGlyph widget
// plus full-contrast ink - rather than the quiet --ink-low one-liner every
// lifecycle notice shares. data-attention="error" is the same urgent anchor
// ToolCallItem tags a failed row with (parity §11).
function FailureLine({ item, turn }: { item: ItemModel; turn: TurnModel }) {
  return (
    <div className={CLASS.failure} data-testid="system-notice-failure" data-attention="error">
      <FailureGlyph />
      <span className={CLASS.failureText}>{failureText(item, turn)}</span>
    </div>
  );
}

function SystemLine({ item, turn, sessionRef }: { item: ItemModel; turn: TurnModel; sessionRef?: string }) {
  if (isTurnFailureItem(item)) return <FailureLine item={item} turn={turn} />;
  if (isScaffoldItem(item)) return <ScaffoldDisclosure item={item} sessionRef={sessionRef} />;
  if (isRoundTimingsItem(item)) return <RoundTimingsLine item={item} />;
  return (
    <div className={CLASS.line} data-testid="system-notice-line">
      {noticeText(item)}
    </div>
  );
}

function SystemGroup({ run, turn, sessionRef }: { run: SystemRun; turn: TurnModel; sessionRef?: string }) {
  const count = run.items.length;
  // Every SystemGroup caller already checked shouldGroup(run), i.e.
  // count >= MIN_GROUP_SIZE - so items[0] always exists. That guarantee
  // lives in the caller, not in SystemRun's own type, so check it for real
  // here rather than asserting past it.
  const firstItem = run.items[0];
  if (!firstItem) throw new Error("SystemGroup rendered with an empty run");
  const first = firstLine(noticeText(firstItem), 60);
  // Open/closed state lives in the shared disclosureStore keyed by session ref
  // plus the run's first item id - the run's stable identity across renders -
  // so an expanded group survives a remount without cross-session collision.
  const disclosureKey = itemScopeKey(sessionRef, firstItem.id);
  const open = isDisclosureOpen(disclosureKey, false);
  return (
    <details className={CLASS.group} data-testid="system-notice-group" open={open}>
      {/* biome-ignore lint/a11y/noStaticElementInteractions: <summary> is natively keyboard-operable; controlled to keep the store the single source of truth (see ToolCallItem.tsx) */}
      <summary
        className={CLASS.summary}
        onClick={(e) => {
          e.preventDefault();
          toggleDisclosure(disclosureKey, false);
        }}
      >
        {count} system events · {first}
      </summary>
      <div className={CLASS.groupBody}>
        {run.items.map((it) => (
          <SystemLine key={it.id} item={it} turn={turn} sessionRef={sessionRef} />
        ))}
      </div>
    </details>
  );
}

// Deliberately NOT memoized with types.ts's ignoringTurn, unlike every
// other registered item renderer (wave-4 T5c): this component reads
// turn.items (systemRunFor, above) to compute its own consecutive-run
// grouping, so its correct output depends on turn identity - a sibling
// system item joining or leaving this item's run changes turn.items
// without changing THIS item's own reference or live status, and ignoring
// turn identity would leave an already-mounted run stale (still showing as
// standalone lines, or the wrong count) when that happens. The accepted
// cost: unmemoized, this component re-renders on EVERY delta whenever its
// turn is visible (its unmemoized ancestors re-invoke it regardless of
// props - React only bails out at a memo boundary). That is fine here
// because its render is cheap - one linear systemRunFor scan plus a few
// divs, no markdown/diff work - unlike the heavy renderers ignoringTurn
// exists to protect.
export function SystemNoticeItem({ item, turn, sessionRef }: ItemRenderProps) {
  const run = systemRunFor(turn.items, item.id);
  if (!run) return null; // defensive - the registry only dispatches systemMessage items here

  if (shouldGroup(run)) {
    if (!run.isFirst) return null; // absorbed into the run's first member
    return <SystemGroup run={run} turn={turn} sessionRef={sessionRef} />;
  }

  return <SystemLine item={item} turn={turn} sessionRef={sessionRef} />;
}

registerItemRenderer("systemMessage", SystemNoticeItem);
