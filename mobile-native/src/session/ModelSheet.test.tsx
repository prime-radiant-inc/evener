// The model sheet route (spec 8.5), rendered with a host in modelHosts the
// way ConversationScreen provides one. The host's controls are a
// SessionControls-shaped fake whose calls are recorded; only native edges are
// mocked.
import { type ModelListResponse, type Thread, type ThreadCapabilities, WireError } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { act, type ReactTestInstance, type ReactTestRenderer } from "react-test-renderer";
import { afterEach, beforeEach, describe, expect, it, onTestFinished, vi } from "vitest";
import { createConversationService } from "../../../mobile/src/services/conversation";
import { paletteFor } from "../design/tokens";
import type { MobileConversation } from "../projectedRows";
import {
	playedHaptics,
	pressable,
	render,
	renderedText,
	settle,
	textOf,
	unmountMountedTrees,
} from "../renderNative.testkit";
import { markers } from "../markerTestUtils";
import type { Routes } from "../screens";
import { SessionControls } from "../sessionControls";
import { Sheet } from "../sheet/Sheet";
import { sheetKey } from "../sheet/sheetHosts";
import { type ModelHost, ModelSheet, modelHosts } from "./ModelSheet";

const navigation = vi.hoisted(() => ({ goBack: vi.fn(), navigate: vi.fn(), dispatch: vi.fn() }));
const viewport = vi.hoisted(() => ({ fontScale: 1, scrolls: [] as { y: number; animated: boolean }[] }));

