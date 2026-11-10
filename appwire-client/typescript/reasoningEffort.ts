// Shared vocabulary for reasoning-effort chips and pickers, so every surface
// (the web's session status row, command palette and spawn form; native's
// composer settings and its slash command) speaks one language:
// "" is "(default)" (the session default applies), and "none" is an explicit
// off the user chose — labelled by whether the model can actually express it.

import { EFFORT_TIERS, FALLBACK_EFFORT_LEVELS } from "./reasoningEffort.gen";

export { FALLBACK_EFFORT_LEVELS };

export function effortLabel(level: string, levels: string[]): string {
  if (level === "") return "(default)";
  if (level === "none") {
    // Only a model whose ladder lists a none level can actually turn
    // thinking off; elsewhere an explicit none omits the field and the
    // provider's own default applies.
    return levels.includes("none") ? "none (off)" : "none (provider default)";
  }
  return level;
}

// The effort lattice ranks the daemon's six reasoning-effort tiers, least to
// most. The tiers come from the generated mirror of llm/effort (#5553), so a
// tier added or re-ranked there reaches every picker with no second edit.
// "none" is an off, not a tier, and a level outside the lattice carries no
// rank.
const EFFORT_RANK: Record<string, number> = Object.fromEntries(EFFORT_TIERS.map((level, index) => [level, index + 1]));

// Places level among levels at its rank. A picker must still display a level
// the ladder omits (the session or draft already holds it), but where its
// rank reads: appended after a max-topped ladder, a current xhigh would show
// below max. A level the ladder already lists keeps its place; a level with
// no rank keeps the position at the end.
export function insertEffortLevel(levels: string[], level: string): string[] {
  if (levels.includes(level)) return levels;
  const rank = EFFORT_RANK[level];
  if (rank === undefined) return [...levels, level];
  const at = levels.findIndex((candidate) => {
    const candidateRank = EFFORT_RANK[candidate];
    return candidateRank !== undefined && candidateRank > rank;
  });
  if (at === -1) return [...levels, level];
  return [...levels.slice(0, at), level, ...levels.slice(at)];
}

// The option values a live-session effort picker offers: a "(default)" head,
// the model's ladder as the model orders it (a ladder-listed "none" rides in
// place), and the current value inserted at its rank when the session already
// runs at a level the ladder omits — the select must be able to display it.
export function effortOptionLevels(levels: string[], current: string): string[] {
  return ["", ...(current !== "" ? insertEffortLevel(levels, current) : levels)];
}

// The session picker uses this ladder when reasoning is supported but the
// provider does not enumerate levels. The daemon clamps the requested value.
export function sessionEffortLevels(levels: string[] | undefined, supportsReasoning: boolean | undefined): string[] {
  if (levels?.length) return levels;
  return supportsReasoning ? [...FALLBACK_EFFORT_LEVELS] : [];
}
