import type { Thread } from "@evener/appwire-client";
import { makeTranscriptDisplayConfig } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { ClientProvider } from "../../shell/clientContext";
import { conversationPaneLifetime } from "../../shell/paneLifetime";
import { type OpenPaneRecord, resetWorkspaceStoreForTests, workspaceStore } from "../../shell/workspace";
import { connectionStore } from "../../stores/connection";
import { activitySummary, answerActivityRead } from "../../stores/sessionActivityTestUtils";
import { resetThreadsStoreForTests } from "../../stores/threads";
import { transcriptDisplayStore } from "../../stores/transcriptDisplay";
import { makeTranscriptPreviewModel } from "../../transcriptDisplay/previewFixture";
import { resetDisclosureStoreForTests } from "../../widgets/disclosure/disclosureStore";
import {
  captureTranscriptView,
  resetTranscriptViewRegistryForTests,
} from "../session/transcript/flow/transcriptViewRegistry";
import { retainedTranscriptReadView } from "../session/transcript/transcriptReadView";
import { resetTranscriptPagingForTests } from "../session/transcript/useTranscript";
import { ReadOnlyThreadContent } from "./ReadOnlyThreadContent";

let height: PropertyDescriptor | undefined;
beforeEach(() => {
  connectionStore.setState({ state: "idle", client: null, serverInfo: undefined });
  resetThreadsStoreForTests();
  resetTranscriptPagingForTests();
  resetWorkspaceStoreForTests();
  resetDisclosureStoreForTests();
  resetTranscriptViewRegistryForTests();
  transcriptDisplayStore.setState({ viewport: "desktop" });
  transcriptDisplayStore
    .getState()
    .setLocal("desktop", makeTranscriptDisplayConfig({ kind: "preset", level: "tools" }));
  height = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", { configurable: true, value: 500 });
});
afterEach(() => {
  cleanup();
  resetWorkspaceStoreForTests();
  if (height) Object.defineProperty(HTMLElement.prototype, "offsetHeight", height);
  else Reflect.deleteProperty(HTMLElement.prototype, "offsetHeight");
});

function fixture(long = false) {
  const fake = new FakeClient("ready");
  fake.on("evener/thread/activity/read", answerActivityRead);
  fake.on("thread/read", ({ ref }) => {
    if (ref === undefined) throw new Error("thread/read requires the requested ref");
    return {
      thread: {
        id: "thread-shared",
        sessionId: "session-shared",
        preview: "test",
        ephemeral: false,
        modelProvider: "scripted",
        createdAt: 1000,
        updatedAt: 1000,
        status: { type: "idle" },
        cwd: "/tmp/project",
        cliVersion: "1.0.0",
        source: "evener",
        name: "Shared thread",
        evener: { ref, capabilities: makeTranscriptPreviewModel().capabilities, queue: { revision: 0 } },
        turns: long
          ? [
              {
                id: "long-turn",
                status: "completed",
                itemsView: "full",
                items: [
                  {
                    id: "long-message",
                    turnId: "long-turn",
                    type: "agentMessage",
                    text: "Measured readonly reply",
                    status: "completed",
                  },
                ],
              },
            ]
          : [
              {
                id: "turn",
                status: "completed",
                itemsView: "full",
                items: [
                  {
                    id: "message",
                    turnId: "turn",
                    type: "userMessage",
                    text: "real shared content",
                    status: "completed",
                  },
                  {
                    id: "call-a",
                    turnId: "turn",
                    type: "commandExecution",
                    toolName: "read_file",
                    description: "Read A",
                    output: "A",
                    status: "completed",
                  },
                  {
                    id: "call-b",
                    turnId: "turn",
                    type: "commandExecution",
                    toolName: "read_file",
                    description: "Read B",
                    output: "B",
                    status: "completed",
                  },
                  {
                    id: "call-c",
                    turnId: "turn",
                    type: "commandExecution",
                    toolName: "read_file",
                    description: "Read C",
                    output: "C",
                    status: "completed",
                  },
                ],
              },
            ],
      } satisfies Thread,
    };
  });
  connectionStore.getState().connect(fake);
  const a: OpenPaneRecord = { id: "content-a", type: "transcript", params: { ref: "shared" }, slot: "main" };
  const b: OpenPaneRecord = { id: "content-b", type: "transcript", params: { ref: "shared" }, slot: "secondary" };
  workspaceStore.setState({ panes: [a, b] });
  return {
    fake,
    a: retainedTranscriptReadView(conversationPaneLifetime(a), "shared", "cascade"),
    b: retainedTranscriptReadView(conversationPaneLifetime(b), "shared", "cascade"),
  };
}

