// Find against the real steer and memory-refresh renderers. A steer's words and
// a memory refresh's index sit behind the divider their row mounts, keyed by
// the session ref plus the item id, so these tests mount the actual rows and
// prove a counted match lands with its words on screen - inside the body the
// divider unfolds, never in its summary or in a fold that stays closed.
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
import { beginDisclosureBaseline, resetDisclosureStoreForTests } from "../../../widgets/disclosure/disclosureStore";
import { resetMermaidRenderStateForTests } from "../../../widgets/mermaid/renderState";
import { TranscriptBody, transcriptRowsForProjection } from "../transcript/TranscriptBody";
import { FindBar } from "./FindBar";
import { FIND_CURRENT_ATTRIBUTE } from "./findMarks";
import { requestFindInSession, resetFindRequestsForTests } from "./findRequest";
import { useFindInSession } from "./useFindInSession";

const REF = "ref_a";
// A view id, as the session pane's own disclosure scope is: the steer and
// memory dividers are keyed by the session ref instead, so a landing has to
// open those through the session's own identity.
const SCOPE = "view:find-steers";

class FakeHighlight {
  readonly ranges: Range[];
  constructor(...ranges: Range[]) {
    this.ranges = ranges;
  }
}

let registry: Map<string, FakeHighlight>;
const rangeRect = Object.getOwnPropertyDescriptor(Range.prototype, "getBoundingClientRect");
const painted = (name: string) => (registry.get(name)?.ranges ?? []).map((range) => range.toString());

const TOOLS = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });

function turn(items: TurnModel["items"]): TurnModel {
  return { id: "t1", status: "completed", items };
}

function modelOf(items: TurnModel["items"]): ThreadModel {
  return { ...makeTranscriptPreviewModel(), ref: REF, turns: [turn(items)] };
}

function steer(id: string, fields: Partial<ItemModel> = {}): ItemModel {
  return { id, turnId: "t1", type: "steering", text: "", status: "completed", ...fields };
}

function memoryItem(id: string, text: string, memoryContext?: unknown): ItemModel {
  return {
    id,
    turnId: "t1",
    type: "systemMessage",
    eventKind: "memory-context",
    text,
    status: "completed",
    ...(memoryContext === undefined ? {} : { raw: { memoryContext } }),
  };
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
const anchorFor = (id: string) => document.querySelector(`[data-view-anchor-id="${id}"]`) as HTMLElement | null;

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
  resetMermaidRenderStateForTests();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  resetDisclosureStoreForTests();
  if (rangeRect) Object.defineProperty(Range.prototype, "getBoundingClientRect", rangeRect);
  else Reflect.deleteProperty(Range.prototype, "getBoundingClientRect");
});

// A steer the human typed is a prompt bubble rendered as Markdown: its words
// are on screen as rendered, with no fold to open.
test("a steer the human typed is counted and painted with nothing to open", async () => {
  const model = modelOf([steer("s1", { text: "stop and **rethink** the `parser`", source: "user" })]);
  requestFindInSession(REF, { query: "rethink the parser" });
  render(<Harness model={model} config={TOOLS} />);

  await waitFor(() => expect(count()).toBe(1));
  await waitFor(() => expect(currentAnchor()?.getAttribute("data-view-anchor-id")).toBe("s1"));
  await waitFor(() => {
    expect(currentAnchor()?.textContent).toContain("stop and rethink the parser");
    expect(painted("evener-find-current")).toContain("rethink the parser");
  });
  expect(currentAnchor()?.querySelector("details")).toBeNull();

  act(() => requestFindInSession(REF, { query: "**rethink**" }));
  await waitFor(() => expect(bar().getAttribute("data-find-status")).toBe("none"));
  expect(count()).toBe(0);
});

