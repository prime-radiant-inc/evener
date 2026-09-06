import type { ConversationGoalActions } from "../../mobile/src/services/conversation";
import { composerCommand } from "./composerCommand";
import type { DraftDocument } from "./draftDocument";

/** The web composer treats attached messages as messages, never commands. */
export function goalObjective(text: string, imageCount = 0): string | null {
  const match = composerCommand(text, imageCount);
  return match?.command.id === "goal" ? match.argsText.trim() : null;
}

export async function submitGoalCommand(
  document: DraftDocument,
  service: ConversationGoalActions,
  clear = false,
): Promise<boolean> {
  const record = document.getSnapshot().record;
  const objective = clear
    ? ""
    : goalObjective(record.draft, record.images?.length);
  if (objective === null) return false;
  const apply = async () => {
    await service.setGoal(objective);
    return true;
  };
  if (clear) await document.submitText("/goal", apply);
  else await document.submit(apply);
  return true;
}
