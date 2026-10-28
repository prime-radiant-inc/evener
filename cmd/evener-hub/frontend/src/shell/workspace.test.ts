import { bindFilePath, parseFileReference } from "@evener/appwire-client/docContent";
import type { DockviewApi } from "dockview-core";
import { lazy } from "react";
import { afterAll, beforeAll, beforeEach, describe, expect, test } from "vitest";
import {
  composerSelectionScope,
  readRecoverySelection,
  readRefComposerSeat,
  writeRecoverySelection,
  writeRefComposerSeat,
} from "../panes/session/composer/draft";
import { retainedConversationComposer } from "../panes/session/composer/retainedComposer";
import { parseZoomParams } from "../panes/zoom/intent";
import { installLocalStorage, MemoryStorage } from "../storageTestUtils";
import type { MutationRecoveryRecord } from "../stores/mutationOutbox";
import { currentAttentionEntryToken, openAttentionSession } from "./attentionHistory";
import {
  captureAttentionPlacement,
  presentAttentionTarget,
  releaseUnreferencedAttentionPanes,
  restoreAttentionPlacement,
} from "./attentionPlacement";
import { conversationPaneLifetime } from "./paneLifetime";
import { type PaneDescriptor, type PaneProps, type PaneTypeId, registerPaneForTests } from "./paneRegistry";
import { openNestedSessionWithOwner } from "./sessionPlacement";
import {
  cancelPaneFocus,
  consumePaneFocus,
  conversationOpenParentRef,
  currentSessionRef,
  documentPaneState,
  isPaneOpen,
  type OpenPaneRecord,
  presentedPanes,
  recordConversationOpenParent,
  registerDockviewApi,
  requestPaneFocus,
  resetWorkspaceStoreForTests,
  workspaceStore,
} from "./workspace";

// Fixture pane types, registered once for the whole file. "settings" is the
// file's singleton fixture, "doc" its non-singleton fixture - both are real
// PaneTypeId values (the union is locked/closed, so fixtures must be drawn
// from it), but neither is the REAL settings/doc pane (those don't exist
// yet).
function fixtureDescriptor<P>(
  id: PaneDescriptor<P>["id"],
  overrides: Partial<PaneDescriptor<P>> = {},
): PaneDescriptor<P> {
  return {
    id,
    title: () => `title for ${id}`,
    component: lazy(() => new Promise<{ default: React.ComponentType<PaneProps<P>> }>(() => {})),
    ...overrides,
  };
}

beforeAll(() => {
  registerPaneForTests(fixtureDescriptor("settings", { singleton: true }));
  registerPaneForTests(fixtureDescriptor("doc"));
  registerPaneForTests(fixtureDescriptor("session"));
  registerPaneForTests(fixtureDescriptor("transcript"));
  registerPaneForTests(fixtureDescriptor("spawn", { singleton: true }));
  // The cascade retype test below retypes a sessionZoom pane, and retypePane
  // resolves its type through paneFor like every open path does.
  // parseParams mirrors the production registration (panes/zoom/index.tsx), so
  // restoreLayout normalizes a sessionZoom panel's params the way a real
  // reload does - including dropping a legacy source descriptor's parentRef.
  registerPaneForTests(fixtureDescriptor("sessionZoom", { parseParams: parseZoomParams }));
  // "welcome" is a real PaneTypeId and the main slot's empty state, so the
  // placement tests below open one. openPane has ALWAYS resolved its type
  // through paneFor (that is where an unregistered type throws), so this is a
  // missing fixture registration, not the store reaching somewhere new.
  registerPaneForTests(fixtureDescriptor("welcome", { singleton: true }));
});

beforeEach(() => {
  resetWorkspaceStoreForTests();
});

test("attention reuses presented and parked targets in their proper seats", () => {
  const state = workspaceStore.getState();
  const a = state.openPane("session", { ref: "local:a" });
  const x = state.openPane("session", { ref: "local:x" });
  const origin = captureAttentionPlacement();
  if (!origin) throw new Error("missing secondary origin");
  const b = presentAttentionTarget("local:b");
  if (!b) throw new Error("missing destination");
  expect(presentedPanes(workspaceStore.getState().panes).map((pane) => pane.id)).toEqual([a, "pane_session_3"]);
  expect(b.pane.slot).toBe("secondary");
  expect(presentAttentionTarget("local:a")?.pane.id).toBe(a);
  expect(b.pane.slot).toBe("secondary");
  const moved = presentAttentionTarget("local:x");
  expect(moved?.pane).toBe(origin.pane);
  expect(moved?.seatId).not.toBe(origin.seatId);
  expect(origin.pane.slot).toBe("main");
  // Restoring a presented record focuses it without moving it back.
  expect(restoreAttentionPlacement(origin)).toBe(true);
  expect(origin.pane.slot).toBe("main");
  presentAttentionTarget("local:c");
  expect(restoreAttentionPlacement(origin)).toBe(true);
  expect(origin.pane.slot).toBe("secondary");
  expect(b.pane.slot).toBe("parked");
  expect(workspaceStore.getState().focusedPaneId).toBe(x);
  releaseUnreferencedAttentionPanes(new Set([b.pane]));
  expect(workspaceStore.getState().panes.map((pane) => pane.id)).toEqual([x, "pane_session_3", "pane_session_4"]);
  releaseUnreferencedAttentionPanes(new Set());
  expect(workspaceStore.getState().panes.map((pane) => pane.id)).toEqual([x, "pane_session_4"]);
});

test("restoring a departed main reader displaces the host's welcome fallback", () => {
  // A -> Next B leaves A parked. Closing B empties main, so the host fills it
  // with its welcome placeholder; Back to A must restore A's own main seat
  // rather than opening a second pane beside a screen that says nothing is
  // open.
  const state = workspaceStore.getState();
  const a = state.openPane("session", { ref: "local:a" });
  const origin = captureAttentionPlacement();
  if (!origin) throw new Error("missing main origin");
  const b = presentAttentionTarget("local:b");
  if (!b) throw new Error("missing destination");
  expect(b.pane.slot).toBe("main");
  state.closePane(b.pane.id);
  const welcome = state.openPane("welcome");
  expect(workspaceStore.getState().mainPane()?.id).toBe(welcome);

  expect(restoreAttentionPlacement(origin)).toBe(true);
  const restored = workspaceStore.getState();
  expect(restored.mainPane()?.id).toBe(a);
  expect(restored.panes.some((pane) => pane.type === "welcome")).toBe(false);
  expect(restored.focusedPaneId).toBe(a);
});

test("restoring a parked pane reclaims its conversation's seat mapping", () => {
  installLocalStorage(new MemoryStorage());
  const state = workspaceStore.getState();
  const a = state.openPane("session", { ref: "local:parked-a" });
  const original = workspaceStore.getState().panes.find((pane) => pane.id === a);
  if (!original?.composerSeatId) throw new Error("original pane lacks its seat");
  writeRecoverySelection("local:parked-a", composerSelectionScope(original.composerSeatId), "original-row");
  const origin = captureAttentionPlacement();
  if (!origin) throw new Error("missing origin entry");

  // Attention navigation parks the original; a nested reopening creates a
  // second pane on the same conversation and takes over the mapping.
  const destination = presentAttentionTarget("local:parked-b");
  if (!destination) throw new Error("missing attention destination");
  expect(original.slot).toBe("parked");
  const secondId = state.openPane("session", { ref: "local:parked-a" });
  const second = workspaceStore.getState().panes.find((pane) => pane.id === secondId);
  if (!second?.composerSeatId) throw new Error("second pane lacks its seat");
  writeRecoverySelection("local:parked-a", composerSelectionScope(second.composerSeatId), "second-row");
  expect(readRefComposerSeat("local:parked-a")).toBe(second.composerSeatId);

  // Browser Back restores the original pane with its existing seat: the
  // focused pane must own the conversation mapping again.
  expect(restoreAttentionPlacement(origin)).toBe(true);
  expect(workspaceStore.getState().focusedPaneId).toBe(a);
  expect(readRefComposerSeat("local:parked-a")).toBe(original.composerSeatId);

  resetWorkspaceStoreForTests();
  const reopenedId = workspaceStore.getState().openPane("session", { ref: "local:parked-a" });
  const reopened = workspaceStore.getState().panes.find((pane) => pane.id === reopenedId);
  if (!reopened?.composerSeatId) throw new Error("reopened pane lacks its seat");
  expect(reopened.composerSeatId).toBe(original.composerSeatId);
  expect(readRecoverySelection("local:parked-a", composerSelectionScope(reopened.composerSeatId))).toBe("original-row");
});

// A drill keeps the same pane and lifetime but reads a different leaf, so the
// entry that owns that pane still describes the reader. If it fell invalid, its
// route would be treated as ordinary and AppShell would reopen the pre-drill
// reader beside the drill, taking focus off the leaf.
test("a drill inside an attention entry's cascade keeps the entry bound to its route", async () => {
  window.history.replaceState({}, "", "/s/local%3Aa");
  const a = workspaceStore.getState().openPane("session", { ref: "local:a" });
  workspaceStore.getState().focusPane(a);
  openAttentionSession("local:b");
  const b = presentAttentionTarget("local:b");
  if (!b) throw new Error("missing attention destination");
  openAttentionSession("local:c");
  const back = new Promise<void>((resolve) => window.addEventListener("popstate", () => resolve(), { once: true }));
  window.history.back();
  await back;
  const token = currentAttentionEntryToken();
  if (!token) throw new Error("the destination entry lost its token before the drill");

  workspaceStore.getState().focusPane(b.pane.id);
  expect(
    workspaceStore.getState().retypePane(b.pane, "sessionZoom", {
      ref: "local:leaf",
      source: { type: "session", params: { ref: "local:b" } },
      edges: [],
    }),
  ).toBe(true);

  expect(currentAttentionEntryToken()).toBe(token);
});

// Retiring a closed target must not detach the history branch: the parked
// source is still reachable with Back, and dropping the retiring entry's
// identity before the replacement lands reset the branch and released that
// source with its staged work.
test("closing an attention target keeps its parked source on the Back branch", async () => {
  window.history.replaceState({}, "", "/s/local%3Aa");
  const aId = workspaceStore.getState().openPane("session", { ref: "local:a" });
  workspaceStore.getState().focusPane(aId);
  const a = workspaceStore.getState().mainPane();
  if (!a) throw new Error("missing source reader");
  const lifetime = conversationPaneLifetime(a);
  openAttentionSession("local:b");
  const b = presentAttentionTarget("local:b");
  if (!b) throw new Error("missing attention target");
  openAttentionSession("local:c");
  const backOnce = new Promise<void>((resolve) => window.addEventListener("popstate", () => resolve(), { once: true }));
  window.history.back();
  await backOnce;

  workspaceStore.getState().closePane(b.pane.id);
  expect(a.slot).toBe("parked");

  // Another hop after the close must not release the still-parked source.
  openAttentionSession("local:d");
  expect(workspaceStore.getState().panes).toContain(a);
  expect(conversationPaneLifetime(a)).toBe(lifetime);
  expect(lifetime.alive).toBe(true);
});

test.each(["open", "toggle", "replace"] as const)("ordinary %s does not focus a parked match", (action) => {
  const id = workspaceStore.getState().openPane("session", { ref: "local:a" });
  const origin = captureAttentionPlacement();
  if (!origin) throw new Error("missing origin");
  presentAttentionTarget("local:b");
  const state = workspaceStore.getState();
  const opened =
    action === "open"
      ? state.openPane("session", { ref: "local:a" })
      : action === "toggle"
        ? state.togglePane("session", { ref: "local:a" }).paneId
        : state.replacePrimary("session", { ref: "local:a" });
  expect(opened).not.toBe(id);
  expect(presentedPanes(workspaceStore.getState().panes).some((pane) => pane.id === opened)).toBe(true);
  expect(workspaceStore.getState().focusedPaneId).toBe(opened);
  if (action === "replace") expect(restoreAttentionPlacement(origin)).toBe(false);
});

test("keyboard origins without readers and without focus use ordinary main placement", () => {
  workspaceStore.getState().openPane("settings");
  const settings = captureAttentionPlacement();
  expect(settings).toMatchObject({ lifetimeSerial: null, requestedRef: null, readViewId: null });
  presentAttentionTarget("local:b");
  if (!settings) throw new Error("missing settings origin");
  expect(restoreAttentionPlacement(settings)).toBe(true);
  workspaceStore.setState({ focusedPaneId: null });
  expect(captureAttentionPlacement()).toBeNull();
  expect(presentAttentionTarget("local:c")?.pane.slot).toBe("main");
  expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:c" });
});

test("attention seat bindings publish atomically with the workspace transition", () => {
  workspaceStore.getState().openPane("session", { ref: "local:a" });
  const origin = captureAttentionPlacement();
  const seats: (string | undefined)[] = [];
  const unsubscribe = workspaceStore.subscribe(() => {
    seats.push(captureAttentionPlacement()?.seatId);
  });
  try {
    presentAttentionTarget("local:b");
    expect(seats).toEqual([origin?.seatId]);
  } finally {
    unsubscribe();
  }
});