// A human note renders the labeled divider, folded. Its body is the one region
// Find searches, so a landing opens it and paints the steered words there.
test("a human note opens its session-keyed divider and paints its body", async () => {
  const model = modelOf([
    steer("hn1", { text: "whiteboard: brass widget", source: "user", steeringKind: "human-note" }),
  ]);
  requestFindInSession(REF, { query: "brass widget" });
  render(<Harness model={model} config={TOOLS} />);

  await waitFor(() => expect(count()).toBe(1));
  await waitFor(() => expect(currentAnchor()?.getAttribute("data-view-anchor-id")).toBe("hn1"));
  await waitFor(() => expect(currentAnchor()?.querySelector("details")?.hasAttribute("open")).toBe(true));
  await waitFor(() => expect(painted("evener-find-current")).toContain("brass widget"));
  // The divider's summary is chrome; only the body carries the searched words.
  const body = currentAnchor()?.querySelector("pre");
  expect(body?.getAttribute("data-message-body")).toBe("");
  // Verbatim: the <pre> shows the steered text and nothing around it.
  expect(body?.textContent).toBe("whiteboard: brass widget");
});

// The reminder wrapper is not on screen: the row strips it before rendering, so
// Find counts the visible text and never the tags around it.
test("a daemon steer is searched by the text its row shows, never the reminder wrapper", async () => {
  const model = modelOf([
    steer("d1", { text: "<SYSTEM-REMINDER>remember the brass widget</SYSTEM-REMINDER>", steeringKind: "nudge" }),
  ]);
  requestFindInSession(REF, { query: "brass widget" });
  render(<Harness model={model} config={TOOLS} />);

  await waitFor(() => expect(count()).toBe(1));
  await waitFor(() => expect(currentAnchor()?.getAttribute("data-view-anchor-id")).toBe("d1"));
  await waitFor(() => expect(painted("evener-find-current")).toContain("brass widget"));

  act(() => requestFindInSession(REF, { query: "SYSTEM-REMINDER" }));
  await waitFor(() => expect(bar().getAttribute("data-find-status")).toBe("none"));
  expect(count()).toBe(0);
});

// The tasks surfaces own the current task and the task list, so those steers
// render nothing at all - a match there would be a count with no row to show.
test.each(["current-task", "task-list"])("a suppressed %s steer renders nothing and is never counted", async (kind) => {
  const model = modelOf([steer("sup1", { text: "the brass widget task", steeringKind: kind })]);
  requestFindInSession(REF, { query: "brass widget" });
  render(<Harness model={model} config={TOOLS} />);

  await waitFor(() => expect(bar().getAttribute("data-find-status")).toBe("none"));
  expect(count()).toBe(0);
  expect(anchorFor("sup1")?.textContent ?? "").toBe("");
});

// A parent's message is conversation: quoted notification markup in it is
// text, not cards, so every word of it is on screen and searchable.
test("a parent steer keeps notification markup as text and opens its divider", async () => {
  const text = 'see <job-notification job_id="job_9">ran it</job-notification>';
  const model = modelOf([steer("p1", { text, source: "parent" })]);
  requestFindInSession(REF, { query: "job_9" });
  render(<Harness model={model} config={TOOLS} />);

  await waitFor(() => expect(count()).toBe(1));
  await waitFor(() => expect(currentAnchor()?.getAttribute("data-view-anchor-id")).toBe("p1"));
  await waitFor(() => expect(currentAnchor()?.querySelector("details")?.hasAttribute("open")).toBe(true));
  await waitFor(() => expect(painted("evener-find-current")).toContain("job_9"));
});

