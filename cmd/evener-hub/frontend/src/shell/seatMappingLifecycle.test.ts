// The composer-seat mapping lifecycle matrix.
//
// Seat-owned restore made the ref/pane seat mappings load-bearing: a pane
// restores the rejected rows its durable seat owns, so every transition that
// creates, moves or removes a mapping decides WHOSE WORK RESTORES WHERE. This
// suite walks every mapping mutation site against the ownership states around
// it. Each cell asserts the mappings that exist, the mappings that retire, and
// the restore determinant: the seat a freshly placed pane for a conversation
// receives (a pane restores exactly the rows owned by that seat - pinned at
// the composer level in Composer.recovery.test.tsx).
//
// Mutation sites covered here: open/placement, close (leaf, cascade, retyped
// reserve, companion, primary replacement), promote/focus, pop/return retype,
// resume identity adoption, cold restore (validated, collision, fresh tab),
// parked panes, and reload. The five review findings this round closed are
// the cells marked FINDING n; their red-first history lives in the workspace,
// retainedComposer, Session.resume and Composer.recovery suites.
import type { DockviewApi } from "dockview-core";
import { beforeEach, describe, expect, test } from "vitest";
import {
  readPaneComposerSeatEntry,
  readRefComposerSeat,
  refSeatsHeldBySeat,
  writePaneComposerSeat,
  writeRefComposerSeat,
} from "../panes/session/composer/draft";
import { installLocalStorage, MemoryStorage } from "../storageTestUtils";
import { setMutationClientIdentityForTests } from "../stores/mutationClientIdentity";
import {
  adoptResumedConversationSeat,
  type OpenPaneRecord,
  registerDockviewApi,
  resetWorkspaceStoreForTests,
  workspaceStore,
} from "./workspace";
import "./AppShell"; // side effect: boot-time pane registrations restoreLayout needs

const sessionPane = (id: string, ref: string, seat?: string): OpenPaneRecord => ({
  id,
  type: "session",
  params: { ref },
  slot: "secondary",
  ...(seat === undefined ? {} : { composerSeatId: seat }),
});

const companionPane = (id: string, ref: string): OpenPaneRecord => ({
  id,
  type: "sessionTasks",
  params: { ref },
  slot: "secondary",
});

// The restore determinant: the seat a pane placed for this conversation now
// holds. A pane under seat S restores exactly the rejected rows owned by S.
function placedSeatFor(ref: string): string | undefined {
  const id = workspaceStore.getState().openPane("session", { ref });
  return workspaceStore.getState().panes.find((pane) => pane.id === id)?.composerSeatId;
}

function setPanes(panes: OpenPaneRecord[]): void {
  workspaceStore.setState({
    panes: panes.map((pane, index) => (index === 0 ? { ...pane, slot: "main" } : pane)),
    focusedPaneId: panes[0]?.id ?? null,
  });
}

// The unit-test dockview double from paneRestore.test.ts, duplicated for the
// cold-restore cells.
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

