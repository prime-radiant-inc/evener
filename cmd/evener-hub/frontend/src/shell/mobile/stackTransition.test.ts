import { expect, test } from "vitest";
import { type ScrollOffsets, startStackTransition } from "./stackTransition";

const CLASSES = { enter: "enter", leavingUnder: "under", leavingOver: "over" };

// A transition with no animation to wait for (reduced motion, or a document
// that runs none) is over before the call returns. Nothing is then left to
// cancel, and a caller holding a cancel function would keep the discarded
// pane's DOM reachable through it.
test.each(["push", "pop"] as const)("a %s that completes at once hands back nothing to cancel", (kind) => {
  const body = document.createElement("div");
  const outgoing = document.createElement("div");
  const incoming = document.createElement("div");
  body.append(incoming);
  document.body.append(body);
  const offsets: ScrollOffsets = new Map();
  let ended = 0;

  const cancel = startStackTransition(body, kind, outgoing, incoming, offsets, CLASSES, () => {
    ended += 1;
  });

  expect(ended).toBe(1);
  expect(cancel).toBeNull();
  expect(outgoing.isConnected).toBe(false);
  expect(body.hasAttribute("data-stack-transition")).toBe(false);
  body.remove();
});
