import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { createNewSessionStore } from "../newSession";
import { markedNodes, markerOf } from "../markerTestUtils";
import { render, renderedText, settle, textOf } from "../renderNative.testkit";
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
	/** The footer under a setting: whether it knows the hub's default, and its words. */
	const footer = (field: string) => {
		const [node] = markedNodes(tree, `default-${field}`);
		return { hubDefault: node ? markerOf(node, `default-${field}`) : null, text: node ? textOf(node) : "" };
	};
	return { tree, store, calls, segments, choose, footer, text: () => renderedText(tree) };
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
	expect(page.footer("contextStrategy")).toEqual({ hubDefault: "known", text: expect.stringContaining("compact") });
	expect(page.footer("maxSubagentDepth")).toEqual({ hubDefault: "known", text: expect.stringContaining("2") });
	// No limit reads in words, not as -1.
	expect(page.footer("maxRounds").hubDefault).toBe("known");
	expect(page.footer("maxRounds").text).not.toContain("-1");
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
	// No limit is the last segment.
	expect(page.segments("Max turns").map(([, lit]) => lit)).toEqual([false, false, false, true]);
	page.choose("Max turns", "Default");
	page.choose("Context strategy", "Default");
	expect(page.store.getState().launchOverrides).toEqual({ sandbox: "read-only", maxSubagentDepth: 3 });
});

it("lights no segment for a value it doesn't offer, and says what the session uses", async () => {
	const page = await mount({ maxSubagentDepth: 2 }, { maxSubagentDepth: 4 });
	expect(page.segments("Max subagent depth").some(([, lit]) => lit)).toBe(false);
	const { hubDefault, text } = page.footer("maxSubagentDepth");
	expect(hubDefault).toBe("known");
	expect(text).toContain("4");
	expect(text).toContain("2");
});

it("speaks of the hub's default without a value until the hub says it", async () => {
	const page = await mount(null, { maxRounds: 250 });
	expect(page.footer("maxRounds")).toEqual({ hubDefault: "unknown", text: expect.stringContaining("250") });
	// A setting left to the hub still says Default follows the hub.
	expect(page.footer("maxSubagentDepth")).toEqual({ hubDefault: "unknown", text: expect.stringMatching(/\S/) });
});
