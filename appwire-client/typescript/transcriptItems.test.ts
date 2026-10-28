import { expect, test } from "vitest";
import type { ItemModel } from "./model";
import { itemAt, turnAt } from "./testing/modelAccessors";
import { applyHistory, testHydrateV6 } from "./testing/reducerTestKit";
import { decodeTranscriptItem, encodeTranscriptItem } from "./transcriptCache";
import {
  imageSessionRouteForSession,
  itemTextPresence,
  markItemIdentityOnly,
  markItemTextOmitted,
} from "./transcriptItems";

test("entry item codec preserves omitted text, identity-only markers, and rich recorded fields", () => {
  const omitted = markItemTextOmitted({ id: "item-1", turnId: "turn-1", type: "assistant", text: "" });
  const explicitEmpty: ItemModel = { id: "item-2", turnId: "turn-1", type: "assistant", text: "" };
  const identity = markItemIdentityOnly({ id: "item-3", turnId: "turn-1", type: "toolResult", text: "" });
  const rich: ItemModel = {
    id: "item-4",
    turnId: "turn-1",
    type: "toolResult",
    text: "tool result",
    output: "structured result",
    error: "denied",
    images: [{ src: "data:image/png;base64,AA==", name: "in.png" }],
    reasoningSummaries: [["reasoning chunk"]],
  };

  const decodedOmitted = decodeTranscriptItem(encodeTranscriptItem(omitted));
  const decodedEmpty = decodeTranscriptItem(encodeTranscriptItem(explicitEmpty));
  const decodedIdentity = decodeTranscriptItem(encodeTranscriptItem(identity));
  const decodedRich = decodeTranscriptItem(encodeTranscriptItem(rich));
  expect(decodedOmitted && itemTextPresence(decodedOmitted)).toBe("omitted");
  expect(decodedEmpty && itemTextPresence(decodedEmpty)).toBe("provided");
  expect(decodedIdentity && encodeTranscriptItem(decodedIdentity).identityOnly).toBe(true);
  expect(decodedRich).toMatchObject(rich);
  expect(decodeTranscriptItem({ id: "bad" })).toBeUndefined();
});

// szw1: reducer.ts's turn/started and turn/completed have no defense against
// a duplicate turn id in model.turns. Both known causes of a live collision
// (eptj, bz2z) are fixed server-side, so this is hardening against a defect
// this reducer would still have if a duplicate ever arrived by some other
// path — turns is presented everywhere else (mapTurn, findItemTurnId) as if
// ids are unique. The failure must not be both silent and destructive: a
// duplicate id is loudly reported (console.error — a reducer is a bad place
// to throw) AND handled without clobbering unrelated data.

// Part A regression coverage: the real wire's turn/completed is a bare
// status/timing stamp with no items (see the case's own comment in
// reducer.ts for the full projector-site citation list). A settled turn
// must KEEP whatever items the model already accumulated via
// item/started + deltas + item/completed, not wipe them.

test("item/completed's own reasoning text differs from the seeded chunks and replaces them", () => {
  // The row's chunks were seeded from the item's own text (item/started here;
  // hydrate's wireItemToModel does the same). Item/completed's explicit text
  // is authoritative for a reasoning row exactly as it is for assistant text
  // (mergeCompletedText): the settle carries the complete flattened reasoning,
  // so it corrects the row instead of leaving the stale seed on screen.
  let model = testHydrateV6();
  model = applyHistory(
    model,
    {
      turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
    },
    1001,
  );
  model = applyHistory(
    model,
    {
      items: [
        {
          ...{ type: "reasoning", id: "item_r", turnId: "turn_1", text: "stale partial seed", status: "inProgress" },
          turnId: "turn_1",
        },
      ],
    },
    1002,
  );
  expect(itemAt(turnAt(model, 0), 0).reasoningSummaries).toEqual([["stale partial seed"]]);

  model = applyHistory(
    model,
    {
      items: [
        {
          ...{ type: "reasoning", id: "item_r", turnId: "turn_1", text: "settled reasoning", status: "completed" },
          turnId: "turn_1",
        },
      ],
    },
    1003,
  );

  const settled = itemAt(turnAt(model, 0), 0);
  expect(settled.text).toBe("settled reasoning");
  expect(settled.reasoningSummaries).toEqual([["settled reasoning"]]);
});

test("imageSessionRouteForSession escapes the session id and rejects what cannot serve", () => {
  expect(imageSessionRouteForSession("sess_t")).toBe("sess_t");
  expect(imageSessionRouteForSession("")).toBeUndefined();
  expect(imageSessionRouteForSession("proj/one")).toBeUndefined();
});
