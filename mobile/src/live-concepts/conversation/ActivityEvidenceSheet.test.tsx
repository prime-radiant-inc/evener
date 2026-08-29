import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { __resetSheetHistory, __sheetHistorySettled } from "../../ui/Sheet";
import type { EvidenceDisplayItem, EvidenceSection } from "../model";
import { ActivityEvidenceSheet } from "./ActivityEvidenceSheet";

const VIEWPORT_HEIGHT = 360;
const ROW_HEIGHT = 72;

class DeterministicResizeObserver implements ResizeObserver {
  static readonly instances = new Set<DeterministicResizeObserver>();
  readonly targets = new Set<Element>();
  private readonly callback: ResizeObserverCallback;

  constructor(callback: ResizeObserverCallback) {
    this.callback = callback;
    DeterministicResizeObserver.instances.add(this);
  }

  observe(target: Element): void {
    this.targets.add(target);
  }

  unobserve(target: Element): void {
    this.targets.delete(target);
  }

  disconnect(): void {
    this.targets.clear();
    DeterministicResizeObserver.instances.delete(this);
  }

  static emitAll(): void {
    for (const observer of DeterministicResizeObserver.instances) {
      const entries = [...observer.targets].map((target) => {
        const rect = target.getBoundingClientRect();
        return {
          target,
          contentRect: rect,
          borderBoxSize: [{ inlineSize: rect.width, blockSize: rect.height }],
          contentBoxSize: [{ inlineSize: rect.width, blockSize: rect.height }],
          devicePixelContentBoxSize: [
            { inlineSize: rect.width, blockSize: rect.height },
          ],
        } as unknown as ResizeObserverEntry;
      });
      if (entries.length > 0) observer.callback(entries, observer);
    }
  }
}

const originalResizeObserver = globalThis.ResizeObserver;
const originalRect = HTMLElement.prototype.getBoundingClientRect;
const originalScrollTo = HTMLElement.prototype.scrollTo;
const originalOffsetHeight = Object.getOwnPropertyDescriptor(
  HTMLElement.prototype,
  "offsetHeight",
);
const originalOffsetWidth = Object.getOwnPropertyDescriptor(
  HTMLElement.prototype,
  "offsetWidth",
);
const originalClientHeight = Object.getOwnPropertyDescriptor(
  HTMLElement.prototype,
  "clientHeight",
);
const originalScrollHeight = Object.getOwnPropertyDescriptor(
  HTMLElement.prototype,
  "scrollHeight",
);

beforeEach(() => {
  globalThis.ResizeObserver = DeterministicResizeObserver;
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", {
    configurable: true,
    get() {
      return this.hasAttribute("data-virtual-list-scroll")
        ? VIEWPORT_HEIGHT
        : 0;
    },
  });
  Object.defineProperty(HTMLElement.prototype, "offsetWidth", {
    configurable: true,
    get() {
      return 390;
    },
  });
  Object.defineProperty(HTMLElement.prototype, "clientHeight", {
    configurable: true,
    get() {
      return this.hasAttribute("data-virtual-list-scroll")
        ? VIEWPORT_HEIGHT
        : 0;
    },
  });
  Object.defineProperty(HTMLElement.prototype, "scrollHeight", {
    configurable: true,
    get() {
      if (!this.hasAttribute("data-virtual-list-scroll")) return 0;
      return Number.parseFloat(
        (this.firstElementChild as HTMLElement | null)?.style.height ?? "0",
      );
    },
  });
  HTMLElement.prototype.getBoundingClientRect = function () {
    const height = this.matches('[data-testid="virtual-transcript-row"]')
      ? ROW_HEIGHT
      : this.hasAttribute("data-virtual-list-scroll")
        ? VIEWPORT_HEIGHT
        : 0;
    return {
      x: 0,
      y: 0,
      top: 0,
      left: 0,
      right: 390,
      bottom: height,
      width: 390,
      height,
      toJSON: () => ({}),
    };
  };
  HTMLElement.prototype.scrollTo = function (
    optionsOrX?: ScrollToOptions | number,
    y?: number,
  ): void {
    this.scrollTop =
      typeof optionsOrX === "number"
        ? (y ?? 0)
        : (optionsOrX?.top ?? this.scrollTop);
    this.dispatchEvent(new Event("scroll"));
  };
});

const bounded = (text: string) => ({
  text,
  truncated: false,
  originalUtf8Bytes: text.length,
});

function evidence(
  sectionCount: number,
  body = "Bounded detail",
): EvidenceDisplayItem {
  return {
    key: `evidence-${sectionCount}`,
    family: "tool",
    title: bounded("Activity and evidence"),
    sections: Array.from({ length: sectionCount }, (_, index) => ({
      heading: bounded(`Section ${index + 1}`),
      body: bounded(index === sectionCount - 1 ? `Final ${body}` : body),
    })),
    redacted: true,
  };
}

