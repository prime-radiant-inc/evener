// The model sheet route (spec 8.5), rendered with a host in modelHosts the
// way ConversationScreen provides one. The host's controls are a
// SessionControls-shaped fake whose calls are recorded; only native edges are
// mocked.
import {
	type ModelControlSnapshot,
	type ModelListResponse,
	type Thread,
	type ThreadCapabilities,
	WireError,
} from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { act, type ReactTestInstance, type ReactTestRenderer } from "react-test-renderer";
import { afterEach, beforeEach, describe, expect, it, onTestFinished, vi } from "vitest";
import { DraftDocument } from "../draftDocument";
import { DraftRepository } from "../draftRepository";
import { createConversationService } from "../conversation/services/conversation";
import { openSqliteSyncDouble } from "../sqliteSync.testkit";
import { PAST_CAPABILITIES as SHARED_PAST_CAPABILITIES } from "../modelControlTestUtils";
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
import { type ActionOutcome, SessionControls } from "../sessionControls";
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
	const snapshot = {
		authority: { instanceId: "instance-a", bootGeneration: "7" },
		revision: 1,
		active: {
			modelProvider: "anthropic",
			model: "claude-sonnet-5",
			reasoningEffortLevels: ["low", "medium", "high"],
			supportsReasoning: true,
		},
	};
	return {
		ref: REF,
		capabilities: CAPABILITIES,
		modelProvider: "anthropic/claude-sonnet-5",
		model: "anthropic/claude-sonnet-5",
		modelControlState: { kind: "ready", snapshot },
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
		changeModel: vi.fn(async (provider: string, model: string): Promise<ActionOutcome> => {
			calls.push(`changeModel:${provider}/${model}`);
			return { disposition: "accepted", notice: "Model updated." };
		}),
		changeVisionModel: vi.fn(async (provider: string, model: string): Promise<ActionOutcome> => {
			calls.push(`changeVisionModel:${provider}/${model}`);
			return { disposition: "accepted", notice: "Vision model updated." };
		}),
		setVisionModel: vi.fn(async (value: string): Promise<ActionOutcome> => {
			calls.push(`setVisionModel:${value}`);
			return { disposition: "accepted", notice: "Vision model updated." };
		}),
		setReasoningEffort: vi.fn(async (level: string): Promise<ActionOutcome> => {
			calls.push(`setReasoningEffort:${level}`);
			return { disposition: "accepted", notice: "Reasoning effort updated." };
		}),
	};
}

let owner: object | undefined;
const mounted: ReactTestRenderer[] = [];

/** The fence the screen owning a pick installs (screens.tsx): the store is
 * still open on the binding the controls were made for. A resume the pick
 * causes can move the store to another identity; the promise carries the
 * answer anyway. */
function screenFence(refs: { store: () => string; madeOn: string }) {
	return () => refs.store() === refs.madeOn;
}

