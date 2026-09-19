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
  remove?: () => Promise<unknown>;
  add?: () => Promise<{ marketplaces: MarketplaceEntry[] }>;
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
      if (method === "evener/marketplace/add")
        return options.add?.() ?? { marketplaces: [marketplace] };
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

it("records an applied removal after selection changes while the request is pending", async () => {
  let releaseRemoval!: () => void;
  const pendingRemoval = new Promise<never>((_resolve, reject) => {
    releaseRemoval = () => reject(cloneLitterError(null, false));
  });
  const hub = marketplaceClient({ remove: () => pendingRemoval });
  harness.connection = readyConnection(hub.client);
  const props = {
    route: { params: { hubId: "hub-1" } },
  } as unknown as ComponentProps<typeof PluginsScreen>;
  const tree = render(<PluginsScreen {...props} />);
  await act(async () => {});
  await browseMarketplace(tree);
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Remove marketplace" }).props.onPress();
  });
  const request = alertRequests.at(-1);
  const remove = request?.buttons?.find((button) => button.text === "Remove");
  if (!remove?.onPress) throw new Error("Remove confirmation was not shown");
  await act(async () => remove.onPress?.());
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "All marketplaces" }).props.onPress();
  });
  releaseRemoval();
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });

  expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Browse acme" }).props.onPress();
  });
  await act(async () => {});
  const removeButton = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
  expect(removeButton.props.disabled).toBe(true);
  expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(1);
});

it("ignores an applied removal result from a replaced client", async () => {
  let releaseOld!: () => void;
  const oldRemoval = new Promise<never>((_resolve, reject) => {
    releaseOld = () => reject(cloneLitterError({ marketplaces: [] }));
  });
  const oldHub = marketplaceClient({ remove: () => oldRemoval });
  const newHub = marketplaceClient({
    list: async () => ({ marketplaces: [] }),
  });
  harness.connection = readyConnection(oldHub.client);
  const props = {
    route: { params: { hubId: "hub-1" } },
  } as unknown as ComponentProps<typeof PluginsScreen>;
  const tree = render(<PluginsScreen {...props} />);
  await act(async () => {});
  await browseMarketplace(tree);
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Remove marketplace" }).props.onPress();
  });
  const request = alertRequests.at(-1);
  const remove = request?.buttons?.find((button) => button.text === "Remove");
  if (!remove?.onPress) throw new Error("Remove confirmation was not shown");
  await act(async () => remove.onPress?.());

  harness.connection = readyConnection(newHub.client);
  await act(async () => tree.update(<PluginsScreen {...props} />));
  await act(async () => {});
  releaseOld();
  await act(async () => {
    await Promise.resolve();
  });

  expect(renderedText(tree)).not.toContain("Marketplace removed; clone cleanup failed");
  expect(newHub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(0);
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
  await confirmMarketplaceRemoval(tree);
  expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(2);
});

it("keeps the guard and warning across a same-client connection flap", async () => {
  const hub = marketplaceClient({
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

  // A passive flap moves only `state`: the ready-only child unmounts for the
  // reconnecting render and remounts for the ready one, with the SAME client
  // throughout - the guard and warning have to outlive that remount.
  harness.connection = { ...readyConnection(hub.client), state: "reconnecting" };
  await act(async () => {
    tree.update(<PluginsScreen {...props} />);
  });
  harness.connection = readyConnection(hub.client);
  await act(async () => {
    tree.update(<PluginsScreen {...props} />);
  });
  await act(async () => {});

  expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");
  await browseMarketplace(tree);
  const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
  expect(remove.props.disabled).toBe(true);
  expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(1);
});

it("records a pending removal settling after a remount whose list read failed", async () => {
  let releaseRemoval!: () => void;
  const pendingRemoval = new Promise<never>((_resolve, reject) => {
    releaseRemoval = () => reject(cloneLitterError(null, false));
  });
  let listCalls = 0;
  const hub = marketplaceClient({
    list: async () => {
      listCalls += 1;
      if (listCalls === 2) throw new Error("list unavailable");
      return { marketplaces: [marketplace] };
    },
    remove: () => pendingRemoval,
  });
  harness.connection = readyConnection(hub.client);
  const props = {
    route: { params: { hubId: "hub-1" } },
  } as unknown as ComponentProps<typeof PluginsScreen>;
  const tree = render(<PluginsScreen {...props} />);
  await act(async () => {});
  await browseMarketplace(tree);
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Remove marketplace" }).props.onPress();
  });
  const request = alertRequests.at(-1);
  const confirm = request?.buttons?.find((button) => button.text === "Remove");
  if (!confirm?.onPress) throw new Error("Remove confirmation was not shown");
  await act(async () => confirm.onPress?.());

  // Leave the browse tab and come back while the removal is still pending:
  // the remounted browser's own list read fails, so no authoritative list
  // has been seen when the outcome settles.
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Installed" }).props.onPress();
  });
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Browse" }).props.onPress();
  });
  await act(async () => {});
  releaseRemoval();
  await act(async () => {});

  // The guard and warning still land - the screen owns them, not the
  // browser that asked - and the failed read keeps its own copy until a
  // fresh read reconciles: the flow that started on the unmounted
  // browser's store refetches nothing, because that store's publishes are
  // dropped with it.
  expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");
  expect(renderedText(tree)).toContain("Could not load marketplaces. Try again when connected.");
  expect(hub.methods.filter((method) => method === "evener/marketplace/list")).toHaveLength(2);

  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Retry marketplaces" }).props.onPress();
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