function repeatedEvidence(
  sectionCount: number,
  section: EvidenceSection,
): EvidenceDisplayItem {
  return {
    key: `repeated-evidence-${sectionCount}`,
    family: "tool",
    title: bounded("Repeated evidence"),
    sections: Array.from({ length: sectionCount }, () => section),
    redacted: false,
  };
}

async function emitMeasurements(): Promise<void> {
  act(() => DeterministicResizeObserver.emitAll());
  await waitFor(() =>
    expect(
      screen.getAllByTestId("virtual-transcript-row").length,
    ).toBeGreaterThan(0),
  );
}

async function focusFinalFromClose(): Promise<void> {
  await waitFor(() =>
    expect(screen.getByRole("button", { name: "Done" })).toHaveFocus(),
  );
  const close = screen.getByRole("button", {
    name: "Close activity and evidence",
  });
  close.focus();
  expect(close).toHaveFocus();
  fireEvent.keyDown(close, { key: "End" });
}

function restoreDescriptor(
  name: "offsetHeight" | "offsetWidth" | "clientHeight" | "scrollHeight",
  descriptor: PropertyDescriptor | undefined,
): void {
  if (descriptor === undefined)
    Reflect.deleteProperty(HTMLElement.prototype, name);
  else Object.defineProperty(HTMLElement.prototype, name, descriptor);
}

afterEach(async () => {
  cleanup();
  await __sheetHistorySettled();
  __resetSheetHistory();
  history.replaceState(null, "");
  DeterministicResizeObserver.instances.clear();
  globalThis.ResizeObserver = originalResizeObserver;
  HTMLElement.prototype.getBoundingClientRect = originalRect;
  HTMLElement.prototype.scrollTo = originalScrollTo;
  restoreDescriptor("offsetHeight", originalOffsetHeight);
  restoreDescriptor("offsetWidth", originalOffsetWidth);
  restoreDescriptor("clientHeight", originalClientHeight);
  restoreDescriptor("scrollHeight", originalScrollHeight);
  vi.restoreAllMocks();
});

