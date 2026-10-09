import { AppwireClient } from "@evener/appwire-client";
import type { SubmittedDraft } from "@evener/appwire-client/state/mutation";
import { act, renderHook, waitFor } from "@testing-library/react";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { conversationPaneLifetime } from "../../../shell/paneLifetime";
import { type OpenPaneRecord, resetWorkspaceStoreForTests, workspaceStore } from "../../../shell/workspace";
import { installLocalStorage, MemoryStorage } from "../../../storageTestUtils";
import {
  type BrowserDraftRepository,
  browserDraftKey,
  createBrowserDraftRepository,
  getBrowserDraftRepository,
  resetBrowserDraftRepositoryForTests,
} from "../../../stores/browserDraftRepository";
import { serializeSessionKey } from "../../../stores/compositionRevision";
import {
  ANNOTATION_NAMESPACES_STORE,
  COMPOSER_DRAFTS_STORE,
  LOCAL_COMPOSITION_DATABASE_VERSION,
} from "../../../stores/localCompositionSchema";
import { MutationOutboxIndexedDB } from "../../../stores/mutationOutboxIndexedDB";
import { holdNextWriteTransaction } from "../../../stores/testing/stalledIndexedDB";
import { resetThreadsStoreForTests, setMutationStorageForTests } from "../../../stores/threads";
import { getToasts, pushToast, resetToastStoreForTests } from "../../../widgets/toast/store";
import { createTestComposerSource } from "../testing/composerSource";
import { installControlledImageEncoding } from "../testing/imageEncoding";
import {
  createAttachmentOperations,
  type PendingAttachment,
  type TextEditor,
  useAttachments,
} from "./attachments/useAttachments";
import { composerDraftStorageKey, draftStorageKey, readComposerDraft, readDraftRevision } from "./draft";
import {
  resetPendingTurnsStoreForTests,
  submitWithPendingTracking,
  subscribeComposerSubmissionCommitted,
} from "./queue/pendingTurnsStore";
import type { SkillEditorValue } from "./skillDocument";
import { type ComposerSourceState, createComposerSourceState } from "./sourceState";

// File-local cleanup, independent of the shared testing helper: injected
// repository/outbox sources are not registered with createTestComposerSource.
// Every cleanup still runs and a failure is rethrown, never swallowed.
type SourceCleanup = () => void | Promise<void>;
const sourceCleanups: SourceCleanup[] = [];
afterEach(async () => {
  const failures: unknown[] = [];
  for (const close of sourceCleanups.splice(0).reverse()) {
    try {
      await close();
    } catch (error) {
      failures.push(error);
    }
  }
  if (failures.length > 0) throw new AggregateError(failures, "source state test cleanup failed");
});

// Superseded durable-ownership scenario, adapted to the approved no-owner
// contract (Step1). The old assertions - a single `ownership: "owned"` winner
// and one `compositionOwnerId` - are replaced by the no-owner facts: both
// same-session sources load the one imported value, a later deliberate full
// edit rebases on the current revision, and the first pane's pre-competition
// submission cannot clear it.
test("two same-session sources on separate adapters rebase a later edit over a captured stale settlement", async () => {
  const fixture = sourceFixture("source-state-race");
  const value: SkillEditorValue = {
    text: "🙂 /same /same /same",
    skillNames: ["same"],
    commandNames: ["same"],
    mentions: [
      { kind: "command", name: "same", offset: 3 },
      { kind: "skill", name: "same", offset: 9 },
    ],
  };
  await fixture.repository.seedDraft(fixture.key, value);
  const secondRepository = fixture.secondRepository();
  const first = fixture.track(
    createComposerSourceState("root", { repository: fixture.repository, key: fixture.key, outbox: fixture.outbox }),
  );
  const second = fixture.track(
    createComposerSourceState("root", { repository: secondRepository, key: fixture.key, outbox: fixture.outbox }),
  );
  await Promise.all([first.ready(), second.ready()]);
  // Both injected sources hydrate the one imported structured value.
  expect(first.getSnapshot()).toMatchObject(value);
  expect(second.getSnapshot()).toMatchObject(value);
  const starting = await requireRow(secondRepository, fixture.key);
  expect(starting).toMatchObject(value);

  // First makes its last local edit, THEN captures its submission, so its own
  // generation cannot pre-explain any later refusal.
  const firstEdit: SkillEditorValue = { ...value, text: "🙂 /same /same /same first" };
  first.acceptEdit(firstEdit);
  await first.whenSettled();
  // Capture the first submission right after its last local edit.
  const submitted = await first.submissionDraft();
  const advanced = await requireRow(fixture.repository, fixture.key);
  expect(advanced).toMatchObject(firstEdit);
  expect(advanced.token.sessionRevision).toBeGreaterThan(starting.token.sessionRevision);
  // Immediately before its competing edit the second SOURCE still holds the
  // starting token, even though durable metadata has advanced. No await sits
  // between this assertion and second.acceptEdit.
  expect(second.getDraftToken()).toEqual(starting.token);

  // The competing durable edit comes from the second source's stale token.
  const secondEdit: SkillEditorValue = { ...value, text: "🙂 /same /same /same second" };
  second.acceptEdit(secondEdit);
  await second.whenSettled();
  const settled = await requireRow(fixture.repository, fixture.key);
  expect(settled).toMatchObject(secondEdit);
  expect(settled.token.sessionRevision).toBe(starting.token.sessionRevision + 2);

  // The first pane's pre-competition submission cannot clear the later value.
  expect(await first.settleDraft(submitted)).toMatchObject({ cleared: false });
  expect(await requireRow(fixture.repository, fixture.key)).toMatchObject(secondEdit);
});

