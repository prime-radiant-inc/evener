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

export interface SlashEmbedding {
  end: number;
  start: number;
  longestRun: number;
}

function compareEmbeddings(a: SlashEmbedding, b: SlashEmbedding): number {
  const aBegins = a.start === 0;
  const bBegins = b.start === 0;
  if (a.longestRun !== b.longestRun) return b.longestRun - a.longestRun;
  if (aBegins !== bBegins) return aBegins ? -1 : 1;
  const aSpan = a.end - a.start;
  const bSpan = b.end - b.start;
  if (aSpan !== bSpan) return aSpan - bSpan;
  return a.start - b.start;
}

export interface SlashMatchEvaluation {
  embedding: SlashEmbedding | null;
  operations: number;
}

// evaluateSlashLabel finds the best valid embedding without enumerating all
// subsequences. It first computes the maximum possible contiguous run (the
// longest common substring), then considers every matching block of that
// length. Prefix/suffix bounds represent every embedding that can achieve the
// winning contiguousness while keeping the work polynomial.
export function evaluateSlashLabel(rawLabel: string, rawQuery: string): SlashMatchEvaluation {
  const label = rawLabel.toLowerCase();
  const query = rawQuery.toLowerCase();
  const queryLength = query.length;
  const labelLength = label.length;
  let operations = 0;
  if (queryLength === 0 || labelLength === 0) return { embedding: null, operations };
  if (label === query) {
    return { embedding: { start: 0, end: queryLength - 1, longestRun: queryLength }, operations };
  }

  let longestRun = 0;
  let previousRuns = new Array<number>(labelLength).fill(0);
  for (let queryIndex = 0; queryIndex < queryLength; queryIndex += 1) {
    const currentRuns = new Array<number>(labelLength).fill(0);
    for (let labelIndex = 0; labelIndex < labelLength; labelIndex += 1) {
      operations += 1;
      if (query[queryIndex] !== label[labelIndex]) continue;
      const previousRun = previousRuns[labelIndex - 1] ?? 0;
      const run = queryIndex === 0 || labelIndex === 0 ? 1 : previousRun + 1;
      currentRuns[labelIndex] = run;
      longestRun = Math.max(longestRun, run);
    }
    previousRuns = currentRuns;
  }
  if (longestRun === 0) return { embedding: null, operations };

  const fixedEnds = Array.from({ length: labelLength }, () =>
    new Array<number | undefined>(queryLength + 1).fill(undefined),
  );
  for (let start = 0; start < labelLength; start += 1) {
    operations += 1;
    const row = fixedEnds[start];
    if (!row || label[start] !== query[0]) continue;
    row[1] = start;
    for (let length = 2; length <= queryLength; length += 1) {
      operations += 1;
      const previousEnd = row[length - 1];
      if (previousEnd === undefined) break;
      const character = query[length - 1];
      if (character === undefined) break;
      const end = label.indexOf(character, previousEnd + 1);
      operations += 1;
      if (end < 0) break;
      row[length] = end;
    }
  }

  const latestStarts = Array.from({ length: queryLength + 1 }, () =>
    new Array<number | undefined>(labelLength + 1).fill(undefined),
  );
  for (let length = 1; length <= queryLength; length += 1) {
    const row = latestStarts[length];
    if (!row) continue;
    for (let bound = 1; bound <= labelLength; bound += 1) {
      for (let start = bound - 1; start >= 0; start -= 1) {
        operations += 1;
        const end = fixedEnds[start]?.[length];
        if (end !== undefined && end < bound) {
          row[bound] = start;
          break;
        }
      }
    }
  }

  const suffixEnds = Array.from({ length: queryLength + 1 }, () =>
    new Array<number | undefined>(labelLength + 1).fill(undefined),
  );
  for (let start = 0; start < queryLength; start += 1) {
    const row = suffixEnds[start];
    if (!row) continue;
    for (let bound = 0; bound <= labelLength; bound += 1) {
      let end = bound - 1;
      let matched = true;
      for (let queryIndex = start; queryIndex < queryLength; queryIndex += 1) {
        operations += 1;
        const character = query[queryIndex];
        if (character === undefined) {
          matched = false;
          break;
        }
        end = label.indexOf(character, end + 1);
        operations += 1;
        if (end < 0) {
          matched = false;
          break;
        }
      }
      if (matched) row[bound] = end;
    }
  }

  let best: SlashEmbedding | null = null;
  const consider = (candidate: SlashEmbedding) => {
    if (!best || compareEmbeddings(candidate, best) < 0) best = candidate;
  };
  for (let queryStart = 0; queryStart + longestRun <= queryLength; queryStart += 1) {
    const block = query.slice(queryStart, queryStart + longestRun);
    for (let labelStart = 0; labelStart + longestRun <= labelLength; labelStart += 1) {
      operations += 1;
      if (!label.startsWith(block, labelStart)) continue;
      const blockEnd = labelStart + longestRun - 1;
      const end =
        queryStart + longestRun === queryLength ? blockEnd : suffixEnds[queryStart + longestRun]?.[blockEnd + 1];
      if (end === undefined) continue;

      const start = queryStart === 0 ? labelStart : latestStarts[queryStart]?.[labelStart];
      if (start !== undefined) consider({ start, end, longestRun });

      const beginningPrefixEnd = fixedEnds[0]?.[queryStart];
      const begins =
        queryStart === 0 ? labelStart === 0 : beginningPrefixEnd !== undefined && beginningPrefixEnd < labelStart;
      if (begins && (start !== 0 || queryStart !== 0)) consider({ start: 0, end, longestRun });
    }
  }
  return { embedding: best, operations };
}

function scoreSlashMatch(item: SlashMenuItem, query: string, index: number): SlashMatch | null {
  const { embedding } = evaluateSlashLabel(item.label, query);
  if (!embedding) return null;
  const { start, end } = embedding;
  return {
    item,
    index,
    exact: item.label.toLowerCase() === query,
    begins: start === 0,
    contiguousness: embedding.longestRun,
    span: end - start,
    start,
  };
}

function compareSlashMatches(a: SlashMatch, b: SlashMatch): number {
  if (a.exact !== b.exact) return a.exact ? -1 : 1;
  if (a.contiguousness !== b.contiguousness) return b.contiguousness - a.contiguousness;
  if (a.begins !== b.begins) return a.begins ? -1 : 1;
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
