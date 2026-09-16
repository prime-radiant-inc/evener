// Which loaded navigation resources a hub invalidation target names, and what
// it obliges them to reach. `evener/navigation/invalidated` payloads and
// mutation receipts both carry `NavigationInvalidationTarget[]`; every store
// that folds them (the web's revalidator, native's paged readers and mutation
// readback) matches a target against a resource key by the one rule here, so a
// section, catalog, pin section or project target reaches the same set of
// loaded pages in every client. Pure: no requests, no store, no scheduler.
import type { NavigationInvalidationTarget } from "../../types.gen";
import { isProjectResource, type ResourceKey, targetBase } from "./types";

function matchesBase(key: ResourceKey, base: Partial<ResourceKey>): boolean {
  if (key.kind !== base.kind) {
    if (!(base.kind === "project" && key.kind === "project_page")) return false;
  }
  if (base.kind === "section" && key.kind === "section") return key.section === base.section;
  if (base.kind === "catalog" && key.kind === "catalog") return key.catalog === base.catalog;
  if (base.kind === "pin_section" && key.kind === "pin_section") return key.sectionId === base.sectionId;
  if (base.kind === "project" && (key.kind === "project" || key.kind === "project_page"))
    return key.projectKey === base.projectKey;
  return key.kind === base.kind;
}

/** True when `target` names the resource `key` identifies: same kind and
 * scope regardless of page offset, a project target also naming that
 * project's pages, and the `all_loaded_projects` wildcard naming every
 * project resource. A target outside the vocabulary names nothing. */
export function matchesTarget(key: ResourceKey, target: NavigationInvalidationTarget): boolean {
  if (target.kind === "all_loaded_projects") return isProjectResource(key);
  const base = targetBase(target);
  return base ? matchesBase(key, base) : false;
}

/** The targets among `targets` that name `key`, in their original order. */
export function matchingTargets(
  key: ResourceKey,
  targets: NavigationInvalidationTarget[],
): NavigationInvalidationTarget[] {
  return targets.filter((target) => matchesTarget(key, target));
}

/** The revision the resource `key` must have reached to reflect `targets`:
 * the highest revision any matching target announces, or zero when none
 * matches or none announces one (the wildcard never does). */
export function requiredRevision(key: ResourceKey, targets: NavigationInvalidationTarget[]): number {
  return Math.max(0, ...matchingTargets(key, targets).map((target) => target.revision ?? 0));
}

/** True when a notification's `sequence` skips past the last one accepted,
 * meaning an invalidation was missed. A repeat or a reordering is not a gap. */
export function isSequenceGap(lastSequence: number, sequence: number): boolean {
  return sequence > lastSequence + 1;
}
