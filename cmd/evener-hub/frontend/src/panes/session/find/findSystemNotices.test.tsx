// Find against the real transcript renderers for system notices and warnings:
// a folded run of 3+ notices renders only its leader, a scaffold's words sit
// behind a disclosure, and a warning's words are a quiet line or a chip block.
// These tests count a match and prove it lands with its words on screen.
import {
  type ItemModel,
  makeTranscriptDisplayConfig,
  projectThread,
  type ThreadModel,
  type TranscriptDisplayConfigV1,
  type TurnModel,
  WarningCodeContextBudget,
} from "@evener/appwire-client";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { useMemo, useRef } from "react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { makeTranscriptPreviewModel } from "../../../transcriptDisplay/previewFixture";
import type { VirtualListHandle } from "../../../widgets";
import { resetDisclosureStoreForTests } from "../../../widgets/disclosure/disclosureStore";
import { TranscriptBody, transcriptRowsForProjection } from "../transcript/TranscriptBody";
import { FindBar } from "./FindBar";
import { FIND_CURRENT_ATTRIBUTE } from "./findMarks";
import { requestFindInSession, resetFindRequestsForTests } from "./findRequest";
import { useFindInSession } from "./useFindInSession";

const REF = "ref_a";
const SCOPE = "find-system-notices";

class FakeHighlight {
  readonly ranges: Range[];
  constructor(...ranges: Range[]) {
    this.ranges = ranges;
  }
}

let registry: Map<string, FakeHighlight>;
const rangeRect = Object.getOwnPropertyDescriptor(Range.prototype, "getBoundingClientRect");
const painted = (name: string) => (registry.get(name)?.ranges ?? []).map((range) => range.toString());

function system(id: string, text: string, overrides: Partial<ItemModel> = {}): ItemModel {
  return { id, turnId: "t1", type: "systemMessage", text, status: "completed", ...overrides };
}

function hook(id: string, exitCode: number): ItemModel {
  return system(id, `hook ${id} permission denied`, { eventKind: "hook_completed", exitCode });
}

function warning(id: string, overrides: Partial<ItemModel> = {}): ItemModel {
  return { id, turnId: "t1", type: "warning", text: "", status: "completed", ...overrides };
}

function turn(items: ItemModel[]): TurnModel {
  return { id: "t1", status: "completed", items };
}

function modelOf(turns: TurnModel[]): ThreadModel {
  return { ...makeTranscriptPreviewModel(), ref: REF, turns };
}

const SHOWN = makeTranscriptDisplayConfig(
  { kind: "preset", level: "tools" },
  { systemEvents: true, promptEvents: true, roundTimings: true },
);
const SHOWN_HOOKS = makeTranscriptDisplayConfig(
  { kind: "preset", level: "tools" },
  { systemEvents: true, promptEvents: true, roundTimings: true, hookExits: "successful" },
);
const FULL = makeTranscriptDisplayConfig(
  { kind: "preset", level: "full" },
  { systemEvents: true, promptEvents: true, roundTimings: true },
);

function Harness({ model, config }: { model: ThreadModel; config: TranscriptDisplayConfigV1 }) {
  const port = useRef<HTMLDivElement>(null);
  const list = useRef<VirtualListHandle>({
    scrollToIndex: () => {},
    getScrollElement: () => port.current,
    isLayoutCurrent: () => true,
    getVisibleRange: () => null,
  });
  const rows = useMemo(() => transcriptRowsForProjection(projectThreadOf(model, config)), [model, config]);
  const find = useFindInSession({
    ref: REF,
    paneId: "pane_a",
    rows,
    listRef: list,
    ownerDocument: document,
    jumpToRow: () => {},
    holdPosition: () => {},
    releaseHold: () => {},
    markGesture: () => {},
    olderPageKey: null,
    olderLoading: false,
    olderError: null,
    olderPermanent: false,
    findOlder: () => {},
    cancelFindOlder: () => {},
    ownsKeyboard: () => true,
    itemLoaded: () => true,
    transcriptMounted: true,
    config,
    disclosureScope: SCOPE,
  });
  return (
    <>
      {find.view && <FindBar view={find.view} />}
      <div ref={port}>
        <TranscriptBody model={model} config={config} surface="preview" disclosureScope={SCOPE} sessionRef={REF} />
      </div>
    </>
  );
}

