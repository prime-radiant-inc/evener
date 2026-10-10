import {
  type CapturedPassage,
  DOC_FILE_MAX_BYTES,
  type DocFileContent,
  DocFileError,
  displayedFingerprint,
  docImageURL,
  formatAnnotationReference,
  type PreparedAnnotation,
} from "@evener/appwire-client";
import * as docContentModule from "@evener/appwire-client/docContent";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { IDBObjectStore as FDBObjectStore } from "fake-indexeddb";
import { lazy } from "react";
import { afterAll, afterEach, beforeEach, expect, type MockInstance, test, vi } from "vitest";
import { ClientProvider } from "../../shell/clientContext";
import { registerPaneForTests } from "../../shell/paneRegistry";
import { registerDockviewApi, resetWorkspaceStoreForTests, workspaceStore } from "../../shell/workspace";
import { connectionStore } from "../../stores/connection";
import {
  createDocumentAnnotationRuntime,
  type DocumentAnnotationPorts,
  type DocumentAnnotationRuntime,
} from "../../stores/documentAnnotationRuntime";
import {
  ANNOTATION_HEADS_STORE,
  ANNOTATION_NAMESPACES_STORE,
  ANNOTATION_OPERATIONS_STORE,
  ANNOTATION_PAYLOADS_STORE,
  type AnnotationOperationRow,
  type AnnotationPayloadRow,
} from "../../stores/localCompositionSchema";
import { resetThreadsStoreForTests } from "../../stores/threads";
import { readModuleCss } from "../../styles/cssBlock";
import type { BoundDocumentMap } from "../../widgets/markdown/documentSourceMap";
import * as documentSourceMapModule from "../../widgets/markdown/documentSourceMap";
import { Composer } from "../session/composer/Composer";
import { browserDocPort } from "./browserDocPort";
import DocPane from "./DocPane";
import docpaneStyles from "./docpane.module.css";
import {
  type DocumentReaderWork,
  disposeDocumentReaderWork,
  documentReaderWork,
  publishReader,
  subscribeDocumentReaderWork,
} from "./documentReadState";
import { FIRST, SOURCE } from "./testing/annotationFixtures";
import {
  acceptedOperation,
  blobBytes,
  cleanupAfterTest,
  docContent,
  fixture,
  rowsForTests,
} from "./testing/annotationRepositoryFixtures";
import { DocumentGeometrySeam, type TestRect } from "./testing/documentGeometryTestUtils";
// Side-effect import: registers the real "doc" pane type (index.tsx), needed
// below so workspaceStore.openPane("doc", ...) can seed an already-open doc
// pane to focus away from before testing that Back moves focus back.
import "./index";

// Only readDocFile is mocked; docImageURL / DocFileError / DOC_FILE_MAX_BYTES
// stay real so the pane's URL building and error-kind switch run for real.
// Each test scripts its own reads through a fresh vi.spyOn on the module's
// export; the imported annotation fixture's afterEach restores every mock
// AFTER its own disposers, so one test's scripted reads never answer the next
// test's and no spy is handed back before the disposers that need it run.
let mockRead: MockInstance<typeof docContentModule.readDocFile>;

// A minimal, test-only "session" pane registration - mirrors
// Transcript.test.tsx's own precedent: real registerPane/paneFor/openPane
// machinery, without pulling in the actual (heavier) panes/session module.
afterAll(
  registerPaneForTests({
    id: "session",
    title: () => "test session",
    component: lazy(() => Promise.resolve({ default: () => null })),
  }),
);

beforeEach(() => {
  resetThreadsStoreForTests();
  resetWorkspaceStoreForTests();
  mockRead = vi.spyOn(docContentModule, "readDocFile");
});

afterEach(() => {
  cleanup();
  registerDockviewApi(null); // never leak a fake dockview host to another test
});

// The doc pane is a reading surface: the Markdown widget composes with its
// host, so the pane pins the prose step on its own host element or its
// documents would render at the ambient body step (see the .markdown rule
// comment in docpane.module.css). jsdom computes no cascade, so this reads
// the stylesheet's own source. This pins BODY text - headings and tables
// keep the widget's fixed ramp (see widgets/markdown).
test("the doc pane's markdown body text reads at the prose step, pinned on the pane's own host element", () => {
  const css = readModuleCss(import.meta.url, "docpane.module.css").replace(/\/\*[\s\S]*?\*\//g, "");
  expect(css.match(/--prose-font-size/g)).toHaveLength(1);
  expect(css).toMatch(/\.markdown\s*\{[^}]*--prose-font-size:\s*var\(--font-size-prose\);/);
});

function textContent(over: Partial<DocFileContent> = {}): DocFileContent {
  return { text: "", binary: false, mediaType: "text/plain", truncated: false, sizeBytes: 0, ...over };
}

function renderFile(path: string) {
  return render(<DocPane params={{ session: "s1", path, kind: "file" }} paneId="doc-1" focused={true} />);
}

test("a text file renders its content inside a <pre>, titled by the filename", async () => {
  mockRead.mockResolvedValue(textContent({ text: "plain body line", sizeBytes: 15 }));
  renderFile("src/notes.txt");
  const body = await screen.findByText("plain body line");
  expect(body.tagName).toBe("PRE");
  expect(screen.getByRole("heading", { name: "notes.txt" })).toBeTruthy();
});

test("a markdown file renders through the sanitizing Markdown widget, not a raw <pre>", async () => {
  mockRead.mockResolvedValue(textContent({ text: "# Heading\n\nbody" }));
  renderFile("docs/README.md");
  // The Markdown widget turns `# Heading` into a real <h1>; a <pre> never would.
  const heading = await screen.findByRole("heading", { name: "Heading" });
  expect(heading.tagName).toBe("H1");
  // The pin's wiring, not just its CSS: the markdown root must sit inside the
  // pane's .markdown host element, which carries --prose-font-size. Without
  // this, a wrapper rename would leave the CSS contract test above green while
  // the documents silently fall back to the ambient body step.
  expect(heading.closest(`.${docpaneStyles.markdown}`)).toBeTruthy();
});

test("a decoded leading BOM stays in source coordinates without demoting the first Markdown heading", async () => {
  mockRead.mockRestore();
  vi.spyOn(globalThis, "fetch").mockResolvedValue(
    new Response(new Uint8Array([0xef, 0xbb, 0xbf, 0x23, 0x20, 0x48, 0x65, 0x61, 0x64, 0x69, 0x6e, 0x67, 0x0a]), {
      headers: { "Content-Type": "text/plain; charset=utf-8" },
    }),
  );
  renderFile("docs/README.md");
  const heading = await screen.findByRole("heading", { name: "Heading" });
  expect(heading.tagName).toBe("H1");
});

test("an annotation locator with a backtick and encoded controls round-trips through pinned Markdown", async () => {
  const source = "First occurrence: same\n";
  const capture: CapturedPassage = {
    evidence: { wholeRevision: "rev-A", fingerprint: displayedFingerprint(source, false) },
    sourceRange: { start: 18, end: 22 },
    start: { line: 1, column: 19 },
    endInclusive: { line: 1, column: 22 },
    selectedRenderedText: "same",
    contextBefore: "First occurrence: ",
    contextAfter: "\n",
    mappingId: "fixture-map",
    displayedSnapshotId: "snapshot-A",
  };
  mockRead.mockResolvedValue(
    textContent({ text: formatAnnotationReference(capture, "dir/a`b\nback\\slash.md", "Keep this scope.") }),
  );
  renderFile("docs/reference.md");
  const locator = await screen.findByText("dir/a`b\\nback\\\\slash.md:1:19-1:22");
  expect(locator.tagName).toBe("CODE");
});

test("a markdown file with raw HTML in the source has it neutralized (sanitizer posture, beyond legacy)", async () => {
  mockRead.mockResolvedValue(textContent({ text: "before <img src=x onerror=alert(1)> after" }));
  const { container } = renderFile("docs/EVIL.md");
  await screen.findByText(/before/);
  // The raw <img> must not survive as a live element - marked's html() override
  // escapes it to text and DOMPurify drops it either way.
  expect(container.querySelector("img")).toBeNull();
});

test("a binary file shows a not-shown notice with the filename and human size", async () => {
  mockRead.mockResolvedValue(textContent({ binary: true, mediaType: "application/octet-stream", sizeBytes: 2048 }));
  renderFile("out/blob.bin");
  expect(await screen.findByText("Binary file not shown")).toBeTruthy();
  // filename + human size in one hint; the pane title also shows the filename,
  // so match the combined hint string to stay unambiguous.
  expect(screen.getByText(/blob\.bin \(2 KiB\)/)).toBeTruthy();
});

test("a truncated file shows the exact truncation notice (first cap of true total) AND the partial content", async () => {
  mockRead.mockResolvedValue(
    textContent({ text: "partial head", truncated: true, sizeBytes: DOC_FILE_MAX_BYTES, totalBytes: 2 * 1024 * 1024 }),
  );
  renderFile("logs/huge.log");
  expect(await screen.findByText("partial head")).toBeTruthy();
  expect(screen.getByText("Truncated")).toBeTruthy(); // the attention chip
  // The notice is exact now: the cap shown, and the file's true total.
  expect(screen.getByText("Showing the first 512 KiB of 2 MiB.")).toBeTruthy();
});

test("an untruncated file shows no truncation notice", async () => {
  mockRead.mockResolvedValue(textContent({ text: "small", sizeBytes: 5 }));
  renderFile("small.txt");
  await screen.findByText("small");
  expect(screen.queryByText(/truncated/i)).toBeNull();
});

test("a 404 (missing file / unknown session) shows the not-found empty state", async () => {
  mockRead.mockRejectedValue(new DocFileError("not-found", 404));
  renderFile("gone.txt");
  expect(await screen.findByText("File not available")).toBeTruthy();
});

test("a 403 (path escapes the cwd) shows the access-denied empty state", async () => {
  mockRead.mockRejectedValue(new DocFileError("forbidden", 403));
  renderFile("../secret");
  expect(await screen.findByText("Access denied")).toBeTruthy();
});

test("a 501 (the session's host can't serve documents yet) says to open it on the host", async () => {
  mockRead.mockRejectedValue(new DocFileError("host-unsupported", 501));
  renderFile("plan.md");
  expect(await screen.findByText("Open it on the host")).toBeTruthy();
});

test("any other failure shows a generic couldn't-load empty state", async () => {
  mockRead.mockRejectedValue(new DocFileError("error", 500));
  renderFile("x.txt");
  expect(await screen.findByText("Couldn't load file")).toBeTruthy();
});

test("shows a loading placeholder while the fetch is in flight", () => {
  mockRead.mockReturnValue(new Promise<DocFileContent>(() => {})); // never resolves
  renderFile("slow.txt");
  expect(screen.getByRole("status", { name: "Loading" })).toBeTruthy();
});

test("an image pane renders an <img> at the /doc/image URL and never fetches raw file bytes", () => {
  render(<DocPane params={{ session: "s1", path: "out/pic.png", kind: "image" }} paneId="doc-2" focused={true} />);
  const img = screen.getByTestId("doc-image") as HTMLImageElement;
  expect(img.getAttribute("src")).toBe(docImageURL(browserDocPort.origin, "s1", "out/pic.png"));
  expect(img.getAttribute("alt")).toBe("pic.png");
  expect(mockRead).not.toHaveBeenCalled();
});

test("the click-to-zoom control announces its action, not the image filename", () => {
  render(<DocPane params={{ session: "s1", path: "out/pic.png", kind: "image" }} paneId="doc-zoom" focused={true} />);
  // Without an explicit label the button's accessible name falls through to the
  // nested <img alt> (the filename), which reads as a duplicate of the pane
  // title rather than an action. The control announces what it DOES instead.
  expect(screen.getByRole("button", { name: "Zoom image" })).toBeTruthy();
  // The image itself keeps its filename alt for the picture it shows.
  expect((screen.getByTestId("doc-image") as HTMLImageElement).getAttribute("alt")).toBe("pic.png");
});

test("clicking an image opens a full-size lightbox dialog", async () => {
  const user = userEvent.setup();
  render(<DocPane params={{ session: "s1", path: "out/pic.png", kind: "image" }} paneId="doc-3" focused={true} />);
  expect(screen.queryByRole("dialog")).toBeNull();
  await user.click(screen.getByTestId("doc-image"));
  const dialog = screen.getByRole("dialog");
  expect(dialog).toBeTruthy();
  const lightbox = screen.getByTestId("doc-lightbox-img") as HTMLImageElement;
  expect(lightbox.getAttribute("src")).toBe(docImageURL(browserDocPort.origin, "s1", "out/pic.png"));
});

test("an image that fails to load shows an unavailable notice instead of a broken image", () => {
  render(<DocPane params={{ session: "s1", path: "out/missing.png", kind: "image" }} paneId="doc-4" focused={true} />);
  fireEvent.error(screen.getByTestId("doc-image"));
  expect(screen.getByText("Image not available")).toBeTruthy();
});

// ============================================================================
// Task 5 annotation integration (tests-first checkpoint).
//
// These cases connect the real reader, repository, runtime, IndexedDB storage
// and comment controls through the approved DocPane
// `annotations?: DocumentAnnotationPorts` injection seam. `DocPane.tsx` does
// not yet declare or use that prop, so the integration cases below are RED
// until the source grant lands; the imports and assertions are the approved
// interface, so the same bodies run unchanged once the seam exists. Only the
// raw-file read (`mockRead`) and the AppWire transport (`FakeClient`) are
// faked; every save, handoff, retry, read ticket and append goes through real
// production code over real fake-IndexedDB storage.
//
// The fixture saves `op-1` but leaves its handoff pending: settle it against
// the real source before observing the producer. The FIRST passage's real DOM
// selection has a literal empty after-context (the fixture operation's stored
// capture carries a different, longer one).
// ============================================================================

type Fixture = Awaited<ReturnType<typeof fixture>>;

// The pointer fields the finished-selection observer reads.
const PRIMARY_POINTER = { pointerId: 1, isPrimary: true, pointerType: "mouse", button: 0, buttons: 1 } as const;

// The fixture saves op-1 with a pending handoff; a later append would otherwise
// return the real `pending/dirty-draft` FIFO result. Settle it first.
async function settleBaseline(f: Fixture): Promise<void> {
  const settled = await f.repo.handoff(f.document, "op-1");
  if (settled.state !== "applied" && settled.state !== "already-applied")
    throw new Error(`baseline op-1 was not settled: ${settled.state}`);
}

// Real ports over the fixture's real repository. Disposal stops further retry
// work first, then joins the runtime's CURRENT pass (dispose must not cancel an
// in-flight retry before the backing closes).
function annotationRuntimePorts(f: Fixture): { runtime: DocumentAnnotationRuntime; ports: DocumentAnnotationPorts } {
  const runtime = createDocumentAnnotationRuntime(f.repo);
  cleanupAfterTest(async () => {
    runtime.dispose();
    await runtime.whenIdle();
  });
  return { runtime, ports: { repository: f.repo, runtime } };
}

// One connected transport built on the shared FakeClient, scripted only at the
// AppWire method boundary. The store's captured client reference is restored
// BEFORE the fake is closed, so closing cannot re-arm a subscription through it.
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

function annotatedParams(f: Fixture, path = "NOTES.md") {
  return { session: "root", path, kind: "file" as const, origin: f.destination };
}

function annotedRender(
  f: Fixture,
  ports: DocumentAnnotationPorts,
  client: FakeClient,
  paneId: string,
  path = "NOTES.md",
) {
  return render(
    <ClientProvider client={client}>
      <DocPane paneId={paneId} focused={true} params={annotatedParams(f, path)} annotations={ports} />
    </ClientProvider>,
  );
}

// Mount a reader host and register BOTH teardown steps this checkpoint owns,
// in the order that actually runs unmount first: reverse-ordered fixture cleanup
// runs the LAST-registered callback first, so reader disposal is registered
// before the host unmount.
function mountAnnotated(
  f: Fixture,
  ports: DocumentAnnotationPorts,
  client: FakeClient,
  paneId: string,
  path = "NOTES.md",
) {
  const host = annotedRender(f, ports, client, paneId, path);
  cleanupAfterTest(() => disposeDocumentReaderWork(paneId));
  cleanupAfterTest(() => host.unmount());
  return host;
}

// Select a passage through a real Selection/Range over the paragraph's text
// node with the normal primary-pointer fields. Offsets are text-node offsets;
// the expected source line/column/text values stay literal in the assertions.
async function selectPassage(paragraphText: string, startOffset: number, endOffset: number): Promise<void> {
  const paragraph = await screen.findByText(paragraphText);
  fireEvent.pointerDown(paragraph, PRIMARY_POINTER);
  const node = paragraph.firstChild;
  if (!node || node.nodeType !== Node.TEXT_NODE) throw new Error("missing source text node");
  const range = document.createRange();
  range.setStart(node, startOffset);
  range.setEnd(node, endOffset);
  const selection = document.getSelection();
  if (!selection) throw new Error("missing selection");
  selection.removeAllRanges();
  selection.addRange(range);
  fireEvent.pointerUp(paragraph, PRIMARY_POINTER);
}

// FIRST: rendered paragraph "First occurrence: same", text-node offsets 18..22.
const selectFirstPassage = () => selectPassage("First occurrence: same", 18, 22);
// SECOND: the genuinely different "same" in the second paragraph (source 73..77).
const selectSecondPassage = () => selectPassage("Other context for a second occurrence is nearby: same", 49, 53);

// The PaneScaffold body: the pane title header's next element sibling.
function paneScaffoldBody(): HTMLElement {
  const heading = screen.getByRole("heading", { name: "NOTES.md" });
  const body = heading.parentElement?.nextElementSibling;
  if (!(body instanceof HTMLElement)) throw new Error("missing PaneScaffold body");
  return body;
}

// Release a held read gate even if an earlier assertion fails, so cleanup can
// finish instead of leaving an unsettled promise behind the repository close.
function releaseOnCleanup(release: () => void): void {
  cleanupAfterTest(release);
}

function payloadRowsFor(f: Fixture, operationId: string): Promise<PreparedAnnotation[]> {
  return rowsForTests<AnnotationPayloadRow>(f.factory, f.databaseName, ANNOTATION_PAYLOADS_STORE).then((rows) =>
    rows.filter((row) => row.operation.operationId === operationId).map((row) => row.operation),
  );
}

// Raw durable operation receipts: the projection `listAnnotations` can hide a
// receipt whose payload is missing, so preservation reads the rows directly.
function operationRowsFor(f: Fixture, operationId: string): Promise<AnnotationOperationRow[]> {
  return rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE).then((rows) =>
    rows.filter((row) => row.operationId === operationId),
  );
}

