// Tests-first checkpoint for the retained reader work (Task 5).
//
// `./documentReadState` is the production module this checkpoint precedes, so
// the static import below deliberately does NOT resolve yet: the file is a
// collection failure until the source grant lands, and `tsc` reports the
// missing module instead of this file hiding it behind an optional call, a
// skip or a stub. Every body below is complete against the approved interface
// so the unchanged tests run once the module exists.
//
// All persistence runs through the real fixture repository, runtime and
// IndexedDB storage; the shared comment helpers are the accepted ones. The
// fixture already SAVED op-1 with a pending handoff, so every test settles it
// against the real source before observing, and every capture/lease pair comes
// from that same accepted fixture operation (never a lease from one read with a
// capture from another).
import {
  type AnnotationCommentWork,
  type DestinationProvenance,
  type DocumentKey,
  editCommentText,
  invalidateCommentCapture,
  type PreparedAnnotation,
  selectCommentPassage,
} from "@evener/appwire-client";
import { expect, test, vi } from "vitest";
import { createDocumentAnnotationRuntime, type DocumentAnnotationPorts } from "../../stores/documentAnnotationRuntime";
import { ANNOTATION_PAYLOADS_STORE, type AnnotationPayloadRow } from "../../stores/localCompositionSchema";
import {
  disposeDocumentReaderWork,
  documentReaderWork,
  publishReader,
  retryDocumentReaders,
  saveReaderAnnotation,
  subscribeDocumentReaderWork,
} from "./documentReadState";
import { REFERENCE } from "./testing/annotationFixtures";
import {
  cleanupAfterTest,
  fixture,
  preparedAnnotationFromLease,
  rowsForTests,
} from "./testing/annotationRepositoryFixtures";

type Fixture = Awaited<ReturnType<typeof fixture>>;

// The whitespace note and its complete expected reference, declared here as
// literals independent of the production formatter (the note's JSON escape is
// spelled out). The reference context/revision are the fixture capture's own
// literal values.
const WHITESPACE_NOTE = "  Keep this\nand this  \n";
const WHITESPACE_REFERENCE =
  'A note on `NOTES.md:1:19-1:22` (captured source positions), selecting “same”: "  Keep this\\nand this  \\n"\n' +
  'Captured whole-file revision: "rev-A".\n' +
  'Context before: "First occurrence: "; after: "\\n\\nOther context for a second occurrence is nearby: same\\n".\n' +
  "Verify the quotation and context against current content before relying on these captured coordinates.";

// Real ports over the fixture's real repository. Registered AFTER the fixture's
// own disposers, so reverse-ordered cleanup disposes the runtime and readers
// before the fixture drops its repository/source/backing.
function openPorts(f: Fixture): DocumentAnnotationPorts {
  const runtime = createDocumentAnnotationRuntime(f.repo);
  cleanupAfterTest(() => runtime.dispose());
  return { repository: f.repo, runtime };
}

function openWork(viewerId: string, key: DocumentKey, ports: DocumentAnnotationPorts) {
  cleanupAfterTest(() => disposeDocumentReaderWork(viewerId));
  return documentReaderWork(viewerId, key, ports);
}

// The fixture saves op-1 but leaves its handoff pending; a later append would
// otherwise return the real `pending/dirty-draft` FIFO result. Settle it against
// the real source first and require the actual outcome.
async function settleBaseline(f: Fixture): Promise<void> {
  const settled = await f.repo.handoff(f.document, "op-1");
  if (settled.state !== "applied" && settled.state !== "already-applied")
    throw new Error(`baseline op-1 was not settled: ${settled.state}`);
}

// The reader's first Send allocates its operation id itself. On a non-secure
// remote HTTP origin `crypto.randomUUID` is absent while `getRandomValues`
// remains. This removes ONLY the platform randomUUID capability (an own-property
// shadow), leaves native getRandomValues untouched, and restores the exact
// descriptor through the fixture's reverse-ordered cleanup.
function installUnavailableRandomUUID(): void {
  const crypto = globalThis.crypto;
  const own = Object.getOwnPropertyDescriptor(crypto, "randomUUID");
  Object.defineProperty(crypto, "randomUUID", { value: undefined, configurable: true });
  cleanupAfterTest(() => {
    // An originally inherited property is restored by REMOVING this temporary own
    // shadow, not by pinning the inherited value as a new own property. An
    // originally own property is restored from its exact descriptor.
    if (own) Object.defineProperty(crypto, "randomUUID", own);
    else Reflect.deleteProperty(crypto, "randomUUID");
  });
}

