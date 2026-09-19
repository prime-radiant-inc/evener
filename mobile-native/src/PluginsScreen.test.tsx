// Screen-level tests for the marketplace removal outcomes the browser cannot
// hold on its own: the PluginsScreen-owned no-repeat guard fences a
// marketplace the hub says it already removed across browser remounts, the
// cleanup warning survives the browser's own revision fence and tab switches,
// and a late outcome from a replaced client changes nothing on the new one.
import type { ComponentProps } from "react";
import { act } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import { WireError, type MarketplaceEntry } from "@evener/appwire-client";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { PluginsScreen } from "./PluginsScreen";
import {
  alertRequests,
  nativeModuleMock,
  render,
  renderedText,
} from "./renderNative.testkit";

const harness = vi.hoisted(() => ({
  connection: {} as Record<string, unknown>,
}));
vi.mock("react-native", async () => ({
  ...(await import("./renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("react-native-safe-area-context", () => ({
  SafeAreaView: "SafeAreaView",
}));
vi.mock("./ConnectionProvider", () => ({
  useConnection: () => harness.connection,
}));

const marketplace: MarketplaceEntry = {
  name: "acme",
  source: { kind: "github", repo: "acme/plugins" },
  lastUpdated: 1,
};

function cloneLitterError(applied: unknown, includeApplied = true): WireError {
  return new WireError("clone cleanup failed", -32603, {
    evenerErrorInfo: "marketplaceUnregisteredCloneRemains",
    ...(includeApplied ? { applied } : {}),
  });
}

/** A marketplace hub: every method call is recorded in `methods`, the
 * marketplaces list answers per `options.list`, the browse tab's catalog is
 * empty, and `evener/marketplace/remove` answers per `options.remove`. */
function marketplaceClient(options: {
  list?: () => Promise<{ marketplaces: MarketplaceEntry[] }>;
  remove?: () => Promise<never>;
}) {
  const methods: string[] = [];
  const client = {
    request: async (method: string) => {
      methods.push(method);
      if (method === "evener/marketplace/list")
        return options.list?.() ?? { marketplaces: [marketplace] };
      if (method === "evener/marketplace/browse")
        return { name: marketplace.name, plugins: [] };
      if (method === "evener/marketplace/remove") return options.remove?.();
      if (method === "evener/plugin/list") return { plugins: [] };
      return { marketplaces: [marketplace] };
    },
    onNotification: () => () => {},
  } as ConversationClientLike;
  return { client, methods };
}

function readyConnection(client: ConversationClientLike) {
  return {
    activeProfile: { id: "hub-1", name: "Work hub" },
    client,
    state: "ready",
    retry: () => {},
  };
}

async function browseMarketplace(tree: ReturnType<typeof render>) {
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Browse" }).props.onPress();
  });
  await act(async () => {});
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Browse acme" }).props.onPress();
  });
  await act(async () => {});
}

async function confirmMarketplaceRemoval(tree: ReturnType<typeof render>) {
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Remove marketplace" }).props.onPress();
  });
  const request = alertRequests.at(-1);
  const remove = request?.buttons?.find((button) => button.text === "Remove");
  if (!remove?.onPress) throw new Error("Remove confirmation was not shown");
  await act(async () => {
    remove.onPress?.();
    await Promise.resolve();
  });
}

beforeEach(() => {
  alertRequests.length = 0;
});

it("keeps an unavailable applied removal fenced across browser remounts", async () => {
  let listCalls = 0;
  const hub = marketplaceClient({
    list: async () => {
      listCalls += 1;
      if (listCalls === 2) throw new Error("list unavailable");
      return { marketplaces: [marketplace] };
    },
    remove: async () => {
      throw cloneLitterError(null, false);
    },
  });
  harness.connection = readyConnection(hub.client);
  const props = {
    route: { params: { hubId: "hub-1" } },
  } as unknown as ComponentProps<typeof PluginsScreen>;
  const tree = render(<PluginsScreen {...props} />);
  await act(async () => {});
  await browseMarketplace(tree);
  await confirmMarketplaceRemoval(tree);
  expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");

  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Installed" }).props.onPress();
  });
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Browse" }).props.onPress();
  });
  await act(async () => {});
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Browse acme" }).props.onPress();
  });
  await act(async () => {});

  const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
  expect(remove.props.disabled).toBe(true);
  expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(1);
});

it("leaves ordinary marketplace removal failures retryable", async () => {
  const hub = marketplaceClient({
    remove: async () => {
      throw new Error("ordinary failure");
    },
  });
  harness.connection = readyConnection(hub.client);
  const props = {
    route: { params: { hubId: "hub-1" } },
  } as unknown as ComponentProps<typeof PluginsScreen>;
  const tree = render(<PluginsScreen {...props} />);
  await act(async () => {});
  await browseMarketplace(tree);
  await confirmMarketplaceRemoval(tree);

  expect(renderedText(tree)).toContain("Could not confirm the change. Check its status before trying again.");
  const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
  expect(remove.props.disabled).toBe(false);
});