// The visible saved-highlight bands the real overlay paints.
function highlightBands(): HTMLElement[] {
  return Array.from(document.querySelectorAll<HTMLElement>("[data-document-highlight]"));
}

// The visible captured passage the comment surface renders (whole-UI finding 4).
function passageText(): string | null {
  return document.querySelector<HTMLElement>("[data-annotation-passage]")?.textContent ?? null;
}

// The exact durable store scope of the repository's beginRead transaction
// (documentAnnotationIndexedDB.ts READ_STORES): a readwrite transaction over the
// namespace and head stores only. Acceptance, saving, listing and handoff each
// open a different store set, so a fault scoped to this shape hits beginRead
// alone and leaves every other real transaction running its real code.
const BEGIN_READ_SCOPE = [ANNOTATION_NAMESPACES_STORE, ANNOTATION_HEADS_STORE].sort().join("|");

// Platform fault injection at the storage boundary. The fake-IndexedDB object
// store is the real object the repository reads through, so throwing from its
// `get` for the beginRead transaction rejects the real beginRead promise while
// the request is never created: the rejection is one handled promise, not an
// orphaned request. Disarm `armed.value` to let the same real transaction run.
function faultBeginReadTransaction(armed: { value: boolean }): void {
  const realGet = FDBObjectStore.prototype.get;
  vi.spyOn(FDBObjectStore.prototype, "get").mockImplementation(function (
    this: IDBObjectStore,
    query: IDBValidKey | IDBKeyRange,
  ) {
    const scope = [...this.transaction.objectStoreNames].sort().join("|");
    if (armed.value && this.transaction.mode === "readwrite" && scope === BEGIN_READ_SCOPE) {
      armed.value = false;
      throw new Error("annotation storage read fault");
    }
    return realGet.call(this, query);
  });
}

// Call-through observation of the real read owners. Every real argument tuple is
// forwarded unchanged; the observers record only COMPLETED results, so an earlier
// acceptance can never stand in for a fresh read. A rejected real read (the
// injected fault) propagates untouched and records nothing.
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

// A FRESH completion, not an earlier one. Both boundaries are captured immediately
// before the recovery trigger: an acceptance recorded after `acceptBase` must be
// accepted, a map must be newly observed after `mapBase` for that fresh lease, and
// the LATEST installed map plus the current work must name that same lease. An
// identical read preserves displayedSnapshotId by design, so a historical map from
// an earlier host can never satisfy this.
async function waitForFreshAcceptedMap(
  acceptances: Awaited<ReturnType<Fixture["repo"]["acceptRead"]>>[],
  maps: BoundDocumentMap[],
  work: DocumentReaderWork,
  acceptBase: number,
  mapBase: number,
): Promise<void> {
  await waitFor(() => {
    const fresh = acceptances.slice(acceptBase).filter((result) => result.state === "accepted");
    expect(fresh.length).toBeGreaterThan(0);
    const latest = fresh.at(-1);
    if (latest?.state !== "accepted") throw new Error("no fresh accepted read");
    const leaseSnapshot = latest.lease.displayedSnapshotId;
    expect(maps.slice(mapBase).some((map) => map.identity.displayedSnapshotId === leaseSnapshot)).toBe(true);
    expect(maps.at(-1)?.identity.displayedSnapshotId).toBe(leaseSnapshot);
    expect(work.lease?.displayedSnapshotId).toBe(leaseSnapshot);
  });
}

// Nonzero jsdom geometry for the scroll owner and the FIRST fragment.
const SCROLL_RECT: TestRect = { left: 0, top: 0, right: 300, bottom: 200 };
const FIRST_RECT: TestRect = { left: 10, top: 6, right: 40, bottom: 16 };

// A call-through observation of the REAL map DocumentMarkdown binds: the spy
// records each successful return and never replaces it, reconstructs a separate
// map or introduces a product accessor.
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

// Read-only readiness boundary: wait until the work holds a current non-null
// lease AND the real bound map the reader paints with names that lease's
// displayed snapshot. Rendering identity (mappingId/displayedSnapshotId the map
// reports) is distinct from durable snapshot identity, so this checks the map
// actually installed for the current acceptance, never assigns the lease, map or
// capture to manufacture readiness.
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

// Attachment bytes on the fixture's real recovery record, read fresh (object
// equality alone does not compare Blob content).
async function recoveryAttachmentBytes(f: Fixture): Promise<Uint8Array | undefined> {
  const [record] = await f.outbox.listRecovery();
  const attachment = record?.attachments?.[0];
  return attachment?.blob ? blobBytes(attachment.blob) : undefined;
}

// Call-through observation of every real repository list read. A committed save
// or retry wake makes the mounted pane re-list annotations (and, through
// refreshSavedAttempt, publish); awaiting each real read a wake starts runs
// those setViews/publishReader continuations INSIDE act while the reader stays
// mounted, instead of letting them land outside it.
function observeListAnnotations(repo: Fixture["repo"]): Promise<unknown>[] {
  const reads: Promise<unknown>[] = [];
  const real = repo.listAnnotations.bind(repo);
  // Forward the COMPLETE real argument tuple so an optional cancellation/context
  // argument a valid repair adds is never dropped by the observer.
  vi.spyOn(repo, "listAnnotations").mockImplementation((...args) => {
    const promise = real(...args);
    reads.push(promise);
    return promise;
  });
  return reads;
}

// Await every real list read a wake has started, until the set stops growing.
// The loop is driven by the reads themselves; 16 rounds is only a tripwire
// against a chain that never quiesces, not a fixed flush count.
async function settleReaderRefreshes(reads: Promise<unknown>[]): Promise<void> {
  let seen = 0;
  let rounds = 0;
  while (seen < reads.length) {
    if (++rounds > 16) throw new Error("reader repository refreshes did not quiesce");
    const batch = reads.slice(seen);
    seen = reads.length;
    // These reads have no expected failure: join them without swallowing any
    // rejection.
    await Promise.all(batch);
    await Promise.resolve();
  }
}

// ---------------------------------------------------------------------------
// Main producer journey: four real retry causes x unchanged/newer comment.
// ---------------------------------------------------------------------------

const RETRY_REASONS = ["reader-open", "composer-open", "foreground", "storage-recovered"] as const;

