// Canonical skill-selection state for the composer. Pure functions only -
// Composer.tsx owns the chips UI and draft persistence; these helpers are the
// one definition of what "selecting a skill" means for the selection list
// itself, so deduplication and removal behave identically whether a name
// arrives from an inline slash-completion choice, a recovery record, or a
// chip's remove button.
//
// A selection is always a CANONICAL catalog name (EvenerSkillInfo.name, the
// same name the wire's {type: "skill", name} input item carries - see
// docs/skills.md's "Canonical skill input on AppWire"). Selecting a skill
// never edits the draft's prose: the names ride their own list, and the wire
// assembles them as skill items AFTER the ordinary text/attachment items.

import { canonicalSkillNames } from "../../../protocol/composerInput";

/**
 * Appends a canonical name unless it is already selected (dedup by name). The
 * whole list is canonicalized, not just the incoming name: a selection list
 * that already carries a padded or empty entry (a corrupted draft, say) would
 * otherwise survive next to the canonical one and render as a duplicate chip.
 */
export function addSkillSelection(names: readonly string[], canonicalName: string): string[] {
  return canonicalSkillNames([...names, canonicalName]);
}

/**
 * Removes exactly the named selection; an absent name leaves the list as-is.
 * Canonicalized like addSkillSelection, for the same reason it promises to
 * behave identically: a padded or duplicated entry that add would have
 * collapsed must not survive the removal of its canonical name.
 */
export function removeSkillSelection(names: readonly string[], canonicalName: string): string[] {
  const target = canonicalName.trim();
  return canonicalSkillNames(names).filter((name) => name !== target);
}
