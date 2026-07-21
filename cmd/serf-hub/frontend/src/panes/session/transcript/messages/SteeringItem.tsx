// The steering item renderer. Source-discriminated per parity issue #24
// (internal/appprojector/appwire_projection.go:588's own comment,
// internal/apptranscript/apptranscript.go:225-228): steering the human
// typed themselves (item.source === "user") is indistinguishable from a
// normal prompt and reuses UserMessageView verbatim; daemon-originated
// steering (no source, or anything else) renders as a quiet, collapsed-by-
// default divider instead - the reader never authored it, so it stays out
// of the way unless opened.
//
// Wave 5 T5 (sanctioned cross-wave edit): three of the legacy classifier's
// "kinds" (cmd/serf-hub/assets/renderer-format.js:415-493, classifySteering)
// are task-list bookkeeping the tasks panel now owns as its own surface
// (chrome/TasksPanel.tsx, model.tasks) - current-task/full-list/task-nudge
// render NOTHING here rather than a divider that would just duplicate the
// panel. Every other kind (tasks-done, loop detection, read-only nudges,
// transcript pointers, notifications, unknown) is a genuine one-off event
// the panel doesn't cover, so it keeps the existing divider unchanged.

import { memo } from "react";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { type ItemRenderProps, ignoringTurn, registerItemRenderer } from "../types";
import styles from "./steeringitem.module.css";
import { UserMessageView } from "./UserMessageItem";

const CLASS = {
  details: requireClass(styles.details, "steeringitem.module.css", "details"),
  summary: requireClass(styles.summary, "steeringitem.module.css", "summary"),
  body: requireClass(styles.body, "steeringitem.module.css", "body"),
};

// isTaskBookkeepingSteering ports just enough of the legacy classifySteering
// (renderer-format.js:415-493) to decide the binary suppress/keep question,
// in the SAME precedence order the legacy code checks them in (current-task,
// then full-list, then tasks-done, then task-nudge) - tasks-done's pattern
// is checked (and always kept) even though it doesn't change this function's
// own return value on its own, purely so it can correctly win over a
// following task-nudge check on the one hypothetical text that matched both
// (never actually happens - these are fixed daemon templates, agent/
// task_reminders.go - but this keeps the precedence honestly identical
// rather than assuming the overlap can't matter). Every other legacy kind
// (loop/read-only/transcript/notification/unknown) is irrelevant to this
// decision - they all fall through to "keep" here exactly as the default
// divider render already did before this function existed.
function isTaskBookkeepingSteering(text: string): boolean {
  const stripped = text.replace(/^\s*<SYSTEM-REMINDER>\s*/i, "").replace(/\s*<\/SYSTEM-REMINDER>\s*$/i, "");

  if (/<CURRENT-TASK\s+id="\d+">[\s\S]*?<\/CURRENT-TASK>/.test(stripped)) return true;
  if (/^Task list:/m.test(stripped)) return true;
  if (/completed all tasks/.test(stripped)) return false; // tasks-done: kept, and wins precedence over task-nudge below
  if (/task_list tool available/.test(stripped)) return true;
  return false;
}

// Memoized ignoring `turn` identity (types.ts's ignoringTurn): this
// component never reads `turn` at all (only `item`, destructured below), so
// a fresh turn object on every streaming delta targeting a DIFFERENT item
// must not re-render an already-settled steering row.
export const SteeringItem = memo(function SteeringItem({ item }: ItemRenderProps) {
  if (item.source === "user") return <UserMessageView item={item} />;
  if (!item.text) return null; // no text, no images path here (daemon steering never gets thumbnails - see below) - nothing to show
  if (isTaskBookkeepingSteering(item.text)) return null; // the tasks panel owns this surface now
  return (
    <details className={CLASS.details} data-testid="steering-item">
      <summary className={CLASS.summary}>Steering injected</summary>
      {/* Daemon-sourced steering images are never rendered as thumbnails -
       * only ever as a placeholder baked into item.text server-side
       * (internal/apptranscript/apptranscript.go's ImagePlaceholder call) -
       * so, unlike UserMessageView, there is no images branch here at all. */}
      <pre className={CLASS.body}>{item.text}</pre>
    </details>
  );
}, ignoringTurn);

registerItemRenderer("steering", SteeringItem);