beforeEach(() => {
  installLocalStorage(new MemoryStorage());
  globalThis.indexedDB = new IDBFactory();
  // The storage universe is replaced above; drop the repository singleton so a
  // default source captures THIS factory, not a previous test's.
  resetBrowserDraftRepositoryForTests();
  resetThreadsStoreForTests();
  resetPendingTurnsStoreForTests();
  resetToastStoreForTests();
});
afterEach(() => {
  resetWorkspaceStoreForTests();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

function image(name = "source.png") {
  return new File([new Uint8Array([1, 2, 3])], name, { type: "image/png" });
}

// The original preservation cases seeded and read the localStorage draft the
// source no longer uses after its repository cutover. Their scenario values and
// expected user-visible results are unchanged; only fixture seeding and the
// readiness/drain waits are migrated to the injected durable repository.
function durableDraft(
  row: {
    text: string;
    skillNames: readonly string[];
    commandNames?: readonly string[];
    mentions?: readonly { kind: string; name: string; offset: number }[];
  } | null,
) {
  if (!row) return null;
  return {
    text: row.text,
    skillNames: [...row.skillNames],
    ...(row.commandNames === undefined ? {} : { commandNames: [...row.commandNames] }),
    ...(row.mentions === undefined ? {} : { mentions: row.mentions.map((mention) => ({ ...mention })) }),
  };
}

async function durableSource(
  fixture: ReturnType<typeof sourceFixture>,
  ref: string,
  key: ReturnType<typeof browserDraftKey>,
): Promise<ComposerSourceState> {
  const source = fixture.track(
    createComposerSourceState(ref, { repository: fixture.repository, key, outbox: fixture.outbox }),
  );
  await source.ready();
  return source;
}

test("source restoration retains mixed atom identity and inert prose", async () => {
  const text = "🙂 /same /same /same";
  const commandOffset = text.indexOf("/same");
  const skillOffset = text.indexOf("/same", commandOffset + 1);
  const value = {
    text,
    skillNames: ["same"],
    commandNames: ["same"],
    mentions: [
      { kind: "command" as const, name: "same", offset: commandOffset },
      { kind: "skill" as const, name: "same", offset: skillOffset },
    ],
  };
  const repository = getBrowserDraftRepository();
  const key = browserDraftKey("root");
  await repository.seedDraft(key, value);
  const source = createTestComposerSource("root");
  await source.ready();
  expect(source.getSnapshot()).toMatchObject(value);
  source.persistDraft(text);
  await source.whenSettled();
  expect(readComposerDraft("root")).toEqual(value);
  expect(durableDraft(await repository.load(key))).toEqual(value);
});

// Dropping mentions at restore/persist would select this deliberately inert token.
test("source persistence keeps a meaningful empty mention set", async () => {
  const value = { text: "/same", skillNames: [], mentions: [] };
  const repository = getBrowserDraftRepository();
  const key = browserDraftKey("root");
  await repository.seedDraft(key, value);
  const source = createTestComposerSource("root");
  await source.ready();
  source.persistDraft(value.text);
  await source.whenSettled();
  expect(readComposerDraft("root")).toEqual(value);
  expect(durableDraft(await repository.load(key))).toEqual(value);
});

test.each([undefined, []])(
  "runtime reads and default submission preserve meaningful mention presence with no command selections (%s)",
  async (mentions) => {
    const fixture = sourceFixture("source-empty-command-metadata");
    const source = await durableSource(fixture, "root", fixture.key);
    const value: SkillEditorValue = {
      text: "literal /same",
      skillNames: [],
      commandNames: [],
      ...(mentions === undefined ? {} : { mentions }),
    };
    source.acceptEdit(value);
    await source.whenSettled();
    const before = await requireRow(fixture.secondRepository(), fixture.key);
    expect(durableDraft(before)).toEqual(value);
    const projected = readComposerDraft("root");
    expect({ ...projected, commandNames: projected.commandNames ?? [] }).toEqual(value);
    const submitted = await source.submissionDraft();
    expect(submitted.text).toBe(value.text);
    expect(submitted.skillNames).toEqual([]);
    expect(submitted.commandNames ?? []).toEqual([]);
    expect(submitted.mentions).toEqual(mentions);
    expect(await source.settleDraft(submitted)).toMatchObject({ cleared: true, persistence: "committed" });
    expect(durableDraft(await requireRow(fixture.secondRepository(), fixture.key))).toEqual({
      text: "",
      skillNames: [],
    });
  },
);

// A single broad marker deletion spans both retained atoms and loses their identity.
test("detached submitted markers preserve atoms between each exact splice", async () => {
  const fixture = sourceFixture("source-state-detached-markers");
  const key = browserDraftKey("root");
  await fixture.repository.seedDraft(key, {
    text: "🙂 [image 1] /same [image 2] /same [image 3] /same",
    skillNames: ["same"],
    commandNames: ["same"],
    mentions: [
      { kind: "command", name: "same", offset: 13 },
      { kind: "skill", name: "same", offset: 29 },
    ],
  });
  const source = await durableSource(fixture, "root", key);
  const operations = createAttachmentOperations(source.editor, source.attachments);
  operations.replaceWithSettled(
    [1, 2, 3].map((marker) => ({
      marker,
      name: `${marker}.png`,
      mediaType: "image/png",
      data: "AQID",
      pending: false,
    })),
  );
  source.clearSubmittedAttachments(source.attachments.getState().items);
  expect(source.attachments.getState().items).toEqual([]);
  await source.whenSettled();
  expect(durableDraft(await fixture.repository.load(key))).toEqual({
    text: "🙂  /same  /same  /same",
    skillNames: ["same"],
    commandNames: ["same"],
    mentions: [
      { kind: "command", name: "same", offset: 4 },
      { kind: "skill", name: "same", offset: 11 },
    ],
  });
});

test("retirement holds copy submitted arrays and release overlapping exact requests once", async () => {
  const fixture = sourceFixture("source-state-retirement-overlap");
  await fixture.repository.seedDraft(fixture.key, { text: "before [image 1][image 2]", skillNames: [] });
  const source = await durableSource(fixture, "root", fixture.key);
  const original: PendingAttachment = {
    marker: 1,
    name: "original.png",
    mediaType: "image/png",
    data: "AQID",
    pending: false,
  };
  const retained: PendingAttachment = { ...original, marker: 2, name: "retained.png" };
  source.attachments.setState({ items: [original, retained] });
  const firstRelease = source.holdSubmittedAttachmentRetirement();
  const secondRelease = source.holdSubmittedAttachmentRetirement();
  try {
    const submitted = [original];
    const retiring = trackOperation(source.clearSubmittedAttachments(submitted));
    submitted[0] = retained;
    let completed = false;
    void retiring.then(() => {
      completed = true;
    });
    const firstDone = trackOperation(firstRelease());
    expect(firstRelease()).toBe(firstDone);
    expect(source.attachments.getState().items).toEqual([original, retained]);
    expect(source.getSnapshot().submittedAttachmentRetirementPending).toBe(true);
    expect(completed).toBe(false);
    const secondDone = trackOperation(secondRelease());
    expect(secondRelease()).toBe(secondDone);
    await Promise.all([retiring, firstDone, secondDone]);
    expect(source.attachments.getState().items).toEqual([retained]);
    expect(source.attachments.getState().items[0]).toBe(retained);
    expect(source.getSnapshot()).toMatchObject({
      text: "before [image 2]",
      submittedAttachmentRetirementPending: false,
    });
    const observer = fixture.secondRepository();
    const after = await requireRow(observer, fixture.key);
    expect(after.text).toBe("before [image 2]");
    const generation = source.getLocalGeneration();
    await source.clearSubmittedAttachments([{ ...retained }]);
    await source.whenSettled();
    expect(source.getLocalGeneration()).toBe(generation);
    expect(await requireRow(observer, fixture.key)).toEqual(after);
  } finally {
    await Promise.all([firstRelease(), secondRelease()]);
  }
});

test("a later retirement hold stays live while the preceding real cleanup write settles", async () => {
  const fixture = sourceFixture("source-state-retirement-later-hold");
  await fixture.repository.seedDraft(fixture.key, { text: "before [image 1][image 2]", skillNames: [] });
  const source = await durableSource(fixture, "root", fixture.key);
  const original: PendingAttachment = {
    marker: 1,
    name: "original.png",
    mediaType: "image/png",
    data: "AQID",
    pending: false,
  };
  const retained: PendingAttachment = { ...original, marker: 2, name: "retained.png" };
  source.attachments.setState({ items: [original, retained] });
  const releaseFirst = source.holdSubmittedAttachmentRetirement();
  const firstRetiring = trackOperation(source.clearSubmittedAttachments([original]));
  const hold = holdNextWriteTransaction([COMPOSER_DRAFTS_STORE, ANNOTATION_NAMESPACES_STORE]);
  let releaseSecond: (() => Promise<void>) | undefined;
  try {
    const firstDone = trackOperation(releaseFirst());
    await hold.reached;
    releaseSecond = source.holdSubmittedAttachmentRetirement();
    const secondRetiring = trackOperation(source.clearSubmittedAttachments([retained]));
    let completed = false;
    void secondRetiring.then(() => {
      completed = true;
    });
    hold.release();
    await source.whenSettled();
    // The actual first producer has completed. That cannot release a later
    // activation's protected request or its retained-source admission flag.
    expect(source.attachments.getState().items).toEqual([retained]);
    expect(source.attachments.getState().items[0]).toBe(retained);
    expect(source.getSnapshot().submittedAttachmentRetirementPending).toBe(true);
    expect(completed).toBe(false);
    await releaseSecond();
    await Promise.all([firstRetiring, secondRetiring, firstDone]);
    expect(source.attachments.getState().items).toEqual([]);
    expect(source.getSnapshot()).toMatchObject({ text: "before ", submittedAttachmentRetirementPending: false });
    expect((await requireRow(fixture.secondRepository(), fixture.key)).text).toBe("before ");
  } finally {
    hold.release();
    await releaseSecond?.();
    await releaseFirst();
  }
});

test("disposing a held retirement releases waiters without later publication or durable cleanup", async () => {
  const fixture = sourceFixture("source-state-retirement-dispose");
  await fixture.repository.seedDraft(fixture.key, { text: "before [image 1]", skillNames: [] });
  const source = await durableSource(fixture, "root", fixture.key);
  const original: PendingAttachment = {
    marker: 1,
    name: "original.png",
    mediaType: "image/png",
    data: "AQID",
    pending: false,
  };
  source.attachments.setState({ items: [original] });
  const before = await requireRow(fixture.secondRepository(), fixture.key);
  const release = source.holdSubmittedAttachmentRetirement();
  const retiring = trackOperation(source.clearSubmittedAttachments([original]));
  const published: unknown[] = [];
  const unsubscribe = source.subscribe(() => published.push(source.getSnapshot()));
  sourceCleanups.push(unsubscribe);
  source.dispose();
  await retiring;
  const closed = source.getSnapshot();
  await release();
  expect(source.alive).toBe(false);
  expect(source.attachments.getState().items).toEqual([]);
  expect(source.getSnapshot()).toBe(closed);
  expect(published).toEqual([]);
  expect(await requireRow(fixture.secondRepository(), fixture.key)).toEqual(before);
});

// Ignoring command selections in the empty-work predicate discards a real recovery.
test("detached command-only recovery persists instead of being discarded", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  await getBrowserDraftRepository().seedDraft(browserDraftKey("root"), {
    text: "",
    skillNames: [],
    commandNames: ["same"],
    mentions: [],
  });
  const input = [{ type: "command" as const, name: "same" }];
  const original = await storage.enqueueIntent({
    targetRef: "root",
    method: "turn/start",
    payload: { ref: "root", input },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const source = createTestComposerSource("root");
  await source.ready();
  source.setActiveRecoveryId(original.clientMutationId);
  await source.queueRecoveryPersistence(original.clientMutationId, "", [], [], ["same"], []);
  expect(source.getSnapshot().activeRecoveryId).toBe(original.clientMutationId);
  expect(source.getSnapshot().commandNames).toEqual(["same"]);
  expect(await storage.getRecovery(original.clientMutationId)).toMatchObject({
    clientMutationId: original.clientMutationId,
    targetRef: "root",
    payload: { input },
    composerText: "",
    composerMentions: [],
  });
});

// Equal prose/skills do not authorize clearing a different command snapshot.
test("source clearing compares the submitted command selection", async () => {
  await getBrowserDraftRepository().seedDraft(browserDraftKey("root"), {
    text: "/same",
    skillNames: [],
    commandNames: ["same"],
    mentions: [{ kind: "command", name: "same", offset: 0 }],
  });
  const source = createTestComposerSource("root");
  await source.ready();
  source.clearIfUnchanged("/same", 0, readDraftRevision("root"), [], ["different"]);
  expect(source.getSnapshot().text).toBe("/same");
  expect(readComposerDraft("root").commandNames).toEqual(["same"]);
});

test("synchronous source cleanup changes only its runtime projection, never legacy bytes or durable metadata", async () => {
  const fixture = sourceFixture("source-runtime-only-clear");
  const value = { text: "ordinary /same", skillNames: [], mentions: [] };
  await fixture.repository.seedDraft(fixture.key, value);
  const source = await durableSource(fixture, "root", fixture.key);
  const before = await requireRow(fixture.secondRepository(), fixture.key);
  // Supported bytes arriving after hydration belong to the migration boundary,
  // not an obsolete synchronous cleanup capability.
  const legacy = "later legacy work";
  localStorage.setItem(draftStorageKey("root"), legacy);
  const remove = vi.spyOn(localStorage, "removeItem");
  expect(source.clearIfUnchanged(value.text, 0, readDraftRevision("root"), [])).toBe(true);
  expect(source.getSnapshot().text).toBe("");
  expect(readComposerDraft("root")).toEqual({ text: "", skillNames: [] });
  expect(remove).not.toHaveBeenCalled();
  expect(localStorage.getItem(draftStorageKey("root"))).toBe(legacy);
  expect(await requireRow(fixture.secondRepository(), fixture.key)).toEqual(before);
});

test("discarding an empty source recovery performs its guarded durable change without a second legacy clear", async () => {
  const fixture = sourceFixture("source-empty-recovery-no-legacy-clear");
  const input = [{ type: "text" as const, text: "recover me" }];
  const sent = await fixture.outbox.enqueueIntent({
    targetRef: "root",
    method: "turn/start",
    payload: { ref: "root", input },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input },
    composerText: "recover me",
  });
  await fixture.outbox.transferToRecovery(sent.clientMutationId, "rejected");
  const source = await durableSource(fixture, "root", fixture.key);
  source.setActiveRecoveryId(sent.clientMutationId);
  const before = await requireRow(fixture.secondRepository(), fixture.key);
  const remove = vi.spyOn(localStorage, "removeItem");
  await source.queueRecoveryPersistence(sent.clientMutationId, "", [], [], [], []);
  expect(await fixture.outbox.getRecovery(sent.clientMutationId)).toBeUndefined();
  expect(source.getSnapshot().activeRecoveryId).toBeNull();
  expect(await requireRow(fixture.secondRepository(), fixture.key)).toMatchObject({
    text: before.text,
    skillNames: before.skillNames,
    token: { ...before.token, sessionRevision: before.token.sessionRevision + 1 },
  });
  expect(remove).not.toHaveBeenCalled();
});

// Detached marker writes must patch the full atom value, not infer chips from text.
test.each(["success", "failure"] as const)("detached mixed atoms survive image decode %s", async (outcome) => {
  const fixture = sourceFixture(`source-state-mixed-atoms-${outcome}`);
  const key = browserDraftKey("root");
  const value = {
    text: "🙂 /same /same /same",
    skillNames: ["same"],
    commandNames: ["same"],
    mentions: [
      { kind: "command" as const, name: "same", offset: 3 },
      { kind: "skill" as const, name: "same", offset: 9 },
    ],
  };
  await fixture.repository.seedDraft(key, value);
  const source = await durableSource(fixture, "root", key);
  const encoding = installControlledImageEncoding();
  const view = renderHook(() => useAttachments(source.editor, source.attachments));
  act(() => view.result.current.ingestFiles([image()], (message) => pushToast("error", message)));
  view.unmount();
  source.editor.write("🙂 /same /same /same[image 1] newer", 33);
  await act(async () => {
    if (outcome === "success") await encoding.resolve();
    else await encoding.reject();
  });
  await waitFor(() => expect(source.attachments.getState().items.some((item) => item.pending)).toBe(false));
  await source.whenSettled();
  expect(durableDraft(await fixture.repository.load(key))).toEqual({
    ...value,
    text: outcome === "success" ? "🙂 /same /same /same[image 1] newer" : "🙂 /same /same /same newer",
  });
  expect(source.getSnapshot()).toMatchObject({ commandNames: ["same"], mentions: value.mentions });
});

test.each(["success", "failure"] as const)(
  "an encode %s settles its original source while detached",
  async (outcome) => {
    const fixture = sourceFixture(`source-state-encode-settles-${outcome}`);
    const rootKey = browserDraftKey("root");
    const childKey = browserDraftKey("child");
    const encoding = installControlledImageEncoding();
    const source = await durableSource(fixture, "root", rootKey);
    source.editor.write("original ", 9);
    const view = renderHook(() => useAttachments(source.editor, source.attachments));
    act(() => view.result.current.ingestFiles([image()], (message) => pushToast("error", message)));
    const child = await durableSource(fixture, "child", childKey);
    child.editor.write("child draft", 11);
    view.unmount();
    await act(async () => {
      if (outcome === "success") await encoding.resolve();
      else await encoding.reject();
    });
    await waitFor(() => expect(source.attachments.getState().items.some((item) => item.pending)).toBe(false));
    await source.whenSettled();
    await child.whenSettled();
    expect(durableDraft(await fixture.repository.load(childKey))).toEqual({ text: "child draft", skillNames: [] });
    if (outcome === "success") {
      expect(source.attachments.getState().items).toEqual([
        { marker: 1, name: "source.png", mediaType: "image/png", pending: false, data: "AQID", width: 8, height: 4 },
      ]);
      expect(durableDraft(await fixture.repository.load(rootKey))).toEqual({
        text: "original [image 1]",
        skillNames: [],
      });
      expect(getToasts()).toEqual([]);
    } else {
      expect(source.attachments.getState().items).toEqual([]);
      expect(durableDraft(await fixture.repository.load(rootKey))).toEqual({ text: "original ", skillNames: [] });
      expect(getToasts().map((toast) => ({ kind: toast.kind, text: toast.text }))).toEqual([
        { kind: "error", text: "source.png (image decode failed)" },
      ]);
    }
  },
);

test.each(["success", "failure"] as const)("disposing a source invalidates its pending encode %s", async (outcome) => {
  const fixture = sourceFixture(`source-state-dispose-encode-${outcome}`);
  const rootKey = browserDraftKey("root");
  const encoding = installControlledImageEncoding();
  const source = await durableSource(fixture, "root", rootKey);
  const view = renderHook(() => useAttachments(source.editor, source.attachments));
  act(() => view.result.current.ingestFiles([image()], (message) => pushToast("error", message)));
  const generation = source.attachments.getState().generationRef.current;
  view.unmount();
  source.dispose();
  const replacement = await durableSource(fixture, "root", rootKey);
  replacement.editor.write("replacement [image 1]", 21);
  expect(source.alive).toBe(false);
  expect(source.attachments.getState().generationRef.current).toBeGreaterThan(generation);
  await act(async () => {
    if (outcome === "success") await encoding.resolve();
    else await encoding.reject();
  });
  await waitFor(() => expect(source.attachments.getState().items).toEqual([]));
  await replacement.whenSettled();
  expect(replacement.attachments.getState().items).toEqual([]);
  expect(durableDraft(await fixture.repository.load(rootKey))).toEqual({
    text: "replacement [image 1]",
    skillNames: [],
  });
  expect(getToasts()).toEqual([]);
});

test.each([
  { action: "close", outcome: "success" },
  { action: "close", outcome: "failure" },
  { action: "reset", outcome: "success" },
  { action: "reset", outcome: "failure" },
])("pane $action ignores late encode $outcome after ID reuse", async ({ action, outcome }) => {
  const encoding = installControlledImageEncoding();
  const pane: OpenPaneRecord = { id: "session-1", type: "session", params: { ref: "root" }, slot: "main" };
  workspaceStore.setState({ panes: [pane], focusedPaneId: pane.id });
  const lifetime = conversationPaneLifetime(pane);
  const source = lifetime.composer;
  if (!source) throw new Error("session pane has no source composer");
  await source.ready();
  const view = renderHook(() => useAttachments(source.editor, source.attachments));
  act(() => view.result.current.ingestFiles([image()], (message) => pushToast("error", message)));
  const generation = source.attachments.getState().generationRef.current;
  view.unmount();
  if (action === "close") workspaceStore.getState().closePane(pane.id);
  else resetWorkspaceStoreForTests();
  const replacementPane: OpenPaneRecord = { id: "session-1", type: "session", params: { ref: "root" }, slot: "main" };
  workspaceStore.setState({ panes: [replacementPane], focusedPaneId: replacementPane.id });
  const replacement = conversationPaneLifetime(replacementPane);
  await replacement.composer?.ready();
  replacement.composer?.editor.write("replacement [image 1]", 21);
  expect(lifetime.alive).toBe(false);
  expect(replacement.serial).not.toBe(lifetime.serial);
  expect(source.attachments.getState().generationRef.current).toBeGreaterThan(generation);
  await act(async () => {
    if (outcome === "success") await encoding.resolve();
    else await encoding.reject();
  });
  expect(source.attachments.getState().items).toEqual([]);
  expect(replacement.composer?.attachments.getState().items).toEqual([]);
  await replacement.composer?.whenSettled();
  expect(durableDraft(await getBrowserDraftRepository().load(browserDraftKey("root")))).toEqual({
    text: "replacement [image 1]",
    skillNames: [],
  });
  expect(getToasts()).toEqual([]);
});

test.each(["success", "failure"] as const)(
  "an old detached encode %s cannot claim a newer same-ref draft",
  async (outcome) => {
    const fixture = sourceFixture(`source-state-old-encode-${outcome}`);
    const rootKey = browserDraftKey("root");
    const encoding = installControlledImageEncoding();
    const source = await durableSource(fixture, "root", rootKey);
    const view = renderHook(() => useAttachments(source.editor, source.attachments));
    act(() => view.result.current.ingestFiles([image()], (message) => pushToast("error", message)));
    view.unmount();
    const newer = await durableSource(fixture, "root", rootKey);
    newer.editor.write("foreign [image 1] /cleanup", 27);
    newer.editSkillNames(["cleanup"]);
    newer.persistDraft("foreign [image 1] /cleanup");
    await act(async () => {
      if (outcome === "success") await encoding.resolve();
      else await encoding.reject();
    });
    await waitFor(() => expect(source.attachments.getState().items.some((item) => item.pending)).toBe(false));
    await newer.whenSettled();
    expect(newer.getSnapshot().text).toBe("foreign [image 1] /cleanup");
    expect(durableDraft(await fixture.repository.load(rootKey))).toEqual({
      text: "foreign [image 1] /cleanup",
      skillNames: ["cleanup"],
    });
    expect(getToasts().map((toast) => toast.text)).toEqual(
      outcome === "failure" ? ["source.png (image decode failed)"] : [],
    );
  },
);

// --- Approved Step1: the source's revision-only repository contract --------
//
// These cases drive the injected repository and outbox on one IndexedDB
// factory, exactly as a production source does after the writer cutover. The
// source APIs they exercise (ready/whenSettled/acceptEdit/submissionDraft/
// prepareAnnotation/acknowledgeAnnotation) are part of the approved contract.

const sourceOperations: Array<Promise<unknown>> = [];
// Register a test-created operation so teardown drains it even when an assertion
// fails before the test awaits it.
function trackOperation<T>(operation: Promise<T>): Promise<T> {
  sourceOperations.push(operation);
  return operation;
}

function sourceFixture(
  databaseName: string,
  makeOutbox: (options: ConstructorParameters<typeof MutationOutboxIndexedDB>[0]) => MutationOutboxIndexedDB = (
    options,
  ) => new MutationOutboxIndexedDB(options),
) {
  const factory = new IDBFactory();
  const key = browserDraftKey("root");
  const repository = createBrowserDraftRepository({ indexedDB: factory, databaseName });
  const outbox = makeOutbox({ indexedDB: factory, databaseName });
  const sources: ComposerSourceState[] = [];
  const repositories: BrowserDraftRepository[] = [repository];
  sourceCleanups.push(() =>
    closeSourceFixture({ sources: sources.splice(0).reverse(), outbox, repositories: repositories.splice(0) }),
  );
  return {
    factory,
    databaseName,
    key,
    repository,
    outbox,
    // A second real adapter on the SAME factory/database: it holds an
    // independent connection and does not receive the first adapter's
    // publications, so a competing source's stale token is genuine.
    secondRepository(): BrowserDraftRepository {
      const extra = createBrowserDraftRepository({ indexedDB: factory, databaseName });
      repositories.push(extra);
      return extra;
    },
    track(source: ComposerSourceState): ComposerSourceState {
      sources.push(source);
      return source;
    },
  };
}

async function requireRow(repository: BrowserDraftRepository, key: ReturnType<typeof browserDraftKey>) {
  const row = await repository.load(key);
  if (!row) throw new Error("expected a durable draft row");
  return row;
}

// A read-only observer on the SAME database, used to sample committed values at
// a deterministic boundary. Its readonly transaction is created SYNCHRONOUSLY
// inside the repository's publication callback - after that write commits but
// before the source can issue its next serial write - so the sample cannot miss
// an intermediate value to a later write. It forwards nothing and changes no
// production operation; it only reads the durable row.
function createDraftObserver(factory: IDBFactory, databaseName: string) {
  let database: IDBDatabase | undefined;
  let stopped = false;
  const opened = new Promise<void>((resolve, reject) => {
    const request = factory.open(databaseName, LOCAL_COMPOSITION_DATABASE_VERSION);
    request.addEventListener(
      "success",
      () => {
        database = request.result;
        resolve();
      },
      { once: true },
    );
    request.addEventListener("error", () => reject(request.error ?? new Error("observer open failed")), { once: true });
  });
  return {
    ready: () => opened,
    // Returns undefined once stopped, so no future notification creates new work.
    read(key: ReturnType<typeof browserDraftKey>): Promise<string | undefined> | undefined {
      if (stopped || !database) return undefined;
      const request = database
        .transaction(COMPOSER_DRAFTS_STORE, "readonly")
        .objectStore(COMPOSER_DRAFTS_STORE)
        .get(serializeSessionKey(key));
      return new Promise<string | undefined>((resolve, reject) => {
        request.addEventListener("success", () => resolve((request.result as { text?: string } | undefined)?.text), {
          once: true,
        });
        request.addEventListener("error", () => reject(request.error ?? new Error("observer read failed")), {
          once: true,
        });
      });
    },
    stop(): void {
      stopped = true;
    },
    close(): void {
      database?.close();
    },
  };
}

// Teardown-only availability check: while the async boundary is absent, cleanup
// still disposes and closes; once it exists, cleanup drains the real in-flight
// accepted work before closing adapters. It never stands in for production
// behavior inside a test.
async function drainIfSupported(source: ComposerSourceState): Promise<void> {
  const candidate = source as unknown as { whenSettled?: () => Promise<void> };
  if (typeof candidate.whenSettled === "function") await candidate.whenSettled();
}

// Dispose every tracked source and attempt its drain, close both adapters, and
// collect any failure so one rejected step cannot skip the rest. Faults are
// released first, and registered test operations are drained before disposal.
async function closeSourceFixture(input: {
  sources: ComposerSourceState[];
  outbox: MutationOutboxIndexedDB;
  repositories: BrowserDraftRepository[];
  disarm?: () => void;
}): Promise<void> {
  const failures: unknown[] = [];
  const attempt = async (step: () => void | Promise<void>): Promise<void> => {
    try {
      await step();
    } catch (error) {
      failures.push(error);
    }
  };
  await attempt(() => input.disarm?.());
  await attempt(async () => {
    const results = await Promise.allSettled(sourceOperations.splice(0));
    for (const result of results) {
      // A registered operation's rejection is a real failure, never swallowed:
      // a test that expects rejection registers an assertion-wrapped promise.
      if (result.status === "rejected") failures.push(result.reason);
    }
  });
  for (const source of input.sources) await attempt(() => source.dispose());
  for (const source of input.sources) await attempt(() => drainIfSupported(source));
  await attempt(() => input.outbox.close());
  for (const repository of input.repositories) await attempt(() => repository.close());
  if (failures.length > 0) throw new AggregateError(failures, "source fixture cleanup failed");
}

// A local TextEditor port backed by one always-current closure: reads report the
// live text/cursor/selection and writes replace them, so a bound source sees
// real external editor state rather than a vacuous stub.
function fakeEditor(initial: {
  text: string;
  cursor: number;
  selection: { start: number; end: number };
}): TextEditor & {
  simulateTyping(text: string, cursor: number, start: number, end: number): void;
  held(): { text: string; cursor: number; selection: { start: number; end: number } };
} {
  let value = { ...initial, selection: { ...initial.selection } };
  return {
    read: () => ({ text: value.text, cursor: value.cursor, selection: { ...value.selection } }),
    write: (text, cursor) => {
      value = { text, cursor, selection: { start: cursor, end: cursor } };
    },
    // A literal UI keystroke state, independent of any source acceptance.
    simulateTyping: (text, cursor, start, end) => {
      value = { text, cursor, selection: { start, end } };
    },
    held: () => ({ ...value, selection: { ...value.selection } }),
  };
}

// Replace the namespace incarnation through real IndexedDB metadata, the way a
// reset/replacement does, without touching the repository under test. Returns
// the exact incarnation written so a caller can assert against it.
async function replaceNamespaceIncarnation(factory: IDBFactory, databaseName: string): Promise<string> {
  const database = await new Promise<IDBDatabase>((resolve, reject) => {
    const request = factory.open(databaseName, LOCAL_COMPOSITION_DATABASE_VERSION);
    request.addEventListener("success", () => resolve(request.result), { once: true });
    request.addEventListener("error", () => reject(request.error ?? new Error("open failed")), { once: true });
  });
  try {
    const rows = await new Promise<Array<{ namespaceKey: string }>>((resolve, reject) => {
      const request = database
        .transaction(ANNOTATION_NAMESPACES_STORE, "readonly")
        .objectStore(ANNOTATION_NAMESPACES_STORE)
        .getAll();
      request.addEventListener("success", () => resolve(request.result), { once: true });
      request.addEventListener("error", () => reject(request.error ?? new Error("read failed")), { once: true });
    });
    const row = rows[0];
    if (!row) throw new Error("namespace row missing");
    const incarnation = `replacement-${crypto.randomUUID()}`;
    await new Promise<void>((resolve, reject) => {
      const transaction = database.transaction(ANNOTATION_NAMESPACES_STORE, "readwrite");
      transaction.objectStore(ANNOTATION_NAMESPACES_STORE).put({ namespaceKey: row.namespaceKey, incarnation });
      transaction.addEventListener("complete", () => resolve(), { once: true });
      transaction.addEventListener("abort", () => reject(transaction.error ?? new Error("aborted")), { once: true });
    });
    return incarnation;
  } finally {
    database.close();
  }
}

// 1. The public write path reaches the injected repository after the approved
// asynchronous readiness boundary.
test("persistDraft reaches the injected IndexedDB repository after readiness", async () => {
  const fixture = sourceFixture("source-state-persist");
  const source = fixture.track(
    createComposerSourceState("root", { repository: fixture.repository, key: fixture.key, outbox: fixture.outbox }),
  );
  source.persistDraft("repository draft");
  await source.ready();
  await source.whenSettled();
  expect(await fixture.repository.load(fixture.key)).toMatchObject({ text: "repository draft", skillNames: [] });
});

// 2. Immutable accepted values, serial acceptance order, accepted writes after
// disposal, and meaningful omitted-versus-empty metadata.
test("accepted explicit values are cloned immutably and finish in order after disposal", async () => {
  const fixture = sourceFixture("source-state-disposed");
  const source = fixture.track(
    createComposerSourceState("root", { repository: fixture.repository, key: fixture.key, outbox: fixture.outbox }),
  );
  await source.ready();
  // A deterministic read-only observer: its readonly transaction starts inside
  // each publication callback, before the source can issue its next serial write.
  // Observation stays enabled through the drain; one narrow local cleanup disarms
  // future reads, drains every already-created read, then closes it - on the
  // failure path too, since it is registered before the risky awaits.
  const observer = createDraftObserver(fixture.factory, fixture.databaseName);
  const observerReads: Array<Promise<string | undefined>> = [];
  let observerStopped = false;
  let unsubscribe: (() => void) | undefined;
  sourceCleanups.push(async () => {
    const failures: unknown[] = [];
    const attempt = async (step: () => void | Promise<void>): Promise<void> => {
      try {
        await step();
      } catch (error) {
        failures.push(error);
      }
    };
    await attempt(() => {
      observerStopped = true;
    });
    await attempt(() => {
      unsubscribe?.();
    });
    await attempt(async () => {
      const results = await Promise.allSettled(observerReads);
      for (const result of results) if (result.status === "rejected") failures.push(result.reason);
    });
    await attempt(() => observer.close());
    if (failures.length > 0) throw new AggregateError(failures, "draft observer cleanup failed");
  });
  await observer.ready();
  unsubscribe = fixture.repository.subscribe(() => {
    if (observerStopped) return;
    const read = observer.read(fixture.key);
    if (read) observerReads.push(trackOperation(read));
  });
  const mention = { kind: "command" as const, name: "same", offset: 0 };
  const first: SkillEditorValue = {
    text: "/same",
    skillNames: ["same"],
    commandNames: ["same"],
    mentions: [mention],
  };
  source.acceptEdit(first);
  first.skillNames.push("must-not-leak");
  first.commandNames?.push("must-not-leak");
  mention.name = "mutated-after-acceptance";
  await source.whenSettled();
  expect(await fixture.repository.load(fixture.key)).toMatchObject({
    text: "/same",
    skillNames: ["same"],
    commandNames: ["same"],
    mentions: [{ kind: "command", name: "same", offset: 0 }],
  });
  // Accept two more entries with no intervening drain or read, then dispose while
  // both are still outstanding.
  source.acceptEdit({ text: "middle", skillNames: [], mentions: [] });
  source.acceptEdit({ text: "last", skillNames: [], mentions: [] });
  const snapshotBeforeDisposal = source.getSnapshot();
  source.dispose();
  // Observation stays enabled through the outstanding drain; the local cleanup
  // disarms it only after this point.
  await source.whenSettled();
  const finalRow = await requireRow(fixture.repository, fixture.key);
  expect(finalRow).toMatchObject({ text: "last", skillNames: [], mentions: [] });
  expect(finalRow.token.sessionRevision).toBe(4);
  // The closed source's snapshot stays stable.
  expect(source.getSnapshot()).toEqual(snapshotBeforeDisposal);
  // Real committed intermediate accepted values, in acceptance order.
  const observedTexts = (await Promise.all(observerReads)).filter(
    (text): text is string => text !== undefined && text !== "",
  );
  expect(observedTexts).toEqual(["/same", "middle", "last"]);
});

test("a disposed source cannot clear its own still-matching durable row", async () => {
  const fixture = sourceFixture("source-state-disposal-only");
  const value: SkillEditorValue = {
    text: "still here /same",
    skillNames: ["same"],
    commandNames: ["same"],
    mentions: [],
  };
  await fixture.repository.seedDraft(fixture.key, value);
  const source = fixture.track(
    createComposerSourceState("root", { repository: fixture.repository, key: fixture.key, outbox: fixture.outbox }),
  );
  await source.ready();
  const submitted = await source.submissionDraft(value);
  const before = await requireRow(fixture.repository, fixture.key);
  // Before disposal the capture is genuinely live and matches durable state, so
  // any refusal is attributable to disposal, not to an already-stale capture.
  const guard = submitted.settlementGuard;
  if (!guard) throw new Error("expected a submission guard");
  expect(submitted).toMatchObject(value);
  expect(guard.token).toEqual(before.token);
  expect(guard.backing).toEqual(source.getRuntimeBacking());
  expect(guard.localGeneration).toBe(source.getLocalGeneration());
  expect(submitted.stillOwnsEdit?.()).toBe(true);
  source.dispose();
  expect(await source.settleDraft(submitted)).toMatchObject({ cleared: false });
  expect(await requireRow(fixture.repository, fixture.key)).toEqual(before);
});

test("accepted metadata keeps omitted and explicit-empty mentions distinct", async () => {
  const fixture = sourceFixture("source-state-metadata");
  const source = fixture.track(
    createComposerSourceState("root", { repository: fixture.repository, key: fixture.key, outbox: fixture.outbox }),
  );
  await source.ready();
  source.acceptEdit({ text: "omitted", skillNames: ["same"], commandNames: ["same"] });
  await source.whenSettled();
  const omitted = await fixture.repository.load(fixture.key);
  expect(omitted).toMatchObject({ text: "omitted", skillNames: ["same"], commandNames: ["same"] });
  expect(omitted?.mentions).toBeUndefined();
  source.acceptEdit({ text: "empty", skillNames: ["same"], commandNames: ["same"], mentions: [] });
  await source.whenSettled();
  const empty = await fixture.repository.load(fixture.key);
  expect(empty).toMatchObject({ text: "empty", skillNames: ["same"], mentions: [] });
  expect(empty?.mentions).toEqual([]);
});

// 3. Typing before asynchronous hydration stays visible and becomes durable.
test("typing before hydration stays visible and is not overwritten by the older value", async () => {
  const fixture = sourceFixture("source-state-early");
  await fixture.repository.seedDraft(fixture.key, { text: "old", skillNames: ["same"] });
  const source = fixture.track(
    createComposerSourceState("root", { repository: fixture.repository, key: fixture.key, outbox: fixture.outbox }),
  );
  source.acceptEdit({ text: "typed", skillNames: [], mentions: [] });
  await source.ready();
  await source.whenSettled();
  expect(source.getSnapshot()).toMatchObject({ text: "typed", mentions: [] });
  expect(await fixture.repository.load(fixture.key)).toMatchObject({ text: "typed", skillNames: [] });
});

// 3b. Supported legacy localStorage bytes transfer before load.
test("a supported legacy draft transfers before load and stays visible", async () => {
  const fixture = sourceFixture("source-state-transfer");
  localStorage.setItem(draftStorageKey("root"), "legacy text");
  const source = fixture.track(
    createComposerSourceState("root", { repository: fixture.repository, key: fixture.key, outbox: fixture.outbox }),
  );
  await source.ready();
  expect(source.getSnapshot()).toMatchObject({ text: "legacy text" });
  expect(await fixture.repository.load(fixture.key)).toMatchObject({ text: "legacy text", skillNames: [] });
  expect(localStorage.getItem(draftStorageKey("root"))).toBeNull();
});

const migrationValues: Array<{ name: string; v1: string; v2: string | null; expected: SkillEditorValue }> = [
  { name: "v1", v1: "literal /same legacy", v2: null, expected: { text: "literal /same legacy", skillNames: [] } },
  {
    name: "v2 atoms",
    v1: "older v1 fallback",
    v2: '{"text":"🙂 /same /same","skillNames":["same"],"commandNames":["same"],"mentions":[{"kind":"command","name":"same","offset":3},{"kind":"skill","name":"same","offset":9}]}',
    expected: {
      text: "🙂 /same /same",
      skillNames: ["same"],
      commandNames: ["same"],
      mentions: [
        { kind: "command", name: "same", offset: 3 },
        { kind: "skill", name: "same", offset: 9 },
      ],
    },
  },
  {
    name: "v2 empty mentions",
    v1: "older empty-mention fallback",
    v2: '{"text":"/same","skillNames":[],"commandNames":["same"],"mentions":[]}',
    expected: { text: "/same", skillNames: [], commandNames: ["same"], mentions: [] },
  },
];

test.each(migrationValues)(
  "pending $name transfer cannot initialize a row before same-source healthy retry",
  async ({ v1, v2, expected }) => {
    const factory = new IDBFactory();
    const databaseName = `pending-migration-${crypto.randomUUID()}`;
    const key = browserDraftKey("root");
    let abortTransfer = true;
    let faults = 0;
    const repository = createBrowserDraftRepository({
      indexedDB: factory,
      databaseName,
      beforeCommit(operation) {
        if (abortTransfer && operation === "transferSupportedDrafts") {
          faults += 1;
          throw new Error("migration transaction aborted");
        }
      },
    });
    const outbox = new MutationOutboxIndexedDB({ indexedDB: factory, databaseName });
    localStorage.setItem(draftStorageKey("root"), v1);
    if (v2 !== null) localStorage.setItem(composerDraftStorageKey("root"), v2);
    localStorage.setItem("unrelated-migration-input", "untouched");
    const rpc = vi.spyOn(AppwireClient.prototype, "request");
    const transfer = vi.spyOn(repository, "transferSupportedDrafts");
    const seed = vi.spyOn(repository, "seedDraft");
    const source = createComposerSourceState("root", { repository, key, outbox });
    const identity = source.sourceId;
    sourceCleanups.push(() =>
      closeSourceFixture({
        sources: [source],
        outbox,
        repositories: [repository],
        disarm: () => {
          abortTransfer = false;
        },
      }),
    );
    // A pending initialization may refuse ready; neither outcome may finalize an
    // import or seed a first row. Observe the actual transfer, never mock its result.
    await Promise.allSettled([source.ready()]);
    await source.whenSettled();
    expect(faults).toBeGreaterThan(0);
    expect(await Promise.all(transfer.mock.results.map((result) => result.value))).toEqual(
      transfer.mock.results.map(() => ({ state: "pending" })),
    );
    expect(localStorage.getItem(draftStorageKey("root"))).toBe(v1);
    expect(localStorage.getItem(composerDraftStorageKey("root"))).toBe(v2);
    // Soft assertions let this same execution also expose the healthy retry;
    // they remain failures, not waivers or expected-failure declarations.
    expect.soft(await repository.load(key)).toBeNull();
    expect.soft(seed).not.toHaveBeenCalled();
    abortTransfer = false;
    await source.ready();
    await source.whenSettled();
    const row = await requireRow(repository, key);
    expect(durableDraft(row)).toEqual(expected);
    expect(row.key).toEqual(key);
    expect(row.token.sessionRevision).toBe(1);
    expect(row.token.namespaceIncarnation).not.toBe("");
    expect(source.sourceId).toBe(identity);
    expect(source.alive).toBe(true);
    expect.soft(localStorage.getItem(draftStorageKey("root"))).toBeNull();
    expect.soft(localStorage.getItem(composerDraftStorageKey("root"))).toBeNull();
    expect(localStorage.getItem("unrelated-migration-input")).toBe("untouched");
    const prepared = await source.prepareAnnotation();
    expect(prepared).toMatchObject({ key, token: row.token, backing: { kind: "draft" }, localGeneration: 0 });
    expect(source.getSnapshot()).toMatchObject({ ...expected, commandNames: expected.commandNames ?? [] });
    expect(source.getSnapshot().mentions).toEqual(expected.mentions);
    expect(rpc).not.toHaveBeenCalled();
    expect(await outbox.listOutbox("root")).toEqual([]);
  },
);

test.each(["turn", "note"] as const)(
  "first composer opening preserves legacy atoms after inactive %s recovery metadata",
  async (method) => {
    const fixture = sourceFixture(`first-composer-after-${method}`);
    const independent = fixture.secondRepository();
    const v1 = "older v1 value";
    const v2 =
      '{"text":"🙂 /same /same","skillNames":["same"],"commandNames":["same"],"mentions":[{"kind":"skill","name":"same","offset":3},{"kind":"command","name":"same","offset":9}]}';
    const expected: SkillEditorValue = {
      text: "🙂 /same /same",
      skillNames: ["same"],
      commandNames: ["same"],
      mentions: [
        { kind: "skill", name: "same", offset: 3 },
        { kind: "command", name: "same", offset: 9 },
      ],
    };
    localStorage.setItem(draftStorageKey("root"), v1);
    localStorage.setItem(composerDraftStorageKey("root"), v2);
    expect(await independent.load(fixture.key)).toBeNull();
    const rpc = vi.spyOn(AppwireClient.prototype, "request");
    const sent = await fixture.outbox.enqueueIntent({
      targetRef: "root",
      threadId: "thread-1",
      method: method === "turn" ? "turn/queue" : "notes/human/set",
      payload:
        method === "turn"
          ? { ref: "root", input: [{ type: "text", text: "rejected input" }] }
          : { ref: "root", expectedInstanceId: "thread-1", note: "unsaved note" },
      attachments: [],
      optimisticDisplay: null,
      composerText: method === "turn" ? "rejected input" : "unsaved note",
    });
    const recovery = await fixture.outbox.transferToRecovery(sent.clientMutationId, "rejected", "refused");
    expect(recovery).toEqual({ ...sent, recoveryKind: "rejected", recoveryReason: "refused" });
    // This is the FIRST source. No fixture draft or helper pre-transfer hid the
    // background writer's migration boundary.
    const source = fixture.track(
      createComposerSourceState("root", { repository: fixture.repository, key: fixture.key, outbox: fixture.outbox }),
    );
    await source.ready();
    const row = await requireRow(independent, fixture.key);
    expect.soft(durableDraft(row)).toEqual(expected);
    expect.soft(source.getSnapshot()).toMatchObject(expected);
    expect.soft(row.token.sessionRevision).toBe(2);
    expect(row.key).toEqual(fixture.key);
    expect(source.getDraftToken()).toEqual(row.token);
    expect(source.getRuntimeBacking()).toEqual({ kind: "draft" });
    expect(await fixture.outbox.getRecovery(sent.clientMutationId)).toEqual(recovery);
    expect(localStorage.getItem(draftStorageKey("root"))).toBeNull();
    expect(localStorage.getItem(composerDraftStorageKey("root"))).toBeNull();
    expect(await fixture.outbox.listOutbox("root")).toEqual([]);
    expect(rpc).not.toHaveBeenCalled();
  },
);

test("a rejected initialization retries in the same dirty source before one explicit submission", async () => {
  const fixture = sourceFixture("source-initialization-retry");
  const stored: SkillEditorValue = { text: "old durable /same", skillNames: [], commandNames: ["same"], mentions: [] };
  const independent = fixture.secondRepository();
  await independent.seedDraft(fixture.key, stored);
  const baseline = await requireRow(independent, fixture.key);
  const open = fixture.factory.open.bind(fixture.factory);
  let unavailable = true;
  let faults = 0;
  const opening = vi.spyOn(fixture.factory, "open").mockImplementation((...args) => {
    if (unavailable) {
      faults += 1;
      throw new Error("transient initialization open failure");
    }
    return open(...args);
  });
  const rpc = vi.spyOn(AppwireClient.prototype, "request");
  const source = fixture.track(
    createComposerSourceState("root", { repository: fixture.repository, key: fixture.key, outbox: fixture.outbox }),
  );
  const identity = source.sourceId;
  const commandMention = { kind: "command" as const, name: "same", offset: 3 };
  const accepted: SkillEditorValue = {
    text: "🙂 /same /same accepted",
    skillNames: ["same"],
    commandNames: ["same"],
    mentions: [commandMention, { kind: "skill", name: "same", offset: 9 }],
  };
  try {
    source.acceptEdit(accepted);
    accepted.skillNames.push("must-not-leak");
    commandMention.name = "must-not-leak";
    await expect(source.ready()).rejects.toThrow("transient initialization open failure");
    await source.whenSettled();
    expect(faults).toBeGreaterThan(0);
    expect(getToasts().map(({ kind, text }) => ({ kind, text }))).toEqual([
      { kind: "error", text: "Couldn't load the draft. Try again." },
    ]);
    expect(await independent.load(fixture.key)).toEqual(baseline);
    expect(rpc).not.toHaveBeenCalled();
    unavailable = false;
    await expect(source.ready()).resolves.toBeUndefined();
    await source.whenSettled();
    const expected: SkillEditorValue = {
      text: "🙂 /same /same accepted",
      skillNames: ["same"],
      commandNames: ["same"],
      mentions: [
        { kind: "command", name: "same", offset: 3 },
        { kind: "skill", name: "same", offset: 9 },
      ],
    };
    const row = await requireRow(independent, fixture.key);
    expect(durableDraft(row)).toEqual(expected);
    expect(row.token).toEqual({ namespaceIncarnation: baseline.token.namespaceIncarnation, sessionRevision: 2 });
    expect(source.getSnapshot()).toMatchObject(expected);
    expect(source.sourceId).toBe(identity);
    expect(source.getLocalGeneration()).toBe(1);
    expect(source.getRuntimeBacking()).toEqual({ kind: "draft" });
    const prepared = await source.prepareAnnotation();
    if (!prepared) throw new Error("expected useful preparation after initialization retry");
    expect(prepared).toMatchObject({ token: row.token, localGeneration: 1, backing: { kind: "draft" } });
    expect(rpc).not.toHaveBeenCalled();
    const later: SkillEditorValue = { text: "later edit /same", skillNames: [], commandNames: ["same"], mentions: [] };
    // Only this explicit call sends. The real wrapper captures its old guard
    // before perform; a later accepted edit during perform must survive cleanup.
    const perform = vi.fn(async () => {
      source.acceptEdit(later);
      await source.whenSettled();
    });
    const failed = vi.fn();
    await submitWithPendingTracking({ ref: "root", ...expected, source, onFailure: failed }, perform);
    expect(perform).toHaveBeenCalledTimes(1);
    expect(failed).not.toHaveBeenCalled();
    expect(source.getSnapshot()).toMatchObject(later);
    expect(durableDraft(await requireRow(independent, fixture.key))).toEqual(later);
    expect((await requireRow(independent, fixture.key)).token.sessionRevision).toBe(3);
    expect(source.sourceId).toBe(identity);
    expect(source.getLocalGeneration()).toBe(2);
    expect(source.ownsPreparation(prepared)).toBe(false);
  } finally {
    unavailable = false;
    await source.whenSettled();
    opening.mockRestore();
  }
});

// 5. A real storage abort - a SYNCHRONOUS commit-boundary fault, not an
// asynchronous request failure - keeps the SAME accepted entry for a bounded
// retry and leaves the source useful.
test("a real storage abort keeps the same accepted entry for a later bounded retry", async () => {
  const factory = new IDBFactory();
  const databaseName = `source-state-abort-${crypto.randomUUID()}`;
  const key = browserDraftKey("root");
  let abortCommits = true;
  const repository = createBrowserDraftRepository({
    indexedDB: factory,
    databaseName,
    beforeCommit(operation) {
      if (abortCommits && operation === "commitEdit") throw new Error("storage aborted");
    },
  });
  const outbox = new MutationOutboxIndexedDB({ indexedDB: factory, databaseName });
  const source = createComposerSourceState("root", { repository, key, outbox });
  sourceCleanups.push(() =>
    closeSourceFixture({
      sources: [source],
      outbox,
      repositories: [repository],
      disarm: () => {
        abortCommits = false;
      },
    }),
  );
  await source.ready();
  const baseline = await repository.load(key);
  if (!baseline) throw new Error("expected the ready-time metadata row");
  const mention = { kind: "command" as const, name: "same", offset: 0 };
  const value: SkillEditorValue = {
    text: "/same",
    skillNames: ["same"],
    commandNames: ["same"],
    mentions: [mention],
  };
  source.acceptEdit(value);
  value.skillNames.push("must-not-leak");
  mention.name = "mutated";
  await source.whenSettled();
  // The abort left the ready-time baseline row; the accepted work is unsaved.
  expect(await repository.load(key)).toEqual(baseline);
  expect(getToasts().some((toast) => toast.kind === "error")).toBe(true);
  expect(await source.prepareAnnotation()).toBeNull();
  // Disarm the fault and retry the SAME accepted entry through the bounded drain.
  abortCommits = false;
  await source.whenSettled();
  expect(await repository.load(key)).toMatchObject({
    text: "/same",
    skillNames: ["same"],
    commandNames: ["same"],
    mentions: [{ kind: "command", name: "same", offset: 0 }],
  });
  expect(source.alive).toBe(true);
});

// 6. Namespace replacement: the captured write is refused and left unsaved, a
// guarded reconciliation follows, a fresh deliberate edit writes, and the old
// destructive callback stays unauthorized.
test("a namespace replacement refuses the captured write, reconciles, and leaves the old cleanup unauthorized", async () => {
  const fixture = sourceFixture("source-state-namespace");
  await fixture.repository.seedDraft(fixture.key, { text: "original", skillNames: ["same"] });
  const baseline = await fixture.repository.load(fixture.key);
  if (!baseline) throw new Error("expected the seeded baseline row");
  const source = fixture.track(
    createComposerSourceState("root", { repository: fixture.repository, key: fixture.key, outbox: fixture.outbox }),
  );
  await source.ready();
  // A valid preparation exists before the namespace is replaced.
  const originalPreparation = await source.prepareAnnotation();
  if (!originalPreparation) throw new Error("expected the original preparation");
  const replacementIncarnation = await replaceNamespaceIncarnation(fixture.factory, fixture.databaseName);
  // The captured write is refused by the namespace fence and left unsaved.
  source.acceptEdit({ text: "stale write", skillNames: ["same"], mentions: [] });
  await source.whenSettled();
  const afterRefusal = await fixture.repository.load(fixture.key);
  if (!afterRefusal) throw new Error("expected the row after the refused write");
  // A lazy load re-tokens to the replacement namespace while preserving the full
  // ordinary content and the session revision; only the incarnation follows the
  // reset, so compare content and revision, and the actual replacement token.
  expect(afterRefusal).toMatchObject({ text: "original", skillNames: ["same"] });
  expect(afterRefusal.token.sessionRevision).toBe(baseline.token.sessionRevision);
  expect(afterRefusal.token.namespaceIncarnation).toBe(replacementIncarnation);
  // An unsaved accepted queue makes preparation return null while it reconciles.
  expect(await source.prepareAnnotation()).toBeNull();
  // A fresh deliberate edit now writes against the reconciled namespace.
  source.acceptEdit({ text: "fresh write", skillNames: ["same"], mentions: [] });
  await source.whenSettled();
  const freshRow = await fixture.repository.load(fixture.key);
  expect(freshRow).toMatchObject({ text: "fresh write" });
  expect(await source.prepareAnnotation()).not.toBeNull();
  // The pre-reset preparation is still unauthorized: acknowledging it changes
  // neither the current source snapshot nor the durable row.
  const snapshotBeforeCleanup = source.getSnapshot();
  source.acknowledgeAnnotation(originalPreparation, originalPreparation.token, {
    text: "old cleanup",
    skillNames: [],
    mentions: [],
  });
  expect(source.getSnapshot()).toEqual(snapshotBeforeCleanup);
  expect(await fixture.repository.load(fixture.key)).toEqual(freshRow);
});

// 7. Annotation preparation and acknowledgment.
test("prepareAnnotation returns the current token/backing/generation and ownsPreparation tracks a generation change", async () => {
  const fixture = sourceFixture("source-state-prepare-ordinary");
  await fixture.repository.seedDraft(fixture.key, { text: "base", skillNames: ["same"] });
  const source = fixture.track(
    createComposerSourceState("root", { repository: fixture.repository, key: fixture.key, outbox: fixture.outbox }),
  );
  await source.ready();
  const preparation = await source.prepareAnnotation();
  if (!preparation) throw new Error("expected an ordinary preparation");
  expect(preparation.key).toEqual(fixture.key);
  expect(preparation.token).toEqual(source.getDraftToken());
  expect(preparation.backing).toEqual({ kind: "draft" });
  expect(preparation.localGeneration).toBe(source.getLocalGeneration());
  expect(source.ownsPreparation(preparation)).toBe(true);
  const durable = await requireRow(fixture.repository, fixture.key);
  expect(preparation.token).toEqual(durable.token);
  source.acceptEdit({ text: "typed", skillNames: ["same"], mentions: [] });
  await source.whenSettled();
  expect(source.ownsPreparation(preparation)).toBe(false);
});

test("prepareAnnotation reconciles a later foreign durable commit's winning value and token", async () => {
  const fixture = sourceFixture("source-state-prepare-foreign");
  const value: SkillEditorValue = {
    text: "shared start /same",
    skillNames: ["same"],
    commandNames: ["same"],
    mentions: [{ kind: "command", name: "same", offset: 13 }],
  };
  await fixture.repository.seedDraft(fixture.key, value);
  const source = fixture.track(
    createComposerSourceState("root", { repository: fixture.repository, key: fixture.key, outbox: fixture.outbox }),
  );
  await source.ready();
  // A second real adapter commits a foreign winning value this source never saw.
  const foreign = fixture.secondRepository();
  const foreignValue: SkillEditorValue = {
    text: "foreign winner /same",
    skillNames: ["same"],
    commandNames: ["same"],
    mentions: [{ kind: "command", name: "same", offset: 15 }],
  };
  const foreignCommit = await foreign.commitEdit(
    fixture.key,
    (await requireRow(foreign, fixture.key)).token,
    foreignValue,
    () => true,
  );
  if (foreignCommit.state !== "committed") throw new Error(`expected the foreign commit, got ${foreignCommit.state}`);
  const preparation = await source.prepareAnnotation();
  if (!preparation) throw new Error("expected a preparation after the foreign commit");
  // The complete reconciled source value at the return boundary, before any
  // extra await.
  expect(source.getSnapshot()).toMatchObject({
    text: "foreign winner /same",
    skillNames: ["same"],
    commandNames: ["same"],
    mentions: [{ kind: "command", name: "same", offset: 15 }],
  });
  const durable = await fixture.repository.load(fixture.key);
  if (!durable) throw new Error("expected the foreign durable row");
  expect(durable).toMatchObject(foreignValue);
  expect(preparation.token).toEqual(durable.token);
});

test("prepareAnnotation drains a distinct pending recovery write at its return boundary", async () => {
  const fixture = sourceFixture("source-state-prepare-recovery");
  setMutationStorageForTests(fixture.outbox);
  // Command-only recovery: the canonical durable input is the text item plus a
  // {type:"command",name} item; the mention is editing metadata only.
  const originalMentions = [{ kind: "command" as const, name: "same", offset: 18 }];
  const originalInput = [
    { type: "text" as const, text: "recovery original /same", mentions: originalMentions },
    { type: "command" as const, name: "same" },
  ];
  const original = await fixture.outbox.enqueueIntent({
    targetRef: "root",
    method: "turn/start",
    payload: { ref: "root", input: originalInput },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: originalInput },
    composerText: "recovery original /same",
    composerMentions: originalMentions,
  });
  await fixture.outbox.transferToRecovery(original.clientMutationId, "rejected");
  const source = fixture.track(
    createComposerSourceState("root", { repository: fixture.repository, key: fixture.key, outbox: fixture.outbox }),
  );
  await source.ready();
  source.setActiveRecoveryId(original.clientMutationId);
  // A pending write whose value DIFFERS from the seeded recovery row.
  const editedText = "recovery edited /same";
  const editedMentions = [{ kind: "command" as const, name: "same", offset: 16 }];
  const expectedInput = [
    { type: "text" as const, text: editedText, mentions: editedMentions },
    { type: "command" as const, name: "same" },
  ];
  let pendingFulfilled = false;
  let pendingRejected = false;
  const pendingWrite = trackOperation(
    source.queueRecoveryPersistence(original.clientMutationId, editedText, [], [], ["same"], editedMentions),
  );
  trackOperation(
    pendingWrite.then(
      () => {
        pendingFulfilled = true;
      },
      () => {
        pendingRejected = true;
      },
    ),
  );
  const preparation = await source.prepareAnnotation();
  // At the return boundary, BEFORE any other await: pending fulfillment, the
  // complete literal selected source value and the source token.
  expect(pendingRejected).toBe(false);
  expect(pendingFulfilled).toBe(true);
  if (!preparation) throw new Error("expected a recovery preparation");
  expect(preparation.backing).toEqual({ kind: "recovery", recoveryId: original.clientMutationId });
  const snapshotAtReturn = source.getSnapshot();
  const tokenAtReturn = source.getDraftToken();
  expect(snapshotAtReturn).toMatchObject({
    activeRecoveryId: original.clientMutationId,
    text: editedText,
    skillNames: [],
    commandNames: ["same"],
    mentions: editedMentions,
  });
  // Independently loaded durable metadata, compared with the CAPTURED values.
  const durableAtReturn = await fixture.repository.load(fixture.key);
  if (!durableAtReturn) throw new Error("expected a durable row at the return boundary");
  expect(preparation.token).toEqual(durableAtReturn.token);
  expect(tokenAtReturn).toEqual(durableAtReturn.token);
  expect(await fixture.outbox.getRecovery(original.clientMutationId)).toMatchObject({
    composerText: editedText,
    composerMentions: editedMentions,
    payload: { input: expectedInput },
  });
  await pendingWrite;
  source.acceptEdit({ text: `${editedText} two`, skillNames: [], commandNames: ["same"], mentions: editedMentions });
  await source.whenSettled();
  expect(source.ownsPreparation(preparation)).toBe(false);
});

test("prepareAnnotation returns null when the selected recovery record is absent", async () => {
  const fixture = sourceFixture("source-state-prepare-missing");
  setMutationStorageForTests(fixture.outbox);
  await fixture.repository.seedDraft(fixture.key, { text: "base", skillNames: [] });
  const source = fixture.track(
    createComposerSourceState("root", { repository: fixture.repository, key: fixture.key, outbox: fixture.outbox }),
  );
  await source.ready();
  source.setActiveRecoveryId("missing-recovery");
  expect(await source.prepareAnnotation()).toBeNull();
});

test("acknowledgeAnnotation publishes an already-committed value without another durable edit", async () => {
  const fixture = sourceFixture("source-state-ack-commit");
  await fixture.repository.seedDraft(fixture.key, { text: "base", skillNames: ["same"] });
  const source = fixture.track(
    createComposerSourceState("root", { repository: fixture.repository, key: fixture.key, outbox: fixture.outbox }),
  );
  await source.ready();
  const preparation = await source.prepareAnnotation();
  if (!preparation) throw new Error("expected an ordinary preparation");
  const value: SkillEditorValue = {
    text: "base /same",
    skillNames: ["same"],
    commandNames: ["same"],
    mentions: [{ kind: "command", name: "same", offset: 5 }],
  };
  // The durable commit is the storage setup; acknowledgment publishes it, it is
  // not another explicit edit.
  const committed = await fixture.repository.commitEdit(fixture.key, preparation.token, value, () => true);
  if (committed.state !== "committed") throw new Error(`expected committed, got ${committed.state}`);
  const afterCommit = await fixture.repository.load(fixture.key);
  if (!afterCommit) throw new Error("expected the committed row");
  source.acknowledgeAnnotation(preparation, committed.draft.token, value);
  await source.whenSettled();
  expect(source.getSnapshot()).toMatchObject(value);
  expect(source.getDraftToken()).toEqual(committed.draft.token);
  // Publication changes nothing durable: the complete row is identical.
  expect(await fixture.repository.load(fixture.key)).toEqual(afterCommit);
});

test("a stale acknowledgment preserves the bound editor's literal later typing and attachment identity", async () => {
  const fixture = sourceFixture("source-state-ack-stale");
  await fixture.repository.seedDraft(fixture.key, { text: "base", skillNames: ["same"] });
  const source = fixture.track(
    createComposerSourceState("root", { repository: fixture.repository, key: fixture.key, outbox: fixture.outbox }),
  );
  await source.ready();
  const preparation = await source.prepareAnnotation();
  if (!preparation) throw new Error("expected an ordinary preparation");
  const editor = fakeEditor({ text: "base", cursor: 4, selection: { start: 4, end: 4 } });
  const unbind = source.bindEditor(editor);
  sourceCleanups.push(unbind);
  const attachment: PendingAttachment = {
    marker: 1,
    name: "stale.png",
    mediaType: "image/png",
    data: "AQID",
    pending: false,
  };
  source.attachments.setState((state) => ({ ...state, items: [attachment], nextMarkerRef: { current: 1 } }));
  const later: SkillEditorValue = {
    text: "typed over /same",
    skillNames: ["same"],
    commandNames: ["same"],
    mentions: [{ kind: "command", name: "same", offset: 11 }],
  };
  source.acceptEdit(later);
  await source.whenSettled();
  // The user's literal later editor state, set through the port (independent of
  // acceptEdit), with a nonempty selection.
  editor.simulateTyping("typed over /same", 15, 11, 15);
  const expectedEditor = { text: "typed over /same", cursor: 15, selection: { start: 11, end: 15 } };
  expect(editor.held()).toEqual(expectedEditor);
  // A stale acknowledgment must change nothing.
  source.acknowledgeAnnotation(preparation, preparation.token, { text: "appended", skillNames: [], mentions: [] });
  await source.whenSettled();
  expect(source.getSnapshot()).toMatchObject(later);
  expect(editor.held()).toEqual(expectedEditor);
  expect(source.attachments.getState().items).toHaveLength(1);
  expect(source.attachments.getState().items[0]).toBe(attachment);
  expect(source.attachments.getState().items[0]).toMatchObject({
    marker: 1,
    name: "stale.png",
    mediaType: "image/png",
    data: "AQID",
    pending: false,
  });
  // The complete durable later value, not only text/mentions.
  expect(await requireRow(fixture.repository, fixture.key)).toMatchObject({
    text: "typed over /same",
    skillNames: ["same"],
    commandNames: ["same"],
    mentions: [{ kind: "command", name: "same", offset: 11 }],
  });
});

test("prepareAnnotation returns null while accepted work is unsaved or the source is disposed", async () => {
  const factory = new IDBFactory();
  const databaseName = `source-state-prepare-null-${crypto.randomUUID()}`;
  const key = browserDraftKey("root");
  let abortCommits = true;
  const repository = createBrowserDraftRepository({
    indexedDB: factory,
    databaseName,
    beforeCommit(operation) {
      if (abortCommits && operation === "commitEdit") throw new Error("storage aborted");
    },
  });
  const outbox = new MutationOutboxIndexedDB({ indexedDB: factory, databaseName });
  const source = createComposerSourceState("root", { repository, key, outbox });
  sourceCleanups.push(() =>
    closeSourceFixture({
      sources: [source],
      outbox,
      repositories: [repository],
      disarm: () => {
        abortCommits = false;
      },
    }),
  );
  await source.ready();
  source.acceptEdit({ text: "unsaved", skillNames: [], mentions: [] });
  await source.whenSettled();
  expect(getToasts().some((toast) => toast.kind === "error")).toBe(true);
  expect(await source.prepareAnnotation()).toBeNull();
  abortCommits = false;
  source.dispose();
  expect(await source.prepareAnnotation()).toBeNull();
});

// 8. Submission capture and settlement.
test("submissionDraft captures generation/backing at entry and the post-drain token", async () => {
  const fixture = sourceFixture("source-state-capture");
  const accepted: SkillEditorValue = {
    text: "sent /keep",
    skillNames: [],
    commandNames: ["keep"],
    mentions: [{ kind: "command", name: "keep", offset: 5 }],
  };
  await fixture.repository.seedDraft(fixture.key, accepted);
  const source = fixture.track(
    createComposerSourceState("root", { repository: fixture.repository, key: fixture.key, outbox: fixture.outbox }),
  );
  await source.ready();
  const baselineBefore = await requireRow(fixture.repository, fixture.key);
  // A successful accepted save whose durable token advances during the drain.
  source.acceptEdit(accepted);
  const entryGeneration = source.getLocalGeneration();
  const entryBacking = source.getRuntimeBacking();
  const suppliedMention = { kind: "command" as const, name: "keep", offset: 5 };
  const supplied: SkillEditorValue = {
    text: "sent /keep",
    skillNames: [],
    commandNames: ["keep"],
    mentions: [suppliedMention],
  };
  const submittedPromise = trackOperation<SubmittedDraft>(source.submissionDraft(supplied));
  // Caller mutation cannot leak into the immutable captured payload.
  supplied.skillNames.push("mutated");
  supplied.commandNames?.push("mutated");
  suppliedMention.name = "mutated";
  // Change local work and return to the SAME full value while the capture waits.
  source.acceptEdit({ text: "different", skillNames: [], mentions: [] });
  source.acceptEdit(accepted);
  const submitted = await submittedPromise;
  expect(submitted).toMatchObject({
    text: "sent /keep",
    skillNames: [],
    commandNames: ["keep"],
    mentions: [{ kind: "command", name: "keep", offset: 5 }],
  });
  const guard = submitted.settlementGuard;
  if (!guard) throw new Error("expected a settlement guard");
  const durable = await requireRow(fixture.repository, fixture.key);
  // The captured token proves a successful accepted save: it advanced past the
  // real baseline in the same incarnation and equals independently committed
  // metadata at this post-drain boundary.
  expect(guard.token.sessionRevision).toBeGreaterThan(baselineBefore.token.sessionRevision);
  expect(guard.token.namespaceIncarnation).toBe(baselineBefore.token.namespaceIncarnation);
  expect(guard.token).toEqual(durable.token);
  // Entry generation/backing are preserved.
  expect(guard.backing).toEqual(entryBacking);
  expect(guard.localGeneration).toBe(entryGeneration);
  // Returning to the same text/metadata does not restore old ownership.
  expect(submitted.stillOwnsEdit?.()).toBe(false);
});

test("an unchanged ordinary submission settles once and an identical repeat preserves the cleared row", async () => {
  const fixture = sourceFixture("source-state-settle-once");
  const seeded: SkillEditorValue = {
    text: "/same sent",
    skillNames: ["same"],
    commandNames: ["same"],
    mentions: [{ kind: "command", name: "same", offset: 0 }],
  };
  await fixture.repository.seedDraft(fixture.key, seeded);
  const source = fixture.track(
    createComposerSourceState("root", { repository: fixture.repository, key: fixture.key, outbox: fixture.outbox }),
  );
  await source.ready();
  // The submission is exactly the seeded full value, so the unchanged check holds.
  const submitted = await source.submissionDraft(seeded);
  const before = await fixture.repository.load(fixture.key);
  if (!before) throw new Error("expected the seeded row");
  expect(await source.settleDraft(submitted)).toMatchObject({ cleared: true, persistence: "committed" });
  const afterFirst = await fixture.repository.load(fixture.key);
  if (!afterFirst) throw new Error("expected the cleared row");
  expect(afterFirst).toMatchObject({ text: "", skillNames: [] });
  expect(afterFirst.mentions).toBeUndefined();
  expect(afterFirst.token.sessionRevision).toBe(before.token.sessionRevision + 1);
  // The source publishes the clear locally: empty value, draft backing, token.
  expect(source.getSnapshot()).toMatchObject({ text: "", skillNames: [], commandNames: [] });
  expect(source.getSnapshot().mentions).toBeUndefined();
  expect(source.getRuntimeBacking()).toEqual({ kind: "draft" });
  expect(source.getDraftToken()).toEqual(afterFirst.token);
  expect(await source.settleDraft(submitted)).toMatchObject({ cleared: false });
  expect(await fixture.repository.load(fixture.key)).toEqual(afterFirst);
});

test("a same-text submission with absent-versus-empty mentions does not clear the durable row", async () => {
  const fixture = sourceFixture("source-state-settle-mismatch");
  await fixture.repository.seedDraft(fixture.key, {
    text: "/same sent",
    skillNames: ["same"],
    commandNames: ["same"],
  });
  const source = fixture.track(
    createComposerSourceState("root", { repository: fixture.repository, key: fixture.key, outbox: fixture.outbox }),
  );
  await source.ready();
  const before = await fixture.repository.load(fixture.key);
  if (!before) throw new Error("expected the seeded row");
  // Same text and selections, but the seeded row omits mentions while the
  // submission carries an explicit empty array: the unchanged check refuses.
  const submitted = await source.submissionDraft({
    text: "/same sent",
    skillNames: ["same"],
    commandNames: ["same"],
    mentions: [],
  });
  expect(await source.settleDraft(submitted)).toMatchObject({ cleared: false });
  expect(await fixture.repository.load(fixture.key)).toEqual(before);
  expect(source.getSnapshot()).toMatchObject({ text: "/same sent" });
});

test("a captured submission cannot clear a newer namespace or a replacement source's value", async () => {
  const fixture = sourceFixture("source-state-stale-cleanup");
  await fixture.repository.seedDraft(fixture.key, { text: "ordinary", skillNames: ["same"] });
  const source = fixture.track(
    createComposerSourceState("root", { repository: fixture.repository, key: fixture.key, outbox: fixture.outbox }),
  );
  await source.ready();
  const submitted = await source.submissionDraft();
  await replaceNamespaceIncarnation(fixture.factory, fixture.databaseName);
  expect(await source.settleDraft(submitted)).toMatchObject({ cleared: false });
  expect(await fixture.repository.load(fixture.key)).toMatchObject({ text: "ordinary" });

  const replacement = fixture.track(
    createComposerSourceState("root", { repository: fixture.repository, key: fixture.key, outbox: fixture.outbox }),
  );
  await replacement.ready();
  replacement.acceptEdit({ text: "replacement value", skillNames: ["same"], mentions: [] });
  await replacement.whenSettled();
  source.dispose();
  expect(await source.settleDraft(submitted)).toMatchObject({ cleared: false });
  expect(await fixture.repository.load(fixture.key)).toMatchObject({ text: "replacement value" });
});

// 6b. Real recovery submission and guarded resend settlement.
async function arrangeSelectedRecovery(databaseName: string) {
  const fixture = sourceFixture(databaseName);
  setMutationStorageForTests(fixture.outbox);
  const seeded: SkillEditorValue = { text: "ordinary", skillNames: ["same"], commandNames: ["same"] };
  await fixture.repository.seedDraft(fixture.key, seeded);
  // Command-only recovery: text plus a {type:"command",name} item; the mention
  // is editing metadata only.
  const originalMentions = [{ kind: "command" as const, name: "same", offset: 18 }];
  const originalInput = [
    { type: "text" as const, text: "recovery original /same", mentions: originalMentions },
    { type: "command" as const, name: "same" },
  ];
  const original = await fixture.outbox.enqueueIntent({
    targetRef: "root",
    method: "turn/start",
    payload: { ref: "root", input: originalInput },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: originalInput },
    composerText: "recovery original /same",
    composerMentions: originalMentions,
  });
  await fixture.outbox.transferToRecovery(original.clientMutationId, "rejected");
  const source = fixture.track(
    createComposerSourceState("root", { repository: fixture.repository, key: fixture.key, outbox: fixture.outbox }),
  );
  await source.ready();
  source.setActiveRecoveryId(original.clientMutationId);
  const editedMentions = [{ kind: "command" as const, name: "same", offset: 16 }];
  const editedInput = [
    { type: "text" as const, text: "recovery edited /same", mentions: editedMentions },
    { type: "command" as const, name: "same" },
  ];
  const pendingWrite = trackOperation(
    source.queueRecoveryPersistence(
      original.clientMutationId,
      "recovery edited /same",
      [],
      [],
      ["same"],
      editedMentions,
    ),
  );
  const preparation = await source.prepareAnnotation();
  await pendingWrite;
  if (!preparation) throw new Error("expected a recovery preparation");
  const entryGeneration = source.getLocalGeneration();
  const entryBacking = source.getRuntimeBacking();
  const submitted = await source.submissionDraft();
  const intent = {
    targetRef: "root",
    method: "turn/start",
    payload: { ref: "root", input: editedInput },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: editedInput },
    composerText: "recovery edited /same",
    composerMentions: editedMentions,
  };
  return { fixture, source, original, intent, preparation, submitted, entryGeneration, entryBacking };
}

