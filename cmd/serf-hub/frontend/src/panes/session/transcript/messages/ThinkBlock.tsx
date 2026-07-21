// The reasoning ("think block") item renderer. Live: fully open, streaming
// (parity #7: "the projector emits a single reasoning item per turn" -
// stays open the whole time it's the current thought, never collapsed
// mid-stream this wave - see the wave-4 T2 scope's own "OPEN + StreamingText
// while live", a deliberate simplification of legacy's mid-stream-
// collapsible button). Settled: collapses to "Thought [for Ns] · preview".
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

import { memo } from "react";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { StreamingText } from "../StreamingText";
import { type ItemRenderProps, ignoringTurn, registerItemRenderer } from "../types";
import { joinedReasoningParagraphs, reasoningPreview, thoughtSeconds } from "./reasoningFormat";
import styles from "./thinkblock.module.css";

const CLASS = {
  block: requireClass(styles.block, "thinkblock.module.css", "block"),
  label: requireClass(styles.label, "thinkblock.module.css", "label"),
  paragraph: requireClass(styles.paragraph, "thinkblock.module.css", "paragraph"),
  details: requireClass(styles.details, "thinkblock.module.css", "details"),
  summary: requireClass(styles.summary, "thinkblock.module.css", "summary"),
  body: requireClass(styles.body, "thinkblock.module.css", "body"),
};

// thoughtLabel never fabricates a duration: `seconds` is undefined whenever
// neither the wire pair nor the observed pair is present on this item (see
// reasoningFormat.ts's own thoughtSeconds comment) - and the label honestly
// omits the number rather than measuring a client-side clock instead.
function thoughtLabel(seconds: number | undefined, preview: string): string {
  const durationText = seconds === undefined ? "Thought" : `Thought for ${seconds}s`;
  return [durationText, preview].filter(Boolean).join(" · ");
}

// Memoized ignoring `turn` identity (types.ts's ignoringTurn): this
// component never reads `turn` at all (only `item`/`live`, destructured
// below), so a fresh turn object on every streaming delta targeting a
// DIFFERENT item must not re-render an already-settled think block.
export const ThinkBlock = memo(function ThinkBlock({ item, live }: ItemRenderProps) {
  if (live) {
    return (
      <div className={CLASS.block} data-testid="think-block" data-live="true">
        <span className={CLASS.label}>Thinking…</span>
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
    );
  }

  const paragraphs = joinedReasoningParagraphs(item.reasoningSummaries);
  if (paragraphs.length === 0) return null; // empty thoughts removed

  const seconds = thoughtSeconds(item.startedAt, item.completedAt, item.observedStartedAt, item.observedCompletedAt);
  const preview = reasoningPreview(item.reasoningSummaries);

  return (
    <div className={CLASS.block} data-testid="think-block" data-live="false">
      <details className={CLASS.details}>
        <summary className={CLASS.summary}>{thoughtLabel(seconds, preview)}</summary>
        <div className={CLASS.body}>
          {paragraphs.map((text, i) => (
            // This settled view only renders once item.reasoningSummaries
            // has stopped streaming (the live branch above is the only one
            // still growing) - paragraphs is a frozen snapshot by the time
            // this runs, so index is stable for this render's whole life.
            // biome-ignore lint/suspicious/noArrayIndexKey: frozen settled snapshot, see above
            <p key={i} className={CLASS.paragraph}>
              {text}
            </p>
          ))}
        </div>
      </details>
    </div>
  );
}, ignoringTurn);

registerItemRenderer("reasoning", ThinkBlock);
