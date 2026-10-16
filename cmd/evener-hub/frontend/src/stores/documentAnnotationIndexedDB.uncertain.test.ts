// Uncertain-append coverage: an annotation append whose transaction COMMITS
// while its completion event never arrives. The handoff honestly answers
// pending, but the receipt is durably applied and the live composer still
// holds its pre-append text - the reconciliation on the next pass updates
// the unchanged composer from the committed backing, preserves a composer
// that moved on, and never appends twice. Every expected reference string is
// the literal REFERENCE from annotationFixtures.ts.

import { buildInput } from "@evener/appwire-client";
import { afterEach, expect, it, vi } from "vitest";
import { REFERENCE } from "../panes/doc/testing/annotationFixtures";
import { blobBytes, fixture } from "../panes/doc/testing/annotationRepositoryFixtures";
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

it("an unobserved completion reconciles staged attachments a peer replaced", async () => {
  const f = await fixture("uncertain-append-image-replacement");
  f.source.setActiveRecoveryId("rec-1");
  const hold = holdNextWriteTransaction(APPEND_STORES);
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const outcome = f.repo.handoff(f.document, "op-1");
  await hold.reached;
  await vi.advanceTimersByTimeAsync(10_001);
  expect(await outcome).toEqual({ state: "pending", reason: "owner-unavailable" });
  vi.useRealTimers();
  hold.release();
  // A peer tab replaces the recovery record's image while the completion
  // goes unobserved, building on the record the append committed.
  const replacement = buildInput(
    `recovery /plan [image 1]\n\n${REFERENCE}`,
    [{ marker: 1, name: "two.png", mediaType: "image/png", data: "Ag==" }],
    [],
    ["plan"],
    [{ kind: "command", name: "plan", offset: 9 }],
  );
  await f.outbox.updateRecoveryInput(
    "rec-1",
    replacement,
    [
      {
        presentationId: "image-2",
        marker: 1,
        name: "two.png",
        mediaType: "image/png",
        blob: new Blob([new Uint8Array([2])]),
      },
    ],
    `recovery /plan [image 1]\n\n${REFERENCE}`,
    [{ kind: "command", name: "plan", offset: 9 }],
  );
  // The retained replay reconciles the unchanged composer from the committed
  // backing: the published text is the peer's, and the staged attachments
  // must become the peer's replacement - not stay the image the composer
  // staged before the append.
  await f.repo.retryPending("storage-recovered");
  expect(f.source.getSnapshot().text).toBe(`recovery /plan [image 1]\n\n${REFERENCE}`);
  await f.source.whenSettled();
  // The next persistence must not write the superseded image back under the
  // newly adopted token.
  f.source.editor.write("typed after the reconcile", 24);
  await f.source.whenSettled();
  const record = await f.outbox.getRecovery("rec-1");
  if (!record) throw new Error("record missing after the reconcile");
  const image = (record.payload.input as Array<{ type: string; data?: string }>).find((item) => item.type === "image");
  expect(image?.data).toBe("Ag==");
  const attachment = record.attachments[0];
  if (!attachment) throw new Error("record attachment missing");
  expect(await blobBytes(attachment.blob)).toEqual(new Uint8Array([2]));
});

it("a faulted reconciliation read keeps the retry outcome pending", async () => {
  const f = await fixture("uncertain-append-faulted-backing");
  const hold = holdNextWriteTransaction(APPEND_STORES);
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const outcome = f.repo.handoff(f.document, "op-1");
  await hold.reached;
  await vi.advanceTimersByTimeAsync(10_001);
  expect(await outcome).toEqual({ state: "pending", reason: "owner-unavailable" });
  vi.useRealTimers();
  hold.release();
  expect(f.source.getSnapshot().text).toBe("ordinary /same");
  // The first pass's reconciliation read faults: the work stays retained, so
  // the outcome the runtime consumes must still report pending work instead
  // of clearing the shared retry intent while the composer is stale.
  const readAppendBacking = vi
    .spyOn(f.drafts, "readAppendBacking")
    .mockRejectedValueOnce(new Error("committed backing read fault"));
  await expect(f.repo.retryPendingWithOutcome("storage-recovered")).resolves.toEqual({ pending: true });
  expect(readAppendBacking).toHaveBeenCalledTimes(1);
  expect(f.source.getSnapshot().text).toBe("ordinary /same");
  // The next pass - the one the retained retry intent owes - completes the
  // reconciliation and reports no further pending work.
  await expect(f.repo.retryPendingWithOutcome("storage-recovered")).resolves.toEqual({ pending: false });
  expect(f.source.getSnapshot().text).toBe(`ordinary /same\n\n${REFERENCE}`);
  readAppendBacking.mockRestore();
});

it("a peer recovery update cannot tear the reconciliation's backing pair", async () => {
  const f = await fixture("uncertain-append-torn-pair");
  f.source.setActiveRecoveryId("rec-1");
  const hold = holdNextWriteTransaction(APPEND_STORES);
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const outcome = f.repo.handoff(f.document, "op-1");
  await hold.reached;
  await vi.advanceTimersByTimeAsync(10_001);
  expect(await outcome).toEqual({ state: "pending", reason: "owner-unavailable" });
  vi.useRealTimers();
  hold.release();
  const peerText = `recovery /plan [image 1]\n\n${REFERENCE}\n\npeer edit`;
  // The peer's recovery update commits while the reconciliation reads its
  // backing: today the row and the record are read in separate transactions,
  // so the adoption lands holding a token the row has already moved past. The
  // next persistence after the reconcile is refused under that stale token,
  // and the typed edit is lost while the composer and record diverge.
  const realLoad = f.drafts.load.bind(f.drafts);
  const heldLoad = vi.spyOn(f.drafts, "load").mockImplementationOnce(async (key) => {
    const row = await realLoad(key);
    await f.outbox.updateRecoveryInput(
      "rec-1",
      buildInput(
        peerText,
        [{ marker: 1, name: "one.png", mediaType: "image/png", data: "AQ==" }],
        [],
        ["plan"],
        [{ kind: "command", name: "plan", offset: 9 }],
      ),
      [
        {
          presentationId: "image-1",
          marker: 1,
          name: "one.png",
          mediaType: "image/png",
          blob: new Blob([new Uint8Array([1])]),
        },
      ],
      peerText,
      [{ kind: "command", name: "plan", offset: 9 }],
    );
    return row;
  });
  await f.repo.retryPending("storage-recovered");
  // The pair the reconciliation adopted must authorize the value it adopted:
  // the next persistence after the reconcile carries the adopted token.
  f.source.editor.write("typed after the reconcile", 24);
  await f.source.whenSettled();
  const record = await f.outbox.getRecovery("rec-1");
  if (!record) throw new Error("record missing after the reconcile");
  expect(record.composerText).toBe("typed after the reconcile");
  heldLoad.mockRestore();
});
