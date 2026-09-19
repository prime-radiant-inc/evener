// White-box hooks into the reducer, for tests only.
//
// chunkViewBackingForTests reports chunkview.ts's internal chunk storage so a
// test can assert the view never copies it. That is a test hook rather than
// protocol API, so index.ts deliberately leaves it out and it is published
// here instead, with the rest of the package's non-shipped test support.
export { chunkViewBackingForTests } from "../chunkview";
