// Semantic validation for user keybinding overrides. The server validates
// STRUCTURE only (it does not know the frontend action registry); everything
// that requires knowing the live bindings happens here, at load and at patch:
//
//   - action ids against the default map (unknown or stale rules are skipped),
//   - chord parseability,
//   - the survey's per-platform never-use list (docs/web-ui/specs/
//     2026-09-03-keybinding-system-survey.md "Reserved-chord landscape") -
//     chords the browser will never deliver to the page,
//   - conflicts against the effective map the payload would produce.
//
// Validation NEVER throws into startup: every problem becomes a warning and
// the offending rule is skipped, so persisted data can degrade to defaults
// but can never crash the shell.

import { type KeySequence, parseChord, serializeChord } from "./chord";
import { DEFAULT_BINDINGS } from "./defaults";
import { GLOBAL_SCOPE, type KeybindingsRegistry } from "./registry";

export type KeybindingsPlatform = "apple" | "other";

/** Same platform split tinykeys applies for `$mod` (and keyhint for display):
 * navigator.platform's Apple tokens. jsdom reports "", so tests default to
 * "other" regardless of the host running them. */
export function currentKeybindingsPlatform(): KeybindingsPlatform {
  if (typeof window === "undefined") return "other";
  return /Mac|iPhone|iPad|iPod/.test(window.navigator.platform) ? "apple" : "other";
}

// The survey's verified never-use list ("not interceptable by pages"),
// platform-split. The Control family is listed unqualified in the survey, so
// it is reserved on every platform. Two survey entries are deliberately NOT
// encoded: "Escape in fullscreen" is context-dependent (Escape is
// settings.close's own default chord), and "Ctrl+Shift+P in Firefox" is a
// private-window-only caveat in one browser, not a platform rule.
const COMMON_RESERVED = [
  "Control+W",
  "Control+N",
  "Control+T",
  "Control+Tab",
  "Control+Shift+Tab",
  "Control+1",
  "Control+2",
  "Control+3",
  "Control+4",
  "Control+5",
  "Control+6",
  "Control+7",
  "Control+8",
  "Control+9",
  "F11",
];
const APPLE_RESERVED = [
  "Meta+W",
  "Meta+N",
  "Meta+T",
  "Meta+Q",
  "Meta+Shift+N",
  "Meta+Shift+T",
  "Meta+1",
  "Meta+2",
  "Meta+3",
  "Meta+4",
  "Meta+5",
  "Meta+6",
  "Meta+7",
  "Meta+8",
  "Meta+9",
];
const OTHER_RESERVED = ["Alt+F4", "Control+Alt+Delete"];

// A reserved match compares REQUIRED modifiers + key only (case-insensitive
// on the key): a user chord that lists extra OPTIONAL modifiers still has a
// no-modifier match the browser will never deliver, so it stays reserved.
// Extra REQUIRED modifiers escape the list (Meta+Shift+W is not the survey's
// Meta+W), and multi-press sequences are never reserved - the browser
// delivers every press after the first to the page.
function canonicalPress(sequence: KeySequence): string | null {
  if (sequence.length !== 1) return null;
  const press = sequence[0];
  if (press === undefined) return null;
  const key = press.key instanceof RegExp ? press.key.source : press.key;
  return `${press.modifiers.join("+")}+${key.toLowerCase()}`;
}

const RESERVED_BY_PLATFORM: Record<KeybindingsPlatform, ReadonlySet<string>> = {
  apple: new Set(
    [...COMMON_RESERVED, ...APPLE_RESERVED].map((chord) => {
      const canonical = canonicalPress(parseChord(chord));
      if (canonical === null) throw new Error(`bad reserved chord "${chord}"`);
      return canonical;
    }),
  ),
  other: new Set(
    [...COMMON_RESERVED, ...OTHER_RESERVED].map((chord) => {
      const canonical = canonicalPress(parseChord(chord));
      if (canonical === null) throw new Error(`bad reserved chord "${chord}"`);
      return canonical;
    }),
  ),
};

