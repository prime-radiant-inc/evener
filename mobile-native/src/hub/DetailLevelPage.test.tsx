// The hub's default detail level, chosen and saved at once (spec 12's
// Display; spec 8.2's levels). It carries the states the old transcript
// editor handled, calmly: an unreadable saved draft (the shared store allows
// exactly that record to be discarded, so the page must offer it even with no
// draft to show), a write the hub never confirmed (checked again on its own
// once the hub is back), and a conflict with the hub's newer setting.
import { createHubUpdateController, type TranscriptDisplayConfigV1 } from "@evener/appwire-client";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { act } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import type { NativePreferencesSnapshot } from "../nativePreferences";
import { render, renderedText } from "../renderNative.testkit";
import { Group, GroupFooter } from "../sheet/Grouped";
import { DetailLevelPage } from "./DetailLevelPage";
import { type HubRoutes, type HubSheetContextValue, HubSheetProvider } from "./hubSheetContext";

const preferences = vi.hoisted(() => ({ value: {} as Record<string, unknown> }));
vi.mock("../NativePreferencesProvider", () => ({ useNativePreferences: () => preferences.value }));
vi.mock("../board/connectionStatus", async (importOriginal) => ({
	...(await importOriginal<typeof import("../board/connectionStatus")>()),
	useConnectionStatusText: () => null,
}));
vi.mock("../ConnectionProvider", () => ({ useConnection: () => ({ state: "ready", fatal: false }) }));
vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

const ADVANCED = {
	roundTimings: false,
	tokenCounts: false,
	estimatedCost: false,
	systemEvents: false,
	promptEvents: false,
	hookExits: "none" as const,
};
const PRESET: TranscriptDisplayConfigV1 = {
	version: 1,
	content: { kind: "preset", level: "intent" },
	advanced: ADVANCED,
};
const CUSTOM: TranscriptDisplayConfigV1 = {
	version: 1,
	content: { kind: "custom", toolIntent: true, toolCalls: false, reasoning: false, expandByDefault: false },
	advanced: ADVANCED,
};

function transcript(
	over: Partial<NativePreferencesSnapshot["transcriptMobile"]> = {},
): NativePreferencesSnapshot["transcriptMobile"] {
	return {
		support: "supported",
		loading: false,
		saving: false,
		confirmed: { revision: 3, config: PRESET },
		draft: null,
		error: null,
		conflict: false,
		writeUncertain: false,
		storageUnavailable: false,
		draftUnreadable: false,
		...over,
	};
}

const context: HubSheetContextValue = {
	hubId: "hub-1",
	hubName: "Work hub",
	client: null,
	ready: true,
	canUseConnection: () => true,
	updates: createHubUpdateController({
		client: () => {
			throw new Error("not in this test");
		},
		awaitRestart: async () => true,
	}),
	hosts: null,
	live: null,
};

function model() {
	const calls: [string, unknown?][] = [];
	return {
		calls,
		editTranscript: vi.fn(async (config: TranscriptDisplayConfigV1) => {
			calls.push(["edit", config]);
		}),
		saveTranscript: vi.fn(async () => {
			calls.push(["save"]);
		}),
		refresh: vi.fn(async () => {
			calls.push(["refresh"]);
		}),
		discardTranscriptDraft: vi.fn(async () => {
			calls.push(["discard"]);
		}),
		rebaseTranscriptDraft: vi.fn(async (revision: number) => {
			calls.push(["rebase", revision]);
		}),
	};
}

function mount(state: NativePreferencesSnapshot["transcriptMobile"] | null, connected = true) {
	const fake = model();
	preferences.value = { model: fake, snapshot: state ? { transcriptMobile: state } : null, connected };
	const page = () => (
		<HubSheetProvider value={context}>
			<DetailLevelPage
				navigation={{} as NativeStackScreenProps<HubRoutes, "DetailLevel">["navigation"]}
				route={{ key: "DetailLevel", name: "DetailLevel", params: { hubId: "hub-1" } }}
			/>
		</HubSheetProvider>
	);
	const tree = render(page());
	const button = (label: string) =>
		tree.root.findAllByProps({ accessibilityRole: "button" }).find((node) => node.props.accessibilityLabel === label) ??
		null;
	const press = async (label: string) => {
		await act(async () => {
			button(label)?.props.onPress();
		});
	};
	const update = (
		next: NativePreferencesSnapshot["transcriptMobile"],
		nextConnected = connected,
		nextModel: ReturnType<typeof model> = fake,
	) => {
		preferences.value = { model: nextModel, snapshot: { transcriptMobile: next }, connected: nextConnected };
		act(() => tree.update(page()));
	};
	return { tree, fake, button, press, update };
}

