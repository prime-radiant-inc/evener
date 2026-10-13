// The conditions the sessioncacheguard's waits accept, as page source.
//
// They live apart from run.mjs because run.mjs auto-runs its scenarios on
// import, so a test cannot import it; the unit test beside the guard imports
// these and evaluates them against representative snapshot shapes, pinning
// that each means what the guard needs.
//
// Both are page source - interpolated into the expression sent to the page -
// so neither may name a Node value.

// A tab that has suppressed the ref under test: a lifetime carrying
// suppressed:true, and no lifetime counts as not suppressed.
export const SUPPRESSED = `(state) => state.lifetime?.suppressed === true`;

// A tab that has learned the other tab's sourceId from its own deletion
// broadcast and suppressed the sibling's ref.
export const SOURCE_DISCOVERED =
  `(state) => typeof state.sourceId === "string" && state.sourceId.length > 0 && ` +
  `state.lifetime?.suppressed === true`;