test("a submitted recovery settles through a guarded resend into the approved local transition", async () => {
  const { fixture, source, original, intent, preparation, submitted, entryGeneration, entryBacking } =
    await arrangeSelectedRecovery("source-state-recovery-submit");
  expect(preparation.backing).toEqual({ kind: "recovery", recoveryId: original.clientMutationId });
  expect(submitted).toMatchObject({
    text: "recovery edited /same",
    skillNames: [],
    commandNames: ["same"],
    mentions: [{ kind: "command", name: "same", offset: 16 }],
  });
  // The captured submission guard is observed against independently loaded
  // metadata before the resend.
  const baselineBeforeResend = await requireRow(fixture.repository, fixture.key);
  const originalGuard = submitted.settlementGuard;
  if (!originalGuard) throw new Error("expected a captured submission guard");
  expect(originalGuard.token).toEqual(baselineBeforeResend.token);
  expect(originalGuard.backing).toEqual(entryBacking);
  expect(originalGuard.backing).toEqual({ kind: "recovery", recoveryId: original.clientMutationId });
  expect(originalGuard.localGeneration).toBe(entryGeneration);
  const guardCopy = {
    backing: { ...originalGuard.backing },
    token: { ...originalGuard.token },
    localGeneration: originalGuard.localGeneration,
  };
  const guard = {
    key: preparation.key,
    token: preparation.token,
    stillAuthorized: () => source.ownsPreparation(preparation),
  };
  const resent = await fixture.outbox.resendRecovery(original.clientMutationId, intent, guard);
  if (!resent) throw new Error("expected the guarded resend to create an accepted outbox record");
  expect(await fixture.outbox.getRecovery(original.clientMutationId)).toBeUndefined();
  expect(await fixture.outbox.getOutbox(resent.clientMutationId)).toMatchObject({
    method: "turn/start",
    state: "submitting",
  });
  // The accepted payload carries the selected submitted value, not an old seed.
  expect(resent.payload.input).toEqual(intent.payload.input);
  const afterResend = await fixture.repository.load(fixture.key);
  if (!afterResend) throw new Error("expected a durable row after the resend");
  // The ordinary value is exactly the initial one, including absent mentions.
  expect(afterResend).toMatchObject({ text: "ordinary", skillNames: ["same"], commandNames: ["same"] });
  expect(afterResend.mentions).toBeUndefined();
  // The resend advanced the durable token past the original captured guard.
  expect(afterResend.token).not.toEqual(guardCopy.token);
  expect(await source.settleDraft(submitted)).toMatchObject({ persistence: "committed" });
  // Approved transition: recovery cleared, empty local value, runtime draft
  // backing at the post-resend token; no second ordinary write.
  expect(source.getSnapshot()).toMatchObject({
    activeRecoveryId: null,
    text: "",
    skillNames: [],
    commandNames: [],
  });
  expect(source.getSnapshot().mentions).toBeUndefined();
  expect(source.getRuntimeBacking()).toEqual({ kind: "draft" });
  expect(source.getDraftToken()).toEqual(afterResend.token);
  expect(await requireRow(fixture.repository, fixture.key)).toEqual(afterResend);
  // The original submission guard stays byte-for-value unchanged.
  expect(submitted.settlementGuard).toEqual(guardCopy);
});

