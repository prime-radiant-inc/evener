// The systemMessage item renderer: quiet lifecycle/skill notices (model
// switch, skill activation, plugin loads, hook completions, round timings,
// ...) plus the scaffolding blocks (the session system prompt and compaction
// summaries). Scaffolding is classified off the wire's typed
// ThreadItem.eventKind discriminator (carried onto ItemModel by reducer.ts's
// wireItemToModel) and rendered as a collapsed-by-default disclosure; every
// other systemMessage kind gets the SAME honest, quiet one-line/grouped
// treatment, without a separate visual identity per sub-kind. That already
// satisfies "system/skill notices: collapsed-by-default quiet groups"
// literally (a skill activation IS a systemMessage item, so it gets the
// quiet/grouped treatment). See the wave-4 T2 report for the uniform
// non-scaffold treatment as a deliberate scope simplification.
//
// Grouping (systemGrouping.ts's systemRunFor/shouldGroup) is recomputed
// fresh from turn.items on every render: a run of 3+ consecutive
// systemMessage items collapses into one disclosure, rendered only by the
// run's FIRST member (every other member of a grouped run renders nothing -
// its content already appears inside the first member's group). A run
// under 3 renders each item as its own standalone line, matching parity
// (contracts-transcript-scroll-liveness.md #12).

import type { ItemModel } from "../../../../protocol/model";
import { Markdown } from "../../../../widgets";
import { isDisclosureOpen, toggleDisclosure } from "../../../../widgets/disclosure/disclosureStore";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { type ItemRenderProps, registerItemRenderer } from "../types";
import { firstLine, formatCharCount } from "./format";
import { type SystemRun, shouldGroup, systemRunFor } from "./systemGrouping";
import styles from "./systemnoticeitem.module.css";

const CLASS = {
  line: requireClass(styles.line, "systemnoticeitem.module.css", "line"),
  group: requireClass(styles.group, "systemnoticeitem.module.css", "group"),
  summary: requireClass(styles.summary, "systemnoticeitem.module.css", "summary"),
  groupBody: requireClass(styles.groupBody, "systemnoticeitem.module.css", "groupBody"),
  scaffold: requireClass(styles.scaffold, "systemnoticeitem.module.css", "scaffold"),
  scaffoldSummary: requireClass(styles.scaffoldSummary, "systemnoticeitem.module.css", "scaffoldSummary"),
  scaffoldBody: requireClass(styles.scaffoldBody, "systemnoticeitem.module.css", "scaffoldBody"),
};

// The session's system prompt (apptranscript.go's PreludeTurn) arrives as a
// systemMessage item with this exact, static id. It is the narrow fallback
// signal for a system prompt projected by an older daemon that predates the
// typed system_prompt eventKind (below) - the id has been stable across every
// version, so a heterogeneous-version relay still collapses it correctly.
const SYSTEM_PROMPT_ITEM_ID = "item_system_prompt";

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
function ScaffoldDisclosure({ item }: { item: ItemModel }) {
  // Open/closed state lives in the shared disclosureStore keyed by item.id
  // (yt2q), so an expanded scaffold survives the VirtualList/dockview remount
  // that would reset a native uncontrolled <details>. Collapsed by default.
  const open = isDisclosureOpen(item.id, false);
  return (
    <details className={CLASS.scaffold} data-testid="system-notice-scaffold" open={open}>
      {/* biome-ignore lint/a11y/noStaticElementInteractions: <summary> is natively keyboard-operable; controlled to keep the store the single source of truth (see ToolCallItem.tsx) */}
      <summary
        className={CLASS.scaffoldSummary}
        onClick={(e) => {
          e.preventDefault();
          toggleDisclosure(item.id, false);
        }}
      >
        {scaffoldLabel(item)} · {formatCharCount(item.text.length)}
      </summary>
      <div className={CLASS.scaffoldBody}>
        <Markdown source={item.text} />
      </div>
    </details>
  );
}

function SystemLine({ item }: { item: ItemModel }) {
  if (isScaffoldItem(item)) return <ScaffoldDisclosure item={item} />;
  return (
    <div className={CLASS.line} data-testid="system-notice-line">
      {noticeText(item)}
    </div>
  );
}

function SystemGroup({ run }: { run: SystemRun }) {
  const count = run.items.length;
  // Every SystemGroup caller already checked shouldGroup(run), i.e.
  // count >= MIN_GROUP_SIZE - so items[0] always exists. That guarantee
  // lives in the caller, not in SystemRun's own type, so check it for real
  // here rather than asserting past it.
  const firstItem = run.items[0];
  if (!firstItem) throw new Error("SystemGroup rendered with an empty run");
  const first = firstLine(noticeText(firstItem), 60);
  // Open/closed state lives in the shared disclosureStore keyed by the run's
  // first item id (yt2q) - the run's stable identity across renders - so an
  // expanded group survives the VirtualList/dockview remount that would reset
  // a native uncontrolled <details>. Collapsed by default.
  const open = isDisclosureOpen(firstItem.id, false);
  return (
    <details className={CLASS.group} data-testid="system-notice-group" open={open}>
      {/* biome-ignore lint/a11y/noStaticElementInteractions: <summary> is natively keyboard-operable; controlled to keep the store the single source of truth (see ToolCallItem.tsx) */}
      <summary
        className={CLASS.summary}
        onClick={(e) => {
          e.preventDefault();
          toggleDisclosure(firstItem.id, false);
        }}
      >
        {count} system events · {first}
      </summary>
      <div className={CLASS.groupBody}>
        {run.items.map((it) => (
          <SystemLine key={it.id} item={it} />
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
export function SystemNoticeItem({ item, turn }: ItemRenderProps) {
  const run = systemRunFor(turn.items, item.id);
  if (!run) return null; // defensive - the registry only dispatches systemMessage items here

  if (shouldGroup(run)) {
    if (!run.isFirst) return null; // absorbed into the run's first member
    return <SystemGroup run={run} />;
  }

  return <SystemLine item={item} />;
}

registerItemRenderer("systemMessage", SystemNoticeItem);
