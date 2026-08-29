import { cleanup, render, screen } from "@testing-library/react";
import {
  forwardRef,
  type ReactNode,
  useImperativeHandle,
  useLayoutEffect,
  useRef,
} from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ConversationDisplayItem, NarrativeDisplayItem } from "../model";
import type { ConversationAnchor, ConversationSkin } from "./contract";
import {
  estimateDisplayRow,
  VirtualTranscript,
  type VirtualTranscriptHandle,
} from "./VirtualTranscript";

const list = vi.hoisted(() => ({
  props: null as null | {
    items: readonly ConversationDisplayItem[];
    onScroll(metrics: {
      offset: number;
      viewport: number;
      total: number;
    }): void;
    renderItem(item: ConversationDisplayItem, index: number): ReactNode;
  },
  anchor: { key: "item-2", offsetPx: -7, priorIndex: 1 } as {
    key: string;
    offsetPx: number;
    priorIndex: number;
  } | null,
  restoreAnchor: vi.fn(async () => {}),
  scrollToEnd: vi.fn(),
  focusKey: vi.fn(),
}));

vi.mock("../../components/timeline/VariableHeightVirtualList", () => ({
  VariableHeightVirtualList: forwardRef(function MockVariableHeightVirtualList(
    props: {
      items: readonly ConversationDisplayItem[];
      onScroll(metrics: {
        offset: number;
        viewport: number;
        total: number;
      }): void;
      renderItem(item: ConversationDisplayItem, index: number): ReactNode;
    },
    ref,
  ) {
    list.props = props;
    const notified = useRef(false);
    useImperativeHandle(ref, () => ({
      captureAnchor: () => list.anchor,
      restoreAnchor: list.restoreAnchor,
      scrollToEnd: list.scrollToEnd,
      focusKey: list.focusKey,
    }));
    useLayoutEffect(() => {
      if (notified.current) return;
      notified.current = true;
      props.onScroll({ offset: 640, viewport: 360, total: 1_000 });
    }, [props]);
    return (
      <div role="feed" aria-label="Conversation transcript">
        {props.items.map((item, index) => (
          <article key={item.key}>{props.renderItem(item, index)}</article>
        ))}
      </div>
    );
  }),
}));

function bounded(text: string) {
  return { text, truncated: false, originalUtf8Bytes: text.length };
}

function narrative(
  key: string,
  sourceKind: NarrativeDisplayItem["sourceKind"] = "assistant",
  body = key,
  streaming = false,
): NarrativeDisplayItem {
  return {
    key,
    sourceKind,
    body: bounded(body),
    label: null,
    tone: "idle",
    streaming,
    questionKey: null,
    evidenceKey: null,
    sequence: key,
  };
}

function skin(id: ConversationSkin["id"]): ConversationSkin {
  return {
    id,
    className: `skin-${id}`,
    composerAppearance: { density: "compact", accent: "forest" },
    renderNarrativeItem: ({ item, body, focused }) => (
      <div data-skin={id} data-focused={focused}>
        {item.key}:{body}
      </div>
    ),
    renderActivityMarker: ({ item, focused }) => (
      <div data-skin={id} data-focused={focused}>
        {item.key}
      </div>
    ),
    renderConversationChrome: () => null,
  };
}

function transcript(
  items: readonly ConversationDisplayItem[],
  overrides: Partial<{
    skin: ConversationSkin;
    savedAnchor: ConversationAnchor | null;
  }> = {},
  ref?: React.Ref<VirtualTranscriptHandle>,
) {
  return (
    <VirtualTranscript
      ref={ref}
      threadKey="thread-live"
      skinId={(overrides.skin ?? skin("stillwater")).id}
      contentSize="large"
      items={items}
      skin={overrides.skin ?? skin("stillwater")}
      savedAnchor={overrides.savedAnchor ?? null}
    />
  );
}

afterEach(() => {
  cleanup();
  list.props = null;
  list.anchor = { key: "item-2", offsetPx: -7, priorIndex: 1 };
  list.restoreAnchor.mockClear();
  list.scrollToEnd.mockClear();
  list.focusKey.mockClear();
});

describe("estimateDisplayRow", () => {
  it("uses the exact display-family estimates", () => {
    expect(estimateDisplayRow(narrative("u", "user"))).toBe(96);
    expect(estimateDisplayRow(narrative("a", "assistant"))).toBe(128);
    expect(estimateDisplayRow(narrative("q", "question"))).toBe(160);
    expect(
      estimateDisplayRow({
        key: "marker",
        sourceKind: "tool",
        semanticKind: "tool",
        label: bounded("Tool"),
        preview: null,
        duration: null,
        tone: "running",
        state: "running",
        evidenceKey: null,
        sequence: "1",
      }),
    ).toBe(56);
  });
});

