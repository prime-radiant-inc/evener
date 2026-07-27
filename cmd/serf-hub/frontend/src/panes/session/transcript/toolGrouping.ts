// Consecutive-run detection for commandExecution items within one turn -
// TurnBlock's clustering mechanic. Runs are derived fresh from the current
// item list on every render, so streaming additions and visibility filtering
// cannot leave stale membership behind. Like systemGrouping.ts, this is
// adjacency-only: any other item type forces a new run.
import type { ItemModel } from "../../../protocol/model";
import { toolCallFailed } from "./toolRenderers";

export interface ToolRun {
  items: ItemModel[];
  /** True only for the run's first member, which owns the cluster disclosure. */
  isFirst: boolean;
  /** True when the run reaches the last item in this turn's current list. */
  isLastActivity: boolean;
}

const MIN_GROUP_SIZE = 3;

function isToolCall(item: ItemModel): boolean {
  return item.type === "commandExecution";
}

// ask_user is deliberately a conversational boundary. It is not treated as
// a failure: the calls before and after it are separate grammatical runs, and
// the ask_user row remains an ordinary ToolCallItem.
function isAskUser(item: ItemModel): boolean {
  return isToolCall(item) && item.toolName === "ask_user";
}

// Failed calls stay visible as individual rows and break the eligible run on
// both sides. This rule is separate from isAskUser because a question is a
// boundary for conversational meaning, not a failure special case.
function joinsRun(item: ItemModel): boolean {
  return isToolCall(item) && !isAskUser(item) && !toolCallFailed(item);
}

// toolRunFor finds the contiguous eligible run containing itemId. A failed
// call returns a singleton so its renderer still owns and displays it;
// ask_user returns undefined so TurnBlock sends it directly to ToolCallItem.
export function toolRunFor(turnItems: ItemModel[], itemId: string): ToolRun | undefined {
  const index = turnItems.findIndex((item) => item.id === itemId);
  const item = turnItems[index];
  if (!item || !isToolCall(item) || isAskUser(item)) return undefined;

  if (toolCallFailed(item)) {
    return { items: [item], isFirst: true, isLastActivity: index === turnItems.length - 1 };
  }

  let start = index;
  while (start > 0) {
    const previous = turnItems[start - 1];
    if (!previous || !joinsRun(previous)) break;
    start -= 1;
  }

  let end = index;
  while (end < turnItems.length - 1) {
    const next = turnItems[end + 1];
    if (!next || !joinsRun(next)) break;
    end += 1;
  }

  return {
    items: turnItems.slice(start, end + 1),
    isFirst: start === index,
    isLastActivity: end === turnItems.length - 1,
  };
}

export function shouldGroup(run: ToolRun): boolean {
  return (
    run.items.length >= MIN_GROUP_SIZE &&
    !run.isLastActivity &&
    run.items.every((item) => item.status !== "inProgress" && !toolCallFailed(item))
  );
}
