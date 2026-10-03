import type { Thread } from "@evener/appwire-client";
import { makeTranscriptDisplayConfig } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, test } from "vitest";
import { ClientProvider } from "../../shell/clientContext";
import { conversationPaneLifetime } from "../../shell/paneLifetime";
import { type OpenPaneRecord, resetWorkspaceStoreForTests, workspaceStore } from "../../shell/workspace";
import { connectionStore } from "../../stores/connection";
import { activitySummary } from "../../stores/sessionActivityTestUtils";
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

function fixture() {
  const fake = new FakeClient("ready");
  fake.on("evener/thread/activity/read", ({ ref }) => activitySummary(ref));
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
        turns: [
          {
            id: "turn",
            status: "completed",
            itemsView: "full",
            items: [
              { id: "message", turnId: "turn", type: "userMessage", text: "real shared content", status: "completed" },
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
