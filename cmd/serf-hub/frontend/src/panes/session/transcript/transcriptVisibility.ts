// Settings -> Transcript's three system-status toggles, as a pure predicate
// over the item model: hook exits (all / normal-only) and the prompt-loaded
// disclosure. TurnBlock applies it to a turn's items BEFORE they reach the
// renderers, so a hidden item is absent from grouping too (SystemNoticeItem
// derives its consecutive-run count from turn.items - filtering at render
// time instead would leave a group announcing "5 system events" while
// showing 3).
//
// Classification is by the wire's typed ThreadItem.eventKind discriminator
// (appwire.ThreadItemEventKind*, carried onto ItemModel by reducer.ts's
// wireItemToModel), never by sniffing English out of the message text. The
// legacy renderer matched /\bhook\b.*\bexit\s+(-?\d+)\b/i and a "system
// prompt" title because it had no structured item type to dispatch on; this
// codebase does, so a change to the announcement wording cannot silently
// break these toggles.
import type { ItemModel } from "../../../protocol/model";

// The session's system prompt (apptranscript.go's PreludeTurn) arrives as a
// systemMessage item with this exact, static id - the narrow fallback signal
// for a system prompt projected by a daemon predating the typed system_prompt
// eventKind. The id has been stable across every version, so a
// heterogeneous-version relay still classifies it correctly.
// SystemNoticeItem.tsx reads it from here so both files agree by
// construction; it lives in this side-effect-free helper rather than in that
// component so importing it never triggers a renderer registration.
export const SYSTEM_PROMPT_ITEM_ID = "item_system_prompt";

// SYSTEM_PRELUDE_TURN_ID is the synthetic turn id for content that belongs
// before the session's first real turn - appwire.SystemPreludeTurnID on the
// wire. apptranscript.PreludeTurn's system-prompt scaffold and
// appprojector's bundled SESSION_START-time announcements (plugin loads,
// prompt-loaded notices) both use it, so a transcript whose only turn is
// this one has never had a real turn: the session is dormant (kata bz2z).
// Session.tsx reads it from here, alongside SYSTEM_PROMPT_ITEM_ID above, for
// the same reason: a side-effect-free home neither pane needs to import a
// renderer to reach.
export const SYSTEM_PRELUDE_TURN_ID = "turn_system";

// A hook's own process exit status arrives as ThreadItem.exitCode on the
// hook_completed item (populated by internal/appprojector from
// events.HookEndData.ExitCode). Absent - not zero - when the projecting
// daemon predates that field.
const HOOK_EXIT_EVENT_KIND = "hook_completed";

// Both prompt kinds answer to the single "Prompt loaded" toggle, matching
// the legacy renderer (which keyed "system prompt" and "prompt loaded" to
// one preference) and the setting's own copy: system_prompt is the full
// prompt scaffold the toggle offers as an expandable disclosure,
// prompt_loaded the quiet "Loaded prompt X (N B)" line naming the same
// event.
const PROMPT_EVENT_KINDS = new Set(["system_prompt", "prompt_loaded"]);

// "Round timings" reports one measurement on two surfaces: TurnSeparator's
// friendly per-turn annotation, and this raw projector line ("Round 2
// total=1.5s llm=1.2s context=25ms ..."). Both answer to the one setting, or
// turning it off would leave the rawer of the two on screen.
const ROUND_TIMINGS_EVENT_KIND = "round_timings";

export interface TranscriptVisibilityPrefs {
  roundTimings: boolean;
  hookExitsAll: boolean;
  hookExitsNormal: boolean;
  promptLoaded: boolean;
}

function isHookExitItem(item: ItemModel): boolean {
  return item.eventKind === HOOK_EXIT_EVENT_KIND;
}

function isPromptItem(item: ItemModel): boolean {
  if (item.eventKind !== undefined && item.eventKind !== "") return PROMPT_EVENT_KINDS.has(item.eventKind);
  return item.id === SYSTEM_PROMPT_ITEM_ID;
}

/**
 * Whether `item` survives the current transcript visibility preferences.
 * Anything no toggle governs is always visible - these settings hide
 * specific, named system events, never anything else.
 *
 * Hook exits: hookExitsAll shows every one; hookExitsNormal shows only those
 * that exited 0. Both on is the same as all-on (all subsumes normal, which is
 * what the setting's copy promises); both off hides every hook exit. An
 * unknown exit code is honestly unknown, so normal-only withholds it rather
 * than claiming it exited cleanly.
 */
export function isItemVisible(item: ItemModel, prefs: TranscriptVisibilityPrefs): boolean {
  if (isHookExitItem(item)) {
    return prefs.hookExitsAll || (prefs.hookExitsNormal && item.exitCode === 0);
  }
  if (isPromptItem(item)) return prefs.promptLoaded;
  if (item.eventKind === ROUND_TIMINGS_EVENT_KIND) return prefs.roundTimings;
  return true;
}

/**
 * `items` filtered by isItemVisible, returning the input array itself when
 * every item survives - the common case, and the one where a fresh array
 * would defeat the memoized item renderers' identity checks.
 */
export function visibleItems(items: ItemModel[], prefs: TranscriptVisibilityPrefs): ItemModel[] {
  const kept = items.filter((item) => isItemVisible(item, prefs));
  return kept.length === items.length ? items : kept;
}
