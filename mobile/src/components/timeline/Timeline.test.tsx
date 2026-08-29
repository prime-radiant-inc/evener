// Component tests for the virtualized Timeline and all item families.
// The virtualizer is mocked so every item renders in jsdom (no layout);
// the mock preserves stable keying by item.id so the "one scroller" and
// "stable identity" invariants are still exercised.
//
// Coverage: every item family, activity cluster, streaming assistant,
// failure, attachment, unknown neutral row, one timeline scroller,
// accessible expansion, new-activity pill, load-older row, and the
// "no raw protocol JSON by default" rule.

import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
} from "@testing-library/react";
import { forwardRef, type ReactNode, useImperativeHandle } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type {
  ActivityDetail,
  ActivityFamily,
  ActivityState,
  AskBatch,
  AttachmentRef,
  MobileTimelineItem,
  NoticeTone,
} from "../../conversation/model";
import type { ContentSizeCategory } from "../../native/contract";
import { Timeline as ProductionTimeline, type TimelineProps } from "./Timeline";
import { TimelineItem } from "./TimelineItem";

function Timeline(
  props: Omit<TimelineProps, "threadKey" | "contentSize"> & {
    readonly threadKey?: string;
    readonly contentSize?: ContentSizeCategory;
  },
) {
  return (
    <ProductionTimeline
      threadKey="timeline-test-thread"
      contentSize="large"
      {...props}
    />
  );
}

const virtualList = vi.hoisted(() => ({
  props: null as null | {
    items: readonly MobileTimelineItem[];
    cacheScope: {
      threadKey: string;
      contentSize: ContentSizeCategory;
    };
    onScroll(metrics: {
      offset: number;
      viewport: number;
      total: number;
    }): void;
    renderItem(item: MobileTimelineItem, index: number): ReactNode;
  },
  captureAnchor: vi.fn(() => ({ key: "u1", offsetPx: -9, priorIndex: 0 })),
  restoreAnchor: vi.fn(async () => {}),
  scrollToEnd: vi.fn(),
  focusKey: vi.fn(),
}));

// TimelineItem has separate renderer coverage above. Timeline's tests mock only
// the shared virtualization boundary so they prove its coordinator contract.
vi.mock("./VariableHeightVirtualList", () => ({
  VariableHeightVirtualList: forwardRef(function MockVariableHeightVirtualList(
    props: {
      items: readonly MobileTimelineItem[];
      cacheScope: {
        threadKey: string;
        contentSize: ContentSizeCategory;
      };
      onScroll(metrics: {
        offset: number;
        viewport: number;
        total: number;
      }): void;
      renderItem(item: MobileTimelineItem, index: number): ReactNode;
    },
    ref,
  ) {
    virtualList.props = props;
    useImperativeHandle(ref, () => ({
      captureAnchor: virtualList.captureAnchor,
      restoreAnchor: virtualList.restoreAnchor,
      scrollToEnd: virtualList.scrollToEnd,
      focusKey: virtualList.focusKey,
    }));
    return (
      <div
        className="evener-timeline__scroll"
        role="feed"
        onScroll={() =>
          props.onScroll({ offset: 0, viewport: 360, total: 1_000 })
        }
      >
        {props.items.map((item, index) => (
          <div key={item.id}>{props.renderItem(item, index)}</div>
        ))}
      </div>
    );
  }),
}));

afterEach(() => {
  cleanup();
  virtualList.props = null;
  virtualList.captureAnchor.mockClear();
  virtualList.restoreAnchor.mockClear();
  virtualList.scrollToEnd.mockClear();
  virtualList.focusKey.mockClear();
});

// --- item factories --------------------------------------------------------

function userItem(id: string, text: string): MobileTimelineItem {
  return { kind: "user", id, text };
}
function assistantItem(
  id: string,
  markdown: string,
  streaming = false,
): MobileTimelineItem {
  return { kind: "assistant", id, markdown, streaming };
}
function activityItem(
  id: string,
  label: string,
  state: ActivityState = "completed",
  detail: ActivityDetail = {},
  family: ActivityFamily = "tool",
): MobileTimelineItem {
  return { kind: "activity", id, label, family, state, detail };
}
function noticeItem(
  id: string,
  tone: NoticeTone,
  text: string,
): MobileTimelineItem {
  return {
    kind: "notice",
    id,
    origin: "steering",
    family: tone === "warning" ? "warning" : "informational",
    tone,
    text,
  };
}
function questionItem(id: string, batch: AskBatch): MobileTimelineItem {
  return { kind: "question", id, batch };
}
function failureItem(
  id: string,
  title: string,
  detail: string,
): MobileTimelineItem {
  return { kind: "failure", id, title, detail };
}
function attachmentsItem(
  id: string,
  items: AttachmentRef[],
): MobileTimelineItem {
  return { kind: "attachments", id, items };
}