describe("VirtualTranscript transitions", () => {
  it("opens at the measured tail unless a saved anchor takes precedence", () => {
    const first = render(transcript([narrative("item-1")]));
    expect(list.scrollToEnd).toHaveBeenCalledWith("auto");
    expect(list.restoreAnchor).not.toHaveBeenCalled();
    first.unmount();
    list.scrollToEnd.mockClear();

    const saved: ConversationAnchor = {
      threadKey: "thread-live",
      itemKey: "item-1",
      offsetPx: -12,
      following: false,
    };
    render(transcript([narrative("item-1")], { savedAnchor: saved }));
    expect(list.restoreAnchor).toHaveBeenCalledWith({
      key: "item-1",
      offsetPx: -12,
      priorIndex: 0,
    });
    expect(list.scrollToEnd).not.toHaveBeenCalled();
  });

  it("uses the exact 48px follow boundary and counts only unseen logical keys", () => {
    const view = render(transcript([narrative("item-1"), narrative("item-2")]));
    list.scrollToEnd.mockClear();
    list.props?.onScroll({ offset: 592, viewport: 360, total: 1_000 });
    view.rerender(
      transcript([
        narrative("item-1"),
        narrative("item-2"),
        narrative("item-3"),
      ]),
    );
    expect(list.scrollToEnd).toHaveBeenCalledWith("auto");
    expect(screen.queryByRole("button", { name: /new activity/i })).toBeNull();

    list.props?.onScroll({ offset: 591, viewport: 360, total: 1_000 });
    view.rerender(
      transcript([
        narrative("item-1"),
        narrative("item-2"),
        narrative("item-3"),
        narrative("item-4"),
        narrative("item-5"),
      ]),
    );
    expect(
      screen.getByRole("button", { name: /2 new activity/i }),
    ).toBeVisible();
  });

  it("follows streaming growth at tail and preserves the anchor without unseen away from tail", () => {
    const view = render(
      transcript([
        narrative("item-1"),
        narrative("item-2", "assistant", "A", true),
      ]),
    );
    list.scrollToEnd.mockClear();
    list.props?.onScroll({ offset: 592, viewport: 360, total: 1_000 });
    view.rerender(
      transcript([
        narrative("item-1"),
        narrative("item-2", "assistant", "A growing", true),
      ]),
    );
    expect(list.scrollToEnd).toHaveBeenCalledWith("auto");

    list.scrollToEnd.mockClear();
    list.props?.onScroll({ offset: 300, viewport: 360, total: 1_000 });
    view.rerender(
      transcript([
        narrative("item-1"),
        narrative("item-2", "assistant", "A complete response", false),
      ]),
    );
    expect(list.restoreAnchor).toHaveBeenCalled();
    expect(list.scrollToEnd).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: /new activity/i })).toBeNull();
  });

  it("reconciles authoritative replacement through surviving, next, previous, and tail fallbacks", () => {
    const view = render(
      transcript([
        narrative("a"),
        narrative("b"),
        narrative("c"),
        narrative("d"),
      ]),
    );
    list.props?.onScroll({ offset: 300, viewport: 360, total: 1_000 });
    for (const [anchorKey, next, expected] of [
      ["b", ["a", "b", "c", "d"], "b"],
      ["b", ["a", "c", "d"], "c"],
      ["d", ["a"], "a"],
      ["b", ["x", "y"], "y"],
    ] as const) {
      view.rerender(
        transcript([
          narrative("a"),
          narrative("b"),
          narrative("c"),
          narrative("d"),
        ]),
      );
      list.anchor = {
        key: anchorKey,
        offsetPx: -7,
        priorIndex: anchorKey === "d" ? 3 : 1,
      };
      list.props?.onScroll({ offset: 300, viewport: 360, total: 1_000 });
      list.restoreAnchor.mockClear();
      view.rerender(transcript(next.map((key) => narrative(key))));
      expect(list.restoreAnchor).toHaveBeenLastCalledWith(
        expect.objectContaining({ key: expected, offsetPx: -7 }),
      );
    }
  });

  it("forwards capture, restore, tail, and focus handle intent", async () => {
    const ref = { current: null as VirtualTranscriptHandle | null };
    render(transcript([narrative("item-1"), narrative("item-2")], {}, ref));

    expect(ref.current?.captureAnchor()).toEqual({
      threadKey: "thread-live",
      itemKey: "item-2",
      offsetPx: -7,
      following: true,
    });
    await ref.current?.restoreAnchor({
      threadKey: "thread-live",
      itemKey: "item-1",
      offsetPx: -3,
      following: false,
    });
    ref.current?.scrollToTail();
    ref.current?.focusKey("item-1");
    expect(list.restoreAnchor).toHaveBeenLastCalledWith({
      key: "item-1",
      offsetPx: -3,
      priorIndex: 0,
    });
    expect(list.scrollToEnd).toHaveBeenLastCalledWith("auto");
    expect(list.focusKey).toHaveBeenCalledWith("item-1");
  });

  it("renders each stable item through all three ConversationSkin callbacks", () => {
    const view = render(
      transcript([narrative("stable")], { skin: skin("stillwater") }),
    );
    expect(screen.getByRole("article").firstElementChild).toHaveAttribute(
      "data-skin",
      "stillwater",
    );
    view.rerender(
      transcript([narrative("stable")], { skin: skin("constellation") }),
    );
    expect(screen.getByRole("article").firstElementChild).toHaveAttribute(
      "data-skin",
      "constellation",
    );
    view.rerender(
      transcript([narrative("stable")], { skin: skin("field-notes") }),
    );
    expect(screen.getByRole("article").firstElementChild).toHaveAttribute(
      "data-skin",
      "field-notes",
    );
  });
});
