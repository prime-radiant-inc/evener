import type { DocumentKey } from "@evener/appwire-client";
import type { DockviewApi } from "dockview-core";
import { IDBFactory } from "fake-indexeddb";
import { lazy } from "react";
import { afterEach, beforeAll, beforeEach, expect, test } from "vitest";
import { acceptedOperation } from "../panes/doc/testing/annotationRepositoryFixtures";
import type { ComposerSourceState } from "../panes/session/composer/sourceState";
import { installLocalStorage, MemoryStorage } from "../storageTestUtils";
import {
  browserDraftKey,
  getBrowserDraftRepository,
  resetBrowserDraftRepositoryForTests,
} from "../stores/browserDraftRepository";
import { createDocumentAnnotationRepository } from "../stores/documentAnnotationIndexedDB";
import * as paneLifetimeModule from "./paneLifetime";
import { conversationPaneLifetime, disposeConversationPaneLifetime } from "./paneLifetime";
import { registerPaneForTests } from "./paneRegistry";
import { type OpenPaneRecord, registerDockviewApi, resetWorkspaceStoreForTests, workspaceStore } from "./workspace";

beforeAll(() => {
  registerPaneForTests({
    id: "session",
    title: () => "Session",
    component: lazy(() => new Promise(() => {})),
  });
  // retypePane (the real Return/retype path) validates the target type.
  registerPaneForTests({
    id: "sessionZoom",
    title: () => "Cascade",
    component: lazy(() => new Promise(() => {})),
  });
});
beforeEach(() => {
  installLocalStorage(new MemoryStorage());
  resetWorkspaceStoreForTests();
});
afterEach(() => resetWorkspaceStoreForTests());

function rootPane(): OpenPaneRecord {
  return { id: "session-1", type: "session", params: { ref: "root" }, slot: "main" };
}

// Runtime source identity belongs to the lifetime, not the record or dedup params.
test("new committed session records get distinct runtime sources without storing sourceId in dedup params", () => {
  const id = workspaceStore.getState().openPane("session", { ref: "root" });
  const original = workspaceStore.getState().panes.find((pane) => pane.id === id);
  if (!original) throw new Error("session did not open");
  expect(original.params).toEqual({ ref: "root" });
  expect(original).not.toHaveProperty("sourceId");
  const originalLifetime = conversationPaneLifetime(original);
  const originalSource = originalLifetime.composer;
  expect(originalSource?.sourceId).toEqual(expect.any(String));
  workspaceStore.getState().closePane(id);
  const replacementId = workspaceStore.getState().openPane("session", { ref: "root" });
  const replacement = workspaceStore.getState().panes.find((pane) => pane.id === replacementId);
  if (!replacement) throw new Error("replacement session did not open");
  const replacementLifetime = conversationPaneLifetime(replacement);
  const replacementSource = replacementLifetime.composer;
  expect(replacementSource?.sourceId).toEqual(expect.any(String));
  expect(replacementSource?.sourceId).not.toBe(originalSource?.sourceId);
  expect(replacementLifetime.serial).not.toBe(originalLifetime.serial);
  expect(originalLifetime.alive).toBe(false);
  expect(replacement.params).toEqual({ ref: "root" });
  expect(replacement).not.toHaveProperty("sourceId");
  expect(workspaceStore.getState().openPane("session", { ref: "root" })).toBe(replacementId);
  expect(workspaceStore.getState().panes.find((pane) => pane.id === replacementId)).toBe(replacement);
  expect(conversationPaneLifetime(replacement)).toBe(replacementLifetime);
  expect(conversationPaneLifetime(replacement).composer).toBe(replacementSource);
});

