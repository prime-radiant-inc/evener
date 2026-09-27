// Calm copy (spec principle 2): QueueSheet says what happened and never asks
// the person to reconnect, since the app reconnects on its own.
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import type { MobileConversation } from "./projectedRows";
import { QueueSheet } from "./QueueSheet";
import type { QueueConversationService } from "../../mobile/src/services/conversation";
import { render, renderedText } from "./renderNative.testkit";

vi.mock("react-native", async () => ({
  ...(await import("./renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));

// A running turn with one queued message. Steer is the only capability the
// sheet's controls read; it adds the per-message steering button.
function queueSheet(ready: boolean, refresh: () => Promise<void>) {
  const conversation = {
    status: { type: "active" },
    capabilities: { steer: true },
    queue: { revision: 3, depth: 1, ids: ["q1"], texts: ["hello"] },
    instanceId: "inst-1",
  } as unknown as MobileConversation;
  const service = {
    cancelQueued: vi.fn().mockResolvedValue(undefined),
  } as unknown as QueueConversationService;
  return (
    <QueueSheet
      conversation={conversation}
      latest={() => conversation}
      service={service}
      ready={ready}
      refresh={refresh}
      close={() => {}}
    />
  );
}

function findButton(tree: ReturnType<typeof render>, label: string) {
  return tree.root.find(
    (node) => node.props.accessibilityRole === "button" && node.props.accessibilityLabel === label,
  );
}

// The sheet shows `sentence` and nothing that asks the person to reconnect.
function expectCalmCopy(tree: ReturnType<typeof render>, sentence: string) {
  const text = renderedText(tree);
  expect(text).toContain(sentence);
  expect(text.toLowerCase()).not.toContain("reconnect");
}

it("says the queue can't change while disconnected, and never asks to reconnect", () => {
  expectCalmCopy(render(queueSheet(false, vi.fn())), "Disconnected. This queue can't change right now.");
});

it("says what happened, without a reconnect directive, when an action's own refresh fails", async () => {
  const tree = render(queueSheet(true, vi.fn().mockRejectedValue(new Error("offline"))));

  await act(async () => {
    findButton(tree, "Cancel queued message 1").props.onPress();
  });
  expectCalmCopy(tree, "The action was acknowledged, but the queue could not be refreshed.");

  // The visible retry button fails the same way: pressing it again states the
  // fact and stops, instead of telling the person to reconnect.
  await act(async () => {
    findButton(tree, "Refresh queue").props.onPress();
  });
  expectCalmCopy(tree, "Could not refresh.");
});
