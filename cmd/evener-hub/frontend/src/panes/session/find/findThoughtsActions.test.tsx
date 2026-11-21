// Find against the real transcript renderers: thoughts and action rows only
// become readable once the disclosure holding them opens, so these tests mount
// the actual rows and prove the counted match lands with its words on screen.
import {
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
import { FIND_CURRENT_ATTRIBUTE } from "./findMarks";
import { requestFindInSession, resetFindRequestsForTests } from "./findRequest";
import { useFindInSession } from "./useFindInSession";

const REF = "ref_a";
const SCOPE = "find-thoughts-and-actions";

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

function reasoning(
  id: string,
  text: string,
  status: "completed" | "inProgress" = "completed",
): TurnModel["items"][number] {
  return {
    id,
    turnId: "t1",
    type: "reasoning",
    text: "",
    status,
    reasoningSummaries: [[text]],
  } as TurnModel["items"][number];
}

function action(id: string, rationale: string): TurnModel["items"][number] {
  return {
    id,
    turnId: "t1",
    type: "commandExecution",
    text: "",
    status: "completed",
    toolName: "shell",
    description: rationale,
  } as TurnModel["items"][number];
}

function readFile(id: string, path: string): TurnModel["items"][number] {
  return {
    id,
    turnId: "t1",
    type: "commandExecution",
    text: "",
    status: "completed",
    toolName: "read_file",
    argumentsJSON: JSON.stringify({ file_path: path }),
  } as TurnModel["items"][number];
}

function taskListView(id: string, rationale: string): TurnModel["items"][number] {
  return {
    id,
    turnId: "t1",
    type: "commandExecution",
    text: "",
    status: "completed",
    toolName: "task_list",
    argumentsJSON: JSON.stringify({ action: "view" }),
    description: rationale,
  } as TurnModel["items"][number];
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
const currentAnchor = () => document.querySelector(`[${FIND_CURRENT_ATTRIBUTE}]`) as HTMLElement | null;

beforeEach(() => {
  registry = new Map();
  vi.stubGlobal("Highlight", FakeHighlight);
  vi.stubGlobal("CSS", { highlights: registry });
  // jsdom has no Range geometry; a match occupies its anchor's test geometry.
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

test("a thought summary match lands with its words visible in the mounted row", async () => {
  const config = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });
  const model = modelOf([turn([reasoning("r1", "the **needle** sits here")])]);
  requestFindInSession(REF, { query: "needle sits" });
  render(<Harness model={model} config={config} />);

  await waitFor(() => expect(count()).toBe(1));
  await waitFor(() => expect(currentAnchor()?.getAttribute("data-view-anchor-id")).toBe("r1"));
  // The thought's body is behind its disclosure: landing opens it, so the
  // counted words are on screen rather than in an unopened fold. The body
  // mounts lazily and the highlight paints on the following frame, so wait for
  // both instead of reading them the instant the anchor is stamped.
  await waitFor(() => {
    expect(currentAnchor()?.querySelector("details")?.open).toBe(true);
    expect(currentAnchor()?.textContent).toContain("needle sits");
    expect(painted("evener-find-current")).toContain("needle sits");
  });
});

test("a thought is not counted where the chat view hides thinking traces", async () => {
  const config = makeTranscriptDisplayConfig({ kind: "preset", level: "chat" });
  const model = modelOf([turn([reasoning("r1", "a hidden needle", "inProgress")], "inProgress")]);
  requestFindInSession(REF, { query: "needle" });
  render(<Harness model={model} config={config} />);

  await waitFor(() => expect(bar().getAttribute("data-find-status")).toBe("none"));
  expect(count()).toBe(0);
  expect(currentAnchor()).toBeNull();
});

test("a streaming thought's unclosed Markdown lands on the words its live row shows", async () => {
  const config = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });
  const model = modelOf([turn([reasoning("r1", "check **the assumptions", "inProgress")], "inProgress")]);
  requestFindInSession(REF, { query: "check the assumptions" });
  render(<Harness model={model} config={config} />);

  await waitFor(() => expect(count()).toBe(1));
  await waitFor(() => expect(currentAnchor()?.getAttribute("data-view-anchor-id")).toBe("r1"));
  await waitFor(() => {
    expect(currentAnchor()?.textContent).toContain("check the assumptions");
    expect(currentAnchor()?.textContent).not.toContain("**");
  });
});