test("the same committed pane retains its source owner across focus changes", () => {
  const pane = rootPane();
  workspaceStore.setState({ panes: [pane], focusedPaneId: pane.id });
  const lifetime = conversationPaneLifetime(pane);
  const composer = lifetime.composer;
  composer?.editor.write("keep this draft", 15);
  workspaceStore.setState({ focusedPaneId: null });
  expect(conversationPaneLifetime(pane)).toBe(lifetime);
  expect(conversationPaneLifetime(pane).composer).toBe(composer);
  expect(composer?.getSnapshot().text).toBe("keep this draft");
  expect(lifetime.alive).toBe(true);
});

test("close followed by ID reuse invalidates the old source generation", () => {
  const oldRootPane = rootPane();
  const old = conversationPaneLifetime(oldRootPane);
  const composer = old.composer;
  if (!composer) throw new Error("session lifetime lacks its source");
  const generation = composer.attachments.getState().generationRef.current;
  disposeConversationPaneLifetime(oldRootPane);
  const replacement = conversationPaneLifetime(rootPane());
  expect(replacement.serial).not.toBe(old.serial);
  expect(old.alive).toBe(false);
  expect(replacement.composer?.attachments.getState().items).toEqual([]);
  expect(composer.attachments.getState().generationRef.current).toBeGreaterThan(generation);
  expect(conversationPaneLifetime(oldRootPane).alive).toBe(false);
  replacement.dispose();
});

test.each(["close", "replacePrimary", "reset", "same-ID replacement"] as const)(
  "%s ends only removed pane lifetimes",
  (action) => {
    const pane = rootPane();
    const other: OpenPaneRecord = {
      id: "transcript-2",
      type: "transcript",
      params: { ref: "child" },
      slot: "secondary",
    };
    workspaceStore.setState({ panes: [pane, other], focusedPaneId: pane.id });
    const lifetime = conversationPaneLifetime(pane);
    const otherLifetime = conversationPaneLifetime(other);
    void lifetime.composer;
    expect(otherLifetime.composer).toBeNull();
    if (action === "close") workspaceStore.getState().closePane(pane.id);
    else if (action === "replacePrimary") workspaceStore.getState().replacePrimary("session", { ref: "replacement" });
    else if (action === "reset") resetWorkspaceStoreForTests();
    else workspaceStore.setState({ panes: [rootPane(), other] });
    expect(lifetime.alive).toBe(false);
    expect(otherLifetime.alive).toBe(action === "close" || action === "same-ID replacement");
  },
);

test.each([false, true])("layout restore success %s disposes only committed replacements", (success) => {
  const pane = rootPane();
  workspaceStore.setState({ panes: [pane], focusedPaneId: pane.id });
  const lifetime = conversationPaneLifetime(pane);
  const composer = lifetime.composer;
  composer?.editor.write("keep this draft", 15);
  // Dockview is the external layout boundary, workspace and source owners run unchanged.
  const api = {
    panels: [{ id: pane.id, params: { paneType: "session", paneParams: { ref: "root" } } }],
    activePanel: { id: pane.id },
    fromJSON() {
      if (!success) throw new Error("dockview: invalid layout");
    },
    clear() {},
  };
  registerDockviewApi(api as unknown as DockviewApi);
  expect(workspaceStore.getState().restoreLayout({})).toBe(success);
  expect(lifetime.alive).toBe(!success);
  const restored = workspaceStore.getState().panes[0];
  expect(restored).toBeDefined();
  if (!restored) throw new Error("layout restore discarded the source pane");
  expect(workspaceStore.getState().focusedPaneId).toBe("session-1");
  if (success) {
    expect(restored).not.toBe(pane);
    expect(conversationPaneLifetime(restored).serial).not.toBe(lifetime.serial);
  } else {
    expect(restored).toBe(pane);
    expect(conversationPaneLifetime(restored).composer).toBe(composer);
    expect(composer?.getSnapshot().text).toBe("keep this draft");
  }
});

// --- retainedComposerSourceFor: the destination resolver (Task 4). It reuses
// the EXISTING WeakMap/workspace record and must never create a lifetime or
// authorize work from a stale serialized identity. Loaded nominally so every
// lifetime case above keeps collecting and executing. -----------------------
function retainedComposerSourceFor() {
  const candidate = (paneLifetimeModule as Record<string, unknown>).retainedComposerSourceFor;
  if (typeof candidate !== "function") throw new Error("retainedComposerSourceFor is not exported yet");
  return candidate as (paneId: string, sourceId: string, sessionRef: string) => unknown;
}

