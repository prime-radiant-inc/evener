import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { createNewSessionStore } from "../newSession";
import { render, renderedText, settle } from "../renderNative.testkit";
import { MoreOptions } from "./MoreOptions";
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

async function mount(defaults: Record<string, unknown> | null, launchOverrides: Record<string, unknown> = {}) {
	const calls: { method: string; params: unknown }[] = [];
	const client = {
		request: async (method: string, params: unknown) => {
			calls.push({ method, params });
			if (!defaults) throw new Error("hub away");
			return { effective: defaults, layers: {}, provenance: {} };
		},
		onNotification: () => () => {},
	};
	const store = createNewSessionStore("hub-1");
	store.setState({ source: "paradise-park", cwd: "/Users/jesse/git/evener", launchOverrides });
	const tree = render(
		<TestSheet value={sheetContext(store, { client: client as never })}>
			<MoreOptions />
		</TestSheet>,
	);
	await settle();
	/** The segments of the control VoiceOver names `group`, as [label, lit]. */
	const segments = (group: string) =>
		tree.root
			.find((node) => node.props.accessibilityRole === "radiogroup" && node.props.accessibilityLabel === group)
			.findAll((node) => node.props.accessibilityRole === "radio" && node.props.onPress)
			.map((segment) => [segment.props.accessibilityLabel, segment.props.accessibilityState.checked]);
	const choose = (group: string, label: string) =>
		act(() =>
			tree.root
				.find((node) => node.props.accessibilityRole === "radiogroup" && node.props.accessibilityLabel === group)
				.find((node) => node.props.accessibilityRole === "radio" && node.props.accessibilityLabel === label)
				.props.onPress(),
		);
	return { tree, store, calls, segments, choose, text: () => renderedText(tree) };
}

it("offers the three settings, each on Default until set", async () => {
	const page = await mount({ contextStrategy: "compact", maxSubagentDepth: 2, maxRounds: -1 });
	expect(page.calls[0]?.params).toMatchObject({ host: "paradise-park", method: "evener/launch/resolve" });
	const text = page.text();
	for (const label of ["Context strategy", "Max subagent depth", "Max turns"]) expect(text).toContain(label);
	expect(page.segments("Context strategy")).toEqual([
		["Default", true],
		["compact", false],
		["session-log", false],
		["ooda", false],
	]);
	expect(page.segments("Max subagent depth").map(([label]) => label)).toEqual(["Default", "1", "2", "3", "5"]);
	expect(page.segments("Max turns").map(([label]) => label)).toEqual(["Default", "100", "500", "No limit"]);
});

it("names the hub's default under each", async () => {
	const page = await mount({ contextStrategy: "compact", maxSubagentDepth: 2, maxRounds: -1 });
	const text = page.text();
	expect(text).toContain("The hub's default is compact.");
	expect(text).toContain("The hub's default is 2.");
	expect(text).toContain("The hub's default is no limit.");
	expect(text).toContain(
		"Everything else uses the hub's launch defaults. Edit them from the Hub, under Launch defaults.",
	);
});

it("sets each value, and Default removes it", async () => {
	const page = await mount({ maxRounds: 500 }, { sandbox: "read-only" });
	page.choose("Context strategy", "ooda");
	page.choose("Max subagent depth", "3");
	page.choose("Max turns", "No limit");
	expect(page.store.getState().launchOverrides).toEqual({
		sandbox: "read-only",
		contextStrategy: "ooda",
		maxSubagentDepth: 3,
		maxRounds: -1,
	});
	expect(page.segments("Max turns").find(([, lit]) => lit)?.[0]).toBe("No limit");
	page.choose("Max turns", "Default");
	page.choose("Context strategy", "Default");
	expect(page.store.getState().launchOverrides).toEqual({ sandbox: "read-only", maxSubagentDepth: 3 });
});

it("lights no segment for a value it doesn't offer, and says what the session uses", async () => {
	const page = await mount({ maxSubagentDepth: 2 }, { maxSubagentDepth: 4 });
	expect(page.segments("Max subagent depth").some(([, lit]) => lit)).toBe(false);
	expect(page.text()).toContain("This session uses 4. The hub's default is 2.");
});

it("speaks of the hub's default without a value until the hub says it", async () => {
	const page = await mount(null, { maxRounds: 250 });
	expect(page.text()).toContain("This session uses 250.");
	expect(page.text()).not.toContain("The hub's default is");
	expect(page.text()).toContain("Default uses the hub's default.");
});
