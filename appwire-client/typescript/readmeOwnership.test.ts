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
// The check scans every sentence that names "Applications" and requires none
// of them to assign mutation reconciliation: an absence check that does not
// pin the subject wording, so a valid rewording ("Applications retain
// credentials ...") still passes, while a sentence handing reconciliation back
// to applications fails. The README is read through Vite's `?raw` import
// because the package declares no @types/node, so node:fs is not an option in
// its tests.
import { expect, test } from "vitest";
import readme from "./README.md?raw";

test("the README never assigns mutation reconciliation to applications", () => {
  const applicationsSentences = readme.replace(/\s+/g, " ").match(/[^.]*\bApplications\b[^.]*\./g) ?? [];
  expect(applicationsSentences.length, "the README must still name what applications own").toBeGreaterThan(0);
  for (const sentence of applicationsSentences) {
    expect(sentence).not.toMatch(/mutation reconciliation/i);
  }
});
