import {
	presetContent,
	type TranscriptDisplayConfigV1,
} from "../../cmd/evener-hub/frontend/src/transcriptDisplay/config";
import type {
	ActivityMember,
	MobileConversation,
	MobileTimelineItem,
	MobileUsage,
} from "../../mobile/src/conversation/model";

export type ActivityPresentation = {
	mode: "full" | "intent" | "critical";
	summary?: string;
};

export interface NativeTranscriptPresentation {
	items: MobileTimelineItem[];
	activityPresentation: ReadonlyMap<string, ActivityPresentation>;
	expandByDefault: boolean;
	usage: MobileUsage | null;
	showDuration: boolean;
}

const ACTION_SUMMARY_UNAVAILABLE = "Action summary unavailable";
const PROMPT_EVENTS = new Set(["system_prompt", "prompt_loaded"]);
const KNOWN_EVENTS = new Set([
	"system_prompt",
	"plugin_loaded",
	"skill_activated",
	"hook_completed",
	"prompt_loaded",
	"context_compaction",
	"compaction",
	"turn_limit",
	"loop_detection",
	"goal_ended",
	"fork_summary",
	"round_timings",
	"tool_repair",
	"model_switch",
	"error",
	"environment",
]);
const CRITICAL_EVENTS = new Set(["error", "tool_repair"]);
const MAX_ACTION_DETAIL_LENGTH = 256;

function writeFileActionSummary(
	item: Extract<MobileTimelineItem, { kind: "activity" }>,
): string | undefined {
	if (item.family !== "tool" || item.label !== "write_file") return undefined;
	if (!item.detail.arguments) return undefined;
	try {
		const args: unknown = JSON.parse(item.detail.arguments);
		if (typeof args !== "object" || args === null) return undefined;
		const record = args as Record<string, unknown>;
		const path =
			typeof record.file_path === "string" ? record.file_path.trim() : "";
		if (!path) return undefined;
		const boundedPath =
			path.length > MAX_ACTION_DETAIL_LENGTH
				? `${path.slice(0, MAX_ACTION_DETAIL_LENGTH - 3)}...`
				: path;
		return `Write ${boundedPath}`;
	} catch {
		return undefined;
	}
}

function actionSummary(
	item: Extract<MobileTimelineItem, { kind: "activity" }>,
): string {
	return (
		item.detail.description?.trim() ||
		writeFileActionSummary(item) ||
		ACTION_SUMMARY_UNAVAILABLE
	);
}

function isCritical(item: MobileTimelineItem): boolean {
	if (item.kind === "activity")
		return item.state === "failed" || item.state === "running";
	return (
		item.kind === "failure" ||
		item.kind === "question" ||
		(item.kind === "notice" && item.tone === "warning")
	);
}

function activityMode(
	item: Extract<MobileTimelineItem, { kind: "activity" }>,
	config: TranscriptDisplayConfigV1,
): ActivityPresentation | null {
	if (isCritical(item))
		return {
			mode: "critical",
			...(item.family === "tool"
				? {
						summary: actionSummary(item),
					}
				: {}),
		};
	if (item.family === "unknown") return { mode: "full" };
	const content =
		config.content.kind === "preset"
			? presetContent(config.content.level)
			: config.content;
	if (item.family === "reasoning")
		return content.reasoning ? { mode: "full" } : null;
	if (!item.detail.description?.trim() && content.toolCalls)
		return { mode: "critical", summary: actionSummary(item) };
	if (content.toolCalls) return { mode: "full" };
	if (content.toolIntent)
		return {
			mode: "intent",
			summary: actionSummary(item),
		};
	if (item.family === "tool" && !item.detail.description?.trim())
		return { mode: "critical", summary: actionSummary(item) };
	return null;
}

// Without transcript preferences nothing is hidden or summarised: every
// activity shows in full until the hub's config arrives, or forever on a hub
// that does not support it.
// One object stands under every activity id in that map, so it is readonly:
// nothing may edit one row's presentation and move the rest with it.
const FULL_PRESENTATION = { mode: "full" } as const;

function memberItem(
	member: ActivityMember,
): Extract<MobileTimelineItem, { kind: "activity" }> {
	return {
		kind: "activity",
		id: member.id,
		label: member.label,
		family: member.family,
		state: member.state,
		detail: member.detail,
		...(member.transcriptKey ? { transcriptKey: member.transcriptKey } : {}),
		...(member.position ? { position: member.position } : {}),
	};
}

