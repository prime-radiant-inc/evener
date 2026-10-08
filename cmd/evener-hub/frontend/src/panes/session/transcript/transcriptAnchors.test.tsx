import type { ItemModel, ProjectedEntry, ProjectedTurn, ThreadModel, TurnModel } from "@evener/appwire-client";
import { makeTranscriptDisplayConfig } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { createRef } from "react";
import { beforeEach, expect, test } from "vitest";
import { useStore } from "zustand";
import { ClientProvider } from "../../../shell/clientContext";
import { conversationPaneLifetime } from "../../../shell/paneLifetime";
import { type OpenPaneRecord, resetWorkspaceStoreForTests, workspaceStore } from "../../../shell/workspace";
import { connectionStore } from "../../../stores/connection";
import { activityThread, answerActivityRead } from "../../../stores/sessionActivityTestUtils";
import { resetThreadsStoreForTests } from "../../../stores/threads";
import { resetTranscriptDisplayStoreForTests, transcriptDisplayStore } from "../../../stores/transcriptDisplay";
import { makeTranscriptPreviewModel } from "../../../transcriptDisplay/previewFixture";
import type { VirtualListHandle } from "../../../widgets";
import { resetDisclosureStoreForTests } from "../../../widgets/disclosure/disclosureStore";
// Registers the tool descriptors (fsTools' read_file is fold: "quiet") the
// same way the real session pane does - through TurnBlock's side-effect
// import of ./tools.
import "./TurnBlock";
import { ReadOnlyThreadContent } from "../../transcript/ReadOnlyThreadContent";
import {
  type CapturedTranscriptView,
  captureTranscriptView,
  resetTranscriptViewRegistryForTests,
} from "./flow/transcriptViewRegistry";
import { readingPointOffset } from "./flow/useTranscriptScroll";
import {
  TranscriptBody,
  type TranscriptTurnRow,
  transcriptAnchorEntriesForRows,
  transcriptRunDisclosureIdsForRows,
} from "./TranscriptBody";
import { mountReaderScene, readerTouch, readerWireTurns } from "./transcriptReaderTestUtils";
import { holdReaderFrames, installTranscriptGeometry } from "./transcriptReadingGeometryTestUtils";
import { retainedTranscriptReadView } from "./transcriptReadView";
import { resetTranscriptPagingForTests } from "./useTranscript";

beforeEach(resetDisclosureStoreForTests);

test.each(["input", "close", "reset", "replacement"] as const)(
  "real same-ref read-only readers isolate %s with pending measurements",
  async (action) => {
    resetWorkspaceStoreForTests();
    resetThreadsStoreForTests();
    resetTranscriptPagingForTests();
    const fake = new FakeClient("ready");
    fake.on("thread/read", ({ ref, includeTurns, requestGeneration }) => {
      if (!ref) throw new Error("Actual read-only hydration requires ref");
      const read = activityThread(ref);
      return {
        ...read,
        requestGeneration,
        thread: { ...read.thread, turns: includeTurns === false ? [] : readerWireTurns(ref) },
      };
    });
    fake.on("thread/unsubscribe", () => ({}));
    fake.on("evener/thread/activity/read", answerActivityRead);
    connectionStore.getState().connect(fake);
    const a: OpenPaneRecord = { id: "reader-a", type: "transcript", params: { ref: "shared" }, slot: "main" };
    const b: OpenPaneRecord = { id: "reader-b", type: "transcript", params: { ref: "shared" }, slot: "secondary" };
    workspaceStore.setState({ panes: [a, b] });
    const av = retainedTranscriptReadView(conversationPaneLifetime(a), "shared", "transcript");
    const bv = retainedTranscriptReadView(conversationPaneLifetime(b), "shared", "transcript");
    const ag = { width: 152, viewportHeight: 400, rowHeights: [1600, 1000] };
    const bg = { width: 152, viewportHeight: 400, rowHeights: [1600, 1000] };
    const external = installTranscriptGeometry((element) => (element.closest('[data-reader="reader-a"]') ? ag : bg));
    const frames = holdReaderFrames();
    function Readers() {
      const panes = useStore(workspaceStore, (state) => state.panes);
      return panes
        .filter((pane) => pane.type === "transcript")
        .map((pane) => {
          const view = retainedTranscriptReadView(conversationPaneLifetime(pane), "shared", "transcript");
          return (
            <div key={view.id} data-reader={pane.id}>
              <ReadOnlyThreadContent ref="shared" view={view} />
            </div>
          );
        });
    }
    const mounted = render(
      <ClientProvider client={fake}>
        <Readers />
      </ClientProvider>,
    );
    const portFor = (id: string) => {
      const port = mounted.container.querySelector<HTMLElement>(
        `[data-reader="${id}"] [data-testid="transcript-virtual-list"] > div`,
      );
      if (!port) throw new Error("Real read-only content has no port");
      return port;
    };
    try {
      await screen.findAllByText("shared current reading content");
      await act(async () => external.notify());
      await act(async () => frames.release());
      await act(async () => frames.release());
      const ap = portFor(a.id);
      const bp = portFor(b.id);
      await act(async () => {
        fireEvent.wheel(ap, { deltaY: -100 });
        ap.scrollTop = 1000;
        fireEvent.scroll(ap);
        fireEvent.wheel(bp, { deltaY: -100 });
        bp.scrollTop = 600;
        fireEvent.scroll(bp);
      });
      await act(async () => {
        ap.scrollTop = 900;
        fireEvent.scroll(ap);
        bp.scrollTop = 500;
        fireEvent.scroll(bp);
      });
      expect([ap.scrollTop, bp.scrollTop]).toEqual([900, 500]);
      ag.width = 352;
      ag.rowHeights[0] = 700;
      await act(async () => external.notify((target) => target === ap));
      if (action === "input") {
        const neighborRevision = bv.positioningRevision;
        await act(async () => {
          fireEvent.wheel(ap, { deltaY: -800 });
          ap.scrollTop = 100;
          fireEvent.scroll(ap);
        });
        await act(async () => external.notify((target) => !!target.closest('[data-reader="reader-a"]')));
        expect(bp.scrollTop).toBe(500);
        expect(bv.positioningRevision).toBe(neighborRevision);
        expect(ap.scrollTop).toBe(100);
      } else {
        av.setReadable(false);
        expect(av.getCapture()).toMatchObject({ anchorOffset: -900 });
        const replacement: OpenPaneRecord = { ...a, params: { ref: "shared" } };
        await act(async () => {
          if (action === "close") workspaceStore.getState().closePane(a.id);
          if (action === "reset") resetWorkspaceStoreForTests();
          if (action === "replacement") workspaceStore.setState({ panes: [replacement, b] });
        });
        expect(av.alive).toBe(false);
        expect(external.observedTargets().filter((target) => target === ap || ap.contains(target))).toEqual([]);
        expect(av.getCapture()).toBeUndefined();
        if (action !== "reset") {
          expect(portFor(b.id)).toBe(bp);
          expect(bv.alive).toBe(true);
          expect(bp.scrollTop).toBe(500);
        }
        if (action === "replacement") {
          const fresh = retainedTranscriptReadView(conversationPaneLifetime(replacement), "shared", "transcript");
          expect(fresh).not.toBe(av);
          const freshPort = portFor(a.id);
          expect(freshPort).not.toBe(ap);
          await act(async () => external.notify());
          await act(async () => {
            fireEvent.wheel(freshPort, { deltaY: -1000 });
            freshPort.scrollTop = 100;
            fireEvent.scroll(freshPort);
          });
          await act(async () => frames.release());
          await act(async () => external.notify());
          expect(freshPort.scrollTop).toBe(100);
          expect(document.activeElement?.getAttribute("data-view-anchor-id")).not.toBe("shared-current-entry");
        }
      }
    } finally {
      mounted.unmount();
      resetWorkspaceStoreForTests();
      connectionStore.setState({ state: "idle", client: null });
      resetThreadsStoreForTests();
      resetTranscriptPagingForTests();
      external.restore();
      frames.restore();
      resetTranscriptViewRegistryForTests();
    }
  },
);

