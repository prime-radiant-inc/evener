// D24 slice 5 — content-levels plumbing. The screen's projection step applies
// the user's transcript display config to the conversation before the
// presentation layer maps it.
//
// The locked seam still projects every conversation at the show-everything
// default (D24-3: project.ts's PROJECT_EVERYTHING_CONFIG), so without this
// step a non-full level only narrows rows at the presentation layer and the
// content the shared projector would have dropped or emptied at projection
// time survives to the renderer. Red-first against the pre-slice screen: a
// running thought carried its text at every level, and a settled thought was
// dropped only by the presentation layer, not by the projection.
//
// The fixtures hydrate a real wire thread through the package (hydrateThread),
// project it exactly as the seam does (projectConversation), and run the
// screen's own projection step over that conversation.
import { createElement, type ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import type { Thread, ThreadItem, Turn } from "@evener/appwire-client";
import {
	hydrateThread,
	makeTranscriptDisplayConfig,
	shippedMobileConfig,
} from "@evener/appwire-client";
import {
	projectConversation,
	type MobileTimelineItem,
} from "../../mobile/src/conversation/project";
import { projectDisplayTranscript } from "./screens";

vi.mock("react-native", async () => {
	const mock = (await import("./renderNative.testkit")).nativeModuleMock();
	return {
		...mock,
		AccessibilityInfo: { announceForAccessibility: vi.fn() },
		ActionSheetIOS: { showActionSheetWithOptions: vi.fn() },
		AppState: {
			currentState: "active",
			addEventListener: () => ({ remove: () => {} }),
		},
		Image: "Image",
		Keyboard: { dismiss: vi.fn() },
		Linking: { openURL: vi.fn() },
		RefreshControl: "RefreshControl",
		StatusBar: "StatusBar",
		Modal: (props: { visible?: boolean; children?: ReactNode }) =>
			props.visible ? createElement("Modal", null, props.children) : null,
	};
});
vi.mock("react-native-safe-area-context", () => ({
	SafeAreaView: "SafeAreaView",
	SafeAreaProvider: (props: { children?: ReactNode }) => props.children ?? null,
	useSafeAreaInsets: () => ({ top: 47, bottom: 34, left: 0, right: 0 }),
}));
vi.mock("react-native-enriched-markdown", () => ({
	EnrichedMarkdownText: "EnrichedMarkdownText",
}));
vi.mock("@react-navigation/elements", () => ({ useHeaderHeight: () => 64 }));
vi.mock("@react-navigation/native", async () => {
	const { useEffect } = await import("react");
	return {
		useFocusEffect: (effect: () => void | (() => void)) => useEffect(effect, []),
		useIsFocused: () => true,
	};
});
vi.mock("expo-clipboard", () => ({
	setStringAsync: vi.fn(async () => {}),
	getStringAsync: vi.fn(async () => ""),
}));
vi.mock("expo-crypto", () => ({
	randomUUID: () => "project-display-uuid",
	getRandomValues: (array: Uint8Array) => array,
}));
vi.mock("expo-sqlite", () => ({
	openDatabaseSync: () => ({}),
}));
vi.mock("expo-sqlite/kv-store", () => {
	const kv = { getItemSync: () => null, setItemSync: () => {}, removeItemSync: () => {} };
	return { Storage: kv, default: kv };
});
vi.mock("expo-file-system", () => ({ File: class File {} }));
vi.mock("expo-image-manipulator", () => ({
	ImageManipulator: { manipulateAsync: vi.fn(async () => ({ uri: "manipulated" })) },
	SaveFormat: { JPEG: "jpeg" },
}));
vi.mock("expo-image-picker", () => ({
	launchImageLibraryAsync: vi.fn(async () => ({ canceled: true, assets: [] })),
	UIImagePickerPreferredAssetRepresentationMode: { Current: "current" },
}));
vi.mock("expo-secure-store", () => ({
	getItemAsync: vi.fn(async () => null),
	setItemAsync: vi.fn(async () => {}),
	deleteItemAsync: vi.fn(async () => {}),
}));

const CAPS = {
	send: true,
	steer: true,
	interrupt: true,
	compact: true,
	clear: true,
};

function item(over: Partial<ThreadItem> & { id: string; type: string }): ThreadItem {
	return { turnId: "turn-1", ...over } as ThreadItem;
}

function turn(id: string, items: ThreadItem[], over: Partial<Turn> = {}): Turn {
	return { id, items, itemsView: "default", status: "completed", ...over };
}

function thread(turns: Turn[]): Thread {
	return {
		id: "thread-1",
		sessionId: "session-1",
		preview: "",
		ephemeral: false,
		modelProvider: "anthropic",
		createdAt: 1_000_000,
		updatedAt: 1_000_000,
		status: { type: "ready" },
		cwd: "/tmp",
		cliVersion: "1.0.0",
		source: "local",
		turns,
		evener: { ref: "ref-1", capabilities: CAPS, queue: {} },
	} as unknown as Thread;
}

// The seam's own projection: config-less, so every row reaches the screen.
function seamConversation() {
	const wire = thread([
		turn("t1", [
			item({ id: "u1", type: "userMessage", text: "please audit the config" }),
			item({
				id: "c1",
				type: "commandExecution",
				toolName: "shell",
				description: "  run the audit  ",
				argumentsJson: JSON.stringify({ cmd: "ls" }),
				output: "tool output text",
				status: "completed",
			}),
			item({ id: "r1", type: "reasoning", text: "auditing quietly", status: "completed" }),
		]),
		turn(
			"t2",
			[item({ id: "r2", type: "reasoning", text: "secret live thought", status: "inProgress" })],
			{ status: "inProgress" },
		),
	]);
	return projectConversation(hydrateThread({ thread: wire }, "ref-1", 0));
}

const reasoningText = (items: MobileTimelineItem[]): string[] =>
	items.flatMap((row) => (row.kind === "activity" ? [row.detail.output ?? ""] : []));

describe("projectDisplayTranscript — the screen applies the content level", () => {
	it("hides a settled thought and empties a running thought's detail at chat", () => {
		const presented = projectDisplayTranscript(
			seamConversation(),
			makeTranscriptDisplayConfig({ kind: "preset", level: "chat" }),
		);
		expect(reasoningText(presented.items)).not.toContain("auditing quietly");
		// The projector's content-free placeholder: the row survives (running is
		// critical) but carries no thought text.
		expect(reasoningText(presented.items)).not.toContain("secret live thought");
	});

	it("keeps the running thought's text at full", () => {
		const presented = projectDisplayTranscript(
			seamConversation(),
			makeTranscriptDisplayConfig({ kind: "preset", level: "full" }),
		);
		expect(reasoningText(presented.items)).toContain("secret live thought");
	});

	it("re-projects when the level changes", () => {
		const conversation = seamConversation();
		const chat = projectDisplayTranscript(
			conversation,
			makeTranscriptDisplayConfig({ kind: "preset", level: "chat" }),
		);
		const full = projectDisplayTranscript(
			conversation,
			makeTranscriptDisplayConfig({ kind: "preset", level: "full" }),
		);
		expect(chat.items).not.toEqual(full.items);
	});

	it("applies the hub's shipped mobile default with no native toggle", () => {
		// shippedMobileConfig is the hub's own default (intent): the hub returns
		// it whenever no stored config exists, so the screen receives it with no
		// local switch. The projection must honor it.
		expect(shippedMobileConfig.content).toEqual({ kind: "preset", level: "intent" });
		const presented = projectDisplayTranscript(seamConversation(), shippedMobileConfig);
		expect(reasoningText(presented.items)).not.toContain("secret live thought");
		expect(reasoningText(presented.items)).not.toContain("auditing quietly");
	});

	it("leaves the projection at show-everything when there is no config", () => {
		const presented = projectDisplayTranscript(seamConversation(), null);
		expect(reasoningText(presented.items)).toContain("secret live thought");
	});

	it("keeps a summarized tool-action row's full detail at intent (obligation pin)", () => {
		// The D24-1 ruling carried into D24-3 and dispositioned here: a
		// summarized tool-action row carries only its summary LINE, not only its
		// summary - the row's full detail (arguments/output) survives. At
		// chat/intent the summary is the projector's trimmed description; at
		// tools/activity/full the row is unsummarized (mode full, untrimmed).
		const intent = projectDisplayTranscript(seamConversation(), shippedMobileConfig);
		const row = intent.items.find((item) => item.id === "c1");
		expect(row).toMatchObject({
			kind: "activity",
			detail: {
				description: "run the audit",
				arguments: JSON.stringify({ cmd: "ls" }),
				output: "tool output text",
			},
		});
		expect(intent.activityPresentation.get("c1")).toEqual({
			mode: "intent",
			summary: "run the audit",
		});

		const tools = projectDisplayTranscript(
			seamConversation(),
			makeTranscriptDisplayConfig({ kind: "preset", level: "tools" }),
		);
		expect(tools.items.find((item) => item.id === "c1")).toMatchObject({
			detail: { description: "  run the audit  ", output: "tool output text" },
		});
		expect(tools.activityPresentation.get("c1")).toEqual({ mode: "full" });
	});
});