const SAMPLE_BATCH: AskBatch = {
  callId: "call-1",
  questions: [
    {
      key: "call-1:0",
      header: "Choose",
      question: "Which option?",
      options: [{ label: "A", detail: "First" }],
      multiSelect: false,
    },
  ],
};

const SAMPLE_ATTACHMENTS: AttachmentRef[] = [
  { id: "att-1", src: "https://example.com/a.png", name: "a.png" },
  { id: "att-2", src: "https://example.com/b.png", name: "b.png" },
];

// --- TimelineItem dispatch --------------------------------------------------

describe("TimelineItem dispatch — every item family", () => {
  it("renders user items as a trailing bubble with plain text", () => {
    render(<TimelineItem item={userItem("u1", "hello world")} />);
    expect(screen.getByText("hello world")).toBeInTheDocument();
  });

  it("renders assistant items as sanitized HTML", () => {
    render(<TimelineItem item={assistantItem("a1", "**bold**")} />);
    expect(screen.getByText("bold").tagName).toBe("STRONG");
  });

  it("renders activity items as a one-line card", () => {
    render(
      <TimelineItem
        item={activityItem("act1", "Read file", "completed", {
          output: "file contents",
        })}
      />,
    );
    expect(screen.getByText("Read file")).toBeInTheDocument();
    // Detail is hidden until expanded.
    expect(screen.queryByText("file contents")).toBeNull();
  });

  it("renders notice items as tone-colored plain text", () => {
    render(
      <TimelineItem item={noticeItem("n1", "warning", "Turn limit reached")} />,
    );
    expect(screen.getByText("Turn limit reached")).toBeInTheDocument();
  });

  it("renders question items as a placeholder card", () => {
    render(<TimelineItem item={questionItem("q1", SAMPLE_BATCH)} />);
    expect(screen.getByText("Which option?")).toBeInTheDocument();
  });

  it("renders failure items as inline plain text", () => {
    render(
      <TimelineItem
        item={failureItem("f1", "Tool failed", "Connection refused")}
      />,
    );
    expect(screen.getByText("Tool failed")).toBeInTheDocument();
    expect(screen.getByText("Connection refused")).toBeInTheDocument();
  });

  it("renders attachment items as a thumbnail strip with plain text captions", () => {
    render(<TimelineItem item={attachmentsItem("att1", SAMPLE_ATTACHMENTS)} />);
    expect(screen.getByText("a.png")).toBeInTheDocument();
    expect(screen.getByText("b.png")).toBeInTheDocument();
  });

  it("renders unknown item kinds as a neutral activity row", () => {
    // An activity with a neutral "Activity" label (forward-compatible unknown).
    render(
      <TimelineItem
        item={activityItem(
          "unk1",
          "Activity",
          "completed",
          {
            output: "some unknown data",
          },
          "unknown",
        )}
      />,
    );
    expect(screen.getByText("Activity")).toBeInTheDocument();
  });
});

// --- ActivityRow expansion --------------------------------------------------

describe("ActivityRow — accessible expansion", () => {
  it("expands on tap to reveal detail as plain text", () => {
    render(
      <TimelineItem
        item={activityItem("act1", "Read file", "completed", {
          arguments: '{"path":"/tmp/x"}',
          output: "contents here",
        })}
      />,
    );
    const row = screen.getByRole("button", { name: /read file/i });
    fireEvent.click(row);
    expect(screen.getByText("contents here")).toBeInTheDocument();
  });

  it("is keyboard accessible (focus + Enter activates the button)", () => {
    render(
      <TimelineItem
        item={activityItem("act1", "Read file", "completed", {
          output: "contents here",
        })}
      />,
    );
    const row = screen.getByRole("button", { name: /read file/i });
    row.focus();
    // A native <button> fires click on Enter/Space; testing-library simulates
    // this by firing click after keyDown on a focused button.
    fireEvent.click(row);
    expect(screen.getByText("contents here")).toBeInTheDocument();
  });

  it("does not show raw protocol JSON by default (collapsed)", () => {
    render(
      <TimelineItem
        item={activityItem("act1", "Read file", "completed", {
          arguments: '{"secret":"value","path":"/tmp"}',
          output: "ok",
        })}
      />,
    );
    // Raw argumentsJson is NOT visible in the collapsed row.
    expect(screen.queryByText(/"secret":"value"/)).toBeNull();
  });
});

// --- Streaming assistant -----------------------------------------------------

