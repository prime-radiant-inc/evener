// Reader-authority regressions (new tests only, Task 5 checkpoint).
//
// Two source-traced defects in the browser document-annotation reader:
//
//  1. `DocPane`'s `captureEvidenceChanged` treats ANY fingerprint difference as a
//     confirmed source change. Two reads whose displayed prefixes overlap but
//     whose coverage differs (and whose whole revisions are equal/absent) are
//     `insufficient-coverage` under the shared `compareDisplayedSources` policy,
//     never a confirmed change. When `acceptRead` throws on such a read, the
//     reader retires the exact immutable operation/capture and the shared retry
//     owner's failed entry instead of retaining them.
//
//  2. `authoritySuspended` hides highlights/selection and the map-triggered
//     retry, but `onSend` still calls `saveReaderAnnotation` unconditionally. A
//     `beginRead`-only fault leaves the retained old lease matching the durable
//     head, so the mounted Send control can create and persist a NEW operation
//     from stale authority while the newest raw response was never accepted.
//
// Both cases run through the real reader, real repository, real runtime and real
// fake-IndexedDB storage; only the raw-file read and the AppWire transport are
// scripted. The production source is intentionally left unchanged: both cases
// are expected RED until a separate source grant lands.

import type { PreparedAnnotation } from "@evener/appwire-client";
import * as docContentModule from "@evener/appwire-client/docContent";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { IDBObjectStore as FDBObjectStore } from "fake-indexeddb";
import { beforeEach, expect, type MockInstance, test, vi } from "vitest";
import { ClientProvider } from "../../shell/clientContext";
import { publishPaneDocumentMoved } from "../../shell/paneDocumentMoves";
import { resetWorkspaceStoreForTests } from "../../shell/workspace";
import { connectionStore } from "../../stores/connection";
import { createDocumentAnnotationRepository } from "../../stores/documentAnnotationIndexedDB";
import { createDocumentAnnotationRuntime, type DocumentAnnotationPorts } from "../../stores/documentAnnotationRuntime";
import {
  ANNOTATION_EVIDENCE_STORE,
  ANNOTATION_EXPIRY_STORE,
  ANNOTATION_HEADS_STORE,
  ANNOTATION_NAMESPACES_STORE,
  ANNOTATION_OPERATIONS_STORE,
  ANNOTATION_PAYLOADS_STORE,
  type AnnotationEvidenceRow,
  type AnnotationExpiryRow,
  type AnnotationHeadRow,
  type AnnotationOperationRow,
  type AnnotationPayloadRow,
} from "../../stores/localCompositionSchema";
import { resetThreadsStoreForTests } from "../../stores/threads";
import type { BoundDocumentMap } from "../../widgets/markdown/documentSourceMap";
import * as documentSourceMapModule from "../../widgets/markdown/documentSourceMap";
import DocPane from "./DocPane";
import { type DocumentReaderWork, disposeDocumentReaderWork, documentReaderWork } from "./documentReadState";
import { FIRST, SOURCE } from "./testing/annotationFixtures";
import {
  blobBytes,
  cleanupAfterTest,
  docContent,
  expectStampedNoEarlierThan,
  fixture,
  rowsForTests,
} from "./testing/annotationRepositoryFixtures";

// Only the raw-file read is mocked; DocFileError / DOC_FILE_MAX_BYTES / the URL
// builder stay real so the pane's error-kind switch runs for real. Each test
// scripts its own reads through this fresh spy; the imported annotation
// fixture's afterEach restores every mock afterwards.
let mockRead: MockInstance<typeof docContentModule.readDocFile>;

beforeEach(() => {
  resetThreadsStoreForTests();
  resetWorkspaceStoreForTests();
  mockRead = vi.spyOn(docContentModule, "readDocFile");
});

type Fixture = Awaited<ReturnType<typeof fixture>>;

// ---------------------------------------------------------------------------
// Narrow local mount/observation/fault helpers. These build on the exported
// production-backed fixture only; no second repository or retry model is
// created. Helpers shared with DocPane.test.tsx are read there but re-expressed
// narrowly here because the frozen file exports none of them.
// ---------------------------------------------------------------------------

// Real ports over the fixture's real repository. Disposal stops further retry
// work first, then joins the runtime's current pass.
function annotationPorts(f: Fixture): {
  runtime: ReturnType<typeof createDocumentAnnotationRuntime>;
  ports: DocumentAnnotationPorts;
} {
  const runtime = createDocumentAnnotationRuntime(f.repo);
  cleanupAfterTest(async () => {
    runtime.dispose();
    await runtime.whenIdle();
  });
  return { runtime, ports: { repository: f.repo, runtime } };
}

// One connected transport built on the shared FakeClient, scripted only at the
// AppWire method boundary.
function connectFakeClient(): FakeClient {
  const client = new FakeClient("ready");
  const previous = connectionStore.getState();
  cleanupAfterTest(() => {
    connectionStore.setState(previous);
    client.close();
  });
  client.on("evener/command/list", () => ({ commands: [] }));
  connectionStore.getState().connect(client);
  return client;
}

function mountAnnotated(f: Fixture, ports: DocumentAnnotationPorts, client: FakeClient, paneId: string) {
  const host = render(
    <ClientProvider client={client}>
      <DocPane
        paneId={paneId}
        focused={true}
        params={{ session: "root", path: "NOTES.md", kind: "file", origin: f.destination }}
        annotations={ports}
      />
    </ClientProvider>,
  );
  cleanupAfterTest(() => disposeDocumentReaderWork(paneId));
  cleanupAfterTest(() => host.unmount());
  return host;
}

// The fixture saves op-1 with a pending handoff; settle it against the real
// source before observing.
async function settleBaseline(f: Fixture): Promise<void> {
  const settled = await f.repo.handoff(f.document, "op-1");
  if (settled.state !== "applied" && settled.state !== "already-applied")
    throw new Error(`baseline op-1 was not settled: ${settled.state}`);
}

const PRIMARY_POINTER = { pointerId: 1, isPrimary: true, pointerType: "mouse", button: 0, buttons: 1 } as const;

// A real Selection/Range over the FIRST paragraph's text node (source 18..22),
// with the normal primary-pointer fields. Offsets are text-node offsets; the
// expected source positions stay literal in the assertions. An optional
// container scopes the query so two mounted panes never collide on the same
// rendered text.
async function selectFirstPassage(container?: HTMLElement): Promise<void> {
  const scope = container ? within(container) : screen;
  const paragraph = await scope.findByText("First occurrence: same");
  fireEvent.pointerDown(paragraph, PRIMARY_POINTER);
  const node = paragraph.firstChild;
  if (!node || node.nodeType !== Node.TEXT_NODE) throw new Error("missing source text node");
  const range = document.createRange();
  range.setStart(node, 18);
  range.setEnd(node, 22);
  const selection = document.getSelection();
  if (!selection) throw new Error("missing selection");
  selection.removeAllRanges();
  selection.addRange(range);
  fireEvent.pointerUp(paragraph, PRIMARY_POINTER);
}

function payloadRowsFor(f: Fixture, operationId: string): Promise<PreparedAnnotation[]> {
  return rowsForTests<AnnotationPayloadRow>(f.factory, f.databaseName, ANNOTATION_PAYLOADS_STORE).then((rows) =>
    rows.filter((row) => row.operation.operationId === operationId).map((row) => row.operation),
  );
}

function operationRowsFor(f: Fixture, operationId: string): Promise<AnnotationOperationRow[]> {
  return rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE).then((rows) =>
    rows.filter((row) => row.operationId === operationId),
  );
}

async function recoveryAttachmentBytes(f: Fixture): Promise<Uint8Array | undefined> {
  const [record] = await f.outbox.listRecovery();
  const attachment = record?.attachments?.[0];
  return attachment?.blob ? blobBytes(attachment.blob) : undefined;
}

// Call-through observation of the REAL map DocumentMarkdown binds: the spy
// records each successful return and never replaces it.
function observeBoundMaps(): { maps: BoundDocumentMap[] } {
  const maps: BoundDocumentMap[] = [];
  const real = documentSourceMapModule.bindDocumentMap;
  vi.spyOn(documentSourceMapModule, "bindDocumentMap").mockImplementation((root, map) => {
    const bound = real(root, map);
    maps.push(bound);
    return bound;
  });
  return { maps };
}