// Kept as a local alias so the test harness reads like its sibling; the
// projection is the same one TranscriptBody would compute.
function projectThreadOf(model: ThreadModel, config: TranscriptDisplayConfigV1) {
  return projectThread(model, config);
}

const bar = () => screen.getByTestId("find-bar");
const count = () => Number(bar().getAttribute("data-find-count"));
const currentAnchor = () => document.querySelector(`[${FIND_CURRENT_ATTRIBUTE}]`) as HTMLElement | null;
const group = () => screen.queryByTestId("system-notice-group") as HTMLDetailsElement | null;

beforeEach(() => {
  registry = new Map();
  vi.stubGlobal("Highlight", FakeHighlight);
  vi.stubGlobal("CSS", { highlights: registry });
  Object.defineProperty(Range.prototype, "getBoundingClientRect", {
    configurable: true,
    value: function (this: Range) {
      return this.startContainer.parentElement?.getBoundingClientRect() ?? new DOMRect();
    },
  });
  resetDisclosureStoreForTests();
  resetFindRequestsForTests();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  resetDisclosureStoreForTests();
  if (rangeRect) Object.defineProperty(Range.prototype, "getBoundingClientRect", rangeRect);
  else Reflect.deleteProperty(Range.prototype, "getBoundingClientRect");
});

// --- a folded run of 3+ notices ---------------------------------------------

test("a match in a later member of a folded run lands on the leader, opens the group, and paints the body", async () => {
  const model = modelOf([
    turn([system("a", "first event"), system("b", "second needle event"), system("c", "third event")]),
  ]);
  requestFindInSession(REF, { query: "needle" });
  render(<Harness model={model} config={SHOWN} />);

  await waitFor(() => expect(count()).toBe(1));
  await waitFor(() => expect(currentAnchor()?.getAttribute("data-view-anchor-id")).toBe("a"));
  await waitFor(() => {
    expect(group()?.open).toBe(true);
    expect(currentAnchor()?.querySelector("[data-message-body]")?.textContent).toContain("second needle event");
    expect(painted("evener-find-current")).toEqual(["needle"]);
  });
});

test("the group's summary preview is never painted as the matched words", async () => {
  const model = modelOf([turn([system("a", "first alpha"), system("b", "beta"), system("c", "gamma")])]);
  requestFindInSession(REF, { query: "first" });
  render(<Harness model={model} config={SHOWN} />);

  // The preview under the summary also says "first alpha"; only the body copy
  // is a word on the page Find may paint.
  await waitFor(() => expect(count()).toBe(1));
  await waitFor(() => expect(group()?.open).toBe(true));
  expect(painted("evener-find-current")).toEqual(["first"]);
});

test("a hidden successful hook between notices does not split their run (open finding #1)", async () => {
  // hookExits "none" hides the hook, so the three notices are adjacent in the
  // rendered turn even though the source still lists the hook between them.
  const config = makeTranscriptDisplayConfig(
    { kind: "preset", level: "tools" },
    { systemEvents: true, hookExits: "none" },
  );
  const model = modelOf([
    turn([system("a", "alpha needle"), hook("h", 0), system("b", "beta needle"), system("c", "gamma needle")]),
  ]);
  requestFindInSession(REF, { query: "needle" });
  render(<Harness model={model} config={config} />);

  // Each member is one match; all land on the leader's single anchor.
  await waitFor(() => expect(count()).toBe(3));
  await waitFor(() => expect(currentAnchor()?.getAttribute("data-view-anchor-id")).toBe("a"));
  await waitFor(() => {
    expect(group()?.open).toBe(true);
    // Painted once per occurrence inside the opened body, not once per hit.
    expect(painted("evener-find-current")).toEqual(["needle", "needle", "needle"]);
  });
});

// --- failures, attention warnings and errors break runs ----------------------

test("a compact hook failure stays standalone and splits the notices around it", async () => {
  const model = modelOf([
    turn([system("a", "alpha"), system("b", "beta"), hook("f", 1), system("c", "gamma needle"), system("d", "delta")]),
  ]);
  requestFindInSession(REF, { query: "gamma needle" });
  render(<Harness model={model} config={SHOWN_HOOKS} />);

  // The failure splits the run, so gamma is its own row rather than a member
  // of a group led by alpha.
  await waitFor(() => expect(count()).toBe(1));
  await waitFor(() => expect(currentAnchor()?.getAttribute("data-view-anchor-id")).toBe("c"));
  expect(group()).toBeNull();
});