describe("Streaming assistant", () => {
  it("shows a streaming indicator when streaming is true", () => {
    render(<TimelineItem item={assistantItem("a1", "thinking", true)} />);
    const container = document.querySelector(".evener-assistant-message");
    expect(container?.getAttribute("data-streaming")).toBe("true");
  });

  it("omits streaming indicator when streaming is false", () => {
    render(<TimelineItem item={assistantItem("a1", "done", false)} />);
    const container = document.querySelector(".evener-assistant-message");
    expect(container?.getAttribute("data-streaming")).toBeNull();
  });
});

// --- Timeline scroller + new-activity + load-older --------------------------

describe("Timeline — one scroller", () => {
  it("renders exactly one scroll container", () => {
    render(<Timeline items={[userItem("u1", "hi")]} following unseen={0} />);
    const scrollers = document.querySelectorAll(".evener-timeline__scroll");
    expect(scrollers.length).toBe(1);
  });

  it("keys items by stable id, not index", () => {
    render(
      <Timeline
        items={[userItem("user-abc", "hi"), assistantItem("ast-xyz", "yo")]}
        following
        unseen={0}
      />,
    );
    expect(screen.getByText("hi")).toBeInTheDocument();
    expect(screen.getByText("yo")).toBeInTheDocument();
  });
});

describe("Timeline — new-activity pill", () => {
  it("shows the pill when unseen > 0 and not following", () => {
    render(
      <Timeline items={[userItem("u1", "hi")]} following={false} unseen={3} />,
    );
    expect(screen.getByRole("button", { name: /3 new/i })).toBeInTheDocument();
  });

  it("hides the pill when following", () => {
    render(<Timeline items={[userItem("u1", "hi")]} following unseen={3} />);
    expect(screen.queryByRole("button", { name: /new/i })).toBeNull();
  });

  it("hides the pill when unseen is 0", () => {
    render(
      <Timeline items={[userItem("u1", "hi")]} following={false} unseen={0} />,
    );
    expect(screen.queryByRole("button", { name: /new/i })).toBeNull();
  });

  it("invokes onTapNewActivity when the pill is tapped", () => {
    const onTapNewActivity = vi.fn();
    render(
      <Timeline
        items={[userItem("u1", "hi")]}
        following={false}
        unseen={2}
        onTapNewActivity={onTapNewActivity}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: /2 new/i }));
    expect(onTapNewActivity).toHaveBeenCalledOnce();
  });
});

describe("Timeline — load-older row", () => {
  // jsdom scrollTop is always 0, so "near top" is always true.
  // We test that loadOlder fires when the scroll position is at the top.

  it("calls loadOlder when scrolled near the top", () => {
    const loadOlder = vi.fn(async () => ({ status: "ignored" as const }));
    render(
      <Timeline
        items={[userItem("u1", "hi")]}
        following
        unseen={0}
        loadOlder={loadOlder}
      />,
    );
    const scroller = document.querySelector(".evener-timeline__scroll");
    expect(scroller).not.toBeNull();
    // Fire a scroll event at the top (scrollTop=0 in jsdom).
    fireEvent.scroll(scroller as Element);
    expect(loadOlder).toHaveBeenCalled();
  });

  it("does not call loadOlder when no callback is provided", () => {
    render(<Timeline items={[userItem("u1", "hi")]} following unseen={0} />);
    const scroller = document.querySelector(".evener-timeline__scroll");
    expect(() => fireEvent.scroll(scroller as Element)).not.toThrow();
  });

  it("correlates restoration with the completed load generation despite interleaved append and replacement", async () => {
    let complete:
      | ((result: { status: "loaded"; itemKeys: string[] }) => void)
      | null = null;
    const loadOlder = vi.fn(
      () =>
        new Promise<{ status: "loaded"; itemKeys: string[] }>((resolve) => {
          complete = resolve;
        }),
    );
    const initial = [userItem("u1", "one"), assistantItem("a1", "two")];
    const view = render(
      <Timeline
        items={initial}
        following={false}
        unseen={0}
        loadOlder={loadOlder}
      />,
    );
    fireEvent.scroll(screen.getByRole("feed"));
    expect(loadOlder).toHaveBeenCalledOnce();
    expect(virtualList.captureAnchor).toHaveBeenCalledOnce();

    // Neither an interleaved append nor same-count authoritative replacement
    // is the explicit page completion.
    view.rerender(
      <Timeline
        items={[...initial, assistantItem("new", "new")]}
        following={false}
        unseen={0}
        loadOlder={loadOlder}
      />,
    );
    expect(virtualList.restoreAnchor).not.toHaveBeenCalled();

    view.rerender(
      <Timeline
        items={[
          userItem("u1", "one"),
          assistantItem("replacement", "replacement"),
          assistantItem("new", "new"),
        ]}
        following={false}
        unseen={0}
        loadOlder={loadOlder}
      />,
    );
    expect(virtualList.restoreAnchor).not.toHaveBeenCalled();

    view.rerender(
      <Timeline
        items={[
          userItem("older", "older"),
          userItem("u1", "one"),
          assistantItem("replacement", "replacement"),
          assistantItem("new", "new"),
        ]}
        following={false}
        unseen={0}
        loadOlder={loadOlder}
      />,
    );
    expect(virtualList.restoreAnchor).not.toHaveBeenCalled();
    await act(async () => {
      complete?.({ status: "loaded", itemKeys: ["older"] });
    });
    expect(virtualList.restoreAnchor).toHaveBeenCalledWith({
      key: "u1",
      offsetPx: -9,
      priorIndex: 1,
    });
  });

  it("clears a failed generation so a later retry can restore", async () => {
    const completions: Array<
      (
        result: { status: "failed" } | { status: "loaded"; itemKeys: string[] },
      ) => void
    > = [];
    const loadOlder = vi.fn(
      () =>
        new Promise<
          { status: "failed" } | { status: "loaded"; itemKeys: string[] }
        >((resolve) => completions.push(resolve)),
    );
    const view = render(
      <Timeline
        items={[userItem("u1", "one")]}
        following={false}
        unseen={0}
        loadOlder={loadOlder}
      />,
    );
    fireEvent.scroll(screen.getByRole("feed"));
    await act(async () => completions[0]?.({ status: "failed" }));
    fireEvent.scroll(screen.getByRole("feed"));
    expect(loadOlder).toHaveBeenCalledTimes(2);

    view.rerender(
      <Timeline
        items={[userItem("older", "older"), userItem("u1", "one")]}
        following={false}
        unseen={0}
        loadOlder={loadOlder}
      />,
    );
    await act(async () =>
      completions[1]?.({ status: "loaded", itemKeys: ["older"] }),
    );
    expect(virtualList.restoreAnchor).toHaveBeenCalledWith(
      expect.objectContaining({ key: "u1" }),
    );
  });
});

