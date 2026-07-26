// Extracts a tool call's displayable duration from ItemModel's wire-stamped
// startedAt/completedAt (both ISO strings the reducer derives from the
// wire's epoch-ms StartedAt/CompletedAt - see internal/appprojector's
// EventToolCallEnd handler, issue #37: "server-truth timing for the hover
// meta"). Real server timestamps, never synthesized from the client's own
// wall clock - same standing rule reasoningFormat.ts's thoughtSeconds
// documents for the reasoning item's equivalent pair.
//
// Kept at millisecond precision (formatDurationMs already handles the
// ms-vs-s presentation split) rather than flooring to whole seconds the way
// thoughtSeconds does for reasoning: a tool call routinely completes in well
// under a second, where "1s" would erase exactly the outlier-spotting
// precision P1's finding is about (the study's own example was "38ms").
import type { ItemModel } from "../../../protocol/model";
import { formatDurationMs } from "./messages/format";

export function toolCallDuration(item: Pick<ItemModel, "startedAt" | "completedAt">): string | undefined {
  if (!item.startedAt || !item.completedAt) return undefined;
  const start = Date.parse(item.startedAt);
  const end = Date.parse(item.completedAt);
  if (!Number.isFinite(start) || !Number.isFinite(end) || end < start) return undefined;
  return formatDurationMs(end - start);
}