it("leaves a re-added marketplace removable after an applied removal reconciles the list", async () => {
  let releaseRemoval!: () => void;
  const firstRemoval = new Promise<never>((_resolve, reject) => {
    releaseRemoval = () => reject(cloneLitterError(null, false));
  });
  let removals = 0;
  let listCalls = 0;
  const hub = marketplaceClient({
    list: async () => {
      listCalls += 1;
      // The hub answers the mount read with acme; the browser remount's
      // read finds the removal the hub already applied - its list omits
      // acme.
      return { marketplaces: listCalls === 1 ? [marketplace] : [] };
    },
    remove: () => {
      removals += 1;
      return removals === 1 ? firstRemoval : Promise.resolve({ marketplaces: [] });
    },
  });
  harness.connection = readyConnection(hub.client);
  const props = {
    route: { params: { hubId: "hub-1" } },
  } as unknown as ComponentProps<typeof PluginsScreen>;
  const tree = render(<PluginsScreen {...props} />);
  await act(async () => {});
  await browseMarketplace(tree);
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Remove marketplace" }).props.onPress();
  });
  const request = alertRequests.at(-1);
  const confirm = request?.buttons?.find((button) => button.text === "Remove");
  if (!confirm?.onPress) throw new Error("Remove confirmation was not shown");
  await act(async () => confirm.onPress?.());

  // Leave the browse tab and come back while the removal is still pending:
  // the remounted browser reads the reconciled list the hub now publishes -
  // acme is gone from it - BEFORE the outcome records with the screen's
  // guard. A record that ignores the list it arrives into fences acme
  // against a re-add forever.
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Installed" }).props.onPress();
  });
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Browse" }).props.onPress();
  });
  await act(async () => {});
  releaseRemoval();
  await act(async () => {});
  expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");

  // Re-add acme through the browser's own modal.
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Add marketplace" }).props.onPress();
  });
  await act(async () => {
    tree.root
      .findByProps({ accessibilityLabel: "Marketplace source" })
      .props.onChangeText("https://example.test/plugins.git");
  });
  const adds = tree.root.findAllByProps({ accessibilityLabel: "Add marketplace" });
  const submit = adds.at(-1);
  if (!submit) throw new Error("Add marketplace submit was not rendered");
  await act(async () => {
    submit.props.onPress();
    await Promise.resolve();
  });
  await act(async () => {});

  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Browse acme" }).props.onPress();
  });
  await act(async () => {});
  const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
  expect(remove.props.disabled).toBe(false);
  await confirmMarketplaceRemoval(tree);
  expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(2);
});

