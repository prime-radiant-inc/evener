import {
  findBuiltinArgument,
  matchBuiltinInvocation,
} from "../../cmd/evener-hub/frontend/src/panes/session/composer/builtinInvocation";
import {
  effortLabel,
  effortOptionLevels,
} from "../../cmd/evener-hub/frontend/src/shell/reasoningEffort";
import { buildComposerInput } from "../../cmd/evener-hub/frontend/src/stores/composerInput";
import type { MobileConversation } from "../../mobile/src/conversation/model";
import type {
  ConversationGoalActions,
  ConversationModelCatalog,
  ConversationService,
} from "../../mobile/src/services/conversation";
import type { DraftDocument } from "./draftDocument";

const commands = [
  { id: "goal", args: true, capability: "goal", label: "Set goal" },
  { id: "compact", capability: "compact", label: "Compact" },
  { id: "shutdown", capability: "shutdown", label: "Shut down" },
  { id: "model", args: true, capability: "changeModel", label: "Set model" },
  { id: "reasoning-effort", args: true, capability: null, label: "Set effort" },
  { id: "interrupt", capability: "interrupt", label: "Interrupt" },
  { id: "steer", args: true, capability: "steer", label: "Steer" },
  { id: "queue", args: true, capability: "queue", label: "Queue" },
  { id: "drain-as-steer", capability: "steer", label: "Drain queue" },
] as const;

export class CommandArgumentError extends Error {}

interface CommandContext {
  isCurrent(): boolean;
  turn(): { activeTurnId?: string; queue: { revision: number } } | null;
  reasoning(): Pick<
    MobileConversation,
    "supportsReasoning" | "reasoningEffort" | "reasoningEffortLevels"
  > | null;
}

/** Attachments follow ordinary message routing, as in the web composer. */
export function composerCommand(text: string, imageCount = 0) {
  return imageCount ? null : matchBuiltinInvocation(text, commands);
}

export async function submitComposerCommand(
  document: DraftDocument,
  service: Pick<
    ConversationService,
    | "compact"
    | "shutdown"
    | "changeModel"
    | "setReasoningEffort"
    | "steer"
    | "queue"
    | "interrupt"
  > &
    ConversationGoalActions &
    ConversationModelCatalog,
  context: CommandContext,
): Promise<(typeof commands)[number]["id"] | null> {
  const record = document.getSnapshot().record;
  const match = composerCommand(record.draft, record.images?.length);
  if (!match || !context.isCurrent()) return null;
  let operation: () => Promise<unknown>;
  const id = match.command.id;
  const invalid = () =>
    new CommandArgumentError(
      match.argsText.trim()
        ? `/${id}: unknown value "${match.argsText.trim()}"`
        : `/${id} needs a value`,
    );
  if (id === "steer" || id === "queue" || id === "drain-as-steer") {
    const turn = context.turn();
    if (!turn?.activeTurnId)
      throw new CommandArgumentError(`/${id}: no active turn`);
    const input = buildComposerInput(match.argsText);
    // The explicit /steer command preserves waiting queue entries. Draining
    // uses its own command and the observed queue revision, as on web.
    operation =
      id === "drain-as-steer"
        ? () => service.steer([], turn.queue.revision)
        : () => service[id](input);
  } else if (id === "model") {
    const catalog = await service.models();
    // A catalog request must never consume text edited while it was loading,
    // or mutate the session after its screen loses ownership.
    if (!context.isCurrent() || document.getSnapshot().record !== record)
      return null;
    const model = findBuiltinArgument(
      (catalog.data ?? []).map((item) => ({
        ...item,
        id: `${item.provider}/${item.model}`,
        label: item.displayName || item.model,
      })),
      match.argsText,
    );
    if (!model) throw invalid();
    operation = () => service.changeModel(model.provider, model.model);
  } else if (id === "reasoning-effort") {
    const current = context.reasoning();
    const levels = current?.supportsReasoning
      ? (current.reasoningEffortLevels ?? [])
      : [];
    const effort = findBuiltinArgument(
      levels.length
        ? effortOptionLevels(levels, current?.reasoningEffort ?? "").map(
            (level) => ({
              id: level,
              label: effortLabel(level, levels),
            }),
          )
        : [],
      match.argsText,
    );
    if (!effort) throw invalid();
    operation = () => service.setReasoningEffort(effort.id);
  } else if (id === "goal") {
    operation = () => service.setGoal(match.argsText.trim());
  } else {
    operation = () => service[id]();
  }
  let completed: typeof match.command.id | null = null;
  await document.submit(async () => {
    if (!context.isCurrent()) return false;
    await operation();
    completed = match.command.id;
    return true;
  });
  return completed;
}
