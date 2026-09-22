// Screen-level tests for the marketplace removal outcomes the browser cannot
// hold on its own: the PluginsScreen-owned no-repeat guard fences a
// marketplace the hub says it already removed across browser remounts, the
// cleanup warning survives the browser's own revision fence and tab switches,
// a clean applied removal retires an obsolete cleanup warning, and a late
// outcome from a replaced client changes nothing on the new one. The fence
// covers only the window between an applied outcome and the first trusted
// read after it - presence retires it too (the fallback ruling), so a
// re-registration the wire cannot tell from a stale row can never stay
// fenced forever.
import type { ComponentProps } from "react";
import { act } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import {
  ErrorMarketplaceRemoveApplied,
  WireError,
  type MarketplaceEntry,
} from "@evener/appwire-client";
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

it("re-fences a name a stale presence read cleared after a browser remount", async () => {
  let listCalls = 0;
  let removals = 0;
  const hub = marketplaceClient({
    list: async () => {
      listCalls += 1;
      // The mount read shows the registration the removal targets; the
      // post-removal reconciliation read fails; the remount's own read
      // answers with the stale row again - the wire's whole-second stamp
      // cannot tell it from a re-registration, and the fallback ruling
      // trusts it as one - and the read after the second removal's outcome
      // fails too, so the re-fence holds to the assertion.
      if (listCalls === 2 || listCalls === 4) throw new Error("list unavailable");
      return { marketplaces: [marketplace] };
    },
    remove: () => {
      removals += 1;
      // The hub answers BOTH removals with the idempotent applied outcome:
      // the removal already stood, so a repeat press never reads as a
      // failed write.
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
  expect(renderedText(tree)).toContain("Could not load marketplaces. Try again when connected.");

  // Leave the browse tab and come back: the guard lives at the screen, so
  // it survives the browser's death, but the remount's own read answers
  // with the stale row - the first authoritative read after the outcome -
  // and the fallback retires the fence for whatever the trusted read
  // vouches for, so Remove re-enables.
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
  expect(remove.props.disabled).toBe(false);
  // Pressing Remove on the stale row reaches a hub that already applied the
  // removal: the same applied outcome answers, which re-fences the name and
  // re-raises the warning - idempotent, never a failed write.
  await confirmMarketplaceRemoval(tree);
  await act(async () => {});
  expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");
  expect(renderedText(tree)).toContain("Could not load marketplaces. Try again when connected.");
  const fenced = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
  expect(fenced.props.disabled).toBe(true);
  expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(2);
});

it("fences the name when a re-registration lands while the confirm dialog is open", async () => {
  let listCalls = 0;
  let landRefresh!: (value: { marketplaces: MarketplaceEntry[] }) => void;
  const hub = marketplaceClient({
    list: () => {
      listCalls += 1;
      // The mount read shows the registration the removal targets. A
      // pull-to-refresh read stays in flight while the confirm dialog is
      // open and answers with another client's re-registration of acme -
      // a fresh lastUpdated - and the post-removal reconciliation read
      // fails.
      if (listCalls === 1) return Promise.resolve({ marketplaces: [marketplace] });
      if (listCalls === 2)
        return new Promise<{ marketplaces: MarketplaceEntry[] }>((resolve) => {
          landRefresh = resolve;
        });
      return Promise.reject(new Error("list unavailable"));
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
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Browse" }).props.onPress();
  });
  await act(async () => {});
  // Pull-to-refresh: the read whose answer another client's refresh has
  // since re-registered, kept in flight while the dialog opens.
  const refresh = tree.root
    .findAll((node) => typeof node.props?.onRefresh === "function")
    .at(-1);
  if (!refresh) throw new Error("no marketplaces list to refresh");
  await act(async () => {
    refresh.props.onRefresh();
    await Promise.resolve();
  });
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Browse acme" }).props.onPress();
  });
  await act(async () => {});
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Remove marketplace" }).props.onPress();
  });
  // The read lands while the confirm dialog is open: acme's registration now
  // carries a fresh identity, not the one the dialog captured.
  await act(async () => {
    landRefresh({ marketplaces: [{ ...marketplace, lastUpdated: 2 }] });
    await Promise.resolve();
  });
  const request = alertRequests.at(-1);
  const confirm = request?.buttons?.find((button) => button.text === "Remove");
  if (!confirm?.onPress) throw new Error("Remove confirmation was not shown");
  await act(async () => {
    confirm.onPress?.();
    await Promise.resolve();
  });
  await act(async () => {});

  // The removal applied and the post-removal reconciliation read failed, so
  // no fresh list has established what the hub now carries: the guard has to
  // fence acme - Remove stays disabled on the already-removed row - and the
  // cleanup warning still raises.
  expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");
  expect(renderedText(tree)).toContain("Could not load marketplaces. Try again when connected.");
  expect(
    hub.methods.filter((method) => method === "evener/marketplace/list"),
  ).toHaveLength(3);
  const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
  expect(remove.props.disabled).toBe(true);
});

