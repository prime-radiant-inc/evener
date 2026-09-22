// Screen-level tests for the removal paths the model tests cannot reach: how
// the browser reports a rejection the hub says already stood (never the
// generic failed write), when it refetches the list, and what a clone-litter
// rejection leaves on screen. Mirrors ProvidersScreen.recovery.test.tsx's
// mocking: every native edge the screen reaches is mocked here, and the
// stores are driven through the SDK's FakeClient.
import { act, type ReactTestRenderer } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import type { MarketplaceEntry } from "@evener/appwire-client";
import {
  ErrorMarketplaceRemoveApplied,
  WireError,
} from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { createPluginsStore } from "@evener/appwire-client/state/extensions";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { MarketplaceBrowser } from "./MarketplaceBrowser";
import { createPluginMutationGate } from "./pluginMutationGate";
import {
  alertRequests,
  nativeModuleMock,
  render,
  renderedText,
} from "./renderNative.testkit";

vi.mock("react-native", async () => ({
  ...(await import("./renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("react-native-safe-area-context", () => ({
  SafeAreaView: "SafeAreaView",
}));
// ConnectionStatus, embedded in the browser's sheets, imports the connection
// provider; this suite renders only the ready state, so the banner never
// calls the hook - but the module must load without the native expo graph
// the provider pulls in.
vi.mock("./ConnectionProvider", () => ({
  useConnection: () => ({
    state: "ready",
    error: null,
    retry: () => {},
    activeProfile: null,
  }),
}));

const ACME: MarketplaceEntry = {
  name: "acme",
  source: { kind: "github", repo: "acme/plugins" },
  lastUpdated: 1,
};

beforeEach(() => {
  alertRequests.length = 0;
});

// Mounts the browser against `reject` as the hub's answer to the removal.
// The list answers the mount read with acme and every later read as already
// removed, the way a hub that applied the removal would.
async function removalUnderTest(fake: FakeClient, reject: () => Error) {
  let listCalls = 0;
  fake.on("evener/marketplace/list", () => {
    listCalls += 1;
    return { marketplaces: listCalls === 1 ? [ACME] : [] };
  });
  fake.on("evener/marketplace/browse", () => ({ name: "acme", plugins: [] }));
  fake.on("evener/marketplace/remove", () => {
    throw reject();
  });
  const client = fake as unknown as ConversationClientLike;
  const tree = render(
    <MarketplaceBrowser
      client={client}
      connectionState="ready"
      hubName="Work hub"
      installed={createPluginsStore(client)}
      gate={createPluginMutationGate()}
      onOpenPlugin={() => {}}
    />,
  );
  await act(async () => {});
  const row = tree.root.findAllByProps({ accessibilityLabel: "Browse acme" })[0];
  if (!row) throw new Error("no acme row");
  await act(async () => {
    row.props.onPress();
  });
  const remove = tree.root.findAllByProps({
    accessibilityLabel: "Remove marketplace",
  })[0];
  const removePress = remove?.props.onPress;
  if (!removePress) throw new Error("no Remove marketplace action");
  act(() => removePress());
  const request = alertRequests.at(-1);
  const confirmPress = request?.buttons?.find((button) => button.text === "Remove")?.onPress;
  if (!confirmPress) throw new Error("no Remove confirm button");
  await act(async () => {
    confirmPress();
  });
  await act(async () => {});
  return { tree, listCalls: () => listCalls };
}

it("reports an applied removal neutrally and reconciles the list", async () => {
  const fake = new FakeClient("ready");
  const { tree, listCalls } = await removalUnderTest(fake, () =>
    new WireError("marketplace removed, but the updated list was unavailable", -32603, {
      evenerErrorInfo: ErrorMarketplaceRemoveApplied,
      appliedUnavailable: true,
    }),
  );
  const text = renderedText(tree);
  expect(text).not.toContain("Could not confirm the change");
  expect(text).not.toContain("clone cleanup failed");
  // The removal stood but the list read failed, so the browser refetched and
  // the already-removed answer retired the row.
  expect(listCalls()).toBe(2);
  expect(text).not.toContain("acme");
});

it("keeps the clone-litter warning and does not refetch the reconciled list", async () => {
  const fake = new FakeClient("ready");
  const { tree, listCalls } = await removalUnderTest(fake, () =>
    new WireError("clone could not be removed", -32603, {
      evenerErrorInfo: "marketplaceUnregisteredCloneRemains",
      applied: { marketplaces: [] },
    }),
  );
  const text = renderedText(tree);
  expect(text).toContain(
    "Marketplace removed; clone cleanup failed. Remove the leftover clone files manually.",
  );
  // The store published the rejection's authoritative applied list, so the
  // live read asks for nothing: the mount's list call is the only one.
  expect(listCalls()).toBe(1);
  expect(text).not.toContain("acme");
});

it("keeps the retryable write-failed copy for an ordinary failure", async () => {
  const fake = new FakeClient("ready");
  const { tree, listCalls } = await removalUnderTest(fake, () =>
    new Error("remove failed"),
  );
  expect(renderedText(tree)).toContain("Could not confirm the change");
  expect(listCalls()).toBe(1);
});