for (const reason of RETRY_REASONS) {
  test.each([false, true])(
    `first Send survives a failed save and ${reason} retry (newer comment=%s)`,
    async (newer) => {
      let fail = false;
      let observeSuccess = false;
      let runtime: DocumentAnnotationRuntime | undefined;
      const f = await fixture(`producer-${reason}-${newer}`, {
        beforeCommit: (operation) => {
          if (operation === "save" && fail) throw new Error("first-save fault");
        },
        onStorageSuccess: () => {
          if (reason === "storage-recovered" && observeSuccess) runtime?.storageSucceeded();
        },
      });
      await settleBaseline(f);
      const baseline = await f.drafts.load(f.key);
      if (!baseline) throw new Error("missing baseline");
      const baselineViews = await f.repo.listAnnotations(f.document);
      const baselineReceipt = baselineViews.find((v) => v.receipt.operationId === "op-1")?.receipt;
      if (!baselineReceipt) throw new Error("missing baseline receipt");
      runtime = createDocumentAnnotationRuntime(f.repo);
      const retainedRuntime = runtime;
      const ports: DocumentAnnotationPorts = { repository: f.repo, runtime };
      cleanupAfterTest(async () => {
        retainedRuntime.dispose();
        await retainedRuntime.whenIdle();
      });
      cleanupAfterTest(() => disposeDocumentReaderWork("doc-first-send"));
      const client = new FakeClient("ready");
      const previousConnection = connectionStore.getState();
      cleanupAfterTest(() => {
        connectionStore.setState(previousConnection);
        client.close();
      });
      // Script BEFORE connect's synchronous publish so the Composer's mount read
      // is answered rather than rejected.
      client.on("evener/command/list", () => ({ commands: [] }));
      connectionStore.getState().connect(client);
      const saves = vi.spyOn(f.repo, "saveAnnotation");
      const retries = vi.spyOn(f.repo, "retryPending");
      const enqueues = vi.spyOn(f.outbox, "enqueueIntent");
      const resends = vi.spyOn(f.outbox, "resendRecovery");
      const callBaseline = client.calls.length;
      mockRead.mockResolvedValue(docContent(SOURCE, "rev-A"));
      const show = () =>
        render(
          <ClientProvider client={client}>
            <DocPane
              paneId="doc-first-send"
              focused={true}
              params={{ session: "root", path: "NOTES.md", kind: "file", origin: f.destination }}
              annotations={ports}
            />
          </ClientProvider>,
        );
      const firstHost = show();
      cleanupAfterTest(() => firstHost.unmount());
      await selectFirstPassage();

      // Capture the complete durable/live/recovery state BEFORE the failed Send.
      const liveBefore = f.source.getSnapshot();
      const recoveryBefore = await f.outbox.listRecovery();
      const attachmentBytesBefore = await recoveryAttachmentBytes(f);

      fireEvent.change(await screen.findByRole("textbox"), { target: { value: "Keep this scope." } });
      fail = true;
      const work = documentReaderWork("doc-first-send", f.document, ports);
      fireEvent.click(screen.getByRole("button", { name: "Send" })); // ONE Send
      // The status text is already "unsaved" for a nonempty note, so it does not
      // join the failed save. Wait on the real phase transition instead, which
      // only settles when the failed save's publishReader has run.
      await waitFor(() => expect(work.phase).toBe("unsaved"));
      expect(screen.getByRole("status").textContent).toBe("unsaved");
      const attempted = work.attempted;
      if (!attempted) throw new Error("first attempt not retained");
      const original = structuredClone(attempted);
      expect(original).toMatchObject({
        note: "Keep this scope.",
        destination: f.destination,
        lease: { key: f.document },
        capture: { sourceRange: FIRST, selectedRenderedText: "same", contextAfter: "" },
      });
      expect(saves.mock.calls[0]?.[0]).toEqual(original);
      // Preserved durable draft, live composition, recovery record and attachment
      // bytes, plus no committed producer payload/receipt.
      expect(await f.drafts.load(f.key)).toEqual(baseline);
      expect(f.source.getSnapshot().text).toBe(liveBefore.text);
      expect(f.source.getSnapshot().skillNames).toEqual(liveBefore.skillNames);
      expect(f.source.getSnapshot().commandNames).toEqual(liveBefore.commandNames);
      expect(f.source.getSnapshot().mentions).toEqual(liveBefore.mentions);
      expect(await f.outbox.listRecovery()).toEqual(recoveryBefore);
      expect(await recoveryAttachmentBytes(f)).toEqual(attachmentBytesBefore);
      expect(await payloadRowsFor(f, original.operationId)).toHaveLength(0);
      expect(await operationRowsFor(f, original.operationId)).toHaveLength(0);
      expect((await f.repo.listAnnotations(f.document)).map((v) => v.receipt.operationId)).toEqual(["op-1"]);

      if (newer) fireEvent.change(screen.getByRole("textbox"), { target: { value: "Newer exact\ncomment" } });
      firstHost.unmount();
      expect(documentReaderWork("doc-first-send", f.document, ports)).toBe(work);
      expect(work.attempted).toEqual(original);
      if (reason === "reader-open") fail = false;
      const secondHost = show();
      cleanupAfterTest(() => secondHost.unmount());
      await screen.findByText("First occurrence: same");
      if (reason !== "reader-open") {
        // The reopen is still failing, so the registered immutable work remains.
        // Join the retry pass inside act: its repositories wake the pane's view
        // refresh, which must not escape the test.
        await act(async () => {
          await retainedRuntime.whenIdle();
        });
        expect(work.attempted).toEqual(original);
        fail = false;
      }
      // Join each retry trigger with the real React work it starts: the
      // foreground reread, the storage-success retry and the composer-open
      // handoff all update DocReader, so their completion is awaited inside an
      // act scope rather than escaping the test.
      if (reason === "composer-open") {
        await act(async () => {
          const composer = render(
            <ClientProvider client={client}>
              <Composer ref="root" paneId="pane-A" focused={true} source={f.source} />
            </ClientProvider>,
          );
          cleanupAfterTest(() => composer.unmount());
          await f.source.ready();
          await retainedRuntime.whenIdle();
        });
      } else if (reason === "foreground") {
        vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
        await act(async () => {
          fireEvent(document, new Event("visibilitychange"));
          await retainedRuntime.whenIdle();
        });
      } else if (reason === "storage-recovered") {
        await act(async () => {
          observeSuccess = true;
          await f.repo.beginRead(f.document); // REAL completed storage operation, no fabricated event
          await retainedRuntime.whenIdle();
        });
      }
      await waitFor(() => expect(work.attempted).toBeNull());
      await act(async () => {
        await retainedRuntime.whenIdle();
      });
      await waitFor(async () =>
        expect(
          (await f.repo.listAnnotations(f.document)).find((v) => v.receipt.operationId === original.operationId)
            ?.handoff.state,
        ).toBe("already-applied"),
      );
      // Join the settled outcome with every durable read and wake-driven
      // reconciliation it depends on, so no repository/reader update escapes
      // React's act scope during this tail.
      await act(async () => {
        expect(retries).toHaveBeenCalledWith(reason);
        expect(saves.mock.calls.every(([operation]) => JSON.stringify(operation) === JSON.stringify(original))).toBe(
          true,
        );
        const reference =
          'A note on `NOTES.md:1:19-1:22` (captured source positions), selecting “same”: "Keep this scope."\n' +
          'Captured whole-file revision: "rev-A".\n' +
          'Context before: "First occurrence: "; after: "".\n' +
          "Verify the quotation and context against current content before relying on these captured coordinates.";
        // Complete expected durable value/token from the captured baseline,
        // replacing only the appended text and sessionRevision; key, skillNames,
        // commandNames, mentions and namespace incarnation are all compared.
        const expectedDraft = {
          ...baseline,
          text: `${baseline.text}\n\n${reference}`,
          token: { ...baseline.token, sessionRevision: baseline.token.sessionRevision + 1 },
        };
        expect(await f.drafts.load(f.key)).toEqual(expectedDraft);
        const live = f.source.getSnapshot();
        expect(live.text).toBe(expectedDraft.text);
        expect(live.skillNames).toEqual(baseline.skillNames);
        expect(live.commandNames).toEqual(baseline.commandNames ?? []);
        expect(live.mentions).toEqual(baseline.mentions);

        // Receipt identity and independently expected fields, plus the exact stored
        // operation and the raw durable operation row (sequence derived from the
        // baseline fixture receipt, not the produced result).
        const producedView = (await f.repo.listAnnotations(f.document)).find(
          (v) => v.receipt.operationId === original.operationId,
        );
        expect(producedView?.operation).toEqual(original);
        expect(producedView?.receipt.operationId).toBe(original.operationId);
        expect(producedView?.receipt.epoch).toBe(original.lease.epoch);
        expect(producedView?.receipt.sessionSequence).toBe(baselineReceipt.sessionSequence + 1);
        expect(await payloadRowsFor(f, original.operationId)).toEqual([original]);
        const appliedRows = await operationRowsFor(f, original.operationId);
        expect(appliedRows).toHaveLength(1);
        const [appliedRow] = appliedRows;
        expect(appliedRow).toMatchObject({
          operationId: original.operationId,
          namespaceIncarnation: original.lease.namespaceIncarnation,
          epoch: original.lease.epoch,
          sessionSequence: baselineReceipt.sessionSequence + 1,
          state: "applied",
          draftRevision: baseline.token.sessionRevision + 1,
        });

        // Existing recovery record and attachment bytes survive the successful save.
        expect(await f.outbox.listRecovery()).toEqual(recoveryBefore);
        expect(await recoveryAttachmentBytes(f)).toEqual(attachmentBytesBefore);

        // Replay the handoff, then freshly reread durable draft, live source, receipt
        // and payload/operation rows and compare with the completed state: exactly one
        // new operation/receipt and no second append or revision change.
        const completedDraft = await f.drafts.load(f.key);
        const completedLive = f.source.getSnapshot();
        const completedRows = await operationRowsFor(f, original.operationId);
        // The replay handoff publishes a real repository wake, so its React work
        // is joined inside act rather than escaping into the teardown.
        await act(async () => {
          expect((await f.repo.handoff(original.lease.key, original.operationId)).state).toBe("already-applied");
        });
        expect(await f.drafts.load(f.key)).toEqual(completedDraft);
        expect(f.source.getSnapshot().text).toBe(completedLive.text);
        expect(f.source.getSnapshot().skillNames).toEqual(completedLive.skillNames);
        expect(f.source.getSnapshot().commandNames).toEqual(completedLive.commandNames);
        expect(f.source.getSnapshot().mentions).toEqual(completedLive.mentions);
        expect(
          (await f.repo.listAnnotations(f.document)).filter((v) => v.receipt.operationId === original.operationId),
        ).toHaveLength(1);
        expect(await payloadRowsFor(f, original.operationId)).toEqual([original]);
        expect(await operationRowsFor(f, original.operationId)).toEqual(completedRows);

        expect(work.comment.note).toBe(newer ? "Newer exact\ncomment" : "");
        expect(screen.getByRole("status").textContent).toBe(newer ? "unsaved" : "saved");
        expect(enqueues).not.toHaveBeenCalled();
        expect(resends).not.toHaveBeenCalled();
        const prohibited = new Set(["turn/start", "turn/steer", "turn/queue", "thread/start", "thread/resume"]);
        expect(client.calls.slice(callBaseline).filter((call) => prohibited.has(call.method))).toEqual([]);
        expect((await f.outbox.listOutbox()).map((row) => row.clientMutationId)).toEqual([]);
      });
    },
  );
}

// ---------------------------------------------------------------------------
// Connected reader behavior: integration the state-only/component-only tests
// cannot establish. Every control below is the real DocPane surface.
// ---------------------------------------------------------------------------

test("a real selection freezes its capture, a different one does not retarget it, Return inserts a newline, and Cancel clears only unsaved work", async () => {
  const f = await fixture("reader-dom-cancel");
  await settleBaseline(f);
  const { ports } = annotationRuntimePorts(f);
  const client = connectFakeClient();
  const saves = vi.spyOn(f.repo, "saveAnnotation");
  mockRead.mockResolvedValue(docContent(SOURCE, "rev-A"));
  const { maps } = observeBoundMaps();
  mountAnnotated(f, ports, client, "doc-reader-cancel");

  await selectFirstPassage();
  // The comment surface shows the literal passage the pending Send will
  // annotate, before any edit.
  expect(passageText()).toBe("same");
  // Before the first edit the reader keeps its capture current, so a genuinely
  // different completed selection updates the displayed passage.
  await selectPassage("Other context for a second occurrence is nearby: same", 0, 53);
  expect(passageText()).toBe("Other context for a second occurrence is nearby: same");
  await selectFirstPassage();
  expect(passageText()).toBe("same");
  const textbox = (await screen.findByRole("textbox")) as HTMLTextAreaElement;
  fireEvent.change(textbox, { target: { value: "Keep this scope." } });
  const work = documentReaderWork("doc-reader-cancel", f.document, ports);
  expect(work.comment.note).toBe("Keep this scope.");
  // Literal source positions, then each capture identity compared with the
  // ACTUAL accepted snapshot / fresh map identity (which need not be equal).
  const boundMap = maps.at(-1);
  if (!boundMap) throw new Error("map not bound");
  expect(work.comment.capture).toMatchObject({
    sourceRange: FIRST,
    start: { line: 1, column: 19 },
    endInclusive: { line: 1, column: 22 },
    selectedRenderedText: "same",
    contextBefore: "First occurrence: ",
    contextAfter: "",
  });
  expect(work.comment.capture?.displayedSnapshotId).toBe(boundMap.identity.displayedSnapshotId);
  expect(work.comment.capture?.mappingId).toBe(boundMap.identity.mappingId);
  const frozen = work.comment.capture;

  // After the edit the displayed passage is the FROZEN FIRST capture's literal
  // text; the box opened with that passage and keeps showing it.
  expect(passageText()).toBe("same");

  // A genuinely different selection (SECOND, source 73..77) does not retarget
  // the already-frozen FIRST capture.
  await selectSecondPassage();
  expect(work.comment.capture).toEqual(frozen);
  expect(work.comment.note).toBe("Keep this scope.");
  expect(passageText()).toBe("same");
  // A whole-paragraph selection after the edit leaves the displayed passage at
  // the frozen FIRST text, not the newly selected paragraph.
  await selectPassage("Other context for a second occurrence is nearby: same", 0, 53);
  expect(passageText()).toBe("same");

  // Return through the real focused textarea inserts the literal newline and
  // never Sends, hands off or submits.
  const user = userEvent.setup();
  textbox.focus();
  textbox.setSelectionRange(textbox.value.length, textbox.value.length);
  await user.type(textbox, "{Enter}");
  expect(work.comment.note).toBe("Keep this scope.\n");
  expect(textbox.value).toBe("Keep this scope.\n");
  expect(saves).not.toHaveBeenCalled();
  expect(screen.getByRole("status").textContent).toBe("unsaved");

  // Cancel clears ONLY the unsaved comment; saved records and composer text stay.
  const savedBefore = await f.repo.listAnnotations(f.document);
  const composerBefore = f.source.getSnapshot();
  fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
  expect(work.comment).toEqual({ note: "", captureFrozen: false });
  expect(await f.repo.listAnnotations(f.document)).toEqual(savedBefore);
  expect(f.source.getSnapshot()).toEqual(composerBefore);
});

test("same-document remount restores the note, capture and PaneScaffold scroll; key replacement retires old work and unmount retains it", async () => {
  const f = await fixture("reader-remount");
  await settleBaseline(f);
  const { ports } = annotationRuntimePorts(f);
  const client = connectFakeClient();
  mockRead.mockResolvedValue(docContent(SOURCE, "rev-A"));

  const paneId = workspaceStore.getState().openPane("doc", annotatedParams(f));
  const first = mountAnnotated(f, ports, client, paneId);
  await selectFirstPassage();
  fireEvent.change(await screen.findByRole("textbox"), { target: { value: "Keep this scope." } });
  const work = documentReaderWork(paneId, f.document, ports);
  const capture = work.comment.capture;

  // The real PaneScaffold body reports its scroll; the reader retains it.
  const body = paneScaffoldBody();
  body.scrollTop = 137;
  fireEvent.scroll(body);
  expect(work.scrollTop).toBe(137);

  // Host unmount alone retains the work.
  first.unmount();
  expect(documentReaderWork(paneId, f.document, ports)).toBe(work);
  expect(work.comment.note).toBe("Keep this scope.");
  expect(work.comment.capture).toEqual(capture);

  const second = mountAnnotated(f, ports, client, paneId);
  await screen.findByText("First occurrence: same");
  expect((screen.getByRole("textbox") as HTMLTextAreaElement).value).toBe("Keep this scope.");
  // The ordinary same-document remount preserves the displayed captured passage.
  expect(passageText()).toBe("same");
  expect(paneScaffoldBody().scrollTop).toBe(137);
  second.unmount();

  // Key replacement retires the old viewer work under the same pane id. Observe
  // the real rerender's effect WITHOUT calling the factory (which would itself
  // repair the work): publish on the old work after the replacement and require
  // its subscriber to have been retired.
  const oldListener = vi.fn();
  subscribeDocumentReaderWork(paneId, oldListener);
  const replaced = mountAnnotated(f, ports, client, paneId, "OTHER.md");
  await screen.findByRole("heading", { name: "OTHER.md" });
  publishReader(work);
  expect(oldListener).not.toHaveBeenCalled();
  replaced.unmount();
  disposeDocumentReaderWork(paneId);

  // Committed workspace removal through the real store retires the work.
  const removedPane = workspaceStore.getState().openPane("doc", annotatedParams(f));
  mountAnnotated(f, ports, client, removedPane);
  workspaceStore.getState().closePane(removedPane);
  expect(() => subscribeDocumentReaderWork(removedPane, () => {})).toThrow("reader work not created");
});

// ---------------------------------------------------------------------------
// Copyable pending reference: a REAL saved operation whose captured origin
// cannot be resolved (the legacy restored-session fallback) stays pending.
// Revealing its note must expose the complete, independently literal snapshot
// reference as selectable/inert text, mark it pending, and append nothing to the
// composer or any other draft.
// ---------------------------------------------------------------------------
const LEGACY_REFERENCE =
  'A note on `NOTES.md:1:19-1:22` (captured source positions), selecting “same”: "Legacy note."\n' +
  'Captured whole-file revision: "rev-A".\n' +
  'Context before: "First occurrence: "; after: "\\n\\nOther context for a second occurrence is nearby: same\\n".\n' +
  "Verify the quotation and context against current content before relying on these captured coordinates.";

test("revealing a saved pending annotation from an unresolvable origin exposes its full copyable reference without retargeting", async () => {
  const f = await fixture("reader-pending-reference");
  await settleBaseline(f);
  const { ports } = annotationRuntimePorts(f);
  const client = connectFakeClient();
  mockRead.mockResolvedValue(docContent(SOURCE, "rev-A"));
  const { maps } = observeBoundMaps();
  const enqueues = vi.spyOn(f.outbox, "enqueueIntent");
  const resends = vi.spyOn(f.outbox, "resendRecovery");
  const callBaseline = client.calls.length;
  const draftBefore = await f.drafts.load(f.key);
  if (!draftBefore) throw new Error("missing baseline draft");
  const liveBefore = f.source.getSnapshot();

  // A genuine saved operation whose captured recipient cannot be resolved: the
  // restored-session fallback origin never maps to a composition owner.
  const legacy = await acceptedOperation(f.repo, f.document, "op-legacy", "Legacy note.", {
    kind: "browser-restored-session",
  });
  await f.repo.saveAnnotation(legacy);
  // Hand it off for real; the unresolvable recipient leaves it pending.
  expect((await f.repo.handoff(f.document, "op-legacy")).state).toBe("pending");
  const savedView = (await f.repo.listAnnotations(f.document)).find((v) => v.receipt.operationId === "op-legacy");
  expect(savedView?.handoff).toEqual({ state: "pending", reason: "owner-unavailable" });

  const seam = new DocumentGeometrySeam();
  seam.install();
  cleanupAfterTest(() => seam.restore());
  mountAnnotated(f, ports, client, "doc-pending-reference");
  const work = documentReaderWork("doc-pending-reference", f.document, ports);

  await screen.findByText("First occurrence: same");
  const boundMap = await waitForCurrentBoundMap(work, maps);
  const scrollOwner = paneScaffoldBody();
  seam.setElementRect(scrollOwner, SCROLL_RECT);
  seam.configure(boundMap, FIRST, [[FIRST_RECT]]);
  fireEvent.scroll(scrollOwner);
  await waitFor(() => expect(highlightBands().length).toBeGreaterThan(0));

  // Reveal the pending note through the REAL highlight interaction.
  const legacyBand = highlightBands().find((band) => band.getAttribute("data-operation-id") === "op-legacy");
  if (!legacyBand) throw new Error("the legacy pending highlight band is not visible");
  fireEvent.click(legacyBand);

  const reference = document.querySelector<HTMLElement>("[data-annotation-reference]");
  expect(reference?.getAttribute("data-handoff-state")).toBe("pending");
  expect(reference?.textContent).toBe(LEGACY_REFERENCE);

  // The reference is inert selectable text and chooses no substitute recipient;
  // nothing was appended to the composer or its durable draft.
  expect(reference?.querySelector("a, script")).toBeNull();
  expect((await f.drafts.load(f.key))?.text.includes(LEGACY_REFERENCE)).toBe(false);
  expect(await f.drafts.load(f.key)).toEqual(draftBefore);
  expect(f.source.getSnapshot().text).toBe(liveBefore.text);
  expect(enqueues).not.toHaveBeenCalled();
  expect(resends).not.toHaveBeenCalled();
  const prohibited = new Set(["turn/start", "turn/steer", "turn/queue", "thread/start", "thread/resume"]);
  expect(client.calls.slice(callBaseline).filter((call) => prohibited.has(call.method))).toEqual([]);
});

