import type { ItemModel, TurnModel } from "../../protocol/model";

export type SessionViewMode = "everything" | "conversation" | "intent";

export const SESSION_VIEW_MODES = [
  { value: "everything", label: "Everything" },
  { value: "conversation", label: "Conversation" },
  { value: "intent", label: "Intent" },
] as const satisfies readonly {
  value: SessionViewMode;
  label: "Everything" | "Conversation" | "Intent";
}[];

export type FocusedEntry =
  | {
      kind: "message";
      id: string;
      turnId: string;
      role: "user" | "agent";
      message: ItemModel;
    }
  | {
      kind: "tool-count";
      id: string;
      turnId: string;
      count: number;
      label: string;
    }
  | {
      kind: "intent";
      id: string;
      turnId: string;
      rationale: string;
    };

type FocusedViewMode = Exclude<SessionViewMode, "everything">;

function messageRole(item: ItemModel): "user" | "agent" | undefined {
  if (item.type === "userMessage") return "user";
  if (item.type === "agentMessage") return "agent";
  return undefined;
}

function toolCountLabel(count: number): string {
  return `${count} tool call${count === 1 ? "" : "s"}`;
}

export function focusedEntries(turns: readonly TurnModel[], mode: FocusedViewMode): FocusedEntry[] {
  const entries: FocusedEntry[] = [];

  for (const turn of turns) {
    let firstToolId: string | undefined;
    let lastToolId: string | undefined;
    let toolCount = 0;

    const flushToolCount = () => {
      if (toolCount === 0 || firstToolId === undefined || lastToolId === undefined) return;
      entries.push({
        kind: "tool-count",
        id: `tools:${firstToolId}:${lastToolId}`,
        turnId: turn.id,
        count: toolCount,
        label: toolCountLabel(toolCount),
      });
      firstToolId = undefined;
      lastToolId = undefined;
      toolCount = 0;
    };

    for (const item of turn.items) {
      if (item.type === "commandExecution") {
        if (mode === "conversation") {
          firstToolId ??= item.id;
          lastToolId = item.id;
          toolCount += 1;
        } else {
          const rationale = item.description?.trim();
          if (rationale) {
            entries.push({
              kind: "intent",
              id: `intent:${item.id}`,
              turnId: turn.id,
              rationale,
            });
          }
        }
        continue;
      }

      if (mode === "conversation") flushToolCount();

      const role = messageRole(item);
      if (role) {
        entries.push({
          kind: "message",
          id: item.id,
          turnId: turn.id,
          role,
          message: item,
        });
      }
    }

    if (mode === "conversation") flushToolCount();
  }

  return entries;
}