test("content uses the shared transcript engine without pane chrome or conversation controls", async () => {
  const { fake, a } = fixture();
  render(
    <ClientProvider client={fake}>
      <ReadOnlyThreadContent ref="shared" view={a} />
    </ClientProvider>,
  );
  expect(await screen.findByText("real shared content")).toBeTruthy();
  expect(screen.getByTestId("transcript-virtual-list")).toBeTruthy();
  expect(screen.queryByRole("heading", { name: "Shared thread" })).toBeNull();
  expect(screen.queryByRole("textbox")).toBeNull();
  expect(screen.queryByTestId("pane-footer")).toBeNull();
  expect(fake.calls.filter((call) => /send|resume|steer|interrupt/.test(call.method))).toHaveLength(0);
});

test("two same-ref columns keep independent disclosures and one can unmount without losing the other", async () => {
  const { fake, a, b } = fixture();
  const first = render(
    <ClientProvider client={fake}>
      <ReadOnlyThreadContent ref="shared" view={a} />
    </ClientProvider>,
  );
  const second = render(
    <ClientProvider client={fake}>
      <ReadOnlyThreadContent ref="shared" view={b} />
    </ClientProvider>,
  );
  await within(first.container).findByText("real shared content");
  await within(second.container).findByText("real shared content");
  const firstRun = within(first.container).getByTestId("tool-run") as HTMLDetailsElement;
  const secondRun = within(second.container).getByTestId("tool-run") as HTMLDetailsElement;
  expect(firstRun.open).toBe(false);
  expect(secondRun.open).toBe(false);
  const summary = firstRun.querySelector("summary");
  if (!summary) throw new Error("tool run has no summary");
  fireEvent.click(summary);
  expect(firstRun.open).toBe(true);
  expect(secondRun.open).toBe(false);
  expect(captureTranscriptView(a.id)).toBeDefined();
  first.unmount();
  expect(a.alive).toBe(true);
  expect(a.getCapture()).toBeDefined();
  expect(captureTranscriptView(a.id)).toBeUndefined();
  expect(captureTranscriptView(b.id)).toBeDefined();
  expect(within(second.container).getByText("real shared content")).toBeTruthy();
  const returned = render(
    <ClientProvider client={fake}>
      <ReadOnlyThreadContent ref="shared" view={a} />
    </ClientProvider>,
  );
  await within(returned.container).findByText("real shared content");
  expect((within(returned.container).getByTestId("tool-run") as HTMLDetailsElement).open).toBe(true);
  act(() => a.dispose());
});

