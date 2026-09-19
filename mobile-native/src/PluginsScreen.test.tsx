// The reconnect-recovery gap #1915's round-1 review found: a transport flap
// keeps the SAME AppwireClient object (only its state moves
// ready -> reconnecting -> ready), so PluginsScreen's own mount effect - the
// only thing that ever called fetchPlugins() - never runs again, and nothing
// else told the store the flap happened. This mounts the real screen (the
// only way to observe the wiring between useConnection's state and the
// store's own connectionChanged) and counts the wire calls it makes.
import type { ComponentProps } from "react";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { WireError, type MarketplaceEntry, type PluginEntry } from "@evener/appwire-client";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { PluginsScreen } from "./PluginsScreen";
import { render, renderedText } from "./renderNative.testkit";

const harness = vi.hoisted(() => ({
	alerts: [] as Array<Array<{ text: string; onPress?: () => void }>>,
	connection: {} as Record<string, unknown>,
}));
vi.mock("react-native", async () => ({
	...(await import("./renderNative.testkit")).nativeModuleMock(),
	Alert: {
		alert: (
			_title: string,
			_message: string,
			buttons: Array<{ text: string; onPress?: () => void }>,
		) => harness.alerts.push(buttons),
	},
}));
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));
vi.mock("./ConnectionProvider", () => ({ useConnection: () => harness.connection }));

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
		fatal: false,
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
	const buttons = harness.alerts.at(-1);
	const remove = buttons?.find((button) => button.text === "Remove");
	if (!remove?.onPress) throw new Error("Remove confirmation was not shown");
	await act(async () => {
		remove.onPress?.();
		await Promise.resolve();
	});
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

it("warns after applied marketplace removal and reconciles the authoritative list", async () => {
	harness.alerts = [];
	const hub = marketplaceClient({
		remove: async () => {
			throw cloneLitterError({ marketplaces: [] });
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
	expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(1);
	expect(renderedText(tree)).toContain("No marketplaces on this hub.");
});

it("keeps an unavailable applied removal fenced across browser remounts", async () => {
	harness.alerts = [];
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
	harness.alerts = [];
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
	const buttons = harness.alerts.at(-1);
	const remove = buttons?.find((button) => button.text === "Remove");
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

it("leaves ordinary marketplace removal failures retryable", async () => {
	harness.alerts = [];
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

it("ignores an applied removal result from a replaced client", async () => {
	harness.alerts = [];
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
	const buttons = harness.alerts.at(-1);
	const remove = buttons?.find((button) => button.text === "Remove");
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
