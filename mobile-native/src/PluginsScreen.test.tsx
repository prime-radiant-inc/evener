// Screen-level tests for the marketplace removal outcomes the browser cannot
// hold on its own: the PluginsScreen-owned no-repeat guard fences a
// marketplace the hub says it already removed across browser remounts, the
// cleanup warning survives the browser's own revision fence and tab switches,
// a clean applied removal retires an obsolete cleanup warning, and a late
// outcome from a replaced client changes nothing on the new one. The fence
// covers only the window between an applied outcome and the first trusted
// read after it - presence retires it too (the fallback ruling), so a
// re-registration the wire cannot tell from a stale row can never stay
// fenced forever. The reconnect-recovery suite main added beside it
// (#1915 round-1 gap: a transport flap keeps the same client, so the
// screen's own mount effect never re-runs) shares this file's harness
// and mocks.
import type { ComponentProps } from "react";
import { act } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import {
  ErrorMarketplaceRemoveApplied,
  WireError,
  type MarketplaceEntry,
  type PluginEntry,
} from "@evener/appwire-client";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { AddMarketplace } from "./MarketplaceBrowser";
import { createPluginMutationGate } from "./pluginMutationGate";
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

/** The props the mounted screen hands its Plugins child, for driving the
 * screen-level recorders directly: `onAppliedRemoval` is the seam whose
 * return the client-switch tests pin, and `appliedRemovalNames` is the
 * fence as the browser sees it. */
