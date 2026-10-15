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
// blank-line joined), so the live-to-settled transition changes only the
// chrome around the document (open stream -> collapsed disclosure), never
// the document itself. The body is ALWAYS italic in both states (Jesse,
// 2026-08-03), with markup-emphasis inverted to roman - see
// thinkblock.module.css. The live view passes the widget's `live` flag, so
// constructs left open at the stream tail are closed for the preview (see
// widgets/markdown/streaming.ts), and re-parses the whole joined document
// on every delta. The trade-off, stated plainly:
//
//   - What live gives up: StreamingText's append-only DOM contract (the
//     fast path that exists precisely so a burst of deltas never re-diffs
//     settled text) and, with it, the guarantee that settled text is never
//     re-laid-out mid-stream. The tail's auto-close is a preview heuristic:
//     a truncated `**bol` renders bold on the GUESS that its closer is
//     still coming, which is wrong for the rare genuinely-unbalanced final
//     source (the settled render then shows it literal - the preview always
//     converges to the truth at settle).
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

// Expanded-trace anatomy adapted from Beautiful UI's Thinking component (beautifului.dev, MIT © 2026 Shane Levine) — see LICENSES/beautiful-ui.txt.

import type { ItemModel, TurnModel } from "@evener/appwire-client";
import { formatTokenCount, scopedDisclosureId } from "@evener/appwire-client";
import { memo } from "react";
import {
  disclosureScopeForSession,
  expandDetailsByDefault,
  useTranscriptRenderContext,
} from "../../../../transcriptDisplay/renderContext";
import { Disclosure, Loader, Markdown, ToolIcon } from "../../../../widgets";
import { disclosureDefault, isDisclosureOpen } from "../../../../widgets/disclosure/disclosureStore";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { type ItemRenderProps, ignoringTurn, registerItemRenderer } from "../types";
import {
  formatThoughtDuration,
  joinedReasoningParagraphs,
  lastMeaningfulThoughtLine,
  segmentReasoningTrace,
  thoughtDurationMs,
} from "./reasoningFormat";
import styles from "./thinkblock.module.css";

const CLASS = {
  block: requireClass(styles.block, "thinkblock.module.css", "block"),
  live: requireClass(styles.live, "thinkblock.module.css", "live"),
  label: requireClass(styles.label, "thinkblock.module.css", "label"),
  icon: requireClass(styles.icon, "thinkblock.module.css", "icon"),
  summaryIcon: requireClass(styles.summaryIcon, "thinkblock.module.css", "summaryIcon"),
  liveBody: requireClass(styles.liveBody, "thinkblock.module.css", "liveBody"),
  summaryText: requireClass(styles.summaryText, "thinkblock.module.css", "summaryText"),
  body: requireClass(styles.body, "thinkblock.module.css", "body"),
  trace: requireClass(styles.trace, "thinkblock.module.css", "trace"),
  step: requireClass(styles.step, "thinkblock.module.css", "step"),
  stepMarker: requireClass(styles.stepMarker, "thinkblock.module.css", "stepMarker"),
  stepBody: requireClass(styles.stepBody, "thinkblock.module.css", "stepBody"),
};

// The thought row leads with the same leading-icon grammar the tool rows use
// (Jesse's review call: "thinking should also have an icon") - the lightbulb
// kind glyph at the transcript's one icon size, in the row's own quiet ink
// (the widget strokes currentColor in the row's ink). The live eyebrow seats
// it in the rail; the settled Disclosure summary leads with it in the content
// column, sized to the glyph alone.
const thoughtIcon = (
  <span className={CLASS.icon} data-testid="think-block-icon" aria-hidden="true">
    <ToolIcon kind="thought" />
  </span>
);
const settledThoughtIcon = (
  <span className={CLASS.summaryIcon} data-testid="think-block-icon" aria-hidden="true">
    <ToolIcon kind="thought" />
  </span>
);

