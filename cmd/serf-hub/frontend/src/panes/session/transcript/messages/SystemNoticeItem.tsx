// The systemMessage item renderer: quiet lifecycle/skill notices (compaction,
// model switch, skill activation, plugin loads, hook completions, round
// timings, ...). protocol/model.ts's ItemModel does not carry the wire's
// eventKind/description fields (types.gen.ts's ThreadItem has both;
// reducer.ts's wireItemToModel - T1-owned, out of this stream's scope to
// edit - never copies them across), so this renderer cannot distinguish
// "skill activated" from "round timings" from "compaction" etc. the way
// legacy's renderer.js does with its own separate coalescing tracks per
// sub-kind. Given that, every systemMessage item gets the SAME honest,
// quiet treatment uniformly - which already satisfies "system/skill
// notices: collapsed-by-default quiet groups" literally (a skill activation
// IS a systemMessage item, so it already gets the quiet/grouped treatment),
// just without a separate visual identity per sub-kind. See the wave-4 T2
// report for this as a flagged, deliberate scope simplification.
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
// systemMessage item with this exact, static id - protocol/model.ts's
// ItemModel carries none of the wire's eventKind/description fields (see
// this file's own top comment), so id is the only signal available here to
// name it precisely, rather than guessing from content.
const SYSTEM_PROMPT_ITEM_ID = "item_system_prompt";

// Any OTHER systemMessage item this long (a compaction/context-checkpoint
// summary is the realistic case - apptranscript.go's ProjectTurn, "Context
// summary"/"Context checkpoint") reads as its own wall of unformatted
// markdown once it clears a couple of sentences, so it earns the same
// disclosure treatment even without a dedicated id. Below this, a notice
// stays a plain quiet line - most lifecycle notices (model switch, goal
// continuation) are well under this.
const SCAFFOLD_CHAR_THRESHOLD = 500;

function isScaffoldItem(item: ItemModel): boolean {
  return item.id === SYSTEM_PROMPT_ITEM_ID || item.text.length > SCAFFOLD_CHAR_THRESHOLD;
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
// "System prompt"); any other scaffolding item falls back to the same
// first-line preview SystemGroup already uses for its own summary.
function scaffoldLabel(item: ItemModel): string {
  if (item.id === SYSTEM_PROMPT_ITEM_ID) return "System prompt";
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