// The same complete real save/handoff scenario, run against both the normal
// crypto capability and a non-secure origin where randomUUID is unavailable.
const CRYPTO_CASES = [
  { label: "randomUUID available", unavailable: false },
  { label: "randomUUID unavailable", unavailable: true },
] as const;

function payloadRows(f: Fixture): Promise<AnnotationPayloadRow[]> {
  return rowsForTests<AnnotationPayloadRow>(f.factory, f.databaseName, ANNOTATION_PAYLOADS_STORE);
}

test("retains comment, frozen capture, attempt and scroll for the same viewer across unsubscribe and remount", async () => {
  let failSave = false;
  const f = await fixture("reader-retain", {
    beforeCommit: (operation) => {
      if (operation === "save" && failSave) throw new Error("first-save fault");
    },
  });
  await settleBaseline(f);
  const ports = openPorts(f);
  const work = openWork("viewer-A", f.document, ports);
  const note = "Keep this\n<script>window.pwned=1</script> /same";
  work.lease = f.operation.lease;
  work.comment = selectCommentPassage(editCommentText(work.comment, note), f.operation.capture);
  work.scrollTop = 137;

  const listener = vi.fn();
  const unsubscribe = subscribeDocumentReaderWork("viewer-A", listener);
  unsubscribe();
  publishReader(work);
  expect(listener).not.toHaveBeenCalled();

  failSave = true;
  await saveReaderAnnotation(work, f.destination);
  expect(work.phase).toBe("unsaved");
  const attempted = work.attempted;
  if (!attempted) throw new Error("first attempt was not retained");

  // Same viewer and key return the SAME retained work; the host remount is only
  // a subscribe, never a disposal.
  expect(documentReaderWork("viewer-A", f.document, ports)).toBe(work);
  expect(work.comment.note).toBe(note);
  expect(work.comment.capture).toEqual(f.operation.capture);
  expect(work.comment.captureFrozen).toBe(true);
  expect(work.attempted).toEqual(attempted);
  expect(work.scrollTop).toBe(137);

  // A distinct viewer does not share editable work.
  const other = openWork("viewer-B", f.document, ports);
  expect(other).not.toBe(work);
  expect(other.comment).toEqual({ note: "", captureFrozen: false });
  expect(other.attempted).toBeNull();
});

test("key replacement and committed disposal retire old work, while host unmount keeps the repository and authored text", async () => {
  const f = await fixture("reader-dispose");
  await settleBaseline(f);
  const ports = openPorts(f);
  const work = openWork("viewer-K", f.document, ports);
  work.comment = editCommentText(work.comment, "authored while reading");

  // A render host subscribes on mount and unsubscribes on unmount; it never
  // disposes the shared work, repository or runtime.
  const unsubscribeHost = subscribeDocumentReaderWork("viewer-K", () => {});
  unsubscribeHost();
  await ports.runtime.whenIdle();
  expect(await f.repo.listAnnotations(f.document)).toHaveLength(1);
  expect(documentReaderWork("viewer-K", f.document, ports)).toBe(work);
  expect(work.comment.note).toBe("authored while reading");

  // Committed key replacement retires the old entry and its subscriptions.
  const oldListener = vi.fn();
  subscribeDocumentReaderWork("viewer-K", oldListener);
  const replacement: DocumentKey = { ...f.document, path: "OTHER.md" };
  const next = documentReaderWork("viewer-K", replacement, ports);
  expect(next).not.toBe(work);
  expect(next.comment).toEqual({ note: "", captureFrozen: false });
  publishReader(work);
  expect(oldListener).not.toHaveBeenCalled();

  const newListener = vi.fn();
  subscribeDocumentReaderWork("viewer-K", newListener);
  publishReader(next);
  expect(newListener).toHaveBeenCalledTimes(1);

  // Explicit committed disposal removes the entry and its subscription.
  disposeDocumentReaderWork("viewer-K");
  publishReader(next);
  expect(newListener).toHaveBeenCalledTimes(1);
  expect(() => subscribeDocumentReaderWork("viewer-K", () => {})).toThrow("reader work not created");
});

