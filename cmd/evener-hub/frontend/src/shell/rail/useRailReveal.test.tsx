import { renderHook } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";
import type { RailResources } from "./railPending";
import { useRailReveal } from "./useRailReveal";

const motionMock = vi.hoisted(() => ({ reduced: false }));

vi.mock("../../motion", () => ({
  useReducedMotion: () => motionMock.reduced,
}));

afterEach(() => {
  motionMock.reduced = false;
  vi.restoreAllMocks();
});

// The reveal's first step: the row is already rendered, so the hook scrolls to
// it and reports the reveal consumed.
function revealRenderedRow(): { scroll: ReturnType<typeof vi.fn>; consumed: ReturnType<typeof vi.fn> } {
  const body = document.createElement("div");
  const row = document.createElement("div");
  row.dataset.sessionRef = "local:target";
  const scroll = vi.fn();
  row.scrollIntoView = scroll;
  body.append(row);
  const consumed = vi.fn();
  renderHook(() =>
    useRailReveal({
      revealTarget: "local:target",
      onRevealConsumed: consumed,
      resources: {} as RailResources,
      archivedLists: {},
      expansion: new Map(),
      isExpanded: () => true,
      setRowExpanded: () => undefined,
      groupingMode: "flat",
      bodyRef: { current: body },
    }),
  );
  return { scroll, consumed };
}

describe("useRailReveal scrolling", () => {
  test("glides to the revealed row and centers it", () => {
    const { scroll, consumed } = revealRenderedRow();
    expect(scroll).toHaveBeenCalledTimes(1);
    expect(scroll).toHaveBeenCalledWith({ block: "center", behavior: "smooth" });
    expect(consumed).toHaveBeenCalledTimes(1);
  });

  test("lands at once under reduced motion", () => {
    motionMock.reduced = true;
    const { scroll, consumed } = revealRenderedRow();
    expect(scroll).toHaveBeenCalledTimes(1);
    expect(scroll).toHaveBeenCalledWith({ block: "center", behavior: "instant" });
    expect(consumed).toHaveBeenCalledTimes(1);
  });
});
