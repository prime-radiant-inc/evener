// The published README's closing ownership summary is a contract a consumer
// plans against, so it must agree with where the ownership actually sits.
// It once read "Applications own ... and mutation reconciliation", which
// contradicts the extracted state/mutation layer (audit UI-05, #2406): the
// package ships the durable records, the outbox's discovery half, the pure
// reconciliation and the `MutationDispatcher` that reconcile identities and
// restore proven-absent records, while the applications keep the transport,
// the platform's storage adapter and the authoritative-read/lifecycle
// orchestration that drives the dispatcher.
//
// The assertion anchors on the ownership sentence itself: the list of
// responsibilities that sentence hands to applications must not name mutation
// reconciliation. It is an absence check with no positive phrase match, so an
// unrelated README rewording cannot turn a docs edit red. The README is read
// through Vite's `?raw` import because the package declares no @types/node, so
// node:fs is not an option in its tests.
import { expect, test } from "vitest";
import readme from "./README.md?raw";

test("the README's ownership sentence does not assign mutation reconciliation to applications", () => {
  const ownership = /Applications own[^.]*\./.exec(readme.replace(/\s+/g, " "));
  expect(ownership, "the README must still name what applications own").not.toBeNull();
  expect(ownership?.[0]).not.toMatch(/mutation reconciliation/i);
});