test("publication reaches current subscribers, skips removed ones and reports same-identity changes", async () => {
  const f = await fixture("reader-listen");
  await settleBaseline(f);
  const ports = openPorts(f);
  const work = openWork("viewer-listen", f.document, ports);

  // The subscriber reads the CURRENT note on every publication, so a mutation
  // on the same work object is visible rather than memoized away.
  const notes: string[] = [];
  const unsubscribe = subscribeDocumentReaderWork("viewer-listen", () => notes.push(work.comment.note));
  publishReader(work);
  work.comment = editCommentText(work.comment, "changed");
  publishReader(work);
  expect(notes).toEqual(["", "changed"]);

  unsubscribe();
  publishReader(work);
  expect(notes).toEqual(["", "changed"]);

  // Subscribing with no work is a real error, not a silent no-op.
  expect(() => subscribeDocumentReaderWork("viewer-absent", () => {})).toThrow("reader work not created");
});

for (const cryptoCase of CRYPTO_CASES) {
  test(`one Send creates one immutable operation, saves before handoff, and persists the literal note (${cryptoCase.label})`, async () => {
    const order: string[] = [];
    let recording = false;
    const f = await fixture(`reader-send-${cryptoCase.unavailable ? "no-uuid" : "uuid"}`, {
      beforeCommit: (operation) => {
        if (recording) order.push(operation);
      },
      onAppendReadsIssued: () => {
        if (recording) order.push("append-reads");
      },
    });
    await settleBaseline(f);
    // Install and register the capability override AFTER the fixture and its
    // baseline settle, but BEFORE the runtime/reader hosts, so reverse-ordered
    // cleanup retires the runtime/readers before this crypto shadow is restored.
    if (cryptoCase.unavailable) installUnavailableRandomUUID();
    // Prove the capability arrangement for this variant: only the platform
    // randomUUID is gone; native getRandomValues is untouched.
    expect(typeof crypto.randomUUID).toBe(cryptoCase.unavailable ? "undefined" : "function");
    expect(typeof crypto.getRandomValues).toBe("function");

    const baseline = await f.drafts.load(f.key);
    if (!baseline) throw new Error("missing fixture baseline draft");
    const ports = openPorts(f);
    const work = openWork(`viewer-send-${cryptoCase.unavailable ? "no-uuid" : "uuid"}`, f.document, ports);
    const saves = vi.spyOn(f.repo, "saveAnnotation");

    work.lease = f.operation.lease;
    work.comment = selectCommentPassage(editCommentText(work.comment, "Keep this scope."), f.operation.capture);
    recording = true;
    await saveReaderAnnotation(work, f.destination);

    // The durable save boundary lands before the draft handoff boundary.
    expect(order).toEqual(["save", "append-reads"]);
    expect(saves).toHaveBeenCalledTimes(1);
    const operation = saves.mock.calls.at(0)?.[0];
    if (!operation) throw new Error("saveAnnotation was not called");
    expect(operation.note).toBe("Keep this scope.");
    expect(operation.capture).toEqual(f.operation.capture);
    // The lease the operation carries is the fixture's accepted pair for that capture.
    expect(operation.lease).toEqual(f.operation.lease);
    expect(operation.destination).toEqual(f.destination);
    expect(operation.lease.key).toEqual(f.document);
    // The first Send allocated a real, nonempty UUID-shaped operation id.
    expect(operation.operationId).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/);

    // Exactly one immutable payload row, equal to the captured argument.
    const payloads = await payloadRows(f);
    const saved = payloads.filter((row) => row.operation.operationId === operation.operationId);
    expect(saved).toHaveLength(1);
    const [savedPayload] = saved;
    if (!savedPayload) throw new Error("saved payload missing");
    expect(savedPayload.operation).toEqual(operation);
    expect(
      (await f.repo.listAnnotations(f.document)).filter((view) => view.receipt.operationId === operation.operationId),
    ).toHaveLength(1);

    // Handoff appended exactly one additional literal reference over the settled
    // baseline, preserving the draft's structured fields.
    const appended = await f.drafts.load(f.key);
    expect(appended?.text).toBe(`${baseline.text}\n\n${REFERENCE}`);
    expect(appended?.skillNames).toEqual(baseline.skillNames);
    expect(appended?.mentions).toEqual(baseline.mentions);
    // The live composer source carries the same appended value with its
    // structured skill/command/mention values preserved.
    const live = f.source.getSnapshot();
    expect(live.text).toBe(`${baseline.text}\n\n${REFERENCE}`);
    expect(live.skillNames).toEqual(["same"]);
    expect(live.commandNames).toEqual([]);
    expect(live.mentions).toEqual([{ kind: "skill", name: "same", offset: 9 }]);
    expect(work.attempted).toBeNull();
    expect(work.phase).toBe("reading");

    // A later edit cannot mutate the already-persisted immutable operation.
    work.comment = editCommentText(work.comment, "changed after send");
    const reread = (await payloadRows(f)).find((row) => row.operation.operationId === operation.operationId);
    expect(reread?.operation).toEqual(operation);
  });
}

