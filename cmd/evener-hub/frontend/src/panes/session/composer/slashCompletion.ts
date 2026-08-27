// Inline slash-command completion for the composer's own textarea, ported
// from Beautiful UI's prompt-bar completion affordance (beautifului.dev,
// MIT © 2026 Shane Levine) — see LICENSES/beautiful-ui.txt. Only the slash
// half of their trailing-token parser is ported: evener has no @-source
// catalog to complete against, so that branch is deliberately dropped
// rather than carried over unused.
//
// Pure functions only - Composer.tsx wires these against its own
// text/caret state and the plugin slash-command catalog (stores/
// commandCatalog.ts), the same catalog the modal command palette reads.

import type { CommandDescriptor, EvenerSkillInfo } from "../../../protocol/types.gen";
import { type ScopedCommand, slashCommandInvocation } from "../../../shell/palette/commands";

export interface SlashToken {
  // Index of the "/" itself, and the caret position the match was computed
  // against (NOT necessarily the end of the token's full run in the
  // draft - see parseSlashToken's own doc comment).
  start: number;
  end: number;
  query: string;
}

// A slash token is a documented bare or singly-qualified catalog name. The
// final negative lookahead is a strict end-of-input anchor: unlike `$`, it
// does not match before a trailing newline. The catalog grammar is ASCII and
// therefore its maximum 128 UTF-8 bytes is also 128 JavaScript characters.
const TRAILING_SLASH_TOKEN_RE = /(^|\s)(\/)((?:[A-Za-z0-9_][A-Za-z0-9_-]*(?::[A-Za-z0-9_][A-Za-z0-9_-]*)?)?)(?![\s\S])/;

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
  if (query.length > 128) return null;
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
  kind: "builtin" | "plugin" | "skill";
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
export function mergeSlashCommands(
  builtins: ScopedCommand[],
  catalog: CommandDescriptor[],
  skills: EvenerSkillInfo[] = [],
): SlashMenuItem[] {
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
  const skillItems: SlashMenuItem[] = skills.map((skill) => ({
    key: `skill:${skill.name}`,
    invocation: `/${skill.name}`,
    label: skill.name,
    hint: skill.description ?? "",
    kind: "skill",
  }));
  return [...builtinItems, ...pluginItems, ...skillItems];
}

interface SlashMatch {
  item: SlashMenuItem;
  index: number;
  exact: boolean;
  begins: boolean;
  contiguousness: number;
  span: number;
  start: number;
}

function scoreSlashMatch(item: SlashMenuItem, query: string, index: number): SlashMatch | null {
  const label = item.label.toLowerCase();
  const positions: number[] = [];
  let labelIndex = 0;
  for (const queryCharacter of query) {
    const matchIndex = label.indexOf(queryCharacter, labelIndex);
    if (matchIndex < 0) return null;
    positions.push(matchIndex);
    labelIndex = matchIndex + 1;
  }

  let longestRun = 1;
  let currentRun = 1;
  for (let positionIndex = 1; positionIndex < positions.length; positionIndex += 1) {
    const position = positions[positionIndex];
    const previousPosition = positions[positionIndex - 1];
    if (position !== undefined && previousPosition !== undefined && position === previousPosition + 1) {
      currentRun += 1;
      longestRun = Math.max(longestRun, currentRun);
    } else {
      currentRun = 1;
    }
  }

  const start = positions[0] ?? 0;
  const end = positions[positions.length - 1] ?? 0;
  return {
    item,
    index,
    exact: label === query,
    begins: start === 0,
    contiguousness: longestRun,
    span: end - start,
    start,
  };
}

function compareSlashMatches(a: SlashMatch, b: SlashMatch): number {
  if (a.exact !== b.exact) return a.exact ? -1 : 1;
  if (a.begins !== b.begins) return a.begins ? -1 : 1;
  if (a.contiguousness !== b.contiguousness) return b.contiguousness - a.contiguousness;
  if (a.span !== b.span) return a.span - b.span;
  if (a.start !== b.start) return a.start - b.start;
  return a.index - b.index;
}

// filterSlashMenuItems matches only the item's own label, case-insensitively.
// Empty queries preserve merge order; non-empty queries are ranked by the
// documented exact/beginning/contiguousness/span/earliest/index tuple.
export function filterSlashMenuItems(items: SlashMenuItem[], query: string): SlashMenuItem[] {
  const q = query.toLowerCase();
  if (!q) return items;
  return items
    .map((item, index) => scoreSlashMatch(item, q, index))
    .filter((match): match is SlashMatch => match !== null)
    .sort(compareSlashMatches)
    .map((match) => match.item);
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
  const suffix = text.slice(token.end);
  const inserted = /^\s/.test(suffix) ? invocation : `${invocation} `;
  return { text: before + inserted + suffix, caret: before.length + inserted.length };
}
