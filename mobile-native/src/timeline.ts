import type { MobileTimelineItem } from "../../mobile/src/conversation/model";

type Notice = Extract<MobileTimelineItem, { kind: "notice" }>;

export function isInterruptedNotice(item: Notice): boolean {
  return (
    item.origin === "steering" &&
    item.steeringKind === "interrupted" &&
    item.tone !== "warning"
  );
}
export type TimelineRow =
  | MobileTimelineItem
  | {
      kind: "details";
      id: string;
      entries: Notice[];
    };

export function timelineGap(before: TimelineRow, after?: TimelineRow): number {
  if (!after) return 0;
  const needsAttention = (item: TimelineRow) =>
    item.kind === "failure" ||
    item.kind === "question" ||
    (item.kind === "notice" && item.tone === "warning") ||
    (item.kind === "activity" && item.state === "failed");
  if (needsAttention(before) || needsAttention(after)) return 24;
  const routine = (item: TimelineRow) =>
    item.kind === "details" ||
    (item.kind === "notice" && isInterruptedNotice(item));
  return routine(before) || routine(after) ? 8 : 24;
}

// Keep technical context available without putting it between the reader and the conversation.
export function groupTimeline(
  items: readonly MobileTimelineItem[],
): TimelineRow[] {
  const rows: TimelineRow[] = [];
  for (const item of items) {
    const internal =
      item.kind === "notice" &&
      item.tone !== "warning" &&
      (item.family === "hidden-instruction" ||
        item.family === "system-prelude" ||
        item.family === "diagnostic");
    if (!internal) {
      rows.push(item);
      continue;
    }
    const previous = rows.at(-1);
    if (previous?.kind === "details") previous.entries.push(item);
    else
      rows.push({ kind: "details", id: `details:${item.id}`, entries: [item] });
  }
  return rows;
}