// Without retained-view supersession the committed reader replays -900 as -225.
test.each(["wheel", "touch", "scrollbar", "selection autoscroll", "native key"] as const)(
  "newer %s movement wins over held width reflow and remount",
  async (input) => {
    const scene = mountReaderScene(`newer-${input}`);
    try {
      await scene.start();
      expect(scene.port().scrollTop).toBe(900);
      await scene.holdReflow();
      const port = scene.port();
      await act(async () => {
        if (input === "wheel") fireEvent.wheel(port, { deltaY: -800 });
        if (input === "touch") {
          readerTouch(port, "touchstart", 100);
          readerTouch(port, "touchmove", 900);
        }
        if (input === "scrollbar" || input === "selection autoscroll") {
          fireEvent.pointerDown(port, { pointerType: "mouse", button: 0, buttons: 1, isPrimary: true });
          fireEvent.pointerMove(port, { pointerType: "mouse", button: -1, buttons: 1, isPrimary: true });
        }
        if (input === "native key") fireEvent.keyDown(port, { key: "PageUp" });
        port.scrollTop = 100;
        fireEvent.scroll(port);
      });
      await act(async () => scene.external.notify());
      expect(port.scrollTop).toBe(100);
      expect(scene.capture()).toMatchObject({ anchorId: "current-entry", anchorOffset: -100 });
      await act(async () => scene.remount());
      await act(async () => scene.external.notify());
      expect(scene.port().scrollTop).toBe(100);
      expect(scene.port().querySelector('[data-view-anchor-id="current-entry"]')?.textContent).toContain("current");
    } finally {
      scene.dispose();
      resetTranscriptViewRegistryForTests();
    }
  },
);

// jsdom supplies no default key scrolling, so movement is explicit and signed.
test.each([
  { name: "Space", key: " ", shiftKey: false, before: 100, newer: 250, height: 1600 },
  { name: "Shift-Space", key: " ", shiftKey: true, before: 900, newer: 100, height: 700 },
  { name: "PageDown", key: "PageDown", shiftKey: false, before: 100, newer: 250, height: 1600 },
  { name: "PageUp", key: "PageUp", shiftKey: false, before: 900, newer: 100, height: 700 },
])(
  "newer native $name paging wins over held width reflow and remount",
  async ({ name, key, shiftKey, before, newer, height }) => {
    const scene = mountReaderScene(`native-paging-${name}`);
    try {
      await scene.start(before);
      // Forward scrolling retains ordinary partial-row measurement compensation.
      await scene.holdReflow(height);
      const port = scene.port();
      const revision = scene.view.positioningRevision;
      await act(async () => {
        fireEvent.keyDown(port, { key, code: key === " " ? "Space" : key, shiftKey });
        expect(scene.view.positioningRevision).toBeGreaterThan(revision);
        port.scrollTop = newer;
        fireEvent.scroll(port);
      });
      await act(async () => scene.external.notify());
      expect(port.scrollTop).toBe(newer);
      expect(scene.capture()).toMatchObject({ anchorId: "current-entry", anchorOffset: -newer });
      await act(async () => scene.remount());
      await act(async () => scene.external.notify());
      expect(scene.port().scrollTop).toBe(newer);
      expect(scene.port().querySelector('[data-view-anchor-id="current-entry"]')?.textContent).toContain("current");
    } finally {
      scene.dispose();
      resetTranscriptViewRegistryForTests();
    }
  },
);

// A restored reader keeps anchorToEnd off while its placement is retained. A
// row above it growing writes past the uncommitted sizer's end; the browser
// clamps that write, and VirtualList completes it after the sizer commits.
test("a retained reader completes a clamped write when a row above it grows", async () => {
  const scene = mountReaderScene("retained-clamped-growth", [300, 300, 300, 300, 300], {
    estimate: 300,
    viewportHeight: 500,
  });
  const row3 = () => scene.port().querySelector('[data-index="3"]')?.getBoundingClientRect().top;
  try {
    await scene.start(900);
    await act(async () => scene.remount());
    await act(async () => scene.frames.release());
    await act(async () => scene.external.notify());
    expect(scene.port().scrollTop).toBe(900);
    expect(row3()).toBe(0);
    expect(scene.layout().virtualizer.options.anchorTo).not.toBe("end");
    scene.geometry.rowHeights[0] = 1300;
    await act(async () => scene.external.notify());
    await act(async () => scene.frames.release());
    expect(scene.port().scrollTop).toBe(1900);
    expect(scene.port().scrollHeight).toBe(2500);
    expect(row3()).toBe(0);
  } finally {
    scene.dispose();
    resetTranscriptViewRegistryForTests();
  }
});

test.each([
  ...["button", "summary", "editor", "editable", "prevented", "composing", "ctrl", "alt", "meta", "nested"].map(
    (input) => ({
      name: `Space on ${input}`,
      input,
      key: " ",
      keyCode: 32,
    }),
  ),
  { name: "Space IME commit", input: "IME commit", key: " ", keyCode: 229 },
  { name: "PageUp IME commit", input: "IME commit", key: "PageUp", keyCode: 229 },
])("$name without viewport movement preserves pending reflow", async ({ input, key, keyCode }) => {
  const scene = mountReaderScene(`non-scrolling-space-${input}`);
  try {
    await scene.start();
    const port = scene.port();
    const holder = document.createElement("div");
    holder.innerHTML =
      input === "button"
        ? '<button type="button"><span>Activate</span></button>'
        : input === "summary"
          ? "<details><summary><span>Expand</span></summary></details>"
          : input === "editor"
            ? "<textarea>keep text</textarea>"
            : input === "editable"
              ? '<div contenteditable="true"><span>keep text</span></div>'
              : "<div>Reader content</div>";
    port.appendChild(holder);
    const target = holder.querySelector<HTMLElement>("span, textarea, div");
    if (!target) throw new Error("Space control target is missing");
    if (input === "nested") {
      target.style.overflowY = "auto";
      Object.defineProperties(target, {
        scrollHeight: { value: 1000 },
        clientHeight: { value: 100 },
        scrollTop: { writable: true, value: 500 },
      });
    }
    if (input === "prevented") target.addEventListener("keydown", (event) => event.preventDefault());
    const editor = screen.getByRole("textbox", { name: "Neighbor editor" }) as HTMLTextAreaElement;
    editor.focus();
    editor.setSelectionRange(5, 9);
    await scene.holdReflow();
    const revision = scene.view.positioningRevision;
    await act(async () => {
      fireEvent.keyDown(target, {
        key,
        code: key === " " ? "Space" : key,
        keyCode,
        isComposing: input === "composing",
        ctrlKey: input === "ctrl",
        altKey: input === "alt",
        metaKey: input === "meta",
      });
      scene.external.notify();
    });
    expect(scene.view.positioningRevision).toBe(revision);
    expect(port.scrollTop).toBe(225);
    expect(document.activeElement).toBe(editor);
    expect([editor.selectionStart, editor.selectionEnd]).toEqual([5, 9]);
    holder.remove();
  } finally {
    scene.dispose();
    resetTranscriptViewRegistryForTests();
  }
});