// A steer carrying a notification card also carries the text written between
// the cards. Each of those spans folds by its own per-fragment id, so a landing
// opens exactly the span holding the words and leaves its neighbours alone.
test("an interleaved steer opens only the text fragment holding the words", async () => {
  const card = `<job-notification job_id="job_5" event="failed" job_type="shell" status="failed" reason="bad" output_bytes="1" exit_code="3">
Job job_5 failed.
</job-notification>`;
  const text = `first fragment\n\n${card}\n\nsecond fragment with brass widget`;
  const model = modelOf([steer("mix1", { text })]);
  requestFindInSession(REF, { query: "brass widget" });
  render(<Harness model={model} config={TOOLS} />);

  await waitFor(() => expect(count()).toBe(1));
  const dividers = () =>
    Array.from(currentAnchor()?.querySelectorAll<HTMLDetailsElement>('[data-testid="steering-item"]') ?? []);
  await waitFor(() => expect(dividers()).toHaveLength(2));
  await waitFor(() => expect(dividers()[1]?.open).toBe(true));
  expect(dividers()[0]?.open).toBe(false);
  await waitFor(() => expect(painted("evener-find-current")).toContain("brass widget"));
  const cardDetails = currentAnchor()?.querySelector<HTMLDetailsElement>('[data-testid="notification-card"] details');
  expect(cardDetails?.open ?? false).toBe(false);
});

// A decoded refresh renders its index as Markdown. The complete recorded Text
// stays in a folded native Source the reader opens themselves, so a word that
// lives only there is not on screen and Find must not count it.
test("a memory refresh lands in its decoded index, not in the folded source", async () => {
  const model = modelOf([
    memoryItem("mem1", "Personal memory index: sourceOnlyWord", {
      scope: "project",
      state: "current",
      truncated: false,
      content: "# Project memory\n\n- brass widget plan",
    }),
  ]);
  requestFindInSession(REF, { query: "brass widget plan" });
  render(<Harness model={model} config={TOOLS} />);

  await waitFor(() => expect(count()).toBe(1));
  await waitFor(() => expect(currentAnchor()?.getAttribute("data-view-anchor-id")).toBe("mem1"));
  await waitFor(() => expect(currentAnchor()?.querySelector("details")?.hasAttribute("open")).toBe(true));
  await waitFor(() => {
    expect(currentAnchor()?.querySelector("[data-message-body]")?.textContent).toContain("brass widget plan");
    expect(painted("evener-find-current")).toContain("brass widget plan");
  });
  expect(currentAnchor()?.querySelector('[data-testid="memory-context-source"]')?.hasAttribute("open")).toBe(false);

  act(() => requestFindInSession(REF, { query: "sourceOnlyWord" }));
  await waitFor(() => expect(bar().getAttribute("data-find-status")).toBe("none"));
  expect(count()).toBe(0);
});

// A refresh keeps itself folded at every verbosity, including an expand-all
// baseline. Find must open it by the reader's own choice, never read the
// baseline as "already open" and leave the counted words in a closed fold.
test("a memory refresh opens even when an expand-all baseline covers its scope", async () => {
  beginDisclosureBaseline(REF, [], true);
  const model = modelOf([
    memoryItem("mem2", "recorded only", {
      scope: "personal",
      state: "current",
      truncated: false,
      content: "- brass widget plan",
    }),
  ]);
  requestFindInSession(REF, { query: "brass widget plan" });
  render(<Harness model={model} config={TOOLS} />);

  await waitFor(() => expect(count()).toBe(1));
  await waitFor(() => expect(currentAnchor()?.querySelector("details")?.hasAttribute("open")).toBe(true));
  await waitFor(() => expect(painted("evener-find-current")).toContain("brass widget plan"));
});

// A raw that does not decode keeps the complete original Text on screen once
// the divider opens, so that is what Find searches.
test("a memory refresh that fails to decode lands on its verbatim text", async () => {
  const model = modelOf([memoryItem("mem3", "unreadable record: brass widget payload")]);
  requestFindInSession(REF, { query: "brass widget payload" });
  render(<Harness model={model} config={TOOLS} />);

  await waitFor(() => expect(count()).toBe(1));
  await waitFor(() => expect(currentAnchor()?.getAttribute("data-view-anchor-id")).toBe("mem3"));
  await waitFor(() => expect(currentAnchor()?.querySelector("details")?.hasAttribute("open")).toBe(true));
  await waitFor(() => expect(painted("evener-find-current")).toContain("brass widget payload"));
  const fallback = currentAnchor()?.querySelector('[data-testid="memory-context-fallback"]');
  expect(fallback?.getAttribute("data-message-body")).toBe("");
});
