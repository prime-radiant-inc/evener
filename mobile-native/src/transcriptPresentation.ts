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
} from "./projectedRows";

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

// An activity that is running or failed is attention-worthy. Notice criticality
// is timeline.ts's isCriticalNotice, not a rule of this layer.
function activityIsCritical(
	item: Extract<MobileTimelineItem, { kind: "activity" }>,
): boolean {
	return item.state === "failed" || item.state === "running";
}

// How an activity row renders. This is a RENDERING hint only: which rows
// exist at all is the shared projector's decision at the user's display
// config, made once inside the store's seam (D24-6 retired this layer's own
// config-driven row filtering — the projector subsumed it). What remains
// here is the mode each surviving row renders in:
//   - a summary-only row (the projector's intent entry; the operator's
//     summary-only ruling) renders its summary line, nothing to expand;
//   - a running or failed activity renders as attention: its summary line
//     above an expandable body (tools carry the summary; reasoning rows do
//     not — their body is the thought);
//   - everything else renders in full.
function presentationFor(
	item: Extract<MobileTimelineItem, { kind: "activity" }>,
): ActivityPresentation {
	if (item.summaryOnly === true) {
		return { mode: "intent", summary: actionSummary(item) };
	}
	if (activityIsCritical(item)) {
		return {
			mode: "critical",
			...(item.family === "tool"
				? {
						summary: actionSummary(item),
					}
				: {}),
		};
	}
	return FULL_PRESENTATION;
}

// Without transcript preferences nothing is summarised: every activity shows
// in full until the hub's config arrives, or forever on a hub that does not
// support it.
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
		...(member.summaryOnly ? { summaryOnly: member.summaryOnly } : {}),
		...(member.transcriptKey ? { transcriptKey: member.transcriptKey } : {}),
		...(member.position ? { position: member.position } : {}),
	};
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
//
// Every row the seam projected reaches the renderer: the shared projector
// decided at the user's config which rows exist (D24-6), so this pass only
// reshapes what survived — unrolling clustered members and seating
// attachments beside the member that produced them — and computes each
// activity's rendering mode.
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
				activityPresentation.set(
					projected.id,
					config ? presentationFor(projected) : FULL_PRESENTATION,
				);
				projectedItems.push(projected);
				// Attachments keep their source position even when that activity is hidden.
				projectedItems.push(
					...(attachmentsByKey.get(member.transcriptKey ?? member.id) ?? []),
				);
			}
		} else if (item.kind === "activity") {
			activityPresentation.set(
				item.id,
				config ? presentationFor(item) : FULL_PRESENTATION,
			);
			projectedItems.push(item);
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