test.each(["selection", "Tab", "editor", "modifier", "horizontal wheel", "ctrl wheel", "nested wheel"] as const)(
  "%s without viewport movement preserves pending reflow and outside focus",
  async (input) => {
    const scene = mountReaderScene(`non-reader-${input}`);
    try {
      await scene.start();
      const port = scene.port();
      const editor = screen.getByRole("textbox", { name: "Neighbor editor" }) as HTMLTextAreaElement;
      editor.focus();
      editor.setSelectionRange(5, 9);
      await scene.holdReflow();
      await act(async () => {
        if (input === "selection") {
          fireEvent.pointerDown(port, { pointerType: "mouse", button: 0, buttons: 1, isPrimary: true });
          fireEvent.pointerMove(port, { pointerType: "mouse", button: -1, buttons: 1, isPrimary: true });
        }
        if (input === "Tab") fireEvent.keyDown(port, { key: "Tab" });
        if (input === "editor") fireEvent.keyDown(editor, { key: "ArrowUp" });
        if (input === "modifier") fireEvent.keyDown(port, { key: "ArrowUp", ctrlKey: true });
        if (input === "horizontal wheel") fireEvent.wheel(port, { deltaX: 800, deltaY: 0 });
        if (input === "ctrl wheel") fireEvent.wheel(port, { deltaY: -800, ctrlKey: true });
        if (input === "nested wheel") {
          const inner = document.createElement("div");
          inner.style.overflowY = "auto";
          Object.defineProperties(inner, {
            scrollHeight: { value: 1000 },
            clientHeight: { value: 100 },
            scrollTop: { writable: true, value: 500 },
          });
          port.appendChild(inner);
          fireEvent.wheel(inner, { deltaY: -50 });
          inner.remove();
        }
        scene.external.notify();
      });
      expect(port.scrollTop).toBe(225);
      expect(document.activeElement).toBe(editor);
      expect([editor.selectionStart, editor.selectionEnd]).toEqual([5, 9]);
    } finally {
      scene.dispose();
      resetTranscriptViewRegistryForTests();
    }
  },
);

// description: a call with a stated intent projects as an ordinary item
// entry at the tool-call levels; an intent-less call projects as a
// "critical" entry (projector.ts's decisionFor), which never folds.
function toolItem(id: string, toolName: string, status = "completed"): ItemModel {
  return {
    id,
    turnId: "t1",
    type: "commandExecution",
    text: "",
    toolName,
    status,
    description: `Look at ${id}`,
    output: "ok",
  } as ItemModel;
}

function turnRow(items: ItemModel[], status: string): TranscriptTurnRow {
  const source: TurnModel = { id: "t1", status, items } as TurnModel;
  const entries: ProjectedEntry[] = items.map((item, sourceIndex) => ({
    kind: "item",
    id: item.id,
    turnId: "t1",
    sourceIndex,
    item,
    isMessage: false,
  }));
  const turn: ProjectedTurn = { id: "t1", source, entries, visibleItems: items };
  return { kind: "turn", id: "t1", turn, sourceTurnIndex: 0, showTurnSeparator: true };
}

// roborev on PR #947: TurnBlock renders a folded run under ONE anchor
// (run:<first entry>), so the anchor registry must advertise that anchor -
// not the three entry ids no element carries while the run is closed - or a
// restore after a remount looks for an id that is not in the DOM.
test("a settled run of quiet tool calls registers one anchor under the run id", () => {
  const items = [toolItem("a", "read_file"), toolItem("b", "read_file"), toolItem("c", "glob")];
  const anchors = transcriptAnchorEntriesForRows([turnRow(items, "completed")]);
  // members: the ids the run stands in for, so a focus or scroll position
  // captured on the second or third call still resolves to the run.
  expect(anchors).toEqual([{ id: "run:a", sourceIndex: 0, index: 0, isMessage: false, members: ["a", "b", "c"] }]);
});

test("a live turn registers every entry, matching the rows TurnBlock renders while the agent works", () => {
  const items = [toolItem("a", "read_file"), toolItem("b", "read_file"), toolItem("c", "glob")];
  const anchors = transcriptAnchorEntriesForRows([turnRow(items, "inProgress")]);
  expect(anchors.map((anchor) => anchor.id)).toEqual(["a", "b", "c"]);
});

test("a tool with no fold policy keeps its own anchor and breaks the run", () => {
  const items = [
    toolItem("a", "read_file"),
    toolItem("b", "mcp_deploy"),
    toolItem("c", "read_file"),
    toolItem("d", "glob"),
  ];
  const anchors = transcriptAnchorEntriesForRows([turnRow(items, "completed")]);
  expect(anchors.map((anchor) => anchor.id)).toEqual(["a", "b", "c", "d"]);
});

// roborev on PR #947 (round five): the Full-view baseline clears stale closed
// choices only for ids in the eligible inventory, and the projector's inventory
// knows source item ids, not the run:<first> ids ToolRunGroup mints.
test("a settled run's disclosure id joins the Full-baseline inventory; a live turn adds none", () => {
  const items = [toolItem("a", "read_file"), toolItem("b", "read_file"), toolItem("c", "glob")];
  expect(transcriptRunDisclosureIdsForRows([turnRow(items, "completed")])).toEqual(["run:a"]);
  expect(transcriptRunDisclosureIdsForRows([turnRow(items, "inProgress")])).toEqual([]);
});

// The behaviour that inventory buys: close a folded run in Full, leave Full,
// come back - Full's "everything open" baseline reopens it.
test("re-entering Full view reopens a run the reader closed there", () => {
  const items = [
    { id: "u", turnId: "t1", type: "userMessage", text: "look around", status: "completed" },
    toolItem("a", "read_file"),
    toolItem("b", "read_file"),
    toolItem("c", "glob"),
  ] as ItemModel[];
  const model = {
    ref: "preview:runs",
    threadId: "thread_runs",
    name: "Runs",
    status: { type: "idle" },
    modelProvider: "preview",
    model: "preview-model",
    askPending: false,
    pendingEscalations: [],
    turns: [{ id: "t1", status: "completed", items }],
  } as unknown as ThreadModel;
  const preset = (level: "tools" | "full") => makeTranscriptDisplayConfig({ kind: "preset", level });
  const view = (level: "tools" | "full") => (
    <TranscriptBody model={model} config={preset(level)} surface="preview" disclosureScope="preview:runs" />
  );
  const { rerender } = render(view("full"));
  const run = () => screen.getByTestId("tool-run") as HTMLDetailsElement;
  expect(run().open).toBe(true);
  const summary = run().querySelector("summary");
  if (summary === null) throw new Error("the run rendered no summary");
  fireEvent.click(summary);
  expect(run().open).toBe(false);
  rerender(view("tools"));
  rerender(view("full"));
  expect(run().open).toBe(true);
});

// A capture taken at a 400px-high, 152px-wide viewport, offset into an entry.
function readingCapture(anchorOffset: number, entryHeight = 1600): CapturedTranscriptView {
  return {
    anchorOffset,
    normalizedOffset: 0,
    followingBottom: false,
    readingPoint: { entryHeight, viewportHeight: 400, viewportWidth: 152 },
  };
}

test.each([
  { oldHeight: 1600, oldOffset: -900, nextHeight: 1000, want: -450 },
  { oldHeight: 1600, oldOffset: -900, nextHeight: 700, want: -225 },
  { oldHeight: 1600, oldOffset: -900, nextHeight: 300, want: 0 },
  // A short entry partly above the top keeps its 180px tail too.
  { oldHeight: 200, oldOffset: -20, nextHeight: 1000, want: -820 },
  { oldHeight: 1600, oldOffset: 30, nextHeight: 700, want: 30 },
  // An entry whose tail is showing keeps that tail where it was: a 1px tail
  // stays 1px rather than growing to fill the pane.
  { oldHeight: 1600, oldOffset: -1599, nextHeight: 2400, want: -2399 },
  { oldHeight: 1600, oldOffset: -1500, nextHeight: 1000, want: -900 },
  { oldHeight: 1600, oldOffset: -1500, nextHeight: 50, want: 0 },
])("width-only bounded reading point $oldOffset at $nextHeight", ({ oldHeight, oldOffset, nextHeight, want }) => {
  const captured = readingCapture(oldOffset, oldHeight);
  expect(readingPointOffset(captured, nextHeight, 400, 352)).toBe(want);
});