const CALM = /\bReconnect\b|\bRefresh\b|transcript display|verbosity/i;

beforeEach(() => {
	preferences.value = {};
});

it("waits quietly, connected, before the hub's setting has loaded (spec 14)", () => {
	const { tree } = mount(null);
	expect(renderedText(tree)).not.toContain("Connecting");
	expect(tree.root.findAllByProps({ accessibilityLabel: "Loading the default detail level" })).not.toHaveLength(0);
});

it("lists spec 8.2's levels with their descriptions, then Custom, with the saved one checked", () => {
	const { tree, button } = mount(transcript());
	for (const label of ["Chat", "Intent", "Tools", "Activity", "Full", "Custom"])
		expect(
			tree.root
				.findAllByProps({ accessibilityRole: "button" })
				.some((node) => String(node.props.accessibilityLabel).startsWith(`${label},`)),
		).toBe(true);
	const intent = tree.root
		.findAllByProps({ accessibilityRole: "button" })
		.find((node) => String(node.props.accessibilityLabel).startsWith("Intent,"));
	expect(intent?.props.accessibilityState).toMatchObject({ selected: true });
	expect(renderedText(tree)).toContain("Choose what the transcript shows");
	expect(button("Chat, Just the conversation")).not.toBeNull();
});

it("saves a chosen level at once", async () => {
	const { fake, press } = mount(transcript());
	await press("Full, Everything, including the agent's reasoning");
	expect(fake.calls).toEqual([["edit", { ...PRESET, content: { kind: "preset", level: "full" } }], ["save"]]);
});

it("holds every row while a save is in flight", () => {
	const { tree } = mount(transcript({ saving: true }));
	const rows = tree.root.findAllByProps({ accessibilityRole: "button" });
	expect(rows.length).toBeGreaterThan(0);
	for (const row of rows) expect(row.props.disabled).toBe(true);
});

it("shows Custom's switches and hook events, and saves each change at once", async () => {
	const { tree, fake } = mount(transcript({ confirmed: { revision: 3, config: CUSTOM } }));
	const text = renderedText(tree);
	for (const label of ["SHOWS", "MORE DETAIL", "HOOK EVENTS"]) expect(text.toUpperCase()).toContain(label);
	for (const label of [
		"Action summaries",
		"Tool calls",
		"Reasoning",
		"Open details by default",
		"Timing",
		"Token counts",
		"Estimated cost",
		"System events",
		"Prompt events",
	])
		expect(tree.root.findAllByType("Switch" as never).some((node) => node.props.accessibilityLabel === label)).toBe(
			true,
		);
	for (const label of ["Failures only", "Successful exits and failures", "All hook events"])
		expect(text).toContain(label);
	const reasoning = tree.root
		.findAllByType("Switch" as never)
		.find((node) => node.props.accessibilityLabel === "Reasoning");
	await act(async () => {
		reasoning?.props.onValueChange(true);
	});
	expect(fake.calls).toEqual([["edit", { ...CUSTOM, content: { ...CUSTOM.content, reasoning: true } }], ["save"]]);
});

it("offers to keep your choice or use the hub's after a conflict", async () => {
	const draft = { revision: 3, config: { ...PRESET, content: { kind: "preset" as const, level: "full" as const } } };
	const hubs = { revision: 4, config: PRESET };
	const { tree, fake, press } = mount(transcript({ conflict: true, draft, confirmed: hubs }));
	expect(renderedText(tree)).toContain("The hub's setting changed while you were choosing.");
	await press("Keep mine");
	expect(fake.calls).toEqual([["rebase", 4], ["save"]]);
	fake.calls.length = 0;
	await press("Use the hub's");
	expect(fake.calls).toEqual([["discard"]]);
});