test("a compact hook failure's own words are searched on its standalone row", async () => {
  const model = modelOf([
    turn([system("a", "alpha"), system("b", "beta"), hook("f", 1), system("c", "gamma"), system("d", "delta")]),
  ]);
  requestFindInSession(REF, { query: "permission denied" });
  render(<Harness model={model} config={SHOWN_HOOKS} />);

  await waitFor(() => expect(count()).toBe(1));
  await waitFor(() => expect(currentAnchor()?.getAttribute("data-view-anchor-id")).toBe("f"));
  await waitFor(() => expect(painted("evener-find-current")).toEqual(["permission denied"]));
  expect(group()).toBeNull();
});

test("an attention warning and a turn error stay standalone and split a run", async () => {
  const attention = system("w", "disk full", {
    eventKind: "warning",
    raw: { warning: { title: "Evener error", hint: "free 2GB" } },
  });
  const failure = system("e", "provider said boom", { eventKind: "error" });
  const model = modelOf([
    turn([system("a", "alpha"), system("b", "beta"), attention, failure, system("c", "gamma"), system("d", "delta")]),
  ]);
  requestFindInSession(REF, { query: "2GB" });
  render(<Harness model={model} config={SHOWN} />);

  await waitFor(() => expect(count()).toBe(1));
  await waitFor(() => expect(currentAnchor()?.getAttribute("data-view-anchor-id")).toBe("w"));
  await waitFor(() => expect(painted("evener-find-current")).toEqual(["2GB"]));
  expect(group()).toBeNull();

  act(() => requestFindInSession(REF, { query: "boom" }));
  await waitFor(() => expect(count()).toBe(1));
  await waitFor(() => expect(currentAnchor()?.getAttribute("data-view-anchor-id")).toBe("e"));
  await waitFor(() => expect(painted("evener-find-current")).toEqual(["boom"]));
});

// --- scaffolds ---------------------------------------------------------------

test("a scaffold match opens its disclosure and lands on the rendered body, never the heading", async () => {
  const model = modelOf([turn([system("p", "# Notes\n\nA **bold** instruction.\n", { eventKind: "system_prompt" })])]);
  render(<Harness model={model} config={SHOWN} />);

  // The size heading ("System prompt · N chars") is chrome, not words.
  act(() => requestFindInSession(REF, { query: "System prompt" }));
  await waitFor(() => expect(bar().getAttribute("data-find-status")).toBe("none"));
  expect(count()).toBe(0);

  act(() => requestFindInSession(REF, { query: "bold instruction" }));
  await waitFor(() => expect(count()).toBe(1));
  await waitFor(() => expect(currentAnchor()?.getAttribute("data-view-anchor-id")).toBe("p"));
  await waitFor(() => {
    expect(currentAnchor()?.querySelector("details")?.open).toBe(true);
    expect(currentAnchor()?.querySelector("[data-message-body]")?.textContent).toContain("bold instruction");
    expect(painted("evener-find-current")).toEqual(["bold instruction"]);
  });
});

test("a notes-context disclosure opens even under the expand-all baseline it ignores", async () => {
  const model = modelOf([turn([system("n", "literal needle from shared notes", { eventKind: "notes-context" })])]);
  // Full starts an open-everything baseline, but notes snapshots ignore it and
  // stay folded until a reader (or Find) opens them explicitly.
  requestFindInSession(REF, { query: "literal needle" });
  render(<Harness model={model} config={FULL} />);

  await waitFor(() => expect(count()).toBe(1));
  await waitFor(() => expect(currentAnchor()?.getAttribute("data-view-anchor-id")).toBe("n"));
  await waitFor(() => {
    expect(currentAnchor()?.querySelector("details")?.open).toBe(true);
    expect(painted("evener-find-current")).toEqual(["literal needle"]);
  });
});

// --- round timings and approval decisions ------------------------------------

