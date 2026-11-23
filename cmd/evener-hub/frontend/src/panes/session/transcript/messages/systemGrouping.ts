// Classification and consecutive-run detection for systemMessage items within
// one turn - SystemNoticeItem.tsx's grouping mechanic, shared with Find and
// the palette. Grouping is derived fresh from turn.items on every render (no
// persisted run state of its own), so it's naturally correct as items stream
// in - mirrors legacy's ensureSystemRun/coalesceSystemRun "adjacency-only" run
// continuation (parity-m4-transcript.md #9: any other item type in between
// forces a new run) without needing a stateful accumulator.
import {
  APPROVAL_DECISION_EVENT_KIND,
  attentionWarningNotice,
  type ItemModel,
  isErrorEvent,
  isQuietYield,
  MEMORY_CONTEXT_EVENT_KIND,
  systemEventWords,
  type ThreadItemEventKind,
  type TranscriptMetadataVisibility,
} from "@evener/appwire-client";
import { SYSTEM_PROMPT_ITEM_ID } from "../transcriptVisibility";

export interface SystemRun {
  items: ItemModel[];
  /** True only for the run's first member - the one that renders the
   * group's disclosure (or, for a sub-threshold run, is simply the first
   * of several standalone lines). Every other member renders nothing of
   * its own once grouped - see SystemNoticeItem. */
  isFirst: boolean;
}

// Below this many adjacent systemMessage items, each renders as its own
// standalone quiet line - no grouping chrome. Parity: contracts-transcript-
// scroll-liveness.md #12 / test-system-churn.js ("fewer than 3 adjacent
// lifecycle events do not coalesce").
const MIN_GROUP_SIZE = 3;

// ItemModel.eventKind is a plain string; typing the constant keeps the
// comparison in joinsRun checked against the wire's kinds.
const INTERRUPTED_EVENT_KIND: ThreadItemEventKind = "interrupted";

// The systemMessage eventKinds whose text is a scaffolding block a reader
// collapses by default rather than a quiet one-liner: the session system
// prompt, a compaction summary/checkpoint, and a shared-notes snapshot.
// Classification is by this typed wire field, never by char count.
export const SCAFFOLD_EVENT_KINDS: ReadonlySet<string> = new Set(["system_prompt", "compaction", "notes-context"]);

export function isScaffoldItem(item: ItemModel): boolean {
  if (item.eventKind !== undefined && item.eventKind !== "") return SCAFFOLD_EVENT_KINDS.has(item.eventKind);
  return item.id === SYSTEM_PROMPT_ITEM_ID;
}

export const ROUND_TIMINGS_EVENT_KIND: ThreadItemEventKind = "round_timings";

export function isRoundTimingsItem(item: ItemModel): boolean {
  return item.eventKind === ROUND_TIMINGS_EVENT_KIND;
}

// FALLBACK_LABEL covers a systemMessage item with no text at all (e.g. a
// plugin-loaded event with no resolved plugin name) - a category label beats
// an invisible row without fabricating specifics the wire didn't provide.
export const FALLBACK_LABEL = "System event";

// What a quiet line says: the package's systemEventWords, which the phone
// reads too. A plugin load carries its summary in the description and no
// text, and a compaction pass's text is the engine's "Layer / Turns / ..."
// report, so both read their structured raw there.
export function noticeText(item: ItemModel): string {
  return systemEventWords(item).text || FALLBACK_LABEL;
}

// A compact hook failure is a failed hook the metadata detail leaves on
// screen: at "all" every exit is routine (a notice), otherwise a nonzero exit
// is the row a reader hunts for and gets the failure treatment.
export function isCompactHookFailure(item: ItemModel, hookExits: TranscriptMetadataVisibility["hookExits"]): boolean {
  return (
    item.eventKind === "hook_completed" &&
    hookExits !== "all" &&
    typeof item.exitCode === "number" &&
    item.exitCode !== 0
  );
}

// A persisted turn failure arrives as a systemMessage item carrying the wire's
// typed "error" eventKind (appwire.ThreadItemEventKindError; the package's
// isErrorEvent). It is the one system item that is not lifecycle churn, and
// the one readers actively hunt for, so it is classified off that typed field
// rather than by reading English out of the message - the same rule the
// scaffold and hook-exit kinds follow.

