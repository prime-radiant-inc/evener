import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import {
	createHubWriteGate,
	createMarketplacesStore,
	createPluginsStore,
} from "@evener/appwire-client/state/extensions";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { WireError } from "@evener/appwire-client";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { alertRequests, render, renderedText } from "../renderNative.testkit";
import type { HubRoutes } from "./hubSheetContext";
import { SearchField } from "../sheet/SearchField";
import { MarketplacePage } from "./MarketplacePage";
import { type PluginsScreenSlot, PluginsScreenSlotProvider, usePublishPluginsScreen } from "./pluginsScreenSlot";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("../ConnectionProvider", () => ({
	useConnection: () => ({ state: "ready", error: null, activeProfile: null }),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));

function slotFor(hubId: string, ready = true): PluginsScreenSlot {
	return hubSlot(hubId, ready).slot;
}

/** A Plugins page's publication over its own fake hub, which lists acme. */
function hubSlot(hubId: string, ready = true, listed = true, catalog: { name: string }[] = []) {
	const hub = new FakeClient("ready");
	hub.on("evener/marketplace/list", () => ({
		marketplaces: listed
			? ["acme", "beta"].map((name) => ({
					name,
					source: { kind: "github" as const, repo: `${name}/plugins` },
					lastUpdated: 1,
				}))
			: [],
	}));
	hub.on("evener/marketplace/browse", (params: { name: string }) => ({ name: params.name, plugins: catalog }));
	hub.on("evener/plugin/list", () => ({ plugins: [] }));
	const client = hub as unknown as ConversationClientLike;
	const gate = createHubWriteGate();
	const marketplaces = createMarketplacesStore(client, gate);
	void marketplaces.getState().fetchMarketplaces();
	const slot: PluginsScreenSlot = {
		hubId,
		client,
		hubName: "Work hub",
		installed: createPluginsStore(client, gate),
		marketplaces,
		gate,
		ready,
		canUseConnection: () => ready,
		appliedRemovalNames: new Set(),
		onAppliedRemoval: () => true,
		onRemovedMarketplace: () => {},
		marketplaceWarning: null,
	};
	return { slot, hub };
}

/** The Plugins page's half: publishes `slot`, or nothing. */
function Publisher({ slot }: { slot: PluginsScreenSlot }) {
	usePublishPluginsScreen(slot);
	return null;
}

function page(
	slot: PluginsScreenSlot | null,
	navigation: { goBack: () => void; popTo: () => void },
	pushed = true,
	name = "acme",
) {
	return (
		<PluginsScreenSlotProvider>
			{slot ? <Publisher slot={slot} /> : null}
			{pushed ? (
				<MarketplacePage
					route={{
						key: "Marketplace",
						name: "Marketplace",
						params: { hubId: "hub-1", name, segment: "browse" },
					}}
					navigation={navigation as unknown as NativeStackScreenProps<HubRoutes, "Marketplace">["navigation"]}
				/>
			) : null}
		</PluginsScreenSlotProvider>
	);
}

/** Mounts the Plugins page's publication, then pushes the page over it, the
 * order the Hub's stack keeps. */
async function pushedOver(slot: PluginsScreenSlot, navigation: { goBack: () => void; popTo: () => void }) {
	const tree = render(page(slot, navigation, false));
	await act(async () => {});
	await act(async () => tree.update(page(slot, navigation)));
	await act(async () => {});
	return tree;
}

it("shows its marketplace from the Plugins page's publication for its hub", async () => {
	const navigation = { goBack: vi.fn(), popTo: vi.fn() };
	const slot = slotFor("hub-1");
	const tree = await pushedOver(slot, navigation);
	expect(renderedText(tree)).toContain("Update source");
	expect(navigation.goBack).not.toHaveBeenCalled();
});

it("goes back when no Plugins page publishes", async () => {
	const navigation = { goBack: vi.fn(), popTo: vi.fn() };
	render(page(null, navigation));
	await act(async () => {});
	expect(navigation.goBack).toHaveBeenCalledTimes(1);
});

it("goes back when the publication is another hub's", async () => {
	const navigation = { goBack: vi.fn(), popTo: vi.fn() };
	const tree = await pushedOver(slotFor("hub-2"), navigation);
	expect(navigation.goBack).toHaveBeenCalledTimes(1);
	expect(renderedText(tree)).not.toContain("Update source");
});

it("follows the Plugins page's newer publication", async () => {
	const navigation = { goBack: vi.fn(), popTo: vi.fn() };
	const slot = slotFor("hub-1");
	const tree = await pushedOver(slot, navigation);
	const update = () => tree.root.findByProps({ accessibilityLabel: "Update source" });
	expect(update().props.disabled).toBe(false);
	await act(async () => tree.update(page({ ...slot, ready: false }, navigation)));
	expect(update().props.disabled).toBe(true);
});

it("runs no Remove confirmed after the Plugins page moved to a new connection", async () => {
	const navigation = { goBack: vi.fn(), popTo: vi.fn() };
	const before = hubSlot("hub-1");
	const tree = await pushedOver(before.slot, navigation);
	await act(async () => tree.root.findByProps({ accessibilityLabel: "Remove marketplace" }).props.onPress());
	const confirm = alertRequests.at(-1)?.buttons?.find((button) => button.text === "Remove");
	// A reconnect hands the Plugins page a new client; it publishes new stores
	// under the page still showing.
	const after = hubSlot("hub-1");
	await act(async () => tree.update(page(after.slot, navigation)));
	await act(async () => {
		confirm?.onPress?.();
	});
	await act(async () => {});
	for (const hub of [before.hub, after.hub])
		expect(hub.calls.map((call) => call.method)).not.toContain("evener/marketplace/remove");
});

it("shows nothing on the new connection's page from a write the old one left out", async () => {
	const navigation = { goBack: vi.fn(), popTo: vi.fn() };
	const before = hubSlot("hub-1");
	let fail: (reason: Error) => void = () => {};
	before.hub.on(
		"evener/marketplace/refresh",
		() =>
			new Promise((_resolve, reject) => {
				fail = reject;
			}),
	);
	const tree = await pushedOver(before.slot, navigation);
	await act(async () => tree.root.findByProps({ accessibilityLabel: "Update source" }).props.onPress());
	await act(async () => tree.update(page(hubSlot("hub-1").slot, navigation)));
	await act(async () => fail(new Error("upstream 502")));
	await act(async () => {});
	expect(renderedText(tree)).not.toContain("Could not confirm the change");
});

it("goes back once when its marketplace leaves the hub's list, however often the list changes after", async () => {
	const navigation = { goBack: vi.fn(), popTo: vi.fn() };
	const gone = hubSlot("hub-1", true, false);
	await pushedOver(gone.slot, navigation);
	await act(async () => {
		await gone.slot.marketplaces.getState().fetchMarketplaces();
	});
	expect(navigation.goBack).toHaveBeenCalledTimes(1);
});

it("goes back when the Plugins page underneath goes", async () => {
	const navigation = { goBack: vi.fn(), popTo: vi.fn() };
	const tree = await pushedOver(slotFor("hub-1"), navigation);
	expect(navigation.goBack).not.toHaveBeenCalled();
	await act(async () => tree.update(page(null, navigation)));
	expect(navigation.goBack).toHaveBeenCalledTimes(1);
});

it("starts each marketplace clean: another marketplace's filter doesn't carry over", async () => {
	const navigation = { goBack: vi.fn(), popTo: vi.fn() };
	const slot = hubSlot("hub-1", true, true, [{ name: "tool" }]).slot;
	const tree = await pushedOver(slot, navigation);
	await act(async () => tree.root.findByType(SearchField).props.onChangeText("zzz"));
	await act(async () => tree.update(page(slot, navigation, true, "beta")));
	await act(async () => {});
	expect(tree.root.findByType(SearchField).props.value).toBe("");
});

it("shows the Plugins page's removal warning on the marketplace's own page", async () => {
	const navigation = { goBack: vi.fn(), popTo: vi.fn() };
	const warning = "Marketplace removed; clone cleanup failed. Remove the leftover clone files manually.";
	const tree = await pushedOver({ ...slotFor("hub-1"), marketplaceWarning: warning }, navigation);
	const shown = tree.root.findByType(MarketplacePage);
	expect(shown.findAll((node) => node.props.children === warning).length).toBeGreaterThan(0);
});

it("opens an installed plugin back on the Plugins page", async () => {
	const navigation = { goBack: vi.fn(), popTo: vi.fn() };
	const { slot, hub } = hubSlot("hub-1", true, true, [{ name: "tool" }]);
	hub.on("evener/plugin/list", () => ({
		plugins: [
			{
				plugin: "tool",
				marketplace: "acme",
				version: "1",
				installPath: "/p/tool",
				enabled: true,
				autoUpgrade: false,
				broken: false,
				installedAt: 1,
				lastUpdated: 1,
			},
		],
	}));
	void slot.installed.getState().fetchPlugins();
	const tree = await pushedOver(slot, navigation);
	await act(async () => tree.root.findByProps({ accessibilityLabel: "Open tool from acme" }).props.onPress());
	expect(navigation.popTo).toHaveBeenCalledWith("Plugins", {
		hubId: "hub-1",
		focus: { plugin: "tool", marketplace: "acme" },
	});
});

it.each([
	["stood", () => ({ marketplaces: [] }), "onRemovedMarketplace"],
	[
		"stood but left a clone behind",
		() => {
			throw new WireError("clone cleanup failed", -32603, {
				evenerErrorInfo: "marketplaceUnregisteredCloneRemains",
				applied: [],
			});
		},
		"onAppliedRemoval",
	],
] as const)(
	"still tells the Plugins page about a removal that %s after its page went",
	async (_name, answer, report) => {
		const navigation = { goBack: vi.fn(), popTo: vi.fn() };
		const { slot, hub } = hubSlot("hub-1");
		let settle: () => void = () => {};
		hub.on(
			"evener/marketplace/remove",
			() =>
				new Promise((resolve, reject) => {
					settle = () => {
						try {
							resolve(answer());
						} catch (error) {
							reject(error);
						}
					};
				}),
		);
		const reported = { onRemovedMarketplace: vi.fn(), onAppliedRemoval: vi.fn(() => true) };
		const watched = { ...slot, ...reported };
		const tree = await pushedOver(watched, navigation);
		await act(async () => tree.root.findByProps({ accessibilityLabel: "Remove marketplace" }).props.onPress());
		const confirm = alertRequests.at(-1)?.buttons?.find((button) => button.text === "Remove");
		await act(async () => {
			confirm?.onPress?.();
		});
		// The page goes back while the removal is still out.
		await act(async () => tree.update(page(watched, navigation, false)));
		await act(async () => settle());
		await act(async () => {});
		expect(reported[report]).toHaveBeenCalledTimes(1);
		expect(reported[report].mock.calls[0]?.[0]).toBe("acme");
	},
);
