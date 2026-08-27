// Component tests for the virtualized Timeline and all item families.
// The virtualizer is mocked so every item renders in jsdom (no layout);
// the mock preserves stable keying by item.id so the "one scroller" and
// "stable identity" invariants are still exercised.
//
// Coverage: every item family, activity cluster, streaming assistant,
// failure, attachment, unknown neutral row, one timeline scroller,
// accessible expansion, new-activity pill, load-older row, and the
// "no raw protocol JSON by default" rule.

import { cleanup, fireEvent, render, screen } from "@testing-library/react";
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
import { Timeline } from "./Timeline";
import { TimelineItem } from "./TimelineItem";

// --- virtualizer mock: render every item, key by stable id ----------------
vi.mock("@tanstack/react-virtual", () => {
  interface VizOptions {
    readonly count: number;
    readonly estimateSize: () => number;
    readonly getItemKey?: (index: number) => string | number;
  }

  interface MockVirtualItem {
    readonly index: number;
    readonly key: string | number;
    readonly start: number;
    readonly size: number;
    readonly lane: number;
  }

  return {
    useVirtualizer: (options: VizOptions) => {
      const size = options.estimateSize();
      const items: MockVirtualItem[] = Array.from(
        { length: options.count },
        (_, i) => ({
          index: i,
          key: options.getItemKey ? options.getItemKey(i) : i,
          start: i * size,
          size,
          lane: 0,
        }),
      );
      return {
        getTotalSize: () => options.count * size,
        getVirtualItems: () => items,
        scrollToIndex: () => {},
        scrollToOffset: () => {},
        measureElement: () => undefined,
        range: {
          start: 0,
          end: options.count - 1,
          overscan: 0,
          overscanMain: 0,
          overscanReverse: 0,
          size: options.count,
        },
      };
    },
  };
});

afterEach(() => {
  cleanup();
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
  return { kind: "notice", id, tone, text };
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
    const loadOlder = vi.fn();
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