test("blank note, absent capture or absent lease cannot save", async () => {
  const f = await fixture("reader-invalid");
  await settleBaseline(f);
  const ports = openPorts(f);
  const work = openWork("viewer-invalid", f.document, ports);
  const saves = vi.spyOn(f.repo, "saveAnnotation");

  // absent lease, absent capture, blank note
  work.comment = { note: "   ", captureFrozen: false };
  await saveReaderAnnotation(work, f.destination);
  // ABSENT lease, present capture, valid note: isolates the lease guard alone.
  work.comment = { note: "Keep this scope.", capture: f.operation.capture, captureFrozen: true };
  await saveReaderAnnotation(work, f.destination);
  // present lease, absent capture, blank note
  work.lease = f.operation.lease;
  work.comment = { note: " \n\t ", captureFrozen: false };
  await saveReaderAnnotation(work, f.destination);
  // present lease and capture, whitespace-only note
  work.comment = { note: "\n\t ", capture: f.operation.capture, captureFrozen: true };
  await saveReaderAnnotation(work, f.destination);
  // present lease and valid note, absent capture: isolates the capture guard alone.
  work.comment = { note: "Keep this scope.", captureFrozen: true };
  await saveReaderAnnotation(work, f.destination);

  expect(work.attempted).toBeNull();
  expect(saves).not.toHaveBeenCalled();
  expect((await f.repo.listAnnotations(f.document)).map((view) => view.receipt.operationId)).toEqual(["op-1"]);
});

test("persists the literal whitespace and newlines of a valid note without trimming", async () => {
  const f = await fixture("reader-whitespace");
  await settleBaseline(f);
  const baseline = await f.drafts.load(f.key);
  if (!baseline) throw new Error("missing fixture baseline draft");
  const ports = openPorts(f);
  const work = openWork("viewer-ws", f.document, ports);
  work.lease = f.operation.lease;
  work.comment = selectCommentPassage(editCommentText(work.comment, WHITESPACE_NOTE), f.operation.capture);
  await saveReaderAnnotation(work, f.destination);

  const notes = (await payloadRows(f)).map((row) => row.operation.note);
  expect(notes).toContain(WHITESPACE_NOTE);
  // The complete expected draft is an independently declared literal; the note's
  // JSON escape is spelled out rather than derived from the formatter.
  const appended = await f.drafts.load(f.key);
  expect(appended?.text).toBe(`${baseline.text}\n\n${WHITESPACE_REFERENCE}`);
  expect(appended?.skillNames).toEqual(baseline.skillNames);
  expect(appended?.mentions).toEqual(baseline.mentions);
  // The live composer source reflects the full appended value and preserves the
  // structured skill/command/mention values.
  const live = f.source.getSnapshot();
  expect(live.text).toBe(`${baseline.text}\n\n${WHITESPACE_REFERENCE}`);
  expect(live.skillNames).toEqual(["same"]);
  expect(live.commandNames).toEqual([]);
  expect(live.mentions).toEqual([{ kind: "skill", name: "same", offset: 9 }]);
});

