import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import {
	createHubWriteGate,
	createMarketplacesStore,
	createPluginsStore,
} from "@evener/appwire-client/state/extensions";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { render, renderedText } from "../renderNative.testkit";
import type { HubRoutes } from "./hubSheetContext";
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
	const hub = new FakeClient("ready");
	hub.on("evener/marketplace/list", () => ({
		marketplaces: [{ name: "acme", source: { kind: "github", repo: "acme/plugins" }, lastUpdated: 1 }],
	}));
	hub.on("evener/marketplace/browse", () => ({ name: "acme", plugins: [] }));
	hub.on("evener/plugin/list", () => ({ plugins: [] }));
	const client = hub as unknown as ConversationClientLike;
	const gate = createHubWriteGate();
	const marketplaces = createMarketplacesStore(client, gate);
	void marketplaces.getState().fetchMarketplaces();
	return {
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
	};
}

/** The Plugins page's half: publishes `slot`, or nothing. */
function Publisher({ slot }: { slot: PluginsScreenSlot }) {
	usePublishPluginsScreen(slot);
	return null;
}

function page(slot: PluginsScreenSlot | null, navigation: { goBack: () => void; popTo: () => void }, pushed = true) {
	return (
		<PluginsScreenSlotProvider>
			{slot ? <Publisher slot={slot} /> : null}
			{pushed ? (
				<MarketplacePage
					route={{
						key: "Marketplace",
						name: "Marketplace",
						params: { hubId: "hub-1", name: "acme", segment: "browse" },
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