test("retainedComposerSourceFor returns the exact live composer source for a matching pane/source/session", () => {
  const pane = rootPane();
  workspaceStore.setState({ panes: [pane], focusedPaneId: pane.id });
  const lifetime = conversationPaneLifetime(pane);
  const composer = lifetime.composer;
  if (!composer) throw new Error("session lifetime lacks its composer source");
  expect(retainedComposerSourceFor()(pane.id, composer.sourceId, "root")).toBe(composer);
});

test("retainedComposerSourceFor refuses a wrong source id or a wrong displayed session ref", () => {
  const pane = rootPane();
  workspaceStore.setState({ panes: [pane], focusedPaneId: pane.id });
  const lifetime = conversationPaneLifetime(pane);
  const composer = lifetime.composer;
  if (!composer) throw new Error("session lifetime lacks its composer source");
  expect(retainedComposerSourceFor()(pane.id, "some-other-source", "root")).toBeNull();
  expect(retainedComposerSourceFor()(pane.id, composer.sourceId, "child")).toBeNull();
});

test("retainedComposerSourceFor never creates a lifetime: a pane with no session ref still resolves to null", () => {
  // params name no session ref, so conversationPaneLifetime(pane) would throw.
  // A null here proves the resolver reads only the existing map/record.
  const pane: OpenPaneRecord = { id: "session-9", type: "session", params: {}, slot: "main" };
  workspaceStore.setState({ panes: [pane], focusedPaneId: pane.id });
  expect(retainedComposerSourceFor()("session-9", "any-source", "root")).toBeNull();
  expect(retainedComposerSourceFor()("no-such-pane", "any-source", "root")).toBeNull();
});

test("retainedComposerSourceFor refuses a disposed lifetime", () => {
  const pane = rootPane();
  workspaceStore.setState({ panes: [pane], focusedPaneId: pane.id });
  const lifetime = conversationPaneLifetime(pane);
  const composer = lifetime.composer;
  if (!composer) throw new Error("session lifetime lacks its composer source");
  disposeConversationPaneLifetime(pane);
  expect(retainedComposerSourceFor()(pane.id, composer.sourceId, "root")).toBeNull();
});

test("retainedComposerSourceFor keeps resolving through a same-pane focus change", () => {
  const pane = rootPane();
  workspaceStore.setState({ panes: [pane], focusedPaneId: pane.id });
  const lifetime = conversationPaneLifetime(pane);
  const composer = lifetime.composer;
  if (!composer) throw new Error("session lifetime lacks its composer source");
  // Return/focus change is the same lifetime: an old serialized identity is not
  // authorized by focus, but the retained source stays resolvable.
  workspaceStore.setState({ focusedPaneId: null });
  expect(retainedComposerSourceFor()(pane.id, composer.sourceId, "root")).toBe(composer);
});

test("a reused pane id resolves to the replacement source, and the old source id no longer resolves", () => {
  const pane = rootPane();
  workspaceStore.setState({ panes: [pane], focusedPaneId: pane.id });
  const lifetime = conversationPaneLifetime(pane);
  const composer = lifetime.composer;
  if (!composer) throw new Error("session lifetime lacks its composer source");
  // Close/reset/reused ID: a fresh record with the same id is a different
  // lifetime. Old serialized identity must not resolve against the replacement.
  const replacement: OpenPaneRecord = { id: pane.id, type: "session", params: { ref: "root" }, slot: "main" };
  workspaceStore.setState({ panes: [replacement], focusedPaneId: replacement.id });
  const replacementLifetime = conversationPaneLifetime(replacement);
  const replacementComposer = replacementLifetime.composer;
  if (!replacementComposer) throw new Error("replacement lifetime lacks its composer source");
  expect(replacementLifetime).not.toBe(lifetime);
  expect(replacementComposer.sourceId).not.toBe(composer.sourceId);
  expect(retainedComposerSourceFor()(pane.id, composer.sourceId, "root")).toBeNull();
  expect(retainedComposerSourceFor()(pane.id, replacementComposer.sourceId, "root")).toBe(replacementComposer);
});