test("a backing replacement between an accepted resend and its settlement stays visible and does not recreate the recovery", async () => {
  const { fixture, source, original, intent, preparation, submitted } =
    await arrangeSelectedRecovery("source-state-recovery-stale");
  const guard = {
    key: preparation.key,
    token: preparation.token,
    stillAuthorized: () => source.ownsPreparation(preparation),
  };
  const resent = await fixture.outbox.resendRecovery(original.clientMutationId, intent, guard);
  if (!resent) throw new Error("expected the guarded resend to create an accepted outbox record");
  // Deliberate backing replacement: leave the removed recovery's selection.
  source.setActiveRecoveryId(null);
  const later: SkillEditorValue = {
    text: "typed after accept",
    skillNames: ["same"],
    commandNames: ["same"],
    mentions: [],
  };
  source.acceptEdit(later);
  await source.whenSettled();
  expect(await source.settleDraft(submitted)).toMatchObject({ cleared: false });
  // The complete literal new value survives on both the source and the durable row.
  expect(source.getSnapshot()).toMatchObject({
    activeRecoveryId: null,
    text: "typed after accept",
    skillNames: ["same"],
    commandNames: ["same"],
    mentions: [],
  });
  expect(source.getRuntimeBacking()).toEqual({ kind: "draft" });
  expect(await requireRow(fixture.repository, fixture.key)).toMatchObject({
    text: "typed after accept",
    skillNames: ["same"],
    commandNames: ["same"],
    mentions: [],
  });
  expect(await fixture.outbox.getRecovery(original.clientMutationId)).toBeUndefined();
});

