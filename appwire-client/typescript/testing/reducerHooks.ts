// White-box hooks into the reducer, for tests only.
//
// chunkViewBackingForTests reports chunkview.ts's internal chunk storage so a
// test can assert the view never copies it. That is a test hook rather than
// protocol API, so index.ts deliberately leaves it out and it is published
// here instead, with the rest of the package's non-shipped test support.
//
// appendChunk itself is published here for the same reason: no production
// path calls it any more (the read model's overlay/delta folds a plain
// string append instead - see reducer.ts's applyOverlayDelta), but tests
// that want chunkview.ts's own O(1)-view properties (reasoningFormat.test.ts,
// chunkview.test.ts) still call it directly rather than through the reducer.
export { appendChunk, chunkViewBackingForTests } from "../chunkview";
