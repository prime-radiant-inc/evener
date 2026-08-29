import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react";
import {
  forwardRef,
  type KeyboardEventHandler,
  type ReactNode,
  useImperativeHandle,
  useLayoutEffect,
  useState,
} from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { __resetSheetHistory, __sheetHistorySettled } from "../../ui/Sheet";
import type { EvidenceDisplayItem } from "../model";
import { ActivityEvidenceSheet } from "./ActivityEvidenceSheet";

const virtualList = vi.hoisted(() => ({
  calls: [] as Array<{
    readonly items: readonly { readonly key: string }[];
    readonly measurementCache: unknown;
    readonly rowSemantics: unknown;
    readonly scrollSemantics: {
      readonly role: string;
      readonly ariaLabel: string;
      readonly pageScrollOwner: boolean;
      readonly locked: boolean;
    };
  }>,
}));

vi.mock("../../components/timeline/VariableHeightVirtualList", () => ({
  VariableHeightVirtualList: forwardRef(function MockVariableHeightVirtualList(
    props: {
      readonly items: readonly { readonly key: string }[];
      readonly getItemKey: (item: { readonly key: string }) => string;
      readonly renderItem: (
        item: { readonly key: string },
        index: number,
      ) => ReactNode;
      readonly measurementCache: unknown;
      readonly rowSemantics: unknown;
      readonly scrollSemantics: {
        readonly role: string;
        readonly ariaLabel: string;
        readonly pageScrollOwner: boolean;
        readonly locked: boolean;
      };
      readonly onKeyDown?: KeyboardEventHandler<HTMLElement>;
    },
    ref,
  ) {
    virtualList.calls.push(props);
    const [start, setStart] = useState(0);
    const [pendingFocus, setPendingFocus] = useState<string | null>(null);
    useImperativeHandle(ref, () => ({
      captureAnchor: () => null,
      restoreAnchor: async () => {},
      scrollToEnd: () => {},
      focusKey(key: string) {
        const index = props.items.findIndex(
          (item) => props.getItemKey(item) === key,
        );
        setStart(Math.max(0, index - 47));
        setPendingFocus(key);
      },
    }));
    useLayoutEffect(() => {
      if (pendingFocus === null) return;
      document
        .querySelector<HTMLElement>(`[data-mock-section-key="${pendingFocus}"]`)
        ?.focus();
      setPendingFocus(null);
    }, [pendingFocus]);
    const visible = props.items.slice(start, start + 48);
    return (
      <section
        aria-label={props.scrollSemantics.ariaLabel}
        data-page-scroll-owner={
          props.scrollSemantics.pageScrollOwner ? "true" : undefined
        }
        data-scroll-locked={props.scrollSemantics.locked ? "true" : undefined}
        onKeyDown={props.onKeyDown}
      >
        {visible.map((item) => (
          <div
            key={props.getItemKey(item)}
            data-mock-section-key={props.getItemKey(item)}
            tabIndex={-1}
          >
            {props.renderItem(item, props.items.indexOf(item))}
          </div>
        ))}
      </section>
    );
  }),
}));

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

afterEach(async () => {
  cleanup();
  await __sheetHistorySettled();
  __resetSheetHistory();
  history.replaceState(null, "");
  virtualList.calls = [];
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

  it("uses a plain bounded list for exactly 48 sections", () => {
    render(
      <ActivityEvidenceSheet
        open
        evidence={evidence(48)}
        triggerKey="marker-48"
        onClose={() => {}}
        onTriggerUnavailable={() => {}}
      />,
    );

    expect(virtualList.calls).toHaveLength(0);
    expect(
      screen.getAllByRole("region", { name: /Evidence section / }),
    ).toHaveLength(48);
    expect(screen.getByText("Final Bounded detail")).toBeVisible();
  });

  it.each([49, 50, 51, 80])(
    "virtualizes %i sections within 48 mounts and reaches the final logical section",
    (sectionCount) => {
      render(
        <ActivityEvidenceSheet
          open
          evidence={evidence(sectionCount)}
          triggerKey={`marker-${sectionCount}`}
          onClose={() => {}}
          onTriggerUnavailable={() => {}}
        />,
      );

      expect(virtualList.calls.length).toBeGreaterThan(0);
      const call = virtualList.calls.at(-1);
      expect(call?.measurementCache).toEqual({ mode: "ephemeral" });
      expect(call?.rowSemantics).toEqual({ mode: "caller-owned" });
      expect(call?.scrollSemantics).toEqual({
        mode: "custom",
        role: "region",
        ariaLabel: "Activity and evidence details",
        pageScrollOwner: true,
        locked: false,
      });
      expect(
        screen.getAllByRole("region", { name: /Evidence section / }).length,
      ).toBeLessThanOrEqual(48);
      const scroller = screen.getByRole("region", {
        name: "Activity and evidence details",
      });
      fireEvent.keyDown(scroller, { key: "End" });
      expect(screen.getByText("Final Bounded detail")).toBeVisible();
      expect(
        screen.getAllByRole("region", { name: /Evidence section / }).length,
      ).toBeLessThanOrEqual(48);
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
