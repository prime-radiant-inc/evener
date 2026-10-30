// The composer-seat lifecycle matrix, derived shape.
//
// Seat ownership is DERIVED AT READ: the mount-restore predicate consults
// only the pane's own durable seat (the record's composerSeatId, resolved as
// the lifetime's compositionOwnerId), and placement mints a fresh seat for
// every pane. No conversation-ref seat mapping exists; the only persisted
// seat record is the per-pane entry a desktop layout save writes and a
// restore validates, which is what keeps a DESKTOP reload restoring this
// tab's own work. Mobile-shaped route reconstruction (a rebuilt pane under a
// fresh id, no pane entry) mints fresh: one tap in the queue strip, ruled
// product behavior. The one exception is the SEAT-CARRY on resume: the pane
// a resume replaces hands its authoring seat to the resumed pane's
// placement, and the superseded nested pane closes in the same transition,
// so a pane's own bounced work still restores after its conversation resumes
// and two live panes never share a seat.

import { act, screen } from "@testing-library/react";
import type { DockviewApi } from "dockview-core";
import { IDBFactory } from "fake-indexeddb";
import { beforeEach, describe, expect, test } from "vitest";
import { mountComposerWithHandle, seedRejectedRecovery } from "../panes/session/composer/composerTestUtils";
import { readPaneComposerSeatEntry, writePaneComposerSeat } from "../panes/session/composer/draft";
import { flushPendingTurnsProjectionForTests } from "../panes/session/composer/queue/testing/flushPendingTurnsProjection";
import { installLocalStorage, MemoryStorage } from "../storageTestUtils";
import { setMutationClientIdentityForTests } from "../stores/mutationClientIdentity";
import { MutationOutboxIndexedDB } from "../stores/mutationOutboxIndexedDB";
import { setMutationStorageForTests } from "../stores/threads";
import {
  type AttentionPlacementEntry,
  captureAttentionPlacement,
  presentAttentionTarget,
  restoreAttentionPlacement,
} from "./attentionPlacement";
import { conversationPaneLifetime } from "./paneLifetime";
import { openTopLevelSession } from "./sessionPlacement";
import {
  carryComposerSeatForPlacement,
  type OpenPaneRecord,
  registerDockviewApi,
  resetWorkspaceStoreForTests,
  workspaceStore,
} from "./workspace";
import "./AppShell"; // side effect: boot-time pane registrations restoreLayout needs

const sessionPane = (
  id: string,
  ref: string,
  slot: "main" | "secondary" | "parked",
  seat?: string,
): OpenPaneRecord => ({
  id,
  type: "session",
  params: { ref },
  slot,
  ...(seat === undefined ? {} : { composerSeatId: seat }),
});

function setPanes(panes: OpenPaneRecord[]): void {
  workspaceStore.setState({ panes, focusedPaneId: panes.find((pane) => pane.slot === "main")?.id ?? null });
}

// The seat a pane placed for this conversation now holds: the restore
// determinant. A pane under seat S restores exactly the rejected rows owned
// by S (the ownership predicate, pinned in Composer.recovery.test.tsx).
function placedSeatFor(ref: string): string | undefined {
  const id = workspaceStore.getState().openPane("session", { ref });
  return workspaceStore.getState().panes.find((pane) => pane.id === id)?.composerSeatId;
}

function seatOfPane(id: string): string | undefined {
  return workspaceStore.getState().panes.find((pane) => pane.id === id)?.composerSeatId;
}

// The unit-test dockview double from paneRestore.test.ts, for the
// desktop-restore cells.
class FakeDockviewApi {
  panels: Array<{ id: string; params: unknown }> = [];
  activePanel: { id: string } | undefined = undefined;
  cleared = false;
  serialized: unknown = { fake: "layout" };
  fromJSONBehavior: (data: unknown) => void = () => {};

