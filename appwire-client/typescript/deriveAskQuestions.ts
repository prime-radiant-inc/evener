// liveAskQuestions answers two different questions, and gets each from a
// different place. "Is anything pending right now" is the server's own
// fact, not the client's to re-derive: ThreadModel.askPending rides the wire
// on hydrate (appwire.EvenerThread.AskPending) and on every thread/status/
// changed notification that can have moved it (appwire.ThreadStatusChanged.
// AskPending), so the client reads it, it does not reconstruct it from
// transcript shape. Earlier rounds of this file tried to re-derive that
// boolean from turn/item markers (an item-less errored turn, in particular)
// and kept disagreeing with the server that already knows the answer - the
// class of bug this comment exists to close out.
//
// "Which questions" has no such shortcut: the wire's askPending is a bool,
// not a list, so finding the actual open ask_user call(s) still means
// scanning the transcript. It mirrors legacy's cold-attach reconstruction
// rule (parity-m5-composer.md §C: "a completed-but-unanswered ask gets both
// anchor and dock; an ask followed by a reply gets only the settled line"):
// scan every item in transcript order, and anything asked-and-acked AFTER
// the most recent resolution item (a plain user message, an interrupt, or a
// user steer - isResolutionItem below) is still live; anything before it
// was already answered (a resolution item settles the WHOLE pending set at
// once, spec §6.1). Once askPending is false there is nothing to scan for -
// the function returns before ever looking at an item.
//
// This alone is not sufficient for the live, in-flight-submission case -
// see appwire-client/typescript/reconcileBatches.ts's own comment for why a purely
// positional signal can't tell "my own reply's echo" apart from "a
// sibling ask_user call that happened to land first" during the network
// round-trip of sending an answer. reconcileBatches layers stateful
// batch-membership on top of the list this function returns; this
// function's only job is "what does the transcript say is live right now,
// ignoring any in-flight request."

import type { AskUserOption } from "./askShared";
import { parseAskUserQuestions } from "./askShared";
import type { ItemModel, ThreadModel } from "./model";

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

// An ask_user call is answerable only if it settled WITHOUT error. Status is
// not a usable failure signal — the projector stamps "completed" even on a
// denied/errored call (ItemModel.error's own doc comment; a Go follow-up) — so
// error PRESENCE is what disqualifies a card: a denied ask must never render as
// answerable, or the human submits answers into a call that already failed.
function isAckedAskUserItem(item: ItemModel): boolean {
  return (
    item.type === "commandExecution" &&
    item.toolName === "ask_user" &&
    item.status === "completed" &&
    item.error === undefined
  );
}

// isUserAuthoredSteer answers one wire-level question: is this steering item
// the human speaking, as opposed to the daemon injecting one on its own
// (SteeringSourceUser) or a human-note update recorded through the same
// source (session_notes_rpc.go's steeringOrigin machinery stamps a note
// SteeringKindHumanNote even though a human wrote it - writing a note is not
// the human speaking through the transcript). Shared with web layoutRoles.ts,
// which asks the same wire question for its own, unrelated reason (routing
// the item to a divider instead of a user bubble); callers add their own
// `item.type === "steering"` gate first, since this only reads the fields a
// non-steering item never sets.
export function isUserAuthoredSteer(item: ItemModel): boolean {
  return item.source === "user" && item.steeringKind !== "human-note";
}

// An item resolves the whole pending ask set at once (spec §6.1): a plain
// user message (type "userMessage" - the wire's literal string for both a
// plain composer send and an ask-dock's own composed [answers] reply), or a
// steering item the server itself treats as the user resolving it. The
// server clears its own pending-ask bookkeeping (agent's askPending) in
// exactly two places outside a plain user message: an interrupted turn
// (session_lifecycle.go calls clearAskPending directly, then appends a
// steering turn carrying SteeringKindInterrupted as the transcript's marker
// of that boundary - "the user is demonstrably present") and an accepted
// user steer (which enters the drain loop as EntryUserInput, the same
// accepted-turn path that clears askPending for a plain user message -
// isUserAuthoredSteer above). A daemon-originated steer with neither marker,
// and a human-note steer, are not the user speaking and resolve nothing.
function isResolutionItem(item: ItemModel): boolean {
  if (item.type === "userMessage") return true;
  if (item.type !== "steering") return false;
  if (item.steeringKind === "interrupted") return true;
  return isUserAuthoredSteer(item);
}

// lastResolutionIndex finds the position of the most recent resolution
// boundary in transcript order, over the flattened item list. -1 when no
// boundary exists yet (a fresh thread, or one that has never had a user
// turn). A turn with no items of its own (e.g. a failed turn, rendered only
// as a systemMessage by the server's projection, never as a live ItemModel
// here) contributes nothing to this scan either way - it is neither a
// question to show nor a boundary to stop at; whether it resolved anything
// is the server's call, already folded into ThreadModel.askPending.
function lastResolutionIndex(items: readonly ItemModel[]): number {
  let last = -1;
  items.forEach((item, index) => {
    if (isResolutionItem(item)) last = index;
  });
  return last;
}

export function liveAskQuestions(model: ThreadModel): AskQuestionRef[] {
  if (!model.askPending) return [];
  const items = model.turns.flatMap((turn) => turn.items);
  const boundary = lastResolutionIndex(items);
  const refs: AskQuestionRef[] = [];
  items.forEach((item, index) => {
    if (index <= boundary || !isAckedAskUserItem(item)) return;
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
