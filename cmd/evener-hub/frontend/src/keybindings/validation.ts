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

import { chordsOverlap, type KeySequence, parseChord } from "./chord";
import { DEFAULT_BINDINGS, defaultBindingShapesForAction } from "./defaults";
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
  sequence: KeySequence;
}

/** Validates a rules payload against the live registry. Conflict detection
 * simulates the FINAL effective map the payload would produce: untouched
 * actions keep their live bindings, every currently-overridden action the
 * payload does not (validly) name gets its defaults restored, and each
 * candidate rule claims its new chord. A chord freed by an earlier rule
 * (rebind-away or unbind) can therefore be claimed by a later one.
 * Conflicts are OVERLAPS (chordsOverlap), not serialization equality: an
 * override whose exact chord overlaps a default's optional modifiers would
 * be shadowed by the earlier-registered default at dispatch time.
 *
 * When a restored default and a rule claim collide, THE RESTORE WINS: the
 * rule is skipped with a conflict warning and its action falls back to its
 * own defaults. This is the choice that leaves every action bound - the
 * alternative (deferring the restore) would strand the dropped action on an
 * override the hub payload no longer contains, diverging the registry from
 * the payload indefinitely. Defaults never conflict with each other, so a
 * conflict always has a rule claim to blame.
 *
 * `characterKeyTriggers: false` mirrors the cheatsheet character-key pref:
 * the live registry has no "?" cheatsheet binding while the pref is off, so
 * the dropped-restore simulation must not claim one either. */
export function validateOverrideRules(
  rules: readonly OverrideRule[],
  registry: KeybindingsRegistry,
  platform: KeybindingsPlatform = currentKeybindingsPlatform(),
  appliedActionIds: ReadonlySet<string> = new Set(),
  characterKeyTriggers = true,
): ValidatedOverrides {
  const warnings: ValidationWarning[] = [];
  const reserved = RESERVED_BY_PLATFORM[platform];

  interface Candidate {
    rule: OverrideRule;
    chord: KeySequence | null;
  }
  const candidates: Candidate[] = [];
  // The payload resolves same-action repeats last-rule-wins; the candidate
  // list keeps only the winning rule.
  const pushCandidate = (candidate: Candidate): void => {
    const prior = candidates.findIndex((c) => c.rule.action === candidate.rule.action);
    if (prior !== -1) candidates.splice(prior, 1);
    candidates.push(candidate);
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
      pushCandidate({ rule, chord: null });
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
    pushCandidate({ rule, chord: sequence });
  }

  const live = new Map<string, EffectiveBinding[]>();
  for (const binding of registry.getState().bindings) {
    const list = live.get(binding.actionId) ?? [];
    list.push({ scope: binding.scope, sequence: binding.chord });
    live.set(binding.actionId, list);
  }
  const dropped = new Set<string>();
  for (const action of appliedActionIds) {
    if (!candidates.some((c) => c.rule.action === action)) dropped.add(action);
  }

  // Conflict resolution on the final simulated map, iterated to a fixpoint:
  // skipping a rule converts its action to a dropped restore (when it was
  // overridden), which can surface a further conflict. Each pass skips at
  // least one rule, so the loop is bounded by the candidate count; the guard
  // break covers the production-unreachable restore-vs-foreign-binding
  // collision, which reconcile's rollback + notification containment handle.
  let guard = candidates.length + 1;
  for (;;) {
    const final = new Map<string, EffectiveBinding[]>();
    for (const [action, bindings] of live) final.set(action, [...bindings]);
    for (const action of dropped) {
      final.set(
        action,
        defaultBindingShapesForAction(action, { characterKeyTriggers }).map((shape) => ({
          scope: shape.scope,
          sequence: shape.sequence,
        })),
      );
    }
    for (const candidate of candidates) {
      const defaultInput = DEFAULT_BINDINGS.find((b) => b.actionId === candidate.rule.action);
      if (defaultInput === undefined) continue;
      final.set(
        candidate.rule.action,
        candidate.chord === null ? [] : [{ scope: defaultInput.scope ?? GLOBAL_SCOPE, sequence: candidate.chord }],
      );
    }
    // Each binding's first overlapping claimant in final-map order, so
    // conflict pairs always name the earliest claimant first.
    const claimants: { action: string; binding: EffectiveBinding }[] = [];
    const conflicts: [string, string][] = [];
    for (const [action, bindings] of final) {
      for (const binding of bindings) {
        const overlapping = claimants.find(
          (c) =>
            c.action !== action &&
            c.binding.scope === binding.scope &&
            chordsOverlap(c.binding.sequence, binding.sequence),
        );
        if (overlapping === undefined) claimants.push({ action, binding });
        else conflicts.push([overlapping.action, action]);
      }
    }
    if (conflicts.length === 0 || guard-- <= 0) break;
    let skipped = false;
    for (const [first, second] of conflicts) {
      const indexFirst = candidates.findIndex((c) => c.rule.action === first);
      const indexSecond = candidates.findIndex((c) => c.rule.action === second);
      let skip = -1;
      let conflictWith = "";
      if (indexFirst !== -1 && indexSecond !== -1) {
        // Both rules claim the chord: the later rule loses.
        skip = Math.max(indexFirst, indexSecond);
        conflictWith = skip === indexFirst ? second : first;
      } else if (indexFirst !== -1) {
        skip = indexFirst;
        conflictWith = second;
      } else if (indexSecond !== -1) {
        skip = indexSecond;
        conflictWith = first;
      }
      if (skip === -1) continue;
      const removed = candidates.splice(skip, 1)[0];
      if (removed === undefined) continue;
      const removedDefault = DEFAULT_BINDINGS.find((b) => b.actionId === removed.rule.action);
      const scope = removedDefault?.scope ?? GLOBAL_SCOPE;
      warnings.push({
        rule: removed.rule,
        reason: "conflict",
        conflictWith,
        message: `chord "${removed.rule.chord ?? ""}" in scope "${scope}" is already bound by "${conflictWith}"`,
      });
      if (appliedActionIds.has(removed.rule.action)) dropped.add(removed.rule.action);
      skipped = true;
    }
    if (!skipped) break;
  }

  return {
    rules: candidates.map((candidate) => ({ action: candidate.rule.action, chord: candidate.chord })),
    warnings,
  };
}
