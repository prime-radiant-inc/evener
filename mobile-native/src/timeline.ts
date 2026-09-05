import type { MobileTimelineItem } from "../../mobile/src/conversation/model";

type Notice = Extract<MobileTimelineItem, { kind: "notice" }>;
export type TimelineRow =
  | MobileTimelineItem
  | {
      kind: "details";
      id: string;
      entries: Notice[];
    };

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
