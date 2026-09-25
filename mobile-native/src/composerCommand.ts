import {
  buildComposerInput,
  effortLabel,
  effortOptionLevels,
  findBuiltinArgument,
  matchBuiltinInvocation,
  mergeSlashCommands,
} from "@evener/appwire-client";
import type { ThreadClearResponse } from "@evener/appwire-client";
import type { MobileConversation } from "./projectedRows";
import { type ControlsSource, conversationControls } from "./conversationControls";
import type {
  ConversationClearActions,
  ConversationForkActions,
  ConversationGoalActions,
  ConversationModelCatalog,
  ConversationService,
} from "../../mobile/src/services/conversation";
import type { DraftDocument } from "./draftDocument";

const localCommands = [
  { id: "project", capability: null, label: "Show in project" },
  { id: "tasks", capability: null, label: "Tasks" },
  { id: "status", capability: null, label: "Status" },
  { id: "copy-id", capability: null, label: "Copy ID" },
] as const;
export type LocalComposerCommand = (typeof localCommands)[number]["id"];
export function isLocalComposerCommand(id: string): id is LocalComposerCommand {
  return localCommands.some((command) => command.id === id);
}

const commands = [
  ...localCommands,
  { id: "goal", args: true, capability: "goal", label: "Set goal" },
  { id: "compact", capability: "compact", label: "Compact" },
  { id: "shutdown", capability: "shutdown", label: "Shut down" },
  { id: "model", args: true, capability: "changeModel", label: "Set model" },
  { id: "reasoning-effort", args: true, capability: null, label: "Set effort" },
  // Stop is sessionControls' stop: an active status and the interrupt
  // capability (the hub advertises interrupt as harness support; the client
  // applies the status, so the rule is one and the transcript's turn id never
  // enters).
  { id: "interrupt", capability: "interrupt", control: "stop", label: "Interrupt" },
  // Steering commands read the session's controls (@evener/appwire-client's
  // sessionControls): the hub advertises steer as harness
  // support alone, so the status -- and, for a drain, the queue a Stop parked
  // -- is applied here, the same rule the web's composer and palette use.
  { id: "steer", args: true, capability: "steer", control: "steer", label: "Steer" },
  { id: "queue", args: true, capability: "queue", control: "queue", label: "Queue" },
  // Argless: it sends the queue and nothing else, so its control is drainQueue
  // (the drain rule plus a queue to drain).
  { id: "drain-as-steer", capability: "steer", control: "drainQueue", label: "Drain queue" },
  { id: "aside", capability: "forkFromTurn", label: "Aside" },
  { id: "clear", capability: "clear", label: "Clear" },
] as const;

/** What the completion registry and submission both read off the session. */
export type ComposerCommandSession = ControlsSource;

export type ComposerCommandSpec = (typeof commands)[number];

/** Whether a registry command may run against the session right now. */
export function composerCommandAvailable(
  command: ComposerCommandSpec,
  session: ComposerCommandSession,
): boolean {
  if ("control" in command) return conversationControls(session)[command.control];
  return (
    command.capability === null || !!session.capabilities[command.capability]
  );
}

/** Completion and submission share one supported-command registry. */
export function builtinComposerItems(session: ComposerCommandSession) {
  return mergeSlashCommands(
    commands
      .filter((command) => composerCommandAvailable(command, session))
      .map((command) => ({ id: command.id, hint: command.label })),
    [],
  );
}

export class CommandArgumentError extends Error {}

interface CommandContext {
  isCurrent(): boolean;
  local(command: LocalComposerCommand): Promise<void>;
  openAside(ref: string, title: string): void;
  cleared(response: ThreadClearResponse): void;
  turn(): ComposerCommandSession | null;
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
    ConversationForkActions &
    ConversationClearActions &
    ConversationModelCatalog,
  context: CommandContext,
): Promise<(typeof commands)[number]["id"] | null> {
  const snapshot = document.getSnapshot();
  const record = snapshot.record;
  if (
    !snapshot.loaded ||
    snapshot.error ||
    snapshot.submitting ||
    record.unconfirmed !== null
  )
    return null;
  const match = composerCommand(record.draft, record.images?.length);
  if (!match || !context.isCurrent()) return null;
  let operation: () => Promise<unknown>;
  let afterSubmit = () => {};
  const id = match.command.id;
  if (isLocalComposerCommand(id)) {
    await context.local(id);
    if (document.getSnapshot().record === record) document.edit("");
    return id;
  }
  // A command with a control (steer, queue, drain, stop) runs only when the
  // session's controls admit it: the same rule that offered it in completion,
  // applied at submission. The v3 mutations name no turn, so no turn id enters.
  const requireControl = (): void => {
    if (!("control" in match.command)) return;
    const turn = context.turn();
    const controls = turn ? conversationControls(turn) : null;
    const control = match.command.control;
    if (!controls || !controls[control])
      throw new CommandArgumentError(
        `/${id}: ${controls?.reason[control] ?? "no active turn"}`,
      );
  };
  const invalid = () =>
    new CommandArgumentError(
      match.argsText.trim()
        ? `/${id}: unknown value "${match.argsText.trim()}"`
        : `/${id} needs a value`,
    );
  if (id === "clear") {
    operation = async () => {
      const response = await service.clear();
      afterSubmit = () => context.cleared(response);
    };
  } else if (id === "aside") {
    operation = async () => {
      const { thread } = await service.forkAside();
      afterSubmit = () =>
        context.openAside(
          thread.evener.ref,
          thread.name || thread.preview || "Aside",
        );
    };
  } else if (id === "steer" || id === "queue" || id === "drain-as-steer") {
    requireControl();
    const turn = context.turn();
    // requireControl refused a missing session above; this narrows for the
    // revision read below.
    if (!turn) throw new CommandArgumentError(`/${id}: no active turn`);
    const input = buildComposerInput(match.argsText);
    // The explicit /steer command preserves waiting queue entries. Draining
    // uses its own command and the observed queue revision, as on web.
    operation =
      id === "drain-as-steer"
        ? () => service.steer([], turn.queue?.revision)
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
    requireControl();
    operation = () => service[id]();
  }
  let completed: typeof match.command.id | null = null;
  await document.submit(async () => {
    if (!context.isCurrent()) return false;
    await operation();
    completed = match.command.id;
    return true;
  });
  if (completed !== null && context.isCurrent()) afterSubmit();
  return completed;
}
