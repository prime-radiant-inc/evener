// Find against the real transcript renderers: a tool call's words can sit
// behind a folded run, its own body disclosure, or a body that clips or folds
// its output away. These tests mount the actual rows and prove every counted
// match lands with its words on screen - the invariant #6336 keeps.
import {
  type ItemModel,
  makeTranscriptDisplayConfig,
  projectThread,
  type ThreadModel,
  type TranscriptDisplayConfigV1,
  type TurnModel,
} from "@evener/appwire-client";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { useMemo, useRef } from "react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { makeTranscriptPreviewModel } from "../../../transcriptDisplay/previewFixture";
import type { VirtualListHandle } from "../../../widgets";
import { resetDisclosureStoreForTests } from "../../../widgets/disclosure/disclosureStore";
import { TranscriptBody, transcriptRowsForProjection } from "../transcript/TranscriptBody";
import { FindBar } from "./FindBar";
import { requestFindInSession, resetFindRequestsForTests } from "./findRequest";
import { resetFindRevealForTests } from "./findReveal";
import { useFindInSession } from "./useFindInSession";

const REF = "ref_a";
const SCOPE = "find-tool-calls";
const TOOLS = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });

class FakeHighlight {
  readonly ranges: Range[];
  constructor(...ranges: Range[]) {
    this.ranges = ranges;
  }
}

let registry: Map<string, FakeHighlight>;
const rangeRect = Object.getOwnPropertyDescriptor(Range.prototype, "getBoundingClientRect");
const painted = (name: string) => (registry.get(name)?.ranges ?? []).map((range) => range.toString());

function turn(items: TurnModel["items"], status: TurnModel["status"] = "completed"): TurnModel {
  return { id: "t1", status, items };
}

function modelOf(turns: TurnModel[]): ThreadModel {
  return { ...makeTranscriptPreviewModel(), ref: REF, turns };
}

function call(id: string, fields: Partial<ItemModel>): TurnModel["items"][number] {
  return {
    id,
    turnId: "t1",
    type: "commandExecution",
    text: "",
    status: "completed",
    toolName: "shell",
    ...fields,
  } as TurnModel["items"][number];
}

// The three settled, uneventful calls TurnBlock folds into one run row.
function foldedRun(outputOf: string): TurnModel["items"] {
  return [
    call("c1", { argumentsJSON: '{"command":"echo one"}', output: "one" }),
    call("c2", { argumentsJSON: '{"command":"echo two"}', output: outputOf }),
    call("c3", { argumentsJSON: '{"command":"echo three"}', output: "three" }),
  ];
}

function Harness({ model, config }: { model: ThreadModel; config: TranscriptDisplayConfigV1 }) {
  const port = useRef<HTMLDivElement>(null);
  const list = useRef<VirtualListHandle>({
    scrollToIndex: () => {},
    getScrollElement: () => port.current,
    isLayoutCurrent: () => true,
    getVisibleRange: () => null,
  });
  const rows = useMemo(() => transcriptRowsForProjection(projectThread(model, config)), [model, config]);
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
    cwd: model.cwd,
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

const bar = () => screen.getByTestId("find-bar");
const count = () => Number(bar().getAttribute("data-find-count"));
const callRow = (id: string) =>
  [...document.querySelectorAll<HTMLElement>("[data-item-id]")].find(
    (row) => row.getAttribute("data-item-id") === id,
  ) ?? null;

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
  resetFindRevealForTests();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  resetDisclosureStoreForTests();
  resetFindRevealForTests();
  if (rangeRect) Object.defineProperty(Range.prototype, "getBoundingClientRect", rangeRect);
  else Reflect.deleteProperty(Range.prototype, "getBoundingClientRect");
});

// A settled turn folds a run of quiet calls into one closed row. Words in one
// of those calls are behind two disclosures: the run and the call itself.
test("a match inside a folded tool run opens the run and the call and lands on its words", async () => {
  const model = modelOf([turn(foldedRun("the needle line"))]);
  render(<Harness model={model} config={TOOLS} />);

  const run = () => screen.getByTestId("tool-run") as HTMLDetailsElement;
  expect(run().open).toBe(false);

  act(() => requestFindInSession(REF, { query: "needle" }));
  await waitFor(() => expect(count()).toBe(1));
  await waitFor(() => {
    expect(run().open).toBe(true);
    const body = callRow("c2")?.querySelector('[data-testid="tool-call-body"]');
    expect(body?.textContent).toContain("needle");
    expect(painted("evener-find-current")).toContain("needle");
  });
});

// Coloured terminal output: the escape sequence falls inside the phrase, so a
// reader sees "FAIL here" and Find must read the same words.
test("coloured shell output is searched and landed on with its control codes interpreted", async () => {
  const model = modelOf([
    turn([call("c1", { argumentsJSON: '{"command":"make"}', output: "build \u001b[31mFAIL\u001b[0m here" })]),
  ]);
  render(<Harness model={model} config={TOOLS} />);

  act(() => requestFindInSession(REF, { query: "FAIL here" }));
  await waitFor(() => expect(count()).toBe(1));
  await waitFor(() => {
    expect(callRow("c1")?.querySelector('[data-testid="tool-call-body"]')?.textContent).toContain("FAIL here");
    expect(painted("evener-find-current")).toContain("FAIL here");
  });
});

