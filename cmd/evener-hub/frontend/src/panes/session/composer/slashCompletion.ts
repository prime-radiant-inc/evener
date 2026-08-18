// Inline slash-command completion for the composer's own textarea, ported
// from Beautiful UI's prompt-bar completion affordance (beautifului.dev,
// MIT © 2026 Shane Levine) — see LICENSES/beautiful-ui.txt. Only the slash
// half of their trailing-token parser is ported: serf has no @-source
// catalog to complete against, so that branch is deliberately dropped
// rather than carried over unused.
//
// Pure functions only - Composer.tsx wires these against its own
// text/caret state and the plugin slash-command catalog (stores/
// commandCatalog.ts), the same catalog the modal command palette reads.

import type { CommandDescriptor } from "../../../protocol/types.gen";
import { type ScopedCommand, slashCommandInvocation } from "../../../shell/palette/commands";

export interface SlashToken {
  // Index of the "/" itself, and the caret position the match was computed
  // against (NOT necessarily the end of the token's full run in the
  // draft - see parseSlashToken's own doc comment).
  start: number;
  end: number;
  query: string;
}

// Beautiful UI's own trailing-token regex, slash-only: a "/" preceded by
// start-of-string or whitespace, followed by zero or more word/hyphen/colon
// characters, anchored at the END of whatever string it's run against. The
// colon is this fork's own addition (Beautiful UI has no qualified-command
// concept): a typed "/plugin:rev" must keep matching as ONE token through
// the colon, or the menu closes the instant the user types past "/plugin"
// into the qualifier - see filterSlashCommands' own note on matching
// against `name` only, and Composer.tsx's commitSlashCompletion for the
// qualified invocation this makes it possible to type manually.
const TRAILING_SLASH_TOKEN_RE = /(^|\s)(\/)([\w:-]*)$/;

// parseSlashToken looks for a slash token the caret trails, by running the
// trailing-token regex against the text BEFORE the caret only - text after
// the caret is invisible to the match. That means a caret sitting in the
// middle of a longer token (e.g. "/wor|ld", caret at the "|") still
// triggers, using the token's PREFIX up to the caret as the query: the
// caret trails "/wor", so that's the token under it, regardless of what
// comes after. A caret with no slash trailing it at all - mid-word
// ("foo/b|ar"), past a completed token's trailing space ("/x |"), or with
// no slash anywhere before it - returns null.
export function parseSlashToken(text: string, caret: number): SlashToken | null {
  const before = text.slice(0, caret);
  const match = TRAILING_SLASH_TOKEN_RE.exec(before);
  if (!match) return null;
  const leadIn = match[1] ?? "";
  const query = match[3] ?? "";
  const start = match.index + leadIn.length;
  return { start, end: caret, query };
}

// SlashMenuItem: one row of the composer's inline slash menu, whether it
// came from the session-scoped BUILT-IN registry (shell/palette/commands.ts's
// sessionBuiltinCommands) or the plugin catalog (stores/commandCatalog.ts) -
// mergeSlashCommands below is the ONE place that flattens both into this
// shared shape, so SlashCompletionMenu.tsx never has to know which source a
// row came from to render it.
export interface SlashMenuItem {
  // Stable React key: "builtin:<id>" or "plugin:<pluginName>:<name>" - a
  // plain command id/name alone could collide between the two sources (or
  // between two plugins registering the same unqualified name).
  key: string;
  // The full "/name" or "/plugin:name" text spliceSlashCommand inserts -
  // slashCommandInvocation's own qualification rule for a plugin row, a bare
  // "/id" for a built-in (built-ins have no plugin source to qualify against).
  invocation: string;
  // What the query matches against: the built-in's own id, or the catalog
  // entry's unqualified name (never the qualified "plugin:name" form - a
  // user filtering the menu types the command's own name, not its plugin
  // prefix).
  label: string;
  // States what the row does ("sets the session goal") for a built-in, or
  // the catalog entry's own description/argumentHint for a plugin row -
  // falling back to naming its plugin provenance only when the catalog gave
  // it no description at all (2026-08-14 decision: "the hint states what it
  // does, or its plugin provenance").
  hint: string;
  kind: "builtin" | "plugin";
}

// mergeSlashCommands is the composer's own single merge point (2026-08-14,
// "the composer is where you act on this session"): every session-scoped
// BUILT-IN the palette used to also list, now unified with the plugin
// catalog into the one inline menu Composer.tsx renders. `builtins` is
// expected pre-resolved against the focused session
// (shell/palette/commands.ts's sessionBuiltinCommands) - an unavailable one
// (a false wire capability) is filtered out here rather than rendered
// disabled, since this menu has no disabled-row affordance the way the
// palette's does; matchBuiltinInvocation (builtinCommand.ts) still resolves
// it from the FULL unfiltered list at submit time, so a typed invocation for
// a momentarily-unavailable command still gets an honest "not available
// right now" instead of silently being sent as a chat message.
export function mergeSlashCommands(builtins: ScopedCommand[], catalog: CommandDescriptor[]): SlashMenuItem[] {
  const builtinItems: SlashMenuItem[] = builtins
    .filter((c) => c.unavailableReason === undefined)
    .map((c) => ({ key: `builtin:${c.id}`, invocation: `/${c.id}`, label: c.id, hint: c.hint, kind: "builtin" }));
  const pluginItems: SlashMenuItem[] = catalog.map((c) => ({
    key: `plugin:${c.pluginName ?? ""}:${c.name}`,
    invocation: slashCommandInvocation(c),
    label: c.name,
    hint: c.description || c.argumentHint || `plugin: ${c.pluginName ?? c.source ?? "unknown"}`,
    kind: "plugin",
  }));
  return [...builtinItems, ...pluginItems];
}

// filterSlashMenuItems: startsWith on the item's own label, case-insensitive
// (the same normalization shell/palette/commands.ts's own filterCommands
// uses for its query). An empty query matches every item, in merge order
// (built-ins first, then the catalog).
export function filterSlashMenuItems(items: SlashMenuItem[], query: string): SlashMenuItem[] {
  const q = query.toLowerCase();
  return items.filter((item) => item.label.toLowerCase().startsWith(q));
}

export interface SlashSpliceResult {
  text: string;
  caret: number;
}

// spliceSlashCommand replaces the token (start..end) with "<invocation> "
// (the caller supplies the full "/name" or "/plugin:name" form - see
// shell/palette/commands.ts's slashCommandInvocation, Composer.tsx's own
// caller of this function), keeping whatever text came before the token and
// whatever came after the caret (relevant when the caret was mid-draft, not
// at the very end of the text) untouched. The caret lands right after the
// inserted trailing space, ahead of any surviving trailing text.
export function spliceSlashCommand(text: string, token: SlashToken, invocation: string): SlashSpliceResult {
  const before = text.slice(0, token.start);
  const after = text.slice(token.end);
  const inserted = `${invocation} `;
  return { text: before + inserted + after, caret: before.length + inserted.length };
}