it("checks the hub's setting on its own each time the hub is back while a write is uncertain", () => {
	// The shared store marks an unconfirmed write as a conflict too.
	const { tree, fake, update } = mount(transcript({ writeUncertain: true, conflict: true }), false);
	expect(renderedText(tree)).toContain("Checking the hub's setting…");
	expect(fake.refresh).not.toHaveBeenCalled();
	update(transcript({ writeUncertain: true }), true);
	expect(fake.refresh).toHaveBeenCalledTimes(1);
	// Once per return of the hub, never per render.
	update(transcript({ writeUncertain: true, loading: true }), true);
	expect(fake.refresh).toHaveBeenCalledTimes(1);
	// A check that failed gets another when the hub comes back again.
	update(transcript({ writeUncertain: true }), false);
	update(transcript({ writeUncertain: true }), true);
	expect(fake.refresh).toHaveBeenCalledTimes(2);
	update(transcript(), true);
	update(transcript({ writeUncertain: true }), true);
	expect(fake.refresh).toHaveBeenCalledTimes(3);
});

it("checks again through a new preferences model while the write is still uncertain", () => {
	const { fake, update } = mount(transcript({ writeUncertain: true }));
	expect(fake.refresh).toHaveBeenCalledTimes(1);
	const replacement = model();
	update(transcript({ writeUncertain: true }), true, replacement);
	expect(replacement.refresh).toHaveBeenCalledTimes(1);
});

it("offers to discard a saved change the phone can't read, even with no draft to show", async () => {
	const { tree, fake, press, button } = mount(
		transcript({
			confirmed: null,
			draftUnreadable: true,
			storageUnavailable: true,
			error: "Could not restore the saved transcript draft.",
		}),
	);
	expect(renderedText(tree)).toContain("A saved change to this setting couldn't be read on this phone.");
	expect(button("Discard it")?.props.disabled).toBe(false);
	await press("Discard it");
	expect(fake.calls).toEqual([["discard"]]);
});

it("offers no discard when nothing unreadable is stored, or a readable draft is", () => {
	expect(mount(transcript()).button("Discard it")).toBeNull();
	const readable = { revision: 3, config: PRESET };
	expect(mount(transcript({ draft: readable })).button("Discard it")).toBeNull();
});

it("lets the discard go through while the hub is away, since it touches only this phone", async () => {
	const { fake, button, press } = mount(
		transcript({ confirmed: null, draftUnreadable: true, storageUnavailable: true }),
		false,
	);
	expect(button("Discard it")?.props.disabled).toBe(false);
	await press("Discard it");
	expect(fake.calls).toEqual([["discard"]]);
});

it("shows a failed load as a line with nothing to press", () => {
	const { tree } = mount(transcript({ confirmed: null, error: "The hub request could not be confirmed." }));
	expect(renderedText(tree)).toContain("Couldn't load this hub's setting.");
	expect(tree.root.findAllByProps({ accessibilityLabel: "Try again" })).toHaveLength(0);
});

it("says so when the hub doesn't keep a default detail level", () => {
	const { tree } = mount(transcript({ support: "unsupported" }));
	expect(renderedText(tree)).toContain("This hub doesn't keep a default detail level.");
});

it.each([
	["loaded", transcript()],
	["conflicted", transcript({ conflict: true })],
	["uncertain", transcript({ writeUncertain: true })],
	["unreadable", transcript({ draftUnreadable: true, storageUnavailable: true })],
])("never asks to reconnect or refresh when %s", (_name, state) => {
	expect(renderedText(mount(state).tree)).not.toMatch(CALM);
});

it("shows an unconfirmed write as a check, not a conflict to resolve", () => {
	const { tree, button } = mount(transcript({ writeUncertain: true, conflict: true }));
	expect(renderedText(tree)).toContain("Checking the hub's setting…");
	expect(renderedText(tree)).not.toContain("The hub's setting changed while you were choosing.");
	expect(button("Keep mine")).toBeNull();
});

it("drops a failed save's line once the check finds the uncertain write landed", async () => {
	const { tree, fake, press, update } = mount(transcript());
	fake.saveTranscript.mockRejectedValueOnce(new Error("reply lost"));
	await press("Full, Everything, including the agent's reasoning");
	update(transcript({ writeUncertain: true, conflict: true }));
	update(transcript({ confirmed: { revision: 4, config: PRESET } }));
	expect(renderedText(tree)).not.toContain("The change couldn't be saved.");
});

