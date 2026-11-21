// The words a system notice or warning row shows, and where a Find hit lands.
// A run of 3+ notices folds into one group and only its leader renders, so
// every member's hit names the leader's anchor and opens the group. A
// scaffold's words sit behind its own disclosure. Find and the palette read
// every row through these rules, so a counted match is always words on screen.
import {
  attentionWarningNotice,
  entryDisplayKey,
  type ItemModel,
  isErrorEvent,
  isInformationalWarning,
  MEMORY_CONTEXT_EVENT_KIND,
  type ProjectedEntry,
  type ProjectedTurn,
  type TranscriptDisplayConfigV1,
  type TurnModel,
  type WarningWords,
  warningWords,
} from "@evener/appwire-client";
import { roundTimingsHeadline, roundTimingsSummary } from "../transcript/messages/roundTimingsView";
import {
  isCompactHookFailure,
  isRoundTimingsItem,
  isScaffoldItem,
  noticeText,
  renderSystemRunFor,
  shouldGroup,
} from "../transcript/messages/systemGrouping";
import { FAILURE_FALLBACK_LABEL, failureText } from "../transcript/turnFailure";
import { markdownAsRead } from "./messageText";

// A scaffold's Markdown is rendered once per diagram render state: a diagram
// can fail after the first read, putting its source on screen.
const scaffoldReads = new WeakMap<ItemModel, { version: number; text: string }>();

function scaffoldText(item: ItemModel, version: number): string {
  const cached = scaffoldReads.get(item);
  if (cached !== undefined && cached.version === version) return cached.text;
  const text = item.eventKind === "notes-context" ? item.text : markdownAsRead(item.text);
  scaffoldReads.set(item, { version, text });
  return text;
}

// WarningBlock paints its title (or the generic chip), the message and the
// hint; the informational quiet line paints the message and its on-screen
// (visually hidden) hint, with no title.
function warningLines(words: WarningWords, withTitle: boolean): readonly string[] {
  const lines: string[] = withTitle ? [words.title ?? "Warning"] : [];
  lines.push(words.message);
  if (words.hint !== undefined) lines.push(words.hint);
  return lines;
}

export function warningTexts(item: ItemModel): readonly string[] {
  const words = warningWords(item.text, item.warning?.title, item.warning?.hint);
  return words === null ? [] : warningLines(words, !isInformationalWarning(item));
}

/** The texts a systemMessage row shows, in the order SystemNoticeItem picks
 * them. Memory refreshes belong to their own slice; a failure, an attention
 * warning, a scaffold, a round-timings headline and a quiet line each read
 * exactly what their renderer paints. */
export function systemMessageTexts(
  item: ItemModel,
  turn: TurnModel | undefined,
  config: TranscriptDisplayConfigV1,
  version: number,
): readonly string[] {
  if (item.eventKind === MEMORY_CONTEXT_EVENT_KIND) return [];
  if (isErrorEvent(item) || isCompactHookFailure(item, config.advanced.hookExits)) {
    const text = turn === undefined ? undefined : failureText(item, turn);
    return [text ?? FAILURE_FALLBACK_LABEL];
  }
  const attention = attentionWarningNotice(item);
  if (attention !== null) return warningLines(attention, true);
  if (isScaffoldItem(item)) return [scaffoldText(item, version)];
  if (isRoundTimingsItem(item)) {
    const summary = roundTimingsSummary(item.raw);
    return [summary === undefined ? noticeText(item) : roundTimingsHeadline(summary)];
  }
  return [noticeText(item)];
}

/** Where a system notice's or warning's hit lands: a grouped run's leader key
 * and the disclosure that hides the words. Null for anything else, so the
 * caller keeps its own placement. */
export interface SystemNoticePlacement {
  readonly key?: string;
  readonly opens?: string;
  readonly opensIgnoreBaseline?: boolean;
}

export function systemNoticePlacement(
  entry: ProjectedEntry,
  turn: ProjectedTurn,
  config: TranscriptDisplayConfigV1,
): SystemNoticePlacement | null {
  const item = entry.item;
  if (item.type !== "systemMessage") return null;
  // Grouping reads the items TurnBlock actually renders (visibleItems), so a
  // hook the metadata hides cannot split a run its reader never sees.
  const run = renderSystemRunFor(turn.visibleItems, item.id, config.advanced);
  if (run !== undefined && shouldGroup(run)) {
    const leader = run.items[0];
    if (leader === undefined) return {};
    const leaderEntry = turn.entries.find(
      (candidate) => candidate.kind !== "intent" && candidate.item.id === leader.id,
    );
    return { key: leaderEntry === undefined ? leader.id : entryDisplayKey(leaderEntry), opens: leader.id };
  }
  if (isScaffoldItem(item)) {
    return { opens: item.id, ...(item.eventKind === "notes-context" ? { opensIgnoreBaseline: true } : {}) };
  }
  return {};
}
