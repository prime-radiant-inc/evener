// N1 (second task-1c browser review): a legitimately zero-height turn row must
// not veto older-history paging.
//
// This is a renderer-level regression: the model carries one ordinary turn
// whose content projects to real rows AND one interrupted daemon-steer turn
// that `projectThread` legitimately projects to zero entries at Chat (the
// shape transcriptProjector.test.ts pins). The real TranscriptBody -> VirtualList
// pipeline is mounted, so the empty turn keeps a real zero-height wrapper (a
// rendered .turn with no content) exactly as it does in the browser - not the
// bare synthetic divs the older scrollMetrics/LoadOlderRow tests use, which
// model hidden content as "no wrappers".
//
// The assertion reads the production paging predicate (shouldAutoLoadOlder)
// against that real port. jsdom performs no layout, so heights are supplied
// the way this repo's other transcript suites supply them (Session.test.tsx,
// virtuallist.test.tsx): a prototype offsetHeight that makes every element
// measure 500 during mount, then a per-element override of the empty turn's
// wrapper to 0. So this proves the predicate over a real rendered empty-turn
// wrapper whose OBSERVED height is zero with the virtualizer's ownership stamp
// present; it does not claim the virtualizer's own measurement callback read
// zero for that element. It does not weaken the transient hold - a bare
// unmeasured row still vetoes (the sibling scrollMetrics tests).
import type { ThreadModel } from "@evener/appwire-client";
import { makeTranscriptDisplayConfig } from "@evener/appwire-client";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, test } from "vitest";
import { resetDisclosureStoreForTests } from "../../../widgets/disclosure/disclosureStore";
import { shouldAutoLoadOlder } from "./flow/scrollMetrics";
import { resetTranscriptViewRegistryForTests } from "./flow/transcriptViewRegistry";
// Registers the tool descriptors the same way the real session pane does.
import "./TurnBlock";
import { TranscriptBody } from "./TranscriptBody";

const CHAT = makeTranscriptDisplayConfig({ kind: "preset", level: "chat" });

const model = {
  ref: "session:measured-zero",
  threadId: "thread_measured_zero",
  name: "Measured zero",
  status: { type: "idle" },
  modelProvider: "anthropic",
  model: "claude",
  askPending: false,
  pendingEscalations: [],
  turns: [
    {
      id: "turn_visible",
      status: "completed",
      items: [
        { id: "user_1", turnId: "turn_visible", type: "userMessage", text: "hello", status: "completed" },
        { id: "agent_1", turnId: "turn_visible", type: "agentMessage", text: "world", status: "completed" },
      ],
    },
    {
      // Projected to zero entries at Chat (transcriptProjector.test.ts's
      // "an interrupted turn's steer marker shows where steering shows").
      id: "turn_empty",
      status: "interrupted",
      items: [
        {
          id: "interrupted_steer",
          turnId: "turn_empty",
          type: "steering",
          text: "",
          steeringKind: "interrupted",
          status: "completed",
        },
      ],
    },
  ],
} as unknown as ThreadModel;

let offsetHeightDescriptor: PropertyDescriptor | undefined;

beforeEach(() => {
  resetDisclosureStoreForTests();
  resetTranscriptViewRegistryForTests();
  offsetHeightDescriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
  // jsdom computes no layout; give every element a box so the virtualizer
  // renders its rows (the same stub Session.test.tsx uses).
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", { configurable: true, value: 500 });
});

afterEach(() => {
  cleanup();
  resetDisclosureStoreForTests();
  resetTranscriptViewRegistryForTests();
  if (offsetHeightDescriptor) Object.defineProperty(HTMLElement.prototype, "offsetHeight", offsetHeightDescriptor);
});

test("a rendered empty turn alongside a visible turn does not veto short-page paging", () => {
  render(<TranscriptBody model={model} config={CHAT} surface="readOnly" disclosureScope="measured-zero:test" />);

  const section = screen.getByTestId("transcript-virtual-list");
  const port = section.firstElementChild as HTMLElement;
  const sizer = port.firstElementChild as HTMLElement;

  // The real render shape the reviewer named: the empty projected turn keeps
  // a wrapper with a real TurnBlock inside it, not "no wrapper".
  const emptyRow = sizer.querySelector('[data-row-id="turn_empty"]') as HTMLElement | null;
  expect(emptyRow).not.toBeNull();
  expect(emptyRow?.querySelector('[data-testid="turn-block"]')).not.toBeNull();
  const emptyWrapper = emptyRow?.parentElement as HTMLElement;
  // Model the settled empty turn's height: zero (turnblock.module.css has no
  // vertical padding, border or minimum height). The wrapper still carries the
  // virtualizer's `data-measured` stamp from its mount measurement.
  expect(emptyWrapper.hasAttribute("data-measured")).toBe(true);
  Object.defineProperty(emptyWrapper, "offsetHeight", { configurable: true, value: 0 });

  // A short page: the content does not fill the scroll port, so older history
  // should load. The visible turn keeps its positive measured height.
  Object.defineProperty(port, "offsetHeight", { configurable: true, value: 500 });
  Object.defineProperty(port, "clientHeight", { configurable: true, value: 500 });
  Object.defineProperty(port, "scrollHeight", { configurable: true, value: 320 });

  expect(shouldAutoLoadOlder(port)).toBe(true);
});
