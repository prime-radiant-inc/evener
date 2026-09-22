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
import type { PluginEntry } from "@evener/appwire-client";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { AddMarketplace } from "./MarketplaceBrowser";
import { createPluginMutationGate } from "./pluginMutationGate";
import { PluginsScreen } from "./PluginsScreen";
import { render, renderedText } from "./renderNative.testkit";

const harness = vi.hoisted(() => ({ connection: {} as Record<string, unknown> }));
vi.mock("react-native", async () => (await import("./renderNative.testkit")).nativeModuleMock());
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
