import type { ItemModel, ProjectedEntry, ProjectedTurn, ThreadModel, TurnModel } from "@evener/appwire-client";
import { makeTranscriptDisplayConfig } from "@evener/appwire-client";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { createRef } from "react";
import { beforeEach, expect, test } from "vitest";
import { useStore } from "zustand";
import { conversationPaneLifetime } from "../../../shell/paneLifetime";
import { resetTranscriptDisplayStoreForTests, transcriptDisplayStore } from "../../../stores/transcriptDisplay";
import { makeTranscriptPreviewModel } from "../../../transcriptDisplay/previewFixture";
import type { VirtualListHandle } from "../../../widgets";
import { resetDisclosureStoreForTests } from "../../../widgets/disclosure/disclosureStore";
// Registers the tool descriptors (fsTools' read_file is fold: "quiet") the
// same way the real session pane does - through TurnBlock's side-effect
// import of ./tools.
import "./TurnBlock";
import { captureTranscriptView, resetTranscriptViewRegistryForTests } from "./flow/transcriptViewRegistry";
import { readingPointOffset } from "./flow/useTranscriptScroll";
import {
  TranscriptBody,
  type TranscriptTurnRow,
  transcriptAnchorEntriesForRows,
  transcriptRunDisclosureIdsForRows,
} from "./TranscriptBody";
import { installTranscriptGeometry } from "./transcriptReadingGeometryTestUtils";
import { retainedTranscriptReadView } from "./transcriptReadView";

beforeEach(resetDisclosureStoreForTests);

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

test.each([
  { oldHeight: 1600, oldOffset: -900, nextHeight: 1000, want: -450 },
  { oldHeight: 1600, oldOffset: -900, nextHeight: 700, want: -225 },
  { oldHeight: 1600, oldOffset: -900, nextHeight: 300, want: 0 },
  { oldHeight: 200, oldOffset: -20, nextHeight: 1000, want: 0 },
  { oldHeight: 1600, oldOffset: 30, nextHeight: 700, want: 30 },
])("width-only bounded reading point $oldOffset at $nextHeight", ({ oldHeight, oldOffset, nextHeight, want }) => {
  const captured = {
    anchorOffset: oldOffset,
    normalizedOffset: 0,
    followingBottom: false,
    readingPoint: { entryHeight: oldHeight, viewportHeight: 400, viewportWidth: 152 },
  };
  expect(readingPointOffset(captured, nextHeight, 400)).toBe(want);
});

test("width-only policy preserves an ordinary display offset without a measured point", () => {
  expect(readingPointOffset({ anchorOffset: -900, normalizedOffset: 0, followingBottom: false }, 700, 400)).toBe(-900);
});

test.each([
  { label: "partial shrink", nextHeight: 700, nextViewport: 400, tailHeight: 1000, want: 225 },
  { label: "same-range unchanged rows", nextHeight: 1600, nextViewport: 500, tailHeight: 1000, want: 825 },
  { label: "equal-estimate overscan", nextHeight: 700, nextViewport: 400, tailHeight: 96, want: 225 },
])("width-only reflow preserves the current entry, $label", async ({ nextHeight, nextViewport, tailHeight, want }) => {
  const geometry = { width: 152, viewportHeight: 400, rowHeights: [1600, tailHeight] };
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
    expect(captureTranscriptView("width-only")).toMatchObject({ anchorId: "current-entry", anchorOffset: -want });
    expect(port.querySelector('[data-view-anchor-id="current-entry"] [data-testid="user-bubble"]')?.textContent).toBe(
      "current",
    );
  } finally {
    mounted?.unmount();
    external.restore();
    resetTranscriptViewRegistryForTests();
  }
});

function readingRow(id: string): TurnModel {
  return {
    id,
    status: "completed",
    items: [{ id: `${id}-entry`, turnId: id, type: "userMessage", text: id, status: "completed" }],
  };
}

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
      expect(captureTranscriptView("recover")).toMatchObject({ anchorId: "current-entry", anchorOffset: -want });
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
    expect(captureTranscriptView(view.id)).toMatchObject({ anchorId: "current-entry", anchorOffset: -225 });
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
    expect(captureTranscriptView("display-reflow")).toMatchObject({ anchorId: "current-entry", anchorOffset: -225 });
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
    expect(captureTranscriptView("reflow-prepend")).toMatchObject({ anchorId: "current-entry", anchorOffset: -450 });
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
    geometry.rowHeights = [700, 0];
    await act(async () => external.notify());
    expect(port.scrollTop).toBeLessThan(900);
    expect(captureTranscriptView("clamp-reflow")).toMatchObject({
      anchorId: "current-entry",
      anchorOffset: -900,
      followingBottom: false,
    });
    geometry.rowHeights = [700, 96];
    await act(async () => external.notify());
    await waitFor(() => expect(port.scrollTop).toBe(225));
    expect(captureTranscriptView("clamp-reflow")).toMatchObject({
      anchorId: "current-entry",
      anchorOffset: -225,
      followingBottom: false,
    });
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
    expect(captureTranscriptView("outside-reflow")).toMatchObject({ anchorId: "current-entry", anchorOffset: 0 });
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
      expect(captureTranscriptView("removed-reflow")).toMatchObject({
        anchorId: "earlier-entry",
        anchorOffset: wantOffset,
      });
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
      expect(captureTranscriptView("normalized-reflow")).toMatchObject({
        anchorId: undefined,
        anchorOffset: 0,
        followingBottom,
      });
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
