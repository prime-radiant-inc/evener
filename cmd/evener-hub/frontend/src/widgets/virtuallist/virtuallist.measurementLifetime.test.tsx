import type { Virtualizer } from "@tanstack/react-virtual";
import { act, cleanup, render } from "@testing-library/react";
import { createRef, StrictMode } from "react";
import { expect, test } from "vitest";
import { installTranscriptGeometry } from "../../panes/session/transcript/transcriptReadingGeometryTestUtils";
import { VirtualList, type VirtualListHandle } from "./index";
import { holdVirtualListMeasurementMicrotasks } from "./measurementTestUtils";

function holdMeasurements() {
  const { pending, restore } = holdVirtualListMeasurementMicrotasks();
  return {
    drain() {
      let deliveries = 0;
      while (pending.length) {
        if (++deliveries > 100) throw new Error("measurement lifetime tripwire");
        pending.shift()?.();
      }
    },
    restore,
  };
}

test.each(["disable", "remove row", "unmount", "strict replay"] as const)(
  "queued measurements respect %s and a fresh mount still measures",
  async (action) => {
    const geometry = { width: 800, viewportHeight: 500, rowHeights: [120] };
    const external = installTranscriptGeometry(
      () => geometry,
      (element) =>
        element.closest<HTMLElement>("[data-measurement-fixture]")?.firstElementChild as HTMLElement | undefined,
    );
    const microtasks = holdMeasurements();
    const ref = createRef<VirtualListHandle>();
    let instance: Virtualizer<HTMLDivElement, HTMLDivElement> | undefined;
    const list = (dynamic = true, count = 1) => (
      <div data-measurement-fixture>
        <VirtualList
          ref={ref}
          dynamic={dynamic}
          count={count}
          estimateSize={() => 96}
          getItemKey={() => "row"}
          onChange={(current) => {
            instance = current;
          }}
          renderRow={() => <div data-lifetime-row />}
        />
      </div>
    );
    try {
      const view = render(action === "strict replay" ? <StrictMode>{list()}</StrictMode> : list());
      const oldRow = view.container.querySelector("[data-lifetime-row]")?.parentElement;
      expect(oldRow?.hasAttribute("data-measured")).toBe(false);
      if (action === "disable") view.rerender(list(false));
      else if (action === "remove row") view.rerender(list(true, 0));
      else if (action === "unmount") view.unmount();
      await act(async () => microtasks.drain());
      if (action === "strict replay") {
        expect(oldRow?.hasAttribute("data-measured")).toBe(true);
        expect(instance?.itemSizeCache.get("row")).toBe(120);
      } else {
        expect(oldRow?.hasAttribute("data-measured")).toBe(false);
        expect(instance?.elementsCache.size).toBe(0);
      }
      cleanup();
      const fresh = render(list());
      await act(async () => microtasks.drain());
      expect(fresh.container.querySelector("[data-lifetime-row]")?.parentElement?.hasAttribute("data-measured")).toBe(
        true,
      );
      expect(instance?.itemSizeCache.get("row")).toBe(120);
    } finally {
      cleanup();
      external.restore();
      microtasks.restore();
    }
  },
);