test("an older raw read resolving after a newer one cannot display or authorize its stale source", async () => {
  const f = await fixture("reader-stale-read");
  await settleBaseline(f);
  const { ports } = annotationRuntimePorts(f);
  const client = connectFakeClient();

  // Two explicit held reads: the mount read is the older; the real foreground
  // reread is the newer. Both requests must be issued before either releases.
  let releaseOlder: ((value: DocFileContent) => void) | undefined;
  const older = new Promise<DocFileContent>((resolve) => {
    releaseOlder = resolve;
  });
  let releaseNewer: ((value: DocFileContent) => void) | undefined;
  const newer = new Promise<DocFileContent>((resolve) => {
    releaseNewer = resolve;
  });
  releaseOnCleanup(() => releaseNewer?.(docContent(SOURCE, "rev-new")));
  releaseOnCleanup(() => releaseOlder?.(docContent("STALE SNAPSHOT BODY\n", "rev-old")));
  // The two held requests are the mount read (older) and the real foreground
  // reread (newer); a stable later response answers the legitimate current read
  // the committed fence schedules, so no request falls through to the real
  // (unmocked) platform fetch.
  mockRead.mockReturnValueOnce(older).mockReturnValueOnce(newer).mockResolvedValue(docContent(SOURCE, "rev-new"));

  const { maps } = observeBoundMaps();
  mountAnnotated(f, ports, client, "doc-stale-read");

  // Wait for the first raw request, THEN drive the real foreground reread, and
  // require both requests before releasing either.
  await waitFor(() => expect(mockRead).toHaveBeenCalledTimes(1));
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
  fireEvent(document, new Event("visibilitychange"));
  await waitFor(() => expect(mockRead).toHaveBeenCalledTimes(2));

  // Release the NEWER response first, then the older (stale) one last.
  await act(async () => {
    releaseNewer?.(docContent(SOURCE, "rev-new"));
  });
  await screen.findByText("First occurrence: same");
  await act(async () => {
    releaseOlder?.(docContent("STALE SNAPSHOT BODY\n", "rev-old"));
  });
  const work = documentReaderWork("doc-stale-read", f.document, ports);
  // The committed fence schedules one more current read; join that real read's
  // React work and the map it installs before observing the replacement lease.
  await waitFor(() => {
    const bound = maps.at(-1);
    expect(work.lease).not.toBeNull();
    expect(bound?.identity.displayedSnapshotId).toBe(work.lease?.displayedSnapshotId);
  });

  // The stale snapshot neither displays nor authorizes; the newer accepted
  // source is what the produced capture/lease names.
  expect(screen.queryByText(/STALE SNAPSHOT BODY/)).toBeNull();
  await selectFirstPassage();
  fireEvent.change(await screen.findByRole("textbox"), { target: { value: "Keep this scope." } });
  expect(work.lease?.epoch).toBeTruthy();
  expect(work.comment.capture?.evidence.wholeRevision).toBe("rev-new");
  expect(work.comment.capture?.sourceRange).toEqual(FIRST);
  expect(work.comment.capture?.displayedSnapshotId).toBe(work.lease?.displayedSnapshotId);
});

test("a real source change expires the old epoch's highlight and old-lease Send while preserving the exact note, then recovers with a new operation", async () => {
  let fail = false;
  const f = await fixture("reader-source-change", {
    beforeCommit: (operation) => {
      if (operation === "save" && fail) throw new Error("first-save fault");
    },
  });
  await settleBaseline(f);
  const { ports } = annotationRuntimePorts(f);
  const client = connectFakeClient();
  mockRead.mockResolvedValue(docContent(SOURCE, "rev-A"));
  const { maps } = observeBoundMaps();
  mountAnnotated(f, ports, client, "doc-source-change");

  await selectFirstPassage();
  fireEvent.change(await screen.findByRole("textbox"), { target: { value: "Keep this scope." } });
  fail = true;
  const saves = vi.spyOn(f.repo, "saveAnnotation");
  const work = documentReaderWork("doc-source-change", f.document, ports);
  fireEvent.click(screen.getByRole("button", { name: "Send" }));
  // The failed save's real completion boundary is the retained attempt becoming
  // unsaved; the status text alone is already "unsaved" for a nonempty note.
  await waitFor(() => expect(work.phase).toBe("unsaved"));
  expect(work.attempted).not.toBeNull();
  const firstAttempt = work.attempted;
  if (!firstAttempt) throw new Error("first attempt not retained");
  const composerBefore = f.source.getSnapshot();

  // Return CHANGED raw content/revision through the real read boundary and drive
  // a genuine foreground reread. The production reader invalidates the stale
  // capture/attempt while preserving the exact note and composer text.
  mockRead.mockResolvedValue(docContent(`${SOURCE}CHANGED`, "rev-B"));
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
  fireEvent(document, new Event("visibilitychange"));
  await waitFor(() => expect(work.attempted).toBeNull());
  expect(work.comment.capture).toBeUndefined();
  expect(work.comment.note).toBe("Keep this scope.");
  expect((screen.getByRole("textbox") as HTMLTextAreaElement).value).toBe("Keep this scope.");
  // The appended composer text is unchanged until the new Send.
  expect(f.source.getSnapshot().text).toBe(composerBefore.text);

  // Recover through a current read, fresh selection and an actual Send.
  fail = false;
  // The replacement lease and its real bound map must be READY before selecting;
  // a null attempt alone is also the no-save state and proves neither.
  await waitForCurrentBoundMap(work, maps);
  await selectFirstPassage();
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "Keep this scope." } });
  fireEvent.click(screen.getByRole("button", { name: "Send" }));
  await waitFor(() => expect(work.attempted).toBeNull());
  const produced = saves.mock.calls.at(-1)?.[0];
  if (!produced) throw new Error("no produced operation");
  // A real NEW save with a distinct operation identity, not the retired attempt.
  expect(produced.operationId).not.toBe(firstAttempt.operationId);
  // `saveReaderAnnotation` clears the attempt BEFORE awaiting handoff, so wait
  // for the produced operation's settled handoff before reading.
  await waitFor(async () =>
    expect(
      (await f.repo.listAnnotations(f.document)).find((v) => v.receipt.operationId === produced.operationId)?.handoff
        .state,
    ).toBe("already-applied"),
  );
  const views = await f.repo.listAnnotations(f.document);
  // The changed-source fence expired op-1's epoch: only the new operation is
  // visible, with the new revision, its receipt and a completed handoff.
  expect(views.map((v) => v.receipt.operationId)).toEqual([produced.operationId]);
  expect(views[0]?.operation.capture.evidence.wholeRevision).toBe("rev-B");
  expect(views[0]?.receipt.operationId).toBe(produced.operationId);
  expect(views[0]?.handoff.state).toBe("already-applied");
  expect(await payloadRowsFor(f, produced.operationId)).toEqual([produced]);
});

test("a failed expiry fence keeps the pending phase, suppresses the stale highlight and old-lease Send, then recovers on a current read", async () => {
  let failFence = true;
  const f = await fixture("reader-expiry-pending", {
    beforeExpiryFence: async () => {
      if (failFence) throw new Error("expiry fence fault");
    },
  });
  await settleBaseline(f);
  const { ports } = annotationRuntimePorts(f);
  const client = connectFakeClient();
  const seam = new DocumentGeometrySeam();
  seam.install();
  cleanupAfterTest(() => seam.restore());
  mockRead.mockResolvedValue(docContent(SOURCE, "rev-A"));
  const { maps } = observeBoundMaps();
  mountAnnotated(f, ports, client, "doc-expiry-pending");

  // The baseline saved annotation is a visible highlight first: configure the
  // REAL FIRST fragment and the PaneScaffold body's nonzero clip, then trigger
  // the production re-measure through a real scroll event.
  await screen.findByText("First occurrence: same");
  const boundMap = maps.at(-1);
  if (!boundMap) throw new Error("map not bound");
  const scrollOwner = paneScaffoldBody();
  seam.setElementRect(scrollOwner, SCROLL_RECT);
  seam.configure(boundMap, FIRST, [[FIRST_RECT]]);
  fireEvent.scroll(scrollOwner);
  await waitFor(() => expect(highlightBands().length).toBeGreaterThan(0));

  // A first edit, then changed raw content drives the expiry fence, which faults.
  await selectFirstPassage();
  fireEvent.change(await screen.findByRole("textbox"), { target: { value: "Keep this scope." } });
  const composerBefore = f.source.getSnapshot();
  mockRead.mockResolvedValue(docContent(`${SOURCE}CHANGED`, "rev-B"));
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
  fireEvent(document, new Event("visibilitychange"));

  const work = documentReaderWork("doc-expiry-pending", f.document, ports);
  await waitFor(() => expect(work.phase).toBe("expiry-pending"));
  expect(screen.getByRole("status").textContent).toBe("expiry-pending");
  // While expiry is pending, the stale epoch's highlights are suppressed and an
  // old-lease Send cannot save.
  expect(highlightBands()).toHaveLength(0);
  const saves = vi.spyOn(f.repo, "saveAnnotation");
  fireEvent.click(screen.getByRole("button", { name: "Send" }));
  await act(async () => {
    await Promise.resolve();
  });
  expect(saves).not.toHaveBeenCalled();
  expect(work.comment.note).toBe("Keep this scope.");
  expect(f.source.getSnapshot().text).toBe(composerBefore.text);

  // Remove the fault; a current read and a fresh selection recover.
  failFence = false;
  mockRead.mockResolvedValue(docContent(`${SOURCE}CHANGED`, "rev-B"));
  fireEvent(document, new Event("visibilitychange"));
  await waitFor(() => expect(work.phase).not.toBe("expiry-pending"));
  // The replacement lease and its real bound map must be READY before selecting,
  // not merely the pending phase retired.
  await waitForCurrentBoundMap(work, maps);
  await selectFirstPassage();
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "Keep this scope." } });
  fireEvent.click(screen.getByRole("button", { name: "Send" }));
  await waitFor(() => expect(work.attempted).toBeNull());
  const produced = saves.mock.calls.at(-1)?.[0];
  if (!produced) throw new Error("no produced operation");
  await waitFor(async () =>
    expect(
      (await f.repo.listAnnotations(f.document)).find((v) => v.receipt.operationId === produced.operationId)?.handoff
        .state,
    ).toBe("already-applied"),
  );
});

test("a namespace reset expires the stale capture, suppresses old-lease Send, then a current read and fresh selection produce a settled new save", async () => {
  let fail = false;
  const f = await fixture("reader-reset", {
    beforeCommit: (operation) => {
      if (operation === "save" && fail) throw new Error("first-save fault");
    },
  });
  await settleBaseline(f);
  const { ports } = annotationRuntimePorts(f);
  const client = connectFakeClient();
  mockRead.mockResolvedValue(docContent(SOURCE, "rev-A"));
  const { maps } = observeBoundMaps();
  mountAnnotated(f, ports, client, "doc-reset");

  await selectFirstPassage();
  fireEvent.change(await screen.findByRole("textbox"), { target: { value: "Keep this scope." } });
  fail = true;
  const saves = vi.spyOn(f.repo, "saveAnnotation");
  const work = documentReaderWork("doc-reset", f.document, ports);
  fireEvent.click(screen.getByRole("button", { name: "Send" }));
  // The failed save's real completion boundary is the retained attempt becoming
  // unsaved; the status text alone is already "unsaved" for a nonempty note.
  await waitFor(() => expect(work.phase).toBe("unsaved"));
  if (!work.attempted) throw new Error("first attempt not retained");
  const firstAttempt = work.attempted;
  const savesBeforeReset = saves.mock.calls.length;

  // Reset rotates the real namespace incarnation through the real repository.
  // Join the real reset and the React work its committed namespace wake produces.
  let reset: Awaited<ReturnType<typeof f.repo.resetOwnedNamespace>> | undefined;
  await act(async () => {
    reset = await f.repo.resetOwnedNamespace(f.key.namespace, f.operation.lease.namespaceIncarnation);
  });
  expect(reset?.committed).toBe(true);
  await waitFor(() => expect(work.comment.capture).toBeUndefined());
  expect(work.attempted).toBeNull();
  expect(work.comment.note).toBe("Keep this scope.");

  // While authority is absent, an old-lease Send is suppressed (no new save).
  fail = false;
  fireEvent.click(screen.getByRole("button", { name: "Send" }));
  await act(async () => {
    await Promise.resolve();
  });
  expect(saves.mock.calls.length).toBe(savesBeforeReset);

  // Bounded soft automatic-recovery observation: a proven reset must produce a NEW
  // current accepted lease (the rotated namespace incarnation) and its REAL bound
  // map on the pane's own, before any later foreground event. On frozen source the
  // pane retires authority but starts no read, so this is RECORDED AS A FAILURE
  // without aborting the test: the later foreground action and the real final-save
  // tail still execute, and a missing recovery still fails the command.
  let automaticLease: DocumentReaderWork["lease"] = null;
  try {
    await waitFor(() => expect(work.lease).not.toBeNull(), { timeout: 750 });
    automaticLease = work.lease;
  } catch {
    automaticLease = null;
  }
  expect.soft(automaticLease).not.toBeNull();
  expect.soft(automaticLease?.namespaceIncarnation).not.toBe(f.operation.lease.namespaceIncarnation);
  let automaticMap: BoundDocumentMap | null = null;
  if (automaticLease) {
    try {
      automaticMap = await waitForCurrentBoundMap(work, maps);
    } catch {
      automaticMap = null;
    }
  }
  expect.soft(automaticMap).not.toBeNull();

  // The later foreground reread must bind a NEW current map. Capture the map-cycle
  // boundary before the event so an already-current automatic-recovery map cannot
  // satisfy this check.
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
  const mapsBeforeForeground = maps.length;
  const mapIdBeforeForeground = maps.at(-1)?.identity.mappingId;
  fireEvent(document, new Event("visibilitychange"));
  await waitFor(() => {
    expect(maps.length).toBeGreaterThan(mapsBeforeForeground);
    expect(maps.at(-1)?.identity.displayedSnapshotId).toBe(work.lease?.displayedSnapshotId);
  });
  expect.soft(maps.at(-1)?.identity.mappingId).not.toBe(mapIdBeforeForeground);
  expect(work.lease?.namespaceIncarnation).not.toBe(f.operation.lease.namespaceIncarnation);

  // Recover through a fresh selection and a real Send, and require the produced
  // save's durable payload/receipt and settled handoff.
  await selectFirstPassage();
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "Keep this scope." } });

  // Observe the REAL completion of the exact operation the Send control submits:
  // the handoff await at documentReadState.ts:190, after which
  // saveReaderAnnotation necessarily resumes to its final publishReader at :191.
  // A read-only reader subscriber resolves on that final publication; the
  // call-through handoff spy marks which publication is the final one. No
  // manufactured producer and no whenIdle substitute.
  let submittedOperationId: string | undefined;
  let handoffResolved = false;
  let resolveFinalPublication: (() => void) | undefined;
  const finalPublication = new Promise<void>((resolve) => {
    resolveFinalPublication = resolve;
  });
  const unsubscribeFinalPublication = subscribeDocumentReaderWork("doc-reset", () => {
    if (handoffResolved && work.attempted === null) resolveFinalPublication?.();
  });
  cleanupAfterTest(unsubscribeFinalPublication);
  // A call-through refresh observer: the save and the handoff each publish a real
  // repository wake whose pane view refresh is real React work. Resolve on a
  // refresh that completes AFTER the handoff, so it too is awaited inside act.
  let resolveRefreshAfterHandoff: (() => void) | undefined;
  const refreshAfterHandoff = new Promise<void>((resolve) => {
    resolveRefreshAfterHandoff = resolve;
  });
  const realListAnnotations = ports.repository.listAnnotations.bind(ports.repository);
  vi.spyOn(ports.repository, "listAnnotations").mockImplementation(async (...args) => {
    const views = await realListAnnotations(...args);
    if (handoffResolved) resolveRefreshAfterHandoff?.();
    return views;
  });
  const realHandoff = ports.runtime.handoff.bind(ports.runtime);
  vi.spyOn(ports.runtime, "handoff").mockImplementation(async (key, operationId) => {
    const result = await realHandoff(key, operationId);
    if (submittedOperationId !== undefined && operationId === submittedOperationId) handoffResolved = true;
    return result;
  });

  fireEvent.click(screen.getByRole("button", { name: "Send" }));
  const produced = saves.mock.calls.at(-1)?.[0];
  if (!produced) throw new Error("no produced operation");
  submittedOperationId = produced.operationId;
  // Await the real operation completion PLUS its resulting React work (the final
  // reader publication and the handoff wake's pane refresh) before any plain
  // durable observation.
  await act(async () => {
    await Promise.all([finalPublication, refreshAfterHandoff]);
  });
  expect(work.attempted).toBeNull();
  expect(produced.operationId).not.toBe(firstAttempt.operationId);
  expect(await payloadRowsFor(f, produced.operationId)).toEqual([produced]);
  expect(await operationRowsFor(f, produced.operationId)).toHaveLength(1);
  await waitFor(async () =>
    expect(
      (await f.repo.listAnnotations(f.document)).find((v) => v.receipt.operationId === produced.operationId)?.handoff
        .state,
    ).toBe("already-applied"),
  );
});

