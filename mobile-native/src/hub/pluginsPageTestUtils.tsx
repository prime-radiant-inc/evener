// Screen-level tests for the marketplace removal outcomes the browser cannot
// hold on its own: the PluginsPage-owned no-repeat guard fences a
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
import { type ReactTestRenderer } from "react-test-renderer";
import { vi } from "vitest";
import { type MarketplaceEntry, type MarketplaceAddParams, type PluginEntry } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import type { ConversationClientLike } from "../conversation/services/conversation";
import { AddMarketplace } from "../MarketplaceBrowser";
import { render, testIDSuffixes } from "../renderNative.testkit";

/** Why the marketplace views and the Plugins page lead with an error, by reason. */
export function problems(tree: ReactTestRenderer): string[] {
	return [...testIDSuffixes(tree, "marketplace-problem-"), ...testIDSuffixes(tree, "plugins-problem-")];
}

export const marketplace: MarketplaceEntry = {
	name: "acme",
	source: { kind: "github", repo: "acme/plugins" },
	lastUpdated: 1,
};

/** The Marketplaces segment's rows for `name`: each reads as the name, then
 * its source. */
export function marketplaceRows(tree: ReturnType<typeof render>, name: string) {
	return tree.root.findAll(
		(node) =>
			node.props.accessibilityRole === "button" &&
			typeof node.props.accessibilityLabel === "string" &&
			node.props.accessibilityLabel.startsWith(`${name}, `),
	);
}

export function marketplaceRow(tree: ReturnType<typeof render>, name: string) {
	const rows = marketplaceRows(tree, name);
	if (rows.length !== 1) throw new Error(`expected one ${name} row, found ${rows.length}`);
	return rows[0];
}

// ---------------------------------------------------------------------------
// The reconnect-recovery suite (#1915's round-1 review gap): a transport
// flap keeps the SAME AppwireClient object (only its state moves
// ready -> reconnecting -> ready), so PluginsPage's own mount effect -
// the only thing that ever called fetchPlugins() - never runs again, and
// nothing else told the store the flap happened. These mount the real
// screen (the only way to observe the wiring between useConnection's
// state and the store's own connectionChanged) and count the wire calls
// it makes.
// ---------------------------------------------------------------------------

/** A plugins client: every method call is recorded in `methods`, every
 * `evener/plugin/list` answers with `plugins`, and `evener/marketplace/list`
 * (the marketplace segments' own read) answers with an empty list - shaped for
 * whichever surface a test mounts. */
export function pluginsClient(plugins: PluginEntry[]) {
	const methods: string[] = [];
	const client = Object.assign(new FakeClient("ready"), {
		request: async (method: string) => {
			methods.push(method);
			if (method === "evener/marketplace/list") return { marketplaces: [] };
			// A hub from before the update check.
			if (method === "evener/plugin/checkUpdates") throw new Error("method not found");
			return { plugins };
		},
		onNotification: () => () => {},
	} as Omit<ConversationClientLike, "state" | "onReady" | "onStateChange">) as ConversationClientLike;
	return { client, methods };
}

export const plugin: PluginEntry = {
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

export function mountAdd(onClose = vi.fn(), onAdd: (params: MarketplaceAddParams) => Promise<void> = async () => {}) {
	const tree = render(
		<AddMarketplace
			client={pluginsClient([]).client}
			connectionState="ready"
			hubName="Work hub"
			ready
			canUseConnection={() => true}
			onClose={onClose}
			onAdd={onAdd}
		/>,
	);
	return { tree, onClose };
}
