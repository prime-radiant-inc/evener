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
import { capitalized, parseArgs, str } from "./toolCallText";

/** The parts of a housekeeping step its words read. */
export type HousekeepingStep = Pick<ItemModel, "argumentsJSON" | "output">;

type StepArgs = Pick<HousekeepingStep, "argumentsJSON">;

// A housekeeping tool's words. Its action is what it did, as a run's line
// says it ("updated its note once"), and actionFor names the one other thing
// a call can do instead (clear a note). A step's line starts from the same
// phrase, capitalized (did), and says more where the call can, so a step and
// its run's line never disagree.
interface HousekeepingTool {
  action: string;
  actionFor?: (step: HousekeepingStep) => string | undefined;
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

// Whether a compact_context call only clears its note, as the tool decides it
// before it prints anything: an empty note, no compaction instructions, and no
// reload_skills selection (absent or null; any other value, an empty array
// included, asks for a compaction).
const onlyClearsNote = (step: Pick<HousekeepingStep, "argumentsJSON">) => {
  const args = parseArgs(step.argumentsJSON);
  return (
    (str(args, "note_to_self") ?? "") === "" &&
    (str(args, "compaction_instructions") ?? "") === "" &&
    (args.reload_skills ?? null) === null
  );
};

// Whether a compact_context call only cleared its note: what the tool
// printed (the start of it: a repeated call gets the registry's repetition
// note after), else, before it has printed, what its arguments ask for. An
// empty note that still asks for a compaction also starts "Note cleared.",
// so the whole first sentence is matched. A step with neither (a
// summary-only row) can't say, and reads as the tool's usual action.
const clearedCompactionNote = (step: HousekeepingStep) => {
  const output = step.output?.trim();
  if (output) return output.startsWith("Note cleared. No compaction requested.");
  return step.argumentsJSON !== undefined && onlyClearsNote(step);
};

const nextPage = (step: StepArgs) => Boolean(str(parseArgs(step.argumentsJSON), "cursor"));

const selectorOf = (step: StepArgs) => str(parseArgs(step.argumentsJSON), "selector")?.trim();

const HOUSEKEEPING: Record<string, HousekeepingTool> = {
  notes_agent_set: {
    action: "updated its note",
    // Only arguments that are here can say the note was cleared: a step
    // without them (a summary-only row) says what the tool usually does.
    actionFor: (step) => (step.argumentsJSON !== undefined && noteCleared(step) ? "cleared its note" : undefined),
    words: (_step, did) => ({ verb: did }),
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
    actionFor: (step) => (clearedCompactionNote(step) ? "cleared its compaction note" : undefined),
    words: (_step, did) => ({ verb: did }),
    progress: (step) => (onlyClearsNote(step) ? "Clearing its compaction note" : "Asking for a context compaction"),
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

// What this call of tool did, in a run's words.
function actionOf(tool: HousekeepingTool, step: HousekeepingStep | undefined): string {
  return (step && tool.actionFor?.(step)) ?? tool.action;
}

/** What a housekeeping call did, as a run's line says it ("updated its
 * note", or "cleared its note" for a call that cleared it); the tool's usual
 * phrase without a step. Undefined for any other tool. */
export function housekeepingAction(toolName: string, step?: HousekeepingStep): string | undefined {
  const tool = housekeepingTool(toolName);
  return tool ? actionOf(tool, step) : undefined;
}

/** A housekeeping step's words; undefined for any other tool. */
export function housekeepingWords(step: Pick<ItemModel, "toolName"> & HousekeepingStep): StepWords | undefined {
  const tool = housekeepingTool(step.toolName);
  return tool?.words(step, capitalized(actionOf(tool, step)));
}

/** What a running housekeeping step is doing; undefined for any other tool. */
export function housekeepingProgress(step: Pick<ItemModel, "toolName" | "argumentsJSON">): string | undefined {
  return housekeepingTool(step.toolName)?.progress(step);
}
