// Spec for the doc-opening provenance provider/helper, plus the real Session and
// Transcript producer sites. Every identity here is real: panes are real
// workspace records, lifetimes come from the real conversationPaneLifetime
// authority, composer source ids are the real runtime ids, and the Session and
// Transcript cases mount the REAL committed-pane wrappers. Nothing fabricates a
// lifetime object or supplies a DocOpeningProvider from the test.
//
// `docOpeningContext.tsx` is loaded at call time (not by a static import) so a
// missing module fails only the cases that call it, while the producer cases
// below still collect and execute. See the loader's own comment.

import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { IDBFactory } from "fake-indexeddb";
import { lazy, type ReactNode } from "react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { ClientProvider } from "../../shell/clientContext";
import { conversationPaneLifetime, disposeConversationPaneLifetime, type PaneLifetime } from "../../shell/paneLifetime";
import { registerPaneForTests } from "../../shell/paneRegistry";
import * as StatusBarModule from "../../shell/statusbar/StatusBar";
import { type OpenPaneRecord, resetWorkspaceStoreForTests, workspaceStore } from "../../shell/workspace";
import { resetBrowserDraftRepositoryForTests } from "../../stores/browserDraftRepository";
import { connectionStore } from "../../stores/connection";
import { activitySummary, activityThread } from "../../stores/sessionActivityTestUtils";
import { resetThreadsStoreForTests } from "../../stores/threads";
import * as SessionChromeModule from "../session/chrome/SessionChrome";
import * as ComposerModule from "../session/composer/Composer";
import type { ComposerSourceState } from "../session/composer/sourceState";
import { CommittedSession } from "../session/testing/CommittedSession";
import { default as CommittedTranscript } from "../transcript/testing/CommittedTranscript";
import "./index"; // registers the real "doc" pane so openBeside resolves a doc open

vi.spyOn(ComposerModule, "Composer").mockImplementation(() => <div data-testid="composer-slot" />);
vi.spyOn(SessionChromeModule, "SessionChrome").mockImplementation(() => <></>);
vi.spyOn(StatusBarModule, "StatusBar").mockImplementation(() => <></>);

// Vite resolves a LITERAL `import()` at transform time, so a literal here would
// fail this whole file's collection while the module is missing. Loading it at
// call time keeps the producer cases below collectable and executable; a missing
// module then fails only the cases that actually call it.
interface DocOpeningContextModule {
  DocOpeningProvider: (props: { origin?: unknown; children: ReactNode }) => ReactNode;
  useDocOpeningOrigin: () => unknown;
  docOpeningOriginFor: (lifetime: PaneLifetime, displayedSessionRef: string) => unknown;
}
async function loadDocOpeningContext(): Promise<DocOpeningContextModule> {
  // A non-literal specifier: Vite cannot resolve it during transform, so a
  // missing module does not fail this file's collection.
  const modulePath = "./docOpeningContext";
  return (await import(/* @vite-ignore */ modulePath)) as unknown as DocOpeningContextModule;
}

const originalIndexedDB = globalThis.indexedDB;
let height: PropertyDescriptor | undefined;

// Every committed conversation pane this file mounts, with the EXACT live
// composer source captured at mount time. Teardown joins these while the owned
// IndexedDB backing is still installed, then disposes them - never looking a
// pane up after the workspace reset, and never creating a lifetime at teardown.
interface OwnedCommittedPane {
  pane: OpenPaneRecord;
  source: ComposerSourceState | null;
}
const ownedCommittedPanes: OwnedCommittedPane[] = [];

beforeEach(() => {
  globalThis.indexedDB = new IDBFactory();
  resetBrowserDraftRepositoryForTests();
  resetThreadsStoreForTests();
  resetWorkspaceStoreForTests();
  connectionStore.setState({ state: "idle", client: null });
  height = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", { configurable: true, value: 500 });
});
afterEach(async () => {
  // Unmount the host first, then join and dispose the owned sources while the
  // owned backing is still installed. Only afterwards reset repositories and
  // restore globals, so an unusual test end (a throw anywhere in the body)
  // still settles real work against the storage it was created with.
  cleanup();
  const failures: unknown[] = [];
  for (const { pane, source } of ownedCommittedPanes.splice(0).reverse()) {
    try {
      await source?.whenSettled();
      disposeConversationPaneLifetime(pane);
    } catch (error) {
      failures.push(error);
    }
  }
  resetWorkspaceStoreForTests();
  resetThreadsStoreForTests();
  resetBrowserDraftRepositoryForTests();
  connectionStore.setState({ state: "idle", client: null });
  globalThis.indexedDB = originalIndexedDB;
  if (height) Object.defineProperty(HTMLElement.prototype, "offsetHeight", height);
  else Reflect.deleteProperty(HTMLElement.prototype, "offsetHeight");
  vi.restoreAllMocks();
  // Re-install the module-scope component stubs this file owns.
  stubSessionComponents();
  if (failures.length > 0) throw new AggregateError(failures, "docOpeningContext cleanup failed");
});

