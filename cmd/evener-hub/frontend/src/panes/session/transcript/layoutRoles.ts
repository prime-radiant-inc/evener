// layoutRoles is the ONE place the transcript answers "where does this row
// sit". It replaces TurnBlock's old MARGIN_ITEM_TYPES exception set, which
// classified rows by WHAT they are ("steering, systemMessage, warning are
// daemon bookkeeping") and smuggled a layout answer ("therefore full width")
// out of a semantic one. That assumption did not survive contact with the
// running app: Jesse's review call was that job notifications and steering
// messages were not indenting with everything else. A layout role must
// answer WHERE, not WHAT - so "run" has no exception set at all, and
// semantics can never again leak into geometry.
//
// Exactly two roles exist inside a turn:
//  - "speaker": a row that renders its own avatar header (the avatar sits in
//    the gutter), so it spans the full width unwrapped.
//  - "run": everything else. One kind of row, indented with the run, no
//    exceptions - including unknown future wire types, so a new item type can
//    never silently fall back to full width.
//
// Transcript chrome (TurnSeparator, SeenDivider, TurnFailureEndCap) is not
// items and stays outside this map entirely; TurnBlock renders it directly.
import type { ItemModel } from "../../../protocol/model";

export type RowRole = "speaker" | "run";

// opts.opensExchange: whether this agentMessage opens an exchange (the
// transcript-wide set Session.tsx threads down). Only an exchange-opening
// agentMessage is a speaker row; a mid-exchange one is part of the run.
export function rowRoleFor(item: ItemModel, opts: { opensExchange?: boolean }): RowRole {
  if (item.type === "userMessage") return "speaker";
  if (item.type === "agentMessage" && opts.opensExchange === true) return "speaker";
  return "run";
}