it("keeps the fence when a pre-removal read lands inside the outcome's window", async () => {
  let releaseStaleRead!: (value: { marketplaces: MarketplaceEntry[] }) => void;
  const staleRead = new Promise<{ marketplaces: MarketplaceEntry[] }>(
    (resolve) => {
      // The read's answer still describes the hub before the removal: acme
      // present under its original registration.
      releaseStaleRead = () => resolve({ marketplaces: [marketplace] });
    },
  );
  let releaseRemoval!: () => void;
  const pendingRemoval = new Promise<never>((_resolve, reject) => {
    releaseRemoval = () => reject(cloneLitterError(null, false));
  });
  let listCalls = 0;
  const hub = marketplaceClient({
    list: () => {
      listCalls += 1;
      // The mount read shows the registration the removal targets; a
      // pull-to-refresh read stays on the wire while the removal runs; every
      // later read fails, so nothing fresh ever lands.
      if (listCalls === 1) return Promise.resolve({ marketplaces: [marketplace] });
      if (listCalls === 2) return staleRead;
      return Promise.reject(new Error("list unavailable"));
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
    tree.root.findByProps({ accessibilityLabel: "All marketplaces" }).props.onPress();
  });
  const refresh = tree.root
    .findAll((node) => typeof node.props?.onRefresh === "function")
    .at(-1);
  if (!refresh) throw new Error("no marketplaces list to refresh");
  await act(async () => {
    refresh.props.onRefresh();
    await Promise.resolve();
  });
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Browse acme" }).props.onPress();
  });
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Remove marketplace" }).props.onPress();
  });
  const request = alertRequests.at(-1);
  const confirm = request?.buttons?.find((button) => button.text === "Remove");
  if (!confirm?.onPress) throw new Error("Remove confirmation was not shown");
  await act(async () => confirm.onPress?.());

  // The stale read answers while the removal is still in flight, so the
  // revision fence holds it behind the newer write. The write then rejects
  // with the applied outcome - and a rejection that publishes nothing hands
  // ownership back down, publishing the held stale answer a beat BEFORE the
  // outcome's continuation records the fence. That answer predates the fence
  // and must never retire it: only reads issued after the outcome count.
  await act(async () => {
    releaseStaleRead({ marketplaces: [marketplace] });
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
  await act(async () => {
    releaseRemoval();
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
  await act(async () => {});
  expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");
  expect(renderedText(tree)).toContain("Could not load marketplaces. Try again when connected.");
  const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
  expect(remove.props.disabled).toBe(true);
  expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(1);
});

it("clears the guard when a fresh read shows the removed name absent", async () => {
  let removals = 0;
  let listCalls = 0;
  const hub = marketplaceClient({
    list: () => {
      listCalls += 1;
      // The mount read shows the registration the removal targets; the
      // post-removal reconciliation read establishes its absence; a later
      // pull-to-refresh answers with another client's same-second re-add.
      if (listCalls === 2) return Promise.resolve({ marketplaces: [] });
      return Promise.resolve({ marketplaces: [marketplace] });
    },
    remove: () => {
      removals += 1;
      return removals === 1
        ? Promise.reject(cloneLitterError(null, false))
        : Promise.resolve({ marketplaces: [] });
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
  expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");

  // The reconciliation read established acme's absence, so the guard forgot
  // it: pull the list again and find another client's re-registration - one
  // the wire cannot even tell from the removed registration, the same
  // lastUpdated - and it has to stay removable.
  const refresh = tree.root
    .findAll((node) => typeof node.props?.onRefresh === "function")
    .at(-1);
  if (!refresh) throw new Error("no marketplaces list to refresh");
  await act(async () => {
    refresh.props.onRefresh();
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

it("clears an obsolete cleanup warning when a later applied removal is clean", async () => {
  let removals = 0;
  let listCalls = 0;
  const hub = marketplaceClient({
    list: () => {
      listCalls += 1;
      // The mount read shows the registration the removal targets; the
      // post-removal reconciliation read establishes its absence; a later
      // pull-to-refresh answers with another client's re-registration; the
      // read after the second removal's outcome fails, so the clean
      // outcome's own re-fence holds to the assertion.
      if (listCalls === 2) return Promise.resolve({ marketplaces: [] });
      if (listCalls >= 4) return Promise.reject(new Error("list unavailable"));
      return Promise.resolve({ marketplaces: [marketplace] });
    },
    remove: () => {
      removals += 1;
      return removals === 1
        ? Promise.reject(cloneLitterError(null, false))
        : Promise.reject(
            new WireError(
              "marketplace removed, but the updated list was unavailable",
              -32603,
              {
                evenerErrorInfo: ErrorMarketplaceRemoveApplied,
                appliedUnavailable: true,
              },
            ),
          );
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
  expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");

  // The reconciliation read established acme's absence, so the guard forgot
  // it: pull the list again, find another client's re-registration, and open
  // it - the fence no longer covers the name.
  const refresh = tree.root
    .findAll((node) => typeof node.props?.onRefresh === "function")
    .at(-1);
  if (!refresh) throw new Error("no marketplaces list to refresh");
  await act(async () => {
    refresh.props.onRefresh();
    await Promise.resolve();
  });
  await act(async () => {});
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Browse acme" }).props.onPress();
  });
  await act(async () => {});
  const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
  expect(remove.props.disabled).toBe(false);

  // Remove it again, and this removal applies CLEANLY: the hub's marker says
  // the unregister and its clone cleanup both landed, so the outcome carries
  // no notice. The earlier clone-cleanup warning is now about a removal this
  // hub fully handled - an obsolete leftover the screen has to retire, or a
  // warning about long-gone litter outlives every later successful removal.
  await confirmMarketplaceRemoval(tree);
  await act(async () => {});
  expect(renderedText(tree)).not.toContain("Marketplace removed; clone cleanup failed");
  // The clean outcome is still an applied removal - never a failed write.
  expect(renderedText(tree)).not.toContain("Could not confirm the change");
  expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(2);
  // And it still fences the name: the applied outcome recorded with the
  // screen's guard whatever its notice said.
  const fenced = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
  expect(fenced.props.disabled).toBe(true);
});

it("clears the cleanup warning when a later removal succeeds", async () => {
  let removals = 0;
  let listCalls = 0;
  const hub = marketplaceClient({
    list: () => {
      listCalls += 1;
      // The mount read shows the registration the removal targets; the
      // post-removal reconciliation read establishes its absence; a later
      // pull-to-refresh answers with another client's re-registration.
      if (listCalls === 2) return Promise.resolve({ marketplaces: [] });
      return Promise.resolve({ marketplaces: [marketplace] });
    },
    remove: () => {
      removals += 1;
      return removals === 1
        ? Promise.reject(cloneLitterError(null, false))
        : Promise.resolve({ marketplaces: [] });
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
  expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");

  // The reconciliation read established acme's absence, so the guard forgot
  // it: pull the list again, find another client's re-registration, and
  // remove it - this time the hub confirms the change outright, the write
  // resolving with nothing left to warn about.
  const refresh = tree.root
    .findAll((node) => typeof node.props?.onRefresh === "function")
    .at(-1);
  if (!refresh) throw new Error("no marketplaces list to refresh");
  await act(async () => {
    refresh.props.onRefresh();
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
  await act(async () => {});
  // The screen-level warning reports the latest removal outcome, so the
  // successful one retires the obsolete cleanup warning - never a failed
  // write either.
  expect(renderedText(tree)).not.toContain("Marketplace removed; clone cleanup failed");
  expect(renderedText(tree)).not.toContain("Could not confirm the change");
  expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(2);
});

it("keeps a marketplace's warning when an unrelated removal succeeds", async () => {
  const beta: MarketplaceEntry = {
    name: "beta",
    source: { kind: "github", repo: "beta/plugins" },
    lastUpdated: 2,
  };
  let removals = 0;
  let listCalls = 0;
  const hub = marketplaceClient({
    list: () => {
      listCalls += 1;
      // The mount read carries both marketplaces; every later read fails, so
      // the stale list is all the screen ever sees.
      if (listCalls === 1)
        return Promise.resolve({ marketplaces: [marketplace, beta] });
      return Promise.reject(new Error("list unavailable"));
    },
    remove: () => {
      removals += 1;
      // acme's removal (first) applies with clone litter; beta's removal
      // (second) the hub confirms outright.
      return removals === 1
        ? Promise.reject(cloneLitterError(null, false))
        : Promise.resolve({ marketplaces: [] });
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
  expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");

  // A different marketplace's clean removal is a fresh outcome for a
  // different name: acme's litter warning is about clone files the hub
  // could not clean, and beta's success says nothing about them, so the
  // warning has to stay up for its own marketplace.
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "All marketplaces" }).props.onPress();
  });
  await act(async () => {});
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Browse beta" }).props.onPress();
  });
  await act(async () => {});
  const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
  expect(remove.props.disabled).toBe(false);
  await confirmMarketplaceRemoval(tree);
  await act(async () => {});
  expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");
  expect(renderedText(tree)).not.toContain("Could not confirm the change");
  expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(2);
});

it("records an applied removal after selection changes while the request is pending", async () => {
  let releaseRemoval!: () => void;
  const pendingRemoval = new Promise<never>((_resolve, reject) => {
    releaseRemoval = () => reject(cloneLitterError(null, false));
  });
  let listCalls = 0;
  const hub = marketplaceClient({
    list: async () => {
      listCalls += 1;
      // The mount read shows the registration the removal targets; every
      // later read fails, so no authoritative read lands after the outcome.
      if (listCalls === 1) return { marketplaces: [marketplace] };
      throw new Error("list unavailable");
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
  let listCalls = 0;
  const hub = marketplaceClient({
    list: async () => {
      listCalls += 1;
      // The mount read shows the registration the removal targets; every
      // later read - the post-removal refetch and the store's own reconnect
      // re-read - fails, so no authoritative read lands after the outcome.
      if (listCalls === 1) return { marketplaces: [marketplace] };
      throw new Error("list unavailable");
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

  // A passive flap moves only `state`: the screen stays mounted behind the
  // banner (connectionDisplay.ts) with the SAME client throughout, and the
  // browser it carries stays on the detail it was showing - the guard and
  // warning have to outlive the store's own reconnect re-read.
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
  // The banner kept this browser mounted through the flap (connectionDisplay:
  // everReady already true, the SAME client throughout), so the detail it had
  // open survives with it - the retention #1952 bought - while the store's
  // own reconnect re-read fails, so no authoritative read has landed and
  // the fence still guards the stale row in the retained detail.
  expect(renderedText(tree)).toContain("All marketplaces");
  expect(renderedText(tree)).toContain("Refresh source");
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
  // The retry read answers with the stale row - the first authoritative read
  // after the outcome, which the fallback trusts - so the fence retires and
  // Remove re-enables; a press on it would only draw the hub's same
  // idempotent applied outcome.
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Browse acme" }).props.onPress();
  });
  await act(async () => {});
  const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
  expect(remove.props.disabled).toBe(false);
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
  // guard, so the record lands over an absence the screen has already seen
  // and the fence covers the name again. The re-add below submits a BLANK
  // name - the hub assigns one - and lands within the same wire second, so
  // neither the add's own published list (a same-timestamp row reads as the
  // stale registration the fence guards) nor any later read can tell the
  // fresh registration from the removed one: only the add itself reporting
  // the name it registered can clear the fence, or the re-added acme could
  // never be removed.
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

it("clears the fence when a blank-name re-add lands in the wire-indistinguishable second", async () => {
  let removals = 0;
  let listCalls = 0;
  const hub = marketplaceClient({
    list: async () => {
      listCalls += 1;
      // The mount read shows the registration the removal targets; the
      // post-removal reconciliation read fails, so the stale row - with its
      // original whole-second stamp - is all the screen ever sees.
      if (listCalls === 2) throw new Error("list unavailable");
      return { marketplaces: [marketplace] };
    },
    remove: () => {
      removals += 1;
      return removals === 1
        ? Promise.reject(cloneLitterError(null, false))
        : Promise.resolve({ marketplaces: [] });
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
  expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");
  expect(renderedText(tree)).toContain("Could not load marketplaces. Try again when connected.");

  // Re-add acme with a BLANK name and the source the stale row still shows,
  // landing within the same whole second as the original registration: the
  // add's own answer is a list indistinguishable from the stale one, so only
  // the add knowing what it re-registered can clear the fence - or the
  // fresh registration could never be removed.
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "All marketplaces" }).props.onPress();
  });
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Add marketplace" }).props.onPress();
  });
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "GitHub repository" }).props.onPress();
  });
  await act(async () => {
    tree.root
      .findByProps({ accessibilityLabel: "Marketplace source" })
      .props.onChangeText("acme/plugins");
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

it("clears the fence for a blank-name re-add that resolves after the browser unmounts", async () => {
  let releaseAdd!: () => void;
  const pendingAdd = new Promise<{ marketplaces: MarketplaceEntry[] }>(
    (resolve) => {
      releaseAdd = () =>
        resolve({
          marketplaces: [
            { ...marketplace, source: { kind: "github", repo: "acme/other" } },
          ],
        });
    },
  );
  let removals = 0;
  let listCalls = 0;
  const hub = marketplaceClient({
    list: async () => {
      listCalls += 1;
      // The mount read shows the registration the removal targets; the
      // post-removal reconciliation read fails; every later read carries
      // the re-registration the add made - a different source, stamped
      // within the same whole second as the removed one.
      if (listCalls === 2) throw new Error("list unavailable");
      return {
        marketplaces:
          listCalls === 1
            ? [marketplace]
            : [{ ...marketplace, source: { kind: "github", repo: "acme/other" } }],
      };
    },
    remove: () => {
      removals += 1;
      return removals === 1
        ? Promise.reject(cloneLitterError(null, false))
        : Promise.resolve({ marketplaces: [] });
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
  expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");
  expect(renderedText(tree)).toContain("Could not load marketplaces. Try again when connected.");

  // Start a BLANK-name add from a different source, then leave the browse
  // tab before it resolves: the store that started it dies with the browser
  // and drops the add's publication, so the registration it made is only in
  // the hub's own list.
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "All marketplaces" }).props.onPress();
  });
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Add marketplace" }).props.onPress();
  });
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "GitHub repository" }).props.onPress();
  });
  await act(async () => {
    tree.root
      .findByProps({ accessibilityLabel: "Marketplace source" })
      .props.onChangeText("acme/other");
  });
  const adds = tree.root.findAllByProps({ accessibilityLabel: "Add marketplace" });
  const submit = adds.at(-1);
  if (!submit) throw new Error("Add marketplace submit was not rendered");
  await act(async () => {
    submit.props.onPress();
    await Promise.resolve();
  });
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Installed" }).props.onPress();
  });
  await act(async () => {
    releaseAdd();
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
  await act(async () => {});

  // The fence the stale row kept has to clear for the registration the add
  // put back, or the re-added marketplace could never be removed.
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Browse" }).props.onPress();
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

it("clears the fence for a wire-indistinguishable re-add made beside another change", async () => {
  let removals = 0;
  let listCalls = 0;
  const hub = marketplaceClient({
    list: async () => {
      listCalls += 1;
      // The mount read shows the registration the removal targets; the
      // post-removal reconciliation read fails; every later read carries
      // the re-registration the add made alongside a marketplace another
      // client registered since the list this screen last carried.
      if (listCalls === 2) throw new Error("list unavailable");
      return {
        marketplaces:
          listCalls === 1
            ? [marketplace]
            : [
                marketplace,
                {
                  name: "gamma",
                  source: { kind: "github", repo: "gamma/plugins" },
                  lastUpdated: 2,
                },
              ],
      };
    },
    remove: () => {
      removals += 1;
      return removals === 1
        ? Promise.reject(cloneLitterError(null, false))
        : Promise.resolve({ marketplaces: [] });
    },
    add: async () => ({
      marketplaces: [
        marketplace,
        {
          name: "gamma",
          source: { kind: "github", repo: "gamma/plugins" },
          lastUpdated: 2,
        },
      ],
    }),
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
  expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");
  expect(renderedText(tree)).toContain("Could not load marketplaces. Try again when connected.");

  // Re-add acme with a BLANK name and its original source, within the same
  // whole second, while another client registers gamma: the add's answer
  // newly carries gamma, and the fence still needs its own re-registration
  // named beside it - or acme could never be removed.
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "All marketplaces" }).props.onPress();
  });
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Add marketplace" }).props.onPress();
  });
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "GitHub repository" }).props.onPress();
  });
  await act(async () => {
    tree.root
      .findByProps({ accessibilityLabel: "Marketplace source" })
      .props.onChangeText("acme/plugins");
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

it("clears the fence for a blank-name re-add held behind a newer list read", async () => {
  let releaseAdd!: () => void;
  const pendingAdd = new Promise<{ marketplaces: MarketplaceEntry[] }>(
    (resolve) => {
      releaseAdd = () =>
        resolve({
          marketplaces: [
            { ...marketplace, source: { kind: "github", repo: "acme/other" } },
          ],
        });
    },
  );
  let releaseRefreshRead!: () => void;
  const heldRead = new Promise<{ marketplaces: MarketplaceEntry[] }>(
    (resolve) => {
      releaseRefreshRead = () =>
        resolve({
          marketplaces: [
            { ...marketplace, source: { kind: "github", repo: "acme/other" } },
          ],
        });
    },
  );
  let removals = 0;
  let listCalls = 0;
  const hub = marketplaceClient({
    list: () => {
      listCalls += 1;
      // The mount read shows the registration the removal targets; the
      // post-removal reconciliation read fails; a pull-to-refresh read -
      // issued while the add is still in flight - stays pending, holding
      // the add's own publication behind it; every later read carries the
      // re-registration the add made.
      if (listCalls === 2) throw new Error("list unavailable");
      if (listCalls === 3) return heldRead;
      return Promise.resolve({
        marketplaces:
          listCalls === 1
            ? [marketplace]
            : [{ ...marketplace, source: { kind: "github", repo: "acme/other" } }],
      });
    },
    remove: () => {
      removals += 1;
      return removals === 1
        ? Promise.reject(cloneLitterError(null, false))
        : Promise.resolve({ marketplaces: [] });
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
  expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");
  expect(renderedText(tree)).toContain("Could not load marketplaces. Try again when connected.");

  // Start a BLANK-name add from a different source, then pull the list
  // before it resolves: the pending read outranks the add, so the store
  // holds the add's publication behind it.
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "All marketplaces" }).props.onPress();
  });
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Add marketplace" }).props.onPress();
  });
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "GitHub repository" }).props.onPress();
  });
  await act(async () => {
    tree.root
      .findByProps({ accessibilityLabel: "Marketplace source" })
      .props.onChangeText("acme/other");
  });
  const adds = tree.root.findAllByProps({ accessibilityLabel: "Add marketplace" });
  const submit = adds.at(-1);
  if (!submit) throw new Error("Add marketplace submit was not rendered");
  await act(async () => {
    submit.props.onPress();
    await Promise.resolve();
  });
  const refresh = tree.root
    .findAll((node) => typeof node.props?.onRefresh === "function")
    .at(-1);
  if (!refresh) throw new Error("no marketplaces list to refresh");
  await act(async () => {
    refresh.props.onRefresh();
    await Promise.resolve();
  });
  await act(async () => {
    releaseAdd();
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
  await act(async () => {});

  // The pending read settles with the same whole-second stamp, so it
  // cannot reconcile the fence either: the add's own registration is the
  // only thing that can name it.
  await act(async () => {
    releaseRefreshRead();
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

it("keeps the fence for a fenced row a blank add's answer carries unchanged", async () => {
  let releaseAdd!: () => void;
  const pendingAdd = new Promise<{ marketplaces: MarketplaceEntry[] }>(
    (resolve) => {
      releaseAdd = () =>
        resolve({
          marketplaces: [
            // acme's row exactly as the pre-add list carried it: the hub's
            // answer read failed and served the stale cache, so this row is
            // NOT one the add created - the add registered delta, named off
            // the same source's own catalog.
            marketplace,
            { name: "delta", source: { kind: "github", repo: "acme/plugins" }, lastUpdated: 2 },
          ],
        });
    },
  );
  let listCalls = 0;
  const hub = marketplaceClient({
    list: () => {
      listCalls += 1;
      // The mount read shows the registration the removal targets; the
      // post-removal reconciliation read fails; a pull-to-refresh read -
      // issued while the add is still in flight - stays pending, holding the
      // add's own publication behind it so only the add's naming can touch
      // the fence.
      if (listCalls === 1) return Promise.resolve({ marketplaces: [marketplace] });
      if (listCalls === 2) return Promise.reject(new Error("list unavailable"));
      return new Promise<{ marketplaces: MarketplaceEntry[] }>(() => {});
    },
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
  expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");
  expect(renderedText(tree)).toContain("Could not load marketplaces. Try again when connected.");

  // Submit a BLANK-name add of the fenced marketplace's own source: only a
  // row the answer newly carries - one the pre-add list did not have, in the
  // wire's own form - may be the registration this add made, so the stale
  // acme row the answer also carries must leave the fence alone.
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "All marketplaces" }).props.onPress();
  });
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Add marketplace" }).props.onPress();
  });
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "GitHub repository" }).props.onPress();
  });
  await act(async () => {
    tree.root
      .findByProps({ accessibilityLabel: "Marketplace source" })
      .props.onChangeText("acme/plugins");
  });
  const adds = tree.root.findAllByProps({ accessibilityLabel: "Add marketplace" });
  const submit = adds.at(-1);
  if (!submit) throw new Error("Add marketplace submit was not rendered");
  await act(async () => {
    submit.props.onPress();
    await Promise.resolve();
  });
  const refresh = tree.root
    .findAll((node) => typeof node.props?.onRefresh === "function")
    .at(-1);
  if (!refresh) throw new Error("no marketplaces list to refresh");
  await act(async () => {
    refresh.props.onRefresh();
    await Promise.resolve();
  });
  await act(async () => {
    releaseAdd();
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
  await act(async () => {});
  expect(hub.methods.filter((method) => method === "evener/marketplace/add")).toHaveLength(1);

  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Browse acme" }).props.onPress();
  });
  await act(async () => {});
  const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
  expect(remove.props.disabled).toBe(true);
  expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(1);
});