// A kept tail stays put when the viewport shrinks below it: the text at the
// viewport top doesn't move, and only content past the new fold is cut. Clamping
// the entry's bottom into the smaller viewport would move the reading line.
test("a kept tail stays in place when the viewport shrinks below it", () => {
  const captured = readingCapture(-1250);
  expect(readingPointOffset(captured, 1600, 300, 352)).toBe(-1250);
});

// A viewport-height-only change (composer-height settlement, say) doesn't
// reflow the entry, so the reading line stays where it was outright.
test.each([
  { label: "inside the entry", oldOffset: -900, nextViewport: 436 },
  { label: "inside the entry, shrinking", oldOffset: -900, nextViewport: 300 },
  { label: "at its start", oldOffset: 0, nextViewport: 436 },
  { label: "reading its tail", oldOffset: -1500, nextViewport: 436 },
])("a height-only change keeps the reading line, $label", ({ oldOffset, nextViewport }) => {
  const captured = readingCapture(oldOffset);
  expect(readingPointOffset(captured, 1600, nextViewport, 152)).toBe(oldOffset);
});

// An entry that shrank at the same width isn't a viewport change: the tail rule
// still bounds it, so it never lands wholly above the viewport.
test("an entry that shrinks at the same width keeps a real part visible", () => {
  const captured = readingCapture(-1500);
  expect(readingPointOffset(captured, 50, 436, 152)).toBe(0);
});

test("width-only policy preserves an ordinary display offset without a measured point", () => {
  expect(readingPointOffset({ anchorOffset: -900, normalizedOffset: 0, followingBottom: false }, 700, 400, 352)).toBe(
    -900,
  );
});

test.each([
  { label: "partial shrink", nextHeight: 700, nextViewport: 400, tailHeight: 1000, want: 225 },
  { label: "same-range unchanged rows", nextHeight: 1600, nextViewport: 500, tailHeight: 1000, want: 825 },
  { label: "equal-estimate overscan", nextHeight: 700, nextViewport: 400, tailHeight: 96, want: 225 },
  { label: "native scrollbar", nextHeight: 700, nextViewport: 400, tailHeight: 1000, scrollbarWidth: 15, want: 225 },
])(
  "width-only reflow preserves the current entry, $label",
  async ({ nextHeight, nextViewport, tailHeight, scrollbarWidth = 0, want }) => {
    const geometry = { width: 152, scrollbarWidth, viewportHeight: 400, rowHeights: [1600, tailHeight] };
    const external = installTranscriptGeometry(() => geometry);
    const listRef = createRef<VirtualListHandle>();
    const row = (id: string): TurnModel => ({
      id,
      status: "completed",
      items: [{ id: `${id}-entry`, turnId: id, type: "userMessage", text: id, status: "completed" }],
    });
    const model = { ...makeTranscriptPreviewModel(), turns: [row("current"), row("tail")] };
    let mounted: ReturnType<typeof render> | undefined;
    try {
      mounted = render(
        <TranscriptBody
          model={model}
          config={makeTranscriptDisplayConfig({ kind: "preset", level: "tools" })}
          surface="readOnly"
          disclosureScope="width-only"
          viewId="width-only"
          listRef={listRef}
        />,
      );
      const port = listRef.current?.getScrollElement();
      if (!port) throw new Error("Real TranscriptBody has no scroll port");
      await act(async () => {
        external.notify();
        port.scrollTop = 1000;
        fireEvent.scroll(port);
      });
      await act(async () => {
        port.scrollTop = 900;
        fireEvent.scroll(port);
      });
      const entry = port.querySelector<HTMLElement>('[data-view-anchor-id="current-entry"]');
      if (!entry) throw new Error("Real TranscriptBody has no current entry");
      expect(entry.getBoundingClientRect().top - port.getBoundingClientRect().top).toBe(-900);
      geometry.width = 352;
      geometry.viewportHeight = nextViewport;
      geometry.rowHeights = [nextHeight, tailHeight];
      await act(async () => external.notify());
      await waitFor(() => expect(port.scrollTop).toBe(want));
      await waitFor(() =>
        expect(captureTranscriptView("width-only")).toMatchObject({ anchorId: "current-entry", anchorOffset: -want }),
      );
      expect(port.querySelector('[data-view-anchor-id="current-entry"] [data-testid="user-bubble"]')?.textContent).toBe(
        "current",
      );
    } finally {
      mounted?.unmount();
      external.restore();
      resetTranscriptViewRegistryForTests();
    }
  },
);

function readingRow(id: string): TurnModel {
  return {
    id,
    status: "completed",
    items: [{ id: `${id}-entry`, turnId: id, type: "userMessage", text: id, status: "completed" }],
  };
}

test.each([
  // The later change is height-only, so the reading line stays where it was.
  { start: 900, intermediate: 225, want: 225 },
  // Scrolled into the entry's last 350px: that tail stays where it was.
  { start: 1250, intermediate: 350, want: 350 },
])(
  "width reflow retains the reading point through a later viewport resize at $start",
  async ({ start, intermediate, want }) => {
    const geometry = { width: 152, viewportHeight: 400, rowHeights: [1600, 1000] };
    const external = installTranscriptGeometry(() => geometry);
    const listRef = createRef<VirtualListHandle>();
    let mounted: ReturnType<typeof render> | undefined;
    try {
      mounted = render(
        <TranscriptBody
          model={{ ...makeTranscriptPreviewModel(), turns: [readingRow("current"), readingRow("tail")] }}
          config={makeTranscriptDisplayConfig({ kind: "preset", level: "tools" })}
          surface="readOnly"
          disclosureScope="viewport-reading"
          viewId="viewport-reading"
          listRef={listRef}
        />,
      );
      const port = listRef.current?.getScrollElement();
      if (!port) throw new Error("Real viewport reader has no scroll port");
      await act(async () => {
        external.notify();
        port.scrollTop = start + 100;
        fireEvent.scroll(port);
      });
      await act(async () => {
        port.scrollTop = start;
        fireEvent.scroll(port);
      });
      geometry.width = 352;
      geometry.rowHeights[0] = 700;
      await act(async () => external.notify());
      expect(port.scrollTop).toBe(intermediate);
      // The reflow completes on its landing's scroll event, a frame before the resize.
      await waitFor(() =>
        expect(captureTranscriptView("viewport-reading")).toMatchObject({ anchorOffset: -intermediate }),
      );
      geometry.viewportHeight = 436;
      await act(async () => external.notify((target) => target === port));
      expect(port.scrollTop).toBe(want);
      expect(captureTranscriptView("viewport-reading")).toMatchObject({
        anchorId: "current-entry",
        anchorOffset: -want,
        followingBottom: false,
      });
      expect(port.querySelector('[data-view-anchor-id="current-entry"] [data-testid="user-bubble"]')?.textContent).toBe(
        "current",
      );
    } finally {
      mounted?.unmount();
      external.restore();
      resetTranscriptViewRegistryForTests();
    }
  },
);