function stubSessionComponents(): void {
  vi.spyOn(ComposerModule, "Composer").mockImplementation(() => <div data-testid="composer-slot" />);
  vi.spyOn(SessionChromeModule, "SessionChrome").mockImplementation(() => <></>);
  vi.spyOn(StatusBarModule, "StatusBar").mockImplementation(() => <></>);
}

// The registry must know "session"/"transcript" so paneFor never throws for an
// unregistered type; light stubs are enough here.
registerPaneForTests({
  id: "session",
  title: () => "Session",
  component: lazy(() => new Promise(() => {})),
});
registerPaneForTests({
  id: "transcript",
  title: () => "Transcript",
  component: lazy(() => new Promise(() => {})),
});

function committedPane(id: string, ref: string, type: "session" | "transcript" = "session"): OpenPaneRecord {
  const pane: OpenPaneRecord = { id, type, params: { ref }, slot: "main" };
  workspaceStore.setState({ panes: [...workspaceStore.getState().panes, pane], focusedPaneId: id });
  ownCommittedPane(id);
  return pane;
}

// Bind one committed record to its real lifetime and live composer source now,
// while the workspace record exists. Both the plain docOpeningOriginFor cases
// and the real producer mounts use this one small path.
function ownCommittedPane(paneId: string): void {
  const pane = workspaceStore.getState().panes.find((record) => record.id === paneId);
  if (!pane) throw new Error(`committed pane ${paneId} was not registered`);
  ownedCommittedPanes.push({ pane, source: conversationPaneLifetime(pane).composer });
}

// --- docOpeningOriginFor / DocOpeningProvider / useDocOpeningOrigin ----------

test("docOpeningOriginFor returns browser-origin carrying the retained composer source id for its own displayed ref", async () => {
  const { docOpeningOriginFor } = await loadDocOpeningContext();
  const pane = committedPane("session-1", "root");
  const lifetime = conversationPaneLifetime(pane);
  const composer = lifetime.composer;
  if (!composer) throw new Error("session lifetime lacks its composer source");
  expect(docOpeningOriginFor(lifetime, "root")).toEqual({
    kind: "browser-origin",
    paneId: "session-1",
    compositionOwnerId: composer.sourceId,
  });
});

test("docOpeningOriginFor yields explicit browser-readonly-session for a displayed ref the session lifetime does not own", async () => {
  const { docOpeningOriginFor } = await loadDocOpeningContext();
  const pane = committedPane("session-1", "root");
  const lifetime = conversationPaneLifetime(pane);
  // The child's displayed ref is not this pane's source ref: it must never be
  // granted the source pane's origin.
  expect(docOpeningOriginFor(lifetime, "child")).toEqual({
    kind: "browser-readonly-session",
    sourcePaneId: "session-1",
  });
});

test("docOpeningOriginFor yields browser-readonly-session for a transcript lifetime, which has no composer source", async () => {
  const { docOpeningOriginFor } = await loadDocOpeningContext();
  const pane = committedPane("transcript-1", "child", "transcript");
  const lifetime = conversationPaneLifetime(pane);
  expect(lifetime.sourceType).toBe("transcript");
  expect(lifetime.composer).toBeNull();
  expect(docOpeningOriginFor(lifetime, "child")).toEqual({
    kind: "browser-readonly-session",
    sourcePaneId: "transcript-1",
  });
});

test("docOpeningOriginFor yields browser-readonly-session once the lifetime is disposed", async () => {
  const { docOpeningOriginFor } = await loadDocOpeningContext();
  const pane = committedPane("session-1", "root");
  const lifetime = conversationPaneLifetime(pane);
  lifetime.dispose();
  expect(lifetime.alive).toBe(false);
  expect(docOpeningOriginFor(lifetime, "root")).toEqual({
    kind: "browser-readonly-session",
    sourcePaneId: "session-1",
  });
});

test("DocOpeningProvider publishes the exact captured origin to consumers", async () => {
  const mod = await loadDocOpeningContext();
  const origin = { kind: "browser-origin" as const, paneId: "owner-pane", compositionOwnerId: "owner-A" };
  function OriginProbe() {
    const present = mod.useDocOpeningOrigin();
    return <output data-testid="origin">{present === undefined ? "absent" : JSON.stringify(present)}</output>;
  }
  render(
    <mod.DocOpeningProvider origin={origin}>
      <OriginProbe />
    </mod.DocOpeningProvider>,
  );
  expect(screen.getByTestId("origin").textContent).toBe(JSON.stringify(origin));
});

test("a consumer with no provider sees no origin, even while a real focused live source exists", async () => {
  const mod = await loadDocOpeningContext();
  // A real focused session pane whose retained composer source is live: an
  // erroneous "fall back to the focused owner" would resolve here and fail.
  const pane = committedPane("session-1", "root");
  const composer = conversationPaneLifetime(pane).composer;
  if (!composer) throw new Error("session lifetime lacks its composer source");
  workspaceStore.setState({ focusedPaneId: pane.id });
  function OriginProbe() {
    const present = mod.useDocOpeningOrigin();
    return <output data-testid="origin">{present === undefined ? "absent" : JSON.stringify(present)}</output>;
  }
  render(<OriginProbe />);
  expect(screen.getByTestId("origin").textContent).toBe("absent");
});