test("a failed reader reopen preserves saved views and editable work, then a readable reread recovers and settles a real save", async () => {
  const f = await fixture("reader-reopen-failure");
  await settleBaseline(f);
  const { ports } = annotationRuntimePorts(f);
  const client = connectFakeClient();
  mockRead.mockResolvedValue(docContent(SOURCE, "rev-A"));
  const { maps } = observeBoundMaps();
  mountAnnotated(f, ports, client, "doc-reopen-failure");

  await selectFirstPassage();
  fireEvent.change(await screen.findByRole("textbox"), { target: { value: "Keep this scope." } });
  const work = documentReaderWork("doc-reopen-failure", f.document, ports);
  const savedViewsBefore = await f.repo.listAnnotations(f.document);
  const composerBefore = f.source.getSnapshot();

  // A failed reopen cannot accept empty content, expire saved notes or lose the
  // editable work.
  mockRead.mockRejectedValueOnce(new DocFileError("error", 500));
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
  // Join the real foreground reread AND the shared retained-attempt retry it
  // starts (its repository wake and reader publications), so no React update
  // escapes the act scope into the plain durable read below.
  await act(async () => {
    fireEvent(document, new Event("visibilitychange"));
    await ports.runtime.whenIdle();
  });
  expect(await screen.findByText("Couldn't load file")).toBeTruthy();
  expect(await f.repo.listAnnotations(f.document)).toEqual(savedViewsBefore);
  expect(f.source.getSnapshot()).toEqual(composerBefore);
  expect(work.comment.note).toBe("Keep this scope.");

  // Once readable again, editing recovers and a real save settles.
  mockRead.mockResolvedValue(docContent(SOURCE, "rev-A"));
  await act(async () => {
    fireEvent(document, new Event("visibilitychange"));
    await ports.runtime.whenIdle();
  });
  await screen.findByText("First occurrence: same");
  // The readable reread keeps the retained accepted lease; its real bound map
  // must be installed before a fresh selection can capture through it.
  await waitForCurrentBoundMap(work, maps);
  await selectFirstPassage();
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "Keep this scope." } });
  const saves = vi.spyOn(f.repo, "saveAnnotation");
  fireEvent.click(screen.getByRole("button", { name: "Send" }));
  await waitFor(() => expect(work.attempted).toBeNull());
  const produced = saves.mock.calls.at(-1)?.[0];
  if (!produced) throw new Error("no produced operation");
  // Await the produced receipt's settled state directly: `whenIdle` joins retry
  // passes, not this first-Send handoff.
  await waitFor(async () =>
    expect(
      (await f.repo.listAnnotations(f.document)).find((v) => v.receipt.operationId === produced.operationId)?.handoff
        .state,
    ).toBe("already-applied"),
  );
  const views = await f.repo.listAnnotations(f.document);
  expect(views.length).toBeGreaterThan(savedViewsBefore.length);
  expect(await payloadRowsFor(f, produced.operationId)).toEqual([produced]);
});

// ---------------------------------------------------------------------------
// Reader pipeline ordering: the approved beginRead -> readDocFile -> acceptRead
// -> mapped rendering path, traced with read-only observers placed around the
// real methods. These assertions expose the current pane's fetch-before-ticket
// ordering, its unchanged-content acceptance shortcut and its retry-after-setter
// ordering. They do NOT replace the eight functional producer cases above.
// ---------------------------------------------------------------------------
test("the reader revalidates an unchanged reopen and foreground reread through a real current ticket, and retries reader-open only after the current map is installed", async () => {
  let fail = false;
  const f = await fixture("reader-pipeline-order", {
    beforeCommit: (operation) => {
      if (operation === "save" && fail) throw new Error("first-save fault");
    },
  });
  await settleBaseline(f);
  const { ports } = annotationRuntimePorts(f);
  const client = connectFakeClient();

  // A read-only trace of the real boundaries. Every spy calls through and
  // observes COMPLETION, not mere invocation: a called method can still fail or
  // resolve to nothing, so a genuine ticket is one that settled and was then
  // passed to real acceptRead. None of this replaces behavior or manufactures
  // acceptance state.
  const events: string[] = [];
  const completedReads: Awaited<ReturnType<typeof ports.repository.beginRead>>[] = [];
  const realBeginRead = ports.repository.beginRead.bind(ports.repository);
  vi.spyOn(ports.repository, "beginRead").mockImplementation(async (...args) => {
    const ticket = await realBeginRead(...args);
    completedReads.push(ticket);
    events.push(`begin-read-done:${ticket.requestId}`);
    return ticket;
  });
  // Retain the real completed result together with the real ticket that produced
  // it, so each cycle can attribute its acceptance to the lease/map it publishes.
  const acceptances: {
    ticket: Awaited<ReturnType<typeof ports.repository.beginRead>>;
    result: Awaited<ReturnType<typeof ports.repository.acceptRead>>;
  }[] = [];
  const realAcceptRead = ports.repository.acceptRead.bind(ports.repository);
  vi.spyOn(ports.repository, "acceptRead").mockImplementation(async (ticket, content) => {
    const result = await realAcceptRead(ticket, content);
    if (result.state === "accepted") {
      acceptances.push({ ticket, result });
      events.push(`accept-read-done:${result.lease.displayedSnapshotId}`);
    } else {
      events.push(`accept-read-result:${result.state}`);
    }
    return result;
  });
  // The runtime re-saves retained failures BEFORE it drains the repository
  // (documentAnnotationRuntime.ts save loop, then retryPendingWithOutcome), so a
  // later repository observation can miss an early retry/save. Observe the real
  // runtime retryPending ENTRY instead.
  const realRetryPending = ports.runtime.retryPending.bind(ports.runtime);
  vi.spyOn(ports.runtime, "retryPending").mockImplementation((reason) => {
    events.push(`retry-entry:${reason}`);
    return realRetryPending(reason);
  });
  const maps: BoundDocumentMap[] = [];
  const realBind = documentSourceMapModule.bindDocumentMap;
  vi.spyOn(documentSourceMapModule, "bindDocumentMap").mockImplementation((root, map) => {
    const bound = realBind(root, map);
    maps.push(bound);
    events.push(`bind-map:${bound.identity.displayedSnapshotId}:${bound.identity.mappingId}`);
    return bound;
  });
  mockRead.mockImplementation(async () => {
    events.push("fetch");
    return docContent(SOURCE, "rev-A");
  });

  const host = mountAnnotated(f, ports, client, "doc-pipeline-order");
  await screen.findByText("First occurrence: same");
  const mountEvents = [...events];
  const mountReads = [...completedReads];
  const mountAcceptances = [...acceptances];
  const work = documentReaderWork("doc-pipeline-order", f.document, ports);
  const leaseAfterMount = work.lease;
  if (!leaseAfterMount) throw new Error("mount did not accept a lease");
  const mountMap = maps.at(-1);
  if (!mountMap) throw new Error("mount did not bind a map");

  // The initial mount must COMPLETE its current ticket BEFORE its raw fetch, and
  // the fetched content must be accepted through that exact returned ticket.
  const mountReadDone = mountEvents.findIndex((event) => event.startsWith("begin-read-done"));
  expect.soft(mountReadDone).toBeGreaterThanOrEqual(0);
  expect.soft(mountReadDone >= 0 && mountReadDone < mountEvents.indexOf("fetch")).toBe(true);
  expect.soft(mountReads.length).toBeGreaterThan(0);
  expect.soft(mountAcceptances.length).toBeGreaterThan(0);
  const mountAccepted = mountAcceptances[0];
  expect.soft(mountAccepted !== undefined && mountAccepted.ticket === mountReads[0]).toBe(true);
  expect
    .soft(
      mountAccepted !== undefined &&
        mountAccepted.result.state === "accepted" &&
        mountAccepted.result.lease.displayedSnapshotId === leaseAfterMount.displayedSnapshotId,
    )
    .toBe(true);
  expect(mountMap.identity.displayedSnapshotId).toBe(leaseAfterMount.displayedSnapshotId);

  // A failed first Send retains the immutable attempt for the reopen.
  await selectFirstPassage();
  fireEvent.change(await screen.findByRole("textbox"), { target: { value: "Keep this scope." } });
  fail = true;
  fireEvent.click(screen.getByRole("button", { name: "Send" }));
  await waitFor(() => expect(work.phase).toBe("unsaved"));

  // Reopen the same reader with the retained attempt and an UNCHANGED source.
  host.unmount();
  fail = false;
  const reopenBase = events.length;
  const reopenReadBase = completedReads.length;
  const reopenAcceptBase = acceptances.length;
  const reopened = mountAnnotated(f, ports, client, "doc-pipeline-order");
  await screen.findByText("First occurrence: same");
  await waitFor(() => expect(work.attempted).toBeNull());
  // Join this cycle's real async completion (the retained reader-open retry and
  // the repository wake it publishes) before snapshotting its trace.
  await act(async () => {
    await ports.runtime.whenIdle();
  });
  const reopenEvents = events.slice(reopenBase);
  const reopenReads = completedReads.slice(reopenReadBase);
  const reopenAcceptances = acceptances.slice(reopenAcceptBase);
  const reopenLease = work.lease;
  if (!reopenLease) throw new Error("reopen did not keep a lease");
  const reopenMap = maps.at(-1);
  if (!reopenMap) throw new Error("reopen did not bind a map");

  // The reopen cycle must revalidate through a genuine COMPLETED current ticket:
  // a real beginRead completes BEFORE that cycle's raw fetch, and that exact
  // ticket reaches real acceptRead with an accepted result. Today's
  // unchanged-content shortcut skips the ticket and the acceptance entirely.
  expect.soft(reopenEvents).toContain("fetch");
  const reopenReadDone = reopenEvents.findIndex((event) => event.startsWith("begin-read-done"));
  expect.soft(reopenReadDone).toBeGreaterThanOrEqual(0);
  expect.soft(reopenReadDone >= 0 && reopenReadDone < reopenEvents.indexOf("fetch")).toBe(true);
  const reopenAcceptDone = reopenEvents.findIndex((event) => event.startsWith("accept-read-done"));
  expect.soft(reopenAcceptDone).toBeGreaterThanOrEqual(0);
  expect.soft(reopenReads.length).toBeGreaterThan(0);
  expect.soft(reopenAcceptances.length).toBeGreaterThan(0);
  const reopenAccepted = reopenAcceptances[0];
  expect.soft(reopenAccepted !== undefined && reopenAccepted.ticket === reopenReads[0]).toBe(true);
  // Attribution: this cycle's own completed result must be accepted and its lease
  // must be the one the work and the current map use. acceptances is sliced to
  // this cycle's boundary, so an earlier cycle's identical snapshot cannot
  // satisfy it.
  expect
    .soft(
      reopenAccepted !== undefined &&
        reopenAccepted.result.state === "accepted" &&
        reopenAccepted.result.lease.displayedSnapshotId === reopenLease.displayedSnapshotId,
    )
    .toBe(true);

  // An identical accepted snapshot keeps its durable identity...
  expect.soft(reopenLease.displayedSnapshotId).toBe(leaseAfterMount.displayedSnapshotId);
  // ...and the current real bound map, by its full displayed-snapshot AND mapping
  // identity, is installed and named for that lease.
  expect.soft(reopenMap.identity.displayedSnapshotId).toBe(reopenLease.displayedSnapshotId);
  expect.soft(reopenMap.identity.mappingId).not.toBe(mountMap.identity.mappingId);

  // The retained reader-open retry must begin only AFTER the current real map is
  // installed: the map named for the current lease precedes retryPending entry.
  const reopenRetryEntry = reopenEvents.indexOf("retry-entry:reader-open");
  expect.soft(reopenRetryEntry).toBeGreaterThanOrEqual(0);
  const reopenCurrentMap = reopenEvents.indexOf(
    `bind-map:${reopenLease.displayedSnapshotId}:${reopenMap.identity.mappingId}`,
  );
  expect.soft(reopenCurrentMap).toBeGreaterThanOrEqual(0);
  expect.soft(reopenCurrentMap >= 0 && reopenCurrentMap < reopenRetryEntry).toBe(true);

  // A genuine foreground reread must revalidate through a real current ticket
  // too, not merely re-render the retained snapshot from a local cache.
  const foregroundBase = events.length;
  const foregroundReadBase = completedReads.length;
  const foregroundAcceptBase = acceptances.length;
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
  await act(async () => {
    fireEvent(document, new Event("visibilitychange"));
    await ports.runtime.whenIdle();
  });
  // Join THIS foreground cycle's own acceptance completion and map delivery,
  // bounded by its starting event index: the mount's acceptance must not satisfy
  // this wait. `runtime.whenIdle` alone joins runtime retry, not the pane read.
  await waitFor(() => {
    const cycleEvents = events.slice(foregroundBase);
    expect(cycleEvents.some((event) => event.startsWith("accept-read-done"))).toBe(true);
    expect(cycleEvents.some((event) => event.startsWith("bind-map:"))).toBe(true);
  });
  const foregroundEvents = events.slice(foregroundBase);
  const foregroundReads = completedReads.slice(foregroundReadBase);
  const foregroundAcceptances = acceptances.slice(foregroundAcceptBase);
  expect.soft(foregroundEvents).toContain("fetch");
  const foregroundReadDone = foregroundEvents.findIndex((event) => event.startsWith("begin-read-done"));
  expect.soft(foregroundReadDone).toBeGreaterThanOrEqual(0);
  expect.soft(foregroundReadDone >= 0 && foregroundReadDone < foregroundEvents.indexOf("fetch")).toBe(true);
  expect.soft(foregroundReads.length).toBeGreaterThan(0);
  expect.soft(foregroundAcceptances.length).toBeGreaterThan(0);
  const foregroundAccepted = foregroundAcceptances[0];
  expect.soft(foregroundAccepted !== undefined && foregroundAccepted.ticket === foregroundReads[0]).toBe(true);
  const foregroundLease = work.lease;
  expect
    .soft(
      foregroundAccepted !== undefined &&
        foregroundAccepted.result.state === "accepted" &&
        foregroundAccepted.result.lease.displayedSnapshotId === foregroundLease?.displayedSnapshotId,
    )
    .toBe(true);
  // The foreground cycle's OWN real bound map must be named for its completed
  // accepted lease and carry a FRESH render mapping identity.
  const foregroundExpectedSnapshot =
    foregroundAccepted !== undefined && foregroundAccepted.result.state === "accepted"
      ? foregroundAccepted.result.lease.displayedSnapshotId
      : undefined;
  const foregroundMap = maps.at(-1);
  expect.soft(foregroundMap?.identity.displayedSnapshotId).toBe(foregroundExpectedSnapshot);
  expect.soft(foregroundMap?.identity.mappingId).not.toBe(reopenMap.identity.mappingId);

  reopened.unmount();
});

