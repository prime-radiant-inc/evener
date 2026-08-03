// The reasoning ("think block") item renderer. Live: open and streaming the
// whole time it is the turn's CURRENT thought (wave-4 T2's "OPEN while
// live"), then settling the moment anything later starts in the turn - the
// wire never completes a reasoning item mid-turn, so tail position stands in
// for the completion the wire withholds (see isCurrentThought). Settled:
// collapses to "Thought [· duration] · context" with a trailing
// rotate-on-open chevron (the draft restyle, mockup #4 of /dev/thoughts,
// Jesse's pick 2026-07-31), while the full Markdown body remains available
// at the one disclosure level.
//
// MARKDOWN, LIVE AND SETTLED: agents write reasoning in markdown, so BOTH
// states parse it through the same Markdown widget that renders a settled
// agent message - and from the SAME source (joinedReasoningParagraphs,
// blank-line joined), so the live-to-settled transition changes chrome
// (draft italic -> roman disclosure), never the document. The live view
// re-parses the whole joined document on every delta. The trade-off,
// stated plainly:
//
//   - What live gives up: StreamingText's append-only DOM contract (the
//     fast path that exists precisely so a burst of deltas never re-diffs
//     settled text - AgentMessageItem keeps it for agent prose, where
//     markdown-parsing per delta was rejected as the wave-4 binding
//     constraint) and, with it, the guarantee that settled text is never
//     re-laid-out mid-stream. A construct whose closer has not streamed yet
//     renders literal (`**bol` stays visible source until `d**` arrives).
//   - What live keeps: the reader sees the thought the way the agent wrote
//     it - headings, lists, emphasis, code - while it is still streaming,
//     instead of reading markdown source for the whole in-flight window.
//
// The interleaving hazard this file's history warns about does not apply
// here: reasoningSummaries is string[][] - per-summaryIndex chunk lists
// (protocol/model.ts) - and DIFFERENT indices can interleave (a delta for
// index 0 arriving after index 1 has already started). That only corrupts
// an append-only renderer tracking "how much is rendered" by chunk count;
// the Markdown widget consumes the WHOLE joined source and emits a whole
// element tree on every render, so a mid-document insertion is just another
// re-parse - correct by construction (see ThinkBlock.test.tsx's own
// interleaving test).

import { memo } from "react";
import type { ItemModel, TurnModel } from "../../../../protocol/model";
import { Chevron, Markdown, ToolIcon } from "../../../../widgets";
import { isDisclosureOpen, toggleDisclosure } from "../../../../widgets/disclosure/disclosureStore";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { itemScopeKey } from "../tools/subagentModuleStore";
import { type ItemRenderProps, ignoringTurn, registerItemRenderer } from "../types";
import {
  formatThoughtDuration,
  joinedReasoningParagraphs,
  lastMeaningfulThoughtLine,
  thoughtDurationMs,
} from "./reasoningFormat";
import styles from "./thinkblock.module.css";

const CLASS = {
  block: requireClass(styles.block, "thinkblock.module.css", "block"),
  live: requireClass(styles.live, "thinkblock.module.css", "live"),
  label: requireClass(styles.label, "thinkblock.module.css", "label"),
  icon: requireClass(styles.icon, "thinkblock.module.css", "icon"),
  liveBody: requireClass(styles.liveBody, "thinkblock.module.css", "liveBody"),
  details: requireClass(styles.details, "thinkblock.module.css", "details"),
  summary: requireClass(styles.summary, "thinkblock.module.css", "summary"),
  summaryText: requireClass(styles.summaryText, "thinkblock.module.css", "summaryText"),
  chevron: requireClass(styles.chevron, "thinkblock.module.css", "chevron"),
  body: requireClass(styles.body, "thinkblock.module.css", "body"),
};

// The thought row leads with the same leading-icon grammar the tool rows use
// (Jesse's review call: "thinking should also have an icon") - the lightbulb
// kind glyph at the transcript's one icon size, in the row's own quiet ink
// (the widget strokes currentColor; .label and .summary both set --ink-low).
const thoughtIcon = (
  <span className={CLASS.icon} data-testid="think-block-icon" aria-hidden="true">
    <ToolIcon kind="thought" />
  </span>
);

const THOUGHT_PREVIEW_MAX_LENGTH = 120;

// thoughtLabel never fabricates a duration: `durationMs` is undefined whenever
// neither the wire pair nor the observed pair is available or valid (see
// reasoningFormat.ts). Every part joins on the same " · " separator - the
// draft restyle's "Thought · 12s · preview" grammar (mockup #4), one delimiter
// instead of the old "Thought for 12s" phrase. The context is a bounded
// plain-text rendering of the final meaningful line, not a second Markdown
// body; opening the disclosure still reveals the complete source through
// Markdown.
function thoughtLabel(durationMs: number | undefined, preview: string): string {
  const parts = ["Thought"];
  if (durationMs !== undefined) parts.push(formatThoughtDuration(durationMs));
  if (preview) parts.push(preview);
  return parts.join(" · ");
}