// The wire never completes a reasoning item: once a reply starts in the same
// in-progress turn, the thought is no longer the tail and ThinkBlock renders it
// settled, with the unclosed marker literal.
test("a thought a later streaming reply overtook lands on its literal markers", async () => {
  const config = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });
  const reply = {
    id: "a1",
    turnId: "t1",
    type: "agentMessage",
    text: "Answering now",
    status: "inProgress",
  } as TurnModel["items"][number];
  const model = modelOf([turn([reasoning("r1", "check **the assumptions", "inProgress"), reply], "inProgress")]);
  requestFindInSession(REF, { query: "check the assumptions" });
  render(<Harness model={model} config={config} />);

  await waitFor(() => expect(bar().getAttribute("data-find-status")).toBe("none"));
  expect(count()).toBe(0);

  act(() => requestFindInSession(REF, { query: "check **the assumptions" }));
  await waitFor(() => expect(count()).toBe(1));
  await waitFor(() => expect(currentAnchor()?.getAttribute("data-view-anchor-id")).toBe("r1"));
  await waitFor(() => expect(currentAnchor()?.textContent).toContain("check **the assumptions"));
});

test("an action row with no stated rationale never matches the projector's fallback text", async () => {
  const config = makeTranscriptDisplayConfig({
    kind: "custom",
    toolIntent: true,
    toolCalls: false,
    reasoning: false,
    expandByDefault: false,
    thinkingOpenByDefault: false,
  });
  const model = modelOf([turn([readFile("c1", "agent/tree.go")])]);
  render(<Harness model={model} config={config} />);

  act(() => requestFindInSession(REF, { query: "action" }));
  await waitFor(() => expect(bar().getAttribute("data-find-status")).toBe("none"));
  expect(count()).toBe(0);
});

test("a suppressed action row is never counted, because it renders nothing", async () => {
  const config = makeTranscriptDisplayConfig({
    kind: "custom",
    toolIntent: true,
    toolCalls: false,
    reasoning: false,
    expandByDefault: false,
    thinkingOpenByDefault: false,
  });
  const model = modelOf([turn([taskListView("c1", "view the task list")])]);
  render(<Harness model={model} config={config} />);

  act(() => requestFindInSession(REF, { query: "view the task list" }));
  await waitFor(() => expect(bar().getAttribute("data-find-status")).toBe("none"));
  expect(count()).toBe(0);
});

test("an action rationale match opens the collapsed action group and lands on its words", async () => {
  const config = makeTranscriptDisplayConfig({
    kind: "custom",
    toolIntent: true,
    toolCalls: false,
    reasoning: false,
    expandByDefault: false,
    thinkingOpenByDefault: false,
  });
  const model = modelOf([turn([action("c1", "list the repo"), action("c2", "grep for the needle")])]);
  render(<Harness model={model} config={config} />);

  const group = () => screen.getByTestId("intent-group") as HTMLDetailsElement;
  expect(group().open).toBe(false);

  act(() => requestFindInSession(REF, { query: "grep for the needle" }));
  await waitFor(() => expect(count()).toBe(1));
  await waitFor(() => expect(currentAnchor()?.getAttribute("data-view-anchor-id")).toBe("intent:c2"));
  await waitFor(() => {
    expect(group().open).toBe(true);
    expect(currentAnchor()?.textContent).toContain("grep for the needle");
    expect(painted("evener-find-current")).toContain("grep for the needle");
  });
});