// Real reader, transport store, registry and virtualizer. Only browser layout
// and resize delivery are supplied, because jsdom has neither.
describe("measured readonly return", () => {
  const descriptors = new Map<string, PropertyDescriptor | undefined>();
  const offsets = new WeakMap<HTMLElement, number>();
  const observers = new Set<GeometryObserver>();
  class GeometryObserver {
    readonly elements = new Set<Element>();
    constructor(readonly callback: ResizeObserverCallback) {
      observers.add(this);
    }
    observe(element: Element) {
      this.elements.add(element);
    }
    unobserve(element: Element) {
      this.elements.delete(element);
    }
    disconnect() {
      this.elements.clear();
      observers.delete(this);
    }
  }
  function isPort(element: HTMLElement) {
    return element.parentElement?.dataset.testid === "transcript-virtual-list";
  }
  function rect(top: number, size: number): DOMRect {
    return { x: 0, y: top, top, bottom: top + size, left: 0, right: 390, width: 390, height: size, toJSON: () => ({}) };
  }
  beforeEach(() => {
    for (const property of [
      "scrollTop",
      "offsetHeight",
      "offsetWidth",
      "clientWidth",
      "clientHeight",
      "scrollHeight",
      "getBoundingClientRect",
      "scrollTo",
    ]) {
      descriptors.set(property, Object.getOwnPropertyDescriptor(HTMLElement.prototype, property));
    }
    Object.defineProperties(HTMLElement.prototype, {
      scrollTop: {
        configurable: true,
        get() {
          return offsets.get(this) ?? 0;
        },
        set(top: number) {
          // Native scroll ports clamp assignments, unlike jsdom's property.
          offsets.set(this, Math.max(0, Math.min(top, this.scrollHeight - this.clientHeight)));
        },
      },
      offsetHeight: {
        configurable: true,
        get() {
          return this.hasAttribute("data-index") ? 6206 : 751;
        },
      },
      offsetWidth: {
        configurable: true,
        get() {
          return 390;
        },
      },
      clientWidth: {
        configurable: true,
        get() {
          return isPort(this) ? 390 : 0;
        },
      },
      clientHeight: {
        configurable: true,
        get() {
          return isPort(this) ? 751 : 0;
        },
      },
      scrollHeight: {
        configurable: true,
        get() {
          return isPort(this) ? Math.max(751, Number.parseFloat(this.firstElementChild?.style.height ?? "0")) : 0;
        },
      },
      getBoundingClientRect: {
        configurable: true,
        value: function (this: HTMLElement) {
          const port = this.closest('[data-testid="transcript-virtual-list"]')?.firstElementChild as HTMLElement | null;
          return this.hasAttribute("data-view-anchor-id")
            ? rect(-(port?.scrollTop ?? 0), 6206)
            : rect(
                0,
                isPort(this)
                  ? 751
                  : this.parentElement && isPort(this.parentElement)
                    ? Number.parseFloat(this.style.height)
                    : 6206,
              );
        },
      },
      scrollTo: {
        configurable: true,
        value: function (this: HTMLElement, options: ScrollToOptions) {
          this.scrollTop = Math.max(0, Math.min(options.top ?? 0, this.scrollHeight - this.clientHeight));
        },
      },
    });
    vi.stubGlobal("ResizeObserver", GeometryObserver);
  });
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    for (const [property, descriptor] of descriptors) {
      if (descriptor) Object.defineProperty(HTMLElement.prototype, property, descriptor);
      else Reflect.deleteProperty(HTMLElement.prototype, property);
    }
    descriptors.clear();
    observers.clear();
  });
  function resized() {
    act(() => {
      for (const observer of [...observers]) {
        const entries = [...observer.elements].map((target) => {
          const size = { blockSize: (target as HTMLElement).offsetHeight, inlineSize: 390 };
          return {
            target,
            borderBoxSize: [size],
            contentBoxSize: [size],
            devicePixelContentBoxSize: [size],
            contentRect: target.getBoundingClientRect(),
          } satisfies ResizeObserverEntry;
        });
        observer.callback(entries, observer as unknown as ResizeObserver);
      }
    });
  }
  test.each([1255, 4255])(
    "returns to the measured %ipx away-reading capture without following the end",
    async (top) => {
      const { fake, a } = fixture(true);
      const reader = () => (
        <ClientProvider client={fake}>
          <ReadOnlyThreadContent ref="shared" view={a} />
        </ClientProvider>
      );
      const first = render(reader());
      await screen.findByText("Measured readonly reply");
      resized();
      const port = screen.getByTestId("transcript-virtual-list").firstElementChild as HTMLElement;
      expect(port.scrollTop).toBe(5455);
      act(() => {
        fireEvent.scroll(port);
        fireEvent.wheel(port, { deltaY: -1200 });
        port.scrollTop = top;
        fireEvent.scroll(port);
      });
      expect(captureTranscriptView(a.id)).toMatchObject({
        anchorId: "long-message",
        anchorOffset: -top,
        followingBottom: false,
        readingPoint: { entryHeight: 6206, viewportWidth: 390, viewportHeight: 751 },
      });
      // Filename Open captures the registered view before the source unmounts.
      const captured = captureTranscriptView(a.id);
      if (!captured) throw new Error("measured source capture missing");
      act(() => a.setCapture(captured));
      first.unmount();
      expect(a.getCapture()).toMatchObject({ anchorOffset: -top, followingBottom: false });
      render(reader());
      await screen.findByText("Measured readonly reply");
      resized();
      const returned = screen.getByTestId("transcript-virtual-list").firstElementChild as HTMLElement;
      expect(returned.scrollTop).toBe(top);
      resized();
      act(() => fireEvent.scroll(returned));
      expect(returned.scrollTop).toBe(top);
      expect(returned.scrollTop).toBeGreaterThan(0);
      expect(returned.scrollTop).toBeLessThan(returned.scrollHeight - returned.clientHeight);
      expect(screen.getByText("Measured readonly reply")).toBeTruthy();
      expect(captureTranscriptView(a.id)).toMatchObject({
        anchorId: "long-message",
        anchorOffset: -top,
        followingBottom: false,
      });
    },
  );
});