test("a failed first save retains its exact attempt and reconciles on an approved retry without a second Send", async () => {
  let failSave = false;
  const f = await fixture("reader-retry", {
    beforeCommit: (operation) => {
      if (operation === "save" && failSave) throw new Error("first-save fault");
    },
  });
  await settleBaseline(f);
  const ports = openPorts(f);
  const work = openWork("viewer-retry", f.document, ports);
  const saves = vi.spyOn(f.repo, "saveAnnotation");
  const baseline = await f.drafts.load(f.key);
  if (!baseline) throw new Error("missing fixture baseline draft");

  work.lease = f.operation.lease;
  work.comment = selectCommentPassage(editCommentText(work.comment, "Keep this scope."), f.operation.capture);
  failSave = true;
  await saveReaderAnnotation(work, f.destination);

  expect(work.phase).toBe("unsaved");
  const attempted = work.attempted;
  if (!attempted) throw new Error("first attempt was not retained");
  expect(attempted.note).toBe("Keep this scope.");
  expect(attempted.capture).toEqual(f.operation.capture);
  expect(work.comment.note).toBe("Keep this scope.");
  expect((await f.drafts.load(f.key))?.text).toBe(baseline.text);
  expect((await payloadRows(f)).filter((row) => row.operation.operationId === attempted.operationId)).toHaveLength(0);

  // The approved retry trigger reconciles the real receipt; no second Send.
  const savesBeforeRetry = saves.mock.calls.length;
  failSave = false;
  await retryDocumentReaders("storage-recovered");

  expect(work.attempted).toBeNull();
  expect(work.phase).toBe("reading");
  expect(saves.mock.calls.length).toBe(savesBeforeRetry + 1);
  expect(saves.mock.calls.at(-1)?.[0]).toEqual(attempted);
  const payloads = (await payloadRows(f)).filter((row) => row.operation.operationId === attempted.operationId);
  expect(payloads).toHaveLength(1);
  const [retriedPayload] = payloads;
  if (!retriedPayload) throw new Error("retried payload missing");
  expect(retriedPayload.operation).toEqual(attempted);
  const view = (await f.repo.listAnnotations(f.document)).find(
    (entry) => entry.receipt.operationId === attempted.operationId,
  );
  expect(view?.handoff).toMatchObject({ state: "already-applied" });
  expect((await f.drafts.load(f.key))?.text).toBe(`${baseline.text}\n\n${REFERENCE}`);
});

test("a reconciled receipt clears only its original unchanged comment, preserving a newer comment", async () => {
  let failSave = false;
  const f = await fixture("reader-late", {
    beforeCommit: (operation) => {
      if (operation === "save" && failSave) throw new Error("first-save fault");
    },
  });
  await settleBaseline(f);
  const ports = openPorts(f);
  const work = openWork("viewer-late", f.document, ports);

  work.lease = f.operation.lease;
  work.comment = selectCommentPassage(editCommentText(work.comment, "Keep this scope."), f.operation.capture);
  failSave = true;
  await saveReaderAnnotation(work, f.destination);
  const attempted = work.attempted;
  if (!attempted) throw new Error("first attempt was not retained");

  // The reader authors a NEWER comment before the retry receipt arrives.
  work.comment = editCommentText(work.comment, "Newer exact\ncomment");
  failSave = false;
  await retryDocumentReaders("storage-recovered");

  // The original attempt is retired, but the newer comment is neither cleared
  // nor silently persisted.
  expect(work.attempted).toBeNull();
  expect(work.phase).toBe("unsaved");
  expect(work.comment.note).toBe("Newer exact\ncomment");
  const views = await f.repo.listAnnotations(f.document);
  expect(views.find((entry) => entry.receipt.operationId === attempted.operationId)?.handoff).toMatchObject({
    state: "already-applied",
  });
  expect(views.every((entry) => entry.operation.note !== "Newer exact\ncomment")).toBe(true);
});

test("an older real save receipt cannot retire a newer retained attempt (settlement identity)", async () => {
  // State-level settlement identity coverage only: the newer attempt is
  // installed directly at the real save-observation boundary, NOT through a
  // second real DOM Send. The later DocPane producer tests remain required.
  let failSave = false;
  let installed = false;
  let work: Awaited<ReturnType<typeof documentReaderWork>> | undefined;
  let newer: PreparedAnnotation | undefined;
  let newerWork: AnnotationCommentWork | undefined;
  const f = await fixture("reader-newer-attempt", {
    beforeCommit: (operation) => {
      if (operation !== "save") return;
      if (failSave) throw new Error("first-save fault");
      // The older operation's save is about to commit. Install the newer
      // immutable attempt into the same work slot so the real receipt that
      // follows meets a DIFFERENT attempted operation.
      if (!installed && work && newer && newerWork) {
        installed = true;
        work.attempted = newer;
        work.attemptedComment = newerWork;
        work.comment = newerWork;
        work.phase = "saving";
      }
    },
  });
  await settleBaseline(f);
  const ports = openPorts(f);
  work = openWork("viewer-newer", f.document, ports);
  work.lease = f.operation.lease;
  work.comment = selectCommentPassage(editCommentText(work.comment, "Keep this scope."), f.operation.capture);
  failSave = true;
  await saveReaderAnnotation(work, f.destination);
  const older = work.attempted;
  if (!older) throw new Error("older attempt was not retained");

  // A newer immutable attempt from the same accepted lease pair (distinct
  // operation id), retained directly in the work slot.
  newer = preparedAnnotationFromLease(f.operation.lease, "reader-op-newer", "Newer exact\ncomment", f.destination);
  newerWork = { note: "Newer exact\ncomment", capture: newer.capture, captureFrozen: true };

  // Drive the SAME retained older attempt through saveReaderAnnotation again. It
  // captures its local operation before the first await, so the real save
  // completion reaches settleSavedAttempt with that exact OLD operation while the
  // work slot already holds the newer attempt -- exercising the identity guard.
  failSave = false;
  await saveReaderAnnotation(work, f.destination);

  // The beforeCommit hook installed the newer attempt at the real save boundary.
  expect(installed).toBe(true);
  // The older operation's real receipt committed...
  const olderPayload = (await payloadRows(f)).find((row) => row.operation.operationId === older.operationId);
  expect(olderPayload?.operation).toEqual(older);
  const olderView = (await f.repo.listAnnotations(f.document)).find(
    (entry) => entry.receipt.operationId === older.operationId,
  );
  expect(olderView?.handoff).toMatchObject({ state: "already-applied" });
  // ...but the identity guard refused to retire the newer attempt: object
  // identity, comment and phase survive untouched.
  expect(work.attempted).toBe(newer);
  expect(work.attemptedComment).toBe(newerWork);
  expect(work.comment).toBe(newerWork);
  expect(work.phase).toBe("saving");
});