describe("ActivityEvidenceSheet", () => {
  it("keeps all detail absent before activation and renders only escaped bounded text", () => {
    const hidden = ["SOURCE", "PRELUDE", "NEVER", "DISPLAY"].join("_");
    const safe = evidence(1, `**plain** <img src=x> ${hidden}`);
    const view = render(
      <ActivityEvidenceSheet
        open={false}
        evidence={safe}
        triggerKey="marker-safe"
        onClose={() => {}}
        onTriggerUnavailable={() => {}}
      />,
    );

    expect(screen.queryByText("Activity and evidence")).toBeNull();
    expect(document.body.innerHTML).not.toContain(hidden);
    view.rerender(
      <ActivityEvidenceSheet
        open
        evidence={safe}
        triggerKey="marker-safe"
        onClose={() => {}}
        onTriggerUnavailable={() => {}}
      />,
    );
    const dialog = screen.getByRole("dialog", {
      name: "Activity and evidence",
    });
    expect(dialog).toHaveTextContent(`Final **plain** <img src=x> ${hidden}`);
    expect(dialog.querySelector("img")).toBeNull();
    expect(dialog.querySelector("strong")).toBeNull();
    expect(dialog).toHaveTextContent("State: Redacted");
  });

  it("keeps exactly 48 sections plain and focuses its final semantic section from a close action", async () => {
    render(
      <ActivityEvidenceSheet
        open
        evidence={evidence(48)}
        triggerKey="marker-48"
        onClose={() => {}}
        onTriggerUnavailable={() => {}}
      />,
    );

    expect(screen.queryAllByTestId("virtual-transcript-row")).toHaveLength(0);
    expect(
      screen.getAllByRole("region", { name: /Evidence section / }),
    ).toHaveLength(48);
    await focusFinalFromClose();
    expect(
      screen.getByRole("region", { name: "Evidence section 48" }),
    ).toHaveFocus();
  });

  it.each([49, 50, 51, 80])(
    "uses the real measured list for %i sections and focuses the final semantic section",
    async (sectionCount) => {
      render(
        <ActivityEvidenceSheet
          open
          evidence={evidence(sectionCount)}
          triggerKey={`marker-${sectionCount}`}
          onClose={() => {}}
          onTriggerUnavailable={() => {}}
        />,
      );
      await emitMeasurements();
      expect(
        screen.getAllByTestId("virtual-transcript-row").length,
      ).toBeLessThanOrEqual(48);

      await focusFinalFromClose();
      const finalSection = await screen.findByRole("region", {
        name: `Evidence section ${sectionCount}`,
      });
      await waitFor(() => expect(finalSection).toHaveFocus());
      expect(
        screen.getAllByTestId("virtual-transcript-row").length,
      ).toBeLessThanOrEqual(48);
      expect(
        screen.getByRole("region", { name: "Activity and evidence details" }),
      ).not.toHaveFocus();
    },
  );

  it("keeps 48 repeated identical section references distinct and focuses the final occurrence", async () => {
    const duplicateWarning = vi
      .spyOn(console, "error")
      .mockImplementation(() => {});
    const shared = {
      heading: bounded("Repeated heading"),
      body: bounded("Repeated body"),
    };
    render(
      <ActivityEvidenceSheet
        open
        evidence={repeatedEvidence(48, shared)}
        triggerKey="marker-repeated-48"
        onClose={() => {}}
        onTriggerUnavailable={() => {}}
      />,
    );

    expect(
      screen.getAllByRole("region", { name: /Evidence section / }),
    ).toHaveLength(48);
    await focusFinalFromClose();
    expect(
      screen.getByRole("region", { name: "Evidence section 48" }),
    ).toHaveFocus();
    expect(duplicateWarning.mock.calls.flat().join("\n")).not.toContain(
      "same key",
    );
  });

  it.each([49, 80])(
    "keeps %i repeated identical references distinct through real virtualization",
    async (sectionCount) => {
      const duplicateWarning = vi
        .spyOn(console, "error")
        .mockImplementation(() => {});
      const shared = {
        heading: bounded("Repeated heading"),
        body: bounded("Repeated body"),
      };
      const repeated = repeatedEvidence(sectionCount, shared);
      const view = render(
        <ActivityEvidenceSheet
          open
          evidence={repeated}
          triggerKey={`marker-repeated-${sectionCount}`}
          onClose={() => {}}
          onTriggerUnavailable={() => {}}
        />,
      );
      await emitMeasurements();
      const keysBefore = screen
        .getAllByTestId("virtual-transcript-row")
        .map((row) => row.getAttribute("data-item-key"));
      view.rerender(
        <ActivityEvidenceSheet
          open
          evidence={repeated}
          triggerKey={`marker-repeated-${sectionCount}`}
          onClose={() => {}}
          onTriggerUnavailable={() => {}}
        />,
      );
      expect(
        screen
          .getAllByTestId("virtual-transcript-row")
          .map((row) => row.getAttribute("data-item-key")),
      ).toEqual(keysBefore);

      await focusFinalFromClose();
      const finalSection = await screen.findByRole("region", {
        name: `Evidence section ${sectionCount}`,
      });
      await waitFor(() => expect(finalSection).toHaveFocus());
      expect(
        screen.getAllByTestId("virtual-transcript-row").length,
      ).toBeLessThanOrEqual(48);
      expect(duplicateWarning.mock.calls.flat().join("\n")).not.toContain(
        "same key",
      );
    },
  );

  it("uses Sheet modal, Escape, Tab trap, focus entry, and unavailable-trigger callback", async () => {
    const onClose = vi.fn();
    const onTriggerUnavailable = vi.fn();
    const view = render(
      <ActivityEvidenceSheet
        open
        evidence={evidence(2)}
        triggerKey="evicted-trigger"
        onClose={onClose}
        onTriggerUnavailable={onTriggerUnavailable}
      />,
    );
    const dialog = screen.getByRole("dialog", {
      name: "Activity and evidence",
    });
    expect(dialog).toHaveAttribute("aria-modal", "true");
    expect(
      document.querySelectorAll('[data-page-scroll-owner="true"]'),
    ).toHaveLength(1);
    const done = within(dialog).getByRole("button", { name: "Done" });
    await vi.waitFor(() => expect(done).toHaveFocus());
    const close = within(dialog).getByRole("button", {
      name: "Close activity and evidence",
    });
    fireEvent.keyDown(done, { key: "Tab", shiftKey: true });
    expect(close).toHaveFocus();

    fireEvent.keyDown(document, { key: "Escape" });
    await __sheetHistorySettled();
    expect(onClose).toHaveBeenCalledTimes(1);
    view.rerender(
      <ActivityEvidenceSheet
        open={false}
        evidence={evidence(2)}
        triggerKey="evicted-trigger"
        onClose={onClose}
        onTriggerUnavailable={onTriggerUnavailable}
      />,
    );
    await act(async () => {});
    expect(onTriggerUnavailable).toHaveBeenCalledTimes(1);
  });
});
