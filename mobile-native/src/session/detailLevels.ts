// How much of the agent's work a session shows (spec 8.2 and 8.7). The level
// is chosen per session and remembered on this device (ruling 8). A choice
// replaces the hub config's content and keeps its advanced settings, so
// Hub > Display still owns system events, timings and costs.
//
// DETAIL_LEVELS is ruling 8's table, Jesse's answer: the hub's presets under
// their hub names, described by what each shows on the phone, with Chat
// projected without step lines.
import {
	type ContentLevel,
	makeTranscriptDisplayConfig,
	shippedConfig,
	type TranscriptDisplayConfigV1,
} from "@evener/appwire-client";
import type { SyncStringStorage } from "../syncStringStorage";

export interface DetailLevel {
	level: ContentLevel;
	label: string;
	description: string;
}

export const DETAIL_LEVELS: readonly DetailLevel[] = [
	{ level: "chat", label: "Chat", description: "Just the conversation" },
	{ level: "intent", label: "Intent", description: "Plus one folded line for each run of steps" },
	{ level: "tools", label: "Tools", description: "Plus every command it ran; tap one for its output" },
	{ level: "activity", label: "Activity", description: "Plus every command's output, open as it arrives" },
	{ level: "full", label: "Full", description: "Everything, including the agent's reasoning" },
];

export function detailLevel(level: ContentLevel): DetailLevel {
	const found = DETAIL_LEVELS.find((candidate) => candidate.level === level);
	if (!found) throw new Error(`unknown detail level ${level}`);
	return found;
}

/** What a session's transcript shows: the config it projects at, and whether
 * the screen then keeps just the conversation (Chat). They come together so
 * no caller can take one without the other. */
export interface TranscriptDisplay {
	config: TranscriptDisplayConfigV1 | null;
	justTheConversation: boolean;
}

export function displayForLevel(
	chosen: ContentLevel | null,
	hubConfig: TranscriptDisplayConfigV1 | null,
): TranscriptDisplay {
	// Chat means just the conversation on the phone whether this session or
	// the hub's default chose it (ruling 8).
	const hubChat = hubConfig?.content.kind === "preset" && hubConfig.content.level === "chat";
	const level = chosen ?? (hubChat ? "chat" : null);
	// Nothing chosen here: the session shows exactly what the hub says.
	if (!level) return { config: hubConfig, justTheConversation: false };
	const base = hubConfig ?? shippedConfig("mobile");
	// Chat projects at Intent on purpose: there a subagent's call survives as
	// its own row (a no-intent vector hides it), and the screen then drops the
	// settled and running steps (projectNativeTranscript's justTheConversation).
	// The shared Chat preset keeps a line per step.
	const chat = level === "chat";
	return {
		config: makeTranscriptDisplayConfig({ kind: "preset", level: chat ? "intent" : level }, base.advanced),
		justTheConversation: chat,
	};
}

export function currentLevel(
	chosen: ContentLevel | null,
	hubConfig: TranscriptDisplayConfigV1 | null,
): ContentLevel | "custom" | null {
	if (chosen) return chosen;
	// No hub config yet (or ever): the transcript projects at the config-less
	// show-everything config, which is Full content — so the menu marks that
	// level rather than nothing (projectedRows.ts PROJECT_EVERYTHING_CONFIG).
	if (!hubConfig) return "full";
	return hubConfig.content.kind === "preset" ? hubConfig.content.level : "custom";
}

export function detailMenuLabel(current: ContentLevel | "custom" | null): string {
	if (current === null) return "Detail level";
	return `Detail level · ${current === "custom" ? "Custom" : detailLevel(current).label}`;
}

/** The toast that confirms a change: the change often happens above the
 * visible part of the transcript (spec 8.7). */
export function levelToast(level: ContentLevel): string {
	const { label, description } = detailLevel(level);
	return `${label}: ${description.charAt(0).toLowerCase()}${description.slice(1)}`;
}

const storageKey = (hubId: string) => `evener.native.detail-level.${hubId}`;
const LIMIT = 500;
const LEVELS = new Set<string>(DETAIL_LEVELS.map((level) => level.level));

function read(storage: SyncStringStorage, hubId: string): [string, ContentLevel][] {
	try {
		const raw = storage.getItemSync(storageKey(hubId));
		const value: unknown = raw ? JSON.parse(raw) : [];
		if (!Array.isArray(value)) return [];
		return value.filter(
			(entry): entry is [string, ContentLevel] =>
				Array.isArray(entry) &&
				entry.length === 2 &&
				typeof entry[0] === "string" &&
				typeof entry[1] === "string" &&
				LEVELS.has(entry[1]),
		);
	} catch {
		// Nothing readable: nothing chosen yet.
		return [];
	}
}

/** The level chosen for each session on one hub, newest first. */
export class DetailLevels {
	private entries: [string, ContentLevel][];
	private revision = 0;
	private listeners = new Set<() => void>();

	constructor(
		private readonly storage: SyncStringStorage,
		private readonly hubId: string,
	) {
		this.entries = read(storage, hubId);
	}

	get(ref: string): ContentLevel | null {
		return this.entries.find(([entry]) => entry === ref)?.[1] ?? null;
	}

	set(ref: string, level: ContentLevel): void {
		const next: [string, ContentLevel][] = [[ref, level], ...this.entries.filter(([entry]) => entry !== ref)];
		this.entries = next.slice(0, LIMIT);
		try {
			this.storage.setItemSync(storageKey(this.hubId), JSON.stringify(this.entries));
		} catch {
			// The in-memory copy still serves this launch.
		}
		this.revision += 1;
		for (const listener of [...this.listeners]) listener();
	}

	subscribe = (listener: () => void): (() => void) => {
		this.listeners.add(listener);
		return () => this.listeners.delete(listener);
	};

	getRevision = (): number => this.revision;
}

/** A storage failure throws, like removeHub's other cleanups, so the person
 * hears that some local data could not be deleted. */
export function forgetDetailLevels(storage: SyncStringStorage, hubId: string): void {
	storage.removeItemSync(storageKey(hubId));
}