function provide(session: MobileConversation, state: Partial<ControlsState> = {}, over: Partial<ModelHost> = {}) {
	const calls: string[] = [];
	const controls = fakeControls(calls, state);
	const host: ModelHost = {
		session,
		controls: controls as unknown as SessionControls,
		ready: true,
		toast: vi.fn(),
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

	it("checks the accepted pending target from the shared model-control snapshot", () => {
		provide(
			conversation({
				model: "claude-sonnet-5",
				modelProvider: "anthropic",
				modelControlState: {
					kind: "ready",
					snapshot: {
						authority: { instanceId: "instance-a", bootGeneration: "7" },
						revision: 2,
						active: {
							modelProvider: "anthropic",
							model: "claude-sonnet-5",
							reasoningEffortLevels: ["low", "medium", "high"],
							supportsReasoning: true,
						},
						desired: {
							requestSequence: 2,
							clientMutationId: "request-2",
							modelProvider: "anthropic",
							model: "claude-haiku-5",
							lifecycle: "pending",
						},
					},
				},
			}),
		);
		const tree = sheet();
		expect(pressable(tree, "Claude Sonnet 5")?.props.accessibilityState).toMatchObject({ selected: false });
		expect(pressable(tree, "Claude Haiku 5")?.props.accessibilityState).toMatchObject({ selected: true });
	});

	it("disables model choices while model-control authority is recovering", () => {
		const { calls } = provide(
			conversation({
				modelControlState: {
					kind: "recovering",
					lastSafeDisplay: { modelProvider: "anthropic", model: "claude-sonnet-5" },
				},
			}),
		);

		const choice = pressable(sheet(), "Claude Haiku 5");

		expect(choice?.props.accessibilityState).toMatchObject({ disabled: true });
		act(() => choice?.props.onPress?.());
		expect(calls).toEqual([]);
	});

	it("checks the last safe model while model-control authority is recovering", () => {
		// A recovery's last safe display names the provider AND the model a pick
		// had reached. Rows are keyed by "provider/model", so checking only the
		// provider loses the checkmark for the whole recovery even though the
		// composer still shows the last safe model.
		provide(
			conversation({
				modelControlState: {
					kind: "recovering",
					lastSafeDisplay: { modelProvider: "anthropic", model: "claude-haiku-5" },
				},
			}),
		);
		const tree = sheet();
		expect(pressable(tree, "Claude Haiku 5")?.props.accessibilityState).toMatchObject({ selected: true });
		expect(pressable(tree, "Claude Sonnet 5")?.props.accessibilityState).toMatchObject({ selected: false });
	});

	it("checks a saved record whose provider field holds the whole id", () => {
		// A saved session's legacy read has no separate model id, so its display
		// target carries the whole id in both fields. Joining those would repeat
		// the id's own path segments and match no row.
		provide(
			conversation({
				status: { type: "closed" },
				modelControlState: {
					kind: "unsupported",
					canonicalDisplay: { modelProvider: "anthropic/claude-sonnet-5", model: "anthropic/claude-sonnet-5" },
				},
			}),
		);
		const tree = sheet();
		expect(pressable(tree, "Claude Sonnet 5")?.props.accessibilityState).toMatchObject({ selected: true });
		expect(pressable(tree, "Claude Haiku 5")?.props.accessibilityState).toMatchObject({ selected: false });
	});

	it("changes the model, closes, and announces it once", async () => {
		const { host, calls } = provide(conversation());
		const tree = sheet();
		act(() => pressable(tree, "Claude Haiku 5")?.props.onPress());
		await settle();
		expect(calls).toEqual(["changeModel:anthropic/claude-haiku-5", "goBack"]);
		expect(host.toast).toHaveBeenCalledTimes(1);
	});

	it("closes on the already-selected row without sending, while the session is live", async () => {
		// The shortcut is for a LIVE session: its selected row names the model
		// running now, so re-picking it is a no-op and the sheet just closes. A
		// cold session's selected row can name a choice nobody applied, and that
		// case goes through the resume instead.
		const { calls } = provide(conversation({ status: { type: "idle" } } as Partial<MobileConversation>));
		const tree = sheet();
		const current = pressable(tree, "Claude Sonnet 5");
		expect(current?.props.accessibilityState).toMatchObject({ selected: true });
		act(() => current?.props.onPress?.());
		await settle();
		expect(calls).toEqual(["goBack"]);
	});

	it("stays open, and says nothing, when the change didn't go through", async () => {
		const { calls, controls } = provide(conversation());
		controls.changeModel.mockImplementation(async () => {
			calls.push("changeModel");
			return { disposition: "notSent", message: "" } as const;
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

const PAST_CAPABILITIES = SHARED_PAST_CAPABILITIES;

const THIS_AUTHORITY = { instanceId: "instance-a", bootGeneration: "7" } as const;
const RESUMED_AUTHORITY = { instanceId: "instance-b", bootGeneration: "8" } as const;

function snapshotFor(
	revision: number,
	authority: { instanceId: string; bootGeneration: string },
	desired?: string,
): ModelControlSnapshot {
	return {
		authority,
		revision,
		active: {
			modelProvider: "anthropic",
			model: "claude-sonnet-5",
			reasoningEffortLevels: ["low", "high"],
			supportsReasoning: true,
		},
		...(desired === undefined
			? {}
			: {
					desired: {
						requestSequence: revision,
						clientMutationId: `request-${revision}`,
						modelProvider: "anthropic",
						model: desired,
						lifecycle: "pending" as const,
					},
				}),
	};
}

/** A thread with no daemon behind it: the hub's cold read. */
const COLD_THREAD: Thread = {
	id: "thread-1",
	sessionId: "session-1",
	preview: "",
	ephemeral: false,
	modelProvider: "anthropic/claude-sonnet-5",
	createdAt: 1,
	updatedAt: 1,
	status: { type: "notLoaded" },
	cwd: "/test",
	cliVersion: "test",
	source: "local",
	turns: [],
	evener: {
		ref: REF,
		instanceId: "thread-1",
		capabilities: PAST_CAPABILITIES,
		queue: { revision: 0 },
	},
};

/** The same thread with a daemon behind it, reporting `authority`. */
function liveThread(authority: { instanceId: string; bootGeneration: string }, status: string = "ready"): Thread {
	return {
		...COLD_THREAD,
		status: { type: status },
		evener: {
			ref: REF,
			instanceId: authority.instanceId,
			capabilities: { ...PAST_CAPABILITIES, modelControlVersion: 1 },
			queue: { revision: 0 },
			modelControl: snapshotFor(1, authority),
		},
	};
}

describe("a pick on a cold session (the hub resumes behind it)", () => {
	it("resumes once, sends the resumed daemon's authority, and shows the accepted model", async () => {
		// The resumed daemon's state: what every read after thread/resume reports.
		let live = liveThread(RESUMED_AUTHORITY);
		let resumed = false;
		let refreshed: MobileConversation | null = null;
		const client = new FakeClient("ready");
		client.on("thread/unsubscribe", () => ({}));
		client.on("thread/resume", () => {
			resumed = true;
			return { thread: live };
		});
		client.on("thread/read", () =>
			resumed ? { thread: live, bootGeneration: RESUMED_AUTHORITY.bootGeneration } : { thread: COLD_THREAD },
		);
		client.on("thread/model/set", (params) => {
			const accepted = snapshotFor(2, RESUMED_AUTHORITY, params.model);
			// The daemon keeps the change, so the screen's refresh read sees it.
			live = { ...live, evener: { ...live.evener, modelControl: accepted } };
			return { modelControlVersion: 1, disposition: "accepted" as const, modelControl: accepted };
		});
		const service = createConversationService(client);
		const cold = await service.open(REF);
		// The production cold read: advertised changeModel, no model control.
		expect(cold.modelControlState.kind).toBe("unsupported");
		const controls = new SessionControls(
			service,
			async () => {
				refreshed = await service.open(REF);
			},
			() => {},
			screenFence({ store: () => refreshed?.ref ?? cold.ref, madeOn: cold.ref }),
			() => cold,
			() => true,
			CATALOG,
		);
		onTestFinished(() => {
			controls.dispose();
			service.close();
		});
		provide(cold, {}, { controls });
		const tree = sheet();
		const choice = pressable(tree, "Claude Haiku 5");
		expect(choice?.props.disabled).toBe(false);
		act(() => choice?.props.onPress?.());
		await settle();

		expect(client.calls.filter((call) => call.method === "thread/resume")).toHaveLength(1);
		const setCalls = client.calls.filter((call) => call.method === "thread/model/set");
		expect(setCalls).toHaveLength(1);
		expect(setCalls[0]?.params).toMatchObject({
			ref: REF,
			modelProvider: "anthropic",
			model: "claude-haiku-5",
			expectedInstanceId: RESUMED_AUTHORITY.instanceId,
			expectedBootGeneration: RESUMED_AUTHORITY.bootGeneration,
		});
		// The change went through, so the sheet closed on it.
		expect(navigation.goBack).toHaveBeenCalledTimes(1);

		// The screen's refresh read, which is how the store learns the resumed
		// session and the accepted snapshot, then the sheet it draws.
		if (!refreshed) throw new Error("the sheet's change did not refresh the session");
		provide(refreshed, {}, { controls });
		const after = sheet();
		expect(pressable(after, "Claude Haiku 5")?.props.accessibilityState).toMatchObject({ selected: true });
		expect(pressable(after, "Claude Sonnet 5")?.props.accessibilityState).toMatchObject({ selected: false });
	});

	it("resumes when the session went cold holding a ready snapshot from the daemon that left", async () => {
		// The hub's close frame: a cold status with the past-thread capabilities,
		// and no model control. reducer.ts's "thread/status/changed" branch
		// rewrites status and capabilities and leaves modelControlState alone, so
		// the held ready snapshot still names the departed daemon's boot
		// generation.
		let resumed = false;
		const client = new FakeClient("ready");
		client.on("thread/unsubscribe", () => ({}));
		client.on("thread/resume", () => {
			resumed = true;
			return { thread: liveThread(RESUMED_AUTHORITY) };
		});
		client.on("thread/read", () =>
			resumed
				? { thread: liveThread(RESUMED_AUTHORITY), bootGeneration: RESUMED_AUTHORITY.bootGeneration }
				: { thread: liveThread(THIS_AUTHORITY), bootGeneration: THIS_AUTHORITY.bootGeneration },
		);
		client.on("thread/model/set", () => ({
			modelControlVersion: 1,
			disposition: "accepted" as const,
			modelControl: snapshotFor(2, RESUMED_AUTHORITY, "claude-haiku-5"),
		}));
		const service = createConversationService(client);
		const live = await service.open(REF);
		service.subscribeNotifications(() => {});
		const closed = {
			method: "thread/status/changed" as const,
			params: {
				ref: REF,
				threadId: "thread-1",
				status: { type: "closed" },
				capabilities: PAST_CAPABILITIES,
			},
		};
		// The screen's store folds the same frame for the sheet it draws.
		client.emitNotification(closed);
		const stale = {
			...live,
			status: { type: "closed" },
			capabilities: PAST_CAPABILITIES,
		} as MobileConversation;
		expect(stale.modelControlState).toMatchObject({ kind: "ready" });
		const controls = new SessionControls(
			service,
			async () => {},
			() => {},
			screenFence({ store: () => stale.ref, madeOn: stale.ref }),
			() => stale,
			() => true,
			CATALOG,
		);
		onTestFinished(() => {
			controls.dispose();
			service.close();
		});
		provide(stale, {}, { controls });
		const tree = sheet();
		const choice = pressable(tree, "Claude Haiku 5");
		expect(choice?.props.disabled).toBe(false);
		act(() => choice?.props.onPress?.());
		await settle();

		expect(client.calls.filter((call) => call.method === "thread/resume")).toHaveLength(1);
		const setCalls = client.calls.filter((call) => call.method === "thread/model/set");
		expect(setCalls).toHaveLength(1);
		// The retired authority (instance-a / boot 7) is never sent.
		expect(setCalls[0]?.params).toMatchObject({
			expectedInstanceId: RESUMED_AUTHORITY.instanceId,
			expectedBootGeneration: RESUMED_AUTHORITY.bootGeneration,
		});
		expect(navigation.goBack).toHaveBeenCalledTimes(1);
	});

	it("settles the accepted pick when the resume's reconnect replaces the controls", async () => {
		// resumeThread recycles the transport on purpose (client.ts:432-479:
		// handleSocketLoss, close, retryNow, then the resume RPC). screens.tsx
		// builds SessionControls in a useMemo whose dependency list includes
		// `connected`, so the recycle disposes the controls that started the pick
		// and hands the screen a replacement before the daemon's answer arrives.
		// The acknowledgement still belongs to the retired instance: reporting
		// `false` there leaves the sheet open on a change the daemon accepted.
		let live = liveThread(RESUMED_AUTHORITY);
		let resumed = false;
		const client = new FakeClient("ready");
		client.on("thread/unsubscribe", () => ({}));
		client.on("thread/resume", () => {
			resumed = true;
			return { thread: live };
		});
		client.on("thread/read", () =>
			resumed ? { thread: live, bootGeneration: RESUMED_AUTHORITY.bootGeneration } : { thread: COLD_THREAD },
		);
		client.on("thread/model/set", (params) => {
			const accepted = snapshotFor(2, RESUMED_AUTHORITY, params.model);
			// The daemon keeps the change: the read the reconnected screen takes
			// reports it.
			live = { ...live, evener: { ...live.evener, modelControl: accepted } };
			return { modelControlVersion: 1, disposition: "accepted" as const, modelControl: accepted };
		});
		const service = createConversationService(client);
		const cold = await service.open(REF);
		expect(cold.modelControlState.kind).toBe("unsupported");

		const toast = vi.fn();
		const hostOwner = {};
		owner = hostOwner;
		let controls: SessionControls | null = null;
		// The screen's fence (screens.tsx): the binding the controls' memo
		// captured, retired by the reconnect. The pick's answer rides its
		// promise, so the sheet that started it learns what the daemon said.
		let binding = 1;
		const screen = (connected: boolean) => {
			if (connected) {
				const mine = binding;
				controls = new SessionControls(
					service,
					async () => {},
					() => {},
					() => {
						if (client.state !== "ready") return false;
						return binding === mine;
					},
					() => cold,
					() => true,
					CATALOG,
				);
			} else {
				binding += 1;
				// The memo's cleanup: the pair that started the pick is disposed.
				controls?.dispose();
				controls = null;
			}
			modelHosts.provide(sheetKey(HUB, REF), hostOwner, { session: cold, controls, ready: connected, toast });
		};
		const resume = client.resumeThread.bind(client);
		client.resumeThread = async (ref, options) => {
			// The recycle resumeThread performs: the transport goes down and the
			// screen's `connected` follows it, replacing the controls before the
			// resume RPC goes out.
			client.emitStateChange("reconnecting");
			screen(false);
			client.emitStateChange("ready");
			screen(true);
			return resume(ref, options);
		};
		onTestFinished(() => {
			controls?.dispose();
			service.close();
		});
		screen(true);
		const starter = controls;
		const tree = sheet();
		act(() => pressable(tree, "Claude Haiku 5")?.props.onPress?.());
		await settle();

		// The reconnect replaced the controls that started the pick.
		expect(controls).not.toBe(starter);
		// Exactly one resume and one authority-bound mutation: the replacement
		// never resends what the retired pair already had answered.
		expect(client.calls.filter((call) => call.method === "thread/resume")).toHaveLength(1);
		const setCalls = client.calls.filter((call) => call.method === "thread/model/set");
		expect(setCalls).toHaveLength(1);
		expect(setCalls[0]?.params).toMatchObject({
			ref: REF,
			modelProvider: "anthropic",
			model: "claude-haiku-5",
			expectedInstanceId: RESUMED_AUTHORITY.instanceId,
			expectedBootGeneration: RESUMED_AUTHORITY.bootGeneration,
		});
		// The daemon accepted the pick, so the sheet closed on it and the session
		// announced it once.
		expect(navigation.goBack).toHaveBeenCalledTimes(1);
		expect(toast).toHaveBeenCalledTimes(1);

		// The reconnected screen's own read is what shows the accepted model.
		const resumedSession = await service.open(REF);
		act(() =>
			modelHosts.provide(sheetKey(HUB, REF), hostOwner, {
				session: resumedSession,
				controls,
				ready: true,
				toast,
			}),
		);
		const after = sheet();
		expect(pressable(after, "Claude Haiku 5")?.props.accessibilityState).toMatchObject({ selected: true });
		expect(pressable(after, "Claude Sonnet 5")?.props.accessibilityState).toMatchObject({ selected: false });
	});

	it("confirms a pick whose resume resolved a different ref than the screen's route", async () => {
		// A cold pick's resume can hand back a different identity, and the store
		// follows it. The screen holding the sheet keeps the route it was opened
		// on until the sheet closes (screens.tsx's pendingIdentity), so the
		// store's ref and the route's ref disagree when the daemon answers.
		//
		// The store follows the resume; the sheet's answer rides the pick's
		// promise regardless of where the store landed.
		const RESUMED_REF = "local:resumed";
		let accepted: ModelControlSnapshot | null = null;
		const client = new FakeClient("ready");
		client.on("thread/unsubscribe", () => ({}));
		client.on("thread/resume", () => {
			const thread = liveThread(RESUMED_AUTHORITY);
			return { thread: { ...thread, evener: { ...thread.evener, ref: RESUMED_REF } } };
		});
		client.on("thread/read", (params) => {
			const ref = String((params as { ref: string }).ref);
			// Only the resumed ref carries a daemon; the ref the pick was made on
			// is the cold saved session the hub resumes behind the mutation.
			if (ref !== RESUMED_REF) return { thread: COLD_THREAD };
			const thread = liveThread(RESUMED_AUTHORITY);
			return {
				thread: {
					...thread,
					evener: {
						...thread.evener,
						ref,
						modelControl: accepted ?? snapshotFor(1, RESUMED_AUTHORITY),
					},
				},
				bootGeneration: RESUMED_AUTHORITY.bootGeneration,
			};
		});
		client.on("thread/model/set", (params) => {
			const modelControl = snapshotFor(2, RESUMED_AUTHORITY, params.model);
			return { modelControlVersion: 1, disposition: "accepted" as const, modelControl };
		});
		const service = createConversationService(client);
		const cold = await service.open(REF);
		expect(cold.modelControlState.kind).toBe("unsupported");
		// The composer a person typed in before opening the picker.
		const draftStorage = openSqliteSyncDouble();
		const repository = new DraftRepository(draftStorage.port);
		onTestFinished(() => draftStorage.database.close());
		const document = new DraftDocument(() => repository, { hubId: HUB, sessionRef: REF });
		document.edit("half a thought");

		const toast = vi.fn();
		const hostOwner = {};
		owner = hostOwner;
		let controls: SessionControls | null = null;
		let binding = 1;
		// What the store reports, as the screen's refresh keeps it: the resume
		// moves it onto the identity the daemon resolved.
		let storeRef = cold.ref;
		const screen = (connected: boolean) => {
			if (connected) {
				const mine = binding;
				controls = new SessionControls(
					service,
					async () => {
						const resumedSession = await service.open(storeRef);
						storeRef = resumedSession.ref;
					},
					() => {},
					() => {
						if (client.state !== "ready") return false;
						return binding === mine;
					},
					() => cold,
					() => true,
					CATALOG,
				);
			} else {
				binding += 1;
				// The memo's cleanup: the pair that started the pick is disposed.
				controls?.dispose();
				controls = null;
			}
			modelHosts.provide(sheetKey(HUB, REF), hostOwner, { session: cold, controls, ready: connected, toast });
		};
		const resume = client.resumeThread.bind(client);
		client.resumeThread = async (ref, options) => {
			// The recycle resumeThread performs: the transport goes down and the
			// screen's `connected` follows it, replacing the controls before the
			// resume RPC goes out. The store follows the identity the resume
			// resolved; the route above it does not move.
			client.emitStateChange("reconnecting");
			screen(false);
			client.emitStateChange("ready");
			screen(true);
			return resume(ref, options);
		};
		onTestFinished(() => {
			controls?.dispose();
			service.close();
		});
		screen(true);
		const tree = sheet();
		act(() => pressable(tree, "Claude Haiku 5")?.props.onPress?.());
		await settle();

		// Exactly one resume and one accepted mutation, addressed to the identity
		// the resume resolved.
		expect(client.calls.filter((call) => call.method === "thread/resume")).toHaveLength(1);
		const setCalls = client.calls.filter((call) => call.method === "thread/model/set");
		expect(setCalls).toHaveLength(1);
		expect(setCalls[0]?.params).toMatchObject({
			ref: RESUMED_REF,
			modelProvider: "anthropic",
			model: "claude-haiku-5",
			expectedInstanceId: RESUMED_AUTHORITY.instanceId,
			expectedBootGeneration: RESUMED_AUTHORITY.bootGeneration,
		});
		// The sheet that started the pick closed on the acceptance, and announced
		// it once, even though the store had followed the resume onto a
		// different identity than the route the sheet was opened over.
		expect(navigation.goBack).toHaveBeenCalledTimes(1);
		expect(toast).toHaveBeenCalledTimes(1);

		// The draft belongs to the session it was composed on, so the pick that
		// followed the resolved identity left it untouched on the original ref.
		const original = repository.read({ hubId: HUB, sessionRef: REF });
		expect(original.draft).toBe("half a thought");
		expect(repository.read({ hubId: HUB, sessionRef: RESUMED_REF }).draft).toBe("");
	});

	it("R1: shows a refused pick's reason after an identity-changing resume, and stays open", async () => {
		// RoboRev's open medium: the resume behind a cold pick resolves a
		// different ref, the reconnect replaces the controls that started the
		// pick, and the daemon then REFUSES the frame. The refusal may not be
		// swallowed: the sheet stays open on it and says why. Before outcomes,
		// the refusal answered only through controls state, which a disposed
		// binding may not publish - the promise is what must carry it.
		const RESUMED_REF = "local:resumed";
		const client = new FakeClient("ready");
		client.on("thread/unsubscribe", () => ({}));
		client.on("thread/resume", () => {
			const thread = liveThread(RESUMED_AUTHORITY);
			return { thread: { ...thread, evener: { ...thread.evener, ref: RESUMED_REF } } };
		});
		client.on("thread/read", (params) => {
			const ref = String((params as { ref: string }).ref);
			if (ref !== RESUMED_REF) return { thread: COLD_THREAD };
			const thread = liveThread(RESUMED_AUTHORITY);
			return {
				thread: { ...thread, evener: { ...thread.evener, ref } },
				bootGeneration: RESUMED_AUTHORITY.bootGeneration,
			};
		});
		client.on("thread/model/set", () => {
			throw new WireError("The provider is not available to this session.", -32000);
		});
		const service = createConversationService(client);
		const cold = await service.open(REF);
		expect(cold.modelControlState.kind).toBe("unsupported");

		const toast = vi.fn();
		const hostOwner = {};
		owner = hostOwner;
		let controls: SessionControls | null = null;
		let binding = 1;
		// What the store reports, as the screen's refresh keeps it: the resume
		// moves it onto the identity the daemon resolved.
		let storeRef = cold.ref;
		const screen = (connected: boolean) => {
			if (connected) {
				const mine = binding;
				controls = new SessionControls(
					service,
					async () => {
						const resumedSession = await service.open(storeRef);
						storeRef = resumedSession.ref;
					},
					() => {},
					() => {
						if (client.state !== "ready") return false;
						return binding === mine;
					},
					() => cold,
					() => true,
					CATALOG,
				);
			} else {
				binding += 1;
				controls?.dispose();
				controls = null;
			}
			modelHosts.provide(sheetKey(HUB, REF), hostOwner, { session: cold, controls, ready: connected, toast });
		};
		const resume = client.resumeThread.bind(client);
		client.resumeThread = async (ref, options) => {
			client.emitStateChange("reconnecting");
			screen(false);
			client.emitStateChange("ready");
			storeRef = RESUMED_REF;
			screen(true);
			return resume(ref, options);
		};
		onTestFinished(() => {
			controls?.dispose();
			service.close();
		});
		screen(true);
		const tree = sheet();
		act(() => pressable(tree, "Claude Haiku 5")?.props.onPress?.());
		await settle();

		// Exactly one resume and one refused frame: the refusal reached the
		// daemon on the identity the resume resolved.
		expect(client.calls.filter((call) => call.method === "thread/resume")).toHaveLength(1);
		const setCalls = client.calls.filter((call) => call.method === "thread/model/set");
		expect(setCalls).toHaveLength(1);
		expect(setCalls[0]?.params).toMatchObject({ ref: RESUMED_REF });
		// The refusal is what the sheet shows: it stays open, with the daemon's
		// reason visible, and closes nothing.
		expect(navigation.goBack).toHaveBeenCalledTimes(0);
		expect(toast).not.toHaveBeenCalled();
		expect(renderedText(tree)).toContain("The provider is not available to this session.");
		// The choice may be taken again: the sheet stays usable.
		expect(pressable(tree, "Claude Haiku 5")?.props.disabled).toBe(false);
	});

	it("reports a refused pick that never dispatched as refused, even after its controls were replaced", async () => {
		// The other half of attemptControl's distinction: a step that was refused
		// before it sent anything is not done. The resume answers, but its read
		// establishes no model-control authority, so changeModel fails before any
		// thread/model/set exists. The sheet must stay open rather than confirm a
		// pick the daemon never received.
		let resumed = false;
		const client = new FakeClient("ready");
		client.on("thread/unsubscribe", () => ({}));
		client.on("thread/resume", () => {
			resumed = true;
			// A live daemon that advertises changeModel but carries no snapshot:
			// nothing here can accept a pick.
			const thread = { ...liveThread(RESUMED_AUTHORITY, "idle") };
			delete (thread.evener as { modelControl?: unknown }).modelControl;
			(thread.evener.capabilities as { modelControlVersion?: number }).modelControlVersion = undefined;
			return { thread };
		});
		client.on("thread/read", () =>
			resumed ? { thread: liveThread(RESUMED_AUTHORITY, "idle") } : { thread: COLD_THREAD },
		);
		client.on("thread/model/set", () => {
			throw new Error("thread/model/set must not be dispatched");
		});
		const service = createConversationService(client);
		const cold = await service.open(REF);
		const toast = vi.fn();
		const hostOwner = {};
		owner = hostOwner;
		let controls: SessionControls | null = null;
		let binding = 1;
		const screen = (connected: boolean) => {
			if (connected) {
				const mine = binding;
				controls = new SessionControls(
					service,
					async () => {},
					() => {},
					() => {
						if (client.state !== "ready") return false;
						return binding === mine;
					},
					() => cold,
					() => true,
					CATALOG,
				);
			} else {
				binding += 1;
				controls?.dispose();
				controls = null;
			}
			modelHosts.provide(sheetKey(HUB, REF), hostOwner, { session: cold, controls, ready: connected, toast });
		};
		const resume = client.resumeThread.bind(client);
		client.resumeThread = async (ref, options) => {
			client.emitStateChange("reconnecting");
			screen(false);
			client.emitStateChange("ready");
			screen(true);
			return resume(ref, options);
		};
		onTestFinished(() => {
			controls?.dispose();
			service.close();
		});
		screen(true);
		const tree = sheet();
		act(() => pressable(tree, "Claude Haiku 5")?.props.onPress?.());
		await settle();

		expect(client.calls.filter((call) => call.method === "thread/resume")).toHaveLength(1);
		expect(client.calls.filter((call) => call.method === "thread/model/set")).toHaveLength(0);
		expect(navigation.goBack).not.toHaveBeenCalled();
		expect(toast).not.toHaveBeenCalled();
	});

	it("holds the pick on a LIVE session whose daemon reports no model-control snapshot", async () => {
		// server/appwire_thread_projection.go: ChangeModel does not require a
		// model-control version, but ModelControlVersion is set only for a
		// complete envelope snapshot. A live daemon can therefore advertise
		// changeModel and still read as unsupported. Nothing resumes it, so the
		// pick has no path to authority and the sheet must not offer it.
		const client = new FakeClient("ready");
		client.on("thread/unsubscribe", () => ({}));
		client.on("thread/model/set", () => {
			throw new Error("thread/model/set must not be dispatched");
		});
		// The live read: an idle thread whose envelope carries no snapshot.
		const thread = { ...liveThread(RESUMED_AUTHORITY, "idle") };
		delete (thread.evener as { modelControl?: unknown }).modelControl;
		(thread.evener.capabilities as { modelControlVersion?: number }).modelControlVersion = undefined;
		client.on("thread/read", () => ({ thread }));
		const service = createConversationService(client);
		const live = await service.open(REF);
		expect(live.modelControlState.kind).toBe("unsupported");
		expect(live.status.type).toBe("idle");
		const controls = new SessionControls(
			service,
			async () => {},
			() => {},
			screenFence({ store: () => live.ref, madeOn: live.ref }),
			() => live,
			() => true,
			CATALOG,
		);
		onTestFinished(() => {
			controls.dispose();
			service.close();
		});
		provide(live, {}, { controls });
		const tree = sheet();
		const choice = pressable(tree, "Claude Haiku 5");
		expect(choice?.props.disabled).toBe(true);
		act(() => choice?.props.onPress?.());
		await settle();
		expect(client.calls.filter((call) => call.method === "thread/resume")).toHaveLength(0);
		expect(client.calls.filter((call) => call.method === "thread/model/set")).toHaveLength(0);
	});

	it("sends the model change to the ref the resume resolved, not the retired one", async () => {
		// A resume can hand back a different identity than the one it was given
		// (client.ts's ThreadResumeResponse.thread.evener.ref). Reading the
		// ORIGINAL ref after the resume re-subscribes to a session the daemon
		// just retired, so the mutation would fence on the departed process's
		// authority and fail after the user picked.
		const RESUMED_REF = "local:resumed";
		let resumed = false;
		const client = new FakeClient("ready");
		client.on("thread/unsubscribe", () => ({}));
		client.on("thread/resume", () => {
			resumed = true;
			const thread = liveThread(RESUMED_AUTHORITY);
			return { thread: { ...thread, evener: { ...thread.evener, ref: RESUMED_REF } } };
		});
		client.on("thread/read", (params) => {
			// Only the resumed ref carries the resumed daemon; the retired ref
			// still answers as the cold session it was.
			if (!resumed || params.ref !== RESUMED_REF) return { thread: COLD_THREAD };
			const thread = liveThread(RESUMED_AUTHORITY);
			return {
				thread: { ...thread, evener: { ...thread.evener, ref: RESUMED_REF } },
				bootGeneration: RESUMED_AUTHORITY.bootGeneration,
			};
		});
		client.on("thread/model/set", () => ({
			modelControlVersion: 1,
			disposition: "accepted" as const,
			modelControl: snapshotFor(2, RESUMED_AUTHORITY, "claude-haiku-5"),
		}));
		const service = createConversationService(client);
		const cold = await service.open(REF);
		expect(cold.modelControlState.kind).toBe("unsupported");
		const controls = new SessionControls(
			service,
			async () => {},
			() => {},
			screenFence({ store: () => cold.ref, madeOn: cold.ref }),
			() => cold,
			() => true,
			CATALOG,
		);
		onTestFinished(() => {
			controls.dispose();
			service.close();
		});
		provide(cold, {}, { controls });
		const tree = sheet();
		act(() => pressable(tree, "Claude Haiku 5")?.props.onPress?.());
		await settle();

		expect(client.calls.filter((call) => call.method === "thread/resume")).toHaveLength(1);
		const setCalls = client.calls.filter((call) => call.method === "thread/model/set");
		expect(setCalls).toHaveLength(1);
		expect(setCalls[0]?.params).toMatchObject({
			ref: RESUMED_REF,
			expectedInstanceId: RESUMED_AUTHORITY.instanceId,
			expectedBootGeneration: RESUMED_AUTHORITY.bootGeneration,
		});
	});

	it("resumes and sends when the selected row is the pending pick the departed daemon never applied", async () => {
		// The daemon exits holding a pending pick: the client keeps the ready
		// snapshot, and modelControlDisplay reports the pending target so the
		// sheet checks that row. A same-model shortcut taken on that row would
		// close the sheet without a resume, leaving a choice the departed
		// process never applied looking applied. The row must run through the
		// resume and send once on the authority the post-resume read establishes.
		let resumed = false;
		const client = new FakeClient("ready");
		client.on("thread/unsubscribe", () => ({}));
		client.on("thread/resume", () => {
			resumed = true;
			return { thread: liveThread(RESUMED_AUTHORITY) };
		});
		client.on("thread/read", () =>
			resumed
				? { thread: liveThread(RESUMED_AUTHORITY), bootGeneration: RESUMED_AUTHORITY.bootGeneration }
				: {
						thread: {
							...liveThread(THIS_AUTHORITY),
							evener: {
								...liveThread(THIS_AUTHORITY).evener,
								modelControl: snapshotFor(2, THIS_AUTHORITY, "claude-haiku-5"),
							},
						},
						bootGeneration: THIS_AUTHORITY.bootGeneration,
					},
		);
		client.on("thread/model/set", () => ({
			modelControlVersion: 1,
			disposition: "accepted" as const,
			modelControl: snapshotFor(3, RESUMED_AUTHORITY, "claude-haiku-5"),
		}));
		const service = createConversationService(client);
		const live = await service.open(REF);
		service.subscribeNotifications(() => {});
		// The daemon exits: a cold status with past-thread capabilities. The
		// reducer leaves the held ready snapshot alone, pending pick included.
		client.emitNotification({
			method: "thread/status/changed",
			params: {
				ref: REF,
				threadId: "thread-1",
				status: { type: "closed" },
				capabilities: PAST_CAPABILITIES,
			},
		});
		const stale = { ...live, status: { type: "closed" }, capabilities: PAST_CAPABILITIES } as MobileConversation;
		expect(stale.modelControlState).toMatchObject({ kind: "ready" });
		const controls = new SessionControls(
			service,
			async () => {},
			() => {},
			screenFence({ store: () => stale.ref, madeOn: stale.ref }),
			() => stale,
			() => true,
			CATALOG,
		);
		onTestFinished(() => {
			controls.dispose();
			service.close();
		});
		provide(stale, {}, { controls });
		const tree = sheet();
		const choice = pressable(tree, "Claude Haiku 5");
		// The pending target is the checked row, which is what makes the
		// shortcut tempting in the first place.
		expect(choice?.props.accessibilityState).toMatchObject({ selected: true });
		act(() => choice?.props.onPress?.());
		await settle();

		expect(client.calls.filter((call) => call.method === "thread/resume")).toHaveLength(1);
		const setCalls = client.calls.filter((call) => call.method === "thread/model/set");
		expect(setCalls).toHaveLength(1);
		expect(setCalls[0]?.params).toMatchObject({
			ref: REF,
			modelProvider: "anthropic",
			model: "claude-haiku-5",
			expectedInstanceId: RESUMED_AUTHORITY.instanceId,
			expectedBootGeneration: RESUMED_AUTHORITY.bootGeneration,
		});
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
			if (session.modelControlState.kind !== "ready") throw new Error("model fixture is not ready");
			const modelSnapshot = session.modelControlState.snapshot;
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
					instanceId: modelSnapshot.authority.instanceId,
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
						modelControlVersion: 1,
					},
					modelControl: modelSnapshot,
				},
			};
			const client = new FakeClient("ready");
			client.on("thread/read", () => ({ thread, bootGeneration: modelSnapshot.authority.bootGeneration }));
			client.on("thread/unsubscribe", () => ({}));
			let response = Promise.withResolvers<Record<string, never>>();
			client.on(method, () => response.promise);
			const service = createConversationService(client);
			await service.open(REF);
			const controls = new SessionControls(
				service,
				async () => {},
				() => {},
				screenFence({ store: () => session.ref, madeOn: session.ref }),
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
					else
						response.resolve(
							(method === "thread/model/set"
								? { modelControlVersion: 1, disposition: "accepted", modelControl: modelSnapshot }
								: {}) as never,
						);
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

	it("closes on the already-selected vision model without starting a daemon", async () => {
		// The cold-session exception belongs to the primary picker, where a
		// retained row can be a pick the departed daemon never applied. A vision
		// model is a setting the saved read already reports as in effect, so
		// re-picking it changes nothing and must not resume or send anything.
		const client = new FakeClient("ready");
		client.on("thread/unsubscribe", () => ({}));
		client.on("thread/resume", () => {
			throw new Error("a matching vision pick must not resume the session");
		});
		client.on("thread/vision-model/set", () => {
			throw new Error("a matching vision pick must not be dispatched");
		});
		// COLD_THREAD is the production saved read: advertised change flags and
		// no model-control envelope. The vision setting it reports is the one in
		// effect, which is what makes re-picking it a no-op.
		const thread = {
			...COLD_THREAD,
			evener: { ...COLD_THREAD.evener, visionModel: "lunaroute/glm-5.3-vision" },
		};
		client.on("thread/read", () => ({ thread }));
		const service = createConversationService(client);
		const cold = await service.open(REF);
		// The production cold read: the past capabilities, no model control.
		expect(cold.modelControlState.kind).toBe("unsupported");
		expect(cold.visionModel, "the opened session keeps the vision setting the read reported").toBe(
			"lunaroute/glm-5.3-vision",
		);
		const controls = new SessionControls(
			service,
			async () => {},
			() => {},
			screenFence({ store: () => cold.ref, madeOn: cold.ref }),
			() => cold,
			() => true,
			CATALOG,
		);
		onTestFinished(() => {
			controls.dispose();
			service.close();
		});
		provide(cold, {}, { controls });
		const tree = sheet("vision");
		const rows = tree.root.findAll(
			(node) =>
				String(node.type) === "Pressable" && (node.props.accessibilityLabel ?? textOf(node)) === "GLM 5.3 Vision",
		);
		const states = rows.map((node) => node.props.accessibilityState);
		const selected = rows.find((node) => node.props.accessibilityState?.selected === true);
		expect(states, "exactly one GLM row is the current vision model").toContainEqual(
			expect.objectContaining({ selected: true }),
		);
		act(() => selected?.props.onPress?.());
		await settle();
		expect(navigation.goBack).toHaveBeenCalledTimes(1);
		expect(client.calls.filter((call) => call.method === "thread/vision-model/set")).toHaveLength(0);
		expect(client.calls.filter((call) => call.method === "thread/resume")).toHaveLength(0);
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
