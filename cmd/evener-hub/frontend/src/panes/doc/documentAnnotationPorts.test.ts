// Focused production-port tests for the browser document-annotation reader
// (Task 5). These exercise the REAL `getDocumentAnnotationPorts()` factory, the
// shared browser-draft module singleton, the real annotation repository/runtime
// and real IndexedDB storage — not the injected-port fixtures the reader-state
// tests use. The existing injected-port tests do NOT establish this production
// wiring (one lazy singleton, the real namespace/targetFor boundary and a live
// retained composer source), which is what this file proves.
//
// All persistence runs through the real modules over a fresh fake IndexedDB
// factory installed at file scope. The production ports singleton captures the
// shared `getBrowserDraftRepository()` singleton the first time it is resolved,
// so the storage universe is installed and the draft singleton reset ONCE in
// `beforeAll` — never replaced afterwards while the ports still capture it.
// Cleanup handles are registered immediately and the shared fixtures' disposers
// join owned async work before restoring backing state.
import type { AppwireClientLike, DestinationProvenance, DocumentKey } from "@evener/appwire-client";
import * as docContentModule from "@evener/appwire-client/docContent";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { render, screen } from "@testing-library/react";
import { IDBFactory } from "fake-indexeddb";
import { type ComponentType, createElement, lazy } from "react";
import { afterAll, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { StubResizeObserver } from "../../resizeObserverTestUtils";
import { ClientProvider } from "../../shell/clientContext";
import { conversationPaneLifetime } from "../../shell/paneLifetime";
import { registerPaneForTests } from "../../shell/paneRegistry";
import { type OpenPaneRecord, resetWorkspaceStoreForTests, workspaceStore } from "../../shell/workspace";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import {
  browserDraftKey,
  getBrowserDraftRepository,
  resetBrowserDraftRepositoryForTests,
} from "../../stores/browserDraftRepository";
import { connectionStore } from "../../stores/connection";
import DocPane from "./DocPane";
import {
  disposeDocumentReaderWork,
  documentReaderWork,
  getDocumentAnnotationPorts,
  subscribeDocumentReaderWork,
} from "./documentReadState";
import { REFERENCE, SOURCE } from "./testing/annotationFixtures";
import { acceptedOperation, cleanupAfterTest, docContent } from "./testing/annotationRepositoryFixtures";

let originalIndexedDB: IDBFactory;

beforeAll(() => {
  originalIndexedDB = globalThis.indexedDB;
  globalThis.ResizeObserver = StubResizeObserver;
  installLocalStorage(new MemoryStorage());
  // The one fresh IndexedDB universe the production ports singleton will capture,
  // installed before its first resolution and never replaced afterwards.
  globalThis.indexedDB = new IDBFactory();
  resetBrowserDraftRepositoryForTests();
  // The real `session` pane type openPane() validates against (the heavier
  // panes/session module is never needed for the lifetime assertions).
  registerPaneForTests({ id: "session", title: () => "Session", component: lazy(() => new Promise(() => {})) });
});

// The one production ports singleton is shared across every test in this file,
// so it is retired ONCE here — never closed between tests — after its runtime
// work is joined, and the global IndexedDB universe is restored only after that
// disposal.
afterAll(async () => {
  const ports = getDocumentAnnotationPorts();
  await ports.runtime.whenIdle();
  ports.runtime.dispose();
  ports.repository.close();
  resetBrowserDraftRepositoryForTests();
  globalThis.indexedDB = originalIndexedDB;
});

beforeEach(() => {
  // Registered before each test's own disposers, so the shared fixture
  // afterEach's reverse-ordered run executes it LAST: the workspace is reset
  // only after that test's host/source disposal.
  cleanupAfterTest(resetWorkspaceStoreForTests);
});

function openSessionPane(ref: string): OpenPaneRecord {
  const id = workspaceStore.getState().openPane("session", { ref });
  const pane = workspaceStore.getState().panes.find((record) => record.id === id);
  if (!pane) throw new Error("session pane did not open");
  return pane;
}

test("the production factory is one lazy singleton its reader hosts share across remount", async () => {
  const ports = getDocumentAnnotationPorts();
  // Repeated access resolves the SAME live ports object.
  expect(getDocumentAnnotationPorts()).toBe(ports);

  const key: DocumentKey = {
    namespace: { kind: "browser", origin: location.origin },
    sessionRef: "host-session",
    path: "HOST.md",
  };
  // A reader host with no injected ports binds the production singleton.
  const work = documentReaderWork("host-viewer", key);
  cleanupAfterTest(() => disposeDocumentReaderWork("host-viewer"));
  expect(work.ports).toBe(ports);

  // The real repository wakes an exact-key subscription on a committed save.
  const wakes = vi.fn();
  const off = ports.repository.subscribe(key, wakes);
  cleanupAfterTest(off);
  await ports.repository.saveAnnotation(
    await acceptedOperation(ports.repository, key, "host-op", "Host note.", { kind: "browser-restored-session" }),
  );
  expect(wakes).toHaveBeenCalledTimes(1);

  // A host unmount/subscribe cycle is not a disposal: the same retained work and
  // the same live ports survive.
  const unsubscribe = subscribeDocumentReaderWork("host-viewer", () => {});
  unsubscribe();
  expect(documentReaderWork("host-viewer", key)).toBe(work);
  expect(documentReaderWork("host-viewer", key).ports).toBe(ports);
});

test("the production factory appends one exact draft reference through a live retained composer source", async () => {
  const pane = openSessionPane("root");
  const source = conversationPaneLifetime(pane).composer;
  if (!source) throw new Error("session lifetime lacks a composer source");
  cleanupAfterTest(async () => {
    await source.whenSettled();
    source.dispose();
  });
  await source.ready();

  const ports = getDocumentAnnotationPorts();
  const key: DocumentKey = { ...browserDraftKey("root", location.origin), path: "NOTES.md" };
  const destination: DestinationProvenance = {
    kind: "browser-origin",
    paneId: pane.id,
    compositionOwnerId: source.sourceId,
  };
  const operation = await acceptedOperation(ports.repository, key, "ports-op", "Keep this scope.", destination);
  const receipt = await ports.repository.saveAnnotation(operation);
  expect(receipt.operationId).toBe("ports-op");

  // The production targetFor resolves the live retained source from the real
  // pane lifetime and namespace, so the handoff appends for real.
  const handoff = await ports.repository.handoff(key, "ports-op");
  expect(handoff).toMatchObject({ state: "applied" });

  // Exactly one literal reference over the seeded (empty) baseline draft.
  const appended = await getBrowserDraftRepository().load(browserDraftKey("root", location.origin));
  expect(appended?.text).toBe(REFERENCE);
  expect(appended?.token.sessionRevision).toBe(2);
  // The durable listing reports the committed row as already-applied (a single
  // receipt), while the live handoff above returned the append it performed.
  expect(
    (await ports.repository.listAnnotations(key))
      .filter((view) => view.receipt.operationId === "ports-op")
      .map((view) => view.handoff),
  ).toEqual([{ state: "already-applied", draftRevision: appended?.token.sessionRevision }]);
});

test("a foreign browser origin keeps the production handoff pending and preserves the live durable draft", async () => {
  const pane = openSessionPane("foreign-session");
  const source = conversationPaneLifetime(pane).composer;
  if (!source) throw new Error("session lifetime lacks a composer source");
  cleanupAfterTest(async () => {
    await source.whenSettled();
    source.dispose();
  });
  await source.ready();
  source.editor.write("live draft", 10);
  await source.whenSettled();
  // Complete pre-state captured before the foreign save: the full live source
  // snapshot and the full durable draft row, not only the text.
  const beforeSnapshot = source.getSnapshot();
  const draftKey = browserDraftKey("foreign-session", location.origin);
  const beforeDraft = await getBrowserDraftRepository().load(draftKey);

  const ports = getDocumentAnnotationPorts();
  // A document whose browser namespace is a FOREIGN origin: the production
  // namespace boundary refuses it even though the source is otherwise eligible.
  const foreignKey: DocumentKey = {
    namespace: { kind: "browser", origin: "https://foreign.example" },
    sessionRef: "foreign-session",
    path: "FOREIGN.md",
  };
  const destination: DestinationProvenance = {
    kind: "browser-origin",
    paneId: pane.id,
    compositionOwnerId: source.sourceId,
  };
  await ports.repository.saveAnnotation(
    await acceptedOperation(ports.repository, foreignKey, "foreign-op", "Foreign note.", destination),
  );

  const handoff = await ports.repository.handoff(foreignKey, "foreign-op");
  expect(handoff).toEqual({ state: "pending", reason: "owner-unavailable" });
  expect(
    (await ports.repository.listAnnotations(foreignKey)).find((view) => view.receipt.operationId === "foreign-op")
      ?.handoff,
  ).toEqual({ state: "pending", reason: "owner-unavailable" });

  // The FULL live snapshot and the FULL durable draft row are preserved: no
  // append happened and no field changed.
  expect(source.getSnapshot()).toEqual(beforeSnapshot);
  expect(await getBrowserDraftRepository().load(draftKey)).toEqual(beforeDraft);
});

test("an ordinary document host unmount does not dispose the production ports", async () => {
  const ports = getDocumentAnnotationPorts();
  const readSpy = vi.spyOn(docContentModule, "readDocFile").mockResolvedValue(docContent(SOURCE, "rev-A"));
  const client = new FakeClient("ready");
  const previousConnection = connectionStore.getState();
  cleanupAfterTest(() => {
    readSpy.mockRestore();
    connectionStore.setState(previousConnection);
    client.close();
  });
  client.on("evener/command/list", () => ({ commands: [] }));
  connectionStore.getState().connect(client);

  // JSX is unavailable in a .test.ts file, so the host tree is built with
  // createElement directly. ClientProvider declares children as a required prop,
  // which createElement's rest-children overload does not satisfy, so the
  // component is cast to the props subset that overload accepts.
  const Provider = ClientProvider as ComponentType<{ client: AppwireClientLike }>;
  const host = render(
    createElement(
      Provider,
      { client },
      createElement(DocPane, {
        paneId: "host-doc",
        focused: true,
        params: { session: "host-doc-session", path: "HOST.md", kind: "file" },
        annotations: ports,
      }),
    ),
  );
  // Registered immediately after render and BEFORE the first await, so an early
  // failure still unmounts the host before the raw-file/client restoration.
  let mounted = true;
  cleanupAfterTest(() => disposeDocumentReaderWork("host-doc"));
  cleanupAfterTest(() => {
    if (mounted) host.unmount();
  });
  await screen.findByText("First occurrence: same");
  host.unmount();
  mounted = false;

  // A document host unmount is a distinct operation from app teardown: the
  // production ports remain live for the rest of the app.
  expect(getDocumentAnnotationPorts()).toBe(ports);
});