// --- Source publication boundaries (RED checkpoint on source 809fd317) ------
//
// These cases exercise the approved contract at await boundaries the accepted
// suite never held: `prepareAnnotation` captures generation/backing BEFORE its
// durable read and the selected recovery id AFTER it, and `settleDraft` checks
// its original guard only before awaiting its final read. The seam is a
// transparent hold on the NEXT real repository read: the read runs to
// completion and its exact result is frozen, then the caller is held until
// release() - the read is never substituted.
function holdNextRead(repository: BrowserDraftRepository) {
  const real = repository.load.bind(repository);
  let releaseGate: () => void = () => undefined;
  let reachedGate: () => void = () => undefined;
  const gate = new Promise<void>((resolve) => {
    releaseGate = resolve;
  });
  const reached = new Promise<void>((resolve) => {
    reachedGate = resolve;
  });
  let armed = true;
  const spy = vi.spyOn(repository, "load").mockImplementation(async (key) => {
    const result = await real(key);
    if (armed) {
      armed = false;
      reachedGate();
      await gate;
    }
    return result;
  });
  let restored = false;
  return {
    reached,
    release(): void {
      releaseGate();
      if (!restored) {
        restored = true;
        spy.mockRestore();
      }
    },
  };
}

async function enqueueRecovery(fixture: ReturnType<typeof sourceFixture>, text: string) {
  const input = [
    { type: "text" as const, text },
    { type: "command" as const, name: "same" },
  ];
  const record = await fixture.outbox.enqueueIntent({
    targetRef: "root",
    method: "turn/start",
    payload: { ref: "root", input },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input },
    composerText: text,
  });
  await fixture.outbox.transferToRecovery(record.clientMutationId, "rejected");
  return record;
}

test("prepareAnnotation returns null when an ordinary edit lands during its durable read", async () => {
  const fixture = sourceFixture("source-boundaries-prepare-edit");
  await fixture.repository.seedDraft(fixture.key, { text: "base", skillNames: ["same"] });
  const source = fixture.track(
    createComposerSourceState("root", { repository: fixture.repository, key: fixture.key, outbox: fixture.outbox }),
  );
  await source.ready();
  const held = holdNextRead(fixture.repository);
  try {
    const pending = source.prepareAnnotation();
    await held.reached;
    const newer: SkillEditorValue = { text: "typed during read", skillNames: [], mentions: [] };
    source.acceptEdit(newer);
    held.release();
    expect(await pending).toBeNull();
    // The newer local work and its ordinary backing survive untouched.
    expect(source.getSnapshot()).toMatchObject({ text: "typed during read", skillNames: [] });
    expect(source.getSnapshot().mentions).toEqual([]);
    expect(source.getRuntimeBacking()).toEqual({ kind: "draft" });
  } finally {
    held.release();
  }
});

test("prepareAnnotation returns null when the source closes during its durable read", async () => {
  const fixture = sourceFixture("source-boundaries-prepare-dispose");
  await fixture.repository.seedDraft(fixture.key, { text: "base", skillNames: ["same"] });
  const source = fixture.track(
    createComposerSourceState("root", { repository: fixture.repository, key: fixture.key, outbox: fixture.outbox }),
  );
  await source.ready();
  const held = holdNextRead(fixture.repository);
  try {
    const pending = source.prepareAnnotation();
    await held.reached;
    source.dispose();
    held.release();
    expect(await pending).toBeNull();
  } finally {
    held.release();
  }
});

test("prepareAnnotation returns null when the selected recovery switches during its durable read", async () => {
  const fixture = sourceFixture("source-boundaries-prepare-recovery-switch");
  setMutationStorageForTests(fixture.outbox);
  const first = await enqueueRecovery(fixture, "recovery A /same");
  const second = await enqueueRecovery(fixture, "recovery B /same");
  const source = fixture.track(
    createComposerSourceState("root", { repository: fixture.repository, key: fixture.key, outbox: fixture.outbox }),
  );
  await source.ready();
  source.setActiveRecoveryId(first.clientMutationId);
  const held = holdNextRead(fixture.repository);
  try {
    const pending = source.prepareAnnotation();
    await held.reached;
    source.setActiveRecoveryId(second.clientMutationId);
    held.release();
    expect(await pending).toBeNull();
    // The capture invalidated even though the backing kind stayed "recovery":
    // the newer selection is preserved and never republished from the old one.
    expect(source.getRuntimeBacking()).toEqual({ kind: "recovery", recoveryId: second.clientMutationId });
  } finally {
    held.release();
  }
});

test("prepareAnnotation returns null when a public recovery edit lands during its durable read", async () => {
  const fixture = sourceFixture("source-boundaries-prepare-recovery-edit");
  setMutationStorageForTests(fixture.outbox);
  const original = await enqueueRecovery(fixture, "recovery original /same");
  const source = fixture.track(
    createComposerSourceState("root", { repository: fixture.repository, key: fixture.key, outbox: fixture.outbox }),
  );
  await source.ready();
  source.setActiveRecoveryId(original.clientMutationId);
  const held = holdNextRead(fixture.repository);
  try {
    const pending = source.prepareAnnotation();
    await held.reached;
    // The public recovery edit entry point, not acceptEdit: it must invalidate
    // the capture without switching to ordinary backing.
    source.editText("recovery edited while held");
    held.release();
    expect(await pending).toBeNull();
    expect(source.getSnapshot()).toMatchObject({ text: "recovery edited while held" });
    expect(source.getRuntimeBacking()).toEqual({ kind: "recovery", recoveryId: original.clientMutationId });
  } finally {
    held.release();
  }
});

test("an ordinary settlement cannot publish empty over work that lands during its final durable read", async () => {
  const fixture = sourceFixture("source-boundaries-settle-ordinary");
  await fixture.repository.seedDraft(fixture.key, { text: "sent", skillNames: ["same"] });
  const source = fixture.track(
    createComposerSourceState("root", { repository: fixture.repository, key: fixture.key, outbox: fixture.outbox }),
  );
  await source.ready();
  const submitted = await source.submissionDraft();
  const held = holdNextRead(fixture.repository);
  try {
    const settling = source.settleDraft(submitted);
    await held.reached;
    const newer: SkillEditorValue = { text: "typed during settlement", skillNames: [], mentions: [] };
    source.acceptEdit(newer);
    held.release();
    expect(await settling).toMatchObject({ persistence: "committed" });
    await source.whenSettled();
    // A durable clear is not permission to publish empty local state over work
    // that now owns the draft.
    expect(source.getSnapshot()).toMatchObject({ text: "typed during settlement", skillNames: [] });
    expect(source.getSnapshot().mentions).toEqual([]);
    expect(await requireRow(fixture.repository, fixture.key)).toMatchObject({ text: "typed during settlement" });
  } finally {
    held.release();
  }
});

test("a recovery settlement cannot publish empty over work that lands during its final durable read", async () => {
  const { fixture, source, original, intent, preparation, submitted } = await arrangeSelectedRecovery(
    "source-boundaries-settle-recovery",
  );
  const guard = {
    key: preparation.key,
    token: preparation.token,
    stillAuthorized: () => source.ownsPreparation(preparation),
  };
  const resent = await fixture.outbox.resendRecovery(original.clientMutationId, intent, guard);
  if (!resent) throw new Error("expected the guarded resend to create an accepted outbox record");
  const held = holdNextRead(fixture.repository);
  try {
    const settling = source.settleDraft(submitted);
    await held.reached;
    source.editText("typed during pending settlement");
    held.release();
    expect(await settling).toMatchObject({ persistence: "committed" });
    // The accepted resend's local transition may not wipe the newer recovery
    // edit, and it must not resurrect the deleted recovery record.
    expect(source.getSnapshot()).toMatchObject({
      activeRecoveryId: original.clientMutationId,
      text: "typed during pending settlement",
    });
    expect(source.getRuntimeBacking()).toEqual({ kind: "recovery", recoveryId: original.clientMutationId });
    expect(await fixture.outbox.getRecovery(original.clientMutationId)).toBeUndefined();
  } finally {
    held.release();
  }
});

// --- Remaining writer cutover (RED checkpoint on source 10b5a068) -----------
//
// These cases exercise the cohesive cutover the source still lacks: production
// defaults that start and persist on their own, the runtime draft read
// projection, injected recovery routing, and a single ordered hydration drain.
// They use the real repository/outbox and forward real calls; nothing is
// substituted, and every failure is an assertion (or a bounded useful-condition
// wait) against current behavior, never a missing-API crash.

