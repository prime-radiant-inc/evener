// The Commands and skills sheet route (spec 8.5; rulings 15 and 37), rendered
// with a host in commandHosts the way ConversationScreen provides one. The
// catalog comes from a fake client answering the owning thread/read
// createSessionCommandCatalog makes; only native edges are mocked.
import type { AnyNotification, CommandDescriptor, EvenerSkillInfo } from "@evener/appwire-client";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { act, type ReactTestInstance, type ReactTestRenderer } from "react-test-renderer";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { paletteFor } from "../design/tokens";
import type { ComposerCommandSession } from "../composerCommand";
import { pressable, render, screenConnection, textOf, unmountMountedTrees } from "../renderNative.testkit";
import type { Routes } from "../screens";
import { sheetKey } from "../sheet/sheetHosts";
import { type CommandsHost, CommandsSheet, commandHosts, insertInvocation } from "./CommandsSheet";

const navigation = vi.hoisted(() => ({ goBack: vi.fn(), navigate: vi.fn(), dispatch: vi.fn() }));
const harness = vi.hoisted(() => ({ connection: {} as Record<string, unknown> }));

vi.mock("react-native", async () => (await import("../renderNative.testkit")).nativeModuleMock());
vi.mock("@react-navigation/native", () => ({
	useNavigation: () => navigation,
	usePreventRemove: () => {},
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("../ConnectionProvider", () => ({ useConnection: () => harness.connection }));

const palette = paletteFor("light");
const HUB = "hub-1";
const REF = "local:s1";

const COMMANDS: CommandDescriptor[] = [
	{ name: "release", pluginName: "shepherd-pr", source: "plugin", description: "Cut a release" },
	{ name: "standup", source: "user", description: "Write today's standup" },
];

const SKILLS: EvenerSkillInfo[] = [
	{
		name: "superpowers:brainstorming",
		description: "Explore an idea before building it",
		disableModelInvocation: false,
		userInvocable: true,
		available: true,
	},
	{
		name: "shepherd-pr:shepherd",
		description: "Take a pull request to merge",
		disableModelInvocation: false,
		userInvocable: true,
		available: true,
	},
];

/** A client that answers the catalog and the session's diagnostics, or
 * never answers while `hold` is set. */
function catalogClient({ hold = false, fail = false } = {}) {
	return {
		request: async (method: string) => {
			if (hold) return new Promise(() => {});
			if (fail) throw new Error("refused");
			if (method === "thread/read")
				return {
					thread: {
						evener: {
							ref: REF,
							diagnostics: {
								plugins: [{ name: "shepherd-pr" }, { name: "superpowers" }],
								commands: COMMANDS,
								skills: SKILLS,
							},
						},
					},
				};
			throw new Error(`unexpected ${method}`);
		},
		onNotification: (_listener: (notification: AnyNotification) => void) => () => {},
	};
}

function session(capabilities: Partial<ComposerCommandSession["capabilities"]>): ComposerCommandSession {
	return {
		capabilities,
		status: { type: "idle" },
		queue: { revision: 0, depth: 0 },
	} as unknown as ComposerCommandSession;
}

let owner: object | undefined;

function provide(over: Partial<CommandsHost> = {}) {
	const calls: string[] = [];
	const host: CommandsHost = {
		session: session({ goal: true, compact: true, forkFromTurn: true, changeModel: true, clear: true }),
		choose: vi.fn((invocation: string) => void calls.push(`choose:${invocation}`)),
		...over,
	};
	if (owner) commandHosts.release(sheetKey(HUB, REF), owner);
	owner = {};
	commandHosts.provide(sheetKey(HUB, REF), owner, host);
	navigation.goBack.mockImplementation(() => void calls.push("goBack"));
	return { host, calls };
}

async function sheet(client: unknown = catalogClient()): Promise<ReactTestRenderer> {
	harness.connection = screenConnection(client, "ready");
	const props = {
		route: { key: "commands-sheet", name: "CommandsSheet", params: { hubId: HUB, ref: REF } },
		navigation,
	} as unknown as NativeStackScreenProps<Routes, "CommandsSheet">;
	const tree = render(<CommandsSheet {...props} />);
	await act(async () => {
		await new Promise((resolve) => setTimeout(resolve, 0));
	});
	return tree;
}

/** The section headers and row names, in the order the list shows them. */
function listed(tree: ReactTestRenderer): string[] {
	const list = tree.root.find((node) => String(node.type) === "SectionList");
	return list
		.findAll((node) => node.props.testID === "commands-header" || node.props.testID === "commands-name")
		.map((node: ReactTestInstance) => textOf(node));
}

function texts(tree: ReactTestRenderer): string[] {
	return tree.root.findAll((node) => String(node.type) === "Text").map((node: ReactTestInstance) => textOf(node));
}

beforeEach(() => {
	navigation.goBack.mockReset();
});

afterEach(() => {
	unmountMountedTrees();
	if (owner) commandHosts.release(sheetKey(HUB, REF), owner);
	owner = undefined;
});

describe("the Commands and skills sheet (spec 8.5)", () => {
	it("is titled Commands and skills, with Done", async () => {
		provide();
		const tree = await sheet();
		expect(texts(tree)).toContain("Commands and skills");
		expect(pressable(tree, "Done")).toBeDefined();
	});

	it("lists the commands this session can run, in order, each with its line", async () => {
		provide();
		const tree = await sheet();
		expect(listed(tree).slice(0, 8)).toEqual([
			"Commands",
			"Goal",
			"Compact context",
			"Aside",
			"Tasks",
			"Model",
			"Effort",
			"Clear",
		]);
		expect(texts(tree)).toContain("An objective the agent pursues until it's done");
		expect(texts(tree)).toContain("Free up token space");
		expect(texts(tree)).toContain("A side question in its own session; this one keeps working");
		expect(texts(tree)).toContain("The session's task list");
		expect(texts(tree)).toContain("Change the model");
		expect(texts(tree)).toContain("How long it thinks before acting");
		expect(texts(tree)).toContain("Start fresh in this session");
	});

	it("leaves out a command the session can't run", async () => {
		provide({
			session: session({ goal: false, compact: true, forkFromTurn: false, changeModel: false, clear: false }),
		});
		const tree = await sheet();
		expect(listed(tree).slice(0, 4)).toEqual(["Commands", "Compact context", "Tasks", "Effort"]);
	});

	it("groups skills and plugin commands under their plugin's name, in Menlo as typed, and your own last", async () => {
		provide();
		const tree = await sheet();
		expect(listed(tree).slice(8)).toEqual([
			"shepherd-pr",
			"release",
			"shepherd",
			"superpowers",
			"brainstorming",
			"Your commands and skills",
			"standup",
		]);
		const header = tree.root.findAll(
			(node) => node.props.testID === "commands-header" && textOf(node) === "superpowers",
		)[0];
		expect(header?.props.style).toMatchObject({ fontFamily: "Menlo", fontSize: 12, color: palette.inkMid });
		expect(header?.props.style.textTransform).toBeUndefined();
		const hint = tree.root.findAll(
			(node) => String(node.type) === "Text" && textOf(node) === "Explore an idea before building it",
		)[0];
		expect(hint?.props.style).toMatchObject({ fontSize: 13, lineHeight: 18, color: palette.inkMid });
	});

	it("searches both sections by name and line", async () => {
		provide();
		const tree = await sheet();
		const search = tree.root.find((node) => String(node.type) === "TextInput");
		expect(search.props.accessibilityLabel).toBe("Search commands and skills");
		act(() => search.props.onChangeText("MODEL"));
		expect(listed(tree)).toEqual(["Commands", "Model"]);
		act(() => search.props.onChangeText("pull request"));
		expect(listed(tree)).toEqual(["shepherd-pr", "shepherd"]);
		act(() => search.props.onChangeText("nothing like it"));
		expect(texts(tree)).toContain("No commands or skills match your search.");
	});

	it("shows stand-in rows while skills load", async () => {
		provide();
		const tree = await sheet(catalogClient({ hold: true }));
		expect(tree.root.findAll((node) => node.props.testID === "commands-skeleton").length).toBeGreaterThan(0);
		expect(tree.root.findAll((node) => String(node.type) === "ActivityIndicator")).toEqual([]);
	});

	it("says in one line why skills didn't load", async () => {
		provide();
		const tree = await sheet(catalogClient({ fail: true }));
		expect(texts(tree)).toContain("Could not load commands and skills: refused");
		expect(listed(tree)[0]).toBe("Commands");
	});

	it("lists the commands alone while the hub is away", async () => {
		provide();
		const tree = await sheet(null);
		expect(listed(tree)).toEqual(["Commands", "Goal", "Compact context", "Aside", "Tasks", "Model", "Effort", "Clear"]);
	});

	it("closes, then hands the chosen invocation to the session", async () => {
		const { calls } = provide();
		const tree = await sheet();
		act(() => pressable(tree, "Compact context")?.props.onPress());
		expect(calls).toEqual(["goBack", "choose:/compact"]);
	});

	it("hands a skill over by its full invocation", async () => {
		const { calls } = provide();
		const tree = await sheet();
		act(() => pressable(tree, "brainstorming")?.props.onPress());
		expect(calls).toEqual(["goBack", "choose:/superpowers:brainstorming"]);
	});
});

describe("inserting an invocation (ruling 15)", () => {
	it("puts it at the start of the draft, with the caret after it and its space", () => {
		expect(insertInvocation("", "/compact")).toEqual({ text: "/compact ", caret: 9 });
		expect(insertInvocation("hello", "/goal")).toEqual({ text: "/goal hello", caret: 6 });
		expect(insertInvocation(" hello", "/goal")).toEqual({ text: "/goal hello", caret: 5 });
	});
});