// Call-through observation of the real read owners, forwarding complete
// argument tuples and recording only COMPLETED results.
function observeReadOwners(repo: Fixture["repo"]): {
  beginReads: Awaited<ReturnType<Fixture["repo"]["beginRead"]>>[];
  acceptances: Awaited<ReturnType<Fixture["repo"]["acceptRead"]>>[];
} {
  const beginReads: Awaited<ReturnType<Fixture["repo"]["beginRead"]>>[] = [];
  const acceptances: Awaited<ReturnType<Fixture["repo"]["acceptRead"]>>[] = [];
  const realBeginRead = repo.beginRead.bind(repo);
  vi.spyOn(repo, "beginRead").mockImplementation(async (...args) => {
    const ticket = await realBeginRead(...args);
    beginReads.push(ticket);
    return ticket;
  });
  const realAcceptRead = repo.acceptRead.bind(repo);
  vi.spyOn(repo, "acceptRead").mockImplementation(async (...args) => {
    const result = await realAcceptRead(...args);
    acceptances.push(result);
    return result;
  });
  return { beginReads, acceptances };
}

// Call-through observation of every real repository list read so a wake-driven
// refresh's React updates are joined inside act rather than escaping the test.
function observeListAnnotations(repo: Fixture["repo"]): Promise<unknown>[] {
  const reads: Promise<unknown>[] = [];
  const real = repo.listAnnotations.bind(repo);
  vi.spyOn(repo, "listAnnotations").mockImplementation((...args) => {
    const promise = real(...args);
    reads.push(promise);
    return promise;
  });
  return reads;
}

async function settleReaderRefreshes(reads: Promise<unknown>[]): Promise<void> {
  let seen = 0;
  let rounds = 0;
  while (seen < reads.length) {
    if (++rounds > 16) throw new Error("reader repository refreshes did not quiesce");
    const batch = reads.slice(seen);
    seen = reads.length;
    await Promise.all(batch);
    await Promise.resolve();
  }
}

// Read-only readiness boundary: the work holds a current non-null lease AND the
// real bound map names that lease's displayed snapshot.
async function waitForCurrentBoundMap(work: DocumentReaderWork, maps: BoundDocumentMap[]): Promise<BoundDocumentMap> {
  let bound: BoundDocumentMap | undefined;
  await waitFor(() => {
    bound = maps.at(-1);
    expect(work.lease).not.toBeNull();
    expect(bound?.identity.displayedSnapshotId).toBe(work.lease?.displayedSnapshotId);
  });
  if (!bound) throw new Error("current bound map not installed");
  return bound;
}

// A FRESH completion, not an earlier one. Boundaries are captured immediately
// before the recovery trigger: an acceptance recorded after `acceptBase` must be
// accepted, a map must be newly observed after `mapBase` for that fresh lease,
// and the LATEST installed map plus the current work must name that same lease.
async function waitForFreshAcceptedMap(
  acceptances: Awaited<ReturnType<Fixture["repo"]["acceptRead"]>>[],
  maps: BoundDocumentMap[],
  work: DocumentReaderWork,
  acceptBase: number,
  mapBase: number,
): Promise<Awaited<ReturnType<Fixture["repo"]["acceptRead"]>>> {
  let fresh: Awaited<ReturnType<Fixture["repo"]["acceptRead"]>> | undefined;
  await waitFor(() => {
    const candidates = acceptances.slice(acceptBase).filter((result) => result.state === "accepted");
    expect(candidates.length).toBeGreaterThan(0);
    fresh = candidates.at(-1);
    if (fresh?.state !== "accepted") throw new Error("no fresh accepted read");
    const leaseSnapshot = fresh.lease.displayedSnapshotId;
    expect(maps.slice(mapBase).some((map) => map.identity.displayedSnapshotId === leaseSnapshot)).toBe(true);
    expect(maps.at(-1)?.identity.displayedSnapshotId).toBe(leaseSnapshot);
    expect(work.lease?.displayedSnapshotId).toBe(leaseSnapshot);
  });
  if (fresh?.state !== "accepted") throw new Error("no fresh accepted read");
  return fresh;
}

// Platform fault injection at the REAL beginRead transaction boundary. The
// fake-IndexedDB object store is the real object the repository reads through,
// so throwing from its `get` rejects the real beginRead promise while the
// request is never created (a synchronous platform failure before request
// creation, NOT an asynchronous rollback). Unlike the covering test's one-shot
// seam this one stays armed until disarmed, so no intervening successful read
// can heal a suspended reader.
const BEGIN_READ_SCOPE = [ANNOTATION_NAMESPACES_STORE, ANNOTATION_HEADS_STORE].sort().join("|");

function faultBeginReadTransaction(armed: { value: boolean }): void {
  const realGet = FDBObjectStore.prototype.get;
  vi.spyOn(FDBObjectStore.prototype, "get").mockImplementation(function (
    this: IDBObjectStore,
    query: IDBValidKey | IDBKeyRange,
  ) {
    const scope = [...this.transaction.objectStoreNames].sort().join("|");
    if (armed.value && this.transaction.mode === "readwrite" && scope === BEGIN_READ_SCOPE) {
      throw new Error("annotation storage read fault");
    }
    return realGet.call(this, query);
  });
}

// The capture/operation the real selection builds: FIRST (18..22), rendered text
// "same", empty first-paragraph after-context, full SOURCE fingerprint.
const KEEP_NOTE = "Keep this scope.";
const KEEP_REFERENCE =
  'A note on `NOTES.md:1:19-1:22` (captured source positions), selecting “same”: "Keep this scope."\n' +
  'Captured whole-file revision: "rev-A".\n' +
  'Context before: "First occurrence: "; after: "".\n' +
  "Verify the quotation and context against current content before relying on these captured coordinates.";

// A different displayed PREFIX of the SAME underlying text: matching overlap over
// [0,40), different coverage, equal whole revision. This is the pair the shared
// comparator calls `insufficient-coverage`, never a confirmed change.
const NARROW_PREFIX = SOURCE.slice(0, 40);
if (!SOURCE.startsWith(NARROW_PREFIX)) throw new Error("narrow prefix must overlap the full source");