const THOUGHT_PREVIEW_MAX_LENGTH = 120;

// ThoughtLabel never fabricates a duration: `durationMs` is undefined whenever
// neither the wire pair nor the observed pair is available or valid (see
// reasoningFormat.ts). Every part joins on the same " · " separator - the
// draft restyle's "Thought · 12s · preview" grammar (mockup #4), one delimiter
// instead of the old "Thought for 12s" phrase. The context is a bounded
// plain-text rendering of the final meaningful line, not a second Markdown
// body; opening the disclosure still reveals the complete source through
// Markdown.
// Each optional part rides in its own inline span carrying what it stands for
// (data-duration-ms, data-slot="preview"), so the words stay one line while a
// reader of the DOM can tell the measured duration and the preview apart.
function ThoughtLabel({ durationMs, preview }: { durationMs: number | undefined; preview: string }) {
  return (
    <>
      Thought
      {durationMs !== undefined && (
        <>
          {" · "}
          <span data-duration-ms={durationMs}>{formatThoughtDuration(durationMs)}</span>
        </>
      )}
      {preview && (
        <>
          {" · "}
          <span data-slot="preview">{preview}</span>
        </>
      )}
    </>
  );
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
              summaryIndex leaves no empty-paragraph gap.
              live: the stream is truncated by definition - open constructs
              at its tail (`**bo`, an unclosed fence) are closed for this
              render (see widgets/markdown/streaming.ts), so formatting is
              visible WHILE streaming instead of literal marker source. */}
          {paragraphs.length > 0 && <Markdown source={paragraphs.join("\n\n")} live />}
        </div>
      </div>
    </div>
  );
}

// The content-free counterpart to LiveThinkBlock: shown when the reasoning
// content flag is off, so the reader sees that the agent is thinking - through
// the catalog Loader's pulsing grid - without any of the thought's text. The
// brain glyph and body are deliberately absent; only the Loader and its label
// render.
//
// The token figure is an ESTIMATE. The wire carries no live token accounting
// for a reasoning stream (a turn's usage arrives only when the turn settles -
// see turnMeta.ts), so this is the conventional characters/4 heuristic over
// the same joined source the streaming body would have rendered, marked
// approximate with "~". No count renders until the first chunk lands, so a
// just-started thought shows the label alone rather than a fabricated "~0".
function ContentFreeThinkBlock({ item }: { item: ItemModel }) {
  const paragraphs = joinedReasoningParagraphs(item.reasoningSummaries);
  const characters = paragraphs.reduce((total, paragraph) => total + paragraph.length, 0);
  const estimatedTokens = Math.round(characters / 4);
  const label = estimatedTokens > 0 ? `Thinking… · ~${formatTokenCount(estimatedTokens)} tokens` : "Thinking…";
  return (
    <div
      className={CLASS.block}
      data-testid="think-block"
      data-live="true"
      data-content-free="true"
      data-estimated-tokens={estimatedTokens > 0 ? estimatedTokens : undefined}
    >
      {/* rail: the grid IS this row's status glyph, so it seats in the icon
       * rail the live and settled rows seat their bulb glyph in (the Loader
       * variant owns the slot geometry and the gutter pull - see
       * loader.module.css), keeping all three thinking states' icons on one
       * column while their labels all start at the content edge. */}
      <Loader label={label} rail />
    </div>
  );
}

