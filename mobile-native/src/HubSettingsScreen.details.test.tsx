import type { SettingsOverviewResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import type { ComponentProps } from "react";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { HubSettingsScreen } from "./HubSettingsScreen";
import { pressable, render, screenConnection, textOf } from "./renderNative.testkit";

const harness = vi.hoisted(() => ({ connection: {} as Record<string, unknown> }));
vi.mock("react-native", async () => (await import("./renderNative.testkit")).nativeModuleMock());
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));
vi.mock("./ConnectionProvider", () => ({ useConnection: () => harness.connection }));
vi.mock("@react-navigation/native", async () => {
	const { useEffect } = await import("react");
	return {
		useFocusEffect: (effect: () => void | (() => void)) => useEffect(effect, [effect]),
		useIsFocused: () => true,
	};
});

const props = {
	route: { params: { hubId: "hub-1" } },
	navigation: { navigate: () => {} },
} as unknown as ComponentProps<typeof HubSettingsScreen>;

const customAgent = { name: "fixture-agent", editPath: "editor:///srv/evener/agents/custom-fixture.md" };
const builtInAgent = { name: "builtin-fixture-agent" };
const mcpServer = { name: "fixture-server", transport: "stdio", status: "ready" };
const overview = {
	hub: {
		version: "0.9.731-test",
		commit: "a10b20c30d40e50f60a70b80c90d10e20f30a40b5",
		listenAddr: "[::1]:49152",
		spawnTimeout: "37s",
		bearerTokenAge: "age-from-hub",
		runDir: "/var/run/evener/runtime-fixture",
		daemonIdleTimeoutMillis: 3600000,
		pastIndex: {
			path: "~/.local/state/evener/runtime-fixture/past-index.sqlite",
			size: "12.5 MiB",
			count: 0,
			perPage: 37,
		},
	},
	storage: { stateDir: "/srv/evener/runtime-fixture/a-long-directory-name/another-directory/state" },
	agents: [customAgent, builtInAgent],
	mcpDiscovered: { servers: [mcpServer] },
} satisfies SettingsOverviewResponse;

const details = [
	{ field: "version", label: "Version", value: overview.hub.version },
	{ field: "commit", label: "Commit", value: overview.hub.commit },
	{ field: "listenAddr", label: "Listen address", value: overview.hub.listenAddr },
	{ field: "spawnTimeout", label: "Spawn timeout", value: overview.hub.spawnTimeout },
	{ field: "bearerTokenAge", label: "Bearer token age", value: overview.hub.bearerTokenAge },
	{ field: "runDir", label: "Run directory", value: overview.hub.runDir },
	{ field: "stateDir", label: "State directory", value: overview.storage.stateDir },
	{ field: "pastIndex.path", label: "Past session index", value: overview.hub.pastIndex.path },
	{ field: "pastIndex.size", label: "Index size", value: overview.hub.pastIndex.size },
	{ field: "pastIndex.count", label: "Indexed sessions", value: "0" },
	{ field: "pastIndex.perPage", label: "Past results per page", value: "37" },
].map((detail) => ({ ...detail, section: "Runtime and storage" }));

it.each(["fixture-hub-name", overview.hub.version, overview.hub.listenAddr])(
	"keeps summary data selectable: %s",
	async (value) => {
		const response = Promise.withResolvers<SettingsOverviewResponse>();
		const hub = new FakeClient("ready");
		hub.on("evener/settings/overview", () => response.promise);
		harness.connection = {
			...screenConnection(hub, "ready"),
			activeProfile: { id: "hub-1", name: "fixture-hub-name" },
		};
		const tree = render(<HubSettingsScreen {...props} />);
		await act(async () => response.resolve(overview));
		const text = tree.root.find((node) => String(node.type) === "Text" && textOf(node) === value);
		expect(text.props.selectable).toBe(true);
		expect(text.props.numberOfLines).toBeUndefined();
		act(() => tree.unmount());
	},
);

it.each([
	...details,
	{ field: "mcp.transport", label: "Transport", value: "stdio", section: "Discovered MCP servers" },
	{ field: "mcp.status", label: "Status", value: "ready", section: "Discovered MCP servers" },
	{ field: "agent.name", label: customAgent.name, value: customAgent.name, section: "Agents" },
	{ field: "agent.editPath", label: customAgent.name, value: customAgent.editPath, section: "Agents" },
	{ field: "builtInAgent.name", label: builtInAgent.name, value: builtInAgent.name, section: "Agents" },
	{ field: "mcp.name", label: mcpServer.name, value: mcpServer.name, section: "Discovered MCP servers" },
])("keeps $field selectable and accessible without truncating its value", async ({ field, label, value, section }) => {
	const response = Promise.withResolvers<SettingsOverviewResponse>();
	const hub = new FakeClient("ready");
	hub.on("evener/settings/overview", () => response.promise);
	harness.connection = screenConnection(hub, "ready");
	const tree = render(<HubSettingsScreen {...props} />);
	await act(async () => response.resolve(overview));
	const opener = pressable(tree, section);
	if (!opener) throw new Error("Missing details section control");
	act(() => opener.props.onPress());

	// Names locate the detail, but the oracle is the native Text's selection
	// and wrapping props, with the populated wire value reaching VoiceOver too.
	const row = tree.root.find(
		(node) =>
			String(node.type) === "View" &&
			node.props.accessible === true &&
			(node.props.accessibilityLabel === label || node.props.accessibilityLabel?.startsWith(`${label},`)),
	);
	const text = row.find((node) => String(node.type) === "Text" && textOf(node) === value);
	expect(text.props.selectable).toBe(true);
	expect(text.props.numberOfLines).toBeUndefined();
	expect(row.props.accessibilityLabel).toContain(value);
	if (field === "agent.editPath") {
		const machineText = text.findAll(
			(node) =>
				String(node.type) === "Text" && [node.props.style].flat().some((style) => style?.fontFamily === "Menlo"),
		);
		expect(machineText).toHaveLength(1);
	}
});
