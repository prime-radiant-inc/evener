import {
	presetContent,
	sessionTokens,
	tokenUnitLabel,
	type EvenerUsage,
	type SessionTokens,
	type TranscriptDisplayConfigV1,
} from "@evener/appwire-client";
import type {
	ActivityMember,
	MobileConversation,
	MobileTimelineItem,
} from "../../mobile/src/conversation/project";
import { systemEventVisible } from "../../mobile/src/conversation/project";

export type ActivityPresentation = {
	mode: "full" | "intent" | "critical";
	summary?: string;
};

// The session accounting the transcript footer shows: the conversation's
// token total (the package's turn-summed sessionTokens derivation, shared
// with the web details panel), the thread's own cumulative cache/total
// breakdown, and cost - each undefined/null when the display config hides it
// or the daemon reported none.
//
// cacheReadTokens/totalTokens are read independently of inputTokens/
// outputTokens/scope: the wire's EvenerUsage permits a sparse cumulative
// object (cache or total alone, with no input/output pair at all), and
// sessionTokens has no per-turn equivalent for them, so they must survive
// even when sessionTokens falls back to summing turns or returns null
// outright. They are always whole-session figures (EvenerThread.Usage is not
// windowed the way turns are), so they carry no scope of their own and must
// never inherit whatever scope the derived input/output pair got.
export interface SessionAccounting {
	usage:
		| (Partial<SessionTokens> & Pick<EvenerUsage, "cacheReadTokens" | "totalTokens">)
		| null;
	cost: string | null;
}

export interface UsageRow {
	label: "Input" | "Output" | "Cached" | "Total";
	value: number;
	unit: string;
}

// usageRows picks the footer's visible rows and labels each with what it
// actually counts. Input/Output take the derived pair's own scope (a
// truncated turn window says so); Cached/Total are always the thread's whole
// -session cumulative figures, so they always read plainly, independent of
// whatever scope the derived pair got.
export function usageRows(usage: SessionAccounting["usage"]): UsageRow[] {
	if (!usage) return [];
	const derivedUnit = tokenUnitLabel(usage.scope);
	const cumulativeUnit = tokenUnitLabel(undefined);
	const candidates: [UsageRow["label"], number | undefined, string][] = [
		["Input", usage.inputTokens, derivedUnit],
		["Output", usage.outputTokens, derivedUnit],
		["Cached", usage.cacheReadTokens, cumulativeUnit],
		["Total", usage.totalTokens, cumulativeUnit],
	];
	return candidates
		.filter((row): row is [UsageRow["label"], number, string] => row[1] !== undefined)
		.map(([label, value, unit]) => ({ label, value, unit }));
}

export interface NativeTranscriptPresentation {
	items: MobileTimelineItem[];
	activityPresentation: ReadonlyMap<string, ActivityPresentation>;
	expandByDefault: boolean;
	usage: SessionAccounting | null;
	showDuration: boolean;
}

const ACTION_SUMMARY_UNAVAILABLE = "Action summary unavailable";
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

// The projector owns the event-kind vocabulary and its gate table, so native
// asks it (project.ts's systemEventVisible) rather than keeping a second copy.
// A steering notice carries no eventKind: the projector renders an unknown event,
// so it stays visible exactly as before.
function eventVisible(
	item: Extract<MobileTimelineItem, { kind: "notice" }>,
	config: TranscriptDisplayConfigV1,
): boolean {
	return systemEventVisible(item.eventKind, item.exitCode, config);
}

// A cumulative field's Go zero value ("0") signals absence, not a real
// measurement of zero — the same rule sessionTokens applies to inputTokens/
// outputTokens (threadUsage.ts). cacheReadTokens/totalTokens get no such
// derivation of their own (they are read straight off the wire), so that
// rule is applied here, once, at the point they are read.
function noZero(value: number | undefined): number | undefined {
	return value === 0 ? undefined : value;
}

function accountingFor(
	conversation: MobileConversation | null,
	config: TranscriptDisplayConfigV1,
): SessionAccounting | null {
	if (!conversation) return null;
	const tokens = config.advanced.tokenCounts ? sessionTokens(conversation) : null;
	const cacheReadTokens = config.advanced.tokenCounts ? noZero(conversation.usage?.cacheReadTokens) : undefined;
	const totalTokens = config.advanced.tokenCounts ? noZero(conversation.usage?.totalTokens) : undefined;
	return {
		usage:
			tokens || cacheReadTokens !== undefined || totalTokens !== undefined
				? {
						...(tokens ?? {}),
						...(cacheReadTokens !== undefined ? { cacheReadTokens } : {}),
						...(totalTokens !== undefined ? { totalTokens } : {}),
					}
				: null,
		cost: config.advanced.estimatedCost ? (conversation.cost ?? null) : null,
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
		usage: accountingFor(conversation, config),
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
