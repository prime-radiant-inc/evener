import "fake-indexeddb/auto";
import {
  type AnyNotification,
  makeTranscriptDisplayConfig,
  type ThreadModel,
  type ThreadReadResponse,
  type ThreadTurnsListResponse,
  type Turn,
} from "@evener/appwire-client";
import { currentRead, textDelta } from "@evener/appwire-client/testing/currentTranscript";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, render, screen } from "@testing-library/react";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { createElement } from "react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { TranscriptBody } from "../panes/session/transcript/TranscriptBody";
import { installBackgroundTranscriptOptionsForTests } from "./backgroundTranscriptOptions";
import { connectionStore, resetConnectionStoreForTests } from "./connection";
import { productionEntryTranscriptForTests, setProductionEntryCacheForTests } from "./entryTranscriptRoutes";
import { SessionEntryCache } from "./sessionEntryCache";
import {
  acquireBackgroundThread,
  resetThreadsStoreForTests,
  settleEntryTranscriptForTests,
  threadsStore,
} from "./threads";

let cache: SessionEntryCache;
beforeEach(() => {
  resetConnectionStoreForTests();
  resetThreadsStoreForTests();
  cache = new SessionEntryCache({
    indexedDB: new IDBFactory(),
    keyRange: IDBKeyRange,
    databaseName: `parallel-background-entry-rendering-${crypto.randomUUID()}`,
  });
  setProductionEntryCacheForTests(cache);
});
afterEach(() => {
  resetThreadsStoreForTests();
  cache.close();
});

// A positioned versioned turn: its items carry explicit (entry, item) positions
// so the reducer's position-aware turn weave orders pages as genuinely older.
function pageTurn(turnId: string, entry: number, startItem: number, itemTexts: readonly string[]): Turn {
  return {
    id: turnId,
    itemsView: "full",
    status: "completed",
    version: 1,
    items: itemTexts.map((text, index) => ({
      type: "agentMessage",
      id: `${turnId}-item-${index}`,
      transcriptKey: `apptranscript-item-v2:${turnId}:0:${index}`,
      turnId,
      position: { entry, item: startItem + index },
      version: 1,
      text,
    })),
  };
}

// A genuine older-history page: one positioned versioned turn plus the cursor
// that names the next page down. `length` stays at the latest window's own
// length so the versioned merge accepts the page rather than reading it stale;
// the strictly descending (entry, item) boundary is what makes it older.
function olderPage(
  turnId: string,
  entry: number,
  startItem: number,
  itemTexts: readonly string[],
  nextCursor: string | undefined,
  length = 100,
): ThreadTurnsListResponse {
  return {
    data: [pageTurn(turnId, entry, startItem, itemTexts)],
    nextCursor,
    bootGeneration: "1",
    epoch: 1,
    snapshot: { incarnation: "inc-1", length },
    authoritative: false,
  };
}

// The recorded latest window: a positioned recorded turn in history plus the
// streaming overlay (from currentRead) and its older cursor.
function positionedLatestRead(ref: string): ThreadReadResponse {
  const base = currentRead(ref);
  base.olderCursor = "cursor_latest";
  base.snapshot = { incarnation: "inc-1", length: 100 };
  base.thread.turns = [pageTurn("turn_latest", 2, 0, ["latest-recorded-item"])];
  return base;
}

