import type { ModelDescriptor } from "@evener/appwire-client";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { createNewSessionStore } from "../newSession";
import { pressable, render, renderedText, textOf } from "../renderNative.testkit";
import { ModelPicker } from "./ModelPicker";
import { NewSessionProvider, type NewSessionRoutes } from "./newSessionContext";
import { sheetContext } from "./newSessionTestUtils";

vi.mock("../board/connectionStatus", async (importOriginal) => ({
	...(await importOriginal<typeof import("../board/connectionStatus")>()),
	useConnectionStatusText: () => null,
}));
vi.mock("../ConnectionProvider", () => ({ useConnection: () => ({ state: "ready", fatal: false }) }));
vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

const glm: ModelDescriptor = {
	provider: "lunaroute",
	model: "glm-5.3-vision",
	displayName: "GLM 5.3 Vision",
	contextWindow: 200_000,
	inputCostPerMillion: 0.6,
	outputCostPerMillion: 2.2,
	supportsVision: true,
	supportsTools: true,
	warnings: ["Slow on long contexts"],
};
const qwen: ModelDescriptor = { provider: "ollama", model: "qwen3-coder:30b", supportsTools: true };
const kimi: ModelDescriptor = { provider: "lunaroute", model: "kimi-k2", displayName: "Kimi K2" };
const extra = (n: number): ModelDescriptor => ({ provider: "ollama", model: `extra-${n}` });

function mount(state: { model?: ModelDescriptor | null; recentModels?: ModelDescriptor[]; loadingModels?: boolean }) {
	const store = createNewSessionStore("hub-1");
	const models = [glm, kimi, qwen, ...[1, 2, 3, 4, 5].map(extra)];
	store.setState({ models, recentModels: [], model: null, ...state });
	const navigation = { goBack: vi.fn() };
	const tree = render(
		<NewSessionProvider value={sheetContext(store)}>
			<ModelPicker
				navigation={navigation as unknown as NativeStackScreenProps<NewSessionRoutes, "Model">["navigation"]}
				route={{ key: "Model", name: "Model", params: undefined }}
			/>
		</NewSessionProvider>,
	);
	const titles = () => tree.root.findAll((node) => node.props.testID === "model-title").map(textOf);
	const sections = () => tree.root.findAll((node) => node.props.testID === "section-title").map(textOf);
	return { tree, store, navigation, titles, sections };
}

it("puts Hub default first, checked while no model is chosen", () => {
	const picker = mount({});
	expect(picker.titles()[0]).toBe("Hub default");
	expect(pressable(picker.tree, "Hub default")?.props.accessibilityState).toMatchObject({ selected: true });
	const chosen = mount({ model: glm });
	expect(pressable(chosen.tree, "Hub default")?.props.accessibilityState).toMatchObject({ selected: false });
	expect(pressable(chosen.tree, "GLM 5.3 Vision")?.props.accessibilityState).toMatchObject({ selected: true });
});

it("lists up to five recent models before one group per provider", () => {
	const picker = mount({ recentModels: [qwen, kimi, extra(1), extra(2), extra(3), extra(4)] });
	expect(picker.sections()).toEqual(["Recent", "lunaroute", "ollama"]);
	expect(picker.titles().slice(1, 7)).toEqual([
		"qwen3-coder:30b",
		"Kimi K2",
		"extra-1",
		"extra-2",
		"extra-3",
		"GLM 5.3 Vision",
	]);
});

it("shows each model's context and price, its notes, and what it can do", () => {
	const picker = mount({});
	const row = pressable(picker.tree, "GLM 5.3 Vision");
	if (!row) throw new Error("no GLM row");
	expect(textOf(row)).toContain("200K context · $0.60 in · $2.20 out per M");
	expect(textOf(row)).toContain("Slow on long contexts");
	const glyphs = row.findAll((node) => String(node.type) === "SymbolView" && node.props.accessibilityLabel);
	expect(glyphs.map((glyph) => [glyph.props.name, glyph.props.accessibilityLabel])).toEqual([
		["eye", "Sees images"],
		["wrench.and.screwdriver", "Uses tools"],
	]);
	const plain = pressable(picker.tree, "Kimi K2");
	expect(plain?.findAll((node) => String(node.type) === "SymbolView" && node.props.accessibilityLabel)).toEqual([]);
});

it("chooses a model, or the hub's default, and goes back to the form", () => {
	const picker = mount({ model: glm });
	act(() => pressable(picker.tree, "Kimi K2")?.props.onPress());
	expect(picker.store.getState().model).toMatchObject({ provider: "lunaroute", model: "kimi-k2" });
	expect(picker.navigation.goBack).toHaveBeenCalledTimes(1);
	act(() => pressable(picker.tree, "Hub default")?.props.onPress());
	expect(picker.store.getState().model).toBeNull();
	expect(picker.navigation.goBack).toHaveBeenCalledTimes(2);
});

it("filters the models by the search", () => {
	const picker = mount({ recentModels: [qwen] });
	act(() => picker.tree.root.findByProps({ accessibilityLabel: "Search models" }).props.onChangeText("glm"));
	expect(picker.titles()).toEqual(["Hub default", "GLM 5.3 Vision"]);
});

it("shows quiet rows while the host's models load", () => {
	const picker = mount({ loadingModels: true });
	act(() => picker.store.setState({ models: [] }));
	expect(picker.tree.root.findAll((node) => node.props.testID === "model-skeleton").length).toBeGreaterThan(0);
});

it("says when the host's models couldn't be loaded, and keeps Hub default", () => {
	const picker = mount({});
	act(() =>
		picker.store.setState({
			models: [],
			modelError: "Couldn't load this host's models. The hub's default model still works.",
		}),
	);
	expect(renderedText(picker.tree)).toContain("Couldn't load this host's models. The hub's default model still works.");
	expect(picker.titles()).toEqual(["Hub default"]);
});