// ===========================================================================
// Important 1: matching overlap with different coverage must preserve retry
// evidence, not retire it as confirmed expiry.
// ===========================================================================
test("a failed first Send survives a coverage-differing reread whose acceptRead faults, retaining the exact operation, capture and shared retry owner until the same immutable operation applies once", async () => {
  let failSave = false;
  let failAccept = false;
  const f = await fixture("authority-coverage", {
    beforeCommit: (operation) => {
      if (operation === "save" && failSave) throw new Error("first-save fault");
      if (operation === "acceptRead" && failAccept) throw new Error("acceptance fault");
    },
  });
  await settleBaseline(f);
  const { ports } = annotationPorts(f);
  const client = connectFakeClient();
  mockRead.mockResolvedValue(docContent(SOURCE, "rev-A"));
  const { maps } = observeBoundMaps();
  const { acceptances } = observeReadOwners(f.repo);
  const listReads = observeListAnnotations(f.repo);
  const enqueues = vi.spyOn(f.outbox, "enqueueIntent");
  const resends = vi.spyOn(f.outbox, "resendRecovery");
  const callBaseline = client.calls.length;
  const baseline = await f.drafts.load(f.key);
  if (!baseline) throw new Error("missing baseline draft");
  const baselineOp1 = (await f.repo.listAnnotations(f.document)).find((v) => v.receipt.operationId === "op-1");
  if (!baselineOp1) throw new Error("missing baseline op-1 view");
  const liveBefore = f.source.getSnapshot();
  const recoveryBefore = await f.outbox.listRecovery();
  const attachmentBytesBefore = await recoveryAttachmentBytes(f);

  const firstHost = mountAnnotated(f, ports, client, "doc-authority-coverage");
  const work = documentReaderWork("doc-authority-coverage", f.document, ports);
  await screen.findByText("First occurrence: same");
  await waitForCurrentBoundMap(work, maps);

  // Create the failed first-save attempt through the REAL selection/edit/Send.
  await selectFirstPassage();
  expect(work.comment.capture).toBeDefined();
  fireEvent.change(await screen.findByRole("textbox"), { target: { value: KEEP_NOTE } });
  failSave = true;
  fireEvent.click(screen.getByRole("button", { name: "Send" }));
  await waitFor(() => expect(work.phase).toBe("unsaved"));
  const attempted = work.attempted;
  if (!attempted) throw new Error("first attempt not retained");
  const original = structuredClone(attempted);
  expect(original).toMatchObject({
    note: KEEP_NOTE,
    destination: f.destination,
    lease: { key: f.document },
    capture: { sourceRange: FIRST, selectedRenderedText: "same", contextAfter: "" },
  });
  expect(await payloadRowsFor(f, original.operationId)).toHaveLength(0);
  expect(await operationRowsFor(f, original.operationId)).toHaveLength(0);

  // A reread whose acceptance faults, carrying raw content with matching overlap
  // but different prefix coverage. The shared comparator reports
  // `insufficient-coverage`, never a confirmed change; the frozen capture and the
  // exact attempt must survive with an honest unsaved state.
  failAccept = true;
  mockRead.mockResolvedValue(docContent(NARROW_PREFIX, "rev-A", true));
  firstHost.unmount();
  mountAnnotated(f, ports, client, "doc-authority-coverage");
  await screen.findByText("Truncated"); // the scripted raw response really rendered

  // --- expected RED (production retires here) ---
  expect(work.attempted).toEqual(original);
  expect(work.lease).not.toBeNull();
  expect(work.comment.capture).toEqual(original.capture);
  expect(work.phase).toBe("unsaved");
  const textbox = await screen.findByRole("textbox");
  expect((textbox as HTMLTextAreaElement).value).toBe(KEEP_NOTE);
  expect(screen.getByRole("status").textContent).toBe("unsaved");
  fireEvent.change(textbox, { target: { value: "Edited after coverage fault." } });
  expect(work.comment.note).toBe("Edited after coverage fault.");
  fireEvent.change(textbox, { target: { value: KEEP_NOTE } });
  expect(work.comment.note).toBe(KEEP_NOTE);
  expect(await payloadRowsFor(f, original.operationId)).toHaveLength(0);
  expect(await operationRowsFor(f, original.operationId)).toHaveLength(0);

  // Remove the faults and recover with the original accepted content, through an
  // ordinary foreground reread. The retry of the ORIGINAL operation must return
  // the original accepted coverage/evidence, not a different accepted snapshot.
  failAccept = false;
  failSave = false;
  mockRead.mockResolvedValue(docContent(SOURCE, "rev-A"));
  const acceptBase = acceptances.length;
  const mapBase = maps.length;
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
  await act(async () => {
    fireEvent(document, new Event("visibilitychange"));
    await ports.runtime.whenIdle();
  });
  await waitFor(() => expect(work.attempted).toBeNull());
  const fresh = await waitForFreshAcceptedMap(acceptances, maps, work, acceptBase, mapBase);
  if (fresh.state !== "accepted") throw new Error("recovery read was not accepted");
  expect(fresh.lease.displayedSnapshotId).toBe(original.lease.displayedSnapshotId);
  expect(fresh.lease.epoch).toBe(original.lease.epoch);
  expect(fresh.lease.headRevision).toBe(original.lease.headRevision);
  await act(async () => {
    await ports.runtime.whenIdle();
    await settleReaderRefreshes(listReads);
  });
  await waitFor(async () =>
    expect(
      (await f.repo.listAnnotations(f.document)).find((v) => v.receipt.operationId === original.operationId)?.handoff
        .state,
    ).toBe("already-applied"),
  );
  // The original immutable operation persisted/applied exactly once, with its
  // original lease and literal note/capture.
  const persisted = (await f.repo.listAnnotations(f.document)).find(
    (v) => v.receipt.operationId === original.operationId,
  );
  expect(persisted?.operation).toEqual(original);
  expect(persisted?.receipt.epoch).toBe(original.lease.epoch);
  expect(persisted?.handoff.state).toBe("already-applied");
  expect(await payloadRowsFor(f, original.operationId)).toEqual([original]);
  expect(await operationRowsFor(f, original.operationId)).toHaveLength(1);
  const draftAfter = await f.drafts.load(f.key);
  if (!draftAfter) throw new Error("missing draft after recovery");
  expect(draftAfter.text.split(KEEP_REFERENCE)).toHaveLength(2); // exactly once
  expect(draftAfter).toEqual({
    ...baseline,
    text: `${baseline.text}\n\n${KEEP_REFERENCE}`,
    token: { ...baseline.token, sessionRevision: baseline.token.sessionRevision + 1 },
    editedAt: expect.any(Number),
  });
  expectStampedNoEarlierThan(draftAfter, baseline);
  // Baseline saved note, live composer metadata, recovery/attachment state and
  // zero ordinary submissions all survive.
  expect((await f.repo.listAnnotations(f.document)).find((v) => v.receipt.operationId === "op-1")).toEqual(baselineOp1);
  const liveAfter = f.source.getSnapshot();
  expect(liveAfter.text).toBe(draftAfter.text);
  expect(liveAfter.skillNames).toEqual(liveBefore.skillNames);
  expect(liveAfter.commandNames).toEqual(liveBefore.commandNames);
  expect(liveAfter.mentions).toEqual(liveBefore.mentions);
  expect(await f.outbox.listRecovery()).toEqual(recoveryBefore);
  expect(await recoveryAttachmentBytes(f)).toEqual(attachmentBytesBefore);
  expect(enqueues).not.toHaveBeenCalled();
  expect(resends).not.toHaveBeenCalled();
  const prohibited = new Set(["turn/start", "turn/steer", "turn/queue", "thread/start", "thread/resume"]);
  expect(client.calls.slice(callBaseline).filter((call) => prohibited.has(call.method))).toEqual([]);
  expect((await f.outbox.listOutbox()).map((row) => row.clientMutationId)).toEqual([]);
});