// ---------------------------------------------------------------------------
// Empty document reopen: a document with NO stored annotations still mounts,
// retains an edited unsent note, and reopens to its unchanged content, current
// accepted map and exact note/capture with no foreground or repair action.
// ---------------------------------------------------------------------------
test("reopening a reader for a document with no saved annotations keeps its unchanged content, current accepted map and unsent note", async () => {
  const f = await fixture("reader-empty-remount");
  // A FRESH document key with zero stored annotations: the fixture seeds op-1 on
  // NOTES.md, so a different path in the same namespace/session exercises an
  // empty document without deleting rows or mocking acceptance.
  const emptyDocument = { ...f.document, path: "EMPTY.md" };
  const { ports } = annotationRuntimePorts(f);
  const client = connectFakeClient();
  mockRead.mockResolvedValue(docContent(SOURCE, "rev-A"));
  const { maps } = observeBoundMaps();

  // Read-only observations of the real boundaries: the initial real list refresh
  // is the required completion boundary, and the raw read is held only to make
  // the ordering deterministic. No observer creates tickets, accepts reads,
  // creates reader work after the producer, repairs state or replaces outcomes.
  const refreshes: number[] = [];
  const realListAnnotations = ports.repository.listAnnotations.bind(ports.repository);
  vi.spyOn(ports.repository, "listAnnotations").mockImplementation(async (...args) => {
    const views = await realListAnnotations(...args);
    refreshes.push(views.length);
    return views;
  });

  const host = mountAnnotated(f, ports, client, "doc-empty-remount", "EMPTY.md");
  await screen.findByText("First occurrence: same");
  await selectFirstPassage();
  fireEvent.change(await screen.findByRole("textbox"), { target: { value: "Keep this scope." } });
  const work = documentReaderWork("doc-empty-remount", emptyDocument, ports);
  const capture = work.comment.capture;
  expect(work.comment.note).toBe("Keep this scope.");
  expect(capture).toBeDefined();
  // The document really holds zero stored annotations.
  expect(await f.repo.listAnnotations(emptyDocument)).toEqual([]);

  // Remount normally with the raw read held. Release only after the remount's real
  // raw read has been ENTERED and its initial real list refresh has COMPLETED.
  // This lets a correct single-ticket mount finish, while a redundant
  // empty-refresh revalidation ticket (the defect) is issued in the refresh
  // continuation and supersedes the mount read before its acceptance — the real
  // ordering made deterministic without counting redundant tickets or sleeping.
  host.unmount();
  let release: ((value: DocFileContent) => void) | undefined;
  const held = new Promise<DocFileContent>((resolve) => {
    release = resolve;
  });
  cleanupAfterTest(() => release?.(docContent(SOURCE, "rev-A")));
  const readsBeforeRemount = mockRead.mock.calls.length;
  const refreshesBeforeRemount = refreshes.length;
  mockRead.mockReturnValueOnce(held);
  const reopened = mountAnnotated(f, ports, client, "doc-empty-remount", "EMPTY.md");

  await waitFor(() => {
    expect(mockRead.mock.calls.length).toBeGreaterThan(readsBeforeRemount);
    expect(refreshes.length).toBeGreaterThan(refreshesBeforeRemount);
  });
  release?.(docContent(SOURCE, "rev-A"));

  // Unchanged content, the current accepted map and the exact note/capture must
  // all become usable with no foreground event and no repair action.
  await screen.findByText("First occurrence: same");
  await waitForCurrentBoundMap(work, maps);
  expect(work.comment.note).toBe("Keep this scope.");
  expect(work.comment.capture).toEqual(capture);
  reopened.unmount();
});

