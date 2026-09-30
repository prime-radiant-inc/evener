import type { ReactTestInstance, ReactTestRenderer } from "react-test-renderer";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { paletteFor } from "../design/tokens";
import { render } from "../renderNative.testkit";
import type { Notice } from "./notices";
import { BoardNotices, NoticeRow } from "./BoardNotices";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

const palette = paletteFor("light");
const list: Notice[] = [
	{ key: "signIn:openai", kind: "signIn", text: "openai sign-in expired", action: "Sign in", providerId: "openai" },
	{
		key: "host:studio",
		kind: "host",
		text: "Studio Mac is offline · 3 sessions",
		action: "Details",
		sourceId: "studio",
	},
	{
		key: "plugin:superpowers@evener",
		kind: "plugin",
		text: "superpowers is broken",
		action: "Plugins",
		pluginId: "superpowers",
		marketplace: "evener",
	},
];

function mount(notices: Notice[], connected = true) {
	const navigation = { navigate: vi.fn() };
	const tree = render(<BoardNotices hubId="hub-1" notices={notices} navigation={navigation} connected={connected} />);
	return { tree, navigation };
}
type Style = Record<string, unknown>;
const flatten = (style: unknown): Style =>
	[typeof style === "function" ? style({ pressed: false }) : style]
		.flat(Number.POSITIVE_INFINITY)
		.reduce<Style>((all, part) => ({ ...all, ...(part ?? {}) }), {});
const textWith = (tree: ReactTestRenderer, content: string) =>
	tree.root.find((node) => node.type === ("Text" as never) && node.props.children === content);
const actionButton = (tree: ReactTestRenderer, label: string): ReactTestInstance =>
	tree.root.find((node) => node.type === ("Pressable" as never) && node.props.accessibilityLabel === label);

it("draws each notice as a row: the amber mark, the sentence in ink, the action in blue, and no box", () => {
	const { tree } = mount(list);
	const rows = tree.root.findAll((node) => node.props.testID === "notice");
	expect(rows).toHaveLength(3);
	for (const row of rows) {
		const mark = row.findByType("SymbolView" as never);
		expect(mark.props.name).toBe("exclamationmark.triangle.fill");
		expect(mark.props.tintColor).toBe(palette.attention);
		expect(flatten(row.props.style).backgroundColor).toBeUndefined();
	}
	expect(flatten(textWith(tree, "Studio Mac is offline · 3 sessions").props.style).color).toBe(palette.inkHi);
	expect(flatten(textWith(tree, "Details").props.style).color).toBe(palette.accentInk);
});

it("labels each action with its notice and gives it a 44pt target", () => {
	const { tree } = mount(list);
	for (const label of [
		"Sign in, openai sign-in expired",
		"Details, Studio Mac is offline · 3 sessions",
		"Plugins, superpowers is broken",
	]) {
		const button = actionButton(tree, label);
		const style = flatten(button.props.style);
		const slop = button.props.hitSlop ?? {};
		expect((style.minHeight as number) + (slop.top ?? 0) + (slop.bottom ?? 0)).toBeGreaterThanOrEqual(44);
	}
});

it("opens the Hub at that provider's sign-in, the Hub at that host for a host, and the Hub at that plugin for a plugin", () => {
	const { tree, navigation } = mount(list);
	act(() => actionButton(tree, "Sign in, openai sign-in expired").props.onPress());
	// Ruling 25: the Hub opens at the provider's sign-in, its home under the page.
	expect(navigation.navigate).toHaveBeenLastCalledWith("Hub", {
		screen: "Providers",
		params: { hubId: "hub-1", focus: "openai", signIn: true },
		initial: false,
		// A Providers page already in the Hub's stack takes the link: a pushed
		// detail over it pops, rather than a second Providers page stacking.
		pop: true,
	});
	act(() => actionButton(tree, "Details, Studio Mac is offline · 3 sessions").props.onPress());
	// Ruling 25: the Hub opens at that host, with its home under the page.
	expect(navigation.navigate).toHaveBeenLastCalledWith("Hub", {
		screen: "Hosts",
		params: { hubId: "hub-1", focus: "studio" },
		initial: false,
	});
	act(() => actionButton(tree, "Plugins, superpowers is broken").props.onPress());
	// Ruling 25: the Hub opens at Plugins with that plugin's detail.
	expect(navigation.navigate).toHaveBeenLastCalledWith("Hub", {
		screen: "Plugins",
		params: { hubId: "hub-1", focus: { plugin: "superpowers", marketplace: "evener" } },
		initial: false,
	});
});

it("draws nothing when there are no notices", () => {
	expect(mount([]).tree.toJSON()).toBeNull();
});

it("draws a notice row without an action when it has none", () => {
	const tree = render(<NoticeRow text="Update needed" />);
	expect(tree.root.findAll((node) => node.type === ("Pressable" as never))).toHaveLength(0);
	expect(textWith(tree, "Update needed")).toBeTruthy();
});

it("hides each notice's action while the hub is out of reach, keeping the sentence", () => {
	const { tree } = mount(list, false);
	const rows = tree.root.findAll((node) => node.props.testID === "notice");
	expect(rows).toHaveLength(3);
	// Ruling 21: the action needs the hub, so it hides while it's unreachable.
	expect(rows.flatMap((row) => row.findAll((node) => node.type === ("Pressable" as never)))).toHaveLength(0);
	expect(textWith(tree, "superpowers is broken")).toBeTruthy();
});
