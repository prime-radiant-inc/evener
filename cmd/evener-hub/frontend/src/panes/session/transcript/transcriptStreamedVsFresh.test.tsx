// Streamed-vs-fresh contract for the live transcript: a mounted surface that
// keeps receiving streamed models must render exactly what a fresh mount of
// the final model renders - same rows, same order, same contents - at every
// frame and after settle.
//
// This test passes on the pre-change code too. It pins the user-visible
// contract the incremental derivation must preserve, so the work-reduction
// test can be trusted to have changed only how the answer is computed.
import type { AnyNotification, ThreadModel } from "@evener/appwire-client";
import {
  applyNotification,
  createTranscriptProjector,
  hydrateThread,
  makeTranscriptDisplayConfig,
  projectThread,
  type TranscriptProjection,
} from "@evener/appwire-client";
import { cleanup, render } from "@testing-library/react";
import { afterEach, describe, expect, test } from "vitest";
import { resetTranscriptViewRegistryForTests } from "./flow/transcriptViewRegistry";
import { TranscriptBody, transcriptAnchorEntriesForRows, transcriptRowsForProjection } from "./TranscriptBody";

const CONFIG = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });
const LIVE_TURN_ID = "turn-live";

function wireMessage(turnId: string, index: number, entry: number) {
  return {
    id: `item-${turnId}-${index}`,
    turnId,
    type: index % 3 === 2 ? "commandExecution" : "agentMessage",
    text: `body ${turnId} ${index}`,
    description: index % 3 === 2 ? `did thing ${turnId} ${index}` : undefined,
    status: "completed",
    position: { entry, item: index },
  };
}

function fullThread(turnCount: number): ThreadModel {
  const turns = Array.from({ length: turnCount }, (_, t) => ({
    id: `turn-${t}`,
    status: "completed",
    version: 1,
    itemsView: "full",
    items: Array.from({ length: 3 }, (_, i) => wireMessage(`turn-${t}`, i, t)),
  }));
  return hydrateThread(
    {
      thread: {
        id: "thr_contract",
        sessionId: "sess_contract",
        preview: "contract",
        ephemeral: false,
        modelProvider: "test",
        createdAt: 1,
        updatedAt: 1,
        status: { type: "idle" },
        cwd: "/tmp/project",
        cliVersion: "1.0.0",
        source: "evener",
        evener: { ref: "ref_contract", capabilities: {}, queue: { revision: 0 } },
        turns,
      },
      bootGeneration: "1",
      epoch: 1,
      snapshot: { incarnation: "inc", length: turnCount },
    } as never,
    "ref_contract",
    1000,
  );
}

function openStream(model: ThreadModel): ThreadModel {
  return applyNotification(
    model,
    {
      method: "overlay/upserted",
      params: {
        threadId: "thr_contract",
        ref: "ref_contract",
        item: {
          key: "stream:r1/0:agentMessage",
          kind: "stream",
          turnId: LIVE_TURN_ID,
          roundId: "r1",
          streamId: "r1/0",
          item: {
            type: "agentMessage",
            id: "stream:r1/0:agentMessage",
            turnId: LIVE_TURN_ID,
            roundId: "r1",
            status: "inProgress",
          },
        },
      },
    } as AnyNotification,
    1001,
  );
}

function delta(model: ThreadModel, text: string, now: number): ThreadModel {
  return applyNotification(
    model,
    {
      method: "overlay/delta",
      params: {
        threadId: "thr_contract",
        ref: "ref_contract",
        key: "stream:r1/0:agentMessage",
        field: "text",
        delta: text,
      },
    } as AnyNotification,
    now,
  );
}

function settle(model: ThreadModel): ThreadModel {
  const ended = applyNotification(
    model,
    {
      method: "overlay/end",
      params: { threadId: "thr_contract", ref: "ref_contract", roundId: "r1" },
    } as AnyNotification,
    1003,
  );
  return applyNotification(
    ended,
    {
      method: "history/updated",
      params: {
        threadId: "thr_contract",
        ref: "ref_contract",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc", length: 6 },
        turns: [{ id: LIVE_TURN_ID, status: "completed", version: 1, itemsView: "" }],
        items: [
          {
            type: "agentMessage",
            id: "item-live",
            turnId: LIVE_TURN_ID,
            roundId: "r1",
            text: "token0 token1 token2 token3 done",
            status: "completed",
            position: { entry: 6, item: 0 },
          },
        ],
      },
    } as AnyNotification,
    1004,
  );
}

