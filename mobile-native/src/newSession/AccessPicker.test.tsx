import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { createNewSessionStore } from "../newSession";
import { render, renderedText, settle } from "../renderNative.testkit";
import { AccessPicker } from "./AccessPicker";
import { sheetContext, TestSheet } from "./newSessionTestUtils";

vi.mock("../board/connectionStatus", async (importOriginal) => ({
	...(await importOriginal<typeof import("../board/connectionStatus")>()),
	useConnectionStatusText: () => null,
}));
vi.mock("../ConnectionProvider", () => ({ useConnection: () => ({ state: "ready", fatal: false }) }));
vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

/** Access for paradise-park's evener, whose hub default is `defaults`. */
async function mount(defaults: Record<string, unknown>, launchOverrides: Record<string, unknown> = {}, refuse = false) {
	const calls: { method: string; params: unknown }[] = [];
	const client = {
		request: async (method: string, params: unknown) => {
			calls.push({ method, params });
			if (refuse) throw new Error("hub away");
			return { effective: defaults, layers: {}, provenance: {} };
		},
		onNotification: () => () => {},
	};
	const store = createNewSessionStore("hub-1");
	store.setState({ source: "paradise-park", cwd: "/Users/jesse/git/evener", launchOverrides });
	const tree = render(
		<TestSheet value={sheetContext(store, { client: client as never })}>
			<AccessPicker />
		</TestSheet>,
	);
	await settle();
	const level = (label: string) =>
		tree.root.findAll(
			(node) => node.props.accessibilityRole === "button" && node.props.accessibilityLabel?.startsWith(`${label}, `),
		)[0];
	const network = () =>
		tree.root.findAll((node) => String(node.type) === "Switch" && node.props.accessibilityLabel === "Network")[0];
	return { tree, store, calls, level, network, text: () => renderedText(tree) };
}

it("lists the four levels, checked on the hub's default for the project on the chosen host", async () => {
	const page = await mount({ sandbox: "workspace-write" });
	expect(page.calls[0]?.params).toMatchObject({ host: "paradise-park", method: "evener/launch/resolve" });
	expect(page.level("Full access")?.props.accessibilityLabel).toBe(
		"Full access, No sandbox: reads and writes anywhere",
	);
	expect(
		["Full access", "Workspace write", "Read-only", "Restricted"].map(
			(label) => page.level(label)?.props.accessibilityState.selected,
		),
	).toEqual([false, true, false, false]);
	expect(page.text()).toContain("Access is fixed once the session starts.");
});

it("sets a level, and choosing the hub's own default follows the hub again", async () => {
	const page = await mount({ sandbox: "workspace-write" }, { maxRounds: 7 });
	act(() => page.level("Read-only")?.props.onPress());
	expect(page.store.getState().launchOverrides).toEqual({ maxRounds: 7, sandbox: "read-only" });
	expect(page.level("Read-only")?.props.accessibilityState.selected).toBe(true);
	act(() => page.level("Workspace write")?.props.onPress());
	expect(page.store.getState().launchOverrides).toEqual({ maxRounds: 7 });
});

it("keeps any choice as the session's own while the hub's default is unknown", async () => {
	const page = await mount({}, {}, true);
	act(() => page.level("Full access")?.props.onPress());
	expect(page.store.getState().launchOverrides).toEqual({ sandbox: "off" });
});

it("offers Network only inside a sandbox, on unless set otherwise", async () => {
	const open = await mount({});
	expect(open.network()).toBeUndefined();
	const sandboxed = await mount({ sandbox: "workspace-write" });
	expect(sandboxed.network()?.props.value).toBe(true);
	expect(sandboxed.text()).toContain("Lets the session's commands reach the internet");
	act(() => sandboxed.network()?.props.onValueChange(false));
	expect(sandboxed.store.getState().launchOverrides).toEqual({ sandboxNet: false });
	const hubOff = await mount({ sandbox: "read-only", sandboxNet: false });
	expect(hubOff.network()?.props.value).toBe(false);
});

it("checks nothing, and leaves Network out, until the hub says its default", async () => {
	const page = await mount({}, {}, true);
	expect(
		["Full access", "Workspace write", "Read-only", "Restricted"].map(
			(label) => page.level(label)?.props.accessibilityState.selected,
		),
	).toEqual([false, false, false, false]);
	expect(page.network()).toBeUndefined();
	act(() => page.level("Read-only")?.props.onPress());
	expect(page.level("Read-only")?.props.accessibilityState.selected).toBe(true);
	expect(page.network()?.props.value).toBe(true);
});

it("lets Network go back to following the hub", async () => {
	const page = await mount({ sandbox: "workspace-write" });
	act(() => page.network()?.props.onValueChange(false));
	expect(page.store.getState().launchOverrides).toEqual({ sandboxNet: false });
	act(() => page.network()?.props.onValueChange(true));
	expect(page.store.getState().launchOverrides).toEqual({});
	const hubOff = await mount({ sandbox: "read-only", sandboxNet: false });
	act(() => hubOff.network()?.props.onValueChange(true));
	expect(hubOff.store.getState().launchOverrides).toEqual({ sandboxNet: true });
	act(() => hubOff.network()?.props.onValueChange(false));
	expect(hubOff.store.getState().launchOverrides).toEqual({});
});

it("keeps Network as the session's own while the hub's default is unknown", async () => {
	const page = await mount({}, { sandbox: "read-only" }, true);
	act(() => page.network()?.props.onValueChange(true));
	expect(page.store.getState().launchOverrides).toEqual({ sandbox: "read-only", sandboxNet: true });
});

it("drops Network when the session leaves the sandbox", async () => {
	const page = await mount({ sandbox: "workspace-write" }, { sandbox: "read-only", sandboxNet: false });
	act(() => page.level("Full access")?.props.onPress());
	expect(page.store.getState().launchOverrides).toEqual({ sandbox: "off" });
});