// The 275 -> 240 shrink keeps the reading line where it was. Once observed, the
// 240px viewport is the committed geometry the later width reflow measures
// progress against: the reader is 12578px into a usable depth of 12853.14 - 240,
// and the same fraction of the new depth (7036.17 - 480) is 6537.9. Unobserved,
// the reflow still measures against 275 and lands at 6556.1.
test.each([
  { settled: false, reflowed: 6556.098576275061 },
  { settled: true, reflowed: 6537.9061643301075 },
])("viewport shrink retains committed reading progress, observed=$settled", async ({ settled, reflowed }) => {
  const selector = '[data-view-anchor-id="current-entry"]';
  const geometry = {
    width: 152,
    scrollbarWidth: 15,
    viewportHeight: 275,
    rowHeights: [12974.03125, 1000],
    entryBoxes: { [selector]: { top: 0, height: 12853.140625 } },
  };
  const external = installTranscriptGeometry(() => geometry);
  const listRef = createRef<VirtualListHandle>();
  let mounted: ReturnType<typeof render> | undefined;
  try {
    mounted = render(
      <TranscriptBody
        model={{ ...makeTranscriptPreviewModel(), turns: [readingRow("current"), readingRow("tail")] }}
        config={makeTranscriptDisplayConfig({ kind: "preset", level: "tools" })}
        surface="readOnly"
        disclosureScope="return-viewport-snapshot"
        viewId="return-viewport-snapshot"
        listRef={listRef}
      />,
    );
    const port = listRef.current?.getScrollElement();
    if (!port) throw new Error("Real Return reader has no scroll port");
    await act(async () => {
      external.notify();
      port.scrollTop = 12678;
      fireEvent.scroll(port);
    });
    await act(async () => {
      port.scrollTop = 12578;
      fireEvent.scroll(port);
    });
    expect(captureTranscriptView("return-viewport-snapshot")).toMatchObject({
      anchorOffset: -12578,
      readingPoint: { viewportHeight: 275, entryHeight: 12853.140625 },
    });
    geometry.viewportHeight = 240;
    if (settled) await act(async () => external.notify((target) => target === port));
    expect(listRef.current?.isLayoutCurrent()).toBe(settled);
    expect(port.clientHeight).toBe(240);
    expect(captureTranscriptView("return-viewport-snapshot")?.readingPoint?.viewportHeight).toBe(settled ? 240 : 275);
    expect(port.scrollTop).toBe(12578);
    geometry.width = 352;
    geometry.viewportHeight = 480;
    geometry.rowHeights[0] = 7065.484375;
    geometry.entryBoxes[selector].height = 7036.171875;
    await act(async () => external.notify());
    expect(Math.abs(port.scrollTop - reflowed)).toBeLessThanOrEqual(2);
    // The reflow completes on its landing's scroll event, a frame before the resize.
    await waitFor(() =>
      expect(captureTranscriptView("return-viewport-snapshot")?.readingPoint?.viewportHeight).toBe(480),
    );
    geometry.viewportHeight = 516;
    await act(async () => external.notify((target) => target === port));
    expect(Math.abs(port.scrollTop - reflowed)).toBeLessThanOrEqual(2);
    expect(port.querySelector(`${selector} [data-testid="user-bubble"]`)?.textContent).toBe("current");
  } finally {
    mounted?.unmount();
    external.restore();
    resetTranscriptViewRegistryForTests();
  }
});

test("width-only reflow preserves useful content beside a Chat-filtered daemon steer", async () => {
  const geometry = { width: 152, viewportHeight: 400, rowHeights: [1600, 0, 1000] };
  const external = installTranscriptGeometry(() => geometry);
  const listRef = createRef<VirtualListHandle>();
  let mounted: ReturnType<typeof render> | undefined;
  try {
    mounted = render(
      <TranscriptBody
        model={{
          ...makeTranscriptPreviewModel(),
          turns: [
            readingRow("current"),
            {
              id: "filtered",
              status: "completed",
              items: [
                {
                  id: "filtered-steer",
                  turnId: "filtered",
                  type: "steering",
                  source: "daemon",
                  text: "Internal steering",
                  status: "completed",
                },
              ],
            },
            readingRow("tail"),
          ],
        }}
        config={makeTranscriptDisplayConfig({ kind: "preset", level: "chat" })}
        surface="readOnly"
        disclosureScope="filtered-reading"
        viewId="filtered-reading"
        listRef={listRef}
      />,
    );
    const port = listRef.current?.getScrollElement();
    if (!port) throw new Error("Real Chat reader has no scroll port");
    await startReading(port, external.notify);
    const filtered = port.querySelector('[data-row-id="filtered"]');
    expect(filtered?.textContent).toBe("");
    expect(filtered?.getBoundingClientRect().height).toBe(0);
    expect(port.querySelector('[data-view-anchor-id="filtered-steer"]')).toBeNull();
    expect(port.scrollTop).toBe(900);
    geometry.width = 352;
    geometry.rowHeights[0] = 700;
    await act(async () => external.notify());
    await waitFor(() => expect(port.scrollTop).toBe(225));
    await waitFor(() =>
      expect(captureTranscriptView("filtered-reading")).toMatchObject({
        anchorId: "current-entry",
        anchorOffset: -225,
      }),
    );
    expect(port.querySelector('[data-view-anchor-id="current-entry"] [data-testid="user-bubble"]')?.textContent).toBe(
      "current",
    );
  } finally {
    mounted?.unmount();
    external.restore();
    resetTranscriptViewRegistryForTests();
  }
});

test("width-only reflow preserves the first visible row beside a fractional predecessor", async () => {
  const geometry = { width: 777, viewportHeight: 400, rowHeights: [4156.484375, 29.3125, 4156.484375] };
  const external = installTranscriptGeometry(() => geometry);
  const listRef = createRef<VirtualListHandle>();
  let mounted: ReturnType<typeof render> | undefined;
  try {
    mounted = render(
      <TranscriptBody
        model={{
          ...makeTranscriptPreviewModel(),
          turns: [readingRow("older"), readingRow("current"), readingRow("tail")],
        }}
        config={makeTranscriptDisplayConfig({ kind: "preset", level: "tools" })}
        surface="readOnly"
        disclosureScope="fractional-reading"
        viewId="fractional-reading"
        listRef={listRef}
      />,
    );
    const port = listRef.current?.getScrollElement();
    const entry = port?.querySelector<HTMLElement>('[data-view-anchor-id="current-entry"]');
    if (!port || !entry) throw new Error("Real fractional reader has no current entry");
    const firstVisibleRow = () => {
      const bounds = port.getBoundingClientRect();
      return [...port.querySelectorAll<HTMLElement>("[data-row-id]")].find((node) => {
        const box = node.getBoundingClientRect();
        return box.bottom > bounds.top && box.top < bounds.bottom;
      })?.dataset.rowId;
    };
    await act(async () => {
      external.notify();
      port.scrollTop += entry.getBoundingClientRect().top + 28;
      fireEvent.scroll(port);
    });
    expect(firstVisibleRow()).toBe("current");
    const before = captureTranscriptView("fractional-reading");
    expect(before).toMatchObject({ anchorId: "current-entry" });
    geometry.width = 364;
    geometry.rowHeights = [7036.171875, 29.3125, 7036.171875];
    await act(async () => external.notify());
    await waitFor(() => expect(firstVisibleRow()).toBe("current"));
    // The separator's 1.3px tail stays where it was: its height is unchanged,
    // so its offset is too.
    expect(captureTranscriptView("fractional-reading")).toMatchObject({
      anchorId: "current-entry",
      anchorOffset: before?.anchorOffset,
    });
    expect(entry.querySelector('[data-testid="user-bubble"]')?.textContent).toBe("current");
  } finally {
    mounted?.unmount();
    external.restore();
    resetTranscriptViewRegistryForTests();
  }
});

async function startReading(port: HTMLElement, notify: () => void): Promise<void> {
  await act(async () => {
    notify();
    port.scrollTop = 1000;
    fireEvent.scroll(port);
  });
  await act(async () => {
    port.scrollTop = 900;
    fireEvent.scroll(port);
  });
  expect(port.querySelector('[data-view-anchor-id="current-entry"]')?.getBoundingClientRect().top).toBe(-900);
}