test("a reset and same-ID restored cascade replace source ownership and fence old callbacks", () => {
  const params = {
    ref: "child",
    source: { type: "session", params: { ref: "root" } },
    edges: [{ ownerRef: "root", childRef: "child", delegateId: "edge" }],
  };
  const old: OpenPaneRecord = { id: "restored-cascade", type: "sessionZoom", slot: "main", params };
  workspaceStore.setState({ panes: [old], focusedPaneId: old.id });
  const lifetime = conversationPaneLifetime(old);
  const source = lifetime.composer;
  if (!source) throw new Error("Missing original source composer");
  const oldCallback = () => source.editor.write("late old source", 15);
  resetWorkspaceStoreForTests();
  const restored: OpenPaneRecord = { ...old, params: { ...params } };
  workspaceStore.setState({ panes: [restored], focusedPaneId: restored.id });
  const replacement = conversationPaneLifetime(restored);
  replacement.composer?.editText("new original work");
  oldCallback();
  expect(lifetime.alive).toBe(false);
  expect(replacement.serial).not.toBe(lifetime.serial);
  expect(replacement.sourceRef).toBe("root");
  expect(replacement.composer?.getSnapshot().text).toBe("new original work");
  expect(workspaceStore.getState().panes[0]).toBe(restored);
});

describe("openPane", () => {
  test("opens a new pane, adding it to panes with a fresh id", () => {
    const id = workspaceStore.getState().openPane("doc", { ref: "a", path: "x" });
    const panes = workspaceStore.getState().panes;
    expect(panes).toEqual([{ id, type: "doc", params: { ref: "a", path: "x" }, slot: "main" }]);
  });

  test("defaults params to {} when omitted", () => {
    const id = workspaceStore.getState().openPane("doc");
    expect(workspaceStore.getState().panes.find((p) => p.id === id)?.params).toEqual({});
  });

  test("focuses the newly-opened pane", () => {
    const id = workspaceStore.getState().openPane("doc", { ref: "a" });
    expect(workspaceStore.getState().focusedPaneId).toBe(id);
  });

  test("keepExistingFocus leaves focus alone when the pane is already open", () => {
    const first = workspaceStore.getState().openPane("doc", { ref: "a" });
    const second = workspaceStore.getState().openPane("doc", { ref: "b" });
    expect(workspaceStore.getState().focusedPaneId).toBe(second);

    const again = workspaceStore.getState().openPane("doc", { ref: "a" }, { keepExistingFocus: true });

    expect(again).toBe(first); // still resolves to (and returns) the existing pane
    expect(workspaceStore.getState().focusedPaneId).toBe(second); // focus untouched
  });

  test("keepExistingFocus still focuses a pane it had to create", () => {
    workspaceStore.getState().openPane("doc", { ref: "a" });
    const fresh = workspaceStore.getState().openPane("doc", { ref: "b" }, { keepExistingFocus: true });
    expect(workspaceStore.getState().focusedPaneId).toBe(fresh);
  });

  test("keepExistingFocus still updates an already-open singleton's params without focusing it", () => {
    workspaceStore.getState().openPane("settings", { section: "appearance" });
    const other = workspaceStore.getState().openPane("doc", { ref: "a" });

    const settings = workspaceStore
      .getState()
      .openPane("settings", { section: "credentials" }, { keepExistingFocus: true });

    expect(workspaceStore.getState().panes.find((p) => p.id === settings)?.params).toEqual({ section: "credentials" });
    expect(workspaceStore.getState().focusedPaneId).toBe(other);
  });

  // The one placement rule (openPane's own, not any caller's): the main slot
  // holds exactly one pane; every later open stacks in the secondary group.
  test("the first pane takes the main slot", () => {
    const first = workspaceStore.getState().openPane("doc", { ref: "a" });
    expect(workspaceStore.getState().mainPane()?.id).toBe(first);
  });

  // Welcome is the main slot's empty state, not a peer: it exists to say
  // "nothing is open here". The first real pane therefore takes the main slot
  // FROM it rather than opening beside it - otherwise navigating from "/" to a
  // session leaves half the workspace showing "No session open" while the
  // session itself sits in the secondary group (observed in a real browser).
  test("the first real pane displaces a welcome placeholder from the main slot", () => {
    const welcome = workspaceStore.getState().openPane("welcome");
    const session = workspaceStore.getState().openPane("doc", { ref: "a" });

    expect(workspaceStore.getState().mainPane()?.id).toBe(session);
    expect(workspaceStore.getState().panes.map((p) => p.id)).toEqual([session]);
    expect(workspaceStore.getState().panes.map((p) => p.id)).not.toContain(welcome);
  });

  test("welcome is NOT displaced once a real pane already holds the main slot", () => {
    const main = workspaceStore.getState().openPane("doc", { ref: "a" });
    const welcome = workspaceStore.getState().openPane("welcome"); // secondary, oddly, but the user asked for it

    expect(workspaceStore.getState().mainPane()?.id).toBe(main);
    expect(workspaceStore.getState().panes.map((p) => p.id)).toContain(welcome);
  });

  test("every pane after the first goes to the secondary slot", () => {
    const first = workspaceStore.getState().openPane("doc", { ref: "a" });
    const second = workspaceStore.getState().openPane("doc", { ref: "b" });
    const third = workspaceStore.getState().openPane("doc", { ref: "c" });

    expect(workspaceStore.getState().panes.map((p) => p.slot)).toEqual(["main", "secondary", "secondary"]);
    expect(workspaceStore.getState().mainPane()?.id).toBe(first);
    expect([second, third]).not.toContain(workspaceStore.getState().mainPane()?.id);
  });

  // The one exception to "main takes whatever comes first": a caller that
  // knows its pane must never be the primary one. The store sees only a type
  // and a {ref}, so it cannot tell a subagent from a top-level session - the
  // rail can, and says so here. See docs/web-ui/specs/
  // 2026-07-26-subagent-opens-beside-main.md §A.
  describe("slot: 'secondary' preference", () => {
    test("keeps a pane out of the main slot even when main is empty", () => {
      const beside = workspaceStore.getState().openPane("doc", { ref: "a" }, { slot: "secondary" });
      expect(workspaceStore.getState().mainPane()).toBeNull();
      expect(workspaceStore.getState().panes.find((p) => p.id === beside)?.slot).toBe("secondary");
    });

    // Welcome is displaced by a pane that TAKES main. A pane that refuses main
    // has nothing to displace, so the placeholder must survive - otherwise the
    // workspace is left with a secondary pane and an empty left half.
    test("does not displace a welcome placeholder", () => {
      const welcome = workspaceStore.getState().openPane("welcome");
      workspaceStore.getState().openPane("doc", { ref: "a" }, { slot: "secondary" });
      expect(workspaceStore.getState().mainPane()?.id).toBe(welcome);
    });

    test("still goes secondary when main is already occupied", () => {
      const main = workspaceStore.getState().openPane("doc", { ref: "a" });
      const beside = workspaceStore.getState().openPane("doc", { ref: "b" }, { slot: "secondary" });
      expect(workspaceStore.getState().mainPane()?.id).toBe(main);
      expect(workspaceStore.getState().panes.find((p) => p.id === beside)?.slot).toBe("secondary");
    });

    test("omitting it leaves the default placement exactly as it was", () => {
      const first = workspaceStore.getState().openPane("doc", { ref: "a" });
      const second = workspaceStore.getState().openPane("doc", { ref: "b" });
      expect(workspaceStore.getState().panes.map((p) => p.slot)).toEqual(["main", "secondary"]);
      expect(workspaceStore.getState().mainPane()?.id).toBe(first);
      expect(second).not.toBe(first);
    });

    // Reopening an already-open pane resolves to that pane; the preference is
    // a placement rule for a pane being CREATED, and slot is assign-once.
    test("does not move a pane that is already open", () => {
      const main = workspaceStore.getState().openPane("doc", { ref: "a" });
      const again = workspaceStore.getState().openPane("doc", { ref: "a" }, { slot: "secondary" });
      expect(again).toBe(main);
      expect(workspaceStore.getState().mainPane()?.id).toBe(main);
    });
  });

  test("closing the main pane empties the main slot, and the next open refills it", () => {
    const first = workspaceStore.getState().openPane("doc", { ref: "a" });
    workspaceStore.getState().openPane("doc", { ref: "b" }); // secondary

    workspaceStore.getState().closePane(first);

    expect(workspaceStore.getState().mainPane()).toBeNull(); // no promotion from the right
    const relaunched = workspaceStore.getState().openPane("welcome");
    expect(workspaceStore.getState().mainPane()?.id).toBe(relaunched);
  });

  test("mainPane is null for an empty workspace", () => {
    expect(workspaceStore.getState().mainPane()).toBeNull();
  });

  test("two non-singleton panes with different params are both opened", () => {
    const first = workspaceStore.getState().openPane("doc", { ref: "a" });
    const second = workspaceStore.getState().openPane("doc", { ref: "b" });
    expect(first).not.toBe(second);
    expect(workspaceStore.getState().panes.map((p) => p.id)).toEqual([first, second]);
  });

  test("reopening a non-singleton pane with identical params focuses the existing one instead of duplicating", () => {
    const first = workspaceStore.getState().openPane("doc", { ref: "a" });
    workspaceStore.getState().openPane("doc", { ref: "b" }); // a second, different pane
    const again = workspaceStore.getState().openPane("doc", { ref: "a" });

    expect(again).toBe(first);
    expect(workspaceStore.getState().panes).toHaveLength(2);
    expect(workspaceStore.getState().focusedPaneId).toBe(first);
  });

  test("reopening a singleton pane type focuses the existing instance instead of creating a second", () => {
    const first = workspaceStore.getState().openPane("settings", { section: "appearance" });
    const again = workspaceStore.getState().openPane("settings", { section: "appearance" });

    expect(again).toBe(first);
    expect(workspaceStore.getState().panes).toHaveLength(1);
  });

  test("reopening a singleton pane type with different params updates the existing pane's params in place", () => {
    const first = workspaceStore.getState().openPane("settings", { section: "appearance" });
    const again = workspaceStore.getState().openPane("settings", { section: "credentials" });

    expect(again).toBe(first);
    expect(workspaceStore.getState().panes).toEqual([
      { id: first, type: "settings", params: { section: "credentials" }, slot: "main" },
    ]);
    expect(workspaceStore.getState().focusedPaneId).toBe(first);
  });

  test("throws for an unregistered pane type (mirrors paneFor's own contract)", () => {
    // Every real PaneTypeId gets registered by its own production module at
    // import time, so any real id (like "transcript") is registered as soon
    // as something this file imports pulls that module in. An id outside the
    // closed union (cast past the type check) is the only one guaranteed to
    // stay unregistered whatever this file imports.
    expect(() => workspaceStore.getState().openPane("not-a-real-pane-type" as PaneTypeId, {})).toThrow(
      /not-a-real-pane-type/,
    );
  });
});

