// The words a daemon steer shows as, shared by the web and the phone.
//
// A steer the daemon sends (never one the human typed) is instructions to the
// agent, so a transcript folds it to a label that says what happened, opening
// to its text. The label is keyed by the wire's steering kind
// (events.SteeringKind* on the Go side, generated onto SteeringKind), never
// guessed from the text's prose.
//
// The table is exhaustive over the generated union, less the kinds that never
// show as a labelled steer: current-task and task-list, which the tasks
// surfaces own (SUPPRESSED below), and notification, which renders as its
// cards. Adding a kind in Go and regenerating fails the build here until it
// is given a label, so what the clients say cannot drift from what the
// daemon sends.

import type { SteeringKind } from "./types.gen";

type SuppressedSteeringKind = "current-task" | "task-list";

/** The daemon steer kinds that show as a labelled steer. */
export type LabelledSteeringKind = Exclude<SteeringKind, SuppressedSteeringKind | "notification">;

/** The label each shown daemon steer folds to. Plain words (spec 5): what the
 * steer did, not the engine's name for it. */
export const STEERING_KIND_LABELS: Readonly<Record<LabelledSteeringKind, string>> = {
  interrupted: "Interrupted",
  "interrupted-salvage": "Interrupted draft",
  "agent-message": "Message sent",
  "hook-context": "Hook context",
  // Context a hook added just before the history was compacted.
  "precompact-hook": "Hook context before compacting",
  // Asks the agent to compact, or to drop stale context, before an automatic
  // compaction does it.
  "compact-nudge": "Running low on context",
  "image-description": "Image description",
  // Sent after an empty reply or bare text: keep working, through a tool.
  "no-tool-calls": "Reminded to keep working",
  "loop-detected": "Loop detected",
  "tasks-done": "Tasks complete",
  "task-inactive": "Task list idle",
  "note-handoff": "Note to self",
  "goal-objective": "Goal objective",
  "human-note": "Human note",
  // Tells the agent how to read the transcript from before a compaction.
  "transcript-pointer": "Where to find the full transcript",
  "provider-failure": "Provider failed",
};

const SUPPRESSED: ReadonlySet<string> = new Set<SuppressedSteeringKind>(["current-task", "task-list"]);

/** The label for a steer's kind, or undefined for a kind this build doesn't
 * know (a daemon newer than the client), none at all, or one that never shows
 * as a labelled steer. The wire's kind is a plain string, so the lookup
 * tolerates a miss rather than inventing a label from a raw slug. */
function steeringKindLabel(kind: string | undefined): string | undefined {
  return kind !== undefined && Object.hasOwn(STEERING_KIND_LABELS, kind)
    ? STEERING_KIND_LABELS[kind as LabelledSteeringKind]
    : undefined;
}

// Every daemon steer says it is the system's before it says what it did
// (docs/web-ui/design-system.md: the steering glyph carries no accessible
// name, because the row's own words say "System steered").
const STEERED = "System steered";

/** The label a daemon steer folds to in both clients: "System steered:
 * <what it did>", or a bare "System steered" for a kind with no label, since
 * a colon promises a value. */
export function steeringLabel(kind: string | undefined): string {
  const label = steeringKindLabel(kind);
  return label === undefined ? STEERED : `${STEERED}: ${label}`;
}

/** True for the kinds a transcript leaves out: the current task and the task
 * list, which the tasks surfaces already show. */
export function isSuppressedSteeringKind(kind: string | undefined): boolean {
  return kind !== undefined && SUPPRESSED.has(kind);
}
