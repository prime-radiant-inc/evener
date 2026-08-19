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

export interface BuiltinMatch {
  command: ScopedCommand;
  argsText: string;
}

// A leading "/<token>" optionally followed by whitespace and the rest of the
// message - `<token>` may not itself contain whitespace (a command name
// never does; slashCommandInvocation only ever produces "name" or
// "plugin:name" shapes, neither one with a space in it). The `s` flag lets
// `.` in the args half span newlines - /goal and /steer both accept a
// multi-line objective/steer text.
const INVOCATION_RE = /^\/(\S+)(?:[ \t]+([\s\S]*))?$/;

// matchBuiltinInvocation: does `text` parse as a known BUILT-IN session
// command? `builtins` is expected to be the FULL unfiltered resolved list
// (shell/palette/commands.ts's sessionBuiltinCommands) - unlike the inline
// menu's own merge (which drops an unavailable command so there is nothing
// to pick), matching here still finds an unavailable command so
// runBuiltinCommand below can answer with its real reason instead of the
// draft silently being sent as a literal chat message. A command with no
// `args` only matches when nothing follows the name - "/compact extra text"
// is not a known invocation of the argless /compact, so it falls through to
// being sent as an ordinary message instead of ignoring the trailing text.
export function matchBuiltinInvocation(text: string, builtins: ScopedCommand[]): BuiltinMatch | null {
  const m = INVOCATION_RE.exec(text);
  if (!m) return null;
  const token = m[1] ?? "";
  const argsText = m[2] ?? "";
  const command = builtins.find((c) => c.id === token);
  if (!command) return null;
  if (!command.args && argsText.trim() !== "") return null;
  return { command, argsText };
}

// resolveCommandResult runs the matched command exactly the way the palette
// itself would: an argless command's plain run(), a free-arg command's
// run(ctx, argsText) verbatim, or - for an enum-arg command like /model -
// the args.source(ctx) catalog resolved and searched for an item whose id OR
// label matches the typed text (case-insensitive). /model's own source
// already calls mergeScopedCatalog (commands.ts's own doc comment on that
// command) - reusing the registry entry here, rather than re-deriving the
// catalog in the composer, is what "complete their values from the same
// merged catalog ModelSwitch uses" means in practice: there is only ONE
// place that catalog is built, and this is not it.
async function resolveCommandResult(
  command: ScopedCommand,
  argsText: string,
  ctx: PaletteRunContext,
): Promise<unknown> {
  if (!command.args) return command.run?.(ctx);
  if (command.args.kind === "free") return command.args.run(ctx, argsText);
  const items = await command.args.source(ctx);
  const needle = argsText.trim().toLowerCase();
  const item = items.find((it) => it.id.toLowerCase() === needle || it.label.toLowerCase() === needle);
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
export async function runBuiltinCommand(match: BuiltinMatch, baseCtx: PaletteRunContext): Promise<BuiltinRunOutcome> {
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
