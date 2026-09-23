// The native counterpart to the web pane's timestamps rule (TasksPanel.tsx:
// the completed line renders for done rows only): the store stamps every
// terminal transition (done AND cancelled), so a cancelled row now carries a
// settle stamp on the wire - and must not read as Completed. The guard is the
// sheet's one behavior change from the stamping; the store-side contract is
// pinned in agent/task/task_store_test.go.
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import type { InstanceListResponse } from "@evener/appwire-client";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { TasksSheet } from "./TasksSheet";
import { nativeModuleMock, render, renderedText, scriptedClient } from "./renderNative.testkit";

vi.mock("react-native", async () => ({
  ...(await import("./renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("react-native-safe-area-context", () => ({
  SafeAreaProvider: "SafeAreaProvider",
  SafeAreaView: "SafeAreaView",
}));
vi.mock("./MarkdownResponse", () => ({ MarkdownResponse: () => null }));

const tasks = {
  data: [
    {
      id: 1,
      type: "implement",
      description: "finish it",
      prompt: "",
      status: "done",
      created_at: "2026-09-23T13:40:00Z",
      updated_at: "2026-09-23T14:59:00Z",
      completed_at: "2026-09-23T14:59:00Z",
    },
    {
      id: 2,
      type: "implement",
      description: "drop it",
      prompt: "",
      status: "cancelled",
      created_at: "2026-09-23T13:40:00Z",
      updated_at: "2026-09-23T14:00:00Z",
      completed_at: "2026-09-23T14:00:00Z",
    },
  ],
} as unknown as InstanceListResponse;

it("renders the Completed line for done rows only, never a stamped cancellation", async () => {
  const { client } = scriptedClient(tasks);
  const tree = render(
    <TasksSheet
      client={client as ConversationClientLike}
      sessionRef="local:s"
      threadId="s"
      hasTasks
      connected
      hubName="Work hub"
      close={() => {}}
    />,
  );
  // Let the initial read (store.watch's own refresh) land.
  await act(async () => {});

  // Both rows are terminal, so both sit behind the settled group's toggle.
  const settledToggle = tree.root
    .findAll((node) => typeof node.props.onPress === "function")
    .find((node) => String(node.props.children).includes("Done · settled"));
  await act(async () => {
    settledToggle?.props.onPress();
  });

  // The timestamps - the Completed line among them - render inside the
  // row's own expanded body, so open both rows.
  const rowButton = (label: string) =>
    tree.root
      .findAll((node) => node.props.accessibilityRole === "button")
      .find((node) => node.props.accessibilityLabel === label);
  await act(async () => {
    rowButton("Done: finish it")?.props.onPress();
  });
  await act(async () => {
    rowButton("Cancelled: drop it")?.props.onPress();
  });

  const text = renderedText(tree);
  expect(text).toContain("finish it");
  expect(text).toContain("drop it");
  // Exactly one Completed line: the done row's. A cancelled row carrying the
  // new settle stamp must not claim it.
  expect(text.match(/Completed/g)?.length).toBe(1);
});