  toJSON(): unknown {
    return this.serialized;
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

function restorePanels(panels: Record<string, unknown>): void {
  const fake = new FakeDockviewApi();
  fake.fromJSONBehavior = (data) => {
    const layout = data as { panels?: Record<string, { params?: unknown }> };
    fake.panels = Object.entries(layout.panels ?? {}).map(([id, panel]) => ({ id, params: panel.params }));
    const first = Object.keys(layout.panels ?? {})[0];
    fake.activePanel = first === undefined ? undefined : { id: first };
  };
  registerDockviewApi(asDockviewApi(fake));
  expect(workspaceStore.getState().restoreLayout({ panels })).toBe(true);
}

const sessionPanel = (id: string, ref: string) => ({
  id,
  contentComponent: "default",
  params: { paneType: "session", paneParams: { ref } },
});

beforeEach(() => {
  installLocalStorage(new MemoryStorage());
  setMutationClientIdentityForTests("matrix-tab");
  resetWorkspaceStoreForTests();
});

describe("the derived seat lifecycle matrix", () => {
  // --- PLACEMENT ------------------------------------------------------------
  describe("placement", () => {
    test("an A-B-A full-replacement reopen never reuses the removed pane's seat", () => {
      // The removed pane's rejected work must not auto-restore into the
      // reopening pane: the fresh-pane narrowing, now by construction.
      const first = placedSeatFor("local:a");
      workspaceStore.getState().replacePrimary("session", { ref: "local:b" });
      const reopened = placedSeatFor("local:a");
      expect(reopened).toBeDefined();
      expect(reopened).not.toBe(first);
    });

    test("closing a conversation and reopening hands the new pane a fresh seat", () => {
      const first = placedSeatFor("local:a");
      const firstId = workspaceStore.getState().panes.find((pane) => pane.composerSeatId === first)?.id;
      expect(firstId).toBeDefined();
      workspaceStore.getState().closePane(firstId as string);
      expect(placedSeatFor("local:a")).not.toBe(first);
    });

    test("a resume carry is cancelled when the resumed conversation already occupies main", () => {
      // The resumed conversation already occupies main under its own seat; the
      // retired author pane carried its seat for it. The resume resolves to
      // the occupying pane, and the carry dies with that resolution: a later
      // new-pane open inherits neither the retired pane's seat (the
      // auto-restore determinant, pinned in Composer.recovery.test.tsx) nor
      // its rejected work.
      const mainId = workspaceStore.getState().replacePrimary("session", { ref: "local:occupied" });
      const mainSeat = seatOfPane(mainId);
      if (!mainSeat) throw new Error("main pane lacks a seat");
      carryComposerSeatForPlacement("local:occupied", "author-seat");
      expect(workspaceStore.getState().replacePrimary("session", { ref: "local:occupied" })).toBe(mainId);
      expect(seatOfPane(mainId)).toBe(mainSeat);
      workspaceStore.getState().closePane(mainId);
      const reopenedId = workspaceStore.getState().openPane("session", { ref: "local:occupied" });
      const reopenedSeat = seatOfPane(reopenedId);
      expect(reopenedSeat).toBeDefined();
      expect(reopenedSeat).not.toBe("author-seat");
    });

    test("a retype onto a seated conversation pane cancels its pending carry", () => {
      // A seated pane retyped onto the carried conversation keeps its own
      // seat; the carry dies with that placement decision.
      const otherId = workspaceStore.getState().openPane("session", { ref: "local:retype-other" });
      expect(seatOfPane(otherId)).toBeDefined();
      carryComposerSeatForPlacement("local:retype-carried", "author-seat");
      const pane = workspaceStore.getState().panes.find((record) => record.id === otherId);
      if (!pane) throw new Error("retype pane missing");
      expect(workspaceStore.getState().retypePane(pane, "session", { ref: "local:retype-carried" })).toBe(true);
      expect(workspaceStore.getState().closePane(otherId)).toBeUndefined();
      const reopened = placedSeatFor("local:retype-carried");
      expect(reopened).toBeDefined();
      expect(reopened).not.toBe("author-seat");
    });

    test("a desktop layout restore cancels a pending carry for the conversations it places", () => {
      carryComposerSeatForPlacement("local:restore-carried", "author-seat");
      resetWorkspaceStoreForTests();
      restorePanels({ p1: sessionPanel("p1", "local:restore-carried") });
      expect(workspaceStore.getState().panes.find((record) => record.id === "p1")).toBeDefined();
      workspaceStore.getState().closePane("p1");
      const reopened = placedSeatFor("local:restore-carried");
      expect(reopened).toBeDefined();
      expect(reopened).not.toBe("author-seat");
    });

    test("re-presenting a parked pane cancels its pending carry", () => {
      const id = workspaceStore.getState().openPane("session", { ref: "local:parked-carry" });
      expect(seatOfPane(id)).toBeDefined();
      const origin = captureAttentionPlacement();
      expect(origin).not.toBeNull();
      expect(presentAttentionTarget("local:parked-carry-dest")).not.toBeNull();
      expect(workspaceStore.getState().panes.find((record) => record.id === id)?.slot).toBe("parked");
      carryComposerSeatForPlacement("local:parked-carry", "author-seat");
      // Back navigation re-presents the parked pane.
      expect(presentAttentionTarget("local:parked-carry")).not.toBeNull();
      expect(workspaceStore.getState().panes.find((record) => record.id === id)?.slot).not.toBe("parked");
      workspaceStore.getState().closePane(id);
      const reopened = placedSeatFor("local:parked-carry");
      expect(reopened).toBeDefined();
      expect(reopened).not.toBe("author-seat");
    });

    test("presenting an already-presented pane cancels its pending carry", () => {
      const id = workspaceStore.getState().openPane("session", { ref: "local:presented-carry" });
      expect(seatOfPane(id)).toBeDefined();
      carryComposerSeatForPlacement("local:presented-carry", "author-seat");
      // Attention focuses the pane that is already presented; no new pane.
      expect(presentAttentionTarget("local:presented-carry")?.pane.id).toBe(id);
      workspaceStore.getState().closePane(id);
      const reopened = placedSeatFor("local:presented-carry");
      expect(reopened).toBeDefined();
      expect(reopened).not.toBe("author-seat");
    });

    test("a cascade covering the route in main cancels the route's pending carry", () => {
      setPanes([
        {
          id: "p-cascade",
          type: "sessionZoom",
          params: {
            ref: "local:cascade-carry-child",
            source: { type: "session", params: { ref: "local:cascade-carry" } },
            edges: [{ ownerRef: "local:cascade-carry", childRef: "local:cascade-carry-child", delegateId: "d1" }],
          },
          slot: "main",
        },
      ]);
      carryComposerSeatForPlacement("local:cascade-carry", "author-seat");
      // The route placement retains the cascade in main and closes the plain
      // duplicate; the carry dies with that decision.
      openTopLevelSession("local:cascade-carry");
      expect(workspaceStore.getState().mainPane()?.type).toBe("sessionZoom");
      const reopened = placedSeatFor("local:cascade-carry");
      expect(reopened).toBeDefined();
      expect(reopened).not.toBe("author-seat");
    });

    test("a resume carry is cancelled when placement resolves to an already-open pane", () => {
      // The nested resume target is already open in this tab under its own
      // seat; the superseded author pane carried its seat for the resumed
      // ref. The resume transition's own placement resolves to the open
      // destination, and the carry must die with that resolution - a later
      // reopen of the conversation must never inherit the authoring seat.
      const existingId = workspaceStore.getState().openPane("session", { ref: "local:resumed" });
      const existingSeat = seatOfPane(existingId);
      expect(existingSeat).toBeDefined();
      carryComposerSeatForPlacement("local:resumed", "author-seat");
      expect(workspaceStore.getState().openPane("session", { ref: "local:resumed" })).toBe(existingId);
      expect(seatOfPane(existingId)).toBe(existingSeat);
      // Navigate away (the destination closes) and reopen the conversation.
      workspaceStore.getState().closePane(existingId);
      const reopened = placedSeatFor("local:resumed");
      expect(reopened).toBeDefined();
      expect(reopened).not.toBe("author-seat");
    });
  });

  // --- RELOAD ---------------------------------------------------------------
  describe("reload", () => {
    test("a desktop reload validates the saved pane entry and reuses this tab's seat", () => {
      const id = workspaceStore.getState().openPane("session", { ref: "local:a" });
      const seat = seatOfPane(id);
      // The layout save is the one moment a pane id and a conversation are
      // known to belong together; simulate its persisted pane entry.
      writePaneComposerSeat(id, "local:a", seat as string);
      resetWorkspaceStoreForTests();
      restorePanels({ [id]: sessionPanel(id, "local:a") });
      expect(seatOfPane(id)).toBe(seat);
      expect(readPaneComposerSeatEntry(id)?.seat).toBe(seat);
    });

    test("a genuine close invalidates the persisted pane-seat entry", () => {
      // Another tab can save the shared layout naming this pane id for the
      // same conversation; this tab's reload must not validate a dead entry
      // and auto-restore the closed pane's rejected work.
      const id = workspaceStore.getState().openPane("session", { ref: "local:closed" });
      const seat = seatOfPane(id);
      writePaneComposerSeat(id, "local:closed", seat as string);
      workspaceStore.getState().closePane(id);
      resetWorkspaceStoreForTests();
      restorePanels({ [id]: sessionPanel(id, "local:closed") });
      const restored = seatOfPane(id);
      expect(restored).toBeDefined();
      expect(restored).not.toBe(seat);
    });

    test("a parked pane keeps its persisted entry, so a desktop reload restores its seat", () => {
      // Parking is not a genuine close: the pane keeps its durable record.
      const id = workspaceStore.getState().openPane("session", { ref: "local:parked-entry" });
      const seat = seatOfPane(id);
      writePaneComposerSeat(id, "local:parked-entry", seat as string);
      const destination = presentAttentionTarget("local:elsewhere");
      expect(destination).toBeDefined();
      expect(workspaceStore.getState().panes.find((pane) => pane.id === id)?.slot).toBe("parked");
      resetWorkspaceStoreForTests();
      restorePanels({ [id]: sessionPanel(id, "local:parked-entry") });
      expect(seatOfPane(id)).toBe(seat);
    });

    test("a retype keeps the persisted entry, so a desktop reload restores its seat", () => {
      // Retyping is not a genuine close either: the record keeps the entry.
      const id = workspaceStore.getState().openPane("session", { ref: "local:retyped-entry" });
      const seat = seatOfPane(id);
      writePaneComposerSeat(id, "local:retyped-entry", seat as string);
      const pane = workspaceStore.getState().panes.find((record) => record.id === id);
      expect(pane).toBeDefined();
      expect(
        workspaceStore.getState().retypePane(pane as OpenPaneRecord, "transcript", { ref: "local:retyped-entry" }),
      ).toBe(true);
      resetWorkspaceStoreForTests();
      restorePanels({ [id]: sessionPanel(id, "local:retyped-entry") });
      expect(seatOfPane(id)).toBe(seat);
    });

    test("a mobile-shaped route reconstruction mints a fresh seat: one tap", () => {
      // The mobile reload rebuilds the routed pane under a new id with no
      // pane entry of its own: no auto-restore, one tap in the queue strip.
      // Ruled product behavior.
      const first = placedSeatFor("local:a");
      const seat = first;
      resetWorkspaceStoreForTests();
      restorePanels({ pMobile: sessionPanel("pMobile", "local:a") });
      expect(seatOfPane("pMobile")).not.toBe(seat);
    });

    test("two fresh tabs restore one shared layout with distinct seats", () => {
      restorePanels({ p1: sessionPanel("p1", "local:shared") });
      const seatA = seatOfPane("p1");
      setMutationClientIdentityForTests("matrix-tab-b");
      resetWorkspaceStoreForTests();
      restorePanels({ p1: sessionPanel("p1", "local:shared") });
      expect(seatOfPane("p1")).not.toBe(seatA);
      setMutationClientIdentityForTests("matrix-tab");
    });
  });

  // --- COLLISION ACROSS RELOAD ------------------------------------------------
  describe("cold-restore collisions", () => {
    test("a collision-displaced cascade's ancestor columns are one tap after a reload", () => {
      // The old shared-seat shape: the leaf pane's saved record held seat-s.
      // (Pre-redesign, persisted ref keys named it for both columns too; the
      // derived shape has no such keys, and this cell's red run seeded them
      // through the old API.)
      writePaneComposerSeat("p2", "local:child", "seat-s");
      // A shared layout restores the leaf twice; the displaced cascade keeps
      // its replacement seat, but nothing maps its ancestor columns to that
      // seat anymore. Reconstructing an ancestor conversation after the
      // reload mints fresh: its preserved rows wait in the queue strip, one
      // explicit tap away. Ruled product behavior.
      restorePanels({
        p1: {
          id: "p1",
          contentComponent: "default",
          params: {
            paneType: "sessionZoom",
            paneParams: {
              ref: "local:child",
              source: { type: "session", params: { ref: "local:owner" } },
              edges: [{ ownerRef: "local:owner", childRef: "local:child", delegateId: "delegate-1" }],
            },
          },
        },
        p2: sessionPanel("p2", "local:child"),
      });
      const cascade = seatOfPane("p1");
      const leaf = seatOfPane("p2");
      expect(cascade).toBeDefined();
      expect(leaf).toBeDefined();
      expect(cascade).not.toBe(leaf);
      // The ancestor conversation reconstructed after the reload: fresh.
      resetWorkspaceStoreForTests();
      restorePanels({ p3: sessionPanel("p3", "local:owner") });
      expect(seatOfPane("p3")).not.toBe(cascade);
    });

    test("a validated cascade restore keeps this tab's seat through a deeper shared leaf", () => {
      // The pane entry a desktop save wrote names the cascade's pane id; the
      // shared layout may have drilled deeper elsewhere, and continuity
      // still runs through the cascade path.
      writePaneComposerSeat("p1", "local:child", "tab-seat");
      restorePanels({
        p1: {
          id: "p1",
          contentComponent: "default",
          params: {
            paneType: "sessionZoom",
            paneParams: {
              ref: "local:grandchild",
              source: { type: "session", params: { ref: "local:owner" } },
              edges: [
                { ownerRef: "local:owner", childRef: "local:child", delegateId: "delegate-1" },
                { ownerRef: "local:child", childRef: "local:grandchild", delegateId: "delegate-2" },
              ],
            },
          },
        },
      });
      expect(seatOfPane("p1")).toBe("tab-seat");
    });
  });

  // --- RETYPE (POP/RETURN) ------------------------------------------------------
  describe("pop and return navigation", () => {
    test("a retyped transcript pane keeps its seat on the record, and the return reuses it", () => {
      const id = workspaceStore.getState().openPane("session", { ref: "local:a" });
      const seat = seatOfPane(id);
      const pane = workspaceStore.getState().panes.find((record) => record.id === id);
      if (!pane) throw new Error("missing pane");
      expect(workspaceStore.getState().retypePane(pane, "transcript", { ref: "local:a", parentRef: "local:a" })).toBe(
        true,
      );
      const retyped = workspaceStore.getState().panes.find((record) => record.id === id);
      // The record carries the seat through the read-only interlude: a
      // different pane opening the conversation still cannot take it (the
      // live-seat rule), and the return to a conversation type reuses it.
      expect(retyped?.composerSeatId).toBe(seat);
      expect(placedSeatFor("local:a")).not.toBe(seat);
      expect(workspaceStore.getState().retypePane(retyped as OpenPaneRecord, "session", { ref: "local:a" })).toBe(true);
      expect(seatOfPane(id)).toBe(seat);
    });
  });

  // --- PARK ----------------------------------------------------------------
  describe("parked panes", () => {
    test("a parked pane keeps its seat on the record and covers it while parked", () => {
      setPanes([
        sessionPane("p-main", "local:owner", "main", "seat-main"),
        sessionPane("p-parked", "local:child", "parked", "seat-parked"),
      ]);
      // A parked pane is alive: its seat stays occupied, so a different pane
      // opening its conversation mints fresh rather than sharing.
      expect(placedSeatFor("local:child")).not.toBe("seat-parked");
      expect(seatOfPane("p-parked")).toBe("seat-parked");
    });

    // The real park/unpark round trip with a real Composer mount: parking
    // unmounts the pane (DockHost presents presentedPanes only), the record
    // and its seat survive, and the remounted pane's composer restores the
    // rejected work its seat owns.
    test("a parked pane's own work restores on unpark", async () => {
      globalThis.indexedDB = new IDBFactory();
      const storage = new MutationOutboxIndexedDB();
      setMutationStorageForTests(storage);
      const id = workspaceStore.getState().openPane("session", { ref: "local:a" });
      const seat = seatOfPane(id);
      if (!seat) throw new Error("placed pane lacks a seat");
      const pane = workspaceStore.getState().panes.find((record) => record.id === id);
      if (!pane) throw new Error("missing pane record");
      const source = conversationPaneLifetime(pane).composer;
      if (!source) throw new Error("pane has no composer source");
      const first = await mountComposerWithHandle("local:a", { status: { type: "idle" } }, { source });
      // Park: the pane unmounts, its record and seat stay.
      const origin = captureAttentionPlacement();
      expect(origin).not.toBeNull();
      await act(async () => {
        expect(presentAttentionTarget("local:b")).not.toBeNull();
      });
      first.unmount();
      // An owned row lands while the pane is parked.
      await seedRejectedRecovery(storage, "local:a", "parked bounce", {}, seat);
      await flushPendingTurnsProjectionForTests();
      // Unpark: the pane is presented again and remounts; its composer
      // restores the row its durable seat owns.
      await act(async () => {
        expect(restoreAttentionPlacement(origin as AttentionPlacementEntry)).toBe(true);
      });
      expect(seatOfPane(id)).toBe(seat);
      await mountComposerWithHandle("local:a", { status: { type: "idle" } }, { source });
      expect(screen.getByRole("textbox", { name: /^message$/i }).textContent).toBe("parked bounce");
    });
  });
});
