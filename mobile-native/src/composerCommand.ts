import { matchBuiltinInvocation } from "../../cmd/evener-hub/frontend/src/panes/session/composer/builtinInvocation";
import type {
  ConversationGoalActions,
  ConversationService,
} from "../../mobile/src/services/conversation";
import type { DraftDocument } from "./draftDocument";

const commands = [
  { id: "goal", args: true },
  { id: "compact" },
  { id: "shutdown" },
] as const;

/** Attachments follow ordinary message routing, as in the web composer. */
export function composerCommand(text: string, imageCount = 0) {
  return imageCount ? null : matchBuiltinInvocation(text, commands);
}

export async function submitComposerCommand(
  document: DraftDocument,
  service: Pick<ConversationService, "compact" | "shutdown"> &
    ConversationGoalActions,
): Promise<(typeof commands)[number]["id"] | null> {
  const record = document.getSnapshot().record;
  const match = composerCommand(record.draft, record.images?.length);
  if (!match) return null;
  let completed: typeof match.command.id | null = null;
  await document.submit(async () => {
    if (match.command.id === "goal")
      await service.setGoal(match.argsText.trim());
    else await service[match.command.id]();
    completed = match.command.id;
    return true;
  });
  return completed;
}