describe("source-aware document placement", () => {
  test("promotes the exact secondary opener and retains its parent", async () => {
    const { openDocBeside } = await import("../panes/doc/openDoc");
    const parentRef = "local:034MXwo6BpPH0QQCgdICSf";
    const childRef = "local:02wMz5TxvEMoJEDTDGOTil";
    const state = workspaceStore.getState();
    const parentId = state.openPane("session", { ref: parentRef });
    const sourceId = state.openPane("transcript", { ref: childRef, parentRef }, { slot: "secondary" });
    const parent = workspaceStore.getState().panes.find((pane) => pane.id === parentId);
    const source = workspaceStore.getState().panes.find((pane) => pane.id === sourceId);
    const reference = bindFilePath("docs/a.md", "/work/child");
    if (!reference || !parent || !source) throw new Error("fixture did not create bound panes");
    openDocBeside({ session: childRef, reference, sourcePaneId: sourceId });
    const panes = workspaceStore.getState().panes;
    expect(panes).toContain(parent);
    expect(panes).toContain(source);
    expect(source.slot).toBe("main");
    expect(parent.slot).toBe("secondary");
    const document = panes.find((pane) => pane.type === "doc");
    if (!document) throw new Error("document did not open");
    expect(documentPaneState(document)?.origin).toBe(source);
  });

  test("aliases reuse one document while retaining the latest binding, origin, and reopen generation", async () => {
    const { openDocBeside } = await import("../panes/doc/openDoc");
    const session = "local:02wMz5TxvEMoJEDTDGOTil";
    const workspace = workspaceStore.getState();
    const firstId = workspace.openPane("session", { ref: session });
    const secondId = workspace.openPane("transcript", { ref: session }, { slot: "secondary" });
    const first = workspaceStore.getState().panes.find((pane) => pane.id === firstId);
    const second = workspaceStore.getState().panes.find((pane) => pane.id === secondId);
    const absolute = bindFilePath("/work/child/docs/a.md", "/work/child");
    const located = parseFileReference("docs/a.md:17:4", "code", "/work/child");
    if (!absolute || !located || !first || !second) throw new Error("fixture did not create aliases and sources");

    openDocBeside({ session, reference: absolute, sourcePaneId: first.id });
    const document = workspaceStore.getState().panes.find((pane) => pane.type === "doc");
    if (!document) throw new Error("document did not open");
    expect(documentPaneState(document)).toEqual({ reference: absolute, origin: first, reopen: 0 });

    openDocBeside({ session, reference: located, sourcePaneId: second.id });
    expect(workspaceStore.getState().panes.filter((pane) => pane.type === "doc")).toEqual([document]);
    expect(documentPaneState(document)).toEqual({ reference: located, origin: second, reopen: 1 });
  });

  test("closing a document retires its binding", async () => {
    const { openDocBeside } = await import("../panes/doc/openDoc");
    const session = "local:02wMz5TxvEMoJEDTDGOTil";
    const workspace = workspaceStore.getState();
    const sourceId = workspace.openPane("session", { ref: session });
    const reference = bindFilePath("docs/a.md", "/work/child");
    if (!reference) throw new Error("fixture did not bind the document");
    openDocBeside({ session, reference, sourcePaneId: sourceId });
    const oldDocument = workspaceStore.getState().panes.find((pane) => pane.type === "doc");
    if (!oldDocument) throw new Error("document did not open");
    workspace.closePane(oldDocument.id);
    expect(documentPaneState(oldDocument)).toBeUndefined();
  });

  test("a cascade column's filename open binds the hosting zoom without displacing the cascade", async () => {
    const { openDocBeside } = await import("../panes/doc/openDoc");
    const rootRef = "local:034MXwo6BpPH0QQCgdICSf";
    const childRef = "local:02wMz5TxvEMoJEDTDGOTil";
    const grandchildRef = "local:01ZoomFixtureAAAAAAAAAAA";
    const workspace = workspaceStore.getState();
    const originId = workspace.openPane("session", { ref: rootRef });
    const zoom: OpenPaneRecord = {
      id: "cascade-zoom",
      type: "sessionZoom",
      params: {
        ref: grandchildRef,
        source: { type: "session", params: { ref: rootRef } },
        edges: [
          { ownerRef: rootRef, childRef: childRef, delegateId: "d1" },
          { ownerRef: childRef, childRef: grandchildRef, delegateId: "d2" },
        ],
      },
      slot: "secondary",
    };
    workspaceStore.setState({ panes: [...workspaceStore.getState().panes, zoom] });
    const origin = workspaceStore.getState().panes.find((pane) => pane.id === originId);
    const reference = bindFilePath("docs/a.md", "/work/child");
    if (!reference || !origin) throw new Error("fixture did not create the cascade");
    // The embedded parent column's filename open names the hosting zoom pane.
    openDocBeside({ session: childRef, reference, sourcePaneId: zoom.id });
    const document = workspaceStore.getState().panes.find((pane) => pane.type === "doc");
    if (!document) throw new Error("document did not open");
    expect(documentPaneState(document)?.origin).toBe(zoom);
    // The cascade keeps its placement: the zoom stays secondary and the
    // origin conversation keeps main, so a primary navigation cannot
    // replace the inspection out from under the document.
    expect(zoom.slot).toBe("secondary");
    expect(origin.slot).toBe("main");
  });

  test("a zoom whose cascade does not host the conversation does not adopt its document", async () => {
    const { openDocBeside } = await import("../panes/doc/openDoc");
    const hostedRef = "local:02wMz5TxvEMoJEDTDGOTil";
    const routedRef = "local:034MXwo6BpPH0QQCgdICSf";
    const workspace = workspaceStore.getState();
    const sourceId = workspace.openPane("session", { ref: routedRef });
    const zoom: OpenPaneRecord = {
      id: "cascade-zoom",
      type: "sessionZoom",
      params: {
        ref: hostedRef,
        source: { type: "session", params: { ref: routedRef } },
        edges: [{ ownerRef: routedRef, childRef: hostedRef, delegateId: "d1" }],
      },
      slot: "secondary",
    };
    workspaceStore.setState({ panes: [...workspaceStore.getState().panes, zoom] });
    const reference = bindFilePath("docs/a.md", "/work/child");
    if (!reference) throw new Error("fixture did not bind the document");
    // A stale request whose session no longer appears in the zoom's cascade
    // must not bind the zoom as the document's origin.
    openDocBeside({ session: "local:absent", reference, sourcePaneId: zoom.id });
    const document = workspaceStore.getState().panes.find((pane) => pane.type === "doc");
    if (!document) throw new Error("document did not open");
    expect(documentPaneState(document)?.origin).toBeUndefined();
    expect(workspaceStore.getState().panes.find((pane) => pane.id === sourceId)?.slot).toBe("main");
  });

  test("a cascade retyping transfers its document's origin binding to the replacement pane", async () => {
    const { openDocBeside } = await import("../panes/doc/openDoc");
    const rootRef = "local:034MXwo6BpPH0QQCgdICSf";
    const childRef = "local:02wMz5TxvEMoJEDTDGOTil";
    const grandchildRef = "local:01ZoomFixtureAAAAAAAAAAA";
    const workspace = workspaceStore.getState();
    const originId = workspace.openPane("session", { ref: rootRef });
    const params = {
      ref: childRef,
      source: { type: "session", params: { ref: rootRef } },
      edges: [{ ownerRef: rootRef, childRef: childRef, delegateId: "d1" }],
    };
    const zoom: OpenPaneRecord = { id: "cascade-zoom", type: "sessionZoom", params, slot: "secondary" };
    workspaceStore.setState({ panes: [...workspaceStore.getState().panes, zoom] });
    const origin = workspaceStore.getState().panes.find((pane) => pane.id === originId);
    const reference = bindFilePath("docs/a.md", "/work/child");
    if (!reference || !origin) throw new Error("fixture did not create the cascade");
    openDocBeside({ session: childRef, reference, sourcePaneId: zoom.id });
    const document = workspaceStore.getState().panes.find((pane) => pane.type === "doc");
    if (!document) throw new Error("document did not open");
    expect(documentPaneState(document)?.origin).toBe(zoom);
    // Drilling deeper replaces the zoom's record exactly as every cascade
    // drill, pop and resume does through retypePane: the document's origin
    // must name the surviving pane or Back treats the inspector as gone.
    const drilled = {
      ...params,
      ref: grandchildRef,
      edges: [...params.edges, { ownerRef: childRef, childRef: grandchildRef, delegateId: "d2" }],
    };
    expect(workspaceStore.getState().retypePane(zoom, "sessionZoom", drilled)).toBe(true);
    const replacement = workspaceStore.getState().panes.find((pane) => pane.id === "cascade-zoom");
    if (!replacement || replacement === zoom) throw new Error("retype did not replace the record");
    expect(documentPaneState(document)?.origin).toBe(replacement);
    expect(origin.slot).toBe("main");
  });

  test("reset retires a still-bound document before pane ids are reused", async () => {
    const { openDocBeside } = await import("../panes/doc/openDoc");
    const session = "local:02wMz5TxvEMoJEDTDGOTil";
    const workspace = workspaceStore.getState();
    const sourceId = workspace.openPane("session", { ref: session });
    const source = workspaceStore.getState().panes.find((pane) => pane.id === sourceId);
    const reference = bindFilePath("docs/a.md", "/work/child");
    if (!reference || !source) throw new Error("fixture did not bind the document and source");
    openDocBeside({ session, reference, sourcePaneId: sourceId });
    const oldDocument = workspaceStore.getState().panes.find((pane) => pane.type === "doc");
    if (!oldDocument) throw new Error("document did not open");
    expect(documentPaneState(oldDocument)).toEqual({ reference, origin: source, reopen: 0 });

    resetWorkspaceStoreForTests();
    workspaceStore.getState().openPane("session", { ref: session });
    const reusedId = workspaceStore.getState().openPane("doc", oldDocument.params);
    const replacement = workspaceStore.getState().panes.find((pane) => pane.id === reusedId);
    if (!replacement) throw new Error("replacement document did not open");
    expect(replacement.id).toBe(oldDocument.id);
    expect(replacement).not.toBe(oldDocument);
    expect(documentPaneState(oldDocument)).toBeUndefined();
    expect(documentPaneState(replacement)).toBeUndefined();
  });
});

describe("togglePane and pending focus", () => {
  test("opens secondary panes, closes them, and distinguishes refs", () => {
    const first = workspaceStore.getState().togglePane("session", { ref: "ref_a" });
    expect(first.opened).toBe(true);
    expect(workspaceStore.getState().panes).toMatchObject([{ type: "session", slot: "secondary" }]);
    expect(workspaceStore.getState().focusedPaneId).toBe(first.paneId);
    expect(workspaceStore.getState().togglePane("session", { ref: "ref_a" })).toEqual({
      paneId: first.paneId,
      opened: false,
    });
    expect(workspaceStore.getState().focusedPaneId).toBeNull();

    const third = workspaceStore.getState().togglePane("session", { ref: "ref_a" });
    expect(third.paneId).not.toBe(first.paneId);
    expect(workspaceStore.getState().panes).toMatchObject([{ type: "session", slot: "secondary" }]);
    expect(isPaneOpen(workspaceStore.getState(), "session", { ref: "ref_a" })).toBe(true);
    expect(isPaneOpen(workspaceStore.getState(), "session", { ref: "ref_b" })).toBe(false);
  });

  test("consumes pending focus once and supports cancellation", () => {
    requestPaneFocus("pane_session_1");
    expect(consumePaneFocus("pane_session_1")).toBe(true);
    expect(consumePaneFocus("pane_session_1")).toBe(false);

    requestPaneFocus("pane_session_2");
    cancelPaneFocus("pane_session_2");
    expect(consumePaneFocus("pane_session_2")).toBe(false);

    const id = workspaceStore.getState().openPane("session", { ref: "ordinary" });
    expect(consumePaneFocus(id)).toBe(false);
  });
});

describe("replacePrimary", () => {
  // A replacement that only opens the requested pane leaves old secondary
  // panes behind; this literal workspace is the contract that catches that
  // additive-placement break.
  test("replaces main and clears every secondary pane when the primary identity changes", () => {
    const workspace = workspaceStore.getState();
    workspace.openPane("doc", { ref: "secondary-a" });
    workspace.openPane("doc", { ref: "secondary-b" });

    const sessionId = workspace.replacePrimary("session", { ref: "local:session-b" });

    expect(workspaceStore.getState().panes).toEqual([
      {
        id: sessionId,
        type: "session",
        params: { ref: "local:session-b" },
        slot: "main",
        composerSeatId: expect.any(String),
      },
    ]);
    expect(workspaceStore.getState().focusedPaneId).toBe(sessionId);
  });

  // A replacement that always rebuilds the workspace clears useful secondary
  // panes even when the requested primary is already the current identity.
  test("preserves secondary panes when the requested primary identity is already main", () => {
    const workspace = workspaceStore.getState();
    const mainId = workspace.replacePrimary("session", { ref: "local:session-a" });
    const secondaryId = workspace.openPane("doc", { ref: "secondary" });

    const repeatedId = workspace.replacePrimary("session", { ref: "local:session-a" });

    expect(repeatedId).toBe(mainId);
    expect(workspaceStore.getState().panes).toEqual([
      {
        id: mainId,
        type: "session",
        params: { ref: "local:session-a" },
        slot: "main",
        composerSeatId: expect.any(String),
      },
      { id: secondaryId, type: "doc", params: { ref: "secondary" }, slot: "secondary" },
    ]);
  });

  // Settings is a singleton whose section changes without changing its
  // primary identity; a replacement that closes/reopens it loses the pane id
  // and its secondary neighbors instead of updating the existing main pane.
  test("updates a singleton settings section in place while preserving secondary panes", () => {
    const workspace = workspaceStore.getState();
    const settingsId = workspace.replacePrimary("settings", { section: "general" });
    const secondaryId = workspace.openPane("doc", { ref: "secondary" });

    const updatedId = workspace.replacePrimary("settings", { section: "credentials" });

    expect(updatedId).toBe(settingsId);
    expect(workspaceStore.getState().panes).toEqual([
      { id: settingsId, type: "settings", params: { section: "credentials" }, slot: "main" },
      { id: secondaryId, type: "doc", params: { ref: "secondary" }, slot: "secondary" },
    ]);
  });

  // A session's primary identity is READ FROM the params it is called with,
  // never spelled beside them: with two arguments there is no pair that can
  // disagree, so no caller can ask replacePrimary to keep a live pane while
  // handing it a different ref (kata z44z). That mismatch is what would
  // re-point a mounted pane - same pane id, new ref - and neither host
  // remounts a pane whose id it already has, so the previous session's job
  // list and badge count would stay on screen (katas pcx5/tmyw rest on this).
  // Written in the two-argument form on purpose: re-introducing a separate
  // identity argument breaks the typecheck here.
  test("a session's primary identity is the ref in its own params", () => {
    const workspace = workspaceStore.getState();
    const mainId = workspace.replacePrimary("session", { ref: "local:session-a" });

    expect(workspace.replacePrimary("session", { ref: "local:session-a" })).toBe(mainId);

    const switched = workspace.replacePrimary("session", { ref: "local:session-b" });
    expect(switched).not.toBe(mainId);
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:session-b" });
  });

  test("notifies subscribers once for a primary replacement", () => {
    const snapshots: Array<ReadonlyArray<OpenPaneRecord>> = [];
    const unsubscribe = workspaceStore.subscribe((state) => snapshots.push(state.panes));

    workspaceStore.getState().replacePrimary("spawn", {});

    unsubscribe();
    expect(snapshots).toHaveLength(1);
    expect(snapshots[0]).toEqual(workspaceStore.getState().panes);
  });
});

