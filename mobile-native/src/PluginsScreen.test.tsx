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
import { PluginsScreen } from "./PluginsScreen";
import { render, renderedText } from "./renderNative.testkit";

const harness = vi.hoisted(() => ({ connection: {} as Record<string, unknown> }));
vi.mock("react-native", async () => (await import("./renderNative.testkit")).nativeModuleMock());
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));
vi.mock("./ConnectionProvider", () => ({ useConnection: () => harness.connection }));

/** A plugins client: every method call is recorded in `methods`, every
 * `evener/plugin/list` answers with `plugins`. */
function pluginsClient(plugins: PluginEntry[]) {
	const methods: string[] = [];
	const client = {
		request: async (method: string) => {
			methods.push(method);
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
