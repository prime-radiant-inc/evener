// builtinCommand.ts: the composer's own Enter/submit interception (2026-08-14
// decision, "the composer is where you act on this session"). Pure and
// framework-agnostic on purpose, same rationale as submitRouting.ts - every
// branch here is unit-testable without mounting Composer.tsx.
//
// A message the user is about to submit is checked against the SAME resolved
// BUILT-IN list the inline slash menu offers (shell/palette/commands.ts's
// sessionBuiltinCommands, merged by slashCompletion.ts's mergeSlashCommands):
// if it parses as one, the composer runs that command's RPC instead of
// sending the text as a chat message. A literal message that happens to
// start with a known "/command" therefore executes instead of sending -
// deliberate, matching Slack/Discord muscle memory (decisions.md's own
// entry) - and anything that doesn't match (an unknown "/foo", or a plugin
// catalog command, which this module never even looks at) falls through to
// the composer's ordinary send/queue/steer routing untouched.
import { friendlyErrorMessage } from "../../../protocol/errors";
import { blocked, isBlocked } from "../../../shell/palette/blocked";
import type { PaletteRunContext, ScopedCommand } from "../../../shell/palette/commands";
import { type BuiltinMatch, findBuiltinArgument } from "./builtinInvocation";

// resolveCommandResult runs the matched command exactly the way the palette
// itself would: an argless command's plain run(), a free-arg command's
// run(ctx, argsText) verbatim, or - for an enum-arg command like /model -
// the args.source(ctx) catalog resolved and searched for an item whose id OR
// label matches the typed text (case-insensitive). /model's own source reads
// the same model/list-backed store as ModelSwitch, so the composer does not
// re-derive a second catalog.
async function resolveCommandResult(
  command: ScopedCommand,
  argsText: string,
  ctx: PaletteRunContext,
): Promise<unknown> {
  if (!command.args) return command.run?.(ctx);
  if (command.args.kind === "free") return command.args.run(ctx, argsText);
  const items = await command.args.source(ctx);
  const item = findBuiltinArgument(items, argsText);
  if (!item) {
    const label = argsText.trim();
    return blocked(label ? `/${command.id}: unknown value "${label}"` : `/${command.id} needs a value`);
  }
  return command.args.run(ctx, item);
}

export type BuiltinRunOutcome = { ok: true } | { ok: false; message: string };

// runBuiltinCommand executes the match and reports back whether the
// composer should clear its draft (success) or preserve it (failure) - the
// FEEDBACK itself is a toast plus whatever live chrome the command's own
// mutation already drives (the goal chip, the status row): most built-ins
// (goal, compact, clear, steer, queue, aside, drain-as-steer, interrupt,
// tasks, status, project) push no toast of their own and rely entirely on
// that live chrome, while a few (shutdown, model, reasoning-effort, upgrade)
// already toast on both success and failure. The `toasted` flag below is
// what keeps this function from ever DOUBLING a toast a command already
// pushed itself - it wraps ctx.toasts.push to notice, and only falls back to
// a friendlyErrorMessage toast of its own when nothing did.
export async function runBuiltinCommand(
  match: BuiltinMatch<ScopedCommand>,
  baseCtx: PaletteRunContext,
): Promise<BuiltinRunOutcome> {
  let toasted = false;
  const ctx: PaletteRunContext = {
    ...baseCtx,
    toasts: {
      push: (kind, text) => {
        toasted = true;
        baseCtx.toasts.push(kind, text);
      },
    },
  };
  if (match.command.unavailableReason) {
    // Same verdict the palette's own disabled row carries - a false wire
    // capability, checked BEFORE running anything (never attempted and
    // rejected by the hub, which would cost a round trip for an answer this
    // side already knows).
    const message = `/${match.command.id} is ${match.command.unavailableReason}`;
    baseCtx.toasts.push("error", message);
    return { ok: false, message };
  }
  try {
    const result = await resolveCommandResult(match.command, match.argsText, ctx);
    if (isBlocked(result)) {
      if (!toasted) baseCtx.toasts.push("error", result.message);
      return { ok: false, message: result.message };
    }
    return { ok: true };
  } catch (err) {
    const message = friendlyErrorMessage(err);
    if (!toasted) baseCtx.toasts.push("error", message);
    return { ok: false, message };
  }
}
