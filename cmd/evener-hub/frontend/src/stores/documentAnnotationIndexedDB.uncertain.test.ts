// Uncertain-append coverage: an annotation append whose transaction COMMITS
// while its completion event never arrives. The handoff honestly answers
// pending, but the receipt is durably applied and the live composer still
// holds its pre-append text - the reconciliation on the next pass updates
// the unchanged composer from the committed backing, preserves a composer
// that moved on, and never appends twice. Every expected reference string is
// the literal REFERENCE from annotationFixtures.ts.

import { afterEach, expect, it, vi } from "vitest";
import { REFERENCE } from "../panes/doc/testing/annotationFixtures";
import { fixture } from "../panes/doc/testing/annotationRepositoryFixtures";
import { APPEND_STORES } from "./browserDraftRepository";
import { holdNextWriteTransaction } from "./testing/stalledIndexedDB";

afterEach(() => {
  vi.useRealTimers();
});

it("a committed append with an unobserved completion reconciles its live composer", async () => {
  const f = await fixture("uncertain-append");
  // The append transaction commits, but its completion event stays held: the
  // drafts watchdog answers a storage fault for an outcome it could not
  // observe, so the handoff honestly reports pending.
  const hold = holdNextWriteTransaction(APPEND_STORES);
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const outcome = f.repo.handoff(f.document, "op-1");
  await hold.reached;
  await vi.advanceTimersByTimeAsync(10_001);
  expect(await outcome).toEqual({ state: "pending", reason: "owner-unavailable" });
  vi.useRealTimers();
  hold.release();
  // The commit is durable: the row carries the reference while the live
  // composer still holds its pre-append text.
  const committed = await f.drafts.load(f.key);
  if (!committed) throw new Error("committed row missing");
  expect(committed.text).toBe(`ordinary /same\n\n${REFERENCE}`);
  expect(f.source.getSnapshot().text).toBe("ordinary /same");
  // The retry sweep no longer skips the now-applied operation: the retained
  // replay reconciles the unchanged composer from the committed backing.
  await f.repo.retryPending("storage-recovered");
  expect(f.source.getSnapshot().text).toBe(`ordinary /same\n\n${REFERENCE}`);
  // The applied receipt is the duplicate guard: replaying the handoff and
  // the sweep again never appends a second reference.
  expect((await f.drafts.load(f.key))?.text).toBe(`ordinary /same\n\n${REFERENCE}`);
  expect((await f.repo.handoff(f.document, "op-1")).state).toBe("already-applied");
});

it("an unobserved completion reconciles a recovery-backed composer from its committed record", async () => {
  const f = await fixture("uncertain-append-recovery");
  f.source.setActiveRecoveryId("rec-1");
  const hold = holdNextWriteTransaction(APPEND_STORES);
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const outcome = f.repo.handoff(f.document, "op-1");
  await hold.reached;
  await vi.advanceTimersByTimeAsync(10_001);
  expect(await outcome).toEqual({ state: "pending", reason: "owner-unavailable" });
  vi.useRealTimers();
  hold.release();
  const committed = await f.outbox.getRecovery("rec-1");
  if (!committed) throw new Error("committed recovery record missing");
  expect(committed.composerText).toBe(`recovery /plan [image 1]\n\n${REFERENCE}`);
  expect(f.source.getSnapshot().text).toBe("recovery /plan [image 1]");
  await f.repo.retryPending("storage-recovered");
  expect(f.source.getSnapshot().text).toBe(`recovery /plan [image 1]\n\n${REFERENCE}`);
  expect((await f.outbox.getRecovery("rec-1"))?.composerText).toBe(`recovery /plan [image 1]\n\n${REFERENCE}`);
});

it("an unobserved completion never stomps a composer that moved on", async () => {
  const f = await fixture("uncertain-append-moved-on");
  const hold = holdNextWriteTransaction(APPEND_STORES);
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const outcome = f.repo.handoff(f.document, "op-1");
  await hold.reached;
  await vi.advanceTimersByTimeAsync(10_001);
  expect(await outcome).toEqual({ state: "pending", reason: "owner-unavailable" });
  vi.useRealTimers();
  hold.release();
  // The user switches the live composer's backing to the recovery draft
  // before the retry sweep runs: the newer composer state wins over the
  // reconciled ordinary backing - the reconcile must not publish the
  // appended value over it - while the committed reference and the
  // annotation's copyable listing survive untouched.
  f.source.setActiveRecoveryId("rec-1");
  await f.source.whenSettled();
  const committed = await f.drafts.load(f.key);
  if (!committed) throw new Error("committed row missing");
  expect(committed.text).toBe(`ordinary /same\n\n${REFERENCE}`);
  await f.repo.retryPending("storage-recovered");
  expect(f.source.getSnapshot().text).toBe("ordinary /same");
  expect((await f.drafts.load(f.key))?.text).toBe(`ordinary /same\n\n${REFERENCE}`);
  const views = await f.repo.listAnnotations(f.document);
  expect(views[0]?.handoff.state).toBe("already-applied");
});

it("an uncertain append that rolled back still completes through the sweep", async () => {
  // A one-shot fault inside the abortable window aborts the append BEFORE
  // commit: the receipt honestly stays pending, and the retained record must
  // not block the ordinary retry - the sweep re-runs the append, which
  // acknowledges the composer through the ordinary applied path.
  let armed = true;
  const f = await fixture("uncertain-append-rolled-back", {
    beforeAbortableCompletion: () => {
      if (armed) {
        armed = false;
        throw new Error("completion window fault");
      }
    },
  });
  expect((await f.repo.handoff(f.document, "op-1")).state).toBe("pending");
  await f.repo.retryPending("storage-recovered");
  expect(f.source.getSnapshot().text).toBe(`ordinary /same\n\n${REFERENCE}`);
  expect((await f.drafts.load(f.key))?.text).toBe(`ordinary /same\n\n${REFERENCE}`);
  expect((await f.repo.handoff(f.document, "op-1")).state).toBe("already-applied");
});