// Transparent hold on the NEXT real repository commit. The real commit runs to
// completion FIRST (its exact result is frozen); `release()` then lets the
// production caller resume. Restoration of the spy is separate (`restore()`) so
// a caller can await the real in-flight work before restoring. Used to open the
// window between the scheduled drain and `ready`'s own drain call.
function holdNextCommit(repository: BrowserDraftRepository) {
  const real = repository.commitEdit.bind(repository);
  let releaseGate: () => void = () => undefined;
  let reachedGate: () => void = () => undefined;
  const gate = new Promise<void>((resolve) => {
    releaseGate = resolve;
  });
  const reached = new Promise<void>((resolve) => {
    reachedGate = resolve;
  });
  let armed = true;
  const spy = vi.spyOn(repository, "commitEdit").mockImplementation(async (key, expected, value, stillAuthorized) => {
    const result = await real(key, expected, value, stillAuthorized);
    if (armed) {
      armed = false;
      reachedGate();
      await gate;
    }
    return result;
  });
  let restored = false;
  return {
    reached,
    release(): void {
      releaseGate();
    },
    restore(): void {
      if (!restored) {
        restored = true;
        spy.mockRestore();
      }
    },
  };
}

test("a default source persists an accepted edit without an explicit ready call", async () => {
  const source = createTestComposerSource("writer-cutover-default");
  source.acceptEdit({ text: "default persisted", skillNames: [], mentions: [] });
  await waitFor(async () => {
    const row = await getBrowserDraftRepository().load(browserDraftKey("writer-cutover-default"));
    expect(row?.text).toBe("default persisted");
  });
  expect(source.getSnapshot()).toMatchObject({ text: "default persisted" });
});

test("a committed source edit updates the runtime draft projection without a localStorage fallback", async () => {
  const fixture = sourceFixture("writer-cutover-projection");
  const value: SkillEditorValue = {
    text: "projected value",
    skillNames: ["same"],
    mentions: [{ kind: "skill", name: "same", offset: 15 }],
  };
  const source = fixture.track(
    createComposerSourceState("root", { repository: fixture.repository, key: fixture.key, outbox: fixture.outbox }),
  );
  // Explicit hydration and a committed, awaited edit: the projection is the
  // only thing under test here, not default initialization.
  await source.ready();
  source.acceptEdit(value);
  await source.whenSettled();
  // The full durable value, read independently, is the precondition.
  expect(await fixture.repository.load(fixture.key)).toMatchObject(value);
  // The runtime read projection must reflect it, with meaningful mentions.
  expect(readComposerDraft("root")).toMatchObject(value);
  expect(readComposerDraft("root").mentions).toEqual(value.mentions);
  // No durable localStorage fallback may satisfy the projection: after ready's
  // permitted legacy initialization, the committed edit wrote nothing there.
  expect(localStorage.getItem(composerDraftStorageKey("root"))).toBeNull();
});

test("an injected source routes a recovery save through its injected outbox only", async () => {
  const fixture = sourceFixture("writer-cutover-recovery-routing");
  // Deliberately NO setMutationStorageForTests: the injected outbox on the
  // private factory is the only storage this source may write to.
  await fixture.repository.seedDraft(fixture.key, { text: "ordinary", skillNames: ["same"] });
  const original = await enqueueRecovery(fixture, "recovery original /same");
  const source = fixture.track(
    createComposerSourceState("root", { repository: fixture.repository, key: fixture.key, outbox: fixture.outbox }),
  );
  await source.ready();
  source.setActiveRecoveryId(original.clientMutationId);
  await source.queueRecoveryPersistence(original.clientMutationId, "recovery edited /same", [], [], ["same"], []);
  await waitFor(async () => {
    expect(await fixture.outbox.getRecovery(original.clientMutationId)).toMatchObject({
      composerText: "recovery edited /same",
    });
  });
  // Unrelated ordinary content survives the recovery edit.
  expect(await requireRow(fixture.repository, fixture.key)).toMatchObject({ text: "ordinary", skillNames: ["same"] });
  expect(source.getRuntimeBacking()).toEqual({ kind: "recovery", recoveryId: original.clientMutationId });
});

test("a queued recovery write captures immutable metadata before the caller can mutate it", async () => {
  const fixture = sourceFixture("writer-cutover-recovery-capture");
  setMutationStorageForTests(fixture.outbox);
  await fixture.repository.seedDraft(fixture.key, { text: "ordinary", skillNames: ["same"] });
  const original = await enqueueRecovery(fixture, "recovery original /same");
  const source = fixture.track(
    createComposerSourceState("root", { repository: fixture.repository, key: fixture.key, outbox: fixture.outbox }),
  );
  await source.ready();
  source.setActiveRecoveryId(original.clientMutationId);
  const before = await requireRow(fixture.repository, fixture.key);
  const mentions = [{ kind: "command" as const, name: "same", offset: 16 }];
  const skills = ["same"];
  const commands = ["same"];
  const write = source.queueRecoveryPersistence(
    original.clientMutationId,
    "recovery edited /same",
    [],
    skills,
    commands,
    mentions,
  );
  // Mutate the caller's arrays immediately after acceptance.
  skills.push("leaked");
  commands.push("leaked");
  mentions.push({ kind: "command", name: "leaked", offset: 99 });
  await write;
  // One revision for the recovery edit; unrelated ordinary content survives.
  expect(await requireRow(fixture.repository, fixture.key)).toMatchObject({ text: "ordinary", skillNames: ["same"] });
  expect((await requireRow(fixture.repository, fixture.key)).token.sessionRevision).toBe(
    before.token.sessionRevision + 1,
  );
  expect(source.getRuntimeBacking()).toEqual({ kind: "recovery", recoveryId: original.clientMutationId });
  const row = await fixture.outbox.getRecovery(original.clientMutationId);
  expect(row).toMatchObject({
    composerText: "recovery edited /same",
    composerMentions: [{ kind: "command", name: "same", offset: 16 }],
  });
  expect(row?.payload.input).toEqual(
    expect.arrayContaining([
      { type: "text", text: "recovery edited /same", mentions: [{ kind: "command", name: "same", offset: 16 }] },
    ]),
  );
  // No caller-array mutation leaked into the durable record.
  expect(JSON.stringify(row?.payload.input)).not.toContain("leaked");
  expect(JSON.stringify(row?.composerMentions)).not.toContain("leaked");
});

class HeldRecoveryWriterStorage extends MutationOutboxIndexedDB {
  readonly entered: Promise<void>;
  private enter: () => void = () => {};
  private releaseGate: () => void = () => {};
  private readonly gate: Promise<void>;
  private target: { id: string; action: "update" | "empty-discard" } | undefined;
  constructor(options: ConstructorParameters<typeof MutationOutboxIndexedDB>[0]) {
    super(options);
    this.entered = new Promise((resolve) => {
      this.enter = resolve;
    });
    this.gate = new Promise((resolve) => {
      this.releaseGate = resolve;
    });
  }
  arm(id: string, action: "update" | "empty-discard"): void {
    this.target = { id, action };
  }
  release(): void {
    this.target = undefined;
    this.releaseGate();
  }
  private async hold(id: string, action: "update" | "empty-discard"): Promise<void> {
    if (this.target?.id === id && this.target.action === action) {
      this.target = undefined;
      this.enter();
      await this.gate;
    }
  }
  override async updateRecoveryInput(...args: Parameters<MutationOutboxIndexedDB["updateRecoveryInput"]>) {
    await this.hold(args[0], "update");
    return super.updateRecoveryInput(...args);
  }
  override async discardRecovery(...args: Parameters<MutationOutboxIndexedDB["discardRecovery"]>) {
    await this.hold(args[0], "empty-discard");
    return super.discardRecovery(...args);
  }
}

test.each(["update", "empty-discard"] as const)(
  "a queued recovery %s refuses a stale durable token without changing either row or its local backing",
  async (action) => {
    const fixture = sourceFixture(
      `source-guarded-recovery-${action}`,
      (options) => new HeldRecoveryWriterStorage(options),
    );
    const storage = fixture.outbox as HeldRecoveryWriterStorage;
    setMutationStorageForTests(storage);
    const observer = fixture.secondRepository();
    const failures: unknown[] = [];
    const check = async (assertion: () => void | Promise<void>) => {
      try {
        await assertion();
      } catch (error) {
        failures.push(error);
      }
    };
    let write: Promise<void> | undefined;
    try {
      await fixture.repository.seedDraft(fixture.key, { text: "ordinary", skillNames: [], mentions: [] });
      const original = await enqueueRecovery(fixture, "recovery original /same");
      const source = fixture.track(
        createComposerSourceState("root", { repository: fixture.repository, key: fixture.key, outbox: storage }),
      );
      await source.ready();
      source.setActiveRecoveryId(original.clientMutationId);
      const nextText = action === "update" ? "local recovery edit" : "";
      source.updateText(nextText);
      source.updateSkillNames([]);
      source.updateSelectionMeta({ text: nextText, skillNames: [], commandNames: [], mentions: [] });
      const beforeOrdinary = await requireRow(observer, fixture.key);
      expect(beforeOrdinary.token.sessionRevision).toBe(2);
      const beforeRecovery = await storage.getRecovery(original.clientMutationId);
      expect(beforeRecovery).toBeDefined();
      const beforeLocal = source.getSnapshot();
      const beforeOutbox = await storage.listOutbox("root");
      const beforeOptimistic = await storage.listOptimistic("root");
      storage.arm(original.clientMutationId, action);
      write = trackOperation(source.queueRecoveryPersistence(original.clientMutationId, nextText, [], [], [], []));
      await storage.entered;
      const competitor = await observer.commitEdit(
        fixture.key,
        beforeOrdinary.token,
        { text: "competing ordinary", skillNames: [], mentions: [] },
        () => true,
      );
      expect(competitor.state).toBe("committed");
      if (competitor.state !== "committed") throw new Error("writer competitor did not commit");
      expect(competitor.draft.token.sessionRevision).toBe(3);
      storage.release();
      await write;
      await check(async () => expect(await observer.load(fixture.key)).toEqual(competitor.draft));
      await check(async () => expect(await storage.getRecovery(original.clientMutationId)).toEqual(beforeRecovery));
      await check(async () => expect(await storage.listOutbox("root")).toEqual(beforeOutbox));
      await check(async () => expect(await storage.listOptimistic("root")).toEqual(beforeOptimistic));
      await check(() => expect(source.getSnapshot()).toEqual(beforeLocal));
      await check(() =>
        expect(source.getRuntimeBacking()).toEqual({ kind: "recovery", recoveryId: original.clientMutationId }),
      );
    } catch (error) {
      failures.push(error);
    } finally {
      storage.release();
      await check(async () => {
        await write;
      });
    }
    if (failures.length === 1) throw failures[0];
    if (failures.length > 1) throw new AggregateError(failures, `queued recovery ${action} checkpoint failed`);
  },
);

test("queued recovery writes preserve acceptance order and advance only their own revisions", async () => {
  const fixture = sourceFixture("source-guarded-recovery-sequential");
  setMutationStorageForTests(fixture.outbox);
  await fixture.repository.seedDraft(fixture.key, { text: "ordinary", skillNames: [], mentions: [] });
  const original = await enqueueRecovery(fixture, "recovery original /same");
  const source = fixture.track(
    createComposerSourceState("root", { repository: fixture.repository, key: fixture.key, outbox: fixture.outbox }),
  );
  await source.ready();
  source.setActiveRecoveryId(original.clientMutationId);
  const before = await requireRow(fixture.repository, fixture.key);
  const writes = vi.spyOn(fixture.outbox, "updateRecoveryInput");
  source.updateText("first ordered recovery edit");
  const first = trackOperation(
    source.queueRecoveryPersistence(original.clientMutationId, "first ordered recovery edit", [], [], [], []),
  );
  source.updateText("second ordered recovery edit");
  const second = trackOperation(
    source.queueRecoveryPersistence(original.clientMutationId, "second ordered recovery edit", [], [], [], []),
  );
  await Promise.all([first, second]);
  expect(writes.mock.calls.map((call) => call[3])).toEqual([
    "first ordered recovery edit",
    "second ordered recovery edit",
  ]);
  const after = await requireRow(fixture.repository, fixture.key);
  expect(after).toEqual({
    ...before,
    token: { ...before.token, sessionRevision: before.token.sessionRevision + 2 },
  });
  expect(await fixture.outbox.getRecovery(original.clientMutationId)).toMatchObject({
    composerText: "second ordered recovery edit",
    composerMentions: [],
    payload: { input: [{ type: "text", text: "second ordered recovery edit" }] },
  });
  expect(source.getRuntimeBacking()).toEqual({ kind: "recovery", recoveryId: original.clientMutationId });
  expect(await fixture.outbox.listOutbox("root")).toEqual([]);
  expect(await fixture.outbox.listOptimistic("root")).toEqual([]);
});

test("submissionDraft drains an accepted recovery write before capturing its guard", async () => {
  const fixture = sourceFixture("writer-cutover-recovery-drain");
  setMutationStorageForTests(fixture.outbox);
  await fixture.repository.seedDraft(fixture.key, { text: "ordinary", skillNames: ["same"] });
  const original = await enqueueRecovery(fixture, "recovery original /same");
  const source = fixture.track(
    createComposerSourceState("root", { repository: fixture.repository, key: fixture.key, outbox: fixture.outbox }),
  );
  await source.ready();
  source.setActiveRecoveryId(original.clientMutationId);
  // Hold the real recovery write's transaction so the boundary is deterministic.
  const held = holdNextWriteTransaction(["recovery", "composerDrafts", "annotationNamespaces"]);
  const write = source.queueRecoveryPersistence(
    original.clientMutationId,
    "recovery edited /same",
    [],
    [],
    ["same"],
    [],
  );
  let producerFulfilled = false;
  const writeJoin = write.then(() => {
    producerFulfilled = true;
  });
  // Record the producer's fulfillment state at the exact instant the consumer
  // (submissionDraft) returns its guard.
  let producerAtConsumerReturn: boolean | undefined;
  const submittedP = source.submissionDraft().then((submitted) => {
    producerAtConsumerReturn = producerFulfilled;
    return submitted;
  });
  try {
    await held.reached;
    // Let an early consumer resolution record its flag WHILE the producer is
    // still held; a consumer that drains correctly stays pending here.
    for (let i = 0; i < 200 && producerAtConsumerReturn === undefined; i += 1) await Promise.resolve();
    held.release();
    const submitted = await submittedP;
    await writeJoin;
    // The accepted recovery write must already have fulfilled when the guard was
    // captured, not merely join later.
    expect(producerAtConsumerReturn).toBe(true);
    expect(await fixture.outbox.getRecovery(original.clientMutationId)).toMatchObject({
      composerText: "recovery edited /same",
    });
    expect(submitted.settlementGuard?.token).toEqual((await requireRow(fixture.repository, fixture.key)).token);
  } finally {
    held.release();
    await Promise.allSettled([writeJoin, submittedP]);
  }
});

test("overlapping scheduled and ready drains commit each accepted edit exactly once in order", async () => {
  const fixture = sourceFixture("writer-cutover-hydration-drain");
  await fixture.repository.seedDraft(fixture.key, { text: "seed", skillNames: [] });
  const source = fixture.track(
    createComposerSourceState("root", { repository: fixture.repository, key: fixture.key, outbox: fixture.outbox }),
  );
  // Two distinct full values queued BEFORE the first ready call, the second
  // carrying a meaningful empty mention set.
  source.acceptEdit({ text: "first", skillNames: [], mentions: [] });
  source.acceptEdit({ text: "second", skillNames: [], commandNames: [], mentions: [] });
  const held = holdNextCommit(fixture.repository);
  const readyRun = source.ready();
  try {
    // The scheduled drain from acceptEdit starts first and is held AFTER its
    // real commit completes; `ready` then runs its own direct drain over the
    // same still-queued entries.
    await held.reached;
    held.release();
    await readyRun;
    await source.whenSettled();
    const row = await requireRow(fixture.repository, fixture.key);
    expect(row).toMatchObject({ text: "second", skillNames: [] });
    expect(row.commandNames).toEqual([]);
    expect(row.mentions).toEqual([]);
    // seed (rev 1) + exactly two accepted edits.
    expect(row.token.sessionRevision).toBe(3);
    // Close and reopen the real store: the last accepted value survives.
    fixture.repository.close();
    const reopened = createBrowserDraftRepository({ indexedDB: fixture.factory, databaseName: fixture.databaseName });
    try {
      const reopenedRow = await requireRow(reopened, fixture.key);
      expect(reopenedRow).toMatchObject({ text: "second", skillNames: [], commandNames: [], mentions: [] });
      expect(reopenedRow.token.sessionRevision).toBe(3);
    } finally {
      reopened.close();
    }
  } finally {
    held.release();
    await Promise.allSettled([readyRun, source.whenSettled()]);
    held.restore();
  }
});

// Direct projection lifetime coverage: an older same-ref source's disposal must
// not erase a newer source's projection, and the final disposal must remove it.
test("a retiring same-ref source preserves a newer source's runtime projection", async () => {
  const fixture = sourceFixture("projection-lifetime-owner");
  const older = fixture.track(
    createComposerSourceState("root", { repository: fixture.repository, key: fixture.key, outbox: fixture.outbox }),
  );
  const newer = fixture.track(
    createComposerSourceState("root", { repository: fixture.repository, key: fixture.key, outbox: fixture.outbox }),
  );
  await Promise.all([older.ready(), newer.ready()]);
  const value: SkillEditorValue = { text: "newer projection", skillNames: ["same"], mentions: [] };
  newer.acceptEdit(value);
  await newer.whenSettled();
  expect(readComposerDraft("root")).toMatchObject(value);
  // Both sources called ready and may have published; the older source's
  // disposal must not clear the newer source's projection.
  older.dispose();
  expect(readComposerDraft("root")).toMatchObject(value);
  // The final owner retires: the projection is gone, so the read falls back to
  // the (empty) legacy/lookup path.
  newer.dispose();
  expect(readComposerDraft("root")).toEqual({ text: "", skillNames: [] });
});