it("clears the fence when a same-name re-add registers while reconciliation reads keep failing", async () => {
  let releaseRemoval!: () => void;
  const firstRemoval = new Promise<never>((_resolve, reject) => {
    releaseRemoval = () => reject(cloneLitterError(null, false));
  });
  let removals = 0;
  let listCalls = 0;
  const hub = marketplaceClient({
    list: async () => {
      listCalls += 1;
      // The hub answers the mount read with acme; every reconciliation read
      // after the applied removal fails, so no list ever shows acme gone.
      if (listCalls === 2) throw new Error("list unavailable");
      return { marketplaces: [marketplace] };
    },
    remove: () => {
      removals += 1;
      return removals === 1 ? firstRemoval : Promise.resolve({ marketplaces: [] });
    },
    add: async () => ({ marketplaces: [{ ...marketplace, lastUpdated: 2 }] }),
  });
  harness.connection = readyConnection(hub.client);
  const props = {
    route: { params: { hubId: "hub-1" } },
  } as unknown as ComponentProps<typeof PluginsScreen>;
  const tree = render(<PluginsScreen {...props} />);
  await act(async () => {});
  await browseMarketplace(tree);
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Remove marketplace" }).props.onPress();
  });
  const request = alertRequests.at(-1);
  const confirm = request?.buttons?.find((button) => button.text === "Remove");
  if (!confirm?.onPress) throw new Error("Remove confirmation was not shown");
  await act(async () => confirm.onPress?.());
  releaseRemoval();
  await act(async () => {});
  expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");

  // Re-add acme with no intervening list ever showing it gone: the add is a
  // NEW registration the hub accepted, so the stale removal's fence has to
  // clear for it.
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "All marketplaces" }).props.onPress();
  });
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Add marketplace" }).props.onPress();
  });
  await act(async () => {
    tree.root
      .findByProps({ accessibilityLabel: "Marketplace source" })
      .props.onChangeText("https://example.test/plugins.git");
  });
  const adds = tree.root.findAllByProps({ accessibilityLabel: "Add marketplace" });
  const submit = adds.at(-1);
  if (!submit) throw new Error("Add marketplace submit was not rendered");
  await act(async () => {
    submit.props.onPress();
    await Promise.resolve();
  });
  await act(async () => {});

  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Browse acme" }).props.onPress();
  });
  await act(async () => {});
  const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
  expect(remove.props.disabled).toBe(false);
  await confirmMarketplaceRemoval(tree);
  expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(2);
});

it("clears the fence when another client re-adds the marketplace", async () => {
  let listCalls = 0;
  const hub = marketplaceClient({
    list: async () => {
      listCalls += 1;
      // The mount read shows acme; the reconciliation read after the applied
      // removal fails; the retry read finds the registration another client
      // made - a fresh lastUpdated, not the stale row the removal applied to.
      if (listCalls === 2) throw new Error("list unavailable");
      return {
        marketplaces:
          listCalls >= 3 ? [{ ...marketplace, lastUpdated: 2 }] : [marketplace],
      };
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
  await act(async () => {});

  // The reconciliation read failed, so the hub's list this screen last saw
  // still carries the stale registration the removal applied to. Another
  // client re-adds the marketplace: the retry read's fresh identity has to
  // clear the fence, or the new registration could never be removed.
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Retry marketplaces" }).props.onPress();
  });
  await act(async () => {});
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "All marketplaces" }).props.onPress();
  });
  await act(async () => {});
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Browse acme" }).props.onPress();
  });
  await act(async () => {});
  const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
  expect(remove.props.disabled).toBe(false);
  await confirmMarketplaceRemoval(tree);
  expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(2);
});

it("keeps the fence when an add resolving after unmount registers another name", async () => {
  let releaseAdd!: () => void;
  const pendingAdd = new Promise<{ marketplaces: MarketplaceEntry[] }>(
    (resolve) => {
      releaseAdd = () =>
        resolve({
          marketplaces: [
            marketplace,
            { name: "gamma", source: { kind: "github", repo: "gamma/plugins" }, lastUpdated: 2 },
          ],
        });
    },
  );
  const hub = marketplaceClient({
    remove: async () => {
      throw cloneLitterError(null, false);
    },
    add: () => pendingAdd,
  });
  harness.connection = readyConnection(hub.client);
  const props = {
    route: { params: { hubId: "hub-1" } },
  } as unknown as ComponentProps<typeof PluginsScreen>;
  const tree = render(<PluginsScreen {...props} />);
  await act(async () => {});
  await browseMarketplace(tree);
  await confirmMarketplaceRemoval(tree);
  await act(async () => {});

  // Start an add for another marketplace and leave the browse tab before it
  // resolves: the browser that started it is gone with its store, which
  // discards the answer, so nothing about acme's registration changed - the
  // fence the applied removal raised has to hold.
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "All marketplaces" }).props.onPress();
  });
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Add marketplace" }).props.onPress();
  });
  await act(async () => {
    tree.root
      .findByProps({ accessibilityLabel: "Marketplace source" })
      .props.onChangeText("https://example.test/gamma.git");
  });
  const adds = tree.root.findAllByProps({ accessibilityLabel: "Add marketplace" });
  const submit = adds.at(-1);
  if (!submit) throw new Error("Add marketplace submit was not rendered");
  await act(async () => submit.props.onPress());
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Installed" }).props.onPress();
  });
  await act(async () => {
    releaseAdd();
    await Promise.resolve();
  });
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Browse" }).props.onPress();
  });
  await act(async () => {});
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Browse acme" }).props.onPress();
  });
  await act(async () => {});

  expect(hub.methods.filter((method) => method === "evener/marketplace/add")).toHaveLength(1);
  const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
  expect(remove.props.disabled).toBe(true);
  expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(1);
});