// The redacted counterpart for a critical reasoning row while the reasoning
// content flag is off: a failed or interrupted turn still explains itself, but
// never by showing the thought. Only the icon and the projector's neutral
// summary render - no preview, no disclosure, and no body. The summary text is
// owned by the projector (criticalEntry), not re-derived here, so there is one
// source of truth for it.
function RedactedThinkBlock({ summary }: { summary: string }) {
  return (
    <div className={CLASS.block} data-testid="think-block" data-redacted="true">
      <span className={CLASS.label} data-testid="think-block-redacted">
        {thoughtIcon}
        {summary}
      </span>
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

export const ThinkBlock = memo(function ThinkBlock({
  item,
  turn,
  live,
  sessionRef,
  contentFree,
  redacted,
  projectedSummary,
}: ItemRenderProps) {
  const context = useTranscriptRenderContext();
  const { config } = context;
  const disclosureScope = disclosureScopeForSession(context, sessionRef);
  const disclosureKey = scopedDisclosureId(disclosureScope, item.id);
  const disclosureFallback = expandDetailsByDefault(config) || disclosureDefault(disclosureScope, item.id, false);
  // isDisclosureOpen IS a custom hook (it wraps zustand's useStore - see
  // disclosureStore's own note), so it must be called unconditionally before
  // any early return: an entry that toggles contentFree on a mounted row would
  // otherwise change the hook order between renders.
  const open = isDisclosureOpen(disclosureKey, disclosureFallback);
  // Neither content-free state RENDERS the thought's text. The placeholder
  // reads it only to estimate a length for its "~N tokens" label; the redacted
  // state renders the projector's neutral summary and nothing else.
  if (contentFree) return <ContentFreeThinkBlock item={item} />;
  if (redacted) return <RedactedThinkBlock summary={projectedSummary ?? "Thought not shown"} />;
  const isLive = (live || item.status === "inProgress") && isCurrentThought(item, turn);
  if (isLive) return <LiveThinkBlock item={item} />;

  const paragraphs = joinedReasoningParagraphs(item.reasoningSummaries);
  if (paragraphs.length === 0) return null; // empty thoughts removed

  const sections = segmentReasoningTrace(paragraphs);
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
  return (
    <div className={CLASS.block} data-testid="think-block" data-live="false">
      <Disclosure
        variant="quiet"
        id={disclosureKey}
        defaultOpen={disclosureFallback}
        summary={
          <>
            {settledThoughtIcon}
            {/* The label text in its own shrinkable span: it ellipsizes under
                pressure so the Disclosure's chevron stays on screen. */}
            <span className={CLASS.summaryText}>
              <ThoughtLabel durationMs={durationMs} preview={open ? "" : preview} />
            </span>
          </>
        }
      >
        <div className={CLASS.body}>
          {/* Beautiful UI's Thinking anatomy, ported honestly: a step only
              exists where the text itself already breaks into one via a
              markdown heading (see segmentReasoningTrace - gated on heading
              structure only, deliberately not paragraph breaks). A thought
              with no heading (the common case, a multi-paragraph thought
              with no structure of that kind) renders exactly as it always
              has - no rail, no divider, no panel - so the one-document
              render below stays the fallback, not a special case. */}
          {sections.length > 1 ? (
            <ol className={CLASS.trace} data-testid="think-block-trace">
              {sections.map((section, index) => (
                // biome-ignore lint/suspicious/noArrayIndexKey: sections are a static re-derivation of this settled item's fixed text, not a reorderable or independently-mutating list.
                <li key={index} className={CLASS.step}>
                  <span className={CLASS.stepMarker} aria-hidden="true">
                    {String(index + 1).padStart(2, "0")}
                  </span>
                  <div className={CLASS.stepBody}>
                    <Markdown source={section} />
                  </div>
                </li>
              ))}
            </ol>
          ) : (
            // One document, not one per summaryIndex: a markdown parser needs
            // the whole text to resolve block structure. Blank-line joined so
            // each index still starts its own block-level token rather than
            // being folded into the previous index's paragraph. The live and
            // settled branches use this same complete document; only the
            // live branch opts into streaming preview behavior. Markdown owns
            // its own paragraph/heading/list layout, so nothing re-wraps it in
            // .paragraph.
            <Markdown source={paragraphs.join("\n\n")} />
          )}
        </div>
      </Disclosure>
    </div>
  );
}, thinkBlockPropsEqual);

registerItemRenderer("reasoning", ThinkBlock);