it.each([
	["the hub's setting is loading", { loading: true }],
	["the phone can't keep a change", { storageUnavailable: true }],
])("holds Keep mine and Use the hub's while %s", (_name, over) => {
	const { button } = mount(transcript({ conflict: true, draft: { revision: 2, config: CUSTOM }, ...over }));
	expect(button("Keep mine")?.props.accessibilityState.disabled).toBe(true);
	expect(button("Use the hub's")?.props.accessibilityState.disabled).toBe(true);
});

it("says a choice hasn't reached the hub while the phone holds it unsaved, and saves it again", async () => {
	// The store keeps the draft after a failed save, or after an uncertain
	// write settles without the hub taking it.
	const { tree, fake, press, button } = mount(transcript({ draft: { revision: 3, config: CUSTOM } }));
	expect(renderedText(tree)).toContain("This change hasn't reached the hub yet.");
	expect(renderedText(tree)).not.toContain("Choose it again");
	await press("Save it");
	expect(fake.saveTranscript).toHaveBeenCalledTimes(1);
	expect(button("Save it")).not.toBeNull();
});

it.each([
	["the phone is away from the hub", {}, false],
	["the hub's setting is loading", { loading: true }, true],
])("holds Save it while %s", (_name, over, connected) => {
	const { button } = mount(transcript({ draft: { revision: 3, config: CUSTOM }, ...over }), connected);
	expect(button("Save it")?.props.accessibilityState.disabled).toBe(true);
});

it("says only that the phone couldn't update its copy after the hub confirmed the save", () => {
	// A confirmed save whose local cleanup failed keeps the draft.
	const { tree, button } = mount(
		transcript({
			draft: { revision: 3, config: CUSTOM },
			storageUnavailable: true,
			error: "The hub confirmed this save, but the local draft could not be updated. Check current settings to retry.",
		}),
	);
	expect(renderedText(tree)).toContain("This phone couldn't update its copy of this setting.");
	expect(renderedText(tree)).not.toContain("This change hasn't reached the hub yet.");
	expect(button("Save it")).toBeNull();
});

it.each([
	["Keep mine", "rebaseTranscriptDraft", transcript({ conflict: true, draft: { revision: 2, config: CUSTOM } })],
	[
		"Discard it",
		"discardTranscriptDraft",
		transcript({ confirmed: null, draftUnreadable: true, storageUnavailable: true }),
	],
] as const)("says %s didn't go through when the phone or hub refuses it", async (label, method, state) => {
	const { tree, fake, press } = mount(state);
	fake[method].mockRejectedValueOnce(new Error("refused"));
	await press(label);
	expect(renderedText(tree)).toContain("That didn't go through. Try it again.");
});

it("leaves a refused conflict action to the storage footer when the phone can't keep a change", async () => {
	const { tree, fake, press } = mount(
		transcript({
			conflict: true,
			draft: { revision: 2, config: CUSTOM },
			storageUnavailable: true,
			error: "Could not save the transcript draft locally.",
		}),
	);
	fake.rebaseTranscriptDraft.mockRejectedValueOnce(new Error("refused"));
	await press("Keep mine");
	expect(renderedText(tree)).toContain("This phone couldn't update its copy of this setting.");
	expect(renderedText(tree)).not.toContain("That didn't go through.");
});

it("offers no Save it while the hub's setting is being checked or a conflict needs resolving", () => {
	const draft = { revision: 2, config: CUSTOM };
	expect(mount(transcript({ draft, writeUncertain: true })).button("Save it")).toBeNull();
	expect(mount(transcript({ draft, conflict: true })).button("Save it")).toBeNull();
});

it("saves nothing when the hook events already chosen are chosen again", async () => {
	const { fake, press, button } = mount(transcript({ confirmed: { revision: 3, config: CUSTOM } }));
	expect(button("Failures only")).not.toBeNull();
	await press("Failures only");
	expect(fake.saveTranscript).not.toHaveBeenCalled();
});