describe("closePane", () => {
  test("removes the pane from panes", () => {
    const id = workspaceStore.getState().openPane("doc", { ref: "a" });
    workspaceStore.getState().closePane(id);
    expect(workspaceStore.getState().panes).toEqual([]);
  });

  test("clears focusedPaneId when closing the focused pane", () => {
    const id = workspaceStore.getState().openPane("doc", { ref: "a" });
    workspaceStore.getState().closePane(id);
    expect(workspaceStore.getState().focusedPaneId).toBeNull();
  });

  test("leaves focusedPaneId untouched when closing a different, non-focused pane", () => {
    const first = workspaceStore.getState().openPane("doc", { ref: "a" });
    const second = workspaceStore.getState().openPane("doc", { ref: "b" }); // now focused
    workspaceStore.getState().closePane(first);
    expect(workspaceStore.getState().focusedPaneId).toBe(second);
    expect(workspaceStore.getState().panes.map((p) => p.id)).toEqual([second]);
  });

  test("is a no-op for an unknown paneId", () => {
    const id = workspaceStore.getState().openPane("doc", { ref: "a" });
    workspaceStore.getState().closePane("not-a-real-id");
    expect(workspaceStore.getState().panes.map((p) => p.id)).toEqual([id]);
    expect(workspaceStore.getState().focusedPaneId).toBe(id);
  });
});

describe("focusPane", () => {
  test("sets focusedPaneId to an existing pane's id", () => {
    const first = workspaceStore.getState().openPane("doc", { ref: "a" });
    workspaceStore.getState().openPane("doc", { ref: "b" }); // focus moves here
    workspaceStore.getState().focusPane(first);
    expect(workspaceStore.getState().focusedPaneId).toBe(first);
  });

  test("is a no-op for an unknown paneId", () => {
    const first = workspaceStore.getState().openPane("doc", { ref: "a" });
    workspaceStore.getState().focusPane("not-a-real-id");
    expect(workspaceStore.getState().focusedPaneId).toBe(first);
  });
});

// A minimal DockviewApi double covering only what workspace.ts itself
// touches (toJSON/fromJSON/panels/activePanel/clear). This file tests
// workspace.ts's OWN logic - dedup/focus bookkeeping and how it delegates
// persistence - in isolation from the real dockview library; DockHost's own
// tests (DockHost.test.tsx) exercise real dockview end to end, per this
// task's testing guidance ("mock only what jsdom can't do" is about THAT
// boundary, not this one - a fake api double here is an ordinary unit-test
// seam for a store that would otherwise need a full dockview mount just to
// test a dedup guard).
class FakeDockviewApi {
  panels: Array<{ id: string; params: unknown }> = [];
  activePanel: { id: string } | undefined = undefined;
  cleared = false;
  toJSONResult: unknown = { fake: "layout" };
  fromJSONBehavior: (data: unknown) => void = () => {};

  toJSON(): unknown {
    return this.toJSONResult;
  }

  fromJSON(data: unknown): void {
    this.fromJSONBehavior(data);
  }

  clear(): void {
    this.cleared = true;
    this.panels = [];
    this.activePanel = undefined;
  }

  removePanel(panel: { id: string }): void {
    this.panels = this.panels.filter((p) => p.id !== panel.id);
    if (this.activePanel?.id === panel.id) this.activePanel = undefined;
  }
}

function asDockviewApi(fake: FakeDockviewApi): DockviewApi {
  return fake as unknown as DockviewApi;
}

describe("layoutJSON / restoreLayout (against a fake DockviewApi)", () => {
  test("layoutJSON returns null when no DockviewApi is registered", () => {
    expect(workspaceStore.getState().layoutJSON()).toBeNull();
  });

  test("restoreLayout returns false when no DockviewApi is registered", () => {
    expect(workspaceStore.getState().restoreLayout({ anything: true })).toBe(false);
  });

  test("layoutJSON delegates to the registered api's toJSON()", () => {
    const fake = new FakeDockviewApi();
    fake.toJSONResult = { grid: "serialized" };
    registerDockviewApi(asDockviewApi(fake));

    expect(workspaceStore.getState().layoutJSON()).toEqual({ grid: "serialized" });
  });

  test("registering null clears the api (layoutJSON goes back to null)", () => {
    const fake = new FakeDockviewApi();
    registerDockviewApi(asDockviewApi(fake));
    registerDockviewApi(null);

    expect(workspaceStore.getState().layoutJSON()).toBeNull();
  });

  test("restoreLayout calls fromJSON and, on success, rebuilds panes/focusedPaneId from the api's panels/activePanel", () => {
    const fake = new FakeDockviewApi();
    let receivedData: unknown;
    fake.fromJSONBehavior = (data) => {
      receivedData = data;
      fake.panels = [
        { id: "p1", params: { paneType: "settings", paneParams: { section: "appearance" } } },
        { id: "p2", params: { paneType: "doc", paneParams: { ref: "a", path: "x" } } },
      ];
      fake.activePanel = { id: "p2" };
    };
    registerDockviewApi(asDockviewApi(fake));

    const ok = workspaceStore.getState().restoreLayout({ the: "json" });

    expect(ok).toBe(true);
    expect(receivedData).toEqual({ the: "json" });
    expect(workspaceStore.getState().panes).toEqual([
      // Slots are re-derived from the restored grid order: the first panel is
      // the top-left (main) one, everything after it is in the group to its right.
      { id: "p1", type: "settings", params: { section: "appearance" }, slot: "main" },
      { id: "p2", type: "doc", params: { ref: "a", path: "x" }, slot: "secondary" },
    ]);
    expect(workspaceStore.getState().focusedPaneId).toBe("p2");
  });

  test("restoreLayout falls back to the first panel as focused when the api reports no active panel", () => {
    const fake = new FakeDockviewApi();
    fake.fromJSONBehavior = () => {
      fake.panels = [{ id: "p1", params: { paneType: "doc", paneParams: {} } }];
      fake.activePanel = undefined;
    };
    registerDockviewApi(asDockviewApi(fake));

    workspaceStore.getState().restoreLayout({});

    expect(workspaceStore.getState().focusedPaneId).toBe("p1");
  });

  test("restoreLayout sets focusedPaneId to null when the restored layout has no panels at all", () => {
    const fake = new FakeDockviewApi();
    fake.fromJSONBehavior = () => {
      fake.panels = [];
      fake.activePanel = undefined;
    };
    registerDockviewApi(asDockviewApi(fake));

    const ok = workspaceStore.getState().restoreLayout({});

    expect(ok).toBe(true);
    expect(workspaceStore.getState().panes).toEqual([]);
    expect(workspaceStore.getState().focusedPaneId).toBeNull();
  });

  test("restoreLayout returns false and clears the api when fromJSON throws (structurally invalid data)", () => {
    const fake = new FakeDockviewApi();
    fake.fromJSONBehavior = () => {
      throw new Error("dockview: root must be of type branch");
    };
    registerDockviewApi(asDockviewApi(fake));

    const ok = workspaceStore.getState().restoreLayout({ nonsense: true });

    expect(ok).toBe(false);
    expect(fake.cleared).toBe(true);
    expect(workspaceStore.getState().panes).toEqual([]);
    expect(workspaceStore.getState().focusedPaneId).toBeNull();
  });

  test("restoreLayout skips a restored panel whose paneType isn't a real PaneTypeId and recovers the rest", () => {
    const fake = new FakeDockviewApi();
    fake.fromJSONBehavior = () => {
      fake.panels = [
        { id: "p1", params: { paneType: "not-a-real-type", paneParams: {} } },
        { id: "p2", params: { paneType: "doc", paneParams: { ref: "a" } } },
      ];
      fake.activePanel = { id: "p1" };
    };
    registerDockviewApi(asDockviewApi(fake));

    const ok = workspaceStore.getState().restoreLayout({});

    expect(ok).toBe(true);
    expect(fake.cleared).toBe(false);
    expect(workspaceStore.getState().panes).toEqual([{ id: "p2", type: "doc", params: { ref: "a" }, slot: "main" }]);
    // The skipped panel was the active one; focus falls to the first survivor.
    expect(workspaceStore.getState().focusedPaneId).toBe("p2");
    // The skipped panel must leave the LIVE api synchronously: the render
    // before DockHost's reconciliation runs every api panel through
    // paneFor(), which throws for the unregistered type.
    expect(fake.panels.map((p) => p.id)).toEqual(["p2"]);
    expect(fake.activePanel?.id ?? null).not.toBe("p1");
  });

  test("restoreLayout skips a restored panel whose paneType is a valid PaneTypeId but isn't registered", () => {
    const fake = new FakeDockviewApi();
    fake.fromJSONBehavior = () => {
      // Every real PaneTypeId gets registered by its own production module at
      // import time, so a real id here is registered as soon as something this
      // file imports pulls that module in. An id outside the closed union
      // (cast past the type check) simulates a layout saved by a build that
      // shipped a pane type this one never registers, and is the only id
      // guaranteed to stay unregistered whatever this file imports.
      fake.panels = [
        { id: "p1", params: { paneType: "not-a-real-pane-type", paneParams: { ref: "a" } } },
        { id: "p2", params: { paneType: "doc", paneParams: { ref: "b" } } },
      ];
      fake.activePanel = { id: "p2" };
    };
    registerDockviewApi(asDockviewApi(fake));

    const ok = workspaceStore.getState().restoreLayout({});

    expect(ok).toBe(true);
    expect(fake.cleared).toBe(false);
    expect(workspaceStore.getState().panes).toEqual([{ id: "p2", type: "doc", params: { ref: "b" }, slot: "main" }]);
    expect(workspaceStore.getState().focusedPaneId).toBe("p2");
  });

  test("restoreLayout skips a restored panel that has no params at all, keeping the rest", () => {
    const fake = new FakeDockviewApi();
    fake.fromJSONBehavior = () => {
      fake.panels = [
        { id: "p1", params: undefined },
        { id: "p2", params: { paneType: "doc", paneParams: { ref: "a" } } },
      ];
      fake.activePanel = { id: "p2" };
    };
    registerDockviewApi(asDockviewApi(fake));

    const ok = workspaceStore.getState().restoreLayout({});

    expect(ok).toBe(true);
    expect(fake.cleared).toBe(false);
    expect(workspaceStore.getState().panes).toEqual([{ id: "p2", type: "doc", params: { ref: "a" }, slot: "main" }]);
    expect(workspaceStore.getState().focusedPaneId).toBe("p2");
  });

  // A REMOVED pane type is the one unregistered-type case registration can
  // never fix: a layout saved while "sessionNotes" still existed restores a
  // panel this build can't render. Skipping that panel (DockHost's structural
  // reconciliation then removes its orphaned dockview panel) recovers every
  // other pane instead of discarding the whole workspace.
  test("restoreLayout skips a panel whose paneType this build no longer registers, keeping every other pane", () => {
    const fake = new FakeDockviewApi();
    fake.fromJSONBehavior = () => {
      // "sessionNotes" is no longer in the PaneTypeId union (removed with the
      // notes sidebar pane); the cast simulates a layout persisted by a build
      // that still shipped it.
      fake.panels = [
        { id: "p1", params: { paneType: "doc", paneParams: { ref: "a" } } },
        { id: "p2", params: { paneType: "sessionNotes" as never, paneParams: { ref: "a" } } },
        { id: "p3", params: { paneType: "doc", paneParams: { ref: "b" } } },
      ];
      fake.activePanel = { id: "p2" };
    };
    registerDockviewApi(asDockviewApi(fake));

    const ok = workspaceStore.getState().restoreLayout({});

    expect(ok).toBe(true);
    expect(fake.cleared).toBe(false);
    expect(workspaceStore.getState().panes).toEqual([
      { id: "p1", type: "doc", params: { ref: "a" }, slot: "main" },
      { id: "p3", type: "doc", params: { ref: "b" }, slot: "secondary" },
    ]);
    // The active panel was the skipped one; focus falls to the first survivor.
    expect(workspaceStore.getState().focusedPaneId).toBe("p1");
    // ...and the skipped panel left the live api in the same restore, not in
    // a later reconciliation pass.
    expect(fake.panels.map((p) => p.id)).toEqual(["p1", "p3"]);
    expect(fake.activePanel?.id ?? null).not.toBe("p2");
  });

  // Restored ids come from a PREVIOUS page load's own independently-
  // numbered counter (nextPaneSeq resets to 0 on every fresh load - see
  // openPane's own "fresh id" tests above) - a freshly-minted id after a
  // restore can otherwise collide with one just restored, silently
  // duplicating an id in `panes` (see workspace.ts's bumpPastRestoredIds
  // for the full failure mode this guards against, live-reproduced via
  // DockHost.test.tsx's own save/restore/re-open round trip).
  test("restoreLayout bumps the pane-id counter past every restored id, so the next openPane() can't collide with one", () => {
    const fake = new FakeDockviewApi();
    fake.fromJSONBehavior = () => {
      fake.panels = [
        { id: "pane_doc_1", params: { paneType: "doc", paneParams: { ref: "a" } } },
        { id: "pane_doc_5", params: { paneType: "doc", paneParams: { ref: "b" } } },
      ];
      fake.activePanel = { id: "pane_doc_5" };
    };
    registerDockviewApi(asDockviewApi(fake));

    workspaceStore.getState().restoreLayout({});
    const freshId = workspaceStore.getState().openPane("doc", { ref: "fresh" });

    expect(freshId).not.toBe("pane_doc_1");
    expect(freshId).not.toBe("pane_doc_5");
    expect(workspaceStore.getState().panes.map((p) => p.id)).toEqual(["pane_doc_1", "pane_doc_5", freshId]);
  });

  test("restoreLayout leaves the counter untouched when the restored layout has no panels at all", () => {
    const fake = new FakeDockviewApi();
    fake.fromJSONBehavior = () => {
      fake.panels = [];
      fake.activePanel = undefined;
    };
    registerDockviewApi(asDockviewApi(fake));

    workspaceStore.getState().restoreLayout({});
    const freshId = workspaceStore.getState().openPane("doc", { ref: "fresh" });

    expect(freshId).toBe("pane_doc_1"); // the ordinary first-ever id, nothing to bump past
  });

  test("layoutJSON stamps each recorded conversation open-parent edge onto the layout", () => {
    const fake = new FakeDockviewApi();
    registerDockviewApi(asDockviewApi(fake));
    workspaceStore.getState().openPane("session", { ref: "local:parent" });
    const childId = workspaceStore.getState().openPane("session", { ref: "local:child" });
    const child = workspaceStore.getState().panes.find((pane) => pane.id === childId)!;
    recordConversationOpenParent(child, "local:parent");
    fake.toJSONResult = { panels: { [childId]: { params: {} } } };

    const json = workspaceStore.getState().layoutJSON() as {
      conversationParents?: Record<string, string>;
    };

    expect(json.conversationParents).toEqual({ [childId]: "local:parent" });
  });

  test("restoreLayout re-records the layout's conversation open-parent edges onto the restored panes", () => {
    const fake = new FakeDockviewApi();
    fake.fromJSONBehavior = () => {
      fake.panels = [
        { id: "p1", params: { paneType: "session", paneParams: { ref: "local:parent" } } },
        { id: "p2", params: { paneType: "session", paneParams: { ref: "local:child" } } },
      ];
      fake.activePanel = { id: "p2" };
    };
    registerDockviewApi(asDockviewApi(fake));

    const ok = workspaceStore.getState().restoreLayout({
      conversationParents: { p2: "local:parent" },
    });

    expect(ok).toBe(true);
    const restoredChild = workspaceStore.getState().panes.find((pane) => pane.id === "p2")!;
    expect(conversationOpenParentRef(restoredChild)).toBe("local:parent");
    const restoredParent = workspaceStore.getState().panes.find((pane) => pane.id === "p1")!;
    expect(conversationOpenParentRef(restoredParent)).toBeUndefined();
  });

  test("restoreLayout carries a legacy transcript panel's parentRef into the recorded conversation parent", () => {
    const fake = new FakeDockviewApi();
    let received: unknown;
    fake.fromJSONBehavior = (data) => {
      received = data;
      // The double fakes dockview's own rebuild of the MIGRATED layout, so
      // the panel here is already the session panel migrateLegacyLayout
      // turned it into; what this test proves is the parentRef extraction
      // the migration must do before dockview drops the old params.
      fake.panels = [
        { id: "p1", params: { paneType: "session", paneParams: { ref: "local:parent" } } },
        { id: "p2", params: { paneType: "session", paneParams: { ref: "local:child" } } },
      ];
      fake.activePanel = { id: "p2" };
    };
    registerDockviewApi(asDockviewApi(fake));

    const ok = workspaceStore.getState().restoreLayout({
      panels: {
        p1: { params: { paneType: "session", paneParams: { ref: "local:parent" } } },
        p2: { params: { paneType: "transcript", paneParams: { ref: "local:child", parentRef: "local:parent" } } },
      },
    });

    expect(ok).toBe(true);
    const migrated = received as {
      panels: Record<string, { params: { paneType: string; paneParams: { ref: string; parentRef?: string } } }>;
    };
    expect(migrated.panels.p2!.params.paneType).toBe("session");
    expect(migrated.panels.p2!.params.paneParams.parentRef).toBeUndefined();
    const restoredChild = workspaceStore.getState().panes.find((pane) => pane.id === "p2")!;
    expect(conversationOpenParentRef(restoredChild)).toBe("local:parent");
  });

  // A legacy cascade's `source` names the conversation it was opened from; on a
  // read-only layout that descriptor was a transcript carrying its parentRef.
  // parseZoomParams drops parentRef when the record is normalized, so the edge
  // must be lifted into the retained conversation metadata during migration or
  // Return's reopened conversation loses its Back target (Welcome on the phone).
  test("restoreLayout keeps a legacy cascade source's parentRef on the conversation Return reopens", () => {
    const fake = new FakeDockviewApi();
    fake.fromJSONBehavior = (data) => {
      const layout = data as { panels?: Record<string, { params?: unknown }> };
      fake.panels = Object.entries(layout.panels ?? {}).map(([id, panel]) => ({ id, params: panel.params }));
      fake.activePanel = { id: "cascade" };
    };
    registerDockviewApi(asDockviewApi(fake));

    const ok = workspaceStore.getState().restoreLayout({
      panels: {
        root: { params: { paneType: "session", paneParams: { ref: "local:root" } } },
        cascade: {
          params: {
            paneType: "sessionZoom",
            paneParams: {
              ref: "local:grandchild",
              source: { type: "transcript", params: { ref: "local:child", parentRef: "local:root" } },
              edges: [{ ownerRef: "local:child", childRef: "local:grandchild", delegateId: "d1" }],
            },
          },
        },
      },
    });
    expect(ok).toBe(true);

    // Return reopens the cascade's source conversation in the cascade's own
    // pane (actions.ts retypes it to `source.params`); the source's parent edge
    // must reach the session pane it becomes.
    const cascade = workspaceStore.getState().panes.find((pane) => pane.id === "cascade");
    if (!cascade) throw new Error("cascade pane missing");
    expect(workspaceStore.getState().retypePane(cascade, "session", { ref: "local:child" })).toBe(true);
    const reopened = workspaceStore.getState().panes.find((pane) => pane.id === "cascade");
    if (!reopened) throw new Error("reopened conversation missing");
    expect(conversationOpenParentRef(reopened)).toBe("local:root");
  });
});

