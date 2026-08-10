import type { ItemModel, TurnModel } from "../../protocol/model";

export type SessionViewMode = "everything" | "intent";

export const SESSION_VIEW_MODES = [
  { value: "everything", label: "Everything" },
  { value: "intent", label: "Intent" },
] as const satisfies readonly {
  value: SessionViewMode;
  label: "Everything" | "Intent";
}[];

export interface IntentLine {
  id: string;
  rationale: string;
}

export type FocusedEntry =
  | {
      kind: "message";
      id: string;
      turnId: string;
      sourceIndex: number;
      role: "user" | "agent";
      message: ItemModel;
    }
  | {
      kind: "action-group";
      id: string;
      turnId: string;
      sourceIndex: number;
      count: number;
      label: string;
      intents: IntentLine[];
    };

function messageRole(item: ItemModel): "user" | "agent" | undefined {
  if (item.type === "userMessage") return "user";
  if (item.type === "agentMessage") return "agent";
  return undefined;
}

function actionGroupLabel(count: number): string {
  return `${count} action${count === 1 ? "" : "s"}`;
}

export function focusedEntries(turns: readonly TurnModel[]): FocusedEntry[] {
  const entries: FocusedEntry[] = [];
  let groupFirstId: string | undefined;
  let groupLastId: string | undefined;
  let groupTurnId: string | undefined;
  let groupSourceIndex: number | undefined;
  let groupIntents: IntentLine[] = [];
  let sourceIndex = 0;

  const flushActionGroup = () => {
    if (
      groupIntents.length === 0 ||
      groupFirstId === undefined ||
      groupLastId === undefined ||
      groupTurnId === undefined ||
      groupSourceIndex === undefined
    )
      return;
    entries.push({
      kind: "action-group",
      id: `actions:${groupFirstId}:${groupLastId}`,
      turnId: groupTurnId,
      sourceIndex: groupSourceIndex,
      count: groupIntents.length,
      label: actionGroupLabel(groupIntents.length),
      intents: groupIntents,
    });
    groupFirstId = undefined;
    groupLastId = undefined;
    groupTurnId = undefined;
    groupSourceIndex = undefined;
    groupIntents = [];
  };

  for (const turn of turns) {
    for (const item of turn.items) {
      const itemSourceIndex = sourceIndex;
      sourceIndex += 1;
      if (item.type === "commandExecution") {
        const rationale = item.description?.trim();
        if (rationale) {
          groupFirstId ??= item.id;
          groupTurnId ??= turn.id;
          groupSourceIndex ??= itemSourceIndex;
          groupLastId = item.id;
          groupIntents.push({ id: `intent:${item.id}`, rationale });
        }
        continue;
      }

      const role = messageRole(item);
      if (role) {
        flushActionGroup();
        entries.push({
          kind: "message",
          id: item.id,
          turnId: turn.id,
          sourceIndex: itemSourceIndex,
          role,
          message: item,
        });
      }
    }
  }

  flushActionGroup();

  return entries;
}