test("a round timings notice matches its dominant-phase headline, not its hover breakdown", async () => {
  const raw = {
    roundTimings: { round: 4, total_round_ns: 5_000_000_000, llm_call_ns: 4_000_000_000, tool_exec_ns: 500_000_000 },
  };
  const model = modelOf([turn([system("r", "Round 4 total=5s llm=4s", { eventKind: "round_timings", raw })])]);
  requestFindInSession(REF, { query: "LLM 4s" });
  render(<Harness model={model} config={SHOWN} />);

  await waitFor(() => expect(count()).toBe(1));
  await waitFor(() => expect(currentAnchor()?.getAttribute("data-view-anchor-id")).toBe("r"));
  await waitFor(() => expect(painted("evener-find-current")).toEqual(["LLM 4s"]));

  // The hover-only per-phase breakdown is not DOM text Find can land on.
  act(() => requestFindInSession(REF, { query: "total=" }));
  await waitFor(() => expect(bar().getAttribute("data-find-status")).toBe("none"));
  expect(count()).toBe(0);
});

test("an approval decision matches its words without landing on the timestamp", async () => {
  const raw = {
    approvalDecision: {
      escalationId: "e1",
      approved: true,
      tool: "write_file",
      kind: "file_tool",
      deniedPath: "/tmp/secret",
    },
  };
  const model = modelOf([
    turn([
      system("d", "Allowed: write /tmp/secret", {
        eventKind: "approval_decision",
        raw,
        startedAt: "2026-08-25T18:00:00.000Z",
      }),
    ]),
  ]);
  requestFindInSession(REF, { query: "secret" });
  render(<Harness model={model} config={SHOWN} />);

  await waitFor(() => expect(count()).toBe(1));
  await waitFor(() => expect(currentAnchor()?.getAttribute("data-view-anchor-id")).toBe("d"));
  await waitFor(() => {
    expect(currentAnchor()?.querySelector("[data-message-body]")?.textContent).toBe("Allowed: write /tmp/secret");
    expect(painted("evener-find-current")).toEqual(["secret"]);
  });

  const stamp = currentAnchor()?.textContent?.replace("Allowed: write /tmp/secret", "").trim() ?? "";
  expect(stamp).not.toBe("");
  act(() => requestFindInSession(REF, { query: stamp }));
  await waitFor(() => expect(bar().getAttribute("data-find-status")).toBe("none"));
  expect(count()).toBe(0);
});

// --- warnings ----------------------------------------------------------------

test("an attention warning from a daemon notice matches and paints its block", async () => {
  const notice = system("s", "disk almost full", {
    eventKind: "warning",
    raw: { warning: { title: "Evener error", hint: "free 2GB" } },
  });
  const model = modelOf([turn([notice])]);
  requestFindInSession(REF, { query: "free 2GB" });
  render(<Harness model={model} config={SHOWN} />);

  await waitFor(() => expect(count()).toBe(1));
  await waitFor(() => expect(currentAnchor()?.getAttribute("data-view-anchor-id")).toBe("s"));
  await waitFor(() => expect(painted("evener-find-current")).toEqual(["free 2GB"]));
});

test("an informational warning matches its quiet line and its hidden hint", async () => {
  const quiet = warning("q", {
    text: "context usage is high",
    warning: { code: WarningCodeContextBudget, hint: "no action needed" },
  });
  const model = modelOf([turn([quiet])]);
  requestFindInSession(REF, { query: "no action needed" });
  render(<Harness model={model} config={FULL} />);

  // The hint lives in a VisuallyHidden span, which Find still reads as text.
  await waitFor(() => expect(count()).toBe(1));
  await waitFor(() => expect(currentAnchor()?.getAttribute("data-view-anchor-id")).toBe("q"));
  await waitFor(() => expect(painted("evener-find-current")).toEqual(["no action needed"]));
});

test("an empty warning renders nothing, so it offers no match", async () => {
  const empty = warning("x", { text: "", warning: {} });
  const model = modelOf([turn([empty])]);
  requestFindInSession(REF, { query: "warning" });
  render(<Harness model={model} config={SHOWN} />);

  await waitFor(() => expect(bar().getAttribute("data-find-status")).toBe("none"));
  expect(count()).toBe(0);
  expect(currentAnchor()).toBeNull();
});