// A long read shows only its tail (the shared body's tail fold, then the code
// block's line fold). The match sits in the head, so the row shows an excerpt
// of the stretch that holds it rather than counting a match nobody can see.
test("a match a tool body folds away is revealed in the mounted row", async () => {
  const output = [
    "the needle line",
    ...Array.from({ length: 600 }, (_, index) => `line ${index} ${"x".repeat(24)}`),
  ].join("\n");
  const model = modelOf([
    turn([call("c1", { toolName: "read_file", argumentsJSON: '{"file_path":"a.log"}', output })]),
  ]);
  render(<Harness model={model} config={TOOLS} />);

  act(() => requestFindInSession(REF, { query: "needle line" }));
  await waitFor(() => expect(count()).toBe(1));
  await waitFor(() => {
    const excerpt = callRow("c1")?.querySelector("[data-find-excerpt]");
    expect(excerpt?.textContent).toContain("needle line");
    expect(painted("evener-find-current")).toContain("needle line");
  });
});

// A tool with no descriptor of its own renders its arguments as JSON. Their
// string values are words on the page; the JSON's keys and punctuation are not.
test("a call's argument values are searched, its JSON syntax is not", async () => {
  const model = modelOf([
    turn([
      call("c1", {
        toolName: "github: create_issue",
        argumentsJSON: JSON.stringify({ title: "Add needle support", labels: ["bug"] }),
        output: "created #1",
      }),
    ]),
  ]);
  render(<Harness model={model} config={TOOLS} />);

  act(() => requestFindInSession(REF, { query: "needle support" }));
  await waitFor(() => expect(count()).toBe(1));
  await waitFor(() => {
    expect(callRow("c1")?.querySelector('[data-testid="tool-call-body"]')?.textContent).toContain("needle support");
    expect(painted("evener-find-current")).toContain("needle support");
  });

  // The field name and the braces are plumbing no row paints as words.
  act(() => requestFindInSession(REF, { query: "labels" }));
  await waitFor(() => expect(bar().getAttribute("data-find-status")).toBe("none"));
  expect(count()).toBe(0);
});

test("a call's error text is searched and landed on once its body opens", async () => {
  const model = modelOf([
    turn([
      call("c1", {
        argumentsJSON: '{"command":"rm notes.txt"}',
        error: "needle not permitted",
        status: "failed",
      }),
    ]),
  ]);
  render(<Harness model={model} config={TOOLS} />);

  act(() => requestFindInSession(REF, { query: "not permitted" }));
  await waitFor(() => expect(count()).toBe(1));
  await waitFor(() => {
    expect(callRow("c1")?.querySelector('[data-testid="tool-call-body"]')?.textContent).toContain("not permitted");
    expect(painted("evener-find-current")).toContain("not permitted");
  });
});

// A descriptor that suppresses its row renders nothing at all, so its text is
// not text Find could ever land on.
test("a suppressed tool call is not counted, because it renders nothing", async () => {
  const model = modelOf([
    turn([
      call("c1", {
        toolName: "task_list",
        argumentsJSON: '{"action":"view"}',
        output: "needle in the task list",
      }),
    ]),
  ]);
  render(<Harness model={model} config={TOOLS} />);

  act(() => requestFindInSession(REF, { query: "needle" }));
  await waitFor(() => expect(bar().getAttribute("data-find-status")).toBe("none"));
  expect(count()).toBe(0);
  expect(callRow("c1")).toBeNull();
});

// A match on the call's own summary line needs only that line's disclosure:
// nothing is hidden in the body, so its excerpt must not appear.
test("a summary-line match opens only the summary and shows no excerpt", async () => {
  const model = modelOf([turn([call("c1", { argumentsJSON: '{"command":"grep needler"}', output: "clean" })])]);
  render(<Harness model={model} config={TOOLS} />);

  act(() => requestFindInSession(REF, { query: "needler" }));
  await waitFor(() => expect(count()).toBe(1));
  await waitFor(() => expect(painted("evener-find-current")).toContain("needler"));
  expect(callRow("c1")?.querySelector("[data-find-excerpt]")).toBeNull();
});

// A delegate row deliberately paints no summary line (subagentModule owns its
// presentation), so the descriptor's "Used delegate" words are on no screen and
// Find must not count them. The brief on the row's intent line is on screen,
// and a shell call's own summary still counts and paints.
test("a delegate row's unpainted summary is not counted, its brief is", async () => {
  const model = modelOf([
    turn([
      call("c1", {
        toolName: "delegate",
        argumentsJSON: JSON.stringify({ prompt: "look into the needlebrief" }),
        output: "the needlebody",
      }),
      call("c2", { argumentsJSON: '{"command":"make needlesum"}' }),
    ]),
  ]);
  render(<Harness model={model} config={TOOLS} />);

  act(() => requestFindInSession(REF, { query: "used delegate" }));
  await waitFor(() => expect(bar().getAttribute("data-find-status")).toBe("none"));
  expect(count()).toBe(0);

  act(() => requestFindInSession(REF, { query: "needlebrief" }));
  await waitFor(() => expect(count()).toBe(1));

  act(() => requestFindInSession(REF, { query: "needlesum" }));
  await waitFor(() => expect(count()).toBe(1));
  await waitFor(() => expect(painted("evener-find-current")).toContain("needlesum"));
});