// ---------------------------------------------------------------------------
// Cancel after a failed first Send. Cancel dismisses only the reader's own
// unsaved comment; it must not leave the immutable operation retained by the
// shared retry runtime, where an ordinary later trigger re-saves it and appends
// its reference. A second, different reader's failed operation for the SAME
// document in the SAME runtime must still recover, so this rejects a
// document-wide invalidate as well as the UI-only cancellation defect.
// ---------------------------------------------------------------------------
test("Cancel after a failed first Send retires only that operation, and another reader's failed save for the same document still recovers", async () => {
  // The storage fault is armed only for the two Sends below; the fixture's own
  // baseline op-1 save must complete.
  let fail = false;
  const f = await fixture("cancel-failed-send", {
    beforeCommit: (operation) => {
      if (operation === "save" && fail) throw new Error("first-save fault");
    },
  });
  await settleBaseline(f);
  const { ports } = annotationRuntimePorts(f);
  const client = connectFakeClient();
  mockRead.mockResolvedValue(docContent(SOURCE, "rev-A"));
  const { maps } = observeBoundMaps();
  const enqueues = vi.spyOn(f.outbox, "enqueueIntent");
  const resends = vi.spyOn(f.outbox, "resendRecovery");
  const callBaseline = client.calls.length;
  const baseline = await f.drafts.load(f.key);
  if (!baseline) throw new Error("missing baseline");
  const baselineViews = await f.repo.listAnnotations(f.document);
  const baselineOp1 = baselineViews.find((v) => v.receipt.operationId === "op-1");
  if (!baselineOp1) throw new Error("missing baseline op-1 view");
  const liveBefore = f.source.getSnapshot();
  const recoveryBefore = await f.outbox.listRecovery();
  const attachmentBytesBefore = await recoveryAttachmentBytes(f);

  // Another reader of the SAME document on the SAME shared runtime: its own real
  // rendered selection and a real first Send that faults before persistence. It
  // mounts and faults FIRST, then unmounts, so the global selection stays
  // unambiguous; its retained attempt must survive the later Cancel below.
  const hostB = mountAnnotated(f, ports, client, "doc-sibling");
  const workB = documentReaderWork("doc-sibling", f.document, ports);
  await selectFirstPassage();
  fireEvent.change(await screen.findByRole("textbox"), { target: { value: "Sibling note." } });
  fail = true;
  fireEvent.click(screen.getByRole("button", { name: "Send" }));
  // Join the real completed failed save: the phase transition only settles once
  // the failed save's publishReader has run (the visible "unsaved" label is
  // already present for a nonempty note and proves nothing).
  await waitFor(() => expect(workB.phase).toBe("unsaved"));
  const attemptedB = workB.attempted;
  if (!attemptedB) throw new Error("sibling attempt not retained");
  const originalB = structuredClone(attemptedB);
  expect(originalB).toMatchObject({
    note: "Sibling note.",
    destination: f.destination,
    lease: { key: f.document },
    capture: { sourceRange: FIRST, selectedRenderedText: "same", contextAfter: "" },
  });
  expect(await payloadRowsFor(f, originalB.operationId)).toHaveLength(0);
  expect(await operationRowsFor(f, originalB.operationId)).toHaveLength(0);
  hostB.unmount();

  // The reader under test: the cancelled failed first Send.
  const hostA = mountAnnotated(f, ports, client, "doc-cancel");
  const workA = documentReaderWork("doc-cancel", f.document, ports);
  await selectFirstPassage();
  fireEvent.change(await screen.findByRole("textbox"), { target: { value: "Cancelled note." } });
  fireEvent.click(screen.getByRole("button", { name: "Send" }));
  await waitFor(() => expect(workA.phase).toBe("unsaved"));
  const attemptedA = workA.attempted;
  if (!attemptedA) throw new Error("cancelled attempt not retained");
  const originalA = structuredClone(attemptedA);
  expect(originalA.operationId).not.toBe(originalB.operationId);
  expect(originalA).toMatchObject({
    note: "Cancelled note.",
    destination: f.destination,
    lease: { key: f.document },
    capture: { sourceRange: FIRST, selectedRenderedText: "same", contextAfter: "" },
  });
  // The immutable attempted operation read-only: its payload/receipt have not committed.
  expect(await payloadRowsFor(f, originalA.operationId)).toHaveLength(0);
  expect(await operationRowsFor(f, originalA.operationId)).toHaveLength(0);

  // The user presses the real Cancel control: only the unsaved comment is dismissed.
  fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
  expect(workA.comment).toEqual({ note: "", captureFrozen: false });
  expect(workA.attempted).toBeNull();
  hostA.unmount();

  // Recover storage and drive an ordinary real retry cause (the real foreground
  // event), joining the real retry pass and the reader publications it starts.
  fail = false;
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
  await act(async () => {
    fireEvent(document, new Event("visibilitychange"));
    await ports.runtime.whenIdle();
  });
  await waitFor(() => expect(workB.attempted).toBeNull());
  await act(async () => {
    await ports.runtime.whenIdle();
  });
  await waitFor(async () =>
    expect(
      (await f.repo.listAnnotations(f.document)).find((v) => v.receipt.operationId === originalB.operationId)?.handoff
        .state,
    ).toBe("already-applied"),
  );

  // After the completed recovery, actually remount the cancelled reader through
  // the real helper and require it to open on the recovered content with a
  // current accepted map but NO reopened comment box: Cancel dismissed only that
  // unsaved comment, so nothing about it comes back.
  const reopenedA = mountAnnotated(f, ports, client, "doc-cancel");
  await screen.findByText("First occurrence: same");
  await waitForCurrentBoundMap(workA, maps);
  expect(screen.queryByRole("textbox")).toBeNull();
  expect(screen.queryByRole("button", { name: "Cancel" })).toBeNull();
  expect(workA.comment).toEqual({ note: "", captureFrozen: false });
  expect(workA.attempted).toBeNull();
  reopenedA.unmount();

  // (1) The cancelled operation left no durable payload/receipt, no reference in
  //     the draft, and no reopened unsaved comment. Soft, so the sibling's own
  //     preservation assertions below still run and report.
  const cancelledReference =
    'A note on `NOTES.md:1:19-1:22` (captured source positions), selecting “same”: "Cancelled note."\n' +
    'Captured whole-file revision: "rev-A".\n' +
    'Context before: "First occurrence: "; after: "".\n' +
    "Verify the quotation and context against current content before relying on these captured coordinates.";
  expect.soft(await payloadRowsFor(f, originalA.operationId)).toEqual([]);
  expect.soft(await operationRowsFor(f, originalA.operationId)).toEqual([]);
  expect
    .soft((await f.repo.listAnnotations(f.document)).map((v) => v.receipt.operationId))
    .toEqual(["op-1", originalB.operationId]);
  expect.soft(workA.attempted).toBeNull();
  expect.soft(workA.comment.note).toBe("");

  // (2) The sibling retried, persisted exactly once and appended exactly one
  //     literal reference of its own, with its immutable note/capture/destination.
  const siblingReference =
    'A note on `NOTES.md:1:19-1:22` (captured source positions), selecting “same”: "Sibling note."\n' +
    'Captured whole-file revision: "rev-A".\n' +
    'Context before: "First occurrence: "; after: "".\n' +
    "Verify the quotation and context against current content before relying on these captured coordinates.";
  const persistedB = (await f.repo.listAnnotations(f.document)).find(
    (v) => v.receipt.operationId === originalB.operationId,
  );
  expect(persistedB?.operation).toEqual(originalB);
  expect(persistedB?.receipt.operationId).toBe(originalB.operationId);
  expect(persistedB?.handoff.state).toBe("already-applied");
  expect(await payloadRowsFor(f, originalB.operationId)).toEqual([originalB]);
  expect(await operationRowsFor(f, originalB.operationId)).toHaveLength(1);
  const draftAfter = await f.drafts.load(f.key);
  if (!draftAfter) throw new Error("missing draft after retry");
  expect(draftAfter.text.split(siblingReference)).toHaveLength(2); // exactly once
  expect.soft(draftAfter.text.split(cancelledReference)).toHaveLength(1); // the cancelled ref is absent
  // The draft must be the preserved baseline plus ONLY the sibling's reference;
  // soft so assertion (3) still reports alongside it.
  expect.soft(draftAfter).toEqual({
    ...baseline,
    text: `${baseline.text}\n\n${siblingReference}`,
    token: { ...baseline.token, sessionRevision: baseline.token.sessionRevision + 1 },
  });

  // (3) The already-saved annotation/applied draft survive; unrelated recovery and
  //     outbox state are unchanged and no automatic send/queue/steer/start/resume
  //     occurs.
  const viewAfter = (await f.repo.listAnnotations(f.document)).find((v) => v.receipt.operationId === "op-1");
  expect(viewAfter).toEqual(baselineOp1);
  expect(draftAfter.text.startsWith(baseline.text)).toBe(true);
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
// Cancel while a save is IN FLIGHT, before it commits. The real Cancel control
// stays enabled during a save, so a user can dismiss an unsaved comment while
// its persistence is already running. The repository's `beforeCommit("save")`
// hook is synchronous and runs while the real transaction is still active, so it
// dispatches the real Cancel control there and then returns, letting the
// transaction complete: no invented async hold, no delay after commit.
// ---------------------------------------------------------------------------
test("Cancel at the real precommit boundary of a first Send leaves no cancelled payload, receipt, reference or extra draft revision", async () => {
  let cancelArmed = false;
  let precommitCancelled = false;
  const f = await fixture("inflight-first-send", {
    beforeCommit: (operation) => {
      if (operation !== "save" || !cancelArmed) return;
      cancelArmed = false;
      const cancel = screen.queryByRole("button", { name: "Cancel" });
      if (!cancel) return;
      precommitCancelled = true;
      fireEvent.click(cancel);
    },
  });
  await settleBaseline(f);
  const { ports } = annotationRuntimePorts(f);
  const client = connectFakeClient();
  mockRead.mockResolvedValue(docContent(SOURCE, "rev-A"));
  const { maps } = observeBoundMaps();
  const listReads = observeListAnnotations(f.repo);
  // Call-through observers forward the COMPLETE real argument tuple, so an
  // optional cancellation/context argument a valid repair adds is never silently
  // dropped. The first Send joins the real runtime.save promise, which already
  // returns null for a handled save failure, rather than a raw repository call.
  const realRuntimeSave = ports.runtime.save.bind(ports.runtime);
  let inFlightSave: Promise<unknown> | undefined;
  let sentOperation: PreparedAnnotation | undefined;
  vi.spyOn(ports.runtime, "save").mockImplementation((...args) => {
    const [operation] = args;
    sentOperation = operation;
    const promise = realRuntimeSave(...args);
    inFlightSave = promise;
    return promise;
  });
  const realHandoff = ports.runtime.handoff.bind(ports.runtime);
  let handoffPromise: Promise<unknown> | undefined;
  vi.spyOn(ports.runtime, "handoff").mockImplementation((...args) => {
    const promise = realHandoff(...args);
    handoffPromise = promise;
    return promise;
  });
  const enqueues = vi.spyOn(f.outbox, "enqueueIntent");
  const resends = vi.spyOn(f.outbox, "resendRecovery");
  const callBaseline = client.calls.length;
  const baseline = await f.drafts.load(f.key);
  if (!baseline) throw new Error("missing baseline");
  const baselineOp1 = (await f.repo.listAnnotations(f.document)).find((v) => v.receipt.operationId === "op-1");
  if (!baselineOp1) throw new Error("missing baseline op-1 view");
  const liveBefore = f.source.getSnapshot();
  const recoveryBefore = await f.outbox.listRecovery();
  const attachmentBytesBefore = await recoveryAttachmentBytes(f);

  const hostA = mountAnnotated(f, ports, client, "doc-inflight-first");
  const workA = documentReaderWork("doc-inflight-first", f.document, ports);
  await selectFirstPassage();
  fireEvent.change(await screen.findByRole("textbox"), { target: { value: "Cancelled note." } });
  cancelArmed = true;
  fireEvent.click(screen.getByRole("button", { name: "Send" }));
  if (!sentOperation) throw new Error("first Send did not start its save");
  if (!inFlightSave) throw new Error("missing in-flight save promise");
  const originalA = structuredClone(sentOperation);
  const inFlight = inFlightSave;

  // Join the real in-flight save (where the precommit Cancel fired), then the
  // reader's save continuation and its handoff, then every repository-wake
  // refresh each commit starts — all with the reader MOUNTED. This waits on the
  // real async owners, never on the unwanted append, so a correct fix still
  // reaches every assertion.
  await act(async () => {
    await inFlight;
    await settleReaderRefreshes(listReads);
    await Promise.resolve();
    if (handoffPromise) await handoffPromise;
    await settleReaderRefreshes(listReads);
  });
  expect(precommitCancelled).toBe(true);

  const cancelledReference =
    'A note on `NOTES.md:1:19-1:22` (captured source positions), selecting “same”: "Cancelled note."\n' +
    'Captured whole-file revision: "rev-A".\n' +
    'Context before: "First occurrence: "; after: "".\n' +
    "Verify the quotation and context against current content before relying on these captured coordinates.";
  // Soft: the cancelled absence must not abort the preservation/remount tails.
  expect.soft(await payloadRowsFor(f, originalA.operationId)).toEqual([]);
  expect.soft(await operationRowsFor(f, originalA.operationId)).toEqual([]);
  expect.soft((await f.repo.listAnnotations(f.document)).map((v) => v.receipt.operationId)).toEqual(["op-1"]);
  expect.soft(workA.attempted).toBeNull();
  expect.soft(workA.comment.note).toBe("");
  const draftAfter = await f.drafts.load(f.key);
  if (!draftAfter) throw new Error("missing draft");
  expect.soft(draftAfter.text.split(cancelledReference)).toHaveLength(1);
  expect.soft(draftAfter).toEqual(baseline);

  // Real remount: the cancelled comment box must not reopen. The reader was
  // mounted through the whole save above; this is the separate preservation tail.
  hostA.unmount();
  const reopenedA = mountAnnotated(f, ports, client, "doc-inflight-first");
  await screen.findByText("First occurrence: same");
  await waitForCurrentBoundMap(workA, maps);
  expect(screen.queryByRole("textbox")).toBeNull();
  expect(screen.queryByRole("button", { name: "Cancel" })).toBeNull();
  reopenedA.unmount();

  // Preservation: the saved annotation/draft and unrelated state survive, and no
  // automatic submission occurs.
  const viewAfter = (await f.repo.listAnnotations(f.document)).find((v) => v.receipt.operationId === "op-1");
  expect(viewAfter).toEqual(baselineOp1);
  expect(draftAfter.text.startsWith(baseline.text)).toBe(true);
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

test("Cancel at the real precommit boundary of a foreground retry leaves no cancelled persistence and the same-document sibling still recovers", async () => {
  let fail = false;
  let cancelArmed = false;
  let cancelTargetId: string | undefined;
  let savingOperationId: string | undefined;
  let precommitCancelled = false;
  const f = await fixture("inflight-retry", {
    beforeCommit: (operation) => {
      if (operation !== "save") return;
      if (fail) throw new Error("first-save fault");
      if (!cancelArmed || savingOperationId === undefined || savingOperationId !== cancelTargetId) return;
      cancelArmed = false;
      const cancel = screen.queryByRole("button", { name: "Cancel" });
      if (!cancel) return;
      precommitCancelled = true;
      fireEvent.click(cancel);
    },
  });
  await settleBaseline(f);
  const { ports } = annotationRuntimePorts(f);
  const client = connectFakeClient();
  mockRead.mockResolvedValue(docContent(SOURCE, "rev-A"));
  const { maps } = observeBoundMaps();
  const listReads = observeListAnnotations(f.repo);
  const realSaveAnnotation = f.repo.saveAnnotation.bind(f.repo);
  vi.spyOn(f.repo, "saveAnnotation").mockImplementation((...args) => {
    const [operation] = args;
    savingOperationId = operation.operationId;
    return realSaveAnnotation(...args);
  });
  const enqueues = vi.spyOn(f.outbox, "enqueueIntent");
  const resends = vi.spyOn(f.outbox, "resendRecovery");
  const callBaseline = client.calls.length;
  const baseline = await f.drafts.load(f.key);
  if (!baseline) throw new Error("missing baseline");
  const baselineOp1 = (await f.repo.listAnnotations(f.document)).find((v) => v.receipt.operationId === "op-1");
  if (!baselineOp1) throw new Error("missing baseline op-1 view");
  const liveBefore = f.source.getSnapshot();
  const recoveryBefore = await f.outbox.listRecovery();
  const attachmentBytesBefore = await recoveryAttachmentBytes(f);

  // A same-document sibling reader on the SAME shared runtime fails first, then
  // unmounts, so the global selection stays unambiguous and its retained failure
  // must still recover.
  const hostB = mountAnnotated(f, ports, client, "doc-inflight-sibling");
  const workB = documentReaderWork("doc-inflight-sibling", f.document, ports);
  await selectFirstPassage();
  fireEvent.change(await screen.findByRole("textbox"), { target: { value: "Sibling note." } });
  fail = true;
  fireEvent.click(screen.getByRole("button", { name: "Send" }));
  await waitFor(() => expect(workB.phase).toBe("unsaved"));
  const attemptedB = workB.attempted;
  if (!attemptedB) throw new Error("sibling attempt not retained");
  const originalB = structuredClone(attemptedB);
  hostB.unmount();

  // The cancelled reader: its first Send fails before persistence and stays
  // mounted through the whole retry.
  const hostA = mountAnnotated(f, ports, client, "doc-inflight-retry");
  const workA = documentReaderWork("doc-inflight-retry", f.document, ports);
  await selectFirstPassage();
  fireEvent.change(await screen.findByRole("textbox"), { target: { value: "Cancelled note." } });
  fireEvent.click(screen.getByRole("button", { name: "Send" }));
  await waitFor(() => expect(workA.phase).toBe("unsaved"));
  const attemptedA = workA.attempted;
  if (!attemptedA) throw new Error("cancelled attempt not retained");
  const originalA = structuredClone(attemptedA);
  expect(originalA.operationId).not.toBe(originalB.operationId);
  // Before the retry starts, no payload/receipt has committed for the cancelled op.
  expect(await payloadRowsFor(f, originalA.operationId)).toHaveLength(0);
  expect(await operationRowsFor(f, originalA.operationId)).toHaveLength(0);

  // Recover storage, then start a genuine foreground retry while the reader stays
  // mounted. Dispose the real Cancel control at the retry save's precommit
  // boundary, before the transaction commits, then let it complete.
  fail = false;
  cancelTargetId = originalA.operationId;
  cancelArmed = true;
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
  await act(async () => {
    fireEvent(document, new Event("visibilitychange"));
    await ports.runtime.whenIdle();
  });
  expect(precommitCancelled).toBe(true);
  await waitFor(() => expect(workB.attempted).toBeNull());
  await act(async () => {
    await ports.runtime.whenIdle();
  });
  await waitFor(async () =>
    expect(
      (await f.repo.listAnnotations(f.document)).find((v) => v.receipt.operationId === originalB.operationId)?.handoff
        .state,
    ).toBe("already-applied"),
  );
  // Join the retry drain's repository-wake reader refreshes inside act while the
  // reader stays mounted.
  await act(async () => {
    await settleReaderRefreshes(listReads);
  });

  const cancelledReference =
    'A note on `NOTES.md:1:19-1:22` (captured source positions), selecting “same”: "Cancelled note."\n' +
    'Captured whole-file revision: "rev-A".\n' +
    'Context before: "First occurrence: "; after: "".\n' +
    "Verify the quotation and context against current content before relying on these captured coordinates.";
  // Soft: the cancelled absence must not abort the sibling/preservation tails.
  expect.soft(await payloadRowsFor(f, originalA.operationId)).toEqual([]);
  expect.soft(await operationRowsFor(f, originalA.operationId)).toEqual([]);
  expect
    .soft((await f.repo.listAnnotations(f.document)).map((v) => v.receipt.operationId))
    .toEqual(["op-1", originalB.operationId]);
  expect.soft(workA.attempted).toBeNull();
  expect.soft(workA.comment.note).toBe("");

  // The sibling retried, persisted exactly once and retained its immutable work.
  const siblingReference =
    'A note on `NOTES.md:1:19-1:22` (captured source positions), selecting “same”: "Sibling note."\n' +
    'Captured whole-file revision: "rev-A".\n' +
    'Context before: "First occurrence: "; after: "".\n' +
    "Verify the quotation and context against current content before relying on these captured coordinates.";
  const persistedB = (await f.repo.listAnnotations(f.document)).find(
    (v) => v.receipt.operationId === originalB.operationId,
  );
  expect(persistedB?.operation).toEqual(originalB);
  expect(persistedB?.receipt.operationId).toBe(originalB.operationId);
  expect(persistedB?.handoff.state).toBe("already-applied");
  expect(await payloadRowsFor(f, originalB.operationId)).toEqual([originalB]);
  expect(await operationRowsFor(f, originalB.operationId)).toHaveLength(1);
  const draftAfter = await f.drafts.load(f.key);
  if (!draftAfter) throw new Error("missing draft after retry");
  expect(draftAfter.text.split(siblingReference)).toHaveLength(2);
  expect.soft(draftAfter.text.split(cancelledReference)).toHaveLength(1);
  expect.soft(draftAfter).toEqual({
    ...baseline,
    text: `${baseline.text}\n\n${siblingReference}`,
    token: { ...baseline.token, sessionRevision: baseline.token.sessionRevision + 1 },
  });

  // Real remount of the cancelled reader: no reopened comment box.
  hostA.unmount();
  const reopenedA = mountAnnotated(f, ports, client, "doc-inflight-retry");
  await screen.findByText("First occurrence: same");
  await waitForCurrentBoundMap(workA, maps);
  expect(screen.queryByRole("textbox")).toBeNull();
  expect(screen.queryByRole("button", { name: "Cancel" })).toBeNull();
  reopenedA.unmount();

  const viewAfter = (await f.repo.listAnnotations(f.document)).find((v) => v.receipt.operationId === "op-1");
  expect(viewAfter).toEqual(baselineOp1);
  expect(draftAfter.text.startsWith(baseline.text)).toBe(true);
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
// Read-storage faults. A metadata-storage failure is not proof the document
// changed: healthy raw reading must continue and a failed first Send must retain
// its exact immutable operation until real recovery.
// ---------------------------------------------------------------------------
test("a first reader open whose beginRead storage read faults still shows the scripted raw content with no fabricated authority, then recovers through an ordinary read", async () => {
  const f = await fixture("read-fault-first-open");
  await settleBaseline(f);
  const { ports } = annotationRuntimePorts(f);
  const client = connectFakeClient();
  mockRead.mockResolvedValue(docContent(SOURCE, "rev-A"));
  const { maps } = observeBoundMaps();
  const { acceptances } = observeReadOwners(f.repo);
  const listReads = observeListAnnotations(f.repo);
  const enqueues = vi.spyOn(f.outbox, "enqueueIntent");
  const resends = vi.spyOn(f.outbox, "resendRecovery");
  const callBaseline = client.calls.length;
  const realRuntimeSave = ports.runtime.save.bind(ports.runtime);
  let inFlightSave: Promise<unknown> | undefined;
  let sentOperation: PreparedAnnotation | undefined;
  vi.spyOn(ports.runtime, "save").mockImplementation((...args) => {
    const [operation] = args;
    sentOperation = operation;
    const promise = realRuntimeSave(...args);
    inFlightSave = promise;
    return promise;
  });
  const realHandoff = ports.runtime.handoff.bind(ports.runtime);
  let handoffPromise: Promise<unknown> | undefined;
  vi.spyOn(ports.runtime, "handoff").mockImplementation((...args) => {
    const promise = realHandoff(...args);
    handoffPromise = promise;
    return promise;
  });
  const fault = { value: false };
  faultBeginReadTransaction(fault);
  fault.value = true;

  mountAnnotated(f, ports, client, "doc-read-fault-open");
  const work = documentReaderWork("doc-read-fault-open", f.document, ports);

  // The supplied scripted readDocFile adapter returned the fixture content even
  // though the metadata read failed, so the reader must render that content and
  // leave the loading skeleton. (This is a fixture adapter, not a real HTTP journey.)
  await screen.findByText("First occurrence: same");
  expect(mockRead).toHaveBeenCalled();
  expect(screen.queryByRole("status", { name: "Loading" })).toBeNull();
  // No fabricated annotation authority from the failed metadata read: the
  // pre-acceptance slot is empty and a real selection opens no usable box.
  expect(work.lease).toBeNull();
  expect(work.attempted).toBeNull();
  expect(highlightBands()).toHaveLength(0);
  await selectFirstPassage();
  expect(screen.queryByRole("textbox")).toBeNull();
  expect(work.comment.capture).toBeUndefined();
  const baselineOp1 = (await f.repo.listAnnotations(f.document)).find((v) => v.receipt.operationId === "op-1");
  if (!baselineOp1) throw new Error("missing baseline op-1 view");

  // Remove the storage failure and drive the real foreground reread. Join the
  // FRESH real acceptance completion AND its bound-map publication before the
  // recovery is complete: an old map/lease is not evidence a new read finished.
  fault.value = false;
  const acceptBase = acceptances.length;
  const mapBase = maps.length;
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
  await act(async () => {
    fireEvent(document, new Event("visibilitychange"));
    await ports.runtime.whenIdle();
  });
  await waitForFreshAcceptedMap(acceptances, maps, work, acceptBase, mapBase);

  // A normal selection and Send now settle through the real current authority.
  await selectFirstPassage();
  fireEvent.change(await screen.findByRole("textbox"), { target: { value: "Recovered note." } });
  fireEvent.click(screen.getByRole("button", { name: "Send" }));
  if (!sentOperation) throw new Error("first Send did not start its save");
  if (!inFlightSave) throw new Error("missing in-flight save promise");
  const produced = structuredClone(sentOperation);
  await act(async () => {
    await inFlightSave;
    await settleReaderRefreshes(listReads);
    await Promise.resolve();
    if (handoffPromise) await handoffPromise;
    await settleReaderRefreshes(listReads);
  });
  expect(work.attempted).toBeNull();
  const recoveredReference =
    'A note on `NOTES.md:1:19-1:22` (captured source positions), selecting “same”: "Recovered note."\n' +
    'Captured whole-file revision: "rev-A".\n' +
    'Context before: "First occurrence: "; after: "".\n' +
    "Verify the quotation and context against current content before relying on these captured coordinates.";
  await waitFor(async () =>
    expect(
      (await f.repo.listAnnotations(f.document)).find((v) => v.receipt.operationId === produced.operationId)?.handoff
        .state,
    ).toBe("already-applied"),
  );
  expect(await payloadRowsFor(f, produced.operationId)).toEqual([produced]);
  const draftAfter = await f.drafts.load(f.key);
  if (!draftAfter) throw new Error("missing draft after recovery");
  expect(draftAfter.text.split(recoveredReference)).toHaveLength(2); // exactly once
  expect((await f.repo.listAnnotations(f.document)).find((v) => v.receipt.operationId === "op-1")).toEqual(baselineOp1);
  // The recovered save causes no automatic submission or outbox mutation.
  expect(enqueues).not.toHaveBeenCalled();
  expect(resends).not.toHaveBeenCalled();
  const prohibited = new Set(["turn/start", "turn/steer", "turn/queue", "thread/start", "thread/resume"]);
  expect(client.calls.slice(callBaseline).filter((call) => prohibited.has(call.method))).toEqual([]);
  expect((await f.outbox.listOutbox()).map((row) => row.clientMutationId)).toEqual([]);
});

test("a failed first Send keeps its exact immutable operation, note and capture through an unchanged reread whose beginRead faults, then applies it once on recovery", async () => {
  let fail = false;
  const f = await fixture("read-fault-failed-send", {
    beforeCommit: (operation) => {
      if (operation === "save" && fail) throw new Error("first-save fault");
    },
  });
  await settleBaseline(f);
  const { ports } = annotationRuntimePorts(f);
  const client = connectFakeClient();
  mockRead.mockResolvedValue(docContent(SOURCE, "rev-A"));
  const { maps } = observeBoundMaps();
  const listReads = observeListAnnotations(f.repo);
  const enqueues = vi.spyOn(f.outbox, "enqueueIntent");
  const resends = vi.spyOn(f.outbox, "resendRecovery");
  const callBaseline = client.calls.length;
  const baseline = await f.drafts.load(f.key);
  if (!baseline) throw new Error("missing baseline");
  const baselineOp1 = (await f.repo.listAnnotations(f.document)).find((v) => v.receipt.operationId === "op-1");
  if (!baselineOp1) throw new Error("missing baseline op-1 view");
  const liveBefore = f.source.getSnapshot();
  const recoveryBefore = await f.outbox.listRecovery();
  const attachmentBytesBefore = await recoveryAttachmentBytes(f);
  const { acceptances } = observeReadOwners(f.repo);
  const fault = { value: false };
  faultBeginReadTransaction(fault);

  const host = mountAnnotated(f, ports, client, "doc-read-fault-send");
  const work = documentReaderWork("doc-read-fault-send", f.document, ports);
  await screen.findByText("First occurrence: same");
  await selectFirstPassage();
  fireEvent.change(await screen.findByRole("textbox"), { target: { value: "Keep this scope." } });
  fail = true;
  fireEvent.click(screen.getByRole("button", { name: "Send" }));
  await waitFor(() => expect(work.phase).toBe("unsaved"));
  const attempted = work.attempted;
  if (!attempted) throw new Error("first attempt not retained");
  const original = structuredClone(attempted);
  const commentBefore = structuredClone(work.comment);
  expect(original).toMatchObject({
    note: "Keep this scope.",
    destination: f.destination,
    lease: { key: f.document },
    capture: { sourceRange: FIRST, selectedRenderedText: "same", contextAfter: "" },
  });
  expect(await payloadRowsFor(f, original.operationId)).toHaveLength(0);
  expect(await operationRowsFor(f, original.operationId)).toHaveLength(0);

  // The unchanged reread is a real remount and its beginRead faults. The raw
  // content stays readable and the retained immutable work must survive: a
  // storage read fault is not evidence of a source change.
  fault.value = true;
  const acceptedBefore = acceptances.length;
  host.unmount();
  mountAnnotated(f, ports, client, "doc-read-fault-send");
  await screen.findByText("First occurrence: same");
  expect(acceptances.length).toBe(acceptedBefore); // the fault creates no new accepted authority
  expect(work.attempted).toEqual(original);
  expect(work.comment).toEqual(commentBefore);
  // The UI contract, not just the internal slot: the authored note is still shown
  // and editable, honestly labeled unsaved.
  const textbox = await screen.findByRole("textbox");
  expect((textbox as HTMLTextAreaElement).value).toBe("Keep this scope.");
  expect(screen.getByRole("status").textContent).toBe("unsaved");
  fireEvent.change(textbox, { target: { value: "Edited after fault." } });
  expect(work.comment.note).toBe("Edited after fault.");
  fireEvent.change(textbox, { target: { value: "Keep this scope." } });
  expect(work.comment.note).toBe("Keep this scope.");
  expect(await payloadRowsFor(f, original.operationId)).toHaveLength(0);
  expect(await operationRowsFor(f, original.operationId)).toHaveLength(0);

  // Recover storage, then drive the real foreground reread AND the shared
  // retained-failure retry it starts, all with the reader mounted.
  fault.value = false;
  fail = false;
  const acceptBase = acceptances.length;
  const mapBase = maps.length;
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
  await act(async () => {
    fireEvent(document, new Event("visibilitychange"));
    await ports.runtime.whenIdle();
  });
  await waitFor(() => expect(work.attempted).toBeNull());
  await waitForFreshAcceptedMap(acceptances, maps, work, acceptBase, mapBase);
  await act(async () => {
    await settleReaderRefreshes(listReads);
  });
  const keepReference =
    'A note on `NOTES.md:1:19-1:22` (captured source positions), selecting “same”: "Keep this scope."\n' +
    'Captured whole-file revision: "rev-A".\n' +
    'Context before: "First occurrence: "; after: "".\n' +
    "Verify the quotation and context against current content before relying on these captured coordinates.";
  await waitFor(async () =>
    expect(
      (await f.repo.listAnnotations(f.document)).find((v) => v.receipt.operationId === original.operationId)?.handoff
        .state,
    ).toBe("already-applied"),
  );
  await act(async () => {
    await ports.runtime.whenIdle();
    await settleReaderRefreshes(listReads);
  });
  // The original immutable operation persisted exactly once, with its exact
  // note/capture, and appended its literal reference once.
  const persisted = (await f.repo.listAnnotations(f.document)).find(
    (v) => v.receipt.operationId === original.operationId,
  );
  expect(persisted?.operation).toEqual(original);
  expect(persisted?.handoff.state).toBe("already-applied");
  expect(await payloadRowsFor(f, original.operationId)).toEqual([original]);
  expect(await operationRowsFor(f, original.operationId)).toHaveLength(1);
  const draftAfter = await f.drafts.load(f.key);
  if (!draftAfter) throw new Error("missing draft after recovery");
  expect(draftAfter.text.split(keepReference)).toHaveLength(2); // exactly once
  expect(draftAfter).toEqual({
    ...baseline,
    text: `${baseline.text}\n\n${keepReference}`,
    token: { ...baseline.token, sessionRevision: baseline.token.sessionRevision + 1 },
  });
  // The saved baseline and unrelated state survive; no submission mutation.
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

test("a failed first Send survives an unchanged acceptRead fault with its authority and capture intact, then applies the same immutable operation once on recovery", async () => {
  let fail = false;
  let acceptFault = false;
  const f = await fixture("read-fault-accept", {
    beforeCommit: (operation) => {
      if (operation === "save" && fail) throw new Error("first-save fault");
      if (operation === "acceptRead" && acceptFault) throw new Error("acceptance fault");
    },
  });
  await settleBaseline(f);
  const { ports } = annotationRuntimePorts(f);
  const client = connectFakeClient();
  mockRead.mockResolvedValue(docContent(SOURCE, "rev-A"));
  const { maps } = observeBoundMaps();
  const listReads = observeListAnnotations(f.repo);
  const enqueues = vi.spyOn(f.outbox, "enqueueIntent");
  const resends = vi.spyOn(f.outbox, "resendRecovery");
  const callBaseline = client.calls.length;
  const baseline = await f.drafts.load(f.key);
  if (!baseline) throw new Error("missing baseline");
  const baselineOp1 = (await f.repo.listAnnotations(f.document)).find((v) => v.receipt.operationId === "op-1");
  if (!baselineOp1) throw new Error("missing baseline op-1 view");
  const liveBefore = f.source.getSnapshot();
  const recoveryBefore = await f.outbox.listRecovery();
  const attachmentBytesBefore = await recoveryAttachmentBytes(f);
  const { acceptances } = observeReadOwners(f.repo);

  const host = mountAnnotated(f, ports, client, "doc-read-fault-accept");
  const work = documentReaderWork("doc-read-fault-accept", f.document, ports);
  await screen.findByText("First occurrence: same");
  await selectFirstPassage();
  fireEvent.change(await screen.findByRole("textbox"), { target: { value: "Keep this scope." } });
  fail = true;
  fireEvent.click(screen.getByRole("button", { name: "Send" }));
  await waitFor(() => expect(work.phase).toBe("unsaved"));
  const attempted = work.attempted;
  if (!attempted) throw new Error("first attempt not retained");
  const original = structuredClone(attempted);
  const commentBefore = structuredClone(work.comment);
  expect(original).toMatchObject({
    note: "Keep this scope.",
    destination: f.destination,
    lease: { key: f.document },
    capture: { sourceRange: FIRST, selectedRenderedText: "same", contextAfter: "" },
  });
  expect(await payloadRowsFor(f, original.operationId)).toHaveLength(0);
  expect(await operationRowsFor(f, original.operationId)).toHaveLength(0);

  // An unchanged reread whose ACCEPTANCE faults: the raw read completes, but the
  // failed first-save operation, its frozen capture and the current accepted
  // authority must all survive. A storage fault is not a source change.
  acceptFault = true;
  const acceptedBefore = acceptances.length;
  host.unmount();
  mountAnnotated(f, ports, client, "doc-read-fault-accept");
  await screen.findByText("First occurrence: same");
  expect(acceptances.length).toBe(acceptedBefore); // the failed acceptance creates no new accepted authority
  expect(work.attempted).toEqual(original);
  expect(work.comment).toEqual(commentBefore);
  // The UI contract, not just the internal slot: the authored note is still shown
  // and editable, honestly labeled unsaved.
  const textbox = await screen.findByRole("textbox");
  expect((textbox as HTMLTextAreaElement).value).toBe("Keep this scope.");
  expect(screen.getByRole("status").textContent).toBe("unsaved");
  fireEvent.change(textbox, { target: { value: "Edited after fault." } });
  expect(work.comment.note).toBe("Edited after fault.");
  fireEvent.change(textbox, { target: { value: "Keep this scope." } });
  expect(work.comment.note).toBe("Keep this scope.");
  expect(await payloadRowsFor(f, original.operationId)).toHaveLength(0);
  expect(await operationRowsFor(f, original.operationId)).toHaveLength(0);

  // Recover: acceptance and save faults removed; drive the real foreground
  // reread and the shared retained-failure retry it starts.
  acceptFault = false;
  fail = false;
  const acceptBase = acceptances.length;
  const mapBase = maps.length;
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
  await act(async () => {
    fireEvent(document, new Event("visibilitychange"));
    await ports.runtime.whenIdle();
  });
  await waitFor(() => expect(work.attempted).toBeNull());
  await waitForFreshAcceptedMap(acceptances, maps, work, acceptBase, mapBase);
  await act(async () => {
    await settleReaderRefreshes(listReads);
  });
  const keepReference =
    'A note on `NOTES.md:1:19-1:22` (captured source positions), selecting “same”: "Keep this scope."\n' +
    'Captured whole-file revision: "rev-A".\n' +
    'Context before: "First occurrence: "; after: "".\n' +
    "Verify the quotation and context against current content before relying on these captured coordinates.";
  await waitFor(async () =>
    expect(
      (await f.repo.listAnnotations(f.document)).find((v) => v.receipt.operationId === original.operationId)?.handoff
        .state,
    ).toBe("already-applied"),
  );
  await act(async () => {
    await ports.runtime.whenIdle();
    await settleReaderRefreshes(listReads);
  });
  // The same immutable operation applied exactly once with its original lease,
  // and its literal reference appended once.
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
  expect(draftAfter.text.split(keepReference)).toHaveLength(2); // exactly once
  expect(draftAfter).toEqual({
    ...baseline,
    text: `${baseline.text}\n\n${keepReference}`,
    token: { ...baseline.token, sessionRevision: baseline.token.sessionRevision + 1 },
  });
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