test("accepted source invalidation retains the exact note while retiring the stale capture and attempt", async () => {
  let failSave = false;
  const f = await fixture("reader-invalidate", {
    beforeCommit: (operation) => {
      if (operation === "save" && failSave) throw new Error("first-save fault");
    },
  });
  await settleBaseline(f);
  const ports = openPorts(f);
  const work = openWork("viewer-invalidate", f.document, ports);
  const saves = vi.spyOn(f.repo, "saveAnnotation");

  work.lease = f.operation.lease;
  work.comment = selectCommentPassage(editCommentText(work.comment, "Retained note"), f.operation.capture);
  failSave = true;
  await saveReaderAnnotation(work, f.destination);
  if (!work.attempted) throw new Error("attempt was not retained");
  const savesBeforeInvalidation = saves.mock.calls.length;

  // LIMIT: this exercises the accepted-source-change RECIPE through the shared
  // helpers and the real runtime.invalidate, not the real DocPane source-change
  // path. The real accepted-source-change journey belongs to the later
  // DocPane.test.tsx checkpoint.
  ports.runtime.invalidate(work.key);
  work.attempted = null;
  work.attemptedComment = null;
  work.comment = invalidateCommentCapture(work.comment);
  expect(work.comment).toEqual({ note: "Retained note", captureFrozen: false });

  failSave = false;
  await retryDocumentReaders("reader-open");
  expect(saves.mock.calls.length).toBe(savesBeforeInvalidation);
  expect(work.comment.note).toBe("Retained note");
});

test("an unavailable origin saves pending-draft work instead of guessing a destination or submitting input", async () => {
  const f = await fixture("reader-unavailable");
  await settleBaseline(f);
  const ports = openPorts(f);
  const work = openWork("viewer-unavailable", f.document, ports);
  const enqueues = vi.spyOn(f.outbox, "enqueueIntent");
  const resends = vi.spyOn(f.outbox, "resendRecovery");
  const baselineText = (await f.drafts.load(f.key))?.text;

  const unavailable: DestinationProvenance = {
    kind: "browser-origin",
    paneId: "pane-A",
    compositionOwnerId: "owner-that-is-gone",
  };
  work.lease = f.operation.lease;
  work.comment = selectCommentPassage(editCommentText(work.comment, "Keep this scope."), f.operation.capture);
  await saveReaderAnnotation(work, unavailable);

  const saved = (await payloadRows(f)).find(
    (row) =>
      row.operation.destination.kind === "browser-origin" &&
      row.operation.destination.compositionOwnerId === "owner-that-is-gone",
  );
  if (!saved) throw new Error("annotation was not saved");
  // The EXACT supplied destination is persisted, never a guessed one.
  expect(saved.operation.destination).toEqual(unavailable);
  const view = (await f.repo.listAnnotations(f.document)).find(
    (entry) => entry.receipt.operationId === saved.operation.operationId,
  );
  expect(view?.handoff).toEqual({ state: "pending", reason: "owner-unavailable" });
  expect((await f.drafts.load(f.key))?.text).toBe(baselineText);
  expect(enqueues).not.toHaveBeenCalled();
  expect(resends).not.toHaveBeenCalled();
});