test("retainedComposerSourceFor returns null for a normal valid pane that has no retained lifetime yet", () => {
  // A valid record whose lifetime was never created: the resolver must read the
  // existing map only and never create one.
  const pane = rootPane();
  workspaceStore.setState({ panes: [pane], focusedPaneId: pane.id });
  expect(retainedComposerSourceFor()(pane.id, "any-source", "root")).toBeNull();
});

test("retainedComposerSourceFor keeps resolving the same composer across a real retype", () => {
  const pane = rootPane();
  workspaceStore.setState({ panes: [pane], focusedPaneId: pane.id });
  const lifetime = conversationPaneLifetime(pane);
  const composer = lifetime.composer;
  if (!composer) throw new Error("session lifetime lacks its composer source");
  // A real retype (Return/cascade promotion) transfers the SAME lifetime to a
  // replacement record; the resolver must follow the committed record identity.
  const retyped = workspaceStore
    .getState()
    .retypePane(pane, "sessionZoom", { ref: "child", source: { type: "session", params: { ref: "root" } }, edges: [] });
  expect(retyped).toBe(true);
  const replacement = workspaceStore.getState().panes.find((record) => record.id === pane.id);
  if (!replacement) throw new Error("retype dropped the pane");
  expect(replacement).not.toBe(pane);
  expect(conversationPaneLifetime(replacement)).toBe(lifetime);
  expect(retainedComposerSourceFor()(replacement.id, composer.sourceId, "root")).toBe(composer);
});

test("retainedComposerSourceFor returns null after a workspace reset", () => {
  const pane = rootPane();
  workspaceStore.setState({ panes: [pane], focusedPaneId: pane.id });
  const lifetime = conversationPaneLifetime(pane);
  const composer = lifetime.composer;
  if (!composer) throw new Error("session lifetime lacks its composer source");
  resetWorkspaceStoreForTests();
  expect(retainedComposerSourceFor()(pane.id, composer.sourceId, "root")).toBeNull();
});

// Ordinary Session dedupe is unchanged by doc provenance: an identical session
// ref focuses the existing record; a different ref opens a second one.
test("ordinary Session openings keep their existing dedupe", () => {
  const first = workspaceStore.getState().openPane("session", { ref: "a" });
  expect(workspaceStore.getState().openPane("session", { ref: "a" })).toBe(first);
  const second = workspaceStore.getState().openPane("session", { ref: "b" });
  expect(second).not.toBe(first);
  expect(workspaceStore.getState().panes.filter((pane) => pane.type === "session")).toHaveLength(2);
});