it("keeps a re-added marketplace removable when its registration is observed before the removal settles", async () => {
  let releaseRemoval!: () => void;
  const firstRemoval = new Promise<never>((_resolve, reject) => {
    releaseRemoval = () => reject(cloneLitterError(null, false));
  });
  let removals = 0;
  let listCalls = 0;
  const hub = marketplaceClient({
    list: async () => {
      listCalls += 1;
      // The mount read shows the registration the removal targets; the
      // remount's read - issued while the removal is still pending - finds
      // the registration another client made after the hub applied it.
      return {
        marketplaces:
          listCalls === 1 ? [marketplace] : [{ ...marketplace, lastUpdated: 2 }],
      };
    },
    remove: () => {
      removals += 1;
      return removals === 1 ? firstRemoval : Promise.resolve({ marketplaces: [] });
    },
  });
  harness.connection = readyConnection(hub.client);
  const props = {
    route: { params: { hubId: "hub-1" } },
  } as unknown as ComponentProps<typeof PluginsScreen>;
  const tree = render(<PluginsScreen {...props} />);
  await act(async () => {});
  await browseMarketplace(tree);
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Remove marketplace" }).props.onPress();
  });
  const request = alertRequests.at(-1);
  const confirm = request?.buttons?.find((button) => button.text === "Remove");
  if (!confirm?.onPress) throw new Error("Remove confirmation was not shown");
  await act(async () => confirm.onPress?.());

  // Another client re-added acme while the removal was pending, and the
  // remounted browser read that new registration before the outcome settled:
  // the fence the outcome raises must target the registration the write
  // removed, never the newer one that replaced it.
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Installed" }).props.onPress();
  });
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Browse" }).props.onPress();
  });
  await act(async () => {});
  releaseRemoval();
  await act(async () => {});
  expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");

  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Browse acme" }).props.onPress();
  });
  await act(async () => {});
  const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
  expect(remove.props.disabled).toBe(false);
  await confirmMarketplaceRemoval(tree);
  expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(2);
});

it("clears the fence for a re-added marketplace whose registration carries the same wire timestamp", async () => {
  const hub = marketplaceClient({
    remove: async () => {
      throw cloneLitterError(null, false);
    },
    // The re-add lands within the same second the original was registered,
    // so the wire's whole-second lastUpdated does not distinguish them.
    add: async () => ({ marketplaces: [marketplace] }),
  });
  harness.connection = readyConnection(hub.client);
  const props = {
    route: { params: { hubId: "hub-1" } },
  } as unknown as ComponentProps<typeof PluginsScreen>;
  const tree = render(<PluginsScreen {...props} />);
  await act(async () => {});
  await browseMarketplace(tree);
  await confirmMarketplaceRemoval(tree);
  await act(async () => {});

  // Re-add acme by name within the same second: the add this screen itself
  // made replaced the registration the fence guards, so the fence clears
  // even though the wire cannot tell the two registrations apart.
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "All marketplaces" }).props.onPress();
  });
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Add marketplace" }).props.onPress();
  });
  await act(async () => {
    tree.root
      .findByProps({ accessibilityLabel: "Marketplace source" })
      .props.onChangeText("https://example.test/plugins.git");
  });
  await act(async () => {
    tree.root
      .findByProps({ accessibilityLabel: "Marketplace name" })
      .props.onChangeText("acme");
  });
  const adds = tree.root.findAllByProps({ accessibilityLabel: "Add marketplace" });
  const submit = adds.at(-1);
  if (!submit) throw new Error("Add marketplace submit was not rendered");
  await act(async () => {
    submit.props.onPress();
    await Promise.resolve();
  });
  await act(async () => {});

  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Browse acme" }).props.onPress();
  });
  await act(async () => {});
  const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
  expect(remove.props.disabled).toBe(false);
  await confirmMarketplaceRemoval(tree);
  expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(2);
});