function pluginsProps(tree: ReturnType<typeof render>) {
  const found = tree.root.findAll(
    (node) => typeof node.props?.onAppliedRemoval === "function",
  );
  const props = found.at(-1)?.props as
    | {
        onAppliedRemoval: (
          name: string,
          notice: string | null,
          owner: ConversationClientLike,
        ) => boolean;
        appliedRemovalNames: ReadonlySet<string>;
      }
    | undefined;
  if (!props) throw new Error("Plugins child was not rendered");
  return props;
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

it("retires the fence when the read holding a wire-indistinguishable blank re-add lands", async () => {
  let releaseAdd!: () => void;
  const pendingAdd = new Promise<{ marketplaces: MarketplaceEntry[] }>(
    (resolve) => {
      releaseAdd = () => resolve({ marketplaces: [marketplace] });
    },
  );
  let releaseRefreshRead!: () => void;
  const heldRead = new Promise<{ marketplaces: MarketplaceEntry[] }>(
    (resolve) => {
      // The read resolves with the re-registration under the removed
      // registration's own identity: the same name, the same source, the
      // same whole-second stamp - the wire cannot tell it from the stale
      // row, so neither can any diff.
      releaseRefreshRead = () => resolve({ marketplaces: [marketplace] });
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
      // the add's own publication behind it, and every later read carries
      // the re-registration the add made under the indistinguishable
      // identity.
      if (listCalls === 2) return Promise.reject(new Error("list unavailable"));
      if (listCalls === 3) return heldRead;
      return Promise.resolve({ marketplaces: [marketplace] });
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

  // Re-add acme with a BLANK name and its original source - landing within
  // the same whole second, so the add's own answer is a list the wire cannot
  // tell from the stale one and no naming can pick the registration out -
  // while a pull-to-refresh read stays on the wire ahead of the answer, so
  // the store holds the answer's publication behind it.
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

  // The read that held the answer's publication lands with the
  // re-registration the wire cannot tell from the removed one: it is the
  // first publication after the outcome either way, so the fallback ruling
  // retires the fence on its arrival - the exact same-name same-source
  // same-second case the source fallback used to be documented for, covered
  // by the read side instead of any naming.
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

it("answers false for an applied outcome a client switch outran, and records for the client that replaced it", async () => {
  const oldHub = marketplaceClient({});
  const newHub = marketplaceClient({ list: async () => ({ marketplaces: [] }) });
  harness.connection = readyConnection(oldHub.client);
  const props = {
    route: { params: { hubId: "hub-1" } },
  } as unknown as ComponentProps<typeof PluginsScreen>;
  const tree = render(<PluginsScreen {...props} />);
  await act(async () => {});
  await browseMarketplace(tree);
  const notice = "Marketplace removed; clone cleanup failed. Remove the leftover clone files manually.";

  // The outcome's recording path, captured through the props the screen
  // hands the browser: the return says whether the fence was actually
  // stored, and the browser drops a false outcome whole.
  const record = pluginsProps(tree).onAppliedRemoval;

  // Replace the client; the switch's effect has run, so the screen now
  // belongs to the new client's guard.
  harness.connection = readyConnection(newHub.client);
  await act(async () => {
    tree.update(<PluginsScreen {...props} />);
  });
  await act(async () => {});

  // A late applied outcome from the replaced client must answer false -
  // not store the name and answer true, which would send the browser off to
  // set its watermark and refetch for a fence that never existed.
  expect(record("acme", notice, oldHub.client)).toBe(false);
  expect(pluginsProps(tree).appliedRemovalNames.size).toBe(0);

  // The store is alive for the client that replaced it: an outcome of its
  // own records and fences, so the false above was the switch's, not a dead
  // store's.
  let stored = false;
  await act(async () => {
    stored = pluginsProps(tree).onAppliedRemoval("acme", notice, newHub.client);
  });
  expect(stored).toBe(true);
  expect(pluginsProps(tree).appliedRemovalNames.has("acme")).toBe(true);
});

it("stores an applied outcome that recorded before the switch, then drops it with the replaced client", async () => {
  const oldHub = marketplaceClient({});
  const newHub = marketplaceClient({ list: async () => ({ marketplaces: [] }) });
  harness.connection = readyConnection(oldHub.client);
  const props = {
    route: { params: { hubId: "hub-1" } },
  } as unknown as ComponentProps<typeof PluginsScreen>;
  const tree = render(<PluginsScreen {...props} />);
  await act(async () => {});
  const notice = "Marketplace removed; clone cleanup failed. Remove the leftover clone files manually.";
  const record = pluginsProps(tree).onAppliedRemoval;

  // The outcome records while the screen still belongs to the old client -
  // the flank of the client-switch window on the other side from the test
  // above: there the switch outran the outcome's check and the answer had
  // to be false; here the check and the store both outran the switch, and a
  // true answer has to mean the entry was actually stored, because the
  // browser sets its watermark and refetches off that true.
  let answer: boolean | undefined;
  act(() => {
    answer = record("acme", notice, oldHub.client);
  });
  expect(answer).toBe(true);
  // The store the true answered for, as the fence the browser sees: the
  // name is in the guard, committed.
  expect(pluginsProps(tree).appliedRemovalNames.has("acme")).toBe(true);

  // The client switch then replaces the guard wholesale: the fence the
  // replaced client recorded is not the new client's, and nothing of the
  // old outcome may survive it.
  harness.connection = readyConnection(newHub.client);
  await act(async () => {
    tree.update(<PluginsScreen {...props} />);
  });
  expect(pluginsProps(tree).appliedRemovalNames.size).toBe(0);
  expect(renderedText(tree)).not.toContain("clone cleanup failed");
  // And the new client's own outcome still records.
  let fresh = false;
  await act(async () => {
    fresh = pluginsProps(tree).onAppliedRemoval("beta", notice, newHub.client);
  });
  expect(fresh).toBe(true);
  expect(pluginsProps(tree).appliedRemovalNames.has("beta")).toBe(true);
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


// ---------------------------------------------------------------------------
// The reconnect-recovery suite (#1915's round-1 review gap): a transport
// flap keeps the SAME AppwireClient object (only its state moves
// ready -> reconnecting -> ready), so PluginsScreen's own mount effect -
// the only thing that ever called fetchPlugins() - never runs again, and
// nothing else told the store the flap happened. These mount the real
// screen (the only way to observe the wiring between useConnection's
// state and the store's own connectionChanged) and count the wire calls
// it makes.
// ---------------------------------------------------------------------------

/** A plugins client: every method call is recorded in `methods`, every
 * `evener/plugin/list` answers with `plugins`, and `evener/marketplace/list`
 * (the browse tab's own read) answers with an empty list - shaped for
 * whichever surface a test mounts. */
function pluginsClient(plugins: PluginEntry[]) {
	const methods: string[] = [];
	const client = {
		request: async (method: string) => {
			methods.push(method);
			if (method === "evener/marketplace/list") return { marketplaces: [] };
			return { plugins };
		},
		onNotification: () => () => {},
	} as ConversationClientLike;
	return { client, methods };
}

const plugin: PluginEntry = {
	plugin: "demo-plugin",
	marketplace: "core",
	version: "1.0.0",
	enabled: true,
	autoUpgrade: false,
	broken: false,
	installPath: "/plugins/demo-plugin",
	installedAt: 0,
	lastUpdated: 0,
};

it("re-reads the installed list once the connection returns to ready after a flap", async () => {
	const hub = pluginsClient([plugin]);
	harness.connection = {
		activeProfile: { id: "hub-1", name: "Work hub" },
		client: hub.client,
		state: "ready",
		fatal: false,
		retry: () => {},
	};
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsScreen>;
	const tree = render(<PluginsScreen {...props} />);
	await act(async () => {});
	expect(renderedText(tree)).toContain("demo-plugin");
	expect(hub.methods.filter((m) => m === "evener/plugin/list")).toHaveLength(1);

	// A passive flap: the connection layer's own generation guard keeps the
	// SAME client object through it (hubConnection.ts) - only `state` moves.
	harness.connection = { ...harness.connection, state: "reconnecting" };
	await act(async () => {
		tree.update(<PluginsScreen {...props} />);
	});
	expect(renderedText(tree)).toContain("demo-plugin");

	harness.connection = { ...harness.connection, state: "ready" };
	await act(async () => {
		tree.update(<PluginsScreen {...props} />);
	});

	expect(hub.methods.filter((m) => m === "evener/plugin/list")).toHaveLength(2);
});

it("MarketplaceBrowser re-reads its list once the connection returns to ready after a flap", async () => {
	const hub = pluginsClient([plugin]);
	harness.connection = {
		activeProfile: { id: "hub-1", name: "Work hub" },
		client: hub.client,
		state: "ready",
		fatal: false,
		retry: () => {},
	};
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsScreen>;
	const tree = render(<PluginsScreen {...props} />);
	await act(async () => {});
	// Switch to the browse tab, which mounts MarketplaceBrowser.
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Browse" }).props.onPress();
	});
	await act(async () => {});
	expect(hub.methods.filter((m) => m === "evener/marketplace/list")).toHaveLength(1);

	harness.connection = { ...harness.connection, state: "reconnecting" };
	await act(async () => {
		tree.update(<PluginsScreen {...props} />);
	});
	harness.connection = { ...harness.connection, state: "ready" };
	await act(async () => {
		tree.update(<PluginsScreen {...props} />);
	});

	expect(hub.methods.filter((m) => m === "evener/marketplace/list")).toHaveLength(2);
});

it("keeps the marketplace draft and exposes reconnect inside its modal", async () => {
	const hub = pluginsClient([plugin]);
	const retry = vi.fn();
	harness.connection = {
		activeProfile: { id: "hub-1", name: "Work hub" },
		client: hub.client,
		state: "ready",
		fatal: false,
		retry,
	};
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsScreen>;
	const tree = render(<PluginsScreen {...props} />);
	await act(async () => {});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Browse" }).props.onPress();
	});
	await act(async () => {});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Add marketplace" }).props.onPress();
	});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Marketplace source" }).props.onChangeText("https://example.test/plugins.git");
	});

	harness.connection = { ...harness.connection, state: "reconnecting" };
	await act(async () => {
		tree.update(<PluginsScreen {...props} />);
	});
	const sourceInput = tree.root.findByProps({ accessibilityLabel: "Marketplace source" });
	expect(sourceInput.props.value).toBe("https://example.test/plugins.git");
	const reconnects = tree.root.findAllByProps({ accessibilityLabel: "Reconnect" });
	expect(reconnects).toHaveLength(2);
	const modalReconnect = reconnects[reconnects.length - 1];
	if (!modalReconnect) throw new Error("modal reconnect action was not rendered");
	await act(async () => {
		modalReconnect.props.onPress();
	});
	expect(retry).toHaveBeenCalledOnce();
	expect(sourceInput.props.value).toBe("https://example.test/plugins.git");
});