// ===========================================================================
// Important 2: suspended authority must block new Send while preserving editing.
// ===========================================================================
test("a beginRead fault suspends the reader, unavailable Send cannot create or persist a new operation, and a real Send after recovery saves exactly one annotation and appends its reference once", async () => {
  const f = await fixture("authority-suspend");
  await settleBaseline(f);
  const { ports } = annotationPorts(f);
  const client = connectFakeClient();
  mockRead.mockResolvedValue(docContent(SOURCE, "rev-A"));
  const { maps } = observeBoundMaps();
  const { acceptances } = observeReadOwners(f.repo);
  const listReads = observeListAnnotations(f.repo);
  const enqueues = vi.spyOn(f.outbox, "enqueueIntent");
  const resends = vi.spyOn(f.outbox, "resendRecovery");
  const callBaseline = client.calls.length;
  const baseline = await f.drafts.load(f.key);
  if (!baseline) throw new Error("missing baseline draft");
  const baselineOp1 = (await f.repo.listAnnotations(f.document)).find((v) => v.receipt.operationId === "op-1");
  if (!baselineOp1) throw new Error("missing baseline op-1 view");
  const liveBefore = f.source.getSnapshot();
  const recoveryBefore = await f.outbox.listRecovery();
  const attachmentBytesBefore = await recoveryAttachmentBytes(f);

  const firstHost = mountAnnotated(f, ports, client, "doc-authority-suspend");
  const work = documentReaderWork("doc-authority-suspend", f.document, ports);
  await screen.findByText("First occurrence: same");
  await waitForCurrentBoundMap(work, maps);

  // Under a genuinely accepted lease: select a passage and edit a note, never
  // send it. The old lease and frozen capture stay retained but uncommitted.
  await selectFirstPassage();
  fireEvent.change(await screen.findByRole("textbox"), { target: { value: KEEP_NOTE } });
  expect(work.attempted).toBeNull();
  expect(work.lease).not.toBeNull();
  expect(work.comment.capture).toMatchObject({ sourceRange: FIRST, selectedRenderedText: "same", contextAfter: "" });

  // A subsequent reread's beginRead faults at the real transaction boundary while
  // the raw read succeeds. Suspension remains active (the fault stays armed).
  const armed = { value: true };
  faultBeginReadTransaction(armed);
  cleanupAfterTest(() => {
    armed.value = false;
  });
  mockRead.mockResolvedValue(docContent(NARROW_PREFIX, "rev-A", true));
  const acceptedBefore = acceptances.length;
  firstHost.unmount();
  mountAnnotated(f, ports, client, "doc-authority-suspend");
  await screen.findByText("Truncated"); // the scripted raw response really rendered
  expect(acceptances.length).toBe(acceptedBefore); // the failed beginRead accepts nothing

  // Retained authority and edit survive the suspended render.
  expect(work.attempted).toBeNull();
  expect(work.lease).not.toBeNull();
  expect(work.comment.capture).toMatchObject({ sourceRange: FIRST, selectedRenderedText: "same", contextAfter: "" });

  // --- expected RED (Send is currently enabled) ---
  const send = screen.getByRole("button", { name: "Send" }) as HTMLButtonElement;
  expect(send.disabled).toBe(true);
  // The note stays editable and Cancel stays usable.
  const textbox = await screen.findByRole("textbox");
  expect((textbox as HTMLTextAreaElement).disabled).toBe(false);
  expect((screen.getByRole("button", { name: "Cancel" }) as HTMLButtonElement).disabled).toBe(false);
  fireEvent.change(textbox, { target: { value: "Edited while suspended." } });
  expect(work.comment.note).toBe("Edited while suspended.");
  fireEvent.change(textbox, { target: { value: KEEP_NOTE } });
  expect(work.comment.note).toBe(KEEP_NOTE);

  // The mounted Send control must create no immutable attempt, durable payload,
  // handoff receipt or draft append while authority is suspended.
  const saves = vi.spyOn(f.repo, "saveAnnotation");
  await act(async () => {
    fireEvent.click(send);
    await Promise.resolve();
  });
  expect(saves).not.toHaveBeenCalled();
  expect(work.attempted).toBeNull();
  expect(await f.drafts.load(f.key)).toEqual(baseline);
  expect((await f.repo.listAnnotations(f.document)).map((v) => v.receipt.operationId)).toEqual(["op-1"]);
  expect(await payloadRowsFor(f, "op-1")).toEqual([f.operation]);

  // Remove the fault and recover with an ordinary foreground reread. Wait for the
  // newly observed acceptance and its installed map before the real Send.
  armed.value = false;
  mockRead.mockResolvedValue(docContent(SOURCE, "rev-A"));
  const acceptBase = acceptances.length;
  const mapBase = maps.length;
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
  await act(async () => {
    fireEvent(document, new Event("visibilitychange"));
    await ports.runtime.whenIdle();
  });
  const fresh = await waitForFreshAcceptedMap(acceptances, maps, work, acceptBase, mapBase);
  if (fresh.state !== "accepted") throw new Error("recovery read was not accepted");
  expect((screen.getByRole("button", { name: "Send" }) as HTMLButtonElement).disabled).toBe(false);

  // A real Send now proves one correct durable annotation/reference and a
  // once-only draft append through the real lease and capture.
  fireEvent.click(screen.getByRole("button", { name: "Send" }));
  await waitFor(() => expect(saves).toHaveBeenCalled());
  const produced = saves.mock.calls.at(-1)?.[0];
  if (!produced) throw new Error("no produced operation");
  await waitFor(() => expect(work.attempted).toBeNull());
  await act(async () => {
    await ports.runtime.whenIdle();
    await settleReaderRefreshes(listReads);
  });
  await waitFor(async () =>
    expect(
      (await f.repo.listAnnotations(f.document)).find((v) => v.receipt.operationId === produced.operationId)?.handoff
        .state,
    ).toBe("already-applied"),
  );
  await act(async () => {
    await ports.runtime.whenIdle();
    await settleReaderRefreshes(listReads);
  });
  const persisted = (await f.repo.listAnnotations(f.document)).find(
    (v) => v.receipt.operationId === produced.operationId,
  );
  expect(persisted?.operation).toEqual(produced);
  expect(persisted?.handoff.state).toBe("already-applied");
  expect(await payloadRowsFor(f, produced.operationId)).toEqual([produced]);
  expect(await operationRowsFor(f, produced.operationId)).toHaveLength(1);
  const draftAfter = await f.drafts.load(f.key);
  if (!draftAfter) throw new Error("missing draft after recovery");
  expect(draftAfter.text.split(KEEP_REFERENCE)).toHaveLength(2); // exactly once
  expect(draftAfter).toEqual({
    ...baseline,
    text: `${baseline.text}\n\n${KEEP_REFERENCE}`,
    token: { ...baseline.token, sessionRevision: baseline.token.sessionRevision + 1 },
    editedAt: expect.any(Number),
  });
  expectStampedNoEarlierThan(draftAfter, baseline);
  expect((await f.repo.listAnnotations(f.document)).find((v) => v.receipt.operationId === "op-1")).toEqual(baselineOp1);
  const liveAfter = f.source.getSnapshot();
  expect(liveAfter.text).toBe(draftAfter.text);
  expect(liveAfter.skillNames).toEqual(liveBefore.skillNames);
  expect(liveAfter.commandNames).toEqual(liveBefore.commandNames);
  expect(liveAfter.mentions).toEqual(liveBefore.mentions);
  expect(await f.outbox.listRecovery()).toEqual(recoveryBefore);
  expect(await recoveryAttachmentBytes(f)).toEqual(attachmentBytesBefore);
  expect(enqueues).not.toHaveBeenCalled();
  expect(resends).not.toHaveBeenCalled();
  const prohibited = new Set(["turn/start", "turn/steer", "turn/queue", "thread/start", "thread/resume"]);
  expect(client.calls.slice(callBaseline).filter((call) => prohibited.has(call.method))).toEqual([]);
  expect((await f.outbox.listOutbox()).map((row) => row.clientMutationId)).toEqual([]);
});

// ---------------------------------------------------------------------------
// Expiry deletion through the real reader (roborev: expiry never deletes
// payloads in production). The fence invalidates the old epoch immediately,
// but the deletion contract completes only when finishExpiry runs: the
// reader owns the opportunistic call when its reread adopts the replacement
// epoch, and the retry pass's persisted sweep owns the retry after a fault.
// ---------------------------------------------------------------------------

test("a confirmed source change deletes the expired epoch's payload and evidence rows when the reread adopts the replacement", async () => {
  const f = await fixture("reader-expiry-deletion");
  await settleBaseline(f);
  const { ports } = annotationPorts(f);
  const client = connectFakeClient();
  mockRead.mockResolvedValue(docContent(SOURCE, "rev-A"));
  const { maps } = observeBoundMaps();
  const { acceptances } = observeReadOwners(f.repo);
  const listReads = observeListAnnotations(f.repo);
  mountAnnotated(f, ports, client, "doc-expiry-deletion");
  const work = documentReaderWork("doc-expiry-deletion", f.document, ports);
  await waitForCurrentBoundMap(work, maps);

  const headBefore = (await rowsForTests<AnnotationHeadRow>(f.factory, f.databaseName, ANNOTATION_HEADS_STORE))[0];
  const oldEpoch = headBefore?.epoch;
  const oldEvidenceKey = headBefore?.evidenceKey;
  if (!oldEpoch || !oldEvidenceKey) throw new Error("baseline head row missing");
  expect(
    (await rowsForTests<AnnotationPayloadRow>(f.factory, f.databaseName, ANNOTATION_PAYLOADS_STORE)).filter(
      (row) => row.epoch === oldEpoch,
    ).length,
  ).toBeGreaterThan(0);

  // A real changed source through the read boundary: the fence invalidates the
  // old epoch and the pane's own reread adopts the replacement.
  const acceptBase = acceptances.length;
  const mapBase = maps.length;
  mockRead.mockResolvedValue(docContent(`${SOURCE}CHANGED`, "rev-B"));
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
  await act(async () => {
    fireEvent(document, new Event("visibilitychange"));
    await ports.runtime.whenIdle();
  });
  const fresh = await waitForFreshAcceptedMap(acceptances, maps, work, acceptBase, mapBase);
  if (fresh.state !== "accepted") throw new Error("reread was not accepted");

  // The reread's acceptance settles the fence, and with it the expired epoch's
  // deletion: the receipt goes terminal and the payload and old evidence rows
  // are removed. The deletion's own wake is joined so no update escapes act.
  await act(async () => {
    await ports.runtime.whenIdle();
    await settleReaderRefreshes(listReads);
    const expiry = await rowsForTests<AnnotationExpiryRow>(f.factory, f.databaseName, ANNOTATION_EXPIRY_STORE);
    expect(expiry.map((row) => row.receipt.phase)).toEqual(["deleted"]);
    const payloads = await rowsForTests<AnnotationPayloadRow>(f.factory, f.databaseName, ANNOTATION_PAYLOADS_STORE);
    expect(payloads.filter((row) => row.epoch === oldEpoch)).toEqual([]);
    const evidence = await rowsForTests<AnnotationEvidenceRow>(f.factory, f.databaseName, ANNOTATION_EVIDENCE_STORE);
    expect(evidence.filter((row) => row.evidenceKey === oldEvidenceKey)).toEqual([]);
  });
});

