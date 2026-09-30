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
// "Note cleared. No compaction requested." when an empty note asks for no
// compaction; an empty note with compaction instructions still schedules one
// and prints "Note cleared. A compaction will run…"),
// internal/tool/definitions.go (model_list's cursor, doctor_evener's command
// and selector), and session_tools_communicate.go (communicate's end_turn).
// A session that renames its result tool (SessionConfig.ResultToolName) shows
// that tool's calls in the generic words, since only "communicate" is known
// here.

import type { ItemModel } from "./model";
import type { StepWords } from "./stepWords";
import { parseArgs, str } from "./toolCallText";

/** The parts of a housekeeping step its words read. */
export type HousekeepingStep = Pick<ItemModel, "argumentsJSON" | "output">;

type StepArgs = Pick<HousekeepingStep, "argumentsJSON">;

// A housekeeping tool's words. Its action is what it did, as a run's line
// says it ("updated its note once"): one phrase per tool, the same whatever
// the call's arguments. A step's line starts from the same phrase,
// capitalized (did), and says more where the call can.
interface HousekeepingTool {
  action: string;
  words: (step: HousekeepingStep, did: string) => StepWords;
  progress: (step: StepArgs) => string;
}

const noteCleared = (step: StepArgs) => (str(parseArgs(step.argumentsJSON), "note") ?? "").trim() === "";

// A link is named by its label, else its URL.
const linkName = (step: StepArgs) => {
  const args = parseArgs(step.argumentsJSON);
  return str(args, "label")?.trim() || str(args, "url")?.trim() || undefined;
};

// The goal's new status as the tool takes it: complete or blocked.
const goalStatus = (step: StepArgs) => {
  const status = str(parseArgs(step.argumentsJSON), "status");
  return status === "complete" || status === "blocked" ? status : undefined;
};

const nextPage = (step: StepArgs) => Boolean(str(parseArgs(step.argumentsJSON), "cursor"));

const selectorOf = (step: StepArgs) => str(parseArgs(step.argumentsJSON), "selector")?.trim();

const sentence = (phrase: string) => phrase.charAt(0).toUpperCase() + phrase.slice(1);

const HOUSEKEEPING: Record<string, HousekeepingTool> = {
  notes_agent_set: {
    action: "updated its note",
    words: (step, did) => ({ verb: noteCleared(step) ? "Cleared its note" : did }),
    progress: (step) => (noteCleared(step) ? "Clearing its note" : "Updating its note"),
  },
  notes_read: {
    action: "read the session notes",
    words: (_step, did) => ({ verb: did }),
    progress: () => "Reading the session notes",
  },
  urls_add: {
    action: "added a link",
    words: (step, did) => {
      const name = linkName(step);
      return name ? { verb: "Added link", target: name } : { verb: did };
    },
    progress: (step) => {
      const name = linkName(step);
      return name ? `Adding link ${name}` : "Adding a link";
    },
  },
  urls_remove: {
    action: "removed a link",
    words: (_step, did) => ({ verb: did }),
    progress: () => "Removing a link",
  },
  update_goal: {
    action: "updated the goal",
    words: (step, did) => {
      const status = goalStatus(step);
      const verb = status ? `Marked the goal ${status}` : did;
      return step.output?.startsWith("No goal is active") ? { verb, detail: "no goal set" } : { verb };
    },
    progress: (step) => {
      const status = goalStatus(step);
      return status ? `Marking the goal ${status}` : "Updating the goal";
    },
  },
  compact_context: {
    action: "asked for a context compaction",
    words: (step, did) => ({
      // The whole line: an empty note that still asks for a compaction also
      // starts "Note cleared."
      verb: step.output?.trim() === "Note cleared. No compaction requested." ? "Cleared its compaction note" : did,
    }),
    progress: () => "Asking for a context compaction",
  },
  model_list: {
    action: "listed the available models",
    words: (step, did) => ({ verb: nextPage(step) ? "Listed more models" : did }),
    progress: (step) => (nextPage(step) ? "Listing more models" : "Listing the available models"),
  },
  doctor_evener: {
    action: "checked evener's records",
    words: (step, did) => {
      const selector = selectorOf(step);
      const command = str(parseArgs(step.argumentsJSON), "command")?.trim();
      return {
        verb: did,
        ...(selector ? { target: selector } : {}),
        ...(command ? { detail: command } : {}),
      };
    },
    progress: (step) => {
      const selector = selectorOf(step);
      return selector ? `Checking evener's records ${selector}` : "Checking evener's records";
    },
  },
  communicate: {
    action: "reported to its parent",
    words: (step, did) =>
      parseArgs(step.argumentsJSON).end_turn === true ? { verb: did, detail: "done" } : { verb: did },
    progress: () => "Reporting to its parent",
  },
};

function housekeepingTool(toolName: string | undefined): HousekeepingTool | undefined {
  const name = toolName ?? "";
  return Object.hasOwn(HOUSEKEEPING, name) ? HOUSEKEEPING[name] : undefined;
}

/** What a housekeeping tool did, as a run's line says it ("updated its
 * note"); undefined for any other tool. */
export function housekeepingAction(toolName: string): string | undefined {
  return housekeepingTool(toolName)?.action;
}

/** A housekeeping step's words; undefined for any other tool. */
export function housekeepingWords(step: Pick<ItemModel, "toolName"> & HousekeepingStep): StepWords | undefined {
  const tool = housekeepingTool(step.toolName);
  return tool?.words(step, sentence(tool.action));
}

/** What a running housekeeping step is doing; undefined for any other tool. */
export function housekeepingProgress(step: Pick<ItemModel, "toolName" | "argumentsJSON">): string | undefined {
  return housekeepingTool(step.toolName)?.progress(step);
}