it("keeps Add marketplace open when readiness is lost during submit", async () => {
	const hub = pluginsClient([]);
	const onAdd = vi.fn(async () => {});
	const onClose = vi.fn();
	let readinessChecks = 0;
	const tree = render(
		<AddMarketplace
			client={hub.client}
			connectionState="ready"
			hubName="Work hub"
			gate={createPluginMutationGate()}
			ready
			canUseConnection={() => readinessChecks++ === 0}
			onClose={onClose}
			onAdd={onAdd}
		/>,
	);
	const source = tree.root.findByProps({ accessibilityLabel: "Marketplace source" });
	act(() => source.props.onChangeText("https://example.test/plugins.git"));

	// The press passed whenReady's own recheck; the gate rechecks the same
	// predicate once more, after readiness was lost between the two. Nothing
	// ran, so the modal keeps the draft rather than closing as if it had.
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Add marketplace" }).props.onPress();
	});

	expect(onAdd).not.toHaveBeenCalled();
	expect(onClose).not.toHaveBeenCalled();
	expect(
		tree.root.findByProps({ accessibilityLabel: "Marketplace source" }).props.value,
	).toBe("https://example.test/plugins.git");
});

it("never renders the previous hub's retained client once the route names a hub whose profile is not active", async () => {
	// The panel-review gap: the route is re-keyed to hub B while the
	// connection still reports hub A - ready, with hub A's client. The
	// mismatch window's early return hides that data, but the retention
	// hooks must not record hub A's readiness under the route's hub either:
	// when the profile then moves to hub B mid-flap, hub B must meet a wall
	// for a hub it has never been ready for - not a banner over hub A's
	// retained client and rows.
	const hubA = pluginsClient([plugin]);
	harness.connection = {
		activeProfile: { id: "hub-a", name: "A hub" },
		client: hubA.client,
		state: "ready",
		fatal: false,
		retry: () => {},
	};
	const props = {
		route: { params: { hubId: "hub-b" } },
	} as unknown as ComponentProps<typeof PluginsScreen>;
	const tree = render(<PluginsScreen {...props} />);
	await act(async () => {});
	expect(renderedText(tree)).toContain("no longer selected");

	harness.connection = {
		activeProfile: { id: "hub-b", name: "B hub" },
		client: null,
		state: "reconnecting",
		fatal: false,
		retry: () => {},
	};
	await act(async () => {
		tree.update(<PluginsScreen {...props} />);
	});
	const rekeyed = renderedText(tree);
	expect(rekeyed).toContain("B hub");
	expect(rekeyed).toContain("to manage plugins.");
	expect(rekeyed).not.toContain("demo-plugin");
});
