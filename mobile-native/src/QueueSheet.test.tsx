// Calm copy (spec principle 2): nothing here asks the person to reconnect or
// refresh, since the app does both on its own. These pin the factual copy
// QueueSheet shows in place of the old directive tails, so a regression back
// to "Reconnect..." is caught the way #2628 caught NATIVE_MUTATION_HOST_UNAVAILABLE's.
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import type { MobileConversation } from "./projectedRows";
import { QueueSheet } from "./QueueSheet";
import type { QueueConversationService } from "../../mobile/src/services/conversation";
import { nativeModuleMock, render, renderedText } from "./renderNative.testkit";

vi.mock("react-native", async () => ({
  ...(await import("./renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));

const CAPABILITIES = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: false,
  forkFromTurn: false,
  shutdown: true,
  changeModel: true,
  changeVisionModel: true,
  sharedNotes: false,
  queue: true,
  goal: true,
  rename: true,
};

function conversation(): MobileConversation {
  return {
    status: { type: "active" },
    capabilities: CAPABILITIES,
    queue: { revision: 3, depth: 1, ids: ["q1"], texts: ["hello"] },
    instanceId: "inst-1",
  } as unknown as MobileConversation;
}

function service(over: Partial<QueueConversationService> = {}): QueueConversationService {
  return {
    cancelQueued: vi.fn().mockResolvedValue(undefined),
    promoteQueuedAsSteer: vi.fn().mockResolvedValue(undefined),
    drainAsSteer: vi.fn().mockResolvedValue(undefined),
    ...over,
  } as unknown as QueueConversationService;
}

function findButton(tree: ReturnType<typeof render>, label: string) {
  return tree.root.find(
    (node) => node.props.accessibilityRole === "button" && node.props.accessibilityLabel === label,
  );
}

it("says the queue can't change while disconnected, and never asks to reconnect", () => {
  const live = conversation();
  const tree = render(
    <QueueSheet
      conversation={live}
      latest={() => live}
      service={service()}
      ready={false}
      refresh={vi.fn()}
      close={() => {}}
    />,
  );
  expect(renderedText(tree)).toContain("Disconnected");
  expect(renderedText(tree).toLowerCase()).not.toContain("reconnect");
});

it("says what happened, without a reconnect directive, when an action's own refresh fails", async () => {
  const live = conversation();
  const refresh = vi.fn().mockRejectedValue(new Error("offline"));
  const tree = render(
    <QueueSheet
      conversation={live}
      latest={() => live}
      service={service()}
      ready={true}
      refresh={refresh}
      close={() => {}}
    />,
  );

  const cancel = findButton(tree, "Cancel queued message 1");
  await act(async () => {
    cancel.props.onPress();
  });
  await act(async () => {});
  await act(async () => {});

  expect(renderedText(tree)).toContain("The action was acknowledged, but the queue could not be refreshed.");
  expect(renderedText(tree).toLowerCase()).not.toContain("reconnect");

  // The visible retry button fails the same way: pressing it again states the
  // fact and stops, instead of telling the person to reconnect.
  const retry = findButton(tree, "Refresh queue");
  await act(async () => {
    retry.props.onPress();
  });
  await act(async () => {});
  await act(async () => {});

  expect(renderedText(tree)).toContain("Could not refresh.");
  expect(renderedText(tree).toLowerCase()).not.toContain("reconnect");
});