export interface OverrideRule {
  action: string;
  chord: string | null;
}

export type ValidationWarningReason = "unknown-action" | "unparseable-chord" | "reserved-chord" | "conflict";

export interface ValidationWarning {
  rule: OverrideRule;
  reason: ValidationWarningReason;
  message: string;
  /** For "conflict": the other action holding the chord. */
  conflictWith?: string;
}

export interface ValidatedRule {
  action: string;
  chord: KeySequence | null;
}

export interface ValidatedOverrides {
  /** The payload's effective rules: valid, deduped last-rule-wins, in order. */
  rules: ValidatedRule[];
  warnings: ValidationWarning[];
}

interface EffectiveBinding {
  scope: string;
  serialized: string;
}

/** Validates a rules payload against the live registry. Conflict detection
 * simulates the effective map the payload would produce: each accepted rule
 * replaces its action's bindings in the simulation, so a chord freed by an
 * earlier rule (rebind-away or unbind) can be claimed by a later one, and a
 * later rule landing on an earlier rule's new chord is the conflict. */
export function validateOverrideRules(
  rules: readonly OverrideRule[],
  registry: KeybindingsRegistry,
  platform: KeybindingsPlatform = currentKeybindingsPlatform(),
): ValidatedOverrides {
  const warnings: ValidationWarning[] = [];
  const validated: ValidatedRule[] = [];
  const effective = new Map<string, EffectiveBinding[]>();
  for (const binding of registry.getState().bindings) {
    const list = effective.get(binding.actionId) ?? [];
    list.push({ scope: binding.scope, serialized: serializeChord(binding.chord) });
    effective.set(binding.actionId, list);
  }
  const reserved = RESERVED_BY_PLATFORM[platform];

  // The payload resolves same-action repeats last-rule-wins; the validated
  // list keeps only the winning rule.
  const pushRule = (rule: ValidatedRule): void => {
    const prior = validated.findIndex((r) => r.action === rule.action);
    if (prior !== -1) validated.splice(prior, 1);
    validated.push(rule);
  };

  for (const rule of rules) {
    const defaultInput = DEFAULT_BINDINGS.find((b) => b.actionId === rule.action);
    if (defaultInput === undefined) {
      warnings.push({
        rule,
        reason: "unknown-action",
        message: `unknown keybinding action "${rule.action}"`,
      });
      continue;
    }
    if (rule.chord === null) {
      pushRule({ action: rule.action, chord: null });
      effective.set(rule.action, []);
      continue;
    }
    let sequence: KeySequence;
    try {
      sequence = parseChord(rule.chord);
    } catch (error) {
      warnings.push({
        rule,
        reason: "unparseable-chord",
        message: `cannot parse chord "${rule.chord}": ${error instanceof Error ? error.message : String(error)}`,
      });
      continue;
    }
    const canonical = canonicalPress(sequence);
    if (canonical !== null && reserved.has(canonical)) {
      warnings.push({
        rule,
        reason: "reserved-chord",
        message: `chord "${rule.chord}" is reserved by the browser/OS on this platform and would never reach the page`,
      });
      continue;
    }
    const scope = defaultInput.scope ?? GLOBAL_SCOPE;
    const serialized = serializeChord(sequence);
    let conflictWith: string | undefined;
    for (const [otherAction, bindings] of effective) {
      if (otherAction === rule.action) continue;
      if (bindings.some((b) => b.scope === scope && b.serialized === serialized)) {
        conflictWith = otherAction;
        break;
      }
    }
    if (conflictWith !== undefined) {
      warnings.push({
        rule,
        reason: "conflict",
        conflictWith,
        message: `chord "${rule.chord}" in scope "${scope}" is already bound by "${conflictWith}"`,
      });
      continue;
    }
    pushRule({ action: rule.action, chord: sequence });
    effective.set(rule.action, [{ scope, serialized }]);
  }
  return { rules: validated, warnings };
}