it("drops a failed save's line while the hub's setting is being checked", async () => {
	const { tree, fake, press, update } = mount(transcript());
	fake.saveTranscript.mockRejectedValueOnce(new Error("reply lost"));
	await press("Full, Everything, including the agent's reasoning");
	expect(renderedText(tree)).toContain("The change couldn't be saved. Choose it again.");
	update(transcript({ writeUncertain: true, conflict: true }));
	expect(renderedText(tree)).not.toContain("The change couldn't be saved.");
});

it("drops a failed save's line when the phone can't keep the change, since nothing can be chosen again", async () => {
	const { tree, fake, press, update } = mount(transcript());
	fake.saveTranscript.mockRejectedValueOnce(new Error("disk full"));
	await press("Full, Everything, including the agent's reasoning");
	update(transcript({ error: "Could not save the transcript draft locally.", storageUnavailable: true }));
	expect(renderedText(tree)).toContain("This phone couldn't update its copy of this setting.");
	expect(renderedText(tree)).not.toContain("Choose it again");
});

it.each([
	// nativePreferences' own message for a hub-side failure.
	[
		"a setting the hub didn't send",
		{ error: "The hub request could not be confirmed." },
		/Couldn't load this hub's setting/,
	],
	// The shared store's draft-side messages, with the state it sets beside them.
	[
		"a change the phone couldn't keep",
		{ error: "Could not save the transcript draft locally.", storageUnavailable: true },
		/This phone couldn't update its copy of this setting/,
	],
	[
		"a saved change the phone couldn't record",
		{
			error: "The hub confirmed this save, but the local draft could not be updated. Check current settings to retry.",
			storageUnavailable: true,
		},
		/This phone couldn't update its copy of this setting/,
	],
	// A conflict whose cleanup the phone couldn't record: the rows hold, so
	// the page says why.
	[
		"a conflict the phone couldn't record",
		{
			error: "Could not save the transcript draft locally.",
			storageUnavailable: true,
			conflict: true,
			draft: { revision: 2, config: CUSTOM },
		},
		/This phone couldn't update its copy of this setting/,
	],
])("says %s in its own words", (_name, over, expected) => {
	const { tree } = mount(transcript(over));
	expect(renderedText(tree)).not.toMatch(CALM);
	expect(renderedText(tree)).not.toContain("Check current settings");
	expect(renderedText(tree)).toMatch(expected);
});

it("waits quietly while the hub hasn't said whether it keeps the setting", () => {
	const { tree } = mount(transcript({ support: "unknown", confirmed: null }));
	expect(renderedText(tree)).not.toContain("Connecting");
	expect(tree.root.findAllByProps({ accessibilityLabel: "Loading the default detail level" })).not.toHaveLength(0);
});

it("waits quietly while the hub's setting is still loading", () => {
	const { tree } = mount(transcript({ confirmed: null, loading: true }));
	expect(renderedText(tree)).not.toContain("Connecting");
	expect(tree.root.findAllByProps({ accessibilityLabel: "Loading the default detail level" })).not.toHaveLength(0);
});

it("saves nothing when the level already chosen is chosen again", async () => {
	const { fake, press } = mount(transcript());
	await press("Intent, Plus one folded line for each run of steps");
	expect(fake.calls).toEqual([]);
});

it.each([
	["a conflict", { conflict: true }, "The hub's setting changed while you were choosing."],
	["an unsaved choice", {}, "This change hasn't reached the hub yet."],
])("leads with the levels and says %s beneath them (audit M12)", (_name, over, line) => {
	const hubs = { revision: 4, config: PRESET };
	const { tree } = mount(transcript({ draft: { revision: 3, config: CUSTOM }, confirmed: hubs, ...over }));
	// In the page's own order: the levels' group, then the line about the
	// choice made in it.
	const nodes = tree.root.findAll(() => true);
	const levels = nodes.findIndex((node) => node.type === Group && node.findAllByProps({ label: "Custom" }).length > 0);
	const said = nodes.findIndex((node) => node.type === GroupFooter && node.props.children === line);
	expect(levels).toBeGreaterThan(-1);
	expect(said).toBeGreaterThan(levels);
});

it("lets the page's title name the setting, with no group label repeating it", () => {
	const { tree } = mount(transcript());
	expect(tree.root.findAll((node) => node.type === Group && node.props.label === "Default detail level")).toHaveLength(
		0,
	);
});