test("width-only reflow keeps end following without restoring stale entry focus", async () => {
  const geometry = { width: 152, viewportHeight: 400, rowHeights: [1600, 1000] };
  const external = installTranscriptGeometry(() => geometry);
  const listRef = createRef<VirtualListHandle>();
  let mounted: ReturnType<typeof render> | undefined;
  try {
    mounted = render(
      <>
        <button type="button">Outside follower</button>
        <TranscriptBody
          model={{ ...makeTranscriptPreviewModel(), turns: [readingRow("current"), readingRow("tail")] }}
          config={makeTranscriptDisplayConfig({ kind: "preset", level: "tools" })}
          surface="readOnly"
          disclosureScope="following-reflow"
          viewId="following-reflow"
          listRef={listRef}
        />
      </>,
    );
    const port = listRef.current?.getScrollElement();
    if (!port) throw new Error("Real end follower has no port");
    await act(async () => {
      external.notify();
      port.scrollTop = 2200;
      fireEvent.scroll(port);
    });
    const tail = port.querySelector<HTMLElement>('[data-view-anchor-id="tail-entry"]');
    if (!tail) throw new Error("Real end follower has no tail entry");
    tail.tabIndex = -1;
    tail.focus();
    await act(async () => fireEvent.scroll(port));
    expect(tail.getBoundingClientRect().top).toBe(-600);
    const outside = screen.getByRole("button", { name: "Outside follower" });
    outside.focus();
    geometry.width = 352;
    geometry.rowHeights = [700, 1000];
    await act(async () => external.notify());
    await waitFor(() => expect(port.scrollTop).toBe(1300));
    expect(document.activeElement).toBe(outside);
    expect(captureTranscriptView("following-reflow")).toMatchObject({
      anchorId: "tail-entry",
      anchorOffset: -600,
      followingBottom: true,
      readingPoint: { viewportWidth: 352 },
    });
  } finally {
    mounted?.unmount();
    external.restore();
    resetTranscriptViewRegistryForTests();
  }
});

test.each(["zero width", "zero viewport", "zero row", "repeated widths"])(
  "width-only reflow waits through $0 before measured recovery",
  async (scene) => {
    const geometry = { width: 152, viewportHeight: 400, rowHeights: [1600, 1000] };
    const external = installTranscriptGeometry(() => geometry);
    const listRef = createRef<VirtualListHandle>();
    let mounted: ReturnType<typeof render> | undefined;
    try {
      mounted = render(
        <TranscriptBody
          model={{ ...makeTranscriptPreviewModel(), turns: [readingRow("current"), readingRow("tail")] }}
          config={makeTranscriptDisplayConfig({ kind: "preset", level: "tools" })}
          surface="readOnly"
          disclosureScope="recover"
          viewId="recover"
          listRef={listRef}
        />,
      );
      const port = listRef.current?.getScrollElement();
      if (!port) throw new Error("Real reader has no port");
      await startReading(port, external.notify);
      geometry.width = scene === "zero width" ? 0 : 252;
      geometry.viewportHeight = scene === "zero viewport" ? 0 : 400;
      geometry.rowHeights = [scene === "zero width" ? 1600 : scene === "zero row" ? 0 : 700, 1000];
      await act(async () => external.notify((target) => scene !== "repeated widths" || target === port));
      expect(captureTranscriptView("recover")).toMatchObject({ anchorId: "current-entry", anchorOffset: -900 });
      geometry.width = 352;
      geometry.viewportHeight = scene === "zero width" ? 500 : 400;
      geometry.rowHeights = [scene === "zero width" ? 1600 : scene === "repeated widths" ? 1000 : 700, 1000];
      await act(async () => external.notify());
      const want = scene === "zero width" ? 825 : scene === "repeated widths" ? 450 : 225;
      await waitFor(() => expect(port.scrollTop).toBe(want));
      await waitFor(() =>
        expect(captureTranscriptView("recover")).toMatchObject({ anchorId: "current-entry", anchorOffset: -want }),
      );
      expect(port.querySelector('[data-view-anchor-id="current-entry"] [data-testid="user-bubble"]')?.textContent).toBe(
        "current",
      );
    } finally {
      mounted?.unmount();
      external.restore();
      resetTranscriptViewRegistryForTests();
    }
  },
);

test("width-only reflow retains the pre-arm capture when the real read view becomes unreadable", async () => {
  const geometry = { width: 152, viewportHeight: 400, rowHeights: [1600, 1000] };
  const external = installTranscriptGeometry(() => geometry);
  const lifetime = conversationPaneLifetime({
    id: "pre-arm-pane",
    type: "transcript",
    params: { ref: "preview:pre-arm" },
    slot: "secondary",
  });
  const view = retainedTranscriptReadView(lifetime, "preview:pre-arm", "transcript");
  const model = { ...makeTranscriptPreviewModel(), turns: [readingRow("current"), readingRow("tail")] };
  const config = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });
  let mounted: ReturnType<typeof render> | undefined;
  try {
    view.setReadable(true);
    const firstRef = createRef<VirtualListHandle>();
    mounted = render(
      <TranscriptBody
        model={model}
        config={config}
        surface="readOnly"
        disclosureScope={view.id}
        viewId={view.id}
        listRef={firstRef}
      />,
    );
    const port = firstRef.current?.getScrollElement();
    if (!port) throw new Error("Retained reader has no port");
    const focused = port.querySelector<HTMLElement>('[data-view-anchor-id="current-entry"]');
    if (!focused) throw new Error("Retained reader has no source anchor");
    focused.tabIndex = -1;
    focused.focus();
    await startReading(port, external.notify);
    geometry.width = 352;
    geometry.rowHeights = [700, 1000];
    view.setReadable(false);
    const transferred = view.getCapture();
    expect(transferred).toMatchObject({
      anchorId: "current-entry",
      anchorOffset: -900,
      focusedEntryId: "current-entry",
    });
    mounted.unmount();
    view.setReadable(true);
    const returnedRef = createRef<VirtualListHandle>();
    mounted = render(
      <TranscriptBody
        model={model}
        config={config}
        surface="readOnly"
        disclosureScope={view.id}
        viewId={view.id}
        listRef={returnedRef}
        initialViewCapture={transferred}
      />,
    );
    const returnedPort = returnedRef.current?.getScrollElement();
    if (!returnedPort) throw new Error("Remounted reader has no port");
    await act(async () => external.notify());
    await waitFor(() => expect(returnedPort.scrollTop).toBe(225));
    expect(view.getCapture()).toBe(transferred);
    await waitFor(() =>
      expect(captureTranscriptView(view.id)).toMatchObject({ anchorId: "current-entry", anchorOffset: -225 }),
    );
  } finally {
    mounted?.unmount();
    lifetime.dispose();
    external.restore();
    resetTranscriptViewRegistryForTests();
  }
});

test("width-only reflow transfers the original point through real display capture-before-publication", async () => {
  resetTranscriptDisplayStoreForTests();
  const tools = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });
  transcriptDisplayStore.getState().setLocal("desktop", tools);
  const geometry = { width: 152, viewportHeight: 400, rowHeights: [1600, 1000] };
  const external = installTranscriptGeometry(() => geometry);
  const listRef = createRef<VirtualListHandle>();
  const model = { ...makeTranscriptPreviewModel(), turns: [readingRow("current"), readingRow("tail")] };
  function DisplayReader() {
    const config = useStore(transcriptDisplayStore, (state) => state.local.desktop ?? tools);
    return (
      <TranscriptBody
        model={model}
        config={config}
        surface="readOnly"
        disclosureScope="display-reflow"
        viewId="display-reflow"
        listRef={listRef}
      />
    );
  }
  let mounted: ReturnType<typeof render> | undefined;
  let unsubscribe: (() => void) | undefined;
  try {
    mounted = render(<DisplayReader />);
    const port = listRef.current?.getScrollElement();
    if (!port) throw new Error("Display reader has no port");
    await startReading(port, external.notify);
    geometry.width = 352;
    geometry.rowHeights = [700, 1000];
    let publishedCapture: ReturnType<typeof captureTranscriptView>;
    unsubscribe = transcriptDisplayStore.subscribe(() => {
      publishedCapture = captureTranscriptView("display-reflow");
    });
    await act(async () =>
      transcriptDisplayStore
        .getState()
        .setLocal("desktop", makeTranscriptDisplayConfig({ kind: "preset", level: "chat" })),
    );
    expect(publishedCapture).toMatchObject({ anchorId: "current-entry", anchorOffset: -900 });
    await act(async () => external.notify());
    await waitFor(() => expect(port.scrollTop).toBe(225));
    await waitFor(() =>
      expect(captureTranscriptView("display-reflow")).toMatchObject({ anchorId: "current-entry", anchorOffset: -225 }),
    );
  } finally {
    unsubscribe?.();
    mounted?.unmount();
    external.restore();
    resetTranscriptViewRegistryForTests();
    resetTranscriptDisplayStoreForTests();
  }
});