test("a faulted expiry deletion keeps the receipt and the next retry pass completes it", async () => {
  let failDeletion = true;
  const f = await fixture("reader-expiry-deletion-fault", {
    beforeCommit: (operation) => {
      if (operation === "deleteExpiry" && failDeletion) throw new Error("delete fault");
    },
  });
  await settleBaseline(f);
  const { ports } = annotationPorts(f);
  const client = connectFakeClient();
  mockRead.mockResolvedValue(docContent(SOURCE, "rev-A"));
  const { maps } = observeBoundMaps();
  const { acceptances } = observeReadOwners(f.repo);
  const listReads = observeListAnnotations(f.repo);
  mountAnnotated(f, ports, client, "doc-expiry-deletion-fault");
  const work = documentReaderWork("doc-expiry-deletion-fault", f.document, ports);
  await waitForCurrentBoundMap(work, maps);

  const headBefore = (await rowsForTests<AnnotationHeadRow>(f.factory, f.databaseName, ANNOTATION_HEADS_STORE))[0];
  const oldEpoch = headBefore?.epoch;
  const oldEvidenceKey = headBefore?.evidenceKey;
  if (!oldEpoch || !oldEvidenceKey) throw new Error("baseline head row missing");

  const acceptBase = acceptances.length;
  const mapBase = maps.length;
  mockRead.mockResolvedValue(docContent(`${SOURCE}CHANGED`, "rev-B"));
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
  await act(async () => {
    fireEvent(document, new Event("visibilitychange"));
    await ports.runtime.whenIdle();
  });
  await waitForFreshAcceptedMap(acceptances, maps, work, acceptBase, mapBase);
  await act(async () => {
    await ports.runtime.whenIdle();
    await settleReaderRefreshes(listReads);
  });
  await act(async () => {
    await ports.runtime.whenIdle();
    await settleReaderRefreshes(listReads);
  });

  // The reader's opportunistic deletion faulted: the receipt must stay
  // persisted unfinished, and the expired epoch's rows must survive it.
  const expiryRetained = await rowsForTests<AnnotationExpiryRow>(f.factory, f.databaseName, ANNOTATION_EXPIRY_STORE);
  expect(expiryRetained.map((row) => row.receipt.phase)).toEqual(["fenced"]);
  const retainedPayloads = await rowsForTests<AnnotationPayloadRow>(
    f.factory,
    f.databaseName,
    ANNOTATION_PAYLOADS_STORE,
  );
  expect(retainedPayloads.filter((row) => row.epoch === oldEpoch).length).toBeGreaterThan(0);

  // The next retry pass owns the retry: its persisted sweep completes the
  // deletion, with the sweep's own wake joined inside act.
  failDeletion = false;
  await act(async () => {
    await f.repo.retryPending("reader-open");
    await settleReaderRefreshes(listReads);
    const expiryAfter = await rowsForTests<AnnotationExpiryRow>(f.factory, f.databaseName, ANNOTATION_EXPIRY_STORE);
    expect(expiryAfter.map((row) => row.receipt.phase)).toEqual(["deleted"]);
    const payloads = await rowsForTests<AnnotationPayloadRow>(f.factory, f.databaseName, ANNOTATION_PAYLOADS_STORE);
    expect(payloads.filter((row) => row.epoch === oldEpoch)).toEqual([]);
    const evidence = await rowsForTests<AnnotationEvidenceRow>(f.factory, f.databaseName, ANNOTATION_EVIDENCE_STORE);
    expect(evidence.filter((row) => row.evidenceKey === oldEvidenceKey)).toEqual([]);
  });
});

test("a faulted expiry deletion registers retry need so the pane's next read cycle completes the sweep", async () => {
  let failDeletion = true;
  let observeSuccess = false;
  let runtime: ReturnType<typeof createDocumentAnnotationRuntime> | undefined;
  const f = await fixture("reader-expiry-deletion-fault-retry", {
    beforeCommit: (operation) => {
      if (operation === "deleteExpiry" && failDeletion) throw new Error("delete fault");
    },
    // The production singleton wires the repository's storage-success boundary
    // to its runtime (getDocumentAnnotationPorts). Gate the same wiring: with a
    // deletion that keeps faulting, every recovery reread would otherwise start
    // a pass that faults again, and the test wants exactly one pass - the one
    // the pane's own next read cycle starts once the fault is repaired.
    onStorageSuccess: () => {
      if (observeSuccess) runtime?.storageSucceeded();
    },
  });
  await settleBaseline(f);
  runtime = createDocumentAnnotationRuntime(f.repo);
  cleanupAfterTest(async () => {
    runtime?.dispose();
    await runtime?.whenIdle();
  });
  const ports: DocumentAnnotationPorts = { repository: f.repo, runtime };
  const client = connectFakeClient();
  mockRead.mockResolvedValue(docContent(SOURCE, "rev-A"));
  const { maps } = observeBoundMaps();
  const { acceptances } = observeReadOwners(f.repo);
  const listReads = observeListAnnotations(f.repo);
  mountAnnotated(f, ports, client, "doc-expiry-deletion-fault-retry");
  const work = documentReaderWork("doc-expiry-deletion-fault-retry", f.document, ports);
  await waitForCurrentBoundMap(work, maps);

  const headBefore = (await rowsForTests<AnnotationHeadRow>(f.factory, f.databaseName, ANNOTATION_HEADS_STORE))[0];
  const oldEpoch = headBefore?.epoch;
  const oldEvidenceKey = headBefore?.evidenceKey;
  if (!oldEpoch || !oldEvidenceKey) throw new Error("baseline head row missing");

  // A real changed source fences; the adoption acceptance attempts the
  // deletion, which faults and leaves the receipt persisted unfinished.
  const acceptBase = acceptances.length;
  const mapBase = maps.length;
  mockRead.mockResolvedValue(docContent(`${SOURCE}CHANGED`, "rev-B"));
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
  await act(async () => {
    fireEvent(document, new Event("visibilitychange"));
    await ports.runtime.whenIdle();
  });
  await waitForFreshAcceptedMap(acceptances, maps, work, acceptBase, mapBase);
  await act(async () => {
    await ports.runtime.whenIdle();
    await settleReaderRefreshes(listReads);
  });
  await act(async () => {
    await ports.runtime.whenIdle();
    await settleReaderRefreshes(listReads);
    const expiryRetained = await rowsForTests<AnnotationExpiryRow>(f.factory, f.databaseName, ANNOTATION_EXPIRY_STORE);
    expect(expiryRetained.map((row) => row.receipt.phase)).toEqual(["fenced"]);
  });
  // The pane's own next read cycle - a foreground Reload press, no visibility
  // event - is a real storage success. With the retry need registered at the
  // fault, that success starts the completing pass, whose persisted sweep
  // finishes the deletion; without it, the faulted deletion waits for an
  // unrelated foreground return or composer-open event.
  failDeletion = false;
  observeSuccess = true;
  const reloadBase = acceptances.length;
  const reloadMapBase = maps.length;
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Reload" }));
    await ports.runtime.whenIdle();
  });
  await waitForFreshAcceptedMap(acceptances, maps, work, reloadBase, reloadMapBase);
  await act(async () => {
    await ports.runtime.whenIdle();
    await settleReaderRefreshes(listReads);
    const expiryAfter = await rowsForTests<AnnotationExpiryRow>(f.factory, f.databaseName, ANNOTATION_EXPIRY_STORE);
    expect(expiryAfter.map((row) => row.receipt.phase)).toEqual(["deleted"]);
    const payloads = await rowsForTests<AnnotationPayloadRow>(f.factory, f.databaseName, ANNOTATION_PAYLOADS_STORE);
    expect(payloads.filter((row) => row.epoch === oldEpoch)).toEqual([]);
    const evidence = await rowsForTests<AnnotationEvidenceRow>(f.factory, f.databaseName, ANNOTATION_EVIDENCE_STORE);
    expect(evidence.filter((row) => row.evidenceKey === oldEvidenceKey)).toEqual([]);
  });
});

