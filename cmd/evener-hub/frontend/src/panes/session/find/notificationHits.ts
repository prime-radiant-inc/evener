// Which words a steer's notification cards put on screen, and which of them
// only a card's raw disclosure holds. The card paints exactly these strings
// (NotificationCard reads the same helpers), so a counted match is always a
// word a reader can land on: the markup and attributes behind a frame are
// never handed out as text, and a delegate report - which renders no raw
// disclosure at all - is never searched by its verbatim frame (#6351).
import {
  type ItemModel,
  isParentSteer,
  isPlainObject,
  isSuppressedSteeringKind,
  type ParsedNotification,
  type SteeringFragment,
  steeringNotificationFragments,
} from "@evener/appwire-client";
import {
  boundedMessage,
  boundedValue,
  delegateCardLabel,
  excerptText,
  hiddenResultRows,
  shownResultRows,
  structuredResultNote,
  structuredRowLabel,
  structuredValueText,
} from "../transcript/messages/notificationText";
import { markdownAsRead } from "./messageText";

/** One card of a steer: its parse position (the delivery discriminator
 * SteeringItem gives the card), the words it paints, and - only for a card
 * that renders a raw disclosure - the frame those words hide. */
interface ShownNotificationCard {
  readonly notification: ParsedNotification;
  readonly index: number;
  readonly shown: readonly string[];
  readonly raw?: string;
}

// The steer's fragments, when its row renders cards at all: SteeringItem's own
// routing, branch for branch. A parent's steer is conversation, so quoted tags
// in it are text; a human note is labelled by its kind; a user steer is a
// prompt; a suppressed kind renders no row; and no text is no row.
const fragmentsCache = new WeakMap<ItemModel, { text: string; fragments: readonly SteeringFragment[] }>();

function fragmentsOf(item: ItemModel): readonly SteeringFragment[] | null {
  if (item.type !== "steering") return null;
  if (isParentSteer(item.source)) return null;
  if (item.steeringKind === "human-note") return null;
  if (item.source === "user") return null;
  if (!item.text) return null;
  if (isSuppressedSteeringKind(item.steeringKind)) return null;
  const text = item.text;
  const cached = fragmentsCache.get(item);
  if (cached !== undefined && cached.text === text) return cached.fragments;
  const fragments = steeringNotificationFragments(item.text);
  if (fragments === null) return null;
  fragmentsCache.set(item, { text, fragments });
  return fragments;
}

/** Whether this item's row renders notification cards at all. */
export function rendersNotificationCards(item: ItemModel): boolean {
  return fragmentsOf(item)?.some((fragment) => fragment.kind === "notification") ?? false;
}

/** The head: the title, the warning chip's word, and the label beside the
 * title - a delegate's name with the ending a failed or stopped run appends,
 * or the parser's own secondary composition. */
function headTexts(notification: ParsedNotification): string[] {
  const texts = [notification.title];
  if (notification.tone === "warning") texts.push("warning");
  const label = delegateCardLabel(notification);
  if (label !== undefined) {
    texts.push(label);
    if ((notification.tone === "error" || notification.tone === "warning") && notification.ending) {
      texts.push(notification.ending);
    }
  } else if (notification.secondary !== "") {
    texts.push(notification.secondary);
  }
  return texts;
}

/** The metadata fields a job or watch card shows, each as its label and value
 * read together. A delegate card shows none. */
function metadataTexts(notification: ParsedNotification): string[] {
  if (notification.type === "delegate") return [];
  const fields: readonly (readonly [string, string | number | undefined])[] =
    notification.type === "watch"
      ? [
          ["Watch id", notification.watchId],
          ["Job id", notification.jobId === "self" ? undefined : notification.jobId],
        ]
      : [
          ["Job id", notification.jobId],
          ["Watch id", notification.watchId],
          ["Status", notification.status],
          ["Job type", notification.jobType],
          ["Output", notification.outputBytes],
          ["Reason", notification.reason],
          ["Exit code", notification.exitCode],
        ];
  return fields.flatMap(([label, value]) => (value === undefined || value === "" ? [] : [`${label} ${value}`]));
}

/** The structured result as its table reads: each row's label and cell, the
 * quiet note a refused result renders instead, or a non-record result's value. */
function structuredTexts(notification: ParsedNotification): string[] {
  if (notification.structuredResultValid === false) return [structuredResultNote(notification.structuredResultReason)];
  const result = notification.structuredResult;
  if (result === undefined) return [];
  if (!isPlainObject(result)) return [structuredValueText(result)];
  const rows = Object.entries(result);
  if (rows.length === 0) return [];
  const texts = shownResultRows(rows).flatMap(([key, value]) => [
    boundedValue(structuredRowLabel(key)),
    structuredValueText(value),
  ]);
  const hidden = hiddenResultRows(rows.length);
  if (hidden > 0) texts.push(`(+${hidden} more rows)`);
  return texts;
}

/** The expanded card's body, block for block in the card's own order: the
 * metadata, the prose, the message or excerpt, the structured result, the
 * concerns. */
function bodyTexts(notification: ParsedNotification): string[] {
  return [
    ...metadataTexts(notification),
    ...(notification.prose ? [notification.prose] : []),
    notification.message
      ? markdownAsRead(boundedMessage(notification.message))
      : excerptText(notification.excerpt, notification.jobType === "shell"),
    ...structuredTexts(notification),
    ...(notification.concerns.length > 0 ? [`Concerns: ${notification.concerns.join("; ")}`] : []),
  ].filter((text) => text !== "");
}

// A card's painted words are immutable per item, but the Markdown inside a
// delegate report can change under a diagram that fails after the first read,
// so the diagram render state is part of the key.
const shownCache = new WeakMap<ItemModel, { text: string; version: number; cards: readonly ShownNotificationCard[] }>();

/** The steer's cards as their row renders them, or null when the item renders
 * no notification cards. `version` is the diagram render state the words are
 * read at. */
export function notificationCardsFor(item: ItemModel, version: number): readonly ShownNotificationCard[] | null {
  const fragments = fragmentsOf(item);
  if (fragments === null) return null;
  const text = item.text;
  const cached = shownCache.get(item);
  if (cached !== undefined && cached.version === version && cached.text === text) return cached.cards;
  const cards: ShownNotificationCard[] = [];
  fragments.forEach((fragment, index) => {
    if (fragment.kind !== "notification") return;
    const { notification } = fragment;
    cards.push({
      notification,
      index,
      shown: [...headTexts(notification), ...bodyTexts(notification)],
      // A job or watch card keeps its verbatim frame inspectable behind one
      // disclosure; a delegate report is fully extracted into the fields
      // above, so it renders no raw disclosure to search.
      ...(notification.type === "delegate" ? {} : { raw: notification.rawText }),
    });
  });
  shownCache.set(item, { text, version, cards });
  return cards;
}

/** Every word the steer's cards paint, for callers that only need text. */
export function notificationShownTexts(item: ItemModel, version: number): readonly string[] {
  const cards = notificationCardsFor(item, version);
  return cards === null ? [] : cards.flatMap((card) => card.shown);
}