/** The rendered transcript's user-visible shape: every row's identity and every
 * row's whole rendered text, in document order. Rows are the virtual list's
 * own keys, so a reorder, a duplicate or a dropped row shows up here, and the
 * row text covers both a streaming body and a settled one. */
function renderedShape(container: HTMLElement): string[] {
  return [...container.querySelectorAll("[data-testid='transcript-row']")].map(
    (row) =>
      `row:${row.getAttribute("data-row-id") ?? ""}|turn:${
        row.querySelector("[data-turn-id]")?.getAttribute("data-turn-id") ?? ""
      }|text:${(row.textContent ?? "").replace(/\s+/g, " ").trim()}`,
  );
}

function body(model: ThreadModel) {
  return <TranscriptBody model={model} config={CONFIG} surface="preview" disclosureScope="contract:compare" />;
}

/** A frame's derivation as a comparable value: what the virtual list keys on
 * (row ids), what scroll restoration keys on (anchor id and source index), and
 * the rows' full contents. A projection the pane reused must produce exactly
 * this. */
function derivedShape(projection: TranscriptProjection) {
  const rows = transcriptRowsForProjection(projection);
  return {
    projection,
    rowIds: rows.map((row) => row.id),
    anchors: transcriptAnchorEntriesForRows(rows).map((anchor) => `${anchor.id}@${anchor.sourceIndex}#${anchor.index}`),
  };
}

/** A full derivation, computed from nothing: the "fresh" half of every
 * comparison. */
function freshProjection(model: ThreadModel): TranscriptProjection {
  return projectThread(model, CONFIG);
}

afterEach(() => {
  cleanup();
  resetTranscriptViewRegistryForTests();
});

describe("streamed vs fresh", () => {
  test("a mounted surface streamed frame by frame matches a fresh mount of each frame", () => {
    let model = openStream(fullThread(5));
    const frames: ThreadModel[] = [model];
    for (let step = 0; step < 4; step += 1) {
      model = delta(model, `token${step} `, 1002 + step);
      frames.push(model);
    }
    frames.push(settle(model));

    // The same reuse the live pane gets: one projector across the stream,
    // against a fresh whole-thread derivation for every frame.
    const projector = createTranscriptProjector();
    const mounted = render(body(frames[0] as ThreadModel));
    for (const frame of frames) {
      mounted.rerender(body(frame));
      const streamed = renderedShape(mounted.container);
      // Row identities are the list's own keys: a reorder or a duplicate row
      // would show up here before the contents did.
      expect(streamed.length).toBeGreaterThan(0);

      // What the derivation handed the list must equal a fresh full
      // derivation of the same model: same rows, same order, same anchors
      // (and the same contents, through the projection itself).
      const reused = derivedShape(projector.project(frame, CONFIG));
      // A full derivation is what a fresh mount computes on its own.
      expect(reused).toEqual(derivedShape(freshProjection(frame)));

      // Fresh mount of the same model, rendered from nothing.
      const fresh = render(body(frame));
      expect(renderedShape(fresh.container)).toEqual(streamed);
      fresh.unmount();
    }
  });

  test("an earlier message edited mid-stream re-renders in place, matching a fresh mount", () => {
    let model = openStream(fullThread(4));
    model = delta(model, "streaming ", 1002);
    const mounted = render(body(model));
    // An edit to an already-recorded message, as history/updated delivers it.
    model = applyNotification(
      model,
      {
        method: "history/updated",
        params: {
          threadId: "thr_contract",
          ref: "ref_contract",
          bootGeneration: "1",
          epoch: 1,
          snapshot: { incarnation: "inc", length: 5 },
          turns: [{ id: "turn-0", status: "completed", version: 2, itemsView: "" }],
          items: [
            {
              type: "agentMessage",
              id: "item-turn-0-0",
              turnId: "turn-0",
              text: "edited body",
              status: "completed",
              version: 2,
              position: { entry: 0, item: 0 },
            },
          ],
        },
      } as AnyNotification,
      1003,
    );
    mounted.rerender(body(model));
    const edited = renderedShape(mounted.container);
    expect(edited.some((line) => line.includes("edited body"))).toBe(true);

    const fresh = render(body(model));
    expect(renderedShape(fresh.container)).toEqual(edited);
    fresh.unmount();
  });
});