// A recovery selected while ordinary hydration's read is still pending must not
// be overwritten by that hydration: `setActiveRecoveryId` changes the backing
// WITHOUT an edit, so it is invisible to the hydration generation/queue guard.
test("an active recovery selected during ordinary hydration survives its late publication", async () => {
  const fixture = sourceFixture("recovery-during-hydration");
  const key = fixture.key;
  await fixture.repository.seedDraft(key, { text: "ordinary row", skillNames: [] });
  const hold = holdNextWriteTransaction(["composerDrafts", "annotationNamespaces"]);
  const source = fixture.track(
    createComposerSourceState("root", { repository: fixture.repository, key, outbox: fixture.outbox }),
  );
  const ready = source.ready();
  try {
    await hold.reached;
    // The recovery restore path Composer uses (setActiveRecoveryId + raw value
    // updates) during the pending ordinary read.
    source.setActiveRecoveryId("recovery-1");
    source.updateText("recover this later");
    source.updateSkillNames([]);
    source.updateSelectionMeta({ text: "recover this later", skillNames: [] });
    expect(source.getSnapshot()).toMatchObject({ text: "recover this later", activeRecoveryId: "recovery-1" });
    expect(source.getRuntimeBacking()).toEqual({ kind: "recovery", recoveryId: "recovery-1" });
    hold.release();
    await ready;
    await source.whenSettled();
    // Both the recovery backing and its full visible value survive the late
    // ordinary publication.
    expect(source.getSnapshot()).toMatchObject({ text: "recover this later", activeRecoveryId: "recovery-1" });
    expect(source.getRuntimeBacking()).toEqual({ kind: "recovery", recoveryId: "recovery-1" });
    // The ordinary durable row is untouched.
    expect(await requireRow(fixture.repository, key)).toMatchObject({ text: "ordinary row", skillNames: [] });
  } finally {
    hold.release();
  }
});

// Recovery preservation: representative failed-write and real committed-broadcast
// consumer paths. Existing source/storage fixtures remain unchanged.
test.each(["aborted", "refused"] as const)(
  "recovery preservation: preparation retains an unsaved %s complete edit",
  async (outcome) => {
    let abortUpdates = false;
    let abortCount = 0;
    const fixture = sourceFixture(
      `recovery-preservation-prepare-${outcome}`,
      (options) =>
        new HeldRecoveryWriterStorage({
          ...options,
          beforeCommit(operation) {
            if (abortUpdates && operation === "updateRecoveryInput") {
              abortCount += 1;
              throw new Error("recovery update aborted");
            }
          },
        }),
    );
    const storage = fixture.outbox as HeldRecoveryWriterStorage;
    const observer = fixture.secondRepository();
    const recoveryObserver = new MutationOutboxIndexedDB({
      indexedDB: fixture.factory,
      databaseName: fixture.databaseName,
    });
    sourceCleanups.push(() => recoveryObserver.close());
    setMutationStorageForTests(storage);
    const rpc = vi.spyOn(AppwireClient.prototype, "request");
    const perform = vi.spyOn(storage, "resendRecovery");
    let writeJoin: Promise<unknown> | undefined;
    try {
      // Native structuredClone in fake IndexedDB cannot preserve jsdom Blob bytes.
      const { Blob: NodeBlob } = await vi.importActual<{ Blob: typeof Blob }>("node:buffer");
      vi.stubGlobal("Blob", NodeBlob);
      await fixture.repository.seedDraft(fixture.key, { text: "ordinary", skillNames: ["old"], mentions: [] });
      const original = await enqueueRecovery(fixture, "recovery original /same");
      const source = fixture.track(
        createComposerSourceState("root", { repository: fixture.repository, key: fixture.key, outbox: storage }),
      );
      await source.ready();
      source.setActiveRecoveryId(original.clientMutationId);
      expect(await source.prepareAnnotation()).not.toBeNull();
      expect(source.getSnapshot().mentions).toBeUndefined();
      const editor = fakeEditor({ text: "recovery original /same", cursor: 5, selection: { start: 2, end: 5 } });
      sourceCleanups.push(source.bindEditor(editor));
      const attachment = {
        marker: 1,
        name: "retained.png",
        mediaType: "image/png",
        data: "AQID",
        pending: false,
      } satisfies PendingAttachment;
      const newer: SkillEditorValue = {
        text: "🙂 /review /ship [image 1]",
        skillNames: ["review"],
        commandNames: ["ship"],
        mentions: [
          { kind: "skill", name: "review", offset: 3 },
          { kind: "command", name: "ship", offset: 11 },
        ],
      };
      source.attachments.setState({ items: [attachment] });
      source.updateSkillNames(["review"]);
      source.updateSelectionMeta(newer);
      source.editText(newer.text);
      editor.simulateTyping(newer.text, 11, 3, 11);
      const token = source.getDraftToken();
      const generation = source.getLocalGeneration();
      const backing = source.getRuntimeBacking();
      const before = await requireRow(observer, fixture.key);
      expect(durableDraft(before)).toEqual({ text: "ordinary", skillNames: ["old"], mentions: [] });
      expect(before.token.sessionRevision).toBe(2);
      expect(backing).toEqual({ kind: "recovery", recoveryId: original.clientMutationId });
      expect(generation).toBe(1);
      const oldRecovery = await recoveryObserver.getRecovery(original.clientMutationId);
      expect(oldRecovery).toMatchObject({
        composerText: "recovery original /same",
        payload: {
          input: [
            { type: "text", text: "recovery original /same" },
            { type: "command", name: "same" },
          ],
        },
        attachments: [],
      });
      expect(oldRecovery?.composerMentions).toBeUndefined();
      expect(token).toEqual(before.token);
      const updates = vi.spyOn(storage, "updateRecoveryInput");
      if (outcome === "aborted") abortUpdates = true;
      else storage.arm(original.clientMutationId, "update");
      const write = source.queueRecoveryPersistence(
        original.clientMutationId,
        newer.text,
        [attachment],
        newer.skillNames,
        newer.commandNames,
        newer.mentions,
      );
      writeJoin = trackOperation(
        outcome === "aborted" ? expect(write).rejects.toThrow("recovery update aborted") : write,
      );
      let expectedOrdinary = before;
      if (outcome === "refused") {
        await storage.entered;
        const competitor = await observer.commitEdit(
          fixture.key,
          before.token,
          { text: "competing ordinary", skillNames: ["other"], commandNames: ["other"], mentions: [] },
          () => true,
        );
        expect(competitor.state).toBe("committed");
        if (competitor.state !== "committed") throw new Error("expected real competing commit");
        expectedOrdinary = competitor.draft;
        storage.release();
      }
      await writeJoin;
      expect(updates).toHaveBeenCalledTimes(1);
      expect(updates.mock.calls[0]?.[1]).toEqual([
        { type: "text", text: "🙂 /review /ship [image 1]", mentions: newer.mentions },
        { type: "image", mediaType: "image/png", data: "AQID", name: "retained.png" },
        { type: "skill", name: "review" },
        { type: "command", name: "ship" },
      ]);
      if (outcome === "refused") expect(await updates.mock.results[0]?.value).toBeUndefined();
      expect(abortCount).toBe(outcome === "aborted" ? 1 : 0);
      expect(getToasts().map(({ kind, text }) => ({ kind, text }))).toEqual(
        outcome === "aborted"
          ? [{ kind: "error", text: "Couldn't save recovered message: recovery update aborted" }]
          : [],
      );
      expect(source.getSnapshot()).toMatchObject(newer);
      expect(source.attachments.getState().items[0]).toBe(attachment);
      expect(await observer.load(fixture.key)).toEqual(expectedOrdinary);
      expect(await recoveryObserver.getRecovery(original.clientMutationId)).toEqual(oldRecovery);

      const preparation = await source.prepareAnnotation();
      // Soft observations expose every overwritten field before the final null
      // oracle stops RED. The healthy tail below is deliberately not forced.
      expect.soft(source.getSnapshot()).toMatchObject({ ...newer, activeRecoveryId: original.clientMutationId });
      expect.soft(source.attachments.getState().items).toHaveLength(1);
      expect.soft(source.attachments.getState().items[0]).toBe(attachment);
      expect.soft(source.getRuntimeBacking()).toEqual(backing);
      expect.soft(source.getLocalGeneration()).toBe(generation);
      expect.soft(source.getDraftToken()).toEqual(token);
      expect.soft(editor.held()).toEqual({ text: newer.text, cursor: 11, selection: { start: 3, end: 11 } });
      expect.soft(await observer.load(fixture.key)).toEqual(expectedOrdinary);
      expect.soft(await recoveryObserver.getRecovery(original.clientMutationId)).toEqual(oldRecovery);
      expect.soft(await recoveryObserver.listOutbox("root")).toEqual([]);
      expect.soft(await recoveryObserver.listOptimistic("root")).toEqual([]);
      expect.soft(perform).not.toHaveBeenCalled();
      expect.soft(rpc).not.toHaveBeenCalled();
      expect(preparation).toBeNull();

      if (outcome === "aborted") {
        abortUpdates = false;
        // Same source, same still-visible accepted value, public retry. No new
        // source/capture or test-side token refresh makes the old edit succeed.
        const retained = source.getSnapshot();
        await trackOperation(
          source.queueRecoveryPersistence(
            original.clientMutationId,
            retained.text,
            [attachment],
            retained.skillNames,
            retained.commandNames,
            retained.mentions,
          ),
        );
        expect(await source.prepareAnnotation()).not.toBeNull();
        expect(source.getSnapshot()).toMatchObject(newer);
        expect(source.attachments.getState().items[0]).toBe(attachment);
        expect(source.getLocalGeneration()).toBe(generation);
        expect(source.getRuntimeBacking()).toEqual(backing);
        const after = await requireRow(observer, fixture.key);
        expect(after).toEqual({
          ...before,
          token: { ...before.token, sessionRevision: before.token.sessionRevision + 1 },
        });
        expect(source.getDraftToken()).toEqual(after.token);
        const saved = await recoveryObserver.getRecovery(original.clientMutationId);
        expect(saved).toMatchObject({
          composerText: "🙂 /review /ship [image 1]",
          composerMentions: [
            { kind: "skill", name: "review", offset: 3 },
            { kind: "command", name: "ship", offset: 11 },
          ],
          payload: {
            input: [
              { type: "text", text: "🙂 /review /ship [image 1]", mentions: newer.mentions },
              { type: "image", mediaType: "image/png", data: "AQID", name: "retained.png" },
              { type: "skill", name: "review" },
              { type: "command", name: "ship" },
            ],
          },
        });
        expect(saved?.attachments).toHaveLength(1);
        const savedAttachment = saved?.attachments[0];
        expect(savedAttachment).toMatchObject({ marker: 1, name: "retained.png", mediaType: "image/png" });
        if (!savedAttachment) throw new Error("expected healthy retry attachment");
        expect(new Uint8Array(await savedAttachment.blob.arrayBuffer())).toEqual(new Uint8Array([1, 2, 3]));
        expect(await recoveryObserver.listOutbox("root")).toEqual([]);
        expect(await recoveryObserver.listOptimistic("root")).toEqual([]);
        expect(perform).not.toHaveBeenCalled();
        expect(rpc).not.toHaveBeenCalled();
      }
    } finally {
      abortUpdates = false;
      storage.release();
      try {
        await writeJoin;
      } finally {
        vi.unstubAllGlobals();
      }
    }
  },
);

test.each(["sender mention edit", "sender replacement attachment", "other source replacement attachment"] as const)(
  "recovery preservation: accepted broadcast retains a later %s",
  async (change) => {
    const { fixture, source, original } = await arrangeSelectedRecovery(`recovery-preservation-broadcast-${change}`);
    const observer = fixture.secondRepository();
    const recoveryObserver = new MutationOutboxIndexedDB({
      indexedDB: fixture.factory,
      databaseName: fixture.databaseName,
    });
    sourceCleanups.push(() => recoveryObserver.close());
    const value: SkillEditorValue = {
      text: "recovery edited /same",
      skillNames: [],
      commandNames: ["same"],
      mentions: [{ kind: "command", name: "same", offset: 16 }],
    };
    const senderEditor = fakeEditor({ text: value.text, cursor: 7, selection: { start: 2, end: 7 } });
    sourceCleanups.push(source.bindEditor(senderEditor));
    const submittedAttachment = {
      marker: 1,
      name: "submitted.png",
      mediaType: "image/png",
      data: "AQID",
      pending: false,
    } satisfies PendingAttachment;
    source.attachments.setState({ items: [submittedAttachment] });
    await trackOperation(
      source.queueRecoveryPersistence(
        original.clientMutationId,
        value.text,
        [submittedAttachment],
        [],
        ["same"],
        value.mentions,
      ),
    );
    const other = fixture.track(
      createComposerSourceState("root", { repository: observer, key: fixture.key, outbox: fixture.outbox }),
    );
    await other.ready();
    other.setActiveRecoveryId(original.clientMutationId);
    expect(await other.prepareAnnotation()).not.toBeNull();
    const otherEditor = fakeEditor({ text: value.text, cursor: 9, selection: { start: 4, end: 9 } });
    sourceCleanups.push(other.bindEditor(otherEditor));
    other.attachments.setState({ items: [submittedAttachment] });
    expect(other.sourceId).not.toBe(source.sourceId);
    const retained = change === "other source replacement attachment" ? other : source;
    const editor = change === "other source replacement attachment" ? otherEditor : senderEditor;
    const originalToken = retained.getDraftToken();
    const originalBacking = retained.getRuntimeBacking();
    const before = await requireRow(observer, fixture.key);
    expect(durableDraft(before)).toEqual({ text: "ordinary", skillNames: ["same"], commandNames: ["same"] });
    expect(before.token.sessionRevision).toBe(4);
    expect(originalToken).toEqual(before.token);
    expect(originalBacking).toEqual({ kind: "recovery", recoveryId: original.clientMutationId });
    const recovery = await recoveryObserver.getRecovery(original.clientMutationId);
    if (!recovery) throw new Error("expected selected recovery before actual resend");
    const input = [
      {
        type: "text" as const,
        text: "recovery edited /same",
        mentions: [{ kind: "command" as const, name: "same", offset: 16 }],
      },
      { type: "image" as const, mediaType: "image/png", data: "AQID", name: "submitted.png" },
      { type: "command" as const, name: "same" },
    ];
    expect(recovery.payload.input).toEqual(input);
    const intent = {
      targetRef: "root",
      method: "turn/start",
      payload: { ref: "root", input },
      attachments: recovery.attachments,
      optimisticDisplay: { method: "turn/start", input },
      composerText: value.text,
      composerMentions: value.mentions,
    };
    const failure = vi.fn();
    const committed = vi.fn();
    sourceCleanups.push(subscribeComposerSubmissionCommitted(committed));
    const rpc = vi.spyOn(AppwireClient.prototype, "request");
    let held: ReturnType<typeof holdNextRead> | undefined;
    let announceRead: (read: ReturnType<typeof holdNextRead>) => void = () => {};
    const finalRead = new Promise<ReturnType<typeof holdNextRead>>((resolve) => {
      announceRead = resolve;
    });
    let acceptedId: string | undefined;
    let captured: SubmittedDraft | undefined;
    const perform = vi.fn(async (submitted: SubmittedDraft) => {
      captured = submitted;
      const resent = await fixture.outbox.resendRecovery(
        original.clientMutationId,
        intent,
        source.recoveryWriteGuard(submitted),
      );
      if (!resent) throw new Error("expected real guarded resend acceptance");
      acceptedId = resent.clientMutationId;
      // Install only AFTER acceptance, so neither submission capture nor the
      // resend's durable guard read is held instead of the final settlement read.
      held = holdNextRead(fixture.repository);
      announceRead(held);
      return resent;
    });
    const submitting = trackOperation(
      submitWithPendingTracking(
        {
          ref: "root",
          source,
          text: value.text,
          skillNames: [],
          commandNames: ["same"],
          mentions: value.mentions,
          attachments: [submittedAttachment],
          recoveryId: original.clientMutationId,
          onFailure: failure,
        },
        perform,
      ),
    );
    // An unexpected early end fails promptly instead of waiting on an event
    // that a failed/refused perform could never produce.
    const prematureEnd = submitting.then(() => {
      throw new Error("submission ended before its held final read");
    });
    try {
      held = await Promise.race([finalRead, prematureEnd]);
      await Promise.race([held.reached, prematureEnd]);
      expect(perform).toHaveBeenCalledTimes(1);
      expect(captured).toMatchObject(value);
      const capturedGuard = captured?.settlementGuard && {
        ...captured.settlementGuard,
        token: { ...captured.settlementGuard.token },
        backing: { ...captured.settlementGuard.backing },
      };
      const accepted = await requireRow(observer, fixture.key);
      expect(accepted).toEqual({
        ...before,
        token: { ...before.token, sessionRevision: before.token.sessionRevision + 1 },
      });
      expect(await recoveryObserver.getRecovery(original.clientMutationId)).toBeUndefined();
      expect(await recoveryObserver.listOutbox("root")).toMatchObject([
        { clientMutationId: acceptedId, state: "submitting", payload: { input } },
      ]);
      let liveAttachment = submittedAttachment;
      let localValue = value;
      if (change === "sender mention edit") {
        localValue = { text: "recovery edited /same", skillNames: [], commandNames: ["same"], mentions: [] };
        source.updateSelectionMeta(localValue);
        source.editText(value.text);
        editor.simulateTyping(value.text, 12, 6, 12);
      } else {
        liveAttachment = { ...submittedAttachment };
        expect(liveAttachment).toEqual(submittedAttachment);
        expect(liveAttachment).not.toBe(submittedAttachment);
        retained.attachments.setState({ items: [liveAttachment] });
        editor.simulateTyping(value.text, 13, 5, 13);
      }
      const generation = retained.getLocalGeneration();
      const cursor = editor.held();
      expect(retained.getSnapshot()).toMatchObject(localValue);
      expect(retained.attachments.getState().items[0]).toBe(liveAttachment);
      held.release();
      await submitting;
      await Promise.all([source.whenSettled(), other.whenSettled()]);
      expect.soft(failure).not.toHaveBeenCalled();
      expect.soft(perform).toHaveBeenCalledTimes(1);
      expect.soft(committed).toHaveBeenCalledTimes(1);
      expect.soft(committed.mock.calls[0]?.[5]).toMatchObject({
        submitted: value,
        settlement: { persistence: "committed" },
      });
      expect.soft(committed.mock.calls[0]?.[5]?.submitted).toBe(captured);
      expect.soft(captured?.settlementGuard).toEqual(capturedGuard);
      expect.soft(retained.getSnapshot()).toMatchObject({ ...localValue, activeRecoveryId: original.clientMutationId });
      expect.soft(retained.attachments.getState().items).toHaveLength(1);
      expect.soft(retained.attachments.getState().items[0]).toBe(liveAttachment);
      expect.soft(retained.getRuntimeBacking()).toEqual(originalBacking);
      expect.soft(retained.getDraftToken()).toEqual(originalToken);
      expect.soft(retained.getLocalGeneration()).toBe(generation);
      expect.soft(editor.held()).toEqual(cursor);
      expect.soft(await observer.load(fixture.key)).toEqual(accepted);
      expect.soft(await recoveryObserver.getRecovery(original.clientMutationId)).toBeUndefined();
      expect
        .soft(await recoveryObserver.listOutbox("root"))
        .toMatchObject([{ clientMutationId: acceptedId, state: "submitting", payload: { input } }]);
      expect.soft(rpc).not.toHaveBeenCalled();
      expect.soft(getToasts()).toEqual([]);
    } finally {
      held?.release();
      await submitting;
    }
  },
);