describe("exact Open origin lifetime", () => {
  let restoreTranscript: () => void;
  beforeAll(() => {
    restoreTranscript = registerPaneForTests(fixtureDescriptor("transcript"));
  });
  afterAll(() => restoreTranscript());

  async function retainedOriginPair() {
    const { recordTranscriptOpenOrigin, transcriptOpenOrigin } = await import("./workspace");
    const workspace = workspaceStore.getState();
    const ownerId = workspace.openPane("session", { ref: "local:owner" });
    const leafId = workspace.openPane("transcript", { ref: "local:leaf", parentRef: "local:owner" });
    const owner = workspaceStore.getState().panes.find((pane) => pane.id === ownerId)!;
    const leaf = workspaceStore.getState().panes.find((pane) => pane.id === leafId)!;
    recordTranscriptOpenOrigin(leaf, owner);
    expect(transcriptOpenOrigin(leaf)).toBe(owner);
    return { workspace, owner, leaf, recordTranscriptOpenOrigin, transcriptOpenOrigin };
  }

  test.each(["origin", "child"])("closing the %s removes the edge without affecting retained panes", async (end) => {
    const { workspace, owner, leaf, transcriptOpenOrigin } = await retainedOriginPair();
    workspace.closePane(end === "origin" ? owner.id : leaf.id);
    expect(transcriptOpenOrigin(leaf)).toBeUndefined();
    const retained = end === "origin" ? leaf : owner;
    expect(workspaceStore.getState().panes).toEqual([retained]);
    const replacement = workspace.openPane(
      end === "origin" ? "session" : "transcript",
      end === "origin" ? owner.params : leaf.params,
    );
    expect(replacement).not.toBe(end === "origin" ? owner.id : leaf.id);
    expect(transcriptOpenOrigin(leaf)).toBeUndefined();
  });

  test("same primary and host unregistration keep edges; a new primary removes them", async () => {
    const { workspace, owner, leaf, transcriptOpenOrigin } = await retainedOriginPair();
    registerDockviewApi(asDockviewApi(new FakeDockviewApi()));
    registerDockviewApi(null);
    expect(workspace.replacePrimary("session", owner.params)).toBe(owner.id);
    expect(transcriptOpenOrigin(leaf)).toBe(owner);
    workspace.replacePrimary("session", { ref: "local:new-owner" });
    expect(transcriptOpenOrigin(leaf)).toBeUndefined();
    expect(workspaceStore.getState().panes).toHaveLength(1);
  });

  test("same-primary duplicate removal invalidates its origin edge without retargeting to main", async () => {
    const { workspace, owner, leaf, recordTranscriptOpenOrigin, transcriptOpenOrigin } = await retainedOriginPair();
    const duplicateId = workspace.openPane("session", { ref: "local:owner", extra: true });
    const duplicate = workspaceStore.getState().panes.find((pane) => pane.id === duplicateId)!;
    recordTranscriptOpenOrigin(leaf, duplicate);
    expect(transcriptOpenOrigin(leaf)).toBe(duplicate);
    workspace.replacePrimary("session", owner.params);
    expect(workspaceStore.getState().panes).toEqual([owner, leaf]);
    expect(transcriptOpenOrigin(leaf)).toBeUndefined();
  });

  test("reset cannot resurrect an origin when the next workspace reuses both IDs", async () => {
    const { owner, leaf, transcriptOpenOrigin } = await retainedOriginPair();
    resetWorkspaceStoreForTests();
    const workspace = workspaceStore.getState();
    expect(workspace.openPane("session", owner.params)).toBe(owner.id);
    expect(workspace.openPane("transcript", leaf.params)).toBe(leaf.id);
    const restoredLeaf = workspaceStore.getState().panes.find((pane) => pane.id === leaf.id)!;
    expect(transcriptOpenOrigin(leaf)).toBeUndefined();
    expect(transcriptOpenOrigin(restoredLeaf)).toBeUndefined();
  });

  test.each(["saved", "empty", "invalid"])(
    "live reconstruction with %s geometry retains exact records and document/transcript state",
    async (shape) => {
      const { openDocBeside } = await import("../panes/doc/openDoc");
      const { workspace, owner, leaf, transcriptOpenOrigin } = await retainedOriginPair();
      const reference = bindFilePath("/work/owner/docs/a.md:12", "/work/owner");
      if (!reference) throw new Error("reference did not bind");
      openDocBeside({ session: "local:owner", reference, sourcePaneId: owner.id });
      openDocBeside({ session: "local:owner", reference, sourcePaneId: owner.id });
      const before = workspaceStore.getState();
      const document = before.panes.find((pane) => pane.type === "doc");
      if (!document) throw new Error("document did not open");
      const documentState = documentPaneState(document);
      expect(documentState).toEqual({ reference, origin: owner, reopen: 1 });
      const fake = new FakeDockviewApi();
      registerDockviewApi(asDockviewApi(fake));
      registerDockviewApi(null);
      registerDockviewApi(asDockviewApi(fake));
      fake.fromJSONBehavior = () => {
        if (shape === "invalid") throw new Error("invalid layout");
        fake.panels =
          shape === "empty"
            ? []
            : before.panes.map((pane) => ({
                id: pane.id,
                params: { paneType: pane.type, paneParams: pane.params },
              }));
        fake.activePanel = { id: leaf.id };
      };

      expect(workspace.restoreLayout({}, { preserveLivePanes: true })).toBe(shape !== "invalid");

      expect(workspaceStore.getState().panes).toBe(before.panes);
      expect(workspaceStore.getState().focusedPaneId).toBe(document.id);
      expect(documentPaneState(document)).toBe(documentState);
      expect(transcriptOpenOrigin(leaf)).toBe(owner);
    },
  );

  test("live reconstruction does not resurrect closed panes from stale geometry or discard phone-created records", async () => {
    const { workspace, owner, leaf, transcriptOpenOrigin } = await retainedOriginPair();
    const fake = new FakeDockviewApi();
    registerDockviewApi(asDockviewApi(fake));
    registerDockviewApi(null);
    workspace.closePane(leaf.id);
    const replacementId = workspace.openPane("transcript", leaf.params);
    const replacement = workspaceStore.getState().panes.find((pane) => pane.id === replacementId);
    const before = workspaceStore.getState();
    fake.fromJSONBehavior = () => {
      fake.panels = [owner, leaf].map((pane) => ({
        id: pane.id,
        params: { paneType: pane.type, paneParams: pane.params },
      }));
      fake.activePanel = { id: leaf.id };
    };
    registerDockviewApi(asDockviewApi(fake));

    workspace.restoreLayout({}, { preserveLivePanes: true });

    expect(workspaceStore.getState().panes).toBe(before.panes);
    expect(workspaceStore.getState().panes).toContain(replacement);
    expect(workspaceStore.getState().panes).not.toContain(leaf);
    expect(transcriptOpenOrigin(leaf)).toBeUndefined();
    if (!replacement) throw new Error("replacement did not open");
    expect(transcriptOpenOrigin(replacement)).toBeUndefined();
    expect(workspaceStore.getState().focusedPaneId).toBe(replacementId);
  });

  test.each(["same identities", "reused identities", "invalid layout"])(
    "layout restoration with %s replaces origins only after successful restore",
    async (shape) => {
      const { workspace, owner, leaf, transcriptOpenOrigin } = await retainedOriginPair();
      const before = workspaceStore.getState();
      const fake = new FakeDockviewApi();
      fake.fromJSONBehavior = () => {
        if (shape === "invalid layout") throw new Error("invalid layout");
        fake.panels = [
          {
            id: owner.id,
            params: { paneType: shape === "reused identities" ? "doc" : owner.type, paneParams: owner.params },
          },
          { id: leaf.id, params: { paneType: leaf.type, paneParams: leaf.params } },
        ];
      };
      registerDockviewApi(asDockviewApi(fake));
      expect(workspace.restoreLayout({})).toBe(shape !== "invalid layout");
      if (shape === "invalid layout") {
        expect(workspaceStore.getState().panes).toBe(before.panes);
        expect(workspaceStore.getState().focusedPaneId).toBe(before.focusedPaneId);
        expect(transcriptOpenOrigin(leaf)).toBe(owner);
      } else {
        expect(transcriptOpenOrigin(leaf)).toBeUndefined();
        for (const pane of workspaceStore.getState().panes) expect(transcriptOpenOrigin(pane)).toBeUndefined();
      }
      expect(workspaceStore.getState().panes).toHaveLength(2);
    },
  );

  test("failed layout restoration preserves exact pane identities and their Open origin", async () => {
    const { workspace, owner, leaf, transcriptOpenOrigin } = await retainedOriginPair();
    const before = workspaceStore.getState();
    const fake = new FakeDockviewApi();
    fake.fromJSONBehavior = () => {
      throw new Error("invalid layout");
    };
    registerDockviewApi(asDockviewApi(fake));

    expect(workspace.restoreLayout({})).toBe(false);
    expect(workspaceStore.getState().panes).toBe(before.panes);
    expect(workspaceStore.getState().panes[0]).toBe(owner);
    expect(workspaceStore.getState().panes[1]).toBe(leaf);
    expect(workspaceStore.getState().focusedPaneId).toBe(before.focusedPaneId);
    expect(transcriptOpenOrigin(leaf)).toBe(owner);
  });
});