test("reopening a document with a fresh runtime sweeps an unfinished persisted expiry on the first accepted map", async () => {
  let failDeletion = true;
  const f = await fixture("reader-expiry-reopen-sweep", {
    beforeCommit: (operation) => {
      if (operation === "deleteExpiry" && failDeletion) throw new Error("delete fault");
    },
  });
  await settleBaseline(f);
  const first = annotationPorts(f);
  const client = connectFakeClient();
  mockRead.mockResolvedValue(docContent(SOURCE, "rev-A"));
  const { maps } = observeBoundMaps();
  const { acceptances } = observeReadOwners(f.repo);
  const listReads = observeListAnnotations(f.repo);
  const host = mountAnnotated(f, first.ports, client, "doc-expiry-reopen-sweep");
  const work = documentReaderWork("doc-expiry-reopen-sweep", f.document, first.ports);
  await waitForCurrentBoundMap(work, maps);

  const headBefore = (await rowsForTests<AnnotationHeadRow>(f.factory, f.databaseName, ANNOTATION_HEADS_STORE))[0];
  const oldEpoch = headBefore?.epoch;
  const oldEvidenceKey = headBefore?.evidenceKey;
  if (!oldEpoch || !oldEvidenceKey) throw new Error("baseline head row missing");

  // A real changed source fences; the adoption acceptance attempts the
  // deletion, which faults and leaves the receipt persisted unfinished.
  const acceptBase = acceptances.length;
  const mapBase = maps.length;
  mockRead.mockResolvedValue(docContent(`${SOURCE}CHANGED`, "rev-B"));
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
  await act(async () => {
    fireEvent(document, new Event("visibilitychange"));
    await first.runtime.whenIdle();
  });
  await waitForFreshAcceptedMap(acceptances, maps, work, acceptBase, mapBase);
  await act(async () => {
    await first.runtime.whenIdle();
    await settleReaderRefreshes(listReads);
  });
  const expiryRetained = await rowsForTests<AnnotationExpiryRow>(f.factory, f.databaseName, ANNOTATION_EXPIRY_STORE);
  expect(expiryRetained.map((row) => row.receipt.phase)).toEqual(["fenced"]);

  // A reload-shaped reopen: the pane unmounts, its runtime dies with it, and a
  // FRESH adapter and runtime over the same database mount the same document
  // with no registered retry need and no failed save of their own. The first
  // accepted map's reader-open pass must sweep the persisted receipt on its
  // own - no visibility event, no composer-open, no failed save.
  failDeletion = false;
  host.unmount();
  disposeDocumentReaderWork("doc-expiry-reopen-sweep");
  const secondRepo = createDocumentAnnotationRepository({
    indexedDB: f.factory,
    databaseName: f.databaseName,
    drafts: f.drafts,
    targetFor: () => null,
  });
  cleanupAfterTest(() => secondRepo.close());
  const secondRuntime = createDocumentAnnotationRuntime(secondRepo);
  cleanupAfterTest(async () => {
    secondRuntime.dispose();
    await secondRuntime.whenIdle();
  });
  const secondPorts: DocumentAnnotationPorts = { repository: secondRepo, runtime: secondRuntime };
  mockRead.mockResolvedValue(docContent(`${SOURCE}CHANGED`, "rev-B"));
  const listReadsSecond = observeListAnnotations(secondRepo);
  mountAnnotated(f, secondPorts, client, "doc-expiry-reopen-sweep");
  const reopenedWork = documentReaderWork("doc-expiry-reopen-sweep", f.document, secondPorts);
  await waitForCurrentBoundMap(reopenedWork, maps);
  await act(async () => {
    await secondRuntime.whenIdle();
    await settleReaderRefreshes(listReadsSecond);
    const expiryAfter = await rowsForTests<AnnotationExpiryRow>(f.factory, f.databaseName, ANNOTATION_EXPIRY_STORE);
    expect(expiryAfter.map((row) => row.receipt.phase)).toEqual(["deleted"]);
    const payloads = await rowsForTests<AnnotationPayloadRow>(f.factory, f.databaseName, ANNOTATION_PAYLOADS_STORE);
    expect(payloads.filter((row) => row.epoch === oldEpoch)).toEqual([]);
    const evidence = await rowsForTests<AnnotationEvidenceRow>(f.factory, f.databaseName, ANNOTATION_EVIDENCE_STORE);
    expect(evidence.filter((row) => row.evidenceKey === oldEvidenceKey)).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// Epoch-change capture retirement (roborev: acceptance invalidated retained
// captures only when the incarnation changed, so another reader's fence left
// this reader holding a frozen old-epoch capture its Send could never
// validate and a fresh selection could not replace).
// ---------------------------------------------------------------------------

test("an epoch changed by another reader's fence retires the retained capture while preserving the exact note", async () => {
  const f = await fixture("reader-epoch-retire");
  await settleBaseline(f);
  const { ports } = annotationPorts(f);
  const client = connectFakeClient();
  mockRead.mockResolvedValue(docContent(SOURCE, "rev-A"));
  const { maps } = observeBoundMaps();
  const { beginReads, acceptances } = observeReadOwners(f.repo);
  const listReads = observeListAnnotations(f.repo);
  mountAnnotated(f, ports, client, "doc-epoch-retire");
  const work = documentReaderWork("doc-epoch-retire", f.document, ports);
  await waitForCurrentBoundMap(work, maps);
  const oldEpoch = work.lease?.epoch;
  if (!oldEpoch) throw new Error("baseline lease missing");

  // A frozen capture and note from the old epoch, never sent.
  await selectFirstPassage();
  fireEvent.change(await screen.findByRole("textbox"), { target: { value: "Keep this scope." } });
  expect(work.comment.capture).toBeDefined();
  expect(work.comment.note).toBe("Keep this scope.");

  // ANOTHER adapter fences the changed source first. This reader's own next
  // read never sees expiry-pending: the head is already the replacement
  // epoch under the SAME incarnation.
  const second = createDocumentAnnotationRepository({
    indexedDB: f.factory,
    databaseName: f.databaseName,
    drafts: f.drafts,
    targetFor: () => null,
  });
  cleanupAfterTest(() => second.close());
  const beginBase = beginReads.length;
  const acceptBase = acceptances.length;
  const mapBase = maps.length;
  // The raw source has changed for everyone: serve the replacement from here
  // on, so the reread the fence's wake schedules adopts it directly.
  mockRead.mockResolvedValue(docContent(`${SOURCE}CHANGED`, "rev-B"));
  const fenced = await second.acceptRead(await second.beginRead(f.document), docContent(`${SOURCE}CHANGED`, "rev-B"));
  expect(fenced.state).toBe("expiry-pending");
  if (fenced.state !== "expiry-pending" || !fenced.receipt) throw new Error("other adapter did not fence");

  // The fence's wake reaches this pane as an empty list with an unchanged
  // incarnation: no reset is proven, but the wake itself now schedules the
  // adopting reread — the pane never strands its expired authority waiting
  // for a foreground event, a remount or a peer save.
  await act(async () => {
    await ports.runtime.whenIdle();
    await settleReaderRefreshes(listReads);
  });
  await waitFor(() => expect(beginReads.length).toBeGreaterThan(beginBase + 1));

  // The scheduled reread accepts the replacement epoch directly, and the
  // stale capture is retired - the exact note preserved - so Send can never
  // build an operation from old-epoch evidence, and a fresh selection
  // captures again on the replacement epoch. The wake-driven tail stays
  // inside act so no reader update escapes the scope.
  const fresh = await waitForFreshAcceptedMap(acceptances, maps, work, acceptBase, mapBase);
  if (fresh.state !== "accepted") throw new Error("reread was not accepted");
  expect(fresh.lease.epoch).not.toBe(oldEpoch);
  await waitFor(() => expect(work.lease?.epoch).not.toBe(oldEpoch));
  await act(async () => {
    await ports.runtime.whenIdle();
    await settleReaderRefreshes(listReads);
    expect(work.comment.capture).toBeUndefined();
    expect(work.attempted).toBeNull();
    expect(work.comment.note).toBe("Keep this scope.");
    expect((screen.getByRole("textbox") as HTMLTextAreaElement).value).toBe("Keep this scope.");
  });
  await selectFirstPassage();
  expect(work.comment.capture).toBeDefined();
});

// ---------------------------------------------------------------------------
// Saved-evidence epoch scope (roborev: work.saved survived epoch retirement,
// so a faulted reread of an unchanged adopted epoch compared against the
// retired epoch's saved evidence, expired the ADOPTED epoch, and the
// retirement dropped another reader's failed save from the shared retry
// runtime).
// ---------------------------------------------------------------------------

test("a faulted reread of an unchanged adopted epoch keeps that epoch's lease and another reader's failed save retryable", async () => {
  let failSave = false;
  const f = await fixture("authority-saved-epoch-mismatch", {
    beforeCommit: (operation) => {
      if (operation === "save" && failSave) throw new Error("reader-2 save fault");
    },
  });
  await settleBaseline(f);
  const { ports } = annotationPorts(f);
  const client = connectFakeClient();
  const changedSource = `${SOURCE}\nCHANGED`;
  mockRead.mockResolvedValue(docContent(SOURCE, "rev-A"));
  const { maps } = observeBoundMaps();
  const { beginReads, acceptances } = observeReadOwners(f.repo);
  const listReads = observeListAnnotations(f.repo);

  // Reader 1 saves an annotation under epoch A: the save settles and stays
  // retained as this reader's last committed work.
  const host1 = mountAnnotated(f, ports, client, "doc-saved-epoch-a");
  const work1 = documentReaderWork("doc-saved-epoch-a", f.document, ports);
  await waitForCurrentBoundMap(work1, maps);
  const savedEpoch = work1.lease?.epoch;
  if (!savedEpoch) throw new Error("reader-1 baseline lease missing");
  await selectFirstPassage();
  fireEvent.change(await screen.findByRole("textbox"), { target: { value: "Reader 1 note." } });
  fireEvent.click(screen.getByRole("button", { name: "Send" }));
  await waitFor(() => {
    if (!work1.saved) throw new Error("reader-1 save never settled");
  });
  await act(async () => {
    await ports.runtime.whenIdle();
    await settleReaderRefreshes(listReads);
  });
  const saved1 = work1.saved;
  if (!saved1) throw new Error("reader-1 save never settled");
  expect(saved1.lease.epoch).toBe(savedEpoch);
  expect(work1.phase).toBe("reading");

  // ANOTHER adapter fences a changed source first: epoch B replaces A under
  // the same incarnation, and reader 1's next reread adopts the replacement.
  const second = createDocumentAnnotationRepository({
    indexedDB: f.factory,
    databaseName: f.databaseName,
    drafts: f.drafts,
    targetFor: () => null,
  });
  cleanupAfterTest(() => second.close());
  mockRead.mockResolvedValue(docContent(changedSource, "rev-B"));
  const adoptionAcceptBase = acceptances.length;
  const adoptionMapBase = maps.length;
  let fenced: Awaited<ReturnType<Fixture["repo"]["acceptRead"]>> | undefined;
  await act(async () => {
    fenced = await second.acceptRead(await second.beginRead(f.document), docContent(changedSource, "rev-B"));
  });
  if (!fenced) throw new Error("other adapter did not fence");
  expect(fenced.state).toBe("expiry-pending");
  if (fenced.state !== "expiry-pending" || !fenced.receipt) throw new Error("other adapter did not fence");

  // Reader 1 adopts epoch B, observed exactly as the epoch-retire case above:
  // the wake-driven reread's acceptance, map and lease. The epoch-A save
  // survives the adoption, retained for its own epoch.
  const beginAfterFence = beginReads.length;
  await act(async () => {
    await ports.runtime.whenIdle();
    await settleReaderRefreshes(listReads);
  });
  await waitFor(() => expect(beginReads.length).toBeGreaterThan(beginAfterFence + 1));
  const fresh = await waitForFreshAcceptedMap(acceptances, maps, work1, adoptionAcceptBase, adoptionMapBase);
  if (fresh.state !== "accepted") throw new Error("reader-1 reread was not accepted");
  expect(fresh.lease.epoch).not.toBe(savedEpoch);
  const adoptedEpoch = fresh.lease.epoch;
  await waitFor(() => expect(work1.lease?.epoch).toBe(adoptedEpoch));
  await act(async () => {
    await ports.runtime.whenIdle();
    await settleReaderRefreshes(listReads);
    expect(work1.saved?.lease.epoch).toBe(savedEpoch);
  });

  // Reader 2 mounts on epoch B and its Send faults at the real save boundary:
  // the shared runtime retains the failed save for the ADOPTED epoch.
  const host2 = mountAnnotated(f, ports, client, "doc-saved-epoch-b");
  const work2 = documentReaderWork("doc-saved-epoch-b", f.document, ports);
  await waitForCurrentBoundMap(work2, maps);
  expect(work2.lease?.epoch).toBe(adoptedEpoch);
  await selectFirstPassage(host2.container);
  fireEvent.change(await within(host2.container).findByRole("textbox"), { target: { value: "Reader 2 note." } });
  failSave = true;
  fireEvent.click(within(host2.container).getByRole("button", { name: "Send" }));
  await waitFor(() => expect(work2.phase).toBe("unsaved"));
  const reader2Attempt = work2.attempted;
  if (!reader2Attempt) throw new Error("reader-2 attempt not retained");
  expect(reader2Attempt.lease.epoch).toBe(adoptedEpoch);

  // Reader 1's next read faults at the real beginRead boundary while the raw
  // fetch of the UNCHANGED epoch-B source succeeds: no ticket issues, so only
  // retained evidence could "prove" a change here - and the only retained
  // evidence is the save from the already-replaced epoch A.
  const armed = { value: true };
  faultBeginReadTransaction(armed);
  cleanupAfterTest(() => {
    armed.value = false;
  });
  host1.unmount();
  const host1b = mountAnnotated(f, ports, client, "doc-saved-epoch-a");
  await within(host1b.container).findByText("CHANGED"); // the ticket-less raw fetch really rendered
  await act(async () => {
    await ports.runtime.whenIdle();
    await settleReaderRefreshes(listReads);
  });

  // The faulted reread proved nothing about the adopted epoch: its lease
  // survives, the phase never claims an expiry, and the epoch-A save stays
  // retained for its own epoch without ever serving as evidence for B.
  expect(work1.phase).toBe("reading");
  expect(work1.lease?.epoch).toBe(adoptedEpoch);
  expect(work1.saved?.lease.epoch).toBe(savedEpoch);

  // Storage heals. The shared runtime's retry pass must re-apply reader 2's
  // failed save for the adopted epoch: a retirement that never honestly
  // happened must not have dropped it.
  armed.value = false;
  failSave = false;
  await act(async () => {
    await ports.runtime.retryPending("composer-open");
    await settleReaderRefreshes(listReads);
  });
  const appliedIds = (await f.repo.listAnnotations(f.document)).map((view) => view.receipt.operationId);
  expect(appliedIds).toContain(reader2Attempt.operationId);

  // Reader 1's suspended authority revalidates on an ordinary foreground
  // reread: a fresh ticket accepts the unchanged epoch-B source and the pane
  // keeps its lease.
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
  const recoveryAcceptBase = acceptances.length;
  const recoveryMapBase = maps.length;
  await act(async () => {
    fireEvent(document, new Event("visibilitychange"));
    await ports.runtime.whenIdle();
    await settleReaderRefreshes(listReads);
  });
  const recovered = await waitForFreshAcceptedMap(acceptances, maps, work1, recoveryAcceptBase, recoveryMapBase);
  if (recovered.state !== "accepted") throw new Error("reader-1 recovery read was not accepted");
  expect(recovered.lease.epoch).toBe(adoptedEpoch);
  await waitFor(() => expect(work1.lease?.epoch).toBe(adoptedEpoch));
  await act(async () => {
    await ports.runtime.whenIdle();
    await settleReaderRefreshes(listReads);
    expect(work1.phase).toBe("reading");
  });
});

// ---------------------------------------------------------------------------
// Reopened-reader displayed evidence (roborev: a reopened reader displays
// accepted content and persisted pending annotations but retains no attempt,
// comment or save of its own, so a raw reread that proved a change suppressed
// no epoch - and a later retry appended another reader's obsolete reference
// before the paced reread fenced it).
// ---------------------------------------------------------------------------

test("a reopened reader's faulted reread fences another reader's pending handoff behind a changed source", async () => {
  let failAppend = false;
  const f = await fixture("authority-reopened-displayed-evidence", {
    beforeAbortableCompletion: () => {
      if (failAppend) throw new Error("append storage fault");
    },
  });
  await settleBaseline(f);
  const { ports } = annotationPorts(f);
  const client = connectFakeClient();
  const changedSource = `${SOURCE}\nCHANGED`;
  mockRead.mockResolvedValue(docContent(SOURCE, "rev-A"));
  const { maps } = observeBoundMaps();
  const listReads = observeListAnnotations(f.repo);

  // Reader 1 saves an annotation whose handoff stays pending: the append
  // faults at the real completion boundary, so the durable pending row and
  // its receipt outlive the reader that saved them.
  failAppend = true;
  const host1 = mountAnnotated(f, ports, client, "doc-reopened-a");
  const work1 = documentReaderWork("doc-reopened-a", f.document, ports);
  await waitForCurrentBoundMap(work1, maps);
  await selectFirstPassage();
  fireEvent.change(await screen.findByRole("textbox"), { target: { value: "Reader 1 note." } });
  fireEvent.click(screen.getByRole("button", { name: "Send" }));
  const saved = await waitFor(() => {
    if (!work1.saved) throw new Error("reader-1 save never settled");
    return work1.saved;
  });
  const savedEpoch = saved.lease.epoch;
  await act(async () => {
    await ports.runtime.whenIdle();
    await settleReaderRefreshes(listReads);
  });
  const pending = (await f.repo.listAnnotations(f.document)).find(
    (view) => view.operation.operationId === saved.operationId,
  );
  expect(pending?.handoff.state).toBe("pending");
  host1.unmount();

  // Reader 2 REOPENS the document: a fresh viewer with no attempted, composed
  // or saved work of its own, displaying accepted content under the same
  // epoch as the persisted pending annotation.
  const host2 = mountAnnotated(f, ports, client, "doc-reopened-b");
  const work2 = documentReaderWork("doc-reopened-b", f.document, ports);
  await waitForCurrentBoundMap(work2, maps);
  expect(work2.lease?.epoch).toBe(savedEpoch);
  expect(work2.attempted).toBeNull();
  expect(work2.saved).toBeNull();
  host2.unmount();

  // The source changes, and reader 2's next read cycle faults at the real
  // beginRead boundary while the raw fetch of the changed source succeeds.
  mockRead.mockResolvedValue(docContent(changedSource, "rev-B"));
  const armed = { value: true };
  faultBeginReadTransaction(armed);
  cleanupAfterTest(() => {
    armed.value = false;
  });
  const host2b = mountAnnotated(f, ports, client, "doc-reopened-b");
  await within(host2b.container).findByText("CHANGED");
  await act(async () => {
    await ports.runtime.whenIdle();
    await settleReaderRefreshes(listReads);
  });

  // The displayed lease's own evidence proves the change: the honest
  // expiry-pending phase, and the displayed epoch fenced so the other
  // reader's pending handoff can no longer append its obsolete reference.
  expect(work2.phase).toBe("expiry-pending");
  failAppend = false;
  const handoff = await f.repo.handoff(f.document, saved.operationId);
  expect(handoff.state).toBe("cancelled");
  const draft = await f.drafts.load(f.key);
  expect(draft?.text).not.toContain("Reader 1 note");
  host2b.unmount();
});

// ---------------------------------------------------------------------------
// Popout document moves (roborev: popping out an already loaded document moves
// its existing DOM into another window's document without any React state
// change, so the selection observer kept its listeners on the opener's
// document and selections inside the popout never opened the comment box).
// ---------------------------------------------------------------------------

test("a popped-out reader keeps opening the comment box for selections in its own window", async () => {
  const f = await fixture("popout-selection-rebind");
  await settleBaseline(f);
  const { ports } = annotationPorts(f);
  const client = connectFakeClient();
  mockRead.mockResolvedValue(docContent(SOURCE, "rev-A"));
  const { maps } = observeBoundMaps();
  const host = mountAnnotated(f, ports, client, "doc-popout-selection");
  const work = documentReaderWork("doc-popout-selection", f.document, ports);
  await waitForCurrentBoundMap(work, maps);

  // Pop the loaded pane out: dockview adopts the group's existing DOM into the
  // popout window's document - the same cross-document move an iframe's body
  // performs here - and the popout host announces it through the same channel
  // DockHost publishes on. React never re-renders for the adoption.
  const frame = document.createElement("iframe");
  document.body.append(frame);
  cleanupAfterTest(() => frame.remove());
  const owner = frame.contentDocument;
  const ownerWindow = frame.contentWindow;
  if (!owner || !ownerWindow) throw new Error("missing iframe document");
  owner.body.appendChild(host.container);
  act(() => {
    publishPaneDocumentMoved();
  });

  // A finished selection in the popout's own window opens the comment box:
  // the press and the selection both belong to the popout document, exactly as
  // they would for a user selecting prose inside the popped-out pane.
  const paragraph = await within(host.container).findByText("First occurrence: same");
  fireEvent.pointerDown(paragraph, PRIMARY_POINTER);
  const node = paragraph.firstChild;
  if (!node || node.nodeType !== Node.TEXT_NODE) throw new Error("missing source text node");
  const range = owner.createRange();
  range.setStart(node, 18);
  range.setEnd(node, 22);
  const selection = ownerWindow.getSelection();
  if (!selection) throw new Error("selection unavailable");
  selection.removeAllRanges();
  selection.addRange(range);
  fireEvent.pointerUp(paragraph, PRIMARY_POINTER);
  await waitFor(() => {
    if (!work.comment.capture) throw new Error("popout selection never captured");
  });
  expect(work.comment.capture?.sourceRange).toEqual(FIRST);
  host.unmount();
});

// ---------------------------------------------------------------------------
// Stale-ticket recovery (roborev: a stale ticket silently ended the read
// without recovery, leaving a concurrently mounted pane on its loading
// skeleton forever).
// ---------------------------------------------------------------------------

test("a concurrently mounted pane recovers from its superseded ticket with a fresh reread", async () => {
  const f = await fixture("reader-stale-recovery");
  await settleBaseline(f);
  const { ports } = annotationPorts(f);
  const client = connectFakeClient();
  // Pane A's raw read is held on a deferred so pane B's ticket is issued
  // while A's read is still in flight: B's issuance alone supersedes A's
  // ticket, whatever A's raw read then returns.
  let releaseA: (() => void) | undefined;
  mockRead.mockImplementationOnce(
    () =>
      new Promise((resolve) => {
        releaseA = () => resolve(docContent(SOURCE, "rev-A"));
      }),
  );
  mockRead.mockResolvedValue(docContent(SOURCE, "rev-A"));
  const { maps } = observeBoundMaps();
  const { beginReads, acceptances } = observeReadOwners(f.repo);

  const hostA = mountAnnotated(f, ports, client, "doc-stale-a");
  const workA = documentReaderWork("doc-stale-a", f.document, ports);
  await waitFor(() => expect(beginReads.length).toBe(1));
  mountAnnotated(f, ports, client, "doc-stale-b");
  const workB = documentReaderWork("doc-stale-b", f.document, ports);
  await waitFor(() => expect(beginReads.length).toBe(2));
  await act(async () => {
    releaseA?.();
    await Promise.resolve();
  });

  await waitFor(() => {
    expect(acceptances.some((a) => a.state === "stale-ticket")).toBe(true);
    expect(acceptances.some((a) => a.state === "accepted")).toBe(true);
  });
  await waitForCurrentBoundMap(workB, maps);
  expect(workB.lease).not.toBeNull();

  // The superseded pane recovers instead of staying on its loading skeleton:
  // one fresh reread whose ticket is the newest issued accepts, and a
  // lease-holding pane never retries (its stale foreground rereads keep the
  // rendered lease).
  await waitFor(() => expect(workA.lease).not.toBeNull());
  expect(workA.phase).toBe("reading");
  expect(within(hostA.container).getByText("First occurrence: same")).toBeTruthy();
  expect(beginReads.length).toBe(3);
});

test("a remounted pane with retained lease authority recovers from its superseded first read", async () => {
  const f = await fixture("reader-remount-stale");
  await settleBaseline(f);
  const { ports } = annotationPorts(f);
  const client = connectFakeClient();

  // The pane reads and accepts first: its retained work holds a lease and it
  // has content on screen.
  mockRead.mockResolvedValue(docContent(SOURCE, "rev-A"));
  const first = mountAnnotated(f, ports, client, "doc-remount-b");
  const workB = documentReaderWork("doc-remount-b", f.document, ports);
  await waitFor(() => expect(workB.lease).not.toBeNull());
  await waitFor(() => expect(within(first.container).getByText("First occurrence: same")).toBeTruthy());
  first.unmount();
  // The remounted mount retains the lease in the reader work registry.
  expect(workB.lease).not.toBeNull();

  // The fresh mount starts on the loading skeleton; its raw read is held on
  // a deferred so pane A's ticket is issued while B's read is still in
  // flight - A's issuance alone supersedes B's ticket.
  let releaseB: (() => void) | undefined;
  mockRead.mockImplementationOnce(
    () =>
      new Promise((resolve) => {
        releaseB = () => resolve(docContent(SOURCE, "rev-A"));
      }),
  );
  mockRead.mockResolvedValue(docContent(SOURCE, "rev-A"));
  const { beginReads, acceptances } = observeReadOwners(f.repo);
  const second = mountAnnotated(f, ports, client, "doc-remount-b");
  await waitFor(() => expect(beginReads.length).toBe(1));
  mountAnnotated(f, ports, client, "doc-remount-a");
  await waitFor(() => expect(beginReads.length).toBe(2));
  await act(async () => {
    releaseB?.();
    await Promise.resolve();
  });
  await waitFor(() => expect(acceptances.some((a) => a.state === "stale-ticket")).toBe(true));

  // The remounted pane must not stay on its skeleton forever: with nothing
  // rendered, the retained lease cannot keep anything on screen, so the
  // same bounded recovery applies - one fresh reread whose ticket is the
  // newest issued accepts and renders.
  await waitFor(() => expect(beginReads.length).toBe(3));
  await waitFor(() => expect(within(second.container).getByText("First occurrence: same")).toBeTruthy());
  expect(workB.lease).not.toBeNull();
});