// --- Pending note follows only its original retained source (Task 4). Real
// Task3 storage: a real pane-created composer source, a real accepted lease and
// save, then reconciliation whose destination resolver is the real
// retainedComposerSourceFor. The source-independent prerequisites execute and
// leave the durable row genuinely pending before reconciliation. ---------------
test("a saved note stays pending until only its original retained source reconciles it", async () => {
  const originalIndexedDB = globalThis.indexedDB;
  globalThis.indexedDB = new IDBFactory();
  resetBrowserDraftRepositoryForTests();
  try {
    const pane = rootPane();
    workspaceStore.setState({ panes: [pane], focusedPaneId: pane.id });
    const lifetime = conversationPaneLifetime(pane);
    const source = lifetime.composer;
    if (!source) throw new Error("session lifetime lacks its composer source");
    await source.ready();
    const drafts = getBrowserDraftRepository();
    const document: DocumentKey = { ...browserDraftKey("root", location.origin), path: "NOTES.md" };
    const destination = { kind: "browser-origin" as const, paneId: pane.id, compositionOwnerId: source.sourceId };
    const repo = createDocumentAnnotationRepository({
      drafts,
      targetFor: (target, key) => {
        if (target.kind !== "browser-origin") return null;
        const resolve = retainedComposerSourceFor();
        return resolve(target.paneId, target.compositionOwnerId, key.sessionRef) as ComposerSourceState | null;
      },
    });
    try {
      const operation = await acceptedOperation(repo, document, "op-1", "Keep this scope.", destination);
      const receipt = await repo.saveAnnotation(operation);
      // Source-independent: before any reconciliation the durable row is a
      // genuinely pending operation with the exact saved receipt.
      const views = await repo.listAnnotations(document);
      expect(views).toHaveLength(1);
      expect(views[0]?.receipt).toEqual(receipt);
      expect(views[0]?.handoff).toEqual({ state: "pending", reason: "owner-unavailable" });
      const draftKey = browserDraftKey("root");
      const beforeBacking = await drafts.load(draftKey);
      // Distinct real other/child/focused sources with meaningful values: none
      // of them may receive this note.
      const childPane: OpenPaneRecord = {
        id: "child-pane",
        type: "session",
        params: { ref: "child" },
        slot: "secondary",
      };
      const focusedPane: OpenPaneRecord = {
        id: "focused-pane",
        type: "session",
        params: { ref: "focused" },
        slot: "secondary",
      };
      workspaceStore.setState({ panes: [pane, childPane, focusedPane], focusedPaneId: focusedPane.id });
      const childSource = conversationPaneLifetime(childPane).composer;
      const focusedSource = conversationPaneLifetime(focusedPane).composer;
      if (!childSource || !focusedSource) throw new Error("other sources missing");
      await Promise.all([childSource.ready(), focusedSource.ready()]);
      childSource.editor.write("child draft", 11);
      focusedSource.editor.write("focused draft", 13);
      await Promise.all([childSource.whenSettled(), focusedSource.whenSettled()]);
      // Actual Return/retype: the source identity follows the retyped record.
      expect(
        workspaceStore.getState().retypePane(pane, "sessionZoom", {
          ref: "child",
          source: { type: "session", params: { ref: "root" } },
          edges: [],
        }),
      ).toBe(true);
      const retyped = workspaceStore.getState().panes.find((record) => record.id === pane.id);
      if (!retyped) throw new Error("retype dropped the source pane");
      // Reconciliation resolves the ORIGINAL destination through the real
      // retained-source resolver (on the retained, retyped record).
      const applied = await repo.handoff(document, "op-1");
      expect(workspaceStore.getState().panes.includes(retyped)).toBe(true);
      expect(applied).toMatchObject({ state: "applied" });
      // Exactly one append to the ORIGINAL source's durable backing.
      const afterApplied = await drafts.load(draftKey);
      expect(afterApplied?.token.sessionRevision).toBe((beforeBacking?.token.sessionRevision ?? 0) + 1);
      expect(afterApplied?.text).toContain("Keep this scope.");
      // Every other source's durable value is unchanged.
      expect((await drafts.load(browserDraftKey("child")))?.text).toBe("child draft");
      expect((await drafts.load(browserDraftKey("focused")))?.text).toBe("focused draft");
      // Replay is `already-applied` (the production contract), never another
      // append; a fresh backing read after replay proves no re-append.
      const replayed = await repo.handoff(document, "op-1");
      expect(replayed).toEqual({
        state: "already-applied",
        draftRevision: (applied as { draftRevision: number }).draftRevision,
      });
      const afterReplay = await drafts.load(draftKey);
      expect(afterReplay).toEqual(afterApplied);
    } finally {
      await source.whenSettled();
      source.dispose();
      repo.close();
      drafts.close();
    }
  } finally {
    resetBrowserDraftRepositoryForTests();
    globalThis.indexedDB = originalIndexedDB;
  }
});