describe("the seat mapping lifecycle matrix", () => {
  // --- OPEN / PLACEMENT ----------------------------------------------------
  describe("open and placement", () => {
    test("an open into an empty workspace mints a seat and writes both mappings", () => {
      const id = workspaceStore.getState().openPane("session", { ref: "local:a" });
      const pane = workspaceStore.getState().panes.find((record) => record.id === id);
      expect(pane?.composerSeatId).toBeDefined();
      expect(readRefComposerSeat("local:a")).toBe(pane?.composerSeatId);
      // A pane-id mapping belongs to saved desktop geometry, not the open
      // path: it is written when a layout is saved or restored, never here.
      expect(readPaneComposerSeatEntry(id)).toBeUndefined();
    });

    test("a second pane on the same conversation gets a distinct live-excluded seat", () => {
      const first = workspaceStore.getState().openPane("session", { ref: "local:a" });
      const firstSeat = workspaceStore.getState().panes.find((pane) => pane.id === first)?.composerSeatId;
      workspaceStore.getState().openPane("session", { ref: "local:a" }, { slot: "secondary" });
      const seats = workspaceStore
        .getState()
        .panes.filter((pane) => refSeatOf(pane) === "local:a")
        .map((pane) => pane.composerSeatId);
      expect(new Set(seats).size).toBe(seats.length);
      expect(seats).toContain(firstSeat);
    });

    test("closing a conversation and reopening hands the new pane a fresh seat (FINDING 2 core)", () => {
      const first = workspaceStore.getState().openPane("session", { ref: "local:a" });
      const firstSeat = workspaceStore.getState().panes.find((pane) => pane.id === first)?.composerSeatId;
      workspaceStore.getState().closePane(first);
      expect(placedSeatFor("local:a")).not.toBe(firstSeat);
    });
  });

  // --- CLOSE ---------------------------------------------------------------
  describe("close", () => {
    test("closing a leaf pane clears its conversation and pane mappings", () => {
      setPanes([sessionPane("p1", "local:a", "seat-a")]);
      writeRefComposerSeat("local:a", "seat-a");
      writePaneComposerSeat("p1", "local:a", "seat-a");

      workspaceStore.getState().closePane("p1");

      expect(readRefComposerSeat("local:a")).toBeUndefined();
      expect(readPaneComposerSeatEntry("p1")).toBeUndefined();
      expect(refSeatsHeldBySeat("seat-a")).toEqual([]);
    });

    test("closing a pane with a surviving pane on the conversation reconciles to the survivor", () => {
      setPanes([sessionPane("p1", "local:a", "seat-a"), sessionPane("p2", "local:a", "seat-b")]);
      writeRefComposerSeat("local:a", "seat-a");

      workspaceStore.getState().closePane("p1");

      expect(readRefComposerSeat("local:a")).toBe("seat-b");
    });

    test("closing a pane with only a companion survivor clears the mapping (FINDING 3)", () => {
      setPanes([sessionPane("p1", "local:a", "seat-a"), companionPane("p-tasks", "local:a")]);
      writeRefComposerSeat("local:a", "seat-a");

      workspaceStore.getState().closePane("p1");

      expect(readRefComposerSeat("local:a")).toBeUndefined();
    });

    test("closing a companion pane touches no mappings", () => {
      setPanes([sessionPane("p1", "local:a", "seat-a"), companionPane("p-tasks", "local:a")]);
      writeRefComposerSeat("local:a", "seat-a");

      workspaceStore.getState().closePane("p-tasks");

      expect(readRefComposerSeat("local:a")).toBe("seat-a");
    });

    test("closing a retyped transcript pane retires its reserved seat's mappings (FINDING 2)", () => {
      setPanes([sessionPane("p1", "local:a", "seat-a")]);
      workspaceStore
        .getState()
        .retypePane(workspaceStore.getState().panes.find((pane) => pane.id === "p1")!, "transcript", {
          ref: "local:a",
          parentRef: "local:a",
        });
      writeRefComposerSeat("local:a", "seat-a");

      workspaceStore.getState().closePane("p1");

      expect(readRefComposerSeat("local:a")).toBeUndefined();
      expect(placedSeatFor("local:a")).not.toBe("seat-a");
    });

    test("primary replacement retires the removed duplicate's mappings (FINDING 2)", () => {
      setPanes([sessionPane("p-main", "local:a", "seat-main"), sessionPane("p-dup", "local:a", "seat-dup")]);
      writePaneComposerSeat("p-dup", "local:a", "seat-dup");

      expect(workspaceStore.getState().replacePrimary("session", { ref: "local:a" })).toBe("p-main");

      expect(readPaneComposerSeatEntry("p-dup")).toBeUndefined();
      expect(readRefComposerSeat("local:a")).toBe("seat-main");
    });
  });

  // --- PROMOTE / FOCUS -----------------------------------------------------
  describe("promote and focus", () => {
    test("promotion writes the promoted owner's mapping without retiring anything", () => {
      setPanes([sessionPane("p1", "local:a", "seat-a"), sessionPane("p2", "local:a", "seat-b")]);

      workspaceStore.getState().promotePane("p2");

      expect(readRefComposerSeat("local:a")).toBe("seat-b");
      // Promotion writes only the conversation mapping; no retirement runs.
      expect(readPaneComposerSeatEntry("p1")).toBeUndefined();
    });
  });

  // --- POP / RETURN (RETYPE) ------------------------------------------------
  describe("pop and return navigation", () => {
    test("a retyped transcript pane keeps its seat reserved, and the return reuses it", () => {
      setPanes([sessionPane("p1", "local:a", "seat-a")]);
      writeRefComposerSeat("local:a", "seat-a");
      workspaceStore
        .getState()
        .retypePane(workspaceStore.getState().panes.find((pane) => pane.id === "p1")!, "transcript", {
          ref: "local:a",
          parentRef: "local:a",
        });
      expect(readRefComposerSeat("local:a")).toBe("seat-a");

      // The return to a conversation type retypes back and reuses the seat.
      workspaceStore
        .getState()
        .retypePane(workspaceStore.getState().panes.find((pane) => pane.id === "p1")!, "session", { ref: "local:a" });
      expect(workspaceStore.getState().panes.find((pane) => pane.id === "p1")?.composerSeatId).toBe("seat-a");
      expect(readRefComposerSeat("local:a")).toBe("seat-a");
    });
  });

  // --- RESUME IDENTITY REPLACEMENT -----------------------------------------
  describe("resume identity replacement", () => {
    test("adoption carries the authoring seat to an unowned resumed conversation (FINDING 1)", () => {
      setPanes([sessionPane("p1", "local:old", "author-seat")]);

      adoptResumedConversationSeat("p1", "local:resumed");
      // The resumed route replaces the primary pane outright: the
      // replacement pane's placement reads the adopted mapping and is placed
      // under the authoring seat, so the retargeted rows restore into it.
      const replacementId = workspaceStore.getState().replacePrimary("session", { ref: "local:resumed" });
      expect(replacementId).not.toBe("p1");
      expect(workspaceStore.getState().panes.find((pane) => pane.id === replacementId)?.composerSeatId).toBe(
        "author-seat",
      );
      expect(readRefComposerSeat("local:resumed")).toBe("author-seat");
    });

    test("adoption overwrites a mapping no live pane holds", () => {
      setPanes([sessionPane("p1", "local:old", "author-seat")]);
      writeRefComposerSeat("local:resumed", "seat-dead");

      adoptResumedConversationSeat("p1", "local:resumed");

      expect(readRefComposerSeat("local:resumed")).toBe("author-seat");
    });

    test("adoption leaves a live owner covering the resumed conversation alone", () => {
      setPanes([sessionPane("p1", "local:old", "author-seat"), sessionPane("p2", "local:resumed", "seat-owner")]);
      writeRefComposerSeat("local:resumed", "seat-owner");

      adoptResumedConversationSeat("p1", "local:resumed");

      expect(readRefComposerSeat("local:resumed")).toBe("seat-owner");
    });
  });

  // --- COLD RESTORE --------------------------------------------------------
  describe("cold restore", () => {
    test("a validated restore keeps this tab's persisted seat", () => {
      writeRefComposerSeat("local:a", "tab-seat");
      writePaneComposerSeat("p1", "local:a", "tab-seat");

      restorePanels({ p1: sessionPanel("p1", "local:a") });

      expect(workspaceStore.getState().panes.find((pane) => pane.id === "p1")?.composerSeatId).toBe("tab-seat");
    });

    test("a collision replacement carries the cascade's ancestor mappings (FINDING 4 restore half)", () => {
      writeRefComposerSeat("local:child", "seat-s");
      writeRefComposerSeat("local:owner", "seat-s");
      writePaneComposerSeat("p2", "local:child", "seat-s");

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

      const cascade = workspaceStore.getState().panes.find((pane) => pane.id === "p1");
      expect(cascade?.composerSeatId).not.toBe("seat-s");
      expect(readRefComposerSeat("local:owner")).toBe(cascade?.composerSeatId);
      // The phone-reload restore: on a fresh process the cascade is gone, and
      // a reconstructed route for the ancestor conversation is placed under
      // the mapping this restore reconciled - so the rows the cascade's
      // replacement seat owns restore there.
      resetWorkspaceStoreForTests();
      restorePanels({ p3: sessionPanel("p3", "local:owner") });
      expect(workspaceStore.getState().panes.find((pane) => pane.id === "p3")?.composerSeatId).toBe(
        cascade?.composerSeatId,
      );
    });

    test("two fresh tabs restoring one shared layout mint distinct seats", () => {
      restorePanels({ p1: sessionPanel("p1", "local:shared") });
      const seatA = workspaceStore.getState().panes.find((pane) => pane.id === "p1")?.composerSeatId;

      setMutationClientIdentityForTests("matrix-tab-b");
      resetWorkspaceStoreForTests();
      restorePanels({ p1: sessionPanel("p1", "local:shared") });
      const seatB = workspaceStore.getState().panes.find((pane) => pane.id === "p1")?.composerSeatId;

      expect(seatB).not.toBe(seatA);
      setMutationClientIdentityForTests("matrix-tab");
    });
  });

  // --- PARK ----------------------------------------------------------------
  describe("parked panes", () => {
    test("a parked pane keeps its mappings and covers its conversation as a survivor", () => {
      const parked = { ...sessionPane("p-parked", "local:a", "seat-parked"), slot: "parked" as const };
      setPanes([sessionPane("p1", "local:b", "seat-b"), parked]);
      writeRefComposerSeat("local:a", "seat-parked");

      // Closing an unrelated pane leaves the parked pane's mappings alone.
      workspaceStore.getState().closePane("p1");
      expect(readRefComposerSeat("local:a")).toBe("seat-parked");

      // The parked pane also reconciles a closed owner of ITS conversation.
      setPanes([sessionPane("p-owner", "local:a", "seat-c"), parked]);
      writeRefComposerSeat("local:a", "seat-c");
      workspaceStore.getState().closePane("p-owner");
      expect(readRefComposerSeat("local:a")).toBe("seat-parked");
    });
  });

  // --- RELOAD --------------------------------------------------------------
  describe("reload", () => {
    test("a reload restoring the same layout keeps this tab's seats", () => {
      writeRefComposerSeat("local:a", "tab-seat");
      writePaneComposerSeat("p1", "local:a", "tab-seat");

      restorePanels({ p1: sessionPanel("p1", "local:a") });
      const first = workspaceStore.getState().panes.find((pane) => pane.id === "p1")?.composerSeatId;
      resetWorkspaceStoreForTests();
      restorePanels({ p1: sessionPanel("p1", "local:a") });
      const second = workspaceStore.getState().panes.find((pane) => pane.id === "p1")?.composerSeatId;

      expect(second).toBe(first);
      expect(second).toBe("tab-seat");
    });
  });
});

function refSeatOf(pane: OpenPaneRecord): string {
  return (pane.params as { ref?: unknown }).ref === undefined ? "" : ((pane.params as { ref?: string }).ref ?? "");
}