vi.mock("react-native", async () => {
	const native = (await import("../renderNative.testkit")).nativeModuleMock();
	const { useImperativeHandle } = await import("react");
	return {
		...native,
		useWindowDimensions: () => ({ width: 390, height: 844, scale: 3, fontScale: viewport.fontScale }),
		SectionList: (props: Parameters<typeof native.SectionList>[0] & { ref?: unknown }) => {
			useImperativeHandle(props.ref as never, () => ({
				getScrollResponder: () => ({
					scrollTo: (options: { y: number; animated: boolean }) => viewport.scrolls.push(options),
				}),
			}));
			return native.SectionList(props);
		},
	};
});
vi.mock("@react-navigation/native", () => ({
	useNavigation: () => navigation,
	usePreventRemove: () => {},
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

const palette = paletteFor("light");
const HUB = "hub-1";
const REF = "local:s1";

const CATALOG: ModelListResponse = {
	data: [
		{
			provider: "anthropic",
			model: "claude-sonnet-5",
			displayName: "Claude Sonnet 5",
			contextWindow: 200_000,
			inputCostPerMillion: 3,
			outputCostPerMillion: 15,
			supportsVision: true,
		},
		{ provider: "anthropic", model: "claude-haiku-5", displayName: "Claude Haiku 5", supportsVision: false },
		{
			provider: "lunaroute",
			model: "glm-5.3-vision",
			displayName: "GLM 5.3 Vision",
			contextWindow: 128_000,
			supportsVision: true,
			warnings: ["served from one region"],
		},
	],
	recent: [{ provider: "lunaroute", model: "glm-5.3-vision" }],
};

const CAPABILITIES = { changeModel: true, changeVisionModel: true } as ThreadCapabilities;

function conversation(over: Partial<MobileConversation> = {}): MobileConversation {
	return {
		ref: REF,
		capabilities: CAPABILITIES,
		modelProvider: "anthropic/claude-sonnet-5",
		reasoningEffort: "high",
		reasoningEffortLevels: ["low", "medium", "high"],
		supportsReasoning: true,
		visionModel: "",
		...over,
	} as unknown as MobileConversation;
}

type ControlsState = ReturnType<SessionControls["getSnapshot"]>;

function fakeControls(calls: string[], over: Partial<ControlsState> = {}) {
	const state: ControlsState = {
		pending: null,
		lastAction: null,
		error: null,
		notice: null,
		catalog: CATALOG,
		loadingModels: false,
		modelError: null,
		...over,
	};
	return {
		subscribe: () => () => {},
		getSnapshot: () => state,
		loadModels: vi.fn(async () => void calls.push("loadModels")),
		changeModel: vi.fn(async (provider: string, model: string) => {
			calls.push(`changeModel:${provider}/${model}`);
			return true;
		}),
		changeVisionModel: vi.fn(async (provider: string, model: string) => {
			calls.push(`changeVisionModel:${provider}/${model}`);
			return true;
		}),
		setVisionModel: vi.fn(async (value: string) => {
			calls.push(`setVisionModel:${value}`);
			return true;
		}),
		setReasoningEffort: vi.fn(async (level: string) => {
			calls.push(`setReasoningEffort:${level}`);
			return true;
		}),
	};
}

let owner: object | undefined;
const mounted: ReactTestRenderer[] = [];

function provide(session: MobileConversation, state: Partial<ControlsState> = {}, over: Partial<ModelHost> = {}) {
	const calls: string[] = [];
	const controls = fakeControls(calls, state);
	const host: ModelHost = {
		session,
		controls: controls as unknown as SessionControls,
		ready: true,
		toast: vi.fn((message) => void calls.push(`toast:${message.text}`)),
		...over,
	};
	if (owner) modelHosts.release(sheetKey(HUB, REF), owner);
	owner = {};
	modelHosts.provide(sheetKey(HUB, REF), owner, host);
	navigation.goBack.mockImplementation(() => void calls.push("goBack"));
	return { host, controls, calls };
}

function sheet(setting: "model" | "vision" = "model"): ReactTestRenderer {
	const props = {
		route: { key: "model-sheet", name: "ModelSheet", params: { hubId: HUB, ref: REF, setting } },
		navigation,
	} as unknown as NativeStackScreenProps<Routes, "ModelSheet">;
	const tree = render(<ModelSheet {...props} />);
	mounted.push(tree);
	return tree;
}

const sheetTitle = (tree: ReactTestRenderer): string => tree.root.findByType(Sheet).props.title;
const texts = (tree: ReactTestRenderer) =>
	tree.root.findAll((node) => String(node.type) === "Text").map((node: ReactTestInstance) => textOf(node));

/** The section titles and row titles, in the order the list shows them. */
function listed(tree: ReactTestRenderer): string[] {
	const list = tree.root.find((node) => String(node.type) === "SectionList");
	return list
		.findAll((node) => node.props.testID === "section-title" || node.props.testID === "model-title")
		.map((node) => textOf(node));
}

beforeEach(() => {
	navigation.goBack.mockReset();
	navigation.navigate.mockReset();
	viewport.fontScale = 1;
	viewport.scrolls.length = 0;
});

afterEach(() => {
	unmountMountedTrees();
	mounted.length = 0;
	if (owner) modelHosts.release(sheetKey(HUB, REF), owner);
	owner = undefined;
});

describe("choosing a model (spec 8.5)", () => {
	it("lists recent models first, then each provider's", () => {
		provide(conversation());
		const tree = sheet();
		expect(sheetTitle(tree)).toEqual(expect.stringMatching(/\S/));
		expect(listed(tree)).toEqual([
			"Recent",
			"GLM 5.3 Vision",
			"anthropic",
			"Claude Sonnet 5",
			"Claude Haiku 5",
			"lunaroute",
			"GLM 5.3 Vision",
		]);
	});

	it("says each model's context and price beneath its name, in tabular ink-mid", () => {
		provide(conversation());
		const tree = sheet();
		const detail = tree.root.findAll(
			(node) => String(node.type) === "Text" && textOf(node) === "200K context · $3 in · $15 out per M",
		)[0];
		if (!detail) throw new Error("no detail line");
		expect(detail.props.style).toMatchObject({
			fontSize: 13,
			lineHeight: 18,
			color: palette.inkMid,
			fontVariant: ["tabular-nums"],
		});
		expect(texts(tree)).toContain("128K context");
		expect(texts(tree)).toContain("served from one region");
	});

	it("checks the current model", () => {
		provide(conversation());
		const tree = sheet();
		const current = pressable(tree, "Claude Sonnet 5");
		expect(current?.props.accessibilityState).toMatchObject({ selected: true });
		expect(current?.findAll((node) => node.props.name === "checkmark")).toHaveLength(1);
		expect(pressable(tree, "Claude Haiku 5")?.props.accessibilityState).toMatchObject({ selected: false });
	});

	it("changes the model, closes, and the session says it applies from the next turn", async () => {
		const { calls } = provide(conversation());
		const tree = sheet();
		act(() => pressable(tree, "Claude Haiku 5")?.props.onPress());
		await settle();
		expect(calls).toEqual([
			"changeModel:anthropic/claude-haiku-5",
			"goBack",
			"toast:Model changed. Applies from the next turn.",
		]);
	});

	it("stays open, and says nothing, when the change didn't go through", async () => {
		const { calls, controls } = provide(conversation());
		controls.changeModel.mockImplementation(async () => {
			calls.push("changeModel");
			return false;
		});
		const tree = sheet();
		act(() => pressable(tree, "Claude Haiku 5")?.props.onPress());
		await settle();
		expect(calls).toEqual(["changeModel"]);
	});

	it("searches the models", () => {
		provide(conversation());
		const tree = sheet();
		const search = tree.root.find((node) => String(node.type) === "TextInput");
		expect(search.props.accessibilityLabel).toEqual(expect.stringMatching(/\S/));
		act(() => search.props.onChangeText("haiku"));
		expect(listed(tree)).toEqual(["anthropic", "Claude Haiku 5"]);
		act(() => search.props.onChangeText("nothing like it"));
		expect(listed(tree)).toEqual([]);
		expect(markers(tree, "empty-state")).toEqual(["noMatch"]);
	});

	it("shows why the models didn't load, and stand-in rows while they load", () => {
		provide(conversation(), { catalog: null, modelError: "The hub refused." });
		expect(texts(sheet())).toContain("The hub refused.");
		act(() => mounted.pop()?.unmount());
		provide(conversation(), { catalog: null, loadingModels: true });
		const tree = sheet();
		expect(tree.root.findAll((node) => node.props.testID === "model-skeleton").length).toBeGreaterThan(0);
		expect(tree.root.findAll((node) => String(node.type) === "ActivityIndicator")).toEqual([]);
	});

	it("loads the models when the screen hasn't yet", () => {
		const { calls } = provide(conversation(), { catalog: null });
		sheet();
		expect(calls).toEqual(["loadModels"]);
	});

	it("shows why a change failed", () => {
		provide(conversation(), { lastAction: "changeModel", error: "Could not confirm the action: refused" });
		expect(renderedText(sheet())).toContain("Could not confirm the action: refused");
	});
});

describe.each([
	{ action: "model", setting: "model", label: "Claude Haiku 5", method: "thread/model/set", closes: true },
	{
		action: "vision model",
		setting: "vision",
		label: "Claude Sonnet 5",
		method: "thread/vision-model/set",
		closes: true,
	},
	{ action: "vision off", setting: "vision", label: "Off", method: "thread/vision-model/set", closes: true },
	{ action: "effort", setting: "model", label: "Low", method: "thread/reasoning-effort/set", closes: false },
] as const)("rejected $action changes", ({ setting, label, method, closes }) => {
	it.each([1, 2, 3.1])(
		"reveals each refusal after scrolling and permits recovery at text scale %s",
		async (fontScale) => {
			viewport.fontScale = fontScale;
			const session = conversation({ reasoningEffortLevels: ["minimal", "low", "medium", "high", "xhigh"] });
			const thread: Thread = {
				id: "thread",
				sessionId: "session",
				preview: "",
				ephemeral: false,
				modelProvider: "anthropic",
				createdAt: 1,
				updatedAt: 1,
				status: { type: "idle" },
				cwd: "/test",
				cliVersion: "test",
				source: "local",
				turns: [],
				evener: {
					ref: REF,
					instanceId: "instance",
					queue: { revision: 0 },
					capabilities: {
						send: true,
						steer: false,
						interrupt: false,
						compact: true,
						clear: false,
						forkFromTurn: false,
						shutdown: true,
						changeModel: true,
						changeVisionModel: true,
						sharedNotes: false,
						queue: false,
						goal: false,
						rename: true,
					},
				},
			};
			const client = new FakeClient("ready");
			client.on("thread/read", () => ({ thread }));
			client.on("thread/unsubscribe", () => ({}));
			let response = Promise.withResolvers<Record<string, never>>();
			client.on(method, () => response.promise);
			const service = createConversationService(client);
			await service.open(REF);
			const controls = new SessionControls(
				service,
				async () => {},
				() => {},
				() => true,
				() => session,
				() => true,
				CATALOG,
			);
			onTestFinished(() => {
				controls.dispose();
				service.close();
			});
			const { host } = provide(session, {}, { controls });
			const tree = sheet(setting);
			expect(viewport.scrolls).toEqual([]);

			for (const attempt of [0, 1, 2]) {
				response = Promise.withResolvers<Record<string, never>>();
				const settled = new Promise<void>((resolve) => {
					const unsubscribe = controls.subscribe(() => {
						if (controls.getSnapshot().pending !== null) return;
						unsubscribe();
						resolve();
					});
				});
				const choice = pressable(tree, label);
				if (!choice) throw new Error("Missing model action");
				// The native edge records reveal requests from a list the user has
				// scrolled away from its header. It does not simulate UIKit geometry.
				viewport.scrolls.length = 0;
				await act(async () => {
					choice.props.onPress();
					if (attempt < 2) response.reject(new WireError("rejection-sentinel", -32603));
					else response.resolve({});
					await settled;
				});
				expect(client.calls.filter((call) => call.method === method)).toHaveLength(attempt + 1);
				if (attempt < 2) {
					expect(controls.getSnapshot().error).not.toBeNull();
					expect(viewport.scrolls).toEqual([{ y: 0, animated: true }]);
					const list = tree.root.find((node) => String(node.type) === "SectionList");
					const headerContent = list.findAll(
						(node) => node.props.accessibilityRole === "alert" || node.props.accessibilityRole === "radiogroup",
					);
					expect(headerContent[0]?.props.accessibilityRole).toBe("alert");
					expect(navigation.goBack).not.toHaveBeenCalled();
					expect(host.toast).not.toHaveBeenCalled();
					expect(pressable(tree, label)?.props.disabled).toBe(false);
				} else {
					expect(controls.getSnapshot().error).toBeNull();
					expect(viewport.scrolls).toEqual([]);
					expect(tree.root.findAll((node) => node.props.accessibilityRole === "alert")).toHaveLength(0);
					expect(navigation.goBack).toHaveBeenCalledTimes(closes ? 1 : 0);
				}
			}
		},
	);
});

describe("effort", () => {
	it("shows the current choice among the model's supported effort levels", () => {
		provide(conversation());
		const tree = sheet();
		const segments = tree.root.findAll((node) => node.props.accessibilityRole === "radio");
		expect(segments).toHaveLength(3);
		// The shared Segmented marks its choice with `checked`, as a radio does.
		expect(segments.map((node) => node.props.accessibilityState.checked)).toEqual([false, false, true]);
		const high = segments[2];
		if (!high) throw new Error("no High");
		expect([high.props.style].flat()[0]).toMatchObject({ backgroundColor: palette.surface });
		expect(high.findByType("Text" as never).props.style).toMatchObject({ color: palette.inkHi });
	});

	it.each([1, 2, 3.1])("scrolls stacked effort and errors with the models at text scale %s", (fontScale) => {
		viewport.fontScale = fontScale;
		const levels = ["minimal", "low", "medium", "high", "xhigh"];
		provide(conversation({ reasoningEffortLevels: levels }), { modelError: "catalog-error-sentinel" });
		const tree = sheet();
		const list = tree.root.find((node) => String(node.type) === "SectionList");
		const choices = list.findAll((node) => node.props.accessibilityRole === "radio");
		expect(choices).toHaveLength(levels.length);
		expect(list.findByProps({ accessibilityRole: "radiogroup" }).props.style.flexDirection).toBe("column");
		expect(list.findAll((node) => node.props.accessibilityRole === "alert")).toHaveLength(1);
		expect(list.findAll((node) => node.props.testID === "model-title").length).toBeGreaterThan(0);
		// Search stays available while the growing header scrolls away.
		expect(tree.root.findAll((node) => String(node.type) === "TextInput")).toHaveLength(1);
		expect(list.findAll((node) => String(node.type) === "TextInput")).toHaveLength(0);
	});

	it("sets the effort", () => {
		const { calls } = provide(conversation());
		const tree = sheet();
		playedHaptics.length = 0;
		act(() => pressable(tree, "Low")?.props.onPress());
		expect(calls).toEqual(["setReasoningEffort:low"]);
		// Spec 16.6: a selection tick on a segment.
		expect(playedHaptics).toEqual(["selection"]);
	});

	it("is hidden for a model with no levels", () => {
		provide(conversation({ reasoningEffortLevels: [], supportsReasoning: false }));
		const tree = sheet();
		expect(tree.root.findAll((node) => node.props.accessibilityRole === "radio")).toEqual([]);
		expect(tree.root.findAll((node) => node.props.accessibilityRole === "radiogroup")).toEqual([]);
	});
});

describe("the vision model (ruling 17)", () => {
	it("lists only models that can see images, with Session model and Off, and no effort", () => {
		provide(conversation({ visionModel: "off" }));
		const tree = sheet("vision");
		// Titled apart from the session model's sheet.
		expect(sheetTitle(tree)).toEqual(expect.stringMatching(/\S/));
		expect(sheetTitle(tree)).not.toBe(sheetTitle(sheet()));
		expect(listed(tree)).toEqual([
			"Session model",
			"Off",
			"Recent",
			"GLM 5.3 Vision",
			"anthropic",
			"Claude Sonnet 5",
			"lunaroute",
			"GLM 5.3 Vision",
		]);
		expect(pressable(tree, "Off")?.props.accessibilityState).toMatchObject({ selected: true });
		expect(pressable(tree, "Session model")?.props.accessibilityState).toMatchObject({ selected: false });
		expect(tree.root.findAll((node) => node.props.accessibilityRole === "radio")).toEqual([]);
	});

	it("sets the vision model, and closes", async () => {
		const { calls } = provide(conversation());
		const tree = sheet("vision");
		act(() => pressable(tree, "Claude Sonnet 5")?.props.onPress());
		await settle();
		expect(calls).toEqual(["changeVisionModel:anthropic/claude-sonnet-5", "goBack"]);
	});

	it("uses the session model or turns vision off", async () => {
		const { calls } = provide(conversation({ visionModel: "lunaroute/glm-5.3-vision" }));
		const tree = sheet("vision");
		act(() => pressable(tree, "Off")?.props.onPress());
		await settle();
		expect(calls).toEqual(["setVisionModel:off", "goBack"]);
	});
});

describe("when nothing can change", () => {
	it("holds every choice while the session can't take one", () => {
		provide(conversation(), {}, { ready: false });
		const tree = sheet();
		expect(pressable(tree, "Claude Haiku 5")?.props.disabled).toBe(true);
		expect(pressable(tree, "Low")?.props.disabled).toBe(true);
	});

	it("holds the models when the session can't change its model, and keeps effort", () => {
		provide(conversation({ capabilities: { ...CAPABILITIES, changeModel: false } }));
		const tree = sheet();
		expect(pressable(tree, "Claude Haiku 5")?.props.disabled).toBe(true);
		expect(pressable(tree, "Low")?.props.disabled).toBe(false);
	});

	it("stays open while the hub is away, holding every choice and standing in for the models", () => {
		provide(conversation(), {}, { controls: null, ready: false });
		const tree = sheet();
		expect(navigation.goBack).not.toHaveBeenCalled();
		expect(tree.root.findAll((node) => node.props.testID === "model-skeleton").length).toBeGreaterThan(0);
		expect(pressable(tree, "Low")?.props.disabled).toBe(true);
	});

	it("leaves when its session's screen is gone", () => {
		provide(conversation());
		sheet();
		act(() => modelHosts.release(sheetKey(HUB, REF), owner as object));
		expect(navigation.goBack).toHaveBeenCalled();
	});
});