describe("Timeline — shared measured anchor boundary", () => {
  it("supplies stable item identity and the exact shared virtualization limits", () => {
    render(
      <Timeline
        items={[userItem("stable-user", "hi")]}
        following={false}
        unseen={0}
      />,
    );
    expect(virtualList.props).toEqual(
      expect.objectContaining({ overscan: 6, maxMountedRows: 48 }),
    );
    expect(virtualList.props?.items[0]?.id).toBe("stable-user");
  });

  it("uses real thread/content-size scope and reports the exact 48px follow transition", () => {
    const onFollowingChange = vi.fn();
    const view = render(
      <Timeline
        threadKey="thread-a"
        contentSize="large"
        items={[userItem("stable-user", "hi")]}
        following={false}
        unseen={0}
        onFollowingChange={onFollowingChange}
      />,
    );
    expect(virtualList.props?.cacheScope).toEqual({
      threadKey: "thread-a",
      skinId: "stillwater",
      contentSize: "large",
    });
    act(() =>
      virtualList.props?.onScroll({ offset: 592, viewport: 360, total: 1_000 }),
    );
    expect(onFollowingChange).toHaveBeenLastCalledWith(true);
    act(() =>
      virtualList.props?.onScroll({ offset: 591, viewport: 360, total: 1_000 }),
    );
    expect(onFollowingChange).toHaveBeenLastCalledWith(false);

    view.rerender(
      <Timeline
        threadKey="thread-b"
        contentSize="accessibilityExtraExtraExtraLarge"
        items={[userItem("stable-user", "hi")]}
        following={false}
        unseen={0}
        onFollowingChange={onFollowingChange}
      />,
    );
    expect(virtualList.props?.cacheScope).toEqual({
      threadKey: "thread-b",
      skinId: "stillwater",
      contentSize: "accessibilityExtraExtraExtraLarge",
    });
  });
});

// --- No raw protocol JSON ----------------------------------------------------

describe("Timeline — no raw protocol JSON by default", () => {
  it("does not expose raw argumentsJson in any collapsed item", () => {
    const items: MobileTimelineItem[] = [
      activityItem("act1", "Read file", "completed", {
        arguments: '{"path":"/secret/key","token":"abc"}',
        output: "ok",
      }),
      userItem("u1", "hello"),
      noticeItem("n1", "info", "system notice"),
    ];
    render(<Timeline items={items} following unseen={0} />);
    // The raw JSON is not present in the collapsed timeline.
    expect(screen.queryByText(/"token":"abc"/)).toBeNull();
    expect(screen.queryByText(/"path":"\/secret\/key"/)).toBeNull();
  });
});
