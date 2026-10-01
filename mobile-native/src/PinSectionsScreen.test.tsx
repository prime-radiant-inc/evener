// Screen-level test for a pinned section's list: it follows the hub while a
// conversation opened from it covers the screen, and reads the change when
// the person comes back.
import type { ComponentProps } from "react";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { completeSession, wireSnapshot } from "@evener/appwire-client/testing/navigation";
import { PinnedSectionScreen } from "./PinSectionsScreen";
import { render, renderedText, screenConnection } from "./renderNative.testkit";

const harness = vi.hoisted(() => ({
	connection: {} as Record<string, unknown>,
	focused: true,
	kv: new Map<string, string>(),
}));
vi.mock("react-native", async () => ({
	...(await import("./renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));
vi.mock("@react-navigation/native", async () => {
	const { useEffect } = await import("react");
	return {
		useFocusEffect: (effect: () => void | (() => void)) =>
			useEffect(() => (harness.focused ? effect() : undefined), [effect, harness.focused]),
		useIsFocused: () => harness.focused,
	};
});
vi.mock("./ConnectionProvider", () => ({ useConnection: () => harness.connection }));
vi.mock("expo-sqlite/kv-store", () => ({
	Storage: {
		getItemSync: (key: string) => harness.kv.get(key) ?? null,
		setItemSync: (key: string, value: string) => void harness.kv.set(key, value),
		removeItemSync: (key: string) => void harness.kv.delete(key),
		getAllKeysSync: () => [...harness.kv.keys()],
	},
}));
vi.mock("expo-crypto", () => ({ randomUUID: () => "uuid" }));

const props = {
	route: { params: { hubId: "hub-1", sectionId: "s1", title: "Pins" } },
	navigation: { navigate: () => {}, setParams: () => {} },
} as unknown as ComponentProps<typeof PinnedSectionScreen>;

it("reads a change the hub announced while a conversation covered the section", async () => {
	const hub = new FakeClient("ready");
	let sessions = [completeSession({ ref: "local:a", title: "Alpha" })];
	let revision = 1;
	hub.on("evener/navigation/read", (params) => {
		const { resource } = params as { resource: string };
		if (resource === "pin_catalog")
			return wireSnapshot(params as never, { pin_sections: [{ id: "s1", name: "Pins", count: 1 }] });
		if (resource === "pin_section") return wireSnapshot(params as never, { sessions }, `"v${revision}"`, revision);
		throw new Error(`unexpected read ${resource}`);
	});
	harness.focused = true;
	harness.connection = screenConnection(hub, "ready");
	const tree = render(<PinnedSectionScreen {...props} />);
	const show = async (focused: boolean) => {
		harness.focused = focused;
		harness.connection = screenConnection(hub, "ready");
		await act(async () => tree.update(<PinnedSectionScreen {...props} />));
	};
	await act(async () => {});
	expect(renderedText(tree)).toContain("Alpha");
	expect(renderedText(tree)).not.toContain("Beta");

	await show(false);
	sessions = [...sessions, completeSession({ ref: "local:b", title: "Beta" })];
	revision = 2;
	await act(async () => {
		hub.emitNotification({
			method: "evener/navigation/invalidated",
			params: {
				generationId: "generation_test",
				sequence: 1,
				targets: [{ kind: "pin_section", sectionId: "s1", revision: 2 }],
			},
		} as never);
	});
	await show(true);
	await act(async () => {});
	expect(renderedText(tree)).toContain("Beta");
	tree.unmount();
});