test("width-only reflow applies a real 300px prepend once", async () => {
  const geometry = { width: 152, viewportHeight: 400, rowHeights: [1600, 1000] };
  const external = installTranscriptGeometry(() => geometry);
  const listRef = createRef<VirtualListHandle>();
  const model = { ...makeTranscriptPreviewModel(), turns: [readingRow("current"), readingRow("tail")] };
  const body = (next: typeof model) => (
    <TranscriptBody
      model={next}
      config={makeTranscriptDisplayConfig({ kind: "preset", level: "tools" })}
      surface="readOnly"
      disclosureScope="reflow-prepend"
      viewId="reflow-prepend"
      listRef={listRef}
    />
  );
  let mounted: ReturnType<typeof render> | undefined;
  try {
    mounted = render(body(model));
    const port = listRef.current?.getScrollElement();
    if (!port) throw new Error("Prepend reader has no port");
    await startReading(port, external.notify);
    geometry.width = 352;
    geometry.rowHeights = [300, 1000, 1000];
    mounted.rerender(body({ ...model, turns: [readingRow("older"), ...model.turns] }));
    await act(async () => external.notify());
    await waitFor(() => expect(port.scrollTop).toBe(750));
    await waitFor(() =>
      expect(captureTranscriptView("reflow-prepend")).toMatchObject({ anchorId: "current-entry", anchorOffset: -450 }),
    );
    expect(port.querySelector('[data-view-anchor-id="current-entry"] [data-testid="user-bubble"]')?.textContent).toBe(
      "current",
    );
  } finally {
    mounted?.unmount();
    external.restore();
    resetTranscriptViewRegistryForTests();
  }
});

test("width-only reflow retains original intent through a real clamp and later relevant measurement", async () => {
  const geometry = { width: 152, viewportHeight: 400, rowHeights: [1600, 96] };
  const external = installTranscriptGeometry(() => geometry);
  const listRef = createRef<VirtualListHandle>();
  let mounted: ReturnType<typeof render> | undefined;
  try {
    mounted = render(
      <TranscriptBody
        model={{ ...makeTranscriptPreviewModel(), turns: [readingRow("current"), readingRow("tail")] }}
        config={makeTranscriptDisplayConfig({ kind: "preset", level: "tools" })}
        surface="readOnly"
        disclosureScope="clamp-reflow"
        viewId="clamp-reflow"
        listRef={listRef}
      />,
    );
    const port = listRef.current?.getScrollElement();
    if (!port) throw new Error("Clamped reader has no port");
    await startReading(port, external.notify);
    geometry.width = 352;
    geometry.rowHeights = [700, 48];
    await act(async () => external.notify((target) => target === port || target.dataset.index === "0"));
    expect(port.scrollTop).toBeLessThan(900);
    expect(port.querySelector('[data-index="1"]')?.getBoundingClientRect().height).toBe(48);
    expect(captureTranscriptView("clamp-reflow")).toMatchObject({
      anchorId: "current-entry",
      anchorOffset: -900,
      followingBottom: false,
    });
    geometry.rowHeights = [700, 96];
    await act(async () => external.notify());
    await waitFor(() => expect(port.scrollTop).toBe(225));
    await waitFor(() =>
      expect(captureTranscriptView("clamp-reflow")).toMatchObject({
        anchorId: "current-entry",
        anchorOffset: -225,
        followingBottom: false,
      }),
    );
  } finally {
    mounted?.unmount();
    external.restore();
    resetTranscriptViewRegistryForTests();
  }
});

test("width-only reflow mounts its known source after shrink removes it from the rendered range", async () => {
  const geometry = { width: 152, viewportHeight: 400, rowHeights: [1600, ...Array.from({ length: 20 }, () => 96)] };
  const external = installTranscriptGeometry(() => geometry);
  const listRef = createRef<VirtualListHandle>();
  let mounted: ReturnType<typeof render> | undefined;
  try {
    mounted = render(
      <TranscriptBody
        model={{
          ...makeTranscriptPreviewModel(),
          turns: [readingRow("current"), ...Array.from({ length: 20 }, (_, index) => readingRow(`tail-${index}`))],
        }}
        config={makeTranscriptDisplayConfig({ kind: "preset", level: "tools" })}
        surface="readOnly"
        disclosureScope="outside-reflow"
        viewId="outside-reflow"
        listRef={listRef}
      />,
    );
    const port = listRef.current?.getScrollElement();
    if (!port) throw new Error("Virtualized reader has no port");
    await startReading(port, external.notify);
    geometry.width = 352;
    geometry.rowHeights[0] = 100;
    await act(async () => external.notify());
    // The next observer delivery measures newly mounted overscan, including equal estimates.
    await act(async () => external.notify());
    await waitFor(() => expect(port.scrollTop).toBe(0));
    await waitFor(() =>
      expect(captureTranscriptView("outside-reflow")).toMatchObject({ anchorId: "current-entry", anchorOffset: 0 }),
    );
    expect(port.querySelector('[data-view-anchor-id="current-entry"] [data-testid="user-bubble"]')?.textContent).toBe(
      "current",
    );
  } finally {
    mounted?.unmount();
    external.restore();
    resetTranscriptViewRegistryForTests();
  }
});

test.each(["tools", "intent"] as const)(
  "width-only reflow aligns a closed %s alias without opening or focusing it",
  async (level) => {
    const geometry = {
      width: 152,
      viewportHeight: 400,
      rowHeights: [1600, 1000],
      entryBoxes: { summary: { top: 0, height: 40 } },
    };
    const external = installTranscriptGeometry(() => geometry);
    const listRef = createRef<VirtualListHandle>();
    const model = {
      ...makeTranscriptPreviewModel(),
      turns: [
        {
          id: "t1",
          status: "completed",
          items: [toolItem("a", "read_file"), toolItem("b", "read_file"), toolItem("c", "glob")],
        },
        readingRow("tail"),
      ],
    };
    const body = (nextLevel: "full" | "tools" | "intent") => (
      <>
        <button type="button">Outside reader</button>
        <TranscriptBody
          model={model}
          config={makeTranscriptDisplayConfig({ kind: "preset", level: nextLevel })}
          surface="readOnly"
          disclosureScope="alias-reflow"
          viewId="alias-reflow"
          listRef={listRef}
        />
      </>
    );
    let mounted: ReturnType<typeof render> | undefined;
    try {
      mounted = render(body("full"));
      const port = listRef.current?.getScrollElement();
      if (!port) throw new Error("Alias reader has no port");
      await act(async () => {
        external.notify();
        port.scrollTop = 1000;
        fireEvent.scroll(port);
      });
      await act(async () => {
        port.scrollTop = 900;
        fireEvent.scroll(port);
      });
      expect(port.querySelector('[data-view-anchor-id="run:a"]')?.getBoundingClientRect().top).toBe(-900);
      const outside = screen.getByRole("button", { name: "Outside reader" });
      outside.focus();
      geometry.width = 352;
      geometry.rowHeights = [80, 1000];
      mounted.rerender(body(level));
      if (level === "intent") {
        const summary = port.querySelector('details[data-testid="intent-group"] > summary');
        if (!summary) throw new Error("Intent alias has no real summary");
        fireEvent.click(summary);
        expect(summary.parentElement?.hasAttribute("open")).toBe(false);
      }
      await act(async () => external.notify());
      await waitFor(() => expect(port.scrollTop).toBe(0));
      const closed = port.querySelector<HTMLDetailsElement>(
        `details[data-testid="${level === "tools" ? "tool-run" : "intent-group"}"]`,
      );
      expect(closed?.open).toBe(false);
      expect(closed?.querySelector("summary")?.getBoundingClientRect().top).toBe(0);
      expect(closed?.querySelector("summary")?.getBoundingClientRect().height).toBe(40);
      expect(document.activeElement).toBe(outside);
      expect(captureTranscriptView("alias-reflow")?.anchorId).toBe(level === "tools" ? "run:a" : "intent:a");
    } finally {
      mounted?.unmount();
      external.restore();
      resetTranscriptViewRegistryForTests();
    }
  },
);