describe("currentSessionRef", () => {
  test("is the focused pane's session ref", () => {
    workspaceStore.getState().openPane("session", { ref: "local:a" });
    expect(currentSessionRef(workspaceStore.getState())).toBe("local:a");
  });

  test("follows focus to a secondary session pane", () => {
    workspaceStore.getState().openPane("session", { ref: "local:a" });
    workspaceStore.getState().openPane("session", { ref: "local:b" }, { slot: "secondary" });
    expect(currentSessionRef(workspaceStore.getState())).toBe("local:b");
  });

  test("falls back to the main pane's session when the focused pane names none", () => {
    workspaceStore.getState().openPane("session", { ref: "local:a" });
    const settings = workspaceStore.getState().openPane("settings", { section: "appearance" }, { slot: "secondary" });
    workspaceStore.getState().focusPane(settings);
    expect(currentSessionRef(workspaceStore.getState())).toBe("local:a");
  });

  test("is null when neither the focused nor the main pane names a session", () => {
    workspaceStore.getState().openPane("settings", { section: "appearance" });
    expect(currentSessionRef(workspaceStore.getState())).toBeNull();
  });

  test("is null for an empty workspace", () => {
    expect(currentSessionRef(workspaceStore.getState())).toBeNull();
  });

  test("a focused job-log transcript is about the session that owns the job, not the job ref", () => {
    workspaceStore.setState({
      panes: [{ id: "p1", type: "transcript", params: { ref: "job:job_x", parentRef: "local:s" }, slot: "main" }],
      focusedPaneId: "p1",
    });
    expect(currentSessionRef(workspaceStore.getState())).toBe("local:s");
  });

  test("a focused doc pane names its session under `session`", () => {
    workspaceStore.setState({
      panes: [{ id: "p1", type: "doc", params: { session: "local:d", path: "README.md", kind: "file" }, slot: "main" }],
      focusedPaneId: "p1",
    });
    expect(currentSessionRef(workspaceStore.getState())).toBe("local:d");
  });

  test("a focused pane about no session falls through to the main pane", () => {
    workspaceStore.setState({
      panes: [
        { id: "m", type: "session", params: { ref: "local:a" }, slot: "main" },
        { id: "t", type: "transcript", params: { ref: "job:job_x" }, slot: "secondary" },
      ],
      focusedPaneId: "t",
    });
    expect(currentSessionRef(workspaceStore.getState())).toBe("local:a");
  });
});

// A second pane on the same conversation is a distinct reader with its own
// selection scope, and a retarget (drill/return) keeps the pane's own seat.
test("independent panes on one conversation never share a recovery seat", () => {
  installLocalStorage(new MemoryStorage());
  const workspace = workspaceStore.getState();
  const mainId = workspace.replacePrimary("session", { ref: "local:shared" });
  const otherId = workspace.openPane("doc", { ref: "doc" });
  const other = workspaceStore.getState().panes.find((pane) => pane.id === otherId);
  if (!other) throw new Error("missing doc pane");
  expect(workspace.retypePane(other, "session", { ref: "local:shared" })).toBe(true);

  const sessions = workspaceStore
    .getState()
    .panes.filter((pane) => pane.type === "session" && (pane.params as { ref?: unknown }).ref === "local:shared");
  expect(sessions.map((pane) => pane.id).sort()).toEqual([mainId, otherId].sort());
  const seats = sessions.map((pane) => pane.composerSeatId);
  expect(seats.every((seat) => typeof seat === "string" && seat !== "")).toBe(true);
  expect(new Set(seats).size).toBe(2);

  // Retarget the secondary into a cascade: the pane keeps its own seat, so the
  // two live panes still hold distinct scopes.
  const secondary = sessions.find((pane) => pane.id === otherId);
  if (!secondary) throw new Error("missing secondary session pane");
  const keptSeat = secondary.composerSeatId;
  expect(
    workspace.retypePane(secondary, "sessionZoom", {
      ref: "local:shared",
      source: { type: "session", params: { ref: "local:shared" } },
      edges: [],
    }),
  ).toBe(true);
  expect(workspaceStore.getState().panes.find((pane) => pane.id === otherId)?.composerSeatId).toBe(keptSeat);
  const liveSeats = workspaceStore
    .getState()
    .panes.filter((pane) => pane.type === "session" || pane.type === "sessionZoom")
    .map((pane) => pane.composerSeatId);
  expect(new Set(liveSeats).size).toBe(liveSeats.length);
});

test("a child opened beside a retained cascade gets its own recovery seat", () => {
  installLocalStorage(new MemoryStorage());
  const workspace = workspaceStore.getState();
  const ownerRef = "local:owner";
  const childRef = "local:child";
  const ownerId = workspace.openPane("session", { ref: ownerRef });
  const owner = workspaceStore.getState().panes.find((pane) => pane.id === ownerId);
  if (!owner) throw new Error("missing owner pane");
  expect(
    workspace.retypePane(owner, "sessionZoom", {
      ref: childRef,
      source: { type: "session", params: { ref: ownerRef } },
      edges: [],
    }),
  ).toBe(true);
  const cascade = workspaceStore.getState().panes.find((pane) => pane.id === ownerId);
  if (!cascade?.composerSeatId) throw new Error("retained cascade lacks its seat");
  writeRecoverySelection(childRef, composerSelectionScope(cascade.composerSeatId), "preserved-child");

  openNestedSessionWithOwner(childRef, ownerRef);

  const child = workspaceStore
    .getState()
    .panes.find(
      (pane) => pane.id !== ownerId && pane.type === "session" && (pane.params as { ref?: unknown }).ref === childRef,
    );
  if (!child?.composerSeatId) throw new Error("nested child lacks its seat");
  expect(child.composerSeatId).not.toBe(cascade.composerSeatId);
  // The child receives a copy of the selection without moving the cascade's.
  expect(readRecoverySelection(childRef, composerSelectionScope(child.composerSeatId))).toBe("preserved-child");
  expect(readRecoverySelection(childRef, composerSelectionScope(cascade.composerSeatId))).toBe("preserved-child");
  const liveSeats = workspaceStore
    .getState()
    .panes.filter((pane) => pane.type === "session" || pane.type === "sessionZoom")
    .map((pane) => pane.composerSeatId ?? pane.id);
  expect(new Set(liveSeats).size).toBe(liveSeats.length);
});

// Pane ids restart after a mobile reload. A route reconstructed as B can land
// on the id A used before, whose stored pane mapping still names A's seat.
// The conversation mapping must win for a reconstructed route.
test("a reconstructed route keeps its own seat over a colliding stale pane id", () => {
  installLocalStorage(new MemoryStorage());
  const workspace = workspaceStore.getState();
  const aId = workspace.openPane("session", { ref: "local:route-a" });
  const a = workspaceStore.getState().panes.find((pane) => pane.id === aId);
  if (!a?.composerSeatId) throw new Error("route A lacks its seat");
  writeRecoverySelection("local:route-a", composerSelectionScope(a.composerSeatId), "a-row");
  const bId = workspace.openPane("session", { ref: "local:route-b" });
  const b = workspaceStore.getState().panes.find((pane) => pane.id === bId);
  if (!b?.composerSeatId) throw new Error("route B lacks its seat");
  writeRecoverySelection("local:route-b", composerSelectionScope(b.composerSeatId), "b-row");

  // Reload: pane ids restart, and route B is reconstructed on A's former id.
  resetWorkspaceStoreForTests();
  const reloadedId = workspaceStore.getState().openPane("session", { ref: "local:route-b" });
  expect(reloadedId).toBe(aId);
  const reloaded = workspaceStore.getState().panes.find((pane) => pane.id === reloadedId);
  if (!reloaded?.composerSeatId) throw new Error("reloaded route lacks its seat");
  expect(reloaded.composerSeatId).toBe(b.composerSeatId);
  expect(readRecoverySelection("local:route-b", composerSelectionScope(reloaded.composerSeatId))).toBe("b-row");
  expect(readRecoverySelection("local:route-a", composerSelectionScope(reloaded.composerSeatId))).toBeUndefined();
});

// A retarget that retains its seat (resume, then return to the resumed
// session) must also persist the destination ref's mapping. Otherwise a mobile
// reload reconstructs the resumed ref with a fresh seat and loses the
// selection the retained seat still holds.
test("a retained seat on a retargeted conversation survives mobile reload", () => {
  installLocalStorage(new MemoryStorage());
  const workspace = workspaceStore.getState();
  workspace.openPane("session", { ref: "local:owner" });
  const cascadeId = workspace.openPane(
    "sessionZoom",
    { ref: "local:child", source: { type: "session", params: { ref: "local:owner" } }, edges: [] },
    { slot: "secondary" },
  );
  const cascade = workspaceStore.getState().panes.find((pane) => pane.id === cascadeId);
  if (!cascade?.composerSeatId) throw new Error("cascade lacks its seat");
  const seat = cascade.composerSeatId;
  writeRecoverySelection("local:resumed", composerSelectionScope(seat), "resumed-row");

  // Resume and return retain the cascade's seat while the ref changes.
  expect(workspace.retypePane(cascade, "session", { ref: "local:resumed" })).toBe(true);
  const returned = workspaceStore.getState().panes.find((pane) => pane.id === cascadeId);
  expect(returned?.composerSeatId).toBe(seat);

  // Mobile reload reconstructs the resumed ref under a fresh pane id.
  resetWorkspaceStoreForTests();
  const reloadedId = workspaceStore.getState().openPane("session", { ref: "local:resumed" });
  const reloaded = workspaceStore.getState().panes.find((pane) => pane.id === reloadedId);
  if (!reloaded?.composerSeatId) throw new Error("reloaded conversation lacks its seat");
  expect(reloaded.composerSeatId).toBe(seat);
  expect(readRecoverySelection("local:resumed", composerSelectionScope(reloaded.composerSeatId))).toBe("resumed-row");
});

test("two cascade columns keep independent selections under one seat", () => {
  installLocalStorage(new MemoryStorage());
  const ownerRef = "local:cascade-column-owner";
  const childRef = "local:cascade-column-child";
  const params = {
    ref: childRef,
    source: { type: "session" as const, params: { ref: ownerRef } },
    edges: [{ ownerRef, childRef, delegateId: "delegate-1" }],
  };
  const cascadeId = workspaceStore.getState().openPane("sessionZoom", params);
  const cascade = workspaceStore.getState().panes.find((pane) => pane.id === cascadeId);
  if (!cascade?.composerSeatId) throw new Error("cascade lacks its seat");
  const seat = cascade.composerSeatId;
  const lifetime = conversationPaneLifetime(cascade);
  const ownerColumn = retainedConversationComposer(lifetime, ownerRef);
  const childColumn = retainedConversationComposer(lifetime, childRef);
  if (!ownerColumn || !childColumn) throw new Error("cascade columns lack composers");
  expect(ownerColumn.recoverySelectionOwnerId).toBe(childColumn.recoverySelectionOwnerId);

  writeRecoverySelection(ownerRef, composerSelectionScope(seat), "owner-selected-row");
  writeRecoverySelection(childRef, composerSelectionScope(seat), "child-selected-row");

  // Reload reconstructs the cascade under a fresh pane id. Its columns still
  // share the tab-scoped seat, and each ref restores its own selection.
  resetWorkspaceStoreForTests();
  const reloadedId = workspaceStore.getState().openPane("sessionZoom", params);
  const reloaded = workspaceStore.getState().panes.find((pane) => pane.id === reloadedId);
  if (!reloaded?.composerSeatId) throw new Error("reloaded cascade lacks its seat");
  expect(reloaded.composerSeatId).toBe(seat);
  const reloadedLifetime = conversationPaneLifetime(reloaded);
  const reloadedOwner = retainedConversationComposer(reloadedLifetime, ownerRef);
  const reloadedChild = retainedConversationComposer(reloadedLifetime, childRef);
  if (!reloadedOwner || !reloadedChild) throw new Error("reloaded columns lack composers");
  expect(reloadedOwner.recoverySelectionOwnerId).toBe(reloadedChild.recoverySelectionOwnerId);
  expect(readRecoverySelection(ownerRef, reloadedOwner.recoverySelectionOwnerId)).toBe("owner-selected-row");
  expect(readRecoverySelection(childRef, reloadedChild.recoverySelectionOwnerId)).toBe("child-selected-row");
  expect(readRecoverySelection(ownerRef, reloadedOwner.recoverySelectionOwnerId)).not.toBe("child-selected-row");
});

