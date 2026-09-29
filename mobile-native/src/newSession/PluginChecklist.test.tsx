import type { PluginPreviewResponse } from "@evener/appwire-client";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { createNewSessionStore } from "../newSession";
import { palettes } from "../design/tokens";
import { pressable, render, renderedText } from "../renderNative.testkit";
import { sheetContext, TestSheet } from "./newSessionTestUtils";
import { PluginChecklist } from "./PluginChecklist";

vi.mock("../board/connectionStatus", async (importOriginal) => ({
	...(await importOriginal<typeof import("../board/connectionStatus")>()),
	useConnectionStatusText: () => null,
}));
vi.mock("../ConnectionProvider", () => ({ useConnection: () => ({ state: "ready", fatal: false }) }));
vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

/** Past usePluginPreview's 250ms debounce. */
const debounce = () =>
	act(async () => {
		await new Promise((resolve) => setTimeout(resolve, 300));
	});

const candidate = (name: string, marketplace: string | undefined, over: Record<string, unknown> = {}) => ({
	name,
	source: marketplace ? ("installed" as const) : ("dir" as const),
	...(marketplace ? { marketplace } : {}),
	selected: true,
	skillCount: 0,
	agentCount: 0,
	commandCount: 0,
	hookCount: 0,
	mcpCount: 0,
	...over,
});
const preview: PluginPreviewResponse = {
	plugins: [
		candidate("superpowers", "superpowers-marketplace", {
			description: "Skills for disciplined work",
			skillCount: 38,
			agentCount: 3,
		}),
		candidate("superpowers-chrome", "superpowers-marketplace", { selected: false, mcpCount: 1 }),
		candidate("local-tool", undefined, { selected: false }),
	],
	diagnostics: [{ name: "superpowers-chrome", message: "Chrome isn't installed on this host" }],
};

function mount(options: { refuse?: Error; answer?: PluginPreviewResponse; cwd?: string } = {}) {
	const calls: { method: string; params: unknown }[] = [];
	const client = {
		request: async (method: string, params: unknown) => {
			calls.push({ method, params });
			if (options.refuse) throw options.refuse;
			return options.answer ?? preview;
		},
		onNotification: () => () => {},
	};
	const store = createNewSessionStore("hub-1");
	store.setState({
		source: "paradise-park",
		cwd: options.cwd ?? "/Users/jesse/git/evener",
		launchOverrides: { maxRounds: 7 },
	});
	const tree = render(
		<TestSheet value={sheetContext(store, { client: client as never })}>
			<PluginChecklist />
		</TestSheet>,
	);
	const toggle = (name: string) =>
		tree.root.find((node) => String(node.type) === "Switch" && node.props.accessibilityLabel === name);
	return { tree, store, calls, toggle, text: () => renderedText(tree) };
}

it("says it is checking the host's plugins until the first preview lands", async () => {
	const page = mount();
	expect(page.text()).toContain("Checking plugins on paradise-park…");
	await debounce();
	expect(page.text()).not.toContain("Checking plugins on paradise-park…");
});

it("groups plugins by marketplace, as typed, with the rest under Other plugins", async () => {
	const page = mount();
	await debounce();
	const text = page.text();
	expect(text).toContain("superpowers-marketplace");
	expect(text).toContain("Other plugins");
	expect(text.indexOf("superpowers-marketplace")).toBeLessThan(text.indexOf("Other plugins"));
	expect(text).toContain("Skills for disciplined work");
	expect(text).toContain("38 skills · 3 agents");
	expect(text).toContain("1 MCP server");
	expect(text).toContain("Chrome isn't installed on this host");
	expect(page.toggle("superpowers").props.value).toBe(true);
	expect(page.toggle("superpowers-chrome").props.value).toBe(false);
	expect(text).toContain("1 of 3 on. Plugins can't be changed after the session starts.");
});

it("turns one plugin on, all of them, or none, keeping the other overrides", async () => {
	const page = mount();
	await debounce();
	act(() => page.toggle("local-tool").props.onValueChange(true));
	expect(page.store.getState().launchOverrides).toEqual({
		maxRounds: 7,
		enabledPlugins: ["superpowers", "local-tool"],
	});
	act(() => pressable(page.tree, "None")?.props.onPress());
	expect(page.store.getState().launchOverrides.enabledPlugins).toEqual([]);
	act(() => pressable(page.tree, "All")?.props.onPress());
	expect(page.store.getState().launchOverrides.enabledPlugins).toEqual([
		"superpowers",
		"superpowers-chrome",
		"local-tool",
	]);
	await debounce();
	expect(page.text()).toContain("3 of 3 on.");
});

it("marks a plugin whose selection blocks the start", async () => {
	const page = mount({
		answer: { ...preview, selectionErrors: [{ name: "superpowers-chrome", reason: "needs Chrome on this host" }] },
	});
	await debounce();
	expect(page.text()).toContain("needs Chrome on this host");
});

it("filters by name, description or marketplace", async () => {
	const page = mount();
	await debounce();
	act(() => page.tree.root.findByProps({ accessibilityLabel: "Search plugins" }).props.onChangeText("disciplined"));
	expect(page.text()).toContain("superpowers");
	expect(page.text()).not.toContain("local-tool");
});

it("shows a failed preview's message with no button", async () => {
	const page = mount({ refuse: new Error("the plugin cache is locked") });
	await debounce();
	expect(page.text()).toContain("the plugin cache is locked");
	expect(pressable(page.tree, "Retry plugin preview")).toBeUndefined();
	expect(page.text()).not.toMatch(/Retry|Refresh|Reconnect/);
});

it("says plugins wait for a project, rather than checking forever", async () => {
	const page = mount({ cwd: "" });
	await debounce();
	expect(page.calls).toEqual([]);
	expect(page.text()).toContain("Plugins are listed once a project is chosen.");
	expect(page.text()).not.toContain("Checking plugins");
});

it("shows the preview's diagnostics that belong to no listed plugin, in attention ink", async () => {
	const page = mount({
		answer: {
			...preview,
			diagnostics: [
				...(preview.diagnostics ?? []),
				{ message: "installed and bundled plugins are unavailable" },
				{ name: "broken", message: "invalid manifest" },
			],
		},
	});
	await debounce();
	const lines = page.tree.root.findAll(
		(node) =>
			String(node.type) === "Text" &&
			["installed and bundled plugins are unavailable", "broken: invalid manifest"].includes(node.props.children),
	);
	expect(lines).toHaveLength(2);
	for (const line of lines)
		expect(line.props.style).toEqual(
			expect.arrayContaining([expect.objectContaining({ color: palettes.light.attentionInk })]),
		);
	// A row's own warning stays on its row, once.
	expect(page.text().split("Chrome isn't installed on this host")).toHaveLength(2);
});
