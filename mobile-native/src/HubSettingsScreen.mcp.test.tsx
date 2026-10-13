// Screen-level tests for the hub settings screen's discovered-MCP section. The
// hub composes each discovery diagnostic itself - a config parse failure, one
// server's probe failure - and sends it as overview data, not as a rejected
// request, so the screen shows it as the hub wrote it (as the web MCP section
// does). Mocking mirrors HubSettingsScreen.reconnect.test.tsx.
import type { ComponentProps } from "react";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import type { SettingsOverviewResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { HubSettingsScreen } from "./HubSettingsScreen";
import { pressable, render, renderedText, screenConnection as connection } from "./renderNative.testkit";

const harness = vi.hoisted(() => ({ connection: {} as Record<string, unknown> }));
vi.mock("react-native", async () => ({
	...(await import("./renderNative.testkit")).nativeModuleMock(),
	RefreshControl: "RefreshControl",
}));
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));
vi.mock("./ConnectionProvider", () => ({ useConnection: () => harness.connection }));
vi.mock("@react-navigation/native", async () => {
	const { useEffect } = await import("react");
	return {
		useFocusEffect: (effect: () => void | (() => void)) => useEffect(() => effect(), [effect]),
		useIsFocused: () => true,
	};
});
const props = {
	route: { params: { hubId: "hub-1" } },
	navigation: { navigate: () => {} },
} as unknown as ComponentProps<typeof HubSettingsScreen>;

async function renderOverview(overview: SettingsOverviewResponse) {
	const hub = new FakeClient("ready");
	hub.on("evener/settings/overview", () => overview);
	harness.connection = connection(hub, "ready");
	const tree = render(<HubSettingsScreen {...props} />);
	await act(async () => {});
	const section = pressable(tree, "Discovered MCP servers");
	if (!section) throw new Error("no Discovered MCP servers section");
	act(() => section.props.onPress());
	return tree;
}

it("shows the hub's own discovery failure", async () => {
	const tree = await renderOverview({ mcpDiscovered: { error: "mcp-config-diagnostic-from-hub" } });
	expect(renderedText(tree)).toContain("mcp-config-diagnostic-from-hub");
});

it("shows the hub's own probe failure under that server", async () => {
	const tree = await renderOverview({
		mcpDiscovered: {
			servers: [{ name: "remote-api", transport: "http", status: "unreachable", error: "probe-error-from-hub" }],
		},
	});
	expect(renderedText(tree)).toContain("remote-api");
	expect(renderedText(tree)).toContain("probe-error-from-hub");
});
