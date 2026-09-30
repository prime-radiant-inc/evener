// What the session's housekeeping tools did, in both clients: the agent's
// note, the session notes, the link list, the goal, a context compaction, the
// model list, evener's own records, and a delegate's report to its parent.
// Each is told from the call's arguments and, where they can't say, from what
// the tool printed. The words speak of the agent in the third person ("its
// note"), as a reader watching it sees it.
//
// Ground truth: agent/session_tools_notes.go (notes_agent_set, notes_read,
// urls_add, urls_remove), session_tools_goal.go (update_goal, and "No goal is
// active…" when none was set), session_tools_compact.go (compact_context, and
// "Note cleared. No compaction requested." for an empty note),
// internal/tool/definitions.go (model_list's cursor, doctor_evener's command
// and selector), and the communicate result tool's end_turn.

import type { ItemModel } from "./model";
import type { StepWords } from "./stepWords";
import { parseArgs, str } from "./toolCallText";

/** The parts of a housekeeping step its words read. */
export type HousekeepingStep = Pick<ItemModel, "argumentsJSON" | "output">;

/** A housekeeping tool's words, done and while running. */
export interface HousekeepingWords {
  words: (step: HousekeepingStep) => StepWords;
  progress: (step: Pick<HousekeepingStep, "argumentsJSON">) => string;
}

const noteCleared = (step: Pick<HousekeepingStep, "argumentsJSON">) =>
  (str(parseArgs(step.argumentsJSON), "note") ?? "").trim() === "";

// A link is named by its label, else its URL.
const linkName = (step: Pick<HousekeepingStep, "argumentsJSON">) => {
  const args = parseArgs(step.argumentsJSON);
  return str(args, "label")?.trim() || str(args, "url")?.trim() || undefined;
};

// The goal's new status as the tool takes it: complete or blocked.
const goalStatus = (step: Pick<HousekeepingStep, "argumentsJSON">) => {
  const status = str(parseArgs(step.argumentsJSON), "status");
  return status === "complete" || status === "blocked" ? status : undefined;
};

const nextPage = (step: Pick<HousekeepingStep, "argumentsJSON">) =>
  Boolean(str(parseArgs(step.argumentsJSON), "cursor"));

// What each tool did, as a run's line says it ("updated its note once"): one
// phrase per tool, the same whatever the call's arguments. A step's line
// starts from the same phrase, capitalized, and says more where the call can.
const ACTIONS = {
  notes_agent_set: "updated its note",
  notes_read: "read the session notes",
  urls_add: "added a link",
  urls_remove: "removed a link",
  update_goal: "updated the goal",
  compact_context: "asked for a context compaction",
  model_list: "listed the available models",
  doctor_evener: "checked evener's records",
  communicate: "reported to its parent",
} as const;

const sentence = (phrase: string) => phrase.charAt(0).toUpperCase() + phrase.slice(1);

/** What a housekeeping tool did, as a run's line says it ("updated its
 * note"); undefined for any other tool. */
export function housekeepingAction(toolName: string): string | undefined {
  return Object.hasOwn(ACTIONS, toolName) ? ACTIONS[toolName as keyof typeof ACTIONS] : undefined;
}

/** Each housekeeping tool's words, by tool name. */
export const HOUSEKEEPING_WORDS: Record<keyof typeof ACTIONS, HousekeepingWords> = {
  notes_agent_set: {
    words: (step) => ({ verb: noteCleared(step) ? "Cleared its note" : sentence(ACTIONS.notes_agent_set) }),
    progress: (step) => (noteCleared(step) ? "Clearing its note" : "Updating its note"),
  },
  notes_read: {
    words: () => ({ verb: sentence(ACTIONS.notes_read) }),
    progress: () => "Reading the session notes",
  },
  urls_add: {
    words: (step) => {
      const name = linkName(step);
      return name ? { verb: "Added link", target: name } : { verb: sentence(ACTIONS.urls_add) };
    },
    progress: (step) => {
      const name = linkName(step);
      return name ? `Adding link ${name}` : "Adding a link";
    },
  },
  urls_remove: {
    words: () => ({ verb: sentence(ACTIONS.urls_remove) }),
    progress: () => "Removing a link",
  },
  update_goal: {
    words: (step) => {
      const status = goalStatus(step);
      const verb = status ? `Marked the goal ${status}` : sentence(ACTIONS.update_goal);
      return step.output?.startsWith("No goal is active") ? { verb, detail: "no goal set" } : { verb };
    },
    progress: (step) => {
      const status = goalStatus(step);
      return status ? `Marking the goal ${status}` : "Updating the goal";
    },
  },
  compact_context: {
    words: (step) => ({
      verb: step.output?.startsWith("Note cleared.")
        ? "Cleared its compaction note"
        : sentence(ACTIONS.compact_context),
    }),
    progress: () => "Asking for a context compaction",
  },
  model_list: {
    words: (step) => ({ verb: nextPage(step) ? "Listed more models" : sentence(ACTIONS.model_list) }),
    progress: (step) => (nextPage(step) ? "Listing more models" : "Listing the available models"),
  },
  doctor_evener: {
    words: (step) => {
      const args = parseArgs(step.argumentsJSON);
      const selector = str(args, "selector")?.trim();
      const command = str(args, "command")?.trim();
      return {
        verb: sentence(ACTIONS.doctor_evener),
        ...(selector ? { target: selector } : {}),
        ...(command ? { detail: command } : {}),
      };
    },
    progress: (step) => {
      const selector = str(parseArgs(step.argumentsJSON), "selector")?.trim();
      return selector ? `Checking evener's records ${selector}` : "Checking evener's records";
    },
  },
  communicate: {
    words: (step) =>
      parseArgs(step.argumentsJSON).end_turn === true
        ? { verb: sentence(ACTIONS.communicate), detail: "done" }
        : { verb: sentence(ACTIONS.communicate) },
    progress: () => "Reporting to its parent",
  },
};