// Contract 1 (sender identity): two same-ref sources can both reach the SAME
// local generation, because each counter starts at zero. A committed veto
// broadcast by the first source must not clear the second source's own live,
// identical payload or its shared projection - only the ORIGINAL sender's own
// instance may retire its local value.
test("an equal-generation competing same-ref source survives the first source's committed veto broadcast", async () => {
  const fixture = sourceFixture("sender-identity-equal-generation");
  const key = fixture.key;
  const value: SkillEditorValue = { text: "same value", skillNames: [] };
  const secondRepository = fixture.secondRepository();
  const first = fixture.track(
    createComposerSourceState("root", { repository: fixture.repository, key, outbox: fixture.outbox }),
  );
  const second = fixture.track(
    createComposerSourceState("root", { repository: secondRepository, key, outbox: fixture.outbox }),
  );
  await Promise.all([first.ready(), second.ready()]);

  // Each source accepts the identical visible value exactly once, so both local
  // counters reach generation 1 - the coincidence the guard must not treat as
  // identity.
  first.acceptEdit(value);
  await first.whenSettled();

  // The production notification path (the broadcaster Composer uses). The second
  // source accepts its own identical value while the first source's submission
  // is in flight, advancing the shared revision so the first source's durable
  // settlement is a committed veto.
  await submitWithPendingTracking(
    { ref: "root", text: value.text, skillNames: value.skillNames, source: first, onFailure: () => {} },
    async () => {
      second.acceptEdit(value);
      await second.whenSettled();
    },
  );
  await first.whenSettled();
  await second.whenSettled();

  // The second source owns the newer value and must keep it, locally and in the
  // shared runtime projection.
  expect(second.getSnapshot()).toMatchObject(value);
  expect(readComposerDraft("root")).toMatchObject(value);
  // ...and the durable row (independent adapter) still holds it.
  expect(await requireRow(secondRepository, key)).toMatchObject(value);
});

// Contract 2 (full value): the listener compares text/skills/commands but omits
// mentions, so a later same-text metadata-only edit cannot be told apart from
// the original capture. B's mentions change makes its full value different, so
// partial equality must not justify clearing it.
test("a later mentions-only same-ref edit is not erased by the first source's committed veto broadcast", async () => {
  const fixture = sourceFixture("sender-identity-mentions");
  const key = fixture.key;
  const original: SkillEditorValue = {
    text: "/same",
    skillNames: [],
    commandNames: ["same"],
    mentions: [{ kind: "command", name: "same", offset: 0 }],
  };
  const edited: SkillEditorValue = {
    text: "/same",
    skillNames: [],
    commandNames: ["same"],
    mentions: [],
  };
  const secondRepository = fixture.secondRepository();
  const first = fixture.track(
    createComposerSourceState("root", { repository: fixture.repository, key, outbox: fixture.outbox }),
  );
  const second = fixture.track(
    createComposerSourceState("root", { repository: secondRepository, key, outbox: fixture.outbox }),
  );
  await Promise.all([first.ready(), second.ready()]);

  first.acceptEdit(original);
  await first.whenSettled();

  await submitWithPendingTracking(
    {
      ref: "root",
      text: original.text,
      skillNames: original.skillNames,
      commandNames: original.commandNames,
      mentions: original.mentions,
      source: first,
      onFailure: () => {},
    },
    async () => {
      second.acceptEdit(edited);
      await second.whenSettled();
    },
  );
  await first.whenSettled();
  await second.whenSettled();

  expect(second.getSnapshot()).toMatchObject(edited);
  expect(readComposerDraft("root")).toMatchObject(edited);
  expect(await requireRow(secondRepository, key)).toMatchObject(edited);
});

// Contract 2b (full value, isolated on the SAME sender): the sender's captured
// value OMITS mentions, then the same source sets an EXPLICIT empty mention set
// through the supported updateSelectionMeta (which does not increment its local
// generation), and a real committed veto is produced by a competing same-ref
// commit. Sender identity and generation/backing stay satisfied, so only the
// missing full-value (mentions) comparison can explain a wrong clear.
test("a same-sender committed veto does not erase an explicit empty mention set over an absent capture", async () => {
  const fixture = sourceFixture("sender-identity-full-value-isolated");
  const key = fixture.key;
  const secondRepository = fixture.secondRepository();
  const sender = fixture.track(
    createComposerSourceState("root", { repository: fixture.repository, key, outbox: fixture.outbox }),
  );
  const competitor = fixture.track(
    createComposerSourceState("root", { repository: secondRepository, key, outbox: fixture.outbox }),
  );
  await Promise.all([sender.ready(), competitor.ready()]);

  // The sender accepts a value whose mentions are ABSENT.
  sender.acceptEdit({ text: "/same", skillNames: [], commandNames: ["same"] });
  await sender.whenSettled();
  expect(sender.getSnapshot().mentions).toBeUndefined();
  // The exact capture the submission will use omits the mentions property.
  const captured = await sender.submissionDraft();
  expect(captured.text).toBe("/same");
  expect(Object.hasOwn(captured, "mentions")).toBe(false);

  // The real notification path. Before the durable settlement, the SAME sender
  // sets an EXPLICIT empty mention set (no generation bump) and a competing
  // same-ref source commits, advancing the shared revision so the sender's
  // settlement is a committed veto.
  await submitWithPendingTracking(
    { ref: "root", text: "/same", skillNames: [], commandNames: ["same"], source: sender, onFailure: () => {} },
    async () => {
      sender.updateSelectionMeta({ text: "/same", skillNames: [], commandNames: ["same"], mentions: [] });
      competitor.acceptEdit({ text: "/same", skillNames: [], commandNames: ["same"], mentions: [] });
      await competitor.whenSettled();
    },
  );
  await sender.whenSettled();
  await competitor.whenSettled();

  // Text/skills/commands are equal, so they cannot explain preservation. Only
  // the metadata presence differs: the sender's explicit [] must survive.
  expect(sender.getSnapshot()).toMatchObject({ text: "/same", skillNames: [], commandNames: ["same"] });
  expect(sender.getSnapshot().mentions).toEqual([]);
});

// Shared Unit-B post-clear race wiring, used unchanged by the main race and its
// early-failure control. It owns a real repository whose beforeCommit hook (once
// armed) installs the next-transaction hold, the injected source/outbox, the
// production submission, and the accepted closeSourceFixture registration. The
// failure-safe finish disarms future arming FIRST, releases any existing hold,
// then JOINS the whole submission before the accepted teardown drains, disposes
// and closes - each step attempted independently and every failure retained.
interface SenderRaceWiring {
  factory: IDBFactory;
  databaseName: string;
  key: ReturnType<typeof browserDraftKey>;
  source: ComposerSourceState;
  repository: BrowserDraftRepository;
  outbox: MutationOutboxIndexedDB;
  armDeferredHold(): void;
  armEnabled(): boolean;
  currentHold(): ReturnType<typeof holdNextWriteTransaction> | undefined;
  disarm(): void;
  disarmOrder(): string[];
  startSubmission(text: string, skillNames: readonly string[]): Promise<void>;
}

function createSenderRaceWiring(databaseName: string): SenderRaceWiring {
  const factory = new IDBFactory();
  const key = browserDraftKey("root");
  let armLoadHold: (() => void) | undefined;
  let loadHold: ReturnType<typeof holdNextWriteTransaction> | undefined;
  let armed = false;
  const disarmLog: string[] = [];
  const repository = createBrowserDraftRepository({
    indexedDB: factory,
    databaseName,
    beforeCommit: (operation) => {
      if (operation === "clearSubmitted") armLoadHold?.();
    },
  });
  const outbox = new MutationOutboxIndexedDB({ indexedDB: factory, databaseName });
  setMutationStorageForTests(outbox);
  const source = createComposerSourceState("root", { repository, key, outbox });
  let disarmed = false;
  const disarm = () => {
    // Disable future arming BEFORE releasing any existing hold, so a deferred
    // beforeCommit can never install a new hold during cleanup. The recorded
    // order is written once, so a repeated (fallback) cleanup is idempotent.
    armLoadHold = undefined;
    if (disarmed) {
      loadHold?.release();
      return;
    }
    disarmed = true;
    disarmLog.push("arm-disabled");
    loadHold?.release();
    disarmLog.push("hold-released");
  };
  // Register the accepted fallback cleanup BEFORE the first await on the created
  // resources, so an early failure still disarms, joins, drains, disposes and
  // closes. closeSourceFixture is safe to run again after an explicit run.
  sourceCleanups.push(() => closeSourceFixture({ sources: [source], outbox, repositories: [repository], disarm }));
  return {
    factory,
    databaseName,
    key,
    source,
    repository,
    outbox,
    armDeferredHold: () => {
      armed = true;
      armLoadHold = () => {
        loadHold ??= holdNextWriteTransaction(["composerDrafts", "annotationNamespaces"]);
      };
    },
    armEnabled: () => armed,
    currentHold: () => loadHold,
    disarm,
    disarmOrder: () => [...disarmLog],
    startSubmission: (text, skillNames) =>
      trackOperation(
        submitWithPendingTracking({ ref: "root", text, skillNames, source, onFailure: () => {} }, async () => {}),
      ),
  };
}

// The single failure-safe finish both the main race and its control route
// through: run the body, disarm first, JOIN the whole submission, then let the
// caller's verify run, retaining every failure.
async function finishSenderRace(input: {
  wiring: SenderRaceWiring;
  operation: Promise<void>;
  body: () => Promise<void>;
  verify: () => void | Promise<void>;
}): Promise<void> {
  const failures: unknown[] = [];
  try {
    await input.body();
  } catch (error) {
    failures.push(error);
  }
  try {
    input.wiring.disarm();
  } catch (error) {
    failures.push(error);
  }
  try {
    await input.operation;
  } catch (error) {
    failures.push(error);
  }
  try {
    await input.wiring.source.whenSettled();
    await input.verify();
  } catch (error) {
    failures.push(error);
  }
  if (failures.length === 1) throw failures[0];
  if (failures.length > 1) throw new AggregateError(failures, "sender race checkpoint cleanup failed");
}

// Contract 3 (post-clear await race): the durable clear commits, settleDraft
// awaits the real post-clear read, and a same-text local edit lands in that
// window advancing only the local generation (the ordinary shared revision does
// not move on a same-text edit). settleDraft's local re-check preserves the
// edit; the later committed notification must not erase it.
test("a same-text local edit landing during the post-clear read survives the committed completion", async () => {
  const wiring = createSenderRaceWiring("sender-identity-post-clear");
  await wiring.source.ready();
  const value: SkillEditorValue = { text: "same value", skillNames: [] };
  wiring.source.acceptEdit(value);
  await wiring.source.whenSettled();
  expect(await requireRow(wiring.repository, wiring.key)).toMatchObject(value);

  wiring.armDeferredHold();
  const operation = wiring.startSubmission(value.text, value.skillNames);
  await finishSenderRace({
    wiring,
    operation,
    body: async () => {
      await vi.waitFor(() => expect(wiring.currentHold()).toBeDefined());
      await wiring.currentHold()?.reached;
      // The durable clear has committed; the source is awaiting the real
      // post-clear read. A same-text local edit advances only the local
      // generation.
      wiring.source.editText(value.text);
    },
    verify: async () => {
      // The local re-check preserved the edit, and the committed notification
      // must not erase it from the source or the shared projection.
      expect(wiring.source.getSnapshot()).toMatchObject(value);
      expect(readComposerDraft("root")).toMatchObject(value);
      // The committed clear is a real durable consequence: a FRESH reopen after
      // completion reads the empty row.
      const reopened = createBrowserDraftRepository({
        indexedDB: wiring.factory,
        databaseName: wiring.databaseName,
      });
      try {
        expect(await reopened.load(wiring.key)).toMatchObject({ text: "", skillNames: [] });
      } finally {
        reopened.close();
      }
    },
  });
});

// Bounded early-failure controls for the SHARED race wiring: a real deferred
// arming and, for the second phase, a real completed-load hold. The injected
// body failure routes through the same finishSenderRace path as the main race,
// and the control observes the real submission fulfilling BEFORE the first
// disposal/close boundary and future arming disabled before release. The
// control itself passes (a RED-neutral observer of the wiring).
test.each([{ phase: "before the deferred arm" }, { phase: "after the real hold is reached" }])(
  "the shared post-clear race wiring disarms, joins and closes independently ($phase)",
  async ({ phase }) => {
    const wiring = createSenderRaceWiring(`sender-identity-cleanup-control-${phase.replace(/\s+/g, "-")}`);
    await wiring.source.ready();
    wiring.source.acceptEdit({ text: "same value", skillNames: [] });
    await wiring.source.whenSettled();

    let fulfilled = false;
    const boundaries: string[] = [];
    const realDispose = wiring.source.dispose.bind(wiring.source);
    vi.spyOn(wiring.source, "dispose").mockImplementation(() => {
      boundaries.push(`dispose:${fulfilled ? "fulfilled" : "pending"}`);
      realDispose();
    });
    const realOutboxClose = wiring.outbox.close.bind(wiring.outbox);
    vi.spyOn(wiring.outbox, "close").mockImplementation(() => {
      boundaries.push(`outbox.close:${fulfilled ? "fulfilled" : "pending"}`);
      realOutboxClose();
    });
    const realRepositoryClose = wiring.repository.close.bind(wiring.repository);
    vi.spyOn(wiring.repository, "close").mockImplementation(() => {
      boundaries.push(`repository.close:${fulfilled ? "fulfilled" : "pending"}`);
      realRepositoryClose();
    });

    wiring.armDeferredHold();
    let holdReached = false;
    const operation = wiring.startSubmission("same value", []).then(() => {
      fulfilled = true;
    });
    let injected: Error | undefined;
    try {
      await finishSenderRace({
        wiring,
        operation,
        body: async () => {
          if (phase === "after the real hold is reached") {
            await vi.waitFor(() => expect(wiring.currentHold()).toBeDefined());
            await wiring.currentHold()?.reached;
            holdReached = true;
          }
          throw new Error("injected body failure");
        },
        verify: () => {},
      });
    } catch (error) {
      injected = error as Error;
    }

    // Run the accepted cleanup explicitly so its disposal/close boundaries are
    // observable here; the registered fallback repeats it safely.
    await closeSourceFixture({
      sources: [wiring.source],
      outbox: wiring.outbox,
      repositories: [wiring.repository],
      disarm: wiring.disarm,
    });

    // The real submission fulfilled before the first disposal/close boundary.
    expect(fulfilled).toBe(true);
    expect(boundaries[0]).toBe("dispose:fulfilled");
    expect(boundaries).toContain("outbox.close:fulfilled");
    expect(boundaries).toContain("repository.close:fulfilled");
    // Deferred arming really was enabled, and future arming was disabled before
    // the hold was released.
    expect(wiring.armEnabled()).toBe(true);
    expect(wiring.disarmOrder()).toEqual(["arm-disabled", "hold-released"]);
    // The injected failure routed through the shared cleanup path is retained.
    expect(injected?.message).toBe("injected body failure");
    if (phase === "after the real hold is reached") expect(holdReached).toBe(true);
  },
);

// Control: when the same-ref edit is accepted AFTER the first source captures
// its durable token, the stale-token clear is refused and B's value survives.
test("a same-ref edit accepted after the first source captures its token is preserved", async () => {
  const fixture = sourceFixture("preparation-window-control");
  const key = fixture.key;
  const value: SkillEditorValue = { text: "shared value", skillNames: ["same"], mentions: [] };
  const first = fixture.track(
    createComposerSourceState("root", { repository: fixture.repository, key, outbox: fixture.outbox }),
  );
  const second = fixture.track(
    createComposerSourceState("root", { repository: fixture.repository, key, outbox: fixture.outbox }),
  );
  await first.ready();
  await second.ready();

  first.acceptEdit(value);
  await first.whenSettled();
  const submitted = await first.submissionDraft();

  // B accepts and commits the identical value AFTER A's token capture.
  second.acceptEdit(value);
  await second.whenSettled();
  const committedB = await requireRow(fixture.repository, key);
  const revisionB = committedB.token.sessionRevision;

  await first.settleDraft(submitted);
  await first.whenSettled();
  await second.whenSettled();
  expect(second.getSnapshot()).toMatchObject(value);

  first.dispose();
  second.dispose();
  const reopened = createBrowserDraftRepository({ indexedDB: fixture.factory, databaseName: fixture.databaseName });
  try {
    const row = await reopened.load(key);
    expect(row).toMatchObject(value);
    expect(row?.token.sessionRevision).toBe(revisionB);
  } finally {
    reopened.close();
  }
});

// A same-ref source B may commit an IDENTICAL full value while source A's own
// accepted save completion is still held. A then captures the post-drain token
// (B's) and, if A's cleanup clears, B's committed value must not be lost.
test("a same-ref source's identical committed edit survives the first source's preparation-window cleanup", async () => {
  const fixture = sourceFixture("preparation-window-preservation");
  const key = fixture.key;
  const value: SkillEditorValue = { text: "shared value", skillNames: ["same"], mentions: [] };

  const first = fixture.track(
    createComposerSourceState("root", { repository: fixture.repository, key, outbox: fixture.outbox }),
  );
  const second = fixture.track(
    createComposerSourceState("root", { repository: fixture.repository, key, outbox: fixture.outbox }),
  );
  // Both sources ready BEFORE arming the hold intended for A's save.
  await first.ready();
  await second.ready();

  const hold = holdNextWriteTransaction(["composerDrafts", "annotationNamespaces"]);
  try {
    // A's accepted save commits durably; its COMPLETE callback is held so A's
    // drain/preparation waits while B can still transact.
    first.acceptEdit(value);
    await hold.reached;

    // A's submission preparation is invoked NOW, so its SYNCHRONOUS entry
    // capture (generation/backing/shared edit revision) happens BEFORE B edits.
    const submittedP = first.submissionDraft();

    // B commits an identical full value during A's held completion.
    second.acceptEdit(value);
    await second.whenSettled();
    const committedB = await requireRow(fixture.repository, key);
    expect(committedB).toMatchObject(value);
    expect(committedB.mentions).toEqual([]);
    const revisionB = committedB.token.sessionRevision;

    // Release A: its drain resolves and its post-drain load captures B's token.
    hold.release();
    const submitted = await submittedP;
    expect(submitted.text).toBe("shared value");
    expect(submitted.settlementGuard?.token).toEqual(committedB.token);

    await first.settleDraft(submitted);
    await first.whenSettled();
    await second.whenSettled();
    expect(second.getSnapshot()).toMatchObject(value);

    // Actual durable bytes through an independent reopened adapter after both
    // sources retire.
    first.dispose();
    second.dispose();
    const reopened = createBrowserDraftRepository({ indexedDB: fixture.factory, databaseName: fixture.databaseName });
    try {
      const row = await reopened.load(key);
      expect(row).toMatchObject(value);
      expect(row?.mentions).toEqual([]);
      expect(row?.token.sessionRevision).toBe(revisionB);
    } finally {
      reopened.close();
    }
  } finally {
    hold.release();
  }
});