describe("background thread claims", () => {
  test("renders one completed transcript copy after history retires the live overlay", async () => {
    const restoreOptions = installBackgroundTranscriptOptionsForTests({
      residentBytes: 0,
      pendingBytes: 8 * 1024 * 1024,
    });
    resetThreadsStoreForTests();
    setProductionEntryCacheForTests(cache);
    const fake = new FakeClient("ready");
    fake.on("thread/read", ({ ref, requestGeneration }) => ({
      ...positionedLatestRead(ref ?? "local:one"),
      requestGeneration,
    }));
    fake.on("thread/unsubscribe", () => ({}));
    fake.on("thread/turns/list", () => olderPage("turn_old", 1, 0, ["older-completed-item"], "cursor_next"));
    connectionStore.getState().connect(fake);
    const background = acquireBackgroundThread("local:one");
    let paneHeld = false;
    let restoreLocalWindowObserver: (() => void) | undefined;
    let rendered: ReturnType<typeof render> | undefined;
    const view = (model: ThreadModel) =>
      createElement(TranscriptBody, {
        model,
        config: makeTranscriptDisplayConfig({ kind: "preset", level: "full" }),
        surface: "preview",
        disclosureScope: "completed-copy",
      });
    try {
      await background.ready;
      await settleEntryTranscriptForTests("local:one");
      // R51 replaces whole-model absence, not the recorded-body nonresidency
      // boundary. The ordinary publisher keeps metadata and live overlay only.
      expect(threadsStore.getState().threads.has("local:one")).toBe(true);
      const backgroundModel = threadsStore.getState().threads.get("local:one");
      const repository = productionEntryTranscriptForTests("local:one")?.repository;
      if (!backgroundModel || !repository) throw new Error("expected the ordinary background entry owner");
      const recordedIdentities = [
        {
          id: "turn_latest-item-0",
          turnId: "turn_latest",
          type: "agentMessage",
          text: "",
          transcriptKey: "apptranscript-item-v2:turn_latest:0:0",
          position: { entry: 2, item: 0 },
          version: 1,
        },
      ];
      expect(backgroundModel.history?.turns.flatMap((turn) => turn.items)).toStrictEqual(recordedIdentities);
      expect(JSON.stringify(backgroundModel)).not.toContain("latest-recorded-item");
      const canonicalBackgroundModel = repository.getSnapshot();
      expect(canonicalBackgroundModel?.history?.turns.flatMap((turn) => turn.items)).toStrictEqual(recordedIdentities);
      expect(JSON.stringify(canonicalBackgroundModel)).not.toContain("latest-recorded-item");
      expect(repository.retainedInventoryForTesting()).toStrictEqual({
        pendingRows: [],
        inFlightBodies: [],
        retryBodies: [],
        backgroundAcceptBodies: [],
        handoffOverlayBodies: [],
      });
      expect(repository.getRetentionSnapshotForTesting()).toEqual({ bindingHasSeedModel: false });

      // Stream canonical text and tool output while recorded bodies are nonresident.
      // Unlike the removed blob fold, the entry route owns unfinished overlay in memory.
      fake.emitNotification({
        method: "overlay/upserted",
        params: {
          threadId: "thr_local:one",
          ref: "local:one",
          item: {
            key: "stream:s1:tool",
            kind: "stream",
            streamId: "s1",
            roundId: "r1",
            turnId: "turn_1",
            item: {
              type: "commandExecution",
              id: "tool1",
              turnId: "turn_1",
              roundId: "r1",
              callId: "call_tool",
              output: "",
              status: "inProgress",
            },
          },
        },
      } as AnyNotification);
      fake.emitNotification(textDelta("local:one", "-streamed"));
      fake.emitNotification({
        method: "overlay/delta",
        params: {
          threadId: "thr_local:one",
          ref: "local:one",
          key: "stream:s1:tool",
          field: "output",
          delta: "-tool-streamed",
        },
      } as AnyNotification);
      await settleEntryTranscriptForTests("local:one");

      // Open the real local entry window, preserving live overlay without thread/read.
      const readsBeforePromotion = fake.calls.filter((call) => call.method === "thread/read").length;
      const localWindowReads = vi.spyOn(cache, "getLatestWindow");
      restoreLocalWindowObserver = () => localWindowReads.mockRestore();
      const pane = threadsStore.getState().ensureThread("local:one");
      paneHeld = true;
      await pane;
      expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(readsBeforePromotion);
      const promoted = threadsStore.getState().threads.get("local:one");
      if (!promoted) throw new Error("expected the promoted model");
      // Both streamed overlay entries survive promotion with their accumulated
      // text and tool output.
      expect(promoted.overlay?.["stream:s1:agentMessage"]?.item.text).toBe("alpha-streamed");
      expect(promoted.overlay?.["stream:s1:tool"]?.item.output).toBe("-tool-streamed");
      // Call-through storage observation plus a restored recorded body qualify
      // the opening path, rather than claiming that overlay display proves it.
      expect(localWindowReads.mock.calls.length).toBeGreaterThan(0);
      expect(promoted.history?.turns.flatMap((turn) => turn.items).map((item) => item.text)).toContain(
        "latest-recorded-item",
      );

      await act(async () => {
        rendered = render(view(promoted));
      });
      expect(screen.getAllByText("alpha-streamed").length).toBeGreaterThanOrEqual(1);
      expect(screen.getAllByText("-tool-streamed").length).toBeGreaterThanOrEqual(1);

      // Complete the turn: recorded history carries the retained stream content
      // plus later output and retires the live overlay.
      await act(async () => {
        fake.emitNotification({
          method: "history/updated",
          params: {
            threadId: "thr_local:one",
            ref: "local:one",
            bootGeneration: "1",
            epoch: 1,
            snapshot: { incarnation: "inc-1", length: 1 },
            turns: [{ id: "turn_1", status: "completed", itemsView: "full", items: [] }],
            items: [
              {
                type: "agentMessage",
                id: "a1",
                turnId: "turn_1",
                roundId: "r1",
                transcriptKey: "apptranscript-item-v2:turn_1:0:0",
                position: { entry: 1, item: 0 },
                version: 1,
                text: "alpha-streamed-later",
                status: "completed",
              },
              {
                type: "commandExecution",
                id: "tool1",
                turnId: "turn_1",
                roundId: "r1",
                callId: "call_tool",
                transcriptKey: "apptranscript-item-v2:turn_1:0:1",
                position: { entry: 1, item: 1 },
                version: 1,
                output: "-tool-streamed-later",
                status: "completed",
              },
            ],
          },
        } as AnyNotification);
      });

      const completed = threadsStore.getState().threads.get("local:one");
      if (!completed) throw new Error("expected the completed model");
      expect(completed.overlay).toEqual({});
      expect(completed.olderCursor).toBe("cursor_latest");

      await act(async () => {
        rendered?.rerender(view(completed));
      });
      expect(screen.getAllByText("alpha-streamed-later")).toHaveLength(1);
      expect(screen.getAllByText("-tool-streamed-later")).toHaveLength(1);
      // No leftover live overlay copy survives retirement.
      expect(screen.queryByText("alpha-streamed")).toBeNull();
      expect(screen.queryByText("-tool-streamed")).toBeNull();

      await act(async () => {
        rendered?.unmount();
        rendered = undefined;
      });
      // The paging boundary continues past the completed turn.
      await threadsStore.getState().loadOlderTurns("local:one");
      const paged = threadsStore.getState().threads.get("local:one");
      if (!paged) throw new Error("expected the paged model");
      expect(paged.olderCursor).toBe("cursor_next");
      expect(paged.turns.flatMap((turn) => turn.items).map((item) => item.text)).toContain("older-completed-item");
    } finally {
      rendered?.unmount();
      cleanup();
      if (paneHeld) threadsStore.getState().releaseThread("local:one");
      try {
        await background.release();
      } finally {
        resetThreadsStoreForTests();
        restoreLocalWindowObserver?.();
        restoreOptions();
      }
    }
  });
});
