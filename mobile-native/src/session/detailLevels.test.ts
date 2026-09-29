import { makeTranscriptDisplayConfig } from "@evener/appwire-client";
import { describe, expect, it } from "vitest";
import type { SyncStringStorage } from "../syncStringStorage";
import { memoryStorage } from "../syncStringStorageTestUtils";
import {
	currentLevel,
	displayForLevel,
	DETAIL_LEVELS,
	DetailLevels,
	detailMenuLabel,
	forgetDetailLevels,
	levelToast,
} from "./detailLevels";

const hub = makeTranscriptDisplayConfig({ kind: "preset", level: "intent" }, { systemEvents: true });

describe("the levels (spec 8.2; ruling 8)", () => {
	it("lists the hub's five levels in order, each saying what it shows", () => {
		expect(DETAIL_LEVELS.map((level) => [level.label, level.description])).toEqual([
			["Chat", "Just the conversation"],
			["Intent", "Plus one folded line for each run of steps"],
			["Tools", "Plus every command it ran; tap one for its output"],
			["Activity", "Plus every command's output, open as it arrives"],
			["Full", "Everything, including the agent's reasoning"],
		]);
	});

	it("confirms a change with the level and what it shows", () => {
		expect(levelToast("full")).toBe("Full: everything, including the agent's reasoning");
		expect(levelToast("intent")).toBe("Intent: plus one folded line for each run of steps");
	});

	it("names the current level in the menu", () => {
		expect(detailMenuLabel("tools")).toBe("Detail level · Tools");
		expect(detailMenuLabel("custom")).toBe("Detail level · Custom");
		expect(detailMenuLabel(null)).toBe("Detail level");
	});
});

describe("the config a session projects at", () => {
	it("leaves the hub's config alone when nothing was chosen here", () => {
		expect(displayForLevel(null, hub)).toEqual({ config: hub, justTheConversation: false });
		expect(displayForLevel(null, hub).config).toBe(hub);
		expect(displayForLevel(null, null)).toEqual({ config: null, justTheConversation: false });
	});

	it("shows the hub's Chat default as just the conversation too (ruling 8)", () => {
		const chatHub = makeTranscriptDisplayConfig({ kind: "preset", level: "chat" }, hub.advanced);
		expect(displayForLevel(null, chatHub)).toEqual(displayForLevel("chat", hub));
		expect(displayForLevel("tools", chatHub).justTheConversation).toBe(false);
	});

	it("puts a chosen preset over the hub's advanced settings", () => {
		const { config, justTheConversation } = displayForLevel("tools", hub);
		expect(config?.content).toEqual({ kind: "preset", level: "tools" });
		expect(config?.advanced).toEqual(hub.advanced);
		expect(justTheConversation).toBe(false);
	});

	it("projects Chat at Intent, where a subagent's call survives, and keeps just the conversation", () => {
		expect(displayForLevel("chat", hub).config?.content).toEqual({ kind: "preset", level: "intent" });
		expect(displayForLevel("chat", hub).justTheConversation).toBe(true);
		expect(displayForLevel("intent", hub).justTheConversation).toBe(false);
	});

	it("builds on the shipped mobile defaults when the hub has no config", () => {
		const { config } = displayForLevel("full", null);
		expect(config?.content).toEqual({ kind: "preset", level: "full" });
		expect(config?.advanced.systemEvents).toBe(false);
	});

	it("knows the current level for the menu's check", () => {
		expect(currentLevel("full", hub)).toBe("full");
		expect(currentLevel(null, hub)).toBe("intent");
		expect(
			currentLevel(
				null,
				makeTranscriptDisplayConfig({
					kind: "custom",
					toolIntent: true,
					toolCalls: false,
					reasoning: true,
					expandByDefault: false,
				}),
			),
		).toBe("custom");
	});

	it("marks the level the transcript renders at while the hub config is missing", () => {
		// A menu that marked nothing would lie about the transcript on screen.
		expect(currentLevel(null, null)).toBe("full");
		expect(detailMenuLabel(currentLevel(null, null))).toBe("Detail level · Full");
	});
});

describe("the level chosen for each session", () => {
	it("remembers per session and per hub, across a relaunch", () => {
		const storage = memoryStorage();
		const levels = new DetailLevels(storage, "hub-a");
		levels.set("local:s1", "full");
		expect(new DetailLevels(storage, "hub-a").get("local:s1")).toBe("full");
		expect(new DetailLevels(storage, "hub-b").get("local:s1")).toBeNull();
		expect(storage.values.has("evener.native.detail-level.hub-a")).toBe(true);
	});

	it("keeps the newest 500 sessions", () => {
		const storage = memoryStorage();
		const levels = new DetailLevels(storage, "hub-a");
		for (let n = 0; n < 505; n += 1) levels.set(`s${n}`, "tools");
		const reread = new DetailLevels(storage, "hub-a");
		expect(reread.get("s504")).toBe("tools");
		expect(reread.get("s4")).toBeNull();
		expect(reread.get("s5")).toBe("tools");
	});

	it("reads corrupt or unknown values as nothing chosen", () => {
		const storage = memoryStorage(
			new Map([["evener.native.detail-level.hub-a", JSON.stringify([["s1", "loud"], ["s2", "full"], 7])]]),
		);
		const levels = new DetailLevels(storage, "hub-a");
		expect(levels.get("s1")).toBeNull();
		expect(levels.get("s2")).toBe("full");
		expect(
			new DetailLevels(memoryStorage(new Map([["evener.native.detail-level.hub-a", "{nope"]])), "hub-a").get("s2"),
		).toBeNull();
	});

	it("keeps working in memory when storage throws", () => {
		const broken: SyncStringStorage = {
			getItemSync: () => {
				throw new Error("disk");
			},
			setItemSync: () => {
				throw new Error("disk");
			},
			removeItemSync: () => {},
		};
		const levels = new DetailLevels(broken, "hub-a");
		levels.set("s1", "activity");
		expect(levels.get("s1")).toBe("activity");
	});

	it("tells subscribers and bumps its revision on a change", () => {
		const levels = new DetailLevels(memoryStorage(), "hub-a");
		let calls = 0;
		const stop = levels.subscribe(() => calls++);
		const before = levels.getRevision();
		levels.set("s1", "chat");
		expect(calls).toBe(1);
		expect(levels.getRevision()).toBe(before + 1);
		stop();
		levels.set("s1", "full");
		expect(calls).toBe(1);
	});

	it("forgets one hub and nothing else", () => {
		const storage = memoryStorage(
			new Map([
				["evener.native.detail-level.hub-a", "[]"],
				["evener.native.detail-level.hub-b", "[]"],
			]),
		);
		forgetDetailLevels(storage, "hub-a");
		expect([...storage.values.keys()]).toEqual(["evener.native.detail-level.hub-b"]);
	});

	it("lets a storage failure surface when forgetting, so removing the hub can say so", () => {
		const broken: SyncStringStorage = {
			getItemSync: () => null,
			setItemSync: () => {},
			removeItemSync: () => {
				throw new Error("disk");
			},
		};
		expect(() => forgetDetailLevels(broken, "hub-a")).toThrow("disk");
	});
});