function eventVisible(
	item: Extract<MobileTimelineItem, { kind: "notice" }>,
	config: TranscriptDisplayConfigV1,
): boolean {
	if (item.tone === "warning" || item.family === "warning") return true;
	const event = item.eventKind;
	if (!event || !KNOWN_EVENTS.has(event)) return true;
	if (CRITICAL_EVENTS.has(event)) return true;
	if (event === "hook_completed") {
		if (config.advanced.hookExits === "all") return true;
		if (item.exitCode !== undefined && item.exitCode !== 0) return true;
		return config.advanced.hookExits === "successful" && item.exitCode === 0;
	}
	if (PROMPT_EVENTS.has(event)) return config.advanced.promptEvents;
	if (event === "round_timings") return config.advanced.roundTimings;
	return config.advanced.systemEvents;
}

function usageFor(
	usage: MobileUsage | null,
	config: TranscriptDisplayConfigV1 | null,
): MobileUsage | null {
	if (!usage || !config) return usage;
	const tokenCounts = config.advanced.tokenCounts;
	return {
		...usage,
		...(tokenCounts
			? {}
			: {
					inputTokens: undefined,
					outputTokens: undefined,
					cacheReadTokens: undefined,
					totalTokens: undefined,
				}),
		...(config.advanced.estimatedCost ? {} : { cost: undefined }),
	};
}

export function projectNativeTranscript(
	conversation: MobileConversation | null,
	config: TranscriptDisplayConfigV1 | null | undefined,
): NativeTranscriptPresentation {
	const source = conversation?.items ?? [];
	const { items, activityPresentation } = projectTimeline(source, config);
	if (!config)
		return {
			items,
			activityPresentation,
			expandByDefault: false,
			usage: null,
			showDuration: true,
		};
	return {
		items,
		activityPresentation,
		expandByDefault: (config.content.kind === "preset"
			? presetContent(config.content.level)
			: config.content
		).expandByDefault,
		usage: usageFor(conversation?.usage ?? null, config),
		showDuration: config.advanced.roundTimings,
	};
}

// The one place that decides where a row sits relative to its attachments:
// every activity is followed by the attachments that name it as their source,
// and the trailing rows those came from are dropped. Both the configured path
// and the no-config fallback emit through this, so the two cannot disagree
// about adjacency; without a config every activity is simply shown in full.
function projectTimeline(
	source: MobileTimelineItem[],
	config: TranscriptDisplayConfigV1 | null | undefined,
): {
	items: MobileTimelineItem[];
	activityPresentation: Map<string, ActivityPresentation>;
} {
	const activityPresentation = new Map<string, ActivityPresentation>();
	const membersByKey = new Set<string>();
	for (const item of source)
		if (item.kind === "activity")
			for (const member of item.members ?? [])
				membersByKey.add(member.transcriptKey ?? member.id);
	const attachmentsByKey = new Map<string, MobileTimelineItem[]>();
	for (const item of source) {
		if (
			item.kind !== "attachments" ||
			!item.sourceTranscriptKey ||
			!membersByKey.has(item.sourceTranscriptKey)
		)
			continue;
		const attachments = attachmentsByKey.get(item.sourceTranscriptKey) ?? [];
		attachments.push(item);
		attachmentsByKey.set(item.sourceTranscriptKey, attachments);
	}
	const projectedItems: MobileTimelineItem[] = [];
	for (const item of source) {
		if (item.kind === "activity" && item.members?.length) {
			for (const member of item.members) {
				const projected = memberItem(member);
				const presentation = config
					? activityMode(projected, config)
					: FULL_PRESENTATION;
				if (presentation) {
					activityPresentation.set(projected.id, presentation);
					projectedItems.push(projected);
				}
				// Attachments keep their source position even when that activity is hidden.
				projectedItems.push(
					...(attachmentsByKey.get(member.transcriptKey ?? member.id) ?? []),
				);
			}
		} else if (item.kind === "activity") {
			const presentation = config
				? activityMode(item, config)
				: FULL_PRESENTATION;
			if (presentation) {
				activityPresentation.set(item.id, presentation);
				projectedItems.push(item);
			}
		} else if (
			item.kind === "notice" &&
			config &&
			!eventVisible(item, config)
		) {
		} else if (
			item.kind === "attachments" &&
			item.sourceTranscriptKey &&
			attachmentsByKey.has(item.sourceTranscriptKey)
		) {
		} else {
			projectedItems.push(item);
		}
	}
	return { items: projectedItems, activityPresentation };
}