// A cascade column retained for an ancestor conversation owns a composer with
// the pane's seat, but no pane ever held that ref, so nothing else establishes
// its ref-to-seat mapping. Selecting a recovery in that column must map the
// ref to the seat, or closing the cascade and reopening the ancestor
// allocates a fresh seat whose stored selection cannot be found.
test("reopening a never-leaf cascade ancestor restores its retained column's selection", () => {
  installLocalStorage(new MemoryStorage());
  const ownerRef = "local:never-leaf-owner";
  const childRef = "local:never-leaf-child";
  const params = {
    ref: childRef,
    source: { type: "session" as const, params: { ref: ownerRef } },
    edges: [{ ownerRef, childRef, delegateId: "delegate-never-leaf" }],
  };
  const cascadeId = workspaceStore.getState().openPane("sessionZoom", params);
  const cascade = workspaceStore.getState().panes.find((pane) => pane.id === cascadeId);
  if (!cascade?.composerSeatId) throw new Error("cascade lacks its seat");
  const seat = cascade.composerSeatId;
  const lifetime = conversationPaneLifetime(cascade);
  const ancestorColumn = retainedConversationComposer(lifetime, ownerRef);
  if (!ancestorColumn) throw new Error("ancestor column lacks a composer");

  // The user selects a recovery row in the cascade's parent column.
  const selected: MutationRecoveryRecord = {
    version: 1,
    clientMutationId: "ancestor-selected-row",
    targetRef: ownerRef,
    intentSequence: 1,
    createdAt: 0,
    method: "turn/start",
    payload: { ref: ownerRef, input: [] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [] },
    composerText: "",
    state: "submitting",
    recoveryKind: "rejected",
  };
  ancestorColumn.setActiveRecoveryId(selected);
  expect(readRefComposerSeat(ownerRef)).toBe(seat);
  expect(readRecoverySelection(ownerRef, composerSelectionScope(seat))).toBe("ancestor-selected-row");

  // Closing the cascade and reopening the ancestor as its own pane reuses the
  // retained seat, restoring the saved selection instead of a fresh seat that
  // cannot discover it.
  workspaceStore.getState().closePane(cascadeId);
  const reopenedId = workspaceStore.getState().openPane("session", { ref: ownerRef });
  const reopened = workspaceStore.getState().panes.find((pane) => pane.id === reopenedId);
  if (!reopened?.composerSeatId) throw new Error("reopened ancestor lacks its seat");
  expect(reopened.composerSeatId).toBe(seat);
  expect(readRecoverySelection(ownerRef, composerSelectionScope(reopened.composerSeatId))).toBe(
    "ancestor-selected-row",
  );
});

// An independently open pane that owns the ancestor ref established its own
// seat mapping; a retained column selecting a recovery must keep it.
test("a retained column keeps an independently open owner's seat mapping", () => {
  installLocalStorage(new MemoryStorage());
  const ownerRef = "local:independent-owner";
  const childRef = "local:independent-child";
  const independentId = workspaceStore.getState().openPane("session", { ref: ownerRef });
  const independent = workspaceStore.getState().panes.find((pane) => pane.id === independentId);
  if (!independent?.composerSeatId) throw new Error("independent pane lacks its seat");
  const params = {
    ref: childRef,
    source: { type: "session" as const, params: { ref: ownerRef } },
    edges: [{ ownerRef, childRef, delegateId: "delegate-independent-owner" }],
  };
  const cascadeId = workspaceStore.getState().openPane("sessionZoom", params);
  const cascade = workspaceStore.getState().panes.find((pane) => pane.id === cascadeId);
  if (!cascade) throw new Error("cascade missing");
  const ancestorColumn = retainedConversationComposer(conversationPaneLifetime(cascade), ownerRef);
  if (!ancestorColumn) throw new Error("ancestor column lacks a composer");

  const selected: MutationRecoveryRecord = {
    version: 1,
    clientMutationId: "independent-owner-selected-row",
    targetRef: ownerRef,
    intentSequence: 1,
    createdAt: 0,
    method: "turn/start",
    payload: { ref: ownerRef, input: [] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [] },
    composerText: "",
    state: "submitting",
    recoveryKind: "rejected",
  };
  ancestorColumn.setActiveRecoveryId(selected);
  expect(readRefComposerSeat(ownerRef)).toBe(independent.composerSeatId);
  expect(readRecoverySelection(ownerRef, composerSelectionScope(cascade.composerSeatId ?? cascade.id))).toBe(
    "independent-owner-selected-row",
  );
});

// A pane retyped to a read-only transcript keeps its lifetime's seat in
// reserve. Another pane opening the same conversation must receive a distinct
// seat, or the two independent panes would share one recovery-selection scope
// and clobber each other's stored selection.
test("reopening a conversation retyped to a transcript receives a distinct seat", () => {
  installLocalStorage(new MemoryStorage());
  const ref = "local:retained-seat-reservation";
  const firstId = workspaceStore.getState().openPane("session", { ref });
  const first = workspaceStore.getState().panes.find((pane) => pane.id === firstId);
  if (!first?.composerSeatId) throw new Error("first pane lacks its seat");
  const seat = first.composerSeatId;

  // The pane becomes a legacy transcript: the lifetime retains the seat, the
  // pane record loses it.
  expect(workspaceStore.getState().retypePane(first, "transcript", { ref })).toBe(true);
  const retyped = workspaceStore.getState().panes.find((pane) => pane.id === firstId);
  if (!retyped) throw new Error("retyped pane missing");
  expect(retyped.composerSeatId).toBeUndefined();

  // A different pane reopening that conversation cannot take the reserved
  // seat.
  const secondId = workspaceStore.getState().openPane("session", { ref });
  const second = workspaceStore.getState().panes.find((pane) => pane.id === secondId);
  if (!second?.composerSeatId) throw new Error("second pane lacks its seat");
  expect(second.composerSeatId).not.toBe(seat);

  // Returning the original pane to a conversation restores its own retained
  // seat; both panes keep independent scopes.
  expect(workspaceStore.getState().retypePane(retyped, "session", { ref })).toBe(true);
  const returned = workspaceStore.getState().panes.find((pane) => pane.id === firstId);
  if (!returned?.composerSeatId) throw new Error("returned pane lacks its seat");
  expect(returned.composerSeatId).toBe(seat);
  expect(second.composerSeatId).not.toBe(seat);
});

test("closing a competing seat restores the surviving pane's selection", () => {
  installLocalStorage(new MemoryStorage());
  const ref = "local:surviving-seat";
  const mainId = workspaceStore.getState().replacePrimary("session", { ref });
  const main = workspaceStore.getState().panes.find((pane) => pane.id === mainId);
  if (!main?.composerSeatId) throw new Error("main pane lacks its seat");
  const secondaryId = workspaceStore.getState().openPane("doc", { ref: "doc" });
  const secondaryPane = workspaceStore.getState().panes.find((pane) => pane.id === secondaryId);
  if (!secondaryPane) throw new Error("secondary pane missing");
  expect(workspaceStore.getState().retypePane(secondaryPane, "session", { ref })).toBe(true);
  const secondary = workspaceStore.getState().panes.find((pane) => pane.id === secondaryId);
  if (!secondary?.composerSeatId) throw new Error("secondary session lacks its seat");
  expect(secondary.composerSeatId).not.toBe(main.composerSeatId);

  writeRecoverySelection(ref, composerSelectionScope(main.composerSeatId), "main-selected-row");
  writeRecoverySelection(ref, composerSelectionScope(secondary.composerSeatId), "secondary-selected-row");

  // The newer pane's allocation owns the conversation mapping until it closes.
  workspaceStore.getState().closePane(secondaryId);
  resetWorkspaceStoreForTests();
  const reloadedId = workspaceStore.getState().openPane("session", { ref });
  const reloaded = workspaceStore.getState().panes.find((pane) => pane.id === reloadedId);
  if (!reloaded?.composerSeatId) throw new Error("reloaded conversation lacks its seat");
  expect(reloaded.composerSeatId).toBe(main.composerSeatId);
  expect(readRecoverySelection(ref, composerSelectionScope(reloaded.composerSeatId))).toBe("main-selected-row");
});

test("a legacy transcript round trip keeps the pane's seat", () => {
  installLocalStorage(new MemoryStorage());
  const ref = "local:transcript-round-trip";
  const id = workspaceStore.getState().openPane("session", { ref });
  const session = workspaceStore.getState().panes.find((pane) => pane.id === id);
  if (!session?.composerSeatId) throw new Error("session lacks its seat");
  const seat = session.composerSeatId;

  expect(workspaceStore.getState().retypePane(session, "transcript", { ref })).toBe(true);
  const transcript = workspaceStore.getState().panes.find((pane) => pane.id === id);
  expect(transcript?.composerSeatId).toBeUndefined();
  if (!transcript) throw new Error("transcript pane missing");
  // Retyping to the read-only transcript does not close the conversation: the
  // stored mapping stays so a later reopen restores the seat's recovery row.
  expect(readRefComposerSeat(ref)).toBe(seat);

  // Another transition may have moved the conversation mapping while the
  // transcript was showing. Returning to the session keeps the lifetime seat.
  writeRefComposerSeat(ref, "competing-seat");
  expect(workspaceStore.getState().retypePane(transcript, "session", { ref })).toBe(true);
  const returned = workspaceStore.getState().panes.find((pane) => pane.id === id);
  expect(returned?.composerSeatId).toBe(seat);
  expect(readRefComposerSeat(ref)).toBe(seat);
});

test("replacePrimary reconciles the surviving main seat mapping", () => {
  installLocalStorage(new MemoryStorage());
  const ref = "local:primary-map";
  const mainId = workspaceStore.getState().replacePrimary("session", { ref });
  const main = workspaceStore.getState().panes.find((pane) => pane.id === mainId);
  if (!main?.composerSeatId) throw new Error("main pane lacks its seat");
  const secondaryId = workspaceStore.getState().openPane("doc", { ref: "doc" });
  const secondaryPane = workspaceStore.getState().panes.find((pane) => pane.id === secondaryId);
  if (!secondaryPane) throw new Error("secondary pane missing");
  expect(workspaceStore.getState().retypePane(secondaryPane, "session", { ref })).toBe(true);
  const secondary = workspaceStore.getState().panes.find((pane) => pane.id === secondaryId);
  if (!secondary?.composerSeatId) throw new Error("secondary session lacks its seat");
  writeRecoverySelection(ref, composerSelectionScope(main.composerSeatId), "main-row");
  writeRecoverySelection(ref, composerSelectionScope(secondary.composerSeatId), "secondary-row");
  expect(readRefComposerSeat(ref)).toBe(secondary.composerSeatId);

  expect(workspaceStore.getState().replacePrimary("session", { ref })).toBe(mainId);
  expect(readRefComposerSeat(ref)).toBe(main.composerSeatId);
  resetWorkspaceStoreForTests();
  const reloadedId = workspaceStore.getState().openPane("session", { ref });
  const reloaded = workspaceStore.getState().panes.find((pane) => pane.id === reloadedId);
  if (!reloaded?.composerSeatId) throw new Error("reloaded conversation lacks its seat");
  expect(reloaded.composerSeatId).toBe(main.composerSeatId);
  expect(readRecoverySelection(ref, composerSelectionScope(reloaded.composerSeatId))).toBe("main-row");
});

test("focus and promote reconcile the conversation seat mapping", () => {
  installLocalStorage(new MemoryStorage());
  const ref = "local:focus-map";
  const mainId = workspaceStore.getState().replacePrimary("session", { ref });
  const main = workspaceStore.getState().panes.find((pane) => pane.id === mainId);
  if (!main?.composerSeatId) throw new Error("main pane lacks its seat");
  const secondaryId = workspaceStore.getState().openPane("doc", { ref: "doc" });
  const secondaryPane = workspaceStore.getState().panes.find((pane) => pane.id === secondaryId);
  if (!secondaryPane) throw new Error("secondary pane missing");
  expect(workspaceStore.getState().retypePane(secondaryPane, "session", { ref })).toBe(true);
  const secondary = workspaceStore.getState().panes.find((pane) => pane.id === secondaryId);
  if (!secondary?.composerSeatId) throw new Error("secondary session lacks its seat");

  workspaceStore.getState().focusPane(mainId);
  expect(readRefComposerSeat(ref)).toBe(main.composerSeatId);
  workspaceStore.getState().promotePane(secondaryId);
  expect(readRefComposerSeat(ref)).toBe(secondary.composerSeatId);
});

