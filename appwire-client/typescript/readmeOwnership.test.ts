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
// The README's text is loaded through Vite's `?raw` import: the package
// declares no @types/node, so node:fs is not an option in its tests.
import { expect, test } from "vitest";
import readme from "./README.md?raw";

test("the README's ownership summary does not assign mutation reconciliation to applications", () => {
  expect(readme).not.toMatch(/applications own[^.]*\bmutation reconciliation\b/i);
});

test("the README's ownership summary credits the package with the reusable mutation policy", () => {
  expect(readme).toMatch(/\breusable mutation policy\b/);
  expect(readme).toMatch(/\bMutationDispatcher\b/);
});