// --- Real Session / Transcript producer sites ------------------------------
// These mount the REAL committed-pane wrappers with a real FakeClient thread
// whose turn carries a file card and a prose link, click the actual controls,
// and inspect the real workspace doc records. The DocOpeningProvider wiring is
// the behavior under test: with it absent, every opening stays origin-less.

function openTargetsThread(ref: string) {
  const base = activityThread(ref);
  return {
    ...base,
    thread: {
      ...base.thread,
      name: ref,
      cwd: "/scratch",
      turns: [
        {
          id: `${ref}-turn`,
          status: "completed",
          itemsView: "full",
          items: [
            {
              id: `${ref}-card`,
              turnId: `${ref}-turn`,
              type: "commandExecution",
              toolName: "read_file",
              argumentsJson: JSON.stringify({ file_path: "/scratch/card.md" }),
              output: "card body",
              status: "completed",
            },
            {
              id: `${ref}-link`,
              turnId: `${ref}-turn`,
              type: "agentMessage",
              text: "See [notes](/scratch/link.md).",
              status: "completed",
            },
          ],
        },
      ],
    },
  };
}

function docPanes(): OpenPaneRecord[] {
  return workspaceStore.getState().panes.filter((pane) => pane.type === "doc");
}

async function mountSessionProducer(ref: string, paneId: string) {
  const { FakeClient } = await import("@evener/appwire-client/testing/fakeClient");
  const fake = new FakeClient("ready");
  fake.on("thread/read", () => openTargetsThread(ref) as never);
  fake.on("thread/unsubscribe", () => ({}));
  fake.on("evener/thread/activity/read", () => activitySummary(ref) as never);
  connectionStore.getState().connect(fake);
  render(
    <ClientProvider client={fake}>
      <CommittedSession params={{ ref }} paneId={paneId} focused />
    </ClientProvider>,
  );
  // Capture the mounted pane's retained source NOW (record still present); the
  // file afterEach joins and disposes it before the workspace reset and globals
  // restore, on success and failure alike.
  ownCommittedPane(paneId);
  return fake;
}

async function mountTranscriptProducer(ref: string, paneId: string) {
  const { FakeClient } = await import("@evener/appwire-client/testing/fakeClient");
  const fake = new FakeClient("ready");
  fake.on("thread/read", () => openTargetsThread(ref) as never);
  fake.on("thread/unsubscribe", () => ({}));
  fake.on("evener/thread/activity/read", () => activitySummary(ref) as never);
  connectionStore.getState().connect(fake);
  render(
    <ClientProvider client={fake}>
      <CommittedTranscript params={{ ref }} paneId={paneId} focused />
    </ClientProvider>,
  );
  ownCommittedPane(paneId);
  return fake;
}

test("a real Session file card and prose link carry the retained browser-origin of the session pane", async () => {
  await mountSessionProducer("root", "session-pane");
  const card = await screen.findByRole("button", { name: "Open beside: card.md" });
  fireEvent.click(card);
  const owner = conversationPaneLifetime(
    workspaceStore.getState().panes.find((pane) => pane.id === "session-pane") as OpenPaneRecord,
  ).composer;
  if (!owner) throw new Error("session pane lacks its composer source");
  const cardOpen = docPanes().find((pane) => (pane.params as { path?: string }).path === "card.md");
  expect(cardOpen?.params).toMatchObject({
    session: "root",
    path: "card.md",
    kind: "file",
    origin: { kind: "browser-origin", paneId: "session-pane", compositionOwnerId: owner.sourceId },
  });
  fireEvent.click(await screen.findByRole("button", { name: "Open beside: link.md" }));
  const linkOpen = docPanes().find((pane) => (pane.params as { path?: string }).path === "link.md");
  expect(linkOpen?.params).toMatchObject({
    session: "root",
    path: "link.md",
    kind: "file",
    origin: { kind: "browser-origin", paneId: "session-pane", compositionOwnerId: owner.sourceId },
  });
});

test("a real Transcript file card and prose link carry explicit browser-readonly-session", async () => {
  await mountTranscriptProducer("child", "transcript-pane");
  fireEvent.click(await screen.findByRole("button", { name: "Open beside: card.md" }));
  const cardOpen = docPanes().find((pane) => (pane.params as { path?: string }).path === "card.md");
  expect(cardOpen?.params).toMatchObject({
    session: "child",
    path: "card.md",
    kind: "file",
    origin: { kind: "browser-readonly-session", sourcePaneId: "transcript-pane" },
  });
  fireEvent.click(await screen.findByRole("button", { name: "Open beside: link.md" }));
  const linkOpen = docPanes().find((pane) => (pane.params as { path?: string }).path === "link.md");
  expect(linkOpen?.params).toMatchObject({
    session: "child",
    path: "link.md",
    kind: "file",
    origin: { kind: "browser-readonly-session", sourcePaneId: "transcript-pane" },
  });
});