test("opening an existing conversation reconciles its seat mapping", () => {
  installLocalStorage(new MemoryStorage());
  const ref = "local:open-existing";
  const mainId = workspaceStore.getState().replacePrimary("session", { ref });
  const main = workspaceStore.getState().panes.find((pane) => pane.id === mainId);
  if (!main?.composerSeatId) throw new Error("main pane lacks its seat");
  const cascadeId = workspaceStore.getState().openPane("doc", { ref: "doc" });
  const cascadePane = workspaceStore.getState().panes.find((pane) => pane.id === cascadeId);
  if (!cascadePane) throw new Error("cascade pane missing");
  expect(
    workspaceStore.getState().retypePane(cascadePane, "sessionZoom", {
      ref,
      source: { type: "session", params: { ref } },
      edges: [],
    }),
  ).toBe(true);
  const cascade = workspaceStore.getState().panes.find((pane) => pane.id === cascadeId);
  if (!cascade?.composerSeatId) throw new Error("cascade lacks its seat");
  workspaceStore.getState().focusPane(cascadeId);
  expect(readRefComposerSeat(ref)).toBe(cascade.composerSeatId);

  expect(workspaceStore.getState().openPane("session", { ref })).toBe(mainId);
  expect(readRefComposerSeat(ref)).toBe(main.composerSeatId);
  resetWorkspaceStoreForTests();
  const reloadedId = workspaceStore.getState().openPane("session", { ref });
  const reloaded = workspaceStore.getState().panes.find((pane) => pane.id === reloadedId);
  if (!reloaded?.composerSeatId) throw new Error("reloaded conversation lacks its seat");
  expect(reloaded.composerSeatId).toBe(main.composerSeatId);
});

type SeatTransitionContext = { mainId: string; secondaryId: string; ref: string };

test.each<[string, (context: SeatTransitionContext) => void]>([
  ["focus", ({ mainId }) => workspaceStore.getState().focusPane(mainId)],
  ["open", ({ ref }) => void workspaceStore.getState().openPane("session", { ref })],
  ["close", ({ secondaryId }) => workspaceStore.getState().closePane(secondaryId)],
  ["replacePrimary", ({ ref }) => void workspaceStore.getState().replacePrimary("session", { ref })],
])("%s reconciles the conversation seat mapping", (_name, transition) => {
  installLocalStorage(new MemoryStorage());
  const ref = `local:transition-${_name}`;
  const mainId = workspaceStore.getState().replacePrimary("session", { ref });
  const main = workspaceStore.getState().panes.find((pane) => pane.id === mainId);
  if (!main?.composerSeatId) throw new Error("main pane lacks its seat");
  const secondaryId = workspaceStore.getState().openPane("doc", { ref: "doc" });
  const secondaryPane = workspaceStore.getState().panes.find((pane) => pane.id === secondaryId);
  if (!secondaryPane) throw new Error("secondary pane missing");
  expect(workspaceStore.getState().retypePane(secondaryPane, "session", { ref })).toBe(true);
  const secondary = workspaceStore.getState().panes.find((pane) => pane.id === secondaryId);
  if (!secondary?.composerSeatId) throw new Error("secondary session lacks its seat");
  expect(readRefComposerSeat(ref)).toBe(secondary.composerSeatId);

  transition({ mainId, secondaryId, ref });
  expect(readRefComposerSeat(ref)).toBe(main.composerSeatId);
  resetWorkspaceStoreForTests();
  const reloadedId = workspaceStore.getState().openPane("session", { ref });
  const reloaded = workspaceStore.getState().panes.find((pane) => pane.id === reloadedId);
  if (!reloaded?.composerSeatId) throw new Error("reloaded conversation lacks its seat");
  expect(reloaded.composerSeatId).toBe(main.composerSeatId);
});

test("retyping a conversation away reconciles its old ref mapping", () => {
  installLocalStorage(new MemoryStorage());
  const oldRef = "local:sweep-old-ref";
  const newRef = "local:sweep-new-ref";
  const mainId = workspaceStore.getState().replacePrimary("session", { ref: oldRef });
  const main = workspaceStore.getState().panes.find((pane) => pane.id === mainId);
  if (!main?.composerSeatId) throw new Error("main pane lacks its seat");
  const secondaryId = workspaceStore.getState().openPane("doc", { ref: "doc" });
  const secondaryPane = workspaceStore.getState().panes.find((pane) => pane.id === secondaryId);
  if (!secondaryPane) throw new Error("secondary pane missing");
  expect(workspaceStore.getState().retypePane(secondaryPane, "session", { ref: oldRef })).toBe(true);
  const secondary = workspaceStore.getState().panes.find((pane) => pane.id === secondaryId);
  if (!secondary?.composerSeatId) throw new Error("secondary session lacks its seat");
  expect(readRefComposerSeat(oldRef)).toBe(secondary.composerSeatId);

  expect(workspaceStore.getState().retypePane(secondary, "session", { ref: newRef })).toBe(true);
  expect(readRefComposerSeat(oldRef)).toBe(main.composerSeatId);
  expect(readRefComposerSeat(newRef)).toBe(secondary.composerSeatId);
  resetWorkspaceStoreForTests();
  const reloadedOld = workspaceStore.getState().openPane("session", { ref: oldRef });
  const reloadedPane = workspaceStore.getState().panes.find((pane) => pane.id === reloadedOld);
  if (!reloadedPane?.composerSeatId) throw new Error("reloaded old conversation lacks its seat");
  expect(reloadedPane.composerSeatId).toBe(main.composerSeatId);
});

test("drilling the last pane off a conversation keeps its seat mapping recoverable", () => {
  installLocalStorage(new MemoryStorage());
  const parentRef = "local:drill-parent";
  const childRef = "local:drill-child";
  const mainId = workspaceStore.getState().replacePrimary("session", { ref: parentRef });
  const main = workspaceStore.getState().panes.find((pane) => pane.id === mainId);
  if (!main?.composerSeatId) throw new Error("main pane lacks its seat");
  const seat = main.composerSeatId;
  writeRecoverySelection(parentRef, composerSelectionScope(seat), "parent-selected-row");
  // A rejected row under another seat: a fresh seat must never fall back to it.
  writeRecoverySelection(parentRef, composerSelectionScope("rejected-parent-seat"), "rejected-parent-row");

  // Drilling the last pane on the parent conversation leaves the parent as a
  // retained cascade column, not a closed conversation.
  expect(
    workspaceStore.getState().retypePane(main, "sessionZoom", {
      ref: childRef,
      source: { type: "session", params: { ref: parentRef } },
      edges: [{ ownerRef: parentRef, childRef, delegateId: "delegate-1" }],
    }),
  ).toBe(true);
  expect(readRefComposerSeat(parentRef)).toBe(seat);

  // After a mobile reload, reopening the parent conversation reconstructs
  // the retained column's seat and its selected row, not a fresh seat.
  resetWorkspaceStoreForTests();
  const reopenedId = workspaceStore.getState().openPane("session", { ref: parentRef });
  const reopened = workspaceStore.getState().panes.find((pane) => pane.id === reopenedId);
  if (!reopened?.composerSeatId) throw new Error("reopened parent lacks its seat");
  expect(reopened.composerSeatId).toBe(seat);
  expect(readRecoverySelection(parentRef, composerSelectionScope(reopened.composerSeatId))).toBe("parent-selected-row");
});

test("drilling a cascade deeper keeps the intermediate column's seat mapping", () => {
  installLocalStorage(new MemoryStorage());
  const parentRef = "local:deeper-parent";
  const childRef = "local:deeper-child";
  const grandchildRef = "local:deeper-grandchild";
  const cascadeId = workspaceStore.getState().openPane("sessionZoom", {
    ref: childRef,
    source: { type: "session", params: { ref: parentRef } },
    edges: [{ ownerRef: parentRef, childRef, delegateId: "delegate-1" }],
  });
  const cascade = workspaceStore.getState().panes.find((pane) => pane.id === cascadeId);
  if (!cascade?.composerSeatId) throw new Error("cascade lacks its seat");
  const seat = cascade.composerSeatId;
  writeRecoverySelection(childRef, composerSelectionScope(seat), "child-selected-row");
  writeRecoverySelection(childRef, composerSelectionScope("rejected-child-seat"), "rejected-child-row");

  expect(
    workspaceStore.getState().retypePane(cascade, "sessionZoom", {
      ref: grandchildRef,
      source: { type: "session", params: { ref: parentRef } },
      edges: [
        { ownerRef: parentRef, childRef, delegateId: "delegate-1" },
        { ownerRef: childRef, childRef: grandchildRef, delegateId: "delegate-2" },
      ],
    }),
  ).toBe(true);
  // The child remains a retained column of the drilled cascade.
  expect(readRefComposerSeat(childRef)).toBe(seat);

  resetWorkspaceStoreForTests();
  const reopenedId = workspaceStore.getState().openPane("session", { ref: childRef });
  const reopened = workspaceStore.getState().panes.find((pane) => pane.id === reopenedId);
  if (!reopened?.composerSeatId) throw new Error("reopened child lacks its seat");
  expect(reopened.composerSeatId).toBe(seat);
  expect(readRecoverySelection(childRef, composerSelectionScope(reopened.composerSeatId))).toBe("child-selected-row");
});

test("reconciling a retyped ref points it at the cascade that retains it", () => {
  installLocalStorage(new MemoryStorage());
  const ownerRef = "local:retain-owner";
  const childRef = "local:retain-child";
  const movedRef = "local:retain-moved";
  const sessionId = workspaceStore.getState().replacePrimary("session", { ref: ownerRef });
  const session = workspaceStore.getState().panes.find((pane) => pane.id === sessionId);
  if (!session?.composerSeatId) throw new Error("session pane lacks its seat");
  const cascadeId = workspaceStore.getState().openPane(
    "sessionZoom",
    {
      ref: childRef,
      source: { type: "session", params: { ref: ownerRef } },
      edges: [{ ownerRef, childRef, delegateId: "delegate-1" }],
    },
    { slot: "secondary" },
  );
  const cascade = workspaceStore.getState().panes.find((pane) => pane.id === cascadeId);
  if (!cascade?.composerSeatId) throw new Error("cascade lacks its seat");
  writeRecoverySelection(ownerRef, composerSelectionScope(cascade.composerSeatId), "cascade-owner-row");
  writeRecoverySelection(ownerRef, composerSelectionScope("rejected-owner-seat"), "rejected-owner-row");

  expect(workspaceStore.getState().retypePane(session, "session", { ref: movedRef })).toBe(true);
  // No pane shows the owner conversation anymore, but the cascade retains it
  // as a column: its seat owns the ref mapping, not the moved pane's.
  expect(readRefComposerSeat(ownerRef)).toBe(cascade.composerSeatId);

  resetWorkspaceStoreForTests();
  const reopenedId = workspaceStore.getState().openPane("session", { ref: ownerRef });
  const reopened = workspaceStore.getState().panes.find((pane) => pane.id === reopenedId);
  if (!reopened?.composerSeatId) throw new Error("reopened owner lacks its seat");
  expect(reopened.composerSeatId).toBe(cascade.composerSeatId);
  expect(readRecoverySelection(ownerRef, composerSelectionScope(reopened.composerSeatId))).toBe("cascade-owner-row");
});

test("closing a competing pane points its ref at the cascade that retains it", () => {
  installLocalStorage(new MemoryStorage());
  const ownerRef = "local:close-owner";
  const childRef = "local:close-child";
  const sessionId = workspaceStore.getState().replacePrimary("session", { ref: ownerRef });
  const session = workspaceStore.getState().panes.find((pane) => pane.id === sessionId);
  if (!session?.composerSeatId) throw new Error("session pane lacks its seat");
  const cascadeId = workspaceStore.getState().openPane(
    "sessionZoom",
    {
      ref: childRef,
      source: { type: "session", params: { ref: ownerRef } },
      edges: [{ ownerRef, childRef, delegateId: "delegate-1" }],
    },
    { slot: "secondary" },
  );
  const cascade = workspaceStore.getState().panes.find((pane) => pane.id === cascadeId);
  if (!cascade?.composerSeatId) throw new Error("cascade lacks its seat");
  writeRecoverySelection(ownerRef, composerSelectionScope(cascade.composerSeatId), "cascade-owner-row");
  writeRecoverySelection(ownerRef, composerSelectionScope("rejected-owner-seat"), "rejected-owner-row");

  workspaceStore.getState().closePane(sessionId);
  // No pane shows the owner conversation anymore, but the cascade retains it
  // as a column: its seat owns the ref mapping, not the closed pane's.
  expect(readRefComposerSeat(ownerRef)).toBe(cascade.composerSeatId);

  resetWorkspaceStoreForTests();
  const reopenedId = workspaceStore.getState().openPane("session", { ref: ownerRef });
  const reopened = workspaceStore.getState().panes.find((pane) => pane.id === reopenedId);
  if (!reopened?.composerSeatId) throw new Error("reopened owner lacks its seat");
  expect(reopened.composerSeatId).toBe(cascade.composerSeatId);
  expect(readRecoverySelection(ownerRef, composerSelectionScope(reopened.composerSeatId))).toBe("cascade-owner-row");
});
