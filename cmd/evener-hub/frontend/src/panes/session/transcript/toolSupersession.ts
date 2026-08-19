// toolSupersession.ts answers one narrow question for ToolCallItem's
// autoDefault decision (kata hgm1): given a failed tool-call item, was it
// superseded by a later, successful call to the SAME tool?
//
// This exists because of a gap in the shared "only failure earns the eye"
// contract (ToolCallItem.tsx: every failed/denied row starts forced-open,
// forever, no exceptions) - h70z's own panel converged that a
// self-corrected pre-dispatch validation bounce (the daemon rejected a
// malformed call before the tool's real execution ever ran, then the
// model's very next call to the same tool succeeded) reads as a live
// failure forever, right next to the exchange it never actually blocked.
// Three independent personas (auditor / resuming developer / new teammate)
// converged: demote it to collapsed-by-default, but keep it inspectable,
// UNLESS the pattern recurs or nothing ever corrected it.
//
// The rule this implements is general, not ask_user-specific, and touches
// nothing about a REAL execution failure or denial (shell, apply_patch,
// anything) - those keep the existing forced-open contract exactly as
// before. Two facts, both required, before a failed row demotes:
//   1. item.prevalOnly is true - a WIRE fact (agent/session_tool_repair.go's
//      PrevalErr, threaded through events.ToolCallEndData.PrevalOnly): this
//      call never reached the tool's real execution at all. A real
//      execution failure is never prevalOnly, so it never qualifies.
//   2. The NEXT item for the same toolName, later in the thread, succeeded
//      (no error). Only the failure immediately preceding a success
//      demotes - if the model fails twice before finally succeeding, the
//      first failure's "next" is the second failure, so it stays open,
//      matching every persona's "if it recurs, keep it visible" caveat.
import type { ItemModel, ThreadModel } from "../../../protocol/model";

/** True when `item` failed via a pre-dispatch bounce (never reached real
 * execution) AND the next later item for the same tool, anywhere in the
 * thread, succeeded - i.e. the model corrected itself on its very next
 * attempt. False for a real execution failure/denial regardless of what
 * follows it, and false when nothing later ever corrected it. */
export function supersededBySuccess(item: ItemModel, thread: ThreadModel | undefined): boolean {
  if (!item.prevalOnly || !item.toolName || thread === undefined) return false;

  let pastThisItem = false;
  for (const turn of thread.turns) {
    for (const other of turn.items) {
      if (!pastThisItem) {
        if (other.id === item.id) pastThisItem = true;
        continue;
      }
      if (other.toolName !== item.toolName) continue;
      // The first later same-tool item settles the question either way -
      // stop at it rather than scanning past it to some more distant retry.
      return other.error === undefined || other.error === "";
    }
  }
  return false;
}
