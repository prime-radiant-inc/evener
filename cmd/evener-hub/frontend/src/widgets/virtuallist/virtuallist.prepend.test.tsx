import type { Virtualizer } from "@tanstack/react-virtual";
import { act, cleanup, render } from "@testing-library/react";
import { createRef } from "react";
import { expect, test, vi } from "vitest";
import { useTranscriptScroll } from "../../panes/session/transcript/flow/useTranscriptScroll";
import {
  holdReaderFrames,
  installTranscriptGeometry,
} from "../../panes/session/transcript/transcriptReadingGeometryTestUtils";
import { makeTranscriptPreviewModel } from "../../transcriptDisplay/previewFixture";
import { VirtualList, type VirtualListHandle } from "./index";

test("a successful short-row prepend settles without recursive attachment updates", async () => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  // Returning to synchronous ref measurement must trip React's nested-update
  // guard here: every collapsing overscan window attaches another window.
  let rows = [50, 70, 80].map((height, index) => ({ key: `present-${index}`, height }));
  const geometry = { width: 800, viewportHeight: 500, rowHeights: rows.map((row) => row.height) };
  const external = installTranscriptGeometry(
    () => geometry,
    (element) => element.closest<HTMLElement>("[data-list-fixture]")?.firstElementChild as HTMLElement | undefined,
  );
  const frames = holdReaderFrames();
  const ref = createRef<VirtualListHandle>();
  let instance: Virtualizer<HTMLDivElement, HTMLDivElement> | undefined;
  let changes = 0;
  const rowAt = (index: number) => {
    const row = rows[index];
    if (!row) throw new Error(`missing fixture row ${index}`);
    return row;
  };
  function List() {
    const model = {
      ...makeTranscriptPreviewModel(),
      ref: "prepend-loop",
      turns: rows.map((row) => ({
        id: row.key,
        status: "completed",
        items: [{ id: `${row.key}-item`, turnId: row.key, type: "userMessage", text: row.key, status: "completed" }],
      })),
    };
    const flow = useTranscriptScroll({ ref: model.ref, model, listRef: ref, loadOlder: async () => {} });
    return (
      <div data-list-fixture>
        <VirtualList
          ref={ref}
          dynamic
          anchorToEnd
          count={rows.length}
          estimateSize={() => 96}
          getItemKey={(index) => rowAt(index).key}
          onChange={(value) => {
            instance = value;
            if (++changes > 10_000) throw new Error("measurement notification tripwire");
            flow.restoreViewAnchorAfterMeasurement();
          }}
          renderRow={(index) => <div data-row-key={rowAt(index).key} />}
        />
      </div>
    );
  }
  const settle = async () => {
    let deliveries = 0;
    do {
      if (++deliveries > 1000) throw new Error("measurement settlement tripwire");
      await act(async () => external.notify());
    } while (frames.pending.size > 0 || !ref.current?.isMeasurementReady?.());
  };
  try {
    const view = render(<List />);
    const port = ref.current?.getScrollElement();
    if (!port) throw new Error("missing list port");
    Object.defineProperty(port, "scrollHeight", {
      configurable: true,
      get: () => Math.max(500, Number.parseFloat((port.firstElementChild as HTMLElement).style.height)),
    });
    await settle();
    expect(port.scrollTop).toBe(Math.max(0, port.scrollHeight - 500));
    await act(async () => vi.runOnlyPendingTimers());
    expect(instance?.isScrolling).toBe(false);
    const retained = port.querySelector<HTMLElement>('[data-row-key="present-0"]')?.parentElement;
    if (!retained) throw new Error("missing retained row");
    const beforeTop = retained.getBoundingClientRect().top;
    expect(beforeTop).toBe(0);
    const beforeChanges = changes;
    const older = Array.from({ length: 720 }, (_, index) => ({
      key: `older-${index}`,
      height: [30, 60, 90][index] ?? 0,
    }));
    rows = [...older, ...rows];
    geometry.rowHeights = rows.map((row) => row.height);
    view.rerender(<List />);
    await settle();
    expect(ref.current?.getScrollElement()).toBe(port);
    expect(port.querySelector('[data-row-key="present-0"]')?.parentElement).toBe(retained);
    // All measured content is only 380px tall. The browser cannot scroll;
    // the existing row follows the real 180px prefix instead of overlapping it.
    expect(port.scrollTop).toBe(0);
    expect(retained.getBoundingClientRect().top).toBe(180);
    expect(retained.style.transform).toBe("translateY(180px)");
    expect(port.querySelector('[data-row-key="older-1"]')?.parentElement?.style.transform).toBe("translateY(30px)");
    expect(instance?.itemSizeCache.get("present-0")).toBe(50);
    expect(instance?.itemSizeCache.get("present-1")).toBe(70);
    expect(changes - beforeChanges).toBeLessThan(4000);
    const settledChanges = changes;
    await act(async () => external.notify());
    expect(changes).toBe(settledChanges);
  } finally {
    cleanup();
    external.restore();
    frames.restore();
    vi.useRealTimers();
  }
});