it("clears the fence for a wire-indistinguishable blank re-add when no list read succeeds", async () => {
  let listCalls = 0;
  let removals = 0;
  const hub = marketplaceClient({
    list: async () => {
      listCalls += 1;
      // The mount read shows the registration the removal targets; every
      // later list read fails, so no read can ever name the re-registration
      // the add makes - only the add's own answer can.
      if (listCalls === 1) return { marketplaces: [marketplace] };
      throw new Error("list unavailable");
    },
    remove: () => {
      removals += 1;
      return removals === 1
        ? Promise.reject(cloneLitterError(null, false))
        : Promise.resolve({ marketplaces: [] });
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
  expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");
  expect(renderedText(tree)).toContain("Could not load marketplaces. Try again when connected.");

  // Re-add acme with a BLANK name and its original source, within the same
  // whole second as the removed registration, on a hub whose list reads
  // keep failing: the add's own answer is the only thing that can name the
  // re-registration, or the fresh one could never be removed.
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "All marketplaces" }).props.onPress();
  });
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Add marketplace" }).props.onPress();
  });
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "GitHub repository" }).props.onPress();
  });
  await act(async () => {
    tree.root
      .findByProps({ accessibilityLabel: "Marketplace source" })
      .props.onChangeText("acme/plugins");
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

it("clears the fence when another client re-adds the name from a different source in the same wire second", async () => {
  let listCalls = 0;
  let removals = 0;
  const hub = marketplaceClient({
    list: async () => {
      listCalls += 1;
      // The mount read shows the registration the removal targets; the
      // post-removal reconciliation read fails; the retry read finds the
      // registration another client made after the hub applied the removal
      // - the same whole-second stamp, from a different source.
      if (listCalls === 2) throw new Error("list unavailable");
      return {
        marketplaces:
          listCalls === 1
            ? [marketplace]
            : [{ ...marketplace, source: { kind: "github", repo: "acme/other" } }],
      };
    },
    remove: () => {
      removals += 1;
      return removals === 1
        ? Promise.reject(cloneLitterError(null, false))
        : Promise.resolve({ marketplaces: [] });
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
  expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");
  expect(renderedText(tree)).toContain("Could not load marketplaces. Try again when connected.");

  // The re-add's stamp matches the removed registration's whole second, but
  // its source is its own: the fence the stale read kept has to recognize a
  // replacement registration, or the re-added marketplace could never be
  // removed.
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Retry marketplaces" }).props.onPress();
  });
  await act(async () => {});
  const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
  expect(remove.props.disabled).toBe(false);
  await confirmMarketplaceRemoval(tree);
  expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(2);
});

it("re-enables Remove for a same-source same-second re-registration", async () => {
  let removals = 0;
  let listCalls = 0;
  const hub = marketplaceClient({
    list: async () => {
      listCalls += 1;
      // The mount read shows the registration the removal targets; the
      // post-removal reconciliation read fails; the retry read carries
      // another client's re-registration - the SAME source, stamped within
      // the SAME whole second, indistinguishable on the wire from the row
      // the removal took out.
      if (listCalls === 2) throw new Error("list unavailable");
      return { marketplaces: [marketplace] };
    },
    remove: () => {
      removals += 1;
      return removals === 1
        ? Promise.reject(cloneLitterError(null, false))
        : Promise.resolve({ marketplaces: [] });
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
  expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");
  expect(renderedText(tree)).toContain("Could not load marketplaces. Try again when connected.");

  // The reconciliation read failed, so the retry read is the first
  // authoritative read after the outcome - and it carries a row the wire
  // cannot tell from the stale one. The fence has to retire for it (the
  // fallback ruling), or the re-registered marketplace could never be
  // removed from this client.
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Retry marketplaces" }).props.onPress();
  });
  await act(async () => {});
  // The removal's detail is still the open view - the retry read re-enables
  // Remove in place.
  const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
  expect(remove.props.disabled).toBe(false);
  await confirmMarketplaceRemoval(tree);
  expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(2);
});

it("retires the fence on the trusted read when an add resolving after unmount registers another name", async () => {
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
  let listCalls = 0;
  const hub = marketplaceClient({
    list: async () => {
      listCalls += 1;
      // The mount read shows the registration the removal targets; the
      // post-removal reconciliation read fails; the remount's read carries
      // the hub's truth after the add registered gamma - acme's stale row
      // still under the wire-indistinguishable identity, gamma beside it.
      if (listCalls === 2) throw new Error("list unavailable");
      return {
        marketplaces:
          listCalls === 1
            ? [marketplace]
            : [
                marketplace,
                { name: "gamma", source: { kind: "github", repo: "gamma/plugins" }, lastUpdated: 2 },
              ],
      };
    },
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
  // discards the answer's publication, and the answer's own naming reports
  // only the name it registered - gamma - never acme.
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

  expect(hub.methods.filter((method) => method === "evener/marketplace/add")).toHaveLength(1);
  // The remount's read carries the hub's truth - gamma registered, acme's
  // row under the identity the wire cannot tell from the stale one - and it
  // is the first authoritative read after the outcome, so the fallback
  // retires acme's fence for whatever it vouches for: both rows render and
  // acme is removable again, a press only drawing the same idempotent
  // applied outcome.
  expect(
    tree.root.findAllByProps({ accessibilityLabel: "Browse gamma" }).length,
  ).toBeGreaterThan(0);
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Browse acme" }).props.onPress();
  });
  await act(async () => {});
  const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
  expect(remove.props.disabled).toBe(false);
  await confirmMarketplaceRemoval(tree);
  expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(2);
});

it("briefly fences a re-added registration observed before the removal settles, then clears it", async () => {
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
  // the outcome's record lands anyway and fences the name - whatever the
  // hub's truth currently carries - so the re-added registration stays
  // fenced until the next authoritative read re-establishes it.
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

  // The fence covers the re-added registration for now: no authoritative
  // read has landed since the record.
  await act(async () => {
    tree.root.findByProps({ accessibilityLabel: "Browse acme" }).props.onPress();
  });
  await act(async () => {});
  expect(
    tree.root.findByProps({ accessibilityLabel: "Remove marketplace" }).props.disabled,
  ).toBe(true);

  // The next authoritative read carries the re-add's fresh identity, which
  // clears the fence, so the newer registration is removable again.
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
