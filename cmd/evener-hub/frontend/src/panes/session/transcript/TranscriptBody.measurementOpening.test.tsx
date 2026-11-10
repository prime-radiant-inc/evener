import { makeTranscriptDisplayConfig, type ThreadModel, type TurnModel } from "@evener/appwire-client";
import { act, cleanup, render } from "@testing-library/react";
import { createRef } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { useStore } from "zustand";
import { conversationPaneLifetime } from "../../../shell/paneLifetime";
import { resetThreadsStoreForTests, threadsStore } from "../../../stores/threads";
import { makeTranscriptPreviewModel } from "../../../transcriptDisplay/previewFixture";
import type { VirtualListHandle } from "../../../widgets";
import { resetDisclosureStoreForTests } from "../../../widgets/disclosure/disclosureStore";
import { holdVirtualListMeasurementMicrotasks } from "../../../widgets/virtuallist/measurementTestUtils";
import { type CapturedTranscriptView, resetTranscriptViewRegistryForTests } from "./flow/transcriptViewRegistry";
import { useTranscriptScroll } from "./flow/useTranscriptScroll";
import { TranscriptBody } from "./TranscriptBody";
import { holdReaderFrames, installTranscriptGeometry } from "./transcriptReadingGeometryTestUtils";
import { retainedTranscriptReadView } from "./transcriptReadView";
import "./TurnBlock";

const CHAT = makeTranscriptDisplayConfig({ kind: "preset", level: "chat" });
const REF = "measurement-opening";
const EMPTY: TurnModel = {
  id: "filtered",
  status: "interrupted",
  items: [
    {
      id: "interrupted-steer",
      turnId: "filtered",
      type: "steering",
      text: "",
      steeringKind: "interrupted",
      status: "completed",
    },
  ],
};

function restoredTurn(index: number): TurnModel {
  const id = `restored-${index}`;
  return {
    id,
    status: "completed",
    items: [{ id: `${id}-entry`, turnId: id, type: "userMessage", text: id, status: "completed" }],
  };
}

// Hold only microtasks scheduled directly by the widget. React's own act and
// scheduling microtasks still run normally. Baseline synchronous refs need no drain.
function holdMeasurementMicrotasks() {
  const { pending, restore } = holdVirtualListMeasurementMicrotasks();
  return {
    drain() {
      while (pending.length) pending.shift()?.();
    },
    restore,
  };
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  resetThreadsStoreForTests();
  resetDisclosureStoreForTests();
  resetTranscriptViewRegistryForTests();
});

test.each([10, 2])("same-port opening settlement preserves measured placement with %i visible rows", async (count) => {
  const initial: CapturedTranscriptView = {
    anchorId: "restored-3-entry",
    anchorOffset: 0,
    normalizedOffset: 0,
    followingBottom: false,
  };
  const initialModel: ThreadModel = { ...makeTranscriptPreviewModel(), ref: REF, turns: [EMPTY] };
  threadsStore.setState({ threads: new Map([[REF, initialModel]]), localOpenings: new Map([[REF, 1]]) });
  const geometry = { width: 500, viewportHeight: 600, rowHeights: [0] };
  const external = installTranscriptGeometry(() => geometry);
  const frames = holdReaderFrames();
  const microtasks = holdMeasurementMicrotasks();
  const listRef = createRef<VirtualListHandle>();
  const lifetime = conversationPaneLifetime({ id: REF, type: "transcript", params: { ref: REF }, slot: "main" });
  const view = retainedTranscriptReadView(lifetime, REF, "transcript");
  const demandReadiness: boolean[] = [];
  const loadOlder = vi.fn(async () => {
    demandReadiness.push(
      listRef.current?.isLayoutCurrent?.() === true && !threadsStore.getState().localOpenings.has(REF),
    );
  });
  function Parent() {
    const model = useStore(threadsStore, (state) => state.threads.get(REF));
    if (!model) throw new Error("Missing fixture model");
    const flow = useTranscriptScroll({
      ref: REF,
      model,
      listRef,
      initialViewCapture: initial,
      renderedRowCount: model.turns.length,
      loadOlder,
      onReaderIntent: view.supersedePositioning,
      onReaderMovement: view.syncPositioningMovement,
    });
    return (
      <TranscriptBody
        model={model}
        config={CHAT}
        surface="readOnly"
        disclosureScope={view.id}
        viewId={view.id}
        readView={view}
        initialViewCapture={initial}
        listRef={listRef}
        onMeasurementsChange={flow.restoreViewAnchorAfterMeasurement}
      />
    );
  }
  view.setReadable(true);
  const mounted = render(<Parent />);
  try {
    const port = listRef.current?.getScrollElement();
    if (!port) throw new Error("Missing real scroll port");
    // Browser scrollHeight cannot be less than the viewport, even for an empty
    // sizer. Keep the shared helper's browser-clamped scrolling and row geometry.
    Object.defineProperty(port, "scrollHeight", {
      configurable: true,
      get: () =>
        Math.max(port.clientHeight, Number.parseFloat((port.firstElementChild as HTMLElement).style.height) || 0),
    });
    await act(async () => {
      external.notify();
      microtasks.drain();
    });
    expect(port.querySelector('[data-row-id="filtered"]')?.parentElement?.offsetHeight).toBe(0);
    expect(port.querySelector('[data-row-id="filtered"]')?.parentElement?.hasAttribute("data-measured")).toBe(true);
    expect(port.hasAttribute("data-opening-hold")).toBe(true);
    expect(loadOlder).not.toHaveBeenCalled();

    geometry.rowHeights = [...Array<number>(count).fill(96), 0];
    act(() =>
      threadsStore.setState({
        threads: new Map([
          [
            REF,
            { ...initialModel, turns: [...Array.from({ length: count }, (_, index) => restoredTurn(index)), EMPTY] },
          ],
        ]),
        localOpenings: new Map(),
      }),
    );
    expect(listRef.current?.getScrollElement()).toBe(port);
    if (count === 10) expect(loadOlder).not.toHaveBeenCalled();
    // No synthetic scroll event or resize repairs the opening publication.
    await act(async () => microtasks.drain());
    expect(listRef.current?.getScrollElement()).toBe(port);
    expect(port.hasAttribute("data-opening-hold")).toBe(false);
    expect(listRef.current?.isLayoutCurrent?.()).toBe(true);
    expect(port.scrollHeight - port.clientHeight).toBe(count === 10 ? 360 : 0);
    expect(port.scrollTop).toBe(count === 10 ? 288 : 0);
    if (count === 10) {
      expect(port.querySelector('[data-view-anchor-id="restored-3-entry"]')?.getBoundingClientRect().top).toBe(0);
      expect(loadOlder).not.toHaveBeenCalled();
    } else {
      expect(loadOlder).toHaveBeenCalled();
      expect(demandReadiness.every(Boolean)).toBe(true);
    }
  } finally {
    mounted.unmount();
    lifetime.dispose();
    external.restore();
    frames.restore();
    microtasks.restore();
  }
});
