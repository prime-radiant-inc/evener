// The reasoning ("think block") item renderer. Live: fully open, streaming
// (parity #7: "the projector emits a single reasoning item per turn" -
// stays open the whole time it's the current thought, never collapsed
// mid-stream this wave - see the wave-4 T2 scope's own "OPEN + StreamingText
// while live", a deliberate simplification of legacy's mid-stream-
// collapsible button). Settled: collapses to "Thought [for Ns]" - duration
// only, deliberately no reasoning-text preview (see thoughtLabel's own
// comment for why).
//
// reasoningSummaries is string[][] - per-summaryIndex chunk lists
// (protocol/model.ts). Each index's chunks only ever grow by appending
// (reducer.ts's appendReasoningDelta always pushes onto summaries[i]
// specifically) but DIFFERENT indices can interleave (a delta for index 0
// arriving after index 1 has already started) - flattening every index
// into one StreamingText's chunks prop would violate StreamingText's own
// "only ever grows by appending, tracked by count" invariant the moment
// that happens (an earlier index growing would shift a later index's
// chunks to new positions in the flattened array, duplicating/dropping
// text - see ThinkBlock.test.tsx's own interleaving test). Rendering one
// independent StreamingText per index sidesteps this entirely: each index's
// own chunk array is safe in isolation, regardless of what any other index
// does.
//
// MARKDOWN, AND WHY ONLY WHEN SETTLED: agents write reasoning in markdown,
// so the settled body parses it through the same Markdown widget that
// renders a settled agent message. The live path deliberately does NOT -
// it keeps streaming literal text. The trade-off, stated plainly:
//
//   - What live gives up: markdown formatting is visible as source
//     (`## heading`, `**bold**`) for the seconds a thought is in flight.
//   - What live keeps: StreamingText's append-only DOM contract, and with
//     it the interleaving guarantee above. A markdown parser consumes a
//     WHOLE document and emits a whole element tree, so parsing while live
//     would mean either re-parsing every index on every delta (throwing
//     away the append-only fast path that exists precisely because a burst
//     of deltas must not re-diff settled text) or flattening indices into
//     one document (which breaks interleaving outright).
//
// Per-paragraph parsing was the alternative and was rejected: a markdown
// block is not always one paragraph (a fenced code block, a multi-line
// list, a table all span blank lines), so "parse each summaryIndex as it
// stabilizes" has no reliable signal for "stabilized" mid-stream - the
// reducer never tells us an index is finished, only that the whole item
// is. Since the item settling IS that signal, parse there. This mirrors
// AgentMessageItem's identical live-plain/settled-markdown split, so both
// message types behave the same way under streaming.

import { memo } from "react";
import { Markdown } from "../../../../widgets";
import { isDisclosureOpen, toggleDisclosure } from "../../../../widgets/disclosure/disclosureStore";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { StreamingText } from "../StreamingText";
import { itemScopeKey } from "../tools/subagentModuleStore";
import { type ItemRenderProps, ignoringTurn, registerItemRenderer } from "../types";
import { joinedReasoningParagraphs, thoughtSeconds } from "./reasoningFormat";
import styles from "./thinkblock.module.css";

const CLASS = {
  block: requireClass(styles.block, "thinkblock.module.css", "block"),
  live: requireClass(styles.live, "thinkblock.module.css", "live"),
  label: requireClass(styles.label, "thinkblock.module.css", "label"),
  liveBody: requireClass(styles.liveBody, "thinkblock.module.css", "liveBody"),
  paragraph: requireClass(styles.paragraph, "thinkblock.module.css", "paragraph"),
  details: requireClass(styles.details, "thinkblock.module.css", "details"),
  summary: requireClass(styles.summary, "thinkblock.module.css", "summary"),
  body: requireClass(styles.body, "thinkblock.module.css", "body"),
};

// thoughtLabel never fabricates a duration: `seconds` is undefined whenever
// neither the wire pair nor the observed pair is present on this item (see
// reasoningFormat.ts's own thoughtSeconds comment) - and the label honestly
// omits the number rather than measuring a client-side clock instead.
//
// Duration only, deliberately no reasoning-text preview (kdyh/tx8m): a
// preview built from the same text the expanded body renders in full is
// guaranteed to repeat itself the instant a reader opens the disclosure -
// and since a <summary> can only hold plain text, that preview could never
// honestly represent the model's own markdown either. Carrying no agent
// text here removes both problems at once rather than patching each with
// its own workaround.
function thoughtLabel(seconds: number | undefined): string {
  return seconds === undefined ? "Thought" : `Thought for ${seconds}s`;
}

// Memoized ignoring `turn` identity (types.ts's ignoringTurn): this
// component never reads `turn` at all (only `item`/`live`, destructured
// below), so a fresh turn object on every streaming delta targeting a
// DIFFERENT item must not re-render an already-settled think block.
export const ThinkBlock = memo(function ThinkBlock({ item, live, sessionRef }: ItemRenderProps) {
  if (live) {
    return (
      <div className={CLASS.block} data-testid="think-block" data-live="true">
        <div className={CLASS.live}>
          <span className={CLASS.label}>Thinking…</span>
          <div className={CLASS.liveBody}>
            {(item.reasoningSummaries ?? []).map((chunks, i) =>
              // A zero-chunk index (a later summaryIndex has started streaming
              // before this earlier one has) renders nothing rather than an
              // empty <p> - .paragraph carries its own margin, so an empty one
              // would still show as a visible gap. It appears in position the
              // instant its first chunk arrives.
              //
              // index-as-key is deliberate: i IS the stable identity here
              // (summaryIndex, per this file's top comment - "each index's
              // chunks only ever grow by appending", positions never reorder).
              chunks.length === 0 ? null : (
                // biome-ignore lint/suspicious/noArrayIndexKey: i is the stable summaryIndex, see above
                <p key={i} className={CLASS.paragraph}>
                  <StreamingText chunks={chunks} />
                </p>
              ),
            )}
          </div>
        </div>
      </div>
    );
  }

  const paragraphs = joinedReasoningParagraphs(item.reasoningSummaries);
  if (paragraphs.length === 0) return null; // empty thoughts removed

  const seconds = thoughtSeconds(item.startedAt, item.completedAt, item.observedStartedAt, item.observedCompletedAt);
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
          {thoughtLabel(seconds)}
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
}, ignoringTurn);

registerItemRenderer("reasoning", ThinkBlock);