test.each([
  { earlierHeight: 200, want: 0, wantOffset: 0 },
  { earlierHeight: 1000, want: 600, wantOffset: -600 },
])(
  "width-only reflow uses the equally near earlier message at $earlierHeight when its source is removed",
  async ({ earlierHeight, want, wantOffset }) => {
    const geometry = { width: 152, viewportHeight: 400, rowHeights: [200, 1600, 200, 1000] };
    const external = installTranscriptGeometry(() => geometry);
    const listRef = createRef<VirtualListHandle>();
    const model = {
      ...makeTranscriptPreviewModel(),
      turns: [
        readingRow("earlier"),
        { id: "t1", status: "completed", items: [toolItem("removed", "read_file")] },
        readingRow("later"),
        readingRow("tail"),
      ],
    };
    const body = (hideSource: boolean) => (
      <TranscriptBody
        model={model}
        config={makeTranscriptDisplayConfig(
          hideSource
            ? { kind: "custom", toolIntent: false, toolCalls: false, reasoning: false, expandByDefault: false }
            : { kind: "preset", level: "tools" },
        )}
        surface="readOnly"
        disclosureScope="removed-reflow"
        viewId="removed-reflow"
        listRef={listRef}
      />
    );
    let mounted: ReturnType<typeof render> | undefined;
    try {
      mounted = render(body(false));
      const port = listRef.current?.getScrollElement();
      if (!port) throw new Error("Missing-source reader has no port");
      await act(async () => {
        external.notify();
        port.scrollTop = 1200;
        fireEvent.scroll(port);
      });
      await act(async () => {
        port.scrollTop = 1100;
        fireEvent.scroll(port);
      });
      expect(port.querySelector('[data-view-anchor-id="removed"]')?.getBoundingClientRect().top).toBe(-900);
      geometry.width = 352;
      geometry.rowHeights = [earlierHeight, 20, 200, 1000];
      mounted.rerender(body(true));
      expect(port.querySelector('[data-view-anchor-id="intent:removed"]')).toBeNull();
      await act(async () => external.notify());
      await waitFor(() => expect(port.scrollTop).toBe(want));
      await waitFor(() =>
        expect(captureTranscriptView("removed-reflow")).toMatchObject({
          anchorId: "earlier-entry",
          anchorOffset: wantOffset,
        }),
      );
      expect(port.querySelector('[data-view-anchor-id="removed"]')).toBeNull();
      expect(port.querySelector('[data-view-anchor-id="earlier-entry"] [data-testid="user-bubble"]')?.textContent).toBe(
        "earlier",
      );
    } finally {
      mounted?.unmount();
      external.restore();
      resetTranscriptViewRegistryForTests();
    }
  },
);

test.each([
  { height: 400, want: 0, wantOffset: 0, followingBottom: true },
  { height: 1500, want: 450, wantOffset: -450, followingBottom: false },
])(
  "width-only reflow uses normalized fallback with no semantic entry at $height",
  async ({ height, want, wantOffset, followingBottom }) => {
    const geometry = { width: 152, viewportHeight: 400, rowHeights: [1600, 1000] };
    const external = installTranscriptGeometry(() => geometry);
    const listRef = createRef<VirtualListHandle>();
    const body = (withoutEntries: boolean) => (
      <TranscriptBody
        model={{
          ...makeTranscriptPreviewModel(),
          turns: withoutEntries ? [] : [readingRow("current"), readingRow("tail")],
        }}
        config={makeTranscriptDisplayConfig({ kind: "preset", level: "tools" })}
        surface="readOnly"
        disclosureScope="normalized-reflow"
        viewId="normalized-reflow"
        listRef={listRef}
        trailingRow={
          withoutEntries
            ? { id: "remaining-content", content: <div data-testid="remaining-content">Remaining content</div> }
            : undefined
        }
      />
    );
    let mounted: ReturnType<typeof render> | undefined;
    try {
      mounted = render(body(false));
      const port = listRef.current?.getScrollElement();
      if (!port) throw new Error("Real normalized reader has no port");
      await startReading(port, external.notify);
      geometry.width = 352;
      geometry.rowHeights = [height];
      mounted.rerender(body(true));
      await act(async () => external.notify());
      await waitFor(() => expect(port.scrollTop).toBe(want));
      await waitFor(() =>
        expect(captureTranscriptView("normalized-reflow")).toMatchObject({
          anchorId: undefined,
          anchorOffset: 0,
          followingBottom,
        }),
      );
      expect(port.querySelector('[data-testid="remaining-content"]')?.getBoundingClientRect().top).toBe(wantOffset);
    } finally {
      mounted?.unmount();
      external.restore();
      resetTranscriptViewRegistryForTests();
    }
  },
);

test("a real body restores the same entry and offset after collapse and an older-row prepend", async () => {
  const geometry = {
    width: 500,
    viewportHeight: 500,
    rowHeights: [500, 4500],
    entryBoxes: { "[data-view-anchor-id]": { top: 0, height: 120 } },
  };
  const external = installTranscriptGeometry(() => geometry);
  let first: ReturnType<typeof render> | undefined;
  let returned: ReturnType<typeof render> | undefined;
  const row = (id: string): TurnModel => ({
    id,
    status: "completed",
    items: [{ id: `${id}-entry`, turnId: id, type: "userMessage", text: id, status: "completed" }],
  });
  const model: ThreadModel = {
    ...makeTranscriptPreviewModel(),
    ref: "semantic",
    threadId: "semantic-thread",
    name: "Semantic",
    status: { type: "idle" },
    modelProvider: "scripted",
    model: "scripted",
    askPending: false,
    pendingEscalations: [],
    turns: [row("current"), row("tail")],
  };
  const config = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });
  const firstRef = createRef<VirtualListHandle>();
  const portOf = (ref: typeof firstRef) => {
    const port = ref.current?.getScrollElement();
    if (!port) throw new Error("the real body has no scroll port");
    return port;
  };
  try {
    first = render(
      <TranscriptBody
        model={model}
        config={config}
        surface="readOnly"
        disclosureScope="column-a"
        viewId="column-a"
        listRef={firstRef}
      />,
    );
    const originalPort = portOf(firstRef);
    await act(async () => {
      external.notify();
      originalPort.scrollTop = 20;
      fireEvent.scroll(originalPort);
    });
    const capture = captureTranscriptView("column-a");
    expect(capture).toMatchObject({ anchorId: "current-entry", anchorOffset: -20, followingBottom: false });
    if (!capture) throw new Error("body capture is absent");
    first.unmount();
    geometry.rowHeights = [500, 500, 4500];
    const returnedRef = createRef<VirtualListHandle>();
    returned = render(
      <TranscriptBody
        model={{ ...model, turns: [row("older"), ...model.turns] }}
        config={config}
        surface="readOnly"
        disclosureScope="column-a"
        viewId="column-a"
        listRef={returnedRef}
        initialViewCapture={capture}
      />,
    );
    const returnedPort = portOf(returnedRef);
    await act(async () => external.notify());
    await waitFor(() =>
      expect(captureTranscriptView("column-a")).toMatchObject({ anchorId: "current-entry", anchorOffset: -20 }),
    );
    expect(returnedPort.scrollTop).toBe(520);
    returned.unmount();
  } finally {
    first?.unmount();
    returned?.unmount();
    external.restore();
    resetTranscriptViewRegistryForTests();
  }
});