function isSystemMessage(item: ItemModel): boolean {
  return item.type === "systemMessage";
}

// A failure never joins a run. Grouping exists to keep a burst of lifecycle
// churn from reading as a wall of dividers; folding a failure into one buys a
// row of quiet at the cost of hiding the row a reader came for - behind a
// summary that names the run's FIRST member, which need not be the failure at
// all. So a failure both stays out of its neighbours' run and breaks it, and
// so do a daemon warning a human should see (attentionWarningNotice) and an
// interrupted notice (a model round whose output or tool calls were never
// recorded), which stays its own line as on the phone. An informational
// warning still folds. Shared-notes snapshots stay separate under their own
// folded label. A human's Allow or Deny is a decision, not churn, so it stays
// in view too. A memory refresh is a standalone notification (its own
// collapsed "Refreshed my memory" disclosure), so it neither joins a run nor
// lets one straddle it.
function joinsRun(item: ItemModel): boolean {
  return (
    isSystemMessage(item) &&
    item.eventKind !== "notes-context" &&
    item.eventKind !== MEMORY_CONTEXT_EVENT_KIND &&
    item.eventKind !== APPROVAL_DECISION_EVENT_KIND &&
    !isQuietYield(item) &&
    item.eventKind !== INTERRUPTED_EVENT_KIND &&
    !isErrorEvent(item) &&
    attentionWarningNotice(item) === null
  );
}

// systemRunFor finds the contiguous run of systemMessage items in
// `turnItems` that contains `itemId`. Returns undefined when the item
// itself isn't a systemMessage (or isn't found at all) - callers only ever
// invoke this for an item they've already dispatched as "systemMessage",
// so that's a defensive case, not an expected one.
export function systemRunFor(turnItems: readonly ItemModel[], itemId: string): SystemRun | undefined {
  const index = turnItems.findIndex((it) => it.id === itemId);
  const item = turnItems[index];
  if (!item || !isSystemMessage(item)) return undefined;
  // A run of one, always first, so a failure or a daemon warning always renders
  // itself. Returning undefined here would be the opposite of the point:
  // SystemNoticeItem reads that as "not mine" and renders nothing at all.
  if (!joinsRun(item)) return { items: [item], isFirst: true };

  let start = index;
  while (start > 0) {
    const prev = turnItems[start - 1];
    if (!prev || !joinsRun(prev)) break;
    start--;
  }
  let end = index;
  while (end < turnItems.length - 1) {
    const next = turnItems[end + 1];
    if (!next || !joinsRun(next)) break;
    end++;
  }

  return { items: turnItems.slice(start, end + 1), isFirst: start === index };
}

export function shouldGroup(run: SystemRun): boolean {
  return run.items.length >= MIN_GROUP_SIZE;
}

// The run as SystemNoticeItem renders it: a compact hook failure never joins
// its neighbours' group, and breaks the run around itself, so a group never
// hides the failure behind a summary naming a different item.
export function renderSystemRunFor(
  turnItems: readonly ItemModel[],
  itemId: string,
  metadata: TranscriptMetadataVisibility,
): SystemRun | undefined {
  const run = systemRunFor(turnItems, itemId);
  if (!run) return undefined;
  const index = run.items.findIndex((candidate) => candidate.id === itemId);
  if (index < 0) return undefined;
  const current = run.items[index];
  if (!current) return undefined;

  const isCritical = (candidate: ItemModel) => isCompactHookFailure(candidate, metadata.hookExits);
  if (!run.items.some(isCritical)) return run;
  if (isCritical(current)) return { items: [current], isFirst: true };

  let start = index;
  while (start > 0) {
    const previous = run.items[start - 1];
    if (!previous || isCritical(previous)) break;
    start -= 1;
  }
  let end = index;
  while (end < run.items.length - 1) {
    const next = run.items[end + 1];
    if (!next || isCritical(next)) break;
    end += 1;
  }
  return { items: run.items.slice(start, end + 1), isFirst: start === index };
}