// The live view (mockup #4's draft treatment): a quiet "Thinking…" eyebrow
// over the streaming markdown body, in its own component so the two states
// read as the two separate layouts they are.
//
// It carries NO height bound (Jesse, bh8h): a thought that is still running is
// shown in full, however long it grows, so the reader can follow the whole
// block as it streams. The transcript's own scroller is the viewport - the
// same one every other growing item shares - so there is nothing here to clip,
// to fade, or to pin to a tail.
function LiveThinkBlock({ item }: { item: ItemModel }) {
  // One document, not one per summaryIndex: a markdown parser needs the whole
  // text to resolve block structure. Blank-line joined so each index still
  // starts its own block-level token rather than being folded into the
  // previous index's paragraph - the SAME source the settled body renders,
  // so settling never reflows the document. Re-parsed on every delta; safe
  // under interleaved indices because nothing here is append-only (see this
  // file's top comment).
  const paragraphs = joinedReasoningParagraphs(item.reasoningSummaries);
  return (
    <div className={CLASS.block} data-testid="think-block" data-live="true">
      <div className={CLASS.live}>
        <span className={CLASS.label}>
          {thoughtIcon}
          Thinking…
        </span>
        <div className={CLASS.liveBody} data-testid="think-block-live-body">
          {/* Empty so far (the item exists the instant it starts, possibly
              before its first delta) renders no Markdown at all - marked
              turns an empty string into an empty <p>-less document, but an
              explicit guard keeps the zero-content case from depending on
              that detail. Paragraphs that join to whitespace are already
              dropped by joinedReasoningParagraphs, so a zero-chunk
              summaryIndex leaves no empty-paragraph gap. */}
          {paragraphs.length > 0 && <Markdown source={paragraphs.join("\n\n")} />}
        </div>
      </div>
    </div>
  );
}

// isCurrentThought: whether this reasoning item is still the turn's CURRENT
// activity - the tail of turn.items. The wire never emits item/completed for
// a reasoning item (only turn/completed settles it; TurnBlock's isItemLive
// comment defers this per-type nuance HERE), so wire status alone would keep
// a thought "live" for the whole rest of the turn - a finished thought would
// sit open in its draft italic while the assistant answers, instead of
// collapsing to its one-line summary. Tail position is the honest signal:
// the projector abandons its reasoning item exactly when the next activity
// starts. An item absent from turn.items falls back to current (true), which
// keeps wire status the only signal for a renderer exercised outside
// TurnBlock and fails toward showing MORE, never less.
function isCurrentThought(item: ItemModel, turn: TurnModel): boolean {
  const items = turn.items;
  if (items.length === 0 || !items.some((candidate) => candidate.id === item.id)) return true;
  return items[items.length - 1]?.id === item.id;
}

// The memo comparator: ignoringTurn's contract (a fresh turn object on every
// unrelated delta must not re-render a settled block), plus the one turn-
// derived bit this renderer reads - isCurrentThought - compared by VALUE, so
// the single flip from current to superseded re-renders exactly once and
// every other turn.items append stays skipped. types.ts's ignoringTurn
// comment demands exactly this of a renderer that starts reading `turn`.
function thinkBlockPropsEqual(prev: ItemRenderProps, next: ItemRenderProps): boolean {
  return ignoringTurn(prev, next) && isCurrentThought(prev.item, prev.turn) === isCurrentThought(next.item, next.turn);
}

export const ThinkBlock = memo(function ThinkBlock({ item, turn, live, sessionRef }: ItemRenderProps) {
  const isLive = (live || item.status === "inProgress") && isCurrentThought(item, turn);
  if (isLive) return <LiveThinkBlock item={item} />;

  const paragraphs = joinedReasoningParagraphs(item.reasoningSummaries);
  if (paragraphs.length === 0) return null; // empty thoughts removed

  const durationMs = thoughtDurationMs(
    item.startedAt,
    item.completedAt,
    item.observedStartedAt,
    item.observedCompletedAt,
  );
  const preview = lastMeaningfulThoughtLine(paragraphs, THOUGHT_PREVIEW_MAX_LENGTH);
  // Open/closed state lives in the shared disclosureStore keyed by session ref
  // plus item id, so an expanded thought survives a remount without colliding
  // with the same item id in another session. Collapsed by default.
  const disclosureKey = itemScopeKey(sessionRef, item.id);
  const open = isDisclosureOpen(disclosureKey, false);

  return (
    <div className={CLASS.block} data-testid="think-block" data-live="false">
      <details className={CLASS.details} open={open}>
        {/* biome-ignore lint/a11y/noStaticElementInteractions: <summary> is natively keyboard-operable; controlled to keep the store the single source of truth (see ToolCallItem.tsx) */}
        <summary
          className={CLASS.summary}
          onClick={(e) => {
            e.preventDefault();
            toggleDisclosure(disclosureKey, false);
          }}
        >
          {thoughtIcon}
          {/* The label text in its own shrinkable span (not bare summary text):
              as a flex item it can ellipsize under pressure, so the trailing
              chevron always stays on screen at the end of the words instead of
              being pushed past the summary's clipped edge. */}
          <span className={CLASS.summaryText}>
            {open ? thoughtLabel(durationMs, "") : thoughtLabel(durationMs, preview)}
          </span>
          {/* Mockup #4's trailing affordance: the shared widgets/chevron at the
              tail of the collapsed line, turning 90° when open via the same
              data-open idiom ToolRow uses (rotation turns the SQUARE svg, so it
              cannot widen its painted box - see toolcallitem.module.css). */}
          <span
            className={CLASS.chevron}
            aria-hidden="true"
            data-open={open ? "true" : "false"}
            data-testid="think-block-chevron"
          >
            <Chevron />
          </span>
        </summary>
        <div className={CLASS.body}>
          {/* One document, not one per summaryIndex: a markdown parser needs
              the whole text to resolve block structure. Blank-line joined so
              each index still starts its own block-level token rather than
              being folded into the previous index's paragraph. Safe here and
              only here - this settled branch runs after reasoningSummaries
              has stopped growing, so there is no append-only invariant left
              to violate (see this file's top comment). Markdown owns its own
              paragraph/heading/list layout, so nothing re-wraps it in
              .paragraph. */}
          <Markdown source={paragraphs.join("\n\n")} />
        </div>
      </details>
    </div>
  );
}, thinkBlockPropsEqual);

registerItemRenderer("reasoning", ThinkBlock);
