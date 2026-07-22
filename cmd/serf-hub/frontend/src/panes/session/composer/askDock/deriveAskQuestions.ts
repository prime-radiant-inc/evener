// liveAskQuestions is the pure, position-based half of the dock's pending-
// question bookkeeping: a snapshot of "which ask_user questions are
// currently unanswered", derived fresh from the ThreadModel on every call -
// no memory of its own. It mirrors legacy's cold-attach reconstruction rule
// (parity-m5-composer.md §C: "a completed-but-unanswered ask gets both
// anchor and dock; an ask followed by a reply gets only the settled line"):
// scan every item in transcript order, and anything asked-and-acked AFTER
// the most recent plain user message is still live; anything before it was
// already answered (a reply resolves the WHOLE pending set at once, spec
// §6.1).
//
// This alone is not sufficient for the live, in-flight-submission case -
// see reconcileBatches.ts's own comment for why a purely positional signal
// can't tell "my own reply's echo" apart from "a sibling ask_user call that
// happened to land first" during the network round-trip of sending an
// answer. reconcileBatches layers stateful batch-membership on top of the
// list this function returns; this function's only job is "what does the
// transcript say is live right now, ignoring any in-flight request."
import type { ItemModel, ThreadModel } from "../../../../protocol/model";
import type { AskUserOption } from "../../askShared";
import { parseAskUserQuestions } from "../../askShared";

// AskQuestionRef is one flattened, individually-addressable question -
// mirrors legacy's pendingAsk item shape (renderer.js:5832-5844). `key` is
// stable and re-derivable (callId:idx within that call's own questions
// array), never a locally-minted id - the same call posted twice (e.g.
// after a reconnect replay) always re-derives the same keys.
export interface AskQuestionRef {
  key: string;
  callId: string;
  header: string;
  question: string;
  options: AskUserOption[];
  multiSelect: boolean;
  why?: string;
  ifUnanswered?: string;
}

function isAckedAskUserItem(item: ItemModel): boolean {
  return item.type === "commandExecution" && item.toolName === "ask_user" && item.status === "completed";
}

// lastUserMessageIndex finds the position of the most recent plain user
// message (type "userMessage" - the wire's literal string for both a plain
// composer send and an ask-dock's own composed [answers] reply; either one
// is a valid resolution, spec §6.1) in transcript order. -1 when none
// exists yet (a fresh thread, or one that has never had a user turn).
// Written as forEach-over-index rather than a reverse indexed loop so
// `noUncheckedIndexedAccess` never needs an unnecessary bounds guard.
function lastUserMessageIndex(items: readonly ItemModel[]): number {
  let last = -1;
  items.forEach((item, i) => {
    if (item.type === "userMessage") last = i;
  });
  return last;
}

export function liveAskQuestions(model: ThreadModel): AskQuestionRef[] {
  const items = model.turns.flatMap((turn) => turn.items);
  const boundary = lastUserMessageIndex(items);
  const refs: AskQuestionRef[] = [];
  items.slice(boundary + 1).forEach((item) => {
    if (!isAckedAskUserItem(item)) return;
    const questions = parseAskUserQuestions(item);
    if (!questions) return;
    // callId should always be present for a real ask_user call (set at
    // TOOL_CALL_START); item.id is a defensive fallback so a malformed/
    // synthetic item still gets a stable, non-colliding-with-nothing key
    // rather than the parser throwing or the key going missing.
    const callId = item.callId ?? item.id;
    questions.forEach((q, idx) => {
      refs.push({
        key: `${callId}:${idx}`,
        callId,
        header: q.header,
        question: q.question,
        options: q.options,
        multiSelect: q.multiSelect === true,
        why: q.why,
        ifUnanswered: q.ifUnanswered,
      });
    });
  });
  return refs;
}
